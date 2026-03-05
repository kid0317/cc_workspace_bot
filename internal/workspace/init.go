package workspace

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// Init ensures the workspace directory exists and has the required subdirectories.
// If a _template directory is provided, it copies templates on first init.
func Init(workspaceDir string, templateDir string) error {
	// Create required subdirectories.
	dirs := []string{
		workspaceDir,
		filepath.Join(workspaceDir, "skills"),
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

	// Copy template files if template dir is set and workspace is empty.
	if templateDir != "" {
		if err := copyTemplate(templateDir, workspaceDir); err != nil {
			return fmt.Errorf("copy template: %w", err)
		}
	}

	return nil
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
