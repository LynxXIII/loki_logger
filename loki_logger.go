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

type Config struct {
	Name        string
	URL         string
	BatchSize   int
	AccessToken string
}

type LokiWriter struct {
	mu          sync.Mutex
	serviceName string
	addr        string
	path        string
	conn        net.Conn
	logs        []string
	batchSize   int
	accessToken string
}

type LokiStream struct {
	Stream map[string]string `json:"stream"`
	Values [][2]string       `json:"values"`
}

func NewLokiWriter(cfg Config) (*LokiWriter, error) {
	parsedURL, err := url.Parse(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("Error loki parse URL: %v", err)
	}

	addr := net.JoinHostPort(parsedURL.Hostname(), parsedURL.Port())
	conn, err := getConn(addr)
	if err != nil {
		return nil, fmt.Errorf("Error loki connection: %v", err)
	}

	log.SetFlags(log.LstdFlags | log.LUTC | log.Lshortfile)

	lw := &LokiWriter{
		serviceName: cfg.Name,
		addr:        addr,
		path:        parsedURL.Path,
		conn:        conn,
		logs:        make([]string, 0, cfg.BatchSize),
		batchSize:   cfg.BatchSize,
		accessToken: cfg.AccessToken,
	}

	log.SetOutput(lw)

	return lw, nil
}

func getConn(addr string) (net.Conn, error) {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}

	return conn, err
}

func isConnectionAlive(conn net.Conn) bool {
	if err := conn.SetReadDeadline(time.Now().Add(1 * time.Second)); err != nil {
		log.Println(err)
		return false
	}
	defer func() {
		err := conn.SetReadDeadline(time.Time{})
		if err != nil {
			log.Println(err)
		}
	}()

	buf := make([]byte, 1)
	_, err := conn.Read(buf)

	if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		return true
	}

	// Any other error (EOF, connection reset) - the connection is dead
	return err == nil
}

func (w *LokiWriter) prepareLogs() {
	logData := LokiStream{
		Stream: map[string]string{
			"service_name": w.serviceName,
			"level":        "info",
		},
	}

	for _, val := range w.logs {
		parts := strings.Split(val, " ")
		if len(parts) < 2 {
			logData.Values = append(logData.Values, [2]string{strconv.Itoa(int(time.Now().UnixNano())), strings.Join(parts, " ")})
		} else {
			timestamp, err := time.ParseInLocation("2006/01/02 15:04:05", parts[0]+" "+parts[1], time.UTC)
			if err != nil {
				fmt.Println(err)
				logData.Values = append(logData.Values, [2]string{strconv.Itoa(int(time.Now().UnixNano())), strings.Join(parts, " ")})
			} else {
				logData.Values = append(logData.Values, [2]string{strconv.Itoa(int(timestamp.UnixNano())), strings.TrimSpace(strings.Join(parts[2:], " "))})
			}
		}
	}

	go w.sendLogs(&logData)
}

func (w *LokiWriter) sendLogs(logData *LokiStream) {
	jsonData, err := json.Marshal(map[string][]LokiStream{
		"streams": {*logData},
	})
	if err != nil {
		log.Printf("Error loki marshalling JSON: %v", err)
		return
	}

	request := fmt.Sprintf("POST %s HTTP/1.1\r\n"+
		"Host: %s\r\n"+
		"Authorization: Bearer %s\r\n"+
		"Content-Type: application/json\r\n"+
		"Content-Length: %d\r\n"+
		"Connection: keep-alive"+
		"\r\n\r\n%s",
		w.path, w.addr, w.accessToken, len(jsonData), string(jsonData),
	)

	//fmt.Println(request)

	if !isConnectionAlive(w.conn) {
		conn, err := getConn(w.addr)
		if err != nil {
			fmt.Println(err)
			return
		}
		w.conn = conn
	}

	if _, err := w.conn.Write([]byte(request)); err != nil {
		log.Printf("Error loki send request: %v", err)
		return
	}

	response := bufio.NewReader(w.conn)

	strStatus, err := response.ReadString('\n')
	if err != nil {
		log.Printf("Error loki receive status: %v", err)
		return
	}

	status := strings.Split(strStatus, " ")
	if code, err := strconv.Atoi(status[1]); err != nil {
		log.Printf("Error loki parse code: %v", err)
	} else if code < 200 || code >= 300 {
		log.Printf("Error loki code is: %d", code)
	} else {
		fmt.Println("Logs sent")
		return
	}

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

func (w *LokiWriter) Write(p []byte) (n int, err error) {
	w.logs = append(w.logs, string(p))

	if len(w.logs) >= w.batchSize {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.prepareLogs()
		w.logs = w.logs[:0]
	}

	fmt.Println(strings.TrimSpace(string(p)))
	return len(p), nil
}

func (w *LokiWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.prepareLogs()
}
