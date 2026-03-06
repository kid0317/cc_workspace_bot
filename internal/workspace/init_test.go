package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInit_CreatesRequiredDirs(t *testing.T) {
	dir := t.TempDir()
	workspaceDir := filepath.Join(dir, "workspace")

	if err := Init(workspaceDir, ""); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	for _, sub := range []string{"skills", "memory", "tasks", "sessions"} {
		path := filepath.Join(workspaceDir, sub)
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			t.Errorf("expected dir %s to exist", path)
		}
	}
}

func TestInit_CreatesMemoryLock(t *testing.T) {
	dir := t.TempDir()
	workspaceDir := filepath.Join(dir, "workspace")

	if err := Init(workspaceDir, ""); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	lockPath := filepath.Join(workspaceDir, ".memory.lock")
	if _, err := os.Stat(lockPath); err != nil {
		t.Errorf("expected .memory.lock to exist: %v", err)
	}
}

func TestInit_DoesNotOverwriteExistingLock(t *testing.T) {
	dir := t.TempDir()
	workspaceDir := filepath.Join(dir, "workspace")

	if err := Init(workspaceDir, ""); err != nil {
		t.Fatal(err)
	}

	lockPath := filepath.Join(workspaceDir, ".memory.lock")
	if err := os.WriteFile(lockPath, []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Second Init should not erase the existing lock content.
	if err := Init(workspaceDir, ""); err != nil {
		t.Fatalf("second Init() error = %v", err)
	}

	data, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "existing" {
		t.Errorf("lock content = %q, want %q", string(data), "existing")
	}
}

func TestInit_Idempotent(t *testing.T) {
	dir := t.TempDir()
	workspaceDir := filepath.Join(dir, "workspace")

	for range 3 {
		if err := Init(workspaceDir, ""); err != nil {
			t.Fatalf("Init() error = %v", err)
		}
	}
}

func TestInit_CopiesTemplate(t *testing.T) {
	templateDir := t.TempDir()
	workspaceDir := filepath.Join(t.TempDir(), "workspace")

	// Create template files.
	if err := os.WriteFile(filepath.Join(templateDir, "CLAUDE.md"), []byte("template content"), 0o644); err != nil {
		t.Fatal(err)
	}
	skillsDir := filepath.Join(templateDir, "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "memory.md"), []byte("skill content"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Init(workspaceDir, templateDir); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	for _, rel := range []string{"CLAUDE.md", "skills/memory.md"} {
		data, err := os.ReadFile(filepath.Join(workspaceDir, rel))
		if err != nil {
			t.Errorf("expected %s to be copied: %v", rel, err)
			continue
		}
		if len(data) == 0 {
			t.Errorf("copied file %s is empty", rel)
		}
	}
}

func TestInit_TemplateDoesNotOverwriteExisting(t *testing.T) {
	templateDir := t.TempDir()
	workspaceDir := filepath.Join(t.TempDir(), "workspace")

	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write an existing file in workspace before Init.
	existing := filepath.Join(workspaceDir, "CLAUDE.md")
	if err := os.WriteFile(existing, []byte("user content"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Template has same file with different content.
	if err := os.WriteFile(filepath.Join(templateDir, "CLAUDE.md"), []byte("template content"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Init(workspaceDir, templateDir); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	data, err := os.ReadFile(existing)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "user content" {
		t.Errorf("existing file was overwritten: got %q", string(data))
	}
}

func TestCopyTemplate_SkipsSymlinks(t *testing.T) {
	templateDir := t.TempDir()
	workspaceDir := filepath.Join(t.TempDir(), "workspace")

	// Create a real file and a symlink in the template dir.
	real := filepath.Join(templateDir, "real.md")
	if err := os.WriteFile(real, []byte("real"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(templateDir, "link.md")); err != nil {
		t.Skip("symlinks not supported on this platform")
	}

	if err := Init(workspaceDir, templateDir); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	// real.md should be copied; link.md should NOT be created.
	if _, err := os.Stat(filepath.Join(workspaceDir, "real.md")); err != nil {
		t.Error("real.md should have been copied")
	}
	if _, err := os.Stat(filepath.Join(workspaceDir, "link.md")); err == nil {
		t.Error("link.md (symlink) should NOT have been copied")
	}
}
