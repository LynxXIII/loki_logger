package lokilogger

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
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
	Name          string // Service name used for identification of logs in Loki.
	URL           string // Loki API server endpoint URL.
	BatchSize     int    // Number of logs to batch before sending to Loki.
	AccessToken   string // Authentication token for accessing the Loki API.
	FlushInterval time.Duration
	RetryCount    int
}

// LokiLogger Structure represents Loki Log Logger.
type LokiStream struct {
	Stream map[string]string `json:"stream"` // Key-value pairs to identify log stream.
	Values [][2]string       `json:"values"` // Array of log values with timestamp and log message.
}

// LokiLogger Structure represents a logger to Loki.
type LokiLogger struct {
	ctx   context.Context
	mu    sync.Mutex // Mutex to protect concurrent access to LokiLogger resources.
	conn  net.Conn   // TCP connection to Loki API server.
	logs  []string   // Slice to store logs before sending to Loki.
	cfg   Config
	url   *url.URL
	timer *time.Timer
}

// Initializes.
func Init(ctx context.Context, cfg Config) error {
	// Configure log flags for standard flags, timestamp, and file short name.
	log.SetFlags(log.LstdFlags | log.LUTC | log.Lmicroseconds | log.Lshortfile)

	parsedURL, err := url.Parse(cfg.URL)
	if err != nil {
		return fmt.Errorf("Error loki parse URL: %v", err)
	}

	// Create a new LokiLogger instance.
	l := &LokiLogger{
		ctx:   ctx,
		logs:  make([]string, 0, cfg.BatchSize),
		cfg:   cfg,
		timer: time.NewTimer(cfg.FlushInterval),
		url:   parsedURL,
	}

	// Establish a TCP connection to the Loki API server.
	if err := l.checkConn(); err != nil {
		return fmt.Errorf("Error loki connection: %v", err)
	}

	go l.worker()

	// Set the LokiLogger as the output destination for the standard log package.
	log.SetOutput(l)

	return nil
}

func (l *LokiLogger) worker() {
	for {
		select {
		case <-l.ctx.Done():
			l.Shutdown()
			return
		case <-l.timer.C:
			if len(l.logs) > 0 {
				l.Flush()
			}
		}
	}
}

func (l *LokiLogger) Shutdown() {
	if !l.timer.Stop() {
		<-l.timer.C
	}
	l.Flush()
}

// isConnAlive checks if the TCP connection to Loki is still alive.
func (l *LokiLogger) isConnAlive() bool {
	if l.conn == nil {
		return false
	}
	// Set a read deadline to check for timeout on the connection.
	if err := l.conn.SetReadDeadline(time.Now().Add(1 * time.Second)); err != nil {
		log.Println(err)
		return false
	}
	// Restore the default read deadline after checking.
	defer func() {
		err := l.conn.SetReadDeadline(time.Time{})
		if err != nil {
			log.Println(err)
		}
	}()

	// Attempt to read a byte from the connection.
	buf := make([]byte, 1)
	_, err := l.conn.Read(buf)

	// If it's a timeout error, the connection is considered alive.
	if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		return true
	}

	// Any other error (EOF, connection reset) - the connection is dead
	return err == nil
}

// prepareLogs prepares the logs for sending to Loki.  Formats logs into Loki-compatible structure.
func (l *LokiLogger) prepareLogs() {
	// Create a LokiStream struct to hold the log data.
	logData := LokiStream{
		Stream: map[string]string{
			"service_name": l.cfg.Name,
			"level":        "info",
		},
	}

	// Iterate through the collected logs.
	for _, val := range l.logs {
		// Split each log message into parts.
		parts := strings.SplitN(val, " ", 3)

		// If the log message doesn't have enough parts, treat it as a simple log.
		if len(parts) < 3 {
			logData.Values = append(logData.Values, [2]string{strconv.Itoa(int(time.Now().UnixNano())), strings.Join(parts, " ")})
		} else {
			// Attempt to parse the timestamp.
			timestamp, err := time.ParseInLocation("2006/01/02 15:04:05", parts[0]+" "+parts[1], time.UTC)
			// If timestamp parsing fails, use the current timestamp.
			if err != nil {
				log.Println(err)
				logData.Values = append(logData.Values, [2]string{strconv.Itoa(int(time.Now().UnixNano())), strings.Join(parts, " ")})
			} else {
				// Add the timestamp and log message to the data.
				logData.Values = append(logData.Values, [2]string{strconv.Itoa(int(timestamp.UnixNano())), strings.TrimSpace(parts[2])})
			}
		}
	}

	// Launch a goroutine to send the logs to Loki in the background.
	go l.sendLogs(&logData)
}

func (l *LokiLogger) checkConn() error {
	if !l.isConnAlive() {
		if l.conn != nil {
			l.conn.Close()
		}

		// Construct the Loki API server address (host:port).
		addr := net.JoinHostPort(l.url.Hostname(), l.url.Port())

		var conn net.Conn
		var err error

		if l.url.Scheme == "https" {
			if conn, err = tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: false}); err != nil {
				return err
			}
		} else {
			if conn, err = net.Dial("tcp", addr); err != nil {
				return err
			}
		}

		l.conn = conn
	}

	return nil
}

// sendLogs sends the prepared log data to the Loki API server.
func (l *LokiLogger) sendLogs(logData *LokiStream) {
	defer func() {
		select {
		case <-l.ctx.Done():
			if l.conn != nil {
				l.conn.Close()
			}
		default:
		}
	}()
	// Marshal the log data into JSON format.
	jsonData, err := json.Marshal(map[string][]LokiStream{
		"streams": {*logData},
	})
	// If JSON marshaling fails, log the error and return.
	if err != nil {
		log.Printf("Error loki marshalling JSON: %v", err)
		return
	}

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(jsonData); err != nil {
		log.Printf("Error loki gzip JSON: %v", err)
		return
	}
	gz.Close()

	addr := net.JoinHostPort(l.url.Hostname(), l.url.Port())

	// Build the HTTP request string.
	request := fmt.Sprintf("POST %s HTTP/1.1\r\n"+
		"Host: %s\r\n"+
		"Authorization: Bearer %s\r\n"+
		"Content-Type: application/json\r\n"+
		"Content-Length: %d\r\n"+
		"Content-Encoding: gzip\r\n"+
		"Connection: keep-alive"+
		"\r\n\r\n%s",
		l.url.Path, addr, l.cfg.AccessToken, buf.Len(), buf.String(),
	)

	var response *bufio.Reader

	for i := range l.cfg.RetryCount {
		if i > 0 {
			time.Sleep(time.Duration(1<<i) * time.Second)
		}

		// Check if the connection is alive and re-establish if needed.
		if err := l.checkConn(); err != nil {
			log.Printf("Error loki checkConn: %v", err)
			continue
		}

		// Send the HTTP request to the Loki API server.
		if _, err := l.conn.Write([]byte(request)); err != nil {
			log.Printf("Error loki send request: %v", err)
			continue
		}

		// Read the Loki API server's response.
		response = bufio.NewReader(l.conn)

		strStatus, err := response.ReadString('\n')
		if err != nil {
			log.Printf("Error loki receive status: %v", err)
			continue
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

		response = nil
	}

	if response == nil {
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

		log.Print(line)
	}
}

// Write implements the io.Writer interface and writes data to the Loki API server.
func (l *LokiLogger) Write(p []byte) (n int, err error) {
	select {
	case <-l.ctx.Done():
		return 0, fmt.Errorf("context cancelled")
	default:
	}

	l.resetAutoFlushTimer()
	// Add the data to the collected logs.
	l.logs = append(l.logs, string(p))

	// If the number of logs reaches the batch size, prepare and send them to Loki.
	if len(l.logs) >= l.cfg.BatchSize {
		l.Flush()
	}

	fmt.Println(strings.TrimSpace(string(p)))

	return len(p), nil
}

// Sends the log data to the Loki API server.
func (l *LokiLogger) Flush() {
	if l == nil || l.conn == nil || len(l.logs) == 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prepareLogs()
	l.logs = l.logs[:0]
}

func (l *LokiLogger) resetAutoFlushTimer() {
	if !l.timer.Stop() {
		select {
		case <-l.timer.C:
		default:
		}
	}
	l.timer.Reset(l.cfg.FlushInterval)
}
