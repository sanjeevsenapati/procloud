package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"procloud.io/procloud/internal/database/sqlite"
	"procloud.io/procloud/pkg/podman"
	"procloud.io/procloud/pkg/protocol"
)

func TestDNSAPIEndpoints(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_api_dns.db")

	db, err := sqlite.Open(sqlite.DefaultConfig(dbPath))
	if err != nil {
		t.Fatalf("failed opening sqlite db: %v", err)
	}
	defer db.Close()

	repo := sqlite.NewRepository(db)
	mockRuntime := podman.NewMockRuntime()
	server := NewServer(db, repo, mockRuntime)

	// Pre-create default tenant
	ctx := t.Context()
	_ = repo.CreateTenant(ctx, &protocol.Tenant{
		ID:        "tnt-default",
		Name:      "Default Organization",
		Slug:      "default",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})

	// Login as admin
	loginBody, _ := json.Marshal(map[string]string{
		"username": "admin",
		"password": "admin123",
	})
	loginReq := httptest.NewRequest("POST", "/api/v1/auth/login", bytes.NewReader(loginBody))
	loginReq.Header.Set("Content-Type", "application/json")
	loginRec := httptest.NewRecorder()
	server.ServeHTTP(loginRec, loginReq)

	if loginRec.Code != http.StatusOK {
		t.Fatalf("login failed: %s", loginRec.Body.String())
	}
	var loginResp map[string]any
	_ = json.Unmarshal(loginRec.Body.Bytes(), &loginResp)
	token := loginResp["token"].(string)

	authHeaders := func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
	}

	// 1. Create DNS Zone (POST /api/v1/dns/zones)
	zonePayload, _ := json.Marshal(protocol.CreateDNSZoneRequest{
		Name:        "corp.internal",
		Type:        "private",
		VPCID:       "vpc-test",
		Description: "Internal VPC DNS Zone",
	})
	req := httptest.NewRequest("POST", "/api/v1/dns/zones", bytes.NewReader(zonePayload))
	authHeaders(req)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d: %s", rec.Code, rec.Body.String())
	}
	var createdZone protocol.DNSZone
	_ = json.Unmarshal(rec.Body.Bytes(), &createdZone)
	if createdZone.ID == "" || createdZone.Name != "corp.internal" {
		t.Fatalf("unexpected created zone: %+v", createdZone)
	}

	// 2. List DNS Zones (GET /api/v1/dns/zones)
	req = httptest.NewRequest("GET", "/api/v1/dns/zones", nil)
	authHeaders(req)
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}
	var zones []*protocol.DNSZone
	_ = json.Unmarshal(rec.Body.Bytes(), &zones)
	if len(zones) == 0 {
		t.Fatalf("expected at least 1 zone, got 0")
	}

	// 3. Create A Record (POST /api/v1/dns/zones/{id}/records)
	recPayload, _ := json.Marshal(protocol.CreateDNSRecordRequest{
		Name:  "web",
		Type:  "A",
		Value: "198.51.100.25",
		TTL:   300,
	})
	req = httptest.NewRequest("POST", "/api/v1/dns/zones/"+createdZone.ID+"/records", bytes.NewReader(recPayload))
	authHeaders(req)
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created for record, got %d: %s", rec.Code, rec.Body.String())
	}
	var createdRec protocol.DNSRecord
	_ = json.Unmarshal(rec.Body.Bytes(), &createdRec)
	if createdRec.ID == "" || createdRec.Value != "198.51.100.25" {
		t.Fatalf("unexpected record: %+v", createdRec)
	}

	// 4. Create CNAME Record (POST /api/v1/dns/zones/{id}/records)
	cnamePayload, _ := json.Marshal(protocol.CreateDNSRecordRequest{
		Name:  "app",
		Type:  "CNAME",
		Value: "web.corp.internal",
		TTL:   300,
	})
	req = httptest.NewRequest("POST", "/api/v1/dns/zones/"+createdZone.ID+"/records", bytes.NewReader(cnamePayload))
	authHeaders(req)
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created for CNAME record, got %d: %s", rec.Code, rec.Body.String())
	}

	// 5. Query DNS API (POST /api/v1/dns/query)
	queryPayload, _ := json.Marshal(protocol.DNSQueryRequest{
		Host: "app.corp.internal",
		Type: "A",
	})
	req = httptest.NewRequest("POST", "/api/v1/dns/query", bytes.NewReader(queryPayload))
	authHeaders(req)
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for query, got %d: %s", rec.Code, rec.Body.String())
	}
	var qResp protocol.DNSQueryResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &qResp)
	if qResp.Status != "NOERROR" || qResp.Resolved != "198.51.100.25" {
		t.Fatalf("unexpected query response: %+v", qResp)
	}

	// 6. Export BIND Zone (GET /api/v1/dns/zones/{id}/export)
	req = httptest.NewRequest("GET", "/api/v1/dns/zones/"+createdZone.ID+"/export", nil)
	authHeaders(req)
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for export, got %d: %s", rec.Code, rec.Body.String())
	}
	var exportResp map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &exportResp)
	if exportResp["bind_zone"] == "" {
		t.Fatalf("expected non-empty bind_zone export")
	}

	// 7. Delete Record (DELETE /api/v1/dns/zones/{id}/records/{rid})
	req = httptest.NewRequest("DELETE", "/api/v1/dns/zones/"+createdZone.ID+"/records/"+createdRec.ID, nil)
	authHeaders(req)
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK deleting record, got %d: %s", rec.Code, rec.Body.String())
	}

	// 8. Delete Zone (DELETE /api/v1/dns/zones/{id})
	req = httptest.NewRequest("DELETE", "/api/v1/dns/zones/"+createdZone.ID, nil)
	authHeaders(req)
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK deleting zone, got %d: %s", rec.Code, rec.Body.String())
	}
}
