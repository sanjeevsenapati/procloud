package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"procloud.io/procloud/internal/database/sqlite"
	"procloud.io/procloud/internal/job"
)

func main() {
	dbPath := flag.String("db", "procloud.db", "SQLite database path")
	workerID := flag.String("id", "worker-node-1", "Worker identifier")
	flag.Parse()

	log.Printf("[Worker] Starting ProCloud Background Worker %s...", *workerID)

	dbCfg := sqlite.DefaultConfig(*dbPath)
	db, err := sqlite.Open(dbCfg)
	if err != nil {
		log.Fatalf("Fatal: failed to open SQLite db: %v", err)
	}
	defer db.Close()

	repo := sqlite.NewRepository(db)
	engine := job.NewEngine(repo, *workerID)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := engine.Start(ctx); err != nil {
		log.Fatalf("Fatal: failed to start worker engine: %v", err)
	}
	defer engine.Stop()

	log.Printf("[Worker] Worker %s active and waiting for durable jobs...", *workerID)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh

	log.Printf("[Worker] Shutting down worker...")
}
