package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"procloud.io/procloud/pkg/protocol"
)

type Repository struct {
	db *DB
}

func NewRepository(db *DB) *Repository {
	return &Repository{db: db}
}

// -------------------------------------------------------------
// Node Repository
// -------------------------------------------------------------

func (r *Repository) RegisterNode(ctx context.Context, n *protocol.Node) error {
	query := `
		INSERT INTO nodes (
			id, hostname, arch, os, kernel, podman_version,
			total_cpu_millis, total_memory_bytes, total_storage_bytes,
			allocated_cpu, allocated_memory, allocated_storage,
			status, ip_address, last_heartbeat, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(hostname) DO UPDATE SET
			arch = excluded.arch,
			os = excluded.os,
			kernel = excluded.kernel,
			podman_version = excluded.podman_version,
			total_cpu_millis = excluded.total_cpu_millis,
			total_memory_bytes = excluded.total_memory_bytes,
			total_storage_bytes = excluded.total_storage_bytes,
			status = excluded.status,
			ip_address = excluded.ip_address,
			last_heartbeat = excluded.last_heartbeat;
	`
	_, err := r.db.sqlDB.ExecContext(ctx, query,
		n.ID, n.Hostname, n.Arch, n.OS, n.Kernel, n.PodmanVersion,
		n.TotalCPUMillis, n.TotalMemoryBytes, n.TotalStorageBytes,
		n.AllocatedCPU, n.AllocatedMemory, n.AllocatedStorage,
		n.Status, n.IPAddress, n.LastHeartbeat, n.CreatedAt,
	)
	return err
}

func (r *Repository) GetNode(ctx context.Context, id string) (*protocol.Node, error) {
	row := r.db.sqlDB.QueryRowContext(ctx, `
		SELECT id, hostname, arch, os, kernel, podman_version,
		       total_cpu_millis, total_memory_bytes, total_storage_bytes,
		       allocated_cpu, allocated_memory, allocated_storage,
		       status, ip_address, last_heartbeat, created_at
		FROM nodes WHERE id = ?
	`, id)
	return scanNode(row)
}

func (r *Repository) GetPrimaryNode(ctx context.Context) (*protocol.Node, error) {
	row := r.db.sqlDB.QueryRowContext(ctx, `
		SELECT id, hostname, arch, os, kernel, podman_version,
		       total_cpu_millis, total_memory_bytes, total_storage_bytes,
		       allocated_cpu, allocated_memory, allocated_storage,
		       status, ip_address, last_heartbeat, created_at
		FROM nodes ORDER BY created_at ASC LIMIT 1
	`)
	return scanNode(row)
}

func (r *Repository) UpdateNodeHeartbeat(ctx context.Context, id string, status string) error {
	_, err := r.db.sqlDB.ExecContext(ctx, "UPDATE nodes SET last_heartbeat = ?, status = ? WHERE id = ?", time.Now(), status, id)
	return err
}

func (r *Repository) UpdateNodeResources(ctx context.Context, id string, allocCPU, allocMem, allocStorage int64) error {
	_, err := r.db.sqlDB.ExecContext(ctx, `
		UPDATE nodes SET allocated_cpu = ?, allocated_memory = ?, allocated_storage = ? WHERE id = ?
	`, allocCPU, allocMem, allocStorage, id)
	return err
}

func scanNode(row *sql.Row) (*protocol.Node, error) {
	var n protocol.Node
	err := row.Scan(
		&n.ID, &n.Hostname, &n.Arch, &n.OS, &n.Kernel, &n.PodmanVersion,
		&n.TotalCPUMillis, &n.TotalMemoryBytes, &n.TotalStorageBytes,
		&n.AllocatedCPU, &n.AllocatedMemory, &n.AllocatedStorage,
		&n.Status, &n.IPAddress, &n.LastHeartbeat, &n.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("node not found")
	}
	return &n, err
}

// -------------------------------------------------------------
// User Repository
// -------------------------------------------------------------

func (r *Repository) CreateUser(ctx context.Context, u *protocol.User) error {
	_, err := r.db.sqlDB.ExecContext(ctx, `
		INSERT INTO users (id, username, email, password_hash, role, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, u.ID, u.Username, u.Email, u.PasswordHash, u.Role, u.CreatedAt, u.UpdatedAt)
	return err
}

func (r *Repository) GetUserByUsername(ctx context.Context, username string) (*protocol.User, error) {
	row := r.db.sqlDB.QueryRowContext(ctx, `
		SELECT id, username, email, password_hash, role, created_at, updated_at
		FROM users WHERE username = ?
	`, username)
	return scanUser(row)
}

func (r *Repository) GetUserByID(ctx context.Context, id string) (*protocol.User, error) {
	row := r.db.sqlDB.QueryRowContext(ctx, `
		SELECT id, username, email, password_hash, role, created_at, updated_at
		FROM users WHERE id = ?
	`, id)
	return scanUser(row)
}

func (r *Repository) CountUsers(ctx context.Context) (int, error) {
	var count int
	err := r.db.sqlDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&count)
	return count, err
}

func scanUser(row *sql.Row) (*protocol.User, error) {
	var u protocol.User
	err := row.Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.Role, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("user not found")
	}
	return &u, err
}

// -------------------------------------------------------------
// Tenant & Project Repository
// -------------------------------------------------------------

func (r *Repository) CreateTenant(ctx context.Context, t *protocol.Tenant) error {
	_, err := r.db.sqlDB.ExecContext(ctx, `
		INSERT INTO tenants (id, name, slug, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
	`, t.ID, t.Name, t.Slug, t.CreatedAt, t.UpdatedAt)
	return err
}

func (r *Repository) GetTenantByID(ctx context.Context, id string) (*protocol.Tenant, error) {
	row := r.db.sqlDB.QueryRowContext(ctx, "SELECT id, name, slug, created_at, updated_at FROM tenants WHERE id = ?", id)
	var t protocol.Tenant
	err := row.Scan(&t.ID, &t.Name, &t.Slug, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("tenant not found")
	}
	return &t, err
}

func (r *Repository) GetTenantBySlug(ctx context.Context, slug string) (*protocol.Tenant, error) {
	row := r.db.sqlDB.QueryRowContext(ctx, "SELECT id, name, slug, created_at, updated_at FROM tenants WHERE slug = ?", slug)
	var t protocol.Tenant
	err := row.Scan(&t.ID, &t.Name, &t.Slug, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("tenant not found")
	}
	return &t, err
}

func (r *Repository) ListTenants(ctx context.Context) ([]*protocol.Tenant, error) {
	rows, err := r.db.sqlDB.QueryContext(ctx, "SELECT id, name, slug, created_at, updated_at FROM tenants ORDER BY name ASC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*protocol.Tenant
	for rows.Next() {
		var t protocol.Tenant
		if err := rows.Scan(&t.ID, &t.Name, &t.Slug, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		list = append(list, &t)
	}
	return list, nil
}

func (r *Repository) CreateProject(ctx context.Context, p *protocol.Project) error {
	_, err := r.db.sqlDB.ExecContext(ctx, `
		INSERT INTO projects (id, tenant_id, name, slug, repository_url, branch, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, p.ID, p.TenantID, p.Name, p.Slug, p.RepositoryURL, p.Branch, p.CreatedAt, p.UpdatedAt)
	return err
}

func (r *Repository) GetProjectByID(ctx context.Context, id string) (*protocol.Project, error) {
	row := r.db.sqlDB.QueryRowContext(ctx, `
		SELECT id, tenant_id, name, slug, repository_url, branch, created_at, updated_at
		FROM projects WHERE id = ?
	`, id)
	var p protocol.Project
	err := row.Scan(&p.ID, &p.TenantID, &p.Name, &p.Slug, &p.RepositoryURL, &p.Branch, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("project not found")
	}
	return &p, err
}

func (r *Repository) ListProjectsByTenant(ctx context.Context, tenantID string) ([]*protocol.Project, error) {
	rows, err := r.db.sqlDB.QueryContext(ctx, `
		SELECT id, tenant_id, name, slug, repository_url, branch, created_at, updated_at
		FROM projects WHERE tenant_id = ? ORDER BY name ASC
	`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*protocol.Project
	for rows.Next() {
		var p protocol.Project
		if err := rows.Scan(&p.ID, &p.TenantID, &p.Name, &p.Slug, &p.RepositoryURL, &p.Branch, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		list = append(list, &p)
	}
	return list, nil
}

// -------------------------------------------------------------
// VPC & Subnet Repository
// -------------------------------------------------------------

func (r *Repository) CreateVPC(ctx context.Context, v *protocol.VPC) error {
	_, err := r.db.sqlDB.ExecContext(ctx, `
		INSERT INTO vpcs (id, tenant_id, name, cidr, bridge, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, v.ID, v.TenantID, v.Name, v.CIDR, v.Bridge, v.CreatedAt)
	return err
}

func (r *Repository) GetVPCByID(ctx context.Context, id string) (*protocol.VPC, error) {
	row := r.db.sqlDB.QueryRowContext(ctx, "SELECT id, tenant_id, name, cidr, bridge, created_at FROM vpcs WHERE id = ?", id)
	var v protocol.VPC
	err := row.Scan(&v.ID, &v.TenantID, &v.Name, &v.CIDR, &v.Bridge, &v.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("vpc not found")
	}
	return &v, err
}

func (r *Repository) ListVPCsByTenant(ctx context.Context, tenantID string) ([]*protocol.VPC, error) {
	rows, err := r.db.sqlDB.QueryContext(ctx, "SELECT id, tenant_id, name, cidr, bridge, created_at FROM vpcs WHERE tenant_id = ?", tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*protocol.VPC
	for rows.Next() {
		var v protocol.VPC
		if err := rows.Scan(&v.ID, &v.TenantID, &v.Name, &v.CIDR, &v.Bridge, &v.CreatedAt); err != nil {
			return nil, err
		}
		list = append(list, &v)
	}
	return list, nil
}

func (r *Repository) CreateSubnet(ctx context.Context, s *protocol.Subnet) error {
	_, err := r.db.sqlDB.ExecContext(ctx, `
		INSERT INTO subnets (id, vpc_id, name, cidr, gateway, netavark_network_name, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, s.ID, s.VPCID, s.Name, s.CIDR, s.Gateway, s.Netavark, s.CreatedAt)
	return err
}

func (r *Repository) GetSubnetByID(ctx context.Context, id string) (*protocol.Subnet, error) {
	row := r.db.sqlDB.QueryRowContext(ctx, "SELECT id, vpc_id, name, cidr, gateway, netavark_network_name, created_at FROM subnets WHERE id = ?", id)
	var s protocol.Subnet
	err := row.Scan(&s.ID, &s.VPCID, &s.Name, &s.CIDR, &s.Gateway, &s.Netavark, &s.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("subnet not found")
	}
	return &s, err
}

func (r *Repository) ListSubnetsByVPC(ctx context.Context, vpcID string) ([]*protocol.Subnet, error) {
	rows, err := r.db.sqlDB.QueryContext(ctx, "SELECT id, vpc_id, name, cidr, gateway, netavark_network_name, created_at FROM subnets WHERE vpc_id = ?", vpcID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*protocol.Subnet
	for rows.Next() {
		var s protocol.Subnet
		if err := rows.Scan(&s.ID, &s.VPCID, &s.Name, &s.CIDR, &s.Gateway, &s.Netavark, &s.CreatedAt); err != nil {
			return nil, err
		}
		list = append(list, &s)
	}
	return list, nil
}

// -------------------------------------------------------------
// Volume Repository
// -------------------------------------------------------------

func (r *Repository) CreateVolume(ctx context.Context, v *protocol.Volume) error {
	_, err := r.db.sqlDB.ExecContext(ctx, `
		INSERT INTO volumes (id, tenant_id, project_id, name, driver, host_path, size_mb, attached_app, attached_mount, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, v.ID, v.TenantID, v.ProjectID, v.Name, v.Driver, v.HostPath, v.SizeMB, v.AttachedApp, v.AttachedMount, v.Status, v.CreatedAt)
	return err
}

func (r *Repository) GetVolumeByID(ctx context.Context, id string) (*protocol.Volume, error) {
	row := r.db.sqlDB.QueryRowContext(ctx, `
		SELECT id, tenant_id, project_id, name, driver, host_path, size_mb, attached_app, attached_mount, status, created_at
		FROM volumes WHERE id = ?
	`, id)
	return scanVolume(row)
}

func (r *Repository) GetVolumeByName(ctx context.Context, tenantID, name string) (*protocol.Volume, error) {
	row := r.db.sqlDB.QueryRowContext(ctx, `
		SELECT id, tenant_id, project_id, name, driver, host_path, size_mb, attached_app, attached_mount, status, created_at
		FROM volumes WHERE tenant_id = ? AND name = ?
	`, tenantID, name)
	return scanVolume(row)
}

func (r *Repository) ListVolumesByTenant(ctx context.Context, tenantID string) ([]*protocol.Volume, error) {
	rows, err := r.db.sqlDB.QueryContext(ctx, `
		SELECT id, tenant_id, project_id, name, driver, host_path, size_mb, attached_app, attached_mount, status, created_at
		FROM volumes WHERE tenant_id = ? ORDER BY name ASC
	`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*protocol.Volume
	for rows.Next() {
		v, err := scanVolumeRows(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, v)
	}
	return list, nil
}

func (r *Repository) UpdateVolumeAttachment(ctx context.Context, id, appName, mountPath, status string) error {
	_, err := r.db.sqlDB.ExecContext(ctx, `
		UPDATE volumes SET attached_app = ?, attached_mount = ?, status = ? WHERE id = ?
	`, appName, mountPath, status, id)
	return err
}

func (r *Repository) DeleteVolume(ctx context.Context, id string) error {
	_, err := r.db.sqlDB.ExecContext(ctx, "DELETE FROM volumes WHERE id = ?", id)
	return err
}

func scanVolume(row *sql.Row) (*protocol.Volume, error) {
	var v protocol.Volume
	err := row.Scan(&v.ID, &v.TenantID, &v.ProjectID, &v.Name, &v.Driver, &v.HostPath, &v.SizeMB, &v.AttachedApp, &v.AttachedMount, &v.Status, &v.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("volume not found")
	}
	return &v, err
}

func scanVolumeRows(rows *sql.Rows) (*protocol.Volume, error) {
	var v protocol.Volume
	err := rows.Scan(&v.ID, &v.TenantID, &v.ProjectID, &v.Name, &v.Driver, &v.HostPath, &v.SizeMB, &v.AttachedApp, &v.AttachedMount, &v.Status, &v.CreatedAt)
	return &v, err
}

// -------------------------------------------------------------
// Workload Repository
// -------------------------------------------------------------

func (r *Repository) CreateWorkload(ctx context.Context, wl *protocol.Workload) error {
	query := `
		INSERT INTO workloads (
			id, project_id, vpc_id, subnet_id, name,
			image_tag, image_digest, commit_sha, container_id,
			status, health_status, internal_ip, host_port, container_port,
			ingress_domain, current_release, quadlet_unit_name, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id, name) DO UPDATE SET
			image_tag = excluded.image_tag,
			image_digest = excluded.image_digest,
			commit_sha = excluded.commit_sha,
			container_id = excluded.container_id,
			status = excluded.status,
			health_status = excluded.health_status,
			internal_ip = excluded.internal_ip,
			host_port = excluded.host_port,
			container_port = excluded.container_port,
			ingress_domain = excluded.ingress_domain,
			current_release = excluded.current_release,
			quadlet_unit_name = excluded.quadlet_unit_name,
			updated_at = excluded.updated_at;
	`
	_, err := r.db.sqlDB.ExecContext(ctx, query,
		wl.ID, wl.ProjectID, wl.VPCID, wl.SubnetID, wl.Name,
		wl.ImageTag, wl.ImageDigest, wl.CommitSHA, wl.ContainerID,
		wl.Status, wl.HealthStatus, wl.InternalIP, wl.HostPort, wl.ContainerPort,
		wl.IngressDomain, wl.CurrentRelease, wl.QuadletUnitName, wl.CreatedAt, wl.UpdatedAt,
	)
	return err
}

func (r *Repository) GetWorkloadByID(ctx context.Context, id string) (*protocol.Workload, error) {
	row := r.db.sqlDB.QueryRowContext(ctx, `
		SELECT id, project_id, vpc_id, subnet_id, name,
		       image_tag, image_digest, commit_sha, container_id,
		       status, health_status, internal_ip, host_port, container_port,
		       ingress_domain, current_release, quadlet_unit_name, created_at, updated_at
		FROM workloads WHERE id = ?
	`, id)
	return scanWorkload(row)
}

func (r *Repository) GetWorkloadByName(ctx context.Context, projectID, name string) (*protocol.Workload, error) {
	row := r.db.sqlDB.QueryRowContext(ctx, `
		SELECT id, project_id, vpc_id, subnet_id, name,
		       image_tag, image_digest, commit_sha, container_id,
		       status, health_status, internal_ip, host_port, container_port,
		       ingress_domain, current_release, quadlet_unit_name, created_at, updated_at
		FROM workloads WHERE project_id = ? AND name = ?
	`, projectID, name)
	return scanWorkload(row)
}

func (r *Repository) ListWorkloadsByProject(ctx context.Context, projectID string) ([]*protocol.Workload, error) {
	rows, err := r.db.sqlDB.QueryContext(ctx, `
		SELECT id, project_id, vpc_id, subnet_id, name,
		       image_tag, image_digest, commit_sha, container_id,
		       status, health_status, internal_ip, host_port, container_port,
		       ingress_domain, current_release, quadlet_unit_name, created_at, updated_at
		FROM workloads WHERE project_id = ? ORDER BY name ASC
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*protocol.Workload
	for rows.Next() {
		wl, err := scanWorkloadRows(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, wl)
	}
	return list, nil
}

func (r *Repository) ListAllWorkloads(ctx context.Context) ([]*protocol.Workload, error) {
	rows, err := r.db.sqlDB.QueryContext(ctx, `
		SELECT id, project_id, vpc_id, subnet_id, name,
		       image_tag, image_digest, commit_sha, container_id,
		       status, health_status, internal_ip, host_port, container_port,
		       ingress_domain, current_release, quadlet_unit_name, created_at, updated_at
		FROM workloads ORDER BY name ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*protocol.Workload
	for rows.Next() {
		wl, err := scanWorkloadRows(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, wl)
	}
	return list, nil
}

func (r *Repository) UpdateWorkloadStatus(ctx context.Context, id, status, healthStatus, containerID, internalIP string) error {
	_, err := r.db.sqlDB.ExecContext(ctx, `
		UPDATE workloads SET
			status = ?, health_status = ?, container_id = ?, internal_ip = ?, updated_at = ?
		WHERE id = ?
	`, status, healthStatus, containerID, internalIP, time.Now(), id)
	return err
}

func (r *Repository) UpdateWorkloadRelease(ctx context.Context, id string, releaseNum int, imageTag, imageDigest, commitSHA string) error {
	_, err := r.db.sqlDB.ExecContext(ctx, `
		UPDATE workloads SET
			current_release = ?, image_tag = ?, image_digest = ?, commit_sha = ?, updated_at = ?
		WHERE id = ?
	`, releaseNum, imageTag, imageDigest, commitSHA, time.Now(), id)
	return err
}

func scanWorkload(row *sql.Row) (*protocol.Workload, error) {
	var wl protocol.Workload
	err := row.Scan(
		&wl.ID, &wl.ProjectID, &wl.VPCID, &wl.SubnetID, &wl.Name,
		&wl.ImageTag, &wl.ImageDigest, &wl.CommitSHA, &wl.ContainerID,
		&wl.Status, &wl.HealthStatus, &wl.InternalIP, &wl.HostPort, &wl.ContainerPort,
		&wl.IngressDomain, &wl.CurrentRelease, &wl.QuadletUnitName, &wl.CreatedAt, &wl.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("workload not found")
	}
	return &wl, err
}

func scanWorkloadRows(rows *sql.Rows) (*protocol.Workload, error) {
	var wl protocol.Workload
	err := rows.Scan(
		&wl.ID, &wl.ProjectID, &wl.VPCID, &wl.SubnetID, &wl.Name,
		&wl.ImageTag, &wl.ImageDigest, &wl.CommitSHA, &wl.ContainerID,
		&wl.Status, &wl.HealthStatus, &wl.InternalIP, &wl.HostPort, &wl.ContainerPort,
		&wl.IngressDomain, &wl.CurrentRelease, &wl.QuadletUnitName, &wl.CreatedAt, &wl.UpdatedAt,
	)
	return &wl, err
}

// -------------------------------------------------------------
// Deployment Repository
// -------------------------------------------------------------

func (r *Repository) CreateDeployment(ctx context.Context, d *protocol.Deployment) error {
	query := `
		INSERT INTO deployments (
			id, workload_id, project_id, release_number,
			commit_sha, image_tag, image_digest,
			manifest_yaml, quadlet_spec, status, logs,
			error_message, started_at, completed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err := r.db.sqlDB.ExecContext(ctx, query,
		d.ID, d.WorkloadID, d.ProjectID, d.ReleaseNumber,
		d.CommitSHA, d.ImageTag, d.ImageDigest,
		d.ManifestYAML, d.QuadletSpec, d.Status, d.Logs,
		d.ErrorMessage, d.StartedAt, d.CompletedAt,
	)
	return err
}

func (r *Repository) GetDeploymentByID(ctx context.Context, id string) (*protocol.Deployment, error) {
	row := r.db.sqlDB.QueryRowContext(ctx, `
		SELECT id, workload_id, project_id, release_number,
		       commit_sha, image_tag, image_digest,
		       manifest_yaml, quadlet_spec, status, logs,
		       error_message, started_at, completed_at
		FROM deployments WHERE id = ?
	`, id)
	return scanDeployment(row)
}

func (r *Repository) GetLatestDeployment(ctx context.Context, workloadID string) (*protocol.Deployment, error) {
	row := r.db.sqlDB.QueryRowContext(ctx, `
		SELECT id, workload_id, project_id, release_number,
		       commit_sha, image_tag, image_digest,
		       manifest_yaml, quadlet_spec, status, logs,
		       error_message, started_at, completed_at
		FROM deployments WHERE workload_id = ? ORDER BY release_number DESC LIMIT 1
	`, workloadID)
	return scanDeployment(row)
}

func (r *Repository) GetDeploymentByRelease(ctx context.Context, workloadID string, releaseNum int) (*protocol.Deployment, error) {
	row := r.db.sqlDB.QueryRowContext(ctx, `
		SELECT id, workload_id, project_id, release_number,
		       commit_sha, image_tag, image_digest,
		       manifest_yaml, quadlet_spec, status, logs,
		       error_message, started_at, completed_at
		FROM deployments WHERE workload_id = ? AND release_number = ?
	`, workloadID, releaseNum)
	return scanDeployment(row)
}

func (r *Repository) ListDeploymentsByWorkload(ctx context.Context, workloadID string) ([]*protocol.Deployment, error) {
	rows, err := r.db.sqlDB.QueryContext(ctx, `
		SELECT id, workload_id, project_id, release_number,
		       commit_sha, image_tag, image_digest,
		       manifest_yaml, quadlet_spec, status, logs,
		       error_message, started_at, completed_at
		FROM deployments WHERE workload_id = ? ORDER BY release_number DESC
	`, workloadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*protocol.Deployment
	for rows.Next() {
		d, err := scanDeploymentRows(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, d)
	}
	return list, nil
}

func (r *Repository) UpdateDeploymentStatus(ctx context.Context, id, status, errorMsg string, completedAt *time.Time) error {
	_, err := r.db.sqlDB.ExecContext(ctx, `
		UPDATE deployments SET status = ?, error_message = ?, completed_at = ? WHERE id = ?
	`, status, errorMsg, completedAt, id)
	return err
}

func (r *Repository) AppendDeploymentLogs(ctx context.Context, id, logChunk string) error {
	_, err := r.db.sqlDB.ExecContext(ctx, `
		UPDATE deployments SET logs = logs || ? WHERE id = ?
	`, logChunk, id)
	return err
}

func scanDeployment(row *sql.Row) (*protocol.Deployment, error) {
	var d protocol.Deployment
	err := row.Scan(
		&d.ID, &d.WorkloadID, &d.ProjectID, &d.ReleaseNumber,
		&d.CommitSHA, &d.ImageTag, &d.ImageDigest,
		&d.ManifestYAML, &d.QuadletSpec, &d.Status, &d.Logs,
		&d.ErrorMessage, &d.StartedAt, &d.CompletedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("deployment not found")
	}
	return &d, err
}

func scanDeploymentRows(rows *sql.Rows) (*protocol.Deployment, error) {
	var d protocol.Deployment
	err := rows.Scan(
		&d.ID, &d.WorkloadID, &d.ProjectID, &d.ReleaseNumber,
		&d.CommitSHA, &d.ImageTag, &d.ImageDigest,
		&d.ManifestYAML, &d.QuadletSpec, &d.Status, &d.Logs,
		&d.ErrorMessage, &d.StartedAt, &d.CompletedAt,
	)
	return &d, err
}

// -------------------------------------------------------------
// Job Repository (Durable SQLite Queue)
// -------------------------------------------------------------

func (r *Repository) EnqueueJob(ctx context.Context, job *protocol.Job) error {
	query := `
		INSERT INTO jobs (
			id, type, status, payload_json, idempotency_key,
			retry_count, max_retries, scheduled_at, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err := r.db.sqlDB.ExecContext(ctx, query,
		job.ID, job.Type, job.Status, job.PayloadJSON, job.IdempotencyKey,
		job.RetryCount, job.MaxRetries, job.ScheduledAt, job.CreatedAt,
	)
	return err
}

// ClaimNextJob claims the next queued job in a single, short-lived transaction.
func (r *Repository) ClaimNextJob(ctx context.Context, workerID string) (*protocol.Job, error) {
	tx, err := r.db.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	now := time.Now()
	var job protocol.Job

	// Select next candidate job
	row := tx.QueryRowContext(ctx, `
		SELECT id, type, status, payload_json, idempotency_key,
		       retry_count, max_retries, worker_id, error_details,
		       scheduled_at, started_at, completed_at, created_at
		FROM jobs
		WHERE status = 'QUEUED' AND scheduled_at <= ?
		ORDER BY scheduled_at ASC LIMIT 1
	`, now)

	err = row.Scan(
		&job.ID, &job.Type, &job.Status, &job.PayloadJSON, &job.IdempotencyKey,
		&job.RetryCount, &job.MaxRetries, &job.WorkerID, &job.ErrorDetails,
		&job.ScheduledAt, &job.StartedAt, &job.CompletedAt, &job.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil // No job ready
	}
	if err != nil {
		return nil, err
	}

	// Atomically update state to RUNNING
	_, err = tx.ExecContext(ctx, `
		UPDATE jobs SET status = 'RUNNING', worker_id = ?, started_at = ? WHERE id = ?
	`, workerID, now, job.ID)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	job.Status = "RUNNING"
	job.WorkerID = workerID
	job.StartedAt = &now
	return &job, nil
}

func (r *Repository) CompleteJob(ctx context.Context, id string) error {
	now := time.Now()
	_, err := r.db.sqlDB.ExecContext(ctx, `
		UPDATE jobs SET status = 'COMPLETED', completed_at = ? WHERE id = ?
	`, now, id)
	return err
}

func (r *Repository) FailJob(ctx context.Context, id string, errStr string, retryIn *time.Duration) error {
	if retryIn != nil {
		scheduled := time.Now().Add(*retryIn)
		_, err := r.db.sqlDB.ExecContext(ctx, `
			UPDATE jobs SET
				status = 'QUEUED',
				retry_count = retry_count + 1,
				error_details = ?,
				scheduled_at = ?
			WHERE id = ?
		`, errStr, scheduled, id)
		return err
	}

	now := time.Now()
	_, err := r.db.sqlDB.ExecContext(ctx, `
		UPDATE jobs SET
			status = 'FAILED',
			error_details = ?,
			completed_at = ?
		WHERE id = ?
	`, errStr, now, id)
	return err
}

// RecoverInterruptedJobs resets jobs that were marked 'RUNNING' when the process crashed or restarted.
func (r *Repository) RecoverInterruptedJobs(ctx context.Context) (int, error) {
	res, err := r.db.sqlDB.ExecContext(ctx, `
		UPDATE jobs
		SET status = 'QUEUED',
		    retry_count = retry_count + 1,
		    error_details = 'Recovered after process restart'
		WHERE status = 'RUNNING'
	`)
	if err != nil {
		return 0, err
	}
	rows, _ := res.RowsAffected()
	return int(rows), nil
}

func (r *Repository) GetJobByID(ctx context.Context, id string) (*protocol.Job, error) {
	row := r.db.sqlDB.QueryRowContext(ctx, `
		SELECT id, type, status, payload_json, idempotency_key,
		       retry_count, max_retries, worker_id, error_details,
		       scheduled_at, started_at, completed_at, created_at
		FROM jobs WHERE id = ?
	`, id)

	var job protocol.Job
	err := row.Scan(
		&job.ID, &job.Type, &job.Status, &job.PayloadJSON, &job.IdempotencyKey,
		&job.RetryCount, &job.MaxRetries, &job.WorkerID, &job.ErrorDetails,
		&job.ScheduledAt, &job.StartedAt, &job.CompletedAt, &job.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("job not found")
	}
	return &job, err
}

func (r *Repository) ListRecentJobs(ctx context.Context, limit int) ([]*protocol.Job, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.sqlDB.QueryContext(ctx, `
		SELECT id, type, status, payload_json, idempotency_key,
		       retry_count, max_retries, worker_id, error_details,
		       scheduled_at, started_at, completed_at, created_at
		FROM jobs ORDER BY created_at DESC LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*protocol.Job
	for rows.Next() {
		var job protocol.Job
		err := rows.Scan(
			&job.ID, &job.Type, &job.Status, &job.PayloadJSON, &job.IdempotencyKey,
			&job.RetryCount, &job.MaxRetries, &job.WorkerID, &job.ErrorDetails,
			&job.ScheduledAt, &job.StartedAt, &job.CompletedAt, &job.CreatedAt,
		)
		if err != nil {
			return nil, err
		}
		list = append(list, &job)
	}
	return list, nil
}

// -------------------------------------------------------------
// Audit Repository
// -------------------------------------------------------------

func (r *Repository) RecordEvent(ctx context.Context, event *protocol.AuditEvent) error {
	_, err := r.db.sqlDB.ExecContext(ctx, `
		INSERT INTO audit_events (actor, action, resource_type, resource_id, ip_address, details, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, event.Actor, event.Action, event.ResourceType, event.ResourceID, event.IPAddress, event.Details, event.CreatedAt)
	return err
}

func (r *Repository) ListRecentEvents(ctx context.Context, limit int) ([]*protocol.AuditEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.sqlDB.QueryContext(ctx, `
		SELECT id, actor, action, resource_type, resource_id, ip_address, details, created_at
		FROM audit_events ORDER BY id DESC LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*protocol.AuditEvent
	for rows.Next() {
		var ev protocol.AuditEvent
		err := rows.Scan(&ev.ID, &ev.Actor, &ev.Action, &ev.ResourceType, &ev.ResourceID, &ev.IPAddress, &ev.Details, &ev.CreatedAt)
		if err != nil {
			return nil, err
		}
		list = append(list, &ev)
	}
	return list, nil
}
