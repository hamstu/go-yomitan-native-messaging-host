package main

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	ADDR                              = "127.0.0.1"
	PORT                              = 19633
	PROCESS_STARTUP_WAIT              = 5 * time.Second
	YOMITAN_API_NATIVE_MESSAGING_VERSION = 1
)

var (
	blacklistedPaths = []string{"favicon.ico"}
	scriptPath       string
	crowbarFilePath  string
	errorLogPath     string
	errorLogMutex    sync.Mutex
)

func init() {
	ex, err := os.Executable()
	if err != nil {
		panic(err)
	}
	scriptPath = filepath.Dir(ex)
	crowbarFilePath = filepath.Join(scriptPath, ".crowbar")
	errorLogPath = filepath.Join(scriptPath, "error.log")
}

func errorLog(message string, err error) {
	errorLogMutex.Lock()
	defer errorLogMutex.Unlock()

	file, fileErr := os.OpenFile(errorLogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if fileErr != nil {
		return
	}
	defer file.Close()

	utcTime := time.Now().UTC().Format("2006-01-02_15-04-05")
	errStr := ""
	if err != nil {
		errStr = err.Error()
	}

	logEntry := fmt.Sprintf("%s, %s, %s\n",
		utcTime,
		strings.ReplaceAll(strings.ReplaceAll(message, "\r", `\r`), "\n", `\n`),
		strings.ReplaceAll(strings.ReplaceAll(errStr, "\r", `\r`), "\n", `\n`),
	)

	file.WriteString(logEntry)
}

func ensureSingleInstance() {
	waitTime := time.Duration(0)

	// Try to read existing PID and kill the process
	if pidData, err := os.ReadFile(crowbarFilePath); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(pidData))); err == nil {
			// Try to kill the existing process
			if process, err := os.FindProcess(pid); err == nil {
				process.Signal(syscall.SIGTERM)
				waitTime = PROCESS_STARTUP_WAIT
			}
		}
	}

	// Write our PID
	if err := os.WriteFile(crowbarFilePath, []byte(strconv.Itoa(os.Getpid())), 0644); err != nil {
		errorLog("Failed to write crowbar file", err)
	}

	if waitTime > 0 {
		time.Sleep(waitTime)
	}
}

func deleteCrowbarFile() {
	os.Remove(crowbarFilePath)
}

// Native messaging protocol types
type NativeMessage struct {
	Action string              `json:"action"`
	Params map[string][]string `json:"params"`
	Body   string              `json:"body"`
}

type YomitanResponse struct {
	ResponseStatusCode int                 `json:"responseStatusCode"`
	Data               any                 `json:"data"`
	Params             map[string][]string `json:"params,omitempty"`
}

// readMessage reads a native messaging message from the given reader.
func readMessage(r io.Reader) (*YomitanResponse, error) {
	// Read message length (4 bytes, native endian)
	lengthBytes := make([]byte, 4)
	if _, err := io.ReadFull(r, lengthBytes); err != nil {
		if err == io.EOF {
			return nil, nil // Clean exit
		}
		return nil, err
	}

	messageLength := binary.NativeEndian.Uint32(lengthBytes)

	// Read message content
	messageBytes := make([]byte, messageLength)
	if _, err := io.ReadFull(r, messageBytes); err != nil {
		return nil, err
	}

	var response YomitanResponse
	if err := json.Unmarshal(messageBytes, &response); err != nil {
		return nil, err
	}

	return &response, nil
}

// writeMessage writes a native messaging message to the given writer.
func writeMessage(w io.Writer, message interface{}) error {
	messageBytes, err := json.Marshal(message)
	if err != nil {
		return err
	}

	// Write message length (4 bytes, native endian)
	lengthBytes := make([]byte, 4)
	binary.NativeEndian.PutUint32(lengthBytes, uint32(len(messageBytes)))

	if _, err := w.Write(lengthBytes); err != nil {
		return err
	}

	if _, err := w.Write(messageBytes); err != nil {
		return err
	}

	return nil
}

// requestIDParam is added to every forwarded request's params. Yomitan echoes a request's params
// in its reply, so the id routes each reply to its caller even when replies arrive out of order.
const requestIDParam = "_requestId"

const responseTimeout = 30 * time.Second

type pendingRequest struct {
	id    string
	reply chan YomitanResponse
}

// bridge multiplexes HTTP requests over the one native-messaging port.
//
// Yomitan handles each message as it arrives and replies when that message's work finishes, so a
// slow request (an ankiFields call that downloads audio can take seconds) must not hold up a fast
// one (a tokenize takes milliseconds). Requests are written as they come and replies are matched
// by the echoed request id.
//
// A Yomitan that does not echo params would make out-of-order replies unroutable, so until a reply
// has carried an id back the bridge keeps one request in flight and matches replies in order.
type bridge struct {
	writeMu sync.Mutex
	w       io.Writer

	serial  sync.Mutex // held for a whole round trip until echoing is confirmed
	echoes  atomic.Bool
	mu      sync.Mutex
	nextID  uint64
	pending []pendingRequest
}

func newBridge(w io.Writer) *bridge {
	return &bridge{w: w}
}

// roundTrip forwards one request to Yomitan and waits for its reply.
func (b *bridge) roundTrip(message NativeMessage, timeout time.Duration) (YomitanResponse, error) {
	if !b.echoes.Load() {
		b.serial.Lock()
		defer b.serial.Unlock()
	}

	b.mu.Lock()
	b.nextID++
	id := strconv.FormatUint(b.nextID, 10)
	reply := make(chan YomitanResponse, 1)
	b.pending = append(b.pending, pendingRequest{id: id, reply: reply})
	b.mu.Unlock()

	params := make(map[string][]string, len(message.Params)+1)
	for key, values := range message.Params {
		params[key] = values
	}
	params[requestIDParam] = []string{id}
	message.Params = params

	b.writeMu.Lock()
	err := writeMessage(b.w, message)
	b.writeMu.Unlock()
	if err != nil {
		b.forget(id)
		return YomitanResponse{}, err
	}

	select {
	case response := <-reply:
		return response, nil
	case <-time.After(timeout):
		b.forget(id)
		return YomitanResponse{}, errTimeout
	}
}

var errTimeout = fmt.Errorf("timed out waiting for Yomitan")

// forget drops a request that will not wait for its reply. Without echoed ids a late reply is
// matched by position, so the slot stays and absorbs that reply instead of misrouting it.
func (b *bridge) forget(id string) {
	if !b.echoes.Load() {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, p := range b.pending {
		if p.id == id {
			b.pending = append(b.pending[:i], b.pending[i+1:]...)
			return
		}
	}
}

// deliver routes one reply from Yomitan to the request it answers.
func (b *bridge) deliver(response YomitanResponse) {
	var id string
	if ids := response.Params[requestIDParam]; len(ids) > 0 {
		id = ids[0]
		b.echoes.Store(true)
	}
	response.Params = nil

	b.mu.Lock()
	defer b.mu.Unlock()
	index := -1
	if id != "" {
		for i, p := range b.pending {
			if p.id == id {
				index = i
				break
			}
		}
	} else if len(b.pending) > 0 {
		index = 0
	}
	if index < 0 {
		return // its caller gave up
	}
	p := b.pending[index]
	b.pending = append(b.pending[:index], b.pending[index+1:]...)
	p.reply <- response
}

// run reads replies until the browser closes the port.
func (b *bridge) run(r io.Reader) error {
	reader := bufio.NewReader(r)
	for {
		response, err := readMessage(reader)
		if err != nil {
			return err
		}
		if response == nil {
			return nil
		}
		b.deliver(*response)
	}
}

// HTTP request handler
type requestHandler struct {
	bridge *bridge
}

func (h *requestHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Set CORS headers
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "*")
	w.Header().Set("Access-Control-Allow-Headers", "*")
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")

	// Handle OPTIONS requests for CORS
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Only handle POST requests
	if r.Method != "POST" {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	// Parse URL and extract path
	parsedURL, err := url.Parse(r.URL.String())
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	path := strings.TrimPrefix(parsedURL.Path, "/")

	// Check blacklisted paths
	for _, blacklisted := range blacklistedPaths {
		if path == blacklisted {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
	}

	// Handle serverVersion endpoint
	if path == "serverVersion" || path == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]int{"version": YOMITAN_API_NATIVE_MESSAGING_VERSION})
		return
	}

	// Read request body
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// Parse query parameters
	params := parsedURL.Query()
	paramsMap := make(map[string][]string)
	for key, values := range params {
		paramsMap[key] = values
	}

	message := NativeMessage{
		Action: path,
		Params: paramsMap,
		Body:   string(bodyBytes),
	}

	response, err := h.bridge.roundTrip(message, responseTimeout)
	if err == errTimeout {
		w.WriteHeader(http.StatusGatewayTimeout)
		return
	}
	if err != nil {
		errorLog("Failed to send message", err)
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(response.ResponseStatusCode)

	// Marshal response data with ensure_ascii=false equivalent (UTF-8)
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	encoder.Encode(response.Data)
}

func main() {
	// Handle "install" subcommand
	if len(os.Args) >= 2 && os.Args[1] == "install" {
		runInstall(os.Args[2:])
		return
	}

	// Set up signal handling for cleanup
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Ensure single instance
	ensureSingleInstance()
	defer deleteCrowbarFile()

	b := newBridge(os.Stdout)
	server := &http.Server{
		Addr:    fmt.Sprintf("%s:%d", ADDR, PORT),
		Handler: &requestHandler{bridge: b},
	}

	// Start HTTP server in goroutine
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errorLog("HTTP server error", err)
			os.Exit(1)
		}
	}()

	// Replies from the browser extension; the port closing ends the host.
	go func() {
		if err := b.run(os.Stdin); err != nil {
			errorLog("Failed to get message", err)
		}
		server.Close()
		deleteCrowbarFile()
		os.Exit(0)
	}()

	// Wait for signal
	<-sigChan
	server.Close()
}
