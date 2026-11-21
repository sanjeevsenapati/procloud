package migrations

// Migration represents a versioned SQL migration.
type Migration struct {
	Version     int
	Description string
	SQL         string
}

// AllMigrations returns the ordered list of schema migrations.
var AllMigrations = []Migration{
	{
		Version:     1,
		Description: "Initial schema: core tables for single-node ProCloud",
		SQL: `
-- Users
CREATE TABLE IF NOT EXISTS users (
    id TEXT PRIMARY KEY,
    username TEXT NOT NULL UNIQUE,
    email TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    role TEXT NOT NULL DEFAULT 'member',
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL
);

-- Tenants (Organizations)
CREATE TABLE IF NOT EXISTS tenants (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    slug TEXT NOT NULL UNIQUE,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL
);

-- Projects
CREATE TABLE IF NOT EXISTS projects (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    slug TEXT NOT NULL,
    repository_url TEXT NOT NULL DEFAULT '',
    branch TEXT NOT NULL DEFAULT 'main',
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    UNIQUE(tenant_id, slug)
);

-- Compute Nodes
CREATE TABLE IF NOT EXISTS nodes (
    id TEXT PRIMARY KEY,
    hostname TEXT NOT NULL UNIQUE,
    arch TEXT NOT NULL,
    os TEXT NOT NULL,
    kernel TEXT NOT NULL,
    podman_version TEXT NOT NULL,
    total_cpu_millis INTEGER NOT NULL,
    total_memory_bytes INTEGER NOT NULL,
    total_storage_bytes INTEGER NOT NULL,
    allocated_cpu INTEGER NOT NULL DEFAULT 0,
    allocated_memory INTEGER NOT NULL DEFAULT 0,
    allocated_storage INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'ready',
    ip_address TEXT NOT NULL DEFAULT '127.0.0.1',
    last_heartbeat DATETIME NOT NULL,
    created_at DATETIME NOT NULL
);

-- VPCs
CREATE TABLE IF NOT EXISTS vpcs (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    cidr TEXT NOT NULL,
    bridge TEXT NOT NULL,
    created_at DATETIME NOT NULL,
    UNIQUE(tenant_id, name)
);

-- Subnets
CREATE TABLE IF NOT EXISTS subnets (
    id TEXT PRIMARY KEY,
    vpc_id TEXT NOT NULL REFERENCES vpcs(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    cidr TEXT NOT NULL,
    gateway TEXT NOT NULL,
    netavark_network_name TEXT NOT NULL UNIQUE,
    created_at DATETIME NOT NULL
);

-- Volumes
CREATE TABLE IF NOT EXISTS volumes (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    driver TEXT NOT NULL DEFAULT 'local',
    host_path TEXT NOT NULL,
    size_mb INTEGER NOT NULL DEFAULT 1024,
    attached_app TEXT NOT NULL DEFAULT '',
    attached_mount TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'available',
    created_at DATETIME NOT NULL,
    UNIQUE(tenant_id, name)
);

-- Workloads
CREATE TABLE IF NOT EXISTS workloads (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    vpc_id TEXT NOT NULL REFERENCES vpcs(id),
    subnet_id TEXT NOT NULL REFERENCES subnets(id),
    name TEXT NOT NULL,
    image_tag TEXT NOT NULL,
    image_digest TEXT NOT NULL DEFAULT '',
    commit_sha TEXT NOT NULL DEFAULT '',
    container_id TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'stopped',
    health_status TEXT NOT NULL DEFAULT 'pending',
    internal_ip TEXT NOT NULL DEFAULT '',
    host_port INTEGER NOT NULL DEFAULT 0,
    container_port INTEGER NOT NULL DEFAULT 0,
    ingress_domain TEXT NOT NULL DEFAULT '',
    current_release INTEGER NOT NULL DEFAULT 1,
    quadlet_unit_name TEXT NOT NULL,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    UNIQUE(project_id, name)
);

-- Deployments (immutable history)
CREATE TABLE IF NOT EXISTS deployments (
    id TEXT PRIMARY KEY,
    workload_id TEXT NOT NULL REFERENCES workloads(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    release_number INTEGER NOT NULL,
    commit_sha TEXT NOT NULL,
    image_tag TEXT NOT NULL,
    image_digest TEXT NOT NULL,
    manifest_yaml TEXT NOT NULL,
    quadlet_spec TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'queued',
    logs TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    started_at DATETIME NOT NULL,
    completed_at DATETIME
);

-- Background Jobs (SQLite durable queue)
CREATE TABLE IF NOT EXISTS jobs (
    id TEXT PRIMARY KEY,
    type TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'QUEUED',
    payload_json TEXT NOT NULL,
    idempotency_key TEXT NOT NULL DEFAULT '',
    retry_count INTEGER NOT NULL DEFAULT 0,
    max_retries INTEGER NOT NULL DEFAULT 3,
    worker_id TEXT NOT NULL DEFAULT '',
    error_details TEXT NOT NULL DEFAULT '',
    scheduled_at DATETIME NOT NULL,
    started_at DATETIME,
    completed_at DATETIME,
    created_at DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_jobs_status_scheduled ON jobs(status, scheduled_at);
CREATE INDEX IF NOT EXISTS idx_jobs_idempotency ON jobs(idempotency_key) WHERE idempotency_key != '';

-- Audit Events
CREATE TABLE IF NOT EXISTS audit_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    actor TEXT NOT NULL,
    action TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    resource_id TEXT NOT NULL,
    ip_address TEXT NOT NULL DEFAULT '',
    details TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_audit_created ON audit_events(created_at);
`,
	},
	{
		Version:     2,
		Description: "Add workflow_runs table for build & deploy pipelines",
		SQL: `
CREATE TABLE IF NOT EXISTS workflow_runs (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    target_type TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'QUEUED',
    stage TEXT NOT NULL DEFAULT 'init',
    image_tag TEXT NOT NULL,
    source_type TEXT NOT NULL DEFAULT 'inline',
    source_summary TEXT NOT NULL DEFAULT '',
    containerfile TEXT NOT NULL,
    deploy_yaml TEXT NOT NULL,
    build_logs TEXT NOT NULL DEFAULT '',
    deploy_logs TEXT NOT NULL DEFAULT '',
    target_id TEXT NOT NULL DEFAULT '',
    target_name TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    duration_ms INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_workflow_runs_created ON workflow_runs(created_at DESC);
`,
	},
}
