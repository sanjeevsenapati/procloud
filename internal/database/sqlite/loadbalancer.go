package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"procloud.io/procloud/pkg/protocol"
)

// -------------------------------------------------------------
// Load Balancer Repository
// -------------------------------------------------------------

func (r *Repository) CreateLoadBalancer(ctx context.Context, lb *protocol.LoadBalancer) error {
	query := `
		INSERT INTO loadbalancers (
			id, tenant_id, vpc_id, name, description, type, public_ip, status, ssl_cert_secret, ssl_cert_type, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	certType := lb.SSLCertType
	if certType == "" {
		if lb.SSLCertSecret != "" {
			certType = "secret"
		} else {
			certType = "auto"
		}
	}
	_, err := r.db.sqlDB.ExecContext(ctx, query,
		lb.ID, lb.TenantID, lb.VPCID, lb.Name, lb.Description, lb.Type, lb.PublicIP, lb.Status, lb.SSLCertSecret, certType, lb.CreatedAt, lb.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed inserting loadbalancer: %w", err)
	}
	return nil
}

func (r *Repository) GetLoadBalancerByID(ctx context.Context, id string) (*protocol.LoadBalancer, error) {
	query := `
		SELECT id, tenant_id, vpc_id, name, description, type, public_ip, status, ssl_cert_secret, ssl_cert_type, created_at, updated_at
		FROM loadbalancers
		WHERE id = ?
	`
	row := r.db.sqlDB.QueryRowContext(ctx, query, id)
	lb := &protocol.LoadBalancer{}
	err := row.Scan(
		&lb.ID, &lb.TenantID, &lb.VPCID, &lb.Name, &lb.Description, &lb.Type, &lb.PublicIP, &lb.Status, &lb.SSLCertSecret, &lb.SSLCertType, &lb.CreatedAt, &lb.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("loadbalancer %s not found", id)
		}
		return nil, fmt.Errorf("querying loadbalancer: %w", err)
	}

	// Fetch listeners
	listeners, err := r.ListListenersByLB(ctx, lb.ID)
	if err == nil {
		lb.Listeners = listeners
	}

	// Fetch pools
	pools, err := r.ListPoolsByLB(ctx, lb.ID)
	if err == nil {
		lb.Pools = pools
	}

	return lb, nil
}

func (r *Repository) ListLoadBalancers(ctx context.Context, tenantID string) ([]*protocol.LoadBalancer, error) {
	var query string
	var args []interface{}
	if tenantID != "" {
		query = `
			SELECT id, tenant_id, vpc_id, name, description, type, public_ip, status, ssl_cert_secret, ssl_cert_type, created_at, updated_at
			FROM loadbalancers
			WHERE tenant_id = ?
			ORDER BY created_at DESC
		`
		args = append(args, tenantID)
	} else {
		query = `
			SELECT id, tenant_id, vpc_id, name, description, type, public_ip, status, ssl_cert_secret, ssl_cert_type, created_at, updated_at
			FROM loadbalancers
			ORDER BY created_at DESC
		`
	}

	rows, err := r.db.sqlDB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing loadbalancers: %w", err)
	}
	defer rows.Close()

	var list []*protocol.LoadBalancer
	for rows.Next() {
		lb := &protocol.LoadBalancer{}
		if err := rows.Scan(
			&lb.ID, &lb.TenantID, &lb.VPCID, &lb.Name, &lb.Description, &lb.Type, &lb.PublicIP, &lb.Status, &lb.SSLCertSecret, &lb.SSLCertType, &lb.CreatedAt, &lb.UpdatedAt,
		); err != nil {
			return nil, err
		}
		listeners, _ := r.ListListenersByLB(ctx, lb.ID)
		lb.Listeners = listeners
		pools, _ := r.ListPoolsByLB(ctx, lb.ID)
		lb.Pools = pools
		list = append(list, lb)
	}
	return list, nil
}

func (r *Repository) UpdateLoadBalancer(ctx context.Context, lb *protocol.LoadBalancer) error {
	query := `
		UPDATE loadbalancers
		SET name = ?, description = ?, type = ?, public_ip = ?, status = ?, ssl_cert_secret = ?, ssl_cert_type = ?, updated_at = ?
		WHERE id = ?
	`
	certType := lb.SSLCertType
	if certType == "" {
		if lb.SSLCertSecret != "" {
			certType = "secret"
		} else {
			certType = "auto"
		}
	}
	_, err := r.db.sqlDB.ExecContext(ctx, query,
		lb.Name, lb.Description, lb.Type, lb.PublicIP, lb.Status, lb.SSLCertSecret, certType, time.Now(), lb.ID,
	)
	return err
}

func (r *Repository) DeleteLoadBalancer(ctx context.Context, id string) error {
	query := `DELETE FROM loadbalancers WHERE id = ?`
	_, err := r.db.sqlDB.ExecContext(ctx, query, id)
	return err
}

// -------------------------------------------------------------
// Listeners
// -------------------------------------------------------------

func (r *Repository) CreateListener(ctx context.Context, l *protocol.LBListener) error {
	query := `
		INSERT INTO lb_listeners (
			id, loadbalancer_id, name, port, protocol, ssl_cert_secret, ssl_cert_type, default_pool_id, http_redirect, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	httpRedir := 0
	if l.HTTPRedirect {
		httpRedir = 1
	}
	certType := l.SSLCertType
	if certType == "" {
		if l.SSLCertSecret != "" {
			certType = "secret"
		} else {
			certType = "auto"
		}
	}
	_, err := r.db.sqlDB.ExecContext(ctx, query,
		l.ID, l.LoadBalancerID, l.Name, l.Port, l.Protocol, l.SSLCertSecret, certType, l.DefaultPoolID, httpRedir, l.CreatedAt,
	)
	return err
}

func (r *Repository) GetListenerByID(ctx context.Context, id string) (*protocol.LBListener, error) {
	query := `
		SELECT id, loadbalancer_id, name, port, protocol, ssl_cert_secret, ssl_cert_type, default_pool_id, http_redirect, created_at
		FROM lb_listeners
		WHERE id = ?
	`
	row := r.db.sqlDB.QueryRowContext(ctx, query, id)
	l := &protocol.LBListener{}
	var httpRedir int
	if err := row.Scan(
		&l.ID, &l.LoadBalancerID, &l.Name, &l.Port, &l.Protocol, &l.SSLCertSecret, &l.SSLCertType, &l.DefaultPoolID, &httpRedir, &l.CreatedAt,
	); err != nil {
		return nil, err
	}
	l.HTTPRedirect = (httpRedir == 1)
	policies, _ := r.ListPoliciesByListener(ctx, l.ID)
	l.Policies = policies
	return l, nil
}

func (r *Repository) ListListenersByLB(ctx context.Context, lbID string) ([]*protocol.LBListener, error) {
	query := `
		SELECT id, loadbalancer_id, name, port, protocol, ssl_cert_secret, ssl_cert_type, default_pool_id, http_redirect, created_at
		FROM lb_listeners
		WHERE loadbalancer_id = ?
		ORDER BY port ASC
	`
	rows, err := r.db.sqlDB.QueryContext(ctx, query, lbID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*protocol.LBListener
	for rows.Next() {
		l := &protocol.LBListener{}
		var httpRedir int
		if err := rows.Scan(
			&l.ID, &l.LoadBalancerID, &l.Name, &l.Port, &l.Protocol, &l.SSLCertSecret, &l.SSLCertType, &l.DefaultPoolID, &httpRedir, &l.CreatedAt,
		); err != nil {
			return nil, err
		}
		l.HTTPRedirect = (httpRedir == 1)
		policies, _ := r.ListPoliciesByListener(ctx, l.ID)
		l.Policies = policies
		list = append(list, l)
	}
	return list, nil
}

func (r *Repository) UpdateListener(ctx context.Context, l *protocol.LBListener) error {
	query := `
		UPDATE lb_listeners
		SET name = ?, port = ?, protocol = ?, ssl_cert_secret = ?, ssl_cert_type = ?, default_pool_id = ?, http_redirect = ?
		WHERE id = ?
	`
	httpRedir := 0
	if l.HTTPRedirect {
		httpRedir = 1
	}
	certType := l.SSLCertType
	if certType == "" {
		if l.SSLCertSecret != "" {
			certType = "secret"
		} else {
			certType = "auto"
		}
	}
	_, err := r.db.sqlDB.ExecContext(ctx, query,
		l.Name, l.Port, l.Protocol, l.SSLCertSecret, certType, l.DefaultPoolID, httpRedir, l.ID,
	)
	return err
}

func (r *Repository) DeleteListener(ctx context.Context, id string) error {
	query := `DELETE FROM lb_listeners WHERE id = ?`
	_, err := r.db.sqlDB.ExecContext(ctx, query, id)
	return err
}

// -------------------------------------------------------------
// Pools
// -------------------------------------------------------------

func (r *Repository) CreatePool(ctx context.Context, p *protocol.LBPool) error {
	query := `
		INSERT INTO lb_pools (
			id, loadbalancer_id, name, protocol, algorithm, health_path,
			health_interval_sec, health_timeout_sec, health_unhealthy_threshold, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	if p.HealthIntervalSec <= 0 {
		p.HealthIntervalSec = 5
	}
	if p.HealthTimeoutSec <= 0 {
		p.HealthTimeoutSec = 2
	}
	if p.HealthUnhealthyThreshold <= 0 {
		p.HealthUnhealthyThreshold = 3
	}
	if p.HealthPath == "" {
		p.HealthPath = "/healthz"
	}
	_, err := r.db.sqlDB.ExecContext(ctx, query,
		p.ID, p.LoadBalancerID, p.Name, p.Protocol, p.Algorithm, p.HealthPath,
		p.HealthIntervalSec, p.HealthTimeoutSec, p.HealthUnhealthyThreshold, p.CreatedAt,
	)
	return err
}

func (r *Repository) GetPoolByID(ctx context.Context, id string) (*protocol.LBPool, error) {
	query := `
		SELECT id, loadbalancer_id, name, protocol, algorithm, health_path,
		       health_interval_sec, health_timeout_sec, health_unhealthy_threshold, created_at
		FROM lb_pools
		WHERE id = ?
	`
	row := r.db.sqlDB.QueryRowContext(ctx, query, id)
	p := &protocol.LBPool{}
	if err := row.Scan(
		&p.ID, &p.LoadBalancerID, &p.Name, &p.Protocol, &p.Algorithm, &p.HealthPath,
		&p.HealthIntervalSec, &p.HealthTimeoutSec, &p.HealthUnhealthyThreshold, &p.CreatedAt,
	); err != nil {
		return nil, err
	}
	members, _ := r.ListMembersByPool(ctx, p.ID)
	p.Members = members
	return p, nil
}

func (r *Repository) ListPoolsByLB(ctx context.Context, lbID string) ([]*protocol.LBPool, error) {
	query := `
		SELECT id, loadbalancer_id, name, protocol, algorithm, health_path,
		       health_interval_sec, health_timeout_sec, health_unhealthy_threshold, created_at
		FROM lb_pools
		WHERE loadbalancer_id = ?
		ORDER BY created_at ASC
	`
	rows, err := r.db.sqlDB.QueryContext(ctx, query, lbID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*protocol.LBPool
	for rows.Next() {
		p := &protocol.LBPool{}
		if err := rows.Scan(
			&p.ID, &p.LoadBalancerID, &p.Name, &p.Protocol, &p.Algorithm, &p.HealthPath,
			&p.HealthIntervalSec, &p.HealthTimeoutSec, &p.HealthUnhealthyThreshold, &p.CreatedAt,
		); err != nil {
			return nil, err
		}
		members, _ := r.ListMembersByPool(ctx, p.ID)
		p.Members = members
		list = append(list, p)
	}
	return list, nil
}

func (r *Repository) DeletePool(ctx context.Context, id string) error {
	query := `DELETE FROM lb_pools WHERE id = ?`
	_, err := r.db.sqlDB.ExecContext(ctx, query, id)
	return err
}

// -------------------------------------------------------------
// Members
// -------------------------------------------------------------

func (r *Repository) CreateMember(ctx context.Context, m *protocol.LBMember) error {
	query := `
		INSERT INTO lb_members (
			id, pool_id, target_type, target_ref, ip, port, weight, status, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	if m.Weight <= 0 {
		m.Weight = 1
	}
	if m.Status == "" {
		m.Status = "healthy"
	}
	_, err := r.db.sqlDB.ExecContext(ctx, query,
		m.ID, m.PoolID, m.TargetType, m.TargetRef, m.IP, m.Port, m.Weight, m.Status, m.CreatedAt,
	)
	return err
}

func (r *Repository) GetMemberByID(ctx context.Context, id string) (*protocol.LBMember, error) {
	query := `
		SELECT id, pool_id, target_type, target_ref, ip, port, weight, status, created_at
		FROM lb_members
		WHERE id = ?
	`
	row := r.db.sqlDB.QueryRowContext(ctx, query, id)
	m := &protocol.LBMember{}
	if err := row.Scan(
		&m.ID, &m.PoolID, &m.TargetType, &m.TargetRef, &m.IP, &m.Port, &m.Weight, &m.Status, &m.CreatedAt,
	); err != nil {
		return nil, err
	}
	return m, nil
}

func (r *Repository) ListMembersByPool(ctx context.Context, poolID string) ([]*protocol.LBMember, error) {
	query := `
		SELECT id, pool_id, target_type, target_ref, ip, port, weight, status, created_at
		FROM lb_members
		WHERE pool_id = ?
		ORDER BY created_at ASC
	`
	rows, err := r.db.sqlDB.QueryContext(ctx, query, poolID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*protocol.LBMember
	for rows.Next() {
		m := &protocol.LBMember{}
		if err := rows.Scan(
			&m.ID, &m.PoolID, &m.TargetType, &m.TargetRef, &m.IP, &m.Port, &m.Weight, &m.Status, &m.CreatedAt,
		); err != nil {
			return nil, err
		}
		list = append(list, m)
	}
	return list, nil
}

func (r *Repository) UpdateMemberStatus(ctx context.Context, id, status string) error {
	query := `UPDATE lb_members SET status = ? WHERE id = ?`
	_, err := r.db.sqlDB.ExecContext(ctx, query, status, id)
	return err
}

func (r *Repository) DeleteMember(ctx context.Context, id string) error {
	query := `DELETE FROM lb_members WHERE id = ?`
	_, err := r.db.sqlDB.ExecContext(ctx, query, id)
	return err
}

// -------------------------------------------------------------
// Policies
// -------------------------------------------------------------

func (r *Repository) CreatePolicy(ctx context.Context, p *protocol.LBL7Policy) error {
	query := `
		INSERT INTO lb_policies (
			id, listener_id, name, action, rule_type, rule_value, target_pool_id, priority, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	if p.Action == "" {
		p.Action = "FORWARD"
	}
	if p.RuleType == "" {
		p.RuleType = "PATH_PREFIX"
	}
	if p.Priority <= 0 {
		p.Priority = 1
	}
	_, err := r.db.sqlDB.ExecContext(ctx, query,
		p.ID, p.ListenerID, p.Name, p.Action, p.RuleType, p.RuleValue, p.TargetPoolID, p.Priority, p.CreatedAt,
	)
	return err
}

func (r *Repository) ListPoliciesByListener(ctx context.Context, listenerID string) ([]*protocol.LBL7Policy, error) {
	query := `
		SELECT id, listener_id, name, action, rule_type, rule_value, target_pool_id, priority, created_at
		FROM lb_policies
		WHERE listener_id = ?
		ORDER BY priority ASC, created_at ASC
	`
	rows, err := r.db.sqlDB.QueryContext(ctx, query, listenerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*protocol.LBL7Policy
	for rows.Next() {
		p := &protocol.LBL7Policy{}
		if err := rows.Scan(
			&p.ID, &p.ListenerID, &p.Name, &p.Action, &p.RuleType, &p.RuleValue, &p.TargetPoolID, &p.Priority, &p.CreatedAt,
		); err != nil {
			return nil, err
		}
		list = append(list, p)
	}
	return list, nil
}

func (r *Repository) DeletePolicy(ctx context.Context, id string) error {
	query := `DELETE FROM lb_policies WHERE id = ?`
	_, err := r.db.sqlDB.ExecContext(ctx, query, id)
	return err
}
