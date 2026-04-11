package model

import (
	"time"

	"gorm.io/gorm"
)

// Channel represents a stable Feishu channel (p2p / group / thread).
type Channel struct {
	ChannelKey string `gorm:"primaryKey"`
	AppID      string `gorm:"index;not null"`
	ChatType   string `gorm:"not null"` // p2p / group / topic_group
	ChatID     string `gorm:"not null"`
	ThreadID   string // only set for topic_group
	CreatedAt  time.Time
}

// Session represents one conversation session within a channel.
type Session struct {
	ID              string `gorm:"primaryKey"`
	ChannelKey      string `gorm:"index;not null"`
	ClaudeSessionID string // --resume parameter; empty = new context
	Status          string `gorm:"not null;default:'active'"` // active / archived
	CreatedBy       string // open_id of user who created the session
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Message records a single message in a session.
type Message struct {
	ID          string `gorm:"primaryKey"`
	SessionID   string `gorm:"index;not null"`
	SenderID    string // open_id of sender (empty for assistant)
	Role        string `gorm:"not null"` // user / assistant
	Content     string `gorm:"type:text"`
	FeishuMsgID string // original Feishu message_id
	CreatedAt   time.Time
}

// Task mirrors a tasks/<uuid>.yaml file at runtime.
type Task struct {
	ID         string `gorm:"primaryKey"`
	AppID      string `gorm:"index;not null"`
	Name       string
	CronExpr   string
	TargetType string // p2p / group
	TargetID   string // open_id or chat_id
	Prompt     string `gorm:"type:text"`
	Enabled    bool   `gorm:"default:true"`
	// SendOutput controls whether Claude's text output is forwarded to the user.
	// Set to false for background tasks (memory_distill, life_sim) that must
	// never surface internal results to users.
	SendOutput bool       `gorm:"default:true"`
	CreatedBy  string
	CreatedAt  time.Time
	LastRunAt  *time.Time
	DeletedAt  gorm.DeletedAt `gorm:"index"`
}

// TaskYAML is the on-disk YAML representation of a task file.
type TaskYAML struct {
	// Deprecated: id in YAML is ignored. The task ID is derived from the
	// filename by LoadYAML (e.g. "memory_distill.yaml" → ID "memory_distill").
	// This ensures watcher.removeTask can always locate the task by ID.
	ID string `yaml:"id"`
	// Deprecated: app_id in YAML is ignored. The workspace ID is derived from
	// the file path by the Watcher and injected at load time via LoadYAML.
	// Setting this field has no effect.
	AppID      string    `yaml:"app_id"`
	Name       string    `yaml:"name"`
	Cron       string    `yaml:"cron"`
	TargetType string    `yaml:"target_type"`
	TargetID   string    `yaml:"target_id"`
	Prompt     string    `yaml:"prompt"`
	// SendOutput controls whether Claude's text output is forwarded to the user.
	// Nil means unset (LoadYAML defaults to true). Set to false for background
	// tasks (memory_distill, life_sim). Using *bool avoids the Go zero-value
	// trap where omitting send_output in YAML would silently disable output.
	SendOutput *bool `yaml:"send_output"`
	CreatedBy  string    `yaml:"created_by"`
	CreatedAt  time.Time `yaml:"created_at"`
	Enabled    bool      `yaml:"enabled"`
}
