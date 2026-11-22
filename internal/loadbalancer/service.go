package loadbalancer

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"procloud.io/procloud/internal/repository"
	"procloud.io/procloud/pkg/podman"
	"procloud.io/procloud/pkg/protocol"
)

type Service struct {
	repo    repository.LoadBalancerRepository
	runtime podman.ContainerRuntime
	confDir string
	mu      sync.Mutex
}

func NewService(
	repo repository.LoadBalancerRepository,
	runtime podman.ContainerRuntime,
	confDir string,
) *Service {
	if confDir == "" {
		confDir = os.Getenv("PROCLOUD_LB_CONF_DIR")
		if confDir == "" {
			confDir = "/etc/nginx/conf.d/procloud-lb"
		}
	}
	if err := os.MkdirAll(confDir, 0755); err != nil {
		// Fallback to user / temp directory if permission denied (e.g. rootless/dev mode)
		confDir = filepath.Join(os.TempDir(), "procloud-lb")
		_ = os.MkdirAll(confDir, 0755)
	}

	svc := &Service{
		repo:    repo,
		runtime: runtime,
		confDir: confDir,
	}
	_ = svc.ensureDefaultCertificate()
	return svc
}

// -------------------------------------------------------------
// Load Balancer Operations
// -------------------------------------------------------------

func (s *Service) CreateLoadBalancer(ctx context.Context, req protocol.CreateLoadBalancerRequest) (*protocol.LoadBalancer, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("loadbalancer name is required")
	}
	if req.TenantID == "" {
		req.TenantID = "tnt-default"
	}
	if req.Type == "" {
		req.Type = "hybrid"
	}
	if req.PublicIP == "" {
		req.PublicIP = "0.0.0.0"
	}

	lb := &protocol.LoadBalancer{
		ID:          fmt.Sprintf("lb-%d", time.Now().UnixNano()),
		TenantID:    req.TenantID,
		VPCID:       req.VPCID,
		Name:        req.Name,
		Description: req.Description,
		Type:        req.Type,
		PublicIP:    req.PublicIP,
		Status:      "active",
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	if err := s.repo.CreateLoadBalancer(ctx, lb); err != nil {
		return nil, fmt.Errorf("failed creating loadbalancer: %w", err)
	}

	_, _ = s.SynthesizeConfig(ctx, lb.ID)
	return lb, nil
}

func (s *Service) GetLoadBalancer(ctx context.Context, id string) (*protocol.LoadBalancer, error) {
	return s.repo.GetLoadBalancerByID(ctx, id)
}

func (s *Service) ListLoadBalancers(ctx context.Context, tenantID string) ([]*protocol.LoadBalancer, error) {
	return s.repo.ListLoadBalancers(ctx, tenantID)
}

func (s *Service) DeleteLoadBalancer(ctx context.Context, id string) error {
	lb, err := s.repo.GetLoadBalancerByID(ctx, id)
	if err != nil {
		return err
	}

	// Remove config file
	confFile := filepath.Join(s.confDir, fmt.Sprintf("%s.conf", lb.ID))
	_ = os.Remove(confFile)

	if err := s.repo.DeleteLoadBalancer(ctx, id); err != nil {
		return err
	}

	// Reload proxy
	if _, err := exec.LookPath("nginx"); err == nil {
		_ = exec.CommandContext(ctx, "nginx", "-s", "reload").Run()
	}
	return nil
}

// -------------------------------------------------------------
// Listener Operations
// -------------------------------------------------------------

func (s *Service) CreateListener(ctx context.Context, lbID string, req protocol.CreateListenerRequest) (*protocol.LBListener, error) {
	if req.Port <= 0 || req.Port > 65535 {
		return nil, fmt.Errorf("invalid listener port %d", req.Port)
	}
	if req.Protocol == "" {
		req.Protocol = "HTTP"
	}
	req.Protocol = strings.ToUpper(req.Protocol)

	if req.Name == "" {
		req.Name = fmt.Sprintf("%s-%d", strings.ToLower(req.Protocol), req.Port)
	}

	// Normalise alias fields
	if req.SSLCertSecret == "" && req.SSLSecretName != "" {
		req.SSLCertSecret = req.SSLSecretName
	}
	if !req.HTTPRedirect && req.RedirectToHTTPS {
		req.HTTPRedirect = true
	}

	sslType := strings.ToLower(strings.TrimSpace(req.SSLCertType))
	if sslType == "" {
		if req.SSLCertSecret != "" {
			sslType = "secret"
		} else if req.SSLCertData != "" {
			sslType = "custom"
		} else if req.Protocol == "HTTPS" {
			sslType = "auto"
		}
	}

	listenerID := fmt.Sprintf("lsn-%d", time.Now().UnixNano())

	// If custom SSL certificate data is provided, save it to disk
	if (sslType == "custom" || req.SSLCertData != "") && req.SSLCertData != "" {
		sslType = "custom"
		certsDir := filepath.Join(s.confDir, "certs")
		_ = os.MkdirAll(certsDir, 0755)
		certPath := filepath.Join(certsDir, fmt.Sprintf("custom-%s.crt", listenerID))
		keyPath := filepath.Join(certsDir, fmt.Sprintf("custom-%s.key", listenerID))
		if err := os.WriteFile(certPath, []byte(strings.TrimSpace(req.SSLCertData)+"\n"), 0644); err != nil {
			return nil, fmt.Errorf("failed writing custom certificate: %w", err)
		}
		if req.SSLKeyData != "" {
			if err := os.WriteFile(keyPath, []byte(strings.TrimSpace(req.SSLKeyData)+"\n"), 0600); err != nil {
				return nil, fmt.Errorf("failed writing custom private key: %w", err)
			}
		}
	}

	// If secret SSL is selected and Secret exists in Podman Secrets, cache cert if needed
	if sslType == "secret" && req.SSLCertSecret != "" && s.runtime != nil {
		if sec, err := s.runtime.InspectSecret(ctx, req.SSLCertSecret, true); err == nil && sec != nil && sec.SecretData != "" {
			certsDir := filepath.Join(s.confDir, "certs")
			_ = os.MkdirAll(certsDir, 0755)
			if strings.Contains(sec.SecretData, "BEGIN CERTIFICATE") {
				secCertPath := filepath.Join(certsDir, fmt.Sprintf("secret-%s.crt", req.SSLCertSecret))
				_ = os.WriteFile(secCertPath, []byte(sec.SecretData), 0644)
			}
			if strings.Contains(sec.SecretData, "PRIVATE KEY") {
				secKeyPath := filepath.Join(certsDir, fmt.Sprintf("secret-%s.key", req.SSLCertSecret))
				_ = os.WriteFile(secKeyPath, []byte(sec.SecretData), 0600)
			}
		}
	}

	listener := &protocol.LBListener{
		ID:             listenerID,
		LoadBalancerID: lbID,
		Name:           req.Name,
		Port:           req.Port,
		Protocol:       req.Protocol,
		SSLCertSecret:  req.SSLCertSecret,
		SSLCertType:    sslType,
		DefaultPoolID:  req.DefaultPoolID,
		HTTPRedirect:   req.HTTPRedirect,
		CreatedAt:      time.Now(),
	}

	if err := s.repo.CreateListener(ctx, listener); err != nil {
		return nil, err
	}

	_, _ = s.SynthesizeConfig(ctx, lbID)
	return listener, nil
}

func (s *Service) DeleteListener(ctx context.Context, lbID, listenerID string) error {
	if err := s.repo.DeleteListener(ctx, listenerID); err != nil {
		return err
	}

	// Clean up custom certificate files if present
	certsDir := filepath.Join(s.confDir, "certs")
	_ = os.Remove(filepath.Join(certsDir, fmt.Sprintf("custom-%s.crt", listenerID)))
	_ = os.Remove(filepath.Join(certsDir, fmt.Sprintf("custom-%s.key", listenerID)))

	_, _ = s.SynthesizeConfig(ctx, lbID)
	return nil
}

// -------------------------------------------------------------
// Pool Operations
// -------------------------------------------------------------

func (s *Service) CreatePool(ctx context.Context, lbID string, req protocol.CreatePoolRequest) (*protocol.LBPool, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("pool name is required")
	}
	if req.Protocol == "" {
		req.Protocol = "HTTP"
	}
	req.Protocol = strings.ToUpper(req.Protocol)

	if req.Algorithm == "" {
		req.Algorithm = "round_robin"
	}
	if req.HealthPath == "" {
		req.HealthPath = "/healthz"
	}
	if req.HealthIntervalSec <= 0 {
		req.HealthIntervalSec = 5
	}
	if req.HealthTimeoutSec <= 0 {
		req.HealthTimeoutSec = 2
	}
	if req.HealthUnhealthyThreshold <= 0 {
		req.HealthUnhealthyThreshold = 3
	}

	pool := &protocol.LBPool{
		ID:                       fmt.Sprintf("pool-%d", time.Now().UnixNano()),
		LoadBalancerID:           lbID,
		Name:                     req.Name,
		Protocol:                 req.Protocol,
		Algorithm:                req.Algorithm,
		HealthPath:               req.HealthPath,
		HealthIntervalSec:        req.HealthIntervalSec,
		HealthTimeoutSec:         req.HealthTimeoutSec,
		HealthUnhealthyThreshold: req.HealthUnhealthyThreshold,
		CreatedAt:                time.Now(),
	}

	if err := s.repo.CreatePool(ctx, pool); err != nil {
		return nil, err
	}

	_, _ = s.SynthesizeConfig(ctx, lbID)
	return pool, nil
}

func (s *Service) DeletePool(ctx context.Context, lbID, poolID string) error {
	if err := s.repo.DeletePool(ctx, poolID); err != nil {
		return err
	}
	_, _ = s.SynthesizeConfig(ctx, lbID)
	return nil
}

// -------------------------------------------------------------
// Member Operations (Upstream Attach / Detach)
// -------------------------------------------------------------

func (s *Service) AttachMember(ctx context.Context, lbID, poolID string, req protocol.AttachMemberRequest) (*protocol.LBMember, error) {
	if req.Port <= 0 || req.Port > 65535 {
		return nil, fmt.Errorf("invalid member port %d", req.Port)
	}
	if req.TargetType == "" {
		req.TargetType = "container"
	}
	if req.Weight <= 0 {
		req.Weight = 1
	}

	memberIP := req.IP
	if memberIP == "" && req.TargetRef != "" {
		resolvedIP, err := s.ResolveTargetIP(ctx, req.TargetType, req.TargetRef)
		if err == nil && resolvedIP != "" {
			memberIP = resolvedIP
		}
	}
	if memberIP == "" {
		memberIP = "127.0.0.1"
	}

	member := &protocol.LBMember{
		ID:         fmt.Sprintf("mbr-%d", time.Now().UnixNano()),
		PoolID:     poolID,
		TargetType: req.TargetType,
		TargetRef:  req.TargetRef,
		IP:         memberIP,
		Port:       req.Port,
		Weight:     req.Weight,
		Status:     "healthy",
		CreatedAt:  time.Now(),
	}

	if err := s.repo.CreateMember(ctx, member); err != nil {
		return nil, fmt.Errorf("failed attaching member: %w", err)
	}

	_, _ = s.SynthesizeConfig(ctx, lbID)
	return member, nil
}

func (s *Service) DetachMember(ctx context.Context, lbID, memberID string) error {
	if err := s.repo.DeleteMember(ctx, memberID); err != nil {
		return err
	}
	_, _ = s.SynthesizeConfig(ctx, lbID)
	return nil
}

// -------------------------------------------------------------
// L7 Policy Operations
// -------------------------------------------------------------

func (s *Service) CreatePolicy(ctx context.Context, lbID, listenerID string, req protocol.CreatePolicyRequest) (*protocol.LBL7Policy, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("policy name is required")
	}
	if req.RuleType == "" {
		req.RuleType = "PATH_PREFIX"
	}
	if req.RuleValue == "" {
		req.RuleValue = "/"
	}
	if req.TargetPoolID == "" {
		return nil, fmt.Errorf("target_pool_id is required")
	}
	if req.Priority <= 0 {
		req.Priority = 1
	}

	pol := &protocol.LBL7Policy{
		ID:           fmt.Sprintf("pol-%d", time.Now().UnixNano()),
		ListenerID:   listenerID,
		Name:         req.Name,
		Action:       "FORWARD",
		RuleType:     req.RuleType,
		RuleValue:    req.RuleValue,
		TargetPoolID: req.TargetPoolID,
		Priority:     req.Priority,
		CreatedAt:    time.Now(),
	}

	if err := s.repo.CreatePolicy(ctx, pol); err != nil {
		return nil, err
	}

	_, _ = s.SynthesizeConfig(ctx, lbID)
	return pol, nil
}

func (s *Service) DeletePolicy(ctx context.Context, lbID, policyID string) error {
	if err := s.repo.DeletePolicy(ctx, policyID); err != nil {
		return err
	}
	_, _ = s.SynthesizeConfig(ctx, lbID)
	return nil
}

// -------------------------------------------------------------
// Upstream IP Resolution
// -------------------------------------------------------------

func (s *Service) ResolveTargetIP(ctx context.Context, targetType, targetRef string) (string, error) {
	if targetType == "container" {
		ctr, err := s.runtime.InspectContainer(ctx, targetRef)
		if err != nil {
			return "", err
		}
		if ctr != nil && ctr.IPAddress != "" {
			return ctr.IPAddress, nil
		}
	} else if targetType == "pod" {
		pod, err := s.runtime.InspectPod(ctx, targetRef)
		if err != nil {
			return "", err
		}
		if pod != nil && len(pod.Containers) > 0 {
			ctrRef := pod.Containers[0].ID
			if len(pod.Containers[0].Names) > 0 {
				ctrRef = pod.Containers[0].Names[0]
			}
			ctr, err := s.runtime.InspectContainer(ctx, ctrRef)
			if err == nil && ctr != nil && ctr.IPAddress != "" {
				return ctr.IPAddress, nil
			}
		}
	}
	return "", fmt.Errorf("unable to resolve IP for %s/%s", targetType, targetRef)
}

// -------------------------------------------------------------
// Dynamic Configuration Synthesizer (L4 / L7 / SSL / VIP)
// -------------------------------------------------------------

func (s *Service) SynthesizeConfig(ctx context.Context, lbID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	lb, err := s.repo.GetLoadBalancerByID(ctx, lbID)
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# ==========================================================\n"))
	sb.WriteString(fmt.Sprintf("# ProCloud LBaaS Dynamic Configuration: %s (%s)\n", lb.Name, lb.ID))
	sb.WriteString(fmt.Sprintf("# Tenant: %s | Public VIP: %s | Type: %s\n", lb.TenantID, lb.PublicIP, lb.Type))
	sb.WriteString(fmt.Sprintf("# Generated: %s\n", time.Now().UTC().Format(time.RFC3339)))
	sb.WriteString(fmt.Sprintf("# ==========================================================\n\n"))

	// 1. Group Pools by ID for easy resolution
	poolMap := make(map[string]*protocol.LBPool)
	for _, p := range lb.Pools {
		poolMap[p.ID] = p
	}

	// 2. Separate Listeners into L7 (HTTP/HTTPS) and L4 (TCP/UDP)
	var l7Listeners []*protocol.LBListener
	var l4Listeners []*protocol.LBListener

	for _, l := range lb.Listeners {
		switch l.Protocol {
		case "HTTP", "HTTPS":
			l7Listeners = append(l7Listeners, l)
		case "TCP", "UDP":
			l4Listeners = append(l4Listeners, l)
		}
	}

	// ---------------------------------------------------------
	// LAYER 7 (HTTP / HTTPS) CONFIGURATION
	// ---------------------------------------------------------
	if len(l7Listeners) > 0 {
		// Define Upstream Pools for L7
		for _, p := range lb.Pools {
			if p.Protocol == "TCP" || p.Protocol == "UDP" {
				continue
			}
			upstreamName := fmt.Sprintf("upstream_l7_%s_%s", lb.ID, p.ID)
			sb.WriteString(fmt.Sprintf("upstream %s {\n", upstreamName))
			switch p.Algorithm {
			case "least_conn":
				sb.WriteString("    least_conn;\n")
			case "ip_hash":
				sb.WriteString("    ip_hash;\n")
			}
			if len(p.Members) == 0 {
				sb.WriteString("    server 127.0.0.1:65535 down; # empty pool\n")
			} else {
				for _, m := range p.Members {
					state := ""
					if m.Status == "unhealthy" || m.Status == "draining" {
						state = " down"
					}
					sb.WriteString(fmt.Sprintf("    server %s:%d weight=%d max_fails=%d fail_timeout=10s%s;\n",
						m.IP, m.Port, m.Weight, p.HealthUnhealthyThreshold, state))
				}
			}
			sb.WriteString("    keepalive 32;\n")
			sb.WriteString("}\n\n")
		}

		// Define L7 Virtual Host Servers
		for _, l := range l7Listeners {
			listenAddr := fmt.Sprintf("%s:%d", lb.PublicIP, l.Port)
			if lb.PublicIP == "0.0.0.0" || lb.PublicIP == "" {
				listenAddr = fmt.Sprintf("%d", l.Port)
			}

			// If HTTP Redirect is requested on port 80
			if l.Protocol == "HTTP" && l.HTTPRedirect {
				sb.WriteString("server {\n")
				sb.WriteString(fmt.Sprintf("    listen %s;\n", listenAddr))
				sb.WriteString("    server_name _;\n")
				sb.WriteString("    return 301 https://$host$request_uri;\n")
				sb.WriteString("}\n\n")
				continue
			}

			sb.WriteString("server {\n")
			if l.Protocol == "HTTPS" {
				sb.WriteString(fmt.Sprintf("    listen %s ssl http2;\n", listenAddr))
				// TLS Certificate setup
				certPath, keyPath := s.getCertificatePaths(lb.TenantID, l)
				sb.WriteString(fmt.Sprintf("    ssl_certificate %s;\n", certPath))
				sb.WriteString(fmt.Sprintf("    ssl_certificate_key %s;\n", keyPath))
				sb.WriteString("    ssl_protocols TLSv1.2 TLSv1.3;\n")
				sb.WriteString("    ssl_ciphers HIGH:!aNULL:!MD5;\n")
			} else {
				sb.WriteString(fmt.Sprintf("    listen %s;\n", listenAddr))
			}
			sb.WriteString("    server_name _;\n\n")

			// Common reverse proxy headers
			sb.WriteString("    proxy_set_header Host $host;\n")
			sb.WriteString("    proxy_set_header X-Real-IP $remote_addr;\n")
			sb.WriteString("    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;\n")
			sb.WriteString("    proxy_set_header X-Forwarded-Proto $scheme;\n")
			sb.WriteString("    proxy_http_version 1.1;\n")
			sb.WriteString("    proxy_set_header Upgrade $http_upgrade;\n")
			sb.WriteString("    proxy_set_header Connection \"upgrade\";\n\n")

			// L7 Routing Policies
			for _, pol := range l.Policies {
				targetPool := poolMap[pol.TargetPoolID]
				if targetPool == nil {
					continue
				}
				upstreamName := fmt.Sprintf("upstream_l7_%s_%s", lb.ID, targetPool.ID)
				switch pol.RuleType {
				case "PATH_PREFIX":
					sb.WriteString(fmt.Sprintf("    # Policy: %s (Priority %d)\n", pol.Name, pol.Priority))
					sb.WriteString(fmt.Sprintf("    location ^~ %s {\n", pol.RuleValue))
					sb.WriteString(fmt.Sprintf("        proxy_pass http://%s;\n", upstreamName))
					sb.WriteString("    }\n\n")
				case "HOST_HEADER":
					sb.WriteString(fmt.Sprintf("    # Policy: %s (Host: %s)\n", pol.Name, pol.RuleValue))
					sb.WriteString(fmt.Sprintf("    if ($host = \"%s\") {\n", pol.RuleValue))
					sb.WriteString(fmt.Sprintf("        proxy_pass http://%s;\n", upstreamName))
					sb.WriteString("    }\n\n")
				}
			}

			// Default location fallback
			defaultPool := poolMap[l.DefaultPoolID]
			if defaultPool != nil {
				upstreamName := fmt.Sprintf("upstream_l7_%s_%s", lb.ID, defaultPool.ID)
				sb.WriteString("    # Default Fallback Pool\n")
				sb.WriteString("    location / {\n")
				sb.WriteString(fmt.Sprintf("        proxy_pass http://%s;\n", upstreamName))
				sb.WriteString("    }\n")
			} else {
				sb.WriteString("    location / {\n")
				sb.WriteString("        return 503 \"ProCloud LBaaS: No default backend pool configured\";\n")
				sb.WriteString("    }\n")
			}

			sb.WriteString("}\n\n")
		}
	}

	// ---------------------------------------------------------
	// LAYER 4 (TCP / UDP) STREAM CONFIGURATION
	// ---------------------------------------------------------
	if len(l4Listeners) > 0 {
		sb.WriteString("# --- Layer 4 Stream (TCP / UDP) Section ---\n")
		for _, l := range l4Listeners {
			listenAddr := fmt.Sprintf("%s:%d", lb.PublicIP, l.Port)
			if lb.PublicIP == "0.0.0.0" || lb.PublicIP == "" {
				listenAddr = fmt.Sprintf("%d", l.Port)
			}
			udpFlag := ""
			if l.Protocol == "UDP" {
				udpFlag = " udp"
			}

			pool := poolMap[l.DefaultPoolID]
			upstreamName := fmt.Sprintf("upstream_l4_%s_%s", lb.ID, l.ID)
			sb.WriteString(fmt.Sprintf("upstream %s {\n", upstreamName))
			if pool != nil {
				if pool.Algorithm == "least_conn" {
					sb.WriteString("    least_conn;\n")
				}
				for _, m := range pool.Members {
					sb.WriteString(fmt.Sprintf("    server %s:%d weight=%d;\n", m.IP, m.Port, m.Weight))
				}
			} else {
				sb.WriteString("    server 127.0.0.1:65535;\n")
			}
			sb.WriteString("}\n\n")

			sb.WriteString("server {\n")
			sb.WriteString(fmt.Sprintf("    listen %s%s;\n", listenAddr, udpFlag))
			sb.WriteString(fmt.Sprintf("    proxy_pass %s;\n", upstreamName))
			sb.WriteString("    proxy_connect_timeout 3s;\n")
			sb.WriteString("    proxy_timeout 300s;\n")
			sb.WriteString("}\n\n")
		}
	}

	confContent := sb.String()
	targetFile := filepath.Join(s.confDir, fmt.Sprintf("%s.conf", lb.ID))
	if err := os.WriteFile(targetFile, []byte(confContent), 0644); err != nil {
		return confContent, fmt.Errorf("failed writing lb conf file: %w", err)
	}

	// Graceful reload if nginx binary is running
	if _, err := exec.LookPath("nginx"); err == nil {
		_ = exec.CommandContext(ctx, "nginx", "-s", "reload").Run()
	}

	return confContent, nil
}

// -------------------------------------------------------------
// Upstream Health Monitoring Probe
// -------------------------------------------------------------

func (s *Service) CheckMemberHealth(ctx context.Context, m *protocol.LBMember, pool *protocol.LBPool) bool {
	if pool.Protocol == "TCP" || pool.Protocol == "UDP" {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", m.IP, m.Port), time.Duration(pool.HealthTimeoutSec)*time.Second)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	}

	// HTTP / HTTPS check
	scheme := "http"
	if pool.Protocol == "HTTPS" {
		scheme = "https"
	}
	url := fmt.Sprintf("%s://%s:%d%s", scheme, m.IP, m.Port, pool.HealthPath)

	client := &http.Client{
		Timeout: time.Duration(pool.HealthTimeoutSec) * time.Second,
	}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 400
}

// -------------------------------------------------------------
// TLS / SSL Certificate Management (Auto, Secret, Custom)
// -------------------------------------------------------------

func (s *Service) getCertificatePaths(tenantID string, l *protocol.LBListener) (string, string) {
	certsDir := filepath.Join(s.confDir, "certs")
	_ = os.MkdirAll(certsDir, 0755)

	// 1. Custom Certificate Upload
	if l.SSLCertType == "custom" {
		customCert := filepath.Join(certsDir, fmt.Sprintf("custom-%s.crt", l.ID))
		customKey := filepath.Join(certsDir, fmt.Sprintf("custom-%s.key", l.ID))
		if fileExists(customCert) && fileExists(customKey) {
			return customCert, customKey
		}
	}

	// 2. Secret Reference
	if l.SSLCertSecret != "" {
		secCert := fmt.Sprintf("/var/lib/procloud/secrets/%s/%s/tls.crt", tenantID, l.SSLCertSecret)
		secKey := fmt.Sprintf("/var/lib/procloud/secrets/%s/%s/tls.key", tenantID, l.SSLCertSecret)
		if fileExists(secCert) && fileExists(secKey) {
			return secCert, secKey
		}

		vaultCert := filepath.Join(certsDir, fmt.Sprintf("secret-%s.crt", l.SSLCertSecret))
		vaultKey := filepath.Join(certsDir, fmt.Sprintf("secret-%s.key", l.SSLCertSecret))
		if fileExists(vaultCert) && fileExists(vaultKey) {
			return vaultCert, vaultKey
		}

		if l.SSLCertType == "secret" || l.SSLCertType == "" {
			return secCert, secKey
		}
	}

	// 3. Auto-Generated ProCloud Default TLS
	defaultCert := filepath.Join(certsDir, "procloud-default.crt")
	defaultKey := filepath.Join(certsDir, "procloud-default.key")
	if fileExists(defaultCert) && fileExists(defaultKey) {
		return defaultCert, defaultKey
	}
	if fileExists("/etc/ssl/certs/procloud-default.crt") && fileExists("/etc/ssl/private/procloud-default.key") {
		return "/etc/ssl/certs/procloud-default.crt", "/etc/ssl/private/procloud-default.key"
	}

	_ = s.ensureDefaultCertificate()
	return defaultCert, defaultKey
}

func (s *Service) ensureDefaultCertificate() error {
	certsDir := filepath.Join(s.confDir, "certs")
	if err := os.MkdirAll(certsDir, 0755); err != nil {
		return err
	}
	certPath := filepath.Join(certsDir, "procloud-default.crt")
	keyPath := filepath.Join(certsDir, "procloud-default.key")

	if fileExists(certPath) && fileExists(keyPath) {
		return nil
	}

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("failed generating ecdsa key: %w", err)
	}

	notBefore := time.Now().Add(-1 * time.Hour)
	notAfter := notBefore.Add(10 * 365 * 24 * time.Hour)

	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return fmt.Errorf("failed generating serial number: %w", err)
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"ProCloud Sovereign Infrastructure"},
			CommonName:   "procloud.local",
		},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost", "*.internal", "*.procloud.local", "procloud"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("0.0.0.0"), net.ParseIP("198.51.100.25")},
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return fmt.Errorf("failed creating certificate: %w", err)
	}

	certOut, err := os.Create(certPath)
	if err != nil {
		return err
	}
	_ = pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	_ = certOut.Close()

	keyBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return err
	}
	keyOut, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	_ = pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})
	_ = keyOut.Close()

	return nil
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}
