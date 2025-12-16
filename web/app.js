// PTZ Remote Control Client

class PTZClient {
    constructor() {
        this.ws = null;
        this.pc = null;
        this.latency = 0;
        this.lastPTZ = { pan: 0, tilt: 0, zoom: 0 };
        this.currentPTZ = { pan: 0, tilt: 0, zoom: 0 };
        this.gamepadIndex = null;
        this.lastPTZSendTime = 0;
        this.ptzSendInterval = 100; // 10 commands per second max
        this.pendingPTZUpdate = false;
        this.isMoving = false;

        this.elements = {
            // Connection status
            wsDot: document.getElementById('ws-dot'),
            wsStatus: document.getElementById('ws-status'),
            // Camera status
            cameraDot: document.getElementById('camera-dot'),
            cameraStatus: document.getElementById('camera-status'),
            // Latency
            latency: document.getElementById('latency'),
            // Gamepad
            gamepadDot: document.getElementById('gamepad-dot'),
            gamepadStatus: document.getElementById('gamepad-status'),
            // Video
            video: document.getElementById('video'),
            videoOverlay: document.getElementById('video-overlay'),
            videoStatus: document.getElementById('video-status'),
            // PTZ display
            joystickDot: document.getElementById('joystick-dot'),
            zoomFill: document.getElementById('zoom-fill'),
            panValue: document.getElementById('pan-value'),
            tiltValue: document.getElementById('tilt-value'),
            zoomValue: document.getElementById('zoom-value'),
            // Error
            errorBanner: document.getElementById('error-banner'),
            errorMessage: document.getElementById('error-message'),
            errorDismiss: document.getElementById('error-dismiss'),
        };

        this.init();
    }

    init() {
        this.setupErrorDismiss();
        this.connect();
        this.setupGamepad();
        this.startPingLoop();
        this.startGamepadLoop();
        this.startPTZSendLoop();
    }

    // --- WebSocket Connection ---

    connect() {
        const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
        const wsUrl = `${protocol}//${window.location.host}/ws`;

        this.updateConnectionStatus('connecting');

        try {
            this.ws = new WebSocket(wsUrl);
        } catch (e) {
            console.error('WebSocket creation failed:', e);
            this.updateConnectionStatus('disconnected');
            setTimeout(() => this.connect(), 3000);
            return;
        }

        this.ws.onopen = () => {
            console.log('WebSocket connected');
            this.updateConnectionStatus('connected');
        };

        this.ws.onclose = () => {
            console.log('WebSocket disconnected');
            this.updateConnectionStatus('disconnected');
            this.updateCameraStatus(false);
            setTimeout(() => this.connect(), 3000);
        };

        this.ws.onerror = (err) => {
            console.error('WebSocket error:', err);
        };

        this.ws.onmessage = (event) => {
            try {
                this.handleMessage(JSON.parse(event.data));
            } catch (e) {
                console.error('Failed to parse message:', e);
            }
        };
    }

    send(type, payload) {
        if (this.ws && this.ws.readyState === WebSocket.OPEN) {
            this.ws.send(JSON.stringify({ type, payload }));
        }
    }

    updateConnectionStatus(status) {
        const { wsDot, wsStatus } = this.elements;
        switch (status) {
            case 'connected':
                wsDot.className = 'w-1.5 h-1.5 rounded-full bg-green-500';
                wsStatus.textContent = 'Connected';
                wsStatus.className = 'text-green-400';
                break;
            case 'connecting':
                wsDot.className = 'w-1.5 h-1.5 rounded-full bg-yellow-500 animate-pulse';
                wsStatus.textContent = 'Connecting...';
                wsStatus.className = 'text-yellow-400';
                break;
            case 'disconnected':
                wsDot.className = 'w-1.5 h-1.5 rounded-full bg-red-500';
                wsStatus.textContent = 'Disconnected';
                wsStatus.className = 'text-red-400';
                break;
        }
    }

    // --- Message Handling ---

    handleMessage(msg) {
        switch (msg.type) {
            case 'status':
                this.handleStatus(msg.payload);
                break;
            case 'pong':
                this.handlePong(msg.payload);
                break;
            case 'offer':
                this.handleOffer(msg.payload);
                break;
            case 'ice_candidate':
                this.handleICECandidate(msg.payload);
                break;
            case 'error':
                this.handleError(msg.payload);
                break;
            default:
                console.log('Unknown message type:', msg.type);
        }
    }

    handleStatus(payload) {
        this.updateCameraStatus(payload.camera_connected);
        if (payload.control_protocol) {
            console.log('Control protocol:', payload.control_protocol);
        }
        if (payload.video_protocol) {
            console.log('Video protocol:', payload.video_protocol);
        }
    }

    updateCameraStatus(connected) {
        const { cameraDot, cameraStatus } = this.elements;
        if (connected) {
            cameraDot.className = 'w-1.5 h-1.5 rounded-full bg-green-500';
            cameraStatus.textContent = 'OK';
            cameraStatus.className = 'text-green-400';
        } else {
            cameraDot.className = 'w-1.5 h-1.5 rounded-full bg-red-500';
            cameraStatus.textContent = 'Offline';
            cameraStatus.className = 'text-red-400';
        }
    }

    handlePong(payload) {
        this.latency = Date.now() - payload.client_timestamp;
        const latencyEl = this.elements.latency;
        latencyEl.textContent = `${this.latency}ms`;

        // Color code latency
        if (this.latency < 50) {
            latencyEl.className = 'text-green-400 font-mono';
        } else if (this.latency < 150) {
            latencyEl.className = 'text-yellow-400 font-mono';
        } else {
            latencyEl.className = 'text-red-400 font-mono';
        }
    }

    handleError(payload) {
        console.error('Server error:', payload.code, payload.message);
        this.showError(`${payload.code}: ${payload.message}`);
    }

    showError(message) {
        this.elements.errorMessage.textContent = message;
        this.elements.errorBanner.classList.remove('hidden');

        // Auto-hide after 10 seconds
        setTimeout(() => {
            this.elements.errorBanner.classList.add('hidden');
        }, 10000);
    }

    setupErrorDismiss() {
        this.elements.errorDismiss.addEventListener('click', () => {
            this.elements.errorBanner.classList.add('hidden');
        });
    }

    // --- WebRTC ---

    async handleOffer(payload) {
        this.elements.videoStatus.textContent = 'Establishing connection...';

        // Close existing peer connection if any
        if (this.pc) {
            this.pc.close();
        }

        this.pc = new RTCPeerConnection({
            iceServers: [{ urls: 'stun:stun.l.google.com:19302' }]
        });

        this.pc.onicecandidate = (event) => {
            if (event.candidate) {
                this.send('ice_candidate', {
                    candidate: event.candidate.candidate,
                    sdp_mid: event.candidate.sdpMid,
                    sdp_mline_index: event.candidate.sdpMLineIndex
                });
            }
        };

        this.pc.oniceconnectionstatechange = () => {
            console.log('ICE connection state:', this.pc.iceConnectionState);
            if (this.pc.iceConnectionState === 'failed') {
                this.elements.videoStatus.textContent = 'Connection failed. Retrying...';
            }
        };

        this.pc.ontrack = (event) => {
            console.log('Received track:', event.track.kind);
            if (event.streams && event.streams[0]) {
                this.elements.video.srcObject = event.streams[0];
                this.elements.videoOverlay.classList.add('hidden');
            }
        };

        try {
            await this.pc.setRemoteDescription({ type: 'offer', sdp: payload.sdp });
            const answer = await this.pc.createAnswer();
            await this.pc.setLocalDescription(answer);
            this.send('answer', { sdp: answer.sdp });
        } catch (e) {
            console.error('WebRTC error:', e);
            this.elements.videoStatus.textContent = 'WebRTC error: ' + e.message;
        }
    }

    handleICECandidate(payload) {
        if (this.pc && payload.candidate) {
            this.pc.addIceCandidate({
                candidate: payload.candidate,
                sdpMid: payload.sdp_mid,
                sdpMLineIndex: payload.sdp_mline_index
            }).catch(e => console.error('Error adding ICE candidate:', e));
        }
    }

    // --- Gamepad ---

    setupGamepad() {
        window.addEventListener('gamepadconnected', (e) => {
            this.gamepadIndex = e.gamepad.index;
            const name = e.gamepad.id.length > 20
                ? e.gamepad.id.substring(0, 20) + '...'
                : e.gamepad.id;

            this.elements.gamepadDot.className = 'w-1.5 h-1.5 rounded-full bg-green-500';
            this.elements.gamepadStatus.textContent = name;
            this.elements.gamepadStatus.className = 'text-green-400 max-w-[150px] truncate';
        });

        window.addEventListener('gamepaddisconnected', () => {
            this.gamepadIndex = null;
            this.elements.gamepadDot.className = 'w-1.5 h-1.5 rounded-full bg-gray-500';
            this.elements.gamepadStatus.textContent = 'No gamepad';
            this.elements.gamepadStatus.className = 'text-gray-400 max-w-[150px] truncate';

            // Send stop command when gamepad disconnects
            this.sendPTZStop();
            this.updatePTZDisplay(0, 0, 0);
        });
    }

    startGamepadLoop() {
        const pollGamepad = () => {
            if (this.gamepadIndex !== null) {
                const gamepad = navigator.getGamepads()[this.gamepadIndex];
                if (gamepad) {
                    // Left stick for pan/tilt, right stick Y for zoom
                    const pan = this.applyDeadzone(gamepad.axes[0]);
                    const tilt = this.applyDeadzone(-gamepad.axes[1]); // Invert Y
                    const zoom = this.applyDeadzone(-gamepad.axes[3]); // Right stick Y, inverted

                    this.currentPTZ = { pan, tilt, zoom };
                    this.updatePTZDisplay(pan, tilt, zoom);
                }
            }
            requestAnimationFrame(pollGamepad);
        };
        pollGamepad();
    }

    applyDeadzone(value, deadzone = 0.1) {
        if (Math.abs(value) < deadzone) return 0;
        // Scale the value to start from 0 after deadzone
        const sign = value > 0 ? 1 : -1;
        return sign * ((Math.abs(value) - deadzone) / (1 - deadzone));
    }

    // --- PTZ Commands ---

    startPTZSendLoop() {
        // Rate-limited PTZ command sending (10 commands/sec max)
        setInterval(() => {
            const { pan, tilt, zoom } = this.currentPTZ;
            const threshold = 0.02;

            const hasChanged =
                Math.abs(pan - this.lastPTZ.pan) > threshold ||
                Math.abs(tilt - this.lastPTZ.tilt) > threshold ||
                Math.abs(zoom - this.lastPTZ.zoom) > threshold;

            const wasMoving = this.isMoving;
            this.isMoving = pan !== 0 || tilt !== 0 || zoom !== 0;

            if (hasChanged) {
                // If we just stopped moving, send a stop command
                if (wasMoving && !this.isMoving) {
                    this.sendPTZStop();
                } else if (this.isMoving) {
                    this.send('ptz_command', { pan, tilt, zoom });
                }
                this.lastPTZ = { pan, tilt, zoom };
            }
        }, this.ptzSendInterval);
    }

    sendPTZStop() {
        this.send('ptz_stop', {});
        this.lastPTZ = { pan: 0, tilt: 0, zoom: 0 };
        this.currentPTZ = { pan: 0, tilt: 0, zoom: 0 };
        this.isMoving = false;
    }

    updatePTZDisplay(pan, tilt, zoom) {
        // Update numeric values
        this.elements.panValue.textContent = pan.toFixed(2);
        this.elements.tiltValue.textContent = tilt.toFixed(2);
        this.elements.zoomValue.textContent = zoom.toFixed(2);

        // Update joystick visual (pan is X, tilt is Y inverted for visual)
        const joystickX = 50 + (pan * 35); // 35% max offset
        const joystickY = 50 - (tilt * 35); // Invert Y for visual
        this.elements.joystickDot.style.left = `${joystickX}%`;
        this.elements.joystickDot.style.top = `${joystickY}%`;

        // Update zoom bar visual
        const zoomFill = this.elements.zoomFill;
        if (zoom >= 0) {
            zoomFill.classList.remove('negative');
            zoomFill.style.height = `${zoom * 50}%`;
            zoomFill.style.bottom = '50%';
            zoomFill.style.top = 'auto';
        } else {
            zoomFill.classList.add('negative');
            zoomFill.style.height = `${Math.abs(zoom) * 50}%`;
            zoomFill.style.top = '50%';
            zoomFill.style.bottom = 'auto';
        }

        // Color code values based on intensity
        this.colorCodeValue(this.elements.panValue, pan);
        this.colorCodeValue(this.elements.tiltValue, tilt);
        this.colorCodeValue(this.elements.zoomValue, zoom);
    }

    colorCodeValue(element, value) {
        const absValue = Math.abs(value);
        if (absValue === 0) {
            element.className = 'text-gray-500';
        } else if (absValue < 0.3) {
            element.className = 'text-blue-400';
        } else if (absValue < 0.7) {
            element.className = 'text-yellow-400';
        } else {
            element.className = 'text-red-400';
        }
    }

    // --- Ping Loop ---

    startPingLoop() {
        setInterval(() => {
            if (this.ws && this.ws.readyState === WebSocket.OPEN) {
                this.send('ping', { timestamp: Date.now() });
            }
        }, 1000);
    }
}

// Initialize when DOM is ready
document.addEventListener('DOMContentLoaded', () => {
    window.ptzClient = new PTZClient();
});
