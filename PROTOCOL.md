# PTZ Remote Control - WebSocket Protocol Specification

## Overview

All communication between client and server uses JSON messages over a single WebSocket connection. Messages are categorized by `type` field.

## Message Format

```json
{
  "type": "message_type",
  "payload": { ... }
}
```

## Message Types

### Connection & Session

#### `ping` (Client → Server)
Heartbeat to measure latency.
```json
{
  "type": "ping",
  "payload": {
    "timestamp": 1702500000000
  }
}
```

#### `pong` (Server → Client)
Response to ping with server timestamp for latency calculation.
```json
{
  "type": "pong",
  "payload": {
    "client_timestamp": 1702500000000,
    "server_timestamp": 1702500000005
  }
}
```

#### `status` (Server → Client)
Sent on connection and when system status changes.
```json
{
  "type": "status",
  "payload": {
    "camera_connected": true,
    "rtsp_url": "rtsp://...",
    "control_protocol": "visca",
    "video_protocol": "rtsp"
  }
}
```

---

### WebRTC Signaling

#### `offer` (Server → Client)
WebRTC SDP offer from server.
```json
{
  "type": "offer",
  "payload": {
    "sdp": "v=0\r\n..."
  }
}
```

#### `answer` (Client → Server)
WebRTC SDP answer from client.
```json
{
  "type": "answer",
  "payload": {
    "sdp": "v=0\r\n..."
  }
}
```

#### `ice_candidate` (Bidirectional)
ICE candidate exchange.
```json
{
  "type": "ice_candidate",
  "payload": {
    "candidate": "candidate:...",
    "sdp_mid": "0",
    "sdp_mline_index": 0
  }
}
```

---

### PTZ Control

#### `ptz_command` (Client → Server)
PTZ control commands. Values are normalized floats from -1.0 to 1.0.

```json
{
  "type": "ptz_command",
  "payload": {
    "pan": 0.5,
    "tilt": -0.3,
    "zoom": 0.0
  }
}
```

- `pan`: -1.0 (full left) to 1.0 (full right), 0.0 = stop
- `tilt`: -1.0 (full down) to 1.0 (full up), 0.0 = stop
- `zoom`: -1.0 (zoom out) to 1.0 (zoom in), 0.0 = stop

#### `ptz_stop` (Client → Server)
Immediately stop all PTZ movement.
```json
{
  "type": "ptz_stop",
  "payload": {}
}
```

#### `ptz_preset` (Client → Server)
Recall or save a preset position.
```json
{
  "type": "ptz_preset",
  "payload": {
    "action": "recall",
    "preset_number": 1
  }
}
```
- `action`: `"recall"` or `"save"`
- `preset_number`: 1-255

---

### Error Handling

#### `error` (Server → Client)
Error notification.
```json
{
  "type": "error",
  "payload": {
    "code": "CAMERA_DISCONNECTED",
    "message": "Lost connection to camera"
  }
}
```

Error codes:
- `CAMERA_DISCONNECTED` - Camera connection lost
- `RTSP_ERROR` - RTSP stream error
- `VISCA_ERROR` - VISCA command failed
- `INVALID_MESSAGE` - Malformed message received

---

## Connection Lifecycle

1. Client connects to `ws://server:port/ws`
2. Server sends `status` message with current state
3. Server initiates WebRTC by sending `offer`
4. Client responds with `answer`
5. Both exchange `ice_candidate` messages
6. Client sends `ptz_command` messages as gamepad input changes
7. Client sends `ping` periodically (recommended: every 1s)
8. Server responds with `pong`

## Rate Limiting

- PTZ commands should be sent at most 10 times per second
- Client should debounce/throttle gamepad input accordingly
