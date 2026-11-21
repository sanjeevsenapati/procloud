package podman

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// MockRuntime provides an in-memory ContainerRuntime implementation for testing.
type MockRuntime struct {
	mu         sync.Mutex
	Containers map[string]*ContainerStatus
	Pods       map[string]*PodSummary
	Images     map[string]*ImageSummary
	Networks   map[string]*NetworkSummary
	Volumes    map[string]*VolumeSummary
	Units      map[string]string
	Logs       map[string][]string
}

func NewMockRuntime() *MockRuntime {
	m := &MockRuntime{
		Containers: make(map[string]*ContainerStatus),
		Pods:       make(map[string]*PodSummary),
		Images:     make(map[string]*ImageSummary),
		Networks:   make(map[string]*NetworkSummary),
		Volumes:    make(map[string]*VolumeSummary),
		Units:      make(map[string]string),
		Logs:       make(map[string][]string),
	}

	// Pre-populate mock images
	m.Images["alpine:latest"] = &ImageSummary{
		ID:         "alpine123456",
		Repository: "docker.io/library/alpine",
		Tag:        "latest",
		Size:       "5.2 MB",
		Created:    time.Now().Add(-24 * time.Hour).Format(time.RFC3339),
	}
	m.Images["redis:alpine"] = &ImageSummary{
		ID:         "redis1234567",
		Repository: "docker.io/library/redis",
		Tag:        "alpine",
		Size:       "32.4 MB",
		Created:    time.Now().Add(-48 * time.Hour).Format(time.RFC3339),
	}
	return m
}

func (m *MockRuntime) HostInfo(ctx context.Context) (*HostInfo, error) {
	return &HostInfo{
		Hostname:      "mock-host",
		Arch:          "arm64",
		OS:            "linux",
		Kernel:        "6.15.6-mock",
		PodmanVersion: "5.7.0",
		TotalCPUs:     8,
		TotalMemory:   16 * 1024 * 1024 * 1024,
		NetworkEngine: "netavark",
	}, nil
}

// -------------------------------------------------------------
// Images
// -------------------------------------------------------------

func (m *MockRuntime) ListImages(ctx context.Context) ([]*ImageSummary, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var list []*ImageSummary
	for _, img := range m.Images {
		list = append(list, img)
	}
	return list, nil
}

func (m *MockRuntime) BuildImage(ctx context.Context, contextDir, containerfilePath, tag string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	digest := fmt.Sprintf("sha256:%x", time.Now().UnixNano())
	repo := tag
	tagName := "latest"
	if strings.Contains(tag, ":") {
		parts := strings.Split(tag, ":")
		repo = parts[0]
		tagName = parts[1]
	}

	m.Images[tag] = &ImageSummary{
		ID:         digest[:min(12, len(digest))],
		Repository: repo,
		Tag:        tagName,
		Size:       "15.8 MB",
		Created:    time.Now().Format(time.RFC3339),
	}
	return digest, nil
}

func (m *MockRuntime) PullImage(ctx context.Context, image string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.Images[image] = &ImageSummary{
		ID:         fmt.Sprintf("img-%d", time.Now().UnixNano())[:12],
		Repository: image,
		Tag:        "latest",
		Size:       "28.5 MB",
		Created:    time.Now().Format(time.RFC3339),
	}
	return nil
}

func (m *MockRuntime) RemoveImage(ctx context.Context, idOrTag string, force bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.Images, idOrTag)
	return nil
}

// -------------------------------------------------------------
// Containers
// -------------------------------------------------------------

func (m *MockRuntime) ListContainers(ctx context.Context, all bool) ([]*ContainerSummary, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var list []*ContainerSummary
	for _, c := range m.Containers {
		if !all && !c.Running {
			continue
		}
		list = append(list, &ContainerSummary{
			ID:      c.ID,
			Names:   []string{c.Name},
			Image:   c.Image,
			State:   c.Status,
			Status:  fmt.Sprintf("Up %s", c.Status),
			Created: c.StartedAt.Format(time.RFC3339),
		})
	}
	return list, nil
}

func (m *MockRuntime) RunContainer(ctx context.Context, opts RunOptions) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	id := fmt.Sprintf("mock-ctr-%d", time.Now().UnixNano())
	ip := opts.InternalIP
	if ip == "" {
		ip = "10.100.1.5"
	}

	m.Containers[opts.Name] = &ContainerStatus{
		ID:        id,
		Name:      opts.Name,
		Image:     opts.Image,
		Status:    "running",
		Running:   true,
		ExitCode:  0,
		IPAddress: ip,
		StartedAt: time.Now(),
	}
	m.Logs[opts.Name] = []string{fmt.Sprintf("Container %s started successfully", opts.Name)}
	return id, nil
}

func (m *MockRuntime) StartContainer(ctx context.Context, nameOrID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, c := range m.Containers {
		if c.Name == nameOrID || c.ID == nameOrID {
			c.Running = true
			c.Status = "running"
			return nil
		}
	}
	return fmt.Errorf("container %s not found", nameOrID)
}

func (m *MockRuntime) InspectContainer(ctx context.Context, nameOrID string) (*ContainerStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, c := range m.Containers {
		if c.Name == nameOrID || c.ID == nameOrID {
			return c, nil
		}
	}
	return nil, fmt.Errorf("container %s not found", nameOrID)
}

func (m *MockRuntime) StopContainer(ctx context.Context, nameOrID string, timeoutSec int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, c := range m.Containers {
		if c.Name == nameOrID || c.ID == nameOrID {
			c.Running = false
			c.Status = "stopped"
			return nil
		}
	}
	return nil
}

func (m *MockRuntime) RestartContainer(ctx context.Context, nameOrID string, timeoutSec int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, c := range m.Containers {
		if c.Name == nameOrID || c.ID == nameOrID {
			c.Running = true
			c.Status = "running"
			c.StartedAt = time.Now()
			return nil
		}
	}
	return fmt.Errorf("container %s not found", nameOrID)
}

func (m *MockRuntime) RemoveContainer(ctx context.Context, nameOrID string, force bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.Containers, nameOrID)
	return nil
}

func (m *MockRuntime) GetContainerLogs(ctx context.Context, nameOrID string, tailLines int) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	lines := m.Logs[nameOrID]
	if len(lines) == 0 {
		return "Application is running and healthy", nil
	}
	return fmt.Sprintf("%v", lines), nil
}

func (m *MockRuntime) ExecContainer(ctx context.Context, nameOrID string, cmd []string) (string, error) {
	return fmt.Sprintf("Command '%s' executed successfully inside %s\nOutput: OK", strings.Join(cmd, " "), nameOrID), nil
}

func (m *MockRuntime) TopContainer(ctx context.Context, nameOrID string) ([]*ContainerProcess, error) {
	return []*ContainerProcess{
		{
			User:    "root",
			PID:     "101",
			PPID:    "1",
			CPU:     "0.02%",
			Elapsed: "12m",
			TTY:     "?",
			Time:    "0:01",
			Command: "/entrypoint.sh",
		},
		{
			User:    "app",
			PID:     "108",
			PPID:    "101",
			CPU:     "0.15%",
			Elapsed: "12m",
			TTY:     "?",
			Time:    "0:04",
			Command: "procloud-service --port=8080",
		},
	}, nil
}

func (m *MockRuntime) ContainerStats(ctx context.Context, nameOrID string) (*ContainerResourceStats, error) {
	return &ContainerResourceStats{
		ID:         nameOrID,
		Name:       nameOrID,
		CPUTime:    "45.2ms",
		CPUPercent: "0.24%",
		MemUsage:   "24.8MB / 2.0GB",
		MemPercent: "1.24%",
		NetIO:      "1.4kB / 820B",
		BlockIO:    "0B / 0B",
		PIDs:       "4",
	}, nil
}

// -------------------------------------------------------------
// Pods
// -------------------------------------------------------------

func (m *MockRuntime) ListPods(ctx context.Context) ([]*PodSummary, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var list []*PodSummary
	for _, p := range m.Pods {
		list = append(list, p)
	}
	return list, nil
}

func (m *MockRuntime) CreatePod(ctx context.Context, opts PodCreateOptions) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	id := fmt.Sprintf("pod-%d", time.Now().UnixNano())
	m.Pods[opts.Name] = &PodSummary{
		ID:                 id,
		Name:               opts.Name,
		Status:             "Running",
		Created:            time.Now().Format(time.RFC3339),
		NumberOfContainers: 1,
		InfraID:            fmt.Sprintf("infra-%s", id),
		Ports:              opts.Ports,
	}
	return id, nil
}

func (m *MockRuntime) StartPod(ctx context.Context, nameOrID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.Pods[nameOrID]; ok {
		p.Status = "Running"
	}
	return nil
}

func (m *MockRuntime) StopPod(ctx context.Context, nameOrID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.Pods[nameOrID]; ok {
		p.Status = "Exited"
	}
	return nil
}

func (m *MockRuntime) RestartPod(ctx context.Context, nameOrID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.Pods[nameOrID]; ok {
		p.Status = "Running"
	}
	return nil
}

func (m *MockRuntime) RemovePod(ctx context.Context, nameOrID string, force bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.Pods, nameOrID)
	return nil
}

func (m *MockRuntime) InspectPod(ctx context.Context, nameOrID string) (*PodInspect, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	p, ok := m.Pods[nameOrID]
	if !ok {
		return nil, fmt.Errorf("pod %s not found", nameOrID)
	}
	return &PodInspect{
		ID:    p.ID,
		Name:  p.Name,
		State: p.Status,
		Ports: p.Ports,
	}, nil
}

func (m *MockRuntime) GenerateKube(ctx context.Context, nameOrID string) (string, error) {
	yaml := fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: %s
spec:
  containers:
  - name: app
    image: localhost/%s:latest
`, nameOrID, nameOrID)
	return yaml, nil
}

// -------------------------------------------------------------
// Networks & Volumes
// -------------------------------------------------------------

func (m *MockRuntime) ListNetworks(ctx context.Context) ([]*NetworkSummary, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var list []*NetworkSummary
	for _, n := range m.Networks {
		list = append(list, n)
	}
	return list, nil
}

func (m *MockRuntime) CreateNetwork(ctx context.Context, name, subnet, gateway string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.Networks[name] = &NetworkSummary{
		ID:      fmt.Sprintf("net-%d", time.Now().UnixNano())[:12],
		Name:    name,
		Driver:  "bridge",
		Subnet:  subnet,
		Gateway: gateway,
	}
	return nil
}

func (m *MockRuntime) RemoveNetwork(ctx context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.Networks, name)
	return nil
}

func (m *MockRuntime) ListVolumes(ctx context.Context) ([]*VolumeSummary, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var list []*VolumeSummary
	for _, v := range m.Volumes {
		list = append(list, v)
	}
	return list, nil
}

func (m *MockRuntime) CreateVolume(ctx context.Context, name string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.Volumes[name] = &VolumeSummary{
		Name:       name,
		Driver:     "local",
		Scope:      "local",
		Mountpoint: fmt.Sprintf("/var/lib/procloud/volumes/%s", name),
		Size:       "100 MB",
	}
	return m.Volumes[name].Mountpoint, nil
}

func (m *MockRuntime) RemoveVolume(ctx context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.Volumes, name)
	return nil
}

func (m *MockRuntime) WriteQuadletUnit(ctx context.Context, unitName, content string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Units[unitName] = content
	return fmt.Sprintf("/etc/containers/systemd/%s", unitName), nil
}

func (m *MockRuntime) RemoveQuadletUnit(ctx context.Context, unitName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.Units, unitName)
	return nil
}
