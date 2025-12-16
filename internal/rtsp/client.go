package rtsp

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
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
	running       bool
	readStarted   sync.Once
	stopCh        chan struct{}
	closed        bool
	videoTrackURL string // Extracted from SDP
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

// Connect establishes the RTSP connection
func (c *Client) Connect() error {
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
	// Use larger buffer to handle big RTP packets
	c.reader = bufio.NewReaderSize(conn, 256*1024)

	// OPTIONS
	if err := c.options(); err != nil {
		c.conn.Close()
		return err
	}

	// DESCRIBE - get SDP and parse track URLs
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

	c.running = true

	// Start reading immediately after PLAY
	c.readStarted.Do(func() {
		go c.readLoop()
	})

	return nil
}

func (c *Client) sendRequest(method, uri string, headers map[string]string) (int, map[string]string, string, error) {
	c.cseq++

	// Build request
	req := fmt.Sprintf("%s %s RTSP/1.0\r\n", method, uri)
	req += fmt.Sprintf("CSeq: %d\r\n", c.cseq)
	req += "User-Agent: ptz-remote/1.0\r\n"

	// Add auth if present
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

	// Read response
	c.conn.SetReadDeadline(time.Now().Add(10 * time.Second))

	// Status line
	statusLine, err := c.reader.ReadString('\n')
	if err != nil {
		return 0, nil, "", fmt.Errorf("failed to read response: %w", err)
	}

	parts := strings.SplitN(strings.TrimSpace(statusLine), " ", 3)
	if len(parts) < 2 {
		return 0, nil, "", fmt.Errorf("invalid response: %s", statusLine)
	}
	statusCode, _ := strconv.Atoi(parts[1])

	// Headers
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

	// Body (if Content-Length present)
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

	// Parse SDP to find video track control URL
	c.videoTrackURL = c.parseSDPForVideoTrack(body)
	if c.videoTrackURL == "" {
		// Fallback: use the base URL directly
		c.videoTrackURL = uri
	}

	return nil
}

// parseSDPForVideoTrack extracts the video track control URL from SDP
func (c *Client) parseSDPForVideoTrack(sdp string) string {
	lines := strings.Split(sdp, "\n")
	baseURL := c.url.String()

	var inVideoSection bool
	var controlURL string

	for _, line := range lines {
		line = strings.TrimSpace(line)

		// Look for media line (m=video)
		if strings.HasPrefix(line, "m=video") {
			inVideoSection = true
			continue
		}
		// Another media section starts
		if strings.HasPrefix(line, "m=") && !strings.HasPrefix(line, "m=video") {
			inVideoSection = false
			continue
		}

		// Look for control attribute in video section
		if inVideoSection && strings.HasPrefix(line, "a=control:") {
			control := strings.TrimPrefix(line, "a=control:")
			control = strings.TrimSpace(control)

			// Control can be absolute URL or relative
			if strings.HasPrefix(control, "rtsp://") {
				controlURL = control
			} else if control == "*" {
				// Use base URL
				controlURL = baseURL
			} else {
				// Relative URL - append to base
				if strings.HasSuffix(baseURL, "/") {
					controlURL = baseURL + control
				} else {
					controlURL = baseURL + "/" + control
				}
			}
			break
		}
	}

	// If no video-specific control found, look for session-level control
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
	// Use the track URL from SDP, or fall back to base URL
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

	// Extract session ID
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

// RTPChannel returns the channel for receiving RTP packets
func (c *Client) RTPChannel() <-chan []byte {
	return c.rtpChan
}

func (c *Client) readLoop() {
	buf := make([]byte, 128*1024) // Large buffer for video frames

	for {
		select {
		case <-c.stopCh:
			return
		default:
		}

		c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))

		// Read interleaved frame header: $ channel(1) length(2)
		header := make([]byte, 4)
		_, err := io.ReadFull(c.reader, header)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			// Check if we're shutting down
			select {
			case <-c.stopCh:
				return
			default:
			}
			// Connection error - exit loop
			return
		}

		if header[0] != '$' {
			// Not an interleaved frame - might be RTSP response, skip
			// Read until we find a $ or timeout
			continue
		}

		channel := header[1]
		length := int(header[2])<<8 | int(header[3])

		if length == 0 || length > len(buf) {
			continue
		}

		// Read RTP data
		_, err = io.ReadFull(c.reader, buf[:length])
		if err != nil {
			select {
			case <-c.stopCh:
				return
			default:
			}
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
	c.running = false

	// Signal stop to read loop
	close(c.stopCh)

	// Send TEARDOWN
	if c.conn != nil {
		// Best effort teardown
		c.conn.SetWriteDeadline(time.Now().Add(1 * time.Second))
		c.sendRequest("TEARDOWN", c.url.String(), nil)
		return c.conn.Close()
	}
	return nil
}
