package job

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"procloud.io/procloud/internal/repository"
	"procloud.io/procloud/pkg/protocol"
)

// HandlerFunc processes a job given its payload.
type HandlerFunc func(ctx context.Context, payload []byte) error

type Engine struct {
	repo     repository.JobRepository
	workerID string
	handlers map[string]HandlerFunc
	stopCh   chan struct{}
	wg       sync.WaitGroup
	mu       sync.RWMutex
}

func NewEngine(repo repository.JobRepository, workerID string) *Engine {
	if workerID == "" {
		workerID = fmt.Sprintf("worker-%d", time.Now().UnixNano())
	}
	return &Engine{
		repo:     repo,
		workerID: workerID,
		handlers: make(map[string]HandlerFunc),
		stopCh:   make(chan struct{}),
	}
}

// RegisterHandler binds a job type to an execution handler.
func (e *Engine) RegisterHandler(jobType string, fn HandlerFunc) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.handlers[jobType] = fn
}

// Enqueue submits a job for execution.
func (e *Engine) Enqueue(ctx context.Context, jobType string, payload any, idempotencyKey string) (*protocol.Job, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to encode job payload: %w", err)
	}

	job := &protocol.Job{
		ID:             fmt.Sprintf("job-%d", time.Now().UnixNano()),
		Type:           jobType,
		Status:         "QUEUED",
		PayloadJSON:    string(data),
		IdempotencyKey: idempotencyKey,
		RetryCount:     0,
		MaxRetries:     3,
		ScheduledAt:    time.Now(),
		CreatedAt:      time.Now(),
	}

	if err := e.repo.EnqueueJob(ctx, job); err != nil {
		return nil, fmt.Errorf("failed to enqueue job: %w", err)
	}

	return job, nil
}

// Start launches the worker loop and reclaims interrupted jobs.
func (e *Engine) Start(ctx context.Context) error {
	// Recover interrupted jobs from previous crashes/restarts per specification
	recovered, err := e.repo.RecoverInterruptedJobs(ctx)
	if err == nil && recovered > 0 {
		log.Printf("[Job Engine] Recovered %d interrupted jobs after restart", recovered)
	}

	e.wg.Add(1)
	go e.loop(ctx)
	return nil
}

// Stop cleanly terminates workers.
func (e *Engine) Stop() {
	close(e.stopCh)
	e.wg.Wait()
}

func (e *Engine) loop(ctx context.Context) {
	defer e.wg.Done()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-e.stopCh:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.processNext(ctx)
		}
	}
}

func (e *Engine) processNext(ctx context.Context) {
	// Claim next job in a short-lived transaction
	job, err := e.repo.ClaimNextJob(ctx, e.workerID)
	if err != nil || job == nil {
		return
	}

	e.mu.RLock()
	handler, ok := e.handlers[job.Type]
	e.mu.RUnlock()

	if !ok {
		_ = e.repo.FailJob(ctx, job.ID, fmt.Sprintf("unregistered handler for job type %s", job.Type), nil)
		return
	}

	// NOTE: Database transaction is NOT held open while handler executes Podman or build operations!
	jobCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	err = handler(jobCtx, []byte(job.PayloadJSON))
	if err != nil {
		if job.RetryCount < job.MaxRetries {
			backoff := time.Duration(1<<job.RetryCount) * 5 * time.Second
			_ = e.repo.FailJob(ctx, job.ID, err.Error(), &backoff)
		} else {
			_ = e.repo.FailJob(ctx, job.ID, fmt.Sprintf("max retries exceeded: %v", err), nil)
		}
	} else {
		_ = e.repo.CompleteJob(ctx, job.ID)
	}
}

func (e *Engine) ListRecentJobs(ctx context.Context, limit int) ([]*protocol.Job, error) {
	return e.repo.ListRecentJobs(ctx, limit)
}
