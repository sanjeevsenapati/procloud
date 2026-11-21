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
	slug := strings.ToLower(strings.ReplaceAll(name, " ", "-"))
	tenant := &protocol.Tenant{
		ID:        fmt.Sprintf("tnt-%d", time.Now().UnixNano()),
		Name:      name,
		Slug:      slug,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	if err := s.repo.CreateTenant(ctx, tenant); err != nil {
		return nil, fmt.Errorf("failed to create tenant: %w", err)
	}

	return tenant, nil
}

func (s *Service) ListTenants(ctx context.Context) ([]*protocol.Tenant, error) {
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
