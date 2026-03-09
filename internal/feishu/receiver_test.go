package feishu

import (
	"strings"
	"testing"
)

func TestBuildChannelKey(t *testing.T) {
	tests := []struct {
		chatType string
		chatID   string
		threadID string
		appID    string
		want     string
	}{
		{"p2p", "chat_001", "", "app-a", "p2p:chat_001:app-a"},
		{"group", "oc_abc", "", "app-a", "group:oc_abc:app-a"},
		{"topic_group", "oc_abc", "tid_1", "app-a", "thread:oc_abc:tid_1:app-a"},
		{"topic", "oc_abc", "tid_2", "app-b", "thread:oc_abc:tid_2:app-b"},
		{"unknown", "oc_xyz", "", "app-c", "group:oc_xyz:app-c"},
	}
	for _, tt := range tests {
		got := buildChannelKey(tt.chatType, tt.chatID, tt.threadID, tt.appID)
		if got != tt.want {
			t.Errorf("buildChannelKey(%q,%q,%q,%q) = %q, want %q",
				tt.chatType, tt.chatID, tt.threadID, tt.appID, got, tt.want)
		}
	}
}

func TestReplyTarget(t *testing.T) {
	tests := []struct {
		chatType     string
		chatID       string
		senderOpenID string
		wantID       string
		wantType     string
	}{
		{"p2p", "chat_001", "ou_user1", "ou_user1", "open_id"},
		{"group", "oc_abc", "ou_user1", "oc_abc", "chat_id"},
		{"topic_group", "oc_abc", "ou_user1", "oc_abc", "chat_id"},
	}
	for _, tt := range tests {
		gotID, gotType := replyTarget(tt.chatType, tt.chatID, tt.senderOpenID)
		if gotID != tt.wantID || gotType != tt.wantType {
			t.Errorf("replyTarget(%q,%q,%q) = (%q,%q), want (%q,%q)",
				tt.chatType, tt.chatID, tt.senderOpenID,
				gotID, gotType, tt.wantID, tt.wantType)
		}
	}
}

func TestExtractPostText(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name: "extracts text from post",
			content: `{
				"title": "标题",
				"content": [[{"tag":"text","text":"第一行"},{"tag":"a","text":"链接"}],[{"tag":"text","text":"第二行"}]]
			}`,
			want: "标题\n第一行\n第二行",
		},
		{
			name:    "invalid json returns original",
			content: "not-json",
			want:    "not-json",
		},
		{
			name: "no title",
			content: `{"content":[[{"tag":"text","text":"hello"}]]}`,
			want: "hello",
		},
		{
			name: "non-text tags are ignored",
			content: `{"content":[[{"tag":"image","image_key":"k1"},{"tag":"text","text":"ok"}]]}`,
			want: "ok",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractPostText(tt.content)
			if got != tt.want {
				t.Errorf("extractPostText() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSafeStr(t *testing.T) {
	s := "hello"
	if safeStr(&s) != "hello" {
		t.Error("safeStr with value")
	}
	if safeStr(nil) != "" {
		t.Error("safeStr with nil should return empty string")
	}
}

func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"report.pdf", "report.pdf"},
		{"../../etc/passwd", "passwd"},
		{"file/with/slashes.txt", "slashes.txt"},
		// On Linux, backslash is not a path separator; it is replaced with _.
		{"file\\with\\backslash.txt", "file_with_backslash.txt"},
	}
	for _, tt := range tests {
		got := sanitizeFilename(tt.input)
		if got != tt.want {
			t.Errorf("sanitizeFilename(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// TestWelcomeMessageContent verifies welcome messages contain expected identifiers.
// The handlers build messages with fmt.Sprintf using appID and group name;
// this table validates the resulting content shape.
func TestWelcomeMessageContent(t *testing.T) {
	appID := "my-assistant"

	t.Run("bot added to group includes appID and group name", func(t *testing.T) {
		groupName := "产品团队"
		msg := botAddedWelcome(appID, groupName)
		if !strings.Contains(msg, appID) {
			t.Errorf("message missing appID %q: %s", appID, msg)
		}
		if !strings.Contains(msg, groupName) {
			t.Errorf("message missing group name %q: %s", groupName, msg)
		}
		if !strings.Contains(msg, "/new") {
			t.Error("message should mention /new command")
		}
	})

	t.Run("bot added falls back to 本群 when name empty", func(t *testing.T) {
		msg := botAddedWelcome(appID, "")
		if !strings.Contains(msg, "本群") {
			t.Errorf("should use 本群 as fallback, got: %s", msg)
		}
	})

	t.Run("user added message includes user names", func(t *testing.T) {
		names := []string{"张三", "李四"}
		msg := userAddedWelcome(appID, names)
		for _, name := range names {
			if !strings.Contains(msg, name) {
				t.Errorf("message missing user name %q: %s", name, msg)
			}
		}
		if !strings.Contains(msg, appID) {
			t.Errorf("message missing appID %q: %s", appID, msg)
		}
	})

	t.Run("user added falls back to 新成员 when no names", func(t *testing.T) {
		msg := userAddedWelcome(appID, nil)
		if !strings.Contains(msg, "新成员") {
			t.Errorf("should use 新成员 as fallback, got: %s", msg)
		}
	})
}
