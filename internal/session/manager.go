package session

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/kid0317/cc-workspace-bot/internal/claude"
	"github.com/kid0317/cc-workspace-bot/internal/config"
	"github.com/kid0317/cc-workspace-bot/internal/feishu"
)

// Manager maps channel keys to their Workers and lazily starts them.
type Manager struct {
	cfg         *config.Config
	appRegistry map[string]*config.AppConfig // app_id -> AppConfig
	db          *gorm.DB
	executor    *claude.Executor
	senders     map[string]*feishu.Sender // app_id -> Sender

	workers sync.Map // channelKey -> *Worker

	// mu guards the check-then-act in getOrCreateWorker.
	// sync.Map is concurrent-safe for individual operations, but the Load+Store
	// pair needs to be atomic to prevent duplicate worker creation.
	mu sync.Mutex

	// wg tracks all running worker goroutines so Wait() can block until they exit.
	wg sync.WaitGroup
}

// NewManager creates a Manager. senders maps app_id to feishu.Sender.
func NewManager(
	cfg *config.Config,
	db *gorm.DB,
	executor *claude.Executor,
	senders map[string]*feishu.Sender,
) *Manager {
	registry := make(map[string]*config.AppConfig, len(cfg.Apps))
	for i := range cfg.Apps {
		a := &cfg.Apps[i]
		registry[a.ID] = a
	}
	return &Manager{
		cfg:         cfg,
		appRegistry: registry,
		db:          db,
		executor:    executor,
		senders:     senders,
	}
}

// Dispatch routes an IncomingMessage to the appropriate Worker.
// It implements feishu.Dispatcher.
func (m *Manager) Dispatch(ctx context.Context, msg *feishu.IncomingMessage) error {
	worker := m.getOrCreateWorker(ctx, msg)
	if worker == nil {
		slog.Error("session: no worker for message", "channel", msg.ChannelKey)
		return nil
	}

	select {
	case worker.queue <- msg:
	default:
		slog.Warn("session: worker queue full, dropping message", "channel", msg.ChannelKey)
	}
	return nil
}

// Wait blocks until all active session workers have exited.
// Call this after cancelling the context to achieve a clean shutdown.
func (m *Manager) Wait() {
	m.wg.Wait()
}

// getOrCreateWorker returns the existing Worker or starts a new one.
func (m *Manager) getOrCreateWorker(ctx context.Context, msg *feishu.IncomingMessage) *Worker {
	if w, ok := m.workers.Load(msg.ChannelKey); ok {
		return w.(*Worker)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check after acquiring lock.
	if w, ok := m.workers.Load(msg.ChannelKey); ok {
		return w.(*Worker)
	}

	appCfg, ok := m.appRegistry[msg.AppID]
	if !ok {
		slog.Error("session: unknown app_id", "app_id", msg.AppID)
		return nil
	}

	sender, ok := m.senders[msg.AppID]
	if !ok {
		slog.Error("session: no sender for app", "app_id", msg.AppID)
		return nil
	}

	idleTimeout := time.Duration(m.cfg.Session.WorkerIdleTimeoutMinutes) * time.Minute
	worker := newWorker(msg.ChannelKey, appCfg, m.db, m.executor, sender, idleTimeout)

	m.workers.Store(msg.ChannelKey, worker)

	// H-6: track each worker goroutine so Wait() can block until all finish.
	m.wg.Add(1)
	go worker.run(ctx, func() {
		m.wg.Done()
		m.workers.Delete(msg.ChannelKey)
		slog.Info("session worker exited", "channel", msg.ChannelKey)
	})

	return worker
}
