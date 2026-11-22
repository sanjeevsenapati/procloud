# ProCloud Architecture

ProCloud is a sovereign, single-node cloud platform designed to replace complex Kubernetes clusters with a clean, dependable Linux + Podman stack.

## Core Pillars

1. **Host-Native Orchestration**: Podman 4.x / 5.x engine running rootless or rootful OCI containers with direct cgroup v2 control.
2. **Systemd Quadlet Integration**: Container definitions compiled into native declarative systemd `.container`, `.pod`, and `.volume` unit files.
3. **Internal High-Performance Networking**: Netavark bridge network and CoreDNS integration with autonomous round-robin resolution.
4. **SQLite WAL Persistence**: Zero-dependency ACID metadata store operating in Write-Ahead Logging mode (`PRAGMA journal_mode=WAL;`).
5. **Multi-Tenant RBAC & Vault**: Cryptographic tenant isolation with AES-256-GCM secret management.
