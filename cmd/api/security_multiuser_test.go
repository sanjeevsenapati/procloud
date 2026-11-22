package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"procloud.io/procloud/internal/database/sqlite"
	"procloud.io/procloud/pkg/podman"
	"procloud.io/procloud/pkg/protocol"
)

func TestSecurityAndVulnerabilityAssessment(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_va.db")
	db, err := sqlite.Open(sqlite.DefaultConfig(dbPath))
	if err != nil {
		t.Fatalf("sqlite.Open failed: %v", err)
	}
	defer db.Close()

	repo := sqlite.NewRepository(db)
	mockRuntime := podman.NewMockRuntime()
	server := NewServer(db, repo, mockRuntime)

	// 1. Verify HTTP Security Headers on all responses
	req := httptest.NewRequest("GET", "/healthz", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", rec.Code)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("missing or invalid X-Content-Type-Options: %s", rec.Header().Get("X-Content-Type-Options"))
	}
	if rec.Header().Get("X-Frame-Options") != "SAMEORIGIN" {
		t.Errorf("missing or invalid X-Frame-Options: %s", rec.Header().Get("X-Frame-Options"))
	}
	if rec.Header().Get("Referrer-Policy") != "strict-origin-when-cross-origin" {
		t.Errorf("missing or invalid Referrer-Policy: %s", rec.Header().Get("Referrer-Policy"))
	}
	if rec.Header().Get("Content-Security-Policy") == "" {
		t.Errorf("missing Content-Security-Policy header")
	}

	// 2. Verify CORS Preflight OPTIONS Handling
	optionsReq := httptest.NewRequest("OPTIONS", "/api/v1/containers", nil)
	optionsReq.Header.Set("Origin", "http://localhost:3000")
	optionsRec := httptest.NewRecorder()
	server.ServeHTTP(optionsRec, optionsReq)

	if optionsRec.Code != http.StatusNoContent {
		t.Errorf("OPTIONS preflight expected 204 No Content, got %d", optionsRec.Code)
	}
	if optionsRec.Header().Get("Access-Control-Allow-Origin") != "http://localhost:3000" {
		t.Errorf("expected CORS Allow-Origin http://localhost:3000, got %s", optionsRec.Header().Get("Access-Control-Allow-Origin"))
	}

	// 3. Verify SQL Injection Defense on SQLite CheckpointWAL
	ctx := context.Background()
	maliciousMode := "TRUNCATE; DROP TABLE users; --"
	if err := db.CheckpointWAL(ctx, maliciousMode); err == nil {
		t.Errorf("expected CheckpointWAL to reject malicious mode '%s', but it passed", maliciousMode)
	}

	// 4. Verify Path Traversal Defense on SQLite Backup
	maliciousBackupPath := "../../etc/passwd"
	if err := db.Backup(ctx, maliciousBackupPath); err == nil {
		t.Errorf("expected Backup to reject traversal path '%s', but it passed", maliciousBackupPath)
	}

	// 5. Verify RBAC Viewer Restrictions
	// Create an auditor / viewer user
	viewer, err := server.authSvc.CreateUserWithOrg(ctx, "auditor_sec", "auditor@sec.test", "viewerPass123", protocol.RoleViewer, "tnt-default", "Auditor")
	if err != nil {
		t.Fatalf("failed creating viewer user: %v", err)
	}
	viewerToken, _, err := server.authSvc.Login(ctx, viewer.Username, "viewerPass123")
	if err != nil {
		t.Fatalf("failed logging in as viewer: %v", err)
	}

	// 5a. Viewer attempting modification (POST /api/v1/containers) -> MUST return 403 Forbidden
	runBody, _ := json.Marshal(map[string]any{"image": "alpine", "name": "forbidden-ctr"})
	vReq := httptest.NewRequest("POST", "/api/v1/containers", bytes.NewReader(runBody))
	vReq.Header.Set("Authorization", "Bearer "+viewerToken)
	vReq.Header.Set("Content-Type", "application/json")
	vRec := httptest.NewRecorder()
	server.ServeHTTP(vRec, vReq)

	if vRec.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for viewer running container, got %d: %s", vRec.Code, vRec.Body.String())
	}

	// 5b. Create a secret in mock runtime
	mockRuntime.CreateSecret(ctx, podman.SecretCreateOptions{
		Name: "top-secret-token",
		Data: "super-secret-api-key-9999",
	})

	// Viewer attempting to reveal secret plaintext (GET /api/v1/podman/secrets/top-secret-token?showsecret=true) -> MUST return 403
	revealReq := httptest.NewRequest("GET", "/api/v1/podman/secrets/top-secret-token?showsecret=true", nil)
	revealReq.Header.Set("Authorization", "Bearer "+viewerToken)
	revealRec := httptest.NewRecorder()
	server.ServeHTTP(revealRec, revealReq)

	if revealRec.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for viewer revealing secret plaintext, got %d: %s", revealRec.Code, revealRec.Body.String())
	}

	// Viewer reading secret metadata without plaintext (?showsecret=false) -> MUST be allowed (200)
	metaReq := httptest.NewRequest("GET", "/api/v1/podman/secrets/top-secret-token", nil)
	metaReq.Header.Set("Authorization", "Bearer "+viewerToken)
	metaRec := httptest.NewRecorder()
	server.ServeHTTP(metaRec, metaReq)

	if metaRec.Code != http.StatusOK {
		t.Errorf("expected 200 OK for viewer inspecting secret metadata, got %d: %s", metaRec.Code, metaRec.Body.String())
	}
}

func TestSimultaneousMultiUserLoginAndConcurrentUse(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_multiuser.db")
	db, err := sqlite.Open(sqlite.DefaultConfig(dbPath))
	if err != nil {
		t.Fatalf("sqlite.Open failed: %v", err)
	}
	defer db.Close()

	repo := sqlite.NewRepository(db)
	mockRuntime := podman.NewMockRuntime()
	server := NewServer(db, repo, mockRuntime)
	ctx := context.Background()

	// 1. Seed 10 distinct users with different roles
	userCount := 10
	type userCreds struct {
		username string
		password string
		role     string
	}
	users := make([]userCreds, userCount)
	for i := 0; i < userCount; i++ {
		role := protocol.RoleDeveloper
		if i == 0 {
			role = protocol.RoleSuperAdmin
		} else if i == 1 {
			role = protocol.RoleOrgAdmin
		} else if i == 2 {
			role = protocol.RoleViewer
		}

		u := userCreds{
			username: fmt.Sprintf("user_concurrent_%d", i),
			password: fmt.Sprintf("password_%d_strong", i),
			role:     role,
		}
		users[i] = u
		_, err := server.authSvc.CreateUserWithOrg(ctx, u.username, fmt.Sprintf("%s@procloud.local", u.username), u.password, u.role, "tnt-default", fmt.Sprintf("User %d", i))
		if err != nil {
			t.Fatalf("failed seeding user %d: %v", i, err)
		}
	}

	// 2. Perform Simultaneous Logins from 10 concurrent goroutines
	tokens := make([]string, userCount)
	var wg sync.WaitGroup
	loginErrors := make(chan error, userCount)

	for i := 0; i < userCount; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			loginPayload, _ := json.Marshal(map[string]string{
				"username": users[idx].username,
				"password": users[idx].password,
			})
			req := httptest.NewRequest("POST", "/api/v1/auth/login", bytes.NewReader(loginPayload))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				loginErrors <- fmt.Errorf("user %d failed to login (status %d): %s", idx, rec.Code, rec.Body.String())
				return
			}

			var resp struct {
				Token string `json:"token"`
				User  struct {
					Username string `json:"username"`
					Role     string `json:"role"`
				} `json:"user"`
			}
			if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
				loginErrors <- fmt.Errorf("user %d invalid json response: %v", idx, err)
				return
			}
			if resp.User.Username != users[idx].username {
				loginErrors <- fmt.Errorf("user %d received wrong username: %s", idx, resp.User.Username)
				return
			}
			tokens[idx] = resp.Token
		}(i)
	}
	wg.Wait()
	close(loginErrors)

	for err := range loginErrors {
		t.Fatalf("Simultaneous login failure: %v", err)
	}

	// 3. Perform Concurrent Mixed Reads and Writes by All Users Simultaneously
	concurrencyOps := 20
	var opsWg sync.WaitGroup
	opsErrors := make(chan error, concurrencyOps)

	for j := 0; j < concurrencyOps; j++ {
		opsWg.Add(1)
		go func(opID int) {
			defer opsWg.Done()
			userIndex := opID % userCount
			token := tokens[userIndex]
			role := users[userIndex].role

			if role != protocol.RoleViewer {
				// Write Operation: Create a DNS Zone
				zonePayload, _ := json.Marshal(map[string]any{
					"name":        fmt.Sprintf("zone-user-%d-%d.internal", userIndex, opID),
					"description": fmt.Sprintf("Zone created concurrently by user %d", userIndex),
					"ttl":         300,
				})
				req := httptest.NewRequest("POST", "/api/v1/dns/zones", bytes.NewReader(zonePayload))
				req.Header.Set("Authorization", "Bearer "+token)
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()
				server.ServeHTTP(rec, req)

				if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
					opsErrors <- fmt.Errorf("op %d write failed (code %d): %s", opID, rec.Code, rec.Body.String())
					return
				}
			}

			// Read Operation: List Zones and List Containers
			reqList := httptest.NewRequest("GET", "/api/v1/dns/zones", nil)
			reqList.Header.Set("Authorization", "Bearer "+token)
			recList := httptest.NewRecorder()
			server.ServeHTTP(recList, reqList)

			if recList.Code != http.StatusOK {
				opsErrors <- fmt.Errorf("op %d read failed (code %d): %s", opID, recList.Code, recList.Body.String())
				return
			}
		}(j)
	}
	opsWg.Wait()
	close(opsErrors)

	for err := range opsErrors {
		t.Errorf("Concurrent operation error: %v", err)
	}
}

func TestProGitOSSIntegrationAndWebhook(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_progit.db")
	db, err := sqlite.Open(sqlite.DefaultConfig(dbPath))
	if err != nil {
		t.Fatalf("sqlite.Open failed: %v", err)
	}
	defer db.Close()

	repo := sqlite.NewRepository(db)
	mockRuntime := podman.NewMockRuntime()
	server := NewServer(db, repo, mockRuntime)
	ctx := context.Background()

	// Admin token
	admin, err := server.authSvc.CreateUserWithOrg(ctx, "progit_admin", "admin@progit.local", "progitAdmin123", protocol.RoleSuperAdmin, "tnt-default", "ProGit Admin")
	if err != nil {
		t.Fatalf("failed creating admin: %v", err)
	}
	token, _, _ := server.authSvc.Login(ctx, admin.Username, "progitAdmin123")

	// 1. GET /api/v1/integrations/progit -> inspect status
	reqStatus := httptest.NewRequest("GET", "/api/v1/integrations/progit", nil)
	reqStatus.Header.Set("Authorization", "Bearer "+token)
	recStatus := httptest.NewRecorder()
	server.ServeHTTP(recStatus, reqStatus)

	if recStatus.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/integrations/progit expected 200, got %d: %s", recStatus.Code, recStatus.Body.String())
	}
	var statusResp map[string]any
	_ = json.NewDecoder(recStatus.Body).Decode(&statusResp)
	if statusResp["container_name"] != "procloud-progit-oss" {
		t.Errorf("expected container_name procloud-progit-oss, got %v", statusResp["container_name"])
	}

	// 2. POST /api/v1/integrations/progit/launch -> launches container
	reqLaunch := httptest.NewRequest("POST", "/api/v1/integrations/progit/launch", nil)
	reqLaunch.Header.Set("Authorization", "Bearer "+token)
	recLaunch := httptest.NewRecorder()
	server.ServeHTTP(recLaunch, reqLaunch)

	if recLaunch.Code != http.StatusOK {
		t.Fatalf("POST /api/v1/integrations/progit/launch expected 200, got %d: %s", recLaunch.Code, recLaunch.Body.String())
	}
	var launchResp map[string]any
	_ = json.NewDecoder(recLaunch.Body).Decode(&launchResp)
	if launchResp["status"] != "running" {
		t.Errorf("expected status running after launch, got %v", launchResp["status"])
	}

	// 3. POST /api/v1/webhooks/progit -> push webhook trigger
	webhookPayload := map[string]any{
		"ref":   "refs/heads/main",
		"after": "e7b99c15d489f66bb6b8ec4b93b82736b4e72322",
		"repository": map[string]any{
			"name":           "cloud-store",
			"full_name":      "sanjeev/cloud-store",
			"clone_url":      "http://localhost:3000/sanjeev/cloud-store.git",
			"default_branch": "main",
		},
		"pusher": map[string]any{
			"name":  "sanjeev",
			"email": "sanjeev@progit.local",
		},
	}
	body, _ := json.Marshal(webhookPayload)
	reqHook := httptest.NewRequest("POST", "/api/v1/webhooks/progit", bytes.NewReader(body))
	reqHook.Header.Set("Content-Type", "application/json")
	recHook := httptest.NewRecorder()
	server.ServeHTTP(recHook, reqHook)

	if recHook.Code != http.StatusAccepted {
		t.Fatalf("POST /api/v1/webhooks/progit expected 202 Accepted, got %d: %s", recHook.Code, recHook.Body.String())
	}
	var hookResp map[string]any
	_ = json.NewDecoder(recHook.Body).Decode(&hookResp)
	if hookResp["status"] != "QUEUED" {
		t.Errorf("expected job status QUEUED, got %v", hookResp["status"])
	}
	if hookResp["repository"] != "cloud-store" {
		t.Errorf("expected repository cloud-store, got %v", hookResp["repository"])
	}

	// 4. Test Webhook Secret Protection (if configured)
	os.Setenv("PROCLOUD_WEBHOOK_SECRET", "super-secret-hook-key-888")
	defer os.Unsetenv("PROCLOUD_WEBHOOK_SECRET")

	// Missing secret -> 401
	reqMissingSec := httptest.NewRequest("POST", "/api/v1/webhooks/progit", bytes.NewReader(body))
	reqMissingSec.Header.Set("Content-Type", "application/json")
	recMissingSec := httptest.NewRecorder()
	server.ServeHTTP(recMissingSec, reqMissingSec)
	if recMissingSec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized for webhook with missing secret, got %d", recMissingSec.Code)
	}

	// Valid secret -> 202
	reqValidSec := httptest.NewRequest("POST", "/api/v1/webhooks/progit", bytes.NewReader(body))
	reqValidSec.Header.Set("Content-Type", "application/json")
	reqValidSec.Header.Set("X-Progit-Token", "super-secret-hook-key-888")
	recValidSec := httptest.NewRecorder()
	server.ServeHTTP(recValidSec, reqValidSec)
	if recValidSec.Code != http.StatusAccepted {
		t.Errorf("expected 202 Accepted for webhook with valid secret, got %d", recValidSec.Code)
	}
}
