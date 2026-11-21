package monitoring

import (
	"context"
	"log"
	"time"

	"procloud.io/procloud/internal/database/sqlite"
	"procloud.io/procloud/pkg/protocol"
)

type Service struct {
	db     *sqlite.DB
	stopCh chan struct{}
}

func NewService(db *sqlite.DB) *Service {
	return &Service{
		db:     db,
		stopCh: make(chan struct{}),
	}
}

// Start launches the background WAL coordinator and checkpoint monitor.
func (s *Service) Start(ctx context.Context) {
	go s.walCheckpointLoop(ctx)
}

func (s *Service) Stop() {
	close(s.stopCh)
}

func (s *Service) walCheckpointLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Check database status
			status, err := s.db.Status(ctx)
			if err != nil {
				continue
			}

			// If WAL exceeds 20MB, trigger TRUNCATE checkpoint
			if status.WALSizeBytes > 20*1024*1024 {
				log.Printf("[WAL Monitor] WAL size is %d bytes, triggering TRUNCATE checkpoint...", status.WALSizeBytes)
				if err := s.db.CheckpointWAL(ctx, "TRUNCATE"); err != nil {
					log.Printf("[WAL Monitor] Checkpoint error: %v", err)
				}
			} else {
				// Routine PASSIVE checkpoint
				_ = s.db.CheckpointWAL(ctx, "PASSIVE")
			}
		}
	}
}

func (s *Service) GetDatabaseStatus(ctx context.Context) (*protocol.DatabaseStatus, error) {
	return s.db.Status(ctx)
}

func (s *Service) TriggerCheckpoint(ctx context.Context, mode string) error {
	return s.db.CheckpointWAL(ctx, mode)
}

func (s *Service) BackupDatabase(ctx context.Context, destPath string) error {
	return s.db.Backup(ctx, destPath)
}
