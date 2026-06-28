package agent

import (
	"strings"

	"github.com/usewhale/whale/internal/core"
)

// ClassifierDecision is the structured output from the auto-review classifier.
type ClassifierDecision string

const (
	ClassifierDecisionAllow ClassifierDecision = "allow"
	ClassifierDecisionWarn  ClassifierDecision = "warn"
	ClassifierDecisionBlock ClassifierDecision = "block"
)

// ClassifierRisk represents the risk level of an action.
type ClassifierRisk string

const (
	ClassifierRiskLow    ClassifierRisk = "low"
	ClassifierRiskMedium ClassifierRisk = "medium"
	ClassifierRiskHigh   ClassifierRisk = "high"
)

// ClassifierResult is the output of a classifier check on a single tool call.
type ClassifierResult struct {
	Decision ClassifierDecision `json:"decision"`
	Reason   string             `json:"reason"`
	Risk     ClassifierRisk     `json:"risk"`
}

// ClassifierReview contains the classifier result plus metadata about the review.
type ClassifierReview struct {
	ToolCallID string
	ToolName   string
	Result     ClassifierResult
	// Model used for the classifier call (empty if allowlisted / in-CWD).
	Model string
	// DurationMS is the classifier API call latency in milliseconds.
	DurationMS int64
	// FromAllowlist is true when the tool was auto-allowed without a model call.
	FromAllowlist bool
	// FromInCWD is true when the tool was allowed because it operates inside the
	// workspace directory (in-project file writes/edits only).
	FromInCWD bool
}

// ClassifierReviewEvent is emitted via AgentEvent for TUI display and telemetry.
type ClassifierReviewEvent struct {
	ToolCallID   string
	ToolName     string
	Decision     ClassifierDecision
	Reason       string
	Risk         ClassifierRisk
	Model        string
	DurationMS   int64
	FromAllowlist bool
	Blocked      bool
}

// ClassifierAction mirrors the concept from Codex GuardianAssessmentAction:
// the action being reviewed, serialized for the classifier prompt.
type ClassifierAction struct {
	ToolCall core.ToolCall
	// CompactInput is a truncated/simplified representation for the classifier.
	CompactInput string
	// CWD is the workspace root at the time of the call.
	CWD string
}

// classifierBlockedModelText returns the ModelText injected into ToolResult when
// the classifier blocks a tool call. Translated from Claude Code's
// buildYoloRejectionMessage — plain text, not JSON, so the reason never needs
// escaping and FinalizeToolResultChannels cannot misinterpret the envelope.
// classifierBlockedSummary returns a short one-line version of the block reason
// for the TUI tool result summary.
func classifierBlockedSummary(reason string) string {
	r := strings.TrimSpace(reason)
	if idx := strings.Index(r, "."); idx > 0 && idx < 100 {
		return r[:idx+1]
	}
	if len(r) > 100 {
		return r[:100] + "..."
	}
	return r
}

func classifierBlockedModelText(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "action blocked by auto-review classifier"
	}
	return "Auto-review blocked this action.\nReason: " + reason + "\n\nIf you have other tasks that don't depend on this action, continue working on those. You may try a materially safer alternative, or ask the user for guidance."
}

// classifierWarnedModelTextPrefix is prepended to the real tool result when
// the classifier issues a warning.
func classifierWarnedModelTextPrefix(reason string) string {
	return "⚠️ Auto-review warning: " + reason + "\n\n"
}
