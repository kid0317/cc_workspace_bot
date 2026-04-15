package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/kid0317/cc-workspace-bot/internal/claude"
	"github.com/kid0317/cc-workspace-bot/internal/config"
	"github.com/kid0317/cc-workspace-bot/internal/feishu"
	"github.com/kid0317/cc-workspace-bot/internal/model"
)

const (
	statusActive   = "active"
	statusArchived = "archived"
)

// senderIface abstracts the Feishu sender for testing.
type senderIface interface {
	SendText(ctx context.Context, receiveID, receiveType, text string) (string, error)
	SendThinking(ctx context.Context, receiveID, receiveType string) (string, error)
	UpdateCard(ctx context.Context, messageID, text string) error
	SendCard(ctx context.Context, receiveID, receiveIDType, text string) (string, error)
}

// claudeResult mirrors claude.ExecuteResult for internal use; the concrete type
// is used in production but this alias allows tests to construct values directly.
type claudeResult = claude.ExecuteResult

// Worker processes messages for a single channel serially.
// It is lazily started on first message and exits after idleTimeout.
type Worker struct {
	channelKey  string
	appCfg      *config.AppConfig
	db          *gorm.DB
	executor    *claude.Executor
	sender      *feishu.Sender  // kept for backward-compat with newWorker callers
	senderIface senderIface     // used by all send helpers; set from sender in newWorker
	idleTimeout time.Duration
	segmentOpts SegmentOptions  // must be initialised via DefaultSegmentOptions()

	queue  chan *feishu.IncomingMessage
	stopCh chan struct{}

	// pendingAttachmentPrompts caches prompts from attachment-only messages.
	// When the next text message arrives, these are prepended to form a
	// combined prompt before sending to Claude.
	pendingAttachmentPrompts []string
}

func newWorker(
	channelKey string,
	appCfg *config.AppConfig,
	db *gorm.DB,
	executor *claude.Executor,
	sender *feishu.Sender,
	idleTimeout time.Duration,
) *Worker {
	return &Worker{
		channelKey:  channelKey,
		appCfg:      appCfg,
		db:          db,
		executor:    executor,
		sender:      sender,
		senderIface: sender,
		idleTimeout: idleTimeout,
		segmentOpts: DefaultSegmentOptions(),
		queue:       make(chan *feishu.IncomingMessage, 64),
		stopCh:      make(chan struct{}),
	}
}

// isRateLimitError reports whether err is a Feishu rate-limit error (code 99991400).
func isRateLimitError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "99991400")
}

// run is the worker's main goroutine. It blocks until idle or ctx done.
func (w *Worker) run(ctx context.Context, onExit func()) {
	defer onExit()

	timer := time.NewTimer(w.idleTimeout)
	defer timer.Stop()

	slog.Info("session worker started", "channel", w.channelKey)

	for {
		select {
		case msg := <-w.queue:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(w.idleTimeout)
			w.process(ctx, msg)

		case <-timer.C:
			slog.Info("session worker idle timeout, exiting (session kept active)", "channel", w.channelKey)
			return

		case <-w.stopCh:
			return

		case <-ctx.Done():
			return
		}
	}
}

// isAttachmentOnly reports whether the prompt contains ONLY attachment
// references ([图片: ...] / [文件: ...]) with no additional text.
func isAttachmentOnly(prompt string) bool {
	cleaned := prompt
	for _, prefix := range []string{"[图片: ", "[文件: "} {
		for {
			idx := strings.Index(cleaned, prefix)
			if idx < 0 {
				break
			}
			end := strings.IndexByte(cleaned[idx:], ']')
			if end < 0 {
				break
			}
			cleaned = cleaned[:idx] + cleaned[idx+end+1:]
		}
	}
	return strings.TrimSpace(cleaned) == ""
}

// attachmentReplyText returns the acknowledgement text for an attachment-only message.
func attachmentReplyText(prompt string) string {
	hasImage := strings.Contains(prompt, "[图片: ")
	hasFile := strings.Contains(prompt, "[文件: ")
	switch {
	case hasImage && hasFile:
		return "已收到图片/文件，请描述你希望我做什么"
	case hasImage:
		return "已收到图片，请描述你希望我做什么"
	default:
		return "已收到文件，请描述你希望我做什么"
	}
}

// process handles a single incoming message.
// H-8: decomposed into focused helpers to keep each step under 50 lines.
func (w *Worker) process(ctx context.Context, msg *feishu.IncomingMessage) {
	if strings.TrimSpace(msg.Prompt) == "/new" {
		w.pendingAttachmentPrompts = nil
		w.handleNew(ctx, msg)
		return
	}

	sess, err := w.getOrCreateSession(msg.SenderID)
	if err != nil {
		slog.Error("get/create session", "err", err, "channel", w.channelKey)
		return
	}

	msg.Prompt = w.moveAttachments(msg.Prompt, sess.ID)
	w.recordMessage(sess.ID, msg.SenderID, "user", msg.Prompt, msg.MessageID)

	// Cache attachment-only messages and reply with a prompt for description.
	if isAttachmentOnly(msg.Prompt) {
		w.pendingAttachmentPrompts = append(w.pendingAttachmentPrompts, msg.Prompt)
		reply := attachmentReplyText(msg.Prompt)
		if _, err := w.senderIface.SendText(ctx, msg.ReceiveID, msg.ReceiveType, reply); err != nil {
			slog.Error("send attachment ack", "err", err)
		}
		return
	}

	// Merge any pending attachment references into the current prompt.
	if len(w.pendingAttachmentPrompts) > 0 {
		combined := strings.Join(w.pendingAttachmentPrompts, "\n") + "\n" + msg.Prompt
		msg.Prompt = combined
		w.pendingAttachmentPrompts = nil
	}

	var cardMsgID string
	if !w.appCfg.IsCompanion() {
		cardMsgID = w.sendThinkingCard(ctx, msg)
	}
	result, err := w.runClaude(ctx, sess, msg)
	if err != nil {
		w.replyError(ctx, msg, cardMsgID, err)
		return
	}

	w.persistResult(sess, result)

	// Guard against claude returning success but empty text (e.g. context
	// overflow on --resume). Notify the user instead of leaving the thinking
	// card hanging indefinitely.
	if result.Text == "" {
		w.replyError(ctx, msg, cardMsgID,
			fmt.Errorf("AI 返回为空，可能是会话上下文过长。请发送 /new 开启新会话后重试"))
		return
	}

	w.sendResult(ctx, msg, cardMsgID, result.Text)
}

// runClaude invokes the Claude executor and returns the result.
func (w *Worker) runClaude(ctx context.Context, sess *model.Session, msg *feishu.IncomingMessage) (*claude.ExecuteResult, error) {
	return w.executor.Execute(ctx, &claude.ExecuteRequest{
		Prompt:          msg.Prompt,
		SessionID:       sess.ID,
		ClaudeSessionID: sess.ClaudeSessionID,
		AppConfig:       w.appCfg,
		WorkspaceDir:    w.appCfg.WorkspaceDir,
		ChannelKey:      w.channelKey,
		SenderID:        msg.SenderID,
	})
}

// sendThinkingCard posts the initial "thinking..." card and returns its message ID.
func (w *Worker) sendThinkingCard(ctx context.Context, msg *feishu.IncomingMessage) string {
	cardMsgID, err := w.senderIface.SendThinking(ctx, msg.ReceiveID, msg.ReceiveType)
	if err != nil {
		slog.Error("send thinking card", "err", err)
	}
	return cardMsgID
}

// persistResult saves the claude_session_id and the assistant message to DB.
// [[SEND]] markers are stripped before storing to keep DB and resume context clean.
func (w *Worker) persistResult(sess *model.Session, result *claude.ExecuteResult) {
	if result.ClaudeSessionID != "" && sess.ClaudeSessionID == "" {
		if err := w.db.Model(sess).Update("claude_session_id", result.ClaudeSessionID).Error; err != nil {
			slog.Error("update claude_session_id", "err", err)
		}
	}
	storedText := strings.ReplaceAll(result.Text, "[[SEND]]", "\n")
	w.recordMessage(sess.ID, "", "assistant", storedText, "")
}

// sendResult updates the card (work mode) or sends segmented plain text (companion mode).
// Routing is based on IsCompanion(), not cardMsgID, to handle the case where SendThinking
// fails (cardMsgID=="") in work mode — we must not fall through to companion segmenting.
func (w *Worker) sendResult(ctx context.Context, msg *feishu.IncomingMessage, cardMsgID, text string) {
	if text == "" {
		return // claude chose not to respond (expected in group chats)
	}
	if w.appCfg.IsCompanion() {
		// Companion mode: split into segments and send each with a typing delay.
		w.sendCompanionSegments(ctx, msg, text)
		return
	}
	if cardMsgID != "" {
		// Work mode: patch the thinking card with the final result.
		if err := w.senderIface.UpdateCard(ctx, cardMsgID, text); err != nil {
			slog.Error("update card", "err", err)
		}
		return
	}
	// Work mode fallback: SendThinking failed earlier, send as plain text.
	if _, err := w.senderIface.SendText(ctx, msg.ReceiveID, msg.ReceiveType, text); err != nil {
		slog.Error("send text fallback", "err", err)
	}
}

// sendCompanionSegments splits text into segments and sends each one with a
// simulated typing delay. Errors on individual segments are logged and skipped;
// ctx cancellation stops the loop immediately.
func (w *Worker) sendCompanionSegments(ctx context.Context, msg *feishu.IncomingMessage, text string) {
	segments := SplitSegments(text, w.segmentOpts)
	if len(segments) == 0 {
		return
	}

	var prev string
	for i, seg := range segments {
		delay := TypingDelay(prev, seg, i == 0, w.segmentOpts, nil)
		select {
		case <-ctx.Done():
			slog.Warn("companion segment send cancelled",
				"channel", w.channelKey, "sent", i, "total", len(segments))
			return
		case <-time.After(delay):
		}

		sendCtx, sendCancel := context.WithTimeout(ctx, 5*time.Second)
		_, err := w.senderIface.SendText(sendCtx, msg.ReceiveID, msg.ReceiveType, seg)
		sendCancel()
		if err != nil {
			if isRateLimitError(err) {
				// Back off before retry, but respect context cancellation.
				select {
				case <-ctx.Done():
					slog.Warn("companion segment send cancelled during rate-limit backoff",
						"channel", w.channelKey, "sent", i, "total", len(segments))
					return
				case <-time.After(500 * time.Millisecond):
				}
				sendCtx2, sendCancel2 := context.WithTimeout(ctx, 5*time.Second)
				_, err = w.senderIface.SendText(sendCtx2, msg.ReceiveID, msg.ReceiveType, seg)
				sendCancel2()
			}
			if err != nil {
				slog.Error("send companion segment",
					"err", err, "index", i, "total", len(segments))
				// Log and continue; do not stop sending subsequent segments.
			}
		}
		prev = seg
	}
}

// replyError surfaces execution errors to the user.
func (w *Worker) replyError(ctx context.Context, msg *feishu.IncomingMessage, cardMsgID string, err error) {
	slog.Error("claude execute", "err", err)
	reply := fmt.Sprintf("❌ 执行出错：%s", err.Error())
	if cardMsgID != "" {
		_ = w.senderIface.UpdateCard(ctx, cardMsgID, reply)
		return
	}
	_, _ = w.senderIface.SendText(ctx, msg.ReceiveID, msg.ReceiveType, reply)
}

// recordMessage writes a message record to DB. Errors are logged, not propagated.
func (w *Worker) recordMessage(sessionID, senderID, role, content, feishuMsgID string) {
	m := &model.Message{
		ID:          uuid.New().String(),
		SessionID:   sessionID,
		SenderID:    senderID,
		Role:        role,
		Content:     content,
		FeishuMsgID: feishuMsgID,
		CreatedAt:   time.Now(),
	}
	if err := w.db.Create(m).Error; err != nil {
		slog.Error("create message", "role", role, "err", err)
	}
}

// handleNew archives the current session and creates a new one.
func (w *Worker) handleNew(ctx context.Context, msg *feishu.IncomingMessage) {
	if err := w.db.Model(&model.Session{}).
		Where("channel_key = ? AND status = ?", w.channelKey, statusActive).
		Updates(map[string]interface{}{
			"status":     statusArchived,
			"updated_at": time.Now(),
		}).Error; err != nil {
		slog.Error("archive session on /new", "err", err)
	}

	newID := uuid.New().String()
	sessionDir := filepath.Join(w.appCfg.WorkspaceDir, "sessions", newID)
	if err := os.MkdirAll(filepath.Join(sessionDir, "attachments"), 0o755); err != nil {
		slog.Error("create new session dir", "err", err)
	}

	newSess := &model.Session{
		ID:         newID,
		ChannelKey: w.channelKey,
		Status:     statusActive,
		CreatedBy:  msg.SenderID,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	if err := w.db.Create(newSess).Error; err != nil {
		slog.Error("create new session", "err", err)
	}

	_, _ = w.senderIface.SendText(ctx, msg.ReceiveID, msg.ReceiveType, "✅ 已开启新会话")
}

// getOrCreateSession returns the active session for this channel, creating one if needed.
func (w *Worker) getOrCreateSession(senderID string) (*model.Session, error) {
	var sess model.Session
	result := w.db.Where("channel_key = ? AND status = ?", w.channelKey, statusActive).
		Order("created_at DESC").
		First(&sess)

	if result.Error == nil {
		return &sess, nil
	}
	// C-3: use errors.Is for GORM sentinel errors.
	if !errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, result.Error
	}

	newID := uuid.New().String()
	sessionDir := filepath.Join(w.appCfg.WorkspaceDir, "sessions", newID)
	if err := os.MkdirAll(filepath.Join(sessionDir, "attachments"), 0o755); err != nil {
		return nil, fmt.Errorf("create session dir: %w", err)
	}

	sess = model.Session{
		ID:         newID,
		ChannelKey: w.channelKey,
		Status:     statusActive,
		CreatedBy:  senderID,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	if err := w.db.Create(&sess).Error; err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	return &sess, nil
}

// archiveCurrentSession marks the active session as archived.
func (w *Worker) archiveCurrentSession() {
	if err := w.db.Model(&model.Session{}).
		Where("channel_key = ? AND status = ?", w.channelKey, statusActive).
		Updates(map[string]interface{}{
			"status":     statusArchived,
			"updated_at": time.Now(),
		}).Error; err != nil {
		slog.Error("archive session on idle", "err", err)
	}
}

// moveAttachments moves temporary attachment files into the session attachments directory
// and replaces their paths in the prompt string accordingly.
// M-9: correctly handles multiple attachments per type using offset-based iteration.
func (w *Worker) moveAttachments(prompt, sessionID string) string {
	attachDir := filepath.Join(w.appCfg.WorkspaceDir, "sessions", sessionID, "attachments")
	if err := os.MkdirAll(attachDir, 0o755); err != nil {
		slog.Warn("moveAttachments: mkdir failed", "err", err)
		return prompt
	}

	result := prompt
	for _, prefix := range []string{"[图片: ", "[文件: "} {
		result = replacePaths(result, prefix, attachDir)
	}
	return result
}

// replacePaths rewrites all occurrences of [prefix <path>] in s, moving each
// <path> into attachDir. Already-moved paths (inside attachDir) are left as-is.
func replacePaths(s, prefix, attachDir string) string {
	var out strings.Builder
	remaining := s

	for {
		idx := strings.Index(remaining, prefix)
		if idx < 0 {
			out.WriteString(remaining)
			break
		}

		// Write everything up to and including the prefix.
		out.WriteString(remaining[:idx+len(prefix)])
		remaining = remaining[idx+len(prefix):]

		// Find the closing bracket.
		end := strings.IndexByte(remaining, ']')
		if end < 0 {
			// Malformed reference — emit the rest verbatim.
			out.WriteString(remaining)
			break
		}

		oldPath := remaining[:end]
		remaining = remaining[end:] // retains the ']' for the next iteration

		if strings.HasPrefix(oldPath, attachDir) {
			// Already in the right place.
			out.WriteString(oldPath)
			continue
		}

		newPath := filepath.Join(attachDir,
			fmt.Sprintf("%d_%s", time.Now().UnixNano(), filepath.Base(oldPath)))
		if err := os.Rename(oldPath, newPath); err != nil {
			slog.Warn("move attachment", "src", oldPath, "err", err)
			out.WriteString(oldPath) // keep original path on failure
		} else {
			out.WriteString(newPath)
		}
	}
	return out.String()
}
