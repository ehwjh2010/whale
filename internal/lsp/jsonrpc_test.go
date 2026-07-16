package lsp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

// --- Reader tests ---

func TestReader_ReadMessage(t *testing.T) {
	input := "Content-Length: 16\r\n\r\n{\"key\": \"value\"}"
	r := NewReader(strings.NewReader(input))
	body, err := r.ReadMessage()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(body) != `{"key": "value"}` {
		t.Fatalf("got %q, want %q", string(body), `{"key": "value"}`)
	}
}

func TestReader_ReadMessage_MultipleHeaders(t *testing.T) {
	input := "Content-Type: application/vscode-jsonrpc; charset=utf-8\r\nContent-Length: 11\r\n\r\nhello world"
	r := NewReader(strings.NewReader(input))
	body, err := r.ReadMessage()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(body) != "hello world" {
		t.Fatalf("got %q, want %q", string(body), "hello world")
	}
}

func TestReader_ReadMessage_MissingContentLength(t *testing.T) {
	input := "Content-Type: text/plain\r\n\r\nbody"
	r := NewReader(strings.NewReader(input))
	_, err := r.ReadMessage()
	if err == nil {
		t.Fatal("expected error for missing Content-Length")
	}
	if !strings.Contains(err.Error(), "no Content-Length") {
		t.Fatalf("expected 'no Content-Length' error, got: %v", err)
	}
}

func TestReader_ReadMessage_InvalidContentLength(t *testing.T) {
	input := "Content-Length: abc\r\n\r\nbody"
	r := NewReader(strings.NewReader(input))
	_, err := r.ReadMessage()
	if err == nil {
		t.Fatal("expected error for invalid Content-Length")
	}
	if !strings.Contains(err.Error(), "invalid Content-Length") {
		t.Fatalf("expected 'invalid Content-Length' error, got: %v", err)
	}
}

func TestReader_ReadMessage_ZeroContentLength(t *testing.T) {
	input := "Content-Length: 0\r\n\r\n"
	r := NewReader(strings.NewReader(input))
	_, err := r.ReadMessage()
	if err == nil {
		t.Fatal("expected error for zero Content-Length")
	}
	if !strings.Contains(err.Error(), "no Content-Length") {
		t.Fatalf("expected 'no Content-Length' error, got: %v", err)
	}
}

func TestReader_ReadMessage_NegativeContentLength(t *testing.T) {
	input := "Content-Length: -5\r\n\r\nbody"
	r := NewReader(strings.NewReader(input))
	_, err := r.ReadMessage()
	if err == nil {
		t.Fatal("expected error for negative Content-Length")
	}
	if !strings.Contains(err.Error(), "no Content-Length") {
		t.Fatalf("expected 'no Content-Length' error, got: %v", err)
	}
}

func TestReader_ReadMessage_TruncatedBody(t *testing.T) {
	input := "Content-Length: 100\r\n\r\nshort"
	r := NewReader(strings.NewReader(input))
	_, err := r.ReadMessage()
	if err == nil {
		t.Fatal("expected error for truncated body")
	}
}

func TestReader_ReadMessage_ClosedStream(t *testing.T) {
	pr, pw := io.Pipe()
	pw.Close()
	r := NewReader(pr)
	_, err := r.ReadMessage()
	if err == nil {
		t.Fatal("expected error on closed stream")
	}
}

// --- Writer tests ---

func TestWriter_WriteMessage(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	err := w.WriteMessage([]byte(`{"key": "value"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := "Content-Length: 16\r\n\r\n{\"key\": \"value\"}"
	if buf.String() != expected {
		t.Fatalf("got %q, want %q", buf.String(), expected)
	}
}

func TestWriter_WriteMessage_EmptyBody(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	err := w.WriteMessage([]byte{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "Content-Length: 0") {
		t.Fatalf("expected Content-Length: 0, got: %s", buf.String())
	}
}

func TestReaderWriter_RoundTrip(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	msg := `{"jsonrpc":"2.0","id":1,"method":"test"}`
	if err := w.WriteMessage([]byte(msg)); err != nil {
		t.Fatalf("write error: %v", err)
	}
	r := NewReader(bytes.NewReader(buf.Bytes()))
	body, err := r.ReadMessage()
	if err != nil {
		t.Fatalf("read error: %v", err)
	}
	if string(body) != msg {
		t.Fatalf("round-trip: got %q, want %q", string(body), msg)
	}
}

func TestWriter_ConcurrentSafety(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	done := make(chan struct{})
	errs := make(chan error, 10)
	for i := 0; i < 10; i++ {
		go func(n int) {
			err := w.WriteMessage([]byte(fmt.Sprintf(`{"n":%d}`, n)))
			if err != nil {
				errs <- err
			}
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < 10; i++ {
		<-done
	}
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent write error: %v", err)
	}
	r := NewReader(bytes.NewReader(buf.Bytes()))
	count := 0
	for {
		_, err := r.ReadMessage()
		if err != nil {
			break
		}
		count++
	}
	if count != 10 {
		t.Fatalf("expected 10 messages, got %d", count)
	}
}

// --- rpcConn tests ---

func TestNewRPCConn(t *testing.T) {
	pr, pw := io.Pipe()
	c := newRPCConn(pr, pw)
	if c == nil {
		t.Fatal("expected non-nil conn")
	}
	if c.reader == nil || c.writer == nil {
		t.Fatal("expected reader and writer")
	}
}

func TestRPCConn_SendRequest_Success(t *testing.T) {
	serverR, clientW := io.Pipe()
	clientR, serverW := io.Pipe()
	serverWriter := NewWriter(serverW)
	clientConn := newRPCConn(clientR, clientW)
	defer serverR.Close()
	defer serverW.Close()

	go func() {
		body, err := NewReader(serverR).ReadMessage()
		if err != nil {
			return
		}
		var req Message
		json.Unmarshal(body, &req)
		resp := Message{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  json.RawMessage(`{"result":"ok"}`),
		}
		respBytes, _ := json.Marshal(resp)
		serverWriter.WriteMessage(respBytes)
	}()
	go clientConn.readLoop()

	var result struct {
		Result string `json:"result"`
	}
	err := clientConn.sendRequest(context.Background(), "testMethod", map[string]string{"foo": "bar"}, &result)
	if err != nil {
		t.Fatalf("sendRequest error: %v", err)
	}
	if result.Result != "ok" {
		t.Fatalf("got result %q, want %q", result.Result, "ok")
	}
}

func TestRPCConn_SendRequest_ErrorResponse(t *testing.T) {
	serverR, clientW := io.Pipe()
	clientR, serverW := io.Pipe()
	serverWriter := NewWriter(serverW)
	clientConn := newRPCConn(clientR, clientW)
	defer serverR.Close()
	defer serverW.Close()

	go func() {
		body, err := NewReader(serverR).ReadMessage()
		if err != nil {
			return
		}
		var req Message
		json.Unmarshal(body, &req)
		resp := Message{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &RPCError{Code: -32601, Message: "Method not found"},
		}
		respBytes, _ := json.Marshal(resp)
		serverWriter.WriteMessage(respBytes)
	}()
	go clientConn.readLoop()

	err := clientConn.sendRequest(context.Background(), "unknown", nil, nil)
	if err == nil {
		t.Fatal("expected RPC error")
	}
	rpcErr, ok := err.(*RPCError)
	if !ok {
		t.Fatalf("expected *RPCError, got %T: %v", err, err)
	}
	if rpcErr.Code != -32601 || rpcErr.Message != "Method not found" {
		t.Fatalf("got error %+v, want code=-32601 msg='Method not found'", rpcErr)
	}
}

func TestRPCError_ErrorString(t *testing.T) {
	e := &RPCError{Code: -32000, Message: "server error"}
	want := "LSP error -32000: server error"
	if e.Error() != want {
		t.Fatalf("RPCError.Error() = %q, want %q", e.Error(), want)
	}
}

func TestRPCConn_SendRequest_ContextCancellation(t *testing.T) {
	var buf bytes.Buffer
	clientConn := newRPCConn(nil, &buf)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := clientConn.sendRequest(ctx, "test", nil, nil)
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
	if !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("expected cancellation error, got: %v", err)
	}
}

func TestRPCConn_SendRequest_ContextTimeout(t *testing.T) {
	var buf2 bytes.Buffer
	clientConn := newRPCConn(nil, &buf2)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()

	err := clientConn.sendRequest(ctx, "test", nil, nil)
	if err == nil {
		t.Fatal("expected error for timeout")
	}
	if !strings.Contains(err.Error(), "cancelled") && !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("expected timeout/cancellation error, got: %v", err)
	}
}

func TestRPCConn_SendNotification(t *testing.T) {
	var buf bytes.Buffer
	clientConn := newRPCConn(nil, &buf)
	err := clientConn.sendNotification("testEvent", map[string]string{"msg": "hello"})
	if err != nil {
		t.Fatalf("sendNotification error: %v", err)
	}
	output := buf.String()
	if !strings.Contains(output, "Content-Length:") {
		t.Fatalf("missing Content-Length header")
	}
	if !strings.Contains(output, "testEvent") {
		t.Fatalf("expected method in output, got: %s", output)
	}
	if strings.Contains(output, `"id":`) {
		t.Fatalf("notification should not have id, got: %s", output)
	}
}

func TestRPCConn_Shutdown_DrainsPending(t *testing.T) {
	pr, pw := io.Pipe()
	clientConn := newRPCConn(pr, pw)
	defer pr.Close()
	defer pw.Close()

	clientConn.mu.Lock()
	ch := make(chan *Message, 1)
	clientConn.pending[42] = ch
	clientConn.mu.Unlock()

	clientConn.shutdown()

	msg, ok := <-ch
	if ok {
		t.Fatalf("expected closed channel, got message: %+v", msg)
	}

	clientConn.mu.Lock()
	if len(clientConn.pending) > 0 {
		t.Fatalf("expected empty pending map, got %d items", len(clientConn.pending))
	}
	clientConn.mu.Unlock()
}
