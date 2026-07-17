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
	path      string
	args      []string
	found     bool
	checkedAt time.Time
}

// notFoundRetryTTL bounds how long a negative discovery result is trusted,
// so a server installed mid-session is eventually picked up.
const notFoundRetryTTL = time.Minute

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
	starting      map[string]chan struct{} // per-server in-flight start; closed when the attempt ends
	closed        bool                     // set by Close; refuses further starts
	scanning      atomic.Bool

	// startCtx parents every server start; Close cancels it so in-flight
	// handshakes abort promptly, and startWG lets Close wait them out.
	startCtx    context.Context
	startCancel context.CancelFunc
	startWG     sync.WaitGroup
}

// NewManager creates a Manager with the given config and workspace root.
// Pass a nil config to use defaults (built-in language servers only).
func NewManager(cfg *LSPConfig, workspaceRoot string) *Manager {
	if cfg == nil {
		cfg = NewLSPConfig()
		loadDefaults(cfg)
	}
	startCtx, startCancel := context.WithCancel(context.Background())
	return &Manager{
		config:        cfg,
		workspaceRoot: workspaceRoot,
		clients:       make(map[string]*Client),
		foundCache:    make(map[string]foundEntry),
		statusCache:   make(map[string]ServerStatus),
		starting:      make(map[string]chan struct{}),
		startCtx:      startCtx,
		startCancel:   startCancel,
	}
}

// Close shuts down all running language servers gracefully. Servers are
// closed concurrently and outside the manager lock (a stuck server would
// otherwise serialize shutdown and block all other manager calls), and no
// new server may start afterwards.
func (m *Manager) Close() error {
	m.mu.Lock()
	m.closed = true
	clients := m.clients
	m.clients = make(map[string]*Client)
	m.mu.Unlock()

	// Abort in-flight handshakes and wait for their goroutines to unwind —
	// a start racing past Close would otherwise spawn an orphan process.
	m.startCancel()
	m.startWG.Wait()

	var wg sync.WaitGroup
	var errMu sync.Mutex
	var errs []string
	for name, client := range clients {
		wg.Add(1)
		go func(name string, client *Client) {
			defer wg.Done()
			if err := client.Close(); err != nil {
				errMu.Lock()
				errs = append(errs, fmt.Sprintf("%s: %v", name, err))
				errMu.Unlock()
			}
		}(name, client)
	}
	wg.Wait()
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

	// Fast path: already ready (triggers a background start otherwise).
	client, _, ready := m.ReadyClientForFile(filePath)
	if ready {
		return client, name, nil
	}

	// Wait for the in-flight start attempt to finish (its channel is
	// closed however the attempt ends), bounded by a short deadline so
	// slow cold starts return a retry hint instead of blocking the tool.
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for {
		m.mu.RLock()
		client = m.clients[name]
		attempt := m.starting[name]
		m.mu.RUnlock()

		if client != nil && client.alive() {
			return client, name, nil
		}
		if attempt == nil {
			if client != nil && client.isStarting() {
				// Started outside backgroundStart (e.g. clientForServer).
				return nil, name, fmt.Errorf("language server %q is still starting up; retry in a few seconds, or use grep + read_file in the meantime", name)
			}
			// No attempt in flight and no live client: the start already
			// finished and failed (or the server is not installed).
			reason := ""
			m.mu.RLock()
			if st, ok := m.statusCache[name]; ok && st.Reason != "" {
				reason = ": " + st.Reason
			}
			m.mu.RUnlock()
			return nil, name, fmt.Errorf("language server %q failed to start%s; check /lsp, or use grep + read_file in the meantime", name, reason)
		}
		select {
		case <-attempt:
			// Attempt finished; loop to observe the outcome.
		case <-deadline.C:
			return nil, name, fmt.Errorf("language server %q is still starting up; retry in a few seconds, or use grep + read_file in the meantime", name)
		}
	}
}

// EnsureAllAsync triggers a background start for configured language servers
// whose binary was found and that have matching workspace files. Before a
// Warmup scan has produced data, every configured server is eligible. It
// returns without waiting for the starts to finish.
func (m *Manager) EnsureAllAsync() {
	m.mu.RLock()
	scanned := m.extCache != nil
	fc := m.foundCache
	m.mu.RUnlock()
	for name, srv := range m.config.Servers {
		if entry, ok := fc[name]; ok && !entry.found && time.Since(entry.checkedAt) < notFoundRetryTTL {
			continue
		}
		// Only filter by workspace files once a scan has actually run;
		// with no scan data the filter would reject everything.
		if scanned && !m.hasWorkspaceFiles(srv) {
			continue
		}
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
	// Claim synchronously so callers (and their waiters) observe the
	// in-flight attempt immediately; only the slow work runs async.
	if attempt, ok := m.tryClaimStart(name); ok {
		go m.runStart(name, srv, attempt)
	}
}

// tryClaimStart registers an exclusive start attempt for name. It returns
// false when the manager is closed, the server is already running or
// starting, or another attempt is in flight. On success the caller must
// invoke runStart with the returned channel.
func (m *Manager) tryClaimStart(name string) (chan struct{}, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, false
	}
	if client, ok := m.clients[name]; ok && (client.alive() || client.isStarting()) {
		return nil, false
	}
	if _, inFlight := m.starting[name]; inFlight {
		return nil, false
	}
	attempt := make(chan struct{})
	m.starting[name] = attempt
	m.startWG.Add(1)
	return attempt, true
}

// findServerCached memoizes FindServerForConfig results in foundCache.
// Positive results are cached for the session; negative results are
// re-checked after notFoundRetryTTL.
func (m *Manager) findServerCached(name string, srv *ServerConfig) (string, []string, bool) {
	m.mu.RLock()
	entry, ok := m.foundCache[name]
	m.mu.RUnlock()
	if ok && (entry.found || time.Since(entry.checkedAt) < notFoundRetryTTL) {
		return entry.path, entry.args, entry.found
	}
	path, args, found := FindServerForConfig(srv)
	m.mu.Lock()
	m.foundCache[name] = foundEntry{path: path, args: args, found: found, checkedAt: time.Now()}
	m.mu.Unlock()
	return path, args, found
}

// newClient creates a Client pre-populated with server config values.
func (m *Manager) newClient(name string, srv *ServerConfig, command string, args []string) *Client {
	client := &Client{
		language:              name,
		command:               command,
		args:                  args,
		rootURI:               PathToURI(m.workspaceRoot),
		openDocs:              make(map[string]openDocState),
		languageByExt:         srv.ExtensionToLanguage,
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
			if m.closed || m.clients[name] != client {
				m.mu.Unlock()
				return // manager closed or replaced by a newer client
			}
			m.mu.Unlock()
			command, args, found := m.findServerCached(name, srv)
			if !found {
				return
			}
			newClient := m.newClient(name, srv, command, args)
			m.mu.Lock()
			if m.closed || m.clients[name] != client {
				m.mu.Unlock()
				newClient.Close()
				return
			}
			m.clients[name] = newClient
			// Track the restart so Close cancels and waits it out.
			m.startWG.Add(1)
			m.mu.Unlock()
			ctx, cancel := context.WithTimeout(m.startCtx, srv.StartupTimeout())
			err := newClient.Start(ctx)
			cancel()
			m.startWG.Done()
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
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, fmt.Errorf("lsp manager is closed")
	}
	client, ok := m.clients[name]
	// Track this synchronous start so Close waits it out and can cancel it,
	// exactly like the backgroundStart path.
	m.startWG.Add(1)
	m.mu.Unlock()
	defer m.startWG.Done()

	if ok {
		if !client.ready.Load() {
			// Ensure enough time; caller ctx may be shorter than startup
			// timeout. Parent on startCtx so Close aborts the handshake.
			startCtx := ctx
			if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < srv.StartupTimeout() {
				var cancel context.CancelFunc
				startCtx, cancel = context.WithTimeout(m.startCtx, srv.StartupTimeout())
				defer cancel()
			}
			if err := client.Start(startCtx); err != nil {
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

	command, args, found := m.findServerCached(name, srv)
	if !found {
		help := srv.InstallHelp
		if help == "" {
			help = "install " + srv.Command
		}
		m.setStatus(name, "not_installed", srv.Command, "")
		return nil, fmt.Errorf("%s not found. Install it: %s", srv.Command, help)
	}

	// Build the replacement before acquiring the write lock so that
	// discovery I/O does not extend the critical section.
	newClient := m.newClient(name, srv, command, args)

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, fmt.Errorf("lsp manager is closed")
	}
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
			_, _, found := m.findServerCached(name, srv)
			if found && m.hasWorkspaceFiles(srv) {
				m.backgroundStart(name, srv)
			}
		}
	}()
}

// backgroundStart claims and synchronously runs one start attempt. Callers
// that must not block use ensureAsync instead.
func (m *Manager) backgroundStart(name string, srv *ServerConfig) {
	attempt, ok := m.tryClaimStart(name)
	if !ok {
		return
	}
	m.runStart(name, srv, attempt)
}

// runStart performs discovery and the server handshake for a claimed start
// attempt. The attempt channel is closed when the attempt ends, however it
// ends; waiters use it to observe the outcome.
func (m *Manager) runStart(name string, srv *ServerConfig, attempt chan struct{}) {
	defer func() {
		m.mu.Lock()
		delete(m.starting, name)
		m.mu.Unlock()
		close(attempt)
		m.startWG.Done()
	}()

	command, args, found := m.findServerCached(name, srv)
	if !found {
		m.setStatus(name, "not_installed", "", srv.InstallHelp)
		return
	}

	client := m.newClient(name, srv, command, args)

	m.mu.Lock()
	if m.closed {
		// Close ran while we were discovering; do not spawn.
		m.mu.Unlock()
		return
	}
	if dead, ok := m.clients[name]; ok {
		// Reclaim the crashed predecessor's resources off the hot path.
		go dead.Close()
	}
	m.clients[name] = client
	m.mu.Unlock()

	ctx, cancel := context.WithTimeout(m.startCtx, srv.StartupTimeout())
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
	// The outline only enriches read_file output; never let a slow or
	// still-indexing server hold up a plain file read.
	outlineCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	uri := PathToURI(filePath)
	symbols, err := client.DocumentSymbols(outlineCtx, uri)
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
			switch n {
			case ".git", "node_modules", "vendor", "build", "dist", "target":
				continue
			}
			if strings.HasPrefix(n, ".") {
				continue
			}
			scanDir(filepath.Join(dir, n), depth+1, result, fileCount, maxDepth, maxFiles)
		} else {
			result[filepath.Ext(e.Name())] = true
			*fileCount++
		}
	}
}
