package podman

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// ContainerRuntime defines the interface for container operations.
type ContainerRuntime interface {
	HostInfo(ctx context.Context) (*HostInfo, error)
	BuildImage(ctx context.Context, contextDir, containerfilePath, tag string) (string, error)
	RunContainer(ctx context.Context, opts RunOptions) (string, error)
	InspectContainer(ctx context.Context, nameOrID string) (*ContainerStatus, error)
	StopContainer(ctx context.Context, nameOrID string, timeoutSec int) error
	RemoveContainer(ctx context.Context, nameOrID string, force bool) error
	GetContainerLogs(ctx context.Context, nameOrID string, tailLines int) (string, error)
	CreateNetwork(ctx context.Context, name, subnet, gateway string) error
	RemoveNetwork(ctx context.Context, name string) error
	CreateVolume(ctx context.Context, name string) (string, error)
	RemoveVolume(ctx context.Context, name string) error
	WriteQuadletUnit(ctx context.Context, unitName, content string) (string, error)
	RemoveQuadletUnit(ctx context.Context, unitName string) error
}

type HostInfo struct {
	Hostname      string `json:"hostname"`
	Arch          string `json:"arch"`
	OS            string `json:"os"`
	Kernel        string `json:"kernel"`
	PodmanVersion string `json:"podman_version"`
	TotalCPUs     int    `json:"total_cpus"`
	TotalMemory   int64  `json:"total_memory"`
	NetworkEngine string `json:"network_engine"`
}

type RunOptions struct {
	Name         string
	Image        string
	Network      string
	InternalIP   string
	Ports        []string // "host:container"
	Volumes      []string // "host:container:Z"
	Env          map[string]string
	Command      []string
	Restart      string
	Detach       bool
}

type ContainerStatus struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Image     string    `json:"image"`
	Status    string    `json:"status"` // running, exited, stopped
	Running   bool      `json:"running"`
	ExitCode  int       `json:"exit_code"`
	IPAddress string    `json:"ip_address"`
	StartedAt time.Time `json:"started_at"`
}

// PodmanCLI implements ContainerRuntime by executing the podman command.
type PodmanCLI struct {
	binPath       string
	quadletPath   string
	mu            sync.Mutex
}

// NewPodmanCLI initializes podman CLI runtime wrapper.
func NewPodmanCLI() (*PodmanCLI, error) {
	path, err := exec.LookPath("podman")
	if err != nil {
		return nil, fmt.Errorf("podman binary not found in PATH: %w", err)
	}

	// Quadlet configuration directory
	homeDir, _ := os.UserHomeDir()
	quadletDir := fmt.Sprintf("%s/.config/containers/systemd", homeDir)
	_ = os.MkdirAll(quadletDir, 0755)

	return &PodmanCLI{
		binPath:     path,
		quadletPath: quadletDir,
	}, nil
}

func (p *PodmanCLI) HostInfo(ctx context.Context) (*HostInfo, error) {
	cmd := exec.CommandContext(ctx, p.binPath, "info", "--format", "json")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("podman info failed: %w", err)
	}

	var raw struct {
		Host struct {
			Hostname       string `json:"hostname"`
			Arch           string `json:"arch"`
			OS             string `json:"os"`
			Kernel         string `json:"kernel"`
			CPUs           int    `json:"cpus"`
			MemTotal       int64  `json:"memTotal"`
			NetworkBackend string `json:"networkBackend"`
		} `json:"host"`
		Version struct {
			Version string `json:"Version"`
		} `json:"version"`
	}

	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse podman info: %w", err)
	}

	return &HostInfo{
		Hostname:      raw.Host.Hostname,
		Arch:          raw.Host.Arch,
		OS:            raw.Host.OS,
		Kernel:        raw.Host.Kernel,
		PodmanVersion: raw.Version.Version,
		TotalCPUs:     raw.Host.CPUs,
		TotalMemory:   raw.Host.MemTotal,
		NetworkEngine: raw.Host.NetworkBackend,
	}, nil
}

func (p *PodmanCLI) BuildImage(ctx context.Context, contextDir, containerfilePath, tag string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	args := []string{"build", "-t", tag}
	if containerfilePath != "" {
		args = append(args, "-f", containerfilePath)
	}
	args = append(args, contextDir)

	cmd := exec.CommandContext(ctx, p.binPath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("podman build failed: %w\nOutput:\n%s\nStderr:\n%s", err, stdout.String(), stderr.String())
	}

	// Retrieve image inspect / digest
	inspectCmd := exec.CommandContext(ctx, p.binPath, "image", "inspect", tag, "--format", "{{.Digest}}")
	digestOut, _ := inspectCmd.Output()
	digest := strings.TrimSpace(string(digestOut))
	if digest == "" {
		// Use image ID if digest is empty
		idCmd := exec.CommandContext(ctx, p.binPath, "image", "inspect", tag, "--format", "{{.Id}}")
		idOut, _ := idCmd.Output()
		digest = strings.TrimSpace(string(idOut))
	}

	return digest, nil
}

func (p *PodmanCLI) RunContainer(ctx context.Context, opts RunOptions) (string, error) {
	args := []string{"run"}
	if opts.Detach {
		args = append(args, "-d")
	}
	if opts.Name != "" {
		args = append(args, "--name", opts.Name)
	}
	if opts.Network != "" {
		args = append(args, "--network", opts.Network)
	}
	if opts.InternalIP != "" {
		args = append(args, "--ip", opts.InternalIP)
	}
	for _, port := range opts.Ports {
		args = append(args, "-p", port)
	}
	for _, vol := range opts.Volumes {
		args = append(args, "-v", vol)
	}
	for k, v := range opts.Env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}
	if opts.Restart != "" {
		args = append(args, "--restart", opts.Restart)
	}
	args = append(args, opts.Image)
	args = append(args, opts.Command...)

	cmd := exec.CommandContext(ctx, p.binPath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("podman run failed: %w: %s", err, stderr.String())
	}

	containerID := strings.TrimSpace(stdout.String())
	return containerID, nil
}

func (p *PodmanCLI) InspectContainer(ctx context.Context, nameOrID string) (*ContainerStatus, error) {
	cmd := exec.CommandContext(ctx, p.binPath, "inspect", nameOrID)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("podman inspect failed: %w", err)
	}

	var raw []struct {
		ID    string `json:"Id"`
		Name  string `json:"Name"`
		Image string `json:"ImageName"`
		State struct {
			Status    string `json:"Status"`
			Running   bool   `json:"Running"`
			ExitCode  int    `json:"ExitCode"`
			StartedAt string `json:"StartedAt"`
		} `json:"State"`
		NetworkSettings struct {
			IPAddress string `json:"IPAddress"`
			Networks  map[string]struct {
				IPAddress string `json:"IPAddress"`
			} `json:"Networks"`
		} `json:"NetworkSettings"`
	}

	if err := json.Unmarshal(out, &raw); err != nil || len(raw) == 0 {
		return nil, fmt.Errorf("container not found in inspect output")
	}

	c := raw[0]
	ip := c.NetworkSettings.IPAddress
	if ip == "" {
		for _, net := range c.NetworkSettings.Networks {
			if net.IPAddress != "" {
				ip = net.IPAddress
				break
			}
		}
	}

	started, _ := time.Parse(time.RFC3339Nano, c.State.StartedAt)

	return &ContainerStatus{
		ID:        c.ID,
		Name:      strings.TrimPrefix(c.Name, "/"),
		Image:     c.Image,
		Status:    c.State.Status,
		Running:   c.State.Running,
		ExitCode:  c.State.ExitCode,
		IPAddress: ip,
		StartedAt: started,
	}, nil
}

func (p *PodmanCLI) StopContainer(ctx context.Context, nameOrID string, timeoutSec int) error {
	args := []string{"stop"}
	if timeoutSec > 0 {
		args = append(args, "-t", fmt.Sprintf("%d", timeoutSec))
	}
	args = append(args, nameOrID)
	cmd := exec.CommandContext(ctx, p.binPath, args...)
	return cmd.Run()
}

func (p *PodmanCLI) RemoveContainer(ctx context.Context, nameOrID string, force bool) error {
	args := []string{"rm"}
	if force {
		args = append(args, "-f")
	}
	args = append(args, nameOrID)
	cmd := exec.CommandContext(ctx, p.binPath, args...)
	return cmd.Run()
}

func (p *PodmanCLI) GetContainerLogs(ctx context.Context, nameOrID string, tailLines int) (string, error) {
	args := []string{"logs"}
	if tailLines > 0 {
		args = append(args, "--tail", fmt.Sprintf("%d", tailLines))
	}
	args = append(args, nameOrID)
	cmd := exec.CommandContext(ctx, p.binPath, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (p *PodmanCLI) CreateNetwork(ctx context.Context, name, subnet, gateway string) error {
	args := []string{"network", "create", "--driver", "bridge"}
	if subnet != "" {
		args = append(args, "--subnet", subnet)
	}
	if gateway != "" {
		args = append(args, "--gateway", gateway)
	}
	args = append(args, name)
	cmd := exec.CommandContext(ctx, p.binPath, args...)
	return cmd.Run()
}

func (p *PodmanCLI) RemoveNetwork(ctx context.Context, name string) error {
	cmd := exec.CommandContext(ctx, p.binPath, "network", "rm", "-f", name)
	return cmd.Run()
}

func (p *PodmanCLI) CreateVolume(ctx context.Context, name string) (string, error) {
	cmd := exec.CommandContext(ctx, p.binPath, "volume", "create", name)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (p *PodmanCLI) RemoveVolume(ctx context.Context, name string) error {
	cmd := exec.CommandContext(ctx, p.binPath, "volume", "rm", "-f", name)
	return cmd.Run()
}

func (p *PodmanCLI) WriteQuadletUnit(ctx context.Context, unitName, content string) (string, error) {
	target := fmt.Sprintf("%s/%s", p.quadletPath, unitName)
	if err := os.WriteFile(target, []byte(content), 0644); err != nil {
		return "", err
	}
	// Attempt systemctl daemon-reload if running under systemd
	if _, err := exec.LookPath("systemctl"); err == nil {
		_ = exec.CommandContext(ctx, "systemctl", "--user", "daemon-reload").Run()
	}
	return target, nil
}

func (p *PodmanCLI) RemoveQuadletUnit(ctx context.Context, unitName string) error {
	target := fmt.Sprintf("%s/%s", p.quadletPath, unitName)
	_ = os.Remove(target)
	if _, err := exec.LookPath("systemctl"); err == nil {
		_ = exec.CommandContext(ctx, "systemctl", "--user", "daemon-reload").Run()
	}
	return nil
}
