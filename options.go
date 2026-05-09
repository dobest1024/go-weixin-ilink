package ilink

import (
	"log/slog"
	"net/http"
)

const (
	defaultBaseURL         = "https://ilinkai.weixin.qq.com"
	defaultCDNBaseURL      = "https://novac2c.cdn.weixin.qq.com/c2c"
	defaultChannelVersion  = "2.4.3"
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

	// Lifecycle hooks
	hooks Hooks
}

func defaultConfig() *config {
	return &config{
		baseURL:         defaultBaseURL,
		cdnBaseURL:      defaultCDNBaseURL,
		channelVersion:  defaultChannelVersion,
		tokenFile:       defaultTokenFile,
		contextTokenDir: defaultContextTokenDir,
		httpClient:      &http.Client{},
		logger:          slog.Default(),
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
