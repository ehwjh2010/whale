package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/usewhale/whale/internal/agent"
	"github.com/usewhale/whale/internal/core"
	"github.com/usewhale/whale/internal/policy"
	"github.com/usewhale/whale/internal/session"
)

// handlePrompt processes a session/prompt request: extracts user input from
// ContentBlocks, starts a Whale turn, and streams AgentEvents back as ACP
// session/update notifications.
func (h *Handler) handlePrompt(req *RPCRequest) *RPCErrorResponse {
	var params PromptRequest
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, fmt.Sprintf("invalid params: %v", err))
	}

	// Extract text from content blocks.
	input := extractText(params.Prompt)
	if strings.TrimSpace(input) == "" {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "prompt must contain at least one text block")
	}

	// Look up session context.
	h.mu.Lock()
	sctx, ok := h.sessions[params.SessionID]
	h.mu.Unlock()
	if !ok {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, fmt.Sprintf("session not found: %s", params.SessionID))
	}

	// Register this prompt's cancel. If another prompt is already active
	// (sctx.cancel != nil), store as pending. The pending cancel is invoked
	// by session/cancel and checked after acquiring promptMu.
	ctx, cancel := context.WithCancel(context.Background())
	isPending := false
	h.mu.Lock()
	if sctx.cancel == nil {
		sctx.cancel = cancel
	} else {
		sctx.pendingCancels = append(sctx.pendingCancels, cancel)
		isPending = true
	}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		if !isPending {
			// Promote the next queued cancel to active.
			if len(sctx.pendingCancels) > 0 {
				sctx.cancel = sctx.pendingCancels[0]
				sctx.pendingCancels = sctx.pendingCancels[1:]
			} else {
				sctx.cancel = nil
			}
		}
		h.mu.Unlock()
	}()

	// Serialize access to the shared toolset — only one prompt runs at a time.
	h.promptMu.Lock()
	defer h.promptMu.Unlock()

	// We are now the active prompt. Clear pending flag so cleanup runs on finish.
	isPending = false

	// If we were cancelled while queued, exit immediately.
	if ctx.Err() != nil {
		resp := NewSuccessResponse(req.ID, PromptResponse{StopReason: StopReasonCancelled})
		h.transport.SendResponse(resp)
		return nil
	}

	// Update the active session ID for exec-boundary shell approvals.
	if h.activeSessionID != nil {
		*h.activeSessionID = params.SessionID
	}

	// Switch toolset root, worktree context, and policy workspace to session cwd.
	if h.toolset != nil && sctx.cwd != "" {
		h.toolset.SetRoot(sctx.cwd)
		h.toolset.SetWorktreeContext(sctx.cwd, sctx.cwd)
		// Keep the exec-boundary policy workspace root in sync so
		// external_directory checks use the session's cwd.
		// Reload permission rules for the session cwd so project-specific
		// .whale/config.toml rules are honored.
		sessionPolicy := h.policy
		if h.policyLoader != nil {
			sessionPolicy = h.policyLoader(sctx.cwd)
		}
		h.toolset.SetExecBoundaryPolicy(policy.RulePolicy{
			Default:       sessionPolicy.Default,
			Rules:         sessionPolicy.Rules,
			WorkspaceRoot: sctx.cwd,
			WorktreeRoot:  sctx.cwd,
		})
		h.agent.SetToolPolicy(policy.RulePolicy{
			Default:       sessionPolicy.Default,
			Rules:         sessionPolicy.Rules,
			WorkspaceRoot: sctx.cwd,
			WorktreeRoot:  sctx.cwd,
		})
		Logger.Printf("switched toolset root to %s for session %s", sctx.cwd, params.SessionID)
	}

	// Determine run options based on session mode.
	opts := agent.RunOptions{}
	switch sctx.mode {
	case session.ModeAsk:
		opts.ReadOnly = true
	case session.ModePlan:
		// Plan mode — we still allow read-only tools.
		opts.ReadOnly = true
	}

	// Start the Whale turn.
	events, err := h.agent.RunStreamWithTurnOptions(ctx, sctx.whaleSessionID, input, opts)
	if err != nil {
		cancel()
		return NewErrorResponse(req.ID, ErrCodeInternal, fmt.Sprintf("agent error: %v", err))
	}

	// Stream events as session/update notifications until the turn completes.
	stopReason := h.streamEvents(ctx, params.SessionID, events)

	// Send the prompt response.
	resp := NewSuccessResponse(req.ID, PromptResponse{
		StopReason: stopReason,
	})
	if err := h.transport.SendResponse(resp); err != nil {
		Logger.Printf("failed to send prompt response: %v", err)
	}
	return nil
}

// extractText pulls text content from a list of ContentBlocks.
func extractText(blocks []ContentBlock) string {
	var parts []string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if strings.TrimSpace(b.Text) != "" {
				parts = append(parts, b.Text)
			}
		case "resource":
			if b.Resource != nil && strings.TrimSpace(b.Resource.Text) != "" {
				parts = append(parts, b.Resource.Text)
			}
		case "resource_link":
			// Resource links are hints, not content — skip.
		default:
			// Images, audio are not supported yet.
		}
	}
	return strings.Join(parts, "\n")
}

// streamEvents reads AgentEvents from the channel and translates them to
// ACP session/update notifications. Returns the stop reason for the turn.
func (h *Handler) streamEvents(ctx context.Context, acpSessionID string, events <-chan agent.AgentEvent) StopReason {
	hadError := false
	for ev := range events {
		select {
		case <-ctx.Done():
			return StopReasonCancelled
		default:
		}

		// Track terminal events that indicate non-success outcomes.
		switch ev.Type {
		case agent.AgentEventTypeError:
			hadError = true
			if ev.Err != nil {
				Logger.Printf("agent error during prompt: %v", ev.Err)
			}
			continue
		case agent.AgentEventTypeTurnCancelled:
			return StopReasonCancelled
		}

		update := h.translateEvent(ev)
		if update == nil {
			continue
		}

		notif := SessionNotification{
			SessionID: acpSessionID,
			Update:    *update,
		}
		if err := h.transport.SendNotification(MethodSessionUpdate, notif); err != nil {
			Logger.Printf("failed to send session/update: %v", err)
			return StopReasonEndTurn
		}
	}
	if hadError {
		return StopReasonRefusal
	}
	return StopReasonEndTurn
}

// translateEvent converts a Whale AgentEvent into an ACP SessionUpdate.
// Returns nil if the event should not produce a notification.
func (h *Handler) translateEvent(ev agent.AgentEvent) *SessionUpdate {
	switch ev.Type {
	case agent.AgentEventTypeAssistantDelta:
		if ev.Content != "" {
			u := AgentMessageChunk(ev.Content)
			return &u
		}

	case agent.AgentEventTypeReasoningDelta:
		if ev.ReasoningDelta != "" {
			u := AgentThoughtChunk(ev.ReasoningDelta)
			return &u
		}

	case agent.AgentEventTypeToolCall:
		if ev.ToolCall != nil {
			kind := mapToolKind(ev.ToolCall.Name)
			u := ToolCallNotification(
				ev.ToolCall.ID,
				toolTitle(ev.ToolCall.Name),
				kind,
				ToolCallStatusPending,
			)
			return &u
		}

	case agent.AgentEventTypeToolResult:
		if ev.Result != nil {
			status := ToolCallStatusCompleted
			if ev.Result.IsError() {
				status = ToolCallStatusFailed
			}
			content := toolResultContent(ev.Result)
			u := ToolCallStatusUpdate(ev.Result.ToolCallID, status, content)
			return &u
		}

	case agent.AgentEventTypePlanDelta, agent.AgentEventTypePlanUpdate:
		if ev.PlanUpdate != nil {
			entries := make([]PlanEntry, 0, len(ev.PlanUpdate.Plan))
			for _, step := range ev.PlanUpdate.Plan {
				status := PlanStatusPending
				switch step.Status {
				case "in_progress":
					status = PlanStatusInProgress
				case "completed":
					status = PlanStatusCompleted
				}
				entries = append(entries, PlanEntry{
					Content:  step.Step,
					Priority: PlanPriorityHigh,
					Status:   status,
				})
			}
			u := PlanNotification(entries)
			return &u
		}

	case agent.AgentEventTypeDone:
		// End of stream — handled by the caller.
		return nil

	default:
		return nil
	}
	return nil
}

// mapToolKind maps a Whale tool name to an ACP tool kind.
func mapToolKind(toolName string) ToolKind {
	switch {
	case strings.Contains(toolName, "read") || toolName == "grep" || toolName == "ls":
		return ToolKindRead
	case strings.Contains(toolName, "edit") || strings.Contains(toolName, "write") || strings.Contains(toolName, "patch"):
		return ToolKindEdit
	case strings.Contains(toolName, "delete") || strings.Contains(toolName, "rm"):
		return ToolKindDelete
	case strings.Contains(toolName, "search") || strings.Contains(toolName, "glob"):
		return ToolKindSearch
	case strings.Contains(toolName, "shell") || strings.Contains(toolName, "bash") || strings.Contains(toolName, "exec"):
		return ToolKindExecute
	case strings.Contains(toolName, "fetch") || strings.Contains(toolName, "web"):
		return ToolKindFetch
	case strings.Contains(toolName, "think") || strings.Contains(toolName, "reason"):
		return ToolKindThink
	case strings.Contains(toolName, "mode") || strings.Contains(toolName, "switch"):
		return ToolKindSwitchMode
	default:
		return ToolKindOther
	}
}

// toolTitle returns a human-readable title for a tool call.
func toolTitle(toolName string) string {
	switch toolName {
	case "read_file":
		return "Reading file"
	case "write_file":
		return "Writing file"
	case "edit_file":
		return "Editing file"
	case "multi_edit":
		return "Editing file"
	case "grep":
		return "Searching code"
	case "search_files":
		return "Finding files"
	case "ls":
		return "Listing directory"
	case "shell_run":
		return "Running command"
	case "web_search":
		return "Searching the web"
	case "web_fetch":
		return "Fetching URL"
	case "update_plan":
		return "Updating plan"
	case "request_user_input":
		return "Asking user"
	case "spawn_subagent":
		return "Spawning subagent"
	case "parallel_reason":
		return "Parallel reasoning"
	case "recall_memory":
		return "Recalling memory"
	case "remember":
		return "Saving memory"
	default:
		return toolName
	}
}

// toolResultContent converts a Whale ToolResult into ACP ToolCallContent.
func toolResultContent(result *core.ToolResult) []ToolCallContent {
	if result == nil {
		return nil
	}
	text := result.ModelText
	if text == "" {
		text = string(result.Outcome)
	}
	if text == "" && result.Payload != nil {
		if s, ok := result.Payload.(string); ok {
			text = s
		}
	}
	return []ToolCallContent{ToolContent(text)}
}

// NewACPApprovalFunc creates an ApprovalFunc that forwards tool approval requests
// to the ACP client via session/request_permission. This allows the editor to
// display permission dialogs to the user.
func NewACPApprovalFunc(transport *Transport) policy.ApprovalFunc {
	return func(req policy.ApprovalRequest) policy.ApprovalDecision {
		kind := mapToolKind(req.ToolCall.Name)
		permReq := RequestPermissionRequest{
			SessionID: req.SessionID,
			ToolCall: PermissionToolCall{
				ToolCallID: req.ToolCall.ID,
				Title:      fmt.Sprintf("%s: %s", toolTitle(req.ToolCall.Name), req.Reason),
				Kind:       kind,
				Status:     ToolCallStatusPending,
			},
			Options: []PermissionOption{
				{OptionID: "once", Kind: "allow_once", Name: "Allow once"},
				{OptionID: "always", Kind: "allow_always", Name: "Always allow"},
				{OptionID: "reject", Kind: "reject_once", Name: "Reject"},
			},
		}

		resp, err := transport.CallClientMethod(MethodSessionRequestPerm, permReq)
		if err != nil {
			Logger.Printf("request_permission failed: %v — denying", err)
			return policy.ApprovalDeny
		}

		// Parse the nested response: {"outcome": {"outcome":"selected","optionId":"once"}}
		var permResp RequestPermissionResponse
		raw, _ := json.Marshal(resp.Result)
		if err := json.Unmarshal(raw, &permResp); err != nil {
			Logger.Printf("failed to parse permission response: %v — denying", err)
			return policy.ApprovalDeny
		}

		outcome := permResp.Outcome
		if outcome.Outcome == "cancelled" {
			Logger.Printf("permission cancelled by user")
			return policy.ApprovalCancel
		}
		if outcome.Outcome != "selected" {
			Logger.Printf("unknown permission outcome: %s — denying", outcome.Outcome)
			return policy.ApprovalDeny
		}

		switch outcome.OptionID {
		case "once":
			return policy.ApprovalAllow
		case "always":
			return policy.ApprovalAllowForSession
		case "reject":
			return policy.ApprovalDeny
		default:
			Logger.Printf("unknown permission optionId: %s — denying", outcome.OptionID)
			return policy.ApprovalDeny
		}
	}
}
