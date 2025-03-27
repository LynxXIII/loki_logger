package lokilogger

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Config Structure holds Loki specific configuration parameters.
type Config struct {
	BatchSize     int // Number of logs to batch before sending to Loki.
	FlushInterval time.Duration
	Name          string // Service name used for identification of logs in Loki.
	URL           string // Loki API server endpoint URL.
	AccessToken   string // Authentication token for accessing the Loki API.
	RetryCount    int
}

// LokiLogger Structure represents Loki Log Logger.
type LokiStream struct {
	Stream map[string]string `json:"stream,omitempty"` // Key-value pairs to identify log stream.
	Values [][2]string       `json:"values,omitempty"` // Array of log values with timestamp and log message.
}

// LokiLogger Structure represents a logger to Loki.
type LokiLogger struct {
	ctx    context.Context
	mu     sync.Mutex // Mutex to protect concurrent access to LokiLogger resources.
	client *http.Client
	cfg    Config
	logs   []string // Slice to store logs before sending to Loki.
	timer  *time.Timer
}

// Initializes.
func Init(ctx context.Context, cfg Config) error {
	if err := checkUrl(cfg.URL); err != nil {
		return err
	}

	// Configure log flags for standard flags, timestamp, and file short name.
	log.SetFlags(log.LstdFlags | log.LUTC | log.Lmicroseconds | log.Lshortfile)

	// Create a new LokiLogger instance.
	l := &LokiLogger{
		ctx:   ctx,
		logs:  make([]string, 0, cfg.BatchSize),
		cfg:   cfg,
		timer: time.NewTimer(cfg.FlushInterval),
		client: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: true,
				},
				MaxIdleConns:        2,
				IdleConnTimeout:     90 * time.Second,
				TLSHandshakeTimeout: 10 * time.Second,
				DisableKeepAlives:   false,
				DisableCompression:  false,
			},
		},
	}

	go l.worker()

	// Set the LokiLogger as the output destination for the standard log package.
	log.SetOutput(l)

	return nil
}

func checkUrl(rawURL string) error {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return err
	}

	address := net.JoinHostPort(parsedURL.Host, parsedURL.Port())
	conn, err := net.DialTimeout("tcp", address, 2*time.Second)
	if err != nil {
		return err
	}
	conn.Close()

	return nil
}

func (l *LokiLogger) worker() {
	for {
		select {
		case <-l.ctx.Done():
			if !l.timer.Stop() {
				select {
				case <-l.timer.C:
				default:
				}
			}
			l.Flush()
			return
		case <-l.timer.C:
			if len(l.logs) > 0 {
				l.Flush()
			}
		}
	}
}

// prepareLogs prepares the logs for sending to Loki.  Formats logs into Loki-compatible structure.
func (l *LokiLogger) prepareLogs() {
	data := make(map[string][][2]string)

	// Iterate through the collected logs.
	for _, val := range l.logs {
		// Split each log message into parts.
		parts := strings.SplitN(val, " ", 3)

		timestamp := time.Now()
		if t, err := time.ParseInLocation("2006/01/02 15:04:05", parts[0]+" "+parts[1], time.UTC); err != nil {
			log.Println(err)
		} else {
			timestamp = t
			val = strings.TrimSpace(parts[2])
		}

		level := "info"

		if strings.Contains(val, "INFO") {
			val = strings.Replace(val, "INFO ", "", 1)
		}

		if strings.Contains(val, "ERROR") {
			level = "error"
			val = strings.Replace(val, "ERROR ", "", 1)
		}

		if strings.Contains(val, "WARN") {
			level = "warn"
			val = strings.Replace(val, "WARN ", "", 1)
		}

		if strings.Contains(val, "DEBUG") {
			level = "debug"
			val = strings.Replace(val, "DEBUG ", "", 1)
		}

		if _, exists := data[level]; !exists {
			data[level] = make([][2]string, 0, l.cfg.BatchSize)
		}

		data[level] = append(data[level], [2]string{strconv.Itoa(int(timestamp.UnixNano())), val})
	}

	// Launch a goroutine to send the logs to Loki in the background.
	go l.sendLogs(data)
}

// sendLogs sends the prepared log data to the Loki API server.
func (l *LokiLogger) sendLogs(data map[string][][2]string) {
	defer func() {
		select {
		case <-l.ctx.Done():
			l.client.CloseIdleConnections()
		default:
		}
	}()

	var err error

	streams := make(map[string][]LokiStream)
	streams["streams"] = make([]LokiStream, 0, len(data))
	for k, v := range data {
		streams["streams"] = append(streams["streams"], LokiStream{
			Stream: map[string]string{
				"service_name": l.cfg.Name,
				"level":        k,
			},
			Values: v,
		})
	}

	// Marshal the log data into JSON format.
	jsonData, err := json.Marshal(streams)
	// If JSON marshaling fails, log the error and return.
	if err != nil {
		log.Printf("Error loki marshalling JSON: %v", err)
		return
	}

	req, err := http.NewRequest("POST", l.cfg.URL, bytes.NewBuffer(jsonData))
	if err != nil {
		log.Printf("Error loki NewRequest: %v", err)
		return
	}

	req.Header.Set("Content-Type", "application/json")

	if l.cfg.AccessToken != "" {
		req.Header.Set("Authorization", "Bearer "+l.cfg.AccessToken)
	}

	var resp *http.Response

	for attempt := 1; attempt <= l.cfg.RetryCount; attempt++ {
		resp, err = l.client.Do(req)
		if err == nil {
			if resp.StatusCode < 500 {
				defer resp.Body.Close()
				break
			}

			resp.Body.Close()
		}

		log.Printf("Попытка %d не удалась: %v", attempt, err)

		time.Sleep(1 * time.Second * time.Duration(attempt))
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		fmt.Println("Logs sent")
		return
	}

	log.Printf("Error loki code is: %d", resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Error loki read body: %v", err)
		return
	}

	fmt.Println(string(body))
}

// Write implements the io.Writer interface and writes data to the Loki API server.
func (l *LokiLogger) Write(p []byte) (n int, err error) {
	select {
	case <-l.ctx.Done():
		return 0, fmt.Errorf("context cancelled")
	default:
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	l.resetAutoFlushTimer()

	// Add the data to the collected logs.
	l.logs = append(l.logs, string(p))

	// If the number of logs reaches the batch size, prepare and send them to Loki.
	if len(l.logs) >= l.cfg.BatchSize {
		l.prepareLogs()
		l.logs = l.logs[:0]
	}

	fmt.Println(strings.TrimSpace(string(p)))

	return len(p), nil
}

// Sends the log data to the Loki API server.
func (l *LokiLogger) Flush() {
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
