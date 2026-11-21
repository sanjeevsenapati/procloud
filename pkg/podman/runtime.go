package podman

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ContainerRuntime defines the interface for full container, pod, volume, and image management.
type ContainerRuntime interface {
	HostInfo(ctx context.Context) (*HostInfo, error)

	// Images
	ListImages(ctx context.Context) ([]*ImageSummary, error)
	BuildImage(ctx context.Context, contextDir, containerfilePath, tag string) (string, error)
	PullImage(ctx context.Context, image string) error
	RemoveImage(ctx context.Context, idOrTag string, force bool) error

	// Containers
	ListContainers(ctx context.Context, all bool) ([]*ContainerSummary, error)
	RunContainer(ctx context.Context, opts RunOptions) (string, error)
	InspectContainer(ctx context.Context, nameOrID string) (*ContainerStatus, error)
	StartContainer(ctx context.Context, nameOrID string) error
	StopContainer(ctx context.Context, nameOrID string, timeoutSec int) error
	RestartContainer(ctx context.Context, nameOrID string, timeoutSec int) error
	RemoveContainer(ctx context.Context, nameOrID string, force bool) error
	GetContainerLogs(ctx context.Context, nameOrID string, tailLines int) (string, error)
	ExecContainer(ctx context.Context, nameOrID string, cmd []string) (string, error)

	// Pods
	ListPods(ctx context.Context) ([]*PodSummary, error)
	CreatePod(ctx context.Context, opts PodCreateOptions) (string, error)
	StartPod(ctx context.Context, nameOrID string) error
	StopPod(ctx context.Context, nameOrID string) error
	RestartPod(ctx context.Context, nameOrID string) error
	RemovePod(ctx context.Context, nameOrID string, force bool) error
	InspectPod(ctx context.Context, nameOrID string) (*PodInspect, error)
	GenerateKube(ctx context.Context, nameOrID string) (string, error)

	// Networks & Volumes
	ListNetworks(ctx context.Context) ([]*NetworkSummary, error)
	CreateNetwork(ctx context.Context, name, subnet, gateway string) error
	RemoveNetwork(ctx context.Context, name string) error

	ListVolumes(ctx context.Context) ([]*VolumeSummary, error)
	CreateVolume(ctx context.Context, name string) (string, error)
	RemoveVolume(ctx context.Context, name string) error

	// Quadlet Systemd Units
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
	Name       string            `json:"name"`
	Image      string            `json:"image"`
	Network    string            `json:"network"`
	Pod        string            `json:"pod,omitempty"`
	InternalIP string            `json:"internal_ip,omitempty"`
	Ports      []string          `json:"ports,omitempty"`   // "host:container"
	Volumes    []string          `json:"volumes,omitempty"` // "host:container:Z"
	Env        map[string]string `json:"env,omitempty"`
	Command    []string          `json:"command,omitempty"`
	Restart    string            `json:"restart,omitempty"`
	Detach     bool              `json:"detach"`
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

type ContainerSummary struct {
	ID      string   `json:"id"`
	Names   []string `json:"names"`
	Image   string   `json:"image"`
	State   string   `json:"state"` // running, exited, paused, stopped
	Status  string   `json:"status"`
	Ports   []string `json:"ports"`
	Created string   `json:"created"`
	Pod     string   `json:"pod,omitempty"`
}

type PodSummary struct {
	ID                 string   `json:"id"`
	Name               string   `json:"name"`
	Status             string   `json:"status"` // Running, Degraded, Exited, Created
	Created            string   `json:"created"`
	NumberOfContainers int      `json:"number_of_containers"`
	InfraID            string   `json:"infra_id"`
	Ports              []string `json:"ports"`
}

type PodCreateOptions struct {
	Name    string   `json:"name"`
	Ports   []string `json:"ports"` // "8080:80"
	Network string   `json:"network,omitempty"`
}

type PodInspect struct {
	ID         string              `json:"id"`
	Name       string              `json:"name"`
	State      string              `json:"state"`
	Containers []*ContainerSummary `json:"containers"`
	Ports      []string            `json:"ports"`
}

type ImageSummary struct {
	ID         string `json:"id"`
	Repository string `json:"repository"`
	Tag        string `json:"tag"`
	Size       string `json:"size"`
	Created    string `json:"created"`
}

type VolumeSummary struct {
	Name       string `json:"name"`
	Driver     string `json:"driver"`
	Scope      string `json:"scope"`
	Mountpoint string `json:"mountpoint"`
	Size       string `json:"size,omitempty"`
}

type NetworkSummary struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Driver   string `json:"driver"`
	Subnet   string `json:"subnet"`
	Gateway  string `json:"gateway"`
	Internal bool   `json:"internal"`
}

// PodmanCLI implements ContainerRuntime by executing the podman command.
type PodmanCLI struct {
	binPath     string
	quadletPath string
	mu          sync.Mutex
}

func NewPodmanCLI() (*PodmanCLI, error) {
	path, err := exec.LookPath("podman")
	if err != nil {
		return nil, fmt.Errorf("podman binary not found in PATH: %w", err)
	}

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

// -------------------------------------------------------------
// Image Operations
// -------------------------------------------------------------

func (p *PodmanCLI) ListImages(ctx context.Context) ([]*ImageSummary, error) {
	cmd := exec.CommandContext(ctx, p.binPath, "images", "--format", "json")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var raw []struct {
		Id          string   `json:"Id"`
		Names       []string `json:"Names"`
		Size        int64    `json:"Size"`
		CreatedTime int64    `json:"Created"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, err
	}

	var list []*ImageSummary
	for _, img := range raw {
		repo := "<none>"
		tag := "<none>"
		if len(img.Names) > 0 && img.Names[0] != "" {
			parts := strings.Split(img.Names[0], ":")
			repo = parts[0]
			if len(parts) > 1 {
				tag = parts[1]
			}
		}
		sizeMB := fmt.Sprintf("%.1f MB", float64(img.Size)/(1024*1024))
		createdStr := time.Unix(img.CreatedTime, 0).Format(time.RFC3339)

		idShort := img.Id
		if len(idShort) > 12 {
			idShort = idShort[:12]
		}

		list = append(list, &ImageSummary{
			ID:         idShort,
			Repository: repo,
			Tag:        tag,
			Size:       sizeMB,
			Created:    createdStr,
		})
	}
	return list, nil
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

	inspectCmd := exec.CommandContext(ctx, p.binPath, "image", "inspect", tag, "--format", "{{.Digest}}")
	digestOut, _ := inspectCmd.Output()
	digest := strings.TrimSpace(string(digestOut))
	if digest == "" {
		idCmd := exec.CommandContext(ctx, p.binPath, "image", "inspect", tag, "--format", "{{.Id}}")
		idOut, _ := idCmd.Output()
		digest = strings.TrimSpace(string(idOut))
	}
	return digest, nil
}

func (p *PodmanCLI) PullImage(ctx context.Context, image string) error {
	cmd := exec.CommandContext(ctx, p.binPath, "pull", image)
	return cmd.Run()
}

func (p *PodmanCLI) RemoveImage(ctx context.Context, idOrTag string, force bool) error {
	args := []string{"rmi"}
	if force {
		args = append(args, "-f")
	}
	args = append(args, idOrTag)
	cmd := exec.CommandContext(ctx, p.binPath, args...)
	return cmd.Run()
}

// -------------------------------------------------------------
// Container Operations
// -------------------------------------------------------------

func (p *PodmanCLI) ListContainers(ctx context.Context, all bool) ([]*ContainerSummary, error) {
	args := []string{"ps", "--format", "json"}
	if all {
		args = append(args, "-a")
	}
	cmd := exec.CommandContext(ctx, p.binPath, args...)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var raw []struct {
		Id      string   `json:"Id"`
		Names   []string `json:"Names"`
		Image   string   `json:"Image"`
		State   string   `json:"State"`
		Status  string   `json:"Status"`
		Created int64    `json:"Created"`
		Pod     string   `json:"Pod"`
		Ports   []struct {
			HostPort      int `json:"hostPort"`
			ContainerPort int `json:"containerPort"`
		} `json:"Ports"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, err
	}

	var list []*ContainerSummary
	for _, c := range raw {
		var ports []string
		for _, port := range c.Ports {
			ports = append(ports, fmt.Sprintf("%d:%d", port.HostPort, port.ContainerPort))
		}
		idShort := c.Id
		if len(idShort) > 12 {
			idShort = idShort[:12]
		}
		list = append(list, &ContainerSummary{
			ID:      idShort,
			Names:   c.Names,
			Image:   c.Image,
			State:   c.State,
			Status:  c.Status,
			Ports:   ports,
			Created: time.Unix(c.Created, 0).Format(time.RFC3339),
			Pod:     c.Pod,
		})
	}
	return list, nil
}

func (p *PodmanCLI) RunContainer(ctx context.Context, opts RunOptions) (string, error) {
	args := []string{"run"}
	if opts.Detach {
		args = append(args, "-d")
	}
	if opts.Name != "" {
		args = append(args, "--name", opts.Name)
	}
	if opts.Pod != "" {
		args = append(args, "--pod", opts.Pod)
	} else if opts.Network != "" {
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
	return strings.TrimSpace(stdout.String()), nil
}

func (p *PodmanCLI) StartContainer(ctx context.Context, nameOrID string) error {
	cmd := exec.CommandContext(ctx, p.binPath, "start", nameOrID)
	return cmd.Run()
}

func (p *PodmanCLI) StopContainer(ctx context.Context, nameOrID string, timeoutSec int) error {
	args := []string{"stop"}
	if timeoutSec > 0 {
		args = append(args, "-t", strconv.Itoa(timeoutSec))
	}
	args = append(args, nameOrID)
	cmd := exec.CommandContext(ctx, p.binPath, args...)
	return cmd.Run()
}

func (p *PodmanCLI) RestartContainer(ctx context.Context, nameOrID string, timeoutSec int) error {
	args := []string{"restart"}
	if timeoutSec > 0 {
		args = append(args, "-t", strconv.Itoa(timeoutSec))
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

func (p *PodmanCLI) GetContainerLogs(ctx context.Context, nameOrID string, tailLines int) (string, error) {
	args := []string{"logs"}
	if tailLines > 0 {
		args = append(args, "--tail", strconv.Itoa(tailLines))
	}
	args = append(args, nameOrID)
	cmd := exec.CommandContext(ctx, p.binPath, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (p *PodmanCLI) ExecContainer(ctx context.Context, nameOrID string, cmd []string) (string, error) {
	args := append([]string{"exec", nameOrID}, cmd...)
	execCmd := exec.CommandContext(ctx, p.binPath, args...)
	out, err := execCmd.CombinedOutput()
	return string(out), err
}

// -------------------------------------------------------------
// Pod Operations
// -------------------------------------------------------------

func (p *PodmanCLI) ListPods(ctx context.Context) ([]*PodSummary, error) {
	cmd := exec.CommandContext(ctx, p.binPath, "pod", "ps", "--format", "json")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var raw []struct {
		Id         string `json:"Id"`
		Name       string `json:"Name"`
		Status     string `json:"Status"`
		Created    string `json:"Created"`
		InfraId    string `json:"InfraId"`
		Containers []struct {
			Id    string `json:"Id"`
			Names string `json:"Names"`
			State string `json:"State"`
		} `json:"Containers"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, err
	}

	var list []*PodSummary
	for _, pod := range raw {
		idShort := pod.Id
		if len(idShort) > 12 {
			idShort = idShort[:12]
		}
		list = append(list, &PodSummary{
			ID:                 idShort,
			Name:               pod.Name,
			Status:             pod.Status,
			Created:            pod.Created,
			NumberOfContainers: len(pod.Containers),
			InfraID:            pod.InfraId,
		})
	}
	return list, nil
}

func (p *PodmanCLI) CreatePod(ctx context.Context, opts PodCreateOptions) (string, error) {
	args := []string{"pod", "create"}
	if opts.Name != "" {
		args = append(args, "--name", opts.Name)
	}
	if opts.Network != "" {
		args = append(args, "--network", opts.Network)
	}
	for _, port := range opts.Ports {
		args = append(args, "-p", port)
	}

	cmd := exec.CommandContext(ctx, p.binPath, args...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (p *PodmanCLI) StartPod(ctx context.Context, nameOrID string) error {
	cmd := exec.CommandContext(ctx, p.binPath, "pod", "start", nameOrID)
	return cmd.Run()
}

func (p *PodmanCLI) StopPod(ctx context.Context, nameOrID string) error {
	cmd := exec.CommandContext(ctx, p.binPath, "pod", "stop", nameOrID)
	return cmd.Run()
}

func (p *PodmanCLI) RestartPod(ctx context.Context, nameOrID string) error {
	cmd := exec.CommandContext(ctx, p.binPath, "pod", "restart", nameOrID)
	return cmd.Run()
}

func (p *PodmanCLI) RemovePod(ctx context.Context, nameOrID string, force bool) error {
	args := []string{"pod", "rm"}
	if force {
		args = append(args, "-f")
	}
	args = append(args, nameOrID)
	cmd := exec.CommandContext(ctx, p.binPath, args...)
	return cmd.Run()
}

func (p *PodmanCLI) InspectPod(ctx context.Context, nameOrID string) (*PodInspect, error) {
	cmd := exec.CommandContext(ctx, p.binPath, "pod", "inspect", nameOrID)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var raw struct {
		Id         string `json:"Id"`
		Name       string `json:"Name"`
		State      string `json:"State"`
		Containers []struct {
			Id    string `json:"Id"`
			Name  string `json:"Name"`
			State string `json:"State"`
		} `json:"Containers"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, err
	}

	var ctrs []*ContainerSummary
	for _, c := range raw.Containers {
		ctrs = append(ctrs, &ContainerSummary{
			ID:    c.Id,
			Names: []string{c.Name},
			State: c.State,
		})
	}

	return &PodInspect{
		ID:         raw.Id,
		Name:       raw.Name,
		State:      raw.State,
		Containers: ctrs,
	}, nil
}

func (p *PodmanCLI) GenerateKube(ctx context.Context, nameOrID string) (string, error) {
	cmd := exec.CommandContext(ctx, p.binPath, "generate", "kube", nameOrID)
	out, err := cmd.Output()
	return string(out), err
}

// -------------------------------------------------------------
// Volume Operations
// -------------------------------------------------------------

func (p *PodmanCLI) ListVolumes(ctx context.Context) ([]*VolumeSummary, error) {
	cmd := exec.CommandContext(ctx, p.binPath, "volume", "ls", "--format", "json")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var raw []struct {
		Name       string `json:"Name"`
		Driver     string `json:"Driver"`
		Scope      string `json:"Scope"`
		Mountpoint string `json:"Mountpoint"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, err
	}

	var list []*VolumeSummary
	for _, v := range raw {
		list = append(list, &VolumeSummary{
			Name:       v.Name,
			Driver:     v.Driver,
			Scope:      v.Scope,
			Mountpoint: v.Mountpoint,
		})
	}
	return list, nil
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

// -------------------------------------------------------------
// Network Operations
// -------------------------------------------------------------

func (p *PodmanCLI) ListNetworks(ctx context.Context) ([]*NetworkSummary, error) {
	cmd := exec.CommandContext(ctx, p.binPath, "network", "ls", "--format", "json")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var raw []struct {
		Id          string `json:"Id"`
		Name        string `json:"Name"`
		Driver      string `json:"Driver"`
		Subnets     []struct {
			Subnet  string `json:"Subnet"`
			Gateway string `json:"Gateway"`
		} `json:"subnets"`
		Internal    bool `json:"internal"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, err
	}

	var list []*NetworkSummary
	for _, n := range raw {
		subnet := ""
		gateway := ""
		if len(n.Subnets) > 0 {
			subnet = n.Subnets[0].Subnet
			gateway = n.Subnets[0].Gateway
		}
		idShort := n.Id
		if len(idShort) > 12 {
			idShort = idShort[:12]
		}
		list = append(list, &NetworkSummary{
			ID:       idShort,
			Name:     n.Name,
			Driver:   n.Driver,
			Subnet:   subnet,
			Gateway:  gateway,
			Internal: n.Internal,
		})
	}
	return list, nil
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

// -------------------------------------------------------------
// Quadlet Systemd Units
// -------------------------------------------------------------

func (p *PodmanCLI) WriteQuadletUnit(ctx context.Context, unitName, content string) (string, error) {
	target := fmt.Sprintf("%s/%s", p.quadletPath, unitName)
	if err := os.WriteFile(target, []byte(content), 0644); err != nil {
		return "", err
	}
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
