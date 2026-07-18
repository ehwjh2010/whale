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

// TestIntegration_pyrightSmoke starts a real pyright-langserver and checks
// documentSymbol against a Python file. Requires pyright-langserver on PATH.
func TestIntegration_pyrightSmoke(t *testing.T) {
	if _, err := exec.LookPath("pyright-langserver"); err != nil {
		t.Skip("pyright-langserver not found on PATH")
	}

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "main.py"), "def hello():\n    return 42\n")

	mgr := NewManager(nil, dir)
	defer mgr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	mainPy := filepath.Join(dir, "main.py")
	client, err := mgr.ClientForFile(ctx, mainPy)
	if err != nil {
		t.Fatalf("ClientForFile: %v", err)
	}

	lspCtx, cancel2 := context.WithTimeout(ctx, 10*time.Second)
	defer cancel2()
	symbols, err := client.DocumentSymbols(lspCtx, PathToURI(mainPy))
	if err != nil {
		t.Fatalf("DocumentSymbols: %v", err)
	}
	found := false
	for _, s := range symbols {
		if s.Name == "hello" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected 'hello' symbol, got: %v", symbolNames(symbols))
	}
}

// TestIntegration_rustAnalyzerSmoke starts a real rust-analyzer and checks
// documentSymbol against a Rust file. Requires rust-analyzer on PATH (a
// rustup proxy stub is skipped by discovery, which this also exercises).
func TestIntegration_rustAnalyzerSmoke(t *testing.T) {
	if _, err := exec.LookPath("rust-analyzer"); err != nil {
		t.Skip("rust-analyzer not found on PATH")
	}

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "Cargo.toml"), "[package]\nname = \"example\"\nversion = \"0.1.0\"\nedition = \"2021\"\n")
	writeFile(t, filepath.Join(dir, "src", "main.rs"), "fn main() { println!(\"hello\"); }\n")

	mgr := NewManager(nil, dir)
	defer mgr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	mainRs := filepath.Join(dir, "src", "main.rs")
	client, err := mgr.ClientForFile(ctx, mainRs)
	if err != nil {
		t.Fatalf("ClientForFile: %v", err)
	}

	lspCtx, cancel2 := context.WithTimeout(ctx, 30*time.Second)
	defer cancel2()
	symbols, err := client.DocumentSymbols(lspCtx, PathToURI(mainRs))
	if err != nil {
		t.Fatalf("DocumentSymbols: %v", err)
	}
	found := false
	for _, s := range symbols {
		if s.Name == "main" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected 'main' symbol, got: %v", symbolNames(symbols))
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
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
