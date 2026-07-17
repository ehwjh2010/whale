package tools

// LSP (Language Server Protocol) tools provide code intelligence via
// language servers. Nine tools are registered when an LSP manager is
// available:
//
//  lsp_goto_definition        — jump to the definition of a symbol
//  lsp_find_references        — find all usages of a symbol across the workspace
//  lsp_hover                  — get type info and documentation at a position
//  lsp_document_symbol        — list all symbols in a file (functions, types, vars)
//  lsp_workspace_symbol       — search for a symbol by name across all files
//  lsp_go_to_implementation   — find implementations of an interface/method
//  lsp_prepare_call_hierarchy — prepare call hierarchy at a position
//  lsp_incoming_calls         — find all callers of a function
//  lsp_outgoing_calls         — find all functions called by a function
//
// All nine tools require the "lsp.read" capability and are read-only.
//
// # Error handling
//
// When a language server is still indexing or unavailable, the tools return
// an "lsp_not_ready" error with a suggestion to retry or fall back to grep
// + read_file. Server crashes produce "lsp_call_failed" errors with
// friendly messages (lspFriendlyError) that tell the model whether to
// retry or switch to grep.
//
// # Configuration
//
// LSP is configured via an lsp.json file in the data directory. See the
// internal/lsp package for the config schema. When no config is present,
// built-in defaults for common languages are used if the corresponding
// server binary is installed.
//
// # Mock interfaces
//
// lspClient and lspToolProvider are interfaces that abstract the LSP
// subsystem for testing. Toolset.lspOverride allows injecting a mock
// provider in tests without starting real language servers.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/usewhale/whale/internal/core"
	"github.com/usewhale/whale/internal/lsp"
)

// lspClient abstracts the subset of *lsp.Client used by tool handlers.
// lspRequestTimeout bounds every LSP request issued by tool handlers. A
// server that completed the handshake can still queue requests while
// indexing; without a deadline the tool call hangs indefinitely.
const lspRequestTimeout = 10 * time.Second

type lspClient interface {
	GoToDefinition(ctx context.Context, uri string, line, character int) ([]lsp.Location, error)
	FindReferences(ctx context.Context, uri string, line, character int, includeDeclaration bool) ([]lsp.Location, error)
	Hover(ctx context.Context, uri string, line, character int) (*lsp.HoverResult, error)
	DocumentSymbols(ctx context.Context, uri string) ([]lsp.DocumentSymbol, error)
	WorkspaceSymbols(ctx context.Context, query string) ([]lsp.SymbolInformation, error)
	GoToImplementation(ctx context.Context, uri string, line, character int) ([]lsp.Location, error)
	PrepareCallHierarchy(ctx context.Context, uri string, line, character int) ([]lsp.CallHierarchyItem, error)
	IncomingCalls(ctx context.Context, item lsp.CallHierarchyItem) ([]lsp.CallHierarchyIncomingCall, error)
	OutgoingCalls(ctx context.Context, item lsp.CallHierarchyItem) ([]lsp.CallHierarchyOutgoingCall, error)
}

// lspToolProvider abstracts the subset of *lsp.Manager used by tool handlers.
type lspToolProvider interface {
	readyClientForFile(filePath string) (lspClient, string, bool)
	clientForFileQuick(filePath string) (lspClient, string, error)
	readyLanguages() []string
	clientForLanguage(ctx context.Context, langName string) (lspClient, error)
	clientForFile(ctx context.Context, filePath string) (lspClient, error)
	ensureAllAsync()
}

// managerProvider adapts *lsp.Manager to lspToolProvider for tool handlers.
type managerProvider struct {
	m *lsp.Manager
}

func (p *managerProvider) readyClientForFile(filePath string) (lspClient, string, bool) {
	c, name, ok := p.m.ReadyClientForFile(filePath)
	if !ok {
		return nil, name, false
	}
	return c, name, true
}

func (p *managerProvider) readyLanguages() []string {
	return p.m.ReadyLanguages()
}

func (p *managerProvider) clientForLanguage(ctx context.Context, langName string) (lspClient, error) {
	return p.m.ClientForLanguage(ctx, langName)
}

func (p *managerProvider) ensureAllAsync() {
	p.m.EnsureAllAsync()
}

func (p *managerProvider) clientForFileQuick(filePath string) (lspClient, string, error) {
	return p.m.ClientForFileQuick(filePath)
}

func (p *managerProvider) clientForFile(ctx context.Context, filePath string) (lspClient, error) {
	return p.m.ClientForFile(ctx, filePath)
}

func (b *Toolset) lspProvider() lspToolProvider {
	if b.lspOverride != nil {
		return b.lspOverride
	}
	return nil
}

// lspTools returns LSP-based code intelligence tools. Returns nil if no
// LSP manager is configured.
func (b *Toolset) lspTools() []core.Tool {
	p := b.lspProvider()
	if p == nil {
		return nil
	}
	return []core.Tool{
		toolFn{
			name:         "lsp_goto_definition",
			description:  "Find the definition of a symbol at a position using the Language Server Protocol. Requires file_path (workspace-relative or absolute), line (0-based), and character (0-based). Returns definition locations with file URIs and ranges. If the language server is still indexing, the tool returns an error — retry after a moment or fall back to grep.",
			parameters:   lspPositionParams("Find the definition of the symbol at the given position."),
			readOnly:     true,
			capabilities: []string{"lsp.read"},
			fn:           b.lspGoToDefinition,
		},
		toolFn{
			name:         "lsp_find_references",
			description:  "Find all references to a symbol using the Language Server Protocol. Use this to discover where a function, type, variable, or method is used across the codebase. Requires file_path, line (0-based), and character (0-based). If the language server is still indexing, retry after a moment or fall back to grep.",
			parameters:   lspReferencesParams(),
			readOnly:     true,
			capabilities: []string{"lsp.read"},
			fn:           b.lspFindReferences,
		},
		toolFn{
			name:         "lsp_hover",
			description:  "Get type information and documentation for a symbol at a position using the Language Server Protocol. Requires file_path, line (0-based), and character (0-based). If the language server is still indexing, retry after a moment or fall back to read_file.",
			parameters:   lspPositionParams("Get type info and documentation for the symbol at the given position."),
			readOnly:     true,
			capabilities: []string{"lsp.read"},
			fn:           b.lspHover,
		},
		toolFn{
			name:        "lsp_document_symbol",
			description: "List all symbols (functions, types, variables) in a file using the Language Server Protocol. Returns a hierarchical symbol tree. Requires only file_path. If the language server is still indexing, retry after a moment or fall back to grep + read_file.",
			parameters: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"file_path": map[string]any{"type": "string", "description": "Workspace-relative path to the file to inspect."},
				},
				"required": []string{"file_path"},
			},
			readOnly:     true,
			capabilities: []string{"lsp.read"},
			fn:           b.lspDocumentSymbol,
		},
		toolFn{
			name:        "lsp_workspace_symbol",
			description: "Search for symbols by name across the entire workspace using the Language Server Protocol. Requires a query string. Searches all available language servers. If no server is ready yet, retry after a moment or fall back to grep.",
			parameters: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"query": map[string]any{"type": "string", "description": "Symbol name or partial name to search for."},
				},
				"required": []string{"query"},
			},
			readOnly:     true,
			capabilities: []string{"lsp.read"},
			fn:           b.lspWorkspaceSymbol,
		},
		toolFn{
			name:         "lsp_go_to_implementation",
			description:  "Find implementations of an interface or abstract method at a position using the Language Server Protocol. Requires file_path, line (0-based), and character (0-based). Returns implementation locations with file URIs and ranges. If the language server is still indexing, retry after a moment or fall back to grep.",
			parameters:   lspPositionParams("Find implementations of the symbol at the given position."),
			readOnly:     true,
			capabilities: []string{"lsp.read"},
			fn:           b.lspGoToImplementation,
		},
		toolFn{
			name:         "lsp_prepare_call_hierarchy",
			description:  "Prepare call hierarchy for a function or method at a position using the Language Server Protocol. Returns call hierarchy items that can be used with lsp_incoming_calls or lsp_outgoing_calls. Requires file_path, line (0-based), and character (0-based).",
			parameters:   lspPositionParams("Prepare call hierarchy at the given position."),
			readOnly:     true,
			capabilities: []string{"lsp.read"},
			fn:           b.lspPrepareCallHierarchy,
		},
		toolFn{
			name:         "lsp_incoming_calls",
			description:  "Find all functions or methods that call the function at a position using the Language Server Protocol. Internally calls prepareCallHierarchy then incomingCalls. Requires file_path, line (0-based), and character (0-based).",
			parameters:   lspPositionParams("Find incoming calls for the symbol at the given position."),
			readOnly:     true,
			capabilities: []string{"lsp.read"},
			fn:           b.lspIncomingCalls,
		},
		toolFn{
			name:         "lsp_outgoing_calls",
			description:  "Find all functions or methods called by the function at a position using the Language Server Protocol. Internally calls prepareCallHierarchy then outgoingCalls. Requires file_path, line (0-based), and character (0-based).",
			parameters:   lspPositionParams("Find outgoing calls for the symbol at the given position."),
			readOnly:     true,
			capabilities: []string{"lsp.read"},
			fn:           b.lspOutgoingCalls,
		},
	}
}

func lspPositionParams(description string) map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"file_path": map[string]any{"type": "string", "description": "Workspace-relative or absolute path to the file."},
			"line":      map[string]any{"type": "integer", "description": "0-based line number of the symbol position."},
			"character": map[string]any{"type": "integer", "description": "0-based character offset on the line."},
		},
		"required": []string{"file_path", "line", "character"},
	}
}

func lspReferencesParams() map[string]any {
	p := lspPositionParams("Find all references to the symbol at the given position.")
	p["properties"].(map[string]any)["include_declaration"] = map[string]any{
		"type":        "boolean",
		"description": "Whether to include the declaration in results. Default: true.",
	}
	return p
}

// lspFriendlyError converts raw LSP/transport errors into messages that tell
// the model what to do: retry later or fall back to grep/read_file.
func lspFriendlyError(err error) string {
	msg := err.Error()
	lower := strings.ToLower(msg)

	switch {
	case strings.Contains(lower, "no views"):
		return "language server is still indexing the workspace; retry in a few seconds, or use grep + read_file as fallback"
	case strings.Contains(lower, "pipe") || strings.Contains(lower, "closed"):
		return "language server connection lost (will restart); retry the LSP call, or use grep + read_file as fallback"
	case strings.Contains(lower, "deadline exceeded") || strings.Contains(lower, "timeout") || strings.Contains(lower, "cancelled"):
		return "language server request timed out (may still be indexing); retry or use grep + read_file as fallback"
	case strings.Contains(lower, "not found") || strings.Contains(lower, "not configured"):
		return msg
	default:
		return fmt.Sprintf("LSP: %s — if the server is still warming up, retry or use grep + read_file", msg)
	}
}

// --- LSP Tool Handlers ---

func (b *Toolset) lspGoToDefinition(ctx context.Context, call core.ToolCall) (core.ToolResult, error) {
	return b.runLSPPositionOp(ctx, call, "definition",
		func(ctx context.Context, client lspClient, uri string, line, character int, includeDeclaration bool) (any, error) {
			return client.GoToDefinition(ctx, uri, line, character)
		})
}

func (b *Toolset) lspFindReferences(ctx context.Context, call core.ToolCall) (core.ToolResult, error) {
	return b.runLSPPositionOp(ctx, call, "references",
		func(ctx context.Context, client lspClient, uri string, line, character int, includeDeclaration bool) (any, error) {
			return client.FindReferences(ctx, uri, line, character, includeDeclaration)
		})
}

func (b *Toolset) lspHover(ctx context.Context, call core.ToolCall) (core.ToolResult, error) {
	return b.runLSPPositionOp(ctx, call, "hover",
		func(ctx context.Context, client lspClient, uri string, line, character int, includeDeclaration bool) (any, error) {
			return client.Hover(ctx, uri, line, character)
		})
}

func (b *Toolset) lspDocumentSymbol(ctx context.Context, call core.ToolCall) (core.ToolResult, error) {
	var in struct {
		FilePath string `json:"file_path"`
	}
	if err := decodeInput(call.Input, &in); err != nil {
		return marshalToolError(call, "invalid_args", err.Error()), nil
	}
	abs, err := b.safePath(in.FilePath)
	if err != nil {
		return marshalToolError(call, "permission_denied", err.Error()), nil
	}

	uri := lsp.PathToURI(abs)
	p := b.lspProvider()
	if p == nil {
		return marshalToolError(call, "lsp_not_ready", "no language servers configured"), nil
	}
	client, _, err := p.clientForFileQuick(abs)
	if err != nil {
		return marshalToolError(call, "lsp_not_ready", err.Error()), nil
	}

	lspCtx, cancel := context.WithTimeout(ctx, lspRequestTimeout)
	defer cancel()

	symbols, err := client.DocumentSymbols(lspCtx, uri)
	if err != nil {
		return marshalToolError(call, "lsp_call_failed", lspFriendlyError(err)), nil
	}

	var md strings.Builder
	md.WriteString(fmt.Sprintf("## Symbols in %s\n\n", in.FilePath))
	writeDocumentSymbols(&md, symbols, 0)
	md.WriteString(fmt.Sprintf("\n**%d top-level symbols**", len(symbols)))

	return core.ToolResult{
		ModelText: md.String(),
		Metadata:  map[string]any{"symbol_count": len(symbols)},
	}, nil
}

func writeDocumentSymbols(md *strings.Builder, symbols []lsp.DocumentSymbol, depth int) {
	for _, s := range symbols {
		indent := strings.Repeat("  ", depth)
		kind := lsp.SymbolKindName(s.Kind)
		detail := ""
		if s.Detail != "" {
			detail = fmt.Sprintf(" — *%s*", s.Detail)
		}
		md.WriteString(fmt.Sprintf("%s- **%s** (%s)%s — line %d\n",
			indent, s.Name, kind, detail, s.Range.Start.Line+1))
		writeDocumentSymbols(md, s.Children, depth+1)
	}
}

// --- Go-to-implementation ---

func (b *Toolset) lspGoToImplementation(ctx context.Context, call core.ToolCall) (core.ToolResult, error) {
	return b.runLSPPositionOp(ctx, call, "implementation",
		func(ctx context.Context, client lspClient, uri string, line, character int, includeDeclaration bool) (any, error) {
			return client.GoToImplementation(ctx, uri, line, character)
		})
}

// --- Call hierarchy ---

func (b *Toolset) lspPrepareCallHierarchy(ctx context.Context, call core.ToolCall) (core.ToolResult, error) {
	return b.runLSPPositionOp(ctx, call, "call hierarchy",
		func(ctx context.Context, client lspClient, uri string, line, character int, includeDeclaration bool) (any, error) {
			return client.PrepareCallHierarchy(ctx, uri, line, character)
		})
}

func (b *Toolset) lspIncomingCalls(ctx context.Context, call core.ToolCall) (core.ToolResult, error) {
	return b.runLSPPositionOp(ctx, call, "incoming calls",
		func(ctx context.Context, client lspClient, uri string, line, character int, includeDeclaration bool) (any, error) {
			items, err := client.PrepareCallHierarchy(ctx, uri, line, character)
			if err != nil {
				return nil, err
			}
			var all []lsp.CallHierarchyIncomingCall
			for _, item := range items {
				calls, err := client.IncomingCalls(ctx, item)
				if err != nil {
					return nil, err
				}
				all = append(all, calls...)
			}
			return all, nil
		})
}

func (b *Toolset) lspOutgoingCalls(ctx context.Context, call core.ToolCall) (core.ToolResult, error) {
	return b.runLSPPositionOp(ctx, call, "outgoing calls",
		func(ctx context.Context, client lspClient, uri string, line, character int, includeDeclaration bool) (any, error) {
			items, err := client.PrepareCallHierarchy(ctx, uri, line, character)
			if err != nil {
				return nil, err
			}
			var all []lsp.CallHierarchyOutgoingCall
			for _, item := range items {
				calls, err := client.OutgoingCalls(ctx, item)
				if err != nil {
					return nil, err
				}
				all = append(all, calls...)
			}
			return all, nil
		})
}

// --- Workspace symbol ---

func (b *Toolset) lspWorkspaceSymbol(ctx context.Context, call core.ToolCall) (core.ToolResult, error) {
	var in struct {
		Query string `json:"query"`
	}
	if err := decodeInput(call.Input, &in); err != nil {
		return marshalToolError(call, "invalid_args", err.Error()), nil
	}
	if strings.TrimSpace(in.Query) == "" {
		return marshalToolError(call, "invalid_args", "query is required"), nil
	}

	var allSymbols []lsp.SymbolInformation
	var errs []string
	p := b.lspProvider()
	if p == nil {
		return marshalToolError(call, "lsp_not_ready", "no language servers configured"), nil
	}
	p.ensureAllAsync()
	for _, name := range p.readyLanguages() {
		symbols, err := func() ([]lsp.SymbolInformation, error) {
			lspCtx, cancel := context.WithTimeout(ctx, lspRequestTimeout)
			defer cancel()
			client, err := p.clientForLanguage(lspCtx, name)
			if err != nil {
				return nil, err
			}
			return client.WorkspaceSymbols(lspCtx, in.Query)
		}()
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		allSymbols = append(allSymbols, symbols...)
	}

	if len(allSymbols) == 0 {
		msg := fmt.Sprintf("No symbols found matching %q", in.Query)
		if len(errs) > 0 {
			msg += fmt.Sprintf(". Server status: %s", strings.Join(errs, "; "))
		}
		return core.ToolResult{ModelText: msg}, nil
	}

	var md strings.Builder
	md.WriteString(fmt.Sprintf("## Workspace Symbols: %q\n\n", in.Query))
	limit := min(len(allSymbols), 50)
	for i := 0; i < limit; i++ {
		s := allSymbols[i]
		path := lsp.URIToPath(s.Location.URI)
		md.WriteString(fmt.Sprintf("- **%s** (%s) — `%s:%d`",
			s.Name, lsp.SymbolKindName(s.Kind), path, s.Location.Range.Start.Line+1))
		if s.ContainerName != "" {
			md.WriteString(fmt.Sprintf(" in *%s*", s.ContainerName))
		}
		md.WriteString("\n")
	}
	if len(allSymbols) > limit {
		md.WriteString(fmt.Sprintf("\n... and %d more", len(allSymbols)-limit))
	}

	return core.ToolResult{
		ModelText: md.String(),
		Metadata:  map[string]any{"total": len(allSymbols), "shown": limit},
	}, nil
}

func (b *Toolset) runLSPPositionOp(
	ctx context.Context, call core.ToolCall, opName string,
	op func(ctx context.Context, client lspClient, uri string, line, character int, includeDeclaration bool) (any, error),
) (core.ToolResult, error) {
	var in struct {
		FilePath           string `json:"file_path"`
		Line               int    `json:"line"`
		Character          int    `json:"character"`
		IncludeDeclaration *bool  `json:"include_declaration"`
	}
	if err := decodeInput(call.Input, &in); err != nil {
		return marshalToolError(call, "invalid_args", err.Error()), nil
	}
	if in.Line < 0 {
		return marshalToolError(call, "invalid_args", "line must be >= 0"), nil
	}
	if in.Character < 0 {
		return marshalToolError(call, "invalid_args", "character must be >= 0"), nil
	}

	abs, err := b.safePath(in.FilePath)
	if err != nil {
		return marshalToolError(call, "permission_denied", err.Error()), nil
	}

	uri := lsp.PathToURI(abs)
	p := b.lspProvider()
	if p == nil {
		return marshalToolError(call, "lsp_not_ready", "no language servers configured"), nil
	}
	client, _, err := p.clientForFileQuick(abs)
	if err != nil {
		return marshalToolError(call, "lsp_not_ready", err.Error()), nil
	}

	includeDecl := true // default to true as documented
	if in.IncludeDeclaration != nil {
		includeDecl = *in.IncludeDeclaration
	}

	// Bound the request: a server that is ready but still indexing can
	// queue requests for minutes; without a deadline the tool hangs.
	lspCtx, cancel := context.WithTimeout(ctx, lspRequestTimeout)
	defer cancel()
	result, err := op(lspCtx, client, uri, in.Line, in.Character, includeDecl)
	if err != nil {
		return marshalToolError(call, "lsp_call_failed", lspFriendlyError(err)), nil
	}

	resultJSON, _ := json.Marshal(result)
	return core.ToolResult{
		ModelText: formatLSPResult(opName, result),
		Metadata:  map[string]any{"raw": json.RawMessage(resultJSON)},
	}, nil
}

func formatLSPResult(opName string, result any) string {
	switch r := result.(type) {
	case []lsp.Location:
		if len(r) == 0 {
			return "No results found."
		}
		var md strings.Builder
		md.WriteString(fmt.Sprintf("## %s — %d result(s)\n\n", opName, len(r)))
		limit := min(len(r), 50)
		for i := 0; i < limit; i++ {
			loc := r[i]
			path := lsp.URIToPath(loc.URI)
			md.WriteString(fmt.Sprintf("- `%s:%d:%d`\n", path,
				loc.Range.Start.Line+1, loc.Range.Start.Character+1))
		}
		if len(r) > limit {
			md.WriteString(fmt.Sprintf("\n... and %d more", len(r)-limit))
		}
		return md.String()
	case *lsp.HoverResult:
		if r.Contents.Value == "" {
			return "No hover information available."
		}
		return fmt.Sprintf("## Hover\n\n%s", r.Contents.Value)
	default:
		jsonBytes, _ := json.MarshalIndent(result, "", "  ")
		return fmt.Sprintf("## %s\n\n```json\n%s\n```", opName, string(jsonBytes))
	}
}
