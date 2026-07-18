package db

import (
	"fmt"
	"log/slog"
	"path/filepath"

	"gorm.io/gorm"

	"github.com/kid0317/cc-workspace-bot/internal/config"
)

// Registry holds one *gorm.DB per app and owns their lifecycle.
// The map is populated once at startup and thereafter only read,
// so no mutex is required for Get.
type Registry struct {
	dbs map[string]*gorm.DB
}

// NewRegistry opens one SQLite DB per app under {workspaceDir}/bot.db.
// It also sets app.DBPath on each AppConfig (absolute path) as a side effect,
// so that SESSION_CONTEXT.md written by the executor contains the correct path.
// WorkspaceDir must be non-empty for every app; NewRegistry returns an error otherwise.
func NewRegistry(apps []config.AppConfig) (*Registry, error) {
	dbs := make(map[string]*gorm.DB, len(apps))
	for i := range apps {
		app := &apps[i]
		if app.WorkspaceDir == "" {
			return nil, fmt.Errorf("app %q has empty workspace_dir", app.ID)
		}
		dbFile := filepath.Join(app.WorkspaceDir, "bot.db")
		appDB, err := Open(dbFile)
		if err != nil {
			return nil, fmt.Errorf("open db for app %q: %w", app.ID, err)
		}
		abs, err := filepath.Abs(dbFile)
		if err != nil {
			abs = dbFile
		}
		app.DBPath = abs
		dbs[app.ID] = appDB
	}
	return &Registry{dbs: dbs}, nil
}

// Get returns the DB for appID.
// Returns an error if appID was not registered at startup.
// Callers must not proceed with a nil DB — drop or reject the operation on error.
func (r *Registry) Get(appID string) (*gorm.DB, error) {
	appDB, ok := r.dbs[appID]
	if !ok {
		return nil, fmt.Errorf("no DB registered for app %q", appID)
	}
	return appDB, nil
}

// All returns the internal map for iteration (e.g. MigrateTaskIDs).
// The returned map must not be mutated.
func (r *Registry) All() map[string]*gorm.DB {
	return r.dbs
}

// NewRegistryFromMap creates a Registry backed by an existing app_id → *gorm.DB map.
// Intended for tests only — does not set DBPath on any AppConfig.
func NewRegistryFromMap(dbs map[string]*gorm.DB) *Registry {
	return &Registry{dbs: dbs}
}

// Close closes all underlying sql.DB connection pools.
// Call once during graceful shutdown after all workers have stopped.
func (r *Registry) Close() {
	for id, appDB := range r.dbs {
		sqlDB, err := appDB.DB()
		if err != nil {
			slog.Error("registry: get sql.DB for close", "app", id, "err", err)
			continue
		}
		if err := sqlDB.Close(); err != nil {
			slog.Error("registry: close db", "app", id, "err", err)
		}
	}
}
