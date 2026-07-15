package lsp

// Client manages a single language server subprocess and its JSON-RPC
// connection. Each Client corresponds to one language (e.g. "go", "python").
//
// Lifecycle:
//   1. Created by Manager.clientForServer / backgroundStart
//   2. Start() launches the server process and completes the LSP initialize
//      handshake (initialize → initialized notification)
//   3. ready is set to true after the handshake succeeds
//   4. On each tool operation, ensureDocumentOpen sends textDocument/didOpen
//      so the server parses the file (idempotent — skipped if already open)
//   5. Close() sends shutdown → exit, then kills the process after a timeout
//
// Thread safety: Client uses a sync.Mutex for connection/process state
// and an atomic.Bool for the ready flag.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/usewhale/whale/internal/shell"
)

// Client manages a single language server process.
type Client struct {
	language string   // display name (e.g. "go")
	command  string   // resolved executable path
	args     []string // command-line arguments
	rootURI  string

	mu       sync.Mutex
	conn     *rpcConn
	cmd      *exec.Cmd
	caps     *ServerCapabilities
	cancel   context.CancelFunc
	done     chan struct{}
	ready    atomic.Bool     // true after initialize handshake completes
		openDocs  map[string]bool  // URIs that have been didOpen'd
	stderrBuf *strings.Builder // captured stderr for crash diagnostics
}

// Start launches the language server process and performs the LSP handshake.
// The client lock is only held briefly for field writes; the slow
// initialize handshake happens outside the lock.
func (c *Client) Start(ctx context.Context) error {
	c.mu.Lock()
	if c.conn != nil {
		c.mu.Unlock()
		return nil // already started and ready
	}

	cctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(cctx, c.command, c.args...)
	shell.ConfigureCommand(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		c.mu.Unlock()
		return fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		c.mu.Unlock()
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		c.mu.Unlock()
		return fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		cancel()
		c.mu.Unlock()
		return fmt.Errorf("start %s: %w", c.language, err)
	}

	stderrBuf := new(strings.Builder)
	go func() {
		data, _ := io.ReadAll(stderr)
		if len(data) > 0 {
			stderrBuf.Write(data)
		}
	}()

	conn := newRPCConn(stdout, stdin)
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn.readLoop()
	}()

	// Store fields so alive() sees them, then release lock for slow init
	c.conn = conn
	c.cmd = cmd
	c.cancel = cancel
	c.stderrBuf = stderrBuf
	c.done = done
	c.mu.Unlock()

	// Wait for process in background to release OS resources
	go func() {
		_ = cmd.Wait()
	}()

	// Slow: initialize handshake (gopls may take seconds)
	var initResult InitializeResult
	err = conn.sendRequest(cctx, "initialize", InitializeParams{
		ProcessID: os.Getpid(),
		RootURI:   c.rootURI,
		Capabilities: ClientCapabilities{
			TextDocument: &TextDocumentClientCapabilities{
				Hover:            &struct{ ContentFormat []string }{ContentFormat: []string{"markdown", "plaintext"}},
				Definition:       &struct{ LinkSupport bool }{},
				References:       &struct{}{},
				Implementation:   &struct{ LinkSupport bool }{},
				DocumentSymbol: &struct {
					HierarchicalDocumentSymbolSupport bool `json:"hierarchicalDocumentSymbolSupport"`
				}{HierarchicalDocumentSymbolSupport: true},
				CallHierarchy: &struct{}{},
			},
			Workspace: &WorkspaceClientCapabilities{
				Symbol: &struct{ SymbolKind struct{ ValueSet []int } `json:"symbolKind"` }{
					SymbolKind: struct{ ValueSet []int }{
						ValueSet: []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19},
					},
				},
			},
		},
	}, &initResult)
	if err != nil {
		cancel()
		cmd.Process.Kill()
		return fmt.Errorf("initialize: %w", err)
	}

	// Send initialized notification
	if err := conn.sendNotification("initialized", struct{}{}); err != nil {
		cancel()
		cmd.Process.Kill()
		return fmt.Errorf("initialized: %w", err)
	}

	c.mu.Lock()
	c.caps = &initResult.Capabilities
	c.mu.Unlock()
	c.ready.Store(true)
	return nil
}

// alive reports whether the server process and connection are still healthy.
func (c *Client) alive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.ready.Load() {
		return false
	}
	// Check if readLoop has exited
	select {
	case <-c.done:
		return false
	default:
	}
	// Check if process has exited
	if c.cmd.ProcessState != nil && c.cmd.ProcessState.Exited() {
		return false
	}
	return true
}

// Close gracefully shuts down the language server.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		return nil
	}

	// Best-effort graceful shutdown: send shutdown + exit with timeout
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer shutdownCancel()
	_ = c.conn.sendRequest(shutdownCtx, "shutdown", nil, nil)
	_ = c.conn.sendNotification("exit", nil)

	// Cancel context, drain readLoop, kill process
	if c.cancel != nil {
		c.cancel()
	}
	if c.done != nil {
		select {
		case <-c.done:
		case <-time.After(2 * time.Second):
		}
	}
	if c.cmd != nil && c.cmd.Process != nil {
		c.cmd.Process.Kill()
	}

	c.conn = nil
	c.cmd = nil
	c.caps = nil
	c.openDocs = nil
	return nil
}

// --- LSP Operation Methods ---

func (c *Client) ensureDocumentOpen(ctx context.Context, uri string) error {
	c.mu.Lock()
	if c.conn == nil {
		c.mu.Unlock()
		return fmt.Errorf("language server not connected")
	}
	opened := c.openDocs[uri]
	c.mu.Unlock()
	if opened {
		return nil
	}

	path := URIToPath(uri)
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read file for didOpen: %w", err)
	}

	// Determine language ID from extension
	langID := languageIDFromPath(path)

	err = c.conn.sendNotification("textDocument/didOpen", struct {
		TextDocument TextDocumentItem `json:"textDocument"`
	}{
		TextDocument: TextDocumentItem{
			URI:        uri,
			LanguageID: langID,
			Version:    0,
			Text:       string(data),
		},
	})
	if err != nil {
		return fmt.Errorf("didOpen: %w", err)
	}

	c.mu.Lock()
	c.openDocs[uri] = true
	c.mu.Unlock()
	return nil
}

// GoToDefinition returns the definition locations for a symbol.
func (c *Client) GoToDefinition(ctx context.Context, uri string, line, character int) ([]Location, error) {
	if err := c.ensureDocumentOpen(ctx, uri); err != nil {
		return nil, err
	}
	var result []Location
	err := c.conn.sendRequest(ctx,"textDocument/definition", TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: uri},
		Position:     Position{Line: line, Character: character},
	}, &result)
	return result, err
}

// FindReferences returns all references to a symbol.
func (c *Client) FindReferences(ctx context.Context, uri string, line, character int) ([]Location, error) {
	if err := c.ensureDocumentOpen(ctx, uri); err != nil {
		return nil, err
	}
	var result []Location
	err := c.conn.sendRequest(ctx,"textDocument/references", ReferenceParams{
		TextDocumentPositionParams: TextDocumentPositionParams{
			TextDocument: TextDocumentIdentifier{URI: uri},
			Position:     Position{Line: line, Character: character},
		},
		Context: ReferenceContext{IncludeDeclaration: true},
	}, &result)
	return result, err
}

// Hover returns hover information at a position.
func (c *Client) Hover(ctx context.Context, uri string, line, character int) (*HoverResult, error) {
	if err := c.ensureDocumentOpen(ctx, uri); err != nil {
		return nil, err
	}
	var result HoverResult
	err := c.conn.sendRequest(ctx,"textDocument/hover", TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: uri},
		Position:     Position{Line: line, Character: character},
	}, &result)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// DocumentSymbols returns all symbols in a document.
func (c *Client) DocumentSymbols(ctx context.Context, uri string) ([]DocumentSymbol, error) {
	if err := c.ensureDocumentOpen(ctx, uri); err != nil {
		return nil, err
	}
	// Use json.RawMessage to determine format with a single RPC
	var raw json.RawMessage
	err := c.conn.sendRequest(ctx, "textDocument/documentSymbol", struct {
		TextDocument TextDocumentIdentifier `json:"textDocument"`
	}{TextDocument: TextDocumentIdentifier{URI: uri}}, &raw)
	if err != nil {
		return nil, err
	}
	if len(raw) > 0 && raw[0] == '[' && bytes.Contains(raw, []byte(`"location"`)) {
		var siResult []SymbolInformation
		if err := json.Unmarshal(raw, &siResult); err != nil {
			return nil, err
		}
		dsResult := make([]DocumentSymbol, 0, len(siResult))
		for _, si := range siResult {
			dsResult = append(dsResult, DocumentSymbol{
				Name:           si.Name,
				Kind:           si.Kind,
				Range:          si.Location.Range,
				SelectionRange: si.Location.Range,
			})
		}
		return dsResult, nil
	}
	var dsResult []DocumentSymbol
	if err := json.Unmarshal(raw, &dsResult); err != nil {
		return nil, err
	}
	return dsResult, nil
}

// WorkspaceSymbols searches for symbols across the entire workspace.
func (c *Client) WorkspaceSymbols(ctx context.Context, query string) ([]SymbolInformation, error) {
	var result []SymbolInformation
	err := c.conn.sendRequest(ctx,"workspace/symbol", WorkspaceSymbolParams{Query: query}, &result)
	return result, err
}

// GoToImplementation returns implementation locations.
func (c *Client) GoToImplementation(ctx context.Context, uri string, line, character int) ([]Location, error) {
	if err := c.ensureDocumentOpen(ctx, uri); err != nil {
		return nil, err
	}
	var result []Location
	err := c.conn.sendRequest(ctx,"textDocument/implementation", TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: uri},
		Position:     Position{Line: line, Character: character},
	}, &result)
	return result, err
}

// PrepareCallHierarchy returns call hierarchy items for a symbol.
func (c *Client) PrepareCallHierarchy(ctx context.Context, uri string, line, character int) ([]CallHierarchyItem, error) {
	if err := c.ensureDocumentOpen(ctx, uri); err != nil {
		return nil, err
	}
	var result []CallHierarchyItem
	err := c.conn.sendRequest(ctx,"textDocument/prepareCallHierarchy", CallHierarchyPrepareParams{
		TextDocument: TextDocumentIdentifier{URI: uri},
		Position:     Position{Line: line, Character: character},
	}, &result)
	return result, err
}

// IncomingCalls returns functions that call the given item.
func (c *Client) IncomingCalls(ctx context.Context, item CallHierarchyItem) ([]CallHierarchyIncomingCall, error) {
	var result []CallHierarchyIncomingCall
	err := c.conn.sendRequest(ctx,"callHierarchy/incomingCalls", struct {
		Item CallHierarchyItem `json:"item"`
	}{Item: item}, &result)
	return result, err
}

// OutgoingCalls returns functions called by the given item.
func (c *Client) OutgoingCalls(ctx context.Context, item CallHierarchyItem) ([]CallHierarchyOutgoingCall, error) {
	var result []CallHierarchyOutgoingCall
	err := c.conn.sendRequest(ctx,"callHierarchy/outgoingCalls", struct {
		Item CallHierarchyItem `json:"item"`
	}{Item: item}, &result)
	return result, err
}

// languageIDFromPath returns a LSP language ID for a file path.
func languageIDFromPath(path string) string {
	ext := ""
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '.' {
			ext = path[i+1:]
			break
		}
	}
	switch ext {
	case "go":
		return "go"
	case "rs":
		return "rust"
	case "py", "pyi":
		return "python"
	case "ts":
		return "typescript"
	case "tsx":
		return "typescriptreact"
	case "js":
		return "javascript"
	case "jsx":
		return "javascriptreact"
	case "c":
		return "c"
	case "cpp", "cc", "cxx":
		return "cpp"
	case "h":
		return "c"
	case "hpp", "hxx":
		return "cpp"
	case "yaml", "yml":
		return "yaml"
	case "vue":
		return "vue"
	case "json":
		return "json"
	case "css":
		return "css"
	case "html", "htm":
		return "html"
	default:
		return ext
	}
}
