package compute

import (
	"context"
	"fmt"
	"time"

	"procloud.io/procloud/internal/repository"
	"procloud.io/procloud/pkg/podman"
	"procloud.io/procloud/pkg/protocol"
)

type Service struct {
	repo    repository.NodeRepository
	runtime podman.ContainerRuntime
}

func NewService(repo repository.NodeRepository, runtime podman.ContainerRuntime) *Service {
	return &Service{
		repo:    repo,
		runtime: runtime,
	}
}

// RegisterLocalHost queries host info and registers or updates the primary compute node.
func (s *Service) RegisterLocalHost(ctx context.Context) (*protocol.Node, error) {
	info, err := s.runtime.HostInfo(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to query host info: %w", err)
	}

	node := &protocol.Node{
		ID:                fmt.Sprintf("node-%s", info.Hostname),
		Hostname:          info.Hostname,
		Arch:              info.Arch,
		OS:                info.OS,
		Kernel:            info.Kernel,
		PodmanVersion:     info.PodmanVersion,
		TotalCPUMillis:    int64(info.TotalCPUs * 1000),
		TotalMemoryBytes:  info.TotalMemory,
		TotalStorageBytes: 100 * 1024 * 1024 * 1024, // 100 GB default storage
		AllocatedCPU:      0,
		AllocatedMemory:   0,
		AllocatedStorage:  0,
		Status:            "ready",
		IPAddress:         "127.0.0.1",
		LastHeartbeat:     time.Now(),
		CreatedAt:         time.Now(),
	}

	if err := s.repo.RegisterNode(ctx, node); err != nil {
		return nil, fmt.Errorf("failed to register compute node: %w", err)
	}

	return node, nil
}

// Heartbeat refreshes node status and heartbeat timestamp.
func (s *Service) Heartbeat(ctx context.Context, nodeID string) error {
	return s.repo.UpdateNodeHeartbeat(ctx, nodeID, "ready")
}

// GetPrimaryNode returns the authoritative compute node.
func (s *Service) GetPrimaryNode(ctx context.Context) (*protocol.Node, error) {
	return s.repo.GetPrimaryNode(ctx)
}
