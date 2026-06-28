package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/usewhale/whale/internal/core"
	"github.com/usewhale/whale/internal/defaults"
)

const (
	defaultClassifierBaseURL = "https://api.deepseek.com"
	classifierDefaultTimeout = 10 * time.Second
	classifierMaxTokens      = 512
)

// Classifier is the auto-review classifier. It reviews tool calls before
// execution and returns allow/warn/block decisions.
// Translated from Claude Code's classifyYoloAction in yoloClassifier.ts.
type Classifier struct {
	apiKey    string
	baseURL   string
	model     string
	cfg       ClassifierConfig
	timeoutMS int // resolved timeout (cfg.TimeoutMS or classifierDefaultTimeout)
	breaker   *CircuitBreaker
	client    *http.Client
}

// NewClassifier creates a new classifier.
func NewClassifier(cfg ClassifierConfig) *Classifier {
	// Use the config-provided key first (resolved by the app from env + credentials.json),
	// then fall back to direct env read.
	apiKey := strings.TrimSpace(cfg.APIKey)
	if apiKey == "" {
		apiKey = strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY"))
	}
	baseURL := strings.TrimSpace(os.Getenv("DEEPSEEK_BASE_URL"))
	if baseURL == "" {
		baseURL = defaultClassifierBaseURL
	}
	timeout := time.Duration(cfg.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = classifierDefaultTimeout
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = defaults.DefaultModel
	}
	resolvedMS := int(timeout / time.Millisecond)
	if resolvedMS <= 0 {
		resolvedMS = int(classifierDefaultTimeout / time.Millisecond)
	}
	return &Classifier{
		apiKey:    apiKey,
		baseURL:   baseURL,
		model:     model,
		cfg:       cfg,
		timeoutMS: resolvedMS,
		breaker:   NewCircuitBreaker(),
		client: &http.Client{
			Timeout: timeout + 5*time.Second, // HTTP timeout slightly longer than context timeout
		},
	}
}

// IsEnabled returns true if the classifier is currently enabled.
func (c *Classifier) IsEnabled() bool {
	return c != nil && c.cfg.Enabled && c.apiKey != ""
}

// SetEnabled toggles the classifier on or off at runtime.
func (c *Classifier) SetEnabled(enabled bool) {
	if c != nil {
		c.cfg.Enabled = enabled
	}
}

// Review checks a tool call and returns a classification decision.
// It first checks the allowlist, then calls the LLM classifier if needed.
//
// Returns the review result. If the classifier is disabled or the API key
// is missing, it returns allow (fail-open for missing configuration).
func (c *Classifier) Review(
	ctx context.Context,
	messages []core.Message,
	action core.ToolCall,
	workspaceRoot string,
) *ClassifierReview {
	start := time.Now()

	// Fast path: allowlisted tools skip the classifier entirely.
	if isClassifierAllowlisted(action.Name) {
		return &ClassifierReview{
			ToolCallID:    action.ID,
			ToolName:      action.Name,
			FromAllowlist: true,
			Result: ClassifierResult{
				Decision: ClassifierDecisionAllow,
				Reason:   "allowlisted safe tool",
				Risk:     ClassifierRiskLow,
			},
		}
	}

	// Disabled classifier → fail-open (allow). This is the config opt-out path.
	if !c.cfg.Enabled {
		return &ClassifierReview{
			ToolCallID: action.ID,
			ToolName:   action.Name,
			Result: ClassifierResult{
				Decision: ClassifierDecisionAllow,
				Reason:   "classifier disabled",
				Risk:     ClassifierRiskLow,
			},
		}
	}
	// No API key → fail-closed (block). Without a key the classifier can't run,
	// and we must not silently degrade to unrestricted auto-accept.
	// Translated from Claude Code's auto-mode gate: the mode is not offered
	// when the classifier can't run.
	if c.apiKey == "" {
		return &ClassifierReview{
			ToolCallID: action.ID,
			ToolName:   action.Name,
			Result: ClassifierResult{
				Decision: ClassifierDecisionBlock,
				Reason:   "auto-review requires DEEPSEEK_API_KEY",
				Risk:     ClassifierRiskHigh,
			},
		}
	}

	// Build transcript and prompts
	transcript := buildClassifierTranscript(messages, action)
	systemPrompt := buildClassifierSystemPrompt(c.cfg, workspaceRoot)
	userPrompt := buildClassifierUserPrompt(transcript)

	// Set context timeout
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.timeoutMS)*time.Millisecond)
	defer cancel()

	// Call the model
	result, err := c.classify(ctx, systemPrompt, userPrompt)
	duration := time.Since(start).Milliseconds()

	if err != nil {
		// Fail-closed: if the classifier errors or times out, block for safety.
		return &ClassifierReview{
			ToolCallID: action.ID,
			ToolName:   action.Name,
			Model:      c.model,
			DurationMS: duration,
			Result: ClassifierResult{
				Decision: ClassifierDecisionBlock,
				Reason:   fmt.Sprintf("classifier unavailable: %s", err.Error()),
				Risk:     ClassifierRiskHigh,
			},
		}
	}

	return &ClassifierReview{
		ToolCallID: action.ID,
		ToolName:   action.Name,
		Model:      c.model,
		DurationMS: duration,
		Result:     *result,
	}
}

// classify sends a non-streaming JSON-mode request to the DeepSeek API.
func (c *Classifier) classify(ctx context.Context, systemPrompt, userPrompt string) (*ClassifierResult, error) {
	payload := map[string]any{
		"model": c.model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userPrompt},
		},
		"max_tokens":      classifierMaxTokens,
		"temperature":     0,
		"stream":          false,
		"response_format": map[string]string{"type": "json_object"},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}

	url := c.baseURL + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("api call: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("api error %d: %s", resp.StatusCode, truncateString(string(respBody), 200))
	}

	// Parse the DeepSeek chat completion response
	var chatResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	if len(chatResp.Choices) == 0 {
		return nil, fmt.Errorf("empty response from classifier")
	}

	content := strings.TrimSpace(chatResp.Choices[0].Message.Content)
	if content == "" {
		return nil, fmt.Errorf("empty content from classifier")
	}

	// Strip markdown code fences if present (model may wrap JSON in ```json ... ```)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)

	var result ClassifierResult
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return nil, fmt.Errorf("parse classifier output: %w (raw: %s)", err, truncateString(content, 100))
	}

	// Validate the decision
	switch result.Decision {
	case ClassifierDecisionAllow, ClassifierDecisionWarn, ClassifierDecisionBlock:
		// valid
	default:
		// Unknown decision → block for safety
		result.Decision = ClassifierDecisionBlock
		if result.Reason == "" {
			result.Reason = "classifier returned unknown decision"
		}
		result.Risk = ClassifierRiskHigh
	}

	return &result, nil
}

// RecordDenial records a classifier denial in the circuit breaker.
// Returns true if the circuit breaker has triggered (should escalate to user).
func (c *Classifier) RecordDenial(turnID string) bool {
	return c.breaker.RecordDenial(turnID)
}

// RecordNonDenial records a non-denial (allow/warn) to reset the consecutive counter.
func (c *Classifier) RecordNonDenial(turnID string) {
	c.breaker.RecordNonDenial(turnID)
}

// IsInterrupted returns true if the circuit breaker has triggered for this turn.
func (c *Classifier) IsInterrupted(turnID string) bool {
	return c.breaker.IsInterrupted(turnID)
}

// ClearTurn clears the circuit breaker state for a turn.
func (c *Classifier) ClearTurn(turnID string) {
	c.breaker.ClearTurn(turnID)
}

const classifierCircuitBreakerNudge = `<auto_review_circuit_breaker>
🛡️ Auto-review has blocked 3 consecutive actions as too risky.
The agent should stop retrying blocked actions and either:
- Find a materially safer alternative
- Ask the user for explicit approval or clearer instructions
- If stuck, suggest the user switch to Ask mode via /permissions
</auto_review_circuit_breaker>`

func (a *Agent) persistClassifierCircuitBreakerNudge(ctx context.Context, sessionID string) (core.Message, error) {
	return a.store.Create(ctx, core.Message{
		SessionID:    sessionID,
		Role:         core.RoleUser,
		Text:         classifierCircuitBreakerNudge,
		Hidden:       false,
		FinishReason: core.FinishReasonEndTurn,
	})
}
