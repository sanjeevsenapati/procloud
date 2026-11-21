package protocol

import (
	"time"
)

// User represents an authenticated platform user.
type User struct {
	ID           string    `json:"id"`
	Username     string    `json:"username"`
	Email        string    `json:"email"`
	PasswordHash string    `json:"-"`
	Role         string    `json:"role"` // admin, member, viewer
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Tenant represents an organization or workspace boundary.
type Tenant struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Slug      string    `json:"slug"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Project represents a tenant's project hosting applications and resources.
type Project struct {
	ID            string    `json:"id"`
	TenantID      string    `json:"tenant_id"`
	Name          string    `json:"name"`
	Slug          string    `json:"slug"`
	RepositoryURL string    `json:"repository_url,omitempty"`
	Branch        string    `json:"branch,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Node represents a compute node. In single-node architecture, there is 1 authoritative registered node.
type Node struct {
	ID                string    `json:"id"`
	Hostname          string    `json:"hostname"`
	Arch              string    `json:"arch"`
	OS                string    `json:"os"`
	Kernel            string    `json:"kernel"`
	PodmanVersion     string    `json:"podman_version"`
	TotalCPUMillis    int64     `json:"total_cpu_millis"`
	TotalMemoryBytes  int64     `json:"total_memory_bytes"`
	TotalStorageBytes int64     `json:"total_storage_bytes"`
	AllocatedCPU      int64     `json:"allocated_cpu"`
	AllocatedMemory   int64     `json:"allocated_memory"`
	AllocatedStorage  int64     `json:"allocated_storage"`
	Status            string    `json:"status"` // ready, degraded, offline
	IPAddress         string    `json:"ip_address"`
	LastHeartbeat     time.Time `json:"last_heartbeat"`
	CreatedAt         time.Time `json:"created_at"`
}

// VPC represents a tenant's isolated virtual private cloud network.
type VPC struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenant_id"`
	Name      string    `json:"name"`
	CIDR      string    `json:"cidr"` // e.g. "10.100.0.0/16"
	Bridge    string    `json:"bridge"`
	CreatedAt time.Time `json:"created_at"`
}

// Subnet represents an isolated subnet within a VPC.
type Subnet struct {
	ID        string    `json:"id"`
	VPCID     string    `json:"vpc_id"`
	Name      string    `json:"name"`
	CIDR      string    `json:"cidr"` // e.g. "10.100.1.0/24"
	Gateway   string    `json:"gateway"`
	Netavark  string    `json:"netavark_network_name"`
	CreatedAt time.Time `json:"created_at"`
}

// Volume represents a persistent local storage volume independent of containers.
type Volume struct {
	ID             string    `json:"id"`
	TenantID       string    `json:"tenant_id"`
	ProjectID      string    `json:"project_id"`
	Name           string    `json:"name"`
	Driver         string    `json:"driver"` // "local", "podman"
	HostPath       string    `json:"host_path"`
	SizeMB         int64     `json:"size_mb"`
	AttachedApp    string    `json:"attached_app,omitempty"`
	AttachedMount  string    `json:"attached_mount,omitempty"`
	Status         string    `json:"status"` // "available", "in-use", "degraded"
	CreatedAt      time.Time `json:"created_at"`
}

// Workload represents an application deployed onto the node.
type Workload struct {
	ID              string    `json:"id"`
	ProjectID       string    `json:"project_id"`
	VPCID           string    `json:"vpc_id"`
	SubnetID        string    `json:"subnet_id"`
	Name            string    `json:"name"`
	ImageTag        string    `json:"image_tag"`
	ImageDigest     string    `json:"image_digest"`
	CommitSHA       string    `json:"commit_sha"`
	ContainerID     string    `json:"container_id,omitempty"`
	Status          string    `json:"status"` // running, stopped, deploying, failed, rolled_back
	HealthStatus    string    `json:"health_status"` // healthy, unhealthy, pending
	InternalIP      string    `json:"internal_ip,omitempty"`
	HostPort        int       `json:"host_port,omitempty"`
	ContainerPort   int       `json:"container_port,omitempty"`
	IngressDomain   string    `json:"ingress_domain,omitempty"`
	CurrentRelease  int       `json:"current_release"`
	QuadletUnitName string    `json:"quadlet_unit_name"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// Deployment represents an immutable deployment operation and release.
type Deployment struct {
	ID            string    `json:"id"`
	WorkloadID    string    `json:"workload_id"`
	ProjectID     string    `json:"project_id"`
	ReleaseNumber int       `json:"release_number"`
	CommitSHA     string    `json:"commit_sha"`
	ImageTag      string    `json:"image_tag"`
	ImageDigest   string    `json:"image_digest"`
	ManifestYAML  string    `json:"manifest_yaml"`
	QuadletSpec   string    `json:"quadlet_spec"`
	Status        string    `json:"status"` // queued, building, deploying, running, failed, rolled_back
	Logs          string    `json:"logs,omitempty"`
	ErrorMessage  string    `json:"error_message,omitempty"`
	StartedAt     time.Time `json:"started_at"`
	CompletedAt   time.Time `json:"completed_at,omitempty"`
}

// Job represents a background durable task backed by SQLite.
type Job struct {
	ID             string     `json:"id"`
	Type           string     `json:"type"` // build_and_deploy, rollback, volume_backup, wal_checkpoint
	Status         string     `json:"status"` // QUEUED, RUNNING, COMPLETED, FAILED, RETRYING
	PayloadJSON    string     `json:"payload_json"`
	IdempotencyKey string     `json:"idempotency_key,omitempty"`
	RetryCount     int        `json:"retry_count"`
	MaxRetries     int        `json:"max_retries"`
	WorkerID       string     `json:"worker_id,omitempty"`
	ErrorDetails   string     `json:"error_details,omitempty"`
	ScheduledAt    time.Time  `json:"scheduled_at"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

// AuditEvent represents an immutable audit log entry.
type AuditEvent struct {
	ID           int64     `json:"id"`
	Actor        string    `json:"actor"`
	Action       string    `json:"action"`
	ResourceType string    `json:"resource_type"`
	ResourceID   string    `json:"resource_id"`
	IPAddress    string    `json:"ip_address,omitempty"`
	Details      string    `json:"details,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// DatabaseStatus provides health and WAL metrics.
type DatabaseStatus struct {
	Mode             string `json:"mode"`
	ForeignKeys      bool   `json:"foreign_keys"`
	BusyTimeoutMs    int    `json:"busy_timeout_ms"`
	Synchronous      string `json:"synchronous"`
	WALSizeBytes     int64  `json:"wal_size_bytes"`
	DBSizeBytes      int64  `json:"db_size_bytes"`
	OpenConnections  int    `json:"open_connections"`
	MaxOpenConns     int    `json:"max_open_conns"`
	PendingCheckpts  int    `json:"pending_checkpoints"`
	LastBackupTime   string `json:"last_backup_time,omitempty"`
	LastBackupPath   string `json:"last_backup_path,omitempty"`
	Healthy          bool   `json:"healthy"`
}

// WorkflowRun represents an execution of a source -> Containerfile -> deploy.yaml pipeline.
type WorkflowRun struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	TargetType    string    `json:"target_type"` // "container" or "pod"
	Status        string    `json:"status"`      // "QUEUED", "BUILDING", "DEPLOYING", "SUCCESS", "FAILED"
	Stage         string    `json:"stage"`       // "init", "build", "deploy", "completed", "failed"
	ImageTag      string    `json:"image_tag"`
	SourceType    string    `json:"source_type"` // "inline", "git", "path"
	SourceSummary string    `json:"source_summary"`
	Containerfile string    `json:"containerfile"`
	DeployYAML    string    `json:"deploy_yaml"`
	BuildLogs     string    `json:"build_logs"`
	DeployLogs    string    `json:"deploy_logs"`
	TargetID      string    `json:"target_id"`
	TargetName    string    `json:"target_name"`
	ErrorMessage  string    `json:"error_message,omitempty"`
	DurationMs    int64     `json:"duration_ms"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// WorkflowRunRequest represents the incoming submission to build and deploy.
type WorkflowRunRequest struct {
	Name          string            `json:"name"`
	TargetType    string            `json:"target_type"` // "container" or "pod"
	SourceType    string            `json:"source_type"` // "inline", "git", "path"
	SourceFiles   map[string]string `json:"source_files,omitempty"`
	GitURL        string            `json:"git_url,omitempty"`
	GitBranch     string            `json:"git_branch,omitempty"`
	LocalPath     string            `json:"local_path,omitempty"`
	Containerfile string            `json:"containerfile"`
	DeployYAML    string            `json:"deploy_yaml"`
	ImageTag      string            `json:"image_tag"`
	Ports         []string          `json:"ports,omitempty"`
	Env           map[string]string `json:"env,omitempty"`
}

