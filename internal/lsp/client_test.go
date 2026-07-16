package lsp

import (
	"strings"

	"testing"
)

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
		got := languageIDFromPath(tc.path)
		if got != tc.want {
			t.Errorf("languageIDFromPath(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestLanguageIDFromPath_Empty(t *testing.T) {
	got := languageIDFromPath("")
	if got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestLanguageIDFromPath_Windows(t *testing.T) {
	got := languageIDFromPath(`C:\Users\test\main.go`)
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
