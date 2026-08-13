package ilink

import (
	"log/slog"
	"net/http"
)

const (
	defaultBaseURL        = "https://ilinkai.weixin.qq.com"
	defaultCDNBaseURL     = "https://novac2c.cdn.weixin.qq.com/c2c"
	defaultChannelVersion = "2.4.6"
	// defaultAppID is the iLink-App-Id header value. The server rejects or
	// mis-routes requests that omit it, so it must never default to empty.
	defaultAppID           = "bot"
	defaultBotType         = "3"
	defaultTokenFile       = ".ilink-token.json"
	defaultContextTokenDir = ".ilink-context-tokens"
)

type config struct {
	baseURL           string
	cdnBaseURL        string
	channelVersion    string
	appID             string
	botAgent          string
	tokenFile         string
	contextTokenDir   string
	syncBufFile       string
	tokenStore        TokenStore
	contextTokenStore ContextTokenStore
	syncBufStore      SyncBufStore
	httpClient        *http.Client
	logger            *slog.Logger

	// Concurrency: max goroutines for message handling; 0 = serial (default).
	maxWorkers int

	// SKRouteTag is an optional routing hint header sent with every API request.
	skRouteTag string

	// botType selects which QR code flavour to request (get_bot_qrcode?bot_type=).
	botType string

	// verifyCode supplies the pair code when the server challenges a QR scan.
	verifyCode VerifyCodeFunc

	// localTokens lists bot_tokens already held locally, sent with the QR
	// request so the server can recognise an already-bound bot.
	localTokens func() []string

	// voiceTranscoder converts inbound SILK voice payloads to WAV.
	voiceTranscoder VoiceTranscoder

	// AllowFrom restricts message processing to listed user IDs.
	// nil/empty = accept all messages.
	allowFrom map[string]struct{}

	// Lifecycle hooks
	hooks Hooks
}

func defaultConfig() *config {
	return &config{
		baseURL:         defaultBaseURL,
		cdnBaseURL:      defaultCDNBaseURL,
		channelVersion:  defaultChannelVersion,
		appID:           defaultAppID,
		botType:         defaultBotType,
		tokenFile:       defaultTokenFile,
		contextTokenDir: defaultContextTokenDir,
		httpClient:      &http.Client{},
		logger:          slog.Default(),
		verifyCode:      TerminalVerifyCode,
	}
}

// Option configures a Bot.
type Option func(*config)

// WithBaseURL sets the iLink API base URL.
func WithBaseURL(url string) Option {
	return func(c *config) { c.baseURL = url }
}

// WithCDNBaseURL sets the CDN base URL for media upload/download.
func WithCDNBaseURL(url string) Option {
	return func(c *config) { c.cdnBaseURL = url }
}

// WithChannelVersion sets the channel_version sent with every API request.
func WithChannelVersion(v string) Option {
	return func(c *config) { c.channelVersion = v }
}

// WithTokenFile sets the file path for persisting the bot token.
func WithTokenFile(path string) Option {
	return func(c *config) { c.tokenFile = path }
}

// WithContextTokenDir sets the directory for persisting per-user context tokens.
func WithContextTokenDir(dir string) Option {
	return func(c *config) { c.contextTokenDir = dir }
}

// WithTokenStore replaces the default FileTokenStore with a custom implementation.
func WithTokenStore(store TokenStore) Option {
	return func(c *config) { c.tokenStore = store }
}

// WithContextTokenStore replaces the default FileContextTokenStore.
func WithContextTokenStore(store ContextTokenStore) Option {
	return func(c *config) { c.contextTokenStore = store }
}

// WithSyncBufFile sets the file path for persisting the get_updates_buf cursor.
// When set, the poller resumes from the last position after a restart instead
// of re-reading all history. Recommended for production bots.
func WithSyncBufFile(path string) Option {
	return func(c *config) { c.syncBufFile = path }
}

// WithSyncBufStore replaces the default FileSyncBufStore with a custom implementation.
func WithSyncBufStore(store SyncBufStore) Option {
	return func(c *config) { c.syncBufStore = store }
}

// WithHTTPClient sets a custom HTTP client.
// Note: do not set http.Client.Timeout — use context deadlines instead.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *config) {
		if hc.Timeout > 0 {
			c.logger.Warn("ilink: HTTP client has Timeout set — this may break long-polling; remove it")
		}
		c.httpClient = hc
	}
}

// WithLogger sets the logger.
func WithLogger(l *slog.Logger) Option {
	return func(c *config) { c.logger = l }
}

// WithMaxWorkers sets the maximum number of concurrent message handlers.
// 0 (default) means messages are processed serially in the polling goroutine.
// A positive value spawns up to n goroutines for parallel message processing.
func WithMaxWorkers(n int) Option {
	return func(c *config) { c.maxWorkers = n }
}

// WithHooks sets lifecycle hooks for the bot.
func WithHooks(h Hooks) Option {
	return func(c *config) { c.hooks = h }
}

// WithAppID sets the iLink-App-Id header value sent with every API request.
func WithAppID(id string) Option {
	return func(c *config) { c.appID = id }
}

// WithBotAgent sets the bot_agent field in BaseInfo.
// Format: UA-style "Name/Version" tokens (ASCII only, max 256 bytes).
func WithBotAgent(agent string) Option {
	return func(c *config) { c.botAgent = agent }
}

// WithSKRouteTag sets the SKRouteTag header value sent with every API request.
// This is an optional routing hint used by the iLink backend infrastructure.
func WithSKRouteTag(tag string) Option {
	return func(c *config) { c.skRouteTag = tag }
}

// WithBotType sets the bot_type query parameter used when requesting a login
// QR code. Defaults to "3".
func WithBotType(t string) Option {
	return func(c *config) { c.botType = t }
}

// WithVerifyCodeFunc sets the callback used when the server challenges a QR
// scan and asks for the pair code shown on the user's phone.
//
// The callback receives retry=true when a previously submitted code was
// rejected. Return an error to abort the login. Defaults to TerminalVerifyCode,
// which prompts on stdin; pass nil to fail fast instead (ErrNoVerifyCodeFunc).
func WithVerifyCodeFunc(f VerifyCodeFunc) Option {
	return func(c *config) { c.verifyCode = f }
}

// WithLocalTokens supplies the bot_tokens already stored on this machine. They
// are sent with the QR request so the server can detect a bot that is already
// bound to this client and answer "already connected" instead of issuing a
// duplicate session. At most 10 tokens are sent.
//
// By default the SDK sends the single token held by the configured TokenStore.
// Override this when you manage several bots through a BotManager.
func WithLocalTokens(f func() []string) Option {
	return func(c *config) { c.localTokens = f }
}

// WithVoiceTranscoder installs a SILK-to-WAV converter used by
// Context.VoiceWAV and Bot.DownloadVoiceWAV. Without one, inbound voice bytes
// are returned in their original SILK encoding.
func WithVoiceTranscoder(t VoiceTranscoder) Option {
	return func(c *config) { c.voiceTranscoder = t }
}

// WithAllowFrom restricts the bot to only process messages from the listed
// user IDs. When the list is empty (default), all messages are accepted.
// Messages from unlisted senders are silently dropped before dispatch.
func WithAllowFrom(userIDs ...string) Option {
	return func(c *config) {
		c.allowFrom = make(map[string]struct{}, len(userIDs))
		for _, id := range userIDs {
			c.allowFrom[id] = struct{}{}
		}
	}
}
