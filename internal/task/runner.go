package task

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
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

	result, err := r.executor.Execute(ctx, &claude.ExecuteRequest{
		Prompt:          task.Prompt,
		SessionID:       sess.ID,
		ClaudeSessionID: sess.ClaudeSessionID,
		AppConfig:       appCfg,
		WorkspaceDir:    appCfg.WorkspaceDir,
	})
	if err != nil {
		slog.Error("task runner: execute", "err", err, "task_id", task.ID)
		return
	}

	// C-2: log GORM write errors.
	if result.ClaudeSessionID != "" && sess.ClaudeSessionID == "" {
		if err := r.db.Model(sess).Update("claude_session_id", result.ClaudeSessionID).Error; err != nil {
			slog.Error("task runner: update claude_session_id", "err", err)
		}
	}

	if result.Text != "" {
		receiveID, receiveType := receiveTarget(task.TargetType, task.TargetID)
		if _, err := sender.SendText(ctx, receiveID, receiveType, result.Text); err != nil {
			slog.Error("task runner: send text", "err", err)
		}
	}

	now := time.Now()
	if err := r.db.Model(&model.Task{}).Where("id = ?", task.ID).Update("last_run_at", now).Error; err != nil {
		slog.Error("task runner: update last_run_at", "err", err)
	}
}

func (r *Runner) getOrCreateSession(channelKey, appID, createdBy string, appCfg *config.AppConfig) (*model.Session, error) {
	var sess model.Session
	result := r.db.Where("channel_key = ? AND status = ?", channelKey, "active").
		Order("created_at DESC").First(&sess)
	if result.Error == nil {
		return &sess, nil
	}
	// C-3: use errors.Is for GORM sentinel errors.
	if !errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, result.Error
	}

	// Ensure channel record exists.
	chatType, chatID := parseChannelKey(channelKey)
	var ch model.Channel
	if err := r.db.Where("channel_key = ?", channelKey).First(&ch).Error; err != nil {
		ch = model.Channel{
			ChannelKey: channelKey,
			AppID:      appID,
			ChatType:   chatType,
			ChatID:     chatID,
			CreatedAt:  time.Now(),
		}
		if err := r.db.Create(&ch).Error; err != nil {
			slog.Error("task runner: create channel", "err", err)
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

// LoadYAML reads a task YAML file and returns a model.Task.
// M-10: validates the cron expression before returning.
func LoadYAML(path string) (*model.Task, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var ty model.TaskYAML
	if err := yaml.Unmarshal(data, &ty); err != nil {
		return nil, fmt.Errorf("parse task yaml %s: %w", path, err)
	}

	if ty.ID == "" {
		ty.ID = uuid.New().String()
	}

	// M-10: validate cron expression eagerly so we surface bad configs early.
	if ty.Cron != "" {
		parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
		if _, err := parser.Parse(ty.Cron); err != nil {
			return nil, fmt.Errorf("invalid cron expression %q in %s: %w", ty.Cron, path, err)
		}
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
		CreatedBy:  ty.CreatedBy,
		CreatedAt:  ty.CreatedAt,
	}, nil
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
