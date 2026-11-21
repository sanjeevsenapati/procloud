package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"procloud.io/procloud/internal/audit"
	"procloud.io/procloud/internal/auth"
	"procloud.io/procloud/internal/build"
	"procloud.io/procloud/internal/compute"
	"procloud.io/procloud/internal/database/sqlite"
	"procloud.io/procloud/internal/deployment"
	"procloud.io/procloud/internal/ingress"
	"procloud.io/procloud/internal/job"
	"procloud.io/procloud/internal/monitoring"
	"procloud.io/procloud/internal/network"
	"procloud.io/procloud/internal/repository"
	"procloud.io/procloud/internal/scheduler"
	"procloud.io/procloud/internal/storage"
	"procloud.io/procloud/internal/tenant"
	"procloud.io/procloud/internal/vpc"
	"procloud.io/procloud/internal/workflow"
	"procloud.io/procloud/pkg/podman"
	"procloud.io/procloud/pkg/protocol"
	"procloud.io/procloud/web"
)

type Server struct {
	mux           *http.ServeMux
	db            *sqlite.DB
	userRepo      repository.UserRepository
	nodeRepo      repository.NodeRepository
	wlRepo        repository.WorkloadRepository
	depRepo       repository.DeploymentRepository
	authSvc       *auth.Service
	computeSvc    *compute.Service
	tenantSvc     *tenant.Service
	vpcSvc        *vpc.Service
	storageSvc    *storage.Service
	ingressCtrl   *ingress.Controller
	deployEngine  *deployment.Engine
	jobEngine     *job.Engine
	monitoringSvc *monitoring.Service
	auditSvc      *audit.Service
	runtime       podman.ContainerRuntime
	builder       build.ImageBuilder
	wfEngine      *workflow.Engine
}

func NewServer(
	db *sqlite.DB,
	repo *sqlite.Repository,
	runtime podman.ContainerRuntime,
) *Server {
	builder := build.NewPodmanImageBuilder(runtime)
	sched := scheduler.NewSingleNodeScheduler(repo)
	storageSvc := storage.NewService(repo, runtime, "")
	ingressCtrl := ingress.NewController("")
	deployEngine := deployment.NewEngine(repo, repo, builder, runtime, sched, storageSvc, ingressCtrl)

	jobEngine := job.NewEngine(repo, "api-worker-1")
	monitoringSvc := monitoring.NewService(db)
	auditSvc := audit.NewService(repo)
	authSvc := auth.NewService(repo, "procloud-jwt-secret-key-32bytes!")
	computeSvc := compute.NewService(repo, runtime)
	tenantSvc := tenant.NewService(repo)
	netProvider := network.NewNetavarkProvider(runtime)
	vpcSvc := vpc.NewService(repo, netProvider)
	wfEngine := workflow.NewEngine(repo, runtime)

	s := &Server{
		mux:           http.NewServeMux(),
		db:            db,
		userRepo:      repo,
		nodeRepo:      repo,
		wlRepo:        repo,
		depRepo:       repo,
		authSvc:       authSvc,
		computeSvc:    computeSvc,
		tenantSvc:     tenantSvc,
		vpcSvc:        vpcSvc,
		storageSvc:    storageSvc,
		ingressCtrl:   ingressCtrl,
		deployEngine:  deployEngine,
		jobEngine:     jobEngine,
		monitoringSvc: monitoringSvc,
		auditSvc:      auditSvc,
		runtime:       runtime,
		builder:       builder,
		wfEngine:      wfEngine,
	}

	// Ensure default administrator account exists (admin / admin123)
	if _, err := repo.GetUserByUsername(context.Background(), "admin"); err != nil {
		_, regErr := authSvc.Register(context.Background(), "admin", "admin@procloud.io", "admin123", "admin")
		if regErr != nil {
			_, regErr = authSvc.Register(context.Background(), "admin", "admin@procloud.local", "admin123", "admin")
		}
		if regErr != nil {
			log.Printf("[Auth] Notice: failed to register default admin: %v", regErr)
		} else {
			log.Printf("[Auth] Default admin account created: admin / admin123")
		}
	}

	s.registerRoutes()
	return s
}

// ServeHTTP implements http.Handler for Server.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

type claimsCtxKey struct{}

func (s *Server) authRequired(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims := s.checkAuth(r)
		if claims == nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error": "unauthorized: valid Bearer token required. Please log in.",
			})
			return
		}
		ctx := context.WithValue(r.Context(), claimsCtxKey{}, claims)
		next(w, r.WithContext(ctx))
	}
}

func getClaims(r *http.Request) *auth.Claims {
	if c, ok := r.Context().Value(claimsCtxKey{}).(*auth.Claims); ok {
		return c
	}
	return nil
}

func (s *Server) registerRoutes() {
	// Health & Status (Public)
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)

	// Auth & User Management (Login and Register are public; me is protected)
	s.mux.HandleFunc("POST /api/v1/auth/register", s.handleRegister)
	s.mux.HandleFunc("POST /api/v1/auth/login", s.handleLogin)
	s.mux.HandleFunc("GET /api/v1/auth/me", s.authRequired(s.handleMe))

	// Compute Nodes (Protected)
	s.mux.HandleFunc("GET /api/v1/nodes", s.authRequired(s.handleListNodes))
	s.mux.HandleFunc("POST /api/v1/nodes/register", s.authRequired(s.handleRegisterNode))

	// -------------------------------------------------------------
	// Podman Desktop Features: Container Provisioning & Lifecycle (Protected)
	// -------------------------------------------------------------
	s.mux.HandleFunc("GET /api/v1/containers", s.authRequired(s.handleListContainers))
	s.mux.HandleFunc("POST /api/v1/containers", s.authRequired(s.handleRunContainer))
	s.mux.HandleFunc("GET /api/v1/containers/{id}", s.authRequired(s.handleInspectContainer))
	s.mux.HandleFunc("POST /api/v1/containers/{id}/start", s.authRequired(s.handleStartContainer))
	s.mux.HandleFunc("POST /api/v1/containers/{id}/stop", s.authRequired(s.handleStopContainer))
	s.mux.HandleFunc("POST /api/v1/containers/{id}/restart", s.authRequired(s.handleRestartContainer))
	s.mux.HandleFunc("DELETE /api/v1/containers/{id}", s.authRequired(s.handleRemoveContainer))
	s.mux.HandleFunc("GET /api/v1/containers/{id}/logs", s.authRequired(s.handleContainerLogs))
	s.mux.HandleFunc("POST /api/v1/containers/{id}/exec", s.authRequired(s.handleExecContainer))
	s.mux.HandleFunc("GET /api/v1/containers/{id}/top", s.authRequired(s.handleTopContainer))
	s.mux.HandleFunc("GET /api/v1/containers/{id}/stats", s.authRequired(s.handleStatsContainer))
	s.mux.HandleFunc("GET /api/v1/containers/{id}/containerfile", s.authRequired(s.handleContainerfile))
	s.mux.HandleFunc("GET /api/v1/containers/{id}/kube", s.authRequired(s.handleContainerKube))

	// -------------------------------------------------------------
	// Podman Desktop Features: Pod Provisioning & Lifecycle (Protected)
	// -------------------------------------------------------------
	s.mux.HandleFunc("GET /api/v1/pods", s.authRequired(s.handleListPods))
	s.mux.HandleFunc("POST /api/v1/pods", s.authRequired(s.handleCreatePod))
	s.mux.HandleFunc("GET /api/v1/pods/{id}", s.authRequired(s.handleInspectPod))
	s.mux.HandleFunc("POST /api/v1/pods/{id}/start", s.authRequired(s.handleStartPod))
	s.mux.HandleFunc("POST /api/v1/pods/{id}/stop", s.authRequired(s.handleStopPod))
	s.mux.HandleFunc("POST /api/v1/pods/{id}/restart", s.authRequired(s.handleRestartPod))
	s.mux.HandleFunc("DELETE /api/v1/pods/{id}", s.authRequired(s.handleRemovePod))
	s.mux.HandleFunc("GET /api/v1/pods/{id}/kube", s.authRequired(s.handleGenerateKube))
	s.mux.HandleFunc("POST /api/v1/pods/kube/play", s.authRequired(s.handlePlayKube))
	s.mux.HandleFunc("GET /api/v1/pods/{id}/top", s.authRequired(s.handleTopPod))
	s.mux.HandleFunc("GET /api/v1/pods/{id}/logs", s.authRequired(s.handlePodLogs))

	// -------------------------------------------------------------
	// Podman Desktop Features: Image Management (Protected)
	// -------------------------------------------------------------
	s.mux.HandleFunc("GET /api/v1/images", s.authRequired(s.handleListImages))
	s.mux.HandleFunc("POST /api/v1/images/pull", s.authRequired(s.handlePullImage))
	s.mux.HandleFunc("POST /api/v1/images/build", s.authRequired(s.handleBuildImage))
	s.mux.HandleFunc("DELETE /api/v1/images/{id}", s.authRequired(s.handleRemoveImage))

	// -------------------------------------------------------------
	// Podman Desktop Features: Runtime Networks & Volumes (Protected)
	// -------------------------------------------------------------
	s.mux.HandleFunc("GET /api/v1/podman/networks", s.authRequired(s.handleListPodmanNetworks))
	s.mux.HandleFunc("POST /api/v1/podman/networks", s.authRequired(s.handleCreatePodmanNetwork))
	s.mux.HandleFunc("DELETE /api/v1/podman/networks/{id}", s.authRequired(s.handleRemovePodmanNetwork))

	s.mux.HandleFunc("GET /api/v1/podman/volumes", s.authRequired(s.handleListPodmanVolumes))
	s.mux.HandleFunc("POST /api/v1/podman/volumes", s.authRequired(s.handleCreatePodmanVolume))
	s.mux.HandleFunc("DELETE /api/v1/podman/volumes/{id}", s.authRequired(s.handleRemovePodmanVolume))

	// Tenants & Projects (Protected)
	s.mux.HandleFunc("GET /api/v1/tenants", s.authRequired(s.handleListTenants))
	s.mux.HandleFunc("POST /api/v1/tenants", s.authRequired(s.handleCreateTenant))
	s.mux.HandleFunc("GET /api/v1/projects", s.authRequired(s.handleListProjects))
	s.mux.HandleFunc("POST /api/v1/projects", s.authRequired(s.handleCreateProject))

	// VPC & Storage Repositories (Protected)
	s.mux.HandleFunc("GET /api/v1/vpcs", s.authRequired(s.handleListVPCs))
	s.mux.HandleFunc("POST /api/v1/vpcs", s.authRequired(s.handleCreateVPC))
	s.mux.HandleFunc("GET /api/v1/subnets", s.authRequired(s.handleListSubnets))
	s.mux.HandleFunc("POST /api/v1/subnets", s.authRequired(s.handleCreateSubnet))
	s.mux.HandleFunc("GET /api/v1/volumes", s.authRequired(s.handleListVolumes))
	s.mux.HandleFunc("POST /api/v1/volumes", s.authRequired(s.handleCreateVolume))

	// Workloads & Deployments (Protected)
	s.mux.HandleFunc("GET /api/v1/workloads", s.authRequired(s.handleListWorkloads))
	s.mux.HandleFunc("POST /api/v1/deployments", s.authRequired(s.handleCreateDeployment))
	s.mux.HandleFunc("POST /api/v1/deployments/{workload}/rollback", s.authRequired(s.handleRollback))

	// Database & WAL Management (Protected)
	s.mux.HandleFunc("GET /api/v1/database/status", s.authRequired(s.handleDBStatus))
	s.mux.HandleFunc("POST /api/v1/database/checkpoint", s.authRequired(s.handleDBCheckpoint))
	s.mux.HandleFunc("POST /api/v1/database/backup", s.authRequired(s.handleDBBackup))

	// Background Jobs & Audit (Protected)
	s.mux.HandleFunc("GET /api/v1/jobs", s.authRequired(s.handleListJobs))
	s.mux.HandleFunc("GET /api/v1/audit", s.authRequired(s.handleListAudit))

	// Build & Deploy Workflow Engine (Protected)
	s.mux.HandleFunc("POST /api/v1/workflows/run", s.authRequired(s.handleRunWorkflow))
	s.mux.HandleFunc("GET /api/v1/workflows/runs", s.authRequired(s.handleListWorkflowRuns))
	s.mux.HandleFunc("GET /api/v1/workflows/runs/{id}", s.authRequired(s.handleGetWorkflowRun))
	s.mux.HandleFunc("DELETE /api/v1/workflows/runs/{id}", s.authRequired(s.handleDeleteWorkflowRun))
	s.mux.HandleFunc("POST /api/v1/workflows/runs/{id}/rerun", s.authRequired(s.handleRerunWorkflow))

	// Webhooks & Milestone
	s.mux.HandleFunc("POST /api/v1/webhooks/progit", s.handleProGitWebhook)
	s.mux.HandleFunc("POST /api/v1/milestone/run", s.authRequired(s.handleRunMilestone))

	// Static Web UI Console (Podman Desktop + Sovereign Cloud)
	s.mux.Handle("/", web.Handler())
}

// -------------------------------------------------------------
// Authentication Helper & Handlers
// -------------------------------------------------------------

func (s *Server) checkAuth(r *http.Request) *auth.Claims {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return nil
	}
	token := strings.TrimPrefix(authHeader, "Bearer ")
	claims, err := s.authSvc.VerifyToken(token)
	if err != nil {
		return nil
	}
	return claims
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]string{"status": "ok", "app": "procloud", "version": "1.0.0"})
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Email    string `json:"email"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	u, err := s.authSvc.Register(r.Context(), req.Username, req.Email, req.Password, req.Role)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.auditSvc.Log(r.Context(), u.Username, "USER_REGISTERED", "User", u.ID, r.RemoteAddr, "Created account")
	respondJSON(w, http.StatusCreated, u)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	token, user, err := s.authSvc.Login(r.Context(), req.Username, req.Password)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	s.auditSvc.Log(r.Context(), user.Username, "USER_LOGIN", "User", user.ID, r.RemoteAddr, "Successful login")
	respondJSON(w, http.StatusOK, map[string]any{"token": token, "user": user})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	claims := s.checkAuth(r)
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	u, err := s.userRepo.GetUserByID(r.Context(), claims.UserID)
	if err != nil {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	respondJSON(w, http.StatusOK, u)
}

// -------------------------------------------------------------
// Podman Desktop: Container Provisioning & Management
// -------------------------------------------------------------

func (s *Server) handleListContainers(w http.ResponseWriter, r *http.Request) {
	all := r.URL.Query().Get("all") == "true" || r.URL.Query().Get("all") == "1"
	containers, err := s.runtime.ListContainers(r.Context(), all)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, containers)
}

func (s *Server) handleRunContainer(w http.ResponseWriter, r *http.Request) {
	var opts podman.RunOptions
	if err := json.NewDecoder(r.Body).Decode(&opts); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if opts.Image == "" {
		http.Error(w, "image is required", http.StatusBadRequest)
		return
	}
	opts.Detach = true

	ctrID, err := s.runtime.RunContainer(r.Context(), opts)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	actor := "anonymous"
	if claims := s.checkAuth(r); claims != nil {
		actor = claims.Username
	}
	s.auditSvc.Log(r.Context(), actor, "CONTAINER_RUN", "Container", opts.Name, r.RemoteAddr, fmt.Sprintf("Started container %s with image %s", opts.Name, opts.Image))

	respondJSON(w, http.StatusCreated, map[string]string{"id": ctrID, "name": opts.Name, "status": "running"})
}

func (s *Server) handleInspectContainer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	status, err := s.runtime.InspectContainer(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	respondJSON(w, http.StatusOK, status)
}

func (s *Server) handleStartContainer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.runtime.StartContainer(r.Context(), id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"message": "container started", "id": id})
}

func (s *Server) handleStopContainer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.runtime.StopContainer(r.Context(), id, 5); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"message": "container stopped", "id": id})
}

func (s *Server) handleRestartContainer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.runtime.RestartContainer(r.Context(), id, 5); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"message": "container restarted", "id": id})
}

func (s *Server) handleRemoveContainer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	force := r.URL.Query().Get("force") == "true"
	if err := s.runtime.RemoveContainer(r.Context(), id, force); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"message": "container removed", "id": id})
}

func (s *Server) handleContainerLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	tailStr := r.URL.Query().Get("tail")
	tail := 50
	if tailStr != "" {
		tail, _ = strconv.Atoi(tailStr)
	}
	logs, err := s.runtime.GetContainerLogs(r.Context(), id, tail)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"logs": logs})
}

func (s *Server) handleExecContainer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Command []string `json:"command"`
		Cmd     []string `json:"cmd"`
		Raw     string   `json:"raw"`
		Shell   string   `json:"shell"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusOK, map[string]interface{}{
			"error":     "invalid request body",
			"output":    "Error: invalid request body",
			"exit_code": 1,
		})
		return
	}

	// Pre-flight check: Container state
	inspect, inspectErr := s.runtime.InspectContainer(r.Context(), id)
	if inspectErr == nil && inspect != nil {
		state := strings.ToLower(inspect.Status)
		if state != "running" && !inspect.Running {
			cName := inspect.Name
			if cName == "" {
				cName = id
			}
			respondJSON(w, http.StatusOK, map[string]interface{}{
				"error":     fmt.Sprintf("container '%s' is not running (state: %s)", cName, inspect.Status),
				"output":    fmt.Sprintf("Error: cannot open shell session in container '%s' because it is not running (State: %s).\nPlease click 'Start Container' to launch it.", cName, inspect.Status),
				"state":     inspect.Status,
				"exit_code": 125,
			})
			return
		}
	}

	rawCmd := strings.TrimSpace(req.Raw)
	var command []string

	if len(req.Command) > 0 {
		command = req.Command
	} else if len(req.Cmd) > 0 {
		command = req.Cmd
	} else if rawCmd != "" {
		shell := strings.TrimSpace(req.Shell)
		if shell == "" {
			shell = "/bin/sh"
		}
		command = []string{shell, "-c", rawCmd}
	} else {
		respondJSON(w, http.StatusOK, map[string]interface{}{
			"error":     "command is required",
			"output":    "Error: no command provided",
			"exit_code": 1,
		})
		return
	}

	out, err := s.runtime.ExecContainer(r.Context(), id, command)

	// Fallback strategy if shell binary wasn't found in container image
	if err != nil && rawCmd != "" {
		// If /bin/bash or chosen shell failed with executable not found, try /bin/sh
		if req.Shell != "" && req.Shell != "/bin/sh" {
			if fbOut, fbErr := s.runtime.ExecContainer(r.Context(), id, []string{"/bin/sh", "-c", rawCmd}); fbErr == nil {
				out = fbOut
				err = nil
				command = []string{"/bin/sh", "-c", rawCmd}
			}
		}
		// If still failing, try "sh" without path
		if err != nil {
			if fbOut, fbErr := s.runtime.ExecContainer(r.Context(), id, []string{"sh", "-c", rawCmd}); fbErr == nil {
				out = fbOut
				err = nil
				command = []string{"sh", "-c", rawCmd}
			}
		}
		// If still failing, try direct command invocation (e.g. ls -la)
		if err != nil {
			parts := strings.Fields(rawCmd)
			if len(parts) > 0 {
				if fbOut, fbErr := s.runtime.ExecContainer(r.Context(), id, parts); fbErr == nil {
					out = fbOut
					err = nil
					command = parts
				}
			}
		}
	}

	exitCode := 0
	var errStr string
	if err != nil {
		errStr = err.Error()
		exitCode = 1
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
		if strings.TrimSpace(out) == "" {
			out = errStr
		}
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"output":    out,
		"error":     errStr,
		"exit_code": exitCode,
		"command":   command,
	})
}

func (s *Server) handleTopContainer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	procs, err := s.runtime.TopContainer(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, procs)
}

func (s *Server) handleStatsContainer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	stats, err := s.runtime.ContainerStats(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, stats)
}

func (s *Server) handleContainerfile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	status, err := s.runtime.InspectContainer(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	baseImg := status.Image
	if baseImg == "" {
		baseImg = "scratch"
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# Containerfile generated by ProCloud Container Engine\n"))
	sb.WriteString(fmt.Sprintf("# Source Container: %s (ID: %s)\n", status.Name, status.ID))
	sb.WriteString(fmt.Sprintf("# Extracted At: %s\n\n", time.Now().Format(time.RFC3339)))
	sb.WriteString(fmt.Sprintf("FROM %s\n\n", baseImg))

	if status.User != "" && status.User != "0" {
		sb.WriteString(fmt.Sprintf("USER %s\n", status.User))
	}

	if status.WorkingDir != "" {
		sb.WriteString(fmt.Sprintf("WORKDIR %s\n", status.WorkingDir))
	}

	if len(status.Env) > 0 {
		sb.WriteString("\n# Environment Configuration\n")
		for _, e := range status.Env {
			parts := strings.SplitN(e, "=", 2)
			if len(parts) == 2 {
				sb.WriteString(fmt.Sprintf("ENV %s=\"%s\"\n", parts[0], parts[1]))
			}
		}
	}

	if len(status.Ports) > 0 {
		sb.WriteString("\n# Exposed Ports\n")
		for _, p := range status.Ports {
			cPort := p
			if strings.Contains(p, ":") {
				parts := strings.Split(p, ":")
				cPort = parts[len(parts)-1]
			}
			sb.WriteString(fmt.Sprintf("EXPOSE %s\n", cPort))
		}
	}

	if len(status.Mounts) > 0 {
		sb.WriteString("\n# Declared Storage Mounts\n")
		for _, m := range status.Mounts {
			sb.WriteString(fmt.Sprintf("VOLUME [\"%s\"]\n", m.Destination))
		}
	}

	if len(status.Entrypoint) > 0 {
		var epQuoted []string
		for _, ep := range status.Entrypoint {
			epQuoted = append(epQuoted, fmt.Sprintf("%q", ep))
		}
		sb.WriteString(fmt.Sprintf("\nENTRYPOINT [%s]\n", strings.Join(epQuoted, ", ")))
	}

	if len(status.Command) > 0 {
		var cmdQuoted []string
		for _, c := range status.Command {
			cmdQuoted = append(cmdQuoted, fmt.Sprintf("%q", c))
		}
		sb.WriteString(fmt.Sprintf("CMD [%s]\n", strings.Join(cmdQuoted, ", ")))
	}

	respondJSON(w, http.StatusOK, map[string]string{
		"container_id":   status.ID,
		"container_name": status.Name,
		"containerfile":  sb.String(),
	})
}

func (s *Server) handleContainerKube(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	targetID := id
	if status, err := s.runtime.InspectContainer(r.Context(), id); err == nil && status.Pod != "" {
		targetID = status.Pod
	}

	yaml, err := s.runtime.GenerateKube(r.Context(), targetID)
	if err != nil {
		if status, inspErr := s.runtime.InspectContainer(r.Context(), id); inspErr == nil {
			yaml = fmt.Sprintf("apiVersion: v1\nkind: Pod\nmetadata:\n  name: %s\nspec:\n  containers:\n  - name: %s\n    image: %s\n", status.Name, status.Name, status.Image)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	respondJSON(w, http.StatusOK, map[string]string{"kube_yaml": yaml})
}

// -------------------------------------------------------------
// Podman Desktop: Pod Provisioning & Lifecycle
// -------------------------------------------------------------

func (s *Server) handleListPods(w http.ResponseWriter, r *http.Request) {
	pods, err := s.runtime.ListPods(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, pods)
}

func (s *Server) handleCreatePod(w http.ResponseWriter, r *http.Request) {
	var opts podman.PodCreateOptions
	if err := json.NewDecoder(r.Body).Decode(&opts); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if opts.Name == "" {
		http.Error(w, "pod name is required", http.StatusBadRequest)
		return
	}

	podID, err := s.runtime.CreatePod(r.Context(), opts)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	actor := "anonymous"
	if claims := s.checkAuth(r); claims != nil {
		actor = claims.Username
	}
	s.auditSvc.Log(r.Context(), actor, "POD_CREATED", "Pod", opts.Name, r.RemoteAddr, fmt.Sprintf("Created pod %s", opts.Name))

	respondJSON(w, http.StatusCreated, map[string]string{"id": podID, "name": opts.Name, "status": "Created"})
}

func (s *Server) handleInspectPod(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	inspect, err := s.runtime.InspectPod(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	respondJSON(w, http.StatusOK, inspect)
}

func (s *Server) handleStartPod(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.runtime.StartPod(r.Context(), id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"message": "pod started", "id": id})
}

func (s *Server) handleStopPod(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.runtime.StopPod(r.Context(), id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"message": "pod stopped", "id": id})
}

func (s *Server) handleRestartPod(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.runtime.RestartPod(r.Context(), id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"message": "pod restarted", "id": id})
}

func (s *Server) handleRemovePod(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	force := r.URL.Query().Get("force") == "true"
	if err := s.runtime.RemovePod(r.Context(), id, force); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"message": "pod removed", "id": id})
}

func (s *Server) handleGenerateKube(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	yaml, err := s.runtime.GenerateKube(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"kube_yaml": yaml})
}

func (s *Server) handlePlayKube(w http.ResponseWriter, r *http.Request) {
	var yamlStr string
	contentType := r.Header.Get("Content-Type")
	if strings.Contains(contentType, "application/json") {
		var req struct {
			KubeYAML string `json:"kube_yaml"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil && req.KubeYAML != "" {
			yamlStr = req.KubeYAML
		}
	}
	if yamlStr == "" {
		bodyBytes, err := io.ReadAll(r.Body)
		if err == nil {
			yamlStr = string(bodyBytes)
		}
	}
	if strings.TrimSpace(yamlStr) == "" {
		http.Error(w, "Kubernetes YAML manifest is required", http.StatusBadRequest)
		return
	}

	result, err := s.runtime.PlayKube(r.Context(), yamlStr)
	if err != nil {
		http.Error(w, "play kube failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	actor := "anonymous"
	if claims := s.checkAuth(r); claims != nil {
		actor = claims.Username
	}
	s.auditSvc.Log(r.Context(), actor, "KUBE_PLAY", "Pod", fmt.Sprintf("Pods: %v", result.Pods), r.RemoteAddr, "Deployed Podman workloads via Kubernetes YAML manifest")

	respondJSON(w, http.StatusOK, result)
}

func (s *Server) handleTopPod(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	procs, err := s.runtime.TopPod(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, procs)
}

func (s *Server) handlePodLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	container := r.URL.Query().Get("container")
	tailStr := r.URL.Query().Get("tail")
	tail := 50
	if tailStr != "" {
		tail, _ = strconv.Atoi(tailStr)
	}
	logs, err := s.runtime.GetPodLogs(r.Context(), id, container, tail)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"logs": logs})
}

// -------------------------------------------------------------
// Podman Desktop: Image Catalog & Registry
// -------------------------------------------------------------

func (s *Server) handleListImages(w http.ResponseWriter, r *http.Request) {
	images, err := s.runtime.ListImages(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, images)
}

func (s *Server) handlePullImage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Image string `json:"image"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Image == "" {
		http.Error(w, "image name is required", http.StatusBadRequest)
		return
	}
	if err := s.runtime.PullImage(r.Context(), req.Image); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"message": "image pulled successfully", "image": req.Image})
}

func (s *Server) handleBuildImage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Tag           string `json:"tag"`
		Containerfile string `json:"containerfile"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Tag == "" || req.Containerfile == "" {
		http.Error(w, "tag and containerfile are required", http.StatusBadRequest)
		return
	}

	tmpDir, err := os.MkdirTemp("", "procloud-build-*")
	if err != nil {
		http.Error(w, "failed to create temp context dir: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer os.RemoveAll(tmpDir)

	cfilePath := filepath.Join(tmpDir, "Containerfile")
	if err := os.WriteFile(cfilePath, []byte(req.Containerfile), 0644); err != nil {
		http.Error(w, "failed to write Containerfile: "+err.Error(), http.StatusInternalServerError)
		return
	}

	digest, err := s.runtime.BuildImage(r.Context(), tmpDir, cfilePath, req.Tag)
	if err != nil {
		http.Error(w, "image build failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	actor := "anonymous"
	if claims := s.checkAuth(r); claims != nil {
		actor = claims.Username
	}
	s.auditSvc.Log(r.Context(), actor, "IMAGE_BUILD", "Image", req.Tag, r.RemoteAddr, fmt.Sprintf("Built image %s with digest %s", req.Tag, digest))

	respondJSON(w, http.StatusCreated, map[string]string{
		"tag":    req.Tag,
		"digest": digest,
		"status": "built",
	})
}

func (s *Server) handleRemoveImage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	force := r.URL.Query().Get("force") == "true"
	if err := s.runtime.RemoveImage(r.Context(), id, force); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"message": "image removed", "id": id})
}

// -------------------------------------------------------------
// Podman Desktop: Runtime Networks & Volumes
// -------------------------------------------------------------

func (s *Server) handleListPodmanNetworks(w http.ResponseWriter, r *http.Request) {
	nets, err := s.runtime.ListNetworks(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, nets)
}

func (s *Server) handleCreatePodmanNetwork(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string `json:"name"`
		Subnet  string `json:"subnet"`
		Gateway string `json:"gateway"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		http.Error(w, "network name is required", http.StatusBadRequest)
		return
	}
	if err := s.runtime.CreateNetwork(r.Context(), req.Name, req.Subnet, req.Gateway); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusCreated, map[string]string{"message": "network created", "name": req.Name})
}

func (s *Server) handleRemovePodmanNetwork(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.runtime.RemoveNetwork(r.Context(), id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"message": "network removed", "name": id})
}

func (s *Server) handleListPodmanVolumes(w http.ResponseWriter, r *http.Request) {
	vols, err := s.runtime.ListVolumes(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, vols)
}

func (s *Server) handleCreatePodmanVolume(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		http.Error(w, "volume name is required", http.StatusBadRequest)
		return
	}
	path, err := s.runtime.CreateVolume(r.Context(), req.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusCreated, map[string]string{"message": "volume created", "name": req.Name, "path": path})
}

func (s *Server) handleRemovePodmanVolume(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.runtime.RemoveVolume(r.Context(), id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"message": "volume removed", "name": id})
}

// -------------------------------------------------------------
// Existing Infrastructure & Workload Handlers
// -------------------------------------------------------------

func (s *Server) handleListNodes(w http.ResponseWriter, r *http.Request) {
	node, err := s.computeSvc.GetPrimaryNode(r.Context())
	if err != nil {
		respondJSON(w, http.StatusOK, []any{})
		return
	}
	respondJSON(w, http.StatusOK, []any{node})
}

func (s *Server) handleRegisterNode(w http.ResponseWriter, r *http.Request) {
	node, err := s.computeSvc.RegisterLocalHost(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, node)
}

func (s *Server) handleListTenants(w http.ResponseWriter, r *http.Request) {
	list, err := s.tenantSvc.ListTenants(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, list)
}

func (s *Server) handleCreateTenant(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	t, err := s.tenantSvc.CreateTenant(r.Context(), req.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	respondJSON(w, http.StatusCreated, t)
}

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	tenantID := r.URL.Query().Get("tenant_id")
	list, err := s.tenantSvc.ListProjects(r.Context(), tenantID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, list)
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TenantID      string `json:"tenant_id"`
		Name          string `json:"name"`
		RepositoryURL string `json:"repository_url"`
		Branch        string `json:"branch"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	p, err := s.tenantSvc.CreateProject(r.Context(), req.TenantID, req.Name, req.RepositoryURL, req.Branch)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	respondJSON(w, http.StatusCreated, p)
}

func (s *Server) handleListVPCs(w http.ResponseWriter, r *http.Request) {
	tenantID := r.URL.Query().Get("tenant_id")
	vpcs, err := s.vpcSvc.ListVPCs(r.Context(), tenantID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, vpcs)
}

func (s *Server) handleCreateVPC(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TenantID string `json:"tenant_id"`
		Name     string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	v, err := s.vpcSvc.CreateVPC(r.Context(), req.TenantID, req.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	respondJSON(w, http.StatusCreated, v)
}

func (s *Server) handleListSubnets(w http.ResponseWriter, r *http.Request) {
	vpcID := r.URL.Query().Get("vpc_id")
	subs, err := s.vpcSvc.ListSubnets(r.Context(), vpcID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, subs)
}

func (s *Server) handleCreateSubnet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		VPCID string `json:"vpc_id"`
		Name  string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	sub, err := s.vpcSvc.CreateSubnet(r.Context(), req.VPCID, req.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	respondJSON(w, http.StatusCreated, sub)
}

func (s *Server) handleListVolumes(w http.ResponseWriter, r *http.Request) {
	tenantID := r.URL.Query().Get("tenant_id")
	vols, err := s.storageSvc.ListVolumes(r.Context(), tenantID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, vols)
}

func (s *Server) handleCreateVolume(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TenantID  string `json:"tenant_id"`
		ProjectID string `json:"project_id"`
		Name      string `json:"name"`
		SizeMB    int64  `json:"size_mb"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	v, err := s.storageSvc.CreateVolume(r.Context(), req.TenantID, req.ProjectID, req.Name, req.SizeMB)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	respondJSON(w, http.StatusCreated, v)
}

func (s *Server) handleListWorkloads(w http.ResponseWriter, r *http.Request) {
	wls, err := s.wlRepo.ListAllWorkloads(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, wls)
}

func (s *Server) handleCreateDeployment(w http.ResponseWriter, r *http.Request) {
	var req deployment.DeployRequest
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	req.ManifestYAML = body

	dep, err := s.deployEngine.Deploy(r.Context(), req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusCreated, dep)
}

func (s *Server) handleRollback(w http.ResponseWriter, r *http.Request) {
	workload := r.PathValue("workload")
	targetReleaseStr := r.URL.Query().Get("release")
	targetRelease, _ := strconv.Atoi(targetReleaseStr)

	dep, err := s.deployEngine.Rollback(r.Context(), workload, targetRelease)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, dep)
}

func (s *Server) handleDBStatus(w http.ResponseWriter, r *http.Request) {
	status, err := s.monitoringSvc.GetDatabaseStatus(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, status)
}

func (s *Server) handleDBCheckpoint(w http.ResponseWriter, r *http.Request) {
	mode := r.URL.Query().Get("mode")
	if mode == "" {
		mode = "TRUNCATE"
	}
	err := s.monitoringSvc.TriggerCheckpoint(r.Context(), mode)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"message": "checkpoint triggered", "mode": mode})
}

func (s *Server) handleDBBackup(w http.ResponseWriter, r *http.Request) {
	dest := fmt.Sprintf("backups/procloud-hot-backup-%d.db", time.Now().Unix())
	err := s.monitoringSvc.BackupDatabase(r.Context(), dest)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"message": "backup completed", "path": dest})
}

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.jobEngine.ListRecentJobs(r.Context(), 50)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, jobs)
}

func (s *Server) handleListAudit(w http.ResponseWriter, r *http.Request) {
	events, err := s.auditSvc.ListRecent(r.Context(), 50)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, events)
}

func (s *Server) handleProGitWebhook(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Ref        string `json:"ref"`
		After      string `json:"after"`
		Repository struct {
			Name     string `json:"name"`
			CloneURL string `json:"clone_url"`
		} `json:"repository"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	commitSHA := payload.After
	if commitSHA == "" {
		commitSHA = fmt.Sprintf("sha-%d", time.Now().Unix())
	}

	job, err := s.jobEngine.Enqueue(r.Context(), "build_and_deploy", map[string]string{
		"repo":   payload.Repository.Name,
		"commit": commitSHA,
	}, fmt.Sprintf("git-push-%s-%s", payload.Repository.Name, commitSHA))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	respondJSON(w, http.StatusAccepted, map[string]any{"job_id": job.ID, "status": "QUEUED"})
}

func (s *Server) handleRunMilestone(w http.ResponseWriter, r *http.Request) {
	res, err := s.RunMilestone1(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, res)
}

// -------------------------------------------------------------
// Build & Deploy Workflow Handlers
// -------------------------------------------------------------

func (s *Server) handleRunWorkflow(w http.ResponseWriter, r *http.Request) {
	var req protocol.WorkflowRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request payload: "+err.Error(), http.StatusBadRequest)
		return
	}

	claims := getClaims(r)
	actor := "system"
	if claims != nil && claims.Username != "" {
		actor = claims.Username
	}

	run, err := s.wfEngine.Execute(r.Context(), req, actor)
	if err != nil {
		http.Error(w, "workflow execution failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	s.auditSvc.Log(r.Context(), actor, "WORKFLOW_RUN", "workflow", run.ID, r.RemoteAddr, fmt.Sprintf("target=%s, status=%s", run.TargetType, run.Status))

	respondJSON(w, http.StatusOK, run)
}

func (s *Server) handleListWorkflowRuns(w http.ResponseWriter, r *http.Request) {
	limitStr := r.URL.Query().Get("limit")
	limit := 100
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			limit = l
		}
	}

	runs, err := s.wfEngine.ListRuns(r.Context(), limit)
	if err != nil {
		http.Error(w, "failed to list workflow runs: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if runs == nil {
		runs = []*protocol.WorkflowRun{}
	}
	respondJSON(w, http.StatusOK, runs)
}

func (s *Server) handleGetWorkflowRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "missing workflow id", http.StatusBadRequest)
		return
	}

	run, err := s.wfEngine.GetRun(r.Context(), id)
	if err != nil {
		http.Error(w, "workflow run not found: "+err.Error(), http.StatusNotFound)
		return
	}
	respondJSON(w, http.StatusOK, run)
}

func (s *Server) handleDeleteWorkflowRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "missing workflow id", http.StatusBadRequest)
		return
	}

	if err := s.wfEngine.DeleteRun(r.Context(), id); err != nil {
		http.Error(w, "failed to delete workflow run: "+err.Error(), http.StatusInternalServerError)
		return
	}

	claims := getClaims(r)
	actor := "system"
	if claims != nil && claims.Username != "" {
		actor = claims.Username
	}
	s.auditSvc.Log(r.Context(), actor, "WORKFLOW_DELETE", "workflow", id, r.RemoteAddr, "")

	respondJSON(w, http.StatusOK, map[string]string{"status": "deleted", "id": id})
}

func (s *Server) handleRerunWorkflow(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "missing workflow id", http.StatusBadRequest)
		return
	}

	claims := getClaims(r)
	actor := "system"
	if claims != nil && claims.Username != "" {
		actor = claims.Username
	}

	run, err := s.wfEngine.Rerun(r.Context(), id, actor)
	if err != nil {
		http.Error(w, "failed to rerun workflow: "+err.Error(), http.StatusInternalServerError)
		return
	}

	s.auditSvc.Log(r.Context(), actor, "WORKFLOW_RERUN", "workflow", run.ID, r.RemoteAddr, fmt.Sprintf("rerun_of=%s, status=%s", id, run.Status))

	respondJSON(w, http.StatusOK, run)
}

func respondJSON(w http.ResponseWriter, code int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(payload)
}
