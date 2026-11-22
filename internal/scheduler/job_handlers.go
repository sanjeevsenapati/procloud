package scheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"procloud.io/procloud/pkg/protocol"
)

// TargetSpecPayload matches payload structure stored in jobs.
type TargetSpecPayload struct {
	ScheduleID     string `json:"schedule_id"`
	ScheduleName   string `json:"schedule_name"`
	TenantID       string `json:"tenant_id"`
	JobType        string `json:"job_type"`
	TargetSpecJSON string `json:"target_spec_json"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	TriggerType    string `json:"trigger_type"`
}

// parseCommandLine splits a command string into arguments while respecting quotes.
func parseCommandLine(cmdStr string) []string {
	var parts []string
	var current strings.Builder
	inQuote := false
	quoteChar := byte(0)

	for i := 0; i < len(cmdStr); i++ {
		b := cmdStr[i]
		if (b == '"' || b == '\'') && !inQuote {
			inQuote = true
			quoteChar = b
		} else if inQuote && b == quoteChar {
			inQuote = false
			quoteChar = 0
		} else if !inQuote && (b == ' ' || b == '\t') {
			if current.Len() > 0 {
				parts = append(parts, current.String())
				current.Reset()
			}
		} else {
			current.WriteByte(b)
		}
	}
	if current.Len() > 0 {
		parts = append(parts, current.String())
	}
	return parts
}

// ContainerJobHandler runs an ephemeral container and captures logs and exit code.
func ContainerJobHandler(ctx context.Context, job *protocol.Job, payload []byte) (string, int, error) {
	var p TargetSpecPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return "", 1, fmt.Errorf("invalid job payload: %w", err)
	}

	var spec protocol.ScheduleTargetSpec
	if p.TargetSpecJSON != "" {
		_ = json.Unmarshal([]byte(p.TargetSpecJSON), &spec)
	}

	image := strings.TrimSpace(spec.Image)
	if image == "" {
		return "Error: no container image specified for scheduled job", 1, fmt.Errorf("container image is required")
	}

	// Prepare podman run arguments
	containerName := fmt.Sprintf("sched-%s-%d", job.ID, time.Now().Unix()%10000)
	args := []string{"run", "--name", containerName, "--rm"}

	// Environment variables
	for k, v := range spec.Env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}

	// Volumes
	for _, v := range spec.Volumes {
		args = append(args, "-v", v)
	}

	// Resource limits if specified
	if spec.CPURequest != "" {
		args = append(args, "--cpus", spec.CPURequest)
	}
	if spec.MemRequest != "" {
		args = append(args, "--memory", spec.MemRequest)
	}

	args = append(args, image)

	if spec.Command != "" {
		cmdParts := parseCommandLine(spec.Command)
		args = append(args, cmdParts...)
	}
	if len(spec.Args) > 0 {
		args = append(args, spec.Args...)
	}

	timeout := 1800 * time.Second
	if p.TimeoutSeconds > 0 {
		timeout = time.Duration(p.TimeoutSeconds) * time.Second
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(execCtx, "podman", args...)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output

	startTime := time.Now()
	header := fmt.Sprintf("=== ProCloud Scheduled Container Execution ===\nJob ID:    %s\nSchedule:  %s (%s)\nImage:     %s\nStarted:   %s\nCommand:   podman %s\n==============================================\n\n",
		job.ID, p.ScheduleName, p.ScheduleID, image, startTime.Format(time.RFC3339), strings.Join(args, " "))

	err := cmd.Run()
	duration := time.Since(startTime)
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
	}

	footer := fmt.Sprintf("\n\n==============================================\nCompleted: %s (Duration: %v)\nExit Code: %d\nStatus:    %s\n==============================================",
		time.Now().Format(time.RFC3339), duration.Round(time.Millisecond), exitCode, func() string {
			if exitCode == 0 {
				return "SUCCESS"
			}
			return "FAILED"
		}())

	combinedLogs := header + output.String() + footer
	return combinedLogs, exitCode, err
}

// HTTPWebhookJobHandler dispatches an outbound HTTP request and captures the response.
func HTTPWebhookJobHandler(ctx context.Context, job *protocol.Job, payload []byte) (string, int, error) {
	var p TargetSpecPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return "", 1, fmt.Errorf("invalid job payload: %w", err)
	}

	var spec protocol.ScheduleTargetSpec
	if p.TargetSpecJSON != "" {
		_ = json.Unmarshal([]byte(p.TargetSpecJSON), &spec)
	}

	urlStr := strings.TrimSpace(spec.HTTPURL)
	if urlStr == "" {
		return "Error: no HTTP URL specified", 1, fmt.Errorf("http_url is required")
	}

	method := strings.ToUpper(strings.TrimSpace(spec.HTTPMethod))
	if method == "" {
		method = "GET"
	}

	var bodyReader io.Reader
	if spec.HTTPBody != "" && (method == "POST" || method == "PUT" || method == "PATCH") {
		bodyReader = strings.NewReader(spec.HTTPBody)
	}

	req, err := http.NewRequestWithContext(ctx, method, urlStr, bodyReader)
	if err != nil {
		return fmt.Sprintf("Error creating HTTP request: %v", err), 1, err
	}

	for k, v := range spec.HTTPHeaders {
		req.Header.Set(k, v)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	startTime := time.Now()
	resp, err := client.Do(req)
	duration := time.Since(startTime)

	if err != nil {
		logs := fmt.Sprintf("=== HTTP Webhook Execution FAILED ===\nJob ID:   %s\nURL:      %s %s\nError:    %v\nDuration: %v\n",
			job.ID, method, urlStr, err, duration.Round(time.Millisecond))
		return logs, 1, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	exitCode := 0
	if resp.StatusCode >= 400 {
		exitCode = 1
	}

	logs := fmt.Sprintf("=== HTTP Webhook Execution ===\nJob ID:      %s\nURL:         %s %s\nStatus:      %s (Code: %d)\nDuration:    %v\nResponse:\n%s\n",
		job.ID, method, urlStr, resp.Status, resp.StatusCode, duration.Round(time.Millisecond), string(respBody))

	var runErr error
	if exitCode != 0 {
		runErr = fmt.Errorf("HTTP request returned status %s", resp.Status)
	}
	return logs, exitCode, runErr
}
