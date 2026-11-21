package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"procloud.io/procloud/internal/compute"
	"procloud.io/procloud/internal/database/sqlite"
	"procloud.io/procloud/pkg/podman"
)

func main() {
	dbPath := flag.String("db", "procloud.db", "SQLite database path")
	interval := flag.Duration("interval", 15*time.Second, "Heartbeat interval")
	flag.Parse()

	log.Printf("[Agent] Starting ProCloud Node Health & Telemetry Agent...")

	dbCfg := sqlite.DefaultConfig(*dbPath)
	db, err := sqlite.Open(dbCfg)
	if err != nil {
		log.Fatalf("Fatal: failed to open SQLite db: %v", err)
	}
	defer db.Close()

	runtime, err := podman.NewPodmanCLI()
	var rt podman.ContainerRuntime = runtime
	if err != nil {
		rt = podman.NewMockRuntime()
	}

	repo := sqlite.NewRepository(db)
	svc := compute.NewService(repo, rt)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	node, err := svc.RegisterLocalHost(ctx)
	if err != nil {
		log.Fatalf("Fatal: failed to register host: %v", err)
	}
	log.Printf("[Agent] Registered host node %s (%s)", node.Hostname, node.ID)

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	for {
		select {
		case <-sigCh:
			log.Printf("[Agent] Stopping node agent...")
			return
		case <-ticker.C:
			if err := svc.Heartbeat(ctx, node.ID); err != nil {
				log.Printf("[Agent] Warning: heartbeat failed: %v", err)
			}
		}
	}
}
