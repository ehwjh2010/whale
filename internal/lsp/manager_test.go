package lsp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// zzzManager builds a Manager whose only server is a fake executable (see
// fakeExecutable in discovery_test.go) claiming the .zzz extension.
func zzzManager(t *testing.T, mode string, startupMS int) *Manager {
	t.Helper()
	fake := fakeExecutable(t, t.TempDir(), "fake-lsp", mode)
	cfg := NewLSPConfig()
	if err := cfg.RegisterServer("zzz", &ServerConfig{
		Command:             fake,
		ExtensionToLanguage: map[string]string{".zzz": "zzz"},
		StartupTimeoutMS:    startupMS,
	}); err != nil {
		t.Fatalf("register server: %v", err)
	}
	m := NewManager(cfg, t.TempDir())
	t.Cleanup(func() { _ = m.Close() })
	return m
}

// EnsureAllAsync must start configured servers even before Warmup has run
// (extCache == nil): its documented contract is to fall back to starting
// when no scan data exists yet.
func TestEnsureAllAsyncStartsServersBeforeWarmup(t *testing.T) {
	m := zzzManager(t, "hang", 5000)
	m.EnsureAllAsync()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.RLock()
		_, ok := m.clients["zzz"]
		m.mu.RUnlock()
		if ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("EnsureAllAsync started nothing although Warmup never ran")
}

// Build-output directories must not eat the scan quota before source
// directories are reached.
func TestScanDirSkipsBuildOutputDirs(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"build", "dist", "target"} {
		sub := filepath.Join(root, dir)
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		for i := 0; i < 100; i++ {
			if err := os.WriteFile(filepath.Join(sub, fmt.Sprintf("f%03d.txt", i)), nil, 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
		}
	}
	src := filepath.Join(root, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "main.zzz"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write main.zzz: %v", err)
	}
	exts := map[string]bool{}
	var n int
	scanDir(root, 0, exts, &n, 5, 200)
	if !exts[".zzz"] {
		t.Fatalf("scan missed .zzz: build dirs consumed the 200-file quota (exts=%v)", exts)
	}
}

// clientForServer (the synchronous lazy-start path used by ClientForFile /
// ClientForLanguage) must refuse to spawn after Close, like backgroundStart.
func TestClientForServerRefusesAfterClose(t *testing.T) {
	m := zzzManager(t, "hang", 2000)
	if err := m.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	start := time.Now()
	_, err := m.ClientForLanguage(context.Background(), "zzz")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected error after Close")
	}
	if elapsed > time.Second {
		t.Fatalf("ClientForLanguage spent %v after Close; it spawned and waited on a server instead of refusing", elapsed)
	}
	m.mu.RLock()
	n := len(m.clients)
	_, attempted := m.statusCache["zzz"]
	m.mu.RUnlock()
	if n != 0 || attempted {
		t.Fatalf("start attempted after Close: clients=%d statusWritten=%v", n, attempted)
	}
}

// Close must not return until the server process has actually been reaped:
// on Windows a killed-but-unreaped process still holds handles on its
// working directory, breaking cleanup of temp workspaces.
func TestCloseWaitsForProcessExit(t *testing.T) {
	m := zzzManager(t, "hang", 30000)
	m.ReadyClientForFile("main.zzz")
	deadline := time.Now().Add(5 * time.Second)
	var client *Client
	for time.Now().Before(deadline) {
		m.mu.RLock()
		c := m.clients["zzz"]
		m.mu.RUnlock()
		if c != nil && c.isStarting() {
			client = c
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if client == nil {
		t.Fatal("server never began starting")
	}
	if err := m.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	client.mu.Lock()
	procDone := client.procDone
	client.mu.Unlock()
	if procDone == nil {
		t.Fatal("no process was tracked")
	}
	select {
	case <-procDone:
	default:
		t.Fatal("Close returned before the server process was reaped")
	}
}

// Close must cancel and wait out in-flight background starts: otherwise the
// start goroutine can spawn its server process after Close returned, leaking
// it with nobody left to clean up.
func TestCloseCancelsInFlightStart(t *testing.T) {
	m := zzzManager(t, "hang", 30000)
	m.ReadyClientForFile("main.zzz") // async start begins
	time.Sleep(300 * time.Millisecond)
	start := time.Now()
	if err := m.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Close blocked %v on an in-flight start; must cancel it instead", elapsed)
	}
	m.mu.RLock()
	inFlight := len(m.starting)
	clients := len(m.clients)
	m.mu.RUnlock()
	if inFlight != 0 || clients != 0 {
		t.Fatalf("in-flight start survived Close: starting=%d clients=%d", inFlight, clients)
	}
}

// After Close, late Warmup/ensureAsync activity must not spawn new servers:
// nobody would ever close them.
func TestBackgroundStartRefusesAfterClose(t *testing.T) {
	m := zzzManager(t, "hang", 5000)
	if err := m.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	m.EnsureAllAsync()
	time.Sleep(300 * time.Millisecond)
	m.mu.RLock()
	n := len(m.clients)
	m.mu.RUnlock()
	if n != 0 {
		t.Fatalf("%d server(s) spawned after Close; they would leak as orphans", n)
	}
}

func TestEnsureAsyncDoesNotBlockCaller(t *testing.T) {
	m := zzzManager(t, "hang", 2000)
	start := time.Now()
	m.ReadyClientForFile("main.zzz") // triggers the background start
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("ReadyClientForFile blocked %v; server start must run in the background", elapsed)
	}
}

func TestClientForFileQuickFailsFastOnStartFailure(t *testing.T) {
	m := zzzManager(t, "fail", 10000)
	start := time.Now()
	_, _, err := m.ClientForFileQuick("main.zzz")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected error for a server that exits immediately")
	}
	if elapsed > 2500*time.Millisecond {
		t.Fatalf("ClientForFileQuick spun %v after the start already failed; want fast failure", elapsed)
	}
}

func TestFindServerCached(t *testing.T) {
	m := zzzManager(t, "ok", 0)
	srv := m.config.Servers["zzz"]

	// A fresh negative entry short-circuits discovery even though the
	// binary exists.
	m.mu.Lock()
	m.foundCache["zzz"] = foundEntry{found: false, checkedAt: time.Now()}
	m.mu.Unlock()
	if _, _, found := m.findServerCached("zzz", srv); found {
		t.Fatal("fresh negative cache entry must short-circuit discovery")
	}

	// An expired negative entry is re-checked and finds the binary.
	m.mu.Lock()
	m.foundCache["zzz"] = foundEntry{found: false, checkedAt: time.Now().Add(-2 * notFoundRetryTTL)}
	m.mu.Unlock()
	if _, _, found := m.findServerCached("zzz", srv); !found {
		t.Fatal("expired negative entry must re-run discovery")
	}

	// A positive entry is served from the cache verbatim.
	m.mu.Lock()
	m.foundCache["zzz"] = foundEntry{path: "/cached/fake-lsp", found: true, checkedAt: time.Now().Add(-time.Hour)}
	m.mu.Unlock()
	if path, _, found := m.findServerCached("zzz", srv); !found || path != "/cached/fake-lsp" {
		t.Fatalf("positive entry must be served from cache, got %q found=%v", path, found)
	}
}

func TestNewManager(t *testing.T) {
	m := NewManager(nil, "/workspace")
	if m == nil {
		t.Fatal("nil manager")
	}
	if m.config == nil || len(m.config.Servers) == 0 {
		t.Fatal("expected defaults")
	}
}

func TestNewManager_WithConfig(t *testing.T) {
	cfg := NewLSPConfig()
	cfg.RegisterServer("t1", &ServerConfig{Command: "t1", ExtensionToLanguage: map[string]string{".t1": "t1"}})
	m := NewManager(cfg, "/custom")
	if _, ok := m.config.Servers["t1"]; !ok {
		t.Fatal("expected t1")
	}
}

func TestManager_Close_Empty(t *testing.T) {
	m := NewManager(nil, "/w")
	if err := m.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestManager_Close_Clients(t *testing.T) {
	m := NewManager(nil, "/w")
	m.mu.Lock()
	m.clients["a"] = &Client{language: "a"}
	m.clients["b"] = &Client{language: "b"}
	m.mu.Unlock()
	if err := m.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	m.mu.RLock()
	n := len(m.clients)
	m.mu.RUnlock()
	if n != 0 {
		t.Fatalf("expected 0, got %d", n)
	}
}

func TestManager_ReadyLanguages(t *testing.T) {
	m := NewManager(nil, "/w")
	m.mu.Lock()
	c := &Client{language: "go"}
	c.ready.Store(true)
	m.clients["go"] = c
	m.mu.Unlock()
	r := m.ReadyLanguages()
	if len(r) != 1 || r[0] != "go" {
		t.Fatalf("got %v", r)
	}
}

func TestManager_ReadyLanguages_OnlyReady(t *testing.T) {
	m := NewManager(nil, "/w")
	m.mu.Lock()
	ra := &Client{language: "a"}
	ra.ready.Store(true)
	rb := &Client{language: "b"}
	m.clients["a"] = ra
	m.clients["b"] = rb
	m.mu.Unlock()
	r := m.ReadyLanguages()
	if len(r) != 1 || r[0] != "a" {
		t.Fatalf("got %v", r)
	}
}

func TestManager_IsReady(t *testing.T) {
	m := NewManager(nil, "/w")
	if m.IsReady(".go") {
		t.Fatal("expected false")
	}
}

func TestManager_setStatus(t *testing.T) {
	m := NewManager(nil, "/w")
	m.setStatus("go", "loaded", "/bin/gopls", "")
	m.mu.RLock()
	defer m.mu.RUnlock()
	st := m.statusCache["go"]
	if st.State != "loaded" || st.Path != "/bin/gopls" {
		t.Fatalf("got %+v", st)
	}
}

func TestManager_newClient(t *testing.T) {
	m := NewManager(nil, "/w")
	srv := &ServerConfig{Command: "gopls", ExtensionToLanguage: map[string]string{".go": "go"}, Env: map[string]string{"K": "v"}}
	c := m.newClient("go", srv, "/bin/gopls", []string{"-x"})
	if c.language != "go" || c.command != "/bin/gopls" {
		t.Fatalf("got %s %s", c.language, c.command)
	}
}

func TestManager_ClientForFile_UnknownExt(t *testing.T) {
	m := NewManager(nil, "/w")
	_, err := m.ClientForFile(nil, "f.xyz")
	if err == nil || !strings.Contains(err.Error(), "no LSP server") {
		t.Fatal("expected no LSP server error")
	}
}

func TestManager_ClientForLanguage_Unknown(t *testing.T) {
	m := NewManager(nil, "/w")
	_, err := m.ClientForLanguage(nil, "nope")
	if err == nil || !strings.Contains(err.Error(), "unknown language") {
		t.Fatal("expected unknown language error")
	}
}

func TestManager_ReadyClientForFile_NotFound(t *testing.T) {
	m := NewManager(nil, "/w")
	_, _, ok := m.ReadyClientForFile("f.xyz")
	if ok {
		t.Fatal("expected false")
	}
}

func TestManager_ClientForFileQuick_NoServer(t *testing.T) {
	m := NewManager(nil, "/w")
	_, _, err := m.ClientForFileQuick("f.xyz")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestManager_Warmup_EmptyWorkspace(t *testing.T) {
	NewManager(nil, "").Warmup()
}

func TestManager_Warmup_WithFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("pkg main"), 0644)
	NewManager(nil, dir).Warmup()
}

func TestManager_SymbolOutline_NoClient(t *testing.T) {
	m := NewManager(nil, "/w")
	if s := m.SymbolOutline(nil, "f.go"); s != "" {
		t.Fatalf("expected empty, got %s", s)
	}
}

func TestManager_ensureAsync_Nop(t *testing.T) {
	m := NewManager(nil, "/w")
	m.ensureAsync("nope")
}

func TestFormatSymbolsFlat(t *testing.T) {
	s := []DocumentSymbol{
		{Name: "F1", Kind: SymbolKindFunction, Range: Range{Start: Position{Line: 9}}},
		{Name: "T1", Kind: SymbolKindStruct, Range: Range{Start: Position{Line: 24}},
			Children: []DocumentSymbol{
				{Name: "F2", Kind: SymbolKindField, Range: Range{Start: Position{Line: 25}}},
			}},
	}
	var b strings.Builder
	formatSymbolsFlat(&b, s, 0)
	r := b.String()
	for _, want := range []string{"F1", "T1", "F2", "function", "struct", "field"} {
		if !strings.Contains(r, want) {
			t.Fatalf("missing %q", want)
		}
	}
}

func TestFormatSymbolsFlat_Empty(t *testing.T) {
	var b strings.Builder
	formatSymbolsFlat(&b, nil, 0)
	if b.Len() != 0 {
		t.Fatal("expected empty")
	}
}

func TestFormatSymbolsFlat_Nested(t *testing.T) {
	s := []DocumentSymbol{{
		Name: "L1", Kind: SymbolKindNamespace, Range: Range{Start: Position{Line: 0}},
		Children: []DocumentSymbol{{
			Name: "L2", Kind: SymbolKindClass, Range: Range{Start: Position{Line: 1}},
			Children: []DocumentSymbol{{
				Name: "L3", Kind: SymbolKindMethod, Range: Range{Start: Position{Line: 2}},
			}},
		}},
	}}
	var b strings.Builder
	formatSymbolsFlat(&b, s, 0)
	r := b.String()
	for _, want := range []string{"L1", "L2", "L3"} {
		if !strings.Contains(r, want) {
			t.Fatalf("missing %q", want)
		}
	}
}

func TestScanDir(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("p"), 0644)
	os.MkdirAll(filepath.Join(dir, "sub"), 0755)
	os.WriteFile(filepath.Join(dir, "sub", "b.rs"), []byte("fn"), 0644)
	os.MkdirAll(filepath.Join(dir, ".git"), 0755)
	os.WriteFile(filepath.Join(dir, ".git", "cfg"), []byte(""), 0644)
	cache := make(map[string]bool)
	scanDir(dir, 0, cache, new(int), 5, 200)
	if !cache[".go"] || !cache[".rs"] {
		t.Fatal("expected .go and .rs")
	}
}

func TestScanDir_DepthLimit(t *testing.T) {
	dir := t.TempDir()
	// Create a file at depth 6 (0-indexed: a/b/c/d/e/f) — beyond maxDepth 5
	d6 := filepath.Join(dir, "a", "b", "c", "d", "e", "f")
	os.MkdirAll(d6, 0755)
	os.WriteFile(filepath.Join(d6, "deep.go"), []byte("p"), 0644)
	cache := make(map[string]bool)
	scanDir(dir, 0, cache, new(int), 5, 200)
	if cache[".go"] {
		t.Fatal("should not reach depth > 5")
	}
}

func TestScanDir_MissingDir(t *testing.T) {
	scanDir("/nope", 0, make(map[string]bool), new(int), 5, 200)
}

func TestHasWorkspaceFiles(t *testing.T) {
	m := NewManager(nil, "/w")
	srv := &ServerConfig{ExtensionToLanguage: map[string]string{".go": "go"}}
	if m.hasWorkspaceFiles(srv) {
		t.Fatal("expected false with nil cache")
	}
	m.mu.Lock()
	m.extCache = map[string]bool{".go": true}
	m.mu.Unlock()
	if !m.hasWorkspaceFiles(srv) {
		t.Fatal("expected true")
	}
}

func TestHasWorkspaceFiles_NoMatch(t *testing.T) {
	m := NewManager(nil, "/w")
	m.mu.Lock()
	m.extCache = map[string]bool{".py": true}
	m.mu.Unlock()
	srv := &ServerConfig{ExtensionToLanguage: map[string]string{".go": "go"}}
	if m.hasWorkspaceFiles(srv) {
		t.Fatal("expected false")
	}
}

func TestManager_AvailableSummary(t *testing.T) {
	m := NewManager(nil, "/w")
	s := m.AvailableSummary()
	if strings.Contains(s, "scanning") {
		t.Logf("scanning: %s", s)
	}
}

func TestManager_AvailableLanguages(t *testing.T) {
	m := NewManager(nil, "/w")
	for _, name := range m.AvailableLanguages() {
		if name == "" {
			t.Fatal("empty name")
		}
	}
}

func TestManager_ensureAsync_NoopForMissing(t *testing.T) {
	m := NewManager(nil, "/w")
	m.ensureAsync("nonexistent")
}

func TestManager_ReadyClientForFile_TriggersAsync(t *testing.T) {
	m := NewManager(nil, "/w")
	_, _, ok := m.ReadyClientForFile("test.go")
	if ok {
		t.Fatal("expected not ready")
	}
}

func TestScanDir_SkipsNodeModules(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "node_modules"), 0755)
	os.WriteFile(filepath.Join(dir, "node_modules", "big.js"), []byte("x"), 0644)
	os.WriteFile(filepath.Join(dir, "pkg.json"), []byte("{}"), 0644)
	cache := make(map[string]bool)
	scanDir(dir, 0, cache, new(int), 5, 200)
	if cache[".js"] {
		t.Fatal("should skip node_modules")
	}
	if !cache[".json"] {
		t.Fatal("expected .json")
	}
}

func TestScanDir_IgnoresDotDirs(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".hidden"), 0755)
	os.WriteFile(filepath.Join(dir, ".hidden", "a.go"), []byte("x"), 0644)
	os.MkdirAll(filepath.Join(dir, "src"), 0755)
	os.WriteFile(filepath.Join(dir, "src", "b.go"), []byte("x"), 0644)
	cache := make(map[string]bool)
	scanDir(dir, 0, cache, new(int), 5, 200)
	if !cache[".go"] {
		t.Fatal("expected .go found")
	}
}

func TestManager_newClient_SetsRootURI(t *testing.T) {
	m := NewManager(nil, "/my/workspace")
	srv := &ServerConfig{Command: "t", ExtensionToLanguage: map[string]string{".t": "t"}}
	c := m.newClient("t", srv, "/bin/t", nil)
	if c.rootURI != "file:///my/workspace" {
		t.Fatalf("got %s", c.rootURI)
	}
}

func TestManager_newClient_TimeoutDefaults(t *testing.T) {
	m := NewManager(nil, "/w")
	srv := &ServerConfig{Command: "t", ExtensionToLanguage: map[string]string{".t": "t"}}
	c := m.newClient("t", srv, "/bin/t", nil)
	if c.startupTimeout <= 0 || c.shutdownTimeout <= 0 {
		t.Fatal("expected positive timeouts")
	}
}

func TestManager_setStatus_Overwrite(t *testing.T) {
	m := NewManager(nil, "/w")
	m.setStatus("go", "loaded", "/a", "")
	m.setStatus("go", "failed", "/b", "crash")
	m.mu.RLock()
	st := m.statusCache["go"]
	m.mu.RUnlock()
	if st.State != "failed" || st.Reason != "crash" {
		t.Fatalf("got %+v", st)
	}
}

func TestManager_Close_NoPanic(t *testing.T) {
	m := NewManager(nil, "/w")
	for i := 0; i < 5; i++ {
		if err := m.Close(); err != nil {
			t.Fatalf("close %d: %v", i, err)
		}
	}
}

func TestManager_ClientForFile_CachedClient(t *testing.T) {
	m := NewManager(nil, "/w")
	m.mu.Lock()
	m.clients["go"] = &Client{language: "go"}
	m.mu.Unlock()
	srv, _, _ := m.config.ForExtension(".go")
	if srv == nil {
		t.Fatal("expected go config")
	}
}

func TestManager_AvailableSummary_Failed(t *testing.T) {
	m := NewManager(nil, "/w")
	m.mu.Lock()
	m.foundCache = map[string]foundEntry{
		"go": {path: "/bin/gopls", found: true},
	}
	m.extCache = map[string]bool{".go": true}
	m.statusCache = map[string]ServerStatus{
		"go": {Name: "go", State: "failed", Path: "/bin/gopls", Reason: "timeout"},
	}
	m.mu.Unlock()
	s := m.AvailableSummary()
	if !strings.Contains(s, "failed") || !strings.Contains(s, "timeout") {
		t.Fatalf("expected failure info, got: %s", s)
	}
}

func TestManager_ensureAsync_AlreadyRunning(t *testing.T) {
	m := NewManager(nil, "/w")
	m.mu.Lock()
	c := &Client{language: "go"}
	c.ready.Store(true)
	m.clients["go"] = c
	m.mu.Unlock()
	m.ensureAsync("go")
}

func TestManager_Scanning_Atomic(t *testing.T) {
	m := NewManager(nil, "/w")
	if m.scanning.Load() {
		t.Fatal("should not be scanning initially")
	}
}

func TestManager_AvailableSummary_LoadedStatus(t *testing.T) {
	m := NewManager(nil, "/w")
	m.mu.Lock()
	m.foundCache = map[string]foundEntry{
		"go": {path: "/bin/gopls", found: true},
	}
	m.extCache = map[string]bool{".go": true}
	c := &Client{language: "go"}
	c.ready.Store(true)
	m.clients["go"] = c
	m.mu.Unlock()
	s := m.AvailableSummary()
	if !strings.Contains(s, "loaded") {
		t.Fatalf("expected loaded, got: %s", s)
	}
}

func TestManager_AvailableSummary_NotUsed(t *testing.T) {
	m := NewManager(nil, "/w")
	m.mu.Lock()
	m.foundCache = map[string]foundEntry{
		"go": {path: "/bin/gopls", found: true},
	}
	m.mu.Unlock()
	s := m.AvailableSummary()
	t.Logf("summary: %s", s)
}

func TestManager_Close_ErrorCollection(t *testing.T) {
	m := NewManager(nil, "/w")
	m.mu.Lock()
	m.clients["a"] = &Client{language: "a"}
	m.clients["b"] = &Client{language: "b"}
	m.mu.Unlock()
	_ = m.Close()
}

func TestManager_HasWorkspaceFiles_Multiple(t *testing.T) {
	m := NewManager(nil, "/w")
	m.mu.Lock()
	m.extCache = map[string]bool{".go": true, ".py": true}
	m.mu.Unlock()
	srv := &ServerConfig{ExtensionToLanguage: map[string]string{".go": "go"}}
	if !m.hasWorkspaceFiles(srv) {
		t.Fatal("expected true")
	}
}

func TestManager_ClientForFileQuick_FirstAttempt(t *testing.T) {
	// Isolate discovery: with a real gopls on PATH this would spawn it and
	// (on a warm start) return a ready client instead of the expected error.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("PATH", t.TempDir())
	m := NewManager(nil, "/w")
	_, _, err := m.ClientForFileQuick("test.go")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestManager_ClientForFileQuick_AlreadyStarting(t *testing.T) {
	m := zzzManager(t, "hang", 5000)
	// A client mid-startup outside backgroundStart (clientForServer path):
	// no in-flight attempt channel, but isStarting reports true.
	c := &Client{language: "zzz"}
	c.starting.Store(true)
	m.mu.Lock()
	m.clients["zzz"] = c
	m.mu.Unlock()
	_, _, err := m.ClientForFileQuick("test.zzz")
	if err == nil || !strings.Contains(err.Error(), "starting up") {
		t.Fatalf("expected still-starting error, got: %v", err)
	}
}

func TestManager_ClientForFile_AbsolutePath(t *testing.T) {
	m := NewManager(nil, "/w")
	_, err := m.ClientForFile(nil, "/abs/test.xyz")
	if err == nil || !strings.Contains(err.Error(), "no LSP server") {
		t.Fatal("expected no LSP server error")
	}
}

func TestManager_ReadyLanguages_EmptyClients(t *testing.T) {
	m := NewManager(nil, "/w")
	if r := m.ReadyLanguages(); len(r) != 0 {
		t.Fatalf("expected empty, got %v", r)
	}
}

func TestManager_SymbolOutline_NoServer(t *testing.T) {
	m := NewManager(nil, "/w")
	if r := m.SymbolOutline(nil, "test.go"); r != "" {
		t.Fatalf("got %s", r)
	}
}

func TestManager_ensureAsync_NoopForMissing2(t *testing.T) {
	m := NewManager(nil, "/w")
	m.ensureAsync("nonexistent")
}

func TestManager_Client_CloseIdempotent(t *testing.T) {
	c := &Client{}
	for i := 0; i < 3; i++ {
		if err := c.Close(); err != nil {
			t.Fatalf("close %d: %v", i, err)
		}
	}
}

func TestManager_Client_CloseSetsReadyFalse(t *testing.T) {
	c := &Client{}
	c.ready.Store(true)
	c.Close()
	if c.ready.Load() {
		t.Fatal("expected not ready after close")
	}
}

func TestClient_isStarting_Flag(t *testing.T) {
	c := &Client{language: "go"}
	if c.isStarting() {
		t.Fatal("should not start")
	}
	c.starting.Store(true)
	if !c.isStarting() {
		t.Fatal("should start")
	}
}

func TestClient_isStarting_ConnReady(t *testing.T) {
	c := &Client{
		language: "go",
		conn:     &rpcConn{},
	}
	if !c.isStarting() {
		t.Fatal("should be starting when conn exists but not ready")
	}
}

func TestClient_alive_DoneChannelClosed(t *testing.T) {
	c := &Client{}
	c.ready.Store(true)
	done := make(chan struct{})
	close(done)
	c.done = done
	if c.alive() {
		t.Fatal("expected not alive when done closed")
	}
}

func TestClient_alive_ExitedAtomic(t *testing.T) {
	c := &Client{}
	c.ready.Store(true)
	done := make(chan struct{})
	c.done = done
	c.exited.Store(true)
	if c.alive() {
		t.Fatal("expected not alive after exit")
	}
}

func TestManager_Client_SnapshotConnAfterClose(t *testing.T) {
	c := &Client{}
	c.Close()
	if conn := c.snapshotConn(); conn != nil {
		t.Fatal("expected nil conn after close")
	}
}
