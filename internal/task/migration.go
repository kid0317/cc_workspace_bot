package task

import (
	"fmt"
	"log/slog"
	"strings"

	"gorm.io/gorm"

	"github.com/kid0317/cc-workspace-bot/internal/model"
)

// MigrateTaskIDs rewrites legacy task ID formats to the canonical "app_id/slug" scheme.
//
// Before this migration, task IDs were derived from filename alone, causing
// cross-workspace collisions when multiple workspaces used the same filename
// (e.g. every companion workspace has "proactive_reach.yaml"). Two legacy formats
// are handled:
//
//   - Bare slug:      "proactive_reach"          → "xh_yibu/proactive_reach"
//   - Dotted prefix:  "ycm_mate.proactive_reach" → "ycm_mate/proactive_reach"
//
// All other IDs (UUIDs, semantic names) that lack a "/" are also migrated:
//
//	"1ff20d20-..." → "investment/1ff20d20-..."
//
// Idempotency: rows whose ID already contains "/" are skipped entirely.
// Conflict handling: if the target ID already exists, the stale legacy row is
// hard-deleted rather than leaving orphaned records in the DB.
// Transaction: all renames run in a single transaction; failure is logged and
// non-fatal — old-format rows remain functional until the next startup attempt.
func MigrateTaskIDs(db *gorm.DB) {
	// Load all non-migrated rows (those without the "/" namespace separator).
	var legacy []model.Task
	if err := db.Unscoped().Where("id NOT LIKE '%/%'").Find(&legacy).Error; err != nil {
		slog.Error("migrateTaskIDs: query legacy rows", "err", err)
		return
	}
	if len(legacy) == 0 {
		return
	}

	slog.Info("migrateTaskIDs: found legacy task IDs", "count", len(legacy))

	err := db.Transaction(func(tx *gorm.DB) error {
		for _, t := range legacy {
			if t.AppID == "" {
				slog.Warn("migrateTaskIDs: skipping row with empty app_id", "id", t.ID)
				continue
			}

			// Derive the slug: strip the legacy "appID." dot-prefix if present.
			// This handles the transitional format created by the short-lived
			// TaskFileID function (e.g. "ycm_mate.proactive_reach").
			slug := t.ID
			if dotPrefix := t.AppID + "."; strings.HasPrefix(slug, dotPrefix) {
				slug = strings.TrimPrefix(slug, dotPrefix)
			}

			newID := t.AppID + "/" + slug

			// Check for PK collision before updating.
			var count int64
			if err := tx.Unscoped().Model(&model.Task{}).
				Where("id = ?", newID).Count(&count).Error; err != nil {
				return fmt.Errorf("check existing id %q: %w", newID, err)
			}
			if count > 0 {
				// The canonical record already exists; drop the stale legacy row.
				slog.Warn("migrateTaskIDs: target already exists, removing stale legacy row",
					"old_id", t.ID, "new_id", newID)
				if err := tx.Unscoped().
					Exec("DELETE FROM tasks WHERE id = ?", t.ID).Error; err != nil {
					return fmt.Errorf("delete stale row %q: %w", t.ID, err)
				}
				continue
			}

			// Use raw SQL for the primary-key UPDATE — GORM's ORM layer does not
			// support mutating the primary key column through struct-based updates.
			if err := tx.Exec("UPDATE tasks SET id = ? WHERE id = ?", newID, t.ID).Error; err != nil {
				return fmt.Errorf("rename %q → %q: %w", t.ID, newID, err)
			}
			slog.Info("migrateTaskIDs: renamed", "old", t.ID, "new", newID)
		}
		return nil
	})
	if err != nil {
		// Non-fatal: legacy rows continue to work with their old IDs until the
		// next startup retries the migration.
		slog.Error("migrateTaskIDs: transaction failed, legacy IDs unchanged", "err", err)
	}
}
