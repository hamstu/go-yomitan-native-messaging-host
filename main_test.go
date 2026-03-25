package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// --- Native messaging protocol tests ---

func encodeNativeMessage(t *testing.T, v interface{}) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4+len(data))
	binary.NativeEndian.PutUint32(buf[:4], uint32(len(data)))
	copy(buf[4:], data)
	return buf
}

func TestReadMessage(t *testing.T) {
	resp := YomitanResponse{ResponseStatusCode: 200, Data: "hello"}
	encoded := encodeNativeMessage(t, resp)

	got, err := readMessage(bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("readMessage: %v", err)
	}
	if got == nil {
		t.Fatal("readMessage returned nil")
	}
	if got.ResponseStatusCode != 200 {
		t.Errorf("status = %d, want 200", got.ResponseStatusCode)
	}
	if got.Data != "hello" {
		t.Errorf("data = %v, want 'hello'", got.Data)
	}
}

func TestReadMessageEOF(t *testing.T) {
	got, err := readMessage(bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("expected nil error on EOF, got %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil response on EOF, got %v", got)
	}
}

func TestReadMessageTruncated(t *testing.T) {
	// Length header says 100 bytes, but only 5 bytes of payload follow
	buf := make([]byte, 9)
	binary.NativeEndian.PutUint32(buf[:4], 100)
	copy(buf[4:], []byte("short"))

	_, err := readMessage(bytes.NewReader(buf))
	if err == nil {
		t.Fatal("expected error for truncated message")
	}
}

func TestReadMessageInvalidJSON(t *testing.T) {
	payload := []byte("not json!")
	buf := make([]byte, 4+len(payload))
	binary.NativeEndian.PutUint32(buf[:4], uint32(len(payload)))
	copy(buf[4:], payload)

	_, err := readMessage(bytes.NewReader(buf))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestWriteMessage(t *testing.T) {
	msg := NativeMessage{Action: "test", Body: "body"}
	var buf bytes.Buffer

	if err := writeMessage(&buf, msg); err != nil {
		t.Fatalf("writeMessage: %v", err)
	}

	// Decode what was written
	b := buf.Bytes()
	if len(b) < 4 {
		t.Fatal("output too short")
	}
	length := binary.NativeEndian.Uint32(b[:4])
	payload := b[4:]
	if uint32(len(payload)) != length {
		t.Errorf("length header = %d, payload = %d bytes", length, len(payload))
	}

	var decoded NativeMessage
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Action != "test" || decoded.Body != "body" {
		t.Errorf("decoded = %+v, want action=test body=body", decoded)
	}
}

func TestRoundTrip(t *testing.T) {
	original := YomitanResponse{
		ResponseStatusCode: 201,
		Data:               map[string]interface{}{"key": "日本語テスト"},
	}

	var buf bytes.Buffer
	if err := writeMessage(&buf, original); err != nil {
		t.Fatalf("writeMessage: %v", err)
	}

	got, err := readMessage(&buf)
	if err != nil {
		t.Fatalf("readMessage: %v", err)
	}
	if got.ResponseStatusCode != 201 {
		t.Errorf("status = %d, want 201", got.ResponseStatusCode)
	}
	data, ok := got.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("data type = %T, want map", got.Data)
	}
	if data["key"] != "日本語テスト" {
		t.Errorf("data[key] = %v, want 日本語テスト", data["key"])
	}
}

func TestMultipleMessages(t *testing.T) {
	var buf bytes.Buffer
	for i := 0; i < 5; i++ {
		msg := YomitanResponse{ResponseStatusCode: 200 + i, Data: i}
		if err := writeMessage(&buf, msg); err != nil {
			t.Fatalf("writeMessage %d: %v", i, err)
		}
	}

	for i := 0; i < 5; i++ {
		got, err := readMessage(&buf)
		if err != nil {
			t.Fatalf("readMessage %d: %v", i, err)
		}
		if got.ResponseStatusCode != 200+i {
			t.Errorf("message %d: status = %d, want %d", i, got.ResponseStatusCode, 200+i)
		}
	}

	// Next read should return nil (EOF)
	got, err := readMessage(&buf)
	if err != nil {
		t.Fatalf("expected nil error after all messages, got %v", err)
	}
	if got != nil {
		t.Fatal("expected nil after all messages consumed")
	}
}

// --- HTTP handler tests ---

func newTestHandler() (*requestHandler, chan NativeMessage, chan YomitanResponse) {
	msgCh := make(chan NativeMessage, 1)
	respCh := make(chan YomitanResponse, 1)
	h := &requestHandler{messageChan: msgCh, responseChan: respCh}
	return h, msgCh, respCh
}

func TestServerVersionEndpoint(t *testing.T) {
	h, _, _ := newTestHandler()

	for _, path := range []string{"/serverVersion", "/"} {
		req := httptest.NewRequest("POST", path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)

		if w.Code != 200 {
			t.Errorf("POST %s: status = %d, want 200", path, w.Code)
			continue
		}

		var body map[string]int
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Errorf("POST %s: invalid JSON: %v", path, err)
			continue
		}
		if body["version"] != YOMITAN_API_NATIVE_MESSAGING_VERSION {
			t.Errorf("POST %s: version = %d, want %d", path, body["version"], YOMITAN_API_NATIVE_MESSAGING_VERSION)
		}
	}
}

func TestCORSHeaders(t *testing.T) {
	h, _, _ := newTestHandler()

	req := httptest.NewRequest("POST", "/serverVersion", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	headers := map[string]string{
		"Access-Control-Allow-Origin":  "*",
		"Access-Control-Allow-Methods": "*",
		"Access-Control-Allow-Headers": "*",
	}
	for key, want := range headers {
		if got := w.Header().Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestOptionsRequest(t *testing.T) {
	h, _, _ := newTestHandler()

	req := httptest.NewRequest("OPTIONS", "/anything", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("OPTIONS: status = %d, want 200", w.Code)
	}
}

func TestGetRequestRejected(t *testing.T) {
	h, _, _ := newTestHandler()

	req := httptest.NewRequest("GET", "/serverVersion", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestBlacklistedPath(t *testing.T) {
	h, _, _ := newTestHandler()

	req := httptest.NewRequest("POST", "/favicon.ico", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("blacklisted: status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestCustomActionForwarding(t *testing.T) {
	h, msgCh, respCh := newTestHandler()

	// Pre-load a response so the handler doesn't block
	go func() {
		msg := <-msgCh
		if msg.Action != "customAction" {
			t.Errorf("action = %q, want 'customAction'", msg.Action)
		}
		if msg.Body != `{"key":"value"}` {
			t.Errorf("body = %q", msg.Body)
		}
		if msg.Params["foo"] == nil || msg.Params["foo"][0] != "bar" {
			t.Errorf("params = %v, want foo=bar", msg.Params)
		}
		respCh <- YomitanResponse{ResponseStatusCode: 200, Data: "ok"}
	}()

	req := httptest.NewRequest("POST", "/customAction?foo=bar", strings.NewReader(`{"key":"value"}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("custom action: status = %d, want 200", w.Code)
	}
}

func TestMessageChannelTimeout(t *testing.T) {
	// messageChan with zero buffer and nobody reading — should timeout
	msgCh := make(chan NativeMessage) // unbuffered, no reader
	respCh := make(chan YomitanResponse, 1)
	h := &requestHandler{messageChan: msgCh, responseChan: respCh}

	req := httptest.NewRequest("POST", "/slowAction", strings.NewReader("{}"))
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		h.ServeHTTP(w, req)
		close(done)
	}()

	select {
	case <-done:
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("timeout: status = %d, want %d", w.Code, http.StatusServiceUnavailable)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("handler did not timeout")
	}
}

func TestResponseForwardsStatusCode(t *testing.T) {
	h, msgCh, respCh := newTestHandler()

	go func() {
		<-msgCh
		respCh <- YomitanResponse{ResponseStatusCode: 404, Data: "not found"}
	}()

	req := httptest.NewRequest("POST", "/something", strings.NewReader(""))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 404 {
		t.Errorf("forwarded status = %d, want 404", w.Code)
	}
}

// --- readMessage from io.Pipe (simulates streaming stdin) ---

func TestReadMessageFromPipe(t *testing.T) {
	pr, pw := io.Pipe()

	resp := YomitanResponse{ResponseStatusCode: 200, Data: "piped"}

	go func() {
		writeMessage(pw, resp)
		pw.Close()
	}()

	got, err := readMessage(pr)
	if err != nil {
		t.Fatalf("readMessage from pipe: %v", err)
	}
	if got.ResponseStatusCode != 200 {
		t.Errorf("status = %d, want 200", got.ResponseStatusCode)
	}
}
