package task

import (
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"gorm.io/gorm"

	"github.com/kid0317/cc-workspace-bot/internal/config"
	"github.com/kid0317/cc-workspace-bot/internal/model"
)

// Cleaner removes the attachments/ subdirectory for sessions that meet either
// cleanup condition:
//   - session is archived AND updated_at older than retention_days
//   - session created_at older than max_days (force cleanup)
type Cleaner struct {
	db   *gorm.DB
	apps []config.AppConfig
	cfg  config.CleanupConfig
}

// NewCleaner creates a Cleaner and registers the cleanup cron job with sched.
func NewCleaner(db *gorm.DB, apps []config.AppConfig, cfg config.CleanupConfig, sched *Scheduler) (*Cleaner, error) {
	c := &Cleaner{db: db, apps: apps, cfg: cfg}
	if err := sched.AddFunc("attachment-cleanup", cfg.Schedule, c.Run); err != nil {
		return nil, err
	}
	return c, nil
}

// Run executes one cleanup pass across all configured apps.
func (c *Cleaner) Run() {
	now := time.Now()
	retentionCutoff := now.Add(-time.Duration(c.cfg.AttachmentsRetentionDays) * 24 * time.Hour)
	maxCutoff := now.Add(-time.Duration(c.cfg.AttachmentsMaxDays) * 24 * time.Hour)

	total := 0
	for i := range c.apps {
		total += c.cleanApp(&c.apps[i], retentionCutoff, maxCutoff)
	}
	slog.Info("attachment cleanup complete", "deleted", total)
}

// cleanApp removes stale attachment dirs for one app and returns the count deleted.
func (c *Cleaner) cleanApp(app *config.AppConfig, retentionCutoff, maxCutoff time.Time) int {
	var sessions []model.Session
	err := c.db.
		Joins("JOIN channels ON sessions.channel_key = channels.channel_key").
		Where("channels.app_id = ?", app.ID).
		Where(
			"(sessions.status = ? AND sessions.updated_at < ?) OR sessions.created_at < ?",
			"archived", retentionCutoff, maxCutoff,
		).
		Find(&sessions).Error
	if err != nil {
		slog.Error("cleanup: query sessions", "app_id", app.ID, "err", err)
		return 0
	}

	count := 0
	for _, sess := range sessions {
		attachDir := filepath.Join(app.WorkspaceDir, "sessions", sess.ID, "attachments")

		if _, statErr := os.Stat(attachDir); os.IsNotExist(statErr) {
			continue // nothing to remove
		}

		if err := os.RemoveAll(attachDir); err != nil {
			slog.Warn("cleanup: remove attachments", "session_id", sess.ID, "err", err)
			continue
		}
		count++
		slog.Info("cleanup: removed attachments", "session_id", sess.ID, "app_id", app.ID)
	}
	return count
}
