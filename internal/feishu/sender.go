package feishu

import (
	"context"
	"encoding/json"
	"fmt"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// Sender sends messages and updates interactive cards via Feishu API.
type Sender struct {
	client *lark.Client
}

// NewSender creates a Sender with the given Feishu API client.
func NewSender(client *lark.Client) *Sender {
	return &Sender{client: client}
}

// cardContent is the JSON body for an interactive card message.
type cardContent struct {
	Config   cardConfig    `json:"config"`
	Elements []interface{} `json:"elements"`
}

type cardConfig struct {
	WideScreenMode bool `json:"wide_screen_mode"`
}

type cardTextElement struct {
	Tag     string       `json:"tag"`
	Content string       `json:"content"`
	TextTag textTagInner `json:"text"`
}

type textTagInner struct {
	Content string `json:"content"`
	Tag     string `json:"tag"`
}

// buildCard builds a minimal Feishu interactive card JSON string.
// Returns an error so callers can decide how to handle serialization failures.
func buildCard(text string) (string, error) {
	card := map[string]interface{}{
		"config": map[string]interface{}{
			"wide_screen_mode": true,
		},
		"elements": []interface{}{
			map[string]interface{}{
				"tag": "div",
				"text": map[string]interface{}{
					"content": text,
					"tag":     "lark_md",
				},
			},
		},
	}
	b, err := json.Marshal(card)
	if err != nil {
		return "", fmt.Errorf("marshal card: %w", err)
	}
	return string(b), nil
}

// SendCard sends an interactive card message and returns the card message ID.
func (s *Sender) SendCard(ctx context.Context, receiveID, receiveIDType, text string) (string, error) {
	cardJSON, err := buildCard(text)
	if err != nil {
		return "", err
	}

	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(receiveIDType).
		Body(larkim.NewCreateMessageReqBodyBuilder().
			MsgType(larkim.MsgTypeInteractive).
			ReceiveId(receiveID).
			Content(cardJSON).
			Build()).
		Build()

	resp, err := s.client.Im.Message.Create(ctx, req)
	if err != nil {
		return "", fmt.Errorf("send card: %w", err)
	}
	if !resp.Success() {
		return "", fmt.Errorf("send card API error: code=%d msg=%s", resp.Code, resp.Msg)
	}

	if resp.Data != nil && resp.Data.MessageId != nil {
		return *resp.Data.MessageId, nil
	}
	return "", nil
}

// SendThinking sends an initial "thinking..." interactive card and returns the card message ID.
func (s *Sender) SendThinking(ctx context.Context, receiveID string, receiveIDType string) (string, error) {
	return s.SendCard(ctx, receiveID, receiveIDType, "⏳ 思考中...")
}

// UpdateCard patches an existing interactive card with new text.
func (s *Sender) UpdateCard(ctx context.Context, messageID string, text string) error {
	cardJSON, err := buildCard(text)
	if err != nil {
		return err
	}

	req := larkim.NewPatchMessageReqBuilder().
		MessageId(messageID).
		Body(larkim.NewPatchMessageReqBodyBuilder().
			Content(cardJSON).
			Build()).
		Build()

	resp, err := s.client.Im.Message.Patch(ctx, req)
	if err != nil {
		return fmt.Errorf("patch card: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("patch card API error: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

// SendText sends a plain text message to a chat.
func (s *Sender) SendText(ctx context.Context, receiveID string, receiveIDType string, text string) (string, error) {
	content, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return "", fmt.Errorf("marshal text content: %w", err)
	}

	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(receiveIDType).
		Body(larkim.NewCreateMessageReqBodyBuilder().
			MsgType(larkim.MsgTypeText).
			ReceiveId(receiveID).
			Content(string(content)).
			Build()).
		Build()

	resp, err := s.client.Im.Message.Create(ctx, req)
	if err != nil {
		return "", fmt.Errorf("send text: %w", err)
	}
	if !resp.Success() {
		return "", fmt.Errorf("send text API error: code=%d msg=%s", resp.Code, resp.Msg)
	}

	if resp.Data != nil && resp.Data.MessageId != nil {
		return *resp.Data.MessageId, nil
	}
	return "", nil
}
