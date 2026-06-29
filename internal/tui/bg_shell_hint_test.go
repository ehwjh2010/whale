package tui

import (
	"strings"
	"testing"

	"github.com/usewhale/whale/internal/runtime/protocol"
)

func TestBackgroundShellHint(t *testing.T) {
	if got := backgroundShellHint(0); got != "" {
		t.Fatalf("hint(0) = %q, want empty", got)
	}
	if got := backgroundShellHint(1); got != "1 background shell · /ps to view · /stop to stop" {
		t.Fatalf("hint(1) = %q", got)
	}
	if got := backgroundShellHint(3); got != "3 background shells · /ps to view · /stop to stop" {
		t.Fatalf("hint(3) = %q", got)
	}
}

// While working, the hint rides inline on the busy status line and the
// standalone idle row stays hidden — never surfaced twice at once.
func TestBackgroundShellHintInlineWhileBusy(t *testing.T) {
	m := newModel(nil, "", "", "")
	m.mode = modeChat
	m.busy = true
	m.bgShells = []protocol.BackgroundShell{{Command: "go test ./...", Status: "running"}}

	busy := m.renderBusyStatusLine(200)
	if !strings.Contains(busy, "1 background shell · /ps to view · /stop to stop") {
		t.Fatalf("busy line missing inline hint:\n%s", busy)
	}
	if row := m.renderBackgroundShellHint(200); row != "" {
		t.Fatalf("standalone row should be hidden while busy, got:\n%s", row)
	}
}

// When idle the hint becomes its own dim row.
func TestBackgroundShellHintIdleRow(t *testing.T) {
	m := newModel(nil, "", "", "")
	m.mode = modeChat
	m.busy = false
	m.bgShells = []protocol.BackgroundShell{
		{Command: "npm run dev", Status: "running"},
		{Command: "tail -f log", Status: "running"},
	}

	row := m.renderBackgroundShellHint(80)
	if !strings.Contains(row, "2 background shells · /ps to view · /stop to stop") {
		t.Fatalf("idle row missing hint:\n%s", row)
	}

	m.bgShells = nil
	if row := m.renderBackgroundShellHint(80); row != "" {
		t.Fatalf("empty set should render nothing, got:\n%s", row)
	}
}

// shell_wait / shell_cancel must never leak the opaque task id into the
// in-flight tool line.
func TestSummarizeShellWaitCancelHidesTaskID(t *testing.T) {
	// Text mirrors service.summarizeToolCall output: "<tool>: <detail>".
	wait := summarizeToolCallForChat("shell_wait", "shell_wait: task-20260628093258-46")
	if strings.Contains(wait, "task-") {
		t.Fatalf("shell_wait leaked task id: %q", wait)
	}
	if wait != "Waiting on background shell" {
		t.Fatalf("shell_wait summary = %q", wait)
	}

	cancel := summarizeToolCallForChat("shell_cancel", "shell_cancel: task-20260628093258-46")
	if cancel != "Canceling background shell" {
		t.Fatalf("shell_cancel summary = %q", cancel)
	}

	run := summarizeToolCallForChat("shell_run", "shell_run: go build ./...")
	if run != "Running go build ./..." {
		t.Fatalf("shell_run summary = %q", run)
	}
}
