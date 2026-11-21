package ingress

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

type Route struct {
	Domain       string
	Path         string
	UpstreamHost string
	UpstreamPort int
	AppName      string
}

type Controller struct {
	confDir string
	mu      sync.Mutex
	routes  map[string]Route
}

func NewController(confDir string) *Controller {
	if confDir == "" {
		confDir = "/etc/nginx/conf.d/procloud"
	}
	_ = os.MkdirAll(confDir, 0755)

	return &Controller{
		confDir: confDir,
		routes:  make(map[string]Route),
	}
}

// RegisterRoute adds or updates an ingress route and reloads Nginx.
func (c *Controller) RegisterRoute(ctx context.Context, route Route) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.routes[route.AppName] = route
	return c.generateAndReload(ctx)
}

// RemoveRoute removes a route and reloads Nginx.
func (c *Controller) RemoveRoute(ctx context.Context, appName string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.routes, appName)
	_ = os.Remove(filepath.Join(c.confDir, fmt.Sprintf("%s.conf", appName)))
	return c.generateAndReload(ctx)
}

func (c *Controller) generateAndReload(ctx context.Context) error {
	for appName, r := range c.routes {
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("# ProCloud Managed Ingress for %s\n", appName))
		sb.WriteString("server {\n")
		sb.WriteString("    listen 80;\n")
		domain := r.Domain
		if domain == "" {
			domain = "_"
		}
		sb.WriteString(fmt.Sprintf("    server_name %s;\n\n", domain))

		path := r.Path
		if path == "" {
			path = "/"
		}
		sb.WriteString(fmt.Sprintf("    location %s {\n", path))
		sb.WriteString(fmt.Sprintf("        proxy_pass http://%s:%d;\n", r.UpstreamHost, r.UpstreamPort))
		sb.WriteString("        proxy_set_header Host $host;\n")
		sb.WriteString("        proxy_set_header X-Real-IP $remote_addr;\n")
		sb.WriteString("        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;\n")
		sb.WriteString("        proxy_set_header X-Forwarded-Proto $scheme;\n")
		sb.WriteString("        proxy_http_version 1.1;\n")
		sb.WriteString("        proxy_set_header Upgrade $http_upgrade;\n")
		sb.WriteString("        proxy_set_header Connection \"upgrade\";\n")
		sb.WriteString("    }\n")
		sb.WriteString("}\n")

		targetFile := filepath.Join(c.confDir, fmt.Sprintf("%s.conf", appName))
		if err := os.WriteFile(targetFile, []byte(sb.String()), 0644); err != nil {
			return err
		}
	}

	// Graceful reload if nginx binary is running
	if _, err := exec.LookPath("nginx"); err == nil {
		_ = exec.CommandContext(ctx, "nginx", "-s", "reload").Run()
	}

	return nil
}

func (c *Controller) ListRoutes() []Route {
	c.mu.Lock()
	defer c.mu.Unlock()

	var list []Route
	for _, r := range c.routes {
		list = append(list, r)
	}
	return list
}
