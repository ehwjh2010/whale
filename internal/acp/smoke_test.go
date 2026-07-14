package acp_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestSmokeACP runs a full ACP integration test: initialize → new session → prompt.
// Requires DEEPSEEK_API_KEY to be set.
func TestSmokeACP(t *testing.T) {
	apiKey := os.Getenv("DEEPSEEK_API_KEY")
	if apiKey == "" {
		t.Skip("DEEPSEEK_API_KEY not set")
	}

	bin := os.Getenv("WHALE_ACP_BIN")
	if bin == "" {
		t.Skip("set WHALE_ACP_BIN env var to path of built binary")
	}

	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "DEEPSEEK_API_KEY="+apiKey)
	cmd.Stderr = os.Stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}

	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stdin.Close()
		cmd.Process.Kill()
		cmd.Wait()
	}()

	dec := json.NewDecoder(stdout)

	// 1. Initialize
	writeJSON(stdin, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": 1,
		},
	})

	var initResp map[string]any
	if err := dec.Decode(&initResp); err != nil {
		t.Fatalf("decode init response: %v", err)
	}
	t.Logf("init response: %v", initResp)

	result, ok := initResp["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected init result, got: %v", initResp)
	}
	if pv, ok := result["protocolVersion"].(float64); !ok || int(pv) != 1 {
		t.Fatalf("expected protocolVersion 1, got: %v", result["protocolVersion"])
	}

	// 2. Create session
	writeJSON(stdin, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "session/new",
		"params": map[string]any{
			"cwd": "/tmp",
		},
	})

	var sessResp map[string]any
	if err := dec.Decode(&sessResp); err != nil {
		t.Fatalf("decode session/new response: %v", err)
	}
	t.Logf("session/new response: %v", sessResp)

	sessResult := sessResp["result"].(map[string]any)
	sessionID := sessResult["sessionId"].(string)
	if sessionID == "" {
		t.Fatal("expected non-empty sessionId")
	}
	t.Logf("session ID: %s", sessionID)

	// 3. Send prompt
	writeJSON(stdin, map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "session/prompt",
		"params": map[string]any{
			"sessionId": sessionID,
			"prompt": []map[string]any{
				{"type": "text", "text": "Say hello and introduce yourself in one short sentence."},
			},
		},
	})

	// Read notifications (session/update) until we get the prompt response.
	var updateCount int
	for {
		var msg map[string]any
		if err := dec.Decode(&msg); err != nil {
			t.Fatalf("decode message: %v (updates so far: %d)", err, updateCount)
		}
		t.Logf("received: %v", msg)

		// Check if it's the prompt response (has id=3)
		if id, ok := msg["id"].(float64); ok && int(id) == 3 {
			if errData, ok := msg["error"].(map[string]any); ok {
				t.Fatalf("prompt error: %v", errData)
			}
			resp := msg["result"].(map[string]any)
			t.Logf("prompt response: stopReason=%v", resp["stopReason"])
			if sr, ok := resp["stopReason"].(string); ok {
				if sr != "end_turn" {
					t.Errorf("expected stopReason 'end_turn', got: %s", sr)
				}
			}
			break
		}

		// It's a notification (session/update)
		updateCount++
		if params, ok := msg["params"].(map[string]any); ok {
			if upd, ok := params["update"].(map[string]any); ok {
				sessUpdate := upd["sessionUpdate"]
				t.Logf("  sessionUpdate: %v", sessUpdate)
				// Check for content
				if content, ok := upd["content"].(map[string]any); ok {
					if text, ok := content["text"].(string); ok && strings.TrimSpace(text) != "" {
						t.Logf("  text: %s", text[:min(len(text), 80)])
					}
				}
			}
		}
	}

	t.Logf("ACP smoke test passed: %d updates received, final response ok", updateCount)
}

func writeJSON(w interface{ Write([]byte) (int, error) }, v any) {
	data, _ := json.Marshal(v)
	w.Write(data)
	w.Write([]byte("\n"))
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestSmokeACPCancel verifies that sending session/cancel during a long prompt
// correctly interrupts the turn and returns stopReason: cancelled.
func TestSmokeACPCancel(t *testing.T) {
	apiKey := os.Getenv("DEEPSEEK_API_KEY")
	if apiKey == "" {
		t.Skip("DEEPSEEK_API_KEY not set")
	}

	bin := os.Getenv("WHALE_ACP_BIN")
	if bin == "" {
		t.Skip("set WHALE_ACP_BIN env var to path of built binary")
	}

	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "DEEPSEEK_API_KEY="+apiKey)
	cmd.Stderr = os.Stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}

	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stdin.Close()
		cmd.Process.Kill()
		cmd.Wait()
	}()

	dec := json.NewDecoder(stdout)

	// Initialize
	writeJSON(stdin, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": 1}})
	var initResp map[string]any
	dec.Decode(&initResp)

	// New session
	writeJSON(stdin, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "session/new", "params": map[string]any{"cwd": "/tmp"}})
	var sessResp map[string]any
	dec.Decode(&sessResp)
	sessionID := sessResp["result"].(map[string]any)["sessionId"].(string)
	t.Logf("session ID: %s", sessionID)

	// Send a prompt designed to trigger a tool call (so it takes long enough to cancel).
	writeJSON(stdin, map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "session/prompt",
		"params": map[string]any{
			"sessionId": sessionID,
			"prompt":    []map[string]any{{"type": "text", "text": "List files in /tmp then write a summary to /tmp/acp_test.txt"}},
		},
	})

	// Read a few updates, then send cancel.
	var updateCount int
	for {
		var msg map[string]any
		if err := dec.Decode(&msg); err != nil {
			t.Fatalf("decode message: %v", err)
		}

		// Check if it's the prompt response.
		if id, ok := msg["id"].(float64); ok && int(id) == 3 {
			resp := msg["result"].(map[string]any)
			t.Logf("prompt response: stopReason=%v", resp["stopReason"])
			if sr, ok := resp["stopReason"].(string); ok && sr == "cancelled" {
				t.Logf("cancel test passed: stopReason=cancelled after %d updates", updateCount)
				return
			}
			t.Errorf("expected stopReason 'cancelled', got: %v", resp["stopReason"])
			return
		}

		updateCount++

		// After 5 updates, send cancel notification.
		if updateCount == 5 {
			t.Logf("sending session/cancel after %d updates", updateCount)
			writeJSON(stdin, map[string]any{
				"jsonrpc": "2.0", "method": "session/cancel",
				"params": map[string]any{"sessionId": sessionID},
			})
		}

		// Safety: don't wait forever.
		if updateCount > 200 {
			t.Fatal("too many updates, cancel may not have worked")
		}
	}
}
