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
		name       string
		targetType string
		targetID   string
		wantID     string
		wantIDType string
	}{
		{"p2p with ou_ returns open_id", "p2p", "ou_abc", "ou_abc", "open_id"},
		{"p2p with oc_ returns chat_id", "p2p", "oc_abc", "oc_abc", "chat_id"},
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
name: "Daily report"
cron: "0 9 * * 1-5"
target_type: "p2p"
target_id: "ou_user1"
prompt: "Generate daily report"
enabled: true
send_output: true
`)

	task, err := LoadYAML(path, "injected-app")
	if err != nil {
		t.Fatalf("LoadYAML() error = %v", err)
	}

	// ID is "{app_id}/{filename_slug}", computed by the framework.
	if task.ID != "injected-app/task1" {
		t.Errorf("ID = %q, want %q", task.ID, "injected-app/task1")
	}
	// AppID is injected by caller, not from YAML.
	if task.AppID != "injected-app" {
		t.Errorf("AppID = %q, want %q (injected, not from YAML)", task.AppID, "injected-app")
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
	if !task.SendOutput {
		t.Error("SendOutput should be true when send_output: true in YAML")
	}
}

// TestLoadYAML_IDNamespaced verifies that the task ID is always "{app_id}/{slug}",
// regardless of what the YAML id field contains, ensuring global uniqueness.
func TestLoadYAML_IDNamespaced(t *testing.T) {
	tests := []struct {
		filename string
		appID    string
		wantID   string
	}{
		// Semantic name: companion template task
		{"proactive_reach.yaml", "xh_yibu", "xh_yibu/proactive_reach"},
		// UUID filename: Claude-generated task
		{"1ff20d20-4469-4346-8e96-3dda5d71c123.yaml", "investment", "investment/1ff20d20-4469-4346-8e96-3dda5d71c123"},
		// Slug with hyphens
		{"morning-todo-8am.yaml", "ycm_life", "ycm_life/morning-todo-8am"},
	}

	for _, tt := range tests {
		t.Run(tt.wantID, func(t *testing.T) {
			dir := t.TempDir()
			path := writeYAML(t, dir, tt.filename, `
name: "Test task"
cron: "* * * * *"
target_type: "group"
target_id: "oc_chat1"
prompt: "Hello"
enabled: true
`)
			task, err := LoadYAML(path, tt.appID)
			if err != nil {
				t.Fatalf("LoadYAML() error = %v", err)
			}
			if task.ID != tt.wantID {
				t.Errorf("ID = %q, want %q", task.ID, tt.wantID)
			}
		})
	}
}

// TestLoadYAML_SendOutputDefaultsTrue verifies that omitting send_output in
// YAML produces SendOutput=true (not the Go zero value false).
func TestLoadYAML_SendOutputDefaultsTrue(t *testing.T) {
	dir := t.TempDir()
	path := writeYAML(t, dir, "default_output.yaml", `
name: "Default output task"
cron: "0 * * * *"
target_type: "p2p"
target_id: "ou_user1"
prompt: "Do something"
enabled: true
`)

	task, err := LoadYAML(path, "app1")
	if err != nil {
		t.Fatalf("LoadYAML() error = %v", err)
	}

	if !task.SendOutput {
		t.Error("SendOutput should default to true when send_output is absent from YAML")
	}
}

func TestLoadYAML_InvalidCron(t *testing.T) {
	dir := t.TempDir()
	path := writeYAML(t, dir, "bad_cron.yaml", `
name: "Bad cron"
cron: "not a cron expression"
target_type: "p2p"
target_id: "ou_user1"
prompt: "Test"
enabled: true
`)

	_, err := LoadYAML(path, "app1")
	if err == nil {
		t.Error("LoadYAML() with invalid cron should return error")
	}
}

func TestLoadYAML_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	// Unclosed flow sequence — yaml.v3 will return a parse error.
	path := writeYAML(t, dir, "bad.yaml", "[unclosed yaml")

	_, err := LoadYAML(path, "app1")
	if err == nil {
		t.Error("LoadYAML() with invalid YAML should return error")
	}
}

func TestLoadYAML_FileNotFound(t *testing.T) {
	_, err := LoadYAML("/nonexistent/path/task.yaml", "app1")
	if err == nil {
		t.Error("LoadYAML() with missing file should return error")
	}
}

// TestLoadYAML_DisabledTaskAllowsEmptyCron verifies that a disabled task does
// not require a cron expression — it won't be scheduled, so validation is skipped.
func TestLoadYAML_DisabledTaskAllowsEmptyCron(t *testing.T) {
	dir := t.TempDir()
	path := writeYAML(t, dir, "nocron.yaml", `
name: "No cron"
target_type: "p2p"
target_id: "ou_user1"
prompt: "Test"
enabled: false
`)

	task, err := LoadYAML(path, "app1")
	if err != nil {
		t.Fatalf("LoadYAML() disabled task with empty cron should not error: %v", err)
	}
	if task.CronExpr != "" {
		t.Errorf("CronExpr = %q, want empty", task.CronExpr)
	}
}

// TestLoadYAML_UnresolvedPlaceholder verifies that tasks containing unresolved
// __PLACEHOLDER__ tokens are rejected, preventing silent execution failures.
func TestLoadYAML_UnresolvedPlaceholder(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{
			"placeholder in target_id",
			`
name: "Test"
cron: "0 9 * * *"
target_type: "p2p"
target_id: "__TARGET_ID__"
prompt: "Hello"
enabled: true
`,
		},
		{
			"placeholder in target_type",
			`
name: "Test"
cron: "0 9 * * *"
target_type: "__TARGET_TYPE__"
target_id: "ou_abc"
prompt: "Hello"
enabled: true
`,
		},
		{
			"placeholder in prompt",
			`
name: "Test"
cron: "0 9 * * *"
target_type: "p2p"
target_id: "ou_abc"
prompt: "Run task for __WORKSPACE_DIR__"
enabled: true
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := writeYAML(t, dir, "task.yaml", tt.content)
			_, err := LoadYAML(path, "app1")
			if err == nil {
				t.Error("LoadYAML() with unresolved placeholder should return error")
			}
		})
	}
}

// TestLoadYAML_RequiredFields verifies that missing required fields are rejected
// before they can cause silent failures at task execution time.
func TestLoadYAML_RequiredFields(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{
			"missing target_type",
			`
name: "Test"
cron: "0 9 * * *"
target_id: "ou_abc"
prompt: "Hello"
enabled: true
`,
		},
		{
			"missing target_id",
			`
name: "Test"
cron: "0 9 * * *"
target_type: "p2p"
prompt: "Hello"
enabled: true
`,
		},
		{
			"missing prompt",
			`
name: "Test"
cron: "0 9 * * *"
target_type: "p2p"
target_id: "ou_abc"
enabled: true
`,
		},
		{
			"missing cron for enabled task",
			`
name: "Test"
target_type: "p2p"
target_id: "ou_abc"
prompt: "Hello"
enabled: true
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := writeYAML(t, dir, "task.yaml", tt.content)
			_, err := LoadYAML(path, "app1")
			if err == nil {
				t.Errorf("LoadYAML() with %s should return error", tt.name)
			}
		})
	}
}
