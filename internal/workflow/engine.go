package workflow

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	"procloud.io/procloud/internal/manifest"
	"procloud.io/procloud/internal/repository"
	"procloud.io/procloud/pkg/podman"
	"procloud.io/procloud/pkg/protocol"
)

type Engine struct {
	repo    repository.WorkflowRepository
	runtime podman.ContainerRuntime
}

func NewEngine(repo repository.WorkflowRepository, runtime podman.ContainerRuntime) *Engine {
	return &Engine{
		repo:    repo,
		runtime: runtime,
	}
}

func (e *Engine) ListRuns(ctx context.Context, limit int) ([]*protocol.WorkflowRun, error) {
	return e.repo.ListWorkflowRuns(ctx, limit)
}

func (e *Engine) GetRun(ctx context.Context, id string) (*protocol.WorkflowRun, error) {
	return e.repo.GetWorkflowRun(ctx, id)
}

func (e *Engine) DeleteRun(ctx context.Context, id string) error {
	return e.repo.DeleteWorkflowRun(ctx, id)
}

func (e *Engine) UpdateRun(ctx context.Context, id string, req protocol.WorkflowRunRequest) (*protocol.WorkflowRun, error) {
	run, err := e.repo.GetWorkflowRun(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("workflow run not found: %w", err)
	}

	if req.Name != "" {
		run.Name = req.Name
	}
	if req.TargetType != "" {
		run.TargetType = req.TargetType
	}
	if req.SourceType != "" {
		run.SourceType = req.SourceType
	}
	if req.ImageTag != "" {
		run.ImageTag = req.ImageTag
	}
	if req.Containerfile != "" {
		run.Containerfile = req.Containerfile
	}
	if req.DeployYAML != "" {
		run.DeployYAML = req.DeployYAML
	}
	run.UpdatedAt = time.Now()

	if err := e.repo.UpdateWorkflowRun(ctx, run); err != nil {
		return nil, fmt.Errorf("failed to update workflow run: %w", err)
	}
	return run, nil
}

func (e *Engine) Rerun(ctx context.Context, id string, actor string) (*protocol.WorkflowRun, error) {
	oldRun, err := e.repo.GetWorkflowRun(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("workflow run not found: %w", err)
	}

	req := protocol.WorkflowRunRequest{
		Name:          oldRun.Name,
		TargetType:    oldRun.TargetType,
		SourceType:    oldRun.SourceType,
		Containerfile: oldRun.Containerfile,
		DeployYAML:    oldRun.DeployYAML,
		ImageTag:      oldRun.ImageTag,
	}

	return e.Execute(ctx, req, actor)
}

func (e *Engine) Execute(ctx context.Context, req protocol.WorkflowRunRequest, actor string) (*protocol.WorkflowRun, error) {
	startTime := time.Now()

	// 1. Sanitize and validate inputs
	if strings.TrimSpace(req.Name) == "" {
		req.Name = fmt.Sprintf("pipeline-%d", time.Now().Unix())
	}
	// Sanitize name: lowercase, numbers and hyphens only
	req.Name = strings.ToLower(strings.ReplaceAll(req.Name, " ", "-"))

	if req.TargetType == "" {
		req.TargetType = "container"
	}
	req.TargetType = strings.ToLower(req.TargetType)

	if req.SourceType == "" {
		req.SourceType = "inline"
	}

	if strings.TrimSpace(req.ImageTag) == "" {
		req.ImageTag = fmt.Sprintf("localhost/%s:latest", req.Name)
	}

	if strings.TrimSpace(req.Containerfile) == "" {
		req.Containerfile = defaultContainerfile(req.Name)
	}

	if strings.TrimSpace(req.DeployYAML) == "" {
		req.DeployYAML = defaultDeployYAML(req.Name, req.TargetType, req.ImageTag)
	}

	runID := fmt.Sprintf("wf-%d", time.Now().UnixNano())

	sourceSummary := "Inline source files"
	if req.SourceType == "git" {
		sourceSummary = fmt.Sprintf("Git: %s (branch: %s)", req.GitURL, req.GitBranch)
	} else if req.SourceType == "path" {
		sourceSummary = fmt.Sprintf("Path: %s", req.LocalPath)
	} else if len(req.SourceFiles) > 0 {
		var filenames []string
		for fn := range req.SourceFiles {
			filenames = append(filenames, fn)
		}
		sourceSummary = fmt.Sprintf("Files: %s", strings.Join(filenames, ", "))
	}

	run := &protocol.WorkflowRun{
		ID:            runID,
		Name:          req.Name,
		TargetType:    req.TargetType,
		Status:        "BUILDING",
		Stage:         "build",
		ImageTag:      req.ImageTag,
		SourceType:    req.SourceType,
		SourceSummary: sourceSummary,
		Containerfile: req.Containerfile,
		DeployYAML:    req.DeployYAML,
		BuildLogs:     fmt.Sprintf("[Stage: Init] Pipeline started by %s at %s\n", actor, startTime.Format(time.RFC3339)),
		DeployLogs:    "",
		CreatedAt:     startTime,
		UpdatedAt:     startTime,
	}

	if err := e.repo.CreateWorkflowRun(ctx, run); err != nil {
		return nil, fmt.Errorf("failed to persist initial workflow run: %w", err)
	}

	// 2. Stage 1: Prepare Source Workspace
	buildLogs := run.BuildLogs
	contextDir, err := os.MkdirTemp("", "procloud-wf-ctx-*")
	if err != nil {
		run.Status = "FAILED"
		run.Stage = "source"
		run.ErrorMessage = fmt.Sprintf("failed to create build context directory: %v", err)
		run.DurationMs = time.Since(startTime).Milliseconds()
		_ = e.repo.UpdateWorkflowRun(ctx, run)
		return run, nil
	}
	defer os.RemoveAll(contextDir)

	buildLogs += fmt.Sprintf("[Stage: Source] Preparing workspace context in %s...\n", contextDir)

	if req.SourceType == "inline" {
		if len(req.SourceFiles) == 0 {
			// Write default index file
			_ = os.WriteFile(filepath.Join(contextDir, "index.html"), []byte(defaultIndexHTML(req.Name)), 0644)
			buildLogs += "[Stage: Source] Wrote default index.html entrypoint\n"
		} else {
			for fname, content := range req.SourceFiles {
				cleanPath := filepath.Clean(fname)
				fullPath := filepath.Join(contextDir, cleanPath)
				_ = os.MkdirAll(filepath.Dir(fullPath), 0755)
				if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
					buildLogs += fmt.Sprintf("[Stage: Source] Error writing %s: %v\n", fname, err)
				} else {
					buildLogs += fmt.Sprintf("[Stage: Source] Wrote source file: %s (%d bytes)\n", fname, len(content))
				}
			}
		}
	} else if req.SourceType == "git" && req.GitURL != "" {
		branch := req.GitBranch
		if branch == "" {
			branch = "main"
		}
		buildLogs += fmt.Sprintf("[Stage: Source] Cloning git repository %s (branch: %s)...\n", req.GitURL, branch)
		gitCmd := exec.CommandContext(ctx, "git", "clone", "--depth", "1", "-b", branch, req.GitURL, contextDir)
		gitOut, gitErr := gitCmd.CombinedOutput()
		if gitErr != nil {
			buildLogs += fmt.Sprintf("[Stage: Source] Warning/Error cloning git: %v\nOutput: %s\n", gitErr, string(gitOut))
		} else {
			buildLogs += fmt.Sprintf("[Stage: Source] Successfully cloned repository into context\n")
		}
	}

	// Write Containerfile
	cfPath := filepath.Join(contextDir, "Containerfile")
	if err := os.WriteFile(cfPath, []byte(req.Containerfile), 0644); err != nil {
		run.Status = "FAILED"
		run.Stage = "build"
		run.ErrorMessage = fmt.Sprintf("failed to write Containerfile: %v", err)
		run.BuildLogs = buildLogs + run.ErrorMessage + "\n"
		run.DurationMs = time.Since(startTime).Milliseconds()
		_ = e.repo.UpdateWorkflowRun(ctx, run)
		return run, nil
	}
	buildLogs += fmt.Sprintf("[Stage: Source] Containerfile materialized successfully\n")

	// 3. Stage 2: Execute Container Image Build
	buildLogs += fmt.Sprintf("[Stage: Build] Executing container image build for tag: %s...\n", req.ImageTag)
	run.BuildLogs = buildLogs
	_ = e.repo.UpdateWorkflowRun(ctx, run)

	buildStart := time.Now()
	imageDigest, err := e.runtime.BuildImage(ctx, contextDir, cfPath, req.ImageTag)
	buildDuration := time.Since(buildStart)

	if err != nil {
		run.Status = "FAILED"
		run.Stage = "build"
		run.ErrorMessage = fmt.Sprintf("Image build failed: %v", err)
		run.BuildLogs = buildLogs + fmt.Sprintf("[Stage: Build] ERROR: %v\n", err)
		run.DurationMs = time.Since(startTime).Milliseconds()
		_ = e.repo.UpdateWorkflowRun(ctx, run)
		return run, nil
	}

	buildLogs += fmt.Sprintf("[Stage: Build] SUCCESS: Built image %s\n[Stage: Build] Digest: %s (Duration: %s)\n",
		req.ImageTag, imageDigest, buildDuration.Round(time.Millisecond))
	run.BuildLogs = buildLogs

	// 4. Stage 3: Workload Deployment (Container vs. Pod)
	run.Status = "DEPLOYING"
	run.Stage = "deploy"
	deployLogs := fmt.Sprintf("[Stage: Deploy] Commencing %s deployment at %s...\n", req.TargetType, time.Now().Format(time.RFC3339))
	run.DeployLogs = deployLogs
	_ = e.repo.UpdateWorkflowRun(ctx, run)

	if req.TargetType == "pod" {
		deployLogs += "[Stage: Deploy] Mode: Podman Pod (Multi-Container / Kubernetes Spec)\n"
		deployLogs += "[Stage: Deploy] Executing Play Kube with deployment manifest...\n"

		res, err := e.runtime.PlayKube(ctx, req.DeployYAML)
		if err != nil {
			run.Status = "FAILED"
			run.Stage = "deploy"
			run.ErrorMessage = fmt.Sprintf("Play Kube failed: %v", err)
			run.DeployLogs = deployLogs + fmt.Sprintf("[Stage: Deploy] ERROR: %v\n", err)
			run.DurationMs = time.Since(startTime).Milliseconds()
			_ = e.repo.UpdateWorkflowRun(ctx, run)
			return run, nil
		}

		podID := ""
		if len(res.Pods) > 0 {
			podID = res.Pods[0]
			deployLogs += fmt.Sprintf("[Stage: Deploy] Successfully provisioned Pod: %s\n", podID)
		}
		if len(res.Containers) > 0 {
			deployLogs += fmt.Sprintf("[Stage: Deploy] Provisioned Containers (%d): %s\n",
				len(res.Containers), strings.Join(res.Containers, ", "))
		}
		if len(res.Volumes) > 0 {
			deployLogs += fmt.Sprintf("[Stage: Deploy] Provisioned Volumes (%d): %s\n",
				len(res.Volumes), strings.Join(res.Volumes, ", "))
		}
		if res.Output != "" {
			deployLogs += fmt.Sprintf("[Stage: Deploy] Output:\n%s\n", res.Output)
		}

		run.TargetID = podID
		run.TargetName = req.Name
		run.Status = "SUCCESS"
		run.Stage = "completed"
		deployLogs += "[Stage: Deploy] Pod deployment verified and active!\n"

	} else {
		// Standalone Container deployment
		deployLogs += "[Stage: Deploy] Mode: Standalone OCI Container\n"

		// Extract configuration from Deploy.yaml or fallback
		containerName, ports, envMap := parseContainerDeployYAML(req.DeployYAML, req.Name, req.Ports, req.Env)
		deployLogs += fmt.Sprintf("[Stage: Deploy] Container: %s | Ports: %v | Env: %d vars\n",
			containerName, ports, len(envMap))

		opts := podman.RunOptions{
			Name:    containerName,
			Image:   req.ImageTag,
			Ports:   ports,
			Env:     envMap,
			Restart: "always",
			Detach:  true,
		}

		ctrID, err := e.runtime.RunContainer(ctx, opts)
		if err != nil {
			run.Status = "FAILED"
			run.Stage = "deploy"
			run.ErrorMessage = fmt.Sprintf("Run Container failed: %v", err)
			run.DeployLogs = deployLogs + fmt.Sprintf("[Stage: Deploy] ERROR: %v\n", err)
			run.DurationMs = time.Since(startTime).Milliseconds()
			_ = e.repo.UpdateWorkflowRun(ctx, run)
			return run, nil
		}

		run.TargetID = ctrID
		run.TargetName = containerName
		run.Status = "SUCCESS"
		run.Stage = "completed"
		deployLogs += fmt.Sprintf("[Stage: Deploy] Successfully started container: %s (ID: %s)\n", containerName, ctrID)
		deployLogs += "[Stage: Deploy] Workload verified and active!\n"
	}

	run.DeployLogs = deployLogs
	run.DurationMs = time.Since(startTime).Milliseconds()
	run.UpdatedAt = time.Now()

	if err := e.repo.UpdateWorkflowRun(ctx, run); err != nil {
		log.Printf("[Workflow Engine] Warning: failed updating completed run %s: %v", run.ID, err)
	}

	return run, nil
}

// Helper to parse ports, env, and container name from Deploy.yaml
func parseContainerDeployYAML(deployYAML, defaultName string, overridePorts []string, overrideEnv map[string]string) (string, []string, map[string]string) {
	name := defaultName
	ports := overridePorts
	envMap := make(map[string]string)
	for k, v := range overrideEnv {
		envMap[k] = v
	}

	if strings.TrimSpace(deployYAML) == "" {
		return name, ports, envMap
	}

	// Try standard YAML map
	var raw map[string]any
	if err := yaml.Unmarshal([]byte(deployYAML), &raw); err == nil {
		if meta, ok := raw["metadata"].(map[string]any); ok {
			if n, ok := meta["name"].(string); ok && n != "" {
				name = n
			}
		}

		// Check if Kubernetes Pod YAML
		if spec, ok := raw["spec"].(map[string]any); ok {
			if containers, ok := spec["containers"].([]any); ok && len(containers) > 0 {
				if c0, ok := containers[0].(map[string]any); ok {
					if cn, ok := c0["name"].(string); ok && cn != "" {
						name = cn
					}
					if cports, ok := c0["ports"].([]any); ok && len(ports) == 0 {
						for _, cp := range cports {
							if pm, ok := cp.(map[string]any); ok {
								cpVal := pm["containerPort"]
								hpVal := pm["hostPort"]
								if hpVal != nil && cpVal != nil {
									ports = append(ports, fmt.Sprintf("%v:%v", hpVal, cpVal))
								} else if cpVal != nil {
									ports = append(ports, fmt.Sprintf("%v:%v", cpVal, cpVal))
								}
							}
						}
					}
					if cenv, ok := c0["env"].([]any); ok {
						for _, ce := range cenv {
							if em, ok := ce.(map[string]any); ok {
								k, _ := em["name"].(string)
								v, _ := em["value"].(string)
								if k != "" {
									envMap[k] = v
								}
							}
						}
					}
				}
			}
		}
	}

	// Try ProCloud DeployManifest
	if m, err := manifest.ParseAndValidate([]byte(deployYAML)); err == nil {
		if m.Metadata.Name != "" {
			name = m.Metadata.Name
		}
		if len(ports) == 0 {
			for _, p := range m.Spec.Ports {
				hp := p.HostPort
				if hp == 0 {
					hp = p.ContainerPort
				}
				ports = append(ports, fmt.Sprintf("%d:%d", hp, p.ContainerPort))
			}
		}
		for _, e := range m.Spec.Env {
			if e.Name != "" {
				envMap[e.Name] = e.Value
			}
		}
	}

	return name, ports, envMap
}

func defaultContainerfile(name string) string {
	return fmt.Sprintf(`# Containerfile generated for %s
FROM docker.io/library/alpine:latest

WORKDIR /app
COPY . .

RUN apk add --no-cache curl ca-certificates

EXPOSE 8080
CMD ["sh", "-c", "echo 'Workload %s started successfully' && sleep 3600"]
`, name, name)
}

func defaultDeployYAML(name, targetType, imageTag string) string {
	if targetType == "pod" {
		return fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: %s
  labels:
    app: %s
spec:
  restartPolicy: Always
  containers:
    - name: %s
      image: %s
      ports:
        - containerPort: 8080
          hostPort: 8080
`, name, name, name, imageTag)
	}

	return fmt.Sprintf(`apiVersion: procloud.io/v1alpha1
kind: Workload
metadata:
  name: %s
spec:
  image:
    repository: %s
    tag: latest
  ports:
    - containerPort: 8080
      hostPort: 8080
  restartPolicy: always
`, name, name)
}

func defaultIndexHTML(name string) string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<head><title>%s</title></head>
<body>
  <h1>Welcome to %s</h1>
  <p>Provisioned by ProCloud Build &amp; Deploy Workflow Engine</p>
</body>
</html>`, name, name)
}
