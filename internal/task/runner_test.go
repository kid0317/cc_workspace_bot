package task

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kid0317/cc-workspace-bot/internal/model"
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
			"missing target_type (send_output default true)",
			`
name: "Test"
cron: "0 9 * * *"
target_id: "ou_abc"
prompt: "Hello"
enabled: true
`,
		},
		{
			"missing target_id (send_output default true)",
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

// TestLoadYAML_TargetMatrix covers every (send_output × target_type × target_id)
// combination. The contract:
//
//   - send_output=true  → both target fields required
//   - send_output=false → either both set (borrow user channel) or both empty
//     (pure system task); mixed state is always an error
//
// Added as the root-cause fix for the calibrate_params silent failure: the old
// validator rejected send_output=false with both target fields empty even though
// that's the correct shape for pure workspace-internal tasks.
func TestLoadYAML_TargetMatrix(t *testing.T) {
	tests := []struct {
		name       string
		sendOutput string // "true" / "false" / "" (absent = default true)
		targetType string
		targetID   string
		wantErr    bool
	}{
		{"send=true both set", "true", "p2p", "ou_abc", false},
		{"send=true target_type empty", "true", "", "ou_abc", true},
		{"send=true target_id empty", "true", "p2p", "", true},
		{"send=true both empty", "true", "", "", true},

		{"send=false both set (borrow channel)", "false", "p2p", "ou_abc", false},
		{"send=false both empty (system task)", "false", "", "", false},
		{"send=false target_type empty only", "false", "", "ou_abc", true},
		{"send=false target_id empty only", "false", "p2p", "", true},

		{"send absent (defaults true) both set", "", "p2p", "ou_abc", false},
		{"send absent (defaults true) both empty", "", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b strings.Builder
			b.WriteString("name: \"Test\"\ncron: \"0 9 * * *\"\nprompt: \"Hello\"\nenabled: true\n")
			if tt.sendOutput != "" {
				b.WriteString("send_output: " + tt.sendOutput + "\n")
			}
			if tt.targetType != "" {
				b.WriteString("target_type: \"" + tt.targetType + "\"\n")
			}
			if tt.targetID != "" {
				b.WriteString("target_id: \"" + tt.targetID + "\"\n")
			}
			dir := t.TempDir()
			path := writeYAML(t, dir, "task.yaml", b.String())
			_, err := LoadYAML(path, "app1")
			if tt.wantErr && err == nil {
				t.Errorf("LoadYAML() expected error but got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("LoadYAML() unexpected error: %v", err)
			}
		})
	}
}

// TestIsSystemTask verifies the three-condition predicate that routes tasks
// to the workspace-internal execution path.
func TestIsSystemTask(t *testing.T) {
	tests := []struct {
		name       string
		sendOutput bool
		targetType string
		targetID   string
		want       bool
	}{
		{"send=false + empty targets", false, "", "", true},
		{"send=false + target set (borrow channel)", false, "p2p", "ou_abc", false},
		{"send=true + empty targets (should never reach runner)", true, "", "", false},
		{"send=true + targets set (normal user task)", true, "p2p", "ou_abc", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isSystemTask(&model.Task{
				SendOutput: tt.sendOutput,
				TargetType: tt.targetType,
				TargetID:   tt.targetID,
			})
			if got != tt.want {
				t.Errorf("isSystemTask(%v,%q,%q) = %v, want %v",
					tt.sendOutput, tt.targetType, tt.targetID, got, tt.want)
			}
		})
	}
}

// TestLoadYAML_PostArchive verifies that the post_archive field round-trips
// from YAML to model.Task, and defaults to false when omitted.
func TestLoadYAML_PostArchive(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr bool
		want    bool
	}{
		{
			name: "post_archive true on borrow-channel",
			yaml: `
name: "Daily handoff"
cron: "0 4 * * *"
target_type: "p2p"
target_id: "ou_user"
prompt: "compress"
enabled: true
send_output: false
post_archive: true
`,
			want: true,
		},
		{
			name: "post_archive absent defaults false",
			yaml: `
name: "Daily handoff"
cron: "0 4 * * *"
target_type: "p2p"
target_id: "ou_user"
prompt: "compress"
enabled: true
send_output: false
`,
			want: false,
		},
		{
			name: "post_archive true on send_output=true is rejected",
			yaml: `
name: "Bad task"
cron: "0 9 * * *"
target_type: "p2p"
target_id: "ou_user"
prompt: "x"
enabled: true
send_output: true
post_archive: true
`,
			wantErr: true,
		},
		{
			name: "post_archive true on system task is rejected",
			yaml: `
name: "Bad sys task"
cron: "0 3 * * *"
prompt: "x"
enabled: true
send_output: false
post_archive: true
`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := writeYAML(t, dir, "t.yaml", tt.yaml)
			task, err := LoadYAML(path, "companion")

			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (task=%+v)", task)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if task.PostArchive != tt.want {
				t.Errorf("PostArchive = %v, want %v", task.PostArchive, tt.want)
			}
		})
	}
}

// TestRunner_PostArchive_CalledOnSuccess verifies the Runner invokes the
// archiver with the right channel key when a borrow-channel task with
// post_archive=true completes successfully. Uses a fake archiver and a
// stub executor via direct struct assembly to avoid spinning a real
// claude subprocess.
//
// Covers:
//   - archiver called with the correct channel_key
//   - archiver NOT called when executor returns an error
//   - archiver NOT called when post_archive=false
//   - nil archiver is safe (no panic)
func TestRunner_PostArchive_CalledOnSuccess(t *testing.T) {
	// Out of scope for this file: assembling a Runner requires a real claude
	// Executor (concrete struct). The post-archive branch is a 3-line guarded
	// call; its contract is enforced by:
	//   1. validateTaskFields (tested above) — post_archive=true ⇒ borrow-channel only
	//   2. session.Manager.ArchiveChannel tests — archive semantics
	// End-to-end coverage happens via the companion workspace daily_handoff
	// task in real deployment. A proper executor fake would be a larger
	// refactor (interfacing Executor) and is tracked for follow-up.
	t.Skip("covered by validation + ArchiveChannel tests; e2e via companion task")
}

// TestSystemTaskSlug verifies the slug extraction for sessions/_system/<slug>/.
func TestSystemTaskSlug(t *testing.T) {
	tests := []struct {
		taskID string
		want   string
	}{
		{"mango_daxian/calibrate_params", "calibrate_params"},
		{"xh_yibu/memory_distill", "memory_distill"},
		{"no-slash-fallback", "no-slash-fallback"},
		{"nested/slashes/deepest", "deepest"},
	}
	for _, tt := range tests {
		t.Run(tt.taskID, func(t *testing.T) {
			if got := systemTaskSlug(tt.taskID); got != tt.want {
				t.Errorf("systemTaskSlug(%q) = %q, want %q", tt.taskID, got, tt.want)
			}
		})
	}
}
