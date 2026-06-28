package agent

import (
	"encoding/json"
	"strings"

	"github.com/usewhale/whale/internal/core"
)

// classifierTranscriptEntry is a single turn in the classifier transcript.
type classifierTranscriptEntry struct {
	Role    string // "user" or "assistant"
	Content string // compact JSONL line
}

// buildClassifierTranscript builds a compact transcript from conversation
// history for the classifier. Translated from Claude Code's buildTranscriptEntries
// and toCompactBlock in yoloClassifier.ts.
//
// Rules:
//   - User text messages → {"user":"text"}\n
//   - Assistant tool_use blocks → {"tool_name":"compact_input"}\n
//   - Assistant text is EXCLUDED (model-authored text could influence the classifier)
//   - Tool input is truncated for safety and token efficiency
func buildClassifierTranscript(messages []core.Message, currentAction core.ToolCall) string {
	var b strings.Builder

	for _, msg := range messages {
		switch msg.Role {
		case core.RoleUser:
			if text := strings.TrimSpace(msg.Text); text != "" {
				b.WriteString(compactBlock("user", text))
			}
		case core.RoleAssistant:
			for _, tc := range msg.ToolCalls {
				compact := compactToolInput(tc)
				if compact != "" {
					b.WriteString(compactBlock(tc.Name, compact))
				}
			}
			// Deliberately skip msg.Text — model-authored assistant text
			// should not influence the classifier's decision.
		}
	}

	// Append the current action being classified
	if compact := compactToolInput(currentAction); compact != "" {
		b.WriteString(compactBlock(currentAction.Name, compact))
	}

	return b.String()
}

// compactBlock serializes a single transcript entry as a compact JSONL line.
// Format: {"role_or_tool":"compact_value"}\n
// JSON escaping prevents hostile content from breaking out of the string context.
func compactBlock(key, value string) string {
	entry := map[string]string{key: value}
	data, _ := json.Marshal(entry)
	return string(data) + "\n"
}

// compactToolInput produces a compact, classifier-safe representation of a
// tool call's input. Translated from Claude Code's toAutoClassifierInput pattern.
//
// Strategy:
//   - shell_run: extract only the command
//   - write/edit/multi_edit: show file_path + first 200 chars of content
//   - other tools: truncate full JSON input to 500 chars
func compactToolInput(tc core.ToolCall) string {
	input := strings.TrimSpace(tc.Input)
	if input == "" {
		return ""
	}

	switch tc.Name {
	case "shell_run", "bash":
		return extractShellCommand(input)
	case "write", "edit":
		return extractWritePreview(input)
	case "multi_edit":
		return extractMultiEditPreview(input)
	default:
		return truncateString(input, 500)
	}
}

// extractShellCommand extracts the command from a shell_run tool call input.
func extractShellCommand(input string) string {
	var shellInput struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(input), &shellInput); err != nil {
		return truncateString(input, 300)
	}
	return strings.TrimSpace(shellInput.Command)
}

// extractWritePreview extracts a short preview from write/edit tool input.
func extractWritePreview(input string) string {
	var writeInput struct {
		FilePath string `json:"file_path"`
		Content  string `json:"content"`
		Search   string `json:"search"`
	}
	if err := json.Unmarshal([]byte(input), &writeInput); err != nil {
		return truncateString(input, 300)
	}
	var parts []string
	if writeInput.FilePath != "" {
		parts = append(parts, "file="+writeInput.FilePath)
	}
	if writeInput.Search != "" {
		parts = append(parts, "search="+truncateString(writeInput.Search, 100))
	}
	if writeInput.Content != "" {
		parts = append(parts, "content="+truncateString(writeInput.Content, 200))
	}
	if len(parts) == 0 {
		return truncateString(input, 200)
	}
	return strings.Join(parts, " ")
}

// extractMultiEditPreview extracts a short preview from multi_edit tool input.
func extractMultiEditPreview(input string) string {
	var edits struct {
		FilePath string `json:"file_path"`
		Edits    []struct {
			Search  string `json:"search"`
			Replace string `json:"replace"`
		} `json:"edits"`
	}
	if err := json.Unmarshal([]byte(input), &edits); err != nil {
		return truncateString(input, 300)
	}
	var parts []string
	if edits.FilePath != "" {
		parts = append(parts, "file="+edits.FilePath)
	}
	parts = append(parts, "edits="+itoa(len(edits.Edits)))
	return strings.Join(parts, " ")
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	result := ""
	for n > 0 {
		result = string(rune('0'+n%10)) + result
		n /= 10
	}
	return result
}
