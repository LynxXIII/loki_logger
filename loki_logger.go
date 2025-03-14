package lokilogger

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Config Structure holds Loki specific configuration parameters.
type Config struct {
	Name        string // Service name used for identification of logs in Loki.
	URL         string // Loki API server endpoint URL.
	BatchSize   int    // Number of logs to batch before sending to Loki.
	AccessToken string // Authentication token for accessing the Loki API.
}

// LokiLogger Structure represents Loki Log Logger.
type LokiStream struct {
	Stream map[string]string `json:"stream"` // Key-value pairs to identify log stream.
	Values [][2]string       `json:"values"` // Array of log values with timestamp and log message.
}

// LokiLogger Structure represents a logger to Loki.
type LokiLogger struct {
	mu          sync.Mutex // Mutex to protect concurrent access to LokiLogger resources.
	serviceName string     // Service name configured in the configuration.
	addr        string     // Loki API server address.
	path        string     // Loki API server path.
	conn        net.Conn   // TCP connection to Loki API server.
	logs        []string   // Slice to store logs before sending to Loki.
	batchSize   int        // Number of logs to batch before sending to Loki.
	accessToken string     // Authentication token for accessing the Loki API.
}

// NewLokiLogger initializes and returns a LokiLogger instance.
func NewLokiLogger(cfg Config) (*LokiLogger, error) {
	// Parse the Loki API server URL.
	parsedURL, err := url.Parse(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("Error loki parse URL: %v", err)
	}

	// Construct the Loki API server address (host:port).
	addr := net.JoinHostPort(parsedURL.Hostname(), parsedURL.Port())

	// Configure log flags for standard flags, timestamp, and file short name.
	log.SetFlags(log.LstdFlags | log.LUTC | log.Lshortfile)

	// Create a new LokiLogger instance.
	lw := &LokiLogger{
		serviceName: cfg.Name,
		addr:        addr,
		path:        parsedURL.Path,
		logs:        make([]string, 0, cfg.BatchSize),
		batchSize:   cfg.BatchSize,
		accessToken: cfg.AccessToken,
	}

	// Establish a TCP connection to the Loki API server.
	if err := lw.checkConn(); err != nil {
		return nil, fmt.Errorf("Error loki connection: %v", err)
	}

	// Set the LokiLogger as the output destination for the standard log package.
	log.SetOutput(lw)

	return lw, nil
}

// isConnAlive checks if the TCP connection to Loki is still alive.
func (w *LokiLogger) isConnAlive() bool {
	if w.conn == nil {
		return false
	}
	// Set a read deadline to check for timeout on the connection.
	if err := w.conn.SetReadDeadline(time.Now().Add(1 * time.Second)); err != nil {
		log.Println(err)
		return false
	}
	// Restore the default read deadline after checking.
	defer func() {
		err := w.conn.SetReadDeadline(time.Time{})
		if err != nil {
			log.Println(err)
		}
	}()

	// Attempt to read a byte from the connection.
	buf := make([]byte, 1)
	_, err := w.conn.Read(buf)

	// If it's a timeout error, the connection is considered alive.
	if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		return true
	}

	// Any other error (EOF, connection reset) - the connection is dead
	return err == nil
}

// prepareLogs prepares the logs for sending to Loki.  Formats logs into Loki-compatible structure.
func (w *LokiLogger) prepareLogs() {
	// Create a LokiStream struct to hold the log data.
	logData := LokiStream{
		Stream: map[string]string{
			"service_name": w.serviceName,
			"level":        "info",
		},
	}

	// Iterate through the collected logs.
	for _, val := range w.logs {
		// Split each log message into parts.
		parts := strings.Split(val, " ")

		// If the log message doesn't have enough parts, treat it as a simple log.
		if len(parts) < 2 {
			logData.Values = append(logData.Values, [2]string{strconv.Itoa(int(time.Now().UnixNano())), strings.Join(parts, " ")})
		} else {
			// Attempt to parse the timestamp.
			timestamp, err := time.ParseInLocation("2006/01/02 15:04:05", parts[0]+" "+parts[1], time.UTC)
			// If timestamp parsing fails, use the current timestamp.
			if err != nil {
				fmt.Println(err)
				logData.Values = append(logData.Values, [2]string{strconv.Itoa(int(time.Now().UnixNano())), strings.Join(parts, " ")})
			} else {
				// Add the timestamp and log message to the data.
				logData.Values = append(logData.Values, [2]string{strconv.Itoa(int(timestamp.UnixNano())), strings.TrimSpace(strings.Join(parts[2:], " "))})
			}
		}
	}

	// Launch a goroutine to send the logs to Loki in the background.
	go w.sendLogs(&logData)
}

func (w *LokiLogger) checkConn() error {
	if !w.isConnAlive() {
		conn, err := net.Dial("tcp", w.addr)
		if err != nil {
			return err
		}

		w.conn = conn
	}

	return nil
}

// sendLogs sends the prepared log data to the Loki API server.
func (w *LokiLogger) sendLogs(logData *LokiStream) {
	// Marshal the log data into JSON format.
	jsonData, err := json.Marshal(map[string][]LokiStream{
		"streams": {*logData},
	})
	// If JSON marshaling fails, log the error and return.
	if err != nil {
		log.Printf("Error loki marshalling JSON: %v", err)
		return
	}

	// Build the HTTP request string.
	request := fmt.Sprintf("POST %s HTTP/1.1\r\n"+
		"Host: %s\r\n"+
		"Authorization: Bearer %s\r\n"+
		"Content-Type: application/json\r\n"+
		"Content-Length: %d\r\n"+
		"Connection: keep-alive"+
		"\r\n\r\n%s",
		w.path, w.addr, w.accessToken, len(jsonData), string(jsonData),
	)

	// Check if the connection is alive and re-establish if needed.
	if err := w.checkConn(); err != nil {
		log.Printf("Error loki checkConn: %v", err)
		return
	}

	// Send the HTTP request to the Loki API server.
	if _, err := w.conn.Write([]byte(request)); err != nil {
		log.Printf("Error loki send request: %v", err)
		return
	}

	// Read the Loki API server's response.
	response := bufio.NewReader(w.conn)

	strStatus, err := response.ReadString('\n')
	if err != nil {
		log.Printf("Error loki receive status: %v", err)
		return
	}

	// Read status response.
	status := strings.Split(strStatus, " ")
	if code, err := strconv.Atoi(status[1]); err != nil {
		log.Printf("Error loki parse code: %v", err)
	} else if code < 200 || code >= 300 {
		log.Printf("Error loki code is: %d", code)
	} else {
		fmt.Println("Logs sent")
		return
	}

	// Read and print the rest of the response.
	for {
		line, err := response.ReadString('\n')
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Printf("Error loki receive response: %v", err)
			return
		}

		fmt.Print(line)
	}
}

// Write implements the io.Writer interface and writes data to the Loki API server.
func (w *LokiLogger) Write(p []byte) (n int, err error) {
	// Add the data to the collected logs.
	w.logs = append(w.logs, string(p))

	// If the number of logs reaches the batch size, prepare and send them to Loki.
	if len(w.logs) >= w.batchSize {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.prepareLogs()
		w.logs = w.logs[:0]
	}

	fmt.Println(strings.TrimSpace(string(p)))
	return len(p), nil
}

// Sends the log data to the Loki API server.
func (w *LokiLogger) Flush() {
	if w.conn == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.prepareLogs()
}
