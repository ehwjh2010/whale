package lsp

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

// --- LSP Base Types ---

// Position is a zero-based line and character offset in a document.
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// Range is a span in a document.
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// Location represents a location inside a resource.
type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}

// TextDocumentIdentifier identifies a text document.
type TextDocumentIdentifier struct {
	URI string `json:"uri"`
}

// TextDocumentPositionParams is used for position-based requests.
type TextDocumentPositionParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
}

// ReferenceParams extends TextDocumentPositionParams with context.
type ReferenceParams struct {
	TextDocumentPositionParams
	Context ReferenceContext `json:"context"`
}

// ReferenceContext provides additional info for reference searches.
type ReferenceContext struct {
	IncludeDeclaration bool `json:"includeDeclaration"`
}

// TextDocumentItem represents an open document.
type TextDocumentItem struct {
	URI        string `json:"uri"`
	LanguageID string `json:"languageId"`
	Version    int    `json:"version"`
	Text       string `json:"text"`
}

// --- Call Hierarchy ---

// CallHierarchyItem represents a call hierarchy node.
type CallHierarchyItem struct {
	Name           string `json:"name"`
	Kind           int    `json:"kind"`
	URI            string `json:"uri"`
	Range          Range  `json:"range"`
	SelectionRange Range  `json:"selectionRange"`
}

// CallHierarchyIncomingCall represents an incoming call.
type CallHierarchyIncomingCall struct {
	From       CallHierarchyItem `json:"from"`
	FromRanges []Range           `json:"fromRanges"`
}

// CallHierarchyOutgoingCall represents an outgoing call.
type CallHierarchyOutgoingCall struct {
	To         CallHierarchyItem `json:"to"`
	FromRanges []Range           `json:"fromRanges"`
}

// CallHierarchyPrepareParams is the prepare params.
type CallHierarchyPrepareParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
}

// --- Symbols ---

// SymbolKind constants for LSP symbol kinds.
const (
	SymbolKindFile      = 1
	SymbolKindModule    = 2
	SymbolKindNamespace = 3
	SymbolKindPackage   = 4
	SymbolKindClass     = 5
	SymbolKindMethod    = 6
	SymbolKindProperty  = 7
	SymbolKindField     = 8
	SymbolKindConstructor = 9
	SymbolKindEnum      = 10
	SymbolKindInterface = 11
	SymbolKindFunction  = 12
	SymbolKindVariable  = 13
	SymbolKindConstant  = 14
	SymbolKindString    = 15
	SymbolKindNumber    = 16
	SymbolKindBoolean   = 17
	SymbolKindArray        = 18
	SymbolKindObject       = 19
	SymbolKindKey          = 20
	SymbolKindNull         = 21
	SymbolKindEnumMember   = 22
	SymbolKindStruct       = 23
	SymbolKindEvent        = 24
	SymbolKindOperator     = 25
	SymbolKindTypeParameter = 26
)

// SymbolKindName returns a human-readable name for a symbol kind.
func SymbolKindName(kind int) string {
	switch kind {
	case SymbolKindFile:
		return "file"
	case SymbolKindModule:
		return "module"
	case SymbolKindNamespace:
		return "namespace"
	case SymbolKindPackage:
		return "package"
	case SymbolKindClass:
		return "class"
	case SymbolKindMethod:
		return "method"
	case SymbolKindProperty:
		return "property"
	case SymbolKindField:
		return "field"
	case SymbolKindConstructor:
		return "constructor"
	case SymbolKindEnum:
		return "enum"
	case SymbolKindInterface:
		return "interface"
	case SymbolKindFunction:
		return "function"
	case SymbolKindVariable:
		return "variable"
	case SymbolKindConstant:
		return "constant"
	case SymbolKindString:
		return "string"
	default:
		return fmt.Sprintf("symbol(%d)", kind)
	}
}

// SymbolInformation represents a workspace symbol.
type SymbolInformation struct {
	Name          string   `json:"name"`
	Kind          int      `json:"kind"`
	Location      Location `json:"location"`
	ContainerName string   `json:"containerName,omitempty"`
}

// DocumentSymbol is a hierarchical symbol in a document.
type DocumentSymbol struct {
	Name           string           `json:"name"`
	Detail         string           `json:"detail,omitempty"`
	Kind           int              `json:"kind"`
	Range          Range            `json:"range"`
	SelectionRange Range            `json:"selectionRange"`
	Children       []DocumentSymbol `json:"children,omitempty"`
}

// --- Hover ---

// HoverResult contains hover information.
type HoverResult struct {
	Contents HoverContents `json:"contents"`
	Range    *Range        `json:"range,omitempty"`
}

// HoverContents can be a string or MarkupContent.
type HoverContents struct {
	Kind  string `json:"kind"`  // "markdown" or "plaintext"
	Value string `json:"value"`
}

// --- Initialize ---

// InitializeParams is sent to start an LSP session.
type InitializeParams struct {
	ProcessID             int                `json:"processId"`
	RootURI               string             `json:"rootUri"`
	Capabilities          ClientCapabilities `json:"capabilities"`
	WorkspaceFolders      []WorkspaceFolder  `json:"workspaceFolders,omitempty"`
}

// WorkspaceFolder represents a workspace folder.
type WorkspaceFolder struct {
	URI  string `json:"uri"`
	Name string `json:"name"`
}

// ClientCapabilities declares what the client supports.
type ClientCapabilities struct {
	TextDocument *TextDocumentClientCapabilities `json:"textDocument,omitempty"`
	Workspace    *WorkspaceClientCapabilities    `json:"workspace,omitempty"`
}

// TextDocumentClientCapabilities for document operations.
type TextDocumentClientCapabilities struct {
	Hover            *struct{ ContentFormat []string } `json:"hover,omitempty"`
	Definition       *struct{ LinkSupport bool }        `json:"definition,omitempty"`
	References       *struct{}                          `json:"references,omitempty"`
	Implementation   *struct{ LinkSupport bool }        `json:"implementation,omitempty"`
	DocumentSymbol   *struct {
		HierarchicalDocumentSymbolSupport bool `json:"hierarchicalDocumentSymbolSupport"`
	} `json:"documentSymbol,omitempty"`
	CallHierarchy *struct{} `json:"callHierarchy,omitempty"`
}

// WorkspaceClientCapabilities for workspace operations.
type WorkspaceClientCapabilities struct {
	Symbol *struct {
		SymbolKind struct{ ValueSet []int } `json:"symbolKind"`
	} `json:"symbol,omitempty"`
}

// ServerCapabilities declares what the server supports.
type ServerCapabilities struct {
	TextDocumentSync         interface{} `json:"textDocumentSync,omitempty"`
	HoverProvider            interface{} `json:"hoverProvider,omitempty"`
	DefinitionProvider       interface{} `json:"definitionProvider,omitempty"`
	ReferencesProvider       interface{} `json:"referencesProvider,omitempty"`
	ImplementationProvider   interface{} `json:"implementationProvider,omitempty"`
	DocumentSymbolProvider   interface{} `json:"documentSymbolProvider,omitempty"`
	WorkspaceSymbolProvider  interface{} `json:"workspaceSymbolProvider,omitempty"`
	CallHierarchyProvider    interface{} `json:"callHierarchyProvider,omitempty"`
}

// InitializeResult is the server's response to initialize.
type InitializeResult struct {
	Capabilities ServerCapabilities `json:"capabilities"`
}

// --- Workspace Symbol ---

// WorkspaceSymbolParams for workspace/symbol request.
type WorkspaceSymbolParams struct {
	Query string `json:"query"`
}

// --- URI Helpers ---

// PathToURI converts an absolute file path to a file:// URI.
// "C:\foo\bar.go" → "file:///C:/foo/bar.go"
// "/home/user/foo.go" → "file:///home/user/foo.go"
func PathToURI(absPath string) string {
	// Convert to forward slashes
	absPath = filepath.ToSlash(absPath)
	// On Windows, paths start with "C:/..." not "/C:/..."
	// On Unix, paths start with "/home/..."
	if !strings.HasPrefix(absPath, "/") {
		absPath = "/" + absPath
	}
	u := &url.URL{Scheme: "file", Path: absPath}
	return u.String()
}

// URIToPath converts a file:// URI back to an absolute file path.
func URIToPath(uri string) string {
	u, err := url.Parse(uri)
	if err != nil {
		return uri
	}
	path := u.Path
	// On Windows, strip leading slash from "/C:/foo/bar.go"
	if len(path) >= 3 && path[0] == '/' && path[2] == ':' {
		path = path[1:]
	}
	return filepath.FromSlash(path)
}
