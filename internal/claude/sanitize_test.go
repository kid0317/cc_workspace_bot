package claude

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsResumeRecoverable(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{
			"signature error",
			"API Error: 400 messages.299.content.0: Invalid `signature` in `thinking` block",
			true,
		},
		{
			"tool_use id pattern error",
			"messages.53.content.2.tool_use.id: String should match pattern '^[a-zA-Z0-9_-]+$'",
			true,
		},
		{"unrelated 400", "API Error: 400 messages.0.role: invalid value", false},
		{"empty", "", false},
		{"context overflow", "context length exceeded", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsResumeRecoverable(tc.in); got != tc.want {
				t.Fatalf("IsResumeRecoverable(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestSanitizeJSONL_DropsForeignNoTextAndOrphans(t *testing.T) {
	// b: foreign with only tool_use → drop entirely.
	// c: tool_result referencing b's tool_use id → drop as orphan.
	lines := []string{
		`{"uuid":"a","message":{"model":"claude-sonnet-4-6","role":"assistant","content":[{"type":"text","text":"ok"}]}}`,
		`{"uuid":"b","message":{"model":"kimi-k2.5","role":"assistant","content":[{"type":"tool_use","id":"toolu_functions.Bash:13","name":"Bash","input":{}}]}}`,
		`{"uuid":"c","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_functions.Bash:13","content":"x"}]}}`,
		`{"uuid":"d","message":{"model":"<synthetic>","role":"assistant","content":[{"type":"text","text":"sys"}]}}`,
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	stats, err := SanitizeJSONL(path)
	if err != nil {
		t.Fatalf("SanitizeJSONL: %v", err)
	}
	if stats.Dropped != 2 || stats.Rewritten != 0 {
		t.Fatalf("stats = %+v, want Dropped=2 Rewritten=0", stats)
	}

	out, _ := os.ReadFile(path)
	got := string(out)
	if !strings.Contains(got, `"uuid":"a"`) || !strings.Contains(got, `"uuid":"d"`) {
		t.Errorf("expected a/d to remain, got:\n%s", got)
	}
	if strings.Contains(got, `"uuid":"b"`) || strings.Contains(got, `"uuid":"c"`) {
		t.Errorf("b/c should be dropped, got:\n%s", got)
	}

	bak, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if !bytes.Contains(bak, []byte(`"uuid":"b"`)) {
		t.Error("backup missing original kimi line")
	}
}

func TestSanitizeJSONL_RewritesForeignWithText(t *testing.T) {
	// Foreign message mixing thinking, text, and tool_use:
	//   - thinking dropped
	//   - tool_use dropped (id added to orphan set)
	//   - text preserved
	//   - model retagged as "<synthetic>"
	// Plus a tool_result for the dropped tool_use → drop.
	lines := []string{
		`{"uuid":"u1","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}`,
		`{"uuid":"k1","message":{"model":"kimi-k2.5","role":"assistant","content":[` +
			`{"type":"thinking","thinking":"reason","signature":""},` +
			`{"type":"text","text":"已记录到饮食流水"},` +
			`{"type":"tool_use","id":"toolu_functions.Bash:1","name":"Bash","input":{"command":"ls"}}` +
			`]}}`,
		`{"uuid":"r1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_functions.Bash:1","content":"x"}]}}`,
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	stats, err := SanitizeJSONL(path)
	if err != nil {
		t.Fatalf("SanitizeJSONL: %v", err)
	}
	if stats.Rewritten != 1 || stats.Dropped != 1 {
		t.Fatalf("stats = %+v, want Rewritten=1 Dropped=1", stats)
	}

	out, _ := os.ReadFile(path)
	got := string(out)
	// User message intact.
	if !strings.Contains(got, `"uuid":"u1"`) {
		t.Errorf("u1 should remain, got:\n%s", got)
	}
	// Rewritten foreign message: text preserved, thinking and tool_use gone, model is synthetic.
	if !strings.Contains(got, `"uuid":"k1"`) {
		t.Errorf("k1 should be rewritten and remain, got:\n%s", got)
	}
	if !strings.Contains(got, `已记录到饮食流水`) {
		t.Errorf("text content lost, got:\n%s", got)
	}
	if strings.Contains(got, `"thinking"`) || strings.Contains(got, `"toolu_functions.Bash:1"`) {
		t.Errorf("thinking/tool_use should be stripped, got:\n%s", got)
	}
	if !strings.Contains(got, `"<synthetic>"`) {
		t.Errorf("model should be retagged as synthetic, got:\n%s", got)
	}
	// tool_result referencing the dropped tool_use id is gone.
	if strings.Contains(got, `"uuid":"r1"`) {
		t.Errorf("orphan tool_result r1 should be dropped, got:\n%s", got)
	}
}

func TestSanitizeJSONL_Idempotent(t *testing.T) {
	// After sanitize, running again must report no changes (rewritten foreign
	// is now <synthetic>, so it no longer matches isForeignAssistant).
	lines := []string{
		`{"uuid":"k1","message":{"model":"kimi-k2.5","role":"assistant","content":[` +
			`{"type":"thinking","thinking":"r","signature":""},` +
			`{"type":"text","text":"hello"}` +
			`]}}`,
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	first, err := SanitizeJSONL(path)
	if err != nil || first.Rewritten != 1 {
		t.Fatalf("first pass: %+v err=%v", first, err)
	}
	second, err := SanitizeJSONL(path)
	if err != nil {
		t.Fatalf("second pass err: %v", err)
	}
	if second.Affected() != 0 {
		t.Fatalf("second pass not idempotent: %+v", second)
	}
}

func TestSanitizeJSONL_NoForeignNoChange(t *testing.T) {
	lines := []string{
		`{"uuid":"a","message":{"model":"claude-sonnet-4-6","role":"assistant","content":[{"type":"text","text":"ok"}]}}`,
		`{"uuid":"b","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}`,
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	original := []byte(strings.Join(lines, "\n") + "\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	stats, err := SanitizeJSONL(path)
	if err != nil {
		t.Fatalf("SanitizeJSONL: %v", err)
	}
	if stats.Affected() != 0 {
		t.Fatalf("stats = %+v, want zero", stats)
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, original) {
		t.Errorf("file should be unchanged when nothing matched")
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Errorf("backup should not be created when nothing changed; stat err=%v", err)
	}
}

func TestSanitizeResumeForCwd_MissingFileNotAnError(t *testing.T) {
	dir := t.TempDir()
	cleaned, err := SanitizeResumeForCwd(dir, "no-such-session")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if cleaned {
		t.Fatalf("cleaned = true on missing file")
	}
}

func TestCwdToProjectDir(t *testing.T) {
	got, err := cwdToProjectDir("/root/xh_yibu/sessions/06c69765")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.HasSuffix(got, "/.claude/projects/-root-xh-yibu-sessions-06c69765") {
		t.Fatalf("unexpected path: %s", got)
	}
}
