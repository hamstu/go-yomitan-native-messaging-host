package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

// fakeExtension stands in for Yomitan on the other end of the native-messaging port. Each
// message it receives is passed to answer on its own goroutine, as Yomitan handles each message
// independently; answer returns the reply to send, or nil to send none.
func newTestHandler(t *testing.T, answer func(NativeMessage) *YomitanResponse) *requestHandler {
	t.Helper()
	toExt, hostOut := io.Pipe()
	hostIn, fromExt := io.Pipe()
	b := newBridge(hostOut)
	go b.run(hostIn)

	var writeMu sync.Mutex
	go func() {
		reader := bufio.NewReader(toExt)
		for {
			lengthBytes := make([]byte, 4)
			if _, err := io.ReadFull(reader, lengthBytes); err != nil {
				return
			}
			payload := make([]byte, binary.NativeEndian.Uint32(lengthBytes))
			if _, err := io.ReadFull(reader, payload); err != nil {
				return
			}
			var msg NativeMessage
			if err := json.Unmarshal(payload, &msg); err != nil {
				t.Errorf("extension got invalid JSON: %v", err)
				return
			}
			go func() {
				reply := answer(msg)
				if reply == nil {
					return
				}
				writeMu.Lock()
				defer writeMu.Unlock()
				writeMessage(fromExt, reply)
			}()
		}
	}()
	t.Cleanup(func() {
		toExt.Close()
		fromExt.Close()
	})
	return &requestHandler{bridge: b}
}

// echo answers like Yomitan does: with the request's params alongside the data.
func echo(msg NativeMessage, status int, data any) *YomitanResponse {
	return &YomitanResponse{ResponseStatusCode: status, Data: data, Params: msg.Params}
}

func noAnswer(NativeMessage) *YomitanResponse { return nil }

func TestServerVersionEndpoint(t *testing.T) {
	h := newTestHandler(t, noAnswer)

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
	h := newTestHandler(t, noAnswer)

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
	h := newTestHandler(t, noAnswer)

	req := httptest.NewRequest("OPTIONS", "/anything", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("OPTIONS: status = %d, want 200", w.Code)
	}
}

func TestGetRequestRejected(t *testing.T) {
	h := newTestHandler(t, noAnswer)

	req := httptest.NewRequest("GET", "/serverVersion", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestBlacklistedPath(t *testing.T) {
	h := newTestHandler(t, noAnswer)

	req := httptest.NewRequest("POST", "/favicon.ico", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("blacklisted: status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestCustomActionForwarding(t *testing.T) {
	h := newTestHandler(t, func(msg NativeMessage) *YomitanResponse {
		if msg.Action != "customAction" {
			t.Errorf("action = %q, want 'customAction'", msg.Action)
		}
		if msg.Body != `{"key":"value"}` {
			t.Errorf("body = %q", msg.Body)
		}
		if msg.Params["foo"] == nil || msg.Params["foo"][0] != "bar" {
			t.Errorf("params = %v, want foo=bar", msg.Params)
		}
		return echo(msg, 200, "ok")
	})

	req := httptest.NewRequest("POST", "/customAction?foo=bar", strings.NewReader(`{"key":"value"}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("custom action: status = %d, want 200", w.Code)
	}
	if got := strings.TrimSpace(w.Body.String()); got != `"ok"` {
		t.Errorf("body = %s, want \"ok\"", got)
	}
}

func TestResponseTimeout(t *testing.T) {
	h := newTestHandler(t, noAnswer)
	_, err := h.bridge.roundTrip(NativeMessage{Action: "slowAction", Body: "{}"}, 50*time.Millisecond)
	if err != errTimeout {
		t.Fatalf("err = %v, want errTimeout", err)
	}
}

func TestResponseForwardsStatusCode(t *testing.T) {
	h := newTestHandler(t, func(msg NativeMessage) *YomitanResponse {
		return echo(msg, 404, "not found")
	})

	req := httptest.NewRequest("POST", "/something", strings.NewReader(""))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 404 {
		t.Errorf("forwarded status = %d, want 404", w.Code)
	}
}

// A slow request must not hold up a fast one once Yomitan has shown it echoes request ids.
func TestSlowRequestDoesNotBlockFastOne(t *testing.T) {
	release := make(chan struct{})
	h := newTestHandler(t, func(msg NativeMessage) *YomitanResponse {
		if msg.Action == "slow" {
			<-release
		}
		return echo(msg, 200, msg.Action)
	})
	post := func(action string) string {
		req := httptest.NewRequest("POST", "/"+action, strings.NewReader("{}"))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return strings.TrimSpace(w.Body.String())
	}

	// The first reply confirms echoing.
	if got := post("warmup"); got != `"warmup"` {
		t.Fatalf("warmup = %s", got)
	}

	slow := make(chan string, 1)
	go func() { slow <- post("slow") }()
	time.Sleep(20 * time.Millisecond)

	fast := make(chan string, 1)
	go func() { fast <- post("fast") }()
	select {
	case got := <-fast:
		if got != `"fast"` {
			t.Errorf("fast = %s, want \"fast\"", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fast request waited behind the slow one")
	}

	close(release)
	if got := <-slow; got != `"slow"` {
		t.Errorf("slow = %s, want \"slow\"", got)
	}
}

// Many concurrent requests each get their own reply, whatever order Yomitan answers in.
func TestConcurrentRepliesRoutedById(t *testing.T) {
	h := newTestHandler(t, func(msg NativeMessage) *YomitanResponse {
		time.Sleep(time.Duration(len(msg.Body)%7) * time.Millisecond)
		return echo(msg, 200, msg.Body)
	})
	h.bridge.roundTrip(NativeMessage{Action: "warmup", Body: "{}"}, time.Second)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := strings.Repeat("x", i)
			got, err := h.bridge.roundTrip(NativeMessage{Action: "a", Body: body}, 2*time.Second)
			if err != nil {
				t.Errorf("request %d: %v", i, err)
				return
			}
			if got.Data != body {
				t.Errorf("request %d got the reply for %q", i, got.Data)
			}
		}(i)
	}
	wg.Wait()
}

// Without echoed ids, replies are matched in order, so only one request may be in flight.
func TestWithoutEchoRequestsAreSerial(t *testing.T) {
	var inFlight, maxInFlight atomic.Int32
	h := newTestHandler(t, func(msg NativeMessage) *YomitanResponse {
		n := inFlight.Add(1)
		for {
			m := maxInFlight.Load()
			if n <= m || maxInFlight.CompareAndSwap(m, n) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		inFlight.Add(-1)
		return &YomitanResponse{ResponseStatusCode: 200, Data: msg.Body}
	})

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := strconv.Itoa(i)
			got, err := h.bridge.roundTrip(NativeMessage{Action: "a", Body: body}, 2*time.Second)
			if err != nil || got.Data != body {
				t.Errorf("request %d: got %v, %v", i, got.Data, err)
			}
		}(i)
	}
	wg.Wait()
	if maxInFlight.Load() != 1 {
		t.Errorf("max in flight = %d, want 1", maxInFlight.Load())
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
