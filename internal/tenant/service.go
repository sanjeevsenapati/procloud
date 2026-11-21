package tenant

import (
	"context"
	"fmt"
	"strings"
	"time"

	"procloud.io/procloud/internal/repository"
	"procloud.io/procloud/pkg/protocol"
)

type Service struct {
	repo repository.TenantRepository
}

func NewService(repo repository.TenantRepository) *Service {
	return &Service{repo: repo}
}

func (s *Service) CreateTenant(ctx context.Context, name string) (*protocol.Tenant, error) {
	return s.CreateOrganization(ctx, name, "", "free", 20, 10, 8, 16384, 100)
}

func (s *Service) CreateOrganization(ctx context.Context, name, description, plan string, maxCtrs, maxPods, maxCPU, maxMem, maxStorage int) (*protocol.Tenant, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("organization name cannot be empty")
	}
	slug := strings.ToLower(strings.ReplaceAll(name, " ", "-"))
	if plan == "" {
		plan = "free"
	}
	if maxCtrs <= 0 {
		maxCtrs = 20
	}
	if maxPods <= 0 {
		maxPods = 10
	}
	if maxCPU <= 0 {
		maxCPU = 8
	}
	if maxMem <= 0 {
		maxMem = 16384
	}
	if maxStorage <= 0 {
		maxStorage = 100
	}

	tenant := &protocol.Tenant{
		ID:            fmt.Sprintf("tnt-%d", time.Now().UnixNano()),
		Name:          name,
		Slug:          slug,
		Description:   description,
		Plan:          plan,
		Status:        "active",
		MaxContainers: maxCtrs,
		MaxPods:       maxPods,
		MaxCPUCores:   maxCPU,
		MaxMemoryMB:   maxMem,
		MaxStorageGB:  maxStorage,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}

	if err := s.repo.CreateTenant(ctx, tenant); err != nil {
		return nil, fmt.Errorf("failed to create organization: %w", err)
	}

	return tenant, nil
}

func (s *Service) GetOrganization(ctx context.Context, id string) (*protocol.Tenant, error) {
	return s.repo.GetTenantByID(ctx, id)
}

func (s *Service) UpdateOrganization(ctx context.Context, id, name, description, plan, status string, maxCtrs, maxPods, maxCPU, maxMem, maxStorage int) (*protocol.Tenant, error) {
	existing, err := s.repo.GetTenantByID(ctx, id)
	if err != nil {
		return nil, err
	}

	if name != "" {
		existing.Name = strings.TrimSpace(name)
		existing.Slug = strings.ToLower(strings.ReplaceAll(existing.Name, " ", "-"))
	}
	if description != "" {
		existing.Description = description
	}
	if plan != "" {
		existing.Plan = plan
	}
	if status != "" {
		existing.Status = status
	}
	if maxCtrs > 0 {
		existing.MaxContainers = maxCtrs
	}
	if maxPods > 0 {
		existing.MaxPods = maxPods
	}
	if maxCPU > 0 {
		existing.MaxCPUCores = maxCPU
	}
	if maxMem > 0 {
		existing.MaxMemoryMB = maxMem
	}
	if maxStorage > 0 {
		existing.MaxStorageGB = maxStorage
	}
	existing.UpdatedAt = time.Now()

	if err := s.repo.UpdateTenant(ctx, existing); err != nil {
		return nil, fmt.Errorf("failed to update organization: %w", err)
	}
	return existing, nil
}

func (s *Service) DeleteOrganization(ctx context.Context, id string) error {
	return s.repo.DeleteTenant(ctx, id)
}

func (s *Service) ListTenants(ctx context.Context) ([]*protocol.Tenant, error) {
	return s.repo.ListTenants(ctx)
}

func (s *Service) ListOrganizations(ctx context.Context) ([]*protocol.Tenant, error) {
	return s.repo.ListTenants(ctx)
}

func (s *Service) CreateProject(ctx context.Context, tenantID, name, repoURL, branch string) (*protocol.Project, error) {
	if branch == "" {
		branch = "main"
	}
	slug := strings.ToLower(strings.ReplaceAll(name, " ", "-"))
	project := &protocol.Project{
		ID:            fmt.Sprintf("prj-%d", time.Now().UnixNano()),
		TenantID:      tenantID,
		Name:          name,
		Slug:          slug,
		RepositoryURL: repoURL,
		Branch:        branch,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}

	if err := s.repo.CreateProject(ctx, project); err != nil {
		return nil, fmt.Errorf("failed to create project: %w", err)
	}

	return project, nil
}

func (s *Service) ListProjects(ctx context.Context, tenantID string) ([]*protocol.Project, error) {
	return s.repo.ListProjectsByTenant(ctx, tenantID)
}

func (s *Service) GetProject(ctx context.Context, id string) (*protocol.Project, error) {
	return s.repo.GetProjectByID(ctx, id)
}
