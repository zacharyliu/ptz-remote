package visca

import (
	"encoding/binary"
	"fmt"
	"net"
	"sync"
	"time"
)

const minInterval = 33 * time.Millisecond // ~30 commands/sec max per axis

// throttle coalesces rapid updates, sending immediately when possible
// and scheduling a trailing edge send for updates during cooldown
type throttle struct {
	mu           sync.Mutex
	lastSendTime time.Time
	timerRunning bool
	stopCh       <-chan struct{}
	flush        func() // called with mu held
}

func (t *throttle) trigger() {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	if now.Sub(t.lastSendTime) >= minInterval {
		t.flush()
		t.lastSendTime = now
	} else if !t.timerRunning {
		t.timerRunning = true
		remaining := minInterval - now.Sub(t.lastSendTime)
		go func() {
			select {
			case <-time.After(remaining):
				t.mu.Lock()
				t.flush()
				t.lastSendTime = time.Now()
				t.timerRunning = false
				t.mu.Unlock()
			case <-t.stopCh:
			}
		}()
	}
}

// Controller manages VISCA communication with a PTZ camera
type Controller struct {
	conn     net.Conn
	mu       sync.Mutex
	addr     int    // Camera address (1-7), default 1
	seqNum   uint32 // Sequence number for VISCA over IP
	protocol string
	stopCh   chan struct{}

	// Pan/tilt state
	panTilt struct {
		throttle
		pending, sent struct{ pan, tilt float64 }
	}

	// Zoom state
	zoom struct {
		throttle
		pending, sent float64
	}
}

// Config for VISCA controller
type Config struct {
	Address  string // UDP: "192.168.1.100:52381", TCP: "192.168.1.100:5678"
	Protocol string // "udp" or "tcp"
}

// NewController creates a new VISCA controller
func NewController(cfg Config) (*Controller, error) {
	protocol := cfg.Protocol
	if protocol == "" {
		protocol = "udp"
	}

	var conn net.Conn
	var err error
	switch protocol {
	case "udp":
		conn, err = net.DialTimeout("udp", cfg.Address, 5*time.Second)
	case "tcp":
		conn, err = net.DialTimeout("tcp", cfg.Address, 5*time.Second)
	default:
		return nil, fmt.Errorf("unsupported protocol: %s", protocol)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to connect via %s: %w", protocol, err)
	}

	c := &Controller{
		conn:     conn,
		addr:     1,
		protocol: protocol,
		stopCh:   make(chan struct{}),
	}

	// Wire up throttle flush callbacks
	c.panTilt.stopCh = c.stopCh
	c.panTilt.flush = func() {
		if c.panTilt.pending != c.panTilt.sent {
			c.sendPanTiltCmd(c.panTilt.pending.pan, c.panTilt.pending.tilt)
			c.panTilt.sent = c.panTilt.pending
		}
	}

	c.zoom.stopCh = c.stopCh
	c.zoom.flush = func() {
		if c.zoom.pending != c.zoom.sent {
			c.sendZoomCmd(c.zoom.pending)
			c.zoom.sent = c.zoom.pending
		}
	}

	return c, nil
}

// Close closes the VISCA connection
func (c *Controller) Close() error {
	close(c.stopCh)
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// PanTilt sends a pan/tilt command. pan: -1.0 (left) to 1.0 (right), tilt: -1.0 (down) to 1.0 (up)
func (c *Controller) PanTilt(pan, tilt float64) error {
	c.panTilt.mu.Lock()
	c.panTilt.pending.pan = pan
	c.panTilt.pending.tilt = tilt
	changed := c.panTilt.pending != c.panTilt.sent
	c.panTilt.mu.Unlock()

	if changed {
		c.panTilt.trigger()
	}
	return nil
}

// Zoom sends a zoom command. zoom: -1.0 (wide/out) to 1.0 (tele/in)
func (c *Controller) Zoom(zoom float64) error {
	c.zoom.mu.Lock()
	c.zoom.pending = zoom
	changed := c.zoom.pending != c.zoom.sent
	c.zoom.mu.Unlock()

	if changed {
		c.zoom.trigger()
	}
	return nil
}

// Stop stops all PTZ movement immediately
func (c *Controller) Stop() error {
	c.panTilt.mu.Lock()
	c.panTilt.pending = struct{ pan, tilt float64 }{}
	c.panTilt.sent = c.panTilt.pending
	c.panTilt.mu.Unlock()

	c.zoom.mu.Lock()
	c.zoom.pending = 0
	c.zoom.sent = 0
	c.zoom.mu.Unlock()

	c.sendPanTiltCmd(0, 0)
	c.sendZoomCmd(0)
	return nil
}

// RecallPreset recalls a preset position (0-255)
func (c *Controller) RecallPreset(preset int) error {
	if preset < 0 || preset > 255 {
		return fmt.Errorf("preset must be 0-255")
	}
	return c.sendCommand([]byte{0x01, 0x04, 0x3F, 0x02, byte(preset)})
}

// SavePreset saves current position to a preset (0-255)
func (c *Controller) SavePreset(preset int) error {
	if preset < 0 || preset > 255 {
		return fmt.Errorf("preset must be 0-255")
	}
	return c.sendCommand([]byte{0x01, 0x04, 0x3F, 0x01, byte(preset)})
}

// sendPanTiltCmd sends the VISCA pan/tilt drive command
func (c *Controller) sendPanTiltCmd(pan, tilt float64) {
	// VISCA: 01 06 01 VV WW XX YY (VV=pan speed 1-24, WW=tilt speed 1-20)
	panSpeed := byte(clamp(int(abs(pan)*24), 1, 24))
	tiltSpeed := byte(clamp(int(abs(tilt)*20), 1, 20))

	panDir := byte(0x03)  // stop
	tiltDir := byte(0x03) // stop

	if pan < -0.05 {
		panDir = 0x01 // left
	} else if pan > 0.05 {
		panDir = 0x02 // right
	} else {
		panSpeed = 0x01
	}

	if tilt > 0.05 {
		tiltDir = 0x01 // up
	} else if tilt < -0.05 {
		tiltDir = 0x02 // down
	} else {
		tiltSpeed = 0x01
	}

	c.sendCommand([]byte{0x01, 0x06, 0x01, panSpeed, tiltSpeed, panDir, tiltDir})
}

// sendZoomCmd sends the VISCA zoom command
func (c *Controller) sendZoomCmd(zoom float64) {
	// VISCA: 01 04 07 XY (X: 0=stop, 2=tele, 3=wide; Y: speed 0-7)
	var cmd byte
	if zoom > 0.05 {
		cmd = 0x20 | byte(clamp(int(zoom*7), 0, 7))
	} else if zoom < -0.05 {
		cmd = 0x30 | byte(clamp(int(abs(zoom)*7), 0, 7))
	}
	c.sendCommand([]byte{0x01, 0x04, 0x07, cmd})
}

// sendCommand sends a raw VISCA command
func (c *Controller) sendCommand(payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Build VISCA frame: [0x80|addr] [payload...] [0xFF]
	frame := make([]byte, 0, len(payload)+2)
	frame = append(frame, byte(0x80|c.addr))
	frame = append(frame, payload...)
	frame = append(frame, 0xFF)

	// Wrap in VISCA-over-IP for UDP
	var packet []byte
	if c.protocol == "udp" {
		header := make([]byte, 8)
		header[0] = 0x01 // command
		binary.BigEndian.PutUint16(header[2:4], uint16(len(frame)))
		binary.BigEndian.PutUint32(header[4:8], c.seqNum)
		c.seqNum++
		packet = append(header, frame...)
	} else {
		packet = frame
	}

	c.conn.SetWriteDeadline(time.Now().Add(10 * time.Millisecond))
	c.conn.Write(packet)
	return nil
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func clamp(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}
