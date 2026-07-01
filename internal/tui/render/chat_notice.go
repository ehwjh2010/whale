package render

import (
	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
	tuitheme "github.com/usewhale/whale/internal/tui/theme"
	"strings"
)

func renderNotice(m UIMessage, block string, width int) []string {
	contentWidth := width - 2
	if contentWidth < 16 {
		contentWidth = 16
	}
	rendered := strings.TrimRight(renderSystemNotice(m.Notice, block, contentWidth), "\n")
	lines := strings.Split(rendered, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, "  "+line)
	}
	return out
}

func renderSystemNotice(notice *SystemNotice, fallback string, width int) string {
	if notice == nil {
		return renderEntryText("notice", fallback, width)
	}
	text := notice.Text()
	if strings.TrimSpace(text) == "" {
		text = fallback
	}
	if strings.TrimSpace(text) == "" {
		return ""
	}
	line := styledNoticeLine(notice)
	if strings.TrimSpace(xansi.Strip(line)) == "" {
		line = text
	}
	return hardWrapRendered(line, width)
}

func styledNoticeLine(notice *SystemNotice) string {
	if notice == nil {
		return ""
	}
	glyph := noticeGlyph(notice)
	parts := make([]string, 0, 6)
	if glyph != "" {
		parts = append(parts, noticeToneStyle(notice.Tone).Render(glyph))
	}
	if action := strings.TrimSpace(notice.Action); action != "" {
		parts = append(parts, noticeToneStyle(notice.Tone).Bold(true).Render(action))
	}
	if subject := strings.TrimSpace(notice.Subject); subject != "" {
		parts = append(parts, subject)
	}
	if detail := strings.TrimSpace(notice.Detail); detail != "" {
		parts = append(parts, detail)
	}
	if command := strings.TrimSpace(notice.Command); command != "" {
		parts = append(parts, lipgloss.NewStyle().Foreground(tuitheme.Default.Tool).Render(command))
	}
	line := strings.Join(parts, " ")
	if scope := strings.TrimSpace(notice.Scope); scope != "" {
		if line != "" {
			line += " "
		}
		line += tuitheme.MutedStyle().Render("· " + scope)
	}
	return line
}

func noticeGlyph(notice *SystemNotice) string {
	if notice == nil {
		return ""
	}
	switch notice.Tone {
	case "success":
		return "✓"
	case "warn", "warning":
		return "!"
	case "error":
		return "✗"
	default:
		if strings.HasPrefix(notice.Kind, "permission_") || strings.HasPrefix(notice.Kind, "session_") {
			return "•"
		}
		return "•"
	}
}

func noticeToneStyle(tone string) lipgloss.Style {
	switch strings.TrimSpace(tone) {
	case "success":
		return lipgloss.NewStyle().Foreground(tuitheme.Default.Success)
	case "info":
		return lipgloss.NewStyle().Foreground(tuitheme.Default.Info)
	case "warn", "warning":
		return lipgloss.NewStyle().Foreground(tuitheme.Default.Warn)
	case "error":
		return lipgloss.NewStyle().Foreground(tuitheme.Default.Error)
	default:
		return tuitheme.MutedStyle()
	}
}

// reasoningOnlyStatusPrefix mirrors noFinalAnswerStatusPrefix in the tui
// package. The "Reasoning only" title belongs solely to the no-final-answer
// fallback; other KindStatus cards (e.g. "User input required") must not
// inherit it. Kept as a local constant to avoid a render→tui import cycle.
const reasoningOnlyStatusPrefix = "The model returned reasoning only"

// statusCardTitle returns the bold title for a status card, or "" when the
// card should render its body without a title.
func statusCardTitle(m UIMessage) string {
	if strings.HasPrefix(strings.TrimSpace(m.Text), reasoningOnlyStatusPrefix) {
		return "Reasoning only"
	}
	return ""
}

func renderStatusCard(m UIMessage, block string, width int) []string {
	contentWidth := width - 6
	if contentWidth < 16 {
		contentWidth = 16
	}
	body := lipgloss.NewStyle().
		Foreground(tuitheme.Default.Muted).
		Render(hardWrapRendered(renderEntryText(m.Role, block, contentWidth), contentWidth))
	rendered := body
	if title := statusCardTitle(m); title != "" {
		styledTitle := lipgloss.NewStyle().
			Foreground(roleBorderColor(m)).
			Bold(true).
			Render(title)
		rendered = joinTitleAndBody(styledTitle, body)
	}
	card := spacedCardStyle(width, roleBorderColor(m)).
		Render(strings.TrimRight(rendered, "\n"))
	return strings.Split(strings.TrimRight(card, "\n"), "\n")
}
