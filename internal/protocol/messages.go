package protocol

import "encoding/json"

// Message types
const (
	TypePing         = "ping"
	TypePong         = "pong"
	TypeStatus       = "status"
	TypeOffer        = "offer"
	TypeAnswer       = "answer"
	TypeICECandidate = "ice_candidate"
	TypePTZCommand   = "ptz_command"
	TypePTZStop      = "ptz_stop"
	TypePTZPreset    = "ptz_preset"
	TypeError        = "error"
)

// Error codes
const (
	ErrCameraDisconnected = "CAMERA_DISCONNECTED"
	ErrRTSP               = "RTSP_ERROR"
	ErrVISCA              = "VISCA_ERROR"
	ErrInvalidMessage     = "INVALID_MESSAGE"
)

// Message is the base envelope for all WebSocket messages
type Message struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// PingPayload for ping messages
type PingPayload struct {
	Timestamp int64 `json:"timestamp"`
}

// PongPayload for pong messages
type PongPayload struct {
	ClientTimestamp int64 `json:"client_timestamp"`
	ServerTimestamp int64 `json:"server_timestamp"`
}

// StatusPayload for status messages
type StatusPayload struct {
	CameraConnected bool   `json:"camera_connected"`
	RTSPURL         string `json:"rtsp_url,omitempty"`
	ControlProtocol string `json:"control_protocol"`
	VideoProtocol   string `json:"video_protocol"`
}

// SDPPayload for offer/answer messages
type SDPPayload struct {
	SDP string `json:"sdp"`
}

// ICECandidatePayload for ICE candidate messages
type ICECandidatePayload struct {
	Candidate     string `json:"candidate"`
	SDPMid        string `json:"sdp_mid"`
	SDPMLineIndex uint16 `json:"sdp_mline_index"`
}

// PTZCommandPayload for PTZ control messages
type PTZCommandPayload struct {
	Pan  float64 `json:"pan"`
	Tilt float64 `json:"tilt"`
	Zoom float64 `json:"zoom"`
}

// PTZPresetPayload for preset recall/save
type PTZPresetPayload struct {
	Action       string `json:"action"`
	PresetNumber int    `json:"preset_number"`
}

// ErrorPayload for error messages
type ErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// NewMessage creates a new message with the given type and payload
func NewMessage(msgType string, payload any) (*Message, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return &Message{
		Type:    msgType,
		Payload: data,
	}, nil
}

// ParsePayload unmarshals the payload into the given struct
func (m *Message) ParsePayload(v any) error {
	return json.Unmarshal(m.Payload, v)
}
