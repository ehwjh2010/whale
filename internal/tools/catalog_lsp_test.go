package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/usewhale/whale/internal/core"
	"github.com/usewhale/whale/internal/lsp"
)

// --- Mock Types ---

type mockClient struct {
	goToDefFn      func(ctx context.Context, uri string, line, character int) ([]lsp.Location, error)
	findRefsFn     func(ctx context.Context, uri string, line, character int, includeDeclaration bool) ([]lsp.Location, error)
	hoverFn        func(ctx context.Context, uri string, line, character int) (*lsp.HoverResult, error)
	docSymbolsFn   func(ctx context.Context, uri string) ([]lsp.DocumentSymbol, error)
	workspaceSymFn func(ctx context.Context, query string) ([]lsp.SymbolInformation, error)
}

func (m *mockClient) GoToDefinition(ctx context.Context, uri string, line, character int) ([]lsp.Location, error) {
	if m.goToDefFn != nil {
		return m.goToDefFn(ctx, uri, line, character)
	}
	return nil, errors.New("not implemented")
}

func (m *mockClient) FindReferences(ctx context.Context, uri string, line, character int, includeDeclaration bool) ([]lsp.Location, error) {
	if m.findRefsFn != nil {
		return m.findRefsFn(ctx, uri, line, character, includeDeclaration)
	}
	return nil, errors.New("not implemented")
}
func (m *mockClient) Hover(ctx context.Context, uri string, line, character int) (*lsp.HoverResult, error) {
	if m.hoverFn != nil {
		return m.hoverFn(ctx, uri, line, character)
	}
	return nil, errors.New("not implemented")
}

func (m *mockClient) DocumentSymbols(ctx context.Context, uri string) ([]lsp.DocumentSymbol, error) {
	if m.docSymbolsFn != nil {
		return m.docSymbolsFn(ctx, uri)
	}
	return nil, errors.New("not implemented")
}

func (m *mockClient) WorkspaceSymbols(ctx context.Context, query string) ([]lsp.SymbolInformation, error) {
	if m.workspaceSymFn != nil {
		return m.workspaceSymFn(ctx, query)
	}
	return nil, errors.New("not implemented")
}

func (m *mockClient) GoToImplementation(ctx context.Context, uri string, line, character int) ([]lsp.Location, error) {
	return nil, errors.New("not implemented")
}

func (m *mockClient) PrepareCallHierarchy(ctx context.Context, uri string, line, character int) ([]lsp.CallHierarchyItem, error) {
	return nil, errors.New("not implemented")
}

func (m *mockClient) IncomingCalls(ctx context.Context, item lsp.CallHierarchyItem) ([]lsp.CallHierarchyIncomingCall, error) {
	return nil, errors.New("not implemented")
}

func (m *mockClient) OutgoingCalls(ctx context.Context, item lsp.CallHierarchyItem) ([]lsp.CallHierarchyOutgoingCall, error) {
	return nil, errors.New("not implemented")
}

type mockProvider struct {
	readyFileFn          func(filePath string) (lspClient, string, bool)
	clientForFileQuickFn func(filePath string) (lspClient, string, error)
	readyLangsFn         func() []string
	clientForLangFn      func(ctx context.Context, langName string) (lspClient, error)
	clientForFileFn      func(ctx context.Context, filePath string) (lspClient, error)
}

func (m *mockProvider) readyClientForFile(filePath string) (lspClient, string, bool) {
	if m.readyFileFn != nil {
		return m.readyFileFn(filePath)
	}
	return nil, "mock", false
}

func (m *mockProvider) clientForFileQuick(filePath string) (lspClient, string, error) {
	if m.clientForFileQuickFn != nil {
		return m.clientForFileQuickFn(filePath)
	}
	// Default: delegate to readyClientForFile
	c, name, ready := m.readyClientForFile(filePath)
	if !ready {
		return nil, name, fmt.Errorf("language server %q is still starting up", name)
	}
	return c, name, nil
}

func (m *mockProvider) readyLanguages() []string {
	if m.readyLangsFn != nil {
		return m.readyLangsFn()
	}
	return nil
}

func (m *mockProvider) clientForLanguage(ctx context.Context, langName string) (lspClient, error) {
	if m.clientForLangFn != nil {
		return m.clientForLangFn(ctx, langName)
	}
	return nil, errors.New("not implemented")
}

func (m *mockProvider) clientForFile(ctx context.Context, filePath string) (lspClient, error) {
	if m.clientForFileFn != nil {
		return m.clientForFileFn(ctx, filePath)
	}
	return nil, errors.New("not implemented")
}

func toolsetWithProvider(t *testing.T, p lspToolProvider) *Toolset {
	t.Helper()
	dir := t.TempDir()
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	ts.lspOverride = p
	return ts
}

func lspTC(name string, in any) core.ToolCall {
	return tc(name, in)
}

func TestLSPTools_NilManager(t *testing.T) {
	dir := t.TempDir()
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	tools := ts.lspTools()
	if tools != nil {
		t.Fatalf("expected nil tools when no LSP manager, got %d tools", len(tools))
	}
}

func TestLSPTools_WithProvider(t *testing.T) {
	p := &mockProvider{}
	ts := toolsetWithProvider(t, p)
	tools := ts.lspTools()
	if len(tools) != 9 {
		t.Fatalf("expected 9 LSP tools, got %d", len(tools))
	}
	names := make(map[string]bool)
	for _, tool := range tools {
		names[tool.Name()] = true
	}
	expected := []string{"lsp_goto_definition", "lsp_find_references", "lsp_hover", "lsp_document_symbol", "lsp_workspace_symbol", "lsp_go_to_implementation", "lsp_prepare_call_hierarchy", "lsp_incoming_calls", "lsp_outgoing_calls"}
	for _, name := range expected {
		if !names[name] {
			t.Fatalf("missing tool: %s", name)
		}
	}
}

func TestLSPToolReadOnlyAndCapabilities(t *testing.T) {
	p := &mockProvider{}
	ts := toolsetWithProvider(t, p)
	tools := ts.lspTools()
	for _, tool := range tools {
		spec := core.DescribeTool(tool)
		if !spec.ReadOnly {
			t.Fatalf("tool %s should be read-only", tool.Name())
		}
		caps := spec.Capabilities
		if len(caps) != 1 || caps[0] != "lsp.read" {
			t.Fatalf("tool %s should have capability lsp.read, got %v", tool.Name(), caps)
		}
	}
}

func TestLSPFriendlyError_NoViews(t *testing.T) {
	err := errors.New("No views available")
	got := lspFriendlyError(err)
	want := "language server is still indexing the workspace; retry in a few seconds, or use grep + read_file as fallback"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestLSPFriendlyError_PipeClosed(t *testing.T) {
	err := errors.New("pipe closed")
	got := lspFriendlyError(err)
	if !strings.Contains(got, "connection lost") {
		t.Fatalf("expected connection lost message, got %q", got)
	}
}

func TestLSPFriendlyError_DeadlineExceeded(t *testing.T) {
	err := errors.New("deadline exceeded")
	got := lspFriendlyError(err)
	if !strings.Contains(got, "timed out") {
		t.Fatalf("expected timeout message, got %q", got)
	}
}

func TestLSPFriendlyError_Timeout(t *testing.T) {
	err := errors.New("request timeout")
	got := lspFriendlyError(err)
	if !strings.Contains(got, "timed out") {
		t.Fatalf("expected timeout message, got %q", got)
	}
}

func TestLSPFriendlyError_Cancelled(t *testing.T) {
	err := errors.New("context cancelled")
	got := lspFriendlyError(err)
	if !strings.Contains(got, "timed out") {
		t.Fatalf("expected timeout message for cancelled, got %q", got)
	}
}

func TestLSPFriendlyError_NotFound(t *testing.T) {
	err := errors.New("not found")
	got := lspFriendlyError(err)
	if got != err.Error() {
		t.Fatalf("expected error passthrough, got %q", got)
	}
}

func TestLSPFriendlyError_NotConfigured(t *testing.T) {
	err := errors.New("not configured")
	got := lspFriendlyError(err)
	if got != err.Error() {
		t.Fatalf("expected error passthrough, got %q", got)
	}
}

func TestLSPFriendlyError_Generic(t *testing.T) {
	err := errors.New("something unexpected happened")
	got := lspFriendlyError(err)
	if !strings.Contains(got, "LSP:") || !strings.Contains(got, "warming up") {
		t.Fatalf("expected generic LSP message, got %q", got)
	}
}

func TestLSPFriendlyError_CaseInsensitive(t *testing.T) {
	err := errors.New("PIPE CLOSED")
	got := lspFriendlyError(err)
	if !strings.Contains(got, "connection lost") {
		t.Fatalf("expected case-insensitive match, got %q", got)
	}
}

func TestFormatLSPResult_Locations(t *testing.T) {
	locs := []lsp.Location{
		{URI: "file:///D:/src/main.go", Range: lsp.Range{Start: lsp.Position{Line: 9, Character: 4}, End: lsp.Position{Line: 15, Character: 1}}},
	}
	result := formatLSPResult("definition", locs)
	if !strings.Contains(result, "main.go:10:5") {
		t.Fatalf("expected line:char in output, got: %s", result)
	}
	if !strings.Contains(result, "definition") {
		t.Fatalf("expected op name in output, got: %s", result)
	}
}

func TestFormatLSPResult_LocationsEmpty(t *testing.T) {
	result := formatLSPResult("references", []lsp.Location{})
	if result != "No results found." {
		t.Fatalf("expected 'No results found.', got: %s", result)
	}
}

func TestFormatLSPResult_LocationsTruncation(t *testing.T) {
	locs := make([]lsp.Location, 60)
	for i := range locs {
		locs[i] = lsp.Location{
			URI:   fmt.Sprintf("file:///D:/src/file%d.go", i),
			Range: lsp.Range{Start: lsp.Position{Line: i, Character: 0}},
		}
	}
	result := formatLSPResult("references", locs)
	if !strings.Contains(result, "... and 10 more") {
		t.Fatalf("expected truncation message, got: %s", result)
	}
}

func TestFormatLSPResult_Hover(t *testing.T) {
	hover := &lsp.HoverResult{
		Contents: lsp.HoverContents{
			Kind: "markdown", Value: "```go\nfunc main() {}\n```",
		},
	}
	result := formatLSPResult("hover", hover)
	if !strings.Contains(result, "## Hover") {
		t.Fatalf("expected hover header, got: %s", result)
	}
	if !strings.Contains(result, "func main()") {
		t.Fatalf("expected hover content, got: %s", result)
	}
}

func TestFormatLSPResult_HoverEmpty(t *testing.T) {
	hover := &lsp.HoverResult{Contents: lsp.HoverContents{Value: ""}}
	result := formatLSPResult("hover", hover)
	if result != "No hover information available." {
		t.Fatalf("expected no hover info, got: %s", result)
	}
}

func TestFormatLSPResult_DefaultFallback(t *testing.T) {
	result := formatLSPResult("unknown", 42)
	if !strings.Contains(result, "42") {
		t.Fatalf("expected JSON fallback, got: %s", result)
	}
	if !strings.Contains(result, "```json") {
		t.Fatalf("expected JSON block, got: %s", result)
	}
}

func TestWriteDocumentSymbols_Flat(t *testing.T) {
	symbols := []lsp.DocumentSymbol{
		{Name: "main", Kind: lsp.SymbolKindFunction, Range: lsp.Range{Start: lsp.Position{Line: 9}}},
		{Name: "Config", Kind: lsp.SymbolKindStruct, Detail: "type Config struct", Range: lsp.Range{Start: lsp.Position{Line: 24}}},
	}
	var md strings.Builder
	writeDocumentSymbols(&md, symbols, 0)
	result := md.String()
	if !strings.Contains(result, "main") {
		t.Fatalf("missing symbol name, got: %s", result)
	}
	if !strings.Contains(result, "Config") {
		t.Fatalf("missing symbol with detail, got: %s", result)
	}
	if !strings.Contains(result, "function") || !strings.Contains(result, "struct") {
		t.Fatalf("missing symbol kind, got: %s", result)
	}
	if !strings.Contains(result, "line 10") {
		t.Fatalf("expected 1-based line output, got: %s", result)
	}
}

func TestWriteDocumentSymbols_Hierarchical(t *testing.T) {
	symbols := []lsp.DocumentSymbol{{
		Name: "Outer", Kind: lsp.SymbolKindNamespace, Range: lsp.Range{Start: lsp.Position{Line: 0}},
		Children: []lsp.DocumentSymbol{{
			Name: "Inner", Kind: lsp.SymbolKindFunction, Range: lsp.Range{Start: lsp.Position{Line: 1}},
			Children: []lsp.DocumentSymbol{{
				Name: "Deepest", Kind: lsp.SymbolKindVariable, Range: lsp.Range{Start: lsp.Position{Line: 2}},
			}},
		}},
	}}
	var md strings.Builder
	writeDocumentSymbols(&md, symbols, 0)
	result := md.String()
	if !strings.Contains(result, "- **Outer**") {
		t.Fatalf("missing top-level symbol, got: %s", result)
	}
	if !strings.Contains(result, "  - **Inner**") {
		t.Fatalf("missing indented symbol, got: %s", result)
	}
	if !strings.Contains(result, "    - **Deepest**") {
		t.Fatalf("missing deeply indented symbol, got: %s", result)
	}
}

func TestWriteDocumentSymbols_Empty(t *testing.T) {
	var md strings.Builder
	writeDocumentSymbols(&md, nil, 0)
	if md.Len() != 0 {
		t.Fatalf("expected empty output, got: %s", md.String())
	}
}

func TestLSPPositionParams_Schema(t *testing.T) {
	params := lspPositionParams("test description")
	if params["type"] != "object" {
		t.Fatalf("expected type object")
	}
	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("missing properties")
	}
	for _, key := range []string{"file_path", "line", "character"} {
		if _, ok := props[key]; !ok {
			t.Fatalf("missing property: %s", key)
		}
	}
}

func TestLSPReferencesParams_IncludesDeclaration(t *testing.T) {
	params := lspReferencesParams()
	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("missing properties")
	}
	decl, ok := props["include_declaration"]
	if !ok {
		t.Fatalf("missing include_declaration")
	}
	declMap := decl.(map[string]any)
	if declMap["type"] != "boolean" {
		t.Fatalf("include_declaration should be boolean")
	}
}

func TestLSPGoToDefinition_NoProvider(t *testing.T) {
	dir := t.TempDir()
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	res, _ := ts.lspGoToDefinition(context.Background(), lspTC("lsp_goto_definition", map[string]any{
		"file_path": "test.go", "line": 0, "character": 0,
	}))
	if !res.IsError() {
		t.Fatalf("expected error when no provider, got: %+v", res)
	}
	if !strings.Contains(res.ModelText, "no language servers configured") {
		t.Fatalf("expected no servers message, got: %s", res.ModelText)
	}
}

func TestLSPGoToDefinition_NotReady(t *testing.T) {
	p := &mockProvider{
		readyFileFn: func(filePath string) (lspClient, string, bool) {
			return nil, "gopls", false
		},
	}
	ts := toolsetWithProvider(t, p)
	res, _ := ts.lspGoToDefinition(context.Background(), lspTC("lsp_goto_definition", map[string]any{
		"file_path": "test.go", "line": 0, "character": 0,
	}))
	if !res.IsError() {
		t.Fatalf("expected error when not ready, got: %+v", res)
	}
	if !strings.Contains(res.ModelText, "gopls") {
		t.Fatalf("expected server name in error, got: %s", res.ModelText)
	}
}

func TestLSPGoToDefinition_InvalidJSON(t *testing.T) {
	p := &mockProvider{}
	ts := toolsetWithProvider(t, p)
	res, _ := ts.lspGoToDefinition(context.Background(), core.ToolCall{ID: "c1", Name: "lsp_goto_definition", Input: "not-json"})
	if !res.IsError() {
		t.Fatalf("expected error for invalid JSON, got: %+v", res)
	}
}

func TestLSPGoToDefinition_NegativeLine(t *testing.T) {
	p := &mockProvider{}
	ts := toolsetWithProvider(t, p)
	res, _ := ts.lspGoToDefinition(context.Background(), lspTC("lsp_goto_definition", map[string]any{
		"file_path": "test.go", "line": -1, "character": 0,
	}))
	if !res.IsError() {
		t.Fatalf("expected error for negative line, got: %+v", res)
	}
	if !strings.Contains(res.ModelText, "line must be >= 0") {
		t.Fatalf("expected line validation error, got: %s", res.ModelText)
	}
}

func TestLSPGoToDefinition_NegativeCharacter(t *testing.T) {
	p := &mockProvider{}
	ts := toolsetWithProvider(t, p)
	res, _ := ts.lspGoToDefinition(context.Background(), lspTC("lsp_goto_definition", map[string]any{
		"file_path": "test.go", "line": 0, "character": -1,
	}))
	if !res.IsError() {
		t.Fatalf("expected error for negative character, got: %+v", res)
	}
	if !strings.Contains(res.ModelText, "character must be >= 0") {
		t.Fatalf("expected character validation error, got: %s", res.ModelText)
	}
}

func TestLSPGoToDefinition_PathEscapesWorkspace(t *testing.T) {
	p := &mockProvider{
		readyFileFn: func(filePath string) (lspClient, string, bool) {
			t.Fatalf("should not reach provider after path validation")
			return nil, "", false
		},
	}
	ts := toolsetWithProvider(t, p)
	res, _ := ts.lspGoToDefinition(context.Background(), lspTC("lsp_goto_definition", map[string]any{
		"file_path": "..\\..\\etc\\passwd", "line": 0, "character": 0,
	}))
	if !res.IsError() {
		t.Fatalf("expected permission denied, got: %+v", res)
	}
	if !strings.Contains(res.ModelText, "permission_denied") {
		t.Fatalf("expected permission_denied, got: %s", res.ModelText)
	}
}

func TestLSPGoToDefinition_LSPCallFails(t *testing.T) {
	mc := &mockClient{
		goToDefFn: func(ctx context.Context, uri string, line, character int) ([]lsp.Location, error) {
			return nil, errors.New("no views")
		},
	}
	p := &mockProvider{
		readyFileFn: func(filePath string) (lspClient, string, bool) {
			return mc, "mock", true
		},
	}
	ts := toolsetWithProvider(t, p)
	res, _ := ts.lspGoToDefinition(context.Background(), lspTC("lsp_goto_definition", map[string]any{
		"file_path": "test.go", "line": 0, "character": 0,
	}))
	if !res.IsError() {
		t.Fatalf("expected error when LSP call fails, got: %+v", res)
	}
	if !strings.Contains(res.ModelText, "indexing") {
		t.Fatalf("expected friendly error about indexing, got: %s", res.ModelText)
	}
}

func TestLSPGoToDefinition_Success(t *testing.T) {
	mc := &mockClient{
		goToDefFn: func(ctx context.Context, uri string, line, character int) ([]lsp.Location, error) {
			return []lsp.Location{
				{URI: "file:///D:/src/main.go", Range: lsp.Range{Start: lsp.Position{Line: 9, Character: 4}}},
			}, nil
		},
	}
	p := &mockProvider{
		readyFileFn: func(filePath string) (lspClient, string, bool) {
			return mc, "mock", true
		},
	}
	ts := toolsetWithProvider(t, p)
	res, _ := ts.lspGoToDefinition(context.Background(), lspTC("lsp_goto_definition", map[string]any{
		"file_path": "test.go", "line": 0, "character": 0,
	}))
	if res.IsError() {
		t.Fatalf("unexpected error: %s", res.ModelText)
	}
	if !strings.Contains(res.ModelText, "definition") {
		t.Fatalf("expected definition in output, got: %s", res.ModelText)
	}
	if _, ok := res.Metadata["raw"]; !ok {
		t.Fatalf("expected raw result in metadata")
	}
}

func TestRunLSPPositionOp_ZeroResults(t *testing.T) {
	mc := &mockClient{
		goToDefFn: func(ctx context.Context, uri string, line, character int) ([]lsp.Location, error) {
			return []lsp.Location{}, nil
		},
	}
	p := &mockProvider{
		readyFileFn: func(filePath string) (lspClient, string, bool) {
			return mc, "mock", true
		},
	}
	ts := toolsetWithProvider(t, p)
	res, _ := ts.lspGoToDefinition(context.Background(), lspTC("lsp_goto_definition", map[string]any{
		"file_path": "test.go", "line": 0, "character": 0,
	}))
	if res.IsError() {
		t.Fatalf("unexpected error for zero results: %s", res.ModelText)
	}
	if !strings.Contains(res.ModelText, "No results found") {
		t.Fatalf("expected 'No results found', got: %s", res.ModelText)
	}
}

func TestLSPFindReferences_Success(t *testing.T) {
	mc := &mockClient{
		findRefsFn: func(ctx context.Context, uri string, line, character int, includeDeclaration bool) ([]lsp.Location, error) {
			return []lsp.Location{
				{URI: "file:///D:/src/main.go", Range: lsp.Range{Start: lsp.Position{Line: 5, Character: 2}}},
				{URI: "file:///D:/src/util.go", Range: lsp.Range{Start: lsp.Position{Line: 12, Character: 0}}},
			}, nil
		},
	}
	p := &mockProvider{
		readyFileFn: func(filePath string) (lspClient, string, bool) {
			return mc, "mock", true
		},
	}
	ts := toolsetWithProvider(t, p)
	res, _ := ts.lspFindReferences(context.Background(), lspTC("lsp_find_references", map[string]any{
		"file_path": "test.go", "line": 0, "character": 0,
	}))
	if res.IsError() {
		t.Fatalf("unexpected error: %s", res.ModelText)
	}
	if !strings.Contains(res.ModelText, "references") {
		t.Fatalf("expected references in output, got: %s", res.ModelText)
	}
	if !strings.Contains(res.ModelText, "main.go") {
		t.Fatalf("expected main.go in output, got: %s", res.ModelText)
	}
}

func TestLSPFindReferences_NoProvider(t *testing.T) {
	dir := t.TempDir()
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	res, _ := ts.lspFindReferences(context.Background(), lspTC("lsp_find_references", map[string]any{
		"file_path": "test.go", "line": 0, "character": 0,
	}))
	if !res.IsError() {
		t.Fatalf("expected error when no provider, got: %+v", res)
	}
}

func TestLSPFindReferences_InvalidJSON(t *testing.T) {
	p := &mockProvider{}
	ts := toolsetWithProvider(t, p)
	res, _ := ts.lspFindReferences(context.Background(), core.ToolCall{ID: "c1", Name: "lsp_find_references", Input: "bad json"})
	if !res.IsError() {
		t.Fatalf("expected error for invalid JSON, got: %+v", res)
	}
}

func TestLSPHover_Success(t *testing.T) {
	mc := &mockClient{
		hoverFn: func(ctx context.Context, uri string, line, character int) (*lsp.HoverResult, error) {
			return &lsp.HoverResult{
				Contents: lsp.HoverContents{Kind: "markdown", Value: "**string** - underlying type"},
			}, nil
		},
	}
	p := &mockProvider{
		readyFileFn: func(filePath string) (lspClient, string, bool) {
			return mc, "mock", true
		},
	}
	ts := toolsetWithProvider(t, p)
	res, _ := ts.lspHover(context.Background(), lspTC("lsp_hover", map[string]any{
		"file_path": "test.go", "line": 0, "character": 4,
	}))
	if res.IsError() {
		t.Fatalf("unexpected error: %s", res.ModelText)
	}
	if !strings.Contains(res.ModelText, "string") {
		t.Fatalf("expected hover content, got: %s", res.ModelText)
	}
}

func TestLSPHover_NoProvider(t *testing.T) {
	dir := t.TempDir()
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	res, _ := ts.lspHover(context.Background(), lspTC("lsp_hover", map[string]any{
		"file_path": "test.go", "line": 0, "character": 0,
	}))
	if !res.IsError() {
		t.Fatalf("expected error when no provider, got: %+v", res)
	}
}

func TestLSPHover_NegativeLine(t *testing.T) {
	p := &mockProvider{}
	ts := toolsetWithProvider(t, p)
	res, _ := ts.lspHover(context.Background(), lspTC("lsp_hover", map[string]any{
		"file_path": "test.go", "line": -1, "character": 0,
	}))
	if !res.IsError() {
		t.Fatalf("expected error for negative line, got: %+v", res)
	}
}

func TestLSPDocumentSymbol_Success(t *testing.T) {
	mc := &mockClient{
		docSymbolsFn: func(ctx context.Context, uri string) ([]lsp.DocumentSymbol, error) {
			return []lsp.DocumentSymbol{
				{Name: "main", Kind: lsp.SymbolKindFunction, Range: lsp.Range{Start: lsp.Position{Line: 9}}},
			}, nil
		},
	}
	p := &mockProvider{
		readyFileFn: func(filePath string) (lspClient, string, bool) {
			return mc, "mock", true
		},
	}
	ts := toolsetWithProvider(t, p)
	res, _ := ts.lspDocumentSymbol(context.Background(), lspTC("lsp_document_symbol", map[string]any{
		"file_path": "test.go",
	}))
	if res.IsError() {
		t.Fatalf("unexpected error: %s", res.ModelText)
	}
	if !strings.Contains(res.ModelText, "main") {
		t.Fatalf("expected main symbol, got: %s", res.ModelText)
	}
	if !strings.Contains(res.ModelText, "1 top-level symbol") {
		t.Fatalf("expected symbol count, got: %s", res.ModelText)
	}
}

func TestLSPDocumentSymbol_NoProvider(t *testing.T) {
	dir := t.TempDir()
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	res, _ := ts.lspDocumentSymbol(context.Background(), lspTC("lsp_document_symbol", map[string]any{
		"file_path": "test.go",
	}))
	if !res.IsError() {
		t.Fatalf("expected error when no provider, got: %+v", res)
	}
}

func TestLSPDocumentSymbol_NotReady(t *testing.T) {
	p := &mockProvider{
		readyFileFn: func(filePath string) (lspClient, string, bool) {
			return nil, "gopls", false
		},
	}
	ts := toolsetWithProvider(t, p)
	res, _ := ts.lspDocumentSymbol(context.Background(), lspTC("lsp_document_symbol", map[string]any{
		"file_path": "test.go",
	}))
	if !res.IsError() {
		t.Fatalf("expected error when not ready, got: %+v", res)
	}
}

func TestLSPDocumentSymbol_InvalidJSON(t *testing.T) {
	p := &mockProvider{}
	ts := toolsetWithProvider(t, p)
	res, _ := ts.lspDocumentSymbol(context.Background(), core.ToolCall{ID: "c1", Name: "lsp_document_symbol", Input: "bad"})
	if !res.IsError() {
		t.Fatalf("expected error for invalid JSON, got: %+v", res)
	}
}

func TestLSPDocumentSymbol_LSPCallFails(t *testing.T) {
	mc := &mockClient{
		docSymbolsFn: func(ctx context.Context, uri string) ([]lsp.DocumentSymbol, error) {
			return nil, errors.New("pipe closed")
		},
	}
	p := &mockProvider{
		readyFileFn: func(filePath string) (lspClient, string, bool) {
			return mc, "mock", true
		},
	}
	ts := toolsetWithProvider(t, p)
	res, _ := ts.lspDocumentSymbol(context.Background(), lspTC("lsp_document_symbol", map[string]any{
		"file_path": "test.go",
	}))
	if !res.IsError() {
		t.Fatalf("expected error when LSP fails, got: %+v", res)
	}
}

func TestLSPWorkspaceSymbol_Success(t *testing.T) {
	mc := &mockClient{
		workspaceSymFn: func(ctx context.Context, query string) ([]lsp.SymbolInformation, error) {
			return []lsp.SymbolInformation{{
				Name: "HelloWorld", Kind: lsp.SymbolKindFunction,
				Location:      lsp.Location{URI: "file:///D:/src/hello.go", Range: lsp.Range{Start: lsp.Position{Line: 3, Character: 5}}},
				ContainerName: "main",
			}}, nil
		},
	}
	p := &mockProvider{
		readyLangsFn: func() []string { return []string{"go"} },
		clientForLangFn: func(ctx context.Context, langName string) (lspClient, error) {
			return mc, nil
		},
	}
	ts := toolsetWithProvider(t, p)
	res, _ := ts.lspWorkspaceSymbol(context.Background(), lspTC("lsp_workspace_symbol", map[string]any{
		"query": "Hello",
	}))
	if res.IsError() {
		t.Fatalf("unexpected error: %s", res.ModelText)
	}
	if !strings.Contains(res.ModelText, "HelloWorld") {
		t.Fatalf("expected HelloWorld in output, got: %s", res.ModelText)
	}
	if !strings.Contains(res.ModelText, "main") {
		t.Fatalf("expected container name, got: %s", res.ModelText)
	}
}

func TestLSPWorkspaceSymbol_NoProvider(t *testing.T) {
	dir := t.TempDir()
	ts, err := NewToolset(dir)
	if err != nil {
		t.Fatalf("new toolset: %v", err)
	}
	res, _ := ts.lspWorkspaceSymbol(context.Background(), lspTC("lsp_workspace_symbol", map[string]any{
		"query": "test",
	}))
	if !res.IsError() {
		t.Fatalf("expected error when no provider, got: %+v", res)
	}
}

func TestLSPWorkspaceSymbol_EmptyQuery(t *testing.T) {
	p := &mockProvider{}
	ts := toolsetWithProvider(t, p)
	res, _ := ts.lspWorkspaceSymbol(context.Background(), lspTC("lsp_workspace_symbol", map[string]any{
		"query": "",
	}))
	if !res.IsError() {
		t.Fatalf("expected error for empty query, got: %+v", res)
	}
	if !strings.Contains(res.ModelText, "query is required") {
		t.Fatalf("expected query required error, got: %s", res.ModelText)
	}
}

func TestLSPWorkspaceSymbol_WhitespaceQuery(t *testing.T) {
	p := &mockProvider{}
	ts := toolsetWithProvider(t, p)
	res, _ := ts.lspWorkspaceSymbol(context.Background(), lspTC("lsp_workspace_symbol", map[string]any{
		"query": "   ",
	}))
	if !res.IsError() {
		t.Fatalf("expected error for whitespace query, got: %+v", res)
	}
}

func TestLSPWorkspaceSymbol_NoResults(t *testing.T) {
	mc := &mockClient{
		workspaceSymFn: func(ctx context.Context, query string) ([]lsp.SymbolInformation, error) {
			return nil, nil
		},
	}
	p := &mockProvider{
		readyLangsFn: func() []string { return []string{"go"} },
		clientForLangFn: func(ctx context.Context, langName string) (lspClient, error) {
			return mc, nil
		},
	}
	ts := toolsetWithProvider(t, p)
	res, _ := ts.lspWorkspaceSymbol(context.Background(), lspTC("lsp_workspace_symbol", map[string]any{
		"query": "nonexistent",
	}))
	if res.IsError() {
		t.Fatalf("unexpected error for no results: %s", res.ModelText)
	}
	if !strings.Contains(res.ModelText, "No symbols found") {
		t.Fatalf("expected 'No symbols found', got: %s", res.ModelText)
	}
}

func TestLSPWorkspaceSymbol_ClientError(t *testing.T) {
	p := &mockProvider{
		readyLangsFn: func() []string { return []string{"go"} },
		clientForLangFn: func(ctx context.Context, langName string) (lspClient, error) {
			return nil, errors.New("server not found")
		},
	}
	ts := toolsetWithProvider(t, p)
	res, _ := ts.lspWorkspaceSymbol(context.Background(), lspTC("lsp_workspace_symbol", map[string]any{
		"query": "test",
	}))
	if res.IsError() {
		t.Fatalf("unexpected error: %s", res.ModelText)
	}
	if !strings.Contains(res.ModelText, "Server status") {
		t.Fatalf("expected server status in output, got: %s", res.ModelText)
	}
}

func TestLSPWorkspaceSymbol_MultipleLanguages(t *testing.T) {
	goClient := &mockClient{
		workspaceSymFn: func(ctx context.Context, query string) ([]lsp.SymbolInformation, error) {
			return []lsp.SymbolInformation{{Name: "GoFunc", Kind: lsp.SymbolKindFunction, Location: lsp.Location{URI: "file:///D:/src/a.go", Range: lsp.Range{Start: lsp.Position{Line: 1}}}}}, nil
		},
	}
	pyClient := &mockClient{
		workspaceSymFn: func(ctx context.Context, query string) ([]lsp.SymbolInformation, error) {
			return []lsp.SymbolInformation{{Name: "PyFunc", Kind: lsp.SymbolKindFunction, Location: lsp.Location{URI: "file:///D:/src/b.py", Range: lsp.Range{Start: lsp.Position{Line: 2}}}}}, nil
		},
	}
	var callOrder []string
	p := &mockProvider{
		readyLangsFn: func() []string { return []string{"go", "python"} },
		clientForLangFn: func(ctx context.Context, langName string) (lspClient, error) {
			callOrder = append(callOrder, langName)
			switch langName {
			case "go":
				return goClient, nil
			case "python":
				return pyClient, nil
			}
			return nil, errors.New("unknown")
		},
	}
	ts := toolsetWithProvider(t, p)
	res, _ := ts.lspWorkspaceSymbol(context.Background(), lspTC("lsp_workspace_symbol", map[string]any{
		"query": "Func",
	}))
	if res.IsError() {
		t.Fatalf("unexpected error: %s", res.ModelText)
	}
	if len(callOrder) != 2 {
		t.Fatalf("expected both languages queried, got %d calls: %v", len(callOrder), callOrder)
	}
	if !strings.Contains(res.ModelText, "GoFunc") || !strings.Contains(res.ModelText, "PyFunc") {
		t.Fatalf("expected both results, got: %s", res.ModelText)
	}
}

func TestLSPWorkspaceSymbol_Truncation(t *testing.T) {
	mc := &mockClient{
		workspaceSymFn: func(ctx context.Context, query string) ([]lsp.SymbolInformation, error) {
			syms := make([]lsp.SymbolInformation, 60)
			for i := range syms {
				syms[i] = lsp.SymbolInformation{
					Name: fmt.Sprintf("Sym%d", i), Kind: lsp.SymbolKindVariable,
					Location: lsp.Location{
						URI:   fmt.Sprintf("file:///D:/src/file%d.go", i),
						Range: lsp.Range{Start: lsp.Position{Line: i}},
					},
				}
			}
			return syms, nil
		},
	}
	p := &mockProvider{
		readyLangsFn: func() []string { return []string{"go"} },
		clientForLangFn: func(ctx context.Context, langName string) (lspClient, error) {
			return mc, nil
		},
	}
	ts := toolsetWithProvider(t, p)
	res, _ := ts.lspWorkspaceSymbol(context.Background(), lspTC("lsp_workspace_symbol", map[string]any{
		"query": "Sym",
	}))
	if res.IsError() {
		t.Fatalf("unexpected error: %s", res.ModelText)
	}
	if !strings.Contains(res.ModelText, "... and 10 more") {
		t.Fatalf("expected truncation for 60 results, got: %s", res.ModelText)
	}
}
