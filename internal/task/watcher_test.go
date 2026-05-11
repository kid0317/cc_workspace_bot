package task

import (
	"fmt"
	"sync"
	"testing"
)

// newTestWatcher returns a minimal Watcher with just the error-cache plumbing
// populated — enough to exercise shouldLogParseErr / forgetPath / pruneErrCache
// without standing up fsnotify, scheduler, or DB dependencies.
func newTestWatcher() *Watcher {
	return &Watcher{
		dirAppIDs: make(map[string]string),
		errCache:  make(map[parseErrKey]struct{}),
	}
}

func TestShouldLogParseErr_DedupAndHashBreakthrough(t *testing.T) {
	w := newTestWatcher()

	k1 := parseErrKey{path: "/a/x.yaml", contentHash: "hash1", msg: "target_type is required"}
	if !w.shouldLogParseErr(k1) {
		t.Fatal("first call for a new key should log")
	}
	if w.shouldLogParseErr(k1) {
		t.Fatal("repeat call for identical key should be deduped")
	}

	// Same path, same error, different content — mtime-preserving edits like
	// `touch -r` must NOT be suppressed.
	k2 := parseErrKey{path: "/a/x.yaml", contentHash: "hash2", msg: "target_type is required"}
	if !w.shouldLogParseErr(k2) {
		t.Fatal("hash change should break through dedup")
	}

	// Same path, same hash, different error class — also breaks through.
	k3 := parseErrKey{path: "/a/x.yaml", contentHash: "hash1", msg: "invalid cron expression"}
	if !w.shouldLogParseErr(k3) {
		t.Fatal("new error class should break through dedup")
	}
}

func TestShouldLogParseErr_CapDropsNewEntries(t *testing.T) {
	w := newTestWatcher()
	for i := 0; i < maxErrCacheEntries; i++ {
		k := parseErrKey{path: "/a.yaml", contentHash: fmt.Sprintf("%d", i), msg: "err"}
		if !w.shouldLogParseErr(k) {
			t.Fatalf("pre-cap entry %d should be accepted", i)
		}
	}
	// Cache is full now. A new key must be dropped (return false), and
	// crucially must NOT be inserted — so a subsequent call is still dropped.
	overflow := parseErrKey{path: "/a.yaml", contentHash: "overflow", msg: "err"}
	if w.shouldLogParseErr(overflow) {
		t.Error("new key at cap should be dropped")
	}
	if w.shouldLogParseErr(overflow) {
		t.Error("dropped key must not have been inserted")
	}
}

func TestForgetPath_ReallowsNewErrorsAfterSuccess(t *testing.T) {
	w := newTestWatcher()
	k := parseErrKey{path: "/a/x.yaml", contentHash: "hash1", msg: "err"}

	if !w.shouldLogParseErr(k) {
		t.Fatal("initial error should log")
	}
	if w.shouldLogParseErr(k) {
		t.Fatal("dedup should suppress repeat")
	}

	// Successful parse removes all entries for this path.
	w.forgetPath("/a/x.yaml")

	// Unrelated paths are untouched.
	other := parseErrKey{path: "/b/y.yaml", contentHash: "h", msg: "err"}
	if !w.shouldLogParseErr(other) {
		t.Error("other-path entry should still log")
	}

	// The previously-seen key can log again, so a regression on the same
	// file is not silently suppressed.
	if !w.shouldLogParseErr(k) {
		t.Error("forgetPath should requalify previously-seen key")
	}
}

func TestPruneErrCache_DropsMissingPathsOnly(t *testing.T) {
	w := newTestWatcher()
	keep := parseErrKey{path: "/still/here.yaml", contentHash: "h", msg: "err"}
	gone := parseErrKey{path: "/deleted.yaml", contentHash: "h", msg: "err"}
	w.shouldLogParseErr(keep)
	w.shouldLogParseErr(gone)

	w.pruneErrCache(map[string]struct{}{"/still/here.yaml": {}})

	w.errMu.Lock()
	defer w.errMu.Unlock()
	if _, ok := w.errCache[keep]; !ok {
		t.Error("present path should be retained")
	}
	if _, ok := w.errCache[gone]; ok {
		t.Error("missing path should be pruned")
	}
}

func TestShouldLogParseErr_ConcurrentAccess(t *testing.T) {
	// Race detector verifies that errMu protects errCache under contention.
	// Also asserts we never over-insert past the cap.
	w := newTestWatcher()
	const writers = 16
	const perWriter = 200

	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for j := 0; j < perWriter; j++ {
				k := parseErrKey{
					path:        "/a.yaml",
					contentHash: fmt.Sprintf("w%d-%d", worker, j),
					msg:         "err",
				}
				w.shouldLogParseErr(k)
			}
		}(i)
	}
	wg.Wait()

	w.errMu.Lock()
	defer w.errMu.Unlock()
	if len(w.errCache) > maxErrCacheEntries {
		t.Errorf("cache size %d exceeded cap %d", len(w.errCache), maxErrCacheEntries)
	}
}

func TestHashContent_StableAndDistinct(t *testing.T) {
	a := hashContent([]byte("one"))
	b := hashContent([]byte("one"))
	c := hashContent([]byte("two"))
	if a != b {
		t.Errorf("hashContent not deterministic: %q vs %q", a, b)
	}
	if a == c {
		t.Error("different content should hash to different value")
	}
	if len(a) != 16 {
		t.Errorf("unexpected hash length %d (want 16)", len(a))
	}
}

func TestClassifyTarget_AllModes(t *testing.T) {
	tests := []struct {
		name       string
		sendOutput bool
		tt, ti     string
		want       targetMode
	}{
		{"user reply", true, "p2p", "ou_abc", modeUserReply},
		{"send=true missing target_type", true, "", "ou_abc", modeInvalid},
		{"send=true missing target_id", true, "p2p", "", modeInvalid},
		{"send=true both empty", true, "", "", modeInvalid},
		{"borrow channel", false, "p2p", "ou_abc", modeBorrowChannel},
		{"system task", false, "", "", modeSystem},
		{"send=false type only", false, "p2p", "", modeInvalid},
		{"send=false id only", false, "", "ou_abc", modeInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyTarget(tt.sendOutput, tt.tt, tt.ti); got != tt.want {
				t.Errorf("classifyTarget(%v,%q,%q) = %d, want %d",
					tt.sendOutput, tt.tt, tt.ti, got, tt.want)
			}
		})
	}
}
