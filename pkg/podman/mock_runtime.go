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
	Secrets    map[string]*SecretInspect
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
		Secrets:    make(map[string]*SecretInspect),
		Units:      make(map[string]string),
		Logs:       make(map[string][]string),
	}

	// Pre-populate mock secret
	m.Secrets["db_password"] = &SecretInspect{
		ID:         "sec-mock-123456",
		Name:       "db_password",
		Driver:     "file",
		CreatedAt:  time.Now().Add(-2 * time.Hour).Format(time.RFC3339),
		UpdatedAt:  time.Now().Add(-2 * time.Hour).Format(time.RFC3339),
		SecretData: "postgres_super_secret_password",
		Labels:     map[string]string{"env": "production", "service": "database"},
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
	for k, v := range m.Images {
		if v.ID == idOrTag || fmt.Sprintf("%s:%s", v.Repository, v.Tag) == idOrTag {
			delete(m.Images, k)
		}
	}
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
			Labels:  c.Labels,
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

	var mounts []ContainerMountInfo
	for _, v := range opts.Volumes {
		parts := strings.Split(v, ":")
		src := parts[0]
		dest := parts[0]
		mode := "rw"
		if len(parts) > 1 {
			dest = parts[1]
		}
		if len(parts) > 2 {
			mode = parts[2]
		}
		mounts = append(mounts, ContainerMountInfo{
			Source:      src,
			Destination: dest,
			Mode:        mode,
			Type:        "bind",
		})
	}

	var envList []string
	if opts.Env != nil {
		for k, val := range opts.Env {
			envList = append(envList, fmt.Sprintf("%s=%s", k, val))
		}
	} else {
		envList = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "NODE_ENV=production"}
	}

	cmd := opts.Command
	if len(cmd) == 0 {
		cmd = []string{"/entrypoint.sh", "run"}
	}

	rawInspect := fmt.Sprintf(`[
  {
    "Id": "%s",
    "Name": "/%s",
    "ImageName": "%s",
    "State": { "Status": "running", "Running": true, "ExitCode": 0 },
    "NetworkSettings": { "IPAddress": "%s" }
  }
]`, id, opts.Name, opts.Image, ip)

	m.Containers[opts.Name] = &ContainerStatus{
		ID:            id,
		Name:          opts.Name,
		Image:         opts.Image,
		ImageID:       fmt.Sprintf("sha256:%x", time.Now().UnixNano())[:18],
		Status:        "running",
		Running:       true,
		ExitCode:      0,
		IPAddress:     ip,
		NetworkName:   opts.Network,
		Gateway:       "10.100.1.1",
		Ports:         opts.Ports,
		StartedAt:     time.Now(),
		Command:       cmd,
		Entrypoint:    []string{"/bin/sh", "-c"},
		Env:           envList,
		WorkingDir:    "/app",
		User:          "root",
		RestartPolicy: opts.Restart,
		Mounts:        mounts,
		Pod:           opts.Pod,
		Labels:        opts.Labels,
		RawInspect:    rawInspect,
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
			// Ensure enriched fields are set
			if len(c.Env) == 0 {
				c.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "NODE_ENV=production"}
			}
			if len(c.Command) == 0 {
				c.Command = []string{"/entrypoint.sh", "start"}
			}
			if c.WorkingDir == "" {
				c.WorkingDir = "/app"
			}
			if c.NetworkName == "" {
				c.NetworkName = "netavark"
			}
			if c.Gateway == "" {
				c.Gateway = "10.100.1.1"
			}
			if c.RestartPolicy == "" {
				c.RestartPolicy = "always"
			}
			if c.RawInspect == "" {
				c.RawInspect = fmt.Sprintf(`[
  {
    "Id": "%s",
    "Name": "/%s",
    "ImageName": "%s",
    "State": { "Status": "%s", "Running": %t, "ExitCode": %d },
    "NetworkSettings": { "IPAddress": "%s" }
  }
]`, c.ID, c.Name, c.Image, c.Status, c.Running, c.ExitCode, c.IPAddress)
			}
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
		Labels:             opts.Labels,
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
		ID:            p.ID,
		Name:          p.Name,
		State:         p.Status,
		Created:       time.Now().Format(time.RFC3339),
		NumContainers: p.NumberOfContainers,
		Containers: []*ContainerSummary{
			{ID: "mock-infra-" + p.ID, Names: []string{p.Name + "-infra"}, State: "running"},
			{ID: "mock-app-" + p.ID, Names: []string{p.Name + "-web"}, State: "running"},
		},
		Ports:        p.Ports,
		CgroupParent: "user.slice",
		Raw:          `{"Id":"` + p.ID + `","Name":"` + p.Name + `","State":"` + p.Status + `"}`,
	}, nil
}

func (m *MockRuntime) TopPod(ctx context.Context, nameOrID string) ([]*ContainerProcess, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return []*ContainerProcess{
		{User: "root", PID: "1", PPID: "0", CPU: "0.1", Elapsed: "10m", TTY: "?", Time: "0:01", Command: "/catatonit -P"},
		{User: "1000", PID: "25", PPID: "1", CPU: "0.4", Elapsed: "10m", TTY: "?", Time: "0:02", Command: "node /app/server.js"},
	}, nil
}

func (m *MockRuntime) GetPodLogs(ctx context.Context, nameOrID string, container string, tailLines int) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return "[mock-pod] Starting pod workloads...\n[mock-pod] Ready to accept connections.", nil
}

func (m *MockRuntime) GenerateKube(ctx context.Context, nameOrID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	img := fmt.Sprintf("localhost/%s:latest", nameOrID)
	portsSection := ""
	if c, ok := m.Containers[nameOrID]; ok {
		img = c.Image
		if len(c.Ports) > 0 {
			portsSection = "    ports:\n"
			for _, pt := range c.Ports {
				parts := strings.Split(pt, ":")
				cPort := parts[len(parts)-1]
				portsSection += fmt.Sprintf("    - containerPort: %s\n", cPort)
			}
		}
	}

	yaml := fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: %s
spec:
  containers:
  - name: %s
    image: %s
%s`, nameOrID, nameOrID, img, portsSection)
	return yaml, nil
}

func (m *MockRuntime) PlayKube(ctx context.Context, kubeYAML string) (*PlayKubeResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	podName := "kube-pod"
	imageName := "docker.io/library/nginx:alpine"
	lines := strings.Split(kubeYAML, "\n")
	for i, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed == "metadata:" && i+1 < len(lines) {
			for j := i + 1; j < len(lines); j++ {
				sub := strings.TrimSpace(lines[j])
				if strings.HasPrefix(sub, "name:") {
					podName = strings.TrimSpace(strings.TrimPrefix(sub, "name:"))
					break
				}
			}
		}
		if strings.HasPrefix(trimmed, "image:") {
			imageName = strings.TrimSpace(strings.TrimPrefix(trimmed, "image:"))
		}
	}

	podID := fmt.Sprintf("pod-k8s-%d", time.Now().UnixNano())[:12]
	m.Pods[podName] = &PodSummary{
		ID:                 podID,
		Name:               podName,
		Status:             "Running",
		Created:            time.Now().Format(time.RFC3339),
		NumberOfContainers: 1,
		Ports:              []string{"8080:80"},
	}

	ctrID := fmt.Sprintf("ctr-k8s-%d", time.Now().UnixNano())[:12]
	ctrName := podName + "-app"
	m.Containers[ctrName] = &ContainerStatus{
		ID:            ctrID,
		Name:          ctrName,
		Image:         imageName,
		ImageID:       fmt.Sprintf("sha256:%x", time.Now().UnixNano())[:18],
		Status:        "running",
		Running:       true,
		ExitCode:      0,
		IPAddress:     "10.89.0.42",
		NetworkName:   "podman",
		Gateway:       "10.89.0.1",
		Ports:         []string{"8080:80"},
		StartedAt:     time.Now(),
		Command:       []string{"/docker-entrypoint.sh", "nginx", "-g", "daemon off;"},
		Entrypoint:    []string{"/docker-entrypoint.sh"},
		Env:           []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "NGINX_VERSION=1.25.3"},
		WorkingDir:    "/var/www/html",
		User:          "root",
		RestartPolicy: "always",
		Pod:           podName,
		RawInspect: fmt.Sprintf(`[
  {
    "Id": "%s",
    "Name": "/%s",
    "ImageName": "%s",
    "State": { "Status": "running", "Running": true, "ExitCode": 0 },
    "NetworkSettings": { "IPAddress": "10.89.0.42" }
  }
]`, ctrID, ctrName, imageName),
	}
	m.Logs[ctrName] = []string{"Kubernetes pod initialized", "Nginx web server ready"}

	return &PlayKubeResult{
		Pods:       []string{podID},
		Containers: []string{ctrID},
		Output:     fmt.Sprintf("Pod %s (%s) and container %s successfully deployed via Kubernetes manifest.", podName, podID, ctrName),
	}, nil
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

func (m *MockRuntime) CreateNetworkWithOptions(ctx context.Context, opts NetworkCreateOptions) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	driver := opts.Driver
	if driver == "" {
		driver = "bridge"
	}
	dnsEnabled := true
	if opts.DisableDNS {
		dnsEnabled = false
	}
	m.Networks[opts.Name] = &NetworkSummary{
		ID:               fmt.Sprintf("net-%d", time.Now().UnixNano())[:12],
		Name:             opts.Name,
		Driver:           driver,
		Subnet:           opts.Subnet,
		Gateway:          opts.Gateway,
		Internal:         opts.Internal,
		IPv6:             opts.IPv6,
		DNSEnabled:       dnsEnabled,
		NetworkInterface: opts.Parent,
		Labels:           opts.Labels,
	}
	return nil
}

func (m *MockRuntime) CreateNetwork(ctx context.Context, name, subnet, gateway string) error {
	return m.CreateNetworkWithOptions(ctx, NetworkCreateOptions{
		Name:    name,
		Driver:  "bridge",
		Subnet:  subnet,
		Gateway: gateway,
	})
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

func (m *MockRuntime) CreateVolumeWithOptions(ctx context.Context, name string, labels map[string]string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.Volumes[name] = &VolumeSummary{
		Name:       name,
		Driver:     "local",
		Scope:      "local",
		Mountpoint: fmt.Sprintf("/var/lib/procloud/volumes/%s", name),
		Size:       "100 MB",
		Labels:     labels,
	}
	return m.Volumes[name].Mountpoint, nil
}

func (m *MockRuntime) CreateVolume(ctx context.Context, name string) (string, error) {
	return m.CreateVolumeWithOptions(ctx, name, nil)
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

// -------------------------------------------------------------
// Mock Secrets Engine
// -------------------------------------------------------------

func (m *MockRuntime) ListSecrets(ctx context.Context) ([]*SecretSummary, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var list []*SecretSummary
	for _, s := range m.Secrets {
		list = append(list, &SecretSummary{
			ID:        s.ID,
			Name:      s.Name,
			Driver:    s.Driver,
			CreatedAt: s.CreatedAt,
			UpdatedAt: s.UpdatedAt,
			Labels:    s.Labels,
		})
	}
	return list, nil
}

func (m *MockRuntime) CreateSecret(ctx context.Context, opts SecretCreateOptions) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if opts.Name == "" {
		return "", fmt.Errorf("secret name is required")
	}
	id := fmt.Sprintf("sec-%d", time.Now().UnixNano())
	driver := opts.Driver
	if driver == "" {
		driver = "file"
	}
	now := time.Now().Format(time.RFC3339)
	m.Secrets[opts.Name] = &SecretInspect{
		ID:         id,
		Name:       opts.Name,
		Driver:     driver,
		CreatedAt:  now,
		UpdatedAt:  now,
		SecretData: opts.Data,
		Labels:     opts.Labels,
	}
	return id, nil
}

func (m *MockRuntime) InspectSecret(ctx context.Context, nameOrID string, showSecret bool) (*SecretInspect, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for k, s := range m.Secrets {
		if k == nameOrID || s.ID == nameOrID || s.Name == nameOrID {
			secCopy := *s
			if !showSecret {
				secCopy.SecretData = ""
			}
			return &secCopy, nil
		}
	}
	return nil, fmt.Errorf("secret not found: %s", nameOrID)
}

func (m *MockRuntime) RemoveSecret(ctx context.Context, nameOrID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for k, s := range m.Secrets {
		if k == nameOrID || s.ID == nameOrID || s.Name == nameOrID {
			delete(m.Secrets, k)
			return nil
		}
	}
	return fmt.Errorf("secret not found: %s", nameOrID)
}
