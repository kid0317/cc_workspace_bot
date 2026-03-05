package db

import (
	"fmt"

	"github.com/glebarez/sqlite"
	"github.com/kid0317/cc-workspace-bot/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Open opens (or creates) the SQLite database at path and runs AutoMigrate.
func Open(path string) (*gorm.DB, error) {
	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_busy_timeout=5000", path)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	if err := db.AutoMigrate(
		&model.Channel{},
		&model.Session{},
		&model.Message{},
		&model.Task{},
	); err != nil {
		return nil, fmt.Errorf("migrate database: %w", err)
	}

	return db, nil
}
