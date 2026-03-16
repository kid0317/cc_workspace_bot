package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
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

func TestResolveProvider(t *testing.T) {
	tests := []struct {
		name     string
		app      config.AppClaudeConfig
		claude   config.ClaudeConfig
		wantName string
		wantPC   config.ProviderConfig
	}{
		{
			name:   "no config at all defaults to anthropic",
			app:    config.AppClaudeConfig{},
			claude: config.ClaudeConfig{},
			wantName: "anthropic",
			wantPC:   config.ProviderConfig{},
		},
		{
			name: "uses default_provider when app has none",
			app:  config.AppClaudeConfig{},
			claude: config.ClaudeConfig{
				DefaultProvider: "bailian",
				Providers: map[string]config.ProviderConfig{
					"bailian": {BaseURL: "https://bl", AuthToken: "key", Model: "qwen-plus"},
				},
			},
			wantName: "bailian",
			wantPC:   config.ProviderConfig{BaseURL: "https://bl", AuthToken: "key", Model: "qwen-plus"},
		},
		{
			name: "app provider overrides default_provider",
			app:  config.AppClaudeConfig{Provider: "anthropic"},
			claude: config.ClaudeConfig{
				DefaultProvider: "bailian",
				Providers: map[string]config.ProviderConfig{
					"anthropic": {Model: "sonnet"},
					"bailian":   {Model: "qwen-plus", AuthToken: "key"},
				},
			},
			wantName: "anthropic",
			wantPC:   config.ProviderConfig{Model: "sonnet"},
		},
		{
			name: "app model overrides provider default model",
			app:  config.AppClaudeConfig{Model: "kimi-k2.5"},
			claude: config.ClaudeConfig{
				DefaultProvider: "bailian",
				Providers: map[string]config.ProviderConfig{
					"bailian": {AuthToken: "key", Model: "qwen-plus"},
				},
			},
			wantName: "bailian",
			wantPC:   config.ProviderConfig{AuthToken: "key", Model: "kimi-k2.5"},
		},
		{
			name: "app selects provider and overrides model",
			app:  config.AppClaudeConfig{Provider: "bailian", Model: "kimi-k2.5"},
			claude: config.ClaudeConfig{
				DefaultProvider: "anthropic",
				Providers: map[string]config.ProviderConfig{
					"anthropic": {Model: "sonnet"},
					"bailian":   {BaseURL: "https://bl", AuthToken: "key", Model: "qwen-plus"},
				},
			},
			wantName: "bailian",
			wantPC:   config.ProviderConfig{BaseURL: "https://bl", AuthToken: "key", Model: "kimi-k2.5"},
		},
		{
			name:   "trims whitespace from provider name",
			app:    config.AppClaudeConfig{Provider: "  bailian  "},
			claude: config.ClaudeConfig{
				Providers: map[string]config.ProviderConfig{
					"bailian": {AuthToken: "key"},
				},
			},
			wantName: "bailian",
			wantPC:   config.ProviderConfig{AuthToken: "key"},
		},
		{
			name:   "unknown provider returns empty config",
			app:    config.AppClaudeConfig{Provider: "unknown"},
			claude: config.ClaudeConfig{},
			wantName: "unknown",
			wantPC:   config.ProviderConfig{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			appCfg := &config.AppConfig{Claude: tt.app}
			cfg := &config.Config{Claude: tt.claude}
			gotName, gotPC := resolveProvider(appCfg, cfg)
			if gotName != tt.wantName {
				t.Errorf("name = %q, want %q", gotName, tt.wantName)
			}
			if gotPC != tt.wantPC {
				t.Errorf("config = %+v, want %+v", gotPC, tt.wantPC)
			}
		})
	}
}

func TestBuildClaudeEnvVars(t *testing.T) {
	t.Run("bailian provider with full config", func(t *testing.T) {
		pc := config.ProviderConfig{BaseURL: "https://bl", AuthToken: "key123", Model: "qwen-plus"}
		envs := buildClaudeEnvVars("bailian", pc)
		assertEnvContains(t, envs, "ANTHROPIC_BASE_URL", "https://bl")
		assertEnvContains(t, envs, "ANTHROPIC_AUTH_TOKEN", "key123")
		assertEnvContains(t, envs, "ANTHROPIC_MODEL", "qwen-plus")
		assertEnvContains(t, envs, "ANTHROPIC_DEFAULT_HAIKU_MODEL", "qwen-plus")
		assertEnvContains(t, envs, "ANTHROPIC_DEFAULT_SONNET_MODEL", "qwen-plus")
		assertEnvContains(t, envs, "ANTHROPIC_DEFAULT_OPUS_MODEL", "qwen-plus")
	})

	t.Run("bailian without base_url uses hardcoded fallback", func(t *testing.T) {
		pc := config.ProviderConfig{AuthToken: "key", Model: "qwen-plus"}
		envs := buildClaudeEnvVars("bailian", pc)
		assertEnvContains(t, envs, "ANTHROPIC_BASE_URL", "https://coding.dashscope.aliyuncs.com/apps/anthropic")
	})

	t.Run("anthropic with model alias expands alias", func(t *testing.T) {
		pc := config.ProviderConfig{Model: "sonnet"}
		envs := buildClaudeEnvVars("anthropic", pc)
		assertEnvContains(t, envs, "ANTHROPIC_MODEL", "claude-sonnet-4-6")
		assertEnvNotPresent(t, envs, "ANTHROPIC_BASE_URL")
		assertEnvNotPresent(t, envs, "ANTHROPIC_AUTH_TOKEN")
	})

	t.Run("default anthropic no config returns nil", func(t *testing.T) {
		pc := config.ProviderConfig{}
		envs := buildClaudeEnvVars("anthropic", pc)
		if envs != nil {
			t.Errorf("expected nil, got %v", envs)
		}
	})

	t.Run("empty provider name no config returns nil", func(t *testing.T) {
		pc := config.ProviderConfig{}
		envs := buildClaudeEnvVars("", pc)
		if envs != nil {
			t.Errorf("expected nil, got %v", envs)
		}
	})

	t.Run("base_url in config overrides hardcoded fallback", func(t *testing.T) {
		pc := config.ProviderConfig{BaseURL: "https://custom.example.com", Model: "qwen-plus"}
		envs := buildClaudeEnvVars("bailian", pc)
		assertEnvContains(t, envs, "ANTHROPIC_BASE_URL", "https://custom.example.com")
	})
}

func TestBuildSettingsJSON(t *testing.T) {
	t.Run("bailian provider returns JSON with env overrides", func(t *testing.T) {
		pc := config.ProviderConfig{BaseURL: "https://bl", AuthToken: "key123", Model: "kimi-k2.5"}
		got := buildSettingsJSON("bailian", pc)
		if got == "" {
			t.Fatal("expected non-empty JSON")
		}
		var parsed map[string]map[string]string
		if err := json.Unmarshal([]byte(got), &parsed); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		env := parsed["env"]
		if env["ANTHROPIC_BASE_URL"] != "https://bl" {
			t.Errorf("ANTHROPIC_BASE_URL = %q, want https://bl", env["ANTHROPIC_BASE_URL"])
		}
		if env["ANTHROPIC_AUTH_TOKEN"] != "key123" {
			t.Errorf("ANTHROPIC_AUTH_TOKEN = %q, want key123", env["ANTHROPIC_AUTH_TOKEN"])
		}
		if env["ANTHROPIC_MODEL"] != "kimi-k2.5" {
			t.Errorf("ANTHROPIC_MODEL = %q, want kimi-k2.5", env["ANTHROPIC_MODEL"])
		}
	})

	t.Run("default anthropic no config returns empty", func(t *testing.T) {
		got := buildSettingsJSON("anthropic", config.ProviderConfig{})
		if got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})

	t.Run("bailian without base_url uses fallback", func(t *testing.T) {
		pc := config.ProviderConfig{AuthToken: "key", Model: "qwen-plus"}
		got := buildSettingsJSON("bailian", pc)
		var parsed map[string]map[string]string
		if err := json.Unmarshal([]byte(got), &parsed); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if parsed["env"]["ANTHROPIC_BASE_URL"] != "https://coding.dashscope.aliyuncs.com/apps/anthropic" {
			t.Errorf("expected fallback URL, got %q", parsed["env"]["ANTHROPIC_BASE_URL"])
		}
	})
}

func TestBuildArgs_ModelFlag(t *testing.T) {
	t.Run("--model from provider config", func(t *testing.T) {
		cfg := &config.Config{
			Claude: config.ClaudeConfig{
				MaxTurns:        20,
				DefaultProvider: "bailian",
				Providers: map[string]config.ProviderConfig{
					"bailian": {Model: "qwen-plus"},
				},
			},
		}
		appCfg := &config.AppConfig{
			Claude: config.AppClaudeConfig{PermissionMode: "acceptEdits"},
		}
		e := &Executor{cfg: cfg}
		req := &ExecuteRequest{AppConfig: appCfg}
		args := e.buildArgs("hi", req, "/tmp/session")
		assertHasFlag(t, args, "--model", "qwen-plus")
	})

	t.Run("app model overrides provider default", func(t *testing.T) {
		cfg := &config.Config{
			Claude: config.ClaudeConfig{
				MaxTurns:        20,
				DefaultProvider: "anthropic",
				Providers: map[string]config.ProviderConfig{
					"anthropic": {Model: "sonnet"},
				},
			},
		}
		appCfg := &config.AppConfig{
			Claude: config.AppClaudeConfig{PermissionMode: "acceptEdits", Model: "opus"},
		}
		e := &Executor{cfg: cfg}
		req := &ExecuteRequest{AppConfig: appCfg}
		args := e.buildArgs("hi", req, "/tmp/session")
		assertHasFlag(t, args, "--model", "claude-opus-4-6")
	})

	t.Run("no --model when no provider config", func(t *testing.T) {
		cfg := &config.Config{
			Claude: config.ClaudeConfig{MaxTurns: 20},
		}
		appCfg := &config.AppConfig{
			Claude: config.AppClaudeConfig{PermissionMode: "acceptEdits"},
		}
		e := &Executor{cfg: cfg}
		req := &ExecuteRequest{AppConfig: appCfg}
		args := e.buildArgs("hi", req, "/tmp/session")
		assertNoFlag(t, args, "--model")
	})
}

func TestFilterEnv(t *testing.T) {
	env := []string{
		"ANTHROPIC_BASE_URL=https://old.example.com",
		"ANTHROPIC_AUTH_TOKEN=old-token",
		"ANTHROPIC_MODEL=old-model",
		"HOME=/root",
		"PATH=/usr/bin",
		"WORKSPACE_DIR=/tmp",
	}

	filtered := filterEnv(env, "ANTHROPIC_")

	if len(filtered) != 3 {
		t.Fatalf("expected 3 entries, got %d: %v", len(filtered), filtered)
	}
	for _, e := range filtered {
		if strings.HasPrefix(e, "ANTHROPIC_") {
			t.Errorf("ANTHROPIC_ var should be removed: %q", e)
		}
	}
	assertEnvContains(t, filtered, "HOME", "/root")
	assertEnvContains(t, filtered, "PATH", "/usr/bin")
	assertEnvContains(t, filtered, "WORKSPACE_DIR", "/tmp")
}

// assertEnvContains checks that envs contains "KEY=value".
func assertEnvContains(t *testing.T, envs []string, key, value string) {
	t.Helper()
	expected := key + "=" + value
	for _, e := range envs {
		if e == expected {
			return
		}
	}
	t.Errorf("env %q=%q not found in %v", key, value, envs)
}

// assertEnvNotPresent checks that no env starts with "KEY=".
func assertEnvNotPresent(t *testing.T, envs []string, key string) {
	t.Helper()
	prefix := key + "="
	for _, e := range envs {
		if strings.HasPrefix(e, prefix) {
			t.Errorf("env %q should not be present, found %q", key, e)
			return
		}
	}
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

func TestWriteSessionContext(t *testing.T) {
	tests := []struct {
		name       string
		req        *ExecuteRequest
		dbPath     string
		wantLines  []string
	}{
		{
			name: "all fields written",
			req: &ExecuteRequest{
				AppConfig:    &config.AppConfig{ID: "app1"},
				WorkspaceDir: "/workspace/app1",
				SessionID:    "sess-123",
				ChannelKey:   "p2p:ou_abc:cli_app1",
			},
			dbPath: "/data/bot.db",
			wantLines: []string{
				"- App ID: app1",
				"- Workspace: /workspace/app1",
				"- Session ID: sess-123",
				"- Channel key: p2p:ou_abc:cli_app1",
				"- DB path: /data/bot.db",
			},
		},
		{
			name: "group channel key",
			req: &ExecuteRequest{
				AppConfig:    &config.AppConfig{ID: "app2"},
				WorkspaceDir: "/workspace/app2",
				SessionID:    "sess-456",
				ChannelKey:   "group:oc_xyz:cli_app2",
			},
			dbPath: "/var/bot.db",
			wantLines: []string{
				"- Channel key: group:oc_xyz:cli_app2",
				"- DB path: /var/bot.db",
			},
		},
		{
			name: "empty channel key still written",
			req: &ExecuteRequest{
				AppConfig:    &config.AppConfig{ID: "app3"},
				WorkspaceDir: "/workspace/app3",
				SessionID:    "sess-789",
				ChannelKey:   "",
			},
			dbPath: "/data/bot.db",
			wantLines: []string{
				"- Channel key: ",
				"- DB path: /data/bot.db",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			err := writeSessionContext(dir, tt.req, tt.dbPath)
			if err != nil {
				t.Fatalf("writeSessionContext() error = %v", err)
			}
			data, err := os.ReadFile(filepath.Join(dir, "SESSION_CONTEXT.md"))
			if err != nil {
				t.Fatalf("read SESSION_CONTEXT.md: %v", err)
			}
			content := string(data)
			for _, want := range tt.wantLines {
				if !strings.Contains(content, want) {
					t.Errorf("SESSION_CONTEXT.md missing %q\ngot:\n%s", want, content)
				}
			}
		})
	}
}

