package agent

import (
	_ "embed"
	"fmt"
	"strings"
)

// classifierSystemPrompt is the base system prompt embedded from the .txt file.
//
//go:embed classifier_system_prompt.txt
var classifierSystemPrompt string

// ClassifierConfig holds user-customizable classifier rules.
// Mirrors Claude Code's autoMode config in yoloClassifier.ts.
type ClassifierConfig struct {
	// Enabled controls whether auto-review runs at all.
	Enabled bool
	// Model overrides the classifier model. Empty means use the main model.
	Model string
	// APIKey is the DeepSeek API key. Empty falls back to DEEPSEEK_API_KEY env var.
	APIKey string
	// AllowRules are additional allow rules (additive to defaults).
	AllowRules []string
	// DenyRules are additional deny rules (additive to defaults).
	DenyRules []string
	// Environment describes the execution environment for the classifier.
	Environment []string
	// TimeoutMS is the classifier API timeout in milliseconds. 0 uses default (10000).
	TimeoutMS int
}

// DefaultClassifierConfig returns the default classifier configuration.
func DefaultClassifierConfig() ClassifierConfig {
	return ClassifierConfig{
		Enabled:   false,
		TimeoutMS: 10000,
	}
}

// buildClassifierSystemPrompt assembles the full system prompt, combining the
// base template with user-customized allow/deny/environment rules.
// Translated from Claude Code's buildYoloSystemPrompt.
func buildClassifierSystemPrompt(cfg ClassifierConfig, workspaceRoot string) string {
	prompt := classifierSystemPrompt

	// Append user allow rules
	if len(cfg.AllowRules) > 0 {
		prompt += "\n## User-Added Allow Rules:\n"
		for _, rule := range cfg.AllowRules {
			prompt += fmt.Sprintf("- %s\n", rule)
		}
	}

	// Append user deny rules
	if len(cfg.DenyRules) > 0 {
		prompt += "\n## User-Added Deny Rules:\n"
		for _, rule := range cfg.DenyRules {
			prompt += fmt.Sprintf("- %s\n", rule)
		}
	}

	// Append environment context
	prompt += "\n## Environment:\n"
	if workspaceRoot != "" {
		prompt += fmt.Sprintf("- Workspace root: %s\n", workspaceRoot)
	}
	prompt += "- OS: " + detectOS() + "\n"
	for _, env := range cfg.Environment {
		prompt += fmt.Sprintf("- %s\n", env)
	}

	return prompt
}

// buildClassifierUserPrompt builds the user prompt for the classifier.
// It contains the transcript of the conversation plus the action being reviewed.
func buildClassifierUserPrompt(transcript string) string {
	return strings.TrimSpace(`
Below is a transcript of the conversation between a user and a coding agent.
Review the LAST tool call (the action) and classify it.

## Conversation Transcript:
` + transcript + `

## Instructions:
Review the action above carefully. Consider the user's original intent from the conversation context.
Respond with ONLY a JSON object following the output format.
`)
}

// detectOS returns a short OS identifier for the classifier prompt.
func detectOS() string {
	return "linux/macos" // simplified; could use runtime.GOOS
}
