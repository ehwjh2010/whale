package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
	"sync/atomic"
)

// Logger writes log messages to stderr (never stdout, which is reserved for ACP messages).
var Logger = log.New(os.Stderr, "[whale-acp] ", log.LstdFlags)

// Transport handles JSON-RPC 2.0 message reading and writing over stdio.
//
// Architecture: a single goroutine (the dispatcher) reads stdin and routes
// messages to the appropriate consumer. This avoids concurrent reads on the
// underlying scanner, which is not safe for concurrent use.
type Transport struct {
	reader  *bufio.Scanner
	writer  *json.Encoder
	writeMu sync.Mutex
	stderr  io.Writer

	// Pending outbound requests waiting for a client response.
	pending   map[int64]chan json.RawMessage
	pendingMu sync.Mutex
	seq       atomic.Int64

	// activeCancel is called when a cancel notification arrives for the
	// currently active prompt. It allows CallClientMethod to abort waiting
	// for a permission response when the user cancels the turn.
	cancelMu     sync.Mutex
	activeCancel context.CancelFunc

	// Dispatcher channels.
	requestCh      chan *dispatchItem
	responseCh     chan json.RawMessage
	notificationCh chan json.RawMessage
	done           chan struct{}
}

type dispatchItem struct {
	Raw json.RawMessage
	Req *RPCRequest
}

// NewTransport creates a Transport reading from stdin and writing to stdout.
func NewTransport() *Transport {
	return NewTransportWithIO(os.Stdin, os.Stdout, os.Stderr)
}

// NewTransportWithIO allows custom io streams for testing.
func NewTransportWithIO(in io.Reader, out io.Writer, errw io.Writer) *Transport {
	t := &Transport{
		reader:         bufio.NewScanner(in),
		writer:         json.NewEncoder(out),
		stderr:         errw,
		pending:        make(map[int64]chan json.RawMessage),
		requestCh:      make(chan *dispatchItem, 8),
		responseCh:     make(chan json.RawMessage, 8),
		notificationCh: make(chan json.RawMessage, 8),
		done:           make(chan struct{}),
	}
	t.reader.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	return t
}

// StartDispatcher starts the single stdin reader goroutine.
// It classifies each message and routes it to the correct channel.
// Call this once before any reads.
func (t *Transport) StartDispatcher() {
	go func() {
		defer close(t.done)
		for {
			raw, err := t.readRaw()
			if err != nil {
				if err != io.EOF {
					Logger.Printf("dispatcher read error: %v", err)
				}
				// Signal shutdown to all waiters.
				t.pendingMu.Lock()
				for _, ch := range t.pending {
					close(ch)
				}
				t.pending = nil
				t.pendingMu.Unlock()
				close(t.requestCh)
				close(t.responseCh)
				close(t.notificationCh)
				return
			}
			t.dispatch(raw)
		}
	}()
}

// readRaw reads a single line from stdin, validates JSON-RPC, returns raw bytes.
func (t *Transport) readRaw() (json.RawMessage, error) {
	for t.reader.Scan() {
		line := t.reader.Bytes()
		if len(line) == 0 {
			continue
		}
		var check struct {
			JSONRPC string `json:"jsonrpc"`
		}
		if err := json.Unmarshal(line, &check); err != nil || check.JSONRPC != "2.0" {
			Logger.Printf("invalid message: %s", string(line))
			t.writeRawLocked(json.RawMessage(fmt.Sprintf(
				`{"jsonrpc":"2.0","id":null,"error":{"code":%d,"message":"Parse error"}}`,
				ErrCodeParse,
			)))
			continue
		}
		raw := make(json.RawMessage, len(line))
		copy(raw, line)
		return raw, nil
	}
	if err := t.reader.Err(); err != nil {
		return nil, fmt.Errorf("stdin read: %w", err)
	}
	return nil, io.EOF
}

// dispatch routes a raw message to the correct channel.
func (t *Transport) dispatch(raw json.RawMessage) {
	// Classify: request (has id + method), response (has id, no method), notification (no id).
	var envelope struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	json.Unmarshal(raw, &envelope)

	if envelope.ID == nil {
		// Notification: no id.
		select {
		case t.notificationCh <- raw:
		default:
			Logger.Printf("notification channel full, dropping")
		}
		return
	}

	if envelope.Method == "" {
		// Response: has id, no method — must be a response to a pending request.
		// Try to extract numeric id for matching.
		var idWrap struct {
			ID interface{} `json:"id"`
		}
		json.Unmarshal(raw, &idWrap)
		var numID int64
		switch v := idWrap.ID.(type) {
		case float64:
			numID = int64(v)
		case int64:
			numID = v
		default:
			// Non-numeric id — try to match via the pending map.
			// For now, log and drop.
			Logger.Printf("unrecognized response id type: %T", idWrap.ID)
			return
		}

		t.pendingMu.Lock()
		ch, ok := t.pending[numID]
		t.pendingMu.Unlock()
		if ok {
			select {
			case ch <- raw:
			default:
				Logger.Printf("pending response channel full for id=%d", numID)
			}
		} else {
			Logger.Printf("unexpected response for id=%d", numID)
		}
		return
	}

	// Request: has id + method — from the client.
	req, err := ParseMessage(raw)
	if err != nil {
		Logger.Printf("failed to parse request: %v", err)
		return
	}
	select {
	case t.requestCh <- &dispatchItem{Raw: raw, Req: req}:
	default:
		Logger.Printf("request channel full, dropping %s", req.Method)
	}
}

// Requests returns the channel for incoming client requests.
func (t *Transport) Requests() <-chan *dispatchItem { return t.requestCh }

// Notifications returns the channel for incoming client notifications.
func (t *Transport) Notifications() <-chan json.RawMessage { return t.notificationCh }

// Done returns a channel that closes when the dispatcher stops.
func (t *Transport) Done() <-chan struct{} { return t.done }

// GetPendingChannel registers and returns a channel for a pending request.
// The caller must call RemovePending after receiving the response.
func (t *Transport) GetPendingChannel(id int64) chan json.RawMessage {
	ch := make(chan json.RawMessage, 1)
	t.pendingMu.Lock()
	t.pending[id] = ch
	t.pendingMu.Unlock()
	return ch
}

// RemovePending removes a pending request channel.
func (t *Transport) RemovePending(id int64) {
	t.pendingMu.Lock()
	delete(t.pending, id)
	t.pendingMu.Unlock()
}

// SendResponse writes a successful JSON-RPC response to stdout.
func (t *Transport) SendResponse(resp *RPCResponse) error {
	return t.writeJSON(resp)
}

// SendError writes a JSON-RPC error response to stdout.
func (t *Transport) SendError(resp *RPCErrorResponse) error {
	return t.writeJSON(resp)
}

// SendNotification writes a JSON-RPC notification to stdout.
func (t *Transport) SendNotification(method string, params interface{}) error {
	notif := struct {
		JSONRPC string      `json:"jsonrpc"`
		Method  string      `json:"method"`
		Params  interface{} `json:"params,omitempty"`
	}{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}
	return t.writeJSON(notif)
}

// CallClientMethod sends a request to the client and waits for the response.
// It does NOT read stdin — the dispatcher delivers the response via channel.
// SetActiveCancel stores a context.CancelFunc for the currently active prompt.
// CallClientMethod uses this to abort waiting for a response when the prompt is cancelled.
func (t *Transport) SetActiveCancel(cancel context.CancelFunc) {
	t.cancelMu.Lock()
	defer t.cancelMu.Unlock()
	t.activeCancel = cancel
}

// TriggerCancel invokes the stored active cancel function, if any.
// context.CancelFunc is idempotent — calling it multiple times is safe.
func (t *Transport) TriggerCancel() {
	t.cancelMu.Lock()
	fn := t.activeCancel
	t.cancelMu.Unlock()
	if fn != nil {
		fn()
	}
}

// CallClientMethod sends a request to the client and waits for the response.
// If the active prompt is cancelled (via session/cancel), it returns an error.
func (t *Transport) CallClientMethod(method string, params interface{}) (*RPCResponse, error) {
	id := t.seq.Add(1)
	respCh := t.GetPendingChannel(id)
	defer t.RemovePending(id)

	req := struct {
		JSONRPC string      `json:"jsonrpc"`
		ID      int64       `json:"id"`
		Method  string      `json:"method"`
		Params  interface{} `json:"params,omitempty"`
	}{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}
	if err := t.writeJSON(req); err != nil {
		return nil, fmt.Errorf("call %s: write: %w", method, err)
	}

	// Create a cancellable context that supersedes any previous one.
	// The activeCancel is a context.CancelFunc which is idempotent.
	cancelCtx, cancel := context.WithCancel(context.Background())
	t.cancelMu.Lock()
	prev := t.activeCancel
	t.activeCancel = func() {
		cancel()
		if prev != nil {
			prev()
		}
	}
	t.cancelMu.Unlock()
	defer func() {
		t.cancelMu.Lock()
		t.activeCancel = prev
		t.cancelMu.Unlock()
	}()

	// Wait for the response, cancellation, or transport close.
	var raw json.RawMessage
	var ok bool
	select {
	case raw, ok = <-respCh:
	case <-cancelCtx.Done():
		return nil, fmt.Errorf("call %s: cancelled", method)
	}
	if !ok {
		return nil, fmt.Errorf("call %s: transport closed", method)
	}

	var resp RPCResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("call %s: unmarshal response: %w", method, err)
	}
	return &resp, nil
}

// writeJSON serializes and writes a JSON value to stdout (line-delimited).
func (t *Transport) writeJSON(v interface{}) error {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	return t.writer.Encode(v)
}

// writeRawLocked writes raw bytes to stdout (caller must hold writeMu or use dedicated method).
func (t *Transport) writeRawLocked(raw json.RawMessage) {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	t.writer.Encode(json.RawMessage(raw))
}

// ParseMessage parses a raw JSON-RPC message into a RPCRequest for routing.
func ParseMessage(raw json.RawMessage) (*RPCRequest, error) {
	var req RPCRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	return &req, nil
}

// ExtractMethod extracts the method field from a raw message.
func ExtractMethod(raw json.RawMessage) string {
	var check struct {
		Method string `json:"method"`
	}
	json.Unmarshal(raw, &check)
	return check.Method
}

// ExtractParams extracts the params field.
func ExtractParams(raw json.RawMessage) json.RawMessage {
	var check struct {
		Params json.RawMessage `json:"params"`
	}
	json.Unmarshal(raw, &check)
	return check.Params
}
