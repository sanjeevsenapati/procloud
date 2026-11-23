package network

import (
	"context"
	"fmt"

	"procloud.io/procloud/pkg/podman"
)

// NetworkProvider abstracts container networking implementation.
type NetworkProvider interface {
	CreateNetwork(ctx context.Context, netName, subnet, gateway string) error
	CreateNetworkWithOptions(ctx context.Context, opts podman.NetworkCreateOptions) error
	DeleteNetwork(ctx context.Context, netName string) error
}

type NetavarkProvider struct {
	runtime podman.ContainerRuntime
}

func NewNetavarkProvider(runtime podman.ContainerRuntime) *NetavarkProvider {
	return &NetavarkProvider{runtime: runtime}
}

func (n *NetavarkProvider) CreateNetwork(ctx context.Context, netName, subnet, gateway string) error {
	if err := n.runtime.CreateNetwork(ctx, netName, subnet, gateway); err != nil {
		return fmt.Errorf("failed to create netavark network %s: %w", netName, err)
	}
	return nil
}

func (n *NetavarkProvider) CreateNetworkWithOptions(ctx context.Context, opts podman.NetworkCreateOptions) error {
	if err := n.runtime.CreateNetworkWithOptions(ctx, opts); err != nil {
		return fmt.Errorf("failed to create netavark network %s: %w", opts.Name, err)
	}
	return nil
}

func (n *NetavarkProvider) DeleteNetwork(ctx context.Context, netName string) error {
	return n.runtime.RemoveNetwork(ctx, netName)
}
