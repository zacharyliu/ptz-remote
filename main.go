package main

import (
	"embed"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"ptz-remote/internal/server"
)

//go:embed web/*
var staticFiles embed.FS

func main() {
	// Command line flags
	listenAddr := flag.String("listen", ":8080", "HTTP listen address")
	rtspURL := flag.String("rtsp", "", "RTSP URL for camera stream")
	viscaAddr := flag.String("visca", "", "VISCA address (TCP: host:port)")
	viscaProto := flag.String("visca-proto", "udp", "VISCA protocol (udp or tcp)")
	iceIPs := flag.String("ice-ips", "", "Comma-separated list of static server IPs (enables ICE-lite mode)")
	flag.Parse()

	// Create server config
	cfg := server.Config{
		ListenAddr:    *listenAddr,
		RTSPURL:       *rtspURL,
		VISCAAddress:  *viscaAddr,
		VISCAProtocol: *viscaProto,
		ICEIPs:        *iceIPs,
	}

	// Create server
	srv, err := server.New(cfg, staticFiles)
	if err != nil {
		log.Fatalf("Failed to create server: %v", err)
	}

	// Handle graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		log.Println("Shutting down...")
		srv.Stop()
	}()

	// Start server
	log.Printf("PTZ Remote Control Server")
	log.Printf("  Listen: %s", cfg.ListenAddr)
	if cfg.RTSPURL != "" {
		log.Printf("  RTSP: %s", cfg.RTSPURL)
	}
	if cfg.VISCAAddress != "" {
		log.Printf("  VISCA: %s (%s)", cfg.VISCAAddress, cfg.VISCAProtocol)
	}
	if cfg.ICEIPs != "" {
		log.Printf("  WebRTC: ICE-lite mode enabled with IPs: %s", cfg.ICEIPs)
	}

	if err := srv.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("Server error: %v", err)
	}
}
