package workspace

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// Init ensures the workspace directory exists and has the required subdirectories.
// If a _template directory is provided, it copies templates on first init.
// feishuAppID and feishuAppSecret are written to .claude/skills/feishu_ops/feishu.json so that
// feishu_ops scripts can authenticate without exposing credentials to the LLM.
func Init(workspaceDir string, templateDir string, feishuAppID, feishuAppSecret string) error {
	// Create required subdirectories.
	dirs := []string{
		workspaceDir,
		filepath.Join(workspaceDir, ".claude", "skills"),
		filepath.Join(workspaceDir, "memory"),
		filepath.Join(workspaceDir, "tasks"),
		filepath.Join(workspaceDir, "sessions"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("create dir %s: %w", d, err)
		}
	}

	// Create .memory.lock if it doesn't exist.
	lockPath := filepath.Join(workspaceDir, ".memory.lock")
	if _, err := os.Stat(lockPath); os.IsNotExist(err) {
		if err := os.WriteFile(lockPath, nil, 0o644); err != nil {
			return fmt.Errorf("create memory lock: %w", err)
		}
	}

	// Write .claude/skills/feishu_ops/feishu.json if credentials are provided.
	if feishuAppID != "" && feishuAppSecret != "" {
		if err := writeFeishuConfig(workspaceDir, feishuAppID, feishuAppSecret); err != nil {
			return fmt.Errorf("write feishu config: %w", err)
		}
	}

	// Copy template files if template dir is set and workspace is empty.
	if templateDir != "" {
		if err := copyTemplate(templateDir, workspaceDir); err != nil {
			return fmt.Errorf("copy template: %w", err)
		}
	}

	return nil
}

// writeFeishuConfig writes feishu credentials to {workspace}/.claude/skills/feishu_ops/feishu.json.
// The file sits next to the scripts that consume it, making path resolution trivial.
func writeFeishuConfig(workspaceDir, appID, appSecret string) error {
	feishuOpsDir := filepath.Join(workspaceDir, ".claude", "skills", "feishu_ops")
	if err := os.MkdirAll(feishuOpsDir, 0o755); err != nil {
		return fmt.Errorf("create feishu_ops dir: %w", err)
	}

	configPath := filepath.Join(feishuOpsDir, "feishu.json")
	if _, err := os.Stat(configPath); err == nil {
		// Already exists; update credentials in case they changed.
		return updateFeishuConfig(configPath, appID, appSecret)
	}

	return marshalFeishuConfig(configPath, appID, appSecret)
}

// updateFeishuConfig reads the existing feishu.json and updates app_id / app_secret.
func updateFeishuConfig(configPath, appID, appSecret string) error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("read feishu config: %w", err)
	}
	var creds map[string]string
	if err := json.Unmarshal(data, &creds); err != nil {
		// File is malformed; overwrite it.
		return marshalFeishuConfig(configPath, appID, appSecret)
	}
	if creds["app_id"] == appID && creds["app_secret"] == appSecret {
		return nil // nothing to do
	}
	creds["app_id"] = appID
	creds["app_secret"] = appSecret
	return marshalFeishuConfig(configPath, appID, appSecret)
}

func marshalFeishuConfig(configPath, appID, appSecret string) error {
	creds := map[string]string{
		"app_id":     appID,
		"app_secret": appSecret,
	}
	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}
	// 0o600: readable only by the process owner; secrets must not be world-readable.
	return os.WriteFile(configPath, append(data, '\n'), 0o600)
}

// copyTemplate copies files from src to dst, skipping files that already exist.
func copyTemplate(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}

		dstPath := filepath.Join(dst, relPath)

		if d.IsDir() {
			return os.MkdirAll(dstPath, 0o755)
		}

		// M-5: skip symlinks to prevent path traversal via crafted template dirs.
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}

		// Skip if destination already exists.
		if _, err := os.Stat(dstPath); err == nil {
			return nil
		}

		return copyFile(path, dstPath)
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}
