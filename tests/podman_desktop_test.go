package tests

import (
	"context"
	"strings"
	"testing"

	"procloud.io/procloud/pkg/podman"
)

func TestPodmanDesktopFeatures(t *testing.T) {
	ctx := context.Background()
	rt := podman.NewMockRuntime()

	// 1. Image Catalog & Pulling
	images, err := rt.ListImages(ctx)
	if err != nil || len(images) == 0 {
		t.Fatalf("failed listing images: %v", err)
	}
	if err := rt.PullImage(ctx, "docker.io/library/nginx:alpine"); err != nil {
		t.Fatalf("failed pulling image: %v", err)
	}
	imagesAfter, _ := rt.ListImages(ctx)
	if len(imagesAfter) <= len(images) {
		t.Errorf("image count did not increase after pull")
	}

	// 2. Pod Creation, Inspection & Lifecycle
	podID, err := rt.CreatePod(ctx, podman.PodCreateOptions{
		Name:  "web-tier-pod",
		Ports: []string{"8080:80"},
	})
	if err != nil || podID == "" {
		t.Fatalf("failed creating pod: %v", err)
	}

	pods, err := rt.ListPods(ctx)
	if err != nil || len(pods) == 0 {
		t.Fatalf("failed listing pods: %v", err)
	}

	podInspect, err := rt.InspectPod(ctx, "web-tier-pod")
	if err != nil || podInspect.Name != "web-tier-pod" {
		t.Fatalf("pod inspect failed: %v", err)
	}

	kubeYAML, err := rt.GenerateKube(ctx, "web-tier-pod")
	if err != nil || !strings.Contains(kubeYAML, "kind: Pod") {
		t.Fatalf("generate kube failed: %v", err)
	}

	if err := rt.StopPod(ctx, "web-tier-pod"); err != nil {
		t.Errorf("stop pod failed: %v", err)
	}
	if err := rt.StartPod(ctx, "web-tier-pod"); err != nil {
		t.Errorf("start pod failed: %v", err)
	}

	// 3. Container Creation, Exec, Logs & Lifecycle
	ctrID, err := rt.RunContainer(ctx, podman.RunOptions{
		Name:    "nginx-service",
		Image:   "docker.io/library/nginx:alpine",
		Pod:     "web-tier-pod",
		Ports:   []string{"8080:80"},
		Detach:  true,
		Restart: "always",
	})
	if err != nil || ctrID == "" {
		t.Fatalf("failed running container: %v", err)
	}

	ctrs, err := rt.ListContainers(ctx, true)
	if err != nil || len(ctrs) == 0 {
		t.Fatalf("failed listing containers: %v", err)
	}

	logs, err := rt.GetContainerLogs(ctx, "nginx-service", 10)
	if err != nil || !strings.Contains(logs, "started successfully") {
		t.Errorf("failed fetching container logs: %v", err)
	}

	execOut, err := rt.ExecContainer(ctx, "nginx-service", []string{"echo", "procloud-test"})
	if err != nil || !strings.Contains(execOut, "procloud-test") {
		t.Errorf("exec failed: %v, out: %s", err, execOut)
	}

	// 3a. Top Container Processes
	topProcs, err := rt.TopContainer(ctx, "nginx-service")
	if err != nil || len(topProcs) == 0 {
		t.Fatalf("top container failed: %v", err)
	}
	if topProcs[0].Command == "" {
		t.Errorf("top container command is empty")
	}

	// 3b. Container Resource Stats
	stats, err := rt.ContainerStats(ctx, "nginx-service")
	if err != nil || stats.Name == "" {
		t.Fatalf("container stats failed: %v", err)
	}
	if stats.CPUPercent == "" || stats.MemUsage == "" {
		t.Errorf("incomplete container stats: %+v", stats)
	}

	if err := rt.RestartContainer(ctx, "nginx-service", 2); err != nil {
		t.Errorf("restart container failed: %v", err)
	}
	if err := rt.StopContainer(ctx, "nginx-service", 2); err != nil {
		t.Errorf("stop container failed: %v", err)
	}
	if err := rt.RemoveContainer(ctx, "nginx-service", true); err != nil {
		t.Errorf("remove container failed: %v", err)
	}

	// 4. Pod Removal
	if err := rt.RemovePod(ctx, "web-tier-pod", true); err != nil {
		t.Errorf("remove pod failed: %v", err)
	}

	// 5. Network Lifecycle
	if err := rt.CreateNetwork(ctx, "custom-net", "10.89.5.0/24", "10.89.5.1"); err != nil {
		t.Errorf("create network failed: %v", err)
	}
	nets, _ := rt.ListNetworks(ctx)
	if len(nets) == 0 {
		t.Errorf("no networks found")
	}
	if err := rt.RemoveNetwork(ctx, "custom-net"); err != nil {
		t.Errorf("remove network failed: %v", err)
	}

	// 6. Volume Lifecycle
	volPath, err := rt.CreateVolume(ctx, "custom-vol")
	if err != nil || volPath == "" {
		t.Errorf("create volume failed: %v", err)
	}
	vols, _ := rt.ListVolumes(ctx)
	if len(vols) == 0 {
		t.Errorf("no volumes found")
	}
	if err := rt.RemoveVolume(ctx, "custom-vol"); err != nil {
		t.Errorf("remove volume failed: %v", err)
	}

	// 7. Podman Desktop Secrets Management Engine Lifecycle
	secID, err := rt.CreateSecret(ctx, podman.SecretCreateOptions{
		Name:   "app_api_key",
		Driver: "file",
		Data:   "sk_live_983748293489234",
		Labels: map[string]string{"env": "staging", "service": "payment"},
	})
	if err != nil || secID == "" {
		t.Fatalf("failed creating secret: %v", err)
	}

	secrets, err := rt.ListSecrets(ctx)
	if err != nil || len(secrets) == 0 {
		t.Fatalf("failed listing secrets: %v", err)
	}

	// Secret inspect without reveal
	maskedSec, err := rt.InspectSecret(ctx, "app_api_key", false)
	if err != nil || maskedSec.Name != "app_api_key" {
		t.Fatalf("failed inspecting secret without reveal: %v", err)
	}
	if maskedSec.SecretData != "" {
		t.Errorf("expected secret data to be masked/empty, got %s", maskedSec.SecretData)
	}

	// Secret inspect with reveal
	revealedSec, err := rt.InspectSecret(ctx, "app_api_key", true)
	if err != nil || revealedSec.SecretData != "sk_live_983748293489234" {
		t.Fatalf("failed revealing secret data: %v", err)
	}

	// Container run with secret integration
	ctrWithSecID, err := rt.RunContainer(ctx, podman.RunOptions{
		Name:    "secure-worker",
		Image:   "docker.io/library/redis:alpine",
		Secrets: []string{"app_api_key", "db_password"},
		Detach:  true,
	})
	if err != nil || ctrWithSecID == "" {
		t.Fatalf("failed running container with secrets: %v", err)
	}

	if err := rt.RemoveContainer(ctx, "secure-worker", true); err != nil {
		t.Errorf("failed removing secure-worker container: %v", err)
	}

	if err := rt.RemoveSecret(ctx, "app_api_key"); err != nil {
		t.Errorf("failed removing secret: %v", err)
	}
}
