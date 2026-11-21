package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"procloud.io/procloud/internal/database/sqlite"
	"procloud.io/procloud/pkg/podman"
)

func TestMandatoryAuthentication(t *testing.T) {
	ctx := context.Background()

	// 1. Initialise database and mock runtime
	dbPath := t.TempDir() + "/test_auth.db"
	db, err := sqlite.Open(sqlite.DefaultConfig(dbPath))
	if err != nil {
		t.Fatalf("failed opening test db: %v", err)
	}
	defer db.Close()

	repo := sqlite.NewRepository(db)
	mockRuntime := podman.NewMockRuntime()
	mockRuntime.RunContainer(context.Background(), podman.RunOptions{
		Name:  "progit",
		Image: "docker.io/library/alpine:latest",
	})

	server := NewServer(db, repo, mockRuntime)

	// 2. Test Unauthenticated access to protected endpoints -> MUST return 401
	protectedEndpoints := []struct {
		method string
		path   string
	}{
		{"GET", "/api/v1/containers"},
		{"GET", "/api/v1/pods"},
		{"GET", "/api/v1/images"},
		{"GET", "/api/v1/podman/networks"},
		{"GET", "/api/v1/podman/volumes"},
		{"GET", "/api/v1/podman/secrets"},
		{"GET", "/api/v1/database/status"},
		{"GET", "/api/v1/workloads"},
		{"GET", "/api/v1/nodes"},
		{"GET", "/api/v1/auth/me"},
		{"GET", "/api/v1/containers/progit/top"},
		{"GET", "/api/v1/containers/progit/stats"},
		{"GET", "/api/v1/containers/progit/containerfile"},
		{"GET", "/api/v1/containers/progit/kube"},
		{"GET", "/api/v1/workflows/runs"},
	}

	for _, ep := range protectedEndpoints {
		req := httptest.NewRequest(ep.method, ep.path, nil)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("[%s %s] expected 401 Unauthorized without token, got %d", ep.method, ep.path, rec.Code)
		}
	}

	// 3. Test Public endpoint (/healthz) -> MUST return 200 without token
	{
		req := httptest.NewRequest("GET", "/healthz", nil)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("[GET /healthz] expected 200 OK, got %d", rec.Code)
		}
	}

	// 4. Test Invalid token -> MUST return 401
	{
		req := httptest.NewRequest("GET", "/api/v1/containers", nil)
		req.Header.Set("Authorization", "Bearer invalid-garbage-token-here")
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("[GET /api/v1/containers with invalid token] expected 401, got %d", rec.Code)
		}
	}

	// 5. Test Login with seeded Default Admin (admin / admin123) -> MUST succeed and return JWT
	loginBody, _ := json.Marshal(map[string]string{
		"username": "admin",
		"password": "admin123",
	})
	reqLogin := httptest.NewRequest("POST", "/api/v1/auth/login", bytes.NewReader(loginBody))
	reqLogin.Header.Set("Content-Type", "application/json")
	recLogin := httptest.NewRecorder()
	server.ServeHTTP(recLogin, reqLogin)

	if recLogin.Code != http.StatusOK {
		t.Fatalf("[POST /api/v1/auth/login] expected 200 OK, got %d: %s", recLogin.Code, recLogin.Body.String())
	}

	var loginResp struct {
		Token string `json:"token"`
		User  struct {
			Username string `json:"username"`
			Role     string `json:"role"`
		} `json:"user"`
	}
	if err := json.Unmarshal(recLogin.Body.Bytes(), &loginResp); err != nil {
		t.Fatalf("failed decoding login response: %v", err)
	}

	if loginResp.Token == "" {
		t.Fatalf("login response token is empty")
	}
	if loginResp.User.Username != "admin" || loginResp.User.Role != "admin" {
		t.Fatalf("unexpected user details: %+v", loginResp.User)
	}

	// 6. Test Authenticated access with valid Bearer token -> MUST succeed with 200
	for _, ep := range protectedEndpoints {
		req := httptest.NewRequest(ep.method, ep.path, nil)
		req.Header.Set("Authorization", "Bearer "+loginResp.Token)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("[%s %s with Bearer token] expected 200 OK, got %d: %s", ep.method, ep.path, rec.Code, rec.Body.String())
		}
	}

	// 7. Verify /api/v1/auth/me returns the authenticated user
	reqMe := httptest.NewRequest("GET", "/api/v1/auth/me", nil)
	reqMe.Header.Set("Authorization", "Bearer "+loginResp.Token)
	recMe := httptest.NewRecorder()
	server.ServeHTTP(recMe, reqMe)

	if recMe.Code != http.StatusOK {
		t.Fatalf("[GET /api/v1/auth/me] expected 200 OK, got %d", recMe.Code)
	}

	// 7a. Verify Play Kube and Image Build with authentication
	{
		// Unauthenticated play kube -> 401
		reqUnauthPlay := httptest.NewRequest("POST", "/api/v1/pods/kube/play", strings.NewReader(`apiVersion: v1`))
		recUnauthPlay := httptest.NewRecorder()
		server.ServeHTTP(recUnauthPlay, reqUnauthPlay)
		if recUnauthPlay.Code != http.StatusUnauthorized {
			t.Errorf("expected 401 for unauth play kube, got %d", recUnauthPlay.Code)
		}

		// Authenticated play kube -> 200
		reqAuthPlay := httptest.NewRequest("POST", "/api/v1/pods/kube/play", strings.NewReader("apiVersion: v1\nkind: Pod\nmetadata:\n  name: auth-pod\n"))
		reqAuthPlay.Header.Set("Authorization", "Bearer "+loginResp.Token)
		recAuthPlay := httptest.NewRecorder()
		server.ServeHTTP(recAuthPlay, reqAuthPlay)
		if recAuthPlay.Code != http.StatusOK {
			t.Errorf("expected 200 for auth play kube, got %d: %s", recAuthPlay.Code, recAuthPlay.Body.String())
		}

		// Authenticated image build -> 201
		buildJSON, _ := json.Marshal(map[string]string{
			"tag":           "auth-test:v1",
			"containerfile": "FROM alpine:latest\nCMD [\"echo\", \"ok\"]\n",
		})
		reqAuthBuild := httptest.NewRequest("POST", "/api/v1/images/build", bytes.NewReader(buildJSON))
		reqAuthBuild.Header.Set("Content-Type", "application/json")
		reqAuthBuild.Header.Set("Authorization", "Bearer "+loginResp.Token)
		recAuthBuild := httptest.NewRecorder()
		server.ServeHTTP(recAuthBuild, reqAuthBuild)
		if recAuthBuild.Code != http.StatusCreated {
			t.Errorf("expected 201 for auth build image, got %d: %s", recAuthBuild.Code, recAuthBuild.Body.String())
		}

		// Authenticated Secret Management
		secCreateJSON, _ := json.Marshal(podman.SecretCreateOptions{
			Name:   "jwt_signing_key",
			Driver: "file",
			Data:   "super-secret-signing-token-9988",
			Labels: map[string]string{"env": "prod"},
		})
		reqCreateSec := httptest.NewRequest("POST", "/api/v1/podman/secrets", bytes.NewReader(secCreateJSON))
		reqCreateSec.Header.Set("Content-Type", "application/json")
		reqCreateSec.Header.Set("Authorization", "Bearer "+loginResp.Token)
		recCreateSec := httptest.NewRecorder()
		server.ServeHTTP(recCreateSec, reqCreateSec)
		if recCreateSec.Code != http.StatusCreated {
			t.Errorf("expected 201 for auth create secret, got %d: %s", recCreateSec.Code, recCreateSec.Body.String())
		}

		// Authenticated Secret List
		reqListSec := httptest.NewRequest("GET", "/api/v1/podman/secrets", nil)
		reqListSec.Header.Set("Authorization", "Bearer "+loginResp.Token)
		recListSec := httptest.NewRecorder()
		server.ServeHTTP(recListSec, reqListSec)
		if recListSec.Code != http.StatusOK {
			t.Errorf("expected 200 for auth list secrets, got %d: %s", recListSec.Code, recListSec.Body.String())
		}

		// Authenticated Secret Inspect with Reveal
		reqInspectSec := httptest.NewRequest("GET", "/api/v1/podman/secrets/jwt_signing_key?showsecret=true", nil)
		reqInspectSec.Header.Set("Authorization", "Bearer "+loginResp.Token)
		recInspectSec := httptest.NewRecorder()
		server.ServeHTTP(recInspectSec, reqInspectSec)
		if recInspectSec.Code != http.StatusOK {
			t.Errorf("expected 200 for auth inspect secret, got %d: %s", recInspectSec.Code, recInspectSec.Body.String())
		}
		var inspectedSec podman.SecretInspect
		_ = json.Unmarshal(recInspectSec.Body.Bytes(), &inspectedSec)
		if inspectedSec.SecretData != "super-secret-signing-token-9988" {
			t.Errorf("expected revealed secret data, got %s", inspectedSec.SecretData)
		}

		// Authenticated Secret Delete
		reqDelSec := httptest.NewRequest("DELETE", "/api/v1/podman/secrets/jwt_signing_key", nil)
		reqDelSec.Header.Set("Authorization", "Bearer "+loginResp.Token)
		recDelSec := httptest.NewRecorder()
		server.ServeHTTP(recDelSec, reqDelSec)
		if recDelSec.Code != http.StatusOK {
			t.Errorf("expected 200 for auth delete secret, got %d: %s", recDelSec.Code, recDelSec.Body.String())
		}
	}

	// 8. Test that public self-registration is disabled (returns 403 Forbidden)
	regBody, _ := json.Marshal(map[string]string{
		"username": "developer1",
		"email":    "dev1@procloud.io",
		"password": "Password123!",
		"role":     "developer",
	})
	reqReg := httptest.NewRequest("POST", "/api/v1/auth/register", bytes.NewReader(regBody))
	reqReg.Header.Set("Content-Type", "application/json")
	recReg := httptest.NewRecorder()
	server.ServeHTTP(recReg, reqReg)

	if recReg.Code != http.StatusForbidden {
		t.Fatalf("[POST /api/v1/auth/register] expected 403 Forbidden, got %d: %s", recReg.Code, recReg.Body.String())
	}

	// 9. Test Super Admin provisioning a user via POST /api/v1/users
	createUserBody, _ := json.Marshal(map[string]string{
		"username":     "developer1",
		"email":        "dev1@procloud.io",
		"password":     "Password123!",
		"role":         "developer",
		"tenant_id":    "tnt-default",
		"display_name": "Developer One",
	})
	reqCreateUser := httptest.NewRequest("POST", "/api/v1/users", bytes.NewReader(createUserBody))
	reqCreateUser.Header.Set("Content-Type", "application/json")
	reqCreateUser.Header.Set("Authorization", "Bearer "+loginResp.Token)
	recCreateUser := httptest.NewRecorder()
	server.ServeHTTP(recCreateUser, reqCreateUser)

	if recCreateUser.Code != http.StatusCreated {
		t.Fatalf("[POST /api/v1/users] expected 201 Created, got %d: %s", recCreateUser.Code, recCreateUser.Body.String())
	}

	// 10. Test Super Admin creating Organization with initial Org Admin
	createOrgBody, _ := json.Marshal(map[string]any{
		"name":               "Test Org Admin Tenant",
		"description":        "Testing initial org admin creation",
		"plan":               "enterprise",
		"admin_username":     "tenant.admin",
		"admin_email":        "tadmin@tenant.local",
		"admin_password":     "TenantPass123!",
		"admin_display_name": "Tenant Administrator",
	})
	reqCreateOrg := httptest.NewRequest("POST", "/api/v1/organizations", bytes.NewReader(createOrgBody))
	reqCreateOrg.Header.Set("Content-Type", "application/json")
	reqCreateOrg.Header.Set("Authorization", "Bearer "+loginResp.Token)
	recCreateOrg := httptest.NewRecorder()
	server.ServeHTTP(recCreateOrg, reqCreateOrg)

	if recCreateOrg.Code != http.StatusCreated {
		t.Fatalf("[POST /api/v1/organizations] expected 201 Created, got %d: %s", recCreateOrg.Code, recCreateOrg.Body.String())
	}

	// Login as newly provisioned Org Admin
	orgAdminLoginBody, _ := json.Marshal(map[string]string{
		"username": "tenant.admin",
		"password": "TenantPass123!",
	})
	reqOrgAdminLogin := httptest.NewRequest("POST", "/api/v1/auth/login", bytes.NewReader(orgAdminLoginBody))
	reqOrgAdminLogin.Header.Set("Content-Type", "application/json")
	recOrgAdminLogin := httptest.NewRecorder()
	server.ServeHTTP(recOrgAdminLogin, reqOrgAdminLogin)
	if recOrgAdminLogin.Code != http.StatusOK {
		t.Fatalf("new org admin login failed: %d: %s", recOrgAdminLogin.Code, recOrgAdminLogin.Body.String())
	}
	var orgAdminLoginResp struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(recOrgAdminLogin.Body.Bytes(), &orgAdminLoginResp)

	// Verify Org Admin CANNOT create another Org Admin (must return 403 Forbidden)
	badOrgAdminBody, _ := json.Marshal(map[string]string{
		"username":     "illegal.admin",
		"email":        "illegal@tenant.local",
		"password":     "Password123!",
		"role":         "org_admin",
		"display_name": "Illegal Admin",
	})
	reqBadOrgAdmin := httptest.NewRequest("POST", "/api/v1/users", bytes.NewReader(badOrgAdminBody))
	reqBadOrgAdmin.Header.Set("Content-Type", "application/json")
	reqBadOrgAdmin.Header.Set("Authorization", "Bearer "+orgAdminLoginResp.Token)
	recBadOrgAdmin := httptest.NewRecorder()
	server.ServeHTTP(recBadOrgAdmin, reqBadOrgAdmin)

	if recBadOrgAdmin.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden when Org Admin tries to create another Org Admin, got %d: %s", recBadOrgAdmin.Code, recBadOrgAdmin.Body.String())
	}

	// Login as newly created developer user
	devLoginBody, _ := json.Marshal(map[string]string{
		"username": "developer1",
		"password": "Password123!",
	})
	reqDevLogin := httptest.NewRequest("POST", "/api/v1/auth/login", bytes.NewReader(devLoginBody))
	reqDevLogin.Header.Set("Content-Type", "application/json")
	recDevLogin := httptest.NewRecorder()
	server.ServeHTTP(recDevLogin, reqDevLogin)

	if recDevLogin.Code != http.StatusOK {
		t.Fatalf("new user login failed: %d: %s", recDevLogin.Code, recDevLogin.Body.String())
	}

	_ = ctx
	t.Log("SUCCESS: Mandatory authentication and RBAC governance verified!")
}
