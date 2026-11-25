package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"procloud.io/procloud/internal/database/sqlite"
	"procloud.io/procloud/pkg/podman"
)

func TestNetworkDetailsAndDependencyHierarchy(t *testing.T) {
	dbPath := t.TempDir() + "/test_net_dep.db"
	db, err := sqlite.Open(sqlite.DefaultConfig(dbPath))
	if err != nil {
		t.Fatalf("failed opening test db: %v", err)
	}
	defer db.Close()

	repo := sqlite.NewRepository(db)
	mockRuntime := podman.NewMockRuntime()

	// Seed network with a child container attached
	mockRuntime.Networks["app-backend-net"] = &podman.NetworkSummary{
		ID:               "net-12345",
		Name:             "app-backend-net",
		Driver:           "bridge",
		Subnet:           "10.89.5.0/24",
		Gateway:          "10.89.5.1",
		NetworkInterface: "podman9",
		Labels:           map[string]string{"tier": "backend"},
	}

	mockRuntime.Containers["child-ctr-1"] = &podman.ContainerStatus{
		ID:          "child-ctr-1",
		Name:        "api-worker",
		Image:       "alpine:latest",
		Status:      "running",
		NetworkName: "app-backend-net",
		IPAddress:   "10.89.5.12",
		Gateway:     "10.89.5.1",
	}

	// Seed another network with no dependencies
	mockRuntime.Networks["empty-net"] = &podman.NetworkSummary{
		ID:      "net-67890",
		Name:    "empty-net",
		Driver:  "bridge",
		Subnet:  "10.89.6.0/24",
		Gateway: "10.89.6.1",
	}

	server := NewServer(db, repo, mockRuntime)

	// Admin login
	loginBody, _ := json.Marshal(map[string]string{
		"username": "admin",
		"password": "admin123",
	})
	loginReq := httptest.NewRequest("POST", "/api/v1/auth/login", bytes.NewReader(loginBody))
	loginReq.Header.Set("Content-Type", "application/json")
	loginRec := httptest.NewRecorder()
	server.ServeHTTP(loginRec, loginReq)

	if loginRec.Code != http.StatusOK {
		t.Fatalf("failed login: %s", loginRec.Body.String())
	}
	var loginResp struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(loginRec.Body.Bytes(), &loginResp)
	token := loginResp.Token

	// 1. Test Inspect Network details
	t.Run("Inspect Network Details", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/v1/podman/networks/app-backend-net", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}

		var insp podman.NetworkInspect
		if err := json.Unmarshal(rec.Body.Bytes(), &insp); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		if insp.Name != "app-backend-net" {
			t.Errorf("expected name 'app-backend-net', got %s", insp.Name)
		}
		if len(insp.ConnectedContainers) != 1 || insp.ConnectedContainers[0].Name != "api-worker" {
			t.Errorf("expected child container api-worker, got %v", insp.ConnectedContainers)
		}
	})

	// 2. Test Deletion BLOCKED when child components exist without cascade
	t.Run("Delete Blocked Due to Child Dependency", func(t *testing.T) {
		req := httptest.NewRequest("DELETE", "/api/v1/podman/networks/app-backend-net", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)

		if rec.Code != http.StatusConflict {
			t.Fatalf("expected 409 Conflict, got %d: %s", rec.Code, rec.Body.String())
		}

		var errResp map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &errResp)
		if errResp["code"] != "NETWORK_HAS_DEPENDENCIES" {
			t.Errorf("expected code NETWORK_HAS_DEPENDENCIES, got %v", errResp["code"])
		}

		// Verify network still exists!
		if _, ok := mockRuntime.Networks["app-backend-net"]; !ok {
			t.Errorf("network was deleted despite having child dependencies!")
		}
	})

	// 3. Test Deletion Allowed with Cascade after removing child components
	t.Run("Cascade Delete Child Components Then Network", func(t *testing.T) {
		req := httptest.NewRequest("DELETE", "/api/v1/podman/networks/app-backend-net?cascade=true", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
		}

		// Verify child container was removed
		if _, ok := mockRuntime.Containers["child-ctr-1"]; ok {
			t.Errorf("child container was not removed during cascade delete!")
		}

		// Verify network was removed
		if _, ok := mockRuntime.Networks["app-backend-net"]; ok {
			t.Errorf("network was not removed after cascade!")
		}
	})

	// 4. Test Deletion Directly Allowed on empty network
	t.Run("Delete Network With No Dependencies", func(t *testing.T) {
		req := httptest.NewRequest("DELETE", "/api/v1/podman/networks/empty-net", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
		}

		if _, ok := mockRuntime.Networks["empty-net"]; ok {
			t.Errorf("empty network was not removed!")
		}
	})
}
