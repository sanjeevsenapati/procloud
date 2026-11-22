# Cron Scheduler & Background Jobs

ProCloud includes an integrated, enterprise-grade job scheduling engine that handles recurring operational tasks, periodic health checks, and database maintenance without external dependencies.

## Key Features

- **Standard 5-Field Cron Expressions**: Supports standard syntax `min hour day month weekday` (e.g., `*/15 * * * *`, `0 2 * * *`).
- **Pre-Integrated System Jobs**:
  - `system_backup_daily`: Nightly database and configuration snapshots.
  - `container_health_check`: Periodic automated container ping and restart loop.
  - `dangling_images_cleanup`: Pruning unreferenced container images to free storage.
  - `wal_checkpoint_hourly`: Hourly SQLite `PRAGMA wal_checkpoint(TRUNCATE)` maintenance.
- **REST API & Web UI**: Full CRUD operations for custom schedules and on-demand execution.
