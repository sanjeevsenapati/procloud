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
- Modern dark-themed sovereign console embedded in `web/static/index.html` via `embed.FS`.
- Live telemetry, workload table, volume list, job queue monitor, and 1-click Milestone orchestrator.
