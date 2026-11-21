package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
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
	"procloud.io/procloud/pkg/podman"
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

	// -------------------------------------------------------------
	// Podman Desktop Features: Image Management (Protected)
	// -------------------------------------------------------------
	s.mux.HandleFunc("GET /api/v1/images", s.authRequired(s.handleListImages))
	s.mux.HandleFunc("POST /api/v1/images/pull", s.authRequired(s.handlePullImage))
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
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Command) == 0 {
		http.Error(w, "command array is required", http.StatusBadRequest)
		return
	}
	out, err := s.runtime.ExecContainer(r.Context(), id, req.Command)
	if err != nil {
		http.Error(w, fmt.Sprintf("%v: %s", err, out), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"output": out})
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

func respondJSON(w http.ResponseWriter, code int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(payload)
}
