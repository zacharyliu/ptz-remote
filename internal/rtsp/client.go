package rtsp

import (
	"bufio"
	"encoding/base64"
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

// Client handles RTSP connection and RTP streaming
type Client struct {
	url           *url.URL
	conn          net.Conn
	reader        *bufio.Reader
	session       string
	cseq          int
	mu            sync.Mutex
	rtpChan       chan []byte
	stopCh        chan struct{}
	closed        bool
	videoTrackURL string // Extracted from SDP

	// Reconnection
	reconnectMu sync.Mutex
	connected   bool
}

// NewClient creates a new RTSP client
func NewClient(rtspURL string) (*Client, error) {
	parsed, err := url.Parse(rtspURL)
	if err != nil {
		return nil, fmt.Errorf("invalid RTSP URL: %w", err)
	}

	if parsed.Scheme != "rtsp" {
		return nil, fmt.Errorf("URL scheme must be rtsp")
	}

	return &Client{
		url:     parsed,
		rtpChan: make(chan []byte, 500),
		stopCh:  make(chan struct{}),
	}, nil
}

// Connect establishes the RTSP connection and starts streaming
func (c *Client) Connect() error {
	if err := c.connect(); err != nil {
		return err
	}

	// Start read loop
	go c.readLoop()

	// Start keepalive loop
	go c.keepaliveLoop()

	return nil
}

func (c *Client) connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	host := c.url.Host
	if !strings.Contains(host, ":") {
		host += ":554"
	}

	conn, err := net.DialTimeout("tcp", host, 10*time.Second)
	if err != nil {
		return fmt.Errorf("failed to connect to RTSP server: %w", err)
	}
	c.conn = conn
	c.reader = bufio.NewReaderSize(conn, 256*1024)
	c.cseq = 0
	c.session = ""

	// OPTIONS
	if err := c.options(); err != nil {
		c.conn.Close()
		return err
	}

	// DESCRIBE
	if err := c.describe(); err != nil {
		c.conn.Close()
		return err
	}

	// SETUP
	if err := c.setup(); err != nil {
		c.conn.Close()
		return err
	}

	// PLAY
	if err := c.play(); err != nil {
		c.conn.Close()
		return err
	}

	c.reconnectMu.Lock()
	c.connected = true
	c.reconnectMu.Unlock()

	log.Printf("RTSP: Connected and playing")
	return nil
}

func (c *Client) reconnect() {
	c.reconnectMu.Lock()
	if !c.connected {
		c.reconnectMu.Unlock()
		return
	}
	c.connected = false
	c.reconnectMu.Unlock()

	// Close old connection
	c.mu.Lock()
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
	c.mu.Unlock()

	log.Printf("RTSP: Connection lost, reconnecting...")

	// Retry loop
	for attempt := 1; ; attempt++ {
		select {
		case <-c.stopCh:
			return
		default:
		}

		// Exponential backoff: 1s, 2s, 4s, 8s, max 30s
		delay := time.Duration(1<<uint(attempt-1)) * time.Second
		if delay > 30*time.Second {
			delay = 30 * time.Second
		}

		log.Printf("RTSP: Reconnect attempt %d in %v", attempt, delay)
		time.Sleep(delay)

		select {
		case <-c.stopCh:
			return
		default:
		}

		if err := c.connect(); err != nil {
			log.Printf("RTSP: Reconnect failed: %v", err)
			continue
		}

		log.Printf("RTSP: Reconnected successfully")
		return
	}
}

func (c *Client) sendRequest(method, uri string, headers map[string]string) (int, map[string]string, string, error) {
	c.cseq++

	req := fmt.Sprintf("%s %s RTSP/1.0\r\n", method, uri)
	req += fmt.Sprintf("CSeq: %d\r\n", c.cseq)
	req += "User-Agent: ptz-remote/1.0\r\n"

	if c.url.User != nil {
		password, _ := c.url.User.Password()
		auth := base64.StdEncoding.EncodeToString(
			[]byte(c.url.User.Username() + ":" + password))
		req += fmt.Sprintf("Authorization: Basic %s\r\n", auth)
	}

	if c.session != "" {
		req += fmt.Sprintf("Session: %s\r\n", c.session)
	}

	for k, v := range headers {
		req += fmt.Sprintf("%s: %s\r\n", k, v)
	}
	req += "\r\n"

	c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err := c.conn.Write([]byte(req))
	if err != nil {
		return 0, nil, "", fmt.Errorf("failed to send %s: %w", method, err)
	}

	c.conn.SetReadDeadline(time.Now().Add(10 * time.Second))

	statusLine, err := c.reader.ReadString('\n')
	if err != nil {
		return 0, nil, "", fmt.Errorf("failed to read response: %w", err)
	}

	parts := strings.SplitN(strings.TrimSpace(statusLine), " ", 3)
	if len(parts) < 2 {
		return 0, nil, "", fmt.Errorf("invalid response: %s", statusLine)
	}
	statusCode, _ := strconv.Atoi(parts[1])

	respHeaders := make(map[string]string)
	for {
		line, err := c.reader.ReadString('\n')
		if err != nil {
			return statusCode, nil, "", err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		idx := strings.Index(line, ":")
		if idx > 0 {
			key := strings.TrimSpace(line[:idx])
			val := strings.TrimSpace(line[idx+1:])
			respHeaders[key] = val
		}
	}

	body := ""
	if cl, ok := respHeaders["Content-Length"]; ok {
		length, _ := strconv.Atoi(cl)
		if length > 0 {
			bodyBytes := make([]byte, length)
			_, err := io.ReadFull(c.reader, bodyBytes)
			if err != nil {
				return statusCode, respHeaders, "", err
			}
			body = string(bodyBytes)
		}
	}

	return statusCode, respHeaders, body, nil
}

func (c *Client) options() error {
	uri := c.url.String()
	status, _, _, err := c.sendRequest("OPTIONS", uri, nil)
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("OPTIONS failed with status %d", status)
	}
	return nil
}

func (c *Client) describe() error {
	uri := c.url.String()
	headers := map[string]string{
		"Accept": "application/sdp",
	}
	status, _, body, err := c.sendRequest("DESCRIBE", uri, headers)
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("DESCRIBE failed with status %d", status)
	}

	c.videoTrackURL = c.parseSDPForVideoTrack(body)
	if c.videoTrackURL == "" {
		c.videoTrackURL = uri
	}

	return nil
}

func (c *Client) parseSDPForVideoTrack(sdp string) string {
	lines := strings.Split(sdp, "\n")
	baseURL := c.url.String()

	var inVideoSection bool
	var controlURL string

	for _, line := range lines {
		line = strings.TrimSpace(line)

		if strings.HasPrefix(line, "m=video") {
			inVideoSection = true
			continue
		}
		if strings.HasPrefix(line, "m=") && !strings.HasPrefix(line, "m=video") {
			inVideoSection = false
			continue
		}

		if inVideoSection && strings.HasPrefix(line, "a=control:") {
			control := strings.TrimPrefix(line, "a=control:")
			control = strings.TrimSpace(control)

			if strings.HasPrefix(control, "rtsp://") {
				controlURL = control
			} else if control == "*" {
				controlURL = baseURL
			} else {
				if strings.HasSuffix(baseURL, "/") {
					controlURL = baseURL + control
				} else {
					controlURL = baseURL + "/" + control
				}
			}
			break
		}
	}

	if controlURL == "" {
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "a=control:") {
				control := strings.TrimPrefix(line, "a=control:")
				control = strings.TrimSpace(control)
				if strings.HasPrefix(control, "rtsp://") {
					controlURL = control
				} else if control != "*" {
					if strings.HasSuffix(baseURL, "/") {
						controlURL = baseURL + control
					} else {
						controlURL = baseURL + "/" + control
					}
				}
				break
			}
		}
	}

	return controlURL
}

func (c *Client) setup() error {
	uri := c.videoTrackURL
	if uri == "" {
		uri = c.url.String()
	}

	headers := map[string]string{
		"Transport": "RTP/AVP/TCP;unicast;interleaved=0-1",
	}
	status, respHeaders, _, err := c.sendRequest("SETUP", uri, headers)
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("SETUP failed with status %d", status)
	}

	if sess, ok := respHeaders["Session"]; ok {
		parts := strings.Split(sess, ";")
		c.session = strings.TrimSpace(parts[0])
	}

	return nil
}

func (c *Client) play() error {
	uri := c.url.String()
	headers := map[string]string{
		"Range": "npt=0.000-",
	}
	status, _, _, err := c.sendRequest("PLAY", uri, headers)
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("PLAY failed with status %d", status)
	}
	return nil
}

// keepaliveLoop sends periodic keepalive requests to prevent session timeout
func (c *Client) keepaliveLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.stopCh:
			return
		case <-ticker.C:
			c.reconnectMu.Lock()
			connected := c.connected
			c.reconnectMu.Unlock()

			if !connected {
				continue
			}

			c.mu.Lock()
			if c.conn == nil || c.session == "" {
				c.mu.Unlock()
				continue
			}

			// Send GET_PARAMETER as keepalive (more widely supported than OPTIONS with session)
			uri := c.url.String()
			_, _, _, err := c.sendRequest("GET_PARAMETER", uri, nil)
			c.mu.Unlock()

			if err != nil {
				log.Printf("RTSP: Keepalive failed: %v", err)
				go c.reconnect()
			}
		}
	}
}

// RTPChannel returns the channel for receiving RTP packets
func (c *Client) RTPChannel() <-chan []byte {
	return c.rtpChan
}

func (c *Client) readLoop() {
	buf := make([]byte, 128*1024)

	for {
		select {
		case <-c.stopCh:
			return
		default:
		}

		c.reconnectMu.Lock()
		connected := c.connected
		c.reconnectMu.Unlock()

		if !connected {
			// Wait a bit before checking again
			time.Sleep(100 * time.Millisecond)
			continue
		}

		c.mu.Lock()
		conn := c.conn
		reader := c.reader
		c.mu.Unlock()

		if conn == nil || reader == nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}

		conn.SetReadDeadline(time.Now().Add(5 * time.Second))

		// Read interleaved frame header: $ channel(1) length(2)
		header := make([]byte, 4)
		_, err := io.ReadFull(reader, header)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				// Timeout is OK, just no data
				continue
			}

			select {
			case <-c.stopCh:
				return
			default:
			}

			log.Printf("RTSP: Read error: %v", err)
			go c.reconnect()
			continue
		}

		if header[0] != '$' {
			// Not an interleaved frame, skip
			continue
		}

		channel := header[1]
		length := int(header[2])<<8 | int(header[3])

		if length == 0 || length > len(buf) {
			continue
		}

		_, err = io.ReadFull(reader, buf[:length])
		if err != nil {
			select {
			case <-c.stopCh:
				return
			default:
			}
			log.Printf("RTSP: Read payload error: %v", err)
			go c.reconnect()
			continue
		}

		// Channel 0 = RTP video, Channel 1 = RTCP
		if channel == 0 {
			packet := make([]byte, length)
			copy(packet, buf[:length])
			select {
			case c.rtpChan <- packet:
			case <-c.stopCh:
				return
			default:
				// Drop packet if channel full
			}
		}
	}
}

// Close closes the RTSP connection
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil
	}
	c.closed = true

	close(c.stopCh)

	if c.conn != nil {
		c.conn.SetWriteDeadline(time.Now().Add(1 * time.Second))
		c.sendRequest("TEARDOWN", c.url.String(), nil)
		return c.conn.Close()
	}
	return nil
}
