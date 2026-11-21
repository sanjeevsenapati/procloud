# ProCloud — Single Node SQLite-First Development Specification

Update the ProCloud architecture and implementation plan to follow a single-node, SQLite-first approach.

## Architecture constraints

The initial product must operate entirely on one Linux server.

- No virtual machines.
- No Kubernetes.
- No distributed control plane.
- No multi-node scheduler.
- No PostgreSQL dependency in the initial release.
- No mandatory external Redis dependency.
- No distributed storage dependency.
- No external message broker dependency.

Use:

- Go backend.
- React + TypeScript frontend.
- SQLite with WAL mode.
- Podman container runtime.
- Buildah / Podman image builds.
- Quadlet and systemd for container lifecycle.
- Netavark and Linux bridges for container networking.
- nftables for firewall management.
- Nginx for ingress.
- Local persistent storage using filesystem/LVM.
- Existing ProGit repository and authentication integration wherever practical.

## SQLite implementation

SQLite must be the authoritative database for the initial release.

Database requirements:

1. Enable WAL mode.
2. Enable foreign keys on every database connection.
3. Configure busy timeout.
4. Use appropriate synchronous settings.
5. Use short-lived write transactions.
6. Avoid long-running transactions.
7. Use a bounded connection pool.
8. Handle SQLITE_BUSY errors safely.
9. Implement controlled WAL checkpointing.
10. Implement consistent database backup and restore.
11. Add database health and WAL monitoring.
12. Use versioned database migrations.

Use repository interfaces to isolate SQL persistence from business logic.

All database access must go through the database/repository layer. Do not place SQL queries inside HTTP handlers or business services.

Do not use SQLite-specific SQL outside the SQLite implementation.

Design the persistence layer so a PostgreSQL implementation can be added later.

Do not build a PostgreSQL implementation now. Do not run both databases simultaneously.

## Single-node compute architecture

The initial compute model is one Linux host running Podman.

The platform must still maintain a compute-node abstraction in its database, even though there is only one registered node.

This preserves a clean future extension point.

The scheduler should select workloads against the single node's available CPU, memory, and storage capacity.

Implement:

- Node registration.
- Node health.
- Capacity detection.
- Resource profiles.
- Resource reservations.
- Container creation.
- Pod creation.
- Container lifecycle.
- Pod lifecycle.
- Quadlet generation.
- Restart policies.
- Health checks.
- Resource monitoring.

Do not implement distributed scheduling yet.

## Single-node networking

Implement local tenant-isolated networking.

Support:

- VPC creation.
- Subnet creation.
- IPAM.
- Local Linux bridges.
- Netavark integration.
- nftables firewall rules.
- Private workload communication.
- Internal DNS.
- Nginx ingress.
- Private and public application exposure.

Use separate non-overlapping CIDRs for tenant networks.

Do not implement VXLAN, WireGuard overlays, or multi-host routing in the initial release.

Keep a NetworkProvider interface so these capabilities can be added later.

## Single-node storage

Implement persistent local volumes.

Requirements:

- Volume creation.
- Volume deletion.
- Volume attachment.
- Volume detachment.
- Mount-path management.
- Capacity limits.
- Volume usage.
- Backup.
- Restore.
- Volume lifecycle independent of containers.

A container must be recreatable without losing persistent data.

Do not implement Ceph or distributed storage yet.

Keep a StorageProvider interface for future implementations.

## Git deployment workflow

Integrate with existing ProGit.

Support:

- Repository selection.
- Branch selection.
- Webhook triggers.
- Commit SHA tracking.
- Containerfile.
- deploy.yml.
- Image builds.
- Local OCI image storage or an OCI-compatible registry.
- Container provisioning.
- Health checks.
- Deployment logs.
- Rollback.

The initial workflow must work on one Linux host.

Always deploy from immutable commit SHAs and image digests.

## Background jobs

Do not introduce Redis or RabbitMQ just for the initial release.

Implement a durable SQLite-backed job system.

Job requirements:

- Unique job ID.
- Job type.
- Job status.
- Retry count.
- Creation time.
- Start time.
- Completion time.
- Error details.
- Idempotency key.
- Recovery of interrupted jobs after application restart.

Workers should claim jobs using short database transactions and safe concurrency controls.

Never hold database transactions open while performing image builds or Podman operations.

## Project structure

Adapt the existing ProGit repository. If a separate project is required, use:

progit-cloud/
├── cmd/
│   ├── api/
│   ├── agent/
│   └── worker/
├── internal/
│   ├── auth/
│   ├── tenant/
│   ├── project/
│   ├── database/
│   │   ├── sqlite/
│   │   └── migrations/
│   ├── repository/
│   ├── manifest/
│   ├── build/
│   ├── deployment/
│   ├── scheduler/
│   ├── compute/
│   ├── container/
│   ├── pod/
│   ├── network/
│   ├── vpc/
│   ├── storage/
│   ├── ingress/
│   ├── dns/
│   ├── monitoring/
│   └── audit/
├── pkg/
│   ├── podman/
│   ├── quadlet/
│   └── protocol/
├── web/
├── migrations/
├── templates/
├── deployments/
├── scripts/
├── tests/
└── docs/

## Future compatibility requirements

Although the initial release is single-node, avoid architecture decisions that prevent future growth.

Maintain:

- Compute-node abstraction.
- ContainerRuntime interface.
- NetworkProvider interface.
- StorageProvider interface.
- Repository interfaces.
- ImageBuilder interface.
- Scheduler interface.
- Versioned deployment manifest.
- Durable deployment state.
- Immutable deployment history.
- Tenant-aware resource ownership.
- Stable resource identifiers.
- Database migration tooling.

Future PostgreSQL and multi-node support must be possible without rewriting business logic.

However, do not prematurely implement distributed systems.

## First milestone

Implement the following end-to-end workflow:

1. Start ProGit Cloud on one Linux server.
2. Initialise SQLite with WAL.
3. Create an administrator account.
4. Register the local Podman host.
5. Create an organisation and project.
6. Create a local VPC and subnet.
7. Connect an existing ProGit repository.
8. Build an image from its Containerfile.
9. Validate its deploy.yml.
10. Create a persistent volume.
11. Deploy the application using Quadlet and Podman.
12. Configure Nginx ingress.
13. Display application health and logs.
14. Push a new Git commit and deploy the updated version.
15. Roll back to the previous version.
16. Restart the application and verify persistent data remains intact.

All operations must use real Podman, real networking, and real persistent storage.

Do not create mock deployment success responses.

Develop and test this complete single-node workflow before implementing advanced cloud features.
