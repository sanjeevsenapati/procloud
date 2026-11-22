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
	"procloud.io/procloud/internal/dns"
	"procloud.io/procloud/internal/ingress"
	"procloud.io/procloud/internal/job"
	"procloud.io/procloud/internal/loadbalancer"
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
	lbSvc         *loadbalancer.Service
	dnsSvc        *dns.Service
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
	lbSvc := loadbalancer.NewService(repo, runtime, "")
	dnsSvc := dns.NewService(repo)
	if err := dnsSvc.StartUDPServer("127.0.0.1:1053"); err != nil {
		log.Printf("[DNS] Warning: failed starting authoritative UDP listener on 127.0.0.1:1053: %v", err)
	} else {
		log.Printf("[DNS] Authoritative RFC 1035 UDP listener started on 127.0.0.1:1053")
	}
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
		lbSvc:         lbSvc,
		dnsSvc:        dnsSvc,
		deployEngine:  deployEngine,
		jobEngine:     jobEngine,
		monitoringSvc: monitoringSvc,
		auditSvc:      auditSvc,
		runtime:       runtime,
		builder:       builder,
		wfEngine:      wfEngine,
	}

	// -------------------------------------------------------------
	// Initial Bootstrap: Organizations & RBAC Users
	// -------------------------------------------------------------
	ctx := context.Background()
	defaultOrg, _ := repo.GetTenantBySlug(ctx, "default-org")
	if defaultOrg == nil {
		defaultOrg = &protocol.Tenant{
			ID:            "tnt-default",
			Name:          "Default Organization",
			Slug:          "default-org",
			Description:   "Core sovereign cloud platform infrastructure and administrative tenant",
			Plan:          "enterprise",
			Status:        "active",
			MaxContainers: 50,
			MaxPods:       25,
			MaxCPUCores:   16,
			MaxMemoryMB:   32768,
			MaxStorageGB:  500,
			CreatedAt:     time.Now(),
			UpdatedAt:     time.Now(),
		}
		_ = repo.CreateTenant(ctx, defaultOrg)
		log.Printf("[Bootstrap] Default Organization initialized (tnt-default)")

		demoOrg := &protocol.Tenant{
			ID:            "tnt-acme",
			Name:          "Acme Innovations",
			Slug:          "acme-innovations",
			Description:   "Production product workloads, microservices, and client portals",
			Plan:          "pro",
			Status:        "active",
			MaxContainers: 25,
			MaxPods:       15,
			MaxCPUCores:   8,
			MaxMemoryMB:   16384,
			MaxStorageGB:  250,
			CreatedAt:     time.Now(),
			UpdatedAt:     time.Now(),
		}
		_ = repo.CreateTenant(ctx, demoOrg)
	}

	// Ensure default administrator account exists (admin / admin123) with Super Admin role
	adminUser, err := repo.GetUserByUsername(ctx, "admin")
	if err != nil {
		_, regErr := authSvc.CreateUserWithOrg(ctx, "admin", "admin@procloud.io", "admin123", protocol.RoleSuperAdmin, defaultOrg.ID, "Support Administrator")
		if regErr != nil {
			log.Printf("[Auth] Notice: failed to register default admin: %v", regErr)
		} else {
			log.Printf("[Auth] Support Admin account created: admin / admin123")
		}
	} else {
		needsUpdate := false
		if adminUser.Role != protocol.RoleSuperAdmin {
			adminUser.Role = protocol.RoleSuperAdmin
			needsUpdate = true
		}
		if adminUser.TenantID == "" && defaultOrg != nil {
			adminUser.TenantID = defaultOrg.ID
			needsUpdate = true
		}
		if adminUser.DisplayName == "" {
			adminUser.DisplayName = "Support Administrator"
			needsUpdate = true
		}
		if needsUpdate {
			_ = repo.UpdateUser(ctx, adminUser)
		}
	}

	// Seed secondary demo users for RBAC demonstration if few users exist
	userCount, _ := repo.CountUsers(ctx)
	if userCount <= 1 && defaultOrg != nil {
		_, _ = authSvc.CreateUserWithOrg(ctx, "sarah.ops", "sarah@acme.io", "sarah123", protocol.RoleOrgAdmin, "tnt-acme", "Sarah Jenkins (Org Admin)")
		_, _ = authSvc.CreateUserWithOrg(ctx, "alex.dev", "alex@acme.io", "alex123", protocol.RoleDeveloper, "tnt-acme", "Alex Rivera (Developer)")
		_, _ = authSvc.CreateUserWithOrg(ctx, "auditor.chen", "chen@procloud.io", "chen123", protocol.RoleViewer, defaultOrg.ID, "David Chen (Auditor)")
		log.Printf("[Auth] Seeded RBAC demo team members across organizations")
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

func (s *Server) adminOnly(next http.HandlerFunc) http.HandlerFunc {
	return s.authRequired(func(w http.ResponseWriter, r *http.Request) {
		claims := getClaims(r)
		if claims == nil || claims.Role != protocol.RoleSuperAdmin {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error": "forbidden: SuperAdmin permission required",
			})
			return
		}
		next(w, r)
	})
}

func (s *Server) orgAdminOnly(next http.HandlerFunc) http.HandlerFunc {
	return s.authRequired(func(w http.ResponseWriter, r *http.Request) {
		claims := getClaims(r)
		if claims == nil || (claims.Role != protocol.RoleSuperAdmin && claims.Role != protocol.RoleOrgAdmin) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error": "forbidden: Administrator permission required",
			})
			return
		}
		next(w, r)
	})
}

func (s *Server) writeRequired(next http.HandlerFunc) http.HandlerFunc {
	return s.authRequired(func(w http.ResponseWriter, r *http.Request) {
		claims := getClaims(r)
		if claims != nil && claims.Role == protocol.RoleViewer {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error": "forbidden: read-only viewer role cannot perform modification actions",
			})
			return
		}
		next(w, r)
	})
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
	s.mux.HandleFunc("POST /api/v1/nodes/register", s.adminOnly(s.handleRegisterNode))

	// -------------------------------------------------------------
	// Podman Desktop Features: Container Provisioning & Lifecycle (Protected)
	// -------------------------------------------------------------
	s.mux.HandleFunc("GET /api/v1/containers", s.authRequired(s.handleListContainers))
	s.mux.HandleFunc("POST /api/v1/containers", s.writeRequired(s.handleRunContainer))
	s.mux.HandleFunc("GET /api/v1/containers/{id}", s.authRequired(s.handleInspectContainer))
	s.mux.HandleFunc("POST /api/v1/containers/{id}/start", s.writeRequired(s.handleStartContainer))
	s.mux.HandleFunc("POST /api/v1/containers/{id}/stop", s.writeRequired(s.handleStopContainer))
	s.mux.HandleFunc("POST /api/v1/containers/{id}/restart", s.writeRequired(s.handleRestartContainer))
	s.mux.HandleFunc("DELETE /api/v1/containers/{id}", s.writeRequired(s.handleRemoveContainer))
	s.mux.HandleFunc("GET /api/v1/containers/{id}/logs", s.authRequired(s.handleContainerLogs))
	s.mux.HandleFunc("POST /api/v1/containers/{id}/exec", s.writeRequired(s.handleExecContainer))
	s.mux.HandleFunc("GET /api/v1/containers/{id}/top", s.authRequired(s.handleTopContainer))
	s.mux.HandleFunc("GET /api/v1/containers/{id}/stats", s.authRequired(s.handleStatsContainer))
	s.mux.HandleFunc("GET /api/v1/containers/{id}/containerfile", s.authRequired(s.handleContainerfile))
	s.mux.HandleFunc("GET /api/v1/containers/{id}/kube", s.authRequired(s.handleContainerKube))

	// -------------------------------------------------------------
	// Podman Desktop Features: Pod Provisioning & Lifecycle (Protected)
	// -------------------------------------------------------------
	s.mux.HandleFunc("GET /api/v1/pods", s.authRequired(s.handleListPods))
	s.mux.HandleFunc("POST /api/v1/pods", s.writeRequired(s.handleCreatePod))
	s.mux.HandleFunc("GET /api/v1/pods/{id}", s.authRequired(s.handleInspectPod))
	s.mux.HandleFunc("POST /api/v1/pods/{id}/start", s.writeRequired(s.handleStartPod))
	s.mux.HandleFunc("POST /api/v1/pods/{id}/stop", s.writeRequired(s.handleStopPod))
	s.mux.HandleFunc("POST /api/v1/pods/{id}/restart", s.writeRequired(s.handleRestartPod))
	s.mux.HandleFunc("DELETE /api/v1/pods/{id}", s.writeRequired(s.handleRemovePod))
	s.mux.HandleFunc("GET /api/v1/pods/{id}/kube", s.authRequired(s.handleGenerateKube))
	s.mux.HandleFunc("POST /api/v1/pods/kube/play", s.writeRequired(s.handlePlayKube))
	s.mux.HandleFunc("GET /api/v1/pods/{id}/top", s.authRequired(s.handleTopPod))
	s.mux.HandleFunc("GET /api/v1/pods/{id}/logs", s.authRequired(s.handlePodLogs))

	// -------------------------------------------------------------
	// Podman Desktop Features: Image Management (Protected)
	// -------------------------------------------------------------
	s.mux.HandleFunc("GET /api/v1/images", s.authRequired(s.handleListImages))
	s.mux.HandleFunc("POST /api/v1/images/pull", s.writeRequired(s.handlePullImage))
	s.mux.HandleFunc("POST /api/v1/images/build", s.writeRequired(s.handleBuildImage))
	s.mux.HandleFunc("POST /api/v1/images/bulk-delete", s.writeRequired(s.handleBulkRemoveImages))
	s.mux.HandleFunc("DELETE /api/v1/images/{id}", s.writeRequired(s.handleRemoveImage))

	// -------------------------------------------------------------
	// Podman Desktop Features: Runtime Networks & Volumes (Protected)
	// -------------------------------------------------------------
	s.mux.HandleFunc("GET /api/v1/podman/networks", s.authRequired(s.handleListPodmanNetworks))
	s.mux.HandleFunc("POST /api/v1/podman/networks", s.writeRequired(s.handleCreatePodmanNetwork))
	s.mux.HandleFunc("DELETE /api/v1/podman/networks/{id}", s.writeRequired(s.handleRemovePodmanNetwork))

	s.mux.HandleFunc("GET /api/v1/podman/volumes", s.authRequired(s.handleListPodmanVolumes))
	s.mux.HandleFunc("POST /api/v1/podman/volumes", s.writeRequired(s.handleCreatePodmanVolume))
	s.mux.HandleFunc("DELETE /api/v1/podman/volumes/{id}", s.writeRequired(s.handleRemovePodmanVolume))

	// -------------------------------------------------------------
	// Podman Desktop Features: Secret Management (Protected)
	// -------------------------------------------------------------
	s.mux.HandleFunc("GET /api/v1/podman/secrets", s.authRequired(s.handleListPodmanSecrets))
	s.mux.HandleFunc("POST /api/v1/podman/secrets", s.writeRequired(s.handleCreatePodmanSecret))
	s.mux.HandleFunc("GET /api/v1/podman/secrets/{id}", s.authRequired(s.handleInspectPodmanSecret))
	s.mux.HandleFunc("DELETE /api/v1/podman/secrets/{id}", s.writeRequired(s.handleRemovePodmanSecret))

	// -------------------------------------------------------------
	// Organization & Multi-Tenant Management (Protected)
	// -------------------------------------------------------------
	s.mux.HandleFunc("GET /api/v1/organizations", s.authRequired(s.handleListOrganizations))
	s.mux.HandleFunc("POST /api/v1/organizations", s.adminOnly(s.handleCreateOrganization))
	s.mux.HandleFunc("GET /api/v1/organizations/{id}", s.authRequired(s.handleGetOrganization))
	s.mux.HandleFunc("PUT /api/v1/organizations/{id}", s.adminOnly(s.handleUpdateOrganization))
	s.mux.HandleFunc("DELETE /api/v1/organizations/{id}", s.adminOnly(s.handleDeleteOrganization))

	// -------------------------------------------------------------
	// User Management & RBAC Roles (Protected)
	// -------------------------------------------------------------
	s.mux.HandleFunc("GET /api/v1/users", s.orgAdminOnly(s.handleListUsers))
	s.mux.HandleFunc("POST /api/v1/users", s.orgAdminOnly(s.handleCreateUser))
	s.mux.HandleFunc("GET /api/v1/users/{id}", s.orgAdminOnly(s.handleGetUser))
	s.mux.HandleFunc("PUT /api/v1/users/{id}", s.orgAdminOnly(s.handleUpdateUser))
	s.mux.HandleFunc("POST /api/v1/users/{id}/password", s.orgAdminOnly(s.handleResetUserPassword))
	s.mux.HandleFunc("DELETE /api/v1/users/{id}", s.orgAdminOnly(s.handleDeleteUser))
	s.mux.HandleFunc("GET /api/v1/rbac/roles", s.authRequired(s.handleListRBACRoles))

	// Tenants & Projects (Protected)
	s.mux.HandleFunc("GET /api/v1/tenants", s.authRequired(s.handleListTenants))
	s.mux.HandleFunc("POST /api/v1/tenants", s.writeRequired(s.handleCreateTenant))
	s.mux.HandleFunc("GET /api/v1/projects", s.authRequired(s.handleListProjects))
	s.mux.HandleFunc("POST /api/v1/projects", s.writeRequired(s.handleCreateProject))

	// VPC & Storage Repositories (Protected)
	s.mux.HandleFunc("GET /api/v1/vpcs", s.authRequired(s.handleListVPCs))
	s.mux.HandleFunc("POST /api/v1/vpcs", s.writeRequired(s.handleCreateVPC))
	s.mux.HandleFunc("GET /api/v1/subnets", s.authRequired(s.handleListSubnets))
	s.mux.HandleFunc("POST /api/v1/subnets", s.writeRequired(s.handleCreateSubnet))
	s.mux.HandleFunc("GET /api/v1/volumes", s.authRequired(s.handleListVolumes))
	s.mux.HandleFunc("POST /api/v1/volumes", s.writeRequired(s.handleCreateVolume))

	// Workloads & Deployments (Protected)
	s.mux.HandleFunc("GET /api/v1/workloads", s.authRequired(s.handleListWorkloads))
	s.mux.HandleFunc("POST /api/v1/deployments", s.writeRequired(s.handleCreateDeployment))
	s.mux.HandleFunc("POST /api/v1/deployments/{workload}/rollback", s.writeRequired(s.handleRollback))

	// Database & WAL Management (Protected)
	s.mux.HandleFunc("GET /api/v1/database/status", s.adminOnly(s.handleDBStatus))
	s.mux.HandleFunc("POST /api/v1/database/checkpoint", s.adminOnly(s.handleDBCheckpoint))
	s.mux.HandleFunc("POST /api/v1/database/backup", s.adminOnly(s.handleDBBackup))

	// Background Jobs & Audit (Protected)
	s.mux.HandleFunc("GET /api/v1/jobs", s.authRequired(s.handleListJobs))
	s.mux.HandleFunc("GET /api/v1/audit", s.authRequired(s.handleListAudit))

	// Build & Deploy Workflow Engine (Protected)
	s.mux.HandleFunc("POST /api/v1/workflows/run", s.writeRequired(s.handleRunWorkflow))
	s.mux.HandleFunc("GET /api/v1/workflows/runs", s.authRequired(s.handleListWorkflowRuns))
	s.mux.HandleFunc("GET /api/v1/workflows/runs/{id}", s.authRequired(s.handleGetWorkflowRun))
	s.mux.HandleFunc("DELETE /api/v1/workflows/runs/{id}", s.writeRequired(s.handleDeleteWorkflowRun))
	s.mux.HandleFunc("POST /api/v1/workflows/runs/{id}/rerun", s.writeRequired(s.handleRerunWorkflow))

	// Load Balancer as a Service (LBaaS) (Protected)
	s.mux.HandleFunc("GET /api/v1/loadbalancers", s.authRequired(s.handleListLoadBalancers))
	s.mux.HandleFunc("POST /api/v1/loadbalancers", s.writeRequired(s.handleCreateLoadBalancer))
	s.mux.HandleFunc("GET /api/v1/loadbalancers/{id}", s.authRequired(s.handleGetLoadBalancer))
	s.mux.HandleFunc("PUT /api/v1/loadbalancers/{id}", s.writeRequired(s.handleUpdateLoadBalancer))
	s.mux.HandleFunc("DELETE /api/v1/loadbalancers/{id}", s.writeRequired(s.handleDeleteLoadBalancer))
	s.mux.HandleFunc("POST /api/v1/loadbalancers/{id}/listeners", s.writeRequired(s.handleCreateLBListener))
	s.mux.HandleFunc("DELETE /api/v1/loadbalancers/{id}/listeners/{lid}", s.writeRequired(s.handleDeleteLBListener))
	s.mux.HandleFunc("POST /api/v1/loadbalancers/{id}/pools", s.writeRequired(s.handleCreateLBPool))
	s.mux.HandleFunc("DELETE /api/v1/loadbalancers/{id}/pools/{pid}", s.writeRequired(s.handleDeleteLBPool))
	s.mux.HandleFunc("POST /api/v1/loadbalancers/{id}/pools/{pid}/members", s.writeRequired(s.handleAttachLBMember))
	s.mux.HandleFunc("DELETE /api/v1/loadbalancers/{id}/members/{mid}", s.writeRequired(s.handleDetachLBMember))
	s.mux.HandleFunc("POST /api/v1/loadbalancers/{id}/listeners/{lid}/policies", s.writeRequired(s.handleCreateLBPolicy))
	s.mux.HandleFunc("DELETE /api/v1/loadbalancers/{id}/policies/{polid}", s.writeRequired(s.handleDeleteLBPolicy))
	s.mux.HandleFunc("GET /api/v1/loadbalancers/{id}/config", s.authRequired(s.handleGetLBConfig))

	// DNS as a Service (DNSaaS) (Protected)
	s.mux.HandleFunc("GET /api/v1/dns/zones", s.authRequired(s.handleListDNSZones))
	s.mux.HandleFunc("POST /api/v1/dns/zones", s.writeRequired(s.handleCreateDNSZone))
	s.mux.HandleFunc("GET /api/v1/dns/zones/{id}", s.authRequired(s.handleGetDNSZone))
	s.mux.HandleFunc("PUT /api/v1/dns/zones/{id}", s.writeRequired(s.handleUpdateDNSZone))
	s.mux.HandleFunc("DELETE /api/v1/dns/zones/{id}", s.writeRequired(s.handleDeleteDNSZone))
	s.mux.HandleFunc("GET /api/v1/dns/zones/{id}/records", s.authRequired(s.handleListDNSRecords))
	s.mux.HandleFunc("POST /api/v1/dns/zones/{id}/records", s.writeRequired(s.handleCreateDNSRecord))
	s.mux.HandleFunc("GET /api/v1/dns/zones/{id}/records/{rid}", s.authRequired(s.handleGetDNSRecord))
	s.mux.HandleFunc("PUT /api/v1/dns/zones/{id}/records/{rid}", s.writeRequired(s.handleUpdateDNSRecord))
	s.mux.HandleFunc("DELETE /api/v1/dns/zones/{id}/records/{rid}", s.writeRequired(s.handleDeleteDNSRecord))
	s.mux.HandleFunc("GET /api/v1/dns/zones/{id}/export", s.authRequired(s.handleExportBINDZone))
	s.mux.HandleFunc("POST /api/v1/dns/query", s.authRequired(s.handleQueryDNS))

	// Webhooks & Milestone
	s.mux.HandleFunc("POST /api/v1/webhooks/progit", s.handleProGitWebhook)
	s.mux.HandleFunc("POST /api/v1/milestone/run", s.adminOnly(s.handleRunMilestone))

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
	http.Error(w, "Public sign-up is disabled. User accounts and organizations must be provisioned by a Super Administrator.", http.StatusForbidden)
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

	// Filter by organization if requested or if user has non-admin role
	tenantFilter := r.URL.Query().Get("tenant_id")
	claims := s.checkAuth(r)
	if claims != nil && claims.Role != protocol.RoleSuperAdmin && claims.Role != protocol.RoleOrgAdmin {
		tenantFilter = claims.TenantID
	}

	if tenantFilter != "" {
		var filtered []*podman.ContainerSummary
		for _, c := range containers {
			orgID := ""
			if c.Labels != nil {
				orgID = c.Labels["procloud.io/organization"]
			}
			if orgID == "" {
				orgID = "tnt-default"
			}
			if orgID == tenantFilter {
				filtered = append(filtered, c)
			}
		}
		containers = filtered
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

	// Resolve acting user and organization context
	actor := "anonymous"
	tenantID := ""
	tenantName := ""
	claims := s.checkAuth(r)
	if claims != nil {
		actor = claims.Username
		tenantID = claims.TenantID
	}

	// Allow superadmin/admin to override target organization
	if reqTenant := r.URL.Query().Get("tenant_id"); reqTenant != "" && (claims == nil || claims.Role == protocol.RoleSuperAdmin || claims.Role == protocol.RoleOrgAdmin) {
		tenantID = reqTenant
	}
	if opts.Labels != nil && opts.Labels["procloud.io/organization"] != "" && (claims == nil || claims.Role == protocol.RoleSuperAdmin || claims.Role == protocol.RoleOrgAdmin) {
		tenantID = opts.Labels["procloud.io/organization"]
	}
	if tenantID == "" {
		tenantID = "tnt-default"
	}
	if tenantID != "" {
		if org, err := s.tenantSvc.GetOrganization(r.Context(), tenantID); err == nil && org != nil {
			tenantName = org.Name
		} else {
			tenantName = tenantID
		}
	}

	// Auto-tag container with organization and creator metadata labels
	if opts.Labels == nil {
		opts.Labels = make(map[string]string)
	}
	opts.Labels["procloud.io/organization"] = tenantID
	opts.Labels["procloud.io/org-name"] = tenantName
	opts.Labels["procloud.io/created-by"] = actor

	ctrID, err := s.runtime.RunContainer(r.Context(), opts)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.auditSvc.Log(r.Context(), actor, "CONTAINER_RUN", "Container", opts.Name, r.RemoteAddr, fmt.Sprintf("Started container %s with image %s (Org: %s)", opts.Name, opts.Image, tenantName))

	respondJSON(w, http.StatusCreated, map[string]string{
		"id":          ctrID,
		"name":        opts.Name,
		"tenant_id":   tenantID,
		"tenant_name": tenantName,
		"status":      "running",
	})
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

	// Filter by organization if requested or if user has non-admin role
	tenantFilter := r.URL.Query().Get("tenant_id")
	claims := s.checkAuth(r)
	if claims != nil && claims.Role != protocol.RoleSuperAdmin && claims.Role != protocol.RoleOrgAdmin {
		tenantFilter = claims.TenantID
	}

	if tenantFilter != "" {
		var filtered []*podman.PodSummary
		for _, p := range pods {
			orgID := ""
			if p.Labels != nil {
				orgID = p.Labels["procloud.io/organization"]
			}
			if orgID == "" {
				orgID = "tnt-default"
			}
			if orgID == tenantFilter {
				filtered = append(filtered, p)
			}
		}
		pods = filtered
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

	actor := "anonymous"
	tenantID := ""
	tenantName := ""
	claims := s.checkAuth(r)
	if claims != nil {
		actor = claims.Username
		tenantID = claims.TenantID
	}

	if reqTenant := r.URL.Query().Get("tenant_id"); reqTenant != "" && (claims == nil || claims.Role == protocol.RoleSuperAdmin || claims.Role == protocol.RoleOrgAdmin) {
		tenantID = reqTenant
	}
	if opts.Labels != nil && opts.Labels["procloud.io/organization"] != "" && (claims == nil || claims.Role == protocol.RoleSuperAdmin || claims.Role == protocol.RoleOrgAdmin) {
		tenantID = opts.Labels["procloud.io/organization"]
	}
	if tenantID == "" {
		tenantID = "tnt-default"
	}
	if tenantID != "" {
		if org, err := s.tenantSvc.GetOrganization(r.Context(), tenantID); err == nil && org != nil {
			tenantName = org.Name
		} else {
			tenantName = tenantID
		}
	}

	if opts.Labels == nil {
		opts.Labels = make(map[string]string)
	}
	opts.Labels["procloud.io/organization"] = tenantID
	opts.Labels["procloud.io/org-name"] = tenantName
	opts.Labels["procloud.io/created-by"] = actor

	podID, err := s.runtime.CreatePod(r.Context(), opts)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.auditSvc.Log(r.Context(), actor, "POD_CREATED", "Pod", opts.Name, r.RemoteAddr, fmt.Sprintf("Created pod %s (Org: %s)", opts.Name, tenantName))

	respondJSON(w, http.StatusCreated, map[string]string{
		"id":          podID,
		"name":        opts.Name,
		"tenant_id":   tenantID,
		"tenant_name": tenantName,
		"status":      "Created",
	})
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

type BulkDeleteImagesRequest struct {
	IDs   []string `json:"ids"`
	Force bool     `json:"force"`
}

type BulkDeleteImagesResponse struct {
	Deleted []string          `json:"deleted"`
	Failed  []BulkDeleteError `json:"failed"`
}

type BulkDeleteError struct {
	ID    string `json:"id"`
	Error string `json:"error"`
}

func (s *Server) handleBulkRemoveImages(w http.ResponseWriter, r *http.Request) {
	var req BulkDeleteImagesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if len(req.IDs) == 0 {
		http.Error(w, "no image IDs provided", http.StatusBadRequest)
		return
	}

	actor := "anonymous"
	if claims := s.checkAuth(r); claims != nil {
		actor = claims.Username
	}

	deleted := make([]string, 0)
	failed := make([]BulkDeleteError, 0)

	for _, id := range req.IDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if err := s.runtime.RemoveImage(r.Context(), id, req.Force); err != nil {
			failed = append(failed, BulkDeleteError{ID: id, Error: err.Error()})
		} else {
			deleted = append(deleted, id)
			s.auditSvc.Log(r.Context(), actor, "IMAGE_DELETE", "Image", id, r.RemoteAddr, fmt.Sprintf("Removed image %s (force=%v)", id, req.Force))
		}
	}

	respondJSON(w, http.StatusOK, BulkDeleteImagesResponse{
		Deleted: deleted,
		Failed:  failed,
	})
}

func (s *Server) handleRemoveImage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	force := r.URL.Query().Get("force") == "true"
	if err := s.runtime.RemoveImage(r.Context(), id, force); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	actor := "anonymous"
	if claims := s.checkAuth(r); claims != nil {
		actor = claims.Username
	}
	s.auditSvc.Log(r.Context(), actor, "IMAGE_DELETE", "Image", id, r.RemoteAddr, fmt.Sprintf("Removed image %s (force=%v)", id, force))
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
// Podman Desktop Features: Secret Management Handlers
// -------------------------------------------------------------

func (s *Server) handleListPodmanSecrets(w http.ResponseWriter, r *http.Request) {
	secrets, err := s.runtime.ListSecrets(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, secrets)
}

func (s *Server) handleCreatePodmanSecret(w http.ResponseWriter, r *http.Request) {
	var opts podman.SecretCreateOptions
	if err := json.NewDecoder(r.Body).Decode(&opts); err != nil || opts.Name == "" {
		http.Error(w, "secret name and data are required", http.StatusBadRequest)
		return
	}
	secID, err := s.runtime.CreateSecret(r.Context(), opts)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	actor := "anonymous"
	if claims := s.checkAuth(r); claims != nil {
		actor = claims.Username
	}
	s.auditSvc.Log(r.Context(), actor, "SECRET_CREATE", "Secret", opts.Name, r.RemoteAddr, fmt.Sprintf("Created secret %s (id: %s)", opts.Name, secID))

	respondJSON(w, http.StatusCreated, map[string]string{
		"message": "secret created successfully",
		"id":      secID,
		"name":    opts.Name,
	})
}

func (s *Server) handleInspectPodmanSecret(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	showSecret := r.URL.Query().Get("showsecret") == "true"
	sec, err := s.runtime.InspectSecret(r.Context(), id, showSecret)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	actor := "anonymous"
	if claims := s.checkAuth(r); claims != nil {
		actor = claims.Username
	}
	if showSecret {
		s.auditSvc.Log(r.Context(), actor, "SECRET_REVEAL", "Secret", sec.Name, r.RemoteAddr, fmt.Sprintf("Revealed plaintext data for secret %s", sec.Name))
	}

	respondJSON(w, http.StatusOK, sec)
}

func (s *Server) handleRemovePodmanSecret(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.runtime.RemoveSecret(r.Context(), id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	actor := "anonymous"
	if claims := s.checkAuth(r); claims != nil {
		actor = claims.Username
	}
	s.auditSvc.Log(r.Context(), actor, "SECRET_DELETE", "Secret", id, r.RemoteAddr, fmt.Sprintf("Removed secret %s", id))

	respondJSON(w, http.StatusOK, map[string]string{"message": "secret removed", "id": id})
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

// -------------------------------------------------------------
// Organization Management Handlers
// -------------------------------------------------------------

func (s *Server) handleListOrganizations(w http.ResponseWriter, r *http.Request) {
	claims := getClaims(r)
	if claims != nil && claims.Role != protocol.RoleSuperAdmin {
		// Non-SuperAdmin: only return user's assigned organization
		if claims.TenantID != "" {
			org, err := s.tenantSvc.GetOrganization(r.Context(), claims.TenantID)
			if err == nil && org != nil {
				respondJSON(w, http.StatusOK, []*protocol.Tenant{org})
				return
			}
		}
		respondJSON(w, http.StatusOK, []*protocol.Tenant{})
		return
	}
	list, err := s.tenantSvc.ListOrganizations(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if list == nil {
		list = []*protocol.Tenant{}
	}
	respondJSON(w, http.StatusOK, list)
}

func (s *Server) handleCreateOrganization(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name             string `json:"name"`
		Description      string `json:"description"`
		Plan             string `json:"plan"`
		Status           string `json:"status"`
		MaxContainers    int    `json:"max_containers"`
		MaxPods          int    `json:"max_pods"`
		MaxCPUCores      int    `json:"max_cpu_cores"`
		MaxMemoryMB      int    `json:"max_memory_mb"`
		MaxStorageGB     int    `json:"max_storage_gb"`
		AdminUsername    string `json:"admin_username"`
		AdminEmail       string `json:"admin_email"`
		AdminPassword    string `json:"admin_password"`
		AdminDisplayName string `json:"admin_display_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request body: %v", err), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		http.Error(w, "organization name is required", http.StatusBadRequest)
		return
	}
	t, err := s.tenantSvc.CreateOrganization(
		r.Context(), req.Name, req.Description, req.Plan,
		req.MaxContainers, req.MaxPods, req.MaxCPUCores, req.MaxMemoryMB, req.MaxStorageGB,
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Status != "" && req.Status != "active" {
		if updated, uerr := s.tenantSvc.UpdateOrganization(
			r.Context(), t.ID, t.Name, t.Description, t.Plan, req.Status,
			t.MaxContainers, t.MaxPods, t.MaxCPUCores, t.MaxMemoryMB, t.MaxStorageGB,
		); uerr == nil {
			t = updated
		}
	}
	claims := s.checkAuth(r)
	actor := "admin"
	if claims != nil {
		actor = claims.Username
	}
	s.auditSvc.Log(r.Context(), actor, "ORG_CREATE", "Tenant", t.ID, r.RemoteAddr, fmt.Sprintf("Created organization %s (%s)", t.Name, t.Plan))

	// Provision initial Org Admin if requested by Super Admin
	if strings.TrimSpace(req.AdminUsername) != "" && strings.TrimSpace(req.AdminPassword) != "" {
		adminEmail := strings.TrimSpace(req.AdminEmail)
		if adminEmail == "" {
			cleanName := strings.ToLower(strings.Map(func(r rune) rune {
				if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
					return r
				}
				return -1
			}, t.Name))
			if cleanName == "" {
				cleanName = "tenant"
			}
			adminEmail = strings.TrimSpace(req.AdminUsername) + "@" + cleanName + ".local"
		}
		adminName := strings.TrimSpace(req.AdminDisplayName)
		if adminName == "" {
			adminName = strings.TrimSpace(req.AdminUsername)
		}
		_, err := s.authSvc.CreateUserWithOrg(r.Context(), req.AdminUsername, adminEmail, req.AdminPassword, protocol.RoleOrgAdmin, t.ID, adminName)
		if err != nil {
			log.Printf("[Org Admin Provision] warning: failed creating initial admin %s: %v", req.AdminUsername, err)
		} else {
			s.auditSvc.Log(r.Context(), actor, "USER_CREATE", "User", req.AdminUsername, r.RemoteAddr, fmt.Sprintf("Provisioned initial Org Admin %s for tenant %s", req.AdminUsername, t.ID))
		}
	}

	respondJSON(w, http.StatusCreated, t)
}

func (s *Server) handleGetOrganization(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	t, err := s.tenantSvc.GetOrganization(r.Context(), id)
	if err != nil {
		http.Error(w, "organization not found", http.StatusNotFound)
		return
	}
	respondJSON(w, http.StatusOK, t)
}

func (s *Server) handleUpdateOrganization(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Name          string `json:"name"`
		Description   string `json:"description"`
		Plan          string `json:"plan"`
		Status        string `json:"status"`
		MaxContainers int    `json:"max_containers"`
		MaxPods       int    `json:"max_pods"`
		MaxCPUCores   int    `json:"max_cpu_cores"`
		MaxMemoryMB   int    `json:"max_memory_mb"`
		MaxStorageGB  int    `json:"max_storage_gb"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	t, err := s.tenantSvc.UpdateOrganization(
		r.Context(), id, req.Name, req.Description, req.Plan, req.Status,
		req.MaxContainers, req.MaxPods, req.MaxCPUCores, req.MaxMemoryMB, req.MaxStorageGB,
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	claims := s.checkAuth(r)
	actor := "admin"
	if claims != nil {
		actor = claims.Username
	}
	s.auditSvc.Log(r.Context(), actor, "ORG_UPDATE", "Tenant", t.ID, r.RemoteAddr, fmt.Sprintf("Updated organization %s", t.Name))
	respondJSON(w, http.StatusOK, t)
}

func (s *Server) handleDeleteOrganization(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "tnt-default" {
		http.Error(w, "cannot delete the default system organization", http.StatusForbidden)
		return
	}
	if err := s.tenantSvc.DeleteOrganization(r.Context(), id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	claims := s.checkAuth(r)
	actor := "admin"
	if claims != nil {
		actor = claims.Username
	}
	s.auditSvc.Log(r.Context(), actor, "ORG_DELETE", "Tenant", id, r.RemoteAddr, fmt.Sprintf("Deleted organization %s", id))
	respondJSON(w, http.StatusOK, map[string]string{"message": "organization deleted", "id": id})
}

// -------------------------------------------------------------
// User Management & RBAC Handlers
// -------------------------------------------------------------

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	claims := getClaims(r)
	tenantID := r.URL.Query().Get("tenant_id")
	if claims != nil && claims.Role != protocol.RoleSuperAdmin {
		tenantID = claims.TenantID
	}
	role := r.URL.Query().Get("role")
	list, err := s.authSvc.ListUsers(r.Context(), tenantID, role)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if list == nil {
		list = []*protocol.User{}
	}
	respondJSON(w, http.StatusOK, list)
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username    string `json:"username"`
		Email       string `json:"email"`
		Password    string `json:"password"`
		Role        string `json:"role"`
		TenantID    string `json:"tenant_id"`
		DisplayName string `json:"display_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Username == "" || req.Email == "" || req.Password == "" {
		http.Error(w, "username, email, and password are required", http.StatusBadRequest)
		return
	}

	claims := getClaims(r)
	if claims != nil && claims.Role != protocol.RoleSuperAdmin {
		req.TenantID = claims.TenantID
		if req.Role == protocol.RoleSuperAdmin || req.Role == protocol.RoleOrgAdmin {
			http.Error(w, "forbidden: only Super Administrators can create Organisation Admins or Super Admins", http.StatusForbidden)
			return
		}
	}

	if req.Role == "" {
		req.Role = protocol.RoleDeveloper
	}

	u, err := s.authSvc.CreateUserWithOrg(r.Context(), req.Username, req.Email, req.Password, req.Role, req.TenantID, req.DisplayName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	actor := "admin"
	if claims != nil {
		actor = claims.Username
	}
	s.auditSvc.Log(r.Context(), actor, "USER_CREATE", "User", u.ID, r.RemoteAddr, fmt.Sprintf("Created user %s (%s, %s)", u.Username, u.Role, u.TenantID))
	respondJSON(w, http.StatusCreated, u)
}

func (s *Server) handleGetUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	u, err := s.authSvc.GetUser(r.Context(), id)
	if err != nil {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	respondJSON(w, http.StatusOK, u)
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Role        string `json:"role"`
		TenantID    string `json:"tenant_id"`
		Status      string `json:"status"`
		DisplayName string `json:"display_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	claims := getClaims(r)
	if claims != nil && claims.Role != protocol.RoleSuperAdmin {
		targetUser, err := s.authSvc.GetUser(r.Context(), id)
		if err != nil || targetUser == nil || targetUser.TenantID != claims.TenantID || targetUser.Role == protocol.RoleSuperAdmin || targetUser.Role == protocol.RoleOrgAdmin {
			http.Error(w, "forbidden: cannot modify user outside your organization or administrators", http.StatusForbidden)
			return
		}
		req.TenantID = claims.TenantID
		if req.Role == protocol.RoleSuperAdmin || req.Role == protocol.RoleOrgAdmin {
			http.Error(w, "forbidden: only Super Administrators can assign Organisation Admin or Super Admin roles", http.StatusForbidden)
			return
		}
	}

	u, err := s.authSvc.UpdateUser(r.Context(), id, req.Role, req.TenantID, req.Status, req.DisplayName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	actor := "admin"
	if claims != nil {
		actor = claims.Username
	}
	s.auditSvc.Log(r.Context(), actor, "USER_UPDATE", "User", u.ID, r.RemoteAddr, fmt.Sprintf("Updated user %s (%s)", u.Username, u.Role))
	respondJSON(w, http.StatusOK, u)
}

func (s *Server) handleResetUserPassword(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		Password    string `json:"password"`
		NewPassword string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	pw := req.Password
	if pw == "" {
		pw = req.NewPassword
	}
	if len(pw) < 6 {
		http.Error(w, "password must be at least 6 characters", http.StatusBadRequest)
		return
	}

	claims := getClaims(r)
	if claims != nil && claims.Role != protocol.RoleSuperAdmin {
		targetUser, err := s.authSvc.GetUser(r.Context(), id)
		if err != nil || targetUser == nil || targetUser.TenantID != claims.TenantID {
			http.Error(w, "forbidden: cannot reset password for user outside your organization", http.StatusForbidden)
			return
		}
	}

	if err := s.authSvc.ResetPassword(r.Context(), id, pw); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	actor := "admin"
	if claims != nil {
		actor = claims.Username
	}
	s.auditSvc.Log(r.Context(), actor, "USER_PASSWORD_RESET", "User", id, r.RemoteAddr, "Password reset by administrator")
	respondJSON(w, http.StatusOK, map[string]string{"message": "password updated successfully", "id": id, "status": "success"})
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	claims := getClaims(r)
	if claims != nil && claims.UserID == id {
		http.Error(w, "cannot delete your own active administrator account", http.StatusForbidden)
		return
	}
	if claims != nil && claims.Role != protocol.RoleSuperAdmin {
		targetUser, err := s.authSvc.GetUser(r.Context(), id)
		if err != nil || targetUser == nil || targetUser.TenantID != claims.TenantID || targetUser.Role == protocol.RoleSuperAdmin {
			http.Error(w, "forbidden: cannot delete user outside your organization", http.StatusForbidden)
			return
		}
	}

	if err := s.authSvc.DeleteUser(r.Context(), id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	actor := "admin"
	if claims != nil {
		actor = claims.Username
	}
	s.auditSvc.Log(r.Context(), actor, "USER_DELETE", "User", id, r.RemoteAddr, fmt.Sprintf("Deleted user %s", id))
	respondJSON(w, http.StatusOK, map[string]string{"message": "user deleted", "id": id})
}

func (s *Server) handleListRBACRoles(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, auth.GetRBACRoles())
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

	// Resolve tenant context
	tenantID := "tnt-default"
	if claims := s.checkAuth(r); claims != nil && claims.TenantID != "" {
		tenantID = claims.TenantID
	}
	if qTenant := r.URL.Query().Get("tenant_id"); qTenant != "" {
		tenantID = qTenant
	}

	// Resolve project context (ensuring foreign key validity)
	if req.ProjectID == "" {
		projs, _ := s.tenantSvc.ListProjects(r.Context(), tenantID)
		if len(projs) > 0 {
			req.ProjectID = projs[0].ID
		} else {
			p, err := s.tenantSvc.CreateProject(r.Context(), tenantID, "default", "", "main")
			if err == nil && p != nil {
				req.ProjectID = p.ID
			}
		}
	}

	// Resolve VPC and Subnet context (ensuring foreign key validity)
	if req.VPCID == "" {
		vpcs, _ := s.vpcSvc.ListVPCs(r.Context(), tenantID)
		if len(vpcs) > 0 {
			req.VPCID = vpcs[0].ID
		} else {
			vpcs, _ = s.vpcSvc.ListVPCs(r.Context(), "tnt-default")
			if len(vpcs) > 0 {
				req.VPCID = vpcs[0].ID
			} else {
				v, err := s.vpcSvc.CreateVPC(r.Context(), tenantID, "default-vpc")
				if err == nil && v != nil {
					req.VPCID = v.ID
				}
			}
		}
	}

	if req.VPCID != "" && req.SubnetID == "" {
		subnets, _ := s.vpcSvc.ListSubnets(r.Context(), req.VPCID)
		if len(subnets) > 0 {
			req.SubnetID = subnets[0].ID
			req.NetworkName = subnets[0].Netavark
		} else {
			sn, err := s.vpcSvc.CreateSubnet(r.Context(), req.VPCID, "default-subnet")
			if err != nil {
				log.Printf("[CreateSubnet Error] %v", err)
			} else if sn != nil {
				req.SubnetID = sn.ID
				req.NetworkName = sn.Netavark
			}
		}
	}

	log.Printf("[Deploy Handler] ProjectID=%q VPCID=%q SubnetID=%q NetworkName=%q", req.ProjectID, req.VPCID, req.SubnetID, req.NetworkName)

	dep, err := s.deployEngine.Deploy(r.Context(), req)
	if err != nil {
		log.Printf("[Deploy Handler Error] %v", err)
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

// -------------------------------------------------------------
// Load Balancer as a Service (LBaaS) API Handlers
// -------------------------------------------------------------

func (s *Server) handleListLoadBalancers(w http.ResponseWriter, r *http.Request) {
	claims := getClaims(r)
	tenantID := r.URL.Query().Get("tenant_id")
	if claims != nil && claims.Role != "admin" && claims.Role != "superadmin" {
		if claims.TenantID != "" {
			tenantID = claims.TenantID
		}
	}

	lbs, err := s.lbSvc.ListLoadBalancers(r.Context(), tenantID)
	if err != nil {
		http.Error(w, "failed listing loadbalancers: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if lbs == nil {
		lbs = []*protocol.LoadBalancer{}
	}
	respondJSON(w, http.StatusOK, lbs)
}

func (s *Server) handleCreateLoadBalancer(w http.ResponseWriter, r *http.Request) {
	var req protocol.CreateLoadBalancerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid payload: "+err.Error(), http.StatusBadRequest)
		return
	}

	claims := getClaims(r)
	if claims != nil && claims.Role != "admin" && claims.Role != "superadmin" {
		if claims.TenantID != "" {
			req.TenantID = claims.TenantID
		}
	}

	lb, err := s.lbSvc.CreateLoadBalancer(r.Context(), req)
	if err != nil {
		http.Error(w, "failed creating loadbalancer: "+err.Error(), http.StatusInternalServerError)
		return
	}

	actor := "system"
	if claims != nil && claims.Username != "" {
		actor = claims.Username
	}
	s.auditSvc.Log(r.Context(), actor, "LB_CREATE", "loadbalancer", lb.ID, r.RemoteAddr, fmt.Sprintf("name=%s, vip=%s", lb.Name, lb.PublicIP))

	respondJSON(w, http.StatusCreated, lb)
}

func (s *Server) handleGetLoadBalancer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "missing loadbalancer id", http.StatusBadRequest)
		return
	}

	lb, err := s.lbSvc.GetLoadBalancer(r.Context(), id)
	if err != nil {
		http.Error(w, "loadbalancer not found: "+err.Error(), http.StatusNotFound)
		return
	}
	respondJSON(w, http.StatusOK, lb)
}

func (s *Server) handleUpdateLoadBalancer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req protocol.CreateLoadBalancerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid payload: "+err.Error(), http.StatusBadRequest)
		return
	}

	lb, err := s.lbSvc.GetLoadBalancer(r.Context(), id)
	if err != nil {
		http.Error(w, "loadbalancer not found: "+err.Error(), http.StatusNotFound)
		return
	}

	if req.Name != "" {
		lb.Name = req.Name
	}
	if req.Description != "" {
		lb.Description = req.Description
	}
	if req.Type != "" {
		lb.Type = req.Type
	}
	if req.PublicIP != "" {
		lb.PublicIP = req.PublicIP
	}

	if err := s.nodeRepo.(*sqlite.Repository).UpdateLoadBalancer(r.Context(), lb); err != nil {
		http.Error(w, "failed updating loadbalancer: "+err.Error(), http.StatusInternalServerError)
		return
	}
	_, _ = s.lbSvc.SynthesizeConfig(r.Context(), lb.ID)
	respondJSON(w, http.StatusOK, lb)
}

func (s *Server) handleDeleteLoadBalancer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "missing loadbalancer id", http.StatusBadRequest)
		return
	}

	if err := s.lbSvc.DeleteLoadBalancer(r.Context(), id); err != nil {
		http.Error(w, "failed deleting loadbalancer: "+err.Error(), http.StatusInternalServerError)
		return
	}

	claims := getClaims(r)
	actor := "system"
	if claims != nil && claims.Username != "" {
		actor = claims.Username
	}
	s.auditSvc.Log(r.Context(), actor, "LB_DELETE", "loadbalancer", id, r.RemoteAddr, "")

	respondJSON(w, http.StatusOK, map[string]string{"status": "deleted", "id": id})
}

func (s *Server) handleCreateLBListener(w http.ResponseWriter, r *http.Request) {
	lbID := r.PathValue("id")
	var req protocol.CreateListenerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid payload: "+err.Error(), http.StatusBadRequest)
		return
	}

	l, err := s.lbSvc.CreateListener(r.Context(), lbID, req)
	if err != nil {
		http.Error(w, "failed creating listener: "+err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusCreated, l)
}

func (s *Server) handleDeleteLBListener(w http.ResponseWriter, r *http.Request) {
	lbID := r.PathValue("id")
	lid := r.PathValue("lid")

	if err := s.lbSvc.DeleteListener(r.Context(), lbID, lid); err != nil {
		http.Error(w, "failed deleting listener: "+err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"status": "deleted", "id": lid})
}

func (s *Server) handleCreateLBPool(w http.ResponseWriter, r *http.Request) {
	lbID := r.PathValue("id")
	var req protocol.CreatePoolRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid payload: "+err.Error(), http.StatusBadRequest)
		return
	}

	p, err := s.lbSvc.CreatePool(r.Context(), lbID, req)
	if err != nil {
		http.Error(w, "failed creating pool: "+err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusCreated, p)
}

func (s *Server) handleDeleteLBPool(w http.ResponseWriter, r *http.Request) {
	lbID := r.PathValue("id")
	pid := r.PathValue("pid")

	if err := s.lbSvc.DeletePool(r.Context(), lbID, pid); err != nil {
		http.Error(w, "failed deleting pool: "+err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"status": "deleted", "id": pid})
}

func (s *Server) handleAttachLBMember(w http.ResponseWriter, r *http.Request) {
	lbID := r.PathValue("id")
	pid := r.PathValue("pid")
	var req protocol.AttachMemberRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid payload: "+err.Error(), http.StatusBadRequest)
		return
	}

	m, err := s.lbSvc.AttachMember(r.Context(), lbID, pid, req)
	if err != nil {
		http.Error(w, "failed attaching member: "+err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusCreated, m)
}

func (s *Server) handleDetachLBMember(w http.ResponseWriter, r *http.Request) {
	lbID := r.PathValue("id")
	mid := r.PathValue("mid")

	if err := s.lbSvc.DetachMember(r.Context(), lbID, mid); err != nil {
		http.Error(w, "failed detaching member: "+err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"status": "deleted", "id": mid})
}

func (s *Server) handleCreateLBPolicy(w http.ResponseWriter, r *http.Request) {
	lbID := r.PathValue("id")
	lid := r.PathValue("lid")
	var req protocol.CreatePolicyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid payload: "+err.Error(), http.StatusBadRequest)
		return
	}

	pol, err := s.lbSvc.CreatePolicy(r.Context(), lbID, lid, req)
	if err != nil {
		http.Error(w, "failed creating policy: "+err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusCreated, pol)
}

func (s *Server) handleDeleteLBPolicy(w http.ResponseWriter, r *http.Request) {
	lbID := r.PathValue("id")
	polid := r.PathValue("polid")

	if err := s.lbSvc.DeletePolicy(r.Context(), lbID, polid); err != nil {
		http.Error(w, "failed deleting policy: "+err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"status": "deleted", "id": polid})
}

func (s *Server) handleGetLBConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	conf, err := s.lbSvc.SynthesizeConfig(r.Context(), id)
	if err != nil {
		http.Error(w, "failed synthesizing config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"id": id, "config": conf})
}

// -------------------------------------------------------------
// DNS as a Service (DNSaaS) Handlers
// -------------------------------------------------------------

func (s *Server) handleListDNSZones(w http.ResponseWriter, r *http.Request) {
	tenantID := r.URL.Query().Get("tenant_id")
	claims := getClaims(r)
	if claims != nil && claims.Role != "admin" && claims.Role != "superadmin" {
		if claims.TenantID != "" {
			tenantID = claims.TenantID
		}
	}

	zones, err := s.dnsSvc.ListZones(r.Context(), tenantID)
	if err != nil {
		http.Error(w, "failed listing dns zones: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if zones == nil {
		zones = make([]*protocol.DNSZone, 0)
	}
	respondJSON(w, http.StatusOK, zones)
}

func (s *Server) handleCreateDNSZone(w http.ResponseWriter, r *http.Request) {
	var req protocol.CreateDNSZoneRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid payload: "+err.Error(), http.StatusBadRequest)
		return
	}

	claims := getClaims(r)
	if claims != nil && claims.Role != "admin" && claims.Role != "superadmin" {
		if claims.TenantID != "" {
			req.TenantID = claims.TenantID
		}
	}

	zone, err := s.dnsSvc.CreateZone(r.Context(), req)
	if err != nil {
		http.Error(w, "failed creating dns zone: "+err.Error(), http.StatusInternalServerError)
		return
	}

	actor := "system"
	if claims != nil && claims.Username != "" {
		actor = claims.Username
	}
	s.auditSvc.Log(r.Context(), actor, "DNS_ZONE_CREATE", "dns_zone", zone.ID, r.RemoteAddr, fmt.Sprintf("name=%s, type=%s", zone.Name, zone.Type))

	respondJSON(w, http.StatusCreated, zone)
}

func (s *Server) handleGetDNSZone(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "missing zone id", http.StatusBadRequest)
		return
	}

	zone, err := s.dnsSvc.GetZone(r.Context(), id)
	if err != nil {
		http.Error(w, "dns zone not found: "+err.Error(), http.StatusNotFound)
		return
	}
	respondJSON(w, http.StatusOK, zone)
}

func (s *Server) handleUpdateDNSZone(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req protocol.CreateDNSZoneRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid payload: "+err.Error(), http.StatusBadRequest)
		return
	}

	zone, err := s.dnsSvc.UpdateZone(r.Context(), id, req)
	if err != nil {
		http.Error(w, "failed updating dns zone: "+err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, zone)
}

func (s *Server) handleDeleteDNSZone(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "missing zone id", http.StatusBadRequest)
		return
	}

	if err := s.dnsSvc.DeleteZone(r.Context(), id); err != nil {
		http.Error(w, "failed deleting dns zone: "+err.Error(), http.StatusInternalServerError)
		return
	}

	claims := getClaims(r)
	actor := "system"
	if claims != nil && claims.Username != "" {
		actor = claims.Username
	}
	s.auditSvc.Log(r.Context(), actor, "DNS_ZONE_DELETE", "dns_zone", id, r.RemoteAddr, "")

	respondJSON(w, http.StatusOK, map[string]string{"status": "deleted", "id": id})
}

func (s *Server) handleListDNSRecords(w http.ResponseWriter, r *http.Request) {
	zoneID := r.PathValue("id")
	if zoneID == "" {
		http.Error(w, "missing zone id", http.StatusBadRequest)
		return
	}

	records, err := s.dnsSvc.ListRecords(r.Context(), zoneID)
	if err != nil {
		http.Error(w, "failed listing dns records: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if records == nil {
		records = make([]*protocol.DNSRecord, 0)
	}
	respondJSON(w, http.StatusOK, records)
}

func (s *Server) handleCreateDNSRecord(w http.ResponseWriter, r *http.Request) {
	zoneID := r.PathValue("id")
	var req protocol.CreateDNSRecordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid payload: "+err.Error(), http.StatusBadRequest)
		return
	}

	rec, err := s.dnsSvc.CreateRecord(r.Context(), zoneID, req)
	if err != nil {
		http.Error(w, "failed creating dns record: "+err.Error(), http.StatusInternalServerError)
		return
	}

	claims := getClaims(r)
	actor := "system"
	if claims != nil && claims.Username != "" {
		actor = claims.Username
	}
	s.auditSvc.Log(r.Context(), actor, "DNS_RECORD_CREATE", "dns_record", rec.ID, r.RemoteAddr, fmt.Sprintf("zone=%s, name=%s, type=%s, val=%s", zoneID, rec.Name, rec.Type, rec.Value))

	respondJSON(w, http.StatusCreated, rec)
}

func (s *Server) handleGetDNSRecord(w http.ResponseWriter, r *http.Request) {
	rid := r.PathValue("rid")
	rec, err := s.dnsSvc.GetRecord(r.Context(), rid)
	if err != nil {
		http.Error(w, "dns record not found: "+err.Error(), http.StatusNotFound)
		return
	}
	respondJSON(w, http.StatusOK, rec)
}

func (s *Server) handleUpdateDNSRecord(w http.ResponseWriter, r *http.Request) {
	zoneID := r.PathValue("id")
	rid := r.PathValue("rid")
	var req protocol.CreateDNSRecordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid payload: "+err.Error(), http.StatusBadRequest)
		return
	}

	rec, err := s.dnsSvc.UpdateRecord(r.Context(), zoneID, rid, req)
	if err != nil {
		http.Error(w, "failed updating dns record: "+err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, rec)
}

func (s *Server) handleDeleteDNSRecord(w http.ResponseWriter, r *http.Request) {
	zoneID := r.PathValue("id")
	rid := r.PathValue("rid")

	if err := s.dnsSvc.DeleteRecord(r.Context(), zoneID, rid); err != nil {
		http.Error(w, "failed deleting dns record: "+err.Error(), http.StatusInternalServerError)
		return
	}

	claims := getClaims(r)
	actor := "system"
	if claims != nil && claims.Username != "" {
		actor = claims.Username
	}
	s.auditSvc.Log(r.Context(), actor, "DNS_RECORD_DELETE", "dns_record", rid, r.RemoteAddr, "")

	respondJSON(w, http.StatusOK, map[string]string{"status": "deleted", "id": rid})
}

func (s *Server) handleExportBINDZone(w http.ResponseWriter, r *http.Request) {
	zoneID := r.PathValue("id")
	bindConf, err := s.dnsSvc.ExportBINDZone(r.Context(), zoneID)
	if err != nil {
		http.Error(w, "failed exporting BIND zone: "+err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"id": zoneID, "bind_zone": bindConf})
}

func (s *Server) handleQueryDNS(w http.ResponseWriter, r *http.Request) {
	var req protocol.DNSQueryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid payload: "+err.Error(), http.StatusBadRequest)
		return
	}

	resp, err := s.dnsSvc.Query(r.Context(), req)
	if err != nil {
		http.Error(w, "dns query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, resp)
}

func respondJSON(w http.ResponseWriter, code int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(payload)
}
