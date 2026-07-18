package claude

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseLine_GoldenTranscripts pins the text-concatenation semantics of
// parseLine against shared fixtures. The companion output-filter hook
// (workspaces/_companion/.claude/hooks/output_filter.py) extracts the same
// candidate text from the session transcript; its pytest suite consumes the
// SAME fixture files. If parseLine's semantics ever change, both sides must
// be updated together — this is the cross-language contract.
func TestParseLine_GoldenTranscripts(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "golden_transcripts", "*.jsonl"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no golden transcript fixtures found")
	}

	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".jsonl")
		t.Run(name, func(t *testing.T) {
			fh, err := os.Open(f)
			if err != nil {
				t.Fatalf("open fixture: %v", err)
			}
			defer fh.Close()

			e := &Executor{}
			result := &ExecuteResult{}
			sc := bufio.NewScanner(fh)
			sc.Buffer(make([]byte, 1024*1024), 1024*1024)
			for sc.Scan() {
				line := strings.TrimSpace(sc.Text())
				if line == "" {
					continue
				}
				e.parseLine(line, result)
			}
			if err := sc.Err(); err != nil {
				t.Fatalf("scan: %v", err)
			}

			expBytes, err := os.ReadFile(strings.TrimSuffix(f, ".jsonl") + ".expected.txt")
			if err != nil {
				t.Fatalf("read expected: %v", err)
			}
			if got, want := result.Text, string(expBytes); got != want {
				t.Errorf("concatenated text mismatch\n got: %q\nwant: %q", got, want)
			}
		})
	}
}
