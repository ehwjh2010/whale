package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type staticOutlineProvider struct{ outline string }

func (p *staticOutlineProvider) SymbolOutline(_ context.Context, _ string) string {
	return p.outline
}

// A symbol outline must never demote a file that would otherwise be returned
// in full: when base content + outline exceed the result envelope, drop the
// outline and keep the full content.
func TestReadFileKeepsFullContentWhenOutlineOverflowsEnvelope(t *testing.T) {
	dir := t.TempDir()
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	// Base content close to (but under) the full-read/envelope limit; the
	// 8KB outline pushes the combined result over it.
	content := strings.Repeat("abcdefghijklmnopqrstuvwxyz789\n", 1000) // 30000 bytes
	if err := os.WriteFile(filepath.Join(dir, "big.go"), []byte(content), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	ts.SetSymbolOutlineProvider(&staticOutlineProvider{outline: strings.Repeat("- sym\n", 1400)})

	res, err := ts.readFile(context.Background(), tc("read_file", map[string]any{"file_path": "big.go"}))
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}
	if res.IsError() {
		t.Fatalf("read_file error: %s", res.ModelText)
	}
	if !strings.Contains(res.ModelText, `"mode":"full"`) && !strings.Contains(res.ModelText, `"mode": "full"`) {
		t.Fatalf("expected full-content read, got degraded result: %.300s", res.ModelText)
	}
}
