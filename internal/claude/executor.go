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
	// SessionDirOverride, if non-empty, is used as the claude cwd instead of
	// the default <WorkspaceDir>/sessions/<SessionID>. Used by the task runner
	// for system tasks that live outside the regular sessions/ layout (e.g.
	// sessions/_system/<slug>/). SessionID is still used in metadata such as
	// SESSION_CONTEXT.md for observability.
	//
	// INVARIANT: the path MUST resolve inside WorkspaceDir. Execute enforces
	// this with filepath.Rel — escaping the workspace would let a misconfigured
	// caller point claude at an arbitrary filesystem location.
	SessionDirOverride string
	// TaskName, if non-empty, identifies a scheduled task invocation. Surfaces
	// to Langfuse traces via the CC_LF_TASK_NAME env var so cost can be
	// attributed by task name. Empty for interactive user messages.
	TaskName string
}

// ExecuteResult holds the output of a claude CLI invocation.
type ExecuteResult struct {
	Text            string
	ClaudeSessionID string // extracted from stream-json system event
	CostUSD         float64
	DurationMS      int64
	// IsError is set when stream-json emits a result event with is_error=true.
	IsError bool
	// ErrorText carries the result.result payload when IsError. Used by the
	// recovery layer to detect recoverable Anthropic 400s (see IsResumeRecoverable).
	ErrorText string
}

// Executor runs the claude CLI as a subprocess.
type Executor struct {
	cfg *config.Config
}

// New creates a new Executor.
func New(cfg *config.Config) *Executor {
	return &Executor{cfg: cfg}
}

// assertInsideWorkspace returns an error if dir does not resolve inside
// workspace. Protects against a miswired caller (or future bug) pointing
// claude's cwd at an arbitrary filesystem location via SessionDirOverride.
// Compares cleaned paths so ".." components cannot slip through unnoticed.
func assertInsideWorkspace(workspace, dir string) error {
	absWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		return fmt.Errorf("resolve workspace path: %w", err)
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolve session dir path: %w", err)
	}
	rel, err := filepath.Rel(absWorkspace, absDir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("SessionDirOverride %q escapes WorkspaceDir %q", dir, workspace)
	}
	return nil
}

// scannerMaxBytes is the per-line buffer limit for reading claude output.
// 1 MiB is well above any realistic single NDJSON line.
const scannerMaxBytes = 1 << 20 // 1 MiB

// Execute runs claude CLI and returns the final assistant text.
//
// When the call fails with an Anthropic API 400 caused by foreign-provider
// pollution in the resume jsonl (kimi/qwen via the bailian bridge produce
// thinking blocks with empty signatures and tool_use IDs containing '.'/':'),
// the offending lines are stripped from ~/.claude/projects/.../*.jsonl and
// the call is retried once. See sanitize.go for the exact criteria.
func (e *Executor) Execute(ctx context.Context, req *ExecuteRequest) (*ExecuteResult, error) {
	result, err := e.executeOnce(ctx, req)
	if !shouldAttemptResumeRecovery(req, result, err) {
		return result, err
	}

	sessionDir := req.SessionDirOverride
	if sessionDir == "" {
		sessionDir = filepath.Join(req.WorkspaceDir, "sessions", req.SessionID)
	}
	cleaned, sanitizeErr := SanitizeResumeForCwd(sessionDir, req.ClaudeSessionID)
	if sanitizeErr != nil {
		slog.Error("sanitize resume jsonl",
			"err", sanitizeErr, "claude_session_id", req.ClaudeSessionID)
		return result, err
	}
	if !cleaned {
		// No lines matched the foreign-provider rule — retrying would just
		// reproduce the same 400. Surface the original error.
		return result, err
	}
	slog.Info("retrying claude after jsonl sanitize",
		"claude_session_id", req.ClaudeSessionID)
	// Single-shot retry on purpose: the second result is returned as-is,
	// even if it is itself a recoverable 400. A second sanitize pass on the
	// same jsonl would find dropped=0 (already cleaned) and SanitizeResumeForCwd
	// would return cleaned=false, so a hypothetical extra retry loop cannot
	// make progress. Surfacing the second error to the user is the right move:
	// they can /new to start fresh.
	return e.executeOnce(ctx, req)
}

// shouldAttemptResumeRecovery reports whether the (result, err) pair from
// executeOnce describes a recoverable resume-jsonl pollution failure.
func shouldAttemptResumeRecovery(req *ExecuteRequest, result *ExecuteResult, err error) bool {
	if req == nil || req.ClaudeSessionID == "" {
		return false
	}
	var sb strings.Builder
	if result != nil {
		sb.WriteString(result.Text)
		sb.WriteByte(' ')
		sb.WriteString(result.ErrorText)
	}
	if err != nil {
		sb.WriteByte(' ')
		sb.WriteString(err.Error())
	}
	return IsResumeRecoverable(sb.String())
}

// executeOnce performs a single claude CLI invocation without recovery.
func (e *Executor) executeOnce(ctx context.Context, req *ExecuteRequest) (*ExecuteResult, error) {
	sessionDir := req.SessionDirOverride
	if sessionDir == "" {
		sessionDir = filepath.Join(req.WorkspaceDir, "sessions", req.SessionID)
	} else if err := assertInsideWorkspace(req.WorkspaceDir, sessionDir); err != nil {
		return nil, err
	}
	attachmentsDir := filepath.Join(sessionDir, "attachments")

	if err := os.MkdirAll(attachmentsDir, 0o755); err != nil {
		return nil, fmt.Errorf("create session dir: %w", err)
	}
	if err := writeSessionContext(sessionDir, req, req.AppConfig.DBPath); err != nil {
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
	setProcAttrs(cmd)
	// Add provider/model/auth env vars when configured.
	providerName, pc := resolveProvider(req.AppConfig, e.cfg)
	claudeEnvVars := buildClaudeEnvVars(providerName, pc)

	// Filter out vars from inherited env to prevent conflicts.
	// - ANTHROPIC_*: duplicate keys cause getenv() to return the first match
	// - CLAUDECODE/CLAUDE_CODE_*: prevent nested-session detection when the
	//   server itself was launched from within a Claude CLI session
	// - WORKSPACE_DIR: when the server is started from within a Claude Code
	//   session, the parent process's WORKSPACE_DIR is inherited. Linux
	//   getenv() returns the first match, so the appended correct value would
	//   be silently shadowed. Filter before appending.
	baseEnv := filterEnv(filterEnv(os.Environ(), "CLAUDECODE"), "CLAUDE_CODE_")
	baseEnv = filterEnv(baseEnv, "WORKSPACE_DIR")
	if len(claudeEnvVars) > 0 {
		baseEnv = filterEnv(baseEnv, "ANTHROPIC_")
	}
	// Filter prior CC_LF_* so a parent claude session doesn't poison the child's
	// langfuse trace attribution.
	baseEnv = filterEnv(baseEnv, "CC_LF_")
	cmd.Env = append(baseEnv,
		"TERM=xterm-256color",
		"FORCE_COLOR=0",
		"WORKSPACE_DIR="+req.WorkspaceDir,
	)
	if len(claudeEnvVars) > 0 {
		cmd.Env = append(cmd.Env, claudeEnvVars...)
	}
	if lfEnvVars := buildLangfuseEnvVars(req, req.TaskName); len(lfEnvVars) > 0 {
		cmd.Env = append(cmd.Env, lfEnvVars...)
	}

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
		stderrWg    sync.WaitGroup
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
		stderrStr := strings.Join(stderrLines, "\n")
		slog.Error("claude exited with error", "err", err, "stderr", stderrStr)
		if result.Text == "" {
			return nil, fmt.Errorf("claude failed: %w (stderr: %s)", err, stderrStr)
		}
		// If we got some text output before the error, return it rather than
		// discarding potentially useful partial results.
	}

	// Join the stderr goroutine after Wait() so it has had a chance to drain.
	stderrWg.Wait()

	return result, nil
}

// injectRoutingContext prepends a hidden system context block with routing_key,
// sender_id and the current wall-clock time directly into the prompt. Injecting
// into the prompt avoids SESSION_CONTEXT.md race conditions when concurrent
// goroutines write to the same session directory. current_time is included on
// every turn because claude sessions are long-lived and otherwise lose track of
// "now" as the conversation progresses.
func injectRoutingContext(req *ExecuteRequest) string {
	if req.ChannelKey == "" && req.SenderID == "" {
		return req.Prompt
	}
	now := time.Now().Format("2006-01-02 15:04:05 MST Monday")
	return fmt.Sprintf("<system_routing>\nrouting_key: %s\nsender_id: %s\ncurrent_time: %s\n</system_routing>\n\n%s",
		channelKeyToRoutingKey(req.ChannelKey), req.SenderID, now, req.Prompt)
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

	// --add-dir expands acceptEdits coverage from the session cwd up to the
	// whole workspace, so the agent can write to .claude/skills/, memory/,
	// and tasks/ (needed for conversation-driven skill / memory evolution).
	// Scope stays bounded: assertInsideWorkspace guarantees cwd is under
	// WorkspaceDir, and --add-dir only widens access within the same workspace.
	if req.WorkspaceDir != "" {
		args = append(args, "--add-dir", req.WorkspaceDir)
	}

	providerName, pc := resolveProvider(req.AppConfig, e.cfg)

	// --model has the highest priority (above env vars and settings.json).
	// Used together with ANTHROPIC_MODEL env var for belt-and-suspenders coverage.
	if pc.Model != "" {
		args = append(args, "--model", expandModelAlias(pc.Model))
	}

	if ef := resolveEffort(providerName, pc); ef != "" {
		args = append(args, "--effort", ef)
	}

	// --settings overrides ~/.claude/settings.json env vars (higher precedence).
	// This prevents the user's global settings from clobbering provider-specific
	// ANTHROPIC_BASE_URL / ANTHROPIC_AUTH_TOKEN / ANTHROPIC_MODEL.
	if settingsJSON := buildSettingsJSON(providerName, pc); settingsJSON != "" {
		args = append(args, "--settings", settingsJSON)
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
	IsError    bool    `json:"is_error"`
	Result     string  `json:"result"`
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
		if event.IsError {
			result.IsError = true
			result.ErrorText = event.Result
		}
	}
}

// writeSessionContext writes SESSION_CONTEXT.md so skills can resolve paths.
func writeSessionContext(sessionDir string, req *ExecuteRequest, dbPath string) error {
	content := fmt.Sprintf(`# Session Context

- App ID: %s
- Current datetime: %s
- Workspace: %s
- Memory dir: %s
- Memory lock: %s
- Tasks dir: %s
- Session ID: %s
- Session dir: %s
- Attachments dir: %s
- Channel key: %s
- DB path: %s
`,
		req.AppConfig.ID,
		time.Now().Format("2006-01-02 15:04"),
		req.WorkspaceDir,
		filepath.Join(req.WorkspaceDir, "memory"),
		filepath.Join(req.WorkspaceDir, ".memory.lock"),
		filepath.Join(req.WorkspaceDir, "tasks"),
		req.SessionID,
		sessionDir,
		filepath.Join(sessionDir, "attachments"),
		req.ChannelKey,
		dbPath,
	)

	path := filepath.Join(sessionDir, "SESSION_CONTEXT.md")
	return os.WriteFile(path, []byte(content), 0o644)
}

// channelKeyToRoutingKey converts a channel_key to a feishu_ops routing_key.
//
// channel_key formats (internal):
//
//	p2p:{chat_id}:{app_id}              → p2p:{chat_id}
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

// resolveModelFlag returns the effective model for the --model CLI flag.
// App-level setting takes priority over the global default.
// Short aliases (haiku/sonnet/opus) are expanded to full model IDs.
// Returns empty string when neither is set (claude uses its built-in default).
func resolveModelFlag(appCfg *config.AppConfig, cfg *config.Config) string {
	_, pc := resolveProvider(appCfg, cfg)
	if pc.Model == "" {
		return ""
	}
	return expandModelAlias(pc.Model)
}

// providerBaseURLs maps known provider names to their default ANTHROPIC_BASE_URL.
// Used as fallback when ProviderConfig.BaseURL is empty.
var providerBaseURLs = map[string]string{
	"bailian": "https://coding.dashscope.aliyuncs.com/apps/anthropic",
}

// resolveProvider determines the effective provider name and its merged config
// for a given app. Resolution order:
//  1. Provider name: app.claude.provider > claude.default_provider > "anthropic"
//  2. Provider config: looked up from claude.providers[name]
//  3. Model override: app.claude.model overrides the provider's default model
func resolveProvider(appCfg *config.AppConfig, cfg *config.Config) (string, config.ProviderConfig) {
	name := strings.TrimSpace(appCfg.Claude.Provider)
	if name == "" {
		name = strings.TrimSpace(cfg.Claude.DefaultProvider)
	}
	if name == "" {
		name = "anthropic"
	}

	var pc config.ProviderConfig
	if cfg.Claude.Providers != nil {
		pc = cfg.Claude.Providers[name]
	}

	// App-level model overrides provider default.
	if m := strings.TrimSpace(appCfg.Claude.Model); m != "" {
		pc.Model = m
	}

	// App-level effort overrides provider default.
	if ef := strings.TrimSpace(appCfg.Claude.Effort); ef != "" {
		pc.Effort = ef
	}

	return name, pc
}

// resolveEffort returns the effective --effort value, or "" when no flag
// should be appended. Effort is only honored for the default anthropic
// provider; third-party providers (e.g. bailian) may not recognize the flag,
// so we silently drop it to avoid subprocess errors.
func resolveEffort(providerName string, pc config.ProviderConfig) string {
	if !isAnthropicProvider(providerName) {
		return ""
	}
	return strings.TrimSpace(pc.Effort)
}

// isAnthropicProvider reports whether the given provider name refers to the
// native Anthropic API (empty string defaults to anthropic).
func isAnthropicProvider(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	return n == "" || n == "anthropic"
}

// filterEnv returns a copy of env with all entries whose key starts with
// the given prefix removed. This prevents inherited env vars from shadowing
// values we explicitly set for the subprocess.
func filterEnv(env []string, prefix string) []string {
	result := make([]string, 0, len(env))
	for _, e := range env {
		if k, _, ok := strings.Cut(e, "="); ok && strings.HasPrefix(k, prefix) {
			continue
		}
		result = append(result, e)
	}
	return result
}

// buildSettingsJSON returns a JSON string for --settings that overrides the
// env section of ~/.claude/settings.json. Returns "" when no override needed.
func buildSettingsJSON(providerName string, pc config.ProviderConfig) string {
	name := strings.ToLower(strings.TrimSpace(providerName))
	isDefault := name == "" || name == "anthropic"

	if isDefault && pc.AuthToken == "" && pc.Model == "" && pc.BaseURL == "" {
		return ""
	}

	envMap := make(map[string]string)

	baseURL := pc.BaseURL
	if baseURL == "" {
		baseURL = providerBaseURLs[name]
	}
	if baseURL != "" {
		envMap["ANTHROPIC_BASE_URL"] = baseURL
	}

	if pc.AuthToken != "" {
		envMap["ANTHROPIC_AUTH_TOKEN"] = pc.AuthToken
	}

	if pc.Model != "" {
		model := expandModelAlias(pc.Model)
		envMap["ANTHROPIC_MODEL"] = model
		envMap["ANTHROPIC_DEFAULT_HAIKU_MODEL"] = model
		envMap["ANTHROPIC_DEFAULT_SONNET_MODEL"] = model
		envMap["ANTHROPIC_DEFAULT_OPUS_MODEL"] = model
	}

	if len(envMap) == 0 {
		return ""
	}

	settings := map[string]interface{}{
		"env": envMap,
	}
	b, err := json.Marshal(settings)
	if err != nil {
		slog.Error("failed to marshal settings JSON", "err", err)
		return ""
	}
	return string(b)
}

// buildLangfuseEnvVars returns CC_LF_* env vars consumed by the Langfuse Stop /
// SubagentStop hook. Returns nil when the request is malformed (missing
// AppConfig or SessionID) so the hook will skip emit rather than mis-attribute.
//
// Design ref: docs/langfuse-cost-tracking-design.md §5.2.5.
func buildLangfuseEnvVars(req *ExecuteRequest, taskName string) []string {
	if req == nil || req.AppConfig == nil || req.SessionID == "" {
		return nil
	}
	// Strip newlines defensively — env-var values that contain \n can corrupt
	// the env block on some platforms and confuse line-based hook parsers.
	clean := func(s string) string {
		return strings.ReplaceAll(strings.ReplaceAll(s, "\n", ""), "\r", "")
	}
	out := []string{
		"CC_LF_META_VERSION=1",
		"CC_LF_APP_ID=" + clean(req.AppConfig.ID),
		"CC_LF_FRAMEWORK_SESSION_ID=" + clean(req.SessionID),
		"CC_LF_CHANNEL_KEY=" + clean(req.ChannelKey),
		"CC_LF_USER_OPEN_ID=" + clean(req.SenderID),
	}
	if taskName != "" {
		out = append(out, "CC_LF_TASK_NAME="+clean(taskName))
	}
	return out
}

// buildClaudeEnvVars returns the ANTHROPIC_* environment variables to set
// on the claude subprocess. Returns nil if using default anthropic with no
// custom config (pure default behavior — claude CLI uses its own auth and model).
func buildClaudeEnvVars(providerName string, pc config.ProviderConfig) []string {
	name := strings.ToLower(strings.TrimSpace(providerName))
	isDefault := name == "" || name == "anthropic"

	if isDefault && pc.AuthToken == "" && pc.Model == "" && pc.BaseURL == "" {
		return nil
	}

	var envs []string

	// Base URL: explicit config > hardcoded fallback for known providers
	baseURL := pc.BaseURL
	if baseURL == "" {
		baseURL = providerBaseURLs[name]
	}
	if baseURL != "" {
		envs = append(envs, "ANTHROPIC_BASE_URL="+baseURL)
	}

	if pc.AuthToken != "" {
		envs = append(envs, "ANTHROPIC_AUTH_TOKEN="+pc.AuthToken)
	}

	if pc.Model != "" {
		model := expandModelAlias(pc.Model)
		envs = append(envs,
			"ANTHROPIC_MODEL="+model,
			"ANTHROPIC_DEFAULT_HAIKU_MODEL="+model,
			"ANTHROPIC_DEFAULT_SONNET_MODEL="+model,
			"ANTHROPIC_DEFAULT_OPUS_MODEL="+model,
		)
	}

	return envs
}
