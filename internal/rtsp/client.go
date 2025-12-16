package rtsp

import (
	"log"
	"sync"
	"time"

	"github.com/bluenviron/gortsplib/v4"
	"github.com/bluenviron/gortsplib/v4/pkg/base"
	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/bluenviron/gortsplib/v4/pkg/format"
	"github.com/pion/rtp"
)

// Client handles RTSP connection and RTP streaming using gortsplib
type Client struct {
	url     string
	rtpChan chan []byte
	stopCh  chan struct{}

	mu      sync.Mutex
	client  *gortsplib.Client
	stopped bool
}

// NewClient creates a new RTSP client
func NewClient(rtspURL string) (*Client, error) {
	// Validate URL by parsing it
	_, err := base.ParseURL(rtspURL)
	if err != nil {
		return nil, err
	}

	return &Client{
		url:     rtspURL,
		rtpChan: make(chan []byte, 500),
		stopCh:  make(chan struct{}),
	}, nil
}

// Connect establishes the RTSP connection and starts streaming
func (c *Client) Connect() error {
	if err := c.connect(); err != nil {
		return err
	}
	return nil
}

func (c *Client) connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	client := &gortsplib.Client{
		// Use TCP transport (interleaved)
		Transport: func() *gortsplib.Transport {
			t := gortsplib.TransportTCP
			return &t
		}(),
		// Connection timeout
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		// Callback when connection is closed
		OnDecodeError: func(err error) {
			log.Printf("RTSP: Decode error: %v", err)
		},
	}

	// Parse URL
	u, err := base.ParseURL(c.url)
	if err != nil {
		return err
	}

	// Connect to server
	err = client.Start(u.Scheme, u.Host)
	if err != nil {
		return err
	}

	// Get session description
	desc, _, err := client.Describe(u)
	if err != nil {
		client.Close()
		return err
	}

	// Find H264 or H265 video format
	var videoFormat format.Format
	var videoMedia *description.Media

	for _, media := range desc.Medias {
		for _, fmt := range media.Formats {
			switch fmt.(type) {
			case *format.H264, *format.H265:
				videoFormat = fmt
				videoMedia = media
				break
			}
		}
		if videoFormat != nil {
			break
		}
	}

	if videoFormat == nil {
		// Fallback: use first video media
		for _, media := range desc.Medias {
			if media.Type == description.MediaTypeVideo && len(media.Formats) > 0 {
				videoFormat = media.Formats[0]
				videoMedia = media
				break
			}
		}
	}

	if videoFormat == nil {
		client.Close()
		return err
	}

	// Setup the video track
	_, err = client.Setup(desc.BaseURL, videoMedia, 0, 0)
	if err != nil {
		client.Close()
		return err
	}

	// Set callback to receive RTP packets
	client.OnPacketRTPAny(func(media *description.Media, forma format.Format, pkt *rtp.Packet) {
		// Serialize RTP packet to bytes
		buf, err := pkt.Marshal()
		if err != nil {
			return
		}

		// Copy the packet data
		packet := make([]byte, len(buf))
		copy(packet, buf)

		select {
		case c.rtpChan <- packet:
		case <-c.stopCh:
			return
		default:
			// Drop packet if channel full
		}
	})

	// Start playing
	_, err = client.Play(nil)
	if err != nil {
		client.Close()
		return err
	}

	c.client = client
	log.Printf("RTSP: Connected and playing")

	// Start reconnection monitor
	go c.monitorConnection()

	return nil
}

// monitorConnection watches for disconnection and reconnects
func (c *Client) monitorConnection() {
	c.mu.Lock()
	client := c.client
	c.mu.Unlock()

	if client == nil {
		return
	}

	// Wait for client to close
	err := client.Wait()

	select {
	case <-c.stopCh:
		return
	default:
	}

	if err != nil {
		log.Printf("RTSP: Connection lost: %v", err)
	}

	// Reconnect with exponential backoff
	for attempt := 1; ; attempt++ {
		select {
		case <-c.stopCh:
			return
		default:
		}

		delay := min(time.Duration(1<<uint(attempt-1))*time.Second, 30*time.Second)
		log.Printf("RTSP: Reconnect attempt %d in %v", attempt, delay)
		time.Sleep(delay)

		select {
		case <-c.stopCh:
			return
		default:
		}

		if err := c.connect(); err != nil {
			log.Printf("RTSP: Reconnect failed: %v", err)
			continue
		}

		log.Printf("RTSP: Reconnected successfully")
		return
	}
}

// RTPChannel returns the channel for receiving RTP packets
func (c *Client) RTPChannel() <-chan []byte {
	return c.rtpChan
}

// Close closes the RTSP connection
func (c *Client) Close() error {
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return nil
	}
	c.stopped = true
	client := c.client
	c.mu.Unlock()

	close(c.stopCh)
	close(c.rtpChan)

	if client != nil {
		client.Close()
	}
	return nil
}
