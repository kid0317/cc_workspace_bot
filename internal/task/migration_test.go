package task

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/kid0317/cc-workspace-bot/internal/model"
)

// openTestDB creates an in-memory SQLite DB with the tasks table auto-migrated.
func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&model.Task{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return db
}

// seedTask inserts a minimal task row with the given id and app_id.
func seedTask(t *testing.T, db *gorm.DB, id, appID string) {
	t.Helper()
	task := model.Task{
		ID:         id,
		AppID:      appID,
		Name:       id,
		CronExpr:   "0 9 * * *",
		TargetType: "p2p",
		TargetID:   "ou_test",
		Prompt:     "test",
		Enabled:    true,
		SendOutput: true,
		CreatedAt:  time.Now(),
	}
	if err := db.Create(&task).Error; err != nil {
		t.Fatalf("seed task %q: %v", id, err)
	}
}

// taskExists reports whether a task with the given id exists (including soft-deleted).
func taskExists(db *gorm.DB, id string) bool {
	var count int64
	db.Unscoped().Model(&model.Task{}).Where("id = ?", id).Count(&count)
	return count > 0
}

func TestMigrateTaskIDs_EmptyDB(t *testing.T) {
	db := openTestDB(t)
	// Should be a no-op — no panic, no error.
	MigrateTaskIDs(db)
}

func TestMigrateTaskIDs_AlreadyMigrated(t *testing.T) {
	db := openTestDB(t)
	seedTask(t, db, "xh_yibu/proactive_reach", "xh_yibu")

	MigrateTaskIDs(db)

	// Row must still exist under the same ID.
	if !taskExists(db, "xh_yibu/proactive_reach") {
		t.Error("already-migrated row was removed or renamed")
	}
}

func TestMigrateTaskIDs_BareName(t *testing.T) {
	db := openTestDB(t)
	seedTask(t, db, "proactive_reach", "xh_yibu")

	MigrateTaskIDs(db)

	if taskExists(db, "proactive_reach") {
		t.Error("legacy bare-name row should have been renamed")
	}
	if !taskExists(db, "xh_yibu/proactive_reach") {
		t.Error("migrated row xh_yibu/proactive_reach not found")
	}
}

func TestMigrateTaskIDs_UUIDFilename(t *testing.T) {
	db := openTestDB(t)
	seedTask(t, db, "1ff20d20-4469-4346-8e96-3dda5d71c123", "investment")

	MigrateTaskIDs(db)

	if taskExists(db, "1ff20d20-4469-4346-8e96-3dda5d71c123") {
		t.Error("legacy UUID row should have been renamed")
	}
	if !taskExists(db, "investment/1ff20d20-4469-4346-8e96-3dda5d71c123") {
		t.Error("migrated UUID row not found")
	}
}

func TestMigrateTaskIDs_LegacyDotPrefix(t *testing.T) {
	// The transitional TaskFileID function created IDs like "ycm_mate.proactive_reach".
	// Migration must produce "ycm_mate/proactive_reach", not "ycm_mate/ycm_mate.proactive_reach".
	db := openTestDB(t)
	seedTask(t, db, "ycm_mate.proactive_reach", "ycm_mate")

	MigrateTaskIDs(db)

	if taskExists(db, "ycm_mate.proactive_reach") {
		t.Error("legacy dotted-prefix row should have been renamed")
	}
	if taskExists(db, "ycm_mate/ycm_mate.proactive_reach") {
		t.Error("double-namespaced ID must not be created")
	}
	if !taskExists(db, "ycm_mate/proactive_reach") {
		t.Error("migrated row ycm_mate/proactive_reach not found")
	}
}

func TestMigrateTaskIDs_ConflictDropsLegacy(t *testing.T) {
	// Both the legacy and canonical rows exist. Migration should drop the legacy row
	// and leave the canonical one untouched.
	db := openTestDB(t)
	seedTask(t, db, "proactive_reach", "xh_yibu")
	seedTask(t, db, "xh_yibu/proactive_reach", "xh_yibu")

	MigrateTaskIDs(db)

	if taskExists(db, "proactive_reach") {
		t.Error("stale legacy row should have been deleted on conflict")
	}
	if !taskExists(db, "xh_yibu/proactive_reach") {
		t.Error("canonical row must survive conflict resolution")
	}
}

func TestMigrateTaskIDs_EmptyAppID(t *testing.T) {
	// Rows with empty app_id are skipped — no panic, row remains unchanged.
	db := openTestDB(t)
	seedTask(t, db, "orphan_task", "")

	MigrateTaskIDs(db)

	if !taskExists(db, "orphan_task") {
		t.Error("row with empty app_id should be left untouched")
	}
}

func TestMigrateTaskIDs_Idempotent(t *testing.T) {
	// Running the migration twice must produce the same result.
	db := openTestDB(t)
	seedTask(t, db, "life_sim", "xh_yibu")

	MigrateTaskIDs(db)
	MigrateTaskIDs(db) // second run

	if taskExists(db, "life_sim") {
		t.Error("legacy row should be gone after second run")
	}
	if !taskExists(db, "xh_yibu/life_sim") {
		t.Error("canonical row should exist after second run")
	}
}

func TestMigrateTaskIDs_MultipleWorkspaces(t *testing.T) {
	// Two workspaces with the same filename: the collision that started it all.
	db := openTestDB(t)
	// Simulate the state before the fix: only one row exists (last writer won).
	// After migration both get their correct namespaced IDs — but since there is
	// only one legacy row, only xh_yibu gets migrated here. The ycm_mate row would
	// be created fresh by the watcher on the next fsnotify event.
	seedTask(t, db, "proactive_reach", "xh_yibu")

	MigrateTaskIDs(db)

	if !taskExists(db, "xh_yibu/proactive_reach") {
		t.Error("xh_yibu/proactive_reach should exist after migration")
	}
	if taskExists(db, "proactive_reach") {
		t.Error("bare proactive_reach should be gone after migration")
	}
}
