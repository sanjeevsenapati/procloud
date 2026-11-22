package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
	"procloud.io/procloud/pkg/podman"
	"procloud.io/procloud/pkg/protocol"
)

var wsTerminalUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow UI connections across hostnames/ports
	},
}

type wsTerminalResizePayload struct {
	Type string `json:"type"`
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

type safeWSWriter struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func (s *safeWSWriter) WriteBinary(data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn.WriteMessage(websocket.BinaryMessage, data)
}

func (s *safeWSWriter) WriteText(text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn.WriteMessage(websocket.TextMessage, []byte(text))
}

func (s *safeWSWriter) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn.Close()
}

func (s *Server) handleContainerTerminalWS(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "Container ID is required", http.StatusBadRequest)
		return
	}

	// 1. Verify authentication
	claims := s.checkAuth(r)
	if claims == nil {
		http.Error(w, "Unauthorized: valid authentication token required", http.StatusUnauthorized)
		return
	}

	// 2. Verify authorization
	if claims.Role == protocol.RoleViewer {
		http.Error(w, "Forbidden: viewer role cannot access interactive container terminal", http.StatusForbidden)
		return
	}

	// 3. Upgrade to WebSocket
	conn, err := wsTerminalUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[Terminal WS] Upgrade error for container %s: %v", id, err)
		return
	}
	ws := &safeWSWriter{conn: conn}
	defer ws.Close()

	// 4. Verify container status
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	status, err := s.runtime.InspectContainer(ctx, id)
	if err != nil {
		_ = ws.WriteText(fmt.Sprintf("\r\n\x1b[31;1mError inspecting container %s: %v\x1b[0m\r\n", id, err))
		return
	}

	containerName := status.Name
	if containerName == "" {
		containerName = id
	}

	if status.Status != "running" && !status.Running {
		_ = ws.WriteText(fmt.Sprintf("\r\n\x1b[31;1mContainer '%s' is not running (Current State: %s).\x1b[0m\r\n\x1b[33mStart the container first from the Overview tab to attach to an interactive terminal session.\x1b[0m\r\n", containerName, status.Status))
		return
	}

	// 5. Determine command to launch
	requestedShell := strings.TrimSpace(r.URL.Query().Get("shell"))
	if requestedShell == "" {
		requestedShell = "auto"
	}

	var cmd *exec.Cmd
	if _, isMock := s.runtime.(*podman.MockRuntime); isMock {
		// Mock container runtime mode
		cmd = exec.Command("/bin/sh")
	} else {
		// Real Podman runtime
		if requestedShell == "attach" {
			cmd = exec.Command("podman", "attach", id)
		} else if requestedShell == "auto" {
			// Auto fallback: try /bin/bash, then /bin/sh, then sh
			autoScript := "if [ -x /bin/bash ]; then exec /bin/bash -i; elif [ -x /bin/sh ]; then exec /bin/sh -i; else exec sh -i; fi"
			cmd = exec.Command("podman", "exec", "-it", id, "/bin/sh", "-c", autoScript)
		} else {
			cmd = exec.Command("podman", "exec", "-it", id, requestedShell)
		}
	}

	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	// 6. Start PTY session attached to container shell
	ptyFile, err := pty.Start(cmd)
	if err != nil {
		_ = ws.WriteText(fmt.Sprintf("\r\n\x1b[31;1mFailed to spawn PTY terminal session: %v\x1b[0m\r\n", err))
		return
	}

	// Set initial standard terminal window size
	_ = pty.Setsize(ptyFile, &pty.Winsize{Rows: 24, Cols: 80})

	s.auditSvc.Log(r.Context(), claims.Username, "CONTAINER_TERMINAL_ATTACH", "Container", id, r.RemoteAddr, fmt.Sprintf("Opened interactive PTY session for container %s (shell=%s)", containerName, requestedShell))

	doneCh := make(chan struct{})
	var closeOnce sync.Once
	cleanup := func() {
		closeOnce.Do(func() {
			close(doneCh)
			_ = ptyFile.Close()
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}
		})
	}
	defer cleanup()

	// 7. Goroutine: PTY -> WebSocket (container output to browser)
	go func() {
		defer cleanup()
		buf := make([]byte, 4096)
		for {
			select {
			case <-doneCh:
				return
			default:
				n, err := ptyFile.Read(buf)
				if n > 0 {
					if wErr := ws.WriteBinary(buf[:n]); wErr != nil {
						return
					}
				}
				if err != nil {
					return
				}
			}
		}
	}()

	// 8. Main Loop: WebSocket -> PTY (user keystrokes, input, and resize events to container)
	for {
		msgType, payload, err := conn.ReadMessage()
		if err != nil {
			break
		}

		// Handle resize JSON events
		if msgType == websocket.TextMessage && len(payload) > 0 && payload[0] == '{' {
			var resizeMsg wsTerminalResizePayload
			if jErr := json.Unmarshal(payload, &resizeMsg); jErr == nil && resizeMsg.Type == "resize" {
				if resizeMsg.Cols > 0 && resizeMsg.Rows > 0 {
					_ = pty.Setsize(ptyFile, &pty.Winsize{
						Rows: resizeMsg.Rows,
						Cols: resizeMsg.Cols,
					})
				}
				continue
			}
		}

		// Forward raw keystrokes / input directly to container PTY
		if len(payload) > 0 {
			if _, wErr := ptyFile.Write(payload); wErr != nil {
				break
			}
		}
	}
}
