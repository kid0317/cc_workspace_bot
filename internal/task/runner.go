package task

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
	"gopkg.in/yaml.v3"
	"gorm.io/gorm"

	"github.com/kid0317/cc-workspace-bot/internal/claude"
	"github.com/kid0317/cc-workspace-bot/internal/config"
	"github.com/kid0317/cc-workspace-bot/internal/db"
	"github.com/kid0317/cc-workspace-bot/internal/feishu"
	"github.com/kid0317/cc-workspace-bot/internal/model"
)

// ChannelArchiver archives the active session for a given channel key.
// Kept as a small interface so the runner doesn't depend on the concrete
// session.Manager type (and to make unit tests trivially fakeable).
type ChannelArchiver interface {
	ArchiveChannel(channelKey string) error
}

// Runner executes a scheduled task by invoking claude and sending results.
type Runner struct {
	cfg         *config.Config
	appRegistry map[string]*config.AppConfig
	dbReg       *db.Registry
	executor    *claude.Executor
	senders     map[string]*feishu.Sender
	archiver    ChannelArchiver
}

// NewRunner creates a Runner.
// archiver may be nil in tests that don't exercise the PostArchive path;
// the Run method guards against nil before dispatching.
func NewRunner(
	cfg *config.Config,
	reg *db.Registry,
	executor *claude.Executor,
	senders map[string]*feishu.Sender,
	archiver ChannelArchiver,
) *Runner {
	registry := make(map[string]*config.AppConfig, len(cfg.Apps))
	for i := range cfg.Apps {
		a := &cfg.Apps[i]
		registry[a.ID] = a
	}
	return &Runner{
		cfg:         cfg,
		appRegistry: registry,
		dbReg:       reg,
		executor:    executor,
		senders:     senders,
		archiver:    archiver,
	}
}

// Run executes the given task.
func (r *Runner) Run(ctx context.Context, task *model.Task) {
	slog.Info("task runner: executing task", "task_id", task.ID, "name", task.Name)

	appCfg, ok := r.appRegistry[task.AppID]
	if !ok {
		slog.Error("task runner: unknown app_id", "app_id", task.AppID)
		return
	}

	// System tasks (send_output=false + no target) run in an isolated cwd
	// without touching the Channel/Session tables or any user chat context.
	// They cannot send output, so no sender lookup is needed either.
	if isSystemTask(task) {
		r.runSystemTask(ctx, task, appCfg)
		return
	}

	sender, ok := r.senders[task.AppID]
	if !ok {
		slog.Error("task runner: no sender for app", "app_id", task.AppID)
		return
	}

	channelKey := buildChannelKey(task.TargetType, task.TargetID, appCfg.ID)

	sess, err := r.getOrCreateSession(channelKey, task.AppID, task.CreatedBy, appCfg)
	if err != nil {
		slog.Error("task runner: get/create session", "err", err)
		return
	}

	// Each task execution starts a fresh claude conversation (no --resume).
	// Reusing a stale claude_session_id from a previous run causes failures
	// when the session has expired or the context has grown too large.
	result, err := r.executor.Execute(ctx, &claude.ExecuteRequest{
		Prompt:       task.Prompt,
		SessionID:    sess.ID,
		AppConfig:    appCfg,
		WorkspaceDir: appCfg.WorkspaceDir,
		ChannelKey:   channelKey,
		SenderID:     task.CreatedBy,
		TaskName:     task.Name,
	})
	if err != nil {
		slog.Error("task runner: execute", "err", err, "task_id", task.ID)
		return
	}

	if result.Text != "" {
		if task.SendOutput {
			receiveID, receiveType := receiveTarget(task.TargetType, task.TargetID)
			if id, err := sender.SendText(ctx, receiveID, receiveType, result.Text); err != nil {
				slog.Error("task runner: send text", "err", err)
				// Do NOT record: message was not delivered, recording would
				// create a phantom history entry Claude never actually sent.
			} else {
				// Record only on confirmed delivery so conversation history
				// stays consistent with what the user actually received.
				r.recordSentMessage(task.AppID, sess.ID, result.Text, id)
			}
		} else {
			slog.Info("task runner: send_output=false, suppressing text output",
				"task_id", task.ID, "text_len", len(result.Text))
		}
	}

	// PostArchive runs only on successful Execute (err==nil, checked above).
	// Validation guarantees this path is reached only for borrow-channel
	// tasks (send_output=false + target_* set). Archive failures are logged
	// but not treated as fatal — the task itself already did its work.
	if task.PostArchive && r.archiver != nil {
		if aerr := r.archiver.ArchiveChannel(channelKey); aerr != nil {
			slog.Error("task runner: post-archive failed",
				"err", aerr, "task_id", task.ID, "channel", channelKey)
		}
	}

	r.touchLastRun(task.ID)
}

// runSystemTask executes a workspace-internal background task. Unlike user-facing
// tasks, it:
//   - does NOT create a Channel or Session DB record (the task has no counterparty)
//   - does NOT resume any prior claude session (always fresh context)
//   - runs in a dedicated cwd at sessions/_system/<slug>/ that is overwritten in
//     place each run, so disk usage stays bounded without touching the attachment
//     cleanup subsystem.
//
// Used for tasks like calibrate_params that only read/write workspace files and
// have no user to send output to.
func (r *Runner) runSystemTask(ctx context.Context, task *model.Task, appCfg *config.AppConfig) {
	sessionDir := filepath.Join(appCfg.WorkspaceDir, "sessions", "_system", systemTaskSlug(task.ID))
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		slog.Error("task runner: create system session dir", "err", err, "task_id", task.ID)
		return
	}

	result, err := r.executor.Execute(ctx, &claude.ExecuteRequest{
		Prompt:             task.Prompt,
		SessionID:          "_system/" + systemTaskSlug(task.ID),
		AppConfig:          appCfg,
		WorkspaceDir:       appCfg.WorkspaceDir,
		SessionDirOverride: sessionDir,
		// ChannelKey and SenderID intentionally empty: system tasks have no
		// chat counterparty, so injectRoutingContext will skip the header.
	})
	if err != nil {
		slog.Error("task runner: execute system task", "err", err, "task_id", task.ID)
		return
	}

	// System tasks never send output. Any text Claude produced is discarded
	// after being noted in logs — the contract is "do work silently".
	if result.Text != "" {
		slog.Info("task runner: system task produced text (discarded)",
			"task_id", task.ID, "text_len", len(result.Text))
	}

	r.touchLastRun(task.ID)
}

// targetMode classifies the (send_output × target_type × target_id) shape of a
// task. Kept as the single source of truth for both validation (which rejects
// modeInvalid) and runtime routing (which dispatches on modeSystem). If a new
// class of task is added in the future, it must be added here — callers that
// only know about a subset will fail to compile or fall through to a default
// branch, forcing coordinated updates.
type targetMode int

const (
	// modeInvalid: mixed target state (exactly one of target_type / target_id
	// set). Always a typo, never a legitimate shape.
	modeInvalid targetMode = iota
	// modeUserReply: send_output=true with both target fields set. The normal
	// user-facing task — claude output is forwarded to the chat.
	modeUserReply
	// modeBorrowChannel: send_output=false with both target fields set. The
	// task runs in a user channel but the skill is responsible for sending
	// (e.g. proactive_reach uses feishu_ops directly).
	modeBorrowChannel
	// modeSystem: send_output=false with no target fields. Pure workspace-
	// internal work — no chat counterparty, no DB Channel/Session record.
	modeSystem
)

// classifyTarget returns the targetMode of a (sendOutput, targetType, targetID)
// triple. This is the ONLY place the classification rules live; both
// validateTaskFields and isSystemTask delegate here to avoid drift.
func classifyTarget(sendOutput bool, targetType, targetID string) targetMode {
	if sendOutput {
		if targetType == "" || targetID == "" {
			return modeInvalid
		}
		return modeUserReply
	}
	// send_output=false
	switch {
	case targetType == "" && targetID == "":
		return modeSystem
	case targetType != "" && targetID != "":
		return modeBorrowChannel
	default:
		return modeInvalid
	}
}

// isSystemTask reports whether a task should run in the workspace-internal
// background path. Delegates to classifyTarget so the rule is defined once.
func isSystemTask(t *model.Task) bool {
	return classifyTarget(t.SendOutput, t.TargetType, t.TargetID) == modeSystem
}

// systemTaskSlug returns the filename-safe tail of task.ID (after the app_id/).
// task.ID is always "<app_id>/<slug>"; returning just the slug avoids a nested
// <app_id> directory under sessions/_system/.
func systemTaskSlug(taskID string) string {
	if i := strings.LastIndex(taskID, "/"); i >= 0 {
		return taskID[i+1:]
	}
	return taskID
}

// touchLastRun updates the task's last_run_at timestamp. Extracted so both
// user-facing and system task paths stay in sync on scheduling telemetry.
// taskID has format "{app_id}/{slug}"; the app_id prefix selects the right DB.
func (r *Runner) touchLastRun(taskID string) {
	appID := appIDFromTaskID(taskID)
	appDB, err := r.dbReg.Get(appID)
	if err != nil {
		slog.Error("task runner: db not found for touchLastRun", "task_id", taskID, "err", err)
		return
	}
	now := time.Now()
	if err := appDB.Model(&model.Task{}).Where("id = ?", taskID).Update("last_run_at", now).Error; err != nil {
		slog.Error("task runner: update last_run_at", "err", err, "task_id", taskID)
	}
}

// appIDFromTaskID extracts the app_id prefix from a task ID of the form "{app_id}/{slug}".
func appIDFromTaskID(taskID string) string {
	if i := strings.Index(taskID, "/"); i >= 0 {
		return taskID[:i]
	}
	return taskID
}

func (r *Runner) getOrCreateSession(channelKey, appID, createdBy string, appCfg *config.AppConfig) (*model.Session, error) {
	appDB, err := r.dbReg.Get(appID)
	if err != nil {
		return nil, fmt.Errorf("get db for app %q: %w", appID, err)
	}

	var sess model.Session
	// Reuse the most recent session for this channel regardless of status.
	// Filtering by status='active' risks unbounded session growth: if a session
	// is ever archived (e.g. by a /new command), every subsequent task execution
	// on the same channel would create a new orphaned session directory.
	result := appDB.Where("channel_key = ?", channelKey).
		Order("created_at DESC").First(&sess)
	if result.Error == nil {
		return &sess, nil
	}
	// C-3: use errors.Is for GORM sentinel errors.
	if !errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("query session: %w", result.Error)
	}

	// Ensure channel record exists.
	chatType, chatID := parseChannelKey(channelKey)
	var ch model.Channel
	if err := appDB.Where("channel_key = ?", channelKey).First(&ch).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("query channel: %w", err)
		}
		ch = model.Channel{
			ChannelKey: channelKey,
			AppID:      appID,
			ChatType:   chatType,
			ChatID:     chatID,
			CreatedAt:  time.Now(),
		}
		if err := appDB.Create(&ch).Error; err != nil {
			return nil, fmt.Errorf("create channel: %w", err)
		}
	}

	newID := uuid.New().String()
	sessionDir := filepath.Join(appCfg.WorkspaceDir, "sessions", newID)
	if err := os.MkdirAll(filepath.Join(sessionDir, "attachments"), 0o755); err != nil {
		return nil, fmt.Errorf("create session dir: %w", err)
	}

	sess = model.Session{
		ID:         newID,
		ChannelKey: channelKey,
		Status:     "active",
		CreatedBy:  createdBy,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	if err := appDB.Create(&sess).Error; err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	return &sess, nil
}

// placeholderRe matches unresolved template placeholders like __TARGET_TYPE__.
var placeholderRe = regexp.MustCompile(`__[A-Z_]+__`)

// LoadYAML reads a task YAML file and returns a model.Task.
// appID is the workspace ID derived from the file path (e.g. "xh_yibu");
// it overrides whatever app_id the YAML contains, preventing mismatches between
// the Feishu App ID and the workspace ID.
//
// Returns an error if required fields are empty or contain unresolved placeholders.
func LoadYAML(path string, appID string) (*model.Task, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return LoadYAMLFromBytes(data, path, appID)
}

// LoadYAMLFromBytes parses already-read YAML bytes. Exposed so the watcher can
// read once, hash the content for log de-duplication, and reuse the same bytes
// for parsing.
func LoadYAMLFromBytes(data []byte, path, appID string) (*model.Task, error) {
	var ty model.TaskYAML
	if err := yaml.Unmarshal(data, &ty); err != nil {
		return nil, fmt.Errorf("parse task yaml %s: %w", path, err)
	}

	// Task ID = "{app_id}/{filename_slug}" — computed entirely by the framework.
	// This guarantees global uniqueness across workspaces regardless of what
	// filename the model chooses (e.g. "proactive_reach", a UUID, a semantic name).
	// The YAML id field is ignored (see TaskYAML.ID comment in model).
	ty.ID = appID + "/" + strings.TrimSuffix(filepath.Base(path), ".yaml")

	// D1: workspace ID is authoritative; ignore app_id from YAML.
	ty.AppID = appID

	// Resolve send_output first so validateTaskFields can branch on it.
	// Using *bool in TaskYAML avoids the Go zero-value trap (omitted field → false).
	sendOutput := true
	if ty.SendOutput != nil {
		sendOutput = *ty.SendOutput
	}

	if err := validateTaskFields(&ty, sendOutput, path); err != nil {
		return nil, err
	}

	// M-10: validate cron expression eagerly so we surface bad configs early.
	// Skip for disabled tasks with no cron — they will never be scheduled.
	if ty.Cron != "" {
		parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
		if _, err := parser.Parse(ty.Cron); err != nil {
			return nil, fmt.Errorf("invalid cron expression %q in %s: %w", ty.Cron, path, err)
		}
	}

	return &model.Task{
		ID:          ty.ID,
		AppID:       ty.AppID,
		Name:        ty.Name,
		CronExpr:    ty.Cron,
		TargetType:  ty.TargetType,
		TargetID:    ty.TargetID,
		Prompt:      ty.Prompt,
		Enabled:     ty.Enabled,
		SendOutput:  sendOutput,
		PostArchive: ty.PostArchive,
		CreatedBy:   ty.CreatedBy,
		CreatedAt:   ty.CreatedAt,
	}, nil
}

// validateTaskFields enforces the three-class task contract:
//
//   - send_output=true  → target_type and target_id both required (user-facing task)
//   - send_output=false → either both target fields set (borrow a user channel,
//     skill sends via its own path) or both empty (pure system task)
//
// Mixed combinations (only one of target_type/target_id) are rejected: they are
// always a typo, never a legitimate configuration.
//
// Separate from cron/prompt checks so the required-field reason is obvious in
// the error message, instead of a monolithic switch that skips earlier cases.
func validateTaskFields(ty *model.TaskYAML, sendOutput bool, path string) error {
	// D2: reject unresolved template placeholders first. Empty strings don't
	// match placeholderRe, so this check is compatible with system tasks that
	// intentionally omit target_* — it only fires when a field was *meant* to
	// be substituted but wasn't.
	for field, val := range map[string]string{
		"target_type": ty.TargetType,
		"target_id":   ty.TargetID,
		"prompt":      ty.Prompt,
	} {
		if placeholderRe.MatchString(val) {
			return fmt.Errorf("task %s in %s has unresolved placeholder in field %q: %q", ty.ID, path, field, val)
		}
	}

	if ty.Prompt == "" {
		return fmt.Errorf("task %s in %s: prompt is required", ty.ID, path)
	}

	// Single source of truth: classifyTarget. modeInvalid captures every
	// malformed combination so this branch never has to enumerate them.
	switch classifyTarget(sendOutput, ty.TargetType, ty.TargetID) {
	case modeInvalid:
		if sendOutput {
			return fmt.Errorf("task %s in %s: target_type and target_id are required when send_output=true", ty.ID, path)
		}
		return fmt.Errorf("task %s in %s: target_type and target_id must both be set or both be empty (send_output=false)", ty.ID, path)
	case modeUserReply, modeBorrowChannel, modeSystem:
		// all valid
	}

	if ty.Cron == "" && ty.Enabled {
		return fmt.Errorf("task %s in %s: cron is required for enabled tasks", ty.ID, path)
	}

	// post_archive only makes sense for borrow-channel tasks: they target a
	// specific user channel whose session the framework can archive. System
	// tasks have no channel to archive; user-reply tasks are interactive and
	// archiving mid-conversation would drop context the user is still using.
	if ty.PostArchive && classifyTarget(sendOutput, ty.TargetType, ty.TargetID) != modeBorrowChannel {
		return fmt.Errorf("task %s in %s: post_archive=true requires send_output=false with both target_type and target_id set", ty.ID, path)
	}

	return nil
}

// recordSentMessage writes the proactively-sent message to the messages table
// so that conversation history is consistent when the user replies on the same channel.
// Without this, Claude would have no memory of what it said between sessions.
func (r *Runner) recordSentMessage(appID, sessionID, content, feishuMsgID string) {
	appDB, err := r.dbReg.Get(appID)
	if err != nil {
		slog.Error("task runner: db not found for recordSentMessage",
			"app_id", appID, "session_id", sessionID, "err", err)
		return
	}
	m := &model.Message{
		ID:          uuid.New().String(),
		SessionID:   sessionID,
		SenderID:    "",
		Role:        "assistant",
		Content:     content,
		FeishuMsgID: feishuMsgID,
		CreatedAt:   time.Now(),
	}
	if err := appDB.Create(m).Error; err != nil {
		// Message was already delivered to Feishu; DB failure causes amnesia
		// (Claude won't see this message next session) but is not user-visible.
		slog.Error("task runner: record sent message",
			"err", err, "session_id", sessionID, "content_len", len(content))
	}
}

func buildChannelKey(targetType, targetID, appID string) string {
	switch targetType {
	case "p2p":
		return fmt.Sprintf("p2p:%s:%s", targetID, appID)
	default:
		return fmt.Sprintf("group:%s:%s", targetID, appID)
	}
}

func receiveTarget(targetType, targetID string) (string, string) {
	if targetType == "p2p" {
		// Detect ID format: oc_* is a chat_id, ou_* is an open_id.
		// Task YAMLs may contain either format due to historical inconsistency.
		if strings.HasPrefix(targetID, "oc_") {
			return targetID, "chat_id"
		}
		return targetID, "open_id"
	}
	return targetID, "chat_id"
}

// parseChannelKey extracts the chat type and target ID from a channel key.
// M-4: uses strings.SplitN (stdlib) and documents the expected format.
// Channel key formats: "p2p:<targetID>:<appID>" or "group:<targetID>:<appID>".
// Feishu open_ids and chat_ids never contain colons, so splitting on ":" is safe.
func parseChannelKey(key string) (chatType, chatID string) {
	parts := strings.SplitN(key, ":", 3)
	if len(parts) < 2 {
		return "group", key
	}
	return parts[0], parts[1]
}
