package main

import (
	"embed"
	"flag"
	"log"
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
	flag.Parse()

	// Create server config
	cfg := server.Config{
		ListenAddr:    *listenAddr,
		RTSPURL:       *rtspURL,
		VISCAAddress:  *viscaAddr,
		VISCAProtocol: *viscaProto,
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
		os.Exit(0)
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

	if err := srv.Start(); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
