package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kid0317/cc-workspace-bot/internal/config"
)

// ExecuteRequest holds all parameters for a claude CLI invocation.
type ExecuteRequest struct {
	Prompt          string
	SessionID       string
	ClaudeSessionID string // empty = new context (no --resume)
	AppConfig       *config.AppConfig
	WorkspaceDir    string
}

// ExecuteResult holds the output of a claude CLI invocation.
type ExecuteResult struct {
	Text            string
	ClaudeSessionID string // extracted from stream-json system event
	CostUSD         float64
	DurationMS      int64
}

// Executor runs the claude CLI as a subprocess.
type Executor struct {
	cfg *config.Config
}

// New creates a new Executor.
func New(cfg *config.Config) *Executor {
	return &Executor{cfg: cfg}
}

// scannerMaxBytes is the per-line buffer limit for reading claude output.
// 1 MiB is well above any realistic single NDJSON line.
const scannerMaxBytes = 1 << 20 // 1 MiB

// Execute runs claude CLI and returns the final assistant text.
func (e *Executor) Execute(ctx context.Context, req *ExecuteRequest) (*ExecuteResult, error) {
	sessionDir := filepath.Join(req.WorkspaceDir, "sessions", req.SessionID)
	attachmentsDir := filepath.Join(sessionDir, "attachments")

	if err := os.MkdirAll(attachmentsDir, 0o755); err != nil {
		return nil, fmt.Errorf("create session dir: %w", err)
	}
	if err := writeSessionContext(sessionDir, req); err != nil {
		return nil, fmt.Errorf("write session context: %w", err)
	}

	args := e.buildArgs(req, sessionDir)

	timeout := time.Duration(e.cfg.Claude.TimeoutMinutes) * time.Minute
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = sessionDir
	cmd.WaitDelay = 30 * time.Second
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"FORCE_COLOR=0",
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start claude: %w", err)
	}

	// H-5: drain stderr in a goroutine and wait for it to finish.
	var stderrWg sync.WaitGroup
	stderrWg.Add(1)
	go func() {
		defer stderrWg.Done()
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			slog.Debug("claude stderr", "line", sc.Text())
		}
	}()

	// C-4: set an explicit buffer to handle responses > 64 KiB.
	result := &ExecuteResult{}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, scannerMaxBytes), scannerMaxBytes)
	for scanner.Scan() {
		line := scanner.Text()
		if line != "" {
			e.parseLine(line, result)
		}
	}
	if err := scanner.Err(); err != nil {
		slog.Error("claude stdout scanner", "err", err)
	}

	if err := cmd.Wait(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("claude timed out after %d minutes", e.cfg.Claude.TimeoutMinutes)
		}
		slog.Warn("claude exited with error", "err", err)
	}

	// Join the stderr goroutine after Wait() so it has had a chance to drain.
	stderrWg.Wait()

	return result, nil
}

// buildArgs constructs the claude CLI argument list.
func (e *Executor) buildArgs(req *ExecuteRequest, sessionDir string) []string {
	args := []string{
		"-p", req.Prompt,
		"--cwd", sessionDir,
		"--output-format", "stream-json",
		"--permission-mode", permissionMode(req.AppConfig),
		"--max-turns", fmt.Sprintf("%d", e.cfg.Claude.MaxTurns),
	}

	if req.ClaudeSessionID != "" {
		args = append(args, "--resume", req.ClaudeSessionID)
	}

	if tools := req.AppConfig.Claude.AllowedTools; len(tools) > 0 {
		args = append(args, "--allowedTools", strings.Join(tools, " "))
	}

	return args
}

// streamEvent is a single line from claude --output-format stream-json.
type streamEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`

	// system event
	SessionID string `json:"session_id"`

	// assistant event
	Message *assistantMessage `json:"message"`

	// result event
	CostUSD    float64 `json:"cost_usd"`
	DurationMS int64   `json:"duration_ms"`
}

type assistantMessage struct {
	Role    string           `json:"role"`
	Content []messageContent `json:"content"`
}

type messageContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// parseLine extracts useful fields from one NDJSON line.
func (e *Executor) parseLine(line string, result *ExecuteResult) {
	var event streamEvent
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		slog.Debug("claude: skip non-JSON line", "line", line)
		return
	}

	switch event.Type {
	case "system":
		if event.SessionID != "" && result.ClaudeSessionID == "" {
			result.ClaudeSessionID = event.SessionID
		}

	case "assistant":
		if event.Message != nil {
			// M-1: use strings.Builder to avoid O(n²) string concatenation.
			var sb strings.Builder
			sb.WriteString(result.Text)
			for _, c := range event.Message.Content {
				if c.Type == "text" {
					sb.WriteString(c.Text)
				}
			}
			result.Text = sb.String()
		}

	case "result":
		result.CostUSD = event.CostUSD
		result.DurationMS = event.DurationMS
	}
}

// writeSessionContext writes SESSION_CONTEXT.md so skills can resolve paths.
func writeSessionContext(sessionDir string, req *ExecuteRequest) error {
	content := fmt.Sprintf(`# Session Context

- App ID: %s
- Workspace: %s
- Memory dir: %s
- Memory lock: %s
- Tasks dir: %s
- Session ID: %s
- Session dir: %s
- Attachments dir: %s
`,
		req.AppConfig.ID,
		req.WorkspaceDir,
		filepath.Join(req.WorkspaceDir, "memory"),
		filepath.Join(req.WorkspaceDir, ".memory.lock"),
		filepath.Join(req.WorkspaceDir, "tasks"),
		req.SessionID,
		sessionDir,
		filepath.Join(sessionDir, "attachments"),
	)

	path := filepath.Join(sessionDir, "SESSION_CONTEXT.md")
	return os.WriteFile(path, []byte(content), 0o644)
}

func permissionMode(appCfg *config.AppConfig) string {
	if appCfg.Claude.PermissionMode != "" {
		return appCfg.Claude.PermissionMode
	}
	return "acceptEdits"
}
