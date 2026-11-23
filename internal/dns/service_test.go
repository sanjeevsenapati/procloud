package dns

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"procloud.io/procloud/internal/database/sqlite"
	"procloud.io/procloud/pkg/protocol"
)

func TestDNSService_EndToEnd(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	dbPath := filepath.Join(tmpDir, "test_dns.db")
	db, err := sqlite.Open(sqlite.DefaultConfig(dbPath))
	if err != nil {
		t.Fatalf("failed opening sqlite db: %v", err)
	}
	defer db.Close()

	repo := sqlite.NewRepository(db)
	svc := NewService(repo)

	// Setup Tenant
	_ = repo.CreateTenant(ctx, &protocol.Tenant{
		ID:        "tnt-corp",
		Name:      "Corp Tenant",
		Slug:      "corp",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})

	// 1. Create Hosted Zone
	zone, err := svc.CreateZone(ctx, protocol.CreateDNSZoneRequest{
		TenantID:    "tnt-corp",
		Name:        "corp.internal",
		Type:        "private",
		VPCID:       "vpc-100",
		Description: "Internal VPC Domain",
	})
	if err != nil {
		t.Fatalf("failed creating zone: %v", err)
	}

	if zone.ID == "" || zone.Name != "corp.internal" {
		t.Fatalf("unexpected zone: %+v", zone)
	}

	// Verify SOA and NS were automatically provisioned
	recs, err := svc.ListRecords(ctx, zone.ID)
	if err != nil {
		t.Fatalf("failed listing records: %v", err)
	}
	if len(recs) < 2 {
		t.Fatalf("expected at least 2 default records (SOA, NS), got %d", len(recs))
	}

	var hasSOA, hasNS bool
	for _, r := range recs {
		if r.Type == "SOA" {
			hasSOA = true
		}
		if r.Type == "NS" {
			hasNS = true
		}
	}
	if !hasSOA || !hasNS {
		t.Fatalf("expected zone to have both SOA and NS records")
	}

	// 2. Prevent Duplicate Zone
	_, err = svc.CreateZone(ctx, protocol.CreateDNSZoneRequest{
		TenantID: "tnt-corp",
		Name:     "corp.internal",
	})
	if err == nil {
		t.Fatalf("expected error creating duplicate zone, got nil")
	}

	// 3. Create A Record
	recA, err := svc.CreateRecord(ctx, zone.ID, protocol.CreateDNSRecordRequest{
		Name:  "api.corp.internal",
		Type:  "A",
		Value: "10.89.0.25",
		TTL:   300,
	})
	if err != nil {
		t.Fatalf("failed creating A record: %v", err)
	}
	if recA.Name != "api" || recA.Value != "10.89.0.25" {
		t.Fatalf("unexpected A record: %+v", recA)
	}

	// 4. Create CNAME Record
	recCNAME, err := svc.CreateRecord(ctx, zone.ID, protocol.CreateDNSRecordRequest{
		Name:  "gateway.corp.internal",
		Type:  "CNAME",
		Value: "api.corp.internal",
		TTL:   300,
	})
	if err != nil {
		t.Fatalf("failed creating CNAME record: %v", err)
	}
	if recCNAME.Value != "api.corp.internal" {
		t.Fatalf("unexpected CNAME target: %s", recCNAME.Value)
	}

	// 5. Create TXT Record
	recTXT, err := svc.CreateRecord(ctx, zone.ID, protocol.CreateDNSRecordRequest{
		Name:  "_dmarc.corp.internal",
		Type:  "TXT",
		Value: "v=DMARC1; p=none",
		TTL:   600,
	})
	if err != nil {
		t.Fatalf("failed creating TXT record: %v", err)
	}
	if recTXT.Value != "v=DMARC1; p=none" {
		t.Fatalf("unexpected TXT value: %s", recTXT.Value)
	}

	// 6. Test Query Direct A Record
	qResA, err := svc.Query(ctx, protocol.DNSQueryRequest{
		Host: "api.corp.internal",
		Type: "A",
	})
	if err != nil {
		t.Fatalf("failed query A: %v", err)
	}
	if qResA.Status != "NOERROR" || len(qResA.Answers) == 0 {
		t.Fatalf("expected success with answers, got %+v", qResA)
	}
	if qResA.Answers[0].Value != "10.89.0.25" {
		t.Fatalf("expected 10.89.0.25, got %s", qResA.Answers[0].Value)
	}

	// 7. Test Query CNAME with Recursive Resolution
	qResCNAME, err := svc.Query(ctx, protocol.DNSQueryRequest{
		Host: "gateway.corp.internal",
		Type: "A",
	})
	if err != nil {
		t.Fatalf("failed query CNAME: %v", err)
	}
	if len(qResCNAME.Answers) < 2 {
		t.Fatalf("expected at least 2 answers (CNAME + recursive A), got %d: %+v", len(qResCNAME.Answers), qResCNAME)
	}

	// 8. Test Query Non-Existent Name
	qResNX, err := svc.Query(ctx, protocol.DNSQueryRequest{
		Host: "missing.corp.internal",
		Type: "A",
	})
	if err != nil {
		t.Fatalf("unexpected error querying missing name: %v", err)
	}
	if qResNX.Status != "NXDOMAIN" {
		t.Fatalf("expected status NXDOMAIN, got %s", qResNX.Status)
	}

	// 9. Test BIND Zone File Export
	bindFile, err := svc.ExportBINDZone(ctx, zone.ID)
	if err != nil {
		t.Fatalf("failed exporting BIND zone: %v", err)
	}
	if !strings.Contains(bindFile, "$ORIGIN corp.internal.") {
		t.Fatalf("expected $ORIGIN corp.internal. in BIND export, got:\n%s", bindFile)
	}
	if !strings.Contains(bindFile, "api") || !strings.Contains(bindFile, "10.89.0.25") {
		t.Fatalf("expected A record line in BIND export, got:\n%s", bindFile)
	}
	if !strings.Contains(bindFile, "gateway") || !strings.Contains(bindFile, "CNAME") {
		t.Fatalf("expected CNAME record line in BIND export, got:\n%s", bindFile)
	}

	// 10. Update Record
	updatedA, err := svc.UpdateRecord(ctx, zone.ID, recA.ID, protocol.CreateDNSRecordRequest{
		Name:  "api",
		Type:  "A",
		Value: "10.89.0.99",
		TTL:   60,
	})
	if err != nil {
		t.Fatalf("failed updating record: %v", err)
	}
	if updatedA.Value != "10.89.0.99" || updatedA.TTL != 60 {
		t.Fatalf("unexpected updated record: %+v", updatedA)
	}

	// 11. Delete Record
	if err := svc.DeleteRecord(ctx, zone.ID, recTXT.ID); err != nil {
		t.Fatalf("failed deleting record: %v", err)
	}
	deletedRec, _ := svc.GetRecord(ctx, recTXT.ID)
	if deletedRec != nil {
		t.Fatalf("expected record to be deleted")
	}

	// 12. Delete Zone and Cascade Delete
	if err := svc.DeleteZone(ctx, zone.ID); err != nil {
		t.Fatalf("failed deleting zone: %v", err)
	}
	deletedZone, _ := svc.GetZone(ctx, zone.ID)
	if deletedZone != nil {
		t.Fatalf("expected zone to be deleted")
	}
	remainingRecs, _ := svc.ListRecords(ctx, zone.ID)
	if len(remainingRecs) != 0 {
		t.Fatalf("expected all records to be cascade deleted, got %d", len(remainingRecs))
	}
}

func TestDNSService_ALBCNAMEIntegration(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	dbPath := filepath.Join(tmpDir, "test_dns_alb.db")
	db, err := sqlite.Open(sqlite.DefaultConfig(dbPath))
	if err != nil {
		t.Fatalf("failed opening sqlite db: %v", err)
	}
	defer db.Close()

	repo := sqlite.NewRepository(db)
	svc := NewService(repo)

	// Setup Tenant
	_ = repo.CreateTenant(ctx, &protocol.Tenant{
		ID:        "tnt-default",
		Name:      "Default Organization",
		Slug:      "default-org",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})

	// Ensure CNAME for Application Load Balancer
	cnameRec, err := svc.EnsureALBCNAME(ctx, "tnt-default", "store-front", "layer7", "198.51.100.55")
	if err != nil {
		t.Fatalf("failed ensuring ALB CNAME: %v", err)
	}
	if cnameRec == nil || cnameRec.Type != "CNAME" || cnameRec.Name != "store-front" {
		t.Fatalf("unexpected CNAME record: %+v", cnameRec)
	}
	if !strings.Contains(cnameRec.Value, "alb-store-front.procloud.net") {
		t.Fatalf("expected CNAME value to target alb-store-front.procloud.net, got: %s", cnameRec.Value)
	}

	// Verify procloud.net zone exists
	zone, err := svc.GetZoneByName(ctx, "tnt-default", "procloud.net")
	if err != nil || zone == nil {
		t.Fatalf("expected procloud.net zone to be created, got error: %v", err)
	}

	// Query store-front.procloud.net (Type A) - should follow CNAME recursively to 198.51.100.55
	queryResA, err := svc.Query(ctx, protocol.DNSQueryRequest{
		Host: "store-front.procloud.net",
		Type: "A",
	})
	if err != nil {
		t.Fatalf("failed querying store-front.procloud.net: %v", err)
	}
	if queryResA.Status != "NOERROR" || queryResA.Resolved != "198.51.100.55" {
		t.Fatalf("expected NOERROR with resolved IP 198.51.100.55, got status=%s resolved=%s", queryResA.Status, queryResA.Resolved)
	}
	if len(queryResA.Answers) < 2 {
		t.Fatalf("expected at least 2 answers (CNAME + recursive A), got %d: %+v", len(queryResA.Answers), queryResA.Answers)
	}

	// Query store-front.procloud.net (Type CNAME)
	queryResCNAME, err := svc.Query(ctx, protocol.DNSQueryRequest{
		Host: "store-front.procloud.net",
		Type: "CNAME",
	})
	if err != nil {
		t.Fatalf("failed querying CNAME: %v", err)
	}
	if len(queryResCNAME.Answers) == 0 || queryResCNAME.Answers[0].Type != "CNAME" {
		t.Fatalf("expected CNAME answer, got: %+v", queryResCNAME.Answers)
	}

	// Test SyncAllALBCNAMEs with multiple load balancers
	lbs := []*protocol.LoadBalancer{
		{Name: "api-gateway", Type: "layer7", PublicIP: "198.51.100.60"},
		{Name: "web-hybrid", Type: "hybrid", PublicIP: "198.51.100.70"},
		{Name: "raw-tcp", Type: "layer4", PublicIP: "198.51.100.80"}, // L4 NLB, not ALB
	}
	if err := svc.SyncAllALBCNAMEs(ctx, lbs); err != nil {
		t.Fatalf("failed syncing ALB CNAMEs: %v", err)
	}

	// Verify api-gateway and web-hybrid have CNAMEs, raw-tcp does not
	qAPI, _ := svc.Query(ctx, protocol.DNSQueryRequest{Host: "api-gateway.procloud.net", Type: "A"})
	if qAPI == nil || qAPI.Resolved != "198.51.100.60" {
		t.Fatalf("expected api-gateway to resolve to 198.51.100.60, got: %+v", qAPI)
	}

	qHybrid, _ := svc.Query(ctx, protocol.DNSQueryRequest{Host: "web-hybrid.procloud.net", Type: "A"})
	if qHybrid == nil || qHybrid.Resolved != "198.51.100.70" {
		t.Fatalf("expected web-hybrid to resolve to 198.51.100.70, got: %+v", qHybrid)
	}

	qTCP, _ := svc.Query(ctx, protocol.DNSQueryRequest{Host: "raw-tcp.procloud.net", Type: "A"})
	if qTCP != nil && qTCP.Status == "NOERROR" {
		t.Fatalf("expected raw-tcp (layer4) to not have CNAME in procloud.net")
	}

	// Test RemoveALBCNAME
	if err := svc.RemoveALBCNAME(ctx, "tnt-default", "store-front"); err != nil {
		t.Fatalf("failed removing ALB CNAME: %v", err)
	}
	qDeleted, _ := svc.Query(ctx, protocol.DNSQueryRequest{Host: "store-front.procloud.net", Type: "A"})
	if qDeleted != nil && qDeleted.Status == "NOERROR" {
		t.Fatalf("expected store-front to be removed from DNS, but it still resolved")
	}
}
