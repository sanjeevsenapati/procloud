package manifest

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
	"procloud.io/procloud/pkg/quadlet"
)

// DeployManifest defines the schema for deploy.yml v1alpha1.
type DeployManifest struct {
	APIVersion string       `yaml:"apiVersion"`
	Kind       string       `yaml:"kind"`
	Metadata   Metadata     `yaml:"metadata"`
	Spec       WorkloadSpec `yaml:"spec"`
}

type Metadata struct {
	Name        string            `yaml:"name"`
	Project     string            `yaml:"project,omitempty"`
	Namespace   string            `yaml:"namespace,omitempty"`
	Labels      map[string]string `yaml:"labels,omitempty"`
	Annotations map[string]string `yaml:"annotations,omitempty"`
}

type WorkloadSpec struct {
	Replicas      int               `yaml:"replicas,omitempty"`
	Image         ImageSpec         `yaml:"image"`
	Ports         []PortSpec        `yaml:"ports,omitempty"`
	Env           []EnvVar          `yaml:"env,omitempty"`
	Volumes       []VolumeMount     `yaml:"volumes,omitempty"`
	Resources     ResourceSpec      `yaml:"resources,omitempty"`
	HealthCheck   *HealthCheckSpec  `yaml:"healthCheck,omitempty"`
	RestartPolicy string            `yaml:"restartPolicy,omitempty"`
	Command       []string          `yaml:"command,omitempty"`
}

type ImageSpec struct {
	Repository string     `yaml:"repository"`
	Tag        string     `yaml:"tag"`
	Build      *BuildSpec `yaml:"build,omitempty"`
}

type BuildSpec struct {
	Context       string `yaml:"context,omitempty"`
	Containerfile string `yaml:"containerfile,omitempty"`
}

type PortSpec struct {
	Name          string       `yaml:"name,omitempty"`
	ContainerPort int          `yaml:"containerPort"`
	HostPort      int          `yaml:"hostPort,omitempty"`
	Protocol      string       `yaml:"protocol,omitempty"` // TCP, UDP
	Ingress       *IngressSpec `yaml:"ingress,omitempty"`
}

type IngressSpec struct {
	Enabled bool   `yaml:"enabled"`
	Domain  string `yaml:"domain"`
	Path    string `yaml:"path,omitempty"`
	TLS     bool   `yaml:"tls,omitempty"`
}

type EnvVar struct {
	Name  string `yaml:"name"`
	Value string `yaml:"value"`
}

type VolumeMount struct {
	Name      string `yaml:"name"`
	MountPath string `yaml:"mountPath"`
	HostPath  string `yaml:"hostPath,omitempty"`
	Size      string `yaml:"size,omitempty"`
	ReadOnly  bool   `yaml:"readOnly,omitempty"`
}

type ResourceSpec struct {
	Limits   ResourceRequirements `yaml:"limits,omitempty"`
	Requests ResourceRequirements `yaml:"requests,omitempty"`
}

type ResourceRequirements struct {
	CPU    string `yaml:"cpu,omitempty"`    // e.g. "1.0", "500m"
	Memory string `yaml:"memory,omitempty"` // e.g. "512Mi", "1Gi"
}

type HealthCheckSpec struct {
	HTTPGet             *HTTPGetAction `yaml:"httpGet,omitempty"`
	Exec                *ExecAction    `yaml:"exec,omitempty"`
	InitialDelaySeconds int            `yaml:"initialDelaySeconds,omitempty"`
	PeriodSeconds       int            `yaml:"periodSeconds,omitempty"`
	TimeoutSeconds      int            `yaml:"timeoutSeconds,omitempty"`
	FailureThreshold    int            `yaml:"failureThreshold,omitempty"`
}

type HTTPGetAction struct {
	Path string `yaml:"path"`
	Port int    `yaml:"port"`
}

type ExecAction struct {
	Command []string `yaml:"command"`
}

// ParseAndValidate parses YAML content and validates all fields against v1alpha1 rules.
func ParseAndValidate(data []byte) (*DeployManifest, error) {
	var m DeployManifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("invalid YAML syntax: %w", err)
	}

	if m.APIVersion == "" {
		m.APIVersion = "procloud.io/v1alpha1"
	}
	if m.Kind == "" {
		m.Kind = "Deployment"
	}

	if m.Metadata.Name == "" {
		return nil, fmt.Errorf("metadata.name is required")
	}

	// Validate ports
	for _, p := range m.Spec.Ports {
		if p.ContainerPort <= 0 || p.ContainerPort > 65535 {
			return nil, fmt.Errorf("invalid containerPort: %d", p.ContainerPort)
		}
		if p.HostPort < 0 || p.HostPort > 65535 {
			return nil, fmt.Errorf("invalid hostPort: %d", p.HostPort)
		}
	}

	// Validate volumes
	for _, v := range m.Spec.Volumes {
		if v.Name == "" {
			return nil, fmt.Errorf("volume name is required")
		}
		if v.MountPath == "" || !strings.HasPrefix(v.MountPath, "/") {
			return nil, fmt.Errorf("volume %s mountPath must be an absolute path", v.Name)
		}
	}

	return &m, nil
}

// ToQuadletContainer converts a validated manifest into Quadlet ContainerConfig.
func (m *DeployManifest) ToQuadletContainer(networkName string, hostVolumeMappings map[string]string) quadlet.ContainerConfig {
	envMap := make(map[string]string)
	for _, e := range m.Spec.Env {
		envMap[e.Name] = e.Value
	}

	var ports []string
	for _, p := range m.Spec.Ports {
		if p.HostPort > 0 {
			proto := strings.ToLower(p.Protocol)
			if proto == "" {
				proto = "tcp"
			}
			ports = append(ports, fmt.Sprintf("%d:%d/%s", p.HostPort, p.ContainerPort, proto))
		}
	}

	var volumes []string
	for _, v := range m.Spec.Volumes {
		hostPath := v.HostPath
		if mapped, ok := hostVolumeMappings[v.Name]; ok {
			hostPath = mapped
		}
		if hostPath != "" {
			mode := "Z"
			if v.ReadOnly {
				mode = "ro,Z"
			}
			volumes = append(volumes, fmt.Sprintf("%s:%s:%s", hostPath, v.MountPath, mode))
		}
	}

	healthCmd := ""
	if m.Spec.HealthCheck != nil {
		if m.Spec.HealthCheck.HTTPGet != nil {
			healthCmd = fmt.Sprintf("curl -f http://localhost:%d%s || exit 1",
				m.Spec.HealthCheck.HTTPGet.Port, m.Spec.HealthCheck.HTTPGet.Path)
		} else if m.Spec.HealthCheck.Exec != nil {
			healthCmd = strings.Join(m.Spec.HealthCheck.Exec.Command, " ")
		}
	}

	interval := "15s"
	timeout := "5s"
	retries := 3
	if m.Spec.HealthCheck != nil {
		if m.Spec.HealthCheck.PeriodSeconds > 0 {
			interval = fmt.Sprintf("%ds", m.Spec.HealthCheck.PeriodSeconds)
		}
		if m.Spec.HealthCheck.TimeoutSeconds > 0 {
			timeout = fmt.Sprintf("%ds", m.Spec.HealthCheck.TimeoutSeconds)
		}
		if m.Spec.HealthCheck.FailureThreshold > 0 {
			retries = m.Spec.HealthCheck.FailureThreshold
		}
	}

	restart := m.Spec.RestartPolicy
	if restart == "" {
		restart = "always"
	}

	image := fmt.Sprintf("%s:%s", m.Spec.Image.Repository, m.Spec.Image.Tag)
	if m.Spec.Image.Tag == "" {
		image = m.Spec.Image.Repository
	}

	return quadlet.ContainerConfig{
		UnitName:       fmt.Sprintf("%s.container", m.Metadata.Name),
		Description:    fmt.Sprintf("ProCloud Application %s", m.Metadata.Name),
		ContainerName:  m.Metadata.Name,
		Image:          image,
		Environment:    envMap,
		PublishPorts:   ports,
		Volumes:        volumes,
		Network:        networkName,
		HealthCmd:      healthCmd,
		HealthInterval: interval,
		HealthTimeout:  timeout,
		HealthRetries:  retries,
		RestartPolicy:  restart,
		ExecArgs:       m.Spec.Command,
	}
}
