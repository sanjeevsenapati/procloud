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
// DNS Hosted Zone Operations
// -------------------------------------------------------------

func (r *Repository) CreateZone(ctx context.Context, zone *protocol.DNSZone) error {
	query := `
		INSERT INTO dns_zones (
			id, tenant_id, vpc_id, name, type, description, status, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err := r.db.sqlDB.ExecContext(ctx, query,
		zone.ID, zone.TenantID, zone.VPCID, zone.Name, zone.Type, zone.Description, zone.Status, zone.CreatedAt, zone.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed inserting dns_zone: %w", err)
	}
	return nil
}

func (r *Repository) GetZoneByID(ctx context.Context, id string) (*protocol.DNSZone, error) {
	query := `
		SELECT id, tenant_id, vpc_id, name, type, description, status, created_at, updated_at
		FROM dns_zones
		WHERE id = ?
	`
	row := r.db.sqlDB.QueryRowContext(ctx, query, id)
	z := &protocol.DNSZone{}
	err := row.Scan(
		&z.ID, &z.TenantID, &z.VPCID, &z.Name, &z.Type, &z.Description, &z.Status, &z.CreatedAt, &z.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("zone %s not found", id)
		}
		return nil, fmt.Errorf("querying zone: %w", err)
	}

	// Fetch records
	records, err := r.ListRecordsByZone(ctx, z.ID)
	if err == nil {
		z.Records = records
		z.RecordCount = len(records)
	}
	return z, nil
}

func (r *Repository) GetZoneByName(ctx context.Context, tenantID, name string) (*protocol.DNSZone, error) {
	query := `
		SELECT id, tenant_id, vpc_id, name, type, description, status, created_at, updated_at
		FROM dns_zones
		WHERE name = ? AND (tenant_id = ? OR ? = '')
		LIMIT 1
	`
	row := r.db.sqlDB.QueryRowContext(ctx, query, name, tenantID, tenantID)
	z := &protocol.DNSZone{}
	err := row.Scan(
		&z.ID, &z.TenantID, &z.VPCID, &z.Name, &z.Type, &z.Description, &z.Status, &z.CreatedAt, &z.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("zone %s not found", name)
		}
		return nil, fmt.Errorf("querying zone by name: %w", err)
	}

	records, err := r.ListRecordsByZone(ctx, z.ID)
	if err == nil {
		z.Records = records
		z.RecordCount = len(records)
	}
	return z, nil
}

func (r *Repository) ListZones(ctx context.Context, tenantID string) ([]*protocol.DNSZone, error) {
	query := `
		SELECT z.id, z.tenant_id, z.vpc_id, z.name, z.type, z.description, z.status, z.created_at, z.updated_at,
		       (SELECT COUNT(1) FROM dns_records r WHERE r.zone_id = z.id) as record_count
		FROM dns_zones z
		WHERE (? = '' OR z.tenant_id = ?)
		ORDER BY z.created_at DESC
	`
	rows, err := r.db.sqlDB.QueryContext(ctx, query, tenantID, tenantID)
	if err != nil {
		return nil, fmt.Errorf("querying zones: %w", err)
	}
	defer rows.Close()

	var zones []*protocol.DNSZone
	for rows.Next() {
		z := &protocol.DNSZone{}
		if err := rows.Scan(
			&z.ID, &z.TenantID, &z.VPCID, &z.Name, &z.Type, &z.Description, &z.Status, &z.CreatedAt, &z.UpdatedAt, &z.RecordCount,
		); err != nil {
			return nil, fmt.Errorf("scanning zone: %w", err)
		}
		zones = append(zones, z)
	}
	return zones, nil
}

func (r *Repository) UpdateZone(ctx context.Context, zone *protocol.DNSZone) error {
	query := `
		UPDATE dns_zones
		SET vpc_id = ?, description = ?, status = ?, updated_at = ?
		WHERE id = ?
	`
	zone.UpdatedAt = time.Now()
	_, err := r.db.sqlDB.ExecContext(ctx, query,
		zone.VPCID, zone.Description, zone.Status, zone.UpdatedAt, zone.ID,
	)
	return err
}

func (r *Repository) DeleteZone(ctx context.Context, id string) error {
	query := `DELETE FROM dns_zones WHERE id = ?`
	_, err := r.db.sqlDB.ExecContext(ctx, query, id)
	return err
}

// -------------------------------------------------------------
// DNS Resource Record Operations
// -------------------------------------------------------------

func (r *Repository) CreateRecord(ctx context.Context, rec *protocol.DNSRecord) error {
	query := `
		INSERT INTO dns_records (
			id, zone_id, name, type, value, ttl, routing_policy, status, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err := r.db.sqlDB.ExecContext(ctx, query,
		rec.ID, rec.ZoneID, rec.Name, rec.Type, rec.Value, rec.TTL, rec.RoutingPolicy, rec.Status, rec.CreatedAt, rec.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed inserting dns_record: %w", err)
	}
	return nil
}

func (r *Repository) GetRecordByID(ctx context.Context, id string) (*protocol.DNSRecord, error) {
	query := `
		SELECT id, zone_id, name, type, value, ttl, routing_policy, status, created_at, updated_at
		FROM dns_records
		WHERE id = ?
	`
	row := r.db.sqlDB.QueryRowContext(ctx, query, id)
	rec := &protocol.DNSRecord{}
	err := row.Scan(
		&rec.ID, &rec.ZoneID, &rec.Name, &rec.Type, &rec.Value, &rec.TTL, &rec.RoutingPolicy, &rec.Status, &rec.CreatedAt, &rec.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("record %s not found", id)
		}
		return nil, fmt.Errorf("querying record: %w", err)
	}
	return rec, nil
}

func (r *Repository) ListRecordsByZone(ctx context.Context, zoneID string) ([]*protocol.DNSRecord, error) {
	query := `
		SELECT id, zone_id, name, type, value, ttl, routing_policy, status, created_at, updated_at
		FROM dns_records
		WHERE zone_id = ?
		ORDER BY name ASC, type ASC
	`
	rows, err := r.db.sqlDB.QueryContext(ctx, query, zoneID)
	if err != nil {
		return nil, fmt.Errorf("querying records: %w", err)
	}
	defer rows.Close()

	var records []*protocol.DNSRecord
	for rows.Next() {
		rec := &protocol.DNSRecord{}
		if err := rows.Scan(
			&rec.ID, &rec.ZoneID, &rec.Name, &rec.Type, &rec.Value, &rec.TTL, &rec.RoutingPolicy, &rec.Status, &rec.CreatedAt, &rec.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning record: %w", err)
		}
		records = append(records, rec)
	}
	return records, nil
}

func (r *Repository) UpdateRecord(ctx context.Context, rec *protocol.DNSRecord) error {
	query := `
		UPDATE dns_records
		SET name = ?, type = ?, value = ?, ttl = ?, routing_policy = ?, status = ?, updated_at = ?
		WHERE id = ?
	`
	rec.UpdatedAt = time.Now()
	_, err := r.db.sqlDB.ExecContext(ctx, query,
		rec.Name, rec.Type, rec.Value, rec.TTL, rec.RoutingPolicy, rec.Status, rec.UpdatedAt, rec.ID,
	)
	return err
}

func (r *Repository) DeleteRecord(ctx context.Context, id string) error {
	query := `DELETE FROM dns_records WHERE id = ?`
	_, err := r.db.sqlDB.ExecContext(ctx, query, id)
	return err
}

func (r *Repository) LookupRecords(ctx context.Context, zoneID, name, recordType string) ([]*protocol.DNSRecord, error) {
	query := `
		SELECT id, zone_id, name, type, value, ttl, routing_policy, status, created_at, updated_at
		FROM dns_records
		WHERE zone_id = ? AND (name = ? OR name = '@' OR name = '*') AND (type = ? OR ? = '' OR type = 'CNAME')
		ORDER BY type ASC
	`
	rows, err := r.db.sqlDB.QueryContext(ctx, query, zoneID, name, recordType, recordType)
	if err != nil {
		return nil, fmt.Errorf("lookup records: %w", err)
	}
	defer rows.Close()

	var records []*protocol.DNSRecord
	for rows.Next() {
		rec := &protocol.DNSRecord{}
		if err := rows.Scan(
			&rec.ID, &rec.ZoneID, &rec.Name, &rec.Type, &rec.Value, &rec.TTL, &rec.RoutingPolicy, &rec.Status, &rec.CreatedAt, &rec.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning record: %w", err)
		}
		records = append(records, rec)
	}
	return records, nil
}
