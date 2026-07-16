package lsp

import (
	"context"
	"testing"
	"time"
)

func TestRustAnalyzerPerfOnAstEditMcp(t *testing.T) {
	rsFile := ` + '"D:\\src\\ast-edit-mcp\\src\\main.rs"' + @`
	wsDir := ` + '"D:\\src\\ast-edit-mcp"' + @`

	cfg, err := LoadLSPConfig("/nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(cfg, wsDir)

	t0 := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	client, err := m.ClientForFile(ctx, rsFile)
	startupTime := time.Since(t0)
	cancel()
	if err != nil {
		if client != nil {
			client.Close()
		}
		t.Skipf("rust-analyzer not available: %v", err)
	}
	defer client.Close()
	t.Logf("rust-analyzer startup + init handshake: %v", startupTime)

	uri := PathToURI(rsFile)

	t0 = time.Now()
	ctx2, cancel2 := context.WithTimeout(context.Background(), 15*time.Second)
	symbols, err := client.DocumentSymbols(ctx2, uri)
	cancel2()
	opTime := time.Since(t0)
	if err != nil {
		t.Logf("DocumentSymbols: %v (took %v)", err, opTime)
	} else {
		t.Logf("DocumentSymbols: %d symbols in %v", len(symbols), opTime)
	}

	t0 = time.Now()
	ctx3, cancel3 := context.WithTimeout(context.Background(), 15*time.Second)
	hover, err := client.Hover(ctx3, uri, 0, 4)
	cancel3()
	opTime = time.Since(t0)
	if err != nil {
		t.Logf("Hover: %v (took %v)", err, opTime)
	} else if hover != nil {
		t.Logf("Hover returned (kind=%q, %d chars) in %v", hover.Contents.Kind, len(hover.Contents.Value), opTime)
	}

	t0 = time.Now()
	ctx4, cancel4 := context.WithTimeout(context.Background(), 15*time.Second)
	locs, err := client.GoToDefinition(ctx4, uri, 0, 4)
	cancel4()
	opTime = time.Since(t0)
	if err != nil {
		t.Logf("GoToDefinition: %v (took %v)", err, opTime)
	} else {
		t.Logf("GoToDefinition: %d locations in %v", len(locs), opTime)
	}

	t.Logf("--- Timing Summary ---")
}
