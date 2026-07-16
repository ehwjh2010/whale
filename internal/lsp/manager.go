package lsp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type ServerStatus struct {
	Name   string
	State  string
	Path   string
	Reason string
}

type foundEntry struct {
	path  string
	args  []string
	found bool
}

// Manager coordinates the lifecycle of language servers. It is created via
// NewManager(cfg, workspaceRoot) and registered with the toolset via
// tools.Toolset.SetLSPManager(mgr). Typically the application layer
// creates one Manager per workspace on startup, calls Warmup() to
// background-discover files and start relevant servers, then the tool
// handlers query ReadyClientForFile / ClientForLanguage on demand.
//
// Thread safety: Manager uses a sync.RWMutex. All public methods are safe
// for concurrent use.
type Manager struct {
	mu            sync.RWMutex
	config        *LSPConfig
	workspaceRoot string
	clients       map[string]*Client
	extCache      map[string]bool
	foundCache    map[string]foundEntry
	statusCache   map[string]ServerStatus
	scanning      atomic.Bool
}

// NewManager creates a Manager with the given config and workspace root.
// Pass a nil config to use defaults (built-in language servers only).
func NewManager(cfg *LSPConfig, workspaceRoot string) *Manager {
	if cfg == nil {
		cfg = NewLSPConfig()
		loadDefaults(cfg)
	}
	return &Manager{
		config:        cfg,
		workspaceRoot: workspaceRoot,
		clients:       make(map[string]*Client),
		foundCache:    make(map[string]foundEntry),
		statusCache:   make(map[string]ServerStatus),
	}
}

// Close shuts down all running language servers gracefully.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var errs []string
	for name, client := range m.clients {
		if err := client.Close(); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", name, err))
		}
	}
	m.clients = make(map[string]*Client)
	if len(errs) > 0 {
		return fmt.Errorf("close errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

func (m *Manager) ClientForFile(ctx context.Context, filePath string) (*Client, error) {
	ext := filepath.Ext(filePath)
	srv, name, ok := m.config.ForExtension(ext)
	if !ok {
		return nil, fmt.Errorf("no LSP server for %q files", ext)
	}
	return m.clientForServer(ctx, name, srv)
}

// ClientForLanguage returns a ready client for the named language, starting
// it if necessary. This blocks until the server completes the LSP handshake
// or fails. Use this for workspace-symbol searches where multiple servers
// may be queried.
func (m *Manager) ClientForLanguage(ctx context.Context, langName string) (*Client, error) {
	srv, ok := m.config.Servers[langName]
	if !ok {
		return nil, fmt.Errorf("unknown language: %s", langName)
	}
	return m.clientForServer(ctx, langName, srv)
}

// ReadyClientForFile returns a ready LSP client for the given file path.
// If the server for this file's extension hasn't started or is unhealthy,
// returns nil and false. The returned string is the server name for error
// messages.
//
// As a side effect, if the server is configured but not running, this
// triggers a background start so the next call is likely to succeed.
func (m *Manager) ReadyClientForFile(filePath string) (*Client, string, bool) {
	ext := filepath.Ext(filePath)
	_, name, ok := m.config.ForExtension(ext)
	if !ok {
		return nil, "", false
	}
	m.mu.RLock()
	client, ok := m.clients[name]
	m.mu.RUnlock()
	if !ok || client == nil || !client.alive() {
		m.ensureAsync(name)
		return nil, name, false
	}
	return client, name, true
}

// ClientForFileQuick returns a ready LSP client for the given file path.
// On the first call for a language, it triggers a background start and
// waits up to 3 seconds for the server to become ready. On subsequent
// calls while the server is still warming up, it returns immediately
// with an error so the model retries later.
func (m *Manager) ClientForFileQuick(filePath string) (*Client, string, error) {
	ext := filepath.Ext(filePath)
	_, name, ok := m.config.ForExtension(ext)
	if !ok {
		return nil, "", fmt.Errorf("no LSP server for %q files", ext)
	}

	// Fast path: already ready
	client, _, ready := m.ReadyClientForFile(filePath)
	if ready {
		return client, name, nil
	}

	// Already starting from a previous attempt? Fail fast.
	m.mu.RLock()
	existing := m.clients[name]
	m.mu.RUnlock()
	if existing != nil && existing.isStarting() {
		return nil, name, fmt.Errorf("language server %q is still starting up; retry in a few seconds, or use grep + read_file in the meantime", name)
	}

	// Fresh start — wait briefly (ReadyClientForFile already triggered ensureAsync)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		client, _, ready = m.ReadyClientForFile(filePath)
		if ready {
			return client, name, nil
		}
	}
	return nil, name, fmt.Errorf("language server %q is still starting up; retry in a few seconds, or use grep + read_file in the meantime", name)
}

// EnsureAllAsync triggers a background start for every configured language
// server that is not already running. It returns immediately.
func (m *Manager) EnsureAllAsync() {
	for name := range m.config.Servers {
		m.ensureAsync(name)
	}
}

// ensureAsync triggers a background start for the named language server
// if it is not already running or starting. Safe to call concurrently.
func (m *Manager) ensureAsync(name string) {
	srv, ok := m.config.Servers[name]
	if !ok {
		return
	}
	m.mu.RLock()
	client, ok := m.clients[name]
	m.mu.RUnlock()
	if ok && (client.alive() || client.isStarting()) {
		return
	}
	m.backgroundStart(name, srv)
}

// newClient creates a Client pre-populated with server config values.
func (m *Manager) newClient(name string, srv *ServerConfig, command string, args []string) *Client {
	client := &Client{
		language:              name,
		command:               command,
		args:                  args,
		rootURI:               PathToURI(m.workspaceRoot),
		openDocs:              make(map[string]time.Time),
		env:                   srv.Env,
		initializationOptions: srv.InitializationOptions,
		settings:              srv.Settings,
		startupTimeout:        srv.StartupTimeout(),
		shutdownTimeout:       srv.ShutdownTimeout(),
	}
	return client
}

// maybeMonitorRestart starts a background goroutine that watches the client
// for crashes and restarts it when configured. Exponential backoff prevents
// tight restart loops; a hard cap of 100 restarts guards against infinite
// spinning when MaxRestarts is 0 (unlimited). On restart failure the monitor
// exits; the next tool invocation will trigger a fresh start via clientForServer.
func (m *Manager) maybeMonitorRestart(name string, srv *ServerConfig, client *Client) {
	if !srv.ShouldRestartOnCrash() {
		return
	}
	// If MaxRestarts is 0 (unlimited), cap at 100 to prevent infinite CPU spin.
	maxRestarts := srv.MaxRestarts
	if maxRestarts == 0 {
		maxRestarts = 100
	}
	go func() {
		totalRestarts := 0
		for {
			<-client.done
			if totalRestarts >= maxRestarts {
				m.setStatus(name, "failed", "", "max restarts reached")
				return
			}
			// Exponential backoff: 1s, 2s, 4s, 8s, 16s, capped at 30s
			if totalRestarts > 0 {
				delay := time.Duration(1<<min(totalRestarts-1, 5)) * time.Second
				time.Sleep(delay)
			}
			m.mu.Lock()
			if m.clients[name] != client {
				m.mu.Unlock()
				return // replaced by a newer client
			}
			m.mu.Unlock()
			command, args, found := FindServerForConfig(srv)
			if !found {
				return
			}
			newClient := m.newClient(name, srv, command, args)
			m.mu.Lock()
			if m.clients[name] != client {
				m.mu.Unlock()
				newClient.Close()
				return
			}
			m.clients[name] = newClient
			m.mu.Unlock()
			ctx, cancel := context.WithTimeout(context.Background(), srv.StartupTimeout())
			err := newClient.Start(ctx)
			cancel()
			if err != nil {
				totalRestarts++
				m.setStatus(name, "failed", command, err.Error())
				return // exit; next tool call will trigger a fresh start
			}
			totalRestarts++
			m.setStatus(name, "loaded", command, "")
			client = newClient
		}
	}()
}

func (m *Manager) clientForServer(ctx context.Context, name string, srv *ServerConfig) (*Client, error) {
	m.mu.RLock()
	client, ok := m.clients[name]
	m.mu.RUnlock()
	if ok {
		if !client.ready.Load() {
			if err := client.Start(ctx); err != nil {
				m.mu.Lock()
				if m.clients[name] == client {
					delete(m.clients, name)
				}
				m.mu.Unlock()
				m.setStatus(name, "failed", "", err.Error())
				return nil, fmt.Errorf("lazy-start %s: %w", name, err)
			}
		}
		return client, nil
	}

	command, args, found := FindServerForConfig(srv)
	if !found {
		help := srv.InstallHelp
		if help == "" {
			help = "install " + srv.Command
		}
		m.setStatus(name, "not_installed", srv.Command, "")
		return nil, fmt.Errorf("%s not found. Install it: %s", srv.Command, help)
	}

	// Build the replacement before acquiring the write lock so that
	// FindServerForConfig (which does I/O) does not extend the critical section.
	newClient := m.newClient(name, srv, command, args)

	m.mu.Lock()
	if existing, ok := m.clients[name]; ok {
		if existing.alive() {
			m.mu.Unlock()
			return existing, nil
		}
		dead := existing
		m.clients[name] = newClient
		m.mu.Unlock()
		dead.Close()
	} else {
		m.clients[name] = newClient
		m.mu.Unlock()
	}

	if err := newClient.Start(ctx); err != nil {
		m.mu.Lock()
		if m.clients[name] == newClient {
			delete(m.clients, name)
		}
		m.mu.Unlock()
		m.setStatus(name, "failed", command, err.Error())
		return nil, fmt.Errorf("start %s: %w", name, err)
	}
	m.setStatus(name, "loaded", command, "")
	m.maybeMonitorRestart(name, srv, newClient)
	return newClient, nil
}

// AvailableLanguages returns languages whose server binary was found on
// the system, regardless of whether a client is currently running.
func (m *Manager) AvailableLanguages() []string {
	var available []string
	for name, srv := range m.config.Servers {
		if _, _, found := FindServerForConfig(srv); found {
			available = append(available, name)
		}
	}
	return available
}

// ReadyLanguages returns the names of all language servers that are currently
// running and healthy. Used by lsp_workspace_symbol to iterate over active
// servers.
func (m *Manager) ReadyLanguages() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var ready []string
	for name, client := range m.clients {
		if client.alive() {
			ready = append(ready, name)
		}
	}
	return ready
}

func (m *Manager) IsReady(ext string) bool {
	_, _, ok := m.ReadyClientForFile("test" + ext)
	return ok
}

// Warmup scans the workspace for file extensions matching configured
// language servers and starts them in the background. Safe to call
// multiple times (subsequent calls are no-ops while scanning).
func (m *Manager) Warmup() {
	if m.workspaceRoot == "" || len(m.config.Servers) == 0 {
		return
	}
	if !m.scanning.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer m.scanning.Store(false)
		extCache := make(map[string]bool)
		var fileCount int
		scanDir(m.workspaceRoot, 0, extCache, &fileCount, 5, 200)
		m.mu.Lock()
		m.extCache = extCache
		m.mu.Unlock()
		for name, srv := range m.config.Servers {
			path, args, found := FindServerForConfig(srv)
			m.mu.Lock()
			m.foundCache[name] = foundEntry{path: path, args: args, found: found}
			m.mu.Unlock()
			if found && m.hasWorkspaceFiles(srv) {
				m.backgroundStart(name, srv)
			}
		}
	}()
}

func (m *Manager) backgroundStart(name string, srv *ServerConfig) {
	m.mu.RLock()
	client, ok := m.clients[name]
	if ok && (client.alive() || client.isStarting()) {
		m.mu.RUnlock()
		return
	}
	m.mu.RUnlock()

	command, args, found := FindServerForConfig(srv)
	if !found {
		return
	}

	client = m.newClient(name, srv, command, args)

	m.mu.Lock()
	if existing, ok := m.clients[name]; ok && (existing.alive() || existing.isStarting()) {
		m.mu.Unlock()
		return
	}
	m.clients[name] = client
	m.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), srv.StartupTimeout())
	defer cancel()

	if err := client.Start(ctx); err != nil {
		m.mu.Lock()
		if m.clients[name] == client {
			delete(m.clients, name)
		}
		m.mu.Unlock()
		m.setStatus(name, "failed", command, err.Error())
		return
	}
	m.setStatus(name, "loaded", command, "")
	m.maybeMonitorRestart(name, srv, client)
}

func (m *Manager) SymbolOutline(ctx context.Context, filePath string) string {
	client, _, ok := m.ReadyClientForFile(filePath)
	if !ok {
		return ""
	}
	uri := PathToURI(filePath)
	symbols, err := client.DocumentSymbols(ctx, uri)
	if err != nil || len(symbols) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Symbols (LSP)\n")
	formatSymbolsFlat(&b, symbols, 0)
	return b.String()
}

func formatSymbolsFlat(b *strings.Builder, symbols []DocumentSymbol, depth int) {
	for _, s := range symbols {
		b.WriteString(strings.Repeat("  ", depth))
		b.WriteString("- ")
		b.WriteString(s.Name)
		b.WriteString(" [")
		b.WriteString(SymbolKindName(s.Kind))
		b.WriteString("]\n")
		if len(s.Children) > 0 {
			formatSymbolsFlat(b, s.Children, depth+1)
		}
	}
}

func (m *Manager) AvailableSummary() string {
	if m.scanning.Load() {
		return "scanning for language servers..."
	}
	m.mu.RLock()
	fc := m.foundCache
	sc := m.statusCache
	m.mu.RUnlock()
	if fc == nil {
		return "scanning for language servers..."
	}
	var b strings.Builder
	for name, srv := range m.config.Servers {
		entry := fc[name]
		path := entry.path
		if path == "" {
			path = srv.Command
		}
		status := "not installed"
		if entry.found {
			if !m.hasWorkspaceFiles(srv) {
				status = "not used"
			} else {
				m.mu.RLock()
				client, ok := m.clients[name]
				m.mu.RUnlock()
				if ok && client.alive() {
					status = "loaded"
				} else if st, ok := sc[name]; ok && st.State == "failed" {
					status = "failed: " + st.Reason
				} else {
					status = "indexing"
				}
			}
		}
		exts := make([]string, 0, len(srv.ExtensionToLanguage))
		for ext := range srv.ExtensionToLanguage {
			exts = append(exts, ext)
		}
		b.WriteString(fmt.Sprintf("- `%s`  **%s**  %s\n  %s\n\n", name, status, strings.Join(exts, " "), path))
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m *Manager) setStatus(name, state, path, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.statusCache[name] = ServerStatus{Name: name, State: state, Path: path, Reason: reason}
}

func (m *Manager) hasWorkspaceFiles(srv *ServerConfig) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.extCache == nil {
		return false
	}
	for ext := range srv.ExtensionToLanguage {
		if m.extCache[ext] {
			return true
		}
	}
	return false
}

// scanDir walks the workspace tree to discover file extensions, stopping
// when either maxDepth (5) or maxFiles (200) is reached. Extensions found
// here let Warmup pre-start relevant servers; files beyond the limits are
// still covered by lazy start on first LSP tool invocation.
func scanDir(dir string, depth int, result map[string]bool, fileCount *int, maxDepth, maxFiles int) {
	if depth > maxDepth || *fileCount >= maxFiles {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if *fileCount >= maxFiles {
			return
		}
		if e.IsDir() {
			n := e.Name()
			if n == ".git" || n == "node_modules" || n == "vendor" || strings.HasPrefix(n, ".") {
				continue
			}
			scanDir(filepath.Join(dir, n), depth+1, result, fileCount, maxDepth, maxFiles)
		} else {
			result[filepath.Ext(e.Name())] = true
			*fileCount++
		}
	}
}
