//go:build integration

package lsp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestIntegration_goplsSmoke starts a real gopls server and exercises the
// initialize → documentSymbols → shutdown lifecycle. Requires gopls on PATH.
func TestIntegration_goplsSmoke(t *testing.T) {
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls not found on PATH")
	}

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example\n\ngo 1.21\n")
	writeFile(t, filepath.Join(dir, "main.go"), "package main\n\nfunc main() { println(\"hello\") }\n")

	cfg := NewLSPConfig()
	loadDefaults(cfg)
	goSrv, _, ok := cfg.ForExtension(".go")
	if !ok {
		t.Fatal("go server not in defaults")
	}

	mgr := NewManager(cfg, dir)
	defer mgr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	mainGo := filepath.Join(dir, "main.go")
	client, err := mgr.ClientForFile(ctx, mainGo)
	if err != nil {
		t.Fatalf("ClientForFile: %v", err)
	}

	lspCtx, cancel2 := context.WithTimeout(ctx, 10*time.Second)
	defer cancel2()

	symbols, err := client.DocumentSymbols(lspCtx, PathToURI(mainGo))
	if err != nil {
		t.Fatalf("DocumentSymbols: %v", err)
	}

	found := false
	for _, s := range symbols {
		if s.Name == "main" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected 'main' symbol, got %d symbols: %v", len(symbols), symbolNames(symbols))
	}

	// Clean shutdown
	_ = goSrv
	if err := client.Close(); err != nil {
		t.Logf("close: %v", err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func symbolNames(symbols []DocumentSymbol) []string {
	var names []string
	for _, s := range symbols {
		names = append(names, s.Name)
		for _, c := range s.Children {
			names = append(names, c.Name)
		}
	}
	return names
}
