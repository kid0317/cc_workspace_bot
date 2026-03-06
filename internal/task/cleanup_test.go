package task

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/kid0317/cc-workspace-bot/internal/config"
	"github.com/kid0317/cc-workspace-bot/internal/db"
	"github.com/kid0317/cc-workspace-bot/internal/model"
)

// newTestDB opens a fresh SQLite database in a temp file.
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	database, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	return database
}

// insertChannel inserts a channel record for the given app.
func insertChannel(t *testing.T, database *gorm.DB, channelKey, appID string) {
	t.Helper()
	ch := &model.Channel{
		ChannelKey: channelKey,
		AppID:      appID,
		ChatType:   "p2p",
		ChatID:     "user1",
	}
	if err := database.Create(ch).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
}

// insertSession inserts a session and forces specific created_at/updated_at via raw SQL.
func insertSession(t *testing.T, database *gorm.DB, id, channelKey, status string, createdAt, updatedAt time.Time) {
	t.Helper()
	sess := &model.Session{
		ID:         id,
		ChannelKey: channelKey,
		Status:     status,
	}
	if err := database.Create(sess).Error; err != nil {
		t.Fatalf("create session %s: %v", id, err)
	}
	if err := database.Exec(
		"UPDATE sessions SET created_at = ?, updated_at = ? WHERE id = ?",
		createdAt, updatedAt, id,
	).Error; err != nil {
		t.Fatalf("update timestamps for session %s: %v", id, err)
	}
}

// makeAttachDir creates the session attachments directory and a dummy file.
func makeAttachDir(t *testing.T, workspaceDir, sessionID string) string {
	t.Helper()
	attachDir := filepath.Join(workspaceDir, "sessions", sessionID, "attachments")
	if err := os.MkdirAll(attachDir, 0o755); err != nil {
		t.Fatalf("mkdir attachments: %v", err)
	}
	if err := os.WriteFile(filepath.Join(attachDir, "file.jpg"), []byte("data"), 0o644); err != nil {
		t.Fatalf("write dummy file: %v", err)
	}
	return attachDir
}

func TestCleaner_CleanApp(t *testing.T) {
	now := time.Now()
	retentionDays := 7
	maxDays := 30
	retentionCutoff := now.Add(-time.Duration(retentionDays) * 24 * time.Hour)
	maxCutoff := now.Add(-time.Duration(maxDays) * 24 * time.Hour)

	tests := []struct {
		name       string
		status     string
		createdAt  time.Time
		updatedAt  time.Time
		makeAttach bool
		wantCount  int
	}{
		{
			name:       "archived + updated_at old → delete",
			status:     "archived",
			createdAt:  now.Add(-15 * 24 * time.Hour),
			updatedAt:  now.Add(-10 * 24 * time.Hour),
			makeAttach: true,
			wantCount:  1,
		},
		{
			name:       "archived + updated_at recent → keep",
			status:     "archived",
			createdAt:  now.Add(-10 * 24 * time.Hour),
			updatedAt:  now.Add(-3 * 24 * time.Hour), // newer than retention; created_at < maxDays
			makeAttach: true,
			wantCount:  0,
		},
		{
			name:       "active + created_at exceeds max_days → delete",
			status:     "active",
			createdAt:  now.Add(-35 * 24 * time.Hour),
			updatedAt:  now.Add(-35 * 24 * time.Hour),
			makeAttach: true,
			wantCount:  1,
		},
		{
			name:       "active + recent → keep",
			status:     "active",
			createdAt:  now.Add(-1 * 24 * time.Hour),
			updatedAt:  now.Add(-1 * 24 * time.Hour),
			makeAttach: true,
			wantCount:  0,
		},
		{
			name:       "matches criteria but no attachments dir → count 0",
			status:     "archived",
			createdAt:  now.Add(-15 * 24 * time.Hour),
			updatedAt:  now.Add(-10 * 24 * time.Hour),
			makeAttach: false,
			wantCount:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workspaceDir := t.TempDir()
			database := newTestDB(t)

			appCfg := config.AppConfig{
				ID:           "test-app",
				WorkspaceDir: workspaceDir,
			}

			channelKey := "p2p:user1:test-app"
			// Use test name as unique session ID suffix (sanitise spaces).
			sessionID := "sess-" + filepath.Base(t.TempDir())

			insertChannel(t, database, channelKey, "test-app")
			insertSession(t, database, sessionID, channelKey, tt.status, tt.createdAt, tt.updatedAt)

			if tt.makeAttach {
				makeAttachDir(t, workspaceDir, sessionID)
			}

			c := &Cleaner{
				db:   database,
				apps: []config.AppConfig{appCfg},
				cfg: config.CleanupConfig{
					AttachmentsRetentionDays: retentionDays,
					AttachmentsMaxDays:       maxDays,
				},
			}

			got := c.cleanApp(&appCfg, retentionCutoff, maxCutoff)
			if got != tt.wantCount {
				t.Errorf("cleanApp() = %d, want %d", got, tt.wantCount)
			}

			if tt.wantCount > 0 {
				attachDir := filepath.Join(workspaceDir, "sessions", sessionID, "attachments")
				if _, err := os.Stat(attachDir); !os.IsNotExist(err) {
					t.Errorf("attachments dir should have been removed")
				}
			}
		})
	}
}

func TestCleaner_Run_MultipleApps(t *testing.T) {
	now := time.Now()
	database := newTestDB(t)

	ws1 := t.TempDir()
	ws2 := t.TempDir()
	apps := []config.AppConfig{
		{ID: "app1", WorkspaceDir: ws1},
		{ID: "app2", WorkspaceDir: ws2},
	}

	for i, app := range apps {
		ck := "p2p:user::" + app.ID
		sid := "sess-" + app.ID
		insertChannel(t, database, ck, app.ID)
		insertSession(t, database, sid, ck, "archived",
			now.Add(-20*24*time.Hour),
			now.Add(-10*24*time.Hour),
		)
		makeAttachDir(t, app.WorkspaceDir, sid)
		_ = i
	}

	c := &Cleaner{
		db:   database,
		apps: apps,
		cfg:  config.CleanupConfig{AttachmentsRetentionDays: 7, AttachmentsMaxDays: 30},
	}
	c.Run()

	for _, app := range apps {
		attachDir := filepath.Join(app.WorkspaceDir, "sessions", "sess-"+app.ID, "attachments")
		if _, err := os.Stat(attachDir); !os.IsNotExist(err) {
			t.Errorf("app %s attachments should be removed", app.ID)
		}
	}
}

func TestCleaner_CleanApp_WrongApp(t *testing.T) {
	now := time.Now()
	database := newTestDB(t)
	workspaceDir := t.TempDir()

	insertChannel(t, database, "p2p:u1:other-app", "other-app")
	insertSession(t, database, "sess-other", "p2p:u1:other-app", "archived",
		now.Add(-20*24*time.Hour), now.Add(-10*24*time.Hour),
	)
	makeAttachDir(t, workspaceDir, "sess-other")

	appCfg := config.AppConfig{ID: "my-app", WorkspaceDir: workspaceDir}
	c := &Cleaner{
		db:   database,
		apps: []config.AppConfig{appCfg},
		cfg:  config.CleanupConfig{AttachmentsRetentionDays: 7, AttachmentsMaxDays: 30},
	}

	got := c.cleanApp(&appCfg, now.Add(-7*24*time.Hour), now.Add(-30*24*time.Hour))
	if got != 0 {
		t.Errorf("cleanApp() = %d for different app, want 0", got)
	}
}
