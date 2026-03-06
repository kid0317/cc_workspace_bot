package task

import (
	"os"
	"path/filepath"
	"testing"
)

// ── buildChannelKey ──────────────────────────────────────────────────────────

func TestBuildChannelKey(t *testing.T) {
	tests := []struct {
		name       string
		targetType string
		targetID   string
		appID      string
		want       string
	}{
		{"p2p", "p2p", "user123", "app1", "p2p:user123:app1"},
		{"group", "group", "chat456", "app1", "group:chat456:app1"},
		{"unknown defaults to group", "topic", "chat789", "app2", "group:chat789:app2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildChannelKey(tt.targetType, tt.targetID, tt.appID)
			if got != tt.want {
				t.Errorf("buildChannelKey(%q, %q, %q) = %q, want %q",
					tt.targetType, tt.targetID, tt.appID, got, tt.want)
			}
		})
	}
}

// ── parseChannelKey ──────────────────────────────────────────────────────────

func TestParseChannelKey(t *testing.T) {
	tests := []struct {
		name         string
		key          string
		wantChatType string
		wantChatID   string
	}{
		{"p2p key", "p2p:user123:app1", "p2p", "user123"},
		{"group key", "group:chat456:app1", "group", "chat456"},
		{"malformed fallback", "nocol", "group", "nocol"},
		{"two-part key", "p2p:user456", "p2p", "user456"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotType, gotID := parseChannelKey(tt.key)
			if gotType != tt.wantChatType {
				t.Errorf("parseChannelKey(%q) chatType = %q, want %q", tt.key, gotType, tt.wantChatType)
			}
			if gotID != tt.wantChatID {
				t.Errorf("parseChannelKey(%q) chatID = %q, want %q", tt.key, gotID, tt.wantChatID)
			}
		})
	}
}

// ── receiveTarget ────────────────────────────────────────────────────────────

func TestReceiveTarget(t *testing.T) {
	tests := []struct {
		name           string
		targetType     string
		targetID       string
		wantID         string
		wantIDType     string
	}{
		{"p2p returns open_id", "p2p", "ou_abc", "ou_abc", "open_id"},
		{"group returns chat_id", "group", "oc_xyz", "oc_xyz", "chat_id"},
		{"unknown defaults to chat_id", "other", "oc_xyz", "oc_xyz", "chat_id"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotID, gotIDType := receiveTarget(tt.targetType, tt.targetID)
			if gotID != tt.wantID {
				t.Errorf("receiveTarget(%q, %q) id = %q, want %q", tt.targetType, tt.targetID, gotID, tt.wantID)
			}
			if gotIDType != tt.wantIDType {
				t.Errorf("receiveTarget(%q, %q) idType = %q, want %q", tt.targetType, tt.targetID, gotIDType, tt.wantIDType)
			}
		})
	}
}

// ── LoadYAML ─────────────────────────────────────────────────────────────────

func writeYAML(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadYAML_Valid(t *testing.T) {
	dir := t.TempDir()
	path := writeYAML(t, dir, "task1.yaml", `
id: "abc-123"
app_id: "app1"
name: "Daily report"
cron: "0 9 * * 1-5"
target_type: "p2p"
target_id: "ou_user1"
prompt: "Generate daily report"
enabled: true
`)

	task, err := LoadYAML(path)
	if err != nil {
		t.Fatalf("LoadYAML() error = %v", err)
	}

	if task.ID != "abc-123" {
		t.Errorf("ID = %q, want abc-123", task.ID)
	}
	if task.AppID != "app1" {
		t.Errorf("AppID = %q, want app1", task.AppID)
	}
	if task.Name != "Daily report" {
		t.Errorf("Name = %q, want 'Daily report'", task.Name)
	}
	if task.CronExpr != "0 9 * * 1-5" {
		t.Errorf("CronExpr = %q, want '0 9 * * 1-5'", task.CronExpr)
	}
	if !task.Enabled {
		t.Error("Enabled should be true")
	}
}

func TestLoadYAML_EmptyIDGeneratesUUID(t *testing.T) {
	dir := t.TempDir()
	path := writeYAML(t, dir, "task2.yaml", `
app_id: "app1"
name: "No ID task"
cron: "* * * * *"
target_type: "group"
target_id: "oc_chat1"
prompt: "Hello"
enabled: true
`)

	task, err := LoadYAML(path)
	if err != nil {
		t.Fatalf("LoadYAML() error = %v", err)
	}

	if task.ID == "" {
		t.Error("ID should be auto-generated, got empty string")
	}
}

func TestLoadYAML_InvalidCron(t *testing.T) {
	dir := t.TempDir()
	path := writeYAML(t, dir, "bad_cron.yaml", `
id: "x"
app_id: "app1"
name: "Bad cron"
cron: "not a cron expression"
target_type: "p2p"
target_id: "ou_user1"
prompt: "Test"
enabled: true
`)

	_, err := LoadYAML(path)
	if err == nil {
		t.Error("LoadYAML() with invalid cron should return error")
	}
}

func TestLoadYAML_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	// Unclosed flow sequence — yaml.v3 will return a parse error.
	path := writeYAML(t, dir, "bad.yaml", "[unclosed yaml")

	_, err := LoadYAML(path)
	if err == nil {
		t.Error("LoadYAML() with invalid YAML should return error")
	}
}

func TestLoadYAML_FileNotFound(t *testing.T) {
	_, err := LoadYAML("/nonexistent/path/task.yaml")
	if err == nil {
		t.Error("LoadYAML() with missing file should return error")
	}
}

func TestLoadYAML_EmptyCronIsAllowed(t *testing.T) {
	dir := t.TempDir()
	path := writeYAML(t, dir, "nocron.yaml", `
id: "y"
app_id: "app1"
name: "No cron"
target_type: "p2p"
target_id: "ou_user1"
prompt: "Test"
enabled: false
`)

	task, err := LoadYAML(path)
	if err != nil {
		t.Fatalf("LoadYAML() with empty cron should not error: %v", err)
	}
	if task.CronExpr != "" {
		t.Errorf("CronExpr = %q, want empty", task.CronExpr)
	}
}
