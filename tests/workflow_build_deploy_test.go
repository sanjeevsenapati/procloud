package tests

import (
	"context"
	"path/filepath"
	"testing"

	"procloud.io/procloud/internal/database/sqlite"
	"procloud.io/procloud/internal/workflow"
	"procloud.io/procloud/pkg/podman"
	"procloud.io/procloud/pkg/protocol"
)

func TestBuildAndDeployWorkflow(t *testing.T) {
	ctx := context.Background()
	dbDir := t.TempDir()
	dbPath := filepath.Join(dbDir, "test_wf.db")

	db, err := sqlite.Open(sqlite.DefaultConfig(dbPath))
	if err != nil {
		t.Fatalf("failed opening sqlite db: %v", err)
	}
	defer db.Close()

	repo := sqlite.NewRepository(db)
	mockRuntime := podman.NewMockRuntime()
	engine := workflow.NewEngine(repo, mockRuntime)

	// 1. Test Container Workflow Execution
	t.Run("Execute Container Workflow", func(t *testing.T) {
		req := protocol.WorkflowRunRequest{
			Name:       "api-microservice",
			TargetType: "container",
			SourceType: "inline",
			SourceFiles: map[string]string{
				"server.js":    `console.log("Starting API server...");`,
				"package.json": `{"name": "api", "version": "1.0.0"}`,
			},
			Containerfile: `FROM docker.io/library/node:18-alpine
WORKDIR /app
COPY . .
EXPOSE 3000
CMD ["node", "server.js"]
`,
			DeployYAML: `apiVersion: procloud.io/v1alpha1
kind: Workload
metadata:
  name: api-microservice
spec:
  ports:
    - containerPort: 3000
      hostPort: 3000
`,
			ImageTag: "localhost/api-microservice:v1",
		}

		run, err := engine.Execute(ctx, req, "test-admin")
		if err != nil {
			t.Fatalf("Execute returned unexpected error: %v", err)
		}

		if run.Status != "SUCCESS" {
			t.Fatalf("expected status SUCCESS, got %s (error: %s)", run.Status, run.ErrorMessage)
		}
		if run.Stage != "completed" {
			t.Errorf("expected stage completed, got %s", run.Stage)
		}
		if run.TargetID == "" {
			t.Errorf("expected TargetID to be populated")
		}
		if run.TargetName != "api-microservice" {
			t.Errorf("expected TargetName api-microservice, got %s", run.TargetName)
		}

		// Verify image exists in runtime
		if _, ok := mockRuntime.Images["localhost/api-microservice:v1"]; !ok {
			t.Errorf("expected image to be registered in runtime")
		}

		// Verify container exists in runtime
		ctr, err := mockRuntime.InspectContainer(ctx, "api-microservice")
		if err != nil || ctr == nil {
			t.Errorf("expected container api-microservice to be created: %v", err)
		}

		// Verify persistence in SQLite
		persisted, err := engine.GetRun(ctx, run.ID)
		if err != nil || persisted == nil {
			t.Fatalf("failed retrieving workflow run from db: %v", err)
		}
		if persisted.Status != "SUCCESS" {
			t.Errorf("expected persisted status SUCCESS, got %s", persisted.Status)
		}
	})

	// 2. Test Pod Workflow Execution (Play Kube)
	t.Run("Execute Pod Workflow", func(t *testing.T) {
		req := protocol.WorkflowRunRequest{
			Name:       "web-cache-pod",
			TargetType: "pod",
			SourceType: "inline",
			SourceFiles: map[string]string{
				"app.py": `print("Running web app with redis cache")`,
			},
			Containerfile: `FROM docker.io/library/python:3.11-alpine
WORKDIR /app
COPY . .
CMD ["python", "app.py"]
`,
			DeployYAML: `apiVersion: v1
kind: Pod
metadata:
  name: web-cache-pod
  labels:
    app: web-cache
spec:
  restartPolicy: Always
  containers:
    - name: web-app
      image: localhost/web-app:latest
      ports:
        - containerPort: 8080
          hostPort: 8080
    - name: redis-cache
      image: docker.io/library/redis:alpine
      ports:
        - containerPort: 6379
          hostPort: 6379
`,
			ImageTag: "localhost/web-app:latest",
		}

		run, err := engine.Execute(ctx, req, "test-admin")
		if err != nil {
			t.Fatalf("Execute returned unexpected error: %v", err)
		}

		if run.Status != "SUCCESS" {
			t.Fatalf("expected status SUCCESS, got %s (error: %s)", run.Status, run.ErrorMessage)
		}
		if run.TargetType != "pod" {
			t.Errorf("expected target_type pod, got %s", run.TargetType)
		}

		// Verify pod exists in runtime
		pod, err := mockRuntime.InspectPod(ctx, "web-cache-pod")
		if err != nil || pod == nil {
			t.Errorf("expected pod web-cache-pod to be created: %v", err)
		}
	})

	// 3. Test Listing and Rerun
	t.Run("List and Rerun Workflows", func(t *testing.T) {
		list, err := engine.ListRuns(ctx, 10)
		if err != nil {
			t.Fatalf("failed listing runs: %v", err)
		}
		if len(list) < 2 {
			t.Fatalf("expected at least 2 runs, got %d", len(list))
		}

		// Rerun first run
		firstID := list[len(list)-1].ID
		rerun, err := engine.Rerun(ctx, firstID, "test-admin")
		if err != nil {
			t.Fatalf("failed rerunning workflow: %v", err)
		}
		if rerun.Status != "SUCCESS" {
			t.Errorf("expected rerun SUCCESS, got %s", rerun.Status)
		}

		// Delete a run
		err = engine.DeleteRun(ctx, rerun.ID)
		if err != nil {
			t.Fatalf("failed deleting run: %v", err)
		}
		_, err = engine.GetRun(ctx, rerun.ID)
		if err == nil {
			t.Errorf("expected error getting deleted run, got nil")
		}
	})
}
