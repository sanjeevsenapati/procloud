# ProCloud Architecture Thoughts and Enhancements

This document details the architectural reflections, design rationale, and enhancements introduced into ProCloud following the specification in `project.md`.

---

## Part 1: Architectural Analysis of Single-Node SQLite-First Systems

### Why SQLite WAL over Client-Server RDBMS for Single-Node Clouds?
Traditional cloud platforms often introduce premature infrastructure dependencies—installing PostgreSQL, Redis, RabbitMQ, and ZooKeeper/etcd before a single user workload is deployed. On a single node, this introduces:
1. **Memory Bloat**: Multiple database daemons competing for RAM against user containers.
2. **IPC Overhead**: TCP/IP loopback overhead on every query and job pop.
3. **Failure Surface Expansion**: Multiple independent systemd services that can crash or fall out of sync.

By utilizing **SQLite in WAL (Write-Ahead Logging) mode**:
- **Zero-Latency In-Process Access**: Database queries execute directly in memory with zero TCP network stack overhead.
- **Concurrent Readers**: WAL mode permits multiple concurrent reader threads while a writer writes.
- **Crash Durability**: WAL atomic commits ensure uncorrupted recovery after sudden server power cuts.
- **Atomic Concurrency with Retries**: Setting `busy_timeout = 5000ms` and `synchronous = NORMAL` gives sub-millisecond commits without fsync stalls on every read.

### Systemd Quadlet as the True Container Control Plane
Rather than running an active daemon watching containers (which creates a single point of failure), ProCloud translates application manifests directly into systemd Quadlet unit files (`.container`, `.network`, `.volume`).
- When the host boots, **systemd itself** launches and supervises containers.
- If ProCloud is restarting or upgraded, running workloads **never experience downtime**.
- Systemd cgroups v2 handle CPU, memory, and OOM killer policies deterministically at the Linux kernel level.

---

## Part 2: Enhancements Implemented in ProCloud

### Enhancement 1: Clean Persistence Layer Decoupling (PostgreSQL Ready)
- Defined strict repository interfaces in `internal/repository/interfaces.go` (`NodeRepository`, `UserRepository`, `TenantRepository`, `VPCRepository`, `VolumeRepository`, `WorkloadRepository`, `DeploymentRepository`, `JobRepository`, `AuditRepository`).
- Zero SQL statements leak outside `internal/database/sqlite`.
- When ProCloud scales to multi-node or requires multi-region metadata replication, a `database/postgres` implementation of the same interfaces can be introduced without altering a single line of business logic.

### Enhancement 2: Durable SQLite-Backed Background Job Queue
- Implemented in `internal/job/worker.go`.
- Avoids the external Redis/Celery trap for single nodes.
- Short-lived atomic claim transactions (`ClaimNextJob`) ensure lock contention is virtually zero.
- **Safety Rule**: Podman image builds and network operations run completely outside database transactions.
- **Crash Recovery**: Detects interrupted `RUNNING` jobs on startup and resets them to `QUEUED` with exponential backoff.

### Enhancement 3: Non-Blocking Hot Backup & WAL Checkpoint Coordinator
- SQLite WAL files can grow unbounded under heavy write loads if not checkpointed.
- Implemented `internal/monitoring/service.go` which monitors WAL size and triggers `PASSIVE` or `TRUNCATE` checkpoints automatically.
- Live zero-downtime hot backups via SQLite `VACUUM INTO 'backup.db'` without interrupting concurrent read/write operations.

### Enhancement 4: deploy.yml v1alpha1 Manifest & Rollback Engine
- Full GitOps specification in `internal/manifest/manifest.go` supporting:
  - CPU/Memory limits and requests
  - HTTP and Exec health probes
  - Environment variables
  - Persistent volume mounts with SELinux `:Z` labels
  - Ingress domain and path specifications
- Immutable release tracking in `internal/deployment/engine.go`: Each release stores its exact commit SHA, image digest, and Quadlet unit.
- Rollback restores the prior immutable container and reroutes ingress without requiring a rebuild.

### Enhancement 5: ProGit Webhook Integration
- Exposes `/api/v1/webhooks/progit` accepting standard push payloads.
- Extracts commit SHA and triggers automated background build and deployment.

### Enhancement 6: Embedded Zero-Dependency Web Console
- Modern ProGit White & Light Blue sovereign console embedded in `web/static/index.html` via `embed.FS`.
- Live telemetry, workload table, volume list, job queue monitor, and 1-click Milestone orchestrator.

### Enhancement 7: Real Interactive Container Terminal (PTY Attached via WebSockets)
- Implemented in `cmd/api/terminal_ws.go` and `web/static/index.html`.
- **Host-Native PTY Session**: Allocates a true Unix pseudo-terminal (`/dev/pts/*`) via `github.com/creack/pty` directly attached to `podman exec -it <containerID> <shell>` (or `podman attach`).
- **Full Bi-Directional Streaming**: Streams raw keystrokes from the browser terminal to the container PTY, and streams stdout/stderr back over WebSockets (`/api/v1/containers/{id}/terminal?token=...`).
- **Interactive Shell Features**: Full support for curses applications (`vi`, `nano`, `top`, `htop`), signal interrupts (`Ctrl+C` / SIGINT), EOF (`Ctrl+D`), and Tab filename auto-completion.
- **Dynamic PTY Window Resizing**: Client viewport dimension changes trigger `{"type":"resize","cols":...,"rows":...}` messages that dynamically invoke `pty.Setsize` on the container TTY.
- **Strict Per-Container Isolation**: Each container runs an independent `Terminal` instance and DOM viewport; terminal history, screen buffers, and prompts never bleed across containers.
- **Offline-First Zero CDN Dependency**: Embedded `xterm.js` and `xterm-addon-fit.js` inside `web/static/vendor/xterm/`.

### Enhancement 8: Autonomous RFC 1035 UDP DNSaaS & Service Discovery
- Built-in authoritative DNS server listening on UDP port 1053 (`internal/dns/service.go`).
- Provides automated DNS resolution for internal workloads (`<container>.internal`, `<service>.internal`) with round-robin multi-record distribution.
- Full BIND zone export (`GET /api/v1/dns/zones/{id}/export`) and REST CRUD API.

### Enhancement 9: Integrated Distributed Job Scheduler (Cron & Maintenance Engine)
- Background job scheduler in `internal/scheduler/` evaluating standard 5-field cron syntax (`min hour day month weekday`).
- Handles periodic maintenance routines:
  - Daily database snapshots (`system_backup_daily`)
  - Container health inspection loop (`container_health_check`)
  - Dangling container image pruning (`dangling_images_cleanup`)
  - Hourly SQLite WAL truncation (`wal_checkpoint_hourly`)

### Enhancement 10: Cross-Platform Release Packaging for macOS and Linux
- Pure Go cross-compilation produces statically linked, standalone binaries with `CGO_ENABLED=0`.
- Supports 4 major deployment targets:
  - macOS Apple Silicon (`darwin/arm64` — M1/M2/M3/M4)
  - macOS Intel (`darwin/amd64`)
  - Linux x86_64 (`linux/amd64`)
  - Linux ARM64 (`linux/arm64` — aarch64)
- Automated GitHub Actions release pipeline (`.github/workflows/release.yml`) builds, packages `.tar.gz` and `.zip` archives with documentation, computes SHA-256 checksums, and publishes releases on GitHub tags (`v*`).
