package tests

import (
	"context"
	"strings"
	"testing"

	"procloud.io/procloud/pkg/podman"
)

func TestContainerDetailsAndKubeDeploy(t *testing.T) {
	ctx := context.Background()
	rt := podman.NewMockRuntime()

	// 1. Run a container with rich configuration
	ctrID, err := rt.RunContainer(ctx, podman.RunOptions{
		Name:    "api-backend",
		Image:   "docker.io/library/golang:alpine",
		Network: "netavark",
		Ports:   []string{"8080:8080"},
		Volumes: []string{"api-data:/var/data:rw"},
		Env: map[string]string{
			"PORT":     "8080",
			"ENV_MODE": "production",
		},
		Command: []string{"/app/server", "--port=8080"},
		Restart: "always",
	})
	if err != nil || ctrID == "" {
		t.Fatalf("failed running container: %v", err)
	}

	// 2. Inspect container and verify rich metadata
	inspect, err := rt.InspectContainer(ctx, "api-backend")
	if err != nil {
		t.Fatalf("failed inspecting container: %v", err)
	}
	if inspect.Name != "api-backend" {
		t.Errorf("expected name api-backend, got %s", inspect.Name)
	}
	if len(inspect.Env) == 0 {
		t.Errorf("expected non-empty env vars")
	}
	if len(inspect.Mounts) == 0 {
		t.Errorf("expected non-empty mounts")
	}
	if inspect.WorkingDir == "" {
		t.Errorf("expected working directory to be set")
	}

	// 3. Generate Kubernetes manifest for the container
	kubeYAML, err := rt.GenerateKube(ctx, "api-backend")
	if err != nil {
		t.Fatalf("failed generating kube YAML: %v", err)
	}
	if !strings.Contains(kubeYAML, "kind: Pod") || !strings.Contains(kubeYAML, "api-backend") {
		t.Errorf("invalid kube YAML generated: %s", kubeYAML)
	}

	// 4. Test PlayKube deploying a Pod from Kubernetes YAML
	sampleKube := `apiVersion: v1
kind: Pod
metadata:
  name: payment-service-pod
spec:
  containers:
  - name: payment-api
    image: docker.io/library/nginx:alpine
    ports:
    - containerPort: 80
`
	playResult, err := rt.PlayKube(ctx, sampleKube)
	if err != nil {
		t.Fatalf("failed playing kube: %v", err)
	}
	if len(playResult.Pods) == 0 || len(playResult.Containers) == 0 {
		t.Fatalf("expected created pods and containers, got %+v", playResult)
	}

	// Verify pod was created in runtime
	podInspect, err := rt.InspectPod(ctx, "payment-service-pod")
	if err != nil || podInspect.Name != "payment-service-pod" {
		t.Fatalf("deployed pod not found in runtime: %v", err)
	}

	// 5. Test BuildImage from Containerfile
	containerfile := `FROM alpine:latest
RUN echo "building test image"
CMD ["echo", "hello"]
`
	_ = containerfile
	digest, err := rt.BuildImage(ctx, "/tmp", "", "test-app:v1.0.0")
	if err != nil || digest == "" {
		t.Fatalf("failed building image: %v", err)
	}

	images, _ := rt.ListImages(ctx)
	found := false
	for _, img := range images {
		if img.Repository == "test-app" && img.Tag == "v1.0.0" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("built image test-app:v1.0.0 not found in runtime image list")
	}
}
