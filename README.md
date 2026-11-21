# ProCloud — Single-Node Sovereign Cloud Platform

ProCloud is a single-node, SQLite-first sovereign cloud control plane written in Go. It transforms a single Linux server into a cloud platform utilizing **Podman**, **Quadlet**, **systemd**, **Netavark**, **nftables**, **Nginx**, and **SQLite with Write-Ahead Logging (WAL)**.

---

## 🏗️ Architecture & Philosophy

ProCloud is designed around the core principle of **Sovereign Single-Node Simplicity**:
- **Zero Heavy Orchestrators**: No Kubernetes, no Nomad, no distributed etcd control planes.
- **Zero External Middleware**: No external Redis, RabbitMQ, or PostgreSQL dependencies.
- **Embedded WAL Persistence**: SQLite configured with WAL mode, foreign key enforcement, 5000ms busy timeout, and memory temp store.
- **Systemd-Native Container Management**: Generates native Quadlet `.container`, `.network`, and `.volume` units for container lifecycle.
- **Clean Interface Decoupling**: Business logic interacts strictly through repository and provider interfaces (`NodeRepository`, `NetworkProvider`, `StorageProvider`, `ImageBuilder`, `Scheduler`), ensuring a clean path to multi-node or PostgreSQL in future releases without touching business code.

---

## 🚀 Key Enhancements Added

### 1. Hybrid Dual-Mode Runtime Engine
- **Linux Native & Podman Machine**: Auto-detects Linux systemd environments and writes Quadlet units to `~/.config/containers/systemd` or `/etc/containers/systemd`.
- **Hermetic Testing & CI**: Includes an in-memory `MockRuntime` allowing full 16-step milestone verification and unit testing in CI/CD without requiring root privileges.

### 2. Durable SQLite-Backed Job Queue with Concurrency Safety
- Workers claim jobs in atomic, microsecond-duration database transactions:
  ```sql
  UPDATE jobs SET status = 'RUNNING', worker_id = ?, started_at = ? WHERE id = (
      SELECT id FROM jobs WHERE status = 'QUEUED' AND scheduled_at <= ? ORDER BY scheduled_at ASC LIMIT 1
  );
  ```
- **Guaranteed No-Lock Rule**: Database transactions are never held open during image builds or container launches.
- **Interrupted Job Recovery**: On boot, the engine scans for dangling `RUNNING` jobs from prior crashes and resets them to `QUEUED` with incremented retry counts.

### 3. Automated WAL Checkpoint Coordinator & Online Hot Backup
- Background WAL monitor samples journal size every 30 seconds.
- Issues `PASSIVE` checkpoints during idle periods and automatic `TRUNCATE` checkpoints when WAL exceeds 20MB.
- Live, non-blocking online database backups using SQLite's `VACUUM INTO 'backup.db'`.

### 4. GitOps Immutable Releases & 1-Click Rollback
- Every deployment creates an immutable release record containing the exact commit SHA, OCI image digest, validated manifest YAML, and Quadlet unit file.
- Rollbacks bypass rebuilding: they instantly reinstall the immutable Quadlet unit, restart the container, and update Nginx upstream routes.

### 5. Automated Nginx Ingress & IPAM Netavark Integration
- Tenant VPCs are assigned non-overlapping RFC1918 blocks (`10.X.0.0/16`).
- Subnets (`10.X.Y.0/24`) automatically generate Netavark bridge networks with internal Aardvark DNS.
- Dynamic Nginx virtual host generator with WebSocket support and graceful reload (`nginx -s reload`).

### 6. Embedded Zero-Dependency Web Console
- Built-in single-page web console embedded directly into the single `procloud` binary using Go's `embed.FS`.
- Real-time telemetry, node resource allocation, workload list, persistent volume table, job queue status, and 1-click Milestone runner.

---

## 📁 Project Structure

```
procloud/
├── bin/                       # Compiled binaries (procloud, procloud-worker, procloud-agent)
├── cmd/
│   ├── api/                   # Main API server, web console, and milestone orchestrator
│   ├── agent/                 # Node health and heartbeat telemetry agent
│   └── worker/                # Standalone durable SQLite background worker
├── internal/
│   ├── audit/                 # Immutable audit event logging
│   ├── auth/                  # JWT auth, bcrypt hashing, ProGit auth
│   ├── build/                 # ImageBuilder interface & Podman buildah implementation
│   ├── compute/               # Compute node abstraction & host capacity detection
│   ├── database/
│   │   ├── migrations/        # Versioned database migrations
│   │   └── sqlite/            # SQLite WAL connection manager, pool & repositories
│   ├── deployment/            # Deployment orchestrator & rollback engine
│   ├── ingress/               # Nginx reverse proxy generator & reloader
│   ├── job/                   # Durable SQLite background job queue
│   ├── manifest/              # deploy.yml v1alpha1 parser & schema validator
│   ├── monitoring/            # WAL monitoring & automated checkpoint coordinator
│   ├── network/               # NetworkProvider interface & Netavark bridge
│   ├── repository/            # Clean repository interfaces (SQL decoupled)
│   ├── scheduler/             # Capacity-aware single-node scheduler
│   ├── storage/               # Persistent volume manager, tar.gz hot backup/restore
│   ├── tenant/                # Organizations, projects, and tenant isolation
│   └── vpc/                   # IPAM, VPC, and subnet allocation
├── pkg/
│   ├── podman/                # Podman CLI & mock runtime implementations
│   ├── protocol/              # DTOs, API models, database structures
│   └── quadlet/               # Quadlet generator (.container, .volume, .network)
├── web/                       # Embedded web UI console (static/index.html)
├── deployments/               # Sample deploy.yml manifests
├── templates/                 # Nginx and Quadlet templates
├── scripts/                   # run.sh and automation helpers
├── tests/                     # Unit, integration, and Milestone E2E tests
└── docs/                      # Specification & architectural documentation
```

---

## ⚡ Quick Start

### 1. Build the Platform
```bash
go build -o bin/procloud ./cmd/api
go build -o bin/procloud-worker ./cmd/worker
go build -o bin/procloud-agent ./cmd/agent
```

### 2. Run Test Suite
```bash
go test -v ./tests/...
```

### 3. Start ProCloud
```bash
./bin/procloud -port 8080 -db procloud.db
```
Then open **http://localhost:8080** in your browser to view the Sovereign Cloud Web Console.

### 4. Trigger Milestone 1 (16 Steps)
Click the **"▶ Run First Milestone (16 Steps)"** button on the web console, or invoke via curl:
```bash
curl -X POST http://localhost:8080/api/v1/milestone/run
```
