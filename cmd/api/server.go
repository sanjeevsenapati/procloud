package main

import (
	"encoding/json"
	"fmt"
	"io"
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

	s.registerRoutes()
	return s
}

func (s *Server) registerRoutes() {
	// Health & Status
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)

	// Auth
	s.mux.HandleFunc("POST /api/v1/auth/register", s.handleRegister)
	s.mux.HandleFunc("POST /api/v1/auth/login", s.handleLogin)
	s.mux.HandleFunc("GET /api/v1/auth/me", s.handleMe)

	// Nodes
	s.mux.HandleFunc("GET /api/v1/nodes", s.handleListNodes)
	s.mux.HandleFunc("POST /api/v1/nodes/register", s.handleRegisterNode)

	// Tenants & Projects
	s.mux.HandleFunc("GET /api/v1/tenants", s.handleListTenants)
	s.mux.HandleFunc("POST /api/v1/tenants", s.handleCreateTenant)
	s.mux.HandleFunc("GET /api/v1/projects", s.handleListProjects)
	s.mux.HandleFunc("POST /api/v1/projects", s.handleCreateProject)

	// Networking
	s.mux.HandleFunc("GET /api/v1/vpcs", s.handleListVPCs)
	s.mux.HandleFunc("POST /api/v1/vpcs", s.handleCreateVPC)
	s.mux.HandleFunc("GET /api/v1/subnets", s.handleListSubnets)
	s.mux.HandleFunc("POST /api/v1/subnets", s.handleCreateSubnet)

	// Storage
	s.mux.HandleFunc("GET /api/v1/volumes", s.handleListVolumes)
	s.mux.HandleFunc("POST /api/v1/volumes", s.handleCreateVolume)

	// Workloads & Deployments
	s.mux.HandleFunc("GET /api/v1/workloads", s.handleListWorkloads)
	s.mux.HandleFunc("POST /api/v1/deployments", s.handleCreateDeployment)
	s.mux.HandleFunc("POST /api/v1/deployments/{workload}/rollback", s.handleRollback)

	// Database & WAL Management
	s.mux.HandleFunc("GET /api/v1/database/status", s.handleDBStatus)
	s.mux.HandleFunc("POST /api/v1/database/checkpoint", s.handleDBCheckpoint)
	s.mux.HandleFunc("POST /api/v1/database/backup", s.handleDBBackup)

	// Background Jobs & Audit
	s.mux.HandleFunc("GET /api/v1/jobs", s.handleListJobs)
	s.mux.HandleFunc("GET /api/v1/audit", s.handleListAudit)

	// Webhooks (ProGit Integration)
	s.mux.HandleFunc("POST /api/v1/webhooks/progit", s.handleProGitWebhook)

	// First Milestone Orchestrator Endpoint
	s.mux.HandleFunc("POST /api/v1/milestone/run", s.handleRunMilestone)

	// Static Web UI Console
	s.mux.Handle("/", web.Handler())
}

// -------------------------------------------------------------
// HTTP Handlers
// -------------------------------------------------------------

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
	respondJSON(w, http.StatusOK, map[string]any{"token": token, "user": user})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	authHeader := r.Header.Get("Authorization")
	token := strings.TrimPrefix(authHeader, "Bearer ")
	claims, err := s.authSvc.VerifyToken(token)
	if err != nil {
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

	// Submit background deploy job
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
