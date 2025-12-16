# PTZ Remote Control App

## Tech Stack
Backend: Golang server, websockets, WebRTC
Frontend: Plain JS (no frameworks), Tailwind CSS

## Overview

This is a remote control and viewing app for professional PTZ cameras. It provides remote real time re-streaming of the camera source and real time remote control via a game controller.

The backend server communicates with the client frontend via a WebSocket connection. It uses this socket to transmit WebRTC session data as well as the PTZ commands.

The server will eventually want to support multiple types of video backend protocols (RTSP, USB camera) and control protocols (VISCA, Panasonic). Initially, only RTSP and VISCA are supported.

The frontend uses the HTML5 Gamepad API to allow remote control of the camera. It has pan support on one joystick and zoom on the other. If none is connected it still allows viewing of the camera.

Video is streamed over WebRTC. Ideally no re-encoding is performed.

On the backend, use off the shelf libraries where possible for WebRTC communications (pion) and for the WebSocket connection.

The frontend should avoid using frameworks and prefer plain modern JS. It should live in a few static files that can be bundled with the Go app via the standard Go build process so that the entire server is a single binary.

Tailwind CSS is used to provide a clean and professional yet minimal UI for the frontend. It should show the gamepad status, latency, and currently held commands.

---

## Backend Implementation Notes

### Project Structure

```
ptz-remote/
├── main.go                      # Entry point, CLI flags, embeds static files
├── internal/
│   ├── protocol/messages.go     # WebSocket message types and JSON serialization
│   ├── server/server.go         # HTTP server, WebSocket handling, client management
│   ├── webrtc/webrtc.go         # WebRTC session management using Pion
│   ├── rtsp/client.go           # RTSP client for camera feed ingestion
│   └── visca/visca.go           # VISCA-over-IP protocol for PTZ control
└── web/                         # Frontend static files (embedded via go:embed)
    ├── index.html
    └── app.js
```

### RTSP Client (`internal/rtsp/`)

- Connects to RTSP source and negotiates interleaved TCP transport
- Parses SDP from DESCRIBE response to find correct video track control URL
- Handles both absolute and relative control URLs in SDP
- Single read loop started via `sync.Once` to prevent multiple goroutines
- 256KB buffered reader to handle large video frames
- RTP packets broadcast to all connected WebRTC clients via per-client channels

### VISCA Controller (`internal/visca/`)

- Supports VISCA-over-IP via UDP (default) or raw VISCA over TCP
- UDP uses standard VISCA-over-IP framing: 8-byte header (type, length, sequence) + payload
- Fire-and-forget command sending - no waiting for ACK responses
- Built-in rate limiting: max 20 commands/sec (50ms interval) to prevent flooding
- Stop commands bypass rate limiting for immediate response
- Default port for VISCA-over-IP is 52381

### WebRTC (`internal/webrtc/`)

- Uses Pion WebRTC library
- Server creates offer with H264 video track
- RTP packets from RTSP written directly to track (no re-encoding)
- ICE candidates exchanged via WebSocket signaling

### Server Architecture (`internal/server/`)

- Single RTSP connection shared across all clients
- Broadcast goroutine distributes RTP packets to per-client channels (500 packet buffer)
- Each client has dedicated WebRTC session and RTP forwarding goroutine
- WebSocket handles signaling (offer/answer/ICE) and PTZ commands
- Graceful shutdown with proper resource cleanup

### CLI Usage

```bash
# Basic - web UI only
./ptz-remote

# With RTSP video source
./ptz-remote -rtsp "rtsp://192.168.1.100:554/stream"

# With VISCA PTZ control (UDP, default)
./ptz-remote -visca "192.168.1.100:52381"

# Full setup
./ptz-remote -listen ":8080" \
             -rtsp "rtsp://192.168.1.100:554/stream" \
             -visca "192.168.1.100:52381"

# VISCA over TCP (if needed)
./ptz-remote -visca "192.168.1.100:5678" -visca-proto tcp
```

### Protocol

See `PROTOCOL.md` for WebSocket message format specification.

---

## Frontend Implementation Notes

### Files

- `web/index.html` - Single-page UI with Tailwind CSS (via CDN)
- `web/app.js` - PTZClient class handling all client-side logic

### Layout

- Full-viewport design optimized for maximum video display
- Thin header bar with connection status, camera status, latency, and gamepad info
- Video fills remaining screen height with `flex-1`
- PTZ controls rendered as semi-transparent overlay in bottom-right of video
- No scrolling (`overflow: hidden` on body)

### PTZClient Class

**WebSocket Connection:**
- Auto-connects to `/ws` endpoint on page load
- Auto-reconnects after 3 seconds on disconnect
- Visual status indicator (green/yellow/red dot)

**WebRTC Video:**
- Receives SDP offer from server, sends answer
- Handles ICE candidate exchange
- Video element with `object-contain` for proper aspect ratio
- Overlay hidden once video track is received

**Gamepad Integration:**
- Uses HTML5 Gamepad API
- Left stick: Pan (X-axis) and Tilt (Y-axis, inverted)
- Right stick: Zoom (Y-axis, inverted)
- 10% deadzone with scaled output
- Sends `ptz_stop` when gamepad disconnects

**Rate Limiting:**
- PTZ commands sent at most 10 times per second (100ms interval)
- Change threshold of 0.02 to avoid sending redundant updates
- Automatic `ptz_stop` when movement ceases

**UI Updates:**
- Joystick visualization shows pan/tilt position
- Vertical bar shows zoom direction and intensity
- Latency color-coded: green (<50ms), yellow (<150ms), red (>150ms)
- Dismissable error banner for server errors

### Protocol Messages Handled

| Message | Direction | Purpose |
|---------|-----------|---------|
| `ping` | Client → Server | Latency measurement (1/sec) |
| `pong` | Server → Client | Latency response |
| `status` | Server → Client | Camera connection state |
| `offer` | Server → Client | WebRTC SDP offer |
| `answer` | Client → Server | WebRTC SDP answer |
| `ice_candidate` | Bidirectional | ICE candidate exchange |
| `ptz_command` | Client → Server | Pan/tilt/zoom values (-1.0 to 1.0) |
| `ptz_stop` | Client → Server | Immediate stop all movement |
| `error` | Server → Client | Error notifications |
