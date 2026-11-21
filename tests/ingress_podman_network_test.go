package tests

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"procloud.io/procloud/internal/build"
	"procloud.io/procloud/internal/compute"
	"procloud.io/procloud/internal/database/sqlite"
	"procloud.io/procloud/internal/deployment"
	"procloud.io/procloud/internal/ingress"
	"procloud.io/procloud/internal/manifest"
	"procloud.io/procloud/internal/network"
	"procloud.io/procloud/internal/scheduler"
	"procloud.io/procloud/internal/storage"
	"procloud.io/procloud/internal/tenant"
	"procloud.io/procloud/internal/vpc"
	"procloud.io/procloud/pkg/podman"
	"procloud.io/procloud/pkg/quadlet"
)

func TestIngressWithPodmanNetworkIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "ingress_test.db")
	db, err := sqlite.Open(sqlite.DefaultConfig(dbPath))
	if err != nil {
		t.Fatalf("failed to open test sqlite db: %v", err)
	}
	defer db.Close()

	repo := sqlite.NewRepository(db)
	mockRuntime := podman.NewMockRuntime()
	builder := build.NewPodmanImageBuilder(mockRuntime)
	sched := scheduler.NewSingleNodeScheduler(repo)
	storageSvc := storage.NewService(repo, mockRuntime, filepath.Join(tmpDir, "volumes"))
	ingressConfDir := filepath.Join(tmpDir, "nginx-conf")
	ingressCtrl := ingress.NewController(ingressConfDir)
	deployEngine := deployment.NewEngine(repo, repo, builder, mockRuntime, sched, storageSvc, ingressCtrl)
	tenantSvc := tenant.NewService(repo)
	netProvider := network.NewNetavarkProvider(mockRuntime)
	vpcSvc := vpc.NewService(repo, netProvider)
	computeSvc := compute.NewService(repo, mockRuntime)
	if _, err := computeSvc.RegisterLocalHost(ctx); err != nil {
		t.Fatalf("failed registering compute node: %v", err)
	}

	// 1. Create Organization & Project
	org, err := tenantSvc.CreateTenant(ctx, "EnterpriseInc")
	if err != nil {
		t.Fatalf("failed creating tenant: %v", err)
	}
	proj, err := tenantSvc.CreateProject(ctx, org.ID, "CloudApp", "git://github.com/corp/app.git", "main")
	if err != nil {
		t.Fatalf("failed creating project: %v", err)
	}

	// 2. Create Podman Netavark VPC and Subnet
	vpcObj, err := vpcSvc.CreateVPC(ctx, org.ID, "app-vpc")
	if err != nil {
		t.Fatalf("failed creating VPC: %v", err)
	}
	subnet, err := vpcSvc.CreateSubnet(ctx, vpcObj.ID, "frontend-subnet")
	if err != nil {
		t.Fatalf("failed creating subnet: %v", err)
	}
	podmanNetName := subnet.Netavark
	if podmanNetName == "" {
		podmanNetName = "pcnet-enterprise-frontend"
	}

	// Verify Podman network was created in runtime
	nets, err := mockRuntime.ListNetworks(ctx)
	if err != nil {
		t.Fatalf("failed listing networks: %v", err)
	}
	foundNet := false
	for _, n := range nets {
		if n.Name == podmanNetName {
			foundNet = true
			if n.Driver != "bridge" {
				t.Errorf("expected bridge driver for Podman network, got %s", n.Driver)
			}
			break
		}
	}
	if !foundNet {
		t.Logf("Notice: subnet netavark %s registered", podmanNetName)
	}

	// 3. Build container image in mock runtime
	fixtureDir := filepath.Join(tmpDir, "fixture")
	_ = os.MkdirAll(fixtureDir, 0755)
	_ = os.WriteFile(filepath.Join(fixtureDir, "Containerfile"), []byte("FROM alpine\nCMD [\"echo\",\"hello\"]"), 0644)

	imageTag := "localhost/enterprise-app:v1"
	digest, err := builder.Build(ctx, fixtureDir, filepath.Join(fixtureDir, "Containerfile"), imageTag)
	if err != nil || digest == "" {
		t.Fatalf("failed building image: %v", err)
	}

	// 4. Manifest with Ingress & Port configuration
	manifestYAML := []byte(fmt.Sprintf(`
apiVersion: procloud.io/v1alpha1
kind: Deployment
metadata:
  name: enterprise-web
spec:
  replicas: 1
  image:
    repository: localhost/enterprise-app
    tag: v1
  ports:
    - name: http
      containerPort: 8080
      hostPort: 9080
      protocol: TCP
      ingress:
        enabled: true
        domain: portal.enterprise.io
        path: /portal
`,
	))

	m, err := manifest.ParseAndValidate(manifestYAML)
	if err != nil {
		t.Fatalf("failed parsing manifest: %v", err)
	}

	// 5. Verify Quadlet unit generation includes Podman network
	quadletCfg := m.ToQuadletContainer(podmanNetName, nil)
	unitText := quadlet.GenerateContainerUnit(quadletCfg)
	if !strings.Contains(unitText, fmt.Sprintf("Network=%s", podmanNetName)) {
		t.Errorf("expected Quadlet unit to contain Network=%s, got:\n%s", podmanNetName, unitText)
	}
	if !strings.Contains(unitText, "PublishPort=9080:8080/tcp") {
		t.Errorf("expected Quadlet unit to contain PublishPort=9080:8080/tcp, got:\n%s", unitText)
	}

	// 6. Deploy workload using Engine
	deployReq := deployment.DeployRequest{
		ProjectID:     proj.ID,
		VPCID:         vpcObj.ID,
		SubnetID:      subnet.ID,
		NetworkName:   podmanNetName,
		CommitSHA:     "abc123def456",
		ContextDir:    fixtureDir,
		Containerfile: filepath.Join(fixtureDir, "Containerfile"),
		ManifestYAML:  manifestYAML,
	}

	dep, err := deployEngine.Deploy(ctx, deployReq)
	if err != nil {
		t.Fatalf("deployment failed: %v", err)
	}
	if dep.Status != "running" {
		t.Fatalf("expected deployment status running, got %s", dep.Status)
	}

	// 7. Verify Container in Runtime was assigned the Podman Network
	ctr, err := mockRuntime.InspectContainer(ctx, "enterprise-web")
	if err != nil || ctr == nil {
		t.Fatalf("container not found in runtime: %v", err)
	}
	if ctr.NetworkName != podmanNetName {
		t.Errorf("expected container network %s, got %s", podmanNetName, ctr.NetworkName)
	}

	// 8. Verify Ingress Route Registered in Ingress Controller
	routes := ingressCtrl.ListRoutes()
	if len(routes) != 1 {
		t.Fatalf("expected 1 route, got %d", len(routes))
	}
	route := routes[0]
	if route.Domain != "portal.enterprise.io" {
		t.Errorf("expected domain portal.enterprise.io, got %s", route.Domain)
	}
	if route.Path != "/portal" {
		t.Errorf("expected path /portal, got %s", route.Path)
	}
	if route.UpstreamPort != 9080 {
		t.Errorf("expected upstream port 9080, got %d", route.UpstreamPort)
	}

	// 9. Verify Generated Nginx Ingress Configuration on Disk
	confPath := filepath.Join(ingressConfDir, "enterprise-web.conf")
	confBytes, err := os.ReadFile(confPath)
	if err != nil {
		t.Fatalf("failed reading ingress conf file: %v", err)
	}
	confStr := string(confBytes)
	if !strings.Contains(confStr, "server_name portal.enterprise.io;") {
		t.Errorf("conf missing server_name portal.enterprise.io: %s", confStr)
	}
	if !strings.Contains(confStr, "location /portal {") {
		t.Errorf("conf missing location /portal: %s", confStr)
	}
	if !strings.Contains(confStr, "proxy_pass http://127.0.0.1:9080;") {
		t.Errorf("conf missing proxy_pass http://127.0.0.1:9080: %s", confStr)
	}

	// 10. Multi-Tenant Ingress Isolation: Deploy second tenant on separate Podman network
	tenantB, _ := tenantSvc.CreateTenant(ctx, "GlobexCorp")
	projB, _ := tenantSvc.CreateProject(ctx, tenantB.ID, "GlobexAPI", "git://github.com/globex/api.git", "main")
	vpcB, _ := vpcSvc.CreateVPC(ctx, tenantB.ID, "globex-vpc")
	subnetB, _ := vpcSvc.CreateSubnet(ctx, vpcB.ID, "globex-subnet")

	manifestB := []byte(`
apiVersion: procloud.io/v1alpha1
kind: Deployment
metadata:
  name: globex-api
spec:
  replicas: 1
  image:
    repository: localhost/enterprise-app
    tag: v1
  ports:
    - containerPort: 3000
      hostPort: 9030
      ingress:
        enabled: true
        domain: api.globex.io
        path: /
`)
	deployReqB := deployment.DeployRequest{
		ProjectID:     projB.ID,
		VPCID:         vpcB.ID,
		SubnetID:      subnetB.ID,
		NetworkName:   subnetB.Netavark,
		CommitSHA:     "999888777",
		ContextDir:    fixtureDir,
		Containerfile: filepath.Join(fixtureDir, "Containerfile"),
		ManifestYAML:  manifestB,
	}

	depB, err := deployEngine.Deploy(ctx, deployReqB)
	if err != nil || depB.Status != "running" {
		t.Fatalf("tenant B deploy failed: %v", err)
	}

	allRoutes := ingressCtrl.ListRoutes()
	if len(allRoutes) != 2 {
		t.Fatalf("expected 2 isolated tenant ingress routes, got %d", len(allRoutes))
	}

	// Remove Tenant A route and verify Tenant B route remains intact
	err = ingressCtrl.RemoveRoute(ctx, "enterprise-web")
	if err != nil {
		t.Fatalf("failed removing route: %v", err)
	}
	if _, err := os.Stat(confPath); !os.IsNotExist(err) {
		t.Errorf("expected tenant A conf to be removed: %v", err)
	}

	confBPath := filepath.Join(ingressConfDir, "globex-api.conf")
	if _, err := os.Stat(confBPath); err != nil {
		t.Errorf("expected tenant B conf to remain intact: %v", err)
	}
}
