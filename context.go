package ilink

import (
	"context"
	"sync"
)

// HandlerFunc handles a dispatched message.
type HandlerFunc func(*Context)

// Context wraps an incoming message and provides reply helpers.
// It also implements the middleware chain via Next/Abort.
type Context struct {
	// Message is the received WeChat message.
	Message *Message

	// Ctx is the request context (carries deadline/cancellation).
	Ctx context.Context

	// bot gives access to send/media capabilities.
	bot *Bot

	// middleware chain.
	// aborted is a flag rather than a sentinel index: the previous int8
	// sentinel (127) overflowed to -128 on Next's trailing increment and
	// indexed the handler slice out of range.
	handlers []HandlerFunc
	index    int
	aborted  bool

	// per-request KV store
	mu   sync.RWMutex
	keys map[string]any
}

func newContext(ctx context.Context, msg *Message, bot *Bot, handlers []HandlerFunc) *Context {
	return &Context{
		Message:  msg,
		Ctx:      ctx,
		bot:      bot,
		handlers: handlers,
		index:    -1,
	}
}

// Next executes the next handler in the chain.
func (c *Context) Next() {
	c.index++
	for c.index < len(c.handlers) {
		c.handlers[c.index](c)
		if c.aborted {
			return
		}
		c.index++
	}
}

// Abort stops the handler chain after the current handler returns.
func (c *Context) Abort() {
	c.aborted = true
}

// IsAborted reports whether the chain was aborted.
func (c *Context) IsAborted() bool {
	return c.aborted
}

// Set stores a value in the per-request context.
func (c *Context) Set(key string, val any) {
	c.mu.Lock()
	if c.keys == nil {
		c.keys = make(map[string]any)
	}
	c.keys[key] = val
	c.mu.Unlock()
}

// Get retrieves a value from the per-request context.
func (c *Context) Get(key string) (any, bool) {
	c.mu.RLock()
	v, ok := c.keys[key]
	c.mu.RUnlock()
	return v, ok
}

// MustGet retrieves a value or panics if not found.
func (c *Context) MustGet(key string) any {
	v, ok := c.Get(key)
	if !ok {
		panic("ilink: key not found: " + key)
	}
	return v
}

// --- Convenience accessors ---

// Text returns the text content of the message (empty string if not a text message).
func (c *Context) Text() string { return c.Message.Text() }

// Body returns the message rendered the way a chat client shows it: a quoted
// message becomes a "[引用: …]" prefix, and a voice message falls back to its
// speech-to-text transcript. This is what you should feed to an AI pipeline —
// Text alone drops voice input on the floor.
func (c *Context) Body() string { return c.Message.BodyText() }

// RunID returns the run ID to tag outbound messages with. It defaults to the
// inbound message's run_id and can be overridden with SetRunID.
func (c *Context) RunID() string {
	if v, ok := c.Get(runIDKey); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return c.Message.RunID
}

// SetRunID overrides the run ID used for every subsequent reply from this
// context, grouping them into one bubble on the client.
func (c *Context) SetRunID(runID string) { c.Set(runIDKey, runID) }

// runIDKey is the per-request key holding an overridden run ID.
const runIDKey = "ilink.run_id"

// QuotedText returns the text content of the quoted (ref_msg) item, if any.
// Returns empty string when there is no quoted message or the quote contains no text.
func (c *Context) QuotedText() string {
	for _, item := range c.Message.ItemList {
		if item.Type == ItemTypeText && item.TextItem != nil && item.RefMsg != nil {
			if item.RefMsg.MessageItem != nil && item.RefMsg.MessageItem.TextItem != nil {
				return item.RefMsg.MessageItem.TextItem.Text
			}
			return item.RefMsg.Title
		}
	}
	return ""
}

// HasQuote reports whether the message includes a quoted/replied-to item.
func (c *Context) HasQuote() bool {
	for _, item := range c.Message.ItemList {
		if item.RefMsg != nil {
			return true
		}
	}
	return false
}

// UserID returns the sender's user ID.
func (c *Context) UserID() string { return c.Message.FromUserID }

// IsGroup reports whether this is a group message.
func (c *Context) IsGroup() bool { return c.Message.IsGroup() }

// IsPrivate reports whether this is a private (1-on-1) message.
func (c *Context) IsPrivate() bool { return c.Message.IsPrivate() }

// --- Reply helpers ---

// sender returns the send parameters for this message, carrying the run ID so
// every reply is grouped with the run that produced it.
func (c *Context) sender() sendParams {
	return c.bot.sender().withRunID(c.RunID())
}

// ReplyText sends a text reply to the message sender.
func (c *Context) ReplyText(text string) error {
	return c.sender().text(c.Ctx, c.Message.FromUserID, c.Message.ContextToken, text)
}

// ReplyItems sends a reply with custom message items.
func (c *Context) ReplyItems(items []MessageItem) error {
	return c.sender().items(c.Ctx, c.Message.FromUserID, c.Message.ContextToken, items)
}

// Typing sends a "typing" indicator to the sender.
func (c *Context) Typing() error {
	return c.bot.typing.StartTyping(c.Ctx, c.Message.FromUserID, c.Message.ContextToken)
}

// StopTyping cancels the typing indicator.
func (c *Context) StopTyping() error {
	return c.bot.typing.StopTyping(c.Ctx, c.Message.FromUserID, c.Message.ContextToken)
}

// --- Media helpers (delegate to Bot) ---

// Upload encrypts and uploads raw bytes to WeChat CDN.
// fileType: "image" | "video" | "voice" | "file"
func (c *Context) Upload(data []byte, fileType string) (*UploadResult, error) {
	return c.bot.Upload(c.Ctx, data, c.Message.FromUserID, fileType)
}

// DownloadImage downloads and decrypts an inbound image message.
func (c *Context) DownloadImage(img *ImageItem) ([]byte, error) {
	return c.bot.DownloadImage(c.Ctx, img)
}

// DownloadMedia downloads and decrypts a CDN media item (voice/file/video).
func (c *Context) DownloadMedia(media *CDNMedia) ([]byte, error) {
	return c.bot.DownloadMedia(c.Ctx, media)
}
