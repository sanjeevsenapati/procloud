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

func TestImageAndVolumeDetailsEndpoints(t *testing.T) {
	dbPath := t.TempDir() + "/test_details.db"
	db, err := sqlite.Open(sqlite.DefaultConfig(dbPath))
	if err != nil {
		t.Fatalf("failed opening test db: %v", err)
	}
	defer db.Close()

	repo := sqlite.NewRepository(db)
	mockRuntime := podman.NewMockRuntime()

	// Seed mock image
	mockRuntime.Images["test-img-123"] = &podman.ImageSummary{
		ID:         "test-img-123",
		Repository: "docker.io/library/nginx",
		Tag:        "alpine",
		Size:       "23.5 MB",
		Created:    "2026-11-20T10:00:00Z",
	}

	// Seed mock volume
	mockRuntime.Volumes["my-test-vol"] = &podman.VolumeSummary{
		Name:       "my-test-vol",
		Driver:     "local",
		Scope:      "local",
		Mountpoint: "/var/lib/volumes/my-test-vol",
		Labels:     map[string]string{"env": "staging"},
	}

	server := NewServer(db, repo, mockRuntime)

	// Login to get token
	loginBody, _ := json.Marshal(map[string]string{
		"username": "admin",
		"password": "admin123",
	})
	loginReq := httptest.NewRequest("POST", "/api/v1/auth/login", bytes.NewReader(loginBody))
	loginReq.Header.Set("Content-Type", "application/json")
	loginRec := httptest.NewRecorder()
	server.ServeHTTP(loginRec, loginReq)

	if loginRec.Code != http.StatusOK {
		t.Fatalf("failed to login: %s", loginRec.Body.String())
	}
	var loginResp struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(loginRec.Body.Bytes(), &loginResp)
	token := loginResp.Token

	// 1. Test GET /api/v1/images/test-img-123
	t.Run("Inspect Image by ID", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/v1/images/test-img-123", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}

		var img podman.ImageInspect
		if err := json.Unmarshal(rec.Body.Bytes(), &img); err != nil {
			t.Fatalf("failed to parse image inspect response: %v", err)
		}
		if img.ID != "test-img-123" {
			t.Errorf("expected ID 'test-img-123', got %s", img.ID)
		}
		if len(img.RepoTags) == 0 || img.RepoTags[0] != "docker.io/library/nginx:alpine" {
			t.Errorf("unexpected repo tags: %v", img.RepoTags)
		}
	})

	// 2. Test GET /api/v1/podman/volumes/my-test-vol
	t.Run("Inspect Volume by Name", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/v1/podman/volumes/my-test-vol", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}

		var vol podman.VolumeInspect
		if err := json.Unmarshal(rec.Body.Bytes(), &vol); err != nil {
			t.Fatalf("failed to parse volume inspect response: %v", err)
		}
		if vol.Name != "my-test-vol" {
			t.Errorf("expected name 'my-test-vol', got %s", vol.Name)
		}
		if vol.Driver != "local" {
			t.Errorf("expected driver 'local', got %s", vol.Driver)
		}
		if vol.Labels["env"] != "staging" {
			t.Errorf("expected label env=staging, got %v", vol.Labels)
		}
	})
}
