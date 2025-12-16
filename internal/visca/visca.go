package visca

import (
	"encoding/binary"
	"fmt"
	"net"
	"sync"
	"time"
)

// Controller manages VISCA communication with a PTZ camera
type Controller struct {
	conn     net.Conn
	mu       sync.Mutex
	addr     int    // Camera address (1-7), default 1
	seqNum   uint32 // Sequence number for VISCA over IP
	protocol string

	// Rate limiting - only send commands at fixed intervals
	lastPanTilt time.Time
	lastZoom    time.Time
	minInterval time.Duration
}

// Config for VISCA controller
type Config struct {
	// For UDP: address like "192.168.1.100:52381"
	// For TCP: address like "192.168.1.100:5678"
	Address  string
	Protocol string // "udp" or "tcp"
}

// NewController creates a new VISCA controller
func NewController(cfg Config) (*Controller, error) {
	var conn net.Conn
	var err error

	protocol := cfg.Protocol
	if protocol == "" {
		protocol = "udp" // Default to UDP for VISCA over IP
	}

	switch protocol {
	case "udp":
		conn, err = net.DialTimeout("udp", cfg.Address, 5*time.Second)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to VISCA over UDP: %w", err)
		}
	case "tcp":
		conn, err = net.DialTimeout("tcp", cfg.Address, 5*time.Second)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to VISCA over TCP: %w", err)
		}
	default:
		return nil, fmt.Errorf("unsupported protocol: %s", protocol)
	}

	return &Controller{
		conn:        conn,
		addr:        1,
		seqNum:      0,
		protocol:    protocol,
		minInterval: 50 * time.Millisecond, // Max 20 commands/sec
	}, nil
}

// Close closes the VISCA connection
func (c *Controller) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// buildVISCAPayload constructs a raw VISCA command (address + payload + terminator)
func (c *Controller) buildVISCAPayload(payload []byte) []byte {
	// VISCA command format: [address byte] [payload...] [terminator]
	// Address byte: 0x80 | address (1-7)
	cmd := make([]byte, 0, len(payload)+2)
	cmd = append(cmd, byte(0x80|c.addr))
	cmd = append(cmd, payload...)
	cmd = append(cmd, 0xFF) // Terminator
	return cmd
}

// buildVISCAOverIP wraps a VISCA payload in VISCA-over-IP framing
func (c *Controller) buildVISCAOverIP(viscaPayload []byte) []byte {
	// VISCA over IP header (8 bytes):
	// Bytes 0-1: Message type (0x01 0x00 for command)
	// Bytes 2-3: Payload length (big endian)
	// Bytes 4-7: Sequence number (big endian)

	header := make([]byte, 8)
	header[0] = 0x01
	header[1] = 0x00
	binary.BigEndian.PutUint16(header[2:4], uint16(len(viscaPayload)))
	binary.BigEndian.PutUint32(header[4:8], c.seqNum)
	c.seqNum++

	packet := append(header, viscaPayload...)
	return packet
}

// sendCommand sends a command without waiting for response (fire-and-forget)
func (c *Controller) sendCommand(payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	viscaPayload := c.buildVISCAPayload(payload)

	var packet []byte
	if c.protocol == "udp" {
		// Use VISCA over IP framing for UDP
		packet = c.buildVISCAOverIP(viscaPayload)
	} else {
		// Raw VISCA for TCP (some devices)
		packet = viscaPayload
	}

	// Short write deadline - don't block
	c.conn.SetWriteDeadline(time.Now().Add(10 * time.Millisecond))
	_, err := c.conn.Write(packet)
	if err != nil {
		// Non-fatal for UDP - just means we couldn't send this one
		return nil
	}

	return nil
}

// PanTilt sends a pan/tilt command with rate limiting
// pan: -1.0 (left) to 1.0 (right)
// tilt: -1.0 (down) to 1.0 (up)
func (c *Controller) PanTilt(pan, tilt float64) error {
	// Rate limit
	now := time.Now()
	if now.Sub(c.lastPanTilt) < c.minInterval {
		return nil // Skip this command
	}
	c.lastPanTilt = now

	// VISCA Pan-Tilt Drive command: 01 06 01 VV WW XX YY
	// VV = pan speed (01-18), WW = tilt speed (01-14)
	// XX: 01=left, 02=right, 03=stop
	// YY: 01=up, 02=down, 03=stop

	// Calculate speeds (1-24 for pan, 1-20 for tilt)
	panSpeed := byte(clampInt(int(abs(pan)*24), 1, 24))
	tiltSpeed := byte(clampInt(int(abs(tilt)*20), 1, 20))

	// Determine directions
	var panDir, tiltDir byte
	if pan < -0.05 {
		panDir = 0x01 // Left
	} else if pan > 0.05 {
		panDir = 0x02 // Right
	} else {
		panDir = 0x03 // Stop
		panSpeed = 0x01
	}

	if tilt > 0.05 {
		tiltDir = 0x01 // Up
	} else if tilt < -0.05 {
		tiltDir = 0x02 // Down
	} else {
		tiltDir = 0x03 // Stop
		tiltSpeed = 0x01
	}

	payload := []byte{0x01, 0x06, 0x01, panSpeed, tiltSpeed, panDir, tiltDir}
	return c.sendCommand(payload)
}

// Zoom sends a zoom command with rate limiting
// zoom: -1.0 (wide/out) to 1.0 (tele/in)
func (c *Controller) Zoom(zoom float64) error {
	// Rate limit
	now := time.Now()
	if now.Sub(c.lastZoom) < c.minInterval {
		return nil // Skip this command
	}
	c.lastZoom = now

	// VISCA Zoom command: 01 04 07 XY
	// X: 0=stop, 2=tele(in), 3=wide(out)
	// Y: speed 0-7 (for variable speed: 2p or 3p where p=speed)

	var cmd byte
	if zoom > 0.05 {
		// Zoom in (tele) - 0x2p where p is speed 0-7
		speed := byte(clampInt(int(zoom*7), 0, 7))
		cmd = 0x20 | speed
	} else if zoom < -0.05 {
		// Zoom out (wide) - 0x3p where p is speed 0-7
		speed := byte(clampInt(int(abs(zoom)*7), 0, 7))
		cmd = 0x30 | speed
	} else {
		cmd = 0x00 // Stop
	}

	payload := []byte{0x01, 0x04, 0x07, cmd}
	return c.sendCommand(payload)
}

// Stop stops all PTZ movement immediately (bypasses rate limit)
func (c *Controller) Stop() error {
	// Reset rate limit timestamps so stop goes through immediately
	c.lastPanTilt = time.Time{}
	c.lastZoom = time.Time{}

	// Send stop for pan/tilt
	payload := []byte{0x01, 0x06, 0x01, 0x01, 0x01, 0x03, 0x03}
	if err := c.sendCommand(payload); err != nil {
		return err
	}

	// Send stop for zoom
	payload = []byte{0x01, 0x04, 0x07, 0x00}
	return c.sendCommand(payload)
}

// RecallPreset recalls a preset position
func (c *Controller) RecallPreset(preset int) error {
	if preset < 0 || preset > 255 {
		return fmt.Errorf("preset must be 0-255")
	}
	// VISCA Memory Recall: 01 04 3F 02 pp
	payload := []byte{0x01, 0x04, 0x3F, 0x02, byte(preset)}
	return c.sendCommand(payload)
}

// SavePreset saves current position to a preset
func (c *Controller) SavePreset(preset int) error {
	if preset < 0 || preset > 255 {
		return fmt.Errorf("preset must be 0-255")
	}
	// VISCA Memory Set: 01 04 3F 01 pp
	payload := []byte{0x01, 0x04, 0x3F, 0x01, byte(preset)}
	return c.sendCommand(payload)
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func clampInt(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}
