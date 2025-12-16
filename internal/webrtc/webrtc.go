package webrtc

import (
	"fmt"
	"sync"

	"github.com/pion/webrtc/v3"
)

// Session represents a WebRTC session with a client
type Session struct {
	pc          *webrtc.PeerConnection
	videoTrack  *webrtc.TrackLocalStaticRTP
	onICE       func(candidate *webrtc.ICECandidate)
	mu          sync.Mutex
	closed      bool
}

// Config for WebRTC session
type Config struct {
	ICEServers []string // STUN/TURN server URLs
}

// DefaultConfig returns a default WebRTC configuration
func DefaultConfig() Config {
	return Config{
		ICEServers: []string{
			"stun:stun.l.google.com:19302",
		},
	}
}

// NewSession creates a new WebRTC session
func NewSession(cfg Config, onICE func(*webrtc.ICECandidate)) (*Session, error) {
	// Configure WebRTC
	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{},
	}
	for _, url := range cfg.ICEServers {
		config.ICEServers = append(config.ICEServers, webrtc.ICEServer{
			URLs: []string{url},
		})
	}

	// Create peer connection
	pc, err := webrtc.NewPeerConnection(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create peer connection: %w", err)
	}

	session := &Session{
		pc:    pc,
		onICE: onICE,
	}

	// Handle ICE candidates
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c != nil && session.onICE != nil {
			session.onICE(c)
		}
	})

	// Log connection state changes
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		fmt.Printf("WebRTC connection state: %s\n", s.String())
	})

	return session, nil
}

// AddH264Track adds an H264 video track to the session
func (s *Session) AddH264Track() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Create video track
	videoTrack, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264},
		"video",
		"ptz-camera",
	)
	if err != nil {
		return fmt.Errorf("failed to create video track: %w", err)
	}

	// Add track to peer connection
	_, err = s.pc.AddTrack(videoTrack)
	if err != nil {
		return fmt.Errorf("failed to add video track: %w", err)
	}

	s.videoTrack = videoTrack
	return nil
}

// CreateOffer creates an SDP offer
func (s *Session) CreateOffer() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	offer, err := s.pc.CreateOffer(nil)
	if err != nil {
		return "", fmt.Errorf("failed to create offer: %w", err)
	}

	// Set local description
	err = s.pc.SetLocalDescription(offer)
	if err != nil {
		return "", fmt.Errorf("failed to set local description: %w", err)
	}

	// Wait for ICE gathering to complete
	gatherComplete := webrtc.GatheringCompletePromise(s.pc)
	<-gatherComplete

	return s.pc.LocalDescription().SDP, nil
}

// SetAnswer sets the remote SDP answer
func (s *Session) SetAnswer(sdp string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	answer := webrtc.SessionDescription{
		Type: webrtc.SDPTypeAnswer,
		SDP:  sdp,
	}

	err := s.pc.SetRemoteDescription(answer)
	if err != nil {
		return fmt.Errorf("failed to set remote description: %w", err)
	}

	return nil
}

// AddICECandidate adds a remote ICE candidate
func (s *Session) AddICECandidate(candidate string, sdpMid string, sdpMLineIndex uint16) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	ice := webrtc.ICECandidateInit{
		Candidate:     candidate,
		SDPMid:        &sdpMid,
		SDPMLineIndex: &sdpMLineIndex,
	}

	err := s.pc.AddICECandidate(ice)
	if err != nil {
		return fmt.Errorf("failed to add ICE candidate: %w", err)
	}

	return nil
}

// WriteRTP writes an RTP packet to the video track
func (s *Session) WriteRTP(packet []byte) error {
	s.mu.Lock()
	track := s.videoTrack
	s.mu.Unlock()

	if track == nil {
		return fmt.Errorf("no video track")
	}

	// Note: In a real implementation, you'd parse the RTP packet properly
	// For now, this is a simplified version
	_, err := track.Write(packet)
	return err
}

// GetVideoTrack returns the video track for external writers
func (s *Session) GetVideoTrack() *webrtc.TrackLocalStaticRTP {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.videoTrack
}

// Close closes the WebRTC session
func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}
	s.closed = true

	if s.pc != nil {
		return s.pc.Close()
	}
	return nil
}
