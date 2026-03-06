package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kid0317/cc-workspace-bot/internal/config"
)

// validApp returns a minimal valid AppConfig.
func validApp() config.AppConfig {
	return config.AppConfig{
		ID:                      "app1",
		FeishuAppID:             "cli_xxx",
		FeishuAppSecret:         "secret",
		FeishuVerificationToken: "token",
		WorkspaceDir:            "./workspaces/app1",
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     config.Config
		wantErr bool
	}{
		{
			name:    "valid single app",
			cfg:     config.Config{Apps: []config.AppConfig{validApp()}},
			wantErr: false,
		},
		{
			name:    "no apps",
			cfg:     config.Config{},
			wantErr: true,
		},
		{
			name: "missing id",
			cfg: config.Config{Apps: []config.AppConfig{{
				FeishuAppID:             "x",
				FeishuAppSecret:         "y",
				FeishuVerificationToken: "z",
				WorkspaceDir:            "/tmp",
			}}},
			wantErr: true,
		},
		{
			name: "missing feishu_app_id",
			cfg: config.Config{Apps: []config.AppConfig{{
				ID:                      "a",
				FeishuAppSecret:         "y",
				FeishuVerificationToken: "z",
				WorkspaceDir:            "/tmp",
			}}},
			wantErr: true,
		},
		{
			name: "missing feishu_app_secret",
			cfg: config.Config{Apps: []config.AppConfig{{
				ID:                      "a",
				FeishuAppID:             "x",
				FeishuVerificationToken: "z",
				WorkspaceDir:            "/tmp",
			}}},
			wantErr: true,
		},
		{
			name: "missing verification_token",
			cfg: config.Config{Apps: []config.AppConfig{{
				ID:              "a",
				FeishuAppID:     "x",
				FeishuAppSecret: "y",
				WorkspaceDir:    "/tmp",
			}}},
			wantErr: true,
		},
		{
			name: "missing workspace_dir",
			cfg: config.Config{Apps: []config.AppConfig{{
				ID:                      "a",
				FeishuAppID:             "x",
				FeishuAppSecret:         "y",
				FeishuVerificationToken: "z",
			}}},
			wantErr: true,
		},
		{
			name: "valid with optional encrypt key",
			cfg: config.Config{Apps: []config.AppConfig{func() config.AppConfig {
				a := validApp()
				a.FeishuEncryptKey = "enc"
				return a
			}()}},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestAllowedChat(t *testing.T) {
	tests := []struct {
		name         string
		allowedChats []string
		chatID       string
		want         bool
	}{
		{"empty list allows all", []string{}, "chat_any", true},
		{"in list", []string{"chat_a", "chat_b"}, "chat_a", true},
		{"not in list", []string{"chat_a", "chat_b"}, "chat_c", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := config.AppConfig{AllowedChats: tt.allowedChats}
			if got := app.AllowedChat(tt.chatID); got != tt.want {
				t.Errorf("AllowedChat(%q) = %v, want %v", tt.chatID, got, tt.want)
			}
		})
	}
}

func TestLoad_ValidYAML(t *testing.T) {
	yaml := `
apps:
  - id: "test-app"
    feishu_app_id: "cli_xxx"
    feishu_app_secret: "secret"
    feishu_verification_token: "token"
    workspace_dir: "./workspaces/test"
server:
  port: 9090
claude:
  timeout_minutes: 10
  max_turns: 5
session:
  worker_idle_timeout_minutes: 15
cleanup:
  attachments_retention_days: 3
  attachments_max_days: 14
  schedule: "0 3 * * *"
`
	f := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(f, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(f)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Server.Port != 9090 {
		t.Errorf("port = %d, want 9090", cfg.Server.Port)
	}
	if len(cfg.Apps) != 1 {
		t.Fatalf("apps = %d, want 1", len(cfg.Apps))
	}
	if cfg.Apps[0].ID != "test-app" {
		t.Errorf("app id = %q, want test-app", cfg.Apps[0].ID)
	}
	if cfg.Claude.TimeoutMinutes != 10 {
		t.Errorf("timeout_minutes = %d, want 10", cfg.Claude.TimeoutMinutes)
	}
	if cfg.Claude.MaxTurns != 5 {
		t.Errorf("max_turns = %d, want 5", cfg.Claude.MaxTurns)
	}
	if cfg.Session.WorkerIdleTimeoutMinutes != 15 {
		t.Errorf("idle_timeout = %d, want 15", cfg.Session.WorkerIdleTimeoutMinutes)
	}
	if cfg.Cleanup.AttachmentsRetentionDays != 3 {
		t.Errorf("retention_days = %d, want 3", cfg.Cleanup.AttachmentsRetentionDays)
	}
	if cfg.Cleanup.Schedule != "0 3 * * *" {
		t.Errorf("schedule = %q, want '0 3 * * *'", cfg.Cleanup.Schedule)
	}
}

func TestLoad_Defaults(t *testing.T) {
	yaml := `
apps:
  - id: "a"
    feishu_app_id: "x"
    feishu_app_secret: "y"
    feishu_verification_token: "z"
    workspace_dir: "/tmp"
`
	f := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(f, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(f)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Server.Port != 8080 {
		t.Errorf("default port = %d, want 8080", cfg.Server.Port)
	}
	if cfg.Claude.TimeoutMinutes != 5 {
		t.Errorf("default timeout = %d, want 5", cfg.Claude.TimeoutMinutes)
	}
	if cfg.Claude.MaxTurns != 20 {
		t.Errorf("default max_turns = %d, want 20", cfg.Claude.MaxTurns)
	}
	if cfg.Session.WorkerIdleTimeoutMinutes != 30 {
		t.Errorf("default idle_timeout = %d, want 30", cfg.Session.WorkerIdleTimeoutMinutes)
	}
	if cfg.Cleanup.AttachmentsRetentionDays != 7 {
		t.Errorf("default retention_days = %d, want 7", cfg.Cleanup.AttachmentsRetentionDays)
	}
	if cfg.Cleanup.AttachmentsMaxDays != 30 {
		t.Errorf("default max_days = %d, want 30", cfg.Cleanup.AttachmentsMaxDays)
	}
	if cfg.Cleanup.Schedule != "0 2 * * *" {
		t.Errorf("default schedule = %q, want '0 2 * * *'", cfg.Cleanup.Schedule)
	}
}

func TestLoad_FileNotFound(t *testing.T) {
	_, err := config.Load("/nonexistent/path/config.yaml")
	if err == nil {
		t.Error("Load() with missing file should return error")
	}
}
