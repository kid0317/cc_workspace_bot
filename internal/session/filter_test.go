package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ── readFilteredReply ─────────────────────────────────────────────────────────

func writeFinalReply(t *testing.T, dir, content string, mtime time.Time) string {
	t.Helper()
	path := filepath.Join(dir, finalReplyFile)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write final reply: %v", err)
	}
	if !mtime.IsZero() {
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
	}
	return path
}

func TestReadFilteredReply(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name      string
		setup     func(t *testing.T, dir string)
		execStart time.Time
		wantText  string
		wantFound bool
	}{
		{
			name:      "missing file",
			setup:     func(t *testing.T, dir string) {},
			execStart: now,
			wantText:  "",
			wantFound: false,
		},
		{
			name: "fresh file replaces text",
			setup: func(t *testing.T, dir string) {
				writeFinalReply(t, dir, "嗯。今天怎么样。\n", time.Time{})
			},
			execStart: now.Add(-time.Minute),
			wantText:  "嗯。今天怎么样。",
			wantFound: true,
		},
		{
			name: "fresh file with SEND markers preserved",
			setup: func(t *testing.T, dir string) {
				writeFinalReply(t, dir, "嗯。[[SEND]]在。", time.Time{})
			},
			execStart: now.Add(-time.Minute),
			wantText:  "嗯。[[SEND]]在。",
			wantFound: true,
		},
		{
			name: "stale file ignored",
			setup: func(t *testing.T, dir string) {
				writeFinalReply(t, dir, "旧轮残留", now.Add(-time.Hour))
			},
			execStart: now,
			wantText:  "",
			wantFound: false,
		},
		{
			name: "empty file reported found with empty text",
			setup: func(t *testing.T, dir string) {
				writeFinalReply(t, dir, "  \n", time.Time{})
			},
			execStart: now.Add(-time.Minute),
			wantText:  "",
			wantFound: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			tt.setup(t, dir)

			text, found := readFilteredReply(dir, tt.execStart)
			if text != tt.wantText || found != tt.wantFound {
				t.Errorf("readFilteredReply() = (%q, %v), want (%q, %v)",
					text, found, tt.wantText, tt.wantFound)
			}
			// The file must always be consumed (deleted) so it can never leak
			// into a later turn — including the stale case.
			if _, err := os.Stat(filepath.Join(dir, finalReplyFile)); !os.IsNotExist(err) {
				t.Errorf("FINAL_REPLY.md not consumed after read")
			}
		})
	}
}

// ── filterCanary ──────────────────────────────────────────────────────────────

func TestFilterCanary(t *testing.T) {
	t.Run("not armed before first hit", func(t *testing.T) {
		c := newFilterCanary(3)
		for i := 0; i < 10; i++ {
			if c.observe(false) {
				t.Fatalf("unarmed canary fired at miss %d", i+1)
			}
		}
	})

	t.Run("fires at threshold after armed", func(t *testing.T) {
		c := newFilterCanary(3)
		c.observe(true) // arm
		if c.observe(false) || c.observe(false) {
			t.Fatal("fired before threshold")
		}
		if !c.observe(false) {
			t.Fatal("did not fire at threshold")
		}
	})

	t.Run("refires at every threshold multiple", func(t *testing.T) {
		c := newFilterCanary(2)
		c.observe(true)
		fires := 0
		for i := 0; i < 6; i++ {
			if c.observe(false) {
				fires++
			}
		}
		if fires != 3 {
			t.Fatalf("fires = %d, want 3", fires)
		}
	})

	t.Run("hit resets miss counter", func(t *testing.T) {
		c := newFilterCanary(3)
		c.observe(true)
		c.observe(false)
		c.observe(false)
		c.observe(true) // reset
		if c.observe(false) || c.observe(false) {
			t.Fatal("fired before threshold after reset")
		}
		if !c.observe(false) {
			t.Fatal("did not fire at threshold after reset")
		}
	})
}

// ── applyOutputFilter (worker integration) ────────────────────────────────────

func TestApplyOutputFilter(t *testing.T) {
	t.Run("companion replaces text from FINAL_REPLY", func(t *testing.T) {
		app := companionApp(t)
		w := newTestWorker(t, app, &mockSender{}, nil)
		sessDir := filepath.Join(app.WorkspaceDir, "sessions", "s1")
		if err := os.MkdirAll(sessDir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFinalReply(t, sessDir, "嗯。", time.Time{})

		text := w.applyOutputFilter("脏文本：让我先读取记忆。嗯。", "s1", time.Now().Add(-time.Minute))
		if text != "嗯。" {
			t.Errorf("text = %q, want %q", text, "嗯。")
		}
	})

	t.Run("no file keeps raw text (fail-open)", func(t *testing.T) {
		app := companionApp(t)
		w := newTestWorker(t, app, &mockSender{}, nil)
		text := w.applyOutputFilter("原始文本", "s1", time.Now())
		if text != "原始文本" {
			t.Errorf("text = %q, want raw passthrough", text)
		}
	})

	t.Run("empty FINAL_REPLY falls back to raw text, never empty", func(t *testing.T) {
		app := companionApp(t)
		w := newTestWorker(t, app, &mockSender{}, nil)
		sessDir := filepath.Join(app.WorkspaceDir, "sessions", "s1")
		if err := os.MkdirAll(sessDir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFinalReply(t, sessDir, "", time.Time{})

		text := w.applyOutputFilter("原始文本", "s1", time.Now().Add(-time.Minute))
		if text != "原始文本" {
			t.Errorf("text = %q, want raw fallback (empty would trip the empty-text guard)", text)
		}
	})
}

// ── companion-facing hardcoded texts ─────────────────────────────────────────

func TestAttachmentAckText(t *testing.T) {
	tests := []struct {
		name        string
		isCompanion bool
		wantOpsTone bool // true = work-mode instructional tone is acceptable
	}{
		{"work mode keeps instructional ack", false, true},
		{"companion mode uses in-voice ack", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := attachmentAckText(tt.isCompanion, "[图片: /x/a.png]")
			if got == "" {
				t.Fatal("empty ack")
			}
			instructional := containsAny(got, []string{"请描述", "已收到"})
			if tt.wantOpsTone && !instructional {
				t.Errorf("work mode ack changed unexpectedly: %q", got)
			}
			if !tt.wantOpsTone && instructional {
				t.Errorf("companion ack leaks operational tone: %q", got)
			}
		})
	}
}

func TestNewSessionReceipt(t *testing.T) {
	if got := newSessionReceipt(false); got != "✅ 已开启新会话" {
		t.Errorf("work mode receipt changed: %q", got)
	}
	got := newSessionReceipt(true)
	if containsAny(got, []string{"✅", "会话", "session"}) {
		t.Errorf("companion receipt leaks operational tone: %q", got)
	}
	if got == "" {
		t.Error("companion receipt empty")
	}
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if sub != "" && strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
