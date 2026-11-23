package dns

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"procloud.io/procloud/internal/repository"
	"procloud.io/procloud/pkg/protocol"
)

var dnsSeq uint64

func genDNSID(prefix string) string {
	seq := atomic.AddUint64(&dnsSeq, 1)
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano(), seq)
}

type Service struct {
	repo    repository.DNSRepository
	udpConn *net.UDPConn
	stopCh  chan struct{}
	mu      sync.Mutex
}

func NewService(repo repository.DNSRepository) *Service {
	return &Service{
		repo:   repo,
		stopCh: make(chan struct{}),
	}
}

// -------------------------------------------------------------
// Zone Operations
// -------------------------------------------------------------

func (s *Service) CreateZone(ctx context.Context, req protocol.CreateDNSZoneRequest) (*protocol.DNSZone, error) {
	name := strings.TrimSpace(strings.ToLower(req.Name))
	name = strings.TrimSuffix(name, ".")
	if name == "" {
		return nil, fmt.Errorf("zone name is required")
	}

	zType := strings.ToLower(strings.TrimSpace(req.Type))
	if zType == "" {
		zType = "private"
	}
	if zType != "private" && zType != "public" {
		return nil, fmt.Errorf("invalid zone type %s, must be 'private' or 'public'", zType)
	}

	tenantID := req.TenantID
	if tenantID == "" {
		tenantID = "tnt-default"
	}

	// Check for duplicates
	if existing, err := s.repo.GetZoneByName(ctx, tenantID, name); err == nil && existing != nil {
		return nil, fmt.Errorf("zone %s already exists for tenant", name)
	}

	zone := &protocol.DNSZone{
		ID:          genDNSID("zone"),
		TenantID:    tenantID,
		VPCID:       req.VPCID,
		Name:        name,
		Type:        zType,
		Description: req.Description,
		Status:      "active",
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	if err := s.repo.CreateZone(ctx, zone); err != nil {
		return nil, err
	}

	// Auto-provision initial SOA and NS records
	soaRecord := &protocol.DNSRecord{
		ID:            genDNSID("rec"),
		ZoneID:        zone.ID,
		Name:          "@",
		Type:          "SOA",
		Value:         fmt.Sprintf("ns1.procloud.internal. hostmaster.%s. 2026112201 7200 3600 1209600 300", name),
		TTL:           300,
		RoutingPolicy: "simple",
		Status:        "active",
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	_ = s.repo.CreateRecord(ctx, soaRecord)

	nsRecord := &protocol.DNSRecord{
		ID:            genDNSID("rec"),
		ZoneID:        zone.ID,
		Name:          "@",
		Type:          "NS",
		Value:         "ns1.procloud.internal.",
		TTL:           300,
		RoutingPolicy: "simple",
		Status:        "active",
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	_ = s.repo.CreateRecord(ctx, nsRecord)

	zone.RecordCount = 2
	zone.Records = []*protocol.DNSRecord{soaRecord, nsRecord}
	return zone, nil
}

func (s *Service) GetZone(ctx context.Context, id string) (*protocol.DNSZone, error) {
	return s.repo.GetZoneByID(ctx, id)
}

func (s *Service) GetZoneByName(ctx context.Context, tenantID, name string) (*protocol.DNSZone, error) {
	name = strings.TrimSpace(strings.ToLower(name))
	name = strings.TrimSuffix(name, ".")
	return s.repo.GetZoneByName(ctx, tenantID, name)
}

func (s *Service) ListZones(ctx context.Context, tenantID string) ([]*protocol.DNSZone, error) {
	return s.repo.ListZones(ctx, tenantID)
}

func (s *Service) UpdateZone(ctx context.Context, id string, req protocol.CreateDNSZoneRequest) (*protocol.DNSZone, error) {
	zone, err := s.repo.GetZoneByID(ctx, id)
	if err != nil {
		return nil, err
	}

	if req.Description != "" {
		zone.Description = req.Description
	}
	if req.VPCID != "" {
		zone.VPCID = req.VPCID
	}
	if req.Type != "" {
		zone.Type = req.Type
	}

	if err := s.repo.UpdateZone(ctx, zone); err != nil {
		return nil, err
	}
	return zone, nil
}

func (s *Service) DeleteZone(ctx context.Context, id string) error {
	return s.repo.DeleteZone(ctx, id)
}

// -------------------------------------------------------------
// Record Operations
// -------------------------------------------------------------

func (s *Service) CreateRecord(ctx context.Context, zoneID string, req protocol.CreateDNSRecordRequest) (*protocol.DNSRecord, error) {
	zone, err := s.repo.GetZoneByID(ctx, zoneID)
	if err != nil {
		return nil, fmt.Errorf("zone not found: %w", err)
	}

	name := strings.TrimSpace(strings.ToLower(req.Name))
	name = strings.TrimSuffix(name, "."+zone.Name)
	if name == "" {
		name = "@"
	}

	recType := strings.ToUpper(strings.TrimSpace(req.Type))
	validTypes := map[string]bool{"A": true, "AAAA": true, "CNAME": true, "TXT": true, "SRV": true, "PTR": true, "SOA": true, "NS": true}
	if !validTypes[recType] {
		return nil, fmt.Errorf("unsupported record type %s", recType)
	}

	val := strings.TrimSpace(req.Value)
	if val == "" {
		return nil, fmt.Errorf("record value is required")
	}

	// Validate IPv4 for A record
	if recType == "A" {
		ip := net.ParseIP(val)
		if ip == nil || ip.To4() == nil {
			return nil, fmt.Errorf("invalid IPv4 address: %s", val)
		}
	}

	ttl := req.TTL
	if ttl <= 0 {
		ttl = 300
	}
	if ttl < 30 {
		ttl = 30
	}

	policy := req.RoutingPolicy
	if policy == "" {
		policy = "simple"
	}

	rec := &protocol.DNSRecord{
		ID:            genDNSID("rec"),
		ZoneID:        zoneID,
		Name:          name,
		Type:          recType,
		Value:         val,
		TTL:           ttl,
		RoutingPolicy: policy,
		Status:        "active",
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}

	if err := s.repo.CreateRecord(ctx, rec); err != nil {
		return nil, err
	}
	return rec, nil
}

func (s *Service) ListRecords(ctx context.Context, zoneID string) ([]*protocol.DNSRecord, error) {
	return s.repo.ListRecordsByZone(ctx, zoneID)
}

func (s *Service) GetRecord(ctx context.Context, id string) (*protocol.DNSRecord, error) {
	return s.repo.GetRecordByID(ctx, id)
}

func (s *Service) UpdateRecord(ctx context.Context, zoneID, recordID string, req protocol.CreateDNSRecordRequest) (*protocol.DNSRecord, error) {
	rec, err := s.repo.GetRecordByID(ctx, recordID)
	if err != nil {
		return nil, err
	}

	if req.Name != "" {
		rec.Name = strings.TrimSpace(strings.ToLower(req.Name))
	}
	if req.Type != "" {
		rec.Type = strings.ToUpper(strings.TrimSpace(req.Type))
	}
	if req.Value != "" {
		rec.Value = strings.TrimSpace(req.Value)
	}
	if req.TTL > 0 {
		rec.TTL = req.TTL
	}
	if req.RoutingPolicy != "" {
		rec.RoutingPolicy = req.RoutingPolicy
	}

	if err := s.repo.UpdateRecord(ctx, rec); err != nil {
		return nil, err
	}
	return rec, nil
}

func (s *Service) DeleteRecord(ctx context.Context, zoneID, recordID string) error {
	return s.repo.DeleteRecord(ctx, recordID)
}

// -------------------------------------------------------------
// BIND Zone File Exporter (RFC 1035)
// -------------------------------------------------------------

func (s *Service) ExportBINDZone(ctx context.Context, zoneID string) (string, error) {
	zone, err := s.repo.GetZoneByID(ctx, zoneID)
	if err != nil {
		return "", err
	}

	records, err := s.repo.ListRecordsByZone(ctx, zoneID)
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	sb.WriteString("; ==========================================================\n")
	sb.WriteString(fmt.Sprintf("; ProCloud Authoritative DNS Zone: %s\n", zone.Name))
	sb.WriteString(fmt.Sprintf("; Type: %s | VPC: %s | Generated: %s\n", zone.Type, zone.VPCID, time.Now().UTC().Format(time.RFC3339)))
	sb.WriteString("; ==========================================================\n\n")

	sb.WriteString(fmt.Sprintf("$ORIGIN %s.\n", zone.Name))
	sb.WriteString("$TTL 300\n\n")

	// Group SOA and NS first
	for _, r := range records {
		if r.Type == "SOA" {
			sb.WriteString(fmt.Sprintf("%-20s %-5d IN  SOA   %s\n", r.Name, r.TTL, r.Value))
		}
	}
	for _, r := range records {
		if r.Type == "NS" {
			sb.WriteString(fmt.Sprintf("%-20s %-5d IN  NS    %s\n", r.Name, r.TTL, r.Value))
		}
	}
	sb.WriteString("\n")

	// Other records
	for _, r := range records {
		if r.Type == "SOA" || r.Type == "NS" {
			continue
		}
		sb.WriteString(fmt.Sprintf("%-20s %-5d IN  %-5s %s\n", r.Name, r.TTL, r.Type, r.Value))
	}

	return sb.String(), nil
}

// -------------------------------------------------------------
// DNS Query Engine & Resolver
// -------------------------------------------------------------

func (s *Service) Query(ctx context.Context, req protocol.DNSQueryRequest) (*protocol.DNSQueryResponse, error) {
	host := strings.TrimSpace(strings.ToLower(req.Host))
	host = strings.TrimSuffix(host, ".")
	qType := strings.ToUpper(strings.TrimSpace(req.Type))
	if qType == "" {
		qType = "A"
	}

	zones, err := s.repo.ListZones(ctx, "")
	if err != nil {
		return nil, err
	}

	var matchedZone *protocol.DNSZone
	var relName string
	for _, z := range zones {
		if host == z.Name {
			matchedZone = z
			relName = "@"
			break
		} else if strings.HasSuffix(host, "."+z.Name) {
			matchedZone = z
			relName = strings.TrimSuffix(host, "."+z.Name)
			break
		}
	}

	if matchedZone == nil {
		return &protocol.DNSQueryResponse{
			Host:   host,
			Type:   qType,
			Status: "NXDOMAIN",
		}, nil
	}

	recs, err := s.repo.LookupRecords(ctx, matchedZone.ID, relName, qType)
	if err != nil || len(recs) == 0 {
		return &protocol.DNSQueryResponse{
			Host:   host,
			Type:   qType,
			Status: "NXDOMAIN",
		}, nil
	}

	var answers []*protocol.DNSRecord
	resolvedVal := ""
	for _, r := range recs {
		if r.Type == qType {
			answers = append(answers, r)
			if resolvedVal == "" {
				resolvedVal = r.Value
			}
		} else if r.Type == "CNAME" {
			answers = append(answers, r)
			// Follow CNAME recursively
			cnameTarget := r.Value
			cnameTarget = strings.TrimSuffix(cnameTarget, ".")
			cnameResp, _ := s.Query(ctx, protocol.DNSQueryRequest{Host: cnameTarget, Type: qType})
			if cnameResp != nil && len(cnameResp.Answers) > 0 {
				answers = append(answers, cnameResp.Answers...)
				resolvedVal = cnameResp.Resolved
			}
		}
	}

	return &protocol.DNSQueryResponse{
		Host:     host,
		Type:     qType,
		Answers:  answers,
		Resolved: resolvedVal,
		Status:   "NOERROR",
	}, nil
}

// -------------------------------------------------------------
// Lightweight Authoritative UDP DNS Server (RFC 1035)
// -------------------------------------------------------------

func (s *Service) StartUDPServer(addr string) error {
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return err
	}

	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return fmt.Errorf("failed listening on UDP %s: %w", addr, err)
	}

	s.mu.Lock()
	s.udpConn = conn
	s.mu.Unlock()

	go s.serveUDP(conn)
	return nil
}

func (s *Service) StopUDPServer() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.udpConn != nil {
		_ = s.udpConn.Close()
		s.udpConn = nil
	}
}

func (s *Service) serveUDP(conn *net.UDPConn) {
	for {
		buf := make([]byte, 512)
		n, remoteAddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			return
		}

		if n < 12 {
			continue // Invalid packet
		}

		// Handle DNS query in wire format
		go s.handleWireQuery(conn, remoteAddr, buf[:n])
	}
}

func (s *Service) handleWireQuery(conn *net.UDPConn, clientAddr *net.UDPAddr, queryBuf []byte) {
	txID := binary.BigEndian.Uint16(queryBuf[0:2])
	qdCount := binary.BigEndian.Uint16(queryBuf[4:6])
	if qdCount == 0 {
		return
	}

	// Parse QNAME
	offset := 12
	qnameParts := []string{}
	for offset < len(queryBuf) {
		labelLen := int(queryBuf[offset])
		offset++
		if labelLen == 0 {
			break
		}
		if offset+labelLen > len(queryBuf) {
			return
		}
		qnameParts = append(qnameParts, string(queryBuf[offset:offset+labelLen]))
		offset += labelLen
	}

	if offset+4 > len(queryBuf) {
		return
	}
	qTypeNum := binary.BigEndian.Uint16(queryBuf[offset : offset+2])
	offset += 4

	qHost := strings.Join(qnameParts, ".")
	qType := "A"
	if qTypeNum == 28 {
		qType = "AAAA"
	} else if qTypeNum == 5 {
		qType = "CNAME"
	} else if qTypeNum == 16 {
		qType = "TXT"
	} else if qTypeNum == 2 {
		qType = "NS"
	} else if qTypeNum == 6 {
		qType = "SOA"
	}

	// Query Service
	res, err := s.Query(context.Background(), protocol.DNSQueryRequest{
		Host: qHost,
		Type: qType,
	})

	// Build response packet
	respBuf := make([]byte, 512)
	binary.BigEndian.PutUint16(respBuf[0:2], txID)

	flags := uint16(0x8180) // Standard query response, Authoritative, Recursion Available
	if err != nil || res == nil || res.Status == "NXDOMAIN" || len(res.Answers) == 0 {
		flags |= 0x0003 // NXDOMAIN
	}
	binary.BigEndian.PutUint16(respBuf[2:4], flags)
	binary.BigEndian.PutUint16(respBuf[4:6], 1) // QDCOUNT = 1

	// Copy question section verbatim
	qSectionLen := offset - 12
	copy(respBuf[12:12+qSectionLen], queryBuf[12:offset])
	respOffset := 12 + qSectionLen

	anCount := uint16(0)
	if res != nil && res.Status == "NOERROR" {
		for _, ans := range res.Answers {
			if ans.Type == "A" {
				ip := net.ParseIP(ans.Value).To4()
				if ip == nil {
					continue
				}
				// Name pointer to QNAME (0xc00c)
				binary.BigEndian.PutUint16(respBuf[respOffset:respOffset+2], 0xc00c)
				respOffset += 2
				binary.BigEndian.PutUint16(respBuf[respOffset:respOffset+2], 1) // TYPE A
				respOffset += 2
				binary.BigEndian.PutUint16(respBuf[respOffset:respOffset+2], 1) // CLASS IN
				respOffset += 2
				binary.BigEndian.PutUint32(respBuf[respOffset:respOffset+4], uint32(ans.TTL))
				respOffset += 4
				binary.BigEndian.PutUint16(respBuf[respOffset:respOffset+2], 4) // RDLENGTH 4
				respOffset += 2
				copy(respBuf[respOffset:respOffset+4], ip)
				respOffset += 4
				anCount++
			} else if ans.Type == "CNAME" {
				cnameWire := encodeDNSName(ans.Value)
				binary.BigEndian.PutUint16(respBuf[respOffset:respOffset+2], 0xc00c)
				respOffset += 2
				binary.BigEndian.PutUint16(respBuf[respOffset:respOffset+2], 5) // TYPE CNAME
				respOffset += 2
				binary.BigEndian.PutUint16(respBuf[respOffset:respOffset+2], 1) // CLASS IN
				respOffset += 2
				binary.BigEndian.PutUint32(respBuf[respOffset:respOffset+4], uint32(ans.TTL))
				respOffset += 4
				binary.BigEndian.PutUint16(respBuf[respOffset:respOffset+2], uint16(len(cnameWire)))
				respOffset += 2
				copy(respBuf[respOffset:respOffset+len(cnameWire)], cnameWire)
				respOffset += len(cnameWire)
				anCount++
			} else if ans.Type == "NS" {
				nsWire := encodeDNSName(ans.Value)
				binary.BigEndian.PutUint16(respBuf[respOffset:respOffset+2], 0xc00c)
				respOffset += 2
				binary.BigEndian.PutUint16(respBuf[respOffset:respOffset+2], 2) // TYPE NS
				respOffset += 2
				binary.BigEndian.PutUint16(respBuf[respOffset:respOffset+2], 1) // CLASS IN
				respOffset += 2
				binary.BigEndian.PutUint32(respBuf[respOffset:respOffset+4], uint32(ans.TTL))
				respOffset += 4
				binary.BigEndian.PutUint16(respBuf[respOffset:respOffset+2], uint16(len(nsWire)))
				respOffset += 2
				copy(respBuf[respOffset:respOffset+len(nsWire)], nsWire)
				respOffset += len(nsWire)
				anCount++
			} else if ans.Type == "TXT" {
				valBytes := []byte(ans.Value)
				rdLen := len(valBytes) + 1
				binary.BigEndian.PutUint16(respBuf[respOffset:respOffset+2], 0xc00c)
				respOffset += 2
				binary.BigEndian.PutUint16(respBuf[respOffset:respOffset+2], 16) // TYPE TXT
				respOffset += 2
				binary.BigEndian.PutUint16(respBuf[respOffset:respOffset+2], 1) // CLASS IN
				respOffset += 2
				binary.BigEndian.PutUint32(respBuf[respOffset:respOffset+4], uint32(ans.TTL))
				respOffset += 4
				binary.BigEndian.PutUint16(respBuf[respOffset:respOffset+2], uint16(rdLen))
				respOffset += 2
				respBuf[respOffset] = byte(len(valBytes))
				respOffset++
				copy(respBuf[respOffset:respOffset+len(valBytes)], valBytes)
				respOffset += len(valBytes)
				anCount++
			}
		}
	}
	binary.BigEndian.PutUint16(respBuf[6:8], anCount) // ANCOUNT

	_, _ = conn.WriteToUDP(respBuf[:respOffset], clientAddr)
}

func encodeDNSName(name string) []byte {
	name = strings.TrimSuffix(name, ".")
	parts := strings.Split(name, ".")
	var buf []byte
	for _, p := range parts {
		if len(p) == 0 {
			continue
		}
		buf = append(buf, byte(len(p)))
		buf = append(buf, []byte(p)...)
	}
	buf = append(buf, 0)
	return buf
}

// -------------------------------------------------------------
// Application Load Balancer (ALB) DNS CNAME Engine
// -------------------------------------------------------------

// SanitizeDNSLabel converts an arbitrary string into a valid RFC 1123 DNS label:
// lowercase, alphanumeric and hyphens, max 63 characters.
func SanitizeDNSLabel(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var sb strings.Builder
	for _, ch := range name {
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') {
			sb.WriteRune(ch)
		} else if ch == '-' || ch == '_' || ch == ' ' || ch == '.' {
			sb.WriteRune('-')
		}
	}
	res := strings.Trim(sb.String(), "-")
	for strings.Contains(res, "--") {
		res = strings.ReplaceAll(res, "--", "-")
	}
	if len(res) > 63 {
		res = res[:63]
		res = strings.TrimRight(res, "-")
	}
	if res == "" {
		res = "alb"
	}
	return res
}

// EnsureALBCNAME ensures that the procloud.net DNS zone exists and provisions:
// 1. A Canonical A record: "alb-<cleanName>.procloud.net" -> publicIP (e.g. 127.0.0.1)
// 2. A CNAME record: "<cleanName>.procloud.net" -> "alb-<cleanName>.procloud.net."
// Returns the CNAME record.
func (s *Service) EnsureALBCNAME(ctx context.Context, tenantID, lbName, lbType, publicIP string) (*protocol.DNSRecord, error) {
	if lbName == "" {
		return nil, fmt.Errorf("load balancer name is required")
	}
	if tenantID == "" {
		tenantID = "tnt-default"
	}

	cleanName := SanitizeDNSLabel(lbName)

	// Resolve publicIP: if empty or "0.0.0.0", use "127.0.0.1" so it's a valid host VIP
	ip := strings.TrimSpace(publicIP)
	if ip == "" || ip == "0.0.0.0" {
		ip = "127.0.0.1"
	}
	parsedIP := net.ParseIP(ip)
	if parsedIP == nil || parsedIP.To4() == nil {
		ip = "127.0.0.1"
	}

	// 1. Ensure procloud.net zone exists
	zone, err := s.repo.GetZoneByName(ctx, tenantID, "procloud.net")
	if err != nil || zone == nil {
		zone, err = s.repo.GetZoneByName(ctx, "", "procloud.net")
	}
	if zone == nil {
		zone, err = s.CreateZone(ctx, protocol.CreateDNSZoneRequest{
			TenantID:    tenantID,
			Name:        "procloud.net",
			Type:        "private",
			Description: "ProCloud Sovereign Cloud Default Domain & ALB Ingress Routing",
		})
		if err != nil {
			return nil, fmt.Errorf("failed ensuring procloud.net zone: %w", err)
		}
	}

	// 2. Ensure canonical A record: "alb-<cleanName>"
	canonicalName := fmt.Sprintf("alb-%s", cleanName)
	canonicalTarget := fmt.Sprintf("alb-%s.procloud.net.", cleanName)

	records, err := s.repo.ListRecordsByZone(ctx, zone.ID)
	if err != nil {
		records = []*protocol.DNSRecord{}
	}

	var existingARecord *protocol.DNSRecord
	var existingCNAMERecord *protocol.DNSRecord

	for _, r := range records {
		if strings.EqualFold(r.Name, canonicalName) && r.Type == "A" {
			existingARecord = r
		}
		if strings.EqualFold(r.Name, cleanName) && r.Type == "CNAME" {
			existingCNAMERecord = r
		}
	}

	// Create or update canonical A record
	if existingARecord == nil {
		existingARecord = &protocol.DNSRecord{
			ID:            genDNSID("rec"),
			ZoneID:        zone.ID,
			Name:          canonicalName,
			Type:          "A",
			Value:         ip,
			TTL:           300,
			RoutingPolicy: "simple",
			Status:        "active",
			CreatedAt:     time.Now(),
			UpdatedAt:     time.Now(),
		}
		if err := s.repo.CreateRecord(ctx, existingARecord); err != nil {
			return nil, fmt.Errorf("failed creating canonical A record: %w", err)
		}
	} else if existingARecord.Value != ip {
		existingARecord.Value = ip
		existingARecord.UpdatedAt = time.Now()
		_ = s.repo.UpdateRecord(ctx, existingARecord)
	}

	// Create or update CNAME record
	if existingCNAMERecord == nil {
		existingCNAMERecord = &protocol.DNSRecord{
			ID:            genDNSID("rec"),
			ZoneID:        zone.ID,
			Name:          cleanName,
			Type:          "CNAME",
			Value:         canonicalTarget,
			TTL:           300,
			RoutingPolicy: "simple",
			Status:        "active",
			CreatedAt:     time.Now(),
			UpdatedAt:     time.Now(),
		}
		if err := s.repo.CreateRecord(ctx, existingCNAMERecord); err != nil {
			return nil, fmt.Errorf("failed creating CNAME record: %w", err)
		}
	} else if existingCNAMERecord.Value != canonicalTarget {
		existingCNAMERecord.Value = canonicalTarget
		existingCNAMERecord.UpdatedAt = time.Now()
		_ = s.repo.UpdateRecord(ctx, existingCNAMERecord)
	}

	return existingCNAMERecord, nil
}

// RemoveALBCNAME removes the CNAME and canonical A records for an ALB from procloud.net zone.
func (s *Service) RemoveALBCNAME(ctx context.Context, tenantID, lbName string) error {
	cleanName := SanitizeDNSLabel(lbName)
	canonicalName := fmt.Sprintf("alb-%s", cleanName)

	zone, err := s.repo.GetZoneByName(ctx, tenantID, "procloud.net")
	if err != nil || zone == nil {
		zone, err = s.repo.GetZoneByName(ctx, "", "procloud.net")
	}
	if zone == nil {
		return nil
	}

	records, err := s.repo.ListRecordsByZone(ctx, zone.ID)
	if err != nil {
		return nil
	}

	for _, r := range records {
		if strings.EqualFold(r.Name, cleanName) && r.Type == "CNAME" {
			_ = s.repo.DeleteRecord(ctx, r.ID)
		}
		if strings.EqualFold(r.Name, canonicalName) && r.Type == "A" {
			_ = s.repo.DeleteRecord(ctx, r.ID)
		}
	}
	return nil
}

// SyncAllALBCNAMEs iterates through all load balancers and ensures every Application Load Balancer
// has a CNAME record in procloud.net.
func (s *Service) SyncAllALBCNAMEs(ctx context.Context, lbs []*protocol.LoadBalancer) error {
	for _, lb := range lbs {
		lbType := strings.ToLower(strings.TrimSpace(lb.Type))
		if lbType != "layer4" && lbType != "l4" {
			_, err := s.EnsureALBCNAME(ctx, lb.TenantID, lb.Name, lb.Type, lb.PublicIP)
			if err != nil {
				fmt.Printf("[DNS] Sync ALB CNAME warning for %s: %v\n", lb.Name, err)
			}
		}
	}
	return nil
}
