package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"procloud.io/procloud/internal/database/sqlite"
	"procloud.io/procloud/pkg/podman"
)

func main() {
	port := flag.Int("port", 8080, "HTTP server listening port")
	dbPath := flag.String("db", "procloud.db", "SQLite database path")
	useMockRuntime := flag.Bool("mock", false, "Use in-memory mock container runtime")
	flag.Parse()

	log.Printf("==========================================================")
	log.Printf(" ⚡ ProCloud Control Plane — Single Node SQLite-First")
	log.Printf("==========================================================")

	// 1. Initialise SQLite with WAL Mode per specification
	dbCfg := sqlite.DefaultConfig(*dbPath)
	db, err := sqlite.Open(dbCfg)
	if err != nil {
		log.Fatalf("Fatal: failed to initialise SQLite database: %v", err)
	}
	defer db.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	status, err := db.Status(ctx)
	if err == nil {
		log.Printf("[Database] Mode: %s | ForeignKeys: %v | Synchronous: %s | BusyTimeout: %dms",
			status.Mode, status.ForeignKeys, status.Synchronous, status.BusyTimeoutMs)
	}

	// 2. Initialise Container Runtime
	var runtime podman.ContainerRuntime
	if *useMockRuntime {
		log.Printf("[Runtime] Using mock container runtime")
		runtime = podman.NewMockRuntime()
	} else {
		cli, err := podman.NewPodmanCLI()
		if err != nil {
			log.Printf("[Runtime] Podman binary not detected, falling back to mock runtime: %v", err)
			runtime = podman.NewMockRuntime()
		} else {
			log.Printf("[Runtime] Podman CLI connected")
			runtime = cli
		}
	}

	repo := sqlite.NewRepository(db)
	server := NewServer(db, repo, runtime)

	// 3. Register local compute node
	node, err := server.computeSvc.RegisterLocalHost(ctx)
	if err == nil {
		log.Printf("[Compute] Primary node registered: %s (%s, %s, Podman %s)",
			node.Hostname, node.Arch, node.OS, node.PodmanVersion)
	}

	// 4. Start background engines (WAL monitor, job queue)
	server.monitoringSvc.Start(ctx)
	defer server.monitoringSvc.Stop()

	if err := server.jobEngine.Start(ctx); err != nil {
		log.Printf("[Job Engine] Warning: failed starting job queue: %v", err)
	}
	defer server.jobEngine.Stop()

	// 5. Start HTTP server
	httpServer := &http.Server{
		Addr:         fmt.Sprintf(":%d", *port),
		Handler:      server.mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
	}

	go func() {
		log.Printf("[Server] Listening on http://0.0.0.0:%d", *port)
		log.Printf("[Server] Web Console available at http://localhost:%d", *port)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh

	log.Printf("[Shutdown] Gracefully shutting down ProCloud...")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	_ = httpServer.Shutdown(shutdownCtx)
	log.Printf("[Shutdown] ProCloud control plane stopped cleanly.")
}
