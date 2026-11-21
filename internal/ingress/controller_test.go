package ingress

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestController_RegisterRoute(t *testing.T) {
	ctx := context.Background()
	confDir := t.TempDir()
	ctrl := NewController(confDir)

	route := Route{
		AppName:      "portal-app",
		Domain:       "portal.acme.local",
		Path:         "/app",
		UpstreamHost: "127.0.0.1",
		UpstreamPort: 8080,
	}

	if err := ctrl.RegisterRoute(ctx, route); err != nil {
		t.Fatalf("RegisterRoute failed: %v", err)
	}

	// 1. Verify route listed
	routes := ctrl.ListRoutes()
	if len(routes) != 1 {
		t.Fatalf("expected 1 route, got %d", len(routes))
	}
	if routes[0].AppName != "portal-app" || routes[0].Domain != "portal.acme.local" {
		t.Errorf("unexpected route data: %+v", routes[0])
	}

	// 2. Verify config file written
	confFile := filepath.Join(confDir, "portal-app.conf")
	contentBytes, err := os.ReadFile(confFile)
	if err != nil {
		t.Fatalf("failed reading generated conf file: %v", err)
	}
	content := string(contentBytes)

	expectedSnippets := []string{
		"server_name portal.acme.local;",
		"location /app {",
		"proxy_pass http://127.0.0.1:8080;",
		"proxy_set_header Host $host;",
		"proxy_set_header Upgrade $http_upgrade;",
		"proxy_set_header Connection \"upgrade\";",
	}

	for _, snippet := range expectedSnippets {
		if !strings.Contains(content, snippet) {
			t.Errorf("generated conf missing expected snippet: %q\nFull content:\n%s", snippet, content)
		}
	}
}

func TestController_Defaults(t *testing.T) {
	ctx := context.Background()
	confDir := t.TempDir()
	ctrl := NewController(confDir)

	route := Route{
		AppName:      "default-app",
		Domain:       "", // should default to "_"
		Path:         "", // should default to "/"
		UpstreamHost: "10.89.0.5",
		UpstreamPort: 3000,
	}

	if err := ctrl.RegisterRoute(ctx, route); err != nil {
		t.Fatalf("RegisterRoute failed: %v", err)
	}

	confFile := filepath.Join(confDir, "default-app.conf")
	contentBytes, err := os.ReadFile(confFile)
	if err != nil {
		t.Fatalf("failed reading conf file: %v", err)
	}
	content := string(contentBytes)

	if !strings.Contains(content, "server_name _;") {
		t.Errorf("expected default server_name _; got:\n%s", content)
	}
	if !strings.Contains(content, "location / {") {
		t.Errorf("expected default location / {; got:\n%s", content)
	}
	if !strings.Contains(content, "proxy_pass http://10.89.0.5:3000;") {
		t.Errorf("expected proxy_pass http://10.89.0.5:3000;; got:\n%s", content)
	}
}

func TestController_UpdateAndRemoveRoute(t *testing.T) {
	ctx := context.Background()
	confDir := t.TempDir()
	ctrl := NewController(confDir)

	// Step 1: Register initial route
	routeV1 := Route{
		AppName:      "web-store",
		Domain:       "store.acme.local",
		Path:         "/",
		UpstreamHost: "127.0.0.1",
		UpstreamPort: 8080,
	}
	if err := ctrl.RegisterRoute(ctx, routeV1); err != nil {
		t.Fatalf("RegisterRoute v1 failed: %v", err)
	}

	confFile := filepath.Join(confDir, "web-store.conf")
	if _, err := os.Stat(confFile); err != nil {
		t.Fatalf("expected conf file to exist: %v", err)
	}

	// Step 2: Update route with new port and domain
	routeV2 := Route{
		AppName:      "web-store",
		Domain:       "shop.acme.local",
		Path:         "/shop",
		UpstreamHost: "127.0.0.1",
		UpstreamPort: 9090,
	}
	if err := ctrl.RegisterRoute(ctx, routeV2); err != nil {
		t.Fatalf("RegisterRoute v2 failed: %v", err)
	}

	contentBytes, err := os.ReadFile(confFile)
	if err != nil {
		t.Fatalf("failed reading updated conf file: %v", err)
	}
	content := string(contentBytes)

	if !strings.Contains(content, "server_name shop.acme.local;") {
		t.Errorf("expected updated domain shop.acme.local; got:\n%s", content)
	}
	if !strings.Contains(content, "proxy_pass http://127.0.0.1:9090;") {
		t.Errorf("expected updated proxy_pass http://127.0.0.1:9090;; got:\n%s", content)
	}

	// Step 3: Remove route
	if err := ctrl.RemoveRoute(ctx, "web-store"); err != nil {
		t.Fatalf("RemoveRoute failed: %v", err)
	}

	// Verify route is removed from memory
	routes := ctrl.ListRoutes()
	if len(routes) != 0 {
		t.Fatalf("expected 0 routes after removal, got %d", len(routes))
	}

	// Verify config file is deleted from disk
	if _, err := os.Stat(confFile); !os.IsNotExist(err) {
		t.Errorf("expected conf file to be removed from disk, but it still exists: %v", err)
	}
}

func TestController_ConcurrentAccess(t *testing.T) {
	ctx := context.Background()
	confDir := t.TempDir()
	ctrl := NewController(confDir)

	var wg sync.WaitGroup
	workers := 10

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			appName := "app-" + string(rune('a'+id))
			_ = ctrl.RegisterRoute(ctx, Route{
				AppName:      appName,
				Domain:       appName + ".local",
				Path:         "/",
				UpstreamHost: "127.0.0.1",
				UpstreamPort: 8000 + id,
			})
			_ = ctrl.ListRoutes()
		}(i)
	}

	wg.Wait()

	routes := ctrl.ListRoutes()
	if len(routes) != workers {
		t.Fatalf("expected %d routes, got %d", workers, len(routes))
	}
}
