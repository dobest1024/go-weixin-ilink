package ilink

import (
	"context"
	"crypto/rand"
	"fmt"
)

func generateClientID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return fmt.Sprintf("ilink-sdk-%x", b)
}

func newBotMsg(toUserID, contextToken string, items []MessageItem) *Message {
	return &Message{
		FromUserID:   "",
		ToUserID:     toUserID,
		ClientID:     generateClientID(),
		MessageType:  MessageTypeBot,
		MessageState: MessageStateFinish,
		ContextToken: contextToken,
		ItemList:     items,
	}
}

func sendRaw(ctx context.Context, c *client, channelVersion string, msg *Message) error {
	req := &SendMessageRequest{
		Msg:      msg,
		BaseInfo: &BaseInfo{ChannelVersion: channelVersion},
	}
	var resp SendMessageResponse
	if err := c.post(ctx, "/ilink/bot/sendmessage", req, &resp); err != nil {
		return err
	}
	if resp.Ret != 0 {
		return &APIError{Code: resp.ErrCode, Message: resp.ErrMsg}
	}
	return nil
}

// SendText sends a plain-text message.
func sendText(ctx context.Context, c *client, channelVersion, toUserID, text, contextToken string) error {
	msg := newBotMsg(toUserID, contextToken, []MessageItem{
		{Type: ItemTypeText, TextItem: &TextItem{Text: text}},
	})
	return sendRaw(ctx, c, channelVersion, msg)
}

// sendImage sends an image message.
func sendImage(ctx context.Context, c *client, channelVersion, toUserID, contextToken string, img *ImageItem) error {
	msg := newBotMsg(toUserID, contextToken, []MessageItem{
		{Type: ItemTypeImage, ImageItem: img},
	})
	return sendRaw(ctx, c, channelVersion, msg)
}

// sendVoice sends a voice message.
func sendVoice(ctx context.Context, c *client, channelVersion, toUserID, contextToken string, voice *VoiceItem) error {
	msg := newBotMsg(toUserID, contextToken, []MessageItem{
		{Type: ItemTypeVoice, VoiceItem: voice},
	})
	return sendRaw(ctx, c, channelVersion, msg)
}

// sendFile sends a file message.
func sendFile(ctx context.Context, c *client, channelVersion, toUserID, contextToken string, file *FileItem) error {
	msg := newBotMsg(toUserID, contextToken, []MessageItem{
		{Type: ItemTypeFile, FileItem: file},
	})
	return sendRaw(ctx, c, channelVersion, msg)
}

// sendVideo sends a video message.
func sendVideo(ctx context.Context, c *client, channelVersion, toUserID, contextToken string, video *VideoItem) error {
	msg := newBotMsg(toUserID, contextToken, []MessageItem{
		{Type: ItemTypeVideo, VideoItem: video},
	})
	return sendRaw(ctx, c, channelVersion, msg)
}
