package claude

import (
	"strings"
	"testing"

	"github.com/kid0317/cc-workspace-bot/internal/config"
)

func TestChannelKeyToRoutingKey(t *testing.T) {
	tests := []struct {
		channelKey string
		want       string
	}{
		{"p2p:ou_abc:cli_app1", "p2p:ou_abc"},
		{"group:oc_xyz:cli_app1", "group:oc_xyz"},
		{"thread:oc_xyz:tid_123:cli_app1", "group:oc_xyz"},
		{"", ""},
		{"unknown", "unknown"},
	}
	for _, tt := range tests {
		got := channelKeyToRoutingKey(tt.channelKey)
		if got != tt.want {
			t.Errorf("channelKeyToRoutingKey(%q) = %q, want %q", tt.channelKey, got, tt.want)
		}
	}
}

func TestInjectRoutingContext(t *testing.T) {
	appCfg := &config.AppConfig{ID: "app1"}

	t.Run("injects routing block when channel key set", func(t *testing.T) {
		req := &ExecuteRequest{
			Prompt:     "hello",
			ChannelKey: "p2p:ou_abc:cli_app1",
			SenderID:   "ou_abc",
			AppConfig:  appCfg,
		}
		got := injectRoutingContext(req)
		if !strings.HasPrefix(got, "<system_routing>") {
			t.Errorf("expected <system_routing> prefix, got: %q", got)
		}
		if !strings.Contains(got, "routing_key: p2p:ou_abc") {
			t.Errorf("missing routing_key in output: %q", got)
		}
		if !strings.Contains(got, "sender_id: ou_abc") {
			t.Errorf("missing sender_id in output: %q", got)
		}
		if !strings.HasSuffix(got, "hello") {
			t.Errorf("original prompt not preserved: %q", got)
		}
	})

	t.Run("no injection when channel key empty", func(t *testing.T) {
		req := &ExecuteRequest{
			Prompt:    "hello",
			AppConfig: appCfg,
		}
		got := injectRoutingContext(req)
		if got != "hello" {
			t.Errorf("expected unmodified prompt, got: %q", got)
		}
	})

	t.Run("group channel key", func(t *testing.T) {
		req := &ExecuteRequest{
			Prompt:     "ping",
			ChannelKey: "group:oc_xyz:cli_app1",
			SenderID:   "ou_def",
			AppConfig:  appCfg,
		}
		got := injectRoutingContext(req)
		if !strings.Contains(got, "routing_key: group:oc_xyz") {
			t.Errorf("expected group routing_key, got: %q", got)
		}
	})

	t.Run("thread maps to group routing key", func(t *testing.T) {
		req := &ExecuteRequest{
			Prompt:     "ping",
			ChannelKey: "thread:oc_xyz:tid_001:cli_app1",
			SenderID:   "ou_def",
			AppConfig:  appCfg,
		}
		got := injectRoutingContext(req)
		if !strings.Contains(got, "routing_key: group:oc_xyz") {
			t.Errorf("expected thread→group routing_key, got: %q", got)
		}
	})
}

func TestExpandModelAlias(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"haiku", "claude-haiku-4-5-20251001"},
		{"HAIKU", "claude-haiku-4-5-20251001"},
		{"sonnet", "claude-sonnet-4-6"},
		{"Sonnet", "claude-sonnet-4-6"},
		{"opus", "claude-opus-4-6"},
		// Full IDs pass through unchanged.
		{"claude-sonnet-4-6", "claude-sonnet-4-6"},
		{"claude-haiku-4-5-20251001", "claude-haiku-4-5-20251001"},
		{"unknown-model", "unknown-model"},
	}
	for _, tt := range tests {
		got := expandModelAlias(tt.input)
		if got != tt.want {
			t.Errorf("expandModelAlias(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestResolveModel(t *testing.T) {
	tests := []struct {
		name       string
		appModel   string
		globalModel string
		want       string
	}{
		{
			name:        "app-level model takes priority over global",
			appModel:    "opus",
			globalModel: "haiku",
			want:        "claude-opus-4-6",
		},
		{
			name:        "falls back to global when app model is empty",
			appModel:    "",
			globalModel: "sonnet",
			want:        "claude-sonnet-4-6",
		},
		{
			name:        "returns empty when both unset",
			appModel:    "",
			globalModel: "",
			want:        "",
		},
		{
			name:        "trims whitespace from app model",
			appModel:    "  sonnet  ",
			globalModel: "opus",
			want:        "claude-sonnet-4-6",
		},
		{
			name:        "whitespace-only app model falls back to global",
			appModel:    "   ",
			globalModel: "haiku",
			want:        "claude-haiku-4-5-20251001",
		},
		{
			name:        "trims whitespace from global model",
			appModel:    "",
			globalModel: "  opus  ",
			want:        "claude-opus-4-6",
		},
		{
			name:        "full model ID accepted",
			appModel:    "claude-sonnet-4-6",
			globalModel: "",
			want:        "claude-sonnet-4-6",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			appCfg := &config.AppConfig{
				Claude: config.AppClaudeConfig{Model: tt.appModel},
			}
			cfg := &config.Config{
				Claude: config.ClaudeConfig{Model: tt.globalModel},
			}
			got := resolveModel(appCfg, cfg)
			if got != tt.want {
				t.Errorf("resolveModel() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildArgs_Model(t *testing.T) {
	baseCfg := &config.Config{
		Claude: config.ClaudeConfig{MaxTurns: 20},
	}
	baseApp := &config.AppConfig{
		Claude: config.AppClaudeConfig{PermissionMode: "acceptEdits"},
	}

	t.Run("--model flag present when global model set", func(t *testing.T) {
		cfg := &config.Config{
			Claude: config.ClaudeConfig{MaxTurns: 20, Model: "sonnet"},
		}
		e := &Executor{cfg: cfg}
		req := &ExecuteRequest{AppConfig: baseApp}
		args := e.buildArgs("hi", req, "/tmp/session")
		assertHasFlag(t, args, "--model", "claude-sonnet-4-6")
	})

	t.Run("--model flag present when app model set", func(t *testing.T) {
		appCfg := &config.AppConfig{
			Claude: config.AppClaudeConfig{
				PermissionMode: "acceptEdits",
				Model:          "opus",
			},
		}
		e := &Executor{cfg: baseCfg}
		req := &ExecuteRequest{AppConfig: appCfg}
		args := e.buildArgs("hi", req, "/tmp/session")
		assertHasFlag(t, args, "--model", "claude-opus-4-6")
	})

	t.Run("app model overrides global in args", func(t *testing.T) {
		cfg := &config.Config{
			Claude: config.ClaudeConfig{MaxTurns: 20, Model: "haiku"},
		}
		appCfg := &config.AppConfig{
			Claude: config.AppClaudeConfig{
				PermissionMode: "acceptEdits",
				Model:          "opus",
			},
		}
		e := &Executor{cfg: cfg}
		req := &ExecuteRequest{AppConfig: appCfg}
		args := e.buildArgs("hi", req, "/tmp/session")
		assertHasFlag(t, args, "--model", "claude-opus-4-6")
	})

	t.Run("no --model flag when both unset", func(t *testing.T) {
		e := &Executor{cfg: baseCfg}
		req := &ExecuteRequest{AppConfig: baseApp}
		args := e.buildArgs("hi", req, "/tmp/session")
		assertNoFlag(t, args, "--model")
	})

	t.Run("no --model flag when model is whitespace only", func(t *testing.T) {
		cfg := &config.Config{
			Claude: config.ClaudeConfig{MaxTurns: 20, Model: "   "},
		}
		e := &Executor{cfg: cfg}
		req := &ExecuteRequest{AppConfig: baseApp}
		args := e.buildArgs("hi", req, "/tmp/session")
		assertNoFlag(t, args, "--model")
	})
}

// assertHasFlag checks that args contains "--flag value" in sequence.
func assertHasFlag(t *testing.T, args []string, flag, value string) {
	t.Helper()
	for i, a := range args {
		if a == flag {
			if i+1 >= len(args) {
				t.Errorf("flag %q has no value", flag)
				return
			}
			if args[i+1] != value {
				t.Errorf("flag %q = %q, want %q", flag, args[i+1], value)
			}
			return
		}
	}
	t.Errorf("flag %q not found in args: %v", flag, args)
}

// assertNoFlag checks that args does not contain the given flag.
func assertNoFlag(t *testing.T, args []string, flag string) {
	t.Helper()
	for _, a := range args {
		if a == flag {
			t.Errorf("flag %q should not be present in args: %v", flag, args)
			return
		}
	}
}

