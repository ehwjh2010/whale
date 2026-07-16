package lsp

import (
	"testing"
	"time"
)

func TestFindServerForRust(t *testing.T) {
	cfg, err := LoadLSPConfig("/nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	srv, ok := cfg.Servers["rust"]
	if !ok {
		t.Skip("rust server not in default config")
	}

	t0 := time.Now()
	_, _, found := FindServerForConfig(srv)
	t.Logf("FindServerForConfig: %v, found=%v", time.Since(t0), found)

	// Benchmark vscode extension scanning separately
	t0 = time.Now()
	vscodeExtensionServer(srv)
	t.Logf("vscodeExtensionServer: %v", time.Since(t0))
}
