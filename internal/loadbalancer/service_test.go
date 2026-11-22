package loadbalancer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"procloud.io/procloud/internal/database/sqlite"
	"procloud.io/procloud/pkg/podman"
	"procloud.io/procloud/pkg/protocol"
)

func TestLoadBalancerService_EndToEnd(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	dbPath := filepath.Join(tmpDir, "test_lb.db")
	db, err := sqlite.Open(sqlite.DefaultConfig(dbPath))
	if err != nil {
		t.Fatalf("failed opening sqlite db: %v", err)
	}
	defer db.Close()

	repo := sqlite.NewRepository(db)
	mockRuntime := podman.NewMockRuntime()
	confDir := filepath.Join(tmpDir, "nginx_lb")
	svc := NewService(repo, mockRuntime, confDir)

	// Create Tenant
	_ = repo.CreateTenant(ctx, &protocol.Tenant{
		ID:        "tnt-acme",
		Name:      "Acme Corp",
		Slug:      "acme",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})

	// 1. Create LoadBalancer with Public VIP
	lb, err := svc.CreateLoadBalancer(ctx, protocol.CreateLoadBalancerRequest{
		TenantID:    "tnt-acme",
		VPCID:       "vpc-prod",
		Name:        "acme-edge-lb",
		Description: "Production Edge Load Balancer",
		Type:        "hybrid",
		PublicIP:    "198.51.100.25",
	})
	if err != nil {
		t.Fatalf("CreateLoadBalancer failed: %v", err)
	}
	if lb.ID == "" || lb.PublicIP != "198.51.100.25" {
		t.Errorf("unexpected loadbalancer: %+v", lb)
	}

	// 2. Create L7 Backend Pool
	webPool, err := svc.CreatePool(ctx, lb.ID, protocol.CreatePoolRequest{
		Name:              "web-frontend-pool",
		Protocol:          "HTTP",
		Algorithm:         "least_conn",
		HealthPath:        "/healthz",
		HealthIntervalSec: 5,
	})
	if err != nil {
		t.Fatalf("CreatePool web failed: %v", err)
	}

	// 3. Create L4 Backend Pool (TCP - Database)
	dbPool, err := svc.CreatePool(ctx, lb.ID, protocol.CreatePoolRequest{
		Name:      "postgres-db-pool",
		Protocol:  "TCP",
		Algorithm: "round_robin",
	})
	if err != nil {
		t.Fatalf("CreatePool db failed: %v", err)
	}

	// 4. Attach Members to Pools
	mbr1, err := svc.AttachMember(ctx, lb.ID, webPool.ID, protocol.AttachMemberRequest{
		TargetType: "ip",
		IP:         "10.10.1.15",
		Port:       8080,
		Weight:     2,
	})
	if err != nil {
		t.Fatalf("AttachMember mbr1 failed: %v", err)
	}
	if mbr1.IP != "10.10.1.15" || mbr1.Port != 8080 {
		t.Errorf("unexpected member1: %+v", mbr1)
	}

	_, err = svc.AttachMember(ctx, lb.ID, webPool.ID, protocol.AttachMemberRequest{
		TargetType: "ip",
		IP:         "10.10.1.16",
		Port:       8080,
		Weight:     1,
	})
	if err != nil {
		t.Fatalf("AttachMember mbr2 failed: %v", err)
	}

	// Attach L4 TCP member
	_, err = svc.AttachMember(ctx, lb.ID, dbPool.ID, protocol.AttachMemberRequest{
		TargetType: "ip",
		IP:         "10.10.2.50",
		Port:       5432,
		Weight:     1,
	})
	if err != nil {
		t.Fatalf("AttachMember db mbr failed: %v", err)
	}

	// 5. Create L7 HTTPS Listener with SSL
	httpsListener, err := svc.CreateListener(ctx, lb.ID, protocol.CreateListenerRequest{
		Name:          "https-443",
		Port:          443,
		Protocol:      "HTTPS",
		SSLCertSecret: "acme-wildcard-cert",
		DefaultPoolID: webPool.ID,
	})
	if err != nil {
		t.Fatalf("CreateListener https failed: %v", err)
	}

	// 6. Create L7 HTTP Listener with 301 Redirect
	_, err = svc.CreateListener(ctx, lb.ID, protocol.CreateListenerRequest{
		Name:         "http-80",
		Port:         80,
		Protocol:     "HTTP",
		HTTPRedirect: true,
	})
	if err != nil {
		t.Fatalf("CreateListener http redirect failed: %v", err)
	}

	// 7. Create L4 TCP Listener (Port 5432)
	_, err = svc.CreateListener(ctx, lb.ID, protocol.CreateListenerRequest{
		Name:          "tcp-db",
		Port:          5432,
		Protocol:      "TCP",
		DefaultPoolID: dbPool.ID,
	})
	if err != nil {
		t.Fatalf("CreateListener tcp failed: %v", err)
	}

	// 8. Add L7 Routing Policy
	_, err = svc.CreatePolicy(ctx, lb.ID, httpsListener.ID, protocol.CreatePolicyRequest{
		Name:         "api-route",
		RuleType:     "PATH_PREFIX",
		RuleValue:    "/api/v1",
		TargetPoolID: webPool.ID,
		Priority:     1,
	})
	if err != nil {
		t.Fatalf("CreatePolicy failed: %v", err)
	}

	// 9. Verify Generated Configuration
	confContent, err := svc.SynthesizeConfig(ctx, lb.ID)
	if err != nil {
		t.Fatalf("SynthesizeConfig failed: %v", err)
	}

	confFile := filepath.Join(confDir, fmt.Sprintf("%s.conf", lb.ID))
	diskBytes, err := os.ReadFile(confFile)
	if err != nil {
		t.Fatalf("failed reading config file from disk: %v", err)
	}
	diskContent := string(diskBytes)

	expectedSnippets := []string{
		"listen 198.51.100.25:443 ssl http2;",
		"listen 198.51.100.25:80;",
		"return 301 https://$host$request_uri;",
		"listen 198.51.100.25:5432;",
		"proxy_pass http://upstream_l7_",
		"location ^~ /api/v1 {",
		"least_conn;",
		"server 10.10.1.15:8080 weight=2",
		"server 10.10.1.16:8080 weight=1",
		"server 10.10.2.50:5432 weight=1",
	}

	for _, snippet := range expectedSnippets {
		if !strings.Contains(diskContent, snippet) {
			t.Errorf("missing snippet %q in generated conf:\n%s", snippet, confContent)
		}
	}

	// 10. Test Detach and Delete
	if err := svc.DetachMember(ctx, lb.ID, mbr1.ID); err != nil {
		t.Fatalf("DetachMember failed: %v", err)
	}

	updatedConf, _ := svc.SynthesizeConfig(ctx, lb.ID)
	if strings.Contains(updatedConf, "10.10.1.15:8080") {
		t.Errorf("expected detached member to be removed from config")
	}

	if err := svc.DeleteLoadBalancer(ctx, lb.ID); err != nil {
		t.Fatalf("DeleteLoadBalancer failed: %v", err)
	}
	if _, err := os.Stat(confFile); !os.IsNotExist(err) {
		t.Errorf("expected config file to be removed from disk on delete")
	}
}

func TestLoadBalancer_SSLOptions(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := sqlite.Open(sqlite.DefaultConfig(dbPath))
	if err != nil {
		t.Fatalf("failed initializing db: %v", err)
	}
	defer db.Close()
	repo := sqlite.NewRepository(db)
	mockRuntime := podman.NewMockRuntime()

	confDir := filepath.Join(tmpDir, "nginx-conf")
	svc := NewService(repo, mockRuntime, confDir)

	_ = repo.CreateTenant(ctx, &protocol.Tenant{
		ID:        "tnt-test",
		Name:      "Test Tenant",
		Slug:      "test",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})

	lb, err := svc.CreateLoadBalancer(ctx, protocol.CreateLoadBalancerRequest{
		TenantID: "tnt-test",
		Name:     "ssl-test-lb",
		Type:     "layer7",
		PublicIP: "198.51.100.50",
	})
	if err != nil {
		t.Fatalf("CreateLoadBalancer failed: %v", err)
	}

	// 1. Test Option 1: Auto-generated self-signed SSL
	autoLsn, err := svc.CreateListener(ctx, lb.ID, protocol.CreateListenerRequest{
		Name:        "https-auto",
		Port:        8443,
		Protocol:    "HTTPS",
		SSLCertType: "auto",
	})
	if err != nil {
		t.Fatalf("CreateListener auto failed: %v", err)
	}
	if autoLsn.SSLCertType != "auto" {
		t.Errorf("expected SSLCertType to be auto, got %s", autoLsn.SSLCertType)
	}

	// 2. Test Option 2: Secret Reference
	secLsn, err := svc.CreateListener(ctx, lb.ID, protocol.CreateListenerRequest{
		Name:          "https-secret",
		Port:          9443,
		Protocol:      "HTTPS",
		SSLCertType:   "secret",
		SSLCertSecret: "my-vault-tls",
	})
	if err != nil {
		t.Fatalf("CreateListener secret failed: %v", err)
	}
	if secLsn.SSLCertType != "secret" || secLsn.SSLCertSecret != "my-vault-tls" {
		t.Errorf("expected SSLCertType=secret, SSLCertSecret=my-vault-tls, got %s, %s", secLsn.SSLCertType, secLsn.SSLCertSecret)
	}

	// 3. Test Option 3: Custom Certificate Upload
	sampleCert := "-----BEGIN CERTIFICATE-----\nMIIBojCCAUqgAwIBAgIU\n-----END CERTIFICATE-----"
	sampleKey := "-----BEGIN EC PRIVATE KEY-----\nMHcCAQEEI\n-----END EC PRIVATE KEY-----"
	customLsn, err := svc.CreateListener(ctx, lb.ID, protocol.CreateListenerRequest{
		Name:        "https-custom",
		Port:        10443,
		Protocol:    "HTTPS",
		SSLCertType: "custom",
		SSLCertData: sampleCert,
		SSLKeyData:  sampleKey,
	})
	if err != nil {
		t.Fatalf("CreateListener custom failed: %v", err)
	}
	if customLsn.SSLCertType != "custom" {
		t.Errorf("expected SSLCertType=custom, got %s", customLsn.SSLCertType)
	}

	// Verify custom cert files written to disk
	customCertPath := filepath.Join(confDir, "certs", fmt.Sprintf("custom-%s.crt", customLsn.ID))
	customKeyPath := filepath.Join(confDir, "certs", fmt.Sprintf("custom-%s.key", customLsn.ID))
	if !fileExists(customCertPath) || !fileExists(customKeyPath) {
		t.Errorf("custom cert/key files were not created on disk")
	}

	// Verify SynthesizeConfig includes custom cert path
	conf, err := svc.SynthesizeConfig(ctx, lb.ID)
	if err != nil {
		t.Fatalf("SynthesizeConfig failed: %v", err)
	}
	if !strings.Contains(conf, customCertPath) {
		t.Errorf("expected synthesized config to contain custom cert path %s", customCertPath)
	}
	if !strings.Contains(conf, "my-vault-tls") {
		t.Errorf("expected synthesized config to reference secret my-vault-tls")
	}
	if !strings.Contains(conf, "procloud-default.crt") {
		t.Errorf("expected synthesized config to reference default auto cert")
	}

	// Delete custom listener and verify cleanup
	if err := svc.DeleteListener(ctx, lb.ID, customLsn.ID); err != nil {
		t.Fatalf("DeleteListener failed: %v", err)
	}
	if fileExists(customCertPath) || fileExists(customKeyPath) {
		t.Errorf("expected custom cert files to be deleted after DeleteListener")
	}
}
