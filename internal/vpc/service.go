package vpc

import (
	"context"
	"fmt"
	"sync"
	"time"

	"procloud.io/procloud/internal/network"
	"procloud.io/procloud/internal/repository"
	"procloud.io/procloud/pkg/protocol"
)

type Service struct {
	repo            repository.VPCRepository
	networkProvider network.NetworkProvider
	mu              sync.Mutex
	nextVPCIndex    int
}

func NewService(repo repository.VPCRepository, provider network.NetworkProvider) *Service {
	return &Service{
		repo:            repo,
		networkProvider: provider,
		nextVPCIndex:    10,
	}
}

// CreateVPC provisions a tenant VPC with non-overlapping RFC1918 CIDR block.
func (s *Service) CreateVPC(ctx context.Context, tenantID, name string) (*protocol.VPC, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Assign unique 10.X.0.0/16 block per VPC
	s.nextVPCIndex++
	cidr := fmt.Sprintf("10.%d.0.0/16", s.nextVPCIndex)
	bridgeName := fmt.Sprintf("br-vpc-%d", s.nextVPCIndex)

	vpc := &protocol.VPC{
		ID:        fmt.Sprintf("vpc-%d", time.Now().UnixNano()),
		TenantID:  tenantID,
		Name:      name,
		CIDR:      cidr,
		Bridge:    bridgeName,
		CreatedAt: time.Now(),
	}

	if err := s.repo.CreateVPC(ctx, vpc); err != nil {
		return nil, fmt.Errorf("failed to save VPC: %w", err)
	}

	return vpc, nil
}

// CreateSubnet allocates a /24 subnet within a VPC and provisions Netavark bridge network.
func (s *Service) CreateSubnet(ctx context.Context, vpcID, name string) (*protocol.Subnet, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	v, err := s.repo.GetVPCByID(ctx, vpcID)
	if err != nil {
		return nil, fmt.Errorf("vpc not found: %w", err)
	}

	existing, err := s.repo.ListSubnetsByVPC(ctx, vpcID)
	if err != nil {
		return nil, err
	}

	subnetIdx := len(existing) + 1
	// Subnet CIDR based on VPC prefix
	var vpcOctet int
	fmt.Sscanf(v.CIDR, "10.%d.0.0/16", &vpcOctet)
	if vpcOctet == 0 {
		vpcOctet = 10
	}

	subnetCIDR := fmt.Sprintf("10.%d.%d.0/24", vpcOctet, subnetIdx)
	gateway := fmt.Sprintf("10.%d.%d.1", vpcOctet, subnetIdx)
	netName := fmt.Sprintf("pcnet-%s-%s", v.Name, name)

	// Create Podman / Netavark network
	if err := s.networkProvider.CreateNetwork(ctx, netName, subnetCIDR, gateway); err != nil {
		return nil, fmt.Errorf("failed to provision network: %w", err)
	}

	subnet := &protocol.Subnet{
		ID:        fmt.Sprintf("sub-%d", time.Now().UnixNano()),
		VPCID:     vpcID,
		Name:      name,
		CIDR:      subnetCIDR,
		Gateway:   gateway,
		Netavark:  netName,
		CreatedAt: time.Now(),
	}

	if err := s.repo.CreateSubnet(ctx, subnet); err != nil {
		return nil, fmt.Errorf("failed to save subnet: %w", err)
	}

	return subnet, nil
}

func (s *Service) ListVPCs(ctx context.Context, tenantID string) ([]*protocol.VPC, error) {
	return s.repo.ListVPCsByTenant(ctx, tenantID)
}

func (s *Service) ListSubnets(ctx context.Context, vpcID string) ([]*protocol.Subnet, error) {
	return s.repo.ListSubnetsByVPC(ctx, vpcID)
}
