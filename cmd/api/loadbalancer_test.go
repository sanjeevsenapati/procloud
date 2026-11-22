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

func TestLoadBalancerAPIEndpoints(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_api_lb.db")

	db, err := sqlite.Open(sqlite.DefaultConfig(dbPath))
	if err != nil {
		t.Fatalf("failed opening sqlite db: %v", err)
	}
	defer db.Close()

	repo := sqlite.NewRepository(db)
	mockRuntime := podman.NewMockRuntime()
	server := NewServer(db, repo, mockRuntime)

	// Pre-create admin user and tenant
	ctx := t.Context()
	_ = repo.CreateTenant(ctx, &protocol.Tenant{
		ID:        "tnt-default",
		Name:      "Default Organization",
		Slug:      "default",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})

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

	// 1. Create Load Balancer
	createLBReq := protocol.CreateLoadBalancerRequest{
		TenantID:    "tnt-default",
		Name:        "api-gateway-lb",
		Description: "Main Edge Gateway",
		Type:        "hybrid",
		PublicIP:    "198.51.100.50",
	}
	body, _ := json.Marshal(createLBReq)

	req := httptest.NewRequest("POST", "/api/v1/loadbalancers", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/loadbalancers expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var createdLB protocol.LoadBalancer
	if err := json.Unmarshal(w.Body.Bytes(), &createdLB); err != nil {
		t.Fatalf("failed unmarshaling created LB: %v", err)
	}
	if createdLB.ID == "" || createdLB.Name != "api-gateway-lb" {
		t.Errorf("unexpected created LB: %+v", createdLB)
	}

	// 2. List Load Balancers
	listReq := httptest.NewRequest("GET", "/api/v1/loadbalancers", nil)
	listReq.Header.Set("Authorization", "Bearer "+token)
	wList := httptest.NewRecorder()
	server.mux.ServeHTTP(wList, listReq)

	if wList.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/loadbalancers expected 200, got %d: %s", wList.Code, wList.Body.String())
	}
	var lbList []*protocol.LoadBalancer
	_ = json.Unmarshal(wList.Body.Bytes(), &lbList)
	if len(lbList) != 1 {
		t.Fatalf("expected 1 loadbalancer, got %d", len(lbList))
	}

	// 3. Create Backend Pool
	createPoolReq := protocol.CreatePoolRequest{
		Name:      "web-pool",
		Protocol:  "HTTP",
		Algorithm: "round_robin",
	}
	poolBody, _ := json.Marshal(createPoolReq)
	pReq := httptest.NewRequest("POST", "/api/v1/loadbalancers/"+createdLB.ID+"/pools", bytes.NewReader(poolBody))
	pReq.Header.Set("Authorization", "Bearer "+token)
	pReq.Header.Set("Content-Type", "application/json")
	wPool := httptest.NewRecorder()
	server.mux.ServeHTTP(wPool, pReq)

	if wPool.Code != http.StatusCreated {
		t.Fatalf("POST pool expected 201, got %d: %s", wPool.Code, wPool.Body.String())
	}
	var pool protocol.LBPool
	_ = json.Unmarshal(wPool.Body.Bytes(), &pool)

	// 4. Attach Member
	attachReq := protocol.AttachMemberRequest{
		TargetType: "ip",
		IP:         "10.89.0.22",
		Port:       80,
		Weight:     1,
	}
	mBody, _ := json.Marshal(attachReq)
	mReq := httptest.NewRequest("POST", "/api/v1/loadbalancers/"+createdLB.ID+"/pools/"+pool.ID+"/members", bytes.NewReader(mBody))
	mReq.Header.Set("Authorization", "Bearer "+token)
	mReq.Header.Set("Content-Type", "application/json")
	wMember := httptest.NewRecorder()
	server.mux.ServeHTTP(wMember, mReq)

	if wMember.Code != http.StatusCreated {
		t.Fatalf("POST member expected 201, got %d: %s", wMember.Code, wMember.Body.String())
	}

	// 5. Create Listener
	createLsnReq := protocol.CreateListenerRequest{
		Name:          "http-listener",
		Port:          80,
		Protocol:      "HTTP",
		DefaultPoolID: pool.ID,
	}
	lBody, _ := json.Marshal(createLsnReq)
	lReq := httptest.NewRequest("POST", "/api/v1/loadbalancers/"+createdLB.ID+"/listeners", bytes.NewReader(lBody))
	lReq.Header.Set("Authorization", "Bearer "+token)
	lReq.Header.Set("Content-Type", "application/json")
	wLsn := httptest.NewRecorder()
	server.mux.ServeHTTP(wLsn, lReq)

	if wLsn.Code != http.StatusCreated {
		t.Fatalf("POST listener expected 201, got %d: %s", wLsn.Code, wLsn.Body.String())
	}

	// 6. Inspect Load Balancer
	getReq := httptest.NewRequest("GET", "/api/v1/loadbalancers/"+createdLB.ID, nil)
	getReq.Header.Set("Authorization", "Bearer "+token)
	wGet := httptest.NewRecorder()
	server.mux.ServeHTTP(wGet, getReq)

	if wGet.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/loadbalancers/{id} expected 200, got %d: %s", wGet.Code, wGet.Body.String())
	}
	var inspected protocol.LoadBalancer
	_ = json.Unmarshal(wGet.Body.Bytes(), &inspected)
	if len(inspected.Listeners) != 1 || len(inspected.Pools) != 1 {
		t.Errorf("expected 1 listener and 1 pool in inspected LB, got %d and %d", len(inspected.Listeners), len(inspected.Pools))
	}
	if len(inspected.Pools[0].Members) != 1 {
		t.Errorf("expected 1 member in pool, got %d", len(inspected.Pools[0].Members))
	}

	// 7. Get Config
	cfgReq := httptest.NewRequest("GET", "/api/v1/loadbalancers/"+createdLB.ID+"/config", nil)
	cfgReq.Header.Set("Authorization", "Bearer "+token)
	wCfg := httptest.NewRecorder()
	server.mux.ServeHTTP(wCfg, cfgReq)

	if wCfg.Code != http.StatusOK {
		t.Fatalf("GET config expected 200, got %d: %s", wCfg.Code, wCfg.Body.String())
	}

	// 8. Delete Load Balancer
	delReq := httptest.NewRequest("DELETE", "/api/v1/loadbalancers/"+createdLB.ID, nil)
	delReq.Header.Set("Authorization", "Bearer "+token)
	wDel := httptest.NewRecorder()
	server.mux.ServeHTTP(wDel, delReq)

	if wDel.Code != http.StatusOK {
		t.Fatalf("DELETE expected 200, got %d: %s", wDel.Code, wDel.Body.String())
	}
}
