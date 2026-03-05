package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"

	"github.com/kid0317/cc-workspace-bot/internal/config"
)

// Dispatcher routes incoming messages to the session manager.
// Implemented by session.Manager.
type Dispatcher interface {
	Dispatch(ctx context.Context, msg *IncomingMessage) error
}

// IncomingMessage carries a normalized Feishu message ready for processing.
type IncomingMessage struct {
	AppID       string
	ChannelKey  string
	ChatType    string // p2p / group / topic_group
	ChatID      string
	ThreadID    string
	SenderID    string // open_id
	MessageID   string // Feishu message_id (for dedup)
	Prompt      string // text with local paths substituted for attachments
	ReceiveID   string // where to send the reply
	ReceiveType string // open_id / chat_id
}

// Receiver connects to Feishu WebSocket and dispatches messages.
type Receiver struct {
	appCfg     *config.AppConfig
	client     *lark.Client
	dispatcher Dispatcher
	wsClient   *larkws.Client
}

// NewReceiver creates a Receiver for one Feishu app.
func NewReceiver(appCfg *config.AppConfig, dispatcher Dispatcher) *Receiver {
	client := lark.NewClient(appCfg.FeishuAppID, appCfg.FeishuAppSecret)
	return &Receiver{
		appCfg:     appCfg,
		client:     client,
		dispatcher: dispatcher,
	}
}

// LarkClient returns the underlying Feishu API client (used to build Sender).
func (r *Receiver) LarkClient() *lark.Client {
	return r.client
}

// Start connects to Feishu WebSocket and blocks until ctx is cancelled.
func (r *Receiver) Start(ctx context.Context) error {
	eventHandler := dispatcher.NewEventDispatcher(
		r.appCfg.FeishuVerificationToken,
		r.appCfg.FeishuEncryptKey,
	).OnP2MessageReceiveV1(r.handleMessage)

	r.wsClient = larkws.NewClient(
		r.appCfg.FeishuAppID,
		r.appCfg.FeishuAppSecret,
		larkws.WithEventHandler(eventHandler),
		larkws.WithLogLevel(larkcore.LogLevelInfo),
	)

	slog.Info("feishu WS client starting", "app_id", r.appCfg.ID)
	return r.wsClient.Start(ctx)
}

// handleMessage is the callback for P2MessageReceiveV1 events.
func (r *Receiver) handleMessage(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
	msg := event.Event.Message
	sender := event.Event.Sender

	if msg == nil || msg.MessageId == nil {
		return nil
	}

	msgType := safeStr(msg.MessageType)
	chatType := safeStr(msg.ChatType)
	chatID := safeStr(msg.ChatId)
	threadID := safeStr(msg.ThreadId)
	messageID := safeStr(msg.MessageId)
	senderOpenID := ""
	if sender != nil && sender.SenderId != nil {
		senderOpenID = safeStr(sender.SenderId.OpenId)
	}

	// Check allowed chats.
	if !r.appCfg.AllowedChat(chatID) {
		slog.Debug("feishu: chat not allowed", "chat_id", chatID)
		return nil
	}

	// Build channel key.
	channelKey := buildChannelKey(chatType, chatID, threadID, r.appCfg.ID)

	// Determine reply target.
	receiveID, receiveType := replyTarget(chatType, chatID, senderOpenID)

	// Parse message content and download attachments.
	prompt, err := r.parseContent(ctx, msg, msgType, messageID, senderOpenID, receiveID)
	if err != nil {
		slog.Error("feishu: parse content", "err", err)
		return nil
	}
	if prompt == "" {
		return nil
	}

	incoming := &IncomingMessage{
		AppID:       r.appCfg.ID,
		ChannelKey:  channelKey,
		ChatType:    chatType,
		ChatID:      chatID,
		ThreadID:    threadID,
		SenderID:    senderOpenID,
		MessageID:   messageID,
		Prompt:      prompt,
		ReceiveID:   receiveID,
		ReceiveType: receiveType,
	}

	if err := r.dispatcher.Dispatch(ctx, incoming); err != nil {
		slog.Error("feishu: dispatch", "err", err)
	}
	return nil
}

// parseContent extracts text and downloads attachments from a Feishu message.
func (r *Receiver) parseContent(
	ctx context.Context,
	msg *larkim.EventMessage,
	msgType, messageID, senderOpenID, chatID string,
) (string, error) {
	content := safeStr(msg.Content)

	switch msgType {
	case "text":
		var v struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(content), &v); err != nil {
			return "", fmt.Errorf("parse text: %w", err)
		}
		return v.Text, nil

	case "image":
		var v struct {
			ImageKey string `json:"image_key"`
		}
		if err := json.Unmarshal([]byte(content), &v); err != nil {
			return "", fmt.Errorf("parse image: %w", err)
		}
		localPath, err := r.downloadImageResource(ctx, messageID, v.ImageKey)
		if err != nil {
			slog.Warn("feishu: download image failed, skipping", "err", err)
			return "[图片下载失败]", nil
		}
		return fmt.Sprintf("[图片: %s]", localPath), nil

	case "file":
		var v struct {
			FileKey  string `json:"file_key"`
			FileName string `json:"file_name"`
		}
		if err := json.Unmarshal([]byte(content), &v); err != nil {
			return "", fmt.Errorf("parse file: %w", err)
		}
		localPath, err := r.downloadFile(ctx, messageID, v.FileKey, v.FileName)
		if err != nil {
			slog.Warn("feishu: download file failed, skipping", "err", err)
			return fmt.Sprintf("[文件 %s 下载失败]", v.FileName), nil
		}
		return fmt.Sprintf("[文件: %s]", localPath), nil

	case "post":
		// Rich text - extract plain text portions only.
		return extractPostText(content), nil

	default:
		slog.Debug("feishu: unsupported message type", "type", msgType)
		return "", nil
	}
}

// downloadImageResource downloads an image resource from a message.
func (r *Receiver) downloadImageResource(ctx context.Context, messageID, imageKey string) (string, error) {
	req := larkim.NewGetMessageResourceReqBuilder().
		MessageId(messageID).
		FileKey(imageKey).
		Type("image").
		Build()

	resp, err := r.client.Im.MessageResource.Get(ctx, req)
	if err != nil {
		return "", fmt.Errorf("get image resource: %w", err)
	}
	if !resp.Success() {
		return "", fmt.Errorf("get image API error: code=%d msg=%s", resp.Code, resp.Msg)
	}

	// Save to a temp path (actual session dir unknown here; will be moved by worker).
	dir := os.TempDir()
	filename := fmt.Sprintf("feishu_img_%s_%d.jpg", imageKey, time.Now().UnixNano())
	localPath := filepath.Join(dir, filename)

	if err := saveBody(resp.File, localPath); err != nil {
		return "", err
	}
	return localPath, nil
}

// downloadFile downloads a file attachment from a message.
func (r *Receiver) downloadFile(ctx context.Context, messageID, fileKey, fileName string) (string, error) {
	req := larkim.NewGetMessageResourceReqBuilder().
		MessageId(messageID).
		FileKey(fileKey).
		Type("file").
		Build()

	resp, err := r.client.Im.MessageResource.Get(ctx, req)
	if err != nil {
		return "", fmt.Errorf("get file resource: %w", err)
	}
	if !resp.Success() {
		return "", fmt.Errorf("get file API error: code=%d msg=%s", resp.Code, resp.Msg)
	}

	dir := os.TempDir()
	localName := fmt.Sprintf("%d_%s", time.Now().UnixNano(), sanitizeFilename(fileName))
	localPath := filepath.Join(dir, localName)

	if err := saveBody(resp.File, localPath); err != nil {
		return "", err
	}
	return localPath, nil
}

// maxAttachmentBytes is the maximum size we will write from any single attachment (100 MiB).
const maxAttachmentBytes = 100 << 20

// saveBody writes an io.Reader to a local file, capping at maxAttachmentBytes.
func saveBody(body io.Reader, path string) error {
	if body == nil {
		return fmt.Errorf("empty response body")
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	// H-3: limit copy size to prevent runaway disk exhaustion.
	_, err = io.Copy(f, io.LimitReader(body, maxAttachmentBytes))
	return err
}

// DownloadURL downloads content from a URL (with context) and saves it locally.
// H-2: caller supplies context for cancellation / timeout.
func DownloadURL(ctx context.Context, url, destPath string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return saveBody(resp.Body, destPath)
}

// buildChannelKey returns the stable channel identifier.
func buildChannelKey(chatType, chatID, threadID, appID string) string {
	switch chatType {
	case "p2p":
		return fmt.Sprintf("p2p:%s:%s", chatID, appID)
	case "topic_group", "topic":
		return fmt.Sprintf("thread:%s:%s:%s", chatID, threadID, appID)
	default: // group
		return fmt.Sprintf("group:%s:%s", chatID, appID)
	}
}

// replyTarget returns the receive_id and receive_id_type for sending a reply.
func replyTarget(chatType, chatID, senderOpenID string) (string, string) {
	if chatType == "p2p" {
		return senderOpenID, "open_id"
	}
	return chatID, "chat_id"
}

// extractPostText pulls plain text from a Feishu "post" (rich text) content blob.
func extractPostText(content string) string {
	var post struct {
		Title   string                     `json:"title"`
		Content [][]map[string]interface{} `json:"content"`
	}
	if err := json.Unmarshal([]byte(content), &post); err != nil {
		return content
	}
	var sb strings.Builder
	if post.Title != "" {
		sb.WriteString(post.Title + "\n")
	}
	for _, row := range post.Content {
		for _, elem := range row {
			if tag, _ := elem["tag"].(string); tag == "text" {
				if text, _ := elem["text"].(string); text != "" {
					sb.WriteString(text)
				}
			}
		}
		sb.WriteString("\n")
	}
	return strings.TrimSpace(sb.String())
}

func safeStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func sanitizeFilename(name string) string {
	name = filepath.Base(name)
	// Replace any path separators that might have slipped through.
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\\", "_")
	return name
}
