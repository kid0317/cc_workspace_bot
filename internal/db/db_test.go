package db_test

import (
	"path/filepath"
	"testing"

	"github.com/kid0317/cc-workspace-bot/internal/db"
	"github.com/kid0317/cc-workspace-bot/internal/model"
)

func TestOpen_CreatesTablesAndMigrates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	database, err := db.Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	// Verify all expected tables exist by checking GORM can create records.
	ch := model.Channel{ChannelKey: "p2p:u1:a1", AppID: "a1", ChatType: "p2p", ChatID: "u1"}
	if err := database.Create(&ch).Error; err != nil {
		t.Errorf("create Channel: %v", err)
	}

	sess := model.Session{ID: "sess-1", ChannelKey: "p2p:u1:a1", Status: "active"}
	if err := database.Create(&sess).Error; err != nil {
		t.Errorf("create Session: %v", err)
	}

	msg := model.Message{ID: "msg-1", SessionID: "sess-1", Role: "user", Content: "hi"}
	if err := database.Create(&msg).Error; err != nil {
		t.Errorf("create Message: %v", err)
	}

	task := model.Task{ID: "task-1", AppID: "a1", Name: "test", CronExpr: "* * * * *", Enabled: true}
	if err := database.Create(&task).Error; err != nil {
		t.Errorf("create Task: %v", err)
	}
}

func TestOpen_Idempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")

	db1, err := db.Open(path)
	if err != nil {
		t.Fatalf("first Open() error = %v", err)
	}
	_ = db1

	// Second open should also succeed (AutoMigrate is idempotent).
	db2, err := db.Open(path)
	if err != nil {
		t.Fatalf("second Open() error = %v", err)
	}
	_ = db2
}

func TestOpen_InvalidPath(t *testing.T) {
	// A path in a non-existent directory should fail.
	_, err := db.Open("/nonexistent/dir/test.db")
	if err == nil {
		t.Error("Open() with invalid path should return error")
	}
}
