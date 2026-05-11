package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kid0317/cc-workspace-bot/internal/config"
)

func TestAssertInsideWorkspace(t *testing.T) {
	ws := t.TempDir()
	tests := []struct {
		name    string
		dir     string
		wantErr bool
	}{
		{"direct subdirectory", filepath.Join(ws, "sessions", "abc"), false},
		{"nested system path", filepath.Join(ws, "sessions", "_system", "slug"), false},
		{"workspace itself", ws, false},
		{"parent escape via ..", filepath.Join(ws, "..", "evil"), true},
		{"absolute outside", "/tmp/definitely-outside-evil", true},
		{"sibling directory", filepath.Join(ws, "..", filepath.Base(ws)+"-other"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := assertInsideWorkspace(ws, tt.dir)
			if tt.wantErr && err == nil {
				t.Errorf("assertInsideWorkspace(%q, %q) expected error, got nil", ws, tt.dir)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("assertInsideWorkspace(%q, %q) unexpected error: %v", ws, tt.dir, err)
			}
		})
	}
}

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
		if !strings.Contains(got, "current_time: ") {
			t.Errorf("missing current_time in output: %q", got)
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
			name:     "no config at all defaults to anthropic",
			app:      config.AppClaudeConfig{},
			claude:   config.ClaudeConfig{},
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
			name: "trims whitespace from provider name",
			app:  config.AppClaudeConfig{Provider: "  bailian  "},
			claude: config.ClaudeConfig{
				Providers: map[string]config.ProviderConfig{
					"bailian": {AuthToken: "key"},
				},
			},
			wantName: "bailian",
			wantPC:   config.ProviderConfig{AuthToken: "key"},
		},
		{
			name:     "unknown provider returns empty config",
			app:      config.AppClaudeConfig{Provider: "unknown"},
			claude:   config.ClaudeConfig{},
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

func TestBuildLangfuseEnvVars(t *testing.T) {
	t.Run("user message: minimal required fields", func(t *testing.T) {
		req := &ExecuteRequest{
			AppConfig:    &config.AppConfig{ID: "yzk_worker"},
			SessionID:    "sess-abc",
			ChannelKey:   "p2p:oc_xxx:cli_yyy",
			SenderID:     "ou_open_id_zzz",
			WorkspaceDir: "/tmp/ws",
		}
		envs := buildLangfuseEnvVars(req, "")
		assertEnvContains(t, envs, "CC_LF_APP_ID", "yzk_worker")
		assertEnvContains(t, envs, "CC_LF_CHANNEL_KEY", "p2p:oc_xxx:cli_yyy")
		assertEnvContains(t, envs, "CC_LF_USER_OPEN_ID", "ou_open_id_zzz")
		assertEnvContains(t, envs, "CC_LF_FRAMEWORK_SESSION_ID", "sess-abc")
		assertEnvContains(t, envs, "CC_LF_META_VERSION", "1")
		assertEnvNotPresent(t, envs, "CC_LF_TASK_NAME")
	})

	t.Run("scheduled task: includes task name", func(t *testing.T) {
		req := &ExecuteRequest{
			AppConfig:  &config.AppConfig{ID: "yzk_worker"},
			SessionID:  "sess-task-1",
			ChannelKey: "p2p:oc_xxx:cli_yyy",
			SenderID:   "ou_creator",
		}
		envs := buildLangfuseEnvVars(req, "daily_briefing")
		assertEnvContains(t, envs, "CC_LF_TASK_NAME", "daily_briefing")
		assertEnvContains(t, envs, "CC_LF_FRAMEWORK_SESSION_ID", "sess-task-1")
	})

	t.Run("nil app config returns nil", func(t *testing.T) {
		envs := buildLangfuseEnvVars(&ExecuteRequest{SessionID: "x"}, "")
		if envs != nil {
			t.Errorf("expected nil when AppConfig is nil, got %v", envs)
		}
	})

	t.Run("nil request returns nil", func(t *testing.T) {
		envs := buildLangfuseEnvVars(nil, "")
		if envs != nil {
			t.Errorf("expected nil for nil request, got %v", envs)
		}
	})

	t.Run("missing session id returns nil (sentinel for hook to skip)", func(t *testing.T) {
		req := &ExecuteRequest{
			AppConfig: &config.AppConfig{ID: "x"},
			// no SessionID
		}
		envs := buildLangfuseEnvVars(req, "")
		if envs != nil {
			t.Errorf("expected nil when SessionID missing, got %v", envs)
		}
	})

	t.Run("env var values do not contain newlines or unsafe shell chars", func(t *testing.T) {
		// Defensive: app_id / channel_key are user-influenced (config + Feishu IDs)
		req := &ExecuteRequest{
			AppConfig:  &config.AppConfig{ID: "weird\nid"},
			SessionID:  "sess",
			ChannelKey: "ch",
		}
		envs := buildLangfuseEnvVars(req, "")
		for _, e := range envs {
			if strings.Contains(e, "\n") {
				t.Errorf("env var contains newline: %q", e)
			}
		}
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

func TestBuildArgs_AddDirFlag(t *testing.T) {
	t.Run("--add-dir set to WorkspaceDir", func(t *testing.T) {
		cfg := &config.Config{Claude: config.ClaudeConfig{MaxTurns: 20}}
		appCfg := &config.AppConfig{
			Claude: config.AppClaudeConfig{PermissionMode: "acceptEdits"},
		}
		e := &Executor{cfg: cfg}
		req := &ExecuteRequest{AppConfig: appCfg, WorkspaceDir: "/tmp/ws"}
		args := e.buildArgs("hi", req, "/tmp/ws/sessions/s1")
		assertHasFlag(t, args, "--add-dir", "/tmp/ws")
	})

	t.Run("no --add-dir when WorkspaceDir empty", func(t *testing.T) {
		cfg := &config.Config{Claude: config.ClaudeConfig{MaxTurns: 20}}
		appCfg := &config.AppConfig{
			Claude: config.AppClaudeConfig{PermissionMode: "acceptEdits"},
		}
		e := &Executor{cfg: cfg}
		req := &ExecuteRequest{AppConfig: appCfg}
		args := e.buildArgs("hi", req, "/tmp/session")
		assertNoFlag(t, args, "--add-dir")
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

func TestBuildArgs_EffortFlag(t *testing.T) {
	t.Run("--effort from provider config", func(t *testing.T) {
		cfg := &config.Config{
			Claude: config.ClaudeConfig{
				MaxTurns:        20,
				DefaultProvider: "anthropic",
				Providers: map[string]config.ProviderConfig{
					"anthropic": {Model: "sonnet", Effort: "medium"},
				},
			},
		}
		appCfg := &config.AppConfig{
			Claude: config.AppClaudeConfig{PermissionMode: "acceptEdits"},
		}
		e := &Executor{cfg: cfg}
		req := &ExecuteRequest{AppConfig: appCfg}
		args := e.buildArgs("hi", req, "/tmp/session")
		assertHasFlag(t, args, "--effort", "medium")
	})

	t.Run("app effort overrides provider default", func(t *testing.T) {
		cfg := &config.Config{
			Claude: config.ClaudeConfig{
				MaxTurns:        20,
				DefaultProvider: "anthropic",
				Providers: map[string]config.ProviderConfig{
					"anthropic": {Model: "sonnet", Effort: "low"},
				},
			},
		}
		appCfg := &config.AppConfig{
			Claude: config.AppClaudeConfig{PermissionMode: "acceptEdits", Effort: "high"},
		}
		e := &Executor{cfg: cfg}
		req := &ExecuteRequest{AppConfig: appCfg}
		args := e.buildArgs("hi", req, "/tmp/session")
		assertHasFlag(t, args, "--effort", "high")
	})

	t.Run("no --effort when unset", func(t *testing.T) {
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
			Claude: config.AppClaudeConfig{PermissionMode: "acceptEdits"},
		}
		e := &Executor{cfg: cfg}
		req := &ExecuteRequest{AppConfig: appCfg}
		args := e.buildArgs("hi", req, "/tmp/session")
		assertNoFlag(t, args, "--effort")
	})

	t.Run("no --effort for non-anthropic provider", func(t *testing.T) {
		cfg := &config.Config{
			Claude: config.ClaudeConfig{
				MaxTurns:        20,
				DefaultProvider: "bailian",
				Providers: map[string]config.ProviderConfig{
					"bailian": {Model: "qwen-plus", Effort: "high"},
				},
			},
		}
		appCfg := &config.AppConfig{
			Claude: config.AppClaudeConfig{PermissionMode: "acceptEdits", Effort: "max"},
		}
		e := &Executor{cfg: cfg}
		req := &ExecuteRequest{AppConfig: appCfg}
		args := e.buildArgs("hi", req, "/tmp/session")
		assertNoFlag(t, args, "--effort")
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
		name      string
		req       *ExecuteRequest
		dbPath    string
		wantLines []string
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

func TestParseLine_ResultEventCapturesError(t *testing.T) {
	e := &Executor{}
	tests := []struct {
		name        string
		line        string
		wantIsErr   bool
		wantErrText string
	}{
		{
			name:        "error result with is_error and result text",
			line:        `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"API Error: 400 messages.0: Invalid signature","cost_usd":0.001,"duration_ms":120}`,
			wantIsErr:   true,
			wantErrText: "API Error: 400 messages.0: Invalid signature",
		},
		{
			name:        "successful result clears nothing",
			line:        `{"type":"result","subtype":"success","is_error":false,"result":"ok","cost_usd":0.01,"duration_ms":300}`,
			wantIsErr:   false,
			wantErrText: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := &ExecuteResult{}
			e.parseLine(tc.line, r)
			if r.IsError != tc.wantIsErr {
				t.Errorf("IsError = %v, want %v", r.IsError, tc.wantIsErr)
			}
			if r.ErrorText != tc.wantErrText {
				t.Errorf("ErrorText = %q, want %q", r.ErrorText, tc.wantErrText)
			}
		})
	}
}

func TestParseLine_AssistantTextStillAccumulates(t *testing.T) {
	// Regression guard: adding IsError/Result to streamEvent must not break
	// the existing assistant text accumulation path.
	e := &Executor{}
	r := &ExecuteResult{}
	e.parseLine(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hello "}]}}`, r)
	e.parseLine(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"world"}]}}`, r)
	if r.Text != "hello world" {
		t.Errorf("Text = %q, want %q", r.Text, "hello world")
	}
	if r.IsError {
		t.Errorf("IsError unexpectedly true")
	}
}

func TestShouldAttemptResumeRecovery(t *testing.T) {
	tests := []struct {
		name   string
		req    *ExecuteRequest
		result *ExecuteResult
		err    error
		want   bool
	}{
		{
			name:   "nil request",
			req:    nil,
			result: &ExecuteResult{ErrorText: "Invalid `signature` in `thinking` block"},
			want:   false,
		},
		{
			name:   "no claude session id (fresh context)",
			req:    &ExecuteRequest{},
			result: &ExecuteResult{ErrorText: "Invalid `signature` in `thinking` block"},
			want:   false,
		},
		{
			name:   "signature error in ErrorText, with session id",
			req:    &ExecuteRequest{ClaudeSessionID: "abc"},
			result: &ExecuteResult{IsError: true, ErrorText: "API Error: 400 messages.5.content.0: Invalid `signature` in `thinking` block"},
			want:   true,
		},
		{
			name:   "tool_use id error in result.Text",
			req:    &ExecuteRequest{ClaudeSessionID: "abc"},
			result: &ExecuteResult{Text: "API Error: 400 messages.53.content.2.tool_use.id: String should match pattern"},
			want:   true,
		},
		{
			name:   "recoverable pattern in returned err",
			req:    &ExecuteRequest{ClaudeSessionID: "abc"},
			result: nil,
			err:    errString("claude failed: exit 1 (stderr: Invalid `signature` in `thinking` block)"),
			want:   true,
		},
		{
			name:   "unrelated 400 — not recoverable",
			req:    &ExecuteRequest{ClaudeSessionID: "abc"},
			result: &ExecuteResult{IsError: true, ErrorText: "API Error: 400 invalid model"},
			want:   false,
		},
		{
			name:   "successful result with session id",
			req:    &ExecuteRequest{ClaudeSessionID: "abc"},
			result: &ExecuteResult{Text: "ok"},
			want:   false,
		},
		{
			name:   "all nil",
			req:    &ExecuteRequest{ClaudeSessionID: "abc"},
			result: nil,
			err:    nil,
			want:   false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldAttemptResumeRecovery(tc.req, tc.result, tc.err)
			if got != tc.want {
				t.Errorf("shouldAttemptResumeRecovery = %v, want %v", got, tc.want)
			}
		})
	}
}

// errString is a tiny error implementation for tests, avoiding fmt.Errorf
// import drift in this file's existing import set.
type errString string

func (e errString) Error() string { return string(e) }
