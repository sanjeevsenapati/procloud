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

func TestBulkDeleteImages(t *testing.T) {
	dbPath := t.TempDir() + "/test_image_bulk.db"
	db, err := sqlite.Open(sqlite.DefaultConfig(dbPath))
	if err != nil {
		t.Fatalf("failed opening test db: %v", err)
	}
	defer db.Close()

	repo := sqlite.NewRepository(db)
	mockRuntime := podman.NewMockRuntime()

	// Seed mock images
	mockRuntime.Images["img1"] = &podman.ImageSummary{ID: "img1", Repository: "localhost/test-1", Tag: "v1"}
	mockRuntime.Images["img2"] = &podman.ImageSummary{ID: "img2", Repository: "localhost/test-2", Tag: "v2"}
	mockRuntime.Images["img3"] = &podman.ImageSummary{ID: "img3", Repository: "<none>", Tag: "<none>"}

	server := NewServer(db, repo, mockRuntime)

	// Obtain auth token for admin
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

	// 1. Unauthenticated bulk delete -> 401
	unauthReq := httptest.NewRequest("POST", "/api/v1/images/bulk-delete", bytes.NewReader([]byte(`{"ids":["img1"]}`)))
	unauthRec := httptest.NewRecorder()
	server.ServeHTTP(unauthRec, unauthReq)
	if unauthRec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for unauthenticated bulk delete, got %d", unauthRec.Code)
	}

	// 2. Authenticated bulk delete
	delBody, _ := json.Marshal(BulkDeleteImagesRequest{
		IDs:   []string{"img1", "img3"},
		Force: true,
	})
	authReq := httptest.NewRequest("POST", "/api/v1/images/bulk-delete", bytes.NewReader(delBody))
	authReq.Header.Set("Authorization", "Bearer "+token)
	authReq.Header.Set("Content-Type", "application/json")
	authRec := httptest.NewRecorder()
	server.ServeHTTP(authRec, authReq)

	if authRec.Code != http.StatusOK {
		t.Fatalf("expected 200 for bulk delete, got %d: %s", authRec.Code, authRec.Body.String())
	}

	var resp BulkDeleteImagesResponse
	if err := json.Unmarshal(authRec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(resp.Deleted) != 2 {
		t.Errorf("expected 2 deleted images, got %d", len(resp.Deleted))
	}
	if len(resp.Failed) != 0 {
		t.Errorf("expected 0 failed images, got %d", len(resp.Failed))
	}

	// Verify images list from mock runtime
	if _, ok := mockRuntime.Images["img1"]; ok {
		t.Errorf("expected img1 to be removed from mock runtime")
	}
	if _, ok := mockRuntime.Images["img3"]; ok {
		t.Errorf("expected img3 to be removed from mock runtime")
	}
	if _, ok := mockRuntime.Images["img2"]; !ok {
		t.Errorf("expected img2 to still exist in mock runtime")
	}
}
