package tests

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"procloud.io/procloud/internal/database/sqlite"
	"procloud.io/procloud/internal/storage"
	"procloud.io/procloud/pkg/podman"
	"procloud.io/procloud/pkg/protocol"
)

func TestStorageVolumeBackupAndRestore(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "procloud-storage-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "storage.db")
	db, err := sqlite.Open(sqlite.DefaultConfig(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	repo := sqlite.NewRepository(db)
	runtime := podman.NewMockRuntime()
	storageSvc := storage.NewService(repo, runtime, filepath.Join(tmpDir, "volumes"))

	ctx := context.Background()

	// Create Tenant and Project for foreign key integrity
	tenant := &protocol.Tenant{ID: "tnt-1", Name: "Test Tenant", Slug: "test-tenant", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := repo.CreateTenant(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	project := &protocol.Project{ID: "prj-1", TenantID: "tnt-1", Name: "Test Project", Slug: "test-proj", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := repo.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}

	// 1. Create Volume
	vol, err := storageSvc.CreateVolume(ctx, "tnt-1", "prj-1", "db-volume", 1024)
	if err != nil {
		t.Fatalf("failed to create volume: %v", err)
	}

	// 2. Write Data
	testFile := filepath.Join(vol.HostPath, "test.txt")
	if err := os.WriteFile(testFile, []byte("procloud storage engine v1"), 0644); err != nil {
		t.Fatal(err)
	}

	// 3. Backup Volume
	backupDir := filepath.Join(tmpDir, "backups")
	backupFile, err := storageSvc.BackupVolume(ctx, vol.ID, backupDir)
	if err != nil {
		t.Fatalf("backup failed: %v", err)
	}

	// 4. Mutate Data
	_ = os.WriteFile(testFile, []byte("corrupted data"), 0644)

	// 5. Restore Volume
	if err := storageSvc.RestoreVolume(ctx, vol.ID, backupFile); err != nil {
		t.Fatalf("restore failed: %v", err)
	}

	// 6. Verify restored data
	restoredContent, err := os.ReadFile(testFile)
	if err != nil || string(restoredContent) != "procloud storage engine v1" {
		t.Fatalf("restoration mismatch: expected 'procloud storage engine v1', got '%s'", string(restoredContent))
	}
}
