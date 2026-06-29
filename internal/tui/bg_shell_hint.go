package tui

import "fmt"

// backgroundShellHint is the single canonical footer string summarizing running
// background shell tasks. It is reused by both the inline busy-line variant and
// the standalone idle row so the two surfaces never disagree on wording. Returns
// "" when there is nothing to surface.
func backgroundShellHint(n int) string {
	if n <= 0 {
		return ""
	}
	plural := ""
	if n != 1 {
		plural = "s"
	}
	return fmt.Sprintf("%d background shell%s · /ps to view · /stop to stop", n, plural)
}
