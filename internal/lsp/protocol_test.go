package lsp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSymbolKindName(t *testing.T) {
	tests := []struct {
		kind int
		want string
	}{
		{SymbolKindFile, "file"},
		{SymbolKindModule, "module"},
		{SymbolKindNamespace, "namespace"},
		{SymbolKindPackage, "package"},
		{SymbolKindClass, "class"},
		{SymbolKindMethod, "method"},
		{SymbolKindProperty, "property"},
		{SymbolKindField, "field"},
		{SymbolKindConstructor, "constructor"},
		{SymbolKindEnum, "enum"},
		{SymbolKindInterface, "interface"},
		{SymbolKindFunction, "function"},
		{SymbolKindVariable, "variable"},
		{SymbolKindConstant, "constant"},
		{SymbolKindString, "string"},
		{SymbolKindNumber, "number"},
		{SymbolKindBoolean, "boolean"},
		{SymbolKindArray, "array"},
		{SymbolKindObject, "object"},
		{SymbolKindKey, "key"},
		{SymbolKindNull, "null"},
		{SymbolKindEnumMember, "enumMember"},
		{SymbolKindStruct, "struct"},
		{SymbolKindEvent, "event"},
		{SymbolKindOperator, "operator"},
		{SymbolKindTypeParameter, "typeParameter"},
	}
	for _, tc := range tests {
		got := SymbolKindName(tc.kind)
		if got != tc.want {
			t.Errorf("SymbolKindName(%d) = %q, want %q", tc.kind, got, tc.want)
		}
	}
}

func TestSymbolKindName_Unknown(t *testing.T) {
	got := SymbolKindName(9999)
	want := "symbol(9999)"
	if got != want {
		t.Fatalf("SymbolKindName(9999) = %q, want %q", got, want)
	}
}

func TestHoverContents_UnmarshalJSON_MarkupContent(t *testing.T) {
	data := `{"kind":"markdown","value":"**bold text**"}`
	var h HoverContents
	if err := json.Unmarshal([]byte(data), &h); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.Kind != "markdown" || h.Value != "**bold text**" {
		t.Fatalf("got kind=%q value=%q, want kind=markdown value=**bold text**", h.Kind, h.Value)
	}
}

func TestHoverContents_UnmarshalJSON_PlainString(t *testing.T) {
	data := `"just a string"`
	var h HoverContents
	if err := json.Unmarshal([]byte(data), &h); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.Kind != "plaintext" || h.Value != "just a string" {
		t.Fatalf("got kind=%q value=%q, want plain string", h.Kind, h.Value)
	}
}

func TestHoverContents_UnmarshalJSON_MarkedStringArray(t *testing.T) {
	data := `[{"language":"go","value":"func main()"}, "plain text"]`
	var h HoverContents
	if err := json.Unmarshal([]byte(data), &h); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.Kind != "markdown" {
		t.Fatalf("expected markdown kind, got %q", h.Kind)
	}
	if !strings.Contains(h.Value, "func main()") {
		t.Fatalf("expected Go code in output, got: %s", h.Value)
	}
	if !strings.Contains(h.Value, "plain text") {
		t.Fatalf("expected plain text in output, got: %s", h.Value)
	}
	if !strings.Contains(h.Value, "```go") {
		t.Fatalf("expected code fence in output, got: %s", h.Value)
	}
}

func TestHoverContents_UnmarshalJSON_EmptyMarkedStringArray(t *testing.T) {
	data := `[]`
	var h HoverContents
	if err := json.Unmarshal([]byte(data), &h); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.Kind != "markdown" || h.Value != "" {
		t.Fatalf("got kind=%q value=%q", h.Kind, h.Value)
	}
}

func TestHoverContents_UnmarshalJSON_UnsupportedFormat(t *testing.T) {
	data := `[1, 2, 3]`
	var h HoverContents
	err := json.Unmarshal([]byte(data), &h)
	if err == nil {
		t.Fatal("expected error for unsupported format")
	}
	if !strings.Contains(err.Error(), "unsupported format") {
		t.Fatalf("expected 'unsupported format' error, got: %v", err)
	}
}

func TestHoverContents_UnmarshalJSON_Null(t *testing.T) {
	var h HoverContents
	err := json.Unmarshal([]byte("null"), &h)
	if err != nil {
		t.Fatalf("unexpected error for null: %v", err)
	}
	if h.Kind != "plaintext" || h.Value != "" {
		t.Fatalf("null should produce plaintext with empty value, got kind=%q value=%q", h.Kind, h.Value)
	}
}

func TestPathToURI_Windows(t *testing.T) {
	if !isWindows() {
		t.Skip("Windows path test on non-Windows")
	}
	uri := PathToURI(`C:\Users\test\main.go`)
	want := "file:///C:/Users/test/main.go"
	if uri != want {
		t.Fatalf("PathToURI = %q, want %q", uri, want)
	}
}

func TestPathToURI_Unix(t *testing.T) {
	uri := PathToURI("/home/user/main.go")
	want := "file:///home/user/main.go"
	if uri != want {
		t.Fatalf("PathToURI = %q, want %q", uri, want)
	}
}

func TestPathToURI_Relative(t *testing.T) {
	uri := PathToURI("relative/path/file.go")
	want := "file:///relative/path/file.go"
	if uri != want {
		t.Fatalf("PathToURI = %q, want %q", uri, want)
	}
}

func TestURIToPath_Windows(t *testing.T) {
	if !isWindows() {
		t.Skip("Windows path test on non-Windows")
	}
	path := URIToPath("file:///C:/Users/test/main.go")
	want := `C:\Users\test\main.go`
	if !strings.EqualFold(path, want) {
		t.Fatalf("URIToPath = %q, want %q", path, want)
	}
}

func TestURIToPath_Unix(t *testing.T) {
	if isWindows() {
		t.Skip("Unix path test on Windows")
	}
	path := URIToPath("file:///home/user/main.go")
	want := "/home/user/main.go"
	if path != want {
		t.Fatalf("URIToPath = %q, want %q", path, want)
	}
}

func TestURIToPath_InvalidURI(t *testing.T) {
	path := URIToPath("not-a-uri-at-all")
	if path != "not-a-uri-at-all" {
		t.Fatalf("expected input returned unchanged, got %q", path)
	}
}

func TestPathToURI_URIToPath_RoundTrip(t *testing.T) {
	paths := []string{
		`C:\Users\test\main.go`,
	}
	if !isWindows() {
		paths = append(paths, `/home/user/main.go`)
	}
	for _, p := range paths {
		uri := PathToURI(p)
		got := URIToPath(uri)
		if !strings.EqualFold(got, p) {
			t.Errorf("round-trip for %q: got %q", p, got)
		}
	}
}
