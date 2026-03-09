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
	ChannelKey      string // used to derive routing_key for feishu_ops
	SenderID        string // sender's open_id, for p2p feishu_ops calls
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

	// Inject routing metadata directly into the prompt to avoid file-based race
	// conditions when multiple goroutines write SESSION_CONTEXT.md concurrently.
	promptWithCtx := injectRoutingContext(req)
	args := e.buildArgs(promptWithCtx, req, sessionDir)

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
		"WORKSPACE_DIR="+req.WorkspaceDir,
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
	var (
		stderrWg   sync.WaitGroup
		stderrLines []string
		stderrMu    sync.Mutex
	)
	stderrWg.Add(1)
	go func() {
		defer stderrWg.Done()
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			line := sc.Text()
			stderrMu.Lock()
			stderrLines = append(stderrLines, line)
			stderrMu.Unlock()
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
		// Join stderr goroutine first so stderrLines is fully populated.
		stderrWg.Wait()
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("claude timed out after %d minutes", e.cfg.Claude.TimeoutMinutes)
		}
		slog.Warn("claude exited with error", "err", err, "stderr", strings.Join(stderrLines, "\n"))
	}

	// Join the stderr goroutine after Wait() so it has had a chance to drain.
	stderrWg.Wait()

	return result, nil
}

// injectRoutingContext prepends a hidden system context block with routing_key
// and sender_id directly into the prompt. This avoids SESSION_CONTEXT.md race
// conditions when concurrent goroutines write to the same session directory.
func injectRoutingContext(req *ExecuteRequest) string {
	if req.ChannelKey == "" && req.SenderID == "" {
		return req.Prompt
	}
	return fmt.Sprintf("<system_routing>\nrouting_key: %s\nsender_id: %s\n</system_routing>\n\n%s",
		channelKeyToRoutingKey(req.ChannelKey), req.SenderID, req.Prompt)
}

// buildArgs constructs the claude CLI argument list.
func (e *Executor) buildArgs(prompt string, req *ExecuteRequest, sessionDir string) []string {
	args := []string{
		"-p", prompt,
		"--output-format", "stream-json",
		"--verbose",
		"--permission-mode", permissionMode(req.AppConfig),
		"--max-turns", fmt.Sprintf("%d", e.cfg.Claude.MaxTurns),
	}

	if model := resolveModel(req.AppConfig, e.cfg); model != "" {
		args = append(args, "--model", model)
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

// channelKeyToRoutingKey converts a channel_key to a feishu_ops routing_key.
//
// channel_key formats (internal):
//
//	p2p:{open_id}:{app_id}              → p2p:{open_id}
//	group:{chat_id}:{app_id}            → group:{chat_id}
//	thread:{chat_id}:{thread_id}:{app_id} → group:{chat_id}  (send target is the chat)
func channelKeyToRoutingKey(channelKey string) string {
	parts := strings.SplitN(channelKey, ":", 4)
	switch parts[0] {
	case "p2p":
		if len(parts) >= 2 {
			return "p2p:" + parts[1]
		}
	case "group":
		if len(parts) >= 2 {
			return "group:" + parts[1]
		}
	case "thread":
		// thread:{chat_id}:{thread_id}:{app_id} → group:{chat_id}
		if len(parts) >= 2 {
			return "group:" + parts[1]
		}
	}
	return channelKey
}

func permissionMode(appCfg *config.AppConfig) string {
	if appCfg.Claude.PermissionMode != "" {
		return appCfg.Claude.PermissionMode
	}
	return "acceptEdits"
}

// modelAliases maps short names to full Claude model IDs.
// The claude CLI only accepts "sonnet" and "opus" as built-in aliases;
// "haiku" must be expanded here before being passed to --model.
var modelAliases = map[string]string{
	"haiku":  "claude-haiku-4-5-20251001",
	"sonnet": "claude-sonnet-4-6",
	"opus":   "claude-opus-4-6",
}

// expandModelAlias expands a short alias to the full model ID.
// Unknown values are returned as-is (full IDs pass through unchanged).
func expandModelAlias(m string) string {
	if full, ok := modelAliases[strings.ToLower(m)]; ok {
		return full
	}
	return m
}

// resolveModel returns the effective model for this request.
// App-level setting takes priority over the global default.
// Short aliases (haiku/sonnet/opus) are expanded to full model IDs.
// Returns empty string when neither is set (claude uses its built-in default).
func resolveModel(appCfg *config.AppConfig, cfg *config.Config) string {
	m := strings.TrimSpace(appCfg.Claude.Model)
	if m == "" {
		m = strings.TrimSpace(cfg.Claude.Model)
	}
	if m == "" {
		return ""
	}
	return expandModelAlias(m)
}
