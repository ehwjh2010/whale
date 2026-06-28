package main

import "testing"

func TestClassifyApprovalEvent(t *testing.T) {
	tests := []struct {
		name        string
		event       approvalEvent
		includeMode bool
		wantCat     string
		wantExp     string
		wantOK      bool
		wantReason  string
	}{
		{
			name:    "user denied stays needs review",
			event:   approvalEvent{Event: "approval_denied"},
			wantCat: "real_user_denied",
			wantExp: "needs_review",
			wantOK:  true,
		},
		{
			name:    "danger policy is expected block",
			event:   approvalEvent{Event: "approval_policy_denied", Code: "permission_denied", MatchedRule: "shell:rm -rf*=deny"},
			wantCat: "policy_danger_denied",
			wantExp: "block",
			wantOK:  true,
		},
		{
			name:       "read-only policy skipped",
			event:      approvalEvent{Event: "approval_policy_denied", Code: "read_only_turn_denied"},
			wantOK:     false,
			wantReason: "read_only_policy",
		},
		{
			name:       "mode blocked skipped by default",
			event:      approvalEvent{Event: "approval_mode_blocked"},
			wantOK:     false,
			wantReason: "mode_blocked",
		},
		{
			name:        "mode blocked optional",
			event:       approvalEvent{Event: "approval_mode_blocked"},
			includeMode: true,
			wantCat:     "mode_blocked",
			wantExp:     "needs_review",
			wantOK:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCat, gotExp, gotOK, gotReason := classifyApprovalEvent(tt.event, tt.includeMode)
			if gotCat != tt.wantCat || gotExp != tt.wantExp || gotOK != tt.wantOK || gotReason != tt.wantReason {
				t.Fatalf("classify = (%q, %q, %v, %q), want (%q, %q, %v, %q)", gotCat, gotExp, gotOK, gotReason, tt.wantCat, tt.wantExp, tt.wantOK, tt.wantReason)
			}
		})
	}
}

func TestDedupeEventsPrefersAgentDeniedOverPromptDenied(t *testing.T) {
	events := []approvalEvent{
		{Session: "s", ToolCallID: "call-1", Event: "approval_prompt_denied"},
		{Session: "s", ToolCallID: "call-1", Event: "approval_denied", AssistantMessageID: "m-1"},
	}

	got := dedupeEvents(events)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].Event != "approval_denied" || got[0].AssistantMessageID != "m-1" {
		t.Fatalf("deduped event = %+v, want approval_denied with assistant message", got[0])
	}
}

func TestSessionPathForApprovalPath(t *testing.T) {
	got := sessionPathForApprovalPath("/tmp/sessions/abc.approval_events.jsonl")
	want := "/tmp/sessions/abc.jsonl"
	if got != want {
		t.Fatalf("session path = %q, want %q", got, want)
	}
}
