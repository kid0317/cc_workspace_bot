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
	"github.com/kid0317/cc-workspace-bot/internal/feishu"
	"github.com/kid0317/cc-workspace-bot/internal/model"
)

// Runner executes a scheduled task by invoking claude and sending results.
type Runner struct {
	cfg         *config.Config
	appRegistry map[string]*config.AppConfig
	db          *gorm.DB
	executor    *claude.Executor
	senders     map[string]*feishu.Sender
}

// NewRunner creates a Runner.
func NewRunner(
	cfg *config.Config,
	db *gorm.DB,
	executor *claude.Executor,
	senders map[string]*feishu.Sender,
) *Runner {
	registry := make(map[string]*config.AppConfig, len(cfg.Apps))
	for i := range cfg.Apps {
		a := &cfg.Apps[i]
		registry[a.ID] = a
	}
	return &Runner{
		cfg:         cfg,
		appRegistry: registry,
		db:          db,
		executor:    executor,
		senders:     senders,
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
				r.recordSentMessage(sess.ID, result.Text, id)
			}
		} else {
			slog.Info("task runner: send_output=false, suppressing text output",
				"task_id", task.ID, "text_len", len(result.Text))
		}
	}

	now := time.Now()
	if err := r.db.Model(&model.Task{}).Where("id = ?", task.ID).Update("last_run_at", now).Error; err != nil {
		slog.Error("task runner: update last_run_at", "err", err)
	}
}

func (r *Runner) getOrCreateSession(channelKey, appID, createdBy string, appCfg *config.AppConfig) (*model.Session, error) {
	var sess model.Session
	// Reuse the most recent session for this channel regardless of status.
	// Filtering by status='active' risks unbounded session growth: if a session
	// is ever archived (e.g. by a /new command), every subsequent task execution
	// on the same channel would create a new orphaned session directory.
	result := r.db.Where("channel_key = ?", channelKey).
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
	if err := r.db.Where("channel_key = ?", channelKey).First(&ch).Error; err != nil {
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
		if err := r.db.Create(&ch).Error; err != nil {
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
	if err := r.db.Create(&sess).Error; err != nil {
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

	// D2: reject tasks with unresolved template placeholders — they would
	// be stored in DB as real tasks but fail silently at every execution.
	for field, val := range map[string]string{
		"target_type": ty.TargetType,
		"target_id":   ty.TargetID,
		"prompt":      ty.Prompt,
	} {
		if placeholderRe.MatchString(val) {
			return nil, fmt.Errorf("task %s in %s has unresolved placeholder in field %q: %q", ty.ID, path, field, val)
		}
	}

	// D2: required field validation — missing values cause silent execution failures.
	// Cron is only required for enabled tasks; disabled tasks may omit it since
	// they are never scheduled (e.g. template seeds with enabled: false).
	switch {
	case ty.TargetType == "":
		return nil, fmt.Errorf("task %s in %s: target_type is required", ty.ID, path)
	case ty.TargetID == "":
		return nil, fmt.Errorf("task %s in %s: target_id is required", ty.ID, path)
	case ty.Cron == "" && ty.Enabled:
		return nil, fmt.Errorf("task %s in %s: cron is required for enabled tasks", ty.ID, path)
	case ty.Prompt == "":
		return nil, fmt.Errorf("task %s in %s: prompt is required", ty.ID, path)
	}

	// M-10: validate cron expression eagerly so we surface bad configs early.
	// Skip for disabled tasks with no cron — they will never be scheduled.
	if ty.Cron != "" {
		parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
		if _, err := parser.Parse(ty.Cron); err != nil {
			return nil, fmt.Errorf("invalid cron expression %q in %s: %w", ty.Cron, path, err)
		}
	}

	// Resolve send_output: default true if absent from YAML. Using *bool in
	// TaskYAML avoids the Go zero-value trap (omitted field → false).
	sendOutput := true
	if ty.SendOutput != nil {
		sendOutput = *ty.SendOutput
	}

	return &model.Task{
		ID:         ty.ID,
		AppID:      ty.AppID,
		Name:       ty.Name,
		CronExpr:   ty.Cron,
		TargetType: ty.TargetType,
		TargetID:   ty.TargetID,
		Prompt:     ty.Prompt,
		Enabled:    ty.Enabled,
		SendOutput: sendOutput,
		CreatedBy:  ty.CreatedBy,
		CreatedAt:  ty.CreatedAt,
	}, nil
}

// recordSentMessage writes the proactively-sent message to the messages table
// so that conversation history is consistent when the user replies on the same channel.
// Without this, Claude would have no memory of what it said between sessions.
func (r *Runner) recordSentMessage(sessionID, content, feishuMsgID string) {
	m := &model.Message{
		ID:          uuid.New().String(),
		SessionID:   sessionID,
		SenderID:    "",
		Role:        "assistant",
		Content:     content,
		FeishuMsgID: feishuMsgID,
		CreatedAt:   time.Now(),
	}
	if err := r.db.Create(m).Error; err != nil {
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

