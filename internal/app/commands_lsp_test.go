package app

import (
	"strings"
	"testing"
)

func TestLSPDisabledByDefault(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.LSPEnabled {
		t.Fatal("LSP must be off by default")
	}
}

func TestApplyFileConfigSupportsLSPEnabled(t *testing.T) {
	cfg := DefaultConfig()
	enabled := true
	if err := ApplyFileConfig(&cfg, FileConfig{Lsp: FileLSPConfig{Enabled: &enabled}}); err != nil {
		t.Fatalf("ApplyFileConfig: %v", err)
	}
	if !cfg.LSPEnabled {
		t.Fatal("lsp.enabled=true must enable LSP")
	}
	disabled := false
	if err := ApplyFileConfig(&cfg, FileConfig{Lsp: FileLSPConfig{Enabled: &disabled}}); err != nil {
		t.Fatalf("ApplyFileConfig: %v", err)
	}
	if cfg.LSPEnabled {
		t.Fatal("lsp.enabled=false must disable LSP")
	}
}

func TestLSPStatusInfoWhenDisabled(t *testing.T) {
	a := &App{cfg: DefaultConfig()}
	got := a.LSPStatusInfo()
	if !strings.Contains(got, "off") {
		t.Fatalf("status must report off, got: %q", got)
	}
	if !strings.Contains(got, "/lsp on") {
		t.Fatalf("status must tell the user how to enable, got: %q", got)
	}
}

func TestExecuteLSPCommandUsageError(t *testing.T) {
	a := &App{cfg: DefaultConfig()}
	_, err := a.executeLSPCommand("/lsp bogus")
	if err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("expected usage error, got: %v", err)
	}
}

func TestExecuteLSPCommandStatus(t *testing.T) {
	a := &App{cfg: DefaultConfig()}
	res, err := a.executeLSPCommand("/lsp")
	if err != nil {
		t.Fatalf("executeLSPCommand: %v", err)
	}
	if !res.Handled || !strings.Contains(res.Text, "LSP: off") {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestSetLSPEnabledPersistWritesConfig(t *testing.T) {
	dataDir := t.TempDir()
	cfg := DefaultConfig()
	cfg.DataDir = dataDir
	a := &App{cfg: cfg}

	msg, err := a.SetLSPEnabledPersist(true)
	if err != nil {
		t.Fatalf("SetLSPEnabledPersist: %v", err)
	}
	if !a.cfg.LSPEnabled {
		t.Fatal("session config must reflect the toggle")
	}
	if !strings.Contains(msg, "restart") {
		t.Fatalf("without a live manager the message must mention restart, got: %q", msg)
	}
	file, exists, err := LoadConfigFile(GlobalConfigPath(dataDir))
	if err != nil || !exists {
		t.Fatalf("config file not written: exists=%v err=%v", exists, err)
	}
	if file.Lsp.Enabled == nil || !*file.Lsp.Enabled {
		t.Fatalf("lsp.enabled must be persisted as true, got %+v", file.Lsp)
	}

	if _, err := a.SetLSPEnabledPersist(false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	file, _, _ = LoadConfigFile(GlobalConfigPath(dataDir))
	if file.Lsp.Enabled == nil || *file.Lsp.Enabled {
		t.Fatalf("lsp.enabled must be persisted as false, got %+v", file.Lsp)
	}
}
