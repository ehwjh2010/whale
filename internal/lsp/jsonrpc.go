package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"
)

// headerTimeout is how long the reader waits for the Content-Length header line.
const headerTimeout = 30 * time.Second

// Message is a JSON-RPC 2.0 message.
type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError is a JSON-RPC 2.0 error.
type RPCError struct {
	Code    int            `json:"code"`
	Message string         `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("LSP error %d: %s", e.Code, e.Message)
}

// Reader decodes Content-Length-framed JSON messages from an io.Reader.
type Reader struct {
	br *bufio.Reader
}

// NewReader creates a Reader wrapping the given io.Reader.
func NewReader(r io.Reader) *Reader {
	return &Reader{br: bufio.NewReader(r)}
}

// ReadMessage reads one Content-Length-framed message. It blocks until a
// complete message is available or the reader is closed.
func (r *Reader) ReadMessage() ([]byte, error) {
	var contentLength int64
	for {
		line, err := r.br.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("read header: %w", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			// Empty line = end of headers, body follows
			break
		}
		if strings.HasPrefix(line, "Content-Length:") {
			v := strings.TrimSpace(strings.TrimPrefix(line, "Content-Length:"))
			contentLength, err = strconv.ParseInt(v, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid Content-Length %q: %w", v, err)
			}
		}
		// Ignore other headers (Content-Type, etc.)
	}
	if contentLength <= 0 {
		return nil, fmt.Errorf("no Content-Length header in message")
	}
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(r.br, body); err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	return body, nil
}

// Writer encodes JSON messages with Content-Length framing to an io.Writer.
type Writer struct {
	mu sync.Mutex
	w  io.Writer
}

// NewWriter creates a Writer wrapping the given io.Writer.
func NewWriter(w io.Writer) *Writer {
	return &Writer{w: w}
}

// WriteMessage marshals data as JSON and writes it with Content-Length framing.
func (w *Writer) WriteMessage(data []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(data))
	if _, err := w.w.Write([]byte(header)); err != nil {
		return fmt.Errorf("write header: %w", err)
	}
	if _, err := w.w.Write(data); err != nil {
		return fmt.Errorf("write body: %w", err)
	}
	return nil
}

// rpcConn manages JSON-RPC request/response correlation over a Reader/Writer pair.
type rpcConn struct {
	reader  *Reader
	writer  *Writer
	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan *Message
}

func newRPCConn(r io.Reader, w io.Writer) *rpcConn {
	return &rpcConn{
		reader:  NewReader(r),
		writer:  NewWriter(w),
		pending: make(map[int64]chan *Message),
	}
}

// sendRequest sends a JSON-RPC request and waits for the response.
// ctx is used for cancellation/timeout of the blocking wait.
func (c *rpcConn) sendRequest(ctx context.Context, method string, params any, result any) error {
	c.mu.Lock()
	id := c.nextID
	c.nextID++
	ch := make(chan *Message, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal params: %w", err)
	}

	req := Message{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  method,
		Params:  paramsJSON,
	}
	reqBytes, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	if err := c.writer.WriteMessage(reqBytes); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return fmt.Errorf("write request: %w", err)
	}

	select {
	case msg := <-ch:
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		if msg == nil {
			return fmt.Errorf("connection closed")
		}
		if msg.Error != nil {
			return msg.Error
		}
		if result != nil {
			if err := json.Unmarshal(msg.Result, result); err != nil {
				return fmt.Errorf("unmarshal result: %w", err)
			}
		}
		return nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return fmt.Errorf("request cancelled: %w", ctx.Err())
	}
}

// sendNotification sends a JSON-RPC notification (no response expected).
func (c *rpcConn) sendNotification(method string, params any) error {
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal params: %w", err)
	}
	req := Message{
		JSONRPC: "2.0",
		Method:  method,
		Params:  paramsJSON,
	}
	reqBytes, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal notification: %w", err)
	}
	return c.writer.WriteMessage(reqBytes)
}

// readLoop reads incoming messages from the server and routes responses.
func (c *rpcConn) readLoop() error {
	for {
		body, err := c.reader.ReadMessage()
		if err != nil {
			return err
		}
		var msg Message
		if err := json.Unmarshal(body, &msg); err != nil {
			continue // skip malformed messages
		}
		if msg.ID != nil {
			c.mu.Lock()
			ch, ok := c.pending[*msg.ID]
			if ok {
				delete(c.pending, *msg.ID)
			}
			c.mu.Unlock()
			if ok {
				ch <- &msg
			}
		}
		// Notifications (no ID) are silently ignored.
	}
}

// shutdown drains pending channels and sends nil to unblock callers.
func (c *rpcConn) shutdown() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
}
