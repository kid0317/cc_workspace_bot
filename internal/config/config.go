package config

import (
	"fmt"

	"github.com/spf13/viper"
)

// Config is the top-level application configuration.
type Config struct {
	Apps    []AppConfig   `mapstructure:"apps"`
	Server  ServerConfig  `mapstructure:"server"`
	Claude  ClaudeConfig  `mapstructure:"claude"`
	Session SessionConfig `mapstructure:"session"`
	Cleanup CleanupConfig `mapstructure:"cleanup"`
}

// AppConfig represents one Feishu application + its workspace.
type AppConfig struct {
	ID                      string          `mapstructure:"id"`
	FeishuAppID             string          `mapstructure:"feishu_app_id"`
	FeishuAppSecret         string          `mapstructure:"feishu_app_secret"`
	FeishuVerificationToken string          `mapstructure:"feishu_verification_token"`
	FeishuEncryptKey        string          `mapstructure:"feishu_encrypt_key"`
	WorkspaceDir            string          `mapstructure:"workspace_dir"`
	AllowedChats            []string        `mapstructure:"allowed_chats"`
	Claude                  AppClaudeConfig `mapstructure:"claude"`
}

// AllowedChat returns true if the given chat ID is allowed (empty list = all allowed).
func (a *AppConfig) AllowedChat(chatID string) bool {
	if len(a.AllowedChats) == 0 {
		return true
	}
	for _, id := range a.AllowedChats {
		if id == chatID {
			return true
		}
	}
	return false
}

// AppClaudeConfig holds per-app Claude CLI settings.
type AppClaudeConfig struct {
	PermissionMode string   `mapstructure:"permission_mode"`
	AllowedTools   []string `mapstructure:"allowed_tools"`
}

// ServerConfig holds HTTP server settings.
type ServerConfig struct {
	Port int `mapstructure:"port"`
}

// ClaudeConfig holds global Claude CLI settings.
type ClaudeConfig struct {
	TimeoutMinutes int `mapstructure:"timeout_minutes"`
	MaxTurns       int `mapstructure:"max_turns"`
}

// SessionConfig holds session worker settings.
type SessionConfig struct {
	WorkerIdleTimeoutMinutes int `mapstructure:"worker_idle_timeout_minutes"`
}

// CleanupConfig holds attachment cleanup settings.
type CleanupConfig struct {
	AttachmentsRetentionDays int    `mapstructure:"attachments_retention_days"`
	AttachmentsMaxDays       int    `mapstructure:"attachments_max_days"`
	Schedule                 string `mapstructure:"schedule"`
}

// Validate checks that all required fields are populated.
// Call this immediately after Load to catch misconfiguration at startup.
func (c *Config) Validate() error {
	if len(c.Apps) == 0 {
		return fmt.Errorf("config: at least one app must be defined")
	}
	for _, app := range c.Apps {
		if app.ID == "" {
			return fmt.Errorf("config: app is missing 'id'")
		}
		if app.FeishuAppID == "" {
			return fmt.Errorf("config: app %q is missing 'feishu_app_id'", app.ID)
		}
		if app.FeishuAppSecret == "" {
			return fmt.Errorf("config: app %q is missing 'feishu_app_secret'", app.ID)
		}
		if app.FeishuVerificationToken == "" {
			return fmt.Errorf("config: app %q is missing 'feishu_verification_token'", app.ID)
		}
		if app.WorkspaceDir == "" {
			return fmt.Errorf("config: app %q is missing 'workspace_dir'", app.ID)
		}
	}
	return nil
}

// Load reads the YAML config file at the given path.
func Load(path string) (*Config, error) {
	v := viper.New()
	v.SetConfigFile(path)
	v.SetConfigType("yaml")

	// Defaults
	v.SetDefault("server.port", 8080)
	v.SetDefault("claude.timeout_minutes", 5)
	v.SetDefault("claude.max_turns", 20)
	v.SetDefault("session.worker_idle_timeout_minutes", 30)
	v.SetDefault("cleanup.attachments_retention_days", 7)
	v.SetDefault("cleanup.attachments_max_days", 30)
	v.SetDefault("cleanup.schedule", "0 2 * * *")

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	return &cfg, nil
}
