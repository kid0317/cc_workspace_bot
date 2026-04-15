package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/kid0317/cc-workspace-bot/internal/claude"
	"github.com/kid0317/cc-workspace-bot/internal/config"
	"github.com/kid0317/cc-workspace-bot/internal/db"
	"github.com/kid0317/cc-workspace-bot/internal/feishu"
	"github.com/kid0317/cc-workspace-bot/internal/model"
	"github.com/kid0317/cc-workspace-bot/internal/session"
	"github.com/kid0317/cc-workspace-bot/internal/task"
	"github.com/kid0317/cc-workspace-bot/internal/workspace"
	"gorm.io/gorm"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config.yaml")
	flag.Parse()

	// ── Logging ──────────────────────────────────────────────────
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	// ── Config ───────────────────────────────────────────────────
	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("load config", "err", err)
		os.Exit(1)
	}
	// H-4: validate all required fields at startup.
	if err := cfg.Validate(); err != nil {
		slog.Error("invalid config", "err", err)
		os.Exit(1)
	}

	// ── Database ──────────────────────────────────────────────────
	database, err := db.Open("bot.db")
	if err != nil {
		slog.Error("open database", "err", err)
		os.Exit(1)
	}
	if absDB, err := filepath.Abs("bot.db"); err == nil {
		cfg.DBPath = absDB
	}

	// ── Workspace init ────────────────────────────────────────────
	templateDir := filepath.Join("workspaces", "_template")
	for _, appCfg := range cfg.Apps {
		if err := workspace.Init(appCfg.WorkspaceDir, templateDir, appCfg.FeishuAppID, appCfg.FeishuAppSecret); err != nil {
			slog.Error("init workspace", "app", appCfg.ID, "err", err)
			os.Exit(1)
		}
		slog.Info("workspace ready", "app", appCfg.ID, "dir", appCfg.WorkspaceDir)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// ── Claude executor ───────────────────────────────────────────
	executor := claude.New(cfg)

	// ── Feishu receivers + senders ────────────────────────────────
	senders := make(map[string]*feishu.Sender, len(cfg.Apps))
	receivers := make([]*feishu.Receiver, 0, len(cfg.Apps))

	// C-1: use atomic.Pointer to avoid the data race between the main goroutine
	// writing fwd.target and the WS receive goroutines reading it.
	// The pointer is set before any goroutine is launched, so the atomic store
	// is strictly for correctness documentation — Go's memory model already
	// guarantees visibility across the go statement, but atomic makes the
	// race detector happy and the intent explicit.
	fwd := &dispatchForwarder{}

	for i := range cfg.Apps {
		appCfg := &cfg.Apps[i]
		recv := feishu.NewReceiver(appCfg, fwd)
		senders[appCfg.ID] = feishu.NewSender(recv.LarkClient())
		receivers = append(receivers, recv)
	}

	// ── Session manager ───────────────────────────────────────────
	sessionMgr := session.NewManager(cfg, database, executor, senders)
	// Store BEFORE launching any goroutine (Go memory model guarantees visibility
	// across go statements; atomic.Store documents the ordering intent).
	fwd.target.Store(sessionMgr)

	// ── Task subsystem ─────────────────────────────────────────────
	taskRunner := task.NewRunner(cfg, database, executor, senders)
	taskScheduler, err := task.NewScheduler(taskRunner)
	if err != nil {
		slog.Error("create task scheduler", "err", err)
		os.Exit(1)
	}

	taskWatcher, err := task.NewWatcher(taskScheduler, database)
	if err != nil {
		slog.Error("create task watcher", "err", err)
		os.Exit(1)
	}

	// Migrate legacy task IDs to the canonical "app_id/slug" format.
	// Must run before restoreEnabledTasks so that restored records use new IDs.
	task.MigrateTaskIDs(database)

	for _, appCfg := range cfg.Apps {
		tasksDir := filepath.Join(appCfg.WorkspaceDir, "tasks")
		if err := taskWatcher.AddDir(tasksDir, appCfg.ID); err != nil {
			// M-7: close the watcher FD on error to prevent leaks.
			taskWatcher.Close()
			slog.Error("watch tasks dir", "dir", tasksDir, "err", err)
			os.Exit(1)
		}
		restoreEnabledTasks(ctx, database, appCfg.ID, tasksDir, taskScheduler)
	}

	if _, err := task.NewCleaner(database, cfg.Apps, cfg.Cleanup, taskScheduler); err != nil {
		slog.Error("register cleanup job", "err", err)
		os.Exit(1)
	}

	taskScheduler.Start()
	taskWatcher.Start(ctx)

	// ── HTTP health check ─────────────────────────────────────────
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})
	// H-7: set read/write timeouts to prevent resource exhaustion.
	httpServer := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Server.Port),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		slog.Info("HTTP server listening", "port", cfg.Server.Port)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("HTTP server", "err", err)
		}
	}()

	// ── Start Feishu WS clients ───────────────────────────────────
	for _, recv := range receivers {
		r := recv
		go func() {
			if err := r.Start(ctx); err != nil {
				slog.Error("feishu WS client error", "err", err)
			}
		}()
	}

	slog.Info("cc-workspace-bot started", "apps", len(cfg.Apps))

	// ── Wait for shutdown signal ──────────────────────────────────
	<-ctx.Done()
	slog.Info("shutting down...")

	taskScheduler.Stop()

	// H-6: wait for all session workers to finish their in-flight requests.
	sessionMgr.Wait()

	// M-8: use a timeout context for HTTP shutdown and log any error.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		slog.Error("HTTP shutdown", "err", err)
	}

	slog.Info("bye")
}

// dispatchForwarder holds a pointer to session.Manager via atomic.Pointer.
// C-1: atomic load/store prevents any data race between the main goroutine
// (which stores after construction) and WS receive goroutines (which load on message).
type dispatchForwarder struct {
	target atomic.Pointer[session.Manager]
}

func (f *dispatchForwarder) Dispatch(ctx context.Context, msg *feishu.IncomingMessage) error {
	mgr := f.target.Load()
	if mgr == nil {
		return nil
	}
	return mgr.Dispatch(ctx, msg)
}

// syncAppChannels is intentionally a no-op: channel records are created on first message.
func syncAppChannels(_ *gorm.DB, _ *config.AppConfig) {}

// restoreEnabledTasks loads enabled tasks from DB and re-registers them with the scheduler.
// If no DB records exist, it falls back to scanning YAML files in tasksDir and upserting
// them — this handles the case where the tasks/ directory was deleted and recreated
// (causing fsnotify to lose its watch), leaving files on disk but no DB records.
// D3: logs WARN (not Info) when no tasks are found, since companion workspaces are
// expected to have at least one scheduled task; zero tasks indicates misconfiguration.
func restoreEnabledTasks(ctx context.Context, database *gorm.DB, appID string, tasksDir string, sched *task.Scheduler) {
	var tasks []model.Task
	if err := database.Where("app_id = ? AND enabled = ?", appID, true).Find(&tasks).Error; err != nil {
		slog.Error("restore tasks", "app_id", appID, "err", err)
		return
	}

	// Fallback: if DB has no enabled tasks, scan YAML files and upsert them.
	// This recovers from the scenario where tasks/ was deleted+recreated while
	// the server was running (fsnotify loses the watch on the new inode) and the
	// server is subsequently restarted with an empty tasks table.
	if len(tasks) == 0 {
		tasks = syncYAMLTasksToDB(ctx, database, appID, tasksDir)
	}

	for i := range tasks {
		t := &tasks[i]
		if err := sched.Add(ctx, t); err != nil {
			slog.Warn("restore task job", "task_id", t.ID, "err", err)
		}
	}
	if len(tasks) == 0 {
		slog.Warn("restored tasks: none found — proactive features disabled for this workspace",
			"app_id", appID, "tasks_dir", tasksDir)
	} else {
		slog.Info("restored tasks", "app_id", appID, "count", len(tasks), "tasks_dir", tasksDir)
	}
}

// syncYAMLTasksToDB scans tasksDir for *.yaml files, upserts each valid enabled task
// into the DB, and returns the resulting task list. Errors per file are logged and skipped.
func syncYAMLTasksToDB(ctx context.Context, database *gorm.DB, appID string, tasksDir string) []model.Task {
	entries, err := os.ReadDir(tasksDir)
	if err != nil {
		// Directory may not exist yet — not an error worth surfacing at WARN level.
		return nil
	}

	var restored []model.Task
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		path := filepath.Join(tasksDir, e.Name())
		t, err := task.LoadYAML(path, appID)
		if err != nil {
			slog.Warn("restore tasks: skip invalid yaml", "file", path, "err", err)
			continue
		}
		if !t.Enabled {
			continue
		}
		// Upsert: restore previously-deleted records (Unscoped) or create new ones.
		var existing model.Task
		dbErr := database.Unscoped().Where("id = ?", t.ID).First(&existing).Error
		if errors.Is(dbErr, gorm.ErrRecordNotFound) {
			if err := database.Create(t).Error; err != nil {
				slog.Error("restore tasks: create task", "task_id", t.ID, "err", err)
				continue
			}
		} else if dbErr != nil {
			slog.Error("restore tasks: query task", "task_id", t.ID, "err", dbErr)
			continue
		} else {
			updates := map[string]interface{}{
				"name":        t.Name,
				"app_id":      t.AppID,
				"cron_expr":   t.CronExpr,
				"target_type": t.TargetType,
				"target_id":   t.TargetID,
				"prompt":      t.Prompt,
				"enabled":     t.Enabled,
				"send_output": t.SendOutput,
				"deleted_at":  gorm.Expr("NULL"),
			}
			if err := database.Model(&existing).Unscoped().Updates(updates).Error; err != nil {
				slog.Error("restore tasks: update task", "task_id", t.ID, "err", err)
				continue
			}
		}
		slog.Info("restore tasks: recovered from yaml", "task_id", t.ID, "name", t.Name)
		restored = append(restored, *t)
	}
	return restored
}
