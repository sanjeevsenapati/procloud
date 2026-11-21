package quadlet

import (
	"fmt"
	"strings"
)

// ContainerConfig defines parameters for a .container Quadlet unit.
type ContainerConfig struct {
	UnitName       string
	Description    string
	ContainerName  string
	Image          string
	Environment    map[string]string
	PublishPorts   []string // e.g. "8080:8080"
	Volumes        []string // e.g. "/var/lib/procloud/volumes/v1:/data:Z"
	Network        string   // e.g. "procloud-net"
	InternalIP     string
	HealthCmd      string
	HealthInterval string
	HealthTimeout  string
	HealthRetries  int
	RestartPolicy  string // always, on-failure, no
	AutoUpdate     string // registry, local
	ExecArgs       []string
}

// GenerateContainerUnit generates systemd Quadlet [Container] unit content.
func GenerateContainerUnit(cfg ContainerConfig) string {
	var sb strings.Builder

	sb.WriteString("[Unit]\n")
	desc := cfg.Description
	if desc == "" {
		desc = fmt.Sprintf("ProCloud Managed Container %s", cfg.ContainerName)
	}
	sb.WriteString(fmt.Sprintf("Description=%s\n", desc))
	sb.WriteString("After=network-online.target\n")
	sb.WriteString("Wants=network-online.target\n\n")

	sb.WriteString("[Container]\n")
	sb.WriteString(fmt.Sprintf("Image=%s\n", cfg.Image))
	sb.WriteString(fmt.Sprintf("ContainerName=%s\n", cfg.ContainerName))

	for _, p := range cfg.PublishPorts {
		sb.WriteString(fmt.Sprintf("PublishPort=%s\n", p))
	}

	for _, v := range cfg.Volumes {
		sb.WriteString(fmt.Sprintf("Volume=%s\n", v))
	}

	if cfg.Network != "" {
		sb.WriteString(fmt.Sprintf("Network=%s\n", cfg.Network))
	}
	if cfg.InternalIP != "" {
		sb.WriteString(fmt.Sprintf("IP=%s\n", cfg.InternalIP))
	}

	for k, v := range cfg.Environment {
		sb.WriteString(fmt.Sprintf("Environment=%s=%s\n", k, v))
	}

	if cfg.HealthCmd != "" {
		sb.WriteString(fmt.Sprintf("HealthCmd=%s\n", cfg.HealthCmd))
		if cfg.HealthInterval != "" {
			sb.WriteString(fmt.Sprintf("HealthInterval=%s\n", cfg.HealthInterval))
		}
		if cfg.HealthTimeout != "" {
			sb.WriteString(fmt.Sprintf("HealthTimeout=%s\n", cfg.HealthTimeout))
		}
		if cfg.HealthRetries > 0 {
			sb.WriteString(fmt.Sprintf("HealthRetries=%d\n", cfg.HealthRetries))
		}
	}

	if cfg.AutoUpdate != "" {
		sb.WriteString(fmt.Sprintf("AutoUpdate=%s\n", cfg.AutoUpdate))
	}

	if len(cfg.ExecArgs) > 0 {
		sb.WriteString(fmt.Sprintf("Exec=%s\n", strings.Join(cfg.ExecArgs, " ")))
	}

	sb.WriteString("\n[Service]\n")
	restart := cfg.RestartPolicy
	if restart == "" {
		restart = "always"
	}
	sb.WriteString(fmt.Sprintf("Restart=%s\n", restart))
	sb.WriteString("TimeoutStartSec=300\n")
	sb.WriteString("TimeoutStopSec=60\n")

	sb.WriteString("\n[Install]\n")
	sb.WriteString("WantedBy=default.target\n")

	return sb.String()
}

// PodConfig defines parameters for a .pod Quadlet unit.
type PodConfig struct {
	PodName      string
	PublishPorts []string // "8080:80"
	Network      string
}

// GeneratePodUnit generates a systemd Quadlet [Pod] unit.
func GeneratePodUnit(cfg PodConfig) string {
	var sb strings.Builder
	sb.WriteString("[Unit]\n")
	sb.WriteString(fmt.Sprintf("Description=ProCloud Managed Pod %s\n\n", cfg.PodName))

	sb.WriteString("[Pod]\n")
	sb.WriteString(fmt.Sprintf("PodName=%s\n", cfg.PodName))
	for _, p := range cfg.PublishPorts {
		sb.WriteString(fmt.Sprintf("PublishPort=%s\n", p))
	}
	if cfg.Network != "" {
		sb.WriteString(fmt.Sprintf("Network=%s\n", cfg.Network))
	}

	sb.WriteString("\n[Install]\n")
	sb.WriteString("WantedBy=default.target\n")
	return sb.String()
}

// NetworkConfig defines parameters for a .network Quadlet unit.
type NetworkConfig struct {
	NetworkName string
	Driver      string // bridge
	Subnet      string // 10.100.1.0/24
	Gateway     string // 10.100.1.1
	Internal    bool
	DNS         bool
}

// GenerateNetworkUnit generates a systemd Quadlet [Network] unit.
func GenerateNetworkUnit(cfg NetworkConfig) string {
	var sb strings.Builder
	sb.WriteString("[Unit]\n")
	sb.WriteString(fmt.Sprintf("Description=ProCloud Managed Network %s\n\n", cfg.NetworkName))

	sb.WriteString("[Network]\n")
	sb.WriteString(fmt.Sprintf("NetworkName=%s\n", cfg.NetworkName))
	driver := cfg.Driver
	if driver == "" {
		driver = "bridge"
	}
	sb.WriteString(fmt.Sprintf("Driver=%s\n", driver))

	if cfg.Subnet != "" {
		sb.WriteString(fmt.Sprintf("Subnet=%s\n", cfg.Subnet))
	}
	if cfg.Gateway != "" {
		sb.WriteString(fmt.Sprintf("Gateway=%s\n", cfg.Gateway))
	}
	if cfg.Internal {
		sb.WriteString("Internal=true\n")
	}
	if cfg.DNS {
		sb.WriteString("Options=isolate\n")
	}

	return sb.String()
}

// VolumeConfig defines parameters for a .volume Quadlet unit.
type VolumeConfig struct {
	VolumeName string
	Device     string
	Type       string
	Options    string
}

// GenerateVolumeUnit generates a systemd Quadlet [Volume] unit.
func GenerateVolumeUnit(cfg VolumeConfig) string {
	var sb strings.Builder
	sb.WriteString("[Unit]\n")
	sb.WriteString(fmt.Sprintf("Description=ProCloud Managed Volume %s\n\n", cfg.VolumeName))

	sb.WriteString("[Volume]\n")
	sb.WriteString(fmt.Sprintf("VolumeName=%s\n", cfg.VolumeName))
	if cfg.Device != "" {
		sb.WriteString(fmt.Sprintf("Device=%s\n", cfg.Device))
	}
	if cfg.Type != "" {
		sb.WriteString(fmt.Sprintf("Type=%s\n", cfg.Type))
	}
	if cfg.Options != "" {
		sb.WriteString(fmt.Sprintf("Options=%s\n", cfg.Options))
	}
	return sb.String()
}
