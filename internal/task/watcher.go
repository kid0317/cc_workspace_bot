package task

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"gorm.io/gorm"

	"github.com/kid0317/cc-workspace-bot/internal/model"
)

// parseErrKey dedupes parse-error log lines. Keyed on (path, content hash, msg)
// so that an identical error on the same file content is logged once, but a
// content change (even one that preserves mtime, e.g. via `touch -r`) or a
// different error class produces a new log line.
type parseErrKey struct {
	path        string
	contentHash string
	msg         string
}

// Watcher monitors the tasks/ directory of each workspace and syncs YAML files to DB.
type Watcher struct {
	scheduler *Scheduler
	db        *gorm.DB
	watcher   *fsnotify.Watcher
	mu        sync.RWMutex
	dirAppIDs map[string]string // watched dir path → workspace appID

	errMu    sync.Mutex
	errCache map[parseErrKey]struct{}
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
		errCache:  make(map[parseErrKey]struct{}),
	}, nil
}

// maxErrCacheEntries caps errCache size so a high-churn tasks/ directory
// (rapid create+delete with distinct content hashes) cannot exhaust memory
// between 2-minute prune cycles. At ~200 B per key, 1000 entries is ~200 KiB —
// orders of magnitude below any realistic operational footprint, and the cap
// only trips in pathological / adversarial scenarios where we'd rather lose a
// log line than the process.
const maxErrCacheEntries = 1000

// shouldLogParseErr returns true when (path, contentHash, msg) has not been
// logged before, and records the key. Keeps repeated scans from spamming the
// log with the same error — but a file change or a new error class breaks
// through immediately. Returns false (silently drops the log line) when the
// cache is saturated; the saturation itself is logged once per rescan.
func (w *Watcher) shouldLogParseErr(key parseErrKey) bool {
	w.errMu.Lock()
	defer w.errMu.Unlock()
	if _, ok := w.errCache[key]; ok {
		return false
	}
	if len(w.errCache) >= maxErrCacheEntries {
		return false
	}
	w.errCache[key] = struct{}{}
	return true
}

// forgetPath drops every cached error entry for a given path. Called when a
// file parses cleanly or is removed, so subsequent real errors on the same
// path are not suppressed by a stale cache entry, and so the cache shrinks
// at the same pace YAML files stabilise — not only on the 2-minute ticker.
func (w *Watcher) forgetPath(path string) {
	w.errMu.Lock()
	defer w.errMu.Unlock()
	for k := range w.errCache {
		if k.path == path {
			delete(w.errCache, k)
		}
	}
}

// pruneErrCache removes entries whose path is not in keepPaths. Called at the
// end of rescanAll so the cache size stays bounded as YAML files come and go.
func (w *Watcher) pruneErrCache(keepPaths map[string]struct{}) {
	w.errMu.Lock()
	defer w.errMu.Unlock()
	for k := range w.errCache {
		if _, ok := keepPaths[k.path]; !ok {
			delete(w.errCache, k)
		}
	}
}

// hashContent returns a short hex prefix of the SHA-256 of data. 16 chars is
// well beyond the collision risk for per-file dedup — the domain is "same file
// vs. modified file", not cryptographic.
func hashContent(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
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
// It also runs a periodic rescan (every 2 minutes) to recover watches lost
// when a tasks/ directory is deleted and recreated (e.g. during workspace
// re-initialisation). inotify watches are inode-based: a new directory at
// the same path has a different inode and is not automatically re-watched.
func (w *Watcher) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(2 * time.Minute)
		defer ticker.Stop()
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

			case <-ticker.C:
				w.rescanAll(ctx)

			case <-ctx.Done():
				_ = w.watcher.Close()
				return
			}
		}
	}()
}

// rescanAll verifies that every registered tasks/ directory is still being
// watched (its inode may have changed after a delete+recreate), re-adds any
// that have fallen off, and syncs all YAML files found on disk.
func (w *Watcher) rescanAll(ctx context.Context) {
	w.mu.RLock()
	dirs := make(map[string]string, len(w.dirAppIDs))
	for dir, appID := range w.dirAppIDs {
		dirs[dir] = appID
	}
	w.mu.RUnlock()

	watched := make(map[string]struct{})
	for _, p := range w.watcher.WatchList() {
		watched[p] = struct{}{}
	}

	seenPaths := make(map[string]struct{})
	for dir, appID := range dirs {
		if _, ok := watched[dir]; !ok {
			// Directory is no longer watched (deleted+recreated or inotify limit).
			// Re-create if missing, then re-add the watch.
			if err := os.MkdirAll(dir, 0o755); err != nil {
				slog.Error("task watcher: rescan mkdir", "dir", dir, "err", err)
				continue
			}
			if err := w.watcher.Add(dir); err != nil {
				slog.Error("task watcher: rescan re-watch", "dir", dir, "err", err)
				continue
			}
			slog.Warn("task watcher: re-established lost watch", "dir", dir, "app", appID)
		}
		// Sync all YAML files currently on disk for this directory.
		w.syncDir(ctx, dir, appID, seenPaths)
	}
	// Drop cache entries for files that no longer exist anywhere so the map
	// stays bounded even if YAML files are churned over time.
	w.pruneErrCache(seenPaths)
}

// syncDir loads every YAML file in dir and upserts it into the scheduler.
// Used both during rescan and on startup to catch files written while the
// watch was not yet active. seenPaths, if non-nil, is populated with every
// path visited (valid or invalid) so rescanAll can prune stale cache entries.
func (w *Watcher) syncDir(ctx context.Context, dir, appID string, seenPaths map[string]struct{}) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Error("task watcher: sync dir read", "dir", dir, "err", err)
		}
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if seenPaths != nil {
			seenPaths[path] = struct{}{}
		}

		data, readErr := os.ReadFile(path)
		if readErr != nil {
			key := parseErrKey{path: path, contentHash: "", msg: readErr.Error()}
			if w.shouldLogParseErr(key) {
				slog.Error("task watcher: sync dir read", "file", path, "err", readErr)
			}
			continue
		}
		task, err := LoadYAMLFromBytes(data, path, appID)
		if err != nil {
			key := parseErrKey{path: path, contentHash: hashContent(data), msg: err.Error()}
			if w.shouldLogParseErr(key) {
				slog.Error("task watcher: sync dir parse yaml", "file", path, "err", err)
			}
			continue
		}
		// Parse succeeded — drop any stale error entries so a later regression
		// on this path is not silently deduped against an earlier error.
		w.forgetPath(path)
		w.upsertTask(ctx, task)
	}
}

// ListYAMLs scans every registered tasks/ directory and returns the parse
// outcome for each YAML file. Intended for startup summaries and CLI tools
// (cmd/dbcheck) — it intentionally bypasses the log dedup cache so every call
// produces a complete, fresh report.
type YAMLParseResult struct {
	AppID string
	Path  string
	Task  *model.Task // nil when Err != nil
	Err   error
}

// ListYAMLs returns the parse result for every YAML file under the watcher's
// registered task directories. Does not mutate DB or scheduler state.
func (w *Watcher) ListYAMLs() []YAMLParseResult {
	w.mu.RLock()
	dirs := make(map[string]string, len(w.dirAppIDs))
	for dir, appID := range w.dirAppIDs {
		dirs[dir] = appID
	}
	w.mu.RUnlock()

	var results []YAMLParseResult
	for dir, appID := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
				continue
			}
			path := filepath.Join(dir, e.Name())
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				results = append(results, YAMLParseResult{AppID: appID, Path: path, Err: readErr})
				continue
			}
			t, err := LoadYAMLFromBytes(data, path, appID)
			results = append(results, YAMLParseResult{AppID: appID, Path: path, Task: t, Err: err})
		}
	}
	return results
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
		data, readErr := os.ReadFile(event.Name)
		if readErr != nil {
			key := parseErrKey{path: event.Name, contentHash: "", msg: readErr.Error()}
			if w.shouldLogParseErr(key) {
				slog.Error("task watcher: read yaml", "err", readErr, "file", event.Name)
			}
			return
		}
		task, err := LoadYAMLFromBytes(data, event.Name, appID)
		if err != nil {
			key := parseErrKey{path: event.Name, contentHash: hashContent(data), msg: err.Error()}
			if w.shouldLogParseErr(key) {
				slog.Error("task watcher: parse yaml", "err", err, "file", event.Name)
			}
			return
		}
		// Success path: drop any prior error entries for this file so (a) the
		// cache shrinks as files stabilise, and (b) a new regression on the
		// same path is not suppressed by a stale hash match.
		w.forgetPath(event.Name)
		w.upsertTask(ctx, task)

	case event.Op&fsnotify.Remove != 0:
		if appID == "" {
			slog.Warn("task watcher: remove event for unregistered dir, skipping", "file", event.Name)
			return
		}
		base := strings.TrimSuffix(filepath.Base(event.Name), ".yaml")
		w.removeTask(appID + "/" + base)
		w.forgetPath(event.Name)

	case event.Op&fsnotify.Rename != 0:
		if appID == "" {
			slog.Warn("task watcher: rename event for unregistered dir, skipping", "file", event.Name)
			return
		}
		base := strings.TrimSuffix(filepath.Base(event.Name), ".yaml")
		w.removeTask(appID + "/" + base)
		w.forgetPath(event.Name)
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
