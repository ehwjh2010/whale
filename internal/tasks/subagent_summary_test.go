package tasks

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// subagentSummaryLine bounds the preview in runes, never bytes: a CJK report
// (3 bytes/rune) longer than the cap must not be cut mid-rune into invalid
// UTF-8 that json.Marshal would silently rewrite to U+FFFD in session rows.
func TestSubagentSummaryLineCutsOnRuneBoundary(t *testing.T) {
	report := strings.Repeat("界", subagentSummaryMaxChars+50) // 3 bytes each
	line := subagentSummaryLine(report)
	if !utf8.ValidString(line) {
		t.Fatalf("preview is not valid UTF-8: %q", line)
	}
	if n := utf8.RuneCountInString(line); n != subagentSummaryMaxChars {
		t.Fatalf("preview = %d runes, want %d", n, subagentSummaryMaxChars)
	}
}

// Only the first line feeds the preview, and short input passes through whole.
func TestSubagentSummaryLineTakesFirstLine(t *testing.T) {
	if got := subagentSummaryLine("headline\nbody\nmore"); got != "headline" {
		t.Fatalf("preview = %q, want %q", got, "headline")
	}
}
