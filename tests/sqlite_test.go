package tests

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"procloud.io/procloud/internal/database/sqlite"
	"procloud.io/procloud/pkg/protocol"
)

func TestSQLiteWALAndPersistence(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "procloud-db-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "test.db")
	cfg := sqlite.DefaultConfig(dbPath)

	db, err := sqlite.Open(cfg)
	if err != nil {
		t.Fatalf("failed to open sqlite database: %v", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 1. Verify WAL Mode and Pragmas
	status, err := db.Status(ctx)
	if err != nil {
		t.Fatalf("failed to query db status: %v", err)
	}

	if status.Mode != "wal" {
		t.Errorf("expected journal mode 'wal', got '%s'", status.Mode)
	}
	if !status.ForeignKeys {
		t.Errorf("expected foreign keys to be enabled")
	}
	if status.BusyTimeoutMs != 5000 {
		t.Errorf("expected busy timeout 5000ms, got %d", status.BusyTimeoutMs)
	}

	// 2. Test Repository Persistence
	repo := sqlite.NewRepository(db)
	user := &protocol.User{
		ID:           "u1",
		Username:     "alice",
		Email:        "alice@procloud.io",
		PasswordHash: "secret-hash",
		Role:         "admin",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	if err := repo.CreateUser(ctx, user); err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	fetched, err := repo.GetUserByUsername(ctx, "alice")
	if err != nil {
		t.Fatalf("failed to get user: %v", err)
	}
	if fetched.Email != "alice@procloud.io" {
		t.Errorf("unexpected email: %s", fetched.Email)
	}

	// 3. Test Controlled WAL Checkpointing
	if err := db.CheckpointWAL(ctx, "PASSIVE"); err != nil {
		t.Errorf("passive checkpoint failed: %v", err)
	}
	if err := db.CheckpointWAL(ctx, "TRUNCATE"); err != nil {
		t.Errorf("truncate checkpoint failed: %v", err)
	}

	// 4. Test Live Zero-Downtime Backup (VACUUM INTO)
	backupPath := filepath.Join(tmpDir, "backup.db")
	if err := db.Backup(ctx, backupPath); err != nil {
		t.Fatalf("hot backup failed: %v", err)
	}

	if _, err := os.Stat(backupPath); os.IsNotExist(err) {
		t.Errorf("backup file was not created on disk")
	}

	// 5. Verify Backup DB Integrity
	backupCfg := sqlite.DefaultConfig(backupPath)
	backupDB, err := sqlite.Open(backupCfg)
	if err != nil {
		t.Fatalf("failed opening backup db: %v", err)
	}
	defer backupDB.Close()

	backupRepo := sqlite.NewRepository(backupDB)
	backupUser, err := backupRepo.GetUserByUsername(ctx, "alice")
	if err != nil || backupUser.Username != "alice" {
		t.Errorf("backup database verification failed: %v", err)
	}
}
