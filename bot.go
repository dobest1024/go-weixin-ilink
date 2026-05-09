// Package ilink provides a Go SDK for the WeChat iLink bot protocol.
//
// Quick start:
//
//	bot := ilink.NewBot()
//	bot.OnText(func(ctx *ilink.Context) {
//	    ctx.ReplyText("Hello, " + ctx.UserID())
//	})
//	if err := bot.Login(context.Background(), ilink.TerminalQR); err != nil {
//	    log.Fatal(err)
//	}
//	log.Fatal(bot.Run(context.Background()))
package ilink

import (
	"context"
	"fmt"
	"log/slog"
)

// Bot is the main entry point for building a WeChat iLink bot.
//
// Register message handlers with On*, then call Login followed by Run.
// All registered handlers run inside a handler chain; use ctx.Next() and
// ctx.Abort() to control flow between handlers in the same chain.
type Bot struct {
	cfg        *config
	c          *client
	authSvc    *auth
	polling    *poller
	typing     *typingManager
	media      *mediaManager
	dispatcher *dispatcher
	ctxStore   ContextTokenStore
}

// NewBot creates a Bot with the given options.
// Sensible defaults are used for any options not provided.
func NewBot(opts ...Option) *Bot {
	cfg := defaultConfig()
	for _, o := range opts {
		o(cfg)
	}

	c := newClient(cfg.baseURL, cfg.httpClient, cfg.channelVersion, cfg.appID)

	var tokenStore TokenStore
	if cfg.tokenStore != nil {
		tokenStore = cfg.tokenStore
	} else {
		tokenStore = NewFileTokenStore(cfg.tokenFile)
	}

	var ctxStore ContextTokenStore
	if cfg.contextTokenStore != nil {
		ctxStore = cfg.contextTokenStore
	} else {
		var err error
		ctxStore, err = NewFileContextTokenStore(cfg.contextTokenDir)
		if err != nil {
			cfg.logger.Warn("failed to create file context token store, using memory store", "error", err)
			ctxStore = NewMemContextTokenStore()
		}
	}

	var syncBuf SyncBufStore
	if cfg.syncBufStore != nil {
		syncBuf = cfg.syncBufStore
	} else if cfg.syncBufFile != "" {
		syncBuf = NewFileSyncBufStore(cfg.syncBufFile)
	}
	cfg.syncBufStore = syncBuf

	return &Bot{
		cfg:        cfg,
		c:          c,
		authSvc:    newAuth(c, tokenStore, cfg.logger),
		typing:     newTypingManager(c, cfg.logger, cfg.botAgent),
		media:      newMediaManager(c, cfg.httpClient, cfg.cdnBaseURL, cfg.channelVersion, cfg.botAgent, cfg.logger),
		dispatcher: newDispatcher(),
		ctxStore:   ctxStore,
	}
}

// ─── Lifecycle ───────────────────────────────────────────────────────────────

// Login authenticates the bot.
// If valid credentials are stored, they are reused silently.
// Otherwise the QR callback is invoked with base64-encoded QR image content.
// Pass ilink.TerminalQR to render the code in the terminal automatically.
func (b *Bot) Login(ctx context.Context, onQR QRCallback) error {
	if err := b.authSvc.Login(ctx, onQR); err != nil {
		return err
	}
	b.cfg.hooks.callOnLogin()
	return nil
}

// Resume restores the session from stored credentials without validation or QR login.
// Returns nil if credentials were loaded, or ErrNoStoredCredentials if no token exists.
func (b *Bot) Resume() error {
	return b.authSvc.Resume()
}

// Run starts the message-polling loop. Blocks until ctx is cancelled.
// Session expiry (-14) is handled automatically: the poller pauses 1 hour then retries.
// Must call Login before Run.
func (b *Bot) Run(ctx context.Context) error {
	if b.c.getToken() == "" {
		return ErrNotLoggedIn
	}

	if err := notifyStart(ctx, b.c, b.baseInfo()); err != nil {
		b.cfg.logger.Warn("notifyStart failed (ignored)", "error", err)
	}

	b.polling = newPoller(b.c, b.handleMessage, b.cfg.channelVersion, b.cfg.logger,
		b.cfg.syncBufStore, b.cfg.maxWorkers, &b.cfg.hooks, b.cfg.botAgent)
	err := b.polling.Run(ctx)
	b.cfg.hooks.callOnBotStop(err)
	return err
}

// Stop gracefully stops the polling loop and waits for in-flight handlers.
func (b *Bot) Stop() {
	if err := notifyStop(context.Background(), b.c, b.baseInfo()); err != nil {
		b.cfg.logger.Warn("notifyStop failed (ignored)", "error", err)
	}
	if b.polling != nil {
		b.polling.Stop()
	}
}

// ─── Route Registration ───────────────────────────────────────────────────────

// Use registers global middleware that runs before every message handler.
func (b *Bot) Use(handlers ...HandlerFunc) {
	b.dispatcher.use(handlers...)
}

// On registers handlers for messages that satisfy the given matcher.
func (b *Bot) On(m Matcher, handlers ...HandlerFunc) {
	b.dispatcher.add(m, handlers...)
}

// OnText registers handlers for text messages.
func (b *Bot) OnText(handlers ...HandlerFunc) {
	b.dispatcher.add(matchText(), handlers...)
}

// OnImage registers handlers for image messages.
func (b *Bot) OnImage(handlers ...HandlerFunc) {
	b.dispatcher.add(matchImage(), handlers...)
}

// OnVoice registers handlers for voice messages.
func (b *Bot) OnVoice(handlers ...HandlerFunc) {
	b.dispatcher.add(matchVoice(), handlers...)
}

// OnFile registers handlers for file messages.
func (b *Bot) OnFile(handlers ...HandlerFunc) {
	b.dispatcher.add(matchFile(), handlers...)
}

// OnVideo registers handlers for video messages.
func (b *Bot) OnVideo(handlers ...HandlerFunc) {
	b.dispatcher.add(matchVideo(), handlers...)
}

// OnGroup registers handlers for group messages (any content type).
func (b *Bot) OnGroup(handlers ...HandlerFunc) {
	b.dispatcher.add(matchGroup(), handlers...)
}

// OnPrivate registers handlers for private (1-on-1) messages.
func (b *Bot) OnPrivate(handlers ...HandlerFunc) {
	b.dispatcher.add(matchPrivate(), handlers...)
}

// OnGroupText registers handlers for text messages inside a group.
func (b *Bot) OnGroupText(handlers ...HandlerFunc) {
	b.dispatcher.add(andMatch(matchGroup(), matchText()), handlers...)
}

// OnPrivateText registers handlers for text messages in private chat.
func (b *Bot) OnPrivateText(handlers ...HandlerFunc) {
	b.dispatcher.add(andMatch(matchPrivate(), matchText()), handlers...)
}

// OnTextContains registers handlers for text messages containing substr.
func (b *Bot) OnTextContains(substr string, handlers ...HandlerFunc) {
	b.dispatcher.add(matchTextContains(substr), handlers...)
}

// OnTextPrefix registers handlers for text messages starting with prefix.
func (b *Bot) OnTextPrefix(prefix string, handlers ...HandlerFunc) {
	b.dispatcher.add(matchTextPrefix(prefix), handlers...)
}

// OnTextMatch registers handlers for text messages matching a regex pattern.
// Panics if pattern is invalid.
func (b *Bot) OnTextMatch(pattern string, handlers ...HandlerFunc) {
	b.dispatcher.add(matchTextRegex(pattern), handlers...)
}

// OnGroupID registers handlers for messages from a specific group.
func (b *Bot) OnGroupID(groupID string, handlers ...HandlerFunc) {
	b.dispatcher.add(matchGroupID(groupID), handlers...)
}

// OnUserID registers handlers for messages from a specific user.
func (b *Bot) OnUserID(userID string, handlers ...HandlerFunc) {
	b.dispatcher.add(matchUserID(userID), handlers...)
}

// ─── Proactive Send ───────────────────────────────────────────────────────────

// SendText sends a text message to userID using the stored context token.
// Returns ErrNoContextToken if no context token has been stored for the user.
func (b *Bot) SendText(ctx context.Context, userID, text string) error {
	token, err := b.ctxStore.Load(userID)
	if err != nil {
		return fmt.Errorf("load context token: %w", err)
	}
	if token == "" {
		return ErrNoContextToken
	}
	return sendText(ctx, b.c, b.cfg.channelVersion, b.cfg.botAgent, userID, text, token)
}

// SendImage sends an image message to userID.
func (b *Bot) SendImage(ctx context.Context, userID string, img *ImageItem) error {
	token, err := b.ctxStore.Load(userID)
	if err != nil {
		return fmt.Errorf("load context token: %w", err)
	}
	if token == "" {
		return ErrNoContextToken
	}
	return sendImage(ctx, b.c, b.cfg.channelVersion, b.cfg.botAgent, userID, token, img)
}

// SendVoice sends a voice message to userID.
func (b *Bot) SendVoice(ctx context.Context, userID string, voice *VoiceItem) error {
	token, err := b.ctxStore.Load(userID)
	if err != nil {
		return fmt.Errorf("load context token: %w", err)
	}
	if token == "" {
		return ErrNoContextToken
	}
	return sendVoice(ctx, b.c, b.cfg.channelVersion, b.cfg.botAgent, userID, token, voice)
}

// SendFile sends a file message to userID.
func (b *Bot) SendFile(ctx context.Context, userID string, file *FileItem) error {
	token, err := b.ctxStore.Load(userID)
	if err != nil {
		return fmt.Errorf("load context token: %w", err)
	}
	if token == "" {
		return ErrNoContextToken
	}
	return sendFile(ctx, b.c, b.cfg.channelVersion, b.cfg.botAgent, userID, token, file)
}

// SendVideo sends a video message to userID.
func (b *Bot) SendVideo(ctx context.Context, userID string, video *VideoItem) error {
	token, err := b.ctxStore.Load(userID)
	if err != nil {
		return fmt.Errorf("load context token: %w", err)
	}
	if token == "" {
		return ErrNoContextToken
	}
	return sendVideo(ctx, b.c, b.cfg.channelVersion, b.cfg.botAgent, userID, token, video)
}

// ─── Media ────────────────────────────────────────────────────────────────────

// Upload encrypts and uploads raw bytes to WeChat CDN.
// fileType: "image" | "video" | "voice" | "file"
// toUserID: the intended recipient (required by the upload URL API).
func (b *Bot) Upload(ctx context.Context, data []byte, toUserID, fileType string) (*UploadResult, error) {
	return b.media.UploadFile(ctx, data, toUserID, fileType)
}

// Download downloads and decrypts a CDN media file.
// cdnURL: constructed from CDNMedia.EncryptQueryParam via media.BuildDownloadURL.
// aesKeyHex: hex-encoded AES key (from UploadResult or CDNMedia.AESKey on inbound messages).
func (b *Bot) Download(ctx context.Context, cdnURL, aesKeyHex string) ([]byte, error) {
	return b.media.DownloadFile(ctx, cdnURL, aesKeyHex)
}

// DownloadMedia is a convenience wrapper that builds the CDN URL and decrypts using a CDNMedia.
// The AESKey in CDNMedia is expected to be base64-encoded (used by voice/file/video items).
func (b *Bot) DownloadMedia(ctx context.Context, media *CDNMedia) ([]byte, error) {
	cdnURL := b.media.BuildDownloadURL(media)
	return b.media.DownloadFileWithBase64Key(ctx, cdnURL, media.AESKey)
}

// DownloadImage downloads and decrypts an inbound image.
// Inbound ImageItem carries the AES key as a hex string in ImageItem.AESKey
// (distinct from outbound CDNMedia.AESKey which is base64-encoded).
func (b *Bot) DownloadImage(ctx context.Context, img *ImageItem) ([]byte, error) {
	if img == nil || img.Media == nil {
		return nil, fmt.Errorf("image item has no media")
	}
	cdnURL := b.media.BuildDownloadURL(img.Media)
	// For inbound images the key lives in ImageItem.AESKey as a hex string.
	// For outbound/re-sent images it may live in img.Media.AESKey as base64.
	if img.AESKey != "" {
		return b.media.DownloadFile(ctx, cdnURL, img.AESKey)
	}
	return b.media.DownloadFileWithBase64Key(ctx, cdnURL, img.Media.AESKey)
}

// CDNBaseURL returns the configured CDN base URL (useful for building download URLs manually).
func (b *Bot) CDNBaseURL() string { return b.cfg.cdnBaseURL }

// ─── Context Token Management ─────────────────────────────────────────────────

// GetContextToken returns the stored context token for a user.
func (b *Bot) GetContextToken(userID string) (string, error) {
	return b.ctxStore.Load(userID)
}

// SetContextToken manually stores a context token for a user.
func (b *Bot) SetContextToken(userID, token string) error {
	return b.ctxStore.Save(userID, token)
}

// ClearContextToken removes the stored context token for a user.
func (b *Bot) ClearContextToken(userID string) error {
	return b.ctxStore.Clear(userID)
}

// Logger returns the configured logger.
func (b *Bot) Logger() *slog.Logger { return b.cfg.logger }

// baseInfo builds the BaseInfo payload for API requests.
func (b *Bot) baseInfo() *BaseInfo {
	return &BaseInfo{
		ChannelVersion: b.cfg.channelVersion,
		BotAgent:       b.cfg.botAgent,
	}
}

// ─── Internal ────────────────────────────────────────────────────────────────

// handleMessage is the internal messageHandler passed to the poller.
// It persists the context token and dispatches to registered handlers.
func (b *Bot) handleMessage(ctx context.Context, msg *Message) error {
	// Persist context token for later proactive sends
	if msg.ContextToken != "" && msg.FromUserID != "" {
		if err := b.ctxStore.Save(msg.FromUserID, msg.ContextToken); err != nil {
			b.cfg.logger.Warn("failed to persist context token",
				"user_id", msg.FromUserID, "error", err)
		}
	}

	// Build the Context and dispatch (panic-safe)
	msgCtx := newContext(ctx, msg, b, nil)
	func() {
		defer func() {
			if r := recover(); r != nil {
				b.cfg.logger.Error("handler panic recovered",
					"from_user_id", msg.FromUserID, "panic", r)
				b.cfg.hooks.callOnHandlerPanic(r, msg)
			}
		}()
		b.dispatcher.dispatch(msgCtx)
	}()

	return nil
}
