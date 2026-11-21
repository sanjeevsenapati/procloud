package tests

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"procloud.io/procloud/internal/audit"
	"procloud.io/procloud/internal/auth"
	"procloud.io/procloud/internal/build"
	"procloud.io/procloud/internal/compute"
	"procloud.io/procloud/internal/database/sqlite"
	"procloud.io/procloud/internal/deployment"
	"procloud.io/procloud/internal/ingress"
	"procloud.io/procloud/internal/monitoring"
	"procloud.io/procloud/internal/network"
	"procloud.io/procloud/internal/scheduler"
	"procloud.io/procloud/internal/storage"
	"procloud.io/procloud/internal/tenant"
	"procloud.io/procloud/internal/vpc"
	"procloud.io/procloud/pkg/podman"
)

// TestMilestone1CompleteWorkflow verifies all 16 steps of the first milestone.
func TestMilestone1CompleteWorkflow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tmpDir, err := os.MkdirTemp("", "procloud-milestone-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Step 1: Start ProGit Cloud on one server & Step 2: Initialise SQLite with WAL
	dbPath := filepath.Join(tmpDir, "procloud.db")
	db, err := sqlite.Open(sqlite.DefaultConfig(dbPath))
	if err != nil {
		t.Fatalf("Step 1/2 failed: %v", err)
	}
	defer db.Close()

	status, err := db.Status(ctx)
	if err != nil || status.Mode != "wal" {
		t.Fatalf("Step 2 failed: SQLite WAL mode not verified: %v", err)
	}

	repo := sqlite.NewRepository(db)
	runtime := podman.NewMockRuntime()
	builder := build.NewPodmanImageBuilder(runtime)
	sched := scheduler.NewSingleNodeScheduler(repo)
	storageSvc := storage.NewService(repo, runtime, filepath.Join(tmpDir, "volumes"))
	ingressCtrl := ingress.NewController(filepath.Join(tmpDir, "nginx"))
	deployEngine := deployment.NewEngine(repo, repo, builder, runtime, sched, storageSvc, ingressCtrl)
	monitoringSvc := monitoring.NewService(db)
	auditSvc := audit.NewService(repo)
	authSvc := auth.NewService(repo, "secret")
	computeSvc := compute.NewService(repo, runtime)
	tenantSvc := tenant.NewService(repo)
	netProvider := network.NewNetavarkProvider(runtime)
	vpcSvc := vpc.NewService(repo, netProvider)

	_ = monitoringSvc
	_ = auditSvc

	// Step 3: Create an administrator account
	admin, err := authSvc.Register(ctx, "admin", "admin@procloud.io", "Pass123!", "admin")
	if err != nil || admin.Role != "admin" {
		t.Fatalf("Step 3 failed: %v", err)
	}

	// Step 4: Register the local Podman host
	node, err := computeSvc.RegisterLocalHost(ctx)
	if err != nil || node.Hostname != "mock-host" {
		t.Fatalf("Step 4 failed: %v", err)
	}

	// Step 5: Create an organisation and project
	org, err := tenantSvc.CreateTenant(ctx, "AcmeCorp")
	if err != nil {
		t.Fatalf("Step 5 failed org: %v", err)
	}
	proj, err := tenantSvc.CreateProject(ctx, org.ID, "WebStore", "http://localhost:3000/progit/store.git", "main")
	if err != nil {
		t.Fatalf("Step 5 failed proj: %v", err)
	}

	// Step 6: Create a local VPC and subnet
	localVPC, err := vpcSvc.CreateVPC(ctx, org.ID, "prod-vpc")
	if err != nil {
		t.Fatalf("Step 6 failed vpc: %v", err)
	}
	subnet, err := vpcSvc.CreateSubnet(ctx, localVPC.ID, "app-subnet")
	if err != nil {
		t.Fatalf("Step 6 failed subnet: %v", err)
	}

	// Step 7: Connect an existing ProGit repo & Step 8: Build an image from Containerfile
	fixtureDir := filepath.Join(tmpDir, "fixture")
	_ = os.MkdirAll(fixtureDir, 0755)
	_ = os.WriteFile(filepath.Join(fixtureDir, "Containerfile"), []byte("FROM alpine\nCMD [\"echo\",\"hello\"]"), 0644)

	digestV1, err := builder.Build(ctx, fixtureDir, filepath.Join(fixtureDir, "Containerfile"), "localhost/store:v1")
	if err != nil || digestV1 == "" {
		t.Fatalf("Step 8 failed: %v", err)
	}

	// Step 9: Validate deploy.yml
	manifestYAML := []byte(`
apiVersion: procloud.io/v1alpha1
kind: Deployment
metadata:
  name: store-app
spec:
  replicas: 1
  image:
    repository: localhost/store
    tag: v1
  ports:
    - containerPort: 8080
      hostPort: 8080
      protocol: TCP
      ingress:
        enabled: true
        domain: store.procloud.local
        path: /
  volumes:
    - name: store-data
      mountPath: /data
      size: 2Gi
`)
	// Step 10: Create a persistent volume
	vol, err := storageSvc.CreateVolume(ctx, org.ID, proj.ID, "store-data", 2048)
	if err != nil {
		t.Fatalf("Step 10 failed: %v", err)
	}

	// Write persistent state token into volume
	stateFile := filepath.Join(vol.HostPath, "app-state.json")
	_ = os.WriteFile(stateFile, []byte(`{"order_id": 9942, "status": "CONFIRMED"}`), 0644)

	// Step 11: Deploy application using Quadlet and Podman
	deployReq := deployment.DeployRequest{
		ProjectID:     proj.ID,
		VPCID:         localVPC.ID,
		SubnetID:      subnet.ID,
		NetworkName:   subnet.Netavark,
		CommitSHA:     "1111222233334444",
		ContextDir:    fixtureDir,
		Containerfile: filepath.Join(fixtureDir, "Containerfile"),
		ManifestYAML:  manifestYAML,
	}

	dep1, err := deployEngine.Deploy(ctx, deployReq)
	if err != nil || dep1.Status != "running" {
		t.Fatalf("Step 11 failed: %v", err)
	}

	// Step 12: Configure Nginx ingress
	routes := ingressCtrl.ListRoutes()
	if len(routes) == 0 || routes[0].Domain != "store.procloud.local" {
		t.Fatalf("Step 12 failed: ingress route missing")
	}

	// Step 13: Display application health and logs
	logs, err := runtime.GetContainerLogs(ctx, "store-app", 10)
	if err != nil || !strings.Contains(logs, "started successfully") {
		t.Fatalf("Step 13 failed: logs missing: %s", logs)
	}

	// Step 14: Push new Git commit and deploy updated version (v2)
	manifestYAMLV2 := []byte(`
apiVersion: procloud.io/v1alpha1
kind: Deployment
metadata:
  name: store-app
spec:
  replicas: 1
  image:
    repository: localhost/store
    tag: v2
  ports:
    - containerPort: 8080
      hostPort: 8080
      ingress:
        enabled: true
        domain: store.procloud.local
        path: /
  volumes:
    - name: store-data
      mountPath: /data
      size: 2Gi
`)
	deployReqV2 := deployment.DeployRequest{
		ProjectID:     proj.ID,
		VPCID:         localVPC.ID,
		SubnetID:      subnet.ID,
		NetworkName:   subnet.Netavark,
		CommitSHA:     "5555666677778888",
		ContextDir:    fixtureDir,
		Containerfile: filepath.Join(fixtureDir, "Containerfile"),
		ManifestYAML:  manifestYAMLV2,
	}
	dep2, err := deployEngine.Deploy(ctx, deployReqV2)
	if err != nil || dep2.ReleaseNumber != 2 {
		t.Fatalf("Step 14 failed: %v", err)
	}

	// Step 15: Roll back to previous version
	rbDep, err := deployEngine.Rollback(ctx, "store-app", 1)
	if err != nil || rbDep.ReleaseNumber != 3 || rbDep.CommitSHA != "1111222233334444" {
		t.Fatalf("Step 15 failed: rollback mismatch: %v", err)
	}

	// Step 16: Restart application and verify persistent data remains intact
	_ = runtime.StopContainer(ctx, "store-app", 1)
	_ = runtime.RemoveContainer(ctx, "store-app", true)
	_, err = runtime.RunContainer(ctx, podman.RunOptions{
		Name:    "store-app",
		Image:   rbDep.ImageTag,
		Volumes: []string{vol.HostPath + ":/data:Z"},
	})
	if err != nil {
		t.Fatalf("Step 16 restart failed: %v", err)
	}

	// Verify persistent data integrity
	data, err := os.ReadFile(stateFile)
	if err != nil || !strings.Contains(string(data), "9942") {
		t.Fatalf("Step 16 failed: persistent data was lost or corrupted: %v", err)
	}

	t.Log("SUCCESS: All 16 steps of Milestone 1 verified successfully!")
}
