package ptz

// Controller defines the interface for PTZ camera control
type Controller interface {
	// PanTilt sends a pan/tilt command
	// pan: -1.0 (left) to 1.0 (right)
	// tilt: -1.0 (down) to 1.0 (up)
	PanTilt(pan, tilt float64) error

	// Zoom sends a zoom command
	// zoom: -1.0 (wide/out) to 1.0 (tele/in)
	Zoom(zoom float64) error

	// Stop stops all PTZ movement immediately
	Stop() error

	// RecallPreset recalls a preset position (0-255)
	RecallPreset(preset int) error

	// SavePreset saves current position to a preset (0-255)
	SavePreset(preset int) error

	// Close closes the controller connection
	Close() error
}
