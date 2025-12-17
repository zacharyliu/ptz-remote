package panasonic

import (
	"fmt"
	"net/http"
	"sync"
	"time"
)

const minInterval = 50 * time.Millisecond // ~20 commands/sec max

// throttle coalesces rapid updates, sending immediately when possible
// and scheduling a trailing edge send for updates during cooldown
type throttle struct {
	mu           sync.Mutex
	lastSendTime time.Time
	timerRunning bool
	stopCh       <-chan struct{}
	flush        func()
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

// Controller manages HTTP CGI communication with a Panasonic PTZ camera
type Controller struct {
	baseURL string
	client  *http.Client
	mu      sync.Mutex
	stopCh  chan struct{}

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

// Config for Panasonic controller
type Config struct {
	Address string // Camera IP address or hostname (e.g., "192.168.1.100")
}

// NewController creates a new Panasonic controller
func NewController(cfg Config) (*Controller, error) {
	if cfg.Address == "" {
		return nil, fmt.Errorf("camera address is required")
	}

	c := &Controller{
		baseURL: fmt.Sprintf("http://%s/cgi-bin/aw_ptz", cfg.Address),
		client: &http.Client{
			Timeout: 2 * time.Second,
		},
		stopCh: make(chan struct{}),
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

// Close closes the controller
func (c *Controller) Close() error {
	close(c.stopCh)
	return nil
}

// PanTilt sends a pan/tilt command
// pan: -1.0 (left) to 1.0 (right)
// tilt: -1.0 (down) to 1.0 (up)
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

// Zoom sends a zoom command
// zoom: -1.0 (wide/out) to 1.0 (tele/in)
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

// RecallPreset recalls a preset position (0-99 for Panasonic)
func (c *Controller) RecallPreset(preset int) error {
	if preset < 0 || preset > 99 {
		return fmt.Errorf("preset must be 0-99 for Panasonic cameras")
	}
	return c.sendCommand(fmt.Sprintf("#R%02d", preset))
}

// SavePreset saves current position to a preset (0-99 for Panasonic)
func (c *Controller) SavePreset(preset int) error {
	if preset < 0 || preset > 99 {
		return fmt.Errorf("preset must be 0-99 for Panasonic cameras")
	}
	return c.sendCommand(fmt.Sprintf("#M%02d", preset))
}

// sendPanTiltCmd sends the Panasonic pan/tilt command
// Panasonic format: #PTS<pan><tilt> where values are 01-99 (50 = stop)
func (c *Controller) sendPanTiltCmd(pan, tilt float64) {
	// Convert -1.0 to 1.0 range to Panasonic 01-99 range (50 = stop)
	// pan: -1.0 = 01 (full left), 0 = 50 (stop), 1.0 = 99 (full right)
	// tilt: -1.0 = 01 (full down), 0 = 50 (stop), 1.0 = 99 (full up)
	panSpeed := speedToValue(pan)
	tiltSpeed := speedToValue(tilt)

	c.sendCommand(fmt.Sprintf("#PTS%02d%02d", panSpeed, tiltSpeed))
}

// sendZoomCmd sends the Panasonic zoom command
// Panasonic format: #Z<speed> where value is 01-99 (50 = stop)
func (c *Controller) sendZoomCmd(zoom float64) {
	// Convert -1.0 to 1.0 range to Panasonic 01-99 range (50 = stop)
	// zoom: -1.0 = 01 (full wide), 0 = 50 (stop), 1.0 = 99 (full tele)
	zoomSpeed := speedToValue(zoom)
	c.sendCommand(fmt.Sprintf("#Z%02d", zoomSpeed))
}

// sendCommand sends a command to the camera via HTTP CGI
func (c *Controller) sendCommand(cmd string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	url := fmt.Sprintf("%s?cmd=%s&res=1", c.baseURL, cmd)

	resp, err := c.client.Get(url)
	if err != nil {
		return fmt.Errorf("failed to send command: %w", err)
	}
	defer resp.Body.Close()

	return nil
}

// speedToValue converts a -1.0 to 1.0 value to Panasonic's 01-99 range
func speedToValue(v float64) int {
	// Clamp to -1.0 to 1.0
	if v < -1.0 {
		v = -1.0
	} else if v > 1.0 {
		v = 1.0
	}

	// Apply deadzone
	if v > -0.05 && v < 0.05 {
		return 50 // Stop
	}

	// Scale to Panasonic range: -1.0 -> 01, 0 -> 50, 1.0 -> 99
	// The range is split: negative values use 01-49, positive use 51-99
	if v < 0 {
		// Map -1.0 to -0.05 -> 01 to 49
		return int(50 + v*49)
	}
	// Map 0.05 to 1.0 -> 51 to 99
	return int(50 + v*49)
}
