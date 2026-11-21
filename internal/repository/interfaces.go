package repository

import (
	"context"
	"time"

	"procloud.io/procloud/pkg/protocol"
)

type NodeRepository interface {
	RegisterNode(ctx context.Context, node *protocol.Node) error
	GetNode(ctx context.Context, id string) (*protocol.Node, error)
	GetPrimaryNode(ctx context.Context) (*protocol.Node, error)
	UpdateNodeHeartbeat(ctx context.Context, id string, status string) error
	UpdateNodeResources(ctx context.Context, id string, allocCPU, allocMem, allocStorage int64) error
}

type UserRepository interface {
	CreateUser(ctx context.Context, user *protocol.User) error
	GetUserByUsername(ctx context.Context, username string) (*protocol.User, error)
	GetUserByID(ctx context.Context, id string) (*protocol.User, error)
	CountUsers(ctx context.Context) (int, error)
}

type TenantRepository interface {
	CreateTenant(ctx context.Context, tenant *protocol.Tenant) error
	GetTenantByID(ctx context.Context, id string) (*protocol.Tenant, error)
	GetTenantBySlug(ctx context.Context, slug string) (*protocol.Tenant, error)
	ListTenants(ctx context.Context) ([]*protocol.Tenant, error)

	CreateProject(ctx context.Context, project *protocol.Project) error
	GetProjectByID(ctx context.Context, id string) (*protocol.Project, error)
	ListProjectsByTenant(ctx context.Context, tenantID string) ([]*protocol.Project, error)
}

type VPCRepository interface {
	CreateVPC(ctx context.Context, vpc *protocol.VPC) error
	GetVPCByID(ctx context.Context, id string) (*protocol.VPC, error)
	ListVPCsByTenant(ctx context.Context, tenantID string) ([]*protocol.VPC, error)

	CreateSubnet(ctx context.Context, subnet *protocol.Subnet) error
	GetSubnetByID(ctx context.Context, id string) (*protocol.Subnet, error)
	ListSubnetsByVPC(ctx context.Context, vpcID string) ([]*protocol.Subnet, error)
}

type VolumeRepository interface {
	CreateVolume(ctx context.Context, volume *protocol.Volume) error
	GetVolumeByID(ctx context.Context, id string) (*protocol.Volume, error)
	GetVolumeByName(ctx context.Context, tenantID, name string) (*protocol.Volume, error)
	ListVolumesByTenant(ctx context.Context, tenantID string) ([]*protocol.Volume, error)
	UpdateVolumeAttachment(ctx context.Context, id, appName, mountPath, status string) error
	DeleteVolume(ctx context.Context, id string) error
}

type WorkloadRepository interface {
	CreateWorkload(ctx context.Context, wl *protocol.Workload) error
	GetWorkloadByID(ctx context.Context, id string) (*protocol.Workload, error)
	GetWorkloadByName(ctx context.Context, projectID, name string) (*protocol.Workload, error)
	ListWorkloadsByProject(ctx context.Context, projectID string) ([]*protocol.Workload, error)
	ListAllWorkloads(ctx context.Context) ([]*protocol.Workload, error)
	UpdateWorkloadStatus(ctx context.Context, id, status, healthStatus, containerID, internalIP string) error
	UpdateWorkloadRelease(ctx context.Context, id string, releaseNum int, imageTag, imageDigest, commitSHA string) error
}

type DeploymentRepository interface {
	CreateDeployment(ctx context.Context, d *protocol.Deployment) error
	GetDeploymentByID(ctx context.Context, id string) (*protocol.Deployment, error)
	GetLatestDeployment(ctx context.Context, workloadID string) (*protocol.Deployment, error)
	GetDeploymentByRelease(ctx context.Context, workloadID string, releaseNum int) (*protocol.Deployment, error)
	ListDeploymentsByWorkload(ctx context.Context, workloadID string) ([]*protocol.Deployment, error)
	UpdateDeploymentStatus(ctx context.Context, id, status, errorMsg string, completedAt *time.Time) error
	AppendDeploymentLogs(ctx context.Context, id, logChunk string) error
}

type JobRepository interface {
	EnqueueJob(ctx context.Context, job *protocol.Job) error
	ClaimNextJob(ctx context.Context, workerID string) (*protocol.Job, error)
	CompleteJob(ctx context.Context, id string) error
	FailJob(ctx context.Context, id string, errStr string, retryIn *time.Duration) error
	RecoverInterruptedJobs(ctx context.Context) (int, error)
	GetJobByID(ctx context.Context, id string) (*protocol.Job, error)
	ListRecentJobs(ctx context.Context, limit int) ([]*protocol.Job, error)
}

type AuditRepository interface {
	RecordEvent(ctx context.Context, event *protocol.AuditEvent) error
	ListRecentEvents(ctx context.Context, limit int) ([]*protocol.AuditEvent, error)
}

type WorkflowRepository interface {
	CreateWorkflowRun(ctx context.Context, run *protocol.WorkflowRun) error
	GetWorkflowRun(ctx context.Context, id string) (*protocol.WorkflowRun, error)
	ListWorkflowRuns(ctx context.Context, limit int) ([]*protocol.WorkflowRun, error)
	UpdateWorkflowRun(ctx context.Context, run *protocol.WorkflowRun) error
	DeleteWorkflowRun(ctx context.Context, id string) error
}

