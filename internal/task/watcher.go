package task

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"
	"gorm.io/gorm"

	"github.com/kid0317/cc-workspace-bot/internal/model"
)

// Watcher monitors the tasks/ directory of each workspace and syncs YAML files to DB.
type Watcher struct {
	scheduler *Scheduler
	db        *gorm.DB
	watcher   *fsnotify.Watcher
	mu        sync.RWMutex
	dirAppIDs map[string]string // watched dir path → workspace appID
}

// NewWatcher creates a Watcher. The caller must call Start before AddDir,
// or call Close if Start is never called, to avoid leaking the fsnotify FD.
func NewWatcher(scheduler *Scheduler, db *gorm.DB) (*Watcher, error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return &Watcher{
		scheduler: scheduler,
		db:        db,
		watcher:   fw,
		dirAppIDs: make(map[string]string),
	}, nil
}

// Close releases the underlying fsnotify file descriptor.
// Call this if Start was never invoked (e.g. in error-handling paths).
func (w *Watcher) Close() {
	_ = w.watcher.Close()
}

// AddDir registers a tasks/ directory to watch.
// appID is the workspace ID (e.g. "xh_yibu") — it is injected into every
// task loaded from this directory, overriding whatever app_id the YAML contains.
// This eliminates the class of bugs where a YAML file stores the wrong app_id.
func (w *Watcher) AddDir(dir string, appID string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	w.mu.Lock()
	w.dirAppIDs[dir] = appID
	w.mu.Unlock()
	return w.watcher.Add(dir)
}

// Start begins processing fsnotify events until ctx is cancelled.
func (w *Watcher) Start(ctx context.Context) {
	go func() {
		for {
			select {
			case event, ok := <-w.watcher.Events:
				if !ok {
					return
				}
				if !strings.HasSuffix(event.Name, ".yaml") {
					continue
				}
				w.handleEvent(ctx, event)

			case err, ok := <-w.watcher.Errors:
				if !ok {
					return
				}
				slog.Error("task watcher error", "err", err)

			case <-ctx.Done():
				_ = w.watcher.Close()
				return
			}
		}
	}()
}

func (w *Watcher) handleEvent(ctx context.Context, event fsnotify.Event) {
	if !strings.HasSuffix(event.Name, ".yaml") {
		return
	}

	dir := filepath.Dir(event.Name)
	w.mu.RLock()
	appID := w.dirAppIDs[dir]
	w.mu.RUnlock()

	switch {
	case event.Op&(fsnotify.Create|fsnotify.Write) != 0:
		task, err := LoadYAML(event.Name, appID)
		if err != nil {
			slog.Error("task watcher: parse yaml", "err", err, "file", event.Name)
			return
		}
		w.upsertTask(ctx, task)

	case event.Op&fsnotify.Remove != 0:
		// Derive task ID from filename (UUID).
		base := filepath.Base(event.Name)
		id := strings.TrimSuffix(base, ".yaml")
		w.removeTask(id)

	case event.Op&fsnotify.Rename != 0:
		base := filepath.Base(event.Name)
		id := strings.TrimSuffix(base, ".yaml")
		w.removeTask(id)
	}
}

func (w *Watcher) upsertTask(ctx context.Context, task *model.Task) {
	var existing model.Task
	err := w.db.Unscoped().Where("id = ?", task.ID).First(&existing).Error

	// C-3: use errors.Is for GORM sentinel errors.
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if err := w.db.Create(task).Error; err != nil {
			slog.Error("task watcher: create task in DB", "err", err)
			return
		}
	} else if err != nil {
		slog.Error("task watcher: query task", "err", err)
		return
	} else {
		// Update existing. Column names must match the GORM snake_case mapping of
		// model.Task fields. deleted_at must be reset via gorm.Expr("NULL") rather
		// than nil — for struct-based updates GORM skips zero values, but for maps
		// behaviour is driver-dependent and unreliable for nullable columns.
		// gorm.Expr("NULL") is explicit and works regardless of update mode.
		updates := map[string]interface{}{
			"name":        task.Name,
			"app_id":      task.AppID,
			"cron_expr":   task.CronExpr,
			"target_type": task.TargetType,
			"target_id":   task.TargetID,
			"prompt":      task.Prompt,
			"enabled":     task.Enabled,
			"send_output": task.SendOutput,
			"deleted_at":  gorm.Expr("NULL"),
		}
		if err := w.db.Model(&existing).Unscoped().Updates(updates).Error; err != nil {
			slog.Error("task watcher: update task in DB", "err", err)
			return
		}
	}

	// Re-register with scheduler.
	w.scheduler.Remove(task.ID)
	if task.Enabled {
		if err := w.scheduler.Add(ctx, task); err != nil {
			slog.Error("task watcher: register job", "err", err, "task_id", task.ID)
		}
	}

	slog.Info("task watcher: upserted task", "task_id", task.ID, "name", task.Name, "enabled", task.Enabled)
}

func (w *Watcher) removeTask(id string) {
	w.scheduler.Remove(id)
	if err := w.db.Where("id = ?", id).Delete(&model.Task{}).Error; err != nil {
		slog.Error("task watcher: delete task from DB", "task_id", id, "err", err)
	}
	slog.Info("task watcher: removed task", "task_id", id)
}
