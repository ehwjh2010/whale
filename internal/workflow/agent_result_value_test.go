package workflow

import "testing"

// Regression: a schemaless agent() call must return the subagent's full report,
// not the one-line Summary preview. Summary was collapsed to a single line when
// the report/summary split landed; the workflow bridge has to read Report.
func TestAgentResultValueReturnsFullReport(t *testing.T) {
	res := AgentTaskResult{
		Report:  "line one\nline two\nline three",
		Summary: "line one",
	}
	got, ok := agentResultValue(res).(string)
	if !ok {
		t.Fatalf("expected string result, got %T", agentResultValue(res))
	}
	if got != res.Report {
		t.Fatalf("agent() returned %q, want full report %q", got, res.Report)
	}
}

// Structured output still takes precedence over the report body.
func TestAgentResultValuePrefersStructured(t *testing.T) {
	res := AgentTaskResult{
		StructuredResult: map[string]any{"k": "v"},
		Report:           "ignored body",
		Summary:          "ignored",
	}
	if _, ok := agentResultValue(res).(map[string]any); !ok {
		t.Fatalf("expected structured map, got %T", agentResultValue(res))
	}
}

// Legacy/resumed entries journaled before Report existed still return Summary.
func TestAgentResultValueFallsBackToSummary(t *testing.T) {
	res := AgentTaskResult{Summary: "only a summary"}
	if got := agentResultValue(res); got != "only a summary" {
		t.Fatalf("agent() returned %v, want summary fallback", got)
	}
}
