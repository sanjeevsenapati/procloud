package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
	"procloud.io/procloud/internal/database/migrations"
	"procloud.io/procloud/pkg/protocol"
)

// DB wraps the SQLite database with WAL management, retries, and monitoring.
type DB struct {
	sqlDB          *sql.DB
	dbPath         string
	mu             sync.RWMutex
	lastBackupTime time.Time
	lastBackupPath string
}

// Config configures the SQLite database engine.
type Config struct {
	Path            string
	BusyTimeoutMs   int
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

// DefaultConfig returns production-ready SQLite defaults per project.md specification.
func DefaultConfig(path string) Config {
	if path == "" {
		path = "procloud.db"
	}
	return Config{
		Path:            path,
		BusyTimeoutMs:   5000,
		MaxOpenConns:    50,
		MaxIdleConns:    25,
		ConnMaxLifetime: 30 * time.Minute,
	}
}

// Open initializes SQLite connection with WAL mode, foreign keys, and run migrations.
func Open(cfg Config) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(cfg.Path), 0755); err != nil && filepath.Dir(cfg.Path) != "." {
		return nil, fmt.Errorf("failed to create db directory: %w", err)
	}

	// SQLite connection string with busy timeout and WAL pragmas
	dsn := fmt.Sprintf("%s?_pragma=busy_timeout(%d)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)",
		cfg.Path, cfg.BusyTimeoutMs)

	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database: %w", err)
	}

	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(cfg.ConnMaxLifetime)

	// Ensure pragmas are applied to primary connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pragmas := []string{
		"PRAGMA journal_mode = WAL;",
		"PRAGMA foreign_keys = ON;",
		fmt.Sprintf("PRAGMA busy_timeout = %d;", cfg.BusyTimeoutMs),
		"PRAGMA synchronous = NORMAL;",
		"PRAGMA temp_store = MEMORY;",
	}

	for _, p := range pragmas {
		if _, err := sqlDB.ExecContext(ctx, p); err != nil {
			sqlDB.Close()
			return nil, fmt.Errorf("failed pragma '%s': %w", p, err)
		}
	}

	db := &DB{
		sqlDB:  sqlDB,
		dbPath: cfg.Path,
	}

	// Run migrations
	if err := db.Migrate(ctx); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("failed to apply migrations: %w", err)
	}

	return db, nil
}

// Close closes the database connection.
func (d *DB) Close() error {
	return d.sqlDB.Close()
}

// SQLDB returns the underlying *sql.DB for repository access.
func (d *DB) SQLDB() *sql.DB {
	return d.sqlDB
}

// Migrate executes all versioned migrations sequentially within transactions.
func (d *DB) Migrate(ctx context.Context) error {
	_, err := d.sqlDB.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			description TEXT NOT NULL,
			applied_at DATETIME NOT NULL
		);
	`)
	if err != nil {
		return fmt.Errorf("failed to create schema_migrations table: %w", err)
	}

	for _, m := range migrations.AllMigrations {
		var exists int
		err := d.sqlDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE version = ?", m.Version).Scan(&exists)
		if err != nil {
			return fmt.Errorf("failed to query migration status for v%d: %w", m.Version, err)
		}

		if exists == 0 {
			tx, err := d.sqlDB.BeginTx(ctx, nil)
			if err != nil {
				return fmt.Errorf("failed to begin migration transaction for v%d: %w", m.Version, err)
			}

			if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
				tx.Rollback()
				return fmt.Errorf("failed executing migration v%d (%s): %w", m.Version, m.Description, err)
			}

			if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations (version, description, applied_at) VALUES (?, ?, ?)",
				m.Version, m.Description, time.Now()); err != nil {
				tx.Rollback()
				return fmt.Errorf("failed recording migration v%d: %w", m.Version, err)
			}

			if err := tx.Commit(); err != nil {
				return fmt.Errorf("failed committing migration v%d: %w", m.Version, err)
			}
		}
	}

	return nil
}

// CheckpointWAL triggers a controlled WAL checkpoint (PASSIVE, RESTART, or TRUNCATE).
func (d *DB) CheckpointWAL(ctx context.Context, mode string) error {
	modeUpper := strings.ToUpper(strings.TrimSpace(mode))
	switch modeUpper {
	case "PASSIVE", "FULL", "RESTART", "TRUNCATE":
	case "":
		modeUpper = "PASSIVE"
	default:
		return fmt.Errorf("invalid checkpoint mode '%s': must be PASSIVE, FULL, RESTART, or TRUNCATE", mode)
	}
	query := fmt.Sprintf("PRAGMA wal_checkpoint(%s);", modeUpper)
	_, err := d.sqlDB.ExecContext(ctx, query)
	return err
}

// Backup creates a hot, non-blocking online backup using SQLite's VACUUM INTO.
func (d *DB) Backup(ctx context.Context, destPath string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	cleanPath := filepath.Clean(destPath)
	if strings.Contains(cleanPath, "..") {
		return fmt.Errorf("invalid backup path: directory traversal disallowed")
	}

	// Ensure destination directory exists
	if err := os.MkdirAll(filepath.Dir(cleanPath), 0755); err != nil && filepath.Dir(cleanPath) != "." {
		return err
	}
	_ = os.Remove(cleanPath) // Remove if already exists

	escapedPath := strings.ReplaceAll(cleanPath, "'", "''")
	query := fmt.Sprintf("VACUUM INTO '%s';", escapedPath)
	_, err := d.sqlDB.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("sqlite backup failed: %w", err)
	}

	d.lastBackupTime = time.Now()
	d.lastBackupPath = cleanPath
	return nil
}

// ExecWithRetry executes a query, automatically retrying if SQLITE_BUSY or lock contention occurs.
func (d *DB) ExecWithRetry(ctx context.Context, query string, args ...any) (sql.Result, error) {
	var result sql.Result
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		result, lastErr = d.sqlDB.ExecContext(ctx, query, args...)
		if lastErr == nil {
			return result, nil
		}
		if isBusyError(lastErr) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(15*(attempt+1)) * time.Millisecond):
				continue
			}
		}
		return nil, lastErr
	}
	return nil, fmt.Errorf("sqlite query failed after retries: %w", lastErr)
}

func isBusyError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "busy") || strings.Contains(msg, "locked") || strings.Contains(msg, "sqlite_busy")
}

// Status returns runtime database metrics, WAL size, and connection stats.
func (d *DB) Status(ctx context.Context) (*protocol.DatabaseStatus, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var journalMode string
	_ = d.sqlDB.QueryRowContext(ctx, "PRAGMA journal_mode;").Scan(&journalMode)

	var foreignKeys int
	_ = d.sqlDB.QueryRowContext(ctx, "PRAGMA foreign_keys;").Scan(&foreignKeys)

	var busyTimeout int
	_ = d.sqlDB.QueryRowContext(ctx, "PRAGMA busy_timeout;").Scan(&busyTimeout)

	var synchronous int
	_ = d.sqlDB.QueryRowContext(ctx, "PRAGMA synchronous;").Scan(&synchronous)

	syncStr := "NORMAL"
	switch synchronous {
	case 0:
		syncStr = "OFF"
	case 1:
		syncStr = "NORMAL"
	case 2:
		syncStr = "FULL"
	case 3:
		syncStr = "EXTRA"
	}

	var dbSize int64
	if fi, err := os.Stat(d.dbPath); err == nil {
		dbSize = fi.Size()
	}

	var walSize int64
	walPath := d.dbPath + "-wal"
	if fi, err := os.Stat(walPath); err == nil {
		walSize = fi.Size()
	}

	stats := d.sqlDB.Stats()

	lastBackup := ""
	if !d.lastBackupTime.IsZero() {
		lastBackup = d.lastBackupTime.Format(time.RFC3339)
	}

	return &protocol.DatabaseStatus{
		Mode:            journalMode,
		ForeignKeys:     foreignKeys == 1,
		BusyTimeoutMs:   busyTimeout,
		Synchronous:     syncStr,
		DBSizeBytes:     dbSize,
		WALSizeBytes:    walSize,
		OpenConnections: stats.OpenConnections,
		MaxOpenConns:    stats.MaxOpenConnections,
		LastBackupTime:  lastBackup,
		LastBackupPath:  d.lastBackupPath,
		Healthy:         true,
	}, nil
}
