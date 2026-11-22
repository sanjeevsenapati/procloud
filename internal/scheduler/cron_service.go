package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"procloud.io/procloud/internal/repository"
	"procloud.io/procloud/pkg/protocol"
)

var schedSeq uint64

func genSchedID(prefix string) string {
	seq := atomic.AddUint64(&schedSeq, 1)
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano(), seq)
}

type JobEnqueuer interface {
	Enqueue(ctx context.Context, jobType string, payload any, idempotencyKey string) (*protocol.Job, error)
}

type CronService struct {
	repo        repository.ScheduleRepository
	jobRepo     repository.JobRepository
	jobEnqueuer JobEnqueuer
	stopCh      chan struct{}
	wg          sync.WaitGroup
	mu          sync.Mutex
	running     bool
}

func NewCronService(repo repository.ScheduleRepository, jobRepo repository.JobRepository, enqueuer JobEnqueuer) *CronService {
	return &CronService{
		repo:        repo,
		jobRepo:     jobRepo,
		jobEnqueuer: enqueuer,
		stopCh:      make(chan struct{}),
	}
}

// Start launches the background scheduler evaluation loop.
func (cs *CronService) Start(ctx context.Context) error {
	cs.mu.Lock()
	if cs.running {
		cs.mu.Unlock()
		return nil
	}
	cs.running = true
	cs.stopCh = make(chan struct{})
	cs.mu.Unlock()

	cs.wg.Add(1)
	go cs.loop(ctx)
	log.Printf("[Cron Service] Scheduler evaluation engine active (polling every 15s)")
	return nil
}

// Stop cleanly terminates the scheduler ticker loop.
func (cs *CronService) Stop() {
	cs.mu.Lock()
	if !cs.running {
		cs.mu.Unlock()
		return
	}
	cs.running = false
	close(cs.stopCh)
	cs.mu.Unlock()
	cs.wg.Wait()
	log.Printf("[Cron Service] Scheduler evaluation engine stopped")
}

func (cs *CronService) loop(ctx context.Context) {
	defer cs.wg.Done()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	// Initial evaluation on boot
	cs.evaluateDueSchedules(ctx)

	for {
		select {
		case <-cs.stopCh:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			cs.evaluateDueSchedules(ctx)
		}
	}
}

func (cs *CronService) evaluateDueSchedules(ctx context.Context) {
	now := time.Now()
	due, err := cs.repo.ListDueSchedules(ctx, now)
	if err != nil {
		log.Printf("[Cron Service] Error querying due schedules: %v", err)
		return
	}

	for _, s := range due {
		parsed, err := ParseCron(s.CronExpression)
		if err != nil {
			log.Printf("[Cron Service] Invalid cron expression for schedule %s: %v", s.ID, err)
			continue
		}

		// Check concurrency policy
		if s.ConcurrencyPolicy == "Forbid" {
			running, err := cs.jobRepo.IsScheduleRunning(ctx, s.ID)
			if err == nil && running {
				log.Printf("[Cron Service] Schedule %s (%s) is already running, skipping trigger due to 'Forbid' policy", s.ID, s.Name)
				next := parsed.Next(now)
				_ = cs.repo.RecordScheduleRun(ctx, s.ID, now, next, "SKIPPED")
				continue
			}
		}

		// Enqueue Job Run
		jobPayload := map[string]any{
			"schedule_id":        s.ID,
			"schedule_name":      s.Name,
			"tenant_id":          s.TenantID,
			"job_type":           s.JobType,
			"target_spec_json":   s.TargetSpecJSON,
			"timeout_seconds":    s.TimeoutSeconds,
			"trigger_type":       "cron",
		}

		var job *protocol.Job
		jobType := "container_run"
		if s.JobType == "workflow" {
			jobType = "workflow_run"
		} else if s.JobType == "http" {
			jobType = "http_webhook"
		}

		if cs.jobEnqueuer != nil {
			job, err = cs.jobEnqueuer.Enqueue(ctx, jobType, jobPayload, fmt.Sprintf("cron-%s-%d", s.ID, now.Unix()/60))
		} else {
			// Direct DB enqueue fallback
			payloadBytes, _ := json.Marshal(jobPayload)
			job = &protocol.Job{
				ID:             fmt.Sprintf("job-%d-%d", now.UnixNano(), atomic.AddUint64(&schedSeq, 1)),
				Type:           jobType,
				Status:         "QUEUED",
				PayloadJSON:    string(payloadBytes),
				ScheduleID:     s.ID,
				TriggerType:    "cron",
				MaxRetries:     s.MaxRetries,
				ScheduledAt:    now,
				CreatedAt:      now,
			}
			err = cs.jobRepo.EnqueueJob(ctx, job)
		}

		if err != nil {
			log.Printf("[Cron Service] Failed enqueuing job for schedule %s: %v", s.ID, err)
			continue
		}

		next := parsed.Next(now)
		_ = cs.repo.RecordScheduleRun(ctx, s.ID, now, next, "RUNNING")
		log.Printf("[Cron Service] Triggered schedule '%s' (%s) -> Job %s (Next: %s)", s.Name, s.ID, job.ID, next.Format("2006-01-02 15:04:05"))
	}
}

// -------------------------------------------------------------
// CRUD Operations
// -------------------------------------------------------------

func (cs *CronService) CreateSchedule(ctx context.Context, req protocol.CreateScheduleRequest) (*protocol.Schedule, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("schedule name is required")
	}
	parsed, err := ParseCron(req.CronExpression)
	if err != nil {
		return nil, fmt.Errorf("invalid cron expression: %w", err)
	}

	if req.JobType == "" {
		req.JobType = "container"
	}
	if req.ConcurrencyPolicy == "" {
		req.ConcurrencyPolicy = "Forbid"
	}
	if req.MaxRetries <= 0 {
		req.MaxRetries = 3
	}
	if req.TimeoutSeconds <= 0 {
		req.TimeoutSeconds = 1800
	}

	targetJSON, err := json.Marshal(req.TargetSpec)
	if err != nil {
		return nil, fmt.Errorf("failed encoding target specification: %w", err)
	}

	now := time.Now()
	next := parsed.Next(now)

	s := &protocol.Schedule{
		ID:                genSchedID("sched"),
		TenantID:          req.TenantID,
		Name:              req.Name,
		Description:       req.Description,
		CronExpression:    req.CronExpression,
		HumanSchedule:     HumanDescription(req.CronExpression),
		JobType:           req.JobType,
		TargetSpecJSON:    string(targetJSON),
		TargetSpec:        &req.TargetSpec,
		ConcurrencyPolicy: req.ConcurrencyPolicy,
		MaxRetries:        req.MaxRetries,
		TimeoutSeconds:    req.TimeoutSeconds,
		Status:            "active",
		NextRunAt:         next,
		CreatedAt:         now,
		UpdatedAt:         now,
	}

	if err := cs.repo.CreateSchedule(ctx, s); err != nil {
		return nil, fmt.Errorf("failed to save schedule: %w", err)
	}

	return s, nil
}

func (cs *CronService) GetSchedule(ctx context.Context, id string) (*protocol.Schedule, error) {
	s, err := cs.repo.GetScheduleByID(ctx, id)
	if err != nil {
		return nil, err
	}
	s.HumanSchedule = HumanDescription(s.CronExpression)
	if s.TargetSpecJSON != "" {
		var spec protocol.ScheduleTargetSpec
		if err := json.Unmarshal([]byte(s.TargetSpecJSON), &spec); err == nil {
			s.TargetSpec = &spec
		}
	}
	return s, nil
}

func (cs *CronService) ListSchedules(ctx context.Context, tenantID string) ([]*protocol.Schedule, error) {
	list, err := cs.repo.ListSchedules(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	for _, s := range list {
		s.HumanSchedule = HumanDescription(s.CronExpression)
		if s.TargetSpecJSON != "" {
			var spec protocol.ScheduleTargetSpec
			if err := json.Unmarshal([]byte(s.TargetSpecJSON), &spec); err == nil {
				s.TargetSpec = &spec
			}
		}
	}
	return list, nil
}

func (cs *CronService) UpdateSchedule(ctx context.Context, id string, req protocol.UpdateScheduleRequest) (*protocol.Schedule, error) {
	s, err := cs.GetSchedule(ctx, id)
	if err != nil {
		return nil, err
	}

	if req.Name != "" {
		s.Name = req.Name
	}
	if req.Description != "" {
		s.Description = req.Description
	}
	if req.CronExpression != "" {
		parsed, err := ParseCron(req.CronExpression)
		if err != nil {
			return nil, fmt.Errorf("invalid cron expression: %w", err)
		}
		s.CronExpression = req.CronExpression
		s.HumanSchedule = HumanDescription(req.CronExpression)
		s.NextRunAt = parsed.Next(time.Now())
	}
	if req.JobType != "" {
		s.JobType = req.JobType
	}
	if req.TargetSpec != nil {
		targetJSON, err := json.Marshal(req.TargetSpec)
		if err != nil {
			return nil, fmt.Errorf("failed encoding target specification: %w", err)
		}
		s.TargetSpecJSON = string(targetJSON)
		s.TargetSpec = req.TargetSpec
	}
	if req.ConcurrencyPolicy != "" {
		s.ConcurrencyPolicy = req.ConcurrencyPolicy
	}
	if req.MaxRetries != nil {
		s.MaxRetries = *req.MaxRetries
	}
	if req.TimeoutSeconds != nil {
		s.TimeoutSeconds = *req.TimeoutSeconds
	}

	if err := cs.repo.UpdateSchedule(ctx, s); err != nil {
		return nil, err
	}
	return s, nil
}

func (cs *CronService) PauseSchedule(ctx context.Context, id string) error {
	return cs.repo.UpdateScheduleStatus(ctx, id, "paused")
}

func (cs *CronService) ResumeSchedule(ctx context.Context, id string) error {
	s, err := cs.GetSchedule(ctx, id)
	if err != nil {
		return err
	}
	parsed, err := ParseCron(s.CronExpression)
	if err == nil {
		next := parsed.Next(time.Now())
		s.NextRunAt = next
		_ = cs.repo.UpdateSchedule(ctx, s)
	}
	return cs.repo.UpdateScheduleStatus(ctx, id, "active")
}

func (cs *CronService) DeleteSchedule(ctx context.Context, id string) error {
	return cs.repo.DeleteSchedule(ctx, id)
}

// TriggerManualRun forces an immediate execution of a scheduled job ("Run Now").
func (cs *CronService) TriggerManualRun(ctx context.Context, id string) (*protocol.Job, error) {
	s, err := cs.GetSchedule(ctx, id)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	jobPayload := map[string]any{
		"schedule_id":        s.ID,
		"schedule_name":      s.Name,
		"tenant_id":          s.TenantID,
		"job_type":           s.JobType,
		"target_spec_json":   s.TargetSpecJSON,
		"timeout_seconds":    s.TimeoutSeconds,
		"trigger_type":       "manual",
	}

	jobType := "container_run"
	if s.JobType == "workflow" {
		jobType = "workflow_run"
	} else if s.JobType == "http" {
		jobType = "http_webhook"
	}

	var job *protocol.Job
	if cs.jobEnqueuer != nil {
		job, err = cs.jobEnqueuer.Enqueue(ctx, jobType, jobPayload, fmt.Sprintf("manual-%s-%d", s.ID, now.UnixNano()))
	} else {
		payloadBytes, _ := json.Marshal(jobPayload)
		job = &protocol.Job{
			ID:             fmt.Sprintf("job-%d-%d", now.UnixNano(), atomic.AddUint64(&schedSeq, 1)),
			Type:           jobType,
			Status:         "QUEUED",
			PayloadJSON:    string(payloadBytes),
			ScheduleID:     s.ID,
			TriggerType:    "manual",
			MaxRetries:     s.MaxRetries,
			ScheduledAt:    now,
			CreatedAt:      now,
		}
		err = cs.jobRepo.EnqueueJob(ctx, job)
	}

	if err != nil {
		return nil, fmt.Errorf("failed enqueuing manual run: %w", err)
	}

	_ = cs.repo.RecordScheduleRun(ctx, s.ID, now, s.NextRunAt, "RUNNING")
	return job, nil
}

func (cs *CronService) ListScheduleRuns(ctx context.Context, scheduleID string, limit int) ([]*protocol.Job, error) {
	return cs.jobRepo.ListJobsBySchedule(ctx, scheduleID, limit)
}
