package session

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Companion output filter — framework side of the file protocol.
//
// The Stop hook in a companion workspace (.claude/hooks/output_filter.py)
// writes the cleaned reply to sessions/<id>/FINAL_REPLY.md before the claude
// CLI exits. The worker consumes that file and replaces result.Text so that
// sending, DB persistence and RECENT_HISTORY all see the filtered text.
// No file means the hook is absent, gated off, or crashed — fail-open.
// See docs/archive/companion-output-filter-design.md §4.5/§4.6.

const finalReplyFile = "FINAL_REPLY.md"

// mtimeTolerance absorbs filesystem timestamp granularity and small clock
// skew between execStart capture and the hook's write.
const mtimeTolerance = 2 * time.Second

// canaryThreshold is the number of consecutive companion replies without a
// FINAL_REPLY.md (after the filter has been seen working once) that triggers
// a silent-death warning.
const canaryThreshold = 5

// readFilteredReply reads and consumes sessions/<id>/FINAL_REPLY.md.
// The file is always deleted, even when stale, so a leftover can never leak
// into a later turn. found=true means a fresh file existed; its content may
// legitimately be empty only on hook malfunction (the v2 protocol replaces
// empty results with a persona fallback before writing).
func readFilteredReply(sessionDir string, execStart time.Time) (text string, found bool) {
	path := filepath.Join(sessionDir, finalReplyFile)
	info, err := os.Stat(path)
	if err != nil {
		return "", false
	}
	defer func() {
		if rmErr := os.Remove(path); rmErr != nil {
			slog.Warn("remove FINAL_REPLY.md", "err", rmErr, "path", path)
		}
	}()
	if info.ModTime().Before(execStart.Add(-mtimeTolerance)) {
		slog.Warn("stale FINAL_REPLY.md ignored", "path", path,
			"mtime", info.ModTime(), "exec_start", execStart)
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		slog.Warn("read FINAL_REPLY.md", "err", err, "path", path)
		return "", false
	}
	return strings.TrimSpace(string(data)), true
}

// filterCanary detects silent death of the output-filter hook (deleted hook,
// broken registration, persistent crash). It arms on the first observed hit
// so that companion workspaces without the filter installed never alarm.
type filterCanary struct {
	mu        sync.Mutex
	armed     bool
	misses    int
	threshold int
}

func newFilterCanary(threshold int) *filterCanary {
	return &filterCanary{threshold: threshold}
}

// observe records whether FINAL_REPLY.md was seen this turn. It returns true
// when the consecutive-miss count reaches a multiple of the threshold, i.e.
// the caller should emit a warning now (and again periodically while the
// outage persists).
func (c *filterCanary) observe(found bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if found {
		c.armed = true
		c.misses = 0
		return false
	}
	if !c.armed {
		return false
	}
	c.misses++
	return c.misses%c.threshold == 0
}

// applyOutputFilter returns the reply text to use for a companion turn:
// the hook-filtered text when a fresh FINAL_REPLY.md exists, otherwise the
// raw text unchanged (fail-open). It never returns empty when rawText is
// non-empty, so the empty-text guard downstream cannot be tripped by the
// filter itself.
func (w *Worker) applyOutputFilter(rawText, sessionID string, execStart time.Time) string {
	sessionDir := filepath.Join(w.appCfg.WorkspaceDir, "sessions", sessionID)
	filtered, found := readFilteredReply(sessionDir, execStart)
	if w.canary != nil && w.canary.observe(found) {
		slog.Warn("companion output filter canary: FINAL_REPLY.md missing for consecutive replies",
			"channel", w.channelKey, "threshold", canaryThreshold)
	}
	if !found {
		return rawText
	}
	if filtered == "" {
		// v2 protocol should never write an empty file (persona fallback
		// replaces empty results). Defensive: keep the raw text rather than
		// triggering the empty-text guard with an in-character /new nudge.
		slog.Warn("output filter produced empty FINAL_REPLY.md, using raw text",
			"channel", w.channelKey)
		return rawText
	}
	return filtered
}

// attachmentAckText returns the acknowledgement for attachment-only messages.
// Work mode keeps the instructional wording; companion mode must stay in
// voice — operational phrasing is an immersion break (requirements FR/范围:
// 框架硬编码消息 persona 化).
func attachmentAckText(isCompanion bool, prompt string) string {
	if isCompanion {
		return "嗯，看到了。想跟我说说这是什么吗。"
	}
	return attachmentReplyText(prompt)
}

// newSessionReceipt returns the /new confirmation message.
func newSessionReceipt(isCompanion bool) string {
	if isCompanion {
		return "嗯，那我们重新开始。"
	}
	return "✅ 已开启新会话"
}
