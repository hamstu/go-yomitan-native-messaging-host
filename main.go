package main

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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
	ResponseStatusCode int         `json:"responseStatusCode"`
	Data               any         `json:"data"`
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

// getMessage reads from stdin (used in main loop).
func getMessage() (*YomitanResponse, error) {
	return readMessage(bufio.NewReader(os.Stdin))
}

// sendMessage writes to stdout (used in main loop).
func sendMessage(message interface{}) error {
	return writeMessage(os.Stdout, message)
}

// HTTP request handler
type requestHandler struct {
	messageChan chan<- NativeMessage
	responseChan <-chan YomitanResponse
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

	// Send message to native messaging channel
	message := NativeMessage{
		Action: path,
		Params: paramsMap,
		Body:   string(bodyBytes),
	}

	select {
	case h.messageChan <- message:
		// Message sent successfully
	case <-time.After(5 * time.Second):
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}

	// Wait for response from Yomitan
	select {
	case response := <-h.responseChan:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(response.ResponseStatusCode)

		// Marshal response data with ensure_ascii=false equivalent (UTF-8)
		encoder := json.NewEncoder(w)
		encoder.SetEscapeHTML(false)
		encoder.Encode(response.Data)

	case <-time.After(30 * time.Second):
		w.WriteHeader(http.StatusGatewayTimeout)
	}
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

	// Create channels for communication between HTTP server and native messaging
	messageChan := make(chan NativeMessage, 1)
	responseChan := make(chan YomitanResponse, 1)

	// Start HTTP server
	handler := &requestHandler{
		messageChan:  messageChan,
		responseChan: responseChan,
	}

	server := &http.Server{
		Addr:    fmt.Sprintf("%s:%d", ADDR, PORT),
		Handler: handler,
	}

	// Start HTTP server in goroutine
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errorLog("HTTP server error", err)
			os.Exit(1)
		}
	}()

	// Main loop for native messaging
	go func() {
		for {
			select {
			case message := <-messageChan:
				// Send message to browser extension
				if err := sendMessage(message); err != nil {
					errorLog("Failed to send message", err)
					continue
				}

				// Read response from browser extension
				response, err := getMessage()
				if err != nil {
					errorLog("Failed to get message", err)
					// Send error response
					responseChan <- YomitanResponse{
						ResponseStatusCode: 500,
						Data:              map[string]string{"error": "Failed to communicate with extension"},
					}
					continue
				}

				if response == nil {
					// EOF or clean exit
					os.Exit(0)
				}

				// Send response back to HTTP handler
				responseChan <- *response

			case <-sigChan:
				log.Println("Received shutdown signal")
				server.Close()
				deleteCrowbarFile()
				os.Exit(0)
			}
		}
	}()

	// Wait for signal
	<-sigChan
	server.Close()
}
