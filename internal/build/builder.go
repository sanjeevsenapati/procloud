package build

import (
	"context"
	"fmt"

	"procloud.io/procloud/pkg/podman"
)

type ImageBuilder interface {
	Build(ctx context.Context, contextDir, containerfilePath, tag string) (string, error)
}

type PodmanImageBuilder struct {
	runtime podman.ContainerRuntime
}

func NewPodmanImageBuilder(runtime podman.ContainerRuntime) *PodmanImageBuilder {
	return &PodmanImageBuilder{runtime: runtime}
}

func (b *PodmanImageBuilder) Build(ctx context.Context, contextDir, containerfilePath, tag string) (string, error) {
	digest, err := b.runtime.BuildImage(ctx, contextDir, containerfilePath, tag)
	if err != nil {
		return "", fmt.Errorf("image build failed: %w", err)
	}
	return digest, nil
}
