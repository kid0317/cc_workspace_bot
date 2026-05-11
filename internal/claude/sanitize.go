package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ResumeRecoverablePattern matches Anthropic API 400 errors that originate from
// foreign-provider pollution in the resume jsonl (e.g. kimi/qwen via the bailian
// anthropic-compatible bridge):
//   - thinking blocks with empty/invalid signature
//   - tool_use.id with characters outside [a-zA-Z0-9_-] (the bridge wraps
//     OpenAI-style "functions.Bash:13" into "toolu_functions.Bash:13" without
//     sanitizing).
//
// Both can be repaired by dropping the offending non-Anthropic messages from
// the jsonl before re-running --resume.
var ResumeRecoverablePattern = regexp.MustCompile(
	"Invalid `signature` in `thinking` block" +
		`|tool_use\.id: String should match pattern`,
)

// IsResumeRecoverable reports whether the combined output/error text contains
// a known signature that can be fixed by sanitizing the resume jsonl.
func IsResumeRecoverable(s string) bool {
	if s == "" {
		return false
	}
	return ResumeRecoverablePattern.MatchString(s)
}

// cwdToProjectDir converts a cwd path to the corresponding ~/.claude/projects
// directory. Claude CLI flattens the absolute cwd by replacing each '/' and
// '_' with '-'. Example: /root/xh_yibu/sessions/abc → -root-xh-yibu-sessions-abc.
//
// Limitation: the flatten rule is non-injective. Paths like /root/foo/sessions
// and /root/foo_sessions both flatten to "-root-foo-sessions". Two workspaces
// whose flat names collide will share the same project directory in
// ~/.claude/projects/. This is a Claude CLI namespace property, not introduced
// here — the upstream resume mechanism has the same collision. Workspace dirs
// must be chosen so their flat names are unique (callers are expected to keep
// config.yaml paths well-behaved).
func cwdToProjectDir(cwd string) (string, error) {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return "", fmt.Errorf("resolve cwd: %w", err)
	}
	flat := strings.ReplaceAll(abs, "/", "-")
	flat = strings.ReplaceAll(flat, "_", "-")
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, ".claude", "projects", flat), nil
}

// isForeignAssistant reports whether the message is from a non-Anthropic,
// non-synthetic, non-empty provider — i.e. routed through a bridge that may
// emit empty thinking signatures or non-conforming tool_use IDs.
func isForeignAssistant(msg map[string]any) bool {
	model, _ := msg["model"].(string)
	if model == "" || model == "<synthetic>" {
		return false
	}
	return !strings.HasPrefix(model, "claude-")
}

// rebuildForeignContent walks a foreign assistant message's content blocks
// and keeps only those that the Anthropic API can validate (text and unknown
// safe types). It strips:
//   - thinking blocks (signature is empty/invalid)
//   - tool_use blocks (id violates ^[a-zA-Z0-9_-]+$)
//
// Returns the rebuilt content slice, the tool_use IDs that were stripped (so
// the caller can drop matching tool_result entries elsewhere), and whether
// any text content was preserved.
func rebuildForeignContent(content []any) (newContent []any, droppedToolUseIDs []string, hasText bool) {
	for _, c := range content {
		cm, ok := c.(map[string]any)
		if !ok {
			newContent = append(newContent, c)
			continue
		}
		switch t, _ := cm["type"].(string); t {
		case "thinking":
			// Drop — empty signature from the bailian bridge.
		case "tool_use":
			if id, _ := cm["id"].(string); id != "" {
				droppedToolUseIDs = append(droppedToolUseIDs, id)
			}
		case "text":
			newContent = append(newContent, c)
			if s, _ := cm["text"].(string); strings.TrimSpace(s) != "" {
				hasText = true
			}
		default:
			// Unknown block type — keep to be safe.
			newContent = append(newContent, c)
		}
	}
	return
}

// classifyForeignRecord inspects a parsed jsonl record. If it is a foreign
// assistant message, it returns:
//   - drop=true, rewrite=nil  → the entire line should be removed
//   - drop=false, rewrite=<bytes>  → the line should be replaced by these bytes
//     (text-only content, model retagged as "<synthetic>" so future passes
//     treat it as already-cleaned)
//
// toolUseIDs always lists the tool_use IDs that must be considered orphaned
// (i.e. their matching tool_result entries should be dropped), regardless of
// whether the message itself was dropped or rewritten.
//
// If the record is not a foreign message, returns drop=false, rewrite=nil,
// toolUseIDs=nil.
//
// The input rec is NOT mutated. Internally a shallow copy of the message map
// is made before retagging, so the caller's map remains pristine and safe
// for downstream inspection (orphan detection in pass 2 of analyzeJSONL).
func classifyForeignRecord(rec map[string]any) (drop bool, rewrite []byte, toolUseIDs []string) {
	msg, ok := rec["message"].(map[string]any)
	if !ok || !isForeignAssistant(msg) {
		return false, nil, nil
	}
	content, _ := msg["content"].([]any)
	newContent, ids, hasText := rebuildForeignContent(content)
	if !hasText {
		return true, nil, ids
	}
	// Build the rewritten record on shallow copies — never mutate the caller's
	// rec or its message map. analyzeJSONL still reads lines[i].rec in pass 2
	// (for tool_result orphan detection), and that pass must see the
	// original content layout.
	newMsg := make(map[string]any, len(msg)+1)
	for k, v := range msg {
		newMsg[k] = v
	}
	newMsg["content"] = newContent
	newMsg["model"] = "<synthetic>"
	newRec := make(map[string]any, len(rec))
	for k, v := range rec {
		newRec[k] = v
	}
	newRec["message"] = newMsg
	out, err := marshalNoHTMLEscape(newRec)
	if err != nil {
		// Marshal of an already-parsed map shouldn't fail in practice; if it
		// does, fall back to dropping the line so we don't leave the bad data.
		return true, nil, ids
	}
	return false, out, ids
}

// marshalNoHTMLEscape serialises v to JSON without escaping <, >, & — keeping
// human-readable values like "<synthetic>" intact (encoding/json's default
// would emit "<synthetic>", which parses to the same string but
// produces an unfamiliar transcript byte-stream).
func marshalNoHTMLEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	// Encode appends a trailing newline; trim it so the caller controls newlines.
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// referencesAnyToolUseID reports whether the record contains a tool_result
// pointing at one of the orphaned tool_use IDs.
func referencesAnyToolUseID(rec map[string]any, ids map[string]struct{}) bool {
	if len(ids) == 0 {
		return false
	}
	msg, ok := rec["message"].(map[string]any)
	if !ok {
		return false
	}
	content, ok := msg["content"].([]any)
	if !ok {
		return false
	}
	for _, c := range content {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := cm["type"].(string); t != "tool_result" {
			continue
		}
		if id, _ := cm["tool_use_id"].(string); id != "" {
			if _, hit := ids[id]; hit {
				return true
			}
		}
	}
	return false
}

// jsonlLine is a parsed-or-raw row from a session jsonl, annotated with the
// action computed by analyzeJSONL.
type jsonlLine struct {
	raw     []byte          // original bytes (without trailing newline)
	rec     map[string]any  // parsed record, nil for empty/non-JSON lines
	drop    bool            // remove the line entirely
	rewrite []byte          // when non-nil and !drop, replaces raw on output
}

// SanitizeStats summarises what analyzeJSONL would do (or did) to a file.
type SanitizeStats struct {
	// Dropped is the number of lines fully removed. Lines are dropped when:
	//   - they are foreign assistant messages with no salvageable text, or
	//   - they are tool_result entries pointing at a stripped foreign tool_use.
	Dropped int
	// Rewritten is the number of foreign assistant messages whose content
	// was rebuilt to keep only text blocks (and retagged as "<synthetic>").
	Rewritten int
}

// Affected returns the total number of lines either dropped or rewritten.
func (s SanitizeStats) Affected() int { return s.Dropped + s.Rewritten }

// analyzeJSONL parses src into per-line records and decides the action for
// each (keep / drop / rewrite). Pure: it does no I/O.
func analyzeJSONL(src []byte) ([]jsonlLine, SanitizeStats, error) {
	var lines []jsonlLine
	orphaned := make(map[string]struct{})

	sc := bufio.NewScanner(bytes.NewReader(src))
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		raw := append([]byte(nil), sc.Bytes()...)
		if len(bytes.TrimSpace(raw)) == 0 {
			lines = append(lines, jsonlLine{raw: raw})
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal(raw, &rec); err != nil {
			lines = append(lines, jsonlLine{raw: raw})
			continue
		}
		drop, rewrite, ids := classifyForeignRecord(rec)
		for _, id := range ids {
			orphaned[id] = struct{}{}
		}
		lines = append(lines, jsonlLine{raw: raw, rec: rec, drop: drop, rewrite: rewrite})
	}
	if err := sc.Err(); err != nil {
		return nil, SanitizeStats{}, fmt.Errorf("scan jsonl: %w", err)
	}

	// Pass 2: drop tool_result entries pointing at orphaned tool_use IDs.
	// Foreign messages that were rewritten to text-only no longer contain
	// tool_use blocks, so they will not match referencesAnyToolUseID.
	for i := range lines {
		if lines[i].rec == nil || lines[i].drop {
			continue
		}
		if referencesAnyToolUseID(lines[i].rec, orphaned) {
			lines[i].drop = true
			lines[i].rewrite = nil
		}
	}

	var stats SanitizeStats
	for _, ln := range lines {
		switch {
		case ln.drop:
			stats.Dropped++
		case ln.rewrite != nil:
			stats.Rewritten++
		}
	}
	return lines, stats, nil
}

// AnalyzeJSONL reports how many lines would be dropped or rewritten if path
// were sanitized. Read-only; never modifies the file.
func AnalyzeJSONL(path string) (SanitizeStats, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return SanitizeStats{}, fmt.Errorf("read jsonl: %w", err)
	}
	_, stats, err := analyzeJSONL(src)
	return stats, err
}

// SanitizeJSONL rewrites path: foreign assistant messages have their
// thinking/tool_use blocks stripped (text content preserved, model retagged
// as "<synthetic>"), and orphaned tool_result entries are dropped. Returns
// the action counts. The original file is preserved as path + ".bak" when
// anything changes. Idempotent: a second call returns zeros.
func SanitizeJSONL(path string) (SanitizeStats, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return SanitizeStats{}, fmt.Errorf("read jsonl: %w", err)
	}
	lines, stats, err := analyzeJSONL(src)
	if err != nil {
		return SanitizeStats{}, err
	}
	if stats.Affected() == 0 {
		return stats, nil
	}

	backup := path + ".bak"
	if err := os.WriteFile(backup, src, 0o600); err != nil {
		return SanitizeStats{}, fmt.Errorf("backup jsonl: %w", err)
	}

	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return SanitizeStats{}, fmt.Errorf("create tmp: %w", err)
	}
	w := bufio.NewWriter(f)
	writeFail := func(err error) (SanitizeStats, error) {
		_ = f.Close()
		_ = os.Remove(tmp)
		return SanitizeStats{}, err
	}
	for _, ln := range lines {
		if ln.drop {
			continue
		}
		out := ln.raw
		if ln.rewrite != nil {
			out = ln.rewrite
		}
		if _, err := w.Write(out); err != nil {
			return writeFail(fmt.Errorf("write tmp: %w", err))
		}
		if err := w.WriteByte('\n'); err != nil {
			return writeFail(fmt.Errorf("write tmp: %w", err))
		}
	}
	if err := w.Flush(); err != nil {
		return writeFail(fmt.Errorf("flush tmp: %w", err))
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return SanitizeStats{}, fmt.Errorf("close tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return SanitizeStats{}, fmt.Errorf("replace jsonl: %w", err)
	}
	return stats, nil
}

// SanitizeResumeForCwd locates the jsonl that corresponds to (cwd,
// claudeSessionID) and sanitizes it. Returns true when at least one line was
// dropped or rewritten. A missing jsonl is not an error (returns false, nil).
func SanitizeResumeForCwd(cwd, claudeSessionID string) (bool, error) {
	if claudeSessionID == "" {
		return false, nil
	}
	projDir, err := cwdToProjectDir(cwd)
	if err != nil {
		return false, err
	}
	path := filepath.Join(projDir, claudeSessionID+".jsonl")
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("stat jsonl: %w", err)
	}
	stats, err := SanitizeJSONL(path)
	if err != nil {
		return false, err
	}
	if stats.Affected() > 0 {
		slog.Warn("sanitized resume jsonl",
			"path", path,
			"dropped", stats.Dropped,
			"rewritten", stats.Rewritten,
			"backup", path+".bak")
	}
	return stats.Affected() > 0, nil
}
