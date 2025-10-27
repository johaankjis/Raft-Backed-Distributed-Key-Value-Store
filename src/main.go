package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"raft-kv-store/src/raft"
	"raft-kv-store/src/server"
	"raft-kv-store/src/storage"
)

func main() {
	// Command-line flags
	nodeID := flag.String("id", "node-1", "Node ID")
	port := flag.Int("port", 8080, "gRPC server port")
	httpPort := flag.Int("http-port", 9090, "HTTP API port")
	dataDir := flag.String("data-dir", "/tmp/raft-data", "Data directory")
	peersStr := flag.String("peers", "node-2,node-3", "Comma-separated list of peer IDs")
	flag.Parse()

	// Parse peers
	var peers []string
	if *peersStr != "" {
		peers = strings.Split(*peersStr, ",")
	}

	log.Printf("Starting Raft KV Store node: %s", *nodeID)
	log.Printf("Peers: %v", peers)

	// Create data directory
	nodeDataDir := fmt.Sprintf("%s/%s", *dataDir, *nodeID)
	if err := os.MkdirAll(nodeDataDir, 0755); err != nil {
		log.Fatalf("Failed to create data directory: %v", err)
	}

	// Initialize Raft node
	node, err := raft.NewNode(*nodeID, peers, nodeDataDir)
	if err != nil {
		log.Fatalf("Failed to create Raft node: %v", err)
	}

	// Initialize storage
	store, err := storage.NewStorage(nodeDataDir + "/kv")
	if err != nil {
		log.Fatalf("Failed to create storage: %v", err)
	}

	// Start Raft consensus
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	node.Start(ctx)

	// Create and start KV server
	kvServer := server.NewKVServer(node, store, *port)
	
	// Start HTTP API for testing
	go func() {
		if err := kvServer.ServeHTTP(*httpPort); err != nil {
			log.Printf("HTTP server error: %v", err)
		}
	}()

	// Start gRPC server
	go func() {
		if err := kvServer.Start(); err != nil {
			log.Fatalf("Failed to start gRPC server: %v", err)
		}
	}()

	log.Printf("Node %s started successfully", *nodeID)
	log.Printf("gRPC server listening on port %d", *port)
	log.Printf("HTTP API listening on port %d", *httpPort)

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("Shutting down...")
	cancel()
	node.Close()
	store.Close()
}
