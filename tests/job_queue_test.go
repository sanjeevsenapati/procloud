package tests

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"procloud.io/procloud/internal/database/sqlite"
	"procloud.io/procloud/internal/job"
)

func TestDurableSQLiteJobQueue(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "procloud-job-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "jobs.db")
	db, err := sqlite.Open(sqlite.DefaultConfig(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	repo := sqlite.NewRepository(db)
	engine := job.NewEngine(repo, "test-worker-1")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var processedCount int32
	engine.RegisterHandler("email_dispatch", func(ctx context.Context, payload []byte) error {
		atomic.AddInt32(&processedCount, 1)
		return nil
	})

	if err := engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer engine.Stop()

	// Enqueue jobs
	for i := 0; i < 5; i++ {
		_, err := engine.Enqueue(ctx, "email_dispatch", map[string]int{"item": i}, "")
		if err != nil {
			t.Fatalf("enqueue failed: %v", err)
		}
	}

	// Wait for worker processing
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&processedCount) == 5 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if atomic.LoadInt32(&processedCount) != 5 {
		t.Errorf("expected 5 processed jobs, got %d", atomic.LoadInt32(&processedCount))
	}

	// Verify Interrupted Job Recovery on Startup
	// Manually insert an uncompleted RUNNING job
	_, _ = db.SQLDB().Exec(`
		INSERT INTO jobs (id, type, status, payload_json, scheduled_at, created_at)
		VALUES ('interrupted-1', 'email_dispatch', 'RUNNING', '{}', ?, ?)
	`, time.Now(), time.Now())

	recovered, err := repo.RecoverInterruptedJobs(ctx)
	if err != nil {
		t.Fatalf("failed recovering interrupted jobs: %v", err)
	}
	if recovered != 1 {
		t.Errorf("expected 1 recovered job, got %d", recovered)
	}

	job1, _ := repo.GetJobByID(ctx, "interrupted-1")
	if job1.Status != "QUEUED" {
		t.Errorf("expected recovered job status QUEUED, got %s", job1.Status)
	}
}
