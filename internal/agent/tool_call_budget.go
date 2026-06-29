package agent

import (
	"context"

	"github.com/usewhale/whale/internal/core"
)

// toolCallWrapUpNudgeText steers a capped agent (a subagent with maxToolCalls
// set) to stop opening new work and produce its final answer before the hard
// tool-call cap truncates the turn mid-task. Mirrors Codex's budget-limit
// steering: wrap up without aborting, rather than being cut off with a
// forced-summary banner. See [forceSummaryAndFinish] for the hard-cap path this
// is meant to avoid reaching.
const toolCallWrapUpNudgeText = "<tool_call_budget_low>\nYou are close to this run's tool-call limit. Do NOT start new lines of investigation or open new files. Use any remaining calls only to confirm what you already need, then write your final answer now: summarize what you found, call out anything still unverified, and give a clear next step. If you keep calling tools you will be cut off mid-task and your output will be discarded as incomplete progress.\n</tool_call_budget_low>"

// persistToolCallWrapUpNudge records the hidden wrap-up steer as a user message
// so it enters provider history before the next round. Mirrors
// [Agent.persistPlanLoopNudge].
func (a *Agent) persistToolCallWrapUpNudge(ctx context.Context, sessionID string) (core.Message, error) {
	return a.store.Create(ctx, core.Message{
		SessionID:    sessionID,
		Role:         core.RoleUser,
		Text:         toolCallWrapUpNudgeText,
		Hidden:       true,
		FinishReason: core.FinishReasonEndTurn,
	})
}

// toolCallWrapUpThreshold returns the tool-call count at which a capped agent is
// nudged to wrap up — leaving ~20% of the budget (at least a few calls) as
// headroom to actually finalize.
//
// Returns 0 (nudge disabled) when there is no cap, and also when the cap is so
// small that the threshold isn't strictly below it: the turn loop only fires the
// nudge while toolCalls < maxToolCalls, so a threshold equal to the cap could
// never fire. Reporting 0 there makes that contract explicit — callers can rely
// on "threshold > 0 implies firable" rather than re-deriving the headroom rule.
// In practice only tiny caps (<= 3) hit this; such a run has no room to wrap up
// before the hard cap anyway.
func toolCallWrapUpThreshold(maxToolCalls int) int {
	if maxToolCalls <= 0 {
		return 0
	}
	headroom := maxToolCalls / 5
	if headroom < 3 {
		headroom = 3
	}
	threshold := maxToolCalls - headroom
	if threshold < 1 || threshold >= maxToolCalls {
		return 0
	}
	return threshold
}
