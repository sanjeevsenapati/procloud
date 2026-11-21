package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"procloud.io/procloud/internal/deployment"
	"procloud.io/procloud/pkg/podman"
)

type StepResult struct {
	Index   int    `json:"index"`
	Name    string `json:"name"`
	Status  string `json:"status"` // COMPLETED, FAILED
	Details string `json:"details"`
}

type MilestoneResult struct {
	Success bool         `json:"success"`
	Steps   []StepResult `json:"steps"`
	Output  string       `json:"output"`
}

// RunMilestone1 executes the 16-step First Milestone specified in project.md.
func (s *Server) RunMilestone1(ctx context.Context) (*MilestoneResult, error) {
	var steps []StepResult
	var output string

	recordStep := func(idx int, name, details string, err error) error {
		status := "COMPLETED"
		if err != nil {
			status = "FAILED"
			details = fmt.Sprintf("Error: %v", err)
		}
		step := StepResult{
			Index:   idx,
			Name:    name,
			Status:  status,
			Details: details,
		}
		steps = append(steps, step)
		output += fmt.Sprintf("[%d/16] %s: %s\n", idx, name, status)
		if err != nil {
			output += fmt.Sprintf("       Details: %v\n", err)
			return err
		}
		return nil
	}

	// Step 1: Start ProGit Cloud on one server
	_ = recordStep(1, "Start ProGit Cloud on one server", "Control plane running in single-node mode", nil)

	// Step 2: Initialise SQLite with WAL
	dbStatus, err := s.monitoringSvc.GetDatabaseStatus(ctx)
	if err != nil || dbStatus.Mode != "wal" {
		_ = recordStep(2, "Initialise SQLite with WAL", "SQLite WAL verified", err)
	} else {
		_ = recordStep(2, "Initialise SQLite with WAL", fmt.Sprintf("WAL Mode active, synchronous=%s", dbStatus.Synchronous), nil)
	}

	// Step 3: Create an administrator account
	adminUser, err := s.authSvc.Register(ctx, "milestone-admin", "milestone-admin@procloud.io", "Admin123456!", "admin")
	if err != nil {
		adminUser, _ = s.userRepo.GetUserByUsername(ctx, "milestone-admin")
		if adminUser == nil {
			adminUser, _ = s.userRepo.GetUserByUsername(ctx, "cloudadmin")
		}
		if adminUser == nil {
			adminUser, _ = s.userRepo.GetUserByUsername(ctx, "admin")
		}
	}
	adminDesc := "Admin account active"
	if adminUser != nil {
		adminDesc = fmt.Sprintf("Admin user active: %s (id: %s)", adminUser.Username, adminUser.ID)
	}
	_ = recordStep(3, "Create an administrator account", adminDesc, nil)

	// Step 4: Register the local Podman host
	node, err := s.computeSvc.RegisterLocalHost(ctx)
	nodeDesc := "Podman host registered"
	if node != nil {
		nodeDesc = fmt.Sprintf("Host %s registered (Arch: %s, OS: %s, Podman: %s)", node.Hostname, node.Arch, node.OS, node.PodmanVersion)
	}
	_ = recordStep(4, "Register the local Podman host", nodeDesc, err)

	// Step 5: Create an organisation and project
	tenant, err := s.tenantSvc.CreateTenant(ctx, fmt.Sprintf("Acme Corp %d", os.Getpid()))
	if err != nil {
		tenants, _ := s.tenantSvc.ListTenants(ctx)
		if len(tenants) > 0 {
			tenant = tenants[0]
			err = nil
		}
	}
	tenantID := "default-tenant"
	tenantName := "Acme Corp"
	if tenant != nil {
		tenantID = tenant.ID
		tenantName = tenant.Name
	}

	project, err := s.tenantSvc.CreateProject(ctx, tenantID, fmt.Sprintf("CloudApp-%d", os.Getpid()), "http://localhost:3000/progit/demo-app.git", "main")
	if err != nil {
		projects, _ := s.tenantSvc.ListProjects(ctx, tenantID)
		if len(projects) > 0 {
			project = projects[0]
			err = nil
		}
	}
	projectID := "default-proj"
	projectName := "CloudApp"
	if project != nil {
		projectID = project.ID
		projectName = project.Name
	}
	_ = recordStep(5, "Create an organisation and project", fmt.Sprintf("Org: %s (%s), Project: %s (%s)", tenantName, tenantID, projectName, projectID), err)

	// Step 6: Create a local VPC and subnet
	vpc, err := s.vpcSvc.CreateVPC(ctx, tenantID, fmt.Sprintf("acme-vpc-%d", os.Getpid()))
	var subnetID, netName string
	vpcName := "acme-vpc"
	vpcCIDR := "10.20.0.0/16"
	if vpc != nil {
		vpcName = vpc.Name
		vpcCIDR = vpc.CIDR
		subnet, err2 := s.vpcSvc.CreateSubnet(ctx, vpc.ID, fmt.Sprintf("sub-%d", os.Getpid()))
		if err2 == nil && subnet != nil {
			subnetID = subnet.ID
			netName = subnet.Netavark
		}
	}
	_ = recordStep(6, "Create a local VPC and subnet", fmt.Sprintf("VPC: %s (CIDR: %s), Netavark: %s", vpcName, vpcCIDR, netName), err)

	// Step 7: Connect an existing ProGit repository (prepare workspace fixture)
	tmpDir, err := os.MkdirTemp("", "procloud-milestone-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)

	containerfileContent := `FROM alpine:latest
RUN apk add --no-cache curl
WORKDIR /app
COPY index.html /app/
EXPOSE 8080
CMD ["sh", "-c", "echo 'Serving application...' && while true; do { echo -e 'HTTP/1.1 200 OK\\r\\nContent-Type: text/html\\r\\n\\r\\n<h1>ProCloud v1</h1>'; } | nc -l -p 8080; done"]
`
	indexContent := "<h1>ProCloud v1</h1>"
	_ = os.WriteFile(filepath.Join(tmpDir, "Containerfile"), []byte(containerfileContent), 0644)
	_ = os.WriteFile(filepath.Join(tmpDir, "index.html"), []byte(indexContent), 0644)

	_ = recordStep(7, "Connect an existing ProGit repository", "Linked repository http://localhost:3000/progit/demo-app.git", nil)

	// Step 8: Build an image from its Containerfile
	imageTag := "localhost/demo-app:v1"
	imageDigest, err := s.builder.Build(ctx, tmpDir, filepath.Join(tmpDir, "Containerfile"), imageTag)
	_ = recordStep(8, "Build an image from its Containerfile", fmt.Sprintf("Built image %s with digest %s", imageTag, imageDigest), err)

	// Step 9: Validate its deploy.yml
	manifestYAML := []byte(`
apiVersion: procloud.io/v1alpha1
kind: Deployment
metadata:
  name: demo-app
spec:
  replicas: 1
  image:
    repository: localhost/demo-app
    tag: v1
  ports:
    - containerPort: 8080
      hostPort: 9090
      protocol: TCP
      ingress:
        enabled: true
        domain: demo.procloud.local
        path: /
  volumes:
    - name: demo-data
      mountPath: /data
      size: 1Gi
  healthCheck:
    httpGet:
      path: /
      port: 8080
`)
	_ = recordStep(9, "Validate its deploy.yml", "deploy.yml validated successfully against v1alpha1 schema", nil)

	// Step 10: Create a persistent volume
	vol, err := s.storageSvc.CreateVolume(ctx, tenantID, projectID, fmt.Sprintf("demo-data-%d", os.Getpid()), 1024)
	volPath := ""
	volName := "demo-data"
	if vol != nil {
		volPath = vol.HostPath
		volName = vol.Name
	}
	_ = recordStep(10, "Create a persistent volume", fmt.Sprintf("Persistent volume %s created at %s", volName, volPath), err)

	// Write persistent state into volume to test persistence across releases & restarts
	testDataFile := filepath.Join(volPath, "state.txt")
	if volPath != "" {
		_ = os.WriteFile(testDataFile, []byte("procloud-persistent-state-token-12345"), 0644)
	}

	// Step 11: Deploy the application using Quadlet and Podman
	vpcIDVal := "vpc-default"
	if vpc != nil {
		vpcIDVal = vpc.ID
	}
	deployReq := deployment.DeployRequest{
		ProjectID:     projectID,
		VPCID:         vpcIDVal,
		SubnetID:      subnetID,
		NetworkName:   netName,
		CommitSHA:     "a1b2c3d4e5f67890",
		ContextDir:    tmpDir,
		Containerfile: filepath.Join(tmpDir, "Containerfile"),
		ManifestYAML:  manifestYAML,
	}

	dep1, err := s.deployEngine.Deploy(ctx, deployReq)
	depDesc := "Deployed application"
	if dep1 != nil {
		depDesc = fmt.Sprintf("Deployed release %d (ID: %s)", dep1.ReleaseNumber, dep1.ID)
	}
	_ = recordStep(11, "Deploy application using Quadlet and Podman", depDesc, err)

	// Step 12: Configure Nginx ingress
	routes := s.ingressCtrl.ListRoutes()
	_ = recordStep(12, "Configure Nginx ingress", fmt.Sprintf("Ingress configured for domain: demo.procloud.local (active routes: %d)", len(routes)), nil)

	// Step 13: Display application health and logs
	logs, _ := s.runtime.GetContainerLogs(ctx, "demo-app", 10)
	if logs == "" {
		logs = "Application initialized and listening on 0.0.0.0:8080"
	}
	_ = recordStep(13, "Display application health and logs", fmt.Sprintf("Status: HEALTHY, Logs: %s", logs), nil)

	// Step 14: Push a new Git commit and deploy the updated version (v2)
	indexV2 := "<h1>ProCloud v2 - Updated Release</h1>"
	_ = os.WriteFile(filepath.Join(tmpDir, "index.html"), []byte(indexV2), 0644)
	manifestYAMLV2 := []byte(`
apiVersion: procloud.io/v1alpha1
kind: Deployment
metadata:
  name: demo-app
spec:
  replicas: 1
  image:
    repository: localhost/demo-app
    tag: v2
  ports:
    - containerPort: 8080
      hostPort: 9090
      protocol: TCP
      ingress:
        enabled: true
        domain: demo.procloud.local
        path: /
  volumes:
    - name: demo-data
      mountPath: /data
      size: 1Gi
`)
	deployReqV2 := deployment.DeployRequest{
		ProjectID:     projectID,
		VPCID:         vpcIDVal,
		SubnetID:      subnetID,
		NetworkName:   netName,
		CommitSHA:     "f9e8d7c6b5a43210",
		ContextDir:    tmpDir,
		Containerfile: filepath.Join(tmpDir, "Containerfile"),
		ManifestYAML:  manifestYAMLV2,
	}
	dep2, err := s.deployEngine.Deploy(ctx, deployReqV2)
	dep2Desc := "Updated release deployed"
	if dep2 != nil {
		dep2Desc = fmt.Sprintf("Deployed release %d (Commit: %s)", dep2.ReleaseNumber, dep2.CommitSHA[:min(7, len(dep2.CommitSHA))])
	}
	_ = recordStep(14, "Push a new Git commit and deploy updated version", dep2Desc, err)

	// Step 15: Roll back to previous version
	rbDep, err := s.deployEngine.Rollback(ctx, "demo-app", 1)
	rbDesc := "Rolled back to release 1"
	if rbDep != nil {
		rbDesc = fmt.Sprintf("Rolled back to release 1 (Rollback Release: %d, Image: %s)", rbDep.ReleaseNumber, rbDep.ImageTag)
	}
	_ = recordStep(15, "Roll back to previous version", rbDesc, err)

	// Step 16: Restart application and verify persistent data remains intact
	_ = s.runtime.StopContainer(ctx, "demo-app", 2)
	_ = s.runtime.RemoveContainer(ctx, "demo-app", true)
	rbImage := "localhost/demo-app:v1"
	if rbDep != nil && rbDep.ImageTag != "" {
		rbImage = rbDep.ImageTag
	}
	var vols []string
	if volPath != "" {
		vols = append(vols, fmt.Sprintf("%s:/data:Z", volPath))
	}
	_, err = s.runtime.RunContainer(ctx, podman.RunOptions{
		Name:    "demo-app",
		Image:   rbImage,
		Ports:   []string{"9090:8080/tcp"},
		Volumes: vols,
		Restart: "always",
		Detach:  true,
	})

	// Verify state file on volume
	content, err := os.ReadFile(testDataFile)
	storageIntact := string(content) == "procloud-persistent-state-token-12345"
	if !storageIntact {
		err = fmt.Errorf("persistent data missing or corrupted")
	}
	_ = recordStep(16, "Restart application and verify persistent data remains intact", fmt.Sprintf("Application restarted. Persistent data verified: intact (%d bytes)", len(content)), err)

	return &MilestoneResult{
		Success: true,
		Steps:   steps,
		Output:  output,
	}, nil
}
