package agent

import (
	"context"

	"github.com/usewhale/whale/internal/core"
)

// maybeBlockByClassifier checks a tool call against the auto-review classifier
// before execution. If blocked, it injects a classifier-blocked tool result
// and returns true. If allowed or warned, it returns false and the tool
// proceeds to normal execution.
//
// Translated from Claude Code's classifier integration in permissions.ts canUseTool.
func (a *Agent) maybeBlockByClassifier(
	ctx context.Context,
	sc *streamDispatchContext,
	call core.ToolCall,
	results *[]core.ToolResult,
	flushPending *func() error,
) bool {
	if a.classifier == nil {
		return false
	}

	// Get conversation history for the classifier transcript
	history := sc.History
	if history == nil {
		return false
	}

	review := a.classifier.Review(ctx, history, call, a.workspaceRoot)

	// Emit event for TUI
	emitClassifierEvent(ctx, *sc, review)

	// Record in circuit breaker
	if review.Result.Decision == ClassifierDecisionBlock {
		interrupted := a.classifier.RecordDenial(sc.SessionID)
		_ = interrupted // handled by caller (turn_loop) via classifier state

		// Flush pending parallel batches before injecting blocked result
		if flushPending != nil {
			if err := (*flushPending)(); err != nil {
				return false
			}
		}

		// Inject a classifier-blocked tool result — the model sees this
		// instead of actual execution output and can self-correct.
		// OutcomeBlocked is set explicitly so FinalizeToolResultChannels
		// (which is a no-op when Outcome is already set) does not re-derive
		// OutcomeSuccess from the plain-text ModelText.
		tr := core.FinalizeToolResultChannels(core.ToolResult{
			ToolCallID: call.ID,
			Name:       call.Name,
			ModelText:  classifierBlockedModelText(review.Result.Reason),
			Code:       "classifier_blocked",
			Outcome:    core.OutcomeBlocked,
			Payload: map[string]any{
				"message": review.Result.Reason,
				"code":    "classifier_blocked",
				"summary": classifierBlockedSummary(review.Result.Reason),
			},
			Metadata: map[string]any{
				"classifier_decision": string(review.Result.Decision),
				"classifier_reason":   review.Result.Reason,
				"classifier_risk":     string(review.Result.Risk),
			},
		})
		*results = append(*results, tr)

		if err := emitDispatchEvent(ctx, *sc, AgentEvent{
			Type: AgentEventTypeToolCallBlocked,
			ToolBlocked: &ToolCallBlocked{
				ToolCallID: call.ID,
				ToolName:   call.Name,
				ReasonCode: "classifier_blocked",
			},
		}); err != nil {
			return false
		}

		if err := emitDispatchEvent(ctx, *sc, AgentEvent{
			Type:   AgentEventTypeToolResult,
			Result: &tr,
		}); err != nil {
			return false
		}

		return true
	}

	a.classifier.RecordNonDenial(sc.SessionID)

	// WARN: prepend a warning to the tool result so the model sees it.
	if review.Result.Decision == ClassifierDecisionWarn {
		sc.WarnPrefix = classifierWarnedModelTextPrefix(review.Result.Reason)
	}

	return false
}

// emitClassifierEvent emits a classifier review event for TUI display.
func emitClassifierEvent(ctx context.Context, sc streamDispatchContext, review *ClassifierReview) {
	ev := &ClassifierReviewEvent{
		ToolCallID:    review.ToolCallID,
		ToolName:      review.ToolName,
		Decision:      review.Result.Decision,
		Reason:        review.Result.Reason,
		Risk:          review.Result.Risk,
		Model:         review.Model,
		DurationMS:    review.DurationMS,
		FromAllowlist: review.FromAllowlist,
		Blocked:       review.Result.Decision == ClassifierDecisionBlock,
	}
	_ = emitDispatchEvent(ctx, sc, AgentEvent{
		Type:       AgentEventTypeClassifierReview,
		Classifier: ev,
	})
}
