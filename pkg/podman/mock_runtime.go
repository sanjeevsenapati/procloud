package podman

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// MockRuntime provides an in-memory ContainerRuntime implementation for testing.
type MockRuntime struct {
	mu         sync.Mutex
	Containers map[string]*ContainerStatus
	Networks   map[string]string
	Volumes    map[string]string
	Units      map[string]string
	Logs       map[string][]string
}

func NewMockRuntime() *MockRuntime {
	return &MockRuntime{
		Containers: make(map[string]*ContainerStatus),
		Networks:   make(map[string]string),
		Volumes:    make(map[string]string),
		Units:      make(map[string]string),
		Logs:       make(map[string][]string),
	}
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

func (m *MockRuntime) BuildImage(ctx context.Context, contextDir, containerfilePath, tag string) (string, error) {
	digest := fmt.Sprintf("sha256:%x", time.Now().UnixNano())
	return digest, nil
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

func (m *MockRuntime) CreateNetwork(ctx context.Context, name, subnet, gateway string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Networks[name] = subnet
	return nil
}

func (m *MockRuntime) RemoveNetwork(ctx context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.Networks, name)
	return nil
}

func (m *MockRuntime) CreateVolume(ctx context.Context, name string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Volumes[name] = fmt.Sprintf("/var/lib/procloud/volumes/%s", name)
	return m.Volumes[name], nil
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
