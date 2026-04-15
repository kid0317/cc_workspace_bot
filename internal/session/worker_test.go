package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/kid0317/cc-workspace-bot/internal/config"
	"github.com/kid0317/cc-workspace-bot/internal/feishu"
	"github.com/kid0317/cc-workspace-bot/internal/model"
)

// ── mock sender ──────────────────────────────────────────────────────────────

// mockSender records calls for inspection in tests.
type mockSender struct {
	mu           sync.Mutex
	sendTexts    []string    // args to SendText
	sendTextErrs []error     // errors to return, cycled
	updateCards  []string    // args to UpdateCard
	errIdx       int
}

func (m *mockSender) SendText(_ context.Context, _, _, text string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sendTexts = append(m.sendTexts, text)
	if len(m.sendTextErrs) > 0 {
		err := m.sendTextErrs[m.errIdx%len(m.sendTextErrs)]
		m.errIdx++
		return "", err
	}
	return "msg-id", nil
}

func (m *mockSender) SendThinking(_ context.Context, _, _ string) (string, error) {
	return "card-id", nil
}

func (m *mockSender) UpdateCard(_ context.Context, _, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updateCards = append(m.updateCards, text)
	return nil
}

func (m *mockSender) SendCard(_ context.Context, _, _, _ string) (string, error) {
	return "card-id", nil
}

// ── test helpers ─────────────────────────────────────────────────────────────

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&model.Session{}, &model.Message{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func newTestWorker(t *testing.T, appCfg *config.AppConfig, mock *mockSender, opts *SegmentOptions) *Worker {
	t.Helper()
	db := newTestDB(t)
	w := &Worker{
		channelKey:  "p2p:test:app1",
		appCfg:      appCfg,
		db:          db,
		sender:      nil, // concrete sender unused — we use senderIface
		senderIface: mock,
		idleTimeout: 30 * time.Minute,
		queue:       make(chan *feishu.IncomingMessage, 64),
		stopCh:      make(chan struct{}),
		segmentOpts: DefaultSegmentOptions(),
	}
	if opts != nil {
		w.segmentOpts = *opts
	}
	return w
}

func companionApp(t *testing.T) *config.AppConfig {
	t.Helper()
	dir := t.TempDir()
	return &config.AppConfig{
		ID:            "test-companion",
		WorkspaceMode: "companion",
		WorkspaceDir:  dir,
	}
}

func workApp(t *testing.T) *config.AppConfig {
	t.Helper()
	dir := t.TempDir()
	return &config.AppConfig{
		ID:           "test-work",
		WorkspaceDir: dir,
	}
}

func testMsg() *feishu.IncomingMessage {
	return &feishu.IncomingMessage{
		ReceiveID:   "user-123",
		ReceiveType: "open_id",
		SenderID:    "sender-1",
	}
}

// zeroDelayOpts returns SegmentOptions with zero delays for fast tests.
func zeroDelayOpts() SegmentOptions {
	return SegmentOptions{
		Delimiter:           "[[SEND]]",
		MaxRunes:            80,
		MinRunes:            2,
		MaxFallbackSegments: 3,
		BaseDelay:           0,
		PerReadRune:         0,
		PerTypeRune:         0,
		MinDelay:            0,
		MaxDelay:            1 * time.Millisecond,
		FirstMinDelay:       0,
		FirstMaxDelay:       1 * time.Millisecond,
		JitterFraction:      0,
	}
}

// ── isRateLimitError ──────────────────────────────────────────────────────────

func TestIsRateLimitError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"rate limit code", fmt.Errorf("send text API error: code=99991400 msg=rate limit"), true},
		{"other error", fmt.Errorf("send text API error: code=400 msg=bad request"), false},
		{"contains code", fmt.Errorf("error 99991400"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRateLimitError(tt.err)
			if got != tt.want {
				t.Errorf("isRateLimitError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// ── Phase 2 worker tests ──────────────────────────────────────────────────────

// TestWorker_CompanionMultiSegment verifies companion mode splits text and calls
// SendText once per segment in the correct order.
func TestWorker_CompanionMultiSegment(t *testing.T) {
	mock := &mockSender{}
	opts := zeroDelayOpts()
	w := newTestWorker(t, companionApp(t), mock, &opts)

	msg := testMsg()
	text := "段一[[SEND]]段二[[SEND]]段三"
	w.sendResult(context.Background(), msg, "", text)

	mock.mu.Lock()
	got := mock.sendTexts
	mock.mu.Unlock()

	want := []string{"段一", "段二", "段三"}
	if len(got) != len(want) {
		t.Fatalf("SendText called %d times, want %d; calls: %v", len(got), len(want), got)
	}
	for i, seg := range want {
		if got[i] != seg {
			t.Errorf("SendText[%d] = %q, want %q", i, got[i], seg)
		}
	}
}

// TestWorker_NonCompanionUnchanged verifies work mode calls UpdateCard, not SendText.
func TestWorker_NonCompanionUnchanged(t *testing.T) {
	mock := &mockSender{}
	opts := zeroDelayOpts()
	w := newTestWorker(t, workApp(t), mock, &opts)

	msg := testMsg()
	text := "result text[[SEND]]should not split"
	w.sendResult(context.Background(), msg, "card-msg-id", text)

	mock.mu.Lock()
	cards := mock.updateCards
	texts := mock.sendTexts
	mock.mu.Unlock()

	if len(texts) != 0 {
		t.Errorf("SendText called %d times in work mode, want 0", len(texts))
	}
	if len(cards) != 1 {
		t.Fatalf("UpdateCard called %d times, want 1", len(cards))
	}
	if cards[0] != text {
		t.Errorf("UpdateCard got %q, want %q", cards[0], text)
	}
}

// TestWorker_SegmentContinueOnError verifies that a SendText error on one segment
// does not prevent subsequent segments from being sent.
func TestWorker_SegmentContinueOnError(t *testing.T) {
	// Return error on the 2nd call (index 1), success on others
	mock := &mockSender{
		sendTextErrs: []error{nil, fmt.Errorf("send error"), nil},
	}
	opts := zeroDelayOpts()
	w := newTestWorker(t, companionApp(t), mock, &opts)

	msg := testMsg()
	text := "段一[[SEND]]段二[[SEND]]段三"
	w.sendResult(context.Background(), msg, "", text)

	mock.mu.Lock()
	got := mock.sendTexts
	mock.mu.Unlock()

	// All 3 segments should have been attempted
	if len(got) != 3 {
		t.Errorf("expected 3 SendText calls despite error, got %d: %v", len(got), got)
	}
}

// TestWorker_SegmentCtxCancelled verifies that ctx cancellation during segment
// sending stops the loop without goroutine leaks.
func TestWorker_SegmentCtxCancelled(t *testing.T) {
	// Use a delay long enough that ctx cancel fires during the sleep
	slowOpts := SegmentOptions{
		Delimiter:      "[[SEND]]",
		MaxRunes:       80,
		MinRunes:       2,
		BaseDelay:      0,
		PerReadRune:    0,
		PerTypeRune:    0,
		MinDelay:       0,
		MaxDelay:       50 * time.Millisecond,
		FirstMinDelay:  0,
		FirstMaxDelay:  50 * time.Millisecond,
		JitterFraction: 0,
	}
	mock := &mockSender{}
	w := newTestWorker(t, companionApp(t), mock, &slowOpts)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		text := "段一[[SEND]]段二[[SEND]]段三"
		w.sendResult(ctx, testMsg(), "", text)
	}()

	// Cancel after first segment has time to be sent
	time.Sleep(5 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// good — goroutine exited
	case <-time.After(2 * time.Second):
		t.Fatal("sendResult did not exit after ctx cancellation")
	}
}

// TestWorker_PersistResultStripsDelimiter verifies that [[SEND]] is replaced
// with \n in the DB, never stored as-is.
func TestWorker_PersistResultStripsDelimiter(t *testing.T) {
	mock := &mockSender{}
	w := newTestWorker(t, companionApp(t), mock, nil)

	// Create a session to persist into
	db := w.db
	sess := &model.Session{
		ID:         "test-session-id",
		ChannelKey: w.channelKey,
		Status:     statusActive,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	if err := db.Create(sess).Error; err != nil {
		t.Fatalf("create session: %v", err)
	}

	result := &claudeResult{
		ClaudeSessionID: "",
		Text:            "段一[[SEND]]段二[[SEND]]段三",
	}
	w.persistResult(sess, result)

	// Verify DB content has no [[SEND]]
	var msg model.Message
	if err := db.Where("session_id = ? AND role = ?", sess.ID, "assistant").First(&msg).Error; err != nil {
		t.Fatalf("find assistant message: %v", err)
	}
	if strings.Contains(msg.Content, "[[SEND]]") {
		t.Errorf("DB content still contains [[SEND]]: %q", msg.Content)
	}
	// Should contain newlines instead
	if !strings.Contains(msg.Content, "\n") {
		t.Errorf("DB content should contain newlines, got: %q", msg.Content)
	}
}

// TestWorker_RateLimitRetry verifies that a rate-limit error triggers a single
// retry for the affected segment, and that subsequent segments still proceed.
func TestWorker_RateLimitRetry(t *testing.T) {
	// Segment 1: rate-limit on first attempt, success on retry.
	// Segment 2: immediate success.
	rateLimitErr := fmt.Errorf("send text API error: code=99991400 msg=rate limit exceeded")
	mock := &mockSender{
		sendTextErrs: []error{rateLimitErr, nil, nil},
	}
	opts := zeroDelayOpts()
	w := newTestWorker(t, companionApp(t), mock, &opts)

	msg := testMsg()
	text := "段一[[SEND]]段二"
	w.sendResult(context.Background(), msg, "", text)

	mock.mu.Lock()
	got := mock.sendTexts
	mock.mu.Unlock()

	// Segment 1 attempted twice (retry), segment 2 once = 3 total calls.
	if len(got) != 3 {
		t.Fatalf("expected 3 SendText calls (retry on rate-limit), got %d: %v", len(got), got)
	}
	if got[0] != "段一" || got[1] != "段一" {
		t.Errorf("expected retry of segment 1, got calls[0]=%q calls[1]=%q", got[0], got[1])
	}
	if got[2] != "段二" {
		t.Errorf("expected segment 2 after retry, got %q", got[2])
	}
}

// TestWorker_RateLimitRetryCtxCancelled verifies that ctx cancellation during
// the rate-limit backoff sleep stops the send loop cleanly.
func TestWorker_RateLimitRetryCtxCancelled(t *testing.T) {
	rateLimitErr := fmt.Errorf("code=99991400 rate limit")
	// Fail every call so the retry also fails; we want to test the backoff select.
	mock := &mockSender{
		sendTextErrs: []error{rateLimitErr, rateLimitErr, rateLimitErr},
	}
	// Use real (non-zero) backoff so there's time to cancel.
	slowOpts := SegmentOptions{
		Delimiter:      "[[SEND]]",
		MaxRunes:       80,
		MinRunes:       2,
		BaseDelay:      0,
		MaxDelay:       1 * time.Millisecond,
		FirstMaxDelay:  1 * time.Millisecond,
		JitterFraction: 0,
	}
	w := newTestWorker(t, companionApp(t), mock, &slowOpts)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.sendResult(ctx, testMsg(), "", "段一[[SEND]]段二[[SEND]]段三")
	}()

	cancel() // cancel immediately; backoff select should wake on ctx.Done()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("sendResult did not exit after ctx cancellation during rate-limit backoff")
	}
}

// TestWorker_WorkModeNoCardFallback verifies work mode falls back to SendText
// when cardMsgID is empty (SendThinking failed) instead of entering companion segmenting.
func TestWorker_WorkModeNoCardFallback(t *testing.T) {
	mock := &mockSender{}
	opts := zeroDelayOpts()
	w := newTestWorker(t, workApp(t), mock, &opts)

	// cardMsgID == "" simulates SendThinking failure in work mode.
	text := "result[[SEND]]should not segment"
	w.sendResult(context.Background(), testMsg(), "", text)

	mock.mu.Lock()
	texts := mock.sendTexts
	cards := mock.updateCards
	mock.mu.Unlock()

	// Must send exactly 1 plain text (no segmenting), 0 UpdateCard.
	if len(texts) != 1 {
		t.Errorf("work mode fallback: expected 1 SendText, got %d: %v", len(texts), texts)
	}
	if len(cards) != 0 {
		t.Errorf("work mode fallback: expected 0 UpdateCard, got %d", len(cards))
	}
	if len(texts) == 1 && texts[0] != text {
		t.Errorf("work mode fallback: SendText got %q, want %q", texts[0], text)
	}
}

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

// ── isAttachmentOnly ────────────────────────────────────────────────────────

func TestIsAttachmentOnly(t *testing.T) {
	tests := []struct {
		name   string
		prompt string
		want   bool
	}{
		{"single image", "[图片: /path/a.jpg]", true},
		{"single file", "[文件: /path/b.pdf]", true},
		{"image and file", "[图片: /path/a.jpg]\n[文件: /path/b.pdf]", true},
		{"multiple images", "[图片: /path/a.jpg] [图片: /path/b.png]", true},
		{"text with image", "请分析这张图 [图片: /path/a.jpg]", false},
		{"image with text after", "[图片: /path/a.jpg] 分析一下", false},
		{"plain text", "你好", false},
		{"empty string", "", true},
		{"whitespace only", "  \n  ", true},
		{"image with surrounding whitespace", "  [图片: /path/a.jpg]  ", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isAttachmentOnly(tt.prompt)
			if got != tt.want {
				t.Errorf("isAttachmentOnly(%q) = %v, want %v", tt.prompt, got, tt.want)
			}
		})
	}
}

// ── attachmentReplyText ─────────────────────────────────────────────────────

func TestAttachmentReplyText(t *testing.T) {
	tests := []struct {
		name   string
		prompt string
		want   string
	}{
		{"image only", "[图片: /path/a.jpg]", "已收到图片，请描述你希望我做什么"},
		{"file only", "[文件: /path/b.pdf]", "已收到文件，请描述你希望我做什么"},
		{"image and file", "[图片: /path/a.jpg]\n[文件: /path/b.pdf]", "已收到图片/文件，请描述你希望我做什么"},
		{"multiple images", "[图片: /a.jpg] [图片: /b.png]", "已收到图片，请描述你希望我做什么"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := attachmentReplyText(tt.prompt)
			if got != tt.want {
				t.Errorf("attachmentReplyText(%q) = %q, want %q", tt.prompt, got, tt.want)
			}
		})
	}
}
