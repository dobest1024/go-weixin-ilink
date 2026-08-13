package ilink

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
)

func generateClientID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return fmt.Sprintf("ilink-sdk-%x", b)
}

// sendParams carries everything the send helpers need. Bundling it keeps the
// call sites readable now that run_id and the outbound hooks travel along.
type sendParams struct {
	c              *client
	channelVersion string
	botAgent       string
	hooks          *Hooks
	logger         *slog.Logger

	// runID groups every message produced by one agent run — tool-call
	// progress, intermediate text, and the final reply — into one bubble.
	runID string
}

func newBotMsg(toUserID, contextToken, runID string, items []MessageItem) *Message {
	return &Message{
		FromUserID:   "",
		ToUserID:     toUserID,
		ClientID:     generateClientID(),
		MessageType:  MessageTypeBot,
		MessageState: MessageStateFinish,
		ContextToken: contextToken,
		RunID:        runID,
		ItemList:     items,
	}
}

// send delivers one message, running the outbound hooks around the API call.
func (p sendParams) send(ctx context.Context, msg *Message) error {
	if p.hooks != nil {
		if err := p.hooks.callOnBeforeSend(msg); err != nil {
			if errors.Is(err, ErrSendCanceled) && p.logger != nil {
				p.logger.Debug("outbound message canceled by hook", "to_user_id", msg.ToUserID)
			}
			return err
		}
	}

	err := p.deliver(ctx, msg)

	if p.hooks != nil {
		p.hooks.callOnAfterSend(msg, err)
	}
	return err
}

func (p sendParams) deliver(ctx context.Context, msg *Message) error {
	req := &SendMessageRequest{
		Msg:      msg,
		BaseInfo: &BaseInfo{ChannelVersion: p.channelVersion, BotAgent: p.botAgent},
	}
	var resp SendMessageResponse
	if err := p.c.post(ctx, "/ilink/bot/sendmessage", req, &resp); err != nil {
		return err
	}
	// Read ret and errcode together: the server sets either one, and taking
	// only errcode turns a stale-token failure into code 0.
	return apiError(resp.Ret, resp.ErrCode, resp.ErrMsg)
}

// items sends a message carrying the given content items.
func (p sendParams) items(ctx context.Context, toUserID, contextToken string, items []MessageItem) error {
	return p.send(ctx, newBotMsg(toUserID, contextToken, p.runID, items))
}

// text sends a plain-text message.
func (p sendParams) text(ctx context.Context, toUserID, contextToken, text string) error {
	return p.items(ctx, toUserID, contextToken, []MessageItem{
		{Type: ItemTypeText, TextItem: &TextItem{Text: text}},
	})
}

func (p sendParams) image(ctx context.Context, toUserID, contextToken string, img *ImageItem) error {
	return p.items(ctx, toUserID, contextToken, []MessageItem{{Type: ItemTypeImage, ImageItem: img}})
}

func (p sendParams) voice(ctx context.Context, toUserID, contextToken string, voice *VoiceItem) error {
	return p.items(ctx, toUserID, contextToken, []MessageItem{{Type: ItemTypeVoice, VoiceItem: voice}})
}

func (p sendParams) file(ctx context.Context, toUserID, contextToken string, file *FileItem) error {
	return p.items(ctx, toUserID, contextToken, []MessageItem{{Type: ItemTypeFile, FileItem: file}})
}

func (p sendParams) video(ctx context.Context, toUserID, contextToken string, video *VideoItem) error {
	return p.items(ctx, toUserID, contextToken, []MessageItem{{Type: ItemTypeVideo, VideoItem: video}})
}

// withRunID returns a copy bound to a specific agent run.
func (p sendParams) withRunID(runID string) sendParams {
	p.runID = runID
	return p
}

// BuildToolCallStartItem builds the item that tells the user a tool started.
func BuildToolCallStartItem(toolName, toolCallID string, createTimeMs int64) MessageItem {
	return MessageItem{
		Type:         ItemTypeToolCallStart,
		CreateTimeMs: createTimeMs,
		IsCompleted:  false,
		ToolCallStartItem: &ToolCallStartItem{
			ToolName:   toolName,
			ToolCallID: toolCallID,
		},
	}
}

// BuildToolCallResultItem builds the item that reports a tool's outcome.
// status is normalized to one of the ToolCallStatus* constants.
func BuildToolCallResultItem(toolName, toolCallID, status string, createTimeMs int64) MessageItem {
	return MessageItem{
		Type:         ItemTypeToolCallResult,
		CreateTimeMs: createTimeMs,
		IsCompleted:  true,
		ToolCallResultItem: &ToolCallResultItem{
			ToolName:   toolName,
			ToolCallID: toolCallID,
			Status:     NormalizeToolCallStatus(status),
		},
	}
}
