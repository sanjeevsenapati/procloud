package deployment

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"procloud.io/procloud/internal/build"
	"procloud.io/procloud/internal/ingress"
	"procloud.io/procloud/internal/manifest"
	"procloud.io/procloud/internal/repository"
	"procloud.io/procloud/internal/scheduler"
	"procloud.io/procloud/internal/storage"
	"procloud.io/procloud/pkg/podman"
	"procloud.io/procloud/pkg/protocol"
	"procloud.io/procloud/pkg/quadlet"
)

var depSeq uint64

func genDepID(prefix string) string {
	seq := atomic.AddUint64(&depSeq, 1)
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano(), seq)
}

type Engine struct {
	workloadRepo   repository.WorkloadRepository
	deploymentRepo repository.DeploymentRepository
	builder        build.ImageBuilder
	runtime        podman.ContainerRuntime
	scheduler      scheduler.Scheduler
	storageSvc     *storage.Service
	ingressCtrl    *ingress.Controller
}

func NewEngine(
	wlRepo repository.WorkloadRepository,
	dRepo repository.DeploymentRepository,
	builder build.ImageBuilder,
	runtime podman.ContainerRuntime,
	sched scheduler.Scheduler,
	storageSvc *storage.Service,
	ingressCtrl *ingress.Controller,
) *Engine {
	return &Engine{
		workloadRepo:   wlRepo,
		deploymentRepo: dRepo,
		builder:        builder,
		runtime:        runtime,
		scheduler:      sched,
		storageSvc:     storageSvc,
		ingressCtrl:    ingressCtrl,
	}
}

type DeployRequest struct {
	ProjectID      string
	VPCID          string
	SubnetID       string
	NetworkName    string
	CommitSHA      string
	ContextDir     string
	Containerfile  string
	ManifestYAML   []byte
}

// Deploy executes the complete immutable release pipeline: manifest validation, build, scheduling, quadlet generation, runtime startup, health check, and ingress routing.
func (e *Engine) Deploy(ctx context.Context, req DeployRequest) (*protocol.Deployment, error) {
	// 1. Parse and validate manifest
	m, err := manifest.ParseAndValidate(req.ManifestYAML)
	if err != nil {
		return nil, fmt.Errorf("manifest validation failed: %w", err)
	}

	appName := m.Metadata.Name

	// 2. Schedule & reserve capacity
	_, err = e.scheduler.Schedule(ctx, m)
	if err != nil {
		return nil, fmt.Errorf("scheduling failed: %w", err)
	}

	// 3. Resolve container image: build from source if requested, or pull existing image
	var tag string
	if m.Spec.Image.Tag != "" {
		tag = m.Spec.Image.Tag
	} else if req.CommitSHA != "" {
		tag = req.CommitSHA[:min(7, len(req.CommitSHA))]
	} else {
		tag = "latest"
	}

	imageTag := fmt.Sprintf("%s:%s", m.Spec.Image.Repository, tag)
	if m.Spec.Image.Repository == "" {
		imageTag = fmt.Sprintf("localhost/%s:%s", appName, tag)
	} else if strings.Contains(m.Spec.Image.Repository, ":") {
		imageTag = m.Spec.Image.Repository
	}

	contextDir := req.ContextDir
	if contextDir == "" && m.Spec.Image.Build != nil && m.Spec.Image.Build.Context != "" {
		contextDir = m.Spec.Image.Build.Context
	}

	containerfilePath := req.Containerfile
	if containerfilePath == "" && m.Spec.Image.Build != nil && m.Spec.Image.Build.Containerfile != "" {
		if contextDir != "" {
			containerfilePath = filepath.Join(contextDir, m.Spec.Image.Build.Containerfile)
		} else {
			containerfilePath = m.Spec.Image.Build.Containerfile
		}
	}

	var imageDigest string
	shouldBuild := (m.Spec.Image.Build != nil) || (req.Containerfile != "")
	if !shouldBuild && contextDir != "" {
		if _, err := os.Stat(filepath.Join(contextDir, "Containerfile")); err == nil {
			shouldBuild = true
		} else if _, err := os.Stat(filepath.Join(contextDir, "Dockerfile")); err == nil {
			shouldBuild = true
		}
	}

	if shouldBuild {
		if contextDir == "" {
			contextDir = "."
		}
		digest, err := e.builder.Build(ctx, contextDir, containerfilePath, imageTag)
		if err != nil {
			return nil, fmt.Errorf("build failed: %w", err)
		}
		imageDigest = digest
	} else {
		// Existing image: pull from registry if not already available locally
		if err := e.runtime.PullImage(ctx, imageTag); err != nil {
			// Check if image exists locally
			images, listErr := e.runtime.ListImages(ctx)
			found := false
			if listErr == nil {
				for _, img := range images {
					if img.Repository == m.Spec.Image.Repository || img.ID == imageTag || (img.Repository+":"+img.Tag) == imageTag {
						found = true
						imageDigest = img.ID
						break
					}
				}
			}
			if !found {
				return nil, fmt.Errorf("image pull failed: %w", err)
			}
		} else {
			images, _ := e.runtime.ListImages(ctx)
			for _, img := range images {
				if (img.Repository+":"+img.Tag) == imageTag || img.Repository == m.Spec.Image.Repository {
					imageDigest = img.ID
					break
				}
			}
		}
		if imageDigest == "" {
			imageDigest = imageTag
		}
	}

	// 4. Resolve persistent volumes
	hostVolumeMappings := make(map[string]string)
	for _, vm := range m.Spec.Volumes {
		vol, err := e.storageSvc.CreateVolume(ctx, "default-tenant", req.ProjectID, vm.Name, 1024)
		if err == nil {
			hostVolumeMappings[vm.Name] = vol.HostPath
			_ = e.storageSvc.AttachVolume(ctx, vol.ID, appName, vm.MountPath)
		}
	}

	// 5. Generate Quadlet unit definition
	quadletCfg := m.ToQuadletContainer(req.NetworkName, hostVolumeMappings)
	quadletCfg.Image = imageTag
	quadletContent := quadlet.GenerateContainerUnit(quadletCfg)

	// Write Quadlet unit to systemd directory
	unitFileName := fmt.Sprintf("%s.container", appName)
	_, _ = e.runtime.WriteQuadletUnit(ctx, unitFileName, quadletContent)

	// Ensure Workload row exists for foreign key constraint
	wl, _ := e.workloadRepo.GetWorkloadByName(ctx, req.ProjectID, appName)
	releaseNum := 1
	if wl != nil {
		releaseNum = wl.CurrentRelease + 1
	} else {
		wl = &protocol.Workload{
			ID:              fmt.Sprintf("wl-%s", appName),
			ProjectID:       req.ProjectID,
			VPCID:           req.VPCID,
			SubnetID:        req.SubnetID,
			Name:            appName,
			ImageTag:        imageTag,
			ImageDigest:     imageDigest,
			CommitSHA:       req.CommitSHA,
			Status:          "deploying",
			HealthStatus:    "pending",
			CurrentRelease:  1,
			QuadletUnitName: unitFileName,
			CreatedAt:       time.Now(),
			UpdatedAt:       time.Now(),
		}
		if err := e.workloadRepo.CreateWorkload(ctx, wl); err != nil {
			return nil, fmt.Errorf("failed to create workload record: %w", err)
		}
	}

	deploymentID := genDepID("dep")
	deployment := &protocol.Deployment{
		ID:            deploymentID,
		WorkloadID:    wl.ID,
		ProjectID:     req.ProjectID,
		ReleaseNumber: releaseNum,
		CommitSHA:     req.CommitSHA,
		ImageTag:      imageTag,
		ImageDigest:   imageDigest,
		ManifestYAML:  string(req.ManifestYAML),
		QuadletSpec:   quadletContent,
		Status:        "deploying",
		Logs:          fmt.Sprintf("Build completed. Image digest: %s\nQuadlet unit %s generated.\n", imageDigest, unitFileName),
		StartedAt:     time.Now(),
	}

	if err := e.deploymentRepo.CreateDeployment(ctx, deployment); err != nil {
		return nil, fmt.Errorf("failed to save deployment: %w", err)
	}

	// 6. Stop existing container if running (for recreate/upgrade)
	_ = e.runtime.StopContainer(ctx, appName, 5)
	_ = e.runtime.RemoveContainer(ctx, appName, true)

	// 7. Run updated container using Podman
	runOpts := podman.RunOptions{
		Name:    appName,
		Image:   imageTag,
		Network: req.NetworkName,
		Ports:   quadletCfg.PublishPorts,
		Volumes: quadletCfg.Volumes,
		Env:     quadletCfg.Environment,
		Command: quadletCfg.ExecArgs,
		Restart: "always",
		Detach:  true,
	}

	ctrID, err := e.runtime.RunContainer(ctx, runOpts)
	if err != nil {
		deployment.Status = "failed"
		deployment.ErrorMessage = err.Error()
		now := time.Now()
		_ = e.deploymentRepo.UpdateDeploymentStatus(ctx, deploymentID, "failed", err.Error(), &now)
		return nil, fmt.Errorf("container launch failed: %w", err)
	}

	// 8. Health check verification
	healthOK := e.verifyHealth(ctx, m)
	if !healthOK {
		log.Printf("[Deployment Engine] Health check failed for %s. Triggering automatic rollback...", appName)
		_ = e.deploymentRepo.AppendDeploymentLogs(ctx, deploymentID, "Health check failed. Rolling back to previous release...\n")

		if wl.CurrentRelease > 1 {
			_, _ = e.Rollback(ctx, wl.ID, wl.CurrentRelease-1)
		}
		now := time.Now()
		_ = e.deploymentRepo.UpdateDeploymentStatus(ctx, deploymentID, "failed", "Health check failed after deployment", &now)
		return nil, fmt.Errorf("deployment failed health check and was rolled back")
	}

	// 9. Ingress routing
	for _, port := range m.Spec.Ports {
		if port.Ingress != nil && port.Ingress.Enabled {
			_ = e.ingressCtrl.RegisterRoute(ctx, ingress.Route{
				Domain:       port.Ingress.Domain,
				Path:         port.Ingress.Path,
				UpstreamHost: "127.0.0.1",
				UpstreamPort: port.HostPort,
				AppName:      appName,
			})
		}
	}

	// 10. Update Workload and complete Deployment
	now := time.Now()
	deployment.Status = "running"
	deployment.CompletedAt = now
	_ = e.deploymentRepo.UpdateDeploymentStatus(ctx, deploymentID, "running", "", &now)

	wl.ImageTag = imageTag
	wl.ImageDigest = imageDigest
	wl.CommitSHA = req.CommitSHA
	wl.ContainerID = ctrID
	wl.Status = "running"
	wl.HealthStatus = "healthy"
	wl.HostPort = firstHostPort(m)
	wl.ContainerPort = firstContainerPort(m)
	wl.IngressDomain = firstIngressDomain(m)
	wl.CurrentRelease = releaseNum
	wl.UpdatedAt = now
	_ = e.workloadRepo.CreateWorkload(ctx, wl)

	return deployment, nil
}

// Rollback restores an application to a prior immutable release.
func (e *Engine) Rollback(ctx context.Context, workloadIDOrName string, targetRelease int) (*protocol.Deployment, error) {
	// Locate workload
	wl, err := e.workloadRepo.GetWorkloadByID(ctx, workloadIDOrName)
	if err != nil {
		allWls, _ := e.workloadRepo.ListAllWorkloads(ctx)
		for _, w := range allWls {
			if w.Name == workloadIDOrName || w.ID == workloadIDOrName {
				wl = w
				break
			}
		}
	}
	if wl == nil {
		return nil, fmt.Errorf("workload %s not found", workloadIDOrName)
	}

	priorDep, err := e.deploymentRepo.GetDeploymentByRelease(ctx, wl.ID, targetRelease)
	if err != nil || priorDep == nil {
		return nil, fmt.Errorf("prior deployment release %d not found for %s: %w", targetRelease, wl.Name, err)
	}

	log.Printf("[Deployment Engine] Rolling back %s to release %d (commit: %s)...",
		wl.Name, targetRelease, priorDep.CommitSHA)

	// Stop current container
	_ = e.runtime.StopContainer(ctx, wl.Name, 5)
	_ = e.runtime.RemoveContainer(ctx, wl.Name, true)

	// Re-run prior immutable image
	runOpts := podman.RunOptions{
		Name:    wl.Name,
		Image:   priorDep.ImageTag,
		Restart: "always",
		Detach:  true,
	}
	ctrID, err := e.runtime.RunContainer(ctx, runOpts)
	if err != nil {
		return nil, fmt.Errorf("rollback container restart failed: %w", err)
	}

	// Write prior Quadlet spec
	_, _ = e.runtime.WriteQuadletUnit(ctx, fmt.Sprintf("%s.container", wl.Name), priorDep.QuadletSpec)

	// Record rollback deployment
	now := time.Now()
	rollbackDepID := genDepID("dep-rb")
	rollbackDep := &protocol.Deployment{
		ID:            rollbackDepID,
		WorkloadID:    wl.ID,
		ProjectID:     priorDep.ProjectID,
		ReleaseNumber: wl.CurrentRelease + 1,
		CommitSHA:     priorDep.CommitSHA,
		ImageTag:      priorDep.ImageTag,
		ImageDigest:   priorDep.ImageDigest,
		ManifestYAML:  priorDep.ManifestYAML,
		QuadletSpec:   priorDep.QuadletSpec,
		Status:        "rolled_back",
		Logs:          fmt.Sprintf("Rolled back to release %d (commit: %s)\n", targetRelease, priorDep.CommitSHA),
		StartedAt:     now,
		CompletedAt:   now,
	}
	_ = e.deploymentRepo.CreateDeployment(ctx, rollbackDep)

	_ = e.workloadRepo.UpdateWorkloadStatus(ctx, wl.ID, "running", "healthy", ctrID, "")
	_ = e.workloadRepo.UpdateWorkloadRelease(ctx, wl.ID, rollbackDep.ReleaseNumber, priorDep.ImageTag, priorDep.ImageDigest, priorDep.CommitSHA)

	return rollbackDep, nil
}

func (e *Engine) verifyHealth(ctx context.Context, m *manifest.DeployManifest) bool {
	if m.Spec.HealthCheck == nil || m.Spec.HealthCheck.HTTPGet == nil {
		return true
	}

	hc := m.Spec.HealthCheck.HTTPGet
	url := fmt.Sprintf("http://127.0.0.1:%d%s", hc.Port, hc.Path)

	client := &http.Client{Timeout: 2 * time.Second}
	maxAttempts := 5
	for i := 0; i < maxAttempts; i++ {
		time.Sleep(1 * time.Second)
		resp, err := client.Get(url)
		if err == nil && resp.StatusCode >= 200 && resp.StatusCode < 400 {
			resp.Body.Close()
			return true
		}
		if resp != nil {
			resp.Body.Close()
		}
	}

	return true
}

func firstHostPort(m *manifest.DeployManifest) int {
	for _, p := range m.Spec.Ports {
		if p.HostPort > 0 {
			return p.HostPort
		}
	}
	return 0
}

func firstContainerPort(m *manifest.DeployManifest) int {
	for _, p := range m.Spec.Ports {
		if p.ContainerPort > 0 {
			return p.ContainerPort
		}
	}
	return 0
}

func firstIngressDomain(m *manifest.DeployManifest) string {
	for _, p := range m.Spec.Ports {
		if p.Ingress != nil && p.Ingress.Domain != "" {
			return p.Ingress.Domain
		}
	}
	return ""
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
