package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ── replacePaths ─────────────────────────────────────────────────────────────

func TestReplacePaths_SingleAttachment(t *testing.T) {
	srcDir := t.TempDir()
	attachDir := filepath.Join(t.TempDir(), "attachments")
	if err := os.MkdirAll(attachDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a source file to "move".
	src := filepath.Join(srcDir, "image.jpg")
	if err := os.WriteFile(src, []byte("imgdata"), 0o644); err != nil {
		t.Fatal(err)
	}

	prompt := fmt.Sprintf("[图片: %s]", src)
	result := replacePaths(prompt, "[图片: ", attachDir)

	// The new path should be inside attachDir.
	if !strings.Contains(result, attachDir) {
		t.Errorf("result should contain attachDir, got: %s", result)
	}

	// Original file should be gone (renamed).
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Error("source file should have been moved")
	}

	// A file should now exist in attachDir.
	entries, _ := os.ReadDir(attachDir)
	if len(entries) == 0 {
		t.Error("attachDir should contain the moved file")
	}
}

func TestReplacePaths_MultipleAttachments(t *testing.T) {
	srcDir := t.TempDir()
	attachDir := filepath.Join(t.TempDir(), "attachments")
	if err := os.MkdirAll(attachDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create two source files.
	src1 := filepath.Join(srcDir, "a.jpg")
	src2 := filepath.Join(srcDir, "b.jpg")
	for _, p := range []string{src1, src2} {
		if err := os.WriteFile(p, []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Add a small sleep to ensure different UnixNano timestamps in filenames.
	prompt := fmt.Sprintf("[图片: %s] some text [图片: %s]", src1, src2)
	time.Sleep(time.Millisecond)
	result := replacePaths(prompt, "[图片: ", attachDir)

	// Both should be relocated.
	if strings.Contains(result, srcDir) {
		t.Errorf("result should not contain srcDir, got: %s", result)
	}

	entries, _ := os.ReadDir(attachDir)
	if len(entries) != 2 {
		t.Errorf("expected 2 files in attachDir, got %d", len(entries))
	}
}

func TestReplacePaths_AlreadyMoved(t *testing.T) {
	attachDir := filepath.Join(t.TempDir(), "attachments")
	if err := os.MkdirAll(attachDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// File is already inside attachDir — should not be moved again.
	existing := filepath.Join(attachDir, "already.jpg")
	if err := os.WriteFile(existing, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	prompt := fmt.Sprintf("[图片: %s]", existing)
	result := replacePaths(prompt, "[图片: ", attachDir)

	// Result should still reference the same path.
	if !strings.Contains(result, existing) {
		t.Errorf("already-moved path should be preserved, got: %s", result)
	}

	// Only one file should still be in attachDir (not duplicated).
	entries, _ := os.ReadDir(attachDir)
	if len(entries) != 1 {
		t.Errorf("expected 1 file in attachDir, got %d", len(entries))
	}
}

func TestReplacePaths_NoAttachments(t *testing.T) {
	attachDir := t.TempDir()
	prompt := "just a plain text message with no attachments"

	result := replacePaths(prompt, "[图片: ", attachDir)
	if result != prompt {
		t.Errorf("result = %q, want %q", result, prompt)
	}
}

func TestReplacePaths_MalformedReference(t *testing.T) {
	attachDir := t.TempDir()

	// Missing closing bracket — should emit rest verbatim.
	prompt := "[图片: /some/path"
	result := replacePaths(prompt, "[图片: ", attachDir)

	if result == "" {
		t.Error("result should not be empty for malformed input")
	}
	// Should not panic and should contain the remaining content.
	if !strings.Contains(result, "/some/path") {
		t.Errorf("malformed result should retain path text, got: %s", result)
	}
}

func TestReplacePaths_MissingSourceFile(t *testing.T) {
	attachDir := filepath.Join(t.TempDir(), "attachments")
	if err := os.MkdirAll(attachDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Source file doesn't exist — Rename will fail, original path should be kept.
	missingPath := "/nonexistent/path/file.jpg"
	prompt := fmt.Sprintf("[图片: %s]", missingPath)

	result := replacePaths(prompt, "[图片: ", attachDir)

	if !strings.Contains(result, missingPath) {
		t.Errorf("on rename failure, original path should be kept, got: %s", result)
	}
}

func TestReplacePaths_FilePrefix(t *testing.T) {
	srcDir := t.TempDir()
	attachDir := filepath.Join(t.TempDir(), "attachments")
	if err := os.MkdirAll(attachDir, 0o755); err != nil {
		t.Fatal(err)
	}

	src := filepath.Join(srcDir, "doc.pdf")
	if err := os.WriteFile(src, []byte("pdfdata"), 0o644); err != nil {
		t.Fatal(err)
	}

	prompt := fmt.Sprintf("[文件: %s]", src)
	result := replacePaths(prompt, "[文件: ", attachDir)

	if !strings.Contains(result, attachDir) {
		t.Errorf("file attachment should be relocated to attachDir, got: %s", result)
	}
}
