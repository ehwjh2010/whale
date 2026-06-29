package core

import (
	"strings"
	"testing"
)

// Regression: spawn_subagent must surface the child's full final message as the
// tool result body. The generic renderer treated the report as a one-line
// "summary" header and dropped the body, so the parent agent only ever saw the
// first line plus metadata and had to re-do the subagent's work.
func TestRenderSubagentTextSurfacesFullReportBody(t *testing.T) {
	const sentinel = "ZEBRA-PINEAPPLE-7741"
	report := "REPORT-START\n\nSome findings about the codebase.\n\n" + sentinel + "\n\nMore detail follows.\nReport end."
	payload := map[string]any{
		"report":           report,
		"summary":          "REPORT-START",
		"child_session_id": "child-123",
		"tool_calls":       []any{"grep", "read_file"},
		"truncated":        false,
	}

	got := RenderToolResultText("spawn_subagent", OutcomeSuccess, "ok", payload)

	if !strings.Contains(got, sentinel) {
		t.Fatalf("report body dropped from ModelText; want sentinel %q in:\n%s", sentinel, got)
	}
	if !strings.HasPrefix(got, "REPORT-START") {
		t.Fatalf("report body should lead the ModelText, got:\n%s", got)
	}
	if !strings.Contains(got, "child-123") {
		t.Fatalf("metadata trailer missing child_session_id, got:\n%s", got)
	}
}

// An empty report must produce an explicit marker, not a bare metadata tail
// that a model reads as "nothing to act on".
func TestRenderSubagentTextEmptyReportMarker(t *testing.T) {
	payload := map[string]any{
		"report":           "",
		"summary":          "",
		"child_session_id": "child-9",
	}
	got := RenderToolResultText("spawn_subagent", OutcomeSuccess, "ok", payload)
	if !strings.Contains(got, "no output") {
		t.Fatalf("expected empty-output marker, got:\n%s", got)
	}
}

// Back-compat: results produced before the report/summary split folded the full
// body into "summary". Those must still render the body, not just one line.
func TestRenderSubagentTextFallsBackToSummary(t *testing.T) {
	const sentinel = "OLD-FORMAT-TOKEN-42"
	payload := map[string]any{
		"summary":          "REPORT-START\nbody line\n" + sentinel,
		"child_session_id": "child-7",
	}
	got := RenderToolResultText("spawn_subagent", OutcomeSuccess, "ok", payload)
	if !strings.Contains(got, sentinel) {
		t.Fatalf("legacy summary body dropped; want %q in:\n%s", sentinel, got)
	}
}

// A truncated report must tell the parent where the full transcript lives so it
// can recover detail instead of silently working from a clipped body.
func TestRenderSubagentTextTruncationPointsToChildSession(t *testing.T) {
	payload := map[string]any{
		"report":           "REPORT-START\nclipped...",
		"truncated":        true,
		"child_session_id": "child-555",
	}
	got := RenderToolResultText("spawn_subagent", OutcomeSuccess, "ok", payload)
	if !strings.Contains(got, "truncated") || !strings.Contains(got, "child-555") {
		t.Fatalf("truncation notice should reference the child session, got:\n%s", got)
	}
}
