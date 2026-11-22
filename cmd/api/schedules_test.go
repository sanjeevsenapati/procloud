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

func TestSchedulesAPIEndpoints(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_api_schedules.db")

	db, err := sqlite.Open(sqlite.DefaultConfig(dbPath))
	if err != nil {
		t.Fatalf("failed opening sqlite db: %v", err)
	}
	defer db.Close()

	repo := sqlite.NewRepository(db)
	mockRuntime := podman.NewMockRuntime()
	server := NewServer(db, repo, mockRuntime)

	ctx := t.Context()
	_ = repo.CreateTenant(ctx, &protocol.Tenant{
		ID:        "tnt-default",
		Name:      "Default Organization",
		Slug:      "default",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})

	// Login as admin
	loginBody, _ := json.Marshal(map[string]string{
		"username": "admin",
		"password": "admin123",
	})
	loginReq := httptest.NewRequest("POST", "/api/v1/auth/login", bytes.NewReader(loginBody))
	loginReq.Header.Set("Content-Type", "application/json")
	loginRec := httptest.NewRecorder()
	server.ServeHTTP(loginRec, loginReq)

	if loginRec.Code != http.StatusOK {
		t.Fatalf("login failed: %s", loginRec.Body.String())
	}
	var loginResp map[string]any
	_ = json.Unmarshal(loginRec.Body.Bytes(), &loginResp)
	adminToken := loginResp["token"].(string)

	authHeaders := func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+adminToken)
		r.Header.Set("Content-Type", "application/json")
	}

	// 1. Create Schedule
	createPayload, _ := json.Marshal(protocol.CreateScheduleRequest{
		Name:           "nightly-backup",
		Description:    "Nightly database export",
		CronExpression: "0 2 * * *",
		JobType:        "container",
		TargetSpec: protocol.ScheduleTargetSpec{
			Image:   "alpine:latest",
			Command: "echo Backup completed",
		},
		ConcurrencyPolicy: "Forbid",
		MaxRetries:        1,
		TimeoutSeconds:    300,
	})

	req := httptest.NewRequest("POST", "/api/v1/schedules", bytes.NewReader(createPayload))
	authHeaders(req)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d: %s", rec.Code, rec.Body.String())
	}

	var createdSched protocol.Schedule
	if err := json.Unmarshal(rec.Body.Bytes(), &createdSched); err != nil {
		t.Fatalf("failed decoding created schedule: %v", err)
	}
	if createdSched.ID == "" || createdSched.Name != "nightly-backup" {
		t.Fatalf("invalid schedule returned: %+v", createdSched)
	}
	if createdSched.NextRunAt.IsZero() {
		t.Fatalf("expected NextRunAt to be calculated on schedule creation")
	}

	// 2. List Schedules
	req = httptest.NewRequest("GET", "/api/v1/schedules", nil)
	authHeaders(req)
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}
	var listResp []protocol.Schedule
	if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("failed unmarshaling schedules list: %v", err)
	}
	if len(listResp) != 1 || listResp[0].ID != createdSched.ID {
		t.Fatalf("expected 1 schedule in list, got %d", len(listResp))
	}

	// 3. Get Schedule By ID
	req = httptest.NewRequest("GET", "/api/v1/schedules/"+createdSched.ID, nil)
	authHeaders(req)
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}

	// 4. Pause Schedule
	req = httptest.NewRequest("POST", "/api/v1/schedules/"+createdSched.ID+"/pause", nil)
	authHeaders(req)
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK pausing schedule, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify it's paused
	schedPaused, err := repo.GetScheduleByID(ctx, createdSched.ID)
	if err != nil || schedPaused.Status != "paused" {
		t.Fatalf("expected schedule to be paused (Status=paused), got %v", schedPaused.Status)
	}

	// 5. Resume Schedule
	req = httptest.NewRequest("POST", "/api/v1/schedules/"+createdSched.ID+"/resume", nil)
	authHeaders(req)
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK resuming schedule, got %d: %s", rec.Code, rec.Body.String())
	}
	schedResumed, err := repo.GetScheduleByID(ctx, createdSched.ID)
	if err != nil || schedResumed.Status != "active" {
		t.Fatalf("expected schedule to be active (Status=active), got %v", schedResumed.Status)
	}

	// 6. Trigger Run Now (Manual Execution)
	req = httptest.NewRequest("POST", "/api/v1/schedules/"+createdSched.ID+"/run", nil)
	authHeaders(req)
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted on run now, got %d: %s", rec.Code, rec.Body.String())
	}
	var runResp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &runResp)
	jobID, ok := runResp["job_id"].(string)
	if !ok || jobID == "" {
		t.Fatalf("expected valid job_id in run response, got: %+v", runResp)
	}

	// Verify job was created in SQLite with ScheduleID and TriggerType MANUAL
	job, err := repo.GetJobByID(ctx, jobID)
	if err != nil {
		t.Fatalf("failed fetching created job: %v", err)
	}
	if job.ScheduleID != createdSched.ID {
		t.Errorf("expected job ScheduleID to match %s, got %s", createdSched.ID, job.ScheduleID)
	}
	if job.TriggerType != "manual" && job.TriggerType != "MANUAL" {
		t.Errorf("expected TriggerType manual, got %s", job.TriggerType)
	}

	// Simulate worker completing the job with output
	logsOutput := "2026-11-22T13:00:00Z [INFO] Initializing backup container\n2026-11-22T13:00:01Z [INFO] Backup completed successfully (exit 0)"
	err = repo.CompleteJobWithOutput(ctx, jobID, logsOutput, 0)
	if err != nil {
		t.Fatalf("failed completing job with output: %v", err)
	}

	// 7. Get Job Logs endpoint
	req = httptest.NewRequest("GET", "/api/v1/jobs/"+jobID+"/logs", nil)
	authHeaders(req)
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK fetching job logs, got %d: %s", rec.Code, rec.Body.String())
	}
	var logsResp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &logsResp)
	if logsResp["logs"] != logsOutput {
		t.Fatalf("unexpected logs output: %v", logsResp["logs"])
	}
	if logsResp["exit_code"].(float64) != 0 {
		t.Fatalf("expected exit_code 0, got %v", logsResp["exit_code"])
	}

	// 8. Delete Schedule
	req = httptest.NewRequest("DELETE", "/api/v1/schedules/"+createdSched.ID, nil)
	authHeaders(req)
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK && rec.Code != http.StatusNoContent {
		t.Fatalf("expected 200 OK or 204 No Content deleting schedule, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify deletion
	_, err = repo.GetScheduleByID(ctx, createdSched.ID)
	if err == nil {
		t.Fatalf("expected schedule to be deleted from database")
	}
}
