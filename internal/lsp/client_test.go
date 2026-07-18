package lsp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// notifCaptureClient returns a Client whose rpcConn writes into a buffer,
// plus a func decoding the buffered frames into JSON-RPC method names.
func notifCaptureClient(t *testing.T) (*Client, func() []string) {
	t.Helper()
	var buf bytes.Buffer
	c := &Client{
		language:        "zzz",
		openDocs:        make(map[string]openDocState),
		conn:            newRPCConn(strings.NewReader(""), &buf),
		shutdownTimeout: 50 * time.Millisecond,
	}
	methods := func() []string {
		r := NewReader(bytes.NewReader(buf.Bytes()))
		var out []string
		for {
			data, err := r.ReadMessage()
			if err != nil {
				return out
			}
			var m Message
			if json.Unmarshal(data, &m) == nil && m.Method != "" {
				out = append(out, m.Method)
			}
		}
	}
	return c, methods
}

func writeTestDoc(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "doc.zzz")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write doc: %v", err)
	}
	return path
}

func TestEnsureDocumentOpenSkipsUnchanged(t *testing.T) {
	c, methods := notifCaptureClient(t)
	uri := PathToURI(writeTestDoc(t, "one"))
	for i := 0; i < 2; i++ {
		if err := c.ensureDocumentOpen(context.Background(), uri); err != nil {
			t.Fatalf("ensureDocumentOpen: %v", err)
		}
	}
	want := []string{"textDocument/didOpen"}
	if got := methods(); !equalStrings(got, want) {
		t.Fatalf("methods = %v, want %v", got, want)
	}
}

// A changed file must not be re-announced with a bare didOpen: the LSP spec
// forbids didOpen for an already-open document (servers keep the stale
// buffer). The client must close the old document first.
func TestEnsureDocumentOpenReopensChangedFile(t *testing.T) {
	c, methods := notifCaptureClient(t)
	path := writeTestDoc(t, "one")
	uri := PathToURI(path)
	if err := c.ensureDocumentOpen(context.Background(), uri); err != nil {
		t.Fatalf("first ensureDocumentOpen: %v", err)
	}
	if err := os.WriteFile(path, []byte("two"), 0o644); err != nil {
		t.Fatalf("rewrite doc: %v", err)
	}
	bumped := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, bumped, bumped); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	if err := c.ensureDocumentOpen(context.Background(), uri); err != nil {
		t.Fatalf("second ensureDocumentOpen: %v", err)
	}
	want := []string{"textDocument/didOpen", "textDocument/didClose", "textDocument/didOpen"}
	if got := methods(); !equalStrings(got, want) {
		t.Fatalf("methods = %v, want %v", got, want)
	}
}

func TestOpenDocsEvictionBounded(t *testing.T) {
	c, methods := notifCaptureClient(t)
	dir := t.TempDir()
	for i := 0; i <= maxOpenDocs; i++ {
		path := filepath.Join(dir, fmt.Sprintf("doc%03d.zzz", i))
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatalf("write doc: %v", err)
		}
		if err := c.ensureDocumentOpen(context.Background(), PathToURI(path)); err != nil {
			t.Fatalf("ensureDocumentOpen %d: %v", i, err)
		}
	}
	c.mu.Lock()
	n := len(c.openDocs)
	c.mu.Unlock()
	if n > maxOpenDocs {
		t.Fatalf("openDocs holds %d entries, limit is %d", n, maxOpenDocs)
	}
	var closes int
	for _, m := range methods() {
		if m == "textDocument/didClose" {
			closes++
		}
	}
	if closes == 0 {
		t.Fatal("expected a didClose for the evicted document")
	}
}

func TestLanguageIDFromPathRespectsExtensionToLanguage(t *testing.T) {
	c := &Client{language: "zzz", languageByExt: map[string]string{".zzz": "cool-lang"}}
	uri := PathToURI(writeTestDoc(t, "x"))
	// Without a conn ensureDocumentOpen will fail, but languageIDFromPath
	// is called first — its result determines the didOpen payload.
	got := languageIDFromPath(c, URIToPath(uri))
	if got != "cool-lang" {
		t.Fatalf("languageID = %q, want %q (config mapping bypassed)", got, "cool-lang")
	}
}

func TestCloseClearsOpenDocs(t *testing.T) {
	c, _ := notifCaptureClient(t)
	uri := PathToURI(writeTestDoc(t, "one"))
	if err := c.ensureDocumentOpen(context.Background(), uri); err != nil {
		t.Fatalf("ensureDocumentOpen: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	c.mu.Lock()
	n := len(c.openDocs)
	c.mu.Unlock()
	if n != 0 {
		t.Fatalf("openDocs has %d entries after Close; a restarted server would never receive didOpen", n)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestStartFailureReleasesConcurrentWaiter: when the winning Start attempt
// fails, a concurrent Start waiting on readyCh must be released promptly
// instead of sitting out the full startupTimeout.
func TestStartFailureReleasesConcurrentWaiter(t *testing.T) {
	fake := fakeExecutable(t, t.TempDir(), "fake-lsp", "fail")
	c := &Client{
		language:        "zzz",
		command:         fake,
		rootURI:         PathToURI(t.TempDir()),
		startupTimeout:  10 * time.Second,
		shutdownTimeout: time.Second,
	}
	winnerErr := make(chan error, 1)
	go func() { winnerErr <- c.Start(context.Background()) }()
	// Wait until the winner holds the starting flag — bounded, because on a
	// slow machine the winner may already have failed and released it.
	waitUntil := time.Now().Add(3 * time.Second)
	for !c.isStarting() && time.Now().Before(waitUntil) {
		select {
		case err := <-winnerErr:
			// Winner already finished; the "loser" below simply becomes a
			// fresh start attempt, which must also fail fast.
			winnerErr <- err
			waitUntil = time.Now()
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	start := time.Now()
	err := c.Start(context.Background())
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected loser Start to fail")
	}
	// The essential claim: the loser is released when the winner fails,
	// rather than sitting out the full 10s startupTimeout. The margin
	// tolerates slow process spawn under load (AV scans, parallel tests).
	if elapsed > 6*time.Second {
		t.Fatalf("loser waited %v; must be released when the winner fails", elapsed)
	}
	if werr := <-winnerErr; werr == nil {
		t.Fatal("expected winner Start to fail")
	}
}

func TestMergeEnv_MergeWithOverrides(t *testing.T) {
	base := []string{"HOME=/home/user", "PATH=/usr/bin", "LANG=en"}
	overrides := map[string]string{"PATH": "/custom/bin", "FOO": "bar"}
	result := mergeEnv(base, overrides)
	resultMap := envToMap(result)
	if resultMap["HOME"] != "/home/user" {
		t.Fatalf("expected HOME=/home/user, got %s", resultMap["HOME"])
	}
	if resultMap["PATH"] != "/custom/bin" {
		t.Fatalf("expected PATH=/custom/bin, got %s", resultMap["PATH"])
	}
	if resultMap["FOO"] != "bar" {
		t.Fatalf("expected FOO=bar, got %s", resultMap["FOO"])
	}
}

func TestMergeEnv_EmptyBase(t *testing.T) {
	result := mergeEnv(nil, map[string]string{"KEY": "val"})
	resultMap := envToMap(result)
	if resultMap["KEY"] != "val" {
		t.Fatalf("expected KEY=val, got %s", resultMap["KEY"])
	}
	if len(result) < 1 {
		t.Fatalf("expected at least 1 var, got %d", len(result))
	}
}

func TestMergeEnv_EmptyOverrides(t *testing.T) {
	base := []string{"HOME=/home/user", "PATH=/usr/bin"}
	result := mergeEnv(base, nil)
	resultMap := envToMap(result)
	if resultMap["HOME"] != "/home/user" || resultMap["PATH"] != "/usr/bin" {
		t.Fatalf("expected unchanged base env")
	}
}

func TestMergeEnv_OverridesWithEmpty(t *testing.T) {
	base := []string{"KEY=original"}
	result := mergeEnv(base, map[string]string{"KEY": ""})
	resultMap := envToMap(result)
	if resultMap["KEY"] != "" {
		t.Fatalf("expected KEY=\"\", got %q", resultMap["KEY"])
	}
}

func TestMergeEnv_AddsNewVars(t *testing.T) {
	base := []string{"EXISTING=yes"}
	result := mergeEnv(base, map[string]string{"NEW1": "a", "NEW2": "b"})
	resultMap := envToMap(result)
	if resultMap["NEW1"] != "a" || resultMap["NEW2"] != "b" {
		t.Fatalf("expected new vars in result")
	}
}

func TestMergeEnv_MalformedBaseEntry(t *testing.T) {
	base := []string{"MALFORMED", "GOOD=val"}
	result := mergeEnv(base, map[string]string{"ADD": "extra"})
	resultMap := envToMap(result)
	if resultMap["GOOD"] != "val" || resultMap["ADD"] != "extra" {
		t.Fatalf("unexpected merge result")
	}
}

func TestLanguageIDFromPath(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/path/to/file.go", "go"},
		{"main.rs", "rust"},
		{"test.py", "python"},
		{"test.pyi", "python"},
		{"app.ts", "typescript"},
		{"component.tsx", "typescriptreact"},
		{"util.js", "javascript"},
		{"button.jsx", "javascriptreact"},
		{"main.c", "c"},
		{"main.cpp", "cpp"},
		{"main.cc", "cpp"},
		{"main.cxx", "cpp"},
		{"header.h", "c"},
		{"header.hpp", "cpp"},
		{"header.hxx", "cpp"},
		{"config.yaml", "yaml"},
		{"config.yml", "yaml"},
		{"app.vue", "vue"},
		{"data.json", "json"},
		{"style.css", "css"},
		{"index.html", "html"},
		{"page.htm", "html"},
		{"noextension", ""},
		{"/path/to/.hidden", "hidden"},
		{"Makefile", ""},
	}
	for _, tc := range tests {
		got := languageIDFromPath(&Client{}, tc.path)
		if got != tc.want {
			t.Errorf("languageIDFromPath(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestLanguageIDFromPath_Empty(t *testing.T) {
	got := languageIDFromPath(&Client{}, "")
	if got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestLanguageIDFromPath_Windows(t *testing.T) {
	got := languageIDFromPath(&Client{}, `C:\Users\test\main.go`)
	if got != "go" {
		t.Fatalf("expected go, got %q", got)
	}
}

func TestNewClientInitialState(t *testing.T) {
	c := &Client{
		language: "go",
		command:  "gopls",
		rootURI:  "file:///workspace",
	}
	if c.language != "go" || c.command != "gopls" {
		t.Fatal("fields not set correctly")
	}
	if c.ready.Load() || c.starting.Load() || c.exited.Load() {
		t.Fatal("expected all atomic flags false initially")
	}
}

func TestClient_aliveNotReady(t *testing.T) {
	c := &Client{}
	if c.alive() {
		t.Fatal("expected not alive when not ready")
	}
}

func TestClient_CloseNilConn(t *testing.T) {
	c := &Client{}
	if err := c.Close(); err != nil {
		t.Fatalf("Close on nil conn: %v", err)
	}
}

func TestClient_isStarting(t *testing.T) {
	c := &Client{}
	if c.isStarting() {
		t.Fatal("expected false for new client")
	}
}

func TestClient_snapshotConnNil(t *testing.T) {
	c := &Client{}
	if conn := c.snapshotConn(); conn != nil {
		t.Fatal("expected nil conn")
	}
}

func TestClient_lspMethodsNoConn(t *testing.T) {
	c := &Client{}
	tests := []struct {
		name string
		err  error
		fn   func() error
	}{
		{"GoToDefinition", nil, func() error { _, err := c.GoToDefinition(nil, "uri", 0, 0); return err }},
		{"FindReferences", nil, func() error { _, err := c.FindReferences(nil, "uri", 0, 0, true); return err }},
		{"Hover", nil, func() error { _, err := c.Hover(nil, "uri", 0, 0); return err }},
		{"DocumentSymbols", nil, func() error { _, err := c.DocumentSymbols(nil, "uri"); return err }},
		{"WorkspaceSymbols", nil, func() error { _, err := c.WorkspaceSymbols(nil, "q"); return err }},
		{"GoToImplementation", nil, func() error { _, err := c.GoToImplementation(nil, "uri", 0, 0); return err }},
		{"PrepareCallHierarchy", nil, func() error { _, err := c.PrepareCallHierarchy(nil, "uri", 0, 0); return err }},
		{"IncomingCalls", nil, func() error { _, err := c.IncomingCalls(nil, CallHierarchyItem{}); return err }},
		{"OutgoingCalls", nil, func() error { _, err := c.OutgoingCalls(nil, CallHierarchyItem{}); return err }},
	}
	for _, tc := range tests {
		if err := tc.fn(); err == nil {
			t.Errorf("%s: expected error without connection", tc.name)
		}
	}
}

func TestEnsureDocumentOpen_NoConn(t *testing.T) {
	c := &Client{}
	err := c.ensureDocumentOpen(nil, "file:///test.go")
	if err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("expected 'not connected' error, got: %v", err)
	}
}

func envToMap(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, kv := range env {
		if idx := strings.Index(kv, "="); idx >= 0 {
			m[kv[:idx]] = kv[idx+1:]
		}
	}
	return m
}
