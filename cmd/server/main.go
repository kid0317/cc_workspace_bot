package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
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

	// ── Workspace init ────────────────────────────────────────────
	templateDir := filepath.Join("workspaces", "_template")
	for _, appCfg := range cfg.Apps {
		if err := workspace.Init(appCfg.WorkspaceDir, templateDir); err != nil {
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

	for _, appCfg := range cfg.Apps {
		tasksDir := filepath.Join(appCfg.WorkspaceDir, "tasks")
		if err := taskWatcher.AddDir(tasksDir); err != nil {
			// M-7: close the watcher FD on error to prevent leaks.
			taskWatcher.Close()
			slog.Error("watch tasks dir", "dir", tasksDir, "err", err)
			os.Exit(1)
		}
		restoreEnabledTasks(ctx, database, appCfg.ID, taskScheduler)
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
func restoreEnabledTasks(ctx context.Context, database *gorm.DB, appID string, sched *task.Scheduler) {
	var tasks []model.Task
	if err := database.Where("app_id = ? AND enabled = ?", appID, true).Find(&tasks).Error; err != nil {
		slog.Error("restore tasks", "app_id", appID, "err", err)
		return
	}
	for i := range tasks {
		t := &tasks[i]
		if err := sched.Add(ctx, t); err != nil {
			slog.Warn("restore task job", "task_id", t.ID, "err", err)
		}
	}
	slog.Info("restored tasks", "app_id", appID, "count", len(tasks))
}
