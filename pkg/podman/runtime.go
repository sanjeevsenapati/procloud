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
	TopContainer(ctx context.Context, nameOrID string) ([]*ContainerProcess, error)
	ContainerStats(ctx context.Context, nameOrID string) (*ContainerResourceStats, error)

	// Pods
	ListPods(ctx context.Context) ([]*PodSummary, error)
	CreatePod(ctx context.Context, opts PodCreateOptions) (string, error)
	StartPod(ctx context.Context, nameOrID string) error
	StopPod(ctx context.Context, nameOrID string) error
	RestartPod(ctx context.Context, nameOrID string) error
	RemovePod(ctx context.Context, nameOrID string, force bool) error
	InspectPod(ctx context.Context, nameOrID string) (*PodInspect, error)
	GenerateKube(ctx context.Context, nameOrID string) (string, error)
	PlayKube(ctx context.Context, kubeYAML string) (*PlayKubeResult, error)
	TopPod(ctx context.Context, nameOrID string) ([]*ContainerProcess, error)
	GetPodLogs(ctx context.Context, nameOrID string, container string, tailLines int) (string, error)

	// Networks & Volumes
	ListNetworks(ctx context.Context) ([]*NetworkSummary, error)
	CreateNetwork(ctx context.Context, name, subnet, gateway string) error
	RemoveNetwork(ctx context.Context, name string) error

	ListVolumes(ctx context.Context) ([]*VolumeSummary, error)
	CreateVolume(ctx context.Context, name string) (string, error)
	RemoveVolume(ctx context.Context, name string) error

	// Secrets (Podman Desktop Secrets Engine)
	ListSecrets(ctx context.Context) ([]*SecretSummary, error)
	CreateSecret(ctx context.Context, opts SecretCreateOptions) (string, error)
	InspectSecret(ctx context.Context, nameOrID string, showSecret bool) (*SecretInspect, error)
	RemoveSecret(ctx context.Context, nameOrID string) error

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
	Secrets    []string          `json:"secrets,omitempty"` // Podman secrets attached: "name" or "name,type=env,target=VAR"
	Labels     map[string]string `json:"labels,omitempty"`  // Metadata labels: e.g. procloud.io/organization
	Command    []string          `json:"command,omitempty"`
	Restart    string            `json:"restart,omitempty"`
	Detach     bool              `json:"detach"`
}

type SecretSummary struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Driver    string            `json:"driver"`
	CreatedAt string            `json:"created_at"`
	UpdatedAt string            `json:"updated_at"`
	Labels    map[string]string `json:"labels,omitempty"`
}

type SecretCreateOptions struct {
	Name   string            `json:"name"`
	Driver string            `json:"driver,omitempty"` // default "file"
	Data   string            `json:"data"`
	Labels map[string]string `json:"labels,omitempty"`
}

type SecretInspect struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Driver     string            `json:"driver"`
	CreatedAt  string            `json:"created_at"`
	UpdatedAt  string            `json:"updated_at"`
	Labels     map[string]string `json:"labels,omitempty"`
	SecretData string            `json:"secret_data,omitempty"`
	Raw        any               `json:"raw,omitempty"`
}

type PlayKubeResult struct {
	Pods       []string `json:"pods"`
	Containers []string `json:"containers"`
	Volumes    []string `json:"volumes"`
	Output     string   `json:"output"`
}

type ContainerMountInfo struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Mode        string `json:"mode,omitempty"`
	Type        string `json:"type,omitempty"`
}

type ContainerStatus struct {
	ID            string               `json:"id"`
	Name          string               `json:"name"`
	Image         string               `json:"image"`
	ImageID       string               `json:"image_id,omitempty"`
	Status        string               `json:"status"` // running, exited, stopped
	Running       bool                 `json:"running"`
	ExitCode      int                  `json:"exit_code"`
	IPAddress     string               `json:"ip_address"`
	NetworkName   string               `json:"network_name,omitempty"`
	Gateway       string               `json:"gateway,omitempty"`
	Ports         []string             `json:"ports,omitempty"`
	StartedAt     time.Time            `json:"started_at"`
	FinishedAt    time.Time            `json:"finished_at,omitempty"`
	Command       []string             `json:"command,omitempty"`
	Entrypoint    []string             `json:"entrypoint,omitempty"`
	Env           []string             `json:"env,omitempty"`
	WorkingDir    string               `json:"working_dir,omitempty"`
	User          string               `json:"user,omitempty"`
	RestartPolicy string               `json:"restart_policy,omitempty"`
	Mounts        []ContainerMountInfo `json:"mounts,omitempty"`
	Labels        map[string]string    `json:"labels,omitempty"`
	Pod           string               `json:"pod,omitempty"`
	RawInspect    string               `json:"raw_inspect,omitempty"`
}

type ContainerSummary struct {
	ID      string            `json:"id"`
	Names   []string          `json:"names"`
	Image   string            `json:"image"`
	State   string            `json:"state"` // running, exited, paused, stopped
	Status  string            `json:"status"`
	Ports   []string          `json:"ports"`
	Created string            `json:"created"`
	Pod     string            `json:"pod,omitempty"`
	Labels  map[string]string `json:"labels,omitempty"`
}

type PodSummary struct {
	ID                 string            `json:"id"`
	Name               string            `json:"name"`
	Status             string            `json:"status"` // Running, Degraded, Exited, Created
	Created            string            `json:"created"`
	NumberOfContainers int               `json:"number_of_containers"`
	InfraID            string            `json:"infra_id"`
	Ports              []string          `json:"ports"`
	Labels             map[string]string `json:"labels,omitempty"`
}

type PodCreateOptions struct {
	Name    string            `json:"name"`
	Ports   []string          `json:"ports"` // "8080:80"
	Network string            `json:"network,omitempty"`
	Labels  map[string]string `json:"labels,omitempty"`
}

type PodInspect struct {
	ID               string              `json:"id"`
	Name             string              `json:"name"`
	State            string              `json:"state"`
	Created          string              `json:"created,omitempty"`
	Containers       []*ContainerSummary `json:"containers"`
	Ports            []string            `json:"ports,omitempty"`
	CgroupParent     string              `json:"cgroup_parent,omitempty"`
	InfraContainerID string              `json:"infra_container_id,omitempty"`
	SharedNamespaces []string            `json:"shared_namespaces,omitempty"`
	NumContainers    int                 `json:"num_containers,omitempty"`
	Raw              string              `json:"raw,omitempty"`
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
		Id      string            `json:"Id"`
		Names   []string          `json:"Names"`
		Image   string            `json:"Image"`
		State   string            `json:"State"`
		Status  string            `json:"Status"`
		Created int64             `json:"Created"`
		Pod     string            `json:"Pod"`
		Labels  map[string]string `json:"Labels"`
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
			Labels:  c.Labels,
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
	for _, sec := range opts.Secrets {
		if sec != "" {
			args = append(args, "--secret", sec)
		}
	}
	for k, v := range opts.Labels {
		if k != "" {
			args = append(args, "--label", fmt.Sprintf("%s=%s", k, v))
		}
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
		ID      string `json:"Id"`
		Name    string `json:"Name"`
		Image   string `json:"ImageName"`
		ImageID string `json:"Image"`
		Pod     string `json:"Pod"`
		State   struct {
			Status     string `json:"Status"`
			Running    bool   `json:"Running"`
			ExitCode   int    `json:"ExitCode"`
			StartedAt  string `json:"StartedAt"`
			FinishedAt string `json:"FinishedAt"`
		} `json:"State"`
		Config struct {
			Cmd        []string          `json:"Cmd"`
			Entrypoint []string          `json:"Entrypoint"`
			Env        []string          `json:"Env"`
			WorkingDir string            `json:"WorkingDir"`
			User       string            `json:"User"`
			Labels     map[string]string `json:"Labels"`
		} `json:"Config"`
		HostConfig struct {
			RestartPolicy struct {
				Name string `json:"Name"`
			} `json:"RestartPolicy"`
			PortBindings map[string][]struct {
				HostPort string `json:"HostPort"`
			} `json:"PortBindings"`
		} `json:"HostConfig"`
		Mounts []struct {
			Source      string `json:"Source"`
			Destination string `json:"Destination"`
			Mode        string `json:"Mode"`
			Type        string `json:"Type"`
		} `json:"Mounts"`
		NetworkSettings struct {
			IPAddress string `json:"IPAddress"`
			Gateway   string `json:"Gateway"`
			Networks  map[string]struct {
				IPAddress string `json:"IPAddress"`
				Gateway   string `json:"Gateway"`
			} `json:"Networks"`
			Ports map[string][]struct {
				HostPort string `json:"HostPort"`
			} `json:"Ports"`
		} `json:"NetworkSettings"`
	}

	if err := json.Unmarshal(out, &raw); err != nil || len(raw) == 0 {
		return nil, fmt.Errorf("container not found in inspect output")
	}

	c := raw[0]
	ip := c.NetworkSettings.IPAddress
	gateway := c.NetworkSettings.Gateway
	netName := "bridge"
	if ip == "" {
		for nName, net := range c.NetworkSettings.Networks {
			netName = nName
			if net.IPAddress != "" {
				ip = net.IPAddress
				if net.Gateway != "" {
					gateway = net.Gateway
				}
				break
			}
		}
	}

	var ports []string
	if c.HostConfig.PortBindings != nil {
		for cPort, bindings := range c.HostConfig.PortBindings {
			for _, b := range bindings {
				if b.HostPort != "" {
					ports = append(ports, fmt.Sprintf("%s:%s", b.HostPort, cPort))
				}
			}
		}
	}
	if len(ports) == 0 && c.NetworkSettings.Ports != nil {
		for cPort, bindings := range c.NetworkSettings.Ports {
			for _, b := range bindings {
				if b.HostPort != "" {
					ports = append(ports, fmt.Sprintf("%s:%s", b.HostPort, cPort))
				}
			}
		}
	}

	var mounts []ContainerMountInfo
	for _, m := range c.Mounts {
		mounts = append(mounts, ContainerMountInfo{
			Source:      m.Source,
			Destination: m.Destination,
			Mode:        m.Mode,
			Type:        m.Type,
		})
	}

	started, _ := time.Parse(time.RFC3339Nano, c.State.StartedAt)
	finished, _ := time.Parse(time.RFC3339Nano, c.State.FinishedAt)

	return &ContainerStatus{
		ID:            c.ID,
		Name:          strings.TrimPrefix(c.Name, "/"),
		Image:         c.Image,
		ImageID:       c.ImageID,
		Status:        c.State.Status,
		Running:       c.State.Running,
		ExitCode:      c.State.ExitCode,
		IPAddress:     ip,
		NetworkName:   netName,
		Gateway:       gateway,
		Ports:         ports,
		StartedAt:     started,
		FinishedAt:    finished,
		Command:       c.Config.Cmd,
		Entrypoint:    c.Config.Entrypoint,
		Env:           c.Config.Env,
		WorkingDir:    c.Config.WorkingDir,
		User:          c.Config.User,
		RestartPolicy: c.HostConfig.RestartPolicy.Name,
		Mounts:        mounts,
		Labels:        c.Config.Labels,
		Pod:           c.Pod,
		RawInspect:    string(out),
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

type ContainerProcess struct {
	User    string `json:"user"`
	PID     string `json:"pid"`
	PPID    string `json:"ppid"`
	CPU     string `json:"cpu"`
	Elapsed string `json:"elapsed"`
	TTY     string `json:"tty"`
	Time    string `json:"time"`
	Command string `json:"command"`
}

type ContainerResourceStats struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	CPUTime    string `json:"cpu_time"`
	CPUPercent string `json:"cpu_percent"`
	MemUsage   string `json:"mem_usage"`
	MemPercent string `json:"mem_percent"`
	NetIO      string `json:"net_io"`
	BlockIO    string `json:"block_io"`
	PIDs       string `json:"pids"`
}

func (p *PodmanCLI) TopContainer(ctx context.Context, nameOrID string) ([]*ContainerProcess, error) {
	cmd := exec.CommandContext(ctx, p.binPath, "top", nameOrID)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("podman top failed: %w: %s", err, string(out))
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) <= 1 {
		return []*ContainerProcess{}, nil
	}

	var procs []*ContainerProcess
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) >= 8 {
			procs = append(procs, &ContainerProcess{
				User:    fields[0],
				PID:     fields[1],
				PPID:    fields[2],
				CPU:     fields[3],
				Elapsed: fields[4],
				TTY:     fields[5],
				Time:    fields[6],
				Command: strings.Join(fields[7:], " "),
			})
		} else if len(fields) > 0 {
			procs = append(procs, &ContainerProcess{
				User:    fields[0],
				Command: strings.Join(fields[1:], " "),
			})
		}
	}
	return procs, nil
}

func (p *PodmanCLI) ContainerStats(ctx context.Context, nameOrID string) (*ContainerResourceStats, error) {
	cmd := exec.CommandContext(ctx, p.binPath, "stats", "--no-stream", "--format", "json", nameOrID)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("podman stats failed: %w: %s", err, string(out))
	}

	var statsList []struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		CPUTime    string `json:"cpu_time"`
		CPUPercent string `json:"cpu_percent"`
		MemUsage   string `json:"mem_usage"`
		MemPercent string `json:"mem_percent"`
		NetIO      string `json:"net_io"`
		BlockIO    string `json:"block_io"`
		PIDs       string `json:"pids"`
	}

	if err := json.Unmarshal(out, &statsList); err != nil {
		return nil, fmt.Errorf("failed parsing stats json: %w", err)
	}

	if len(statsList) == 0 {
		return nil, fmt.Errorf("no stats available for container %s", nameOrID)
	}

	s := statsList[0]
	return &ContainerResourceStats{
		ID:         s.ID,
		Name:       s.Name,
		CPUTime:    s.CPUTime,
		CPUPercent: s.CPUPercent,
		MemUsage:   s.MemUsage,
		MemPercent: s.MemPercent,
		NetIO:      s.NetIO,
		BlockIO:    s.BlockIO,
		PIDs:       s.PIDs,
	}, nil
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
		Id         string            `json:"Id"`
		Name       string            `json:"Name"`
		Status     string            `json:"Status"`
		Created    string            `json:"Created"`
		InfraId    string            `json:"InfraId"`
		Labels     map[string]string `json:"Labels"`
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
			Labels:             pod.Labels,
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
	for k, v := range opts.Labels {
		if k != "" {
			args = append(args, "--label", fmt.Sprintf("%s=%s", k, v))
		}
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

	type rawPod struct {
		Id               string   `json:"Id"`
		Name             string   `json:"Name"`
		Created          string   `json:"Created"`
		State            string   `json:"State"`
		Hostname         string   `json:"Hostname"`
		CgroupParent     string   `json:"CgroupParent"`
		CgroupPath       string   `json:"CgroupPath"`
		InfraContainerID string   `json:"InfraContainerID"`
		SharedNamespaces []string `json:"SharedNamespaces"`
		NumContainers    int      `json:"NumContainers"`
		InfraConfig      struct {
			PortBindings map[string][]struct {
				HostIp   string `json:"HostIp"`
				HostPort string `json:"HostPort"`
			} `json:"PortBindings"`
		} `json:"InfraConfig"`
		Containers []struct {
			Id    string `json:"Id"`
			Name  string `json:"Name"`
			State string `json:"State"`
		} `json:"Containers"`
	}

	var raw rawPod
	trimmed := bytes.TrimSpace(out)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var slice []rawPod
		if err := json.Unmarshal(out, &slice); err != nil {
			return nil, err
		}
		if len(slice) == 0 {
			return nil, fmt.Errorf("pod not found")
		}
		raw = slice[0]
	} else {
		if err := json.Unmarshal(out, &raw); err != nil {
			return nil, err
		}
	}

	var ctrs []*ContainerSummary
	for _, c := range raw.Containers {
		ctrs = append(ctrs, &ContainerSummary{
			ID:    c.Id,
			Names: []string{c.Name},
			State: c.State,
		})
	}

	var ports []string
	for containerPort, bindings := range raw.InfraConfig.PortBindings {
		for _, b := range bindings {
			ports = append(ports, fmt.Sprintf("%s:%s", b.HostPort, containerPort))
		}
	}

	return &PodInspect{
		ID:               raw.Id,
		Name:             raw.Name,
		State:            raw.State,
		Created:          raw.Created,
		Containers:       ctrs,
		Ports:            ports,
		CgroupParent:     raw.CgroupParent,
		InfraContainerID: raw.InfraContainerID,
		SharedNamespaces: raw.SharedNamespaces,
		NumContainers:    raw.NumContainers,
		Raw:              string(out),
	}, nil
}

func (p *PodmanCLI) TopPod(ctx context.Context, nameOrID string) ([]*ContainerProcess, error) {
	cmd := exec.CommandContext(ctx, p.binPath, "pod", "top", nameOrID)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("podman pod top failed: %w: %s", err, string(out))
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) <= 1 {
		return []*ContainerProcess{}, nil
	}

	var procs []*ContainerProcess
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) >= 8 {
			procs = append(procs, &ContainerProcess{
				User:    fields[0],
				PID:     fields[1],
				PPID:    fields[2],
				CPU:     fields[3],
				Elapsed: fields[4],
				TTY:     fields[5],
				Time:    fields[6],
				Command: strings.Join(fields[7:], " "),
			})
		}
	}
	return procs, nil
}

func (p *PodmanCLI) GetPodLogs(ctx context.Context, nameOrID string, container string, tailLines int) (string, error) {
	if container == "" || container == "all" {
		pod, err := p.InspectPod(ctx, nameOrID)
		if err != nil {
			return "", fmt.Errorf("failed to inspect pod for logs: %w", err)
		}

		var workloadContainers []string
		for _, c := range pod.Containers {
			isInfra := (pod.InfraContainerID != "" && strings.HasPrefix(c.ID, pod.InfraContainerID)) ||
				(len(c.Names) > 0 && strings.HasSuffix(c.Names[0], "-infra"))
			if !isInfra {
				cName := c.ID
				if len(c.Names) > 0 && c.Names[0] != "" {
					cName = c.Names[0]
				}
				workloadContainers = append(workloadContainers, cName)
			}
		}

		if len(workloadContainers) == 0 {
			return "No non-infra workload containers found in this pod.", nil
		}

		if len(workloadContainers) == 1 {
			return p.getSingleContainerPodLogs(ctx, nameOrID, workloadContainers[0], tailLines)
		}

		var b strings.Builder
		for idx, cName := range workloadContainers {
			if idx > 0 {
				b.WriteString("\n")
			}
			b.WriteString(fmt.Sprintf("===> Container [%s] <===\n", cName))
			logs, err := p.getSingleContainerPodLogs(ctx, nameOrID, cName, tailLines)
			if err != nil {
				b.WriteString(fmt.Sprintf("Error retrieving logs: %v\n", err))
			} else if strings.TrimSpace(logs) == "" {
				b.WriteString("(no log output)\n")
			} else {
				b.WriteString(logs)
				if !strings.HasSuffix(logs, "\n") {
					b.WriteString("\n")
				}
			}
		}
		return b.String(), nil
	}

	return p.getSingleContainerPodLogs(ctx, nameOrID, container, tailLines)
}

func (p *PodmanCLI) getSingleContainerPodLogs(ctx context.Context, nameOrID string, container string, tailLines int) (string, error) {
	args := []string{"pod", "logs"}
	if tailLines > 0 {
		args = append(args, "--tail", strconv.Itoa(tailLines))
	}
	if container != "" {
		args = append(args, "-c", container)
	}
	args = append(args, nameOrID)
	cmd := exec.CommandContext(ctx, p.binPath, args...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), nil
	}

	// Fallback directly to `podman logs <container>` if `podman pod logs -c` fails
	fallbackArgs := []string{"logs"}
	if tailLines > 0 {
		fallbackArgs = append(fallbackArgs, "--tail", strconv.Itoa(tailLines))
	}
	fallbackArgs = append(fallbackArgs, container)
	fallbackCmd := exec.CommandContext(ctx, p.binPath, fallbackArgs...)
	fbOut, fbErr := fallbackCmd.CombinedOutput()
	if fbErr == nil {
		return string(fbOut), nil
	}

	outStr := strings.TrimSpace(string(out))
	if outStr != "" {
		return "", fmt.Errorf("%s", outStr)
	}
	fbStr := strings.TrimSpace(string(fbOut))
	if fbStr != "" {
		return "", fmt.Errorf("%s", fbStr)
	}
	return "", err
}

func (p *PodmanCLI) GenerateKube(ctx context.Context, nameOrID string) (string, error) {
	cmd := exec.CommandContext(ctx, p.binPath, "generate", "kube", nameOrID)
	out, err := cmd.Output()
	return string(out), err
}

func (p *PodmanCLI) PlayKube(ctx context.Context, kubeYAML string) (*PlayKubeResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	tmpFile, err := os.CreateTemp("", "kube-*.yaml")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %w", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.WriteString(kubeYAML); err != nil {
		tmpFile.Close()
		return nil, fmt.Errorf("failed to write kube yaml: %w", err)
	}
	tmpFile.Close()

	cmd := exec.CommandContext(ctx, p.binPath, "play", "kube", tmpFile.Name())
	out, err := cmd.CombinedOutput()
	outputStr := string(out)
	if err != nil {
		return nil, fmt.Errorf("podman play kube failed: %w\nOutput: %s", err, outputStr)
	}

	result := &PlayKubeResult{
		Output: outputStr,
	}
	lines := strings.Split(outputStr, "\n")
	var currentSection string
	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "Pod:") {
			val := strings.TrimSpace(strings.TrimPrefix(line, "Pod:"))
			if val != "" {
				result.Pods = append(result.Pods, val)
				currentSection = ""
			} else {
				currentSection = "pod"
			}
		} else if strings.HasPrefix(line, "Container:") {
			val := strings.TrimSpace(strings.TrimPrefix(line, "Container:"))
			if val != "" {
				result.Containers = append(result.Containers, val)
				currentSection = ""
			} else {
				currentSection = "container"
			}
		} else if strings.HasPrefix(line, "Volume:") {
			val := strings.TrimSpace(strings.TrimPrefix(line, "Volume:"))
			if val != "" {
				result.Volumes = append(result.Volumes, val)
				currentSection = ""
			} else {
				currentSection = "volume"
			}
		} else if currentSection == "pod" {
			result.Pods = append(result.Pods, line)
			currentSection = ""
		} else if currentSection == "container" {
			result.Containers = append(result.Containers, line)
			currentSection = ""
		} else if currentSection == "volume" {
			result.Volumes = append(result.Volumes, line)
			currentSection = ""
		}
	}
	return result, nil
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

// -------------------------------------------------------------
// Podman Desktop Secrets Management Engine
// -------------------------------------------------------------

func (p *PodmanCLI) ListSecrets(ctx context.Context) ([]*SecretSummary, error) {
	cmd := exec.CommandContext(ctx, p.binPath, "secret", "ls", "--format", "{{json .}}")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var list []*SecretSummary
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var raw struct {
			ID        string `json:"ID"`
			Name      string `json:"Name"`
			Driver    string `json:"Driver"`
			CreatedAt string `json:"CreatedAt"`
			UpdatedAt string `json:"UpdatedAt"`
		}
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}
		idShort := raw.ID
		if len(idShort) > 12 {
			idShort = idShort[:12]
		}
		list = append(list, &SecretSummary{
			ID:        idShort,
			Name:      raw.Name,
			Driver:    raw.Driver,
			CreatedAt: raw.CreatedAt,
			UpdatedAt: raw.UpdatedAt,
		})
	}
	return list, nil
}

func (p *PodmanCLI) CreateSecret(ctx context.Context, opts SecretCreateOptions) (string, error) {
	if opts.Name == "" {
		return "", fmt.Errorf("secret name is required")
	}
	args := []string{"secret", "create"}
	if opts.Driver != "" {
		args = append(args, "--driver", opts.Driver)
	}
	for k, v := range opts.Labels {
		args = append(args, "--label", fmt.Sprintf("%s=%s", k, v))
	}
	args = append(args, opts.Name, "-")
	cmd := exec.CommandContext(ctx, p.binPath, args...)
	cmd.Stdin = strings.NewReader(opts.Data)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("podman secret create failed: %w: %s", err, stderr.String())
	}
	return strings.TrimSpace(stdout.String()), nil
}

func (p *PodmanCLI) InspectSecret(ctx context.Context, nameOrID string, showSecret bool) (*SecretInspect, error) {
	args := []string{"secret", "inspect"}
	if showSecret {
		args = append(args, "--showsecret")
	}
	args = append(args, "--format", "{{json .}}", nameOrID)
	cmd := exec.CommandContext(ctx, p.binPath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("podman secret inspect failed: %w: %s", err, stderr.String())
	}

	var raw struct {
		ID        string `json:"ID"`
		CreatedAt string `json:"CreatedAt"`
		UpdatedAt string `json:"UpdatedAt"`
		Spec      struct {
			Name   string `json:"Name"`
			Driver struct {
				Name    string            `json:"Name"`
				Options map[string]string `json:"Options"`
			} `json:"Driver"`
			Labels map[string]string `json:"Labels"`
		} `json:"Spec"`
		SecretData string `json:"SecretData"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &raw); err != nil {
		return nil, fmt.Errorf("failed to parse secret inspect json: %w", err)
	}

	driver := raw.Spec.Driver.Name
	if driver == "" {
		driver = "file"
	}
	name := raw.Spec.Name
	if name == "" {
		name = nameOrID
	}

	return &SecretInspect{
		ID:         raw.ID,
		Name:       name,
		Driver:     driver,
		CreatedAt:  raw.CreatedAt,
		UpdatedAt:  raw.UpdatedAt,
		Labels:     raw.Spec.Labels,
		SecretData: raw.SecretData,
		Raw:        raw,
	}, nil
}

func (p *PodmanCLI) RemoveSecret(ctx context.Context, nameOrID string) error {
	cmd := exec.CommandContext(ctx, p.binPath, "secret", "rm", nameOrID)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("podman secret rm failed: %w: %s", err, stderr.String())
	}
	return nil
}
