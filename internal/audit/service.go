package audit

import (
	"context"
	"time"

	"procloud.io/procloud/internal/repository"
	"procloud.io/procloud/pkg/protocol"
)

type Service struct {
	repo repository.AuditRepository
}

func NewService(repo repository.AuditRepository) *Service {
	return &Service{repo: repo}
}

func (s *Service) Log(ctx context.Context, actor, action, resType, resID, ip, details string) {
	event := &protocol.AuditEvent{
		Actor:        actor,
		Action:       action,
		ResourceType: resType,
		ResourceID:   resID,
		IPAddress:    ip,
		Details:      details,
		CreatedAt:    time.Now(),
	}
	_ = s.repo.RecordEvent(ctx, event)
}

func (s *Service) ListRecent(ctx context.Context, limit int) ([]*protocol.AuditEvent, error) {
	return s.repo.ListRecentEvents(ctx, limit)
}
