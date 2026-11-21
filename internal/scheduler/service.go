package scheduler

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"procloud.io/procloud/internal/manifest"
	"procloud.io/procloud/internal/repository"
	"procloud.io/procloud/pkg/protocol"
)

type Scheduler interface {
	Schedule(ctx context.Context, m *manifest.DeployManifest) (*protocol.Node, error)
	Release(ctx context.Context, m *manifest.DeployManifest) error
}

type SingleNodeScheduler struct {
	nodeRepo repository.NodeRepository
}

func NewSingleNodeScheduler(nodeRepo repository.NodeRepository) *SingleNodeScheduler {
	return &SingleNodeScheduler{nodeRepo: nodeRepo}
}

// Schedule checks whether the single registered host has capacity, reserving CPU and Memory.
func (s *SingleNodeScheduler) Schedule(ctx context.Context, m *manifest.DeployManifest) (*protocol.Node, error) {
	node, err := s.nodeRepo.GetPrimaryNode(ctx)
	if err != nil {
		return nil, fmt.Errorf("no compute node registered: %w", err)
	}

	reqCPU := parseCPUMillis(m.Spec.Resources.Requests.CPU)
	reqMem := parseMemoryBytes(m.Spec.Resources.Requests.Memory)

	// Verify capacity
	if node.TotalCPUMillis > 0 && node.AllocatedCPU+reqCPU > node.TotalCPUMillis {
		return nil, fmt.Errorf("insufficient CPU: requested %dm, available %dm",
			reqCPU, node.TotalCPUMillis-node.AllocatedCPU)
	}
	if node.TotalMemoryBytes > 0 && node.AllocatedMemory+reqMem > node.TotalMemoryBytes {
		return nil, fmt.Errorf("insufficient memory: requested %d bytes, available %d bytes",
			reqMem, node.TotalMemoryBytes-node.AllocatedMemory)
	}

	// Update reservation
	newCPU := node.AllocatedCPU + reqCPU
	newMem := node.AllocatedMemory + reqMem
	if err := s.nodeRepo.UpdateNodeResources(ctx, node.ID, newCPU, newMem, node.AllocatedStorage); err != nil {
		return nil, fmt.Errorf("failed to reserve node resources: %w", err)
	}

	node.AllocatedCPU = newCPU
	node.AllocatedMemory = newMem
	return node, nil
}

// Release frees reservations when a workload is decommissioned.
func (s *SingleNodeScheduler) Release(ctx context.Context, m *manifest.DeployManifest) error {
	node, err := s.nodeRepo.GetPrimaryNode(ctx)
	if err != nil {
		return err
	}

	reqCPU := parseCPUMillis(m.Spec.Resources.Requests.CPU)
	reqMem := parseMemoryBytes(m.Spec.Resources.Requests.Memory)

	newCPU := node.AllocatedCPU - reqCPU
	if newCPU < 0 {
		newCPU = 0
	}
	newMem := node.AllocatedMemory - reqMem
	if newMem < 0 {
		newMem = 0
	}

	return s.nodeRepo.UpdateNodeResources(ctx, node.ID, newCPU, newMem, node.AllocatedStorage)
}

func parseCPUMillis(cpuStr string) int64 {
	if cpuStr == "" {
		return 100 // default 0.1 CPU
	}
	if strings.HasSuffix(cpuStr, "m") {
		val, _ := strconv.ParseInt(strings.TrimSuffix(cpuStr, "m"), 10, 64)
		return val
	}
	val, err := strconv.ParseFloat(cpuStr, 64)
	if err != nil {
		return 100
	}
	return int64(val * 1000)
}

func parseMemoryBytes(memStr string) int64 {
	if memStr == "" {
		return 128 * 1024 * 1024 // default 128MB
	}
	memStr = strings.TrimSpace(memStr)
	if strings.HasSuffix(memStr, "Gi") {
		val, _ := strconv.ParseInt(strings.TrimSuffix(memStr, "Gi"), 10, 64)
		return val * 1024 * 1024 * 1024
	}
	if strings.HasSuffix(memStr, "Mi") {
		val, _ := strconv.ParseInt(strings.TrimSuffix(memStr, "Mi"), 10, 64)
		return val * 1024 * 1024
	}
	if strings.HasSuffix(memStr, "Ki") {
		val, _ := strconv.ParseInt(strings.TrimSuffix(memStr, "Ki"), 10, 64)
		return val * 1024
	}
	val, _ := strconv.ParseInt(memStr, 10, 64)
	return val
}
