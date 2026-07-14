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

	// calls tracks in-flight outbound requests (e.g. session/request_permission)
	// by their session so CancelSession can abort exactly the waits belonging to
	// a cancelled session — without disturbing concurrent sessions.
	callsMu sync.Mutex
	calls   map[int64]*outboundCall

	// Dispatcher channels.
	requestCh      chan *dispatchItem
	notificationCh chan json.RawMessage
	done           chan struct{}
}

type dispatchItem struct {
	Raw json.RawMessage
	Req *RPCRequest
}

// outboundCall is an in-flight request to the client, tagged with the session
// on whose behalf it was sent so it can be cancelled per-session.
type outboundCall struct {
	sessionID string
	cancel    context.CancelFunc
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
		calls:          make(map[int64]*outboundCall),
		requestCh:      make(chan *dispatchItem, 8),
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
			t.writeRaw(json.RawMessage(fmt.Sprintf(
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
		// Notification: no id. Block rather than drop — a lost session/cancel
		// silently fails to interrupt a turn. Client responses are delivered
		// inline via the pending map (below), so blocking here cannot deadlock
		// a handler that is awaiting one.
		t.notificationCh <- raw
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
	// Block rather than drop — a dropped request leaves the client waiting
	// forever for a response. Responses are delivered inline via the pending
	// map above, so this cannot deadlock a handler awaiting a client response.
	t.requestCh <- &dispatchItem{Raw: raw, Req: req}
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
	if t.pending == nil {
		// Dispatcher has already shut down (EOF). Return a closed channel so the
		// caller observes ok=false ("transport closed") instead of writing to a
		// nil map and panicking.
		t.pendingMu.Unlock()
		close(ch)
		return ch
	}
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

// CancelSession aborts every in-flight outbound request issued on behalf of
// the given session (e.g. a pending session/request_permission). It leaves
// requests belonging to other sessions untouched, so cancelling one session
// never disturbs a prompt awaiting permission in another.
func (t *Transport) CancelSession(sessionID string) {
	t.callsMu.Lock()
	cancels := make([]context.CancelFunc, 0, len(t.calls))
	for _, c := range t.calls {
		if c.sessionID == sessionID {
			cancels = append(cancels, c.cancel)
		}
	}
	t.callsMu.Unlock()
	for _, fn := range cancels {
		fn()
	}
}

// CallClientMethod sends a request to the client on behalf of sessionID and
// waits for the response. It does NOT read stdin — the dispatcher delivers the
// response via channel. If the session is cancelled (via CancelSession) while
// waiting, it returns an error.
func (t *Transport) CallClientMethod(sessionID, method string, params interface{}) (*RPCResponse, error) {
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

	// Register this call's cancel under its own id, scoped to its session, so
	// CancelSession can reach exactly this wait and it deregisters exactly
	// itself on return — independent of any other concurrent call.
	cancelCtx, cancel := context.WithCancel(context.Background())
	t.callsMu.Lock()
	t.calls[id] = &outboundCall{sessionID: sessionID, cancel: cancel}
	t.callsMu.Unlock()
	defer func() {
		t.callsMu.Lock()
		delete(t.calls, id)
		t.callsMu.Unlock()
		cancel()
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

// writeRaw writes raw bytes to stdout. It acquires writeMu itself, so callers
// must NOT already hold the lock (sync.Mutex is not reentrant).
func (t *Transport) writeRaw(raw json.RawMessage) {
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
