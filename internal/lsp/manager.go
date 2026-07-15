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
// messages. Use this from LSP tool handlers that need fast checks without
// blocking on startup.
func (m *Manager) ReadyClientForFile(filePath string) (*Client, string, bool) {
	ext := filepath.Ext(filePath)
	srv, name, ok := m.config.ForExtension(ext)
	if !ok {
		return nil, "", false
	}
	m.mu.RLock()
	client, ok := m.clients[name]
	m.mu.RUnlock()
	if !ok || client == nil || !client.alive() {
		return nil, name, false
	}
	_ = srv
	return client, name, true
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

	m.mu.Lock()
	if client, ok := m.clients[name]; ok {
		if client.alive() {
			m.mu.Unlock()
			return client, nil
		}
		dead := client
		delete(m.clients, name)
		m.mu.Unlock()
		dead.Close()
		m.mu.Lock()
	}

	command, args, found := FindServerForConfig(srv)
	if !found {
		m.mu.Unlock()
		help := srv.InstallHelp
		if help == "" {
			help = "install " + srv.Command
		}
		m.setStatus(name, "not_installed", srv.Command, "")
		return nil, fmt.Errorf("%s not found. Install it: %s", srv.Command, help)
	}

	client = &Client{
		language: name,
		command:  command,
		args:     args,
		rootURI:  PathToURI(m.workspaceRoot),
		openDocs: make(map[string]bool),
	}

	m.clients[name] = client
	created := client
	m.mu.Unlock()

	if err := client.Start(ctx); err != nil {
		m.mu.Lock()
		if m.clients[name] == created {
			delete(m.clients, name)
		}
		m.mu.Unlock()
		m.setStatus(name, "failed", command, err.Error())
		return nil, fmt.Errorf("start %s: %w", name, err)
	}
	m.setStatus(name, "loaded", command, "")
	return client, nil
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
		scanDir(m.workspaceRoot, 0, extCache)
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
	if ok && client.alive() {
		m.mu.RUnlock()
		return
	}
	m.mu.RUnlock()

	command, args, found := FindServerForConfig(srv)
	if !found {
		return
	}

	client = &Client{
		language: name,
		command:  command,
		args:     args,
		rootURI:  PathToURI(m.workspaceRoot),
		openDocs: make(map[string]bool),
	}

	m.mu.Lock()
	if existing, ok := m.clients[name]; ok && existing.alive() {
		m.mu.Unlock()
		return
	}
	m.clients[name] = client
	m.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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

func scanDir(dir string, depth int, result map[string]bool) {
	if depth > 2 {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			n := e.Name()
			if n == ".git" || n == "node_modules" || n == "vendor" || strings.HasPrefix(n, ".") {
				continue
			}
			scanDir(filepath.Join(dir, n), depth+1, result)
		} else {
			result[filepath.Ext(e.Name())] = true
		}
	}
}
