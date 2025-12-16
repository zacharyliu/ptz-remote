package server

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	pwebrtc "github.com/pion/webrtc/v3"

	"ptz-remote/internal/protocol"
	"ptz-remote/internal/rtsp"
	"ptz-remote/internal/visca"
	"ptz-remote/internal/webrtc"
)

// Config for the server
type Config struct {
	ListenAddr    string
	RTSPURL       string
	VISCAAddress  string
	VISCAProtocol string // "udp" or "tcp"
}

// Server is the main PTZ remote server
type Server struct {
	cfg        Config
	clients    map[*Client]bool
	clientsMu  sync.RWMutex
	rtspClient *rtsp.Client
	viscaCtrl  *visca.Controller
	upgrader   websocket.Upgrader
	staticFS   fs.FS
}

// Client represents a connected WebSocket client
type Client struct {
	conn    *websocket.Conn
	server  *Server
	webrtc  *webrtc.Session
	send    chan []byte
	rtpChan chan []byte // Per-client RTP channel
	stopRTP chan struct{}
	mu      sync.Mutex
	closed  bool
}

// New creates a new server instance
func New(cfg Config, staticFS embed.FS) (*Server, error) {
	// Extract the web subdirectory from embedded FS
	webFS, err := fs.Sub(staticFS, "web")
	if err != nil {
		return nil, fmt.Errorf("failed to access embedded web files: %w", err)
	}

	s := &Server{
		cfg:      cfg,
		clients:  make(map[*Client]bool),
		staticFS: webFS,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin: func(r *http.Request) bool {
				return true // Allow all origins for local use
			},
		},
	}

	return s, nil
}

// Start starts the server
func (s *Server) Start() error {
	// Connect to RTSP if configured
	if s.cfg.RTSPURL != "" {
		client, err := rtsp.NewClient(s.cfg.RTSPURL)
		if err != nil {
			log.Printf("Warning: Failed to create RTSP client: %v", err)
		} else {
			if err := client.Connect(); err != nil {
				log.Printf("Warning: Failed to connect to RTSP: %v", err)
			} else {
				s.rtspClient = client
				log.Printf("Connected to RTSP: %s", s.cfg.RTSPURL)
				// Start broadcasting RTP packets to all clients
				go s.broadcastRTP()
			}
		}
	}

	// Connect to VISCA if configured
	if s.cfg.VISCAAddress != "" {
		ctrl, err := visca.NewController(visca.Config{
			Address:  s.cfg.VISCAAddress,
			Protocol: s.cfg.VISCAProtocol,
		})
		if err != nil {
			log.Printf("Warning: Failed to create VISCA controller: %v", err)
		} else {
			s.viscaCtrl = ctrl
			log.Printf("Connected to VISCA: %s", s.cfg.VISCAAddress)
		}
	}

	// Set up HTTP routes
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handleWebSocket)
	mux.Handle("/", http.FileServer(http.FS(s.staticFS)))

	log.Printf("Server starting on %s", s.cfg.ListenAddr)
	return http.ListenAndServe(s.cfg.ListenAddr, mux)
}

// broadcastRTP reads from RTSP and sends to all connected clients
func (s *Server) broadcastRTP() {
	rtpChan := s.rtspClient.RTPChannel()

	for packet := range rtpChan {
		s.clientsMu.RLock()
		for client := range s.clients {
			// Non-blocking send to each client's RTP channel
			select {
			case client.rtpChan <- packet:
			default:
				// Client's buffer full, drop packet for this client
			}
		}
		s.clientsMu.RUnlock()
	}
}

// Stop stops the server
func (s *Server) Stop() {
	s.clientsMu.Lock()
	for client := range s.clients {
		client.Close()
	}
	s.clientsMu.Unlock()

	if s.rtspClient != nil {
		s.rtspClient.Close()
	}
	if s.viscaCtrl != nil {
		s.viscaCtrl.Close()
	}
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return
	}

	client := &Client{
		conn:    conn,
		server:  s,
		send:    make(chan []byte, 256),
		rtpChan: make(chan []byte, 500),
		stopRTP: make(chan struct{}),
	}

	s.clientsMu.Lock()
	s.clients[client] = true
	s.clientsMu.Unlock()

	// Start client goroutines
	go client.writePump()
	go client.readPump()

	// Send initial status
	client.sendStatus()

	// Initialize WebRTC session
	if err := client.initWebRTC(); err != nil {
		log.Printf("Failed to initialize WebRTC: %v", err)
	}
}

func (c *Client) initWebRTC() error {
	// Create WebRTC session
	session, err := webrtc.NewSession(webrtc.DefaultConfig(), func(candidate *pwebrtc.ICECandidate) {
		// Send ICE candidate to client
		payload := protocol.ICECandidatePayload{
			Candidate:     candidate.ToJSON().Candidate,
			SDPMid:        *candidate.ToJSON().SDPMid,
			SDPMLineIndex: *candidate.ToJSON().SDPMLineIndex,
		}
		c.sendMessage(protocol.TypeICECandidate, payload)
	})
	if err != nil {
		return err
	}
	c.webrtc = session

	// Add video track
	if err := session.AddH264Track(); err != nil {
		return err
	}

	// Create and send offer
	offer, err := session.CreateOffer()
	if err != nil {
		return err
	}

	c.sendMessage(protocol.TypeOffer, protocol.SDPPayload{SDP: offer})

	// Start forwarding RTP from client's channel to WebRTC
	if c.server.rtspClient != nil {
		go c.forwardRTP()
	}

	return nil
}

func (c *Client) forwardRTP() {
	track := c.webrtc.GetVideoTrack()
	if track == nil {
		return
	}

	for {
		select {
		case <-c.stopRTP:
			return
		case packet, ok := <-c.rtpChan:
			if !ok {
				return
			}
			if _, err := track.Write(packet); err != nil {
				// Client disconnected or track closed
				return
			}
		}
	}
}

func (c *Client) sendStatus() {
	status := protocol.StatusPayload{
		CameraConnected: c.server.rtspClient != nil,
		RTSPURL:         c.server.cfg.RTSPURL,
		ControlProtocol: "visca",
		VideoProtocol:   "rtsp",
	}
	c.sendMessage(protocol.TypeStatus, status)
}

func (c *Client) sendMessage(msgType string, payload any) {
	msg, err := protocol.NewMessage(msgType, payload)
	if err != nil {
		log.Printf("Failed to create message: %v", err)
		return
	}

	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("Failed to marshal message: %v", err)
		return
	}

	select {
	case c.send <- data:
	default:
		log.Printf("Client send buffer full, dropping message")
	}
}

func (c *Client) readPump() {
	defer func() {
		c.server.clientsMu.Lock()
		delete(c.server.clients, c)
		c.server.clientsMu.Unlock()
		c.Close()
	}()

	c.conn.SetReadLimit(65536)
	c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("WebSocket error: %v", err)
			}
			return
		}

		c.handleMessage(data)
	}
}

func (c *Client) handleMessage(data []byte) {
	var msg protocol.Message
	if err := json.Unmarshal(data, &msg); err != nil {
		c.sendMessage(protocol.TypeError, protocol.ErrorPayload{
			Code:    protocol.ErrInvalidMessage,
			Message: "Failed to parse message",
		})
		return
	}

	switch msg.Type {
	case protocol.TypePing:
		var payload protocol.PingPayload
		if err := msg.ParsePayload(&payload); err != nil {
			return
		}
		c.sendMessage(protocol.TypePong, protocol.PongPayload{
			ClientTimestamp: payload.Timestamp,
			ServerTimestamp: time.Now().UnixMilli(),
		})

	case protocol.TypeAnswer:
		var payload protocol.SDPPayload
		if err := msg.ParsePayload(&payload); err != nil {
			return
		}
		if c.webrtc != nil {
			if err := c.webrtc.SetAnswer(payload.SDP); err != nil {
				log.Printf("Failed to set answer: %v", err)
			}
		}

	case protocol.TypeICECandidate:
		var payload protocol.ICECandidatePayload
		if err := msg.ParsePayload(&payload); err != nil {
			return
		}
		if c.webrtc != nil {
			if err := c.webrtc.AddICECandidate(payload.Candidate, payload.SDPMid, payload.SDPMLineIndex); err != nil {
				log.Printf("Failed to add ICE candidate: %v", err)
			}
		}

	case protocol.TypePTZCommand:
		var payload protocol.PTZCommandPayload
		if err := msg.ParsePayload(&payload); err != nil {
			return
		}
		c.handlePTZCommand(payload)

	case protocol.TypePTZStop:
		if c.server.viscaCtrl != nil {
			if err := c.server.viscaCtrl.Stop(); err != nil {
				log.Printf("Failed to stop PTZ: %v", err)
			}
		}

	case protocol.TypePTZPreset:
		var payload protocol.PTZPresetPayload
		if err := msg.ParsePayload(&payload); err != nil {
			return
		}
		c.handlePTZPreset(payload)

	default:
		log.Printf("Unknown message type: %s", msg.Type)
	}
}

func (c *Client) handlePTZCommand(cmd protocol.PTZCommandPayload) {
	if c.server.viscaCtrl == nil {
		return
	}

	// Send pan/tilt command
	if err := c.server.viscaCtrl.PanTilt(cmd.Pan, cmd.Tilt); err != nil {
		log.Printf("PTZ pan/tilt error: %v", err)
	}

	// Send zoom command
	if err := c.server.viscaCtrl.Zoom(cmd.Zoom); err != nil {
		log.Printf("PTZ zoom error: %v", err)
	}
}

func (c *Client) handlePTZPreset(preset protocol.PTZPresetPayload) {
	if c.server.viscaCtrl == nil {
		return
	}

	switch preset.Action {
	case "recall":
		if err := c.server.viscaCtrl.RecallPreset(preset.PresetNumber); err != nil {
			log.Printf("Failed to recall preset: %v", err)
		}
	case "save":
		if err := c.server.viscaCtrl.SavePreset(preset.PresetNumber); err != nil {
			log.Printf("Failed to save preset: %v", err)
		}
	}
}

func (c *Client) writePump() {
	ticker := time.NewTicker(30 * time.Second)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// Close closes the client connection
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return
	}
	c.closed = true

	// Stop RTP forwarding
	close(c.stopRTP)

	if c.webrtc != nil {
		c.webrtc.Close()
		c.webrtc = nil
	}

	close(c.send)
}
