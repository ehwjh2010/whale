package lsp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- ServerConfig.Validate tests ---

func TestServerConfig_Validate_Valid(t *testing.T) {
	srv := &ServerConfig{
		Command:             "gopls",
		ExtensionToLanguage: map[string]string{".go": "go"},
	}
	if err := srv.Validate(); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestServerConfig_Validate_MissingCommand(t *testing.T) {
	srv := &ServerConfig{
		ExtensionToLanguage: map[string]string{".go": "go"},
	}
	err := srv.Validate()
	if err == nil {
		t.Fatal("expected error for missing command")
	}
	if !strings.Contains(err.Error(), "command") {
		t.Fatalf("expected error about command, got: %v", err)
	}
}

func TestServerConfig_Validate_EmptyCommand(t *testing.T) {
	srv := &ServerConfig{
		Command:             "  ",
		ExtensionToLanguage: map[string]string{".go": "go"},
	}
	err := srv.Validate()
	if err == nil {
		t.Fatal("expected error for whitespace-only command")
	}
}

func TestServerConfig_Validate_MissingExtensionMap(t *testing.T) {
	srv := &ServerConfig{Command: "gopls"}
	err := srv.Validate()
	if err == nil {
		t.Fatal("expected error for missing extensionToLanguage")
	}
	if !strings.Contains(err.Error(), "extensionToLanguage") {
		t.Fatalf("expected error about extensionToLanguage, got: %v", err)
	}
}

func TestServerConfig_Validate_EmptyExtensionMap(t *testing.T) {
	srv := &ServerConfig{
		Command:             "gopls",
		ExtensionToLanguage: map[string]string{},
	}
	err := srv.Validate()
	if err == nil {
		t.Fatal("expected error for empty extensionToLanguage")
	}
}

// --- ServerConfig timeout tests ---

func TestServerConfig_StartupTimeout_Default(t *testing.T) {
	srv := &ServerConfig{Command: "gopls", ExtensionToLanguage: map[string]string{".go": "go"}}
	got := srv.StartupTimeout()
	if got != 30*time.Second {
		t.Fatalf("expected 30s, got %v", got)
	}
}

func TestServerConfig_StartupTimeout_Custom(t *testing.T) {
	srv := &ServerConfig{
		Command:             "gopls",
		ExtensionToLanguage: map[string]string{".go": "go"},
		StartupTimeoutMS:    60000,
	}
	got := srv.StartupTimeout()
	if got != 60*time.Second {
		t.Fatalf("expected 60s, got %v", got)
	}
}

func TestServerConfig_ShutdownTimeout_Default(t *testing.T) {
	srv := &ServerConfig{Command: "gopls", ExtensionToLanguage: map[string]string{".go": "go"}}
	got := srv.ShutdownTimeout()
	if got != 5*time.Second {
		t.Fatalf("expected 5s, got %v", got)
	}
}

func TestServerConfig_ShutdownTimeout_Custom(t *testing.T) {
	srv := &ServerConfig{
		Command:             "gopls",
		ExtensionToLanguage: map[string]string{".go": "go"},
		ShutdownTimeoutMS:   10000,
	}
	got := srv.ShutdownTimeout()
	if got != 10*time.Second {
		t.Fatalf("expected 10s, got %v", got)
	}
}

// --- ServerConfig behaviour flags ---

func TestServerConfig_ShouldRestartOnCrash_Default(t *testing.T) {
	srv := &ServerConfig{}
	if !srv.ShouldRestartOnCrash() {
		t.Fatal("expected default true")
	}
}

func TestServerConfig_ShouldRestartOnCrash_Explicit(t *testing.T) {
	f := false
	srv := &ServerConfig{RestartOnCrash: &f}
	if srv.ShouldRestartOnCrash() {
		t.Fatal("expected false")
	}
}

func TestServerConfig_ShouldPushDiagnostics_Default(t *testing.T) {
	srv := &ServerConfig{}
	if !srv.ShouldPushDiagnostics() {
		t.Fatal("expected default true")
	}
}

func TestServerConfig_ShouldPushDiagnostics_Explicit(t *testing.T) {
	f := false
	srv := &ServerConfig{Diagnostics: &f}
	if srv.ShouldPushDiagnostics() {
		t.Fatal("expected false")
	}
}

// --- LSPConfig tests ---

func TestNewLSPConfig(t *testing.T) {
	cfg := NewLSPConfig()
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if len(cfg.Servers) != 0 {
		t.Fatalf("expected empty servers, got %d", len(cfg.Servers))
	}
}

func TestRegisterServer_Success(t *testing.T) {
	cfg := NewLSPConfig()
	err := cfg.RegisterServer("go", &ServerConfig{
		Command:             "gopls",
		ExtensionToLanguage: map[string]string{".go": "go"},
	})
	if err != nil {
		t.Fatalf("register error: %v", err)
	}
	if _, ok := cfg.Servers["go"]; !ok {
		t.Fatal("expected go server in config")
	}
}

func TestRegisterServer_Invalid(t *testing.T) {
	cfg := NewLSPConfig()
	err := cfg.RegisterServer("bad", &ServerConfig{})
	if err == nil {
		t.Fatal("expected error for invalid server")
	}
}

func TestRegisterServer_ExtensionConflict(t *testing.T) {
	cfg := NewLSPConfig()
	_ = cfg.RegisterServer("go", &ServerConfig{
		Command:             "gopls",
		ExtensionToLanguage: map[string]string{".go": "go"},
	})
	err := cfg.RegisterServer("gopls2", &ServerConfig{
		Command:             "gopls",
		ExtensionToLanguage: map[string]string{".go": "go"},
	})
	if err == nil {
		t.Fatal("expected error for extension conflict")
	}
	if !strings.Contains(err.Error(), ".go") {
		t.Fatalf("expected error mentioning .go, got: %v", err)
	}
}

func TestForExtension_Found(t *testing.T) {
	cfg := NewLSPConfig()
	_ = cfg.RegisterServer("go", &ServerConfig{
		Command:             "gopls",
		ExtensionToLanguage: map[string]string{".go": "go"},
	})
	srv, name, ok := cfg.ForExtension(".go")
	if !ok {
		t.Fatal("expected to find .go")
	}
	if name != "go" || srv.Command != "gopls" {
		t.Fatalf("got server=%q name=%q, want gopls/go", srv.Command, name)
	}
}

func TestForExtension_WithoutDot(t *testing.T) {
	cfg := NewLSPConfig()
	_ = cfg.RegisterServer("go", &ServerConfig{
		Command:             "gopls",
		ExtensionToLanguage: map[string]string{".go": "go"},
	})
	srv, name, ok := cfg.ForExtension("go")
	if !ok {
		t.Fatal("expected to find when passing extension without dot")
	}
	if name != "go" {
		t.Fatalf("expected name go, got %s", name)
	}
	if srv.Command != "gopls" {
		t.Fatalf("expected command gopls, got %s", srv.Command)
	}
}

func TestForExtension_NotFound(t *testing.T) {
	cfg := NewLSPConfig()
	srv, name, ok := cfg.ForExtension(".xyz")
	if ok {
		t.Fatalf("expected not found, got server=%v name=%s", srv, name)
	}
}

func TestForExtension_CaseInsensitive(t *testing.T) {
	cfg := NewLSPConfig()
	_ = cfg.RegisterServer("go", &ServerConfig{
		Command:             "gopls",
		ExtensionToLanguage: map[string]string{".go": "go"},
	})
	srv, name, ok := cfg.ForExtension(".GO")
	if !ok {
		t.Fatal("expected case-insensitive match for .GO")
	}
	if name != "go" {
		t.Fatalf("expected name go, got %s", name)
	}
	if srv.Command != "gopls" {
		t.Fatalf("expected command gopls, got %s", srv.Command)
	}
}

// --- LoadLSPConfig tests ---

func TestLoadLSPConfig_FileNotFound(t *testing.T) {
	cfg, err := LoadLSPConfig("/nonexistent/path/lsp.json")
	if err != nil {
		t.Fatalf("expected nil error for missing file, got: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	// Should have defaults loaded
	if len(cfg.Servers) == 0 {
		t.Fatal("expected default servers when file not found")
	}
}

func TestLoadLSPConfig_ValidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lsp.json")
	data := `{
		"servers": {
			"go": {
				"command": "gopls",
				"args": ["-remote=auto"],
				"extensionToLanguage": {".go": "go"}
			},
			"python": {
				"command": "pyright",
				"args": ["--stdio"],
				"extensionToLanguage": {".py": "python"}
			}
		}
	}`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadLSPConfig(path)
	if err != nil {
		t.Fatalf("load error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	// Should have user servers plus defaults (non-overlapping)
	if _, ok := cfg.Servers["go"]; !ok {
		t.Fatal("expected go server")
	}
	// Default servers that don't conflict should also be present
	if _, ok := cfg.Servers["rust"]; !ok {
		t.Fatal("expected default rust server")
	}
}

func TestLoadLSPConfig_UserOverridesDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lsp.json")
	data := `{
		"servers": {
			"go": {
				"command": "my-gopls",
				"extensionToLanguage": {".go": "go"}
			}
		}
	}`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadLSPConfig(path)
	if err != nil {
		t.Fatalf("load error: %v", err)
	}
	srv, ok := cfg.Servers["go"]
	if !ok {
		t.Fatal("expected go server")
	}
	if srv.Command != "my-gopls" {
		t.Fatalf("expected 'my-gopls' command, got %q", srv.Command)
	}
}

func TestLoadLSPConfig_UserInvalidFallsBack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lsp.json")
	// User config for 'go' overrides with a server that has no extensionToLanguage
	data := `{
		"servers": {
			"go": {
				"command": "broken"
			}
		}
	}`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadLSPConfig(path)
	if err != nil {
		t.Fatalf("load error: %v", err)
	}
	srv, ok := cfg.Servers["go"]
	if !ok {
		t.Fatal("expected go server (should fall back to default)")
	}
	// Should have fallen back to default gopls
	if srv.Command != "gopls" {
		t.Fatalf("expected default 'gopls' fallback, got %q", srv.Command)
	}
}

func TestLoadLSPConfig_LegacyFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lsp.json")
	data := `{
		"languages": [
			{"name": "go", "command": "gopls", "extensions": [".go"]}
		]
	}`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadLSPConfig(path)
	if err != nil {
		t.Fatalf("load error: %v", err)
	}
	srv, ok := cfg.Servers["go"]
	if !ok {
		t.Fatal("expected go server from legacy format")
	}
	if srv.Command != "gopls" {
		t.Fatalf("expected gopls, got %q", srv.Command)
	}
}

func TestLoadLSPConfig_FlatFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lsp.json")
	data := `{
		"go": {
			"command": "gopls",
			"extensionToLanguage": {".go": "go"}
		}
	}`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadLSPConfig(path)
	if err != nil {
		t.Fatalf("load error: %v", err)
	}
	srv, ok := cfg.Servers["go"]
	if !ok {
		t.Fatal("expected go server from flat format")
	}
	if srv.Command != "gopls" {
		t.Fatalf("expected gopls, got %q", srv.Command)
	}
}

func TestLoadLSPConfig_MalformedJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lsp.json")
	if err := os.WriteFile(path, []byte("{broken"), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	_, err := LoadLSPConfig(path)
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
	if !strings.Contains(err.Error(), "parse lsp config") {
		t.Fatalf("expected parse error, got: %v", err)
	}
}

func TestLoadLSPConfig_FileError(t *testing.T) {
	_, err := LoadLSPConfig(`\invalid|\path\`)
	if err == nil {
		t.Fatal("expected error for invalid path")
	}
}

// --- WriteSampleConfig tests ---

func TestWriteSampleConfig_NewFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lsp.json")
	err := WriteSampleConfig(path)
	if err != nil {
		t.Fatalf("write sample config: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read sample config: %v", err)
	}
	if !json.Valid(data) {
		t.Fatal("sample config is not valid JSON")
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal sample config: %v", err)
	}
	srv, ok := parsed["servers"]
	if !ok {
		t.Fatal("expected 'servers' key in sample config")
	}
	srvMap, ok := srv.(map[string]any)
	if !ok || len(srvMap) == 0 {
		t.Fatal("expected non-empty servers in sample config")
	}
}

func TestWriteSampleConfig_Exists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lsp.json")
	if err := os.WriteFile(path, []byte("existing"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	err := WriteSampleConfig(path)
	if err == nil {
		t.Fatal("expected error when file already exists")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected 'already exists' error, got: %v", err)
	}
}

// --- DefaultConfigPath tests ---

func TestDefaultConfigPath(t *testing.T) {
	path := DefaultConfigPath("/home/user/.whale")
	if filepath.Base(path) != "lsp.json" {
		t.Fatalf("expected lsp.json, got: %s", filepath.Base(path))
	}
	expected := filepath.FromSlash("/home/user/.whale")
	if filepath.Dir(path) != expected {
		t.Fatalf("expected dir %s, got: %s", expected, filepath.Dir(path))
	}
}

func TestDefaultConfigPath_Windows(t *testing.T) {
	path := DefaultConfigPath(`C:\Users\test\.whale`)
	if filepath.Base(path) != "lsp.json" {
		t.Fatalf("expected lsp.json, got: %s", filepath.Base(path))
	}
}

// --- HasConfiguredServers ---

func TestHasConfiguredServers_Empty(t *testing.T) {
	cfg := NewLSPConfig()
	// No servers registered
	if cfg.HasConfiguredServers() {
		t.Fatal("expected false for empty config")
	}
}

func TestHasConfiguredServers_WithDefaults(t *testing.T) {
	// Load default config and check that servers are "configured" only if found
	cfg, err := LoadLSPConfig("/nonexistent")
	if err != nil {
		t.Fatalf("load error: %v", err)
	}
	// HasConfiguredServers runs FindServerForConfig, so it depends on PATH
	// Just check it doesn't panic and returns bool
	_ = cfg.HasConfiguredServers()
}

// --- legacyToServer test ---

func TestLegacyToServer(t *testing.T) {
	legacy := LegacyLanguage{
		Name:        "go",
		Command:     "gopls",
		Args:        []string{"-remote=auto"},
		Extensions:  []string{".go"},
		InstallHelp: "go install gopls",
	}
	srv := legacyToServer(legacy)
	if srv.Command != "gopls" {
		t.Fatalf("expected gopls, got %q", srv.Command)
	}
	if len(srv.ExtensionToLanguage) != 1 || srv.ExtensionToLanguage[".go"] != "go" {
		t.Fatalf("unexpected extension map: %v", srv.ExtensionToLanguage)
	}
	if srv.InstallHelp != "go install gopls" {
		t.Fatalf("expected install help, got %q", srv.InstallHelp)
	}
}

// --- parseUserServers tests ---

func TestParseUserServers_ServersField(t *testing.T) {
	raw := map[string]json.RawMessage{
		"servers": json.RawMessage(`{
			"mygo": {"command": "gopls", "extensionToLanguage": {".go": "go"}}
		}`),
	}
	result := parseUserServers(raw)
	if _, ok := result["mygo"]; !ok {
		t.Fatal("expected mygo server")
	}
}

func TestParseUserServers_LegacyLanguages(t *testing.T) {
	raw := map[string]json.RawMessage{
		"languages": json.RawMessage(`[
			{"name": "go", "command": "gopls", "extensions": [".go"]}
		]`),
	}
	result := parseUserServers(raw)
	if _, ok := result["go"]; !ok {
		t.Fatal("expected go server from legacy languages")
	}
}

func TestParseUserServers_Flat(t *testing.T) {
	raw := map[string]json.RawMessage{
		"go": json.RawMessage(`{"command": "gopls", "extensionToLanguage": {".go": "go"}}`),
	}
	result := parseUserServers(raw)
	if _, ok := result["go"]; !ok {
		t.Fatal("expected go server from flat format")
	}
}

func TestParseUserServers_Empty(t *testing.T) {
	result := parseUserServers(nil)
	if len(result) != 0 {
		t.Fatalf("expected empty result, got %d items", len(result))
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg := NewLSPConfig()
	loadDefaults(cfg)
	// Should have at least 8 default servers
	if len(cfg.Servers) < 8 {
		t.Fatalf("expected at least 8 default servers, got %d", len(cfg.Servers))
	}
	// Check specific defaults exist
	expected := []string{"go", "rust", "python", "typescript", "cpp", "html", "css", "json", "yaml", "vue"}
	for _, name := range expected {
		if _, ok := cfg.Servers[name]; !ok {
			t.Errorf("missing default server: %s", name)
		}
	}
	// Check Go extension mapping
	goSrv := cfg.Servers["go"]
	if goSrv.Command != "gopls" {
		t.Fatalf("expected gopls, got %q", goSrv.Command)
	}
	if goSrv.ExtensionToLanguage[".go"] != "go" {
		t.Fatalf("expected .go -> go, got %v", goSrv.ExtensionToLanguage)
	}
}
