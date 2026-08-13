package ilink

import (
	"context"
	"log/slog"
	"math/rand"
	"sync"
	"time"
)

const (
	typingStatusStart = 1
	typingStatusStop  = 2

	// configTTL is the upper bound on how long a cached typing ticket is used.
	// The actual refresh point is randomised across the window so a fleet of
	// bots does not stampede the config endpoint at the same instant.
	configTTL = 24 * time.Hour
	// configInitialRetry / configMaxRetry bound the exponential backoff applied
	// after a failed getconfig call.
	configInitialRetry = 2 * time.Second
	configMaxRetry     = time.Hour
)

type sendTypingRequest struct {
	IlinkUserID  string    `json:"ilink_user_id"`
	TypingTicket string    `json:"typing_ticket"`
	Status       int       `json:"status"`
	BaseInfo     *BaseInfo `json:"base_info,omitempty"`
}

type sendTypingResponse struct {
	Ret     int    `json:"ret"`
	ErrCode int    `json:"errcode,omitempty"`
	ErrMsg  string `json:"errmsg,omitempty"`
}

type getConfigRequest struct {
	IlinkUserID  string    `json:"ilink_user_id"`
	ContextToken string    `json:"context_token,omitempty"`
	BaseInfo     *BaseInfo `json:"base_info,omitempty"`
}

// configEntry caches one user's bot config alongside its refresh schedule.
type configEntry struct {
	ticket        string
	everSucceeded bool
	nextFetchAt   time.Time
	retryDelay    time.Duration
}

// typingManager owns the per-user config cache that backs the typing ticket.
//
// A failed getconfig must not invalidate a working ticket: the cache keeps the
// last good value, retries with exponential backoff up to an hour, and lets
// callers keep sending typing indicators in the meantime.
type typingManager struct {
	c        *client
	logger   *slog.Logger
	botAgent string

	mu      sync.Mutex
	entries map[string]*configEntry // keyed by userID
	rnd     *rand.Rand
}

func newTypingManager(c *client, logger *slog.Logger, botAgent string) *typingManager {
	return &typingManager{
		c:        c,
		logger:   logger,
		botAgent: botAgent,
		entries:  make(map[string]*configEntry),
		rnd:      rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

func (tm *typingManager) baseInfo() *BaseInfo {
	return &BaseInfo{ChannelVersion: tm.c.channelVersion, BotAgent: tm.botAgent}
}

// getTicket returns the typing ticket for a user, refreshing it when due.
// It never fails the caller over a transient config error: a stale (or empty)
// ticket is returned and the refresh is retried later with backoff.
func (tm *typingManager) getTicket(ctx context.Context, userID, contextToken string) string {
	tm.mu.Lock()
	entry := tm.entries[userID]
	due := entry == nil || time.Now().After(entry.nextFetchAt)
	tm.mu.Unlock()

	if due {
		ticket, err := tm.fetchConfig(ctx, userID, contextToken)
		tm.mu.Lock()
		tm.recordFetch(userID, ticket, err)
		tm.mu.Unlock()
	}

	tm.mu.Lock()
	defer tm.mu.Unlock()
	if e := tm.entries[userID]; e != nil {
		return e.ticket
	}
	return ""
}

// recordFetch updates the cache and the refresh schedule. Caller holds tm.mu.
func (tm *typingManager) recordFetch(userID, ticket string, err error) {
	now := time.Now()
	entry := tm.entries[userID]

	if err == nil {
		// Spread refreshes randomly over the TTL to avoid a thundering herd.
		jitter := time.Duration(tm.rnd.Int63n(int64(configTTL)))
		tm.entries[userID] = &configEntry{
			ticket:        ticket,
			everSucceeded: true,
			nextFetchAt:   now.Add(jitter),
			retryDelay:    configInitialRetry,
		}
		if entry == nil || !entry.everSucceeded {
			tm.logger.Debug("typing ticket cached", "user_id", userID)
		} else {
			tm.logger.Debug("typing ticket refreshed", "user_id", userID)
		}
		return
	}

	tm.logger.Warn("getconfig failed, keeping cached ticket", "user_id", userID, "error", err)

	prevDelay := configInitialRetry
	if entry != nil && entry.retryDelay > 0 {
		prevDelay = entry.retryDelay
	}
	nextDelay := prevDelay * 2
	if nextDelay > configMaxRetry {
		nextDelay = configMaxRetry
	}

	if entry != nil {
		entry.nextFetchAt = now.Add(nextDelay)
		entry.retryDelay = nextDelay
		return
	}
	tm.entries[userID] = &configEntry{
		nextFetchAt: now.Add(configInitialRetry),
		retryDelay:  configInitialRetry,
	}
}

func (tm *typingManager) fetchConfig(ctx context.Context, userID, contextToken string) (string, error) {
	req := &getConfigRequest{
		IlinkUserID:  userID,
		ContextToken: contextToken,
		BaseInfo:     tm.baseInfo(),
	}
	var resp getConfigResponse
	if err := tm.c.post(ctx, "/ilink/bot/getconfig", req, &resp); err != nil {
		return "", err
	}
	if err := apiError(resp.Ret, resp.ErrCode, resp.ErrMsg); err != nil {
		return "", err
	}
	return resp.TypingTicket, nil
}

// ClearConfigCache drops the cached config for a user, forcing a refetch on the
// next typing indicator. Call it after a re-login.
func (tm *typingManager) ClearConfigCache(userID string) {
	tm.mu.Lock()
	delete(tm.entries, userID)
	tm.mu.Unlock()
}

func (tm *typingManager) send(ctx context.Context, userID, contextToken string, status int) error {
	ticket := tm.getTicket(ctx, userID, contextToken)
	if ticket == "" {
		return &APIError{Code: 0, Message: "no typing ticket available for user " + userID}
	}
	req := &sendTypingRequest{
		IlinkUserID:  userID,
		TypingTicket: ticket,
		Status:       status,
		BaseInfo:     tm.baseInfo(),
	}
	var resp sendTypingResponse
	if err := tm.c.post(ctx, "/ilink/bot/sendtyping", req, &resp); err != nil {
		return err
	}
	return apiError(resp.Ret, resp.ErrCode, resp.ErrMsg)
}

func (tm *typingManager) StartTyping(ctx context.Context, userID, contextToken string) error {
	return tm.send(ctx, userID, contextToken, typingStatusStart)
}

func (tm *typingManager) StopTyping(ctx context.Context, userID, contextToken string) error {
	return tm.send(ctx, userID, contextToken, typingStatusStop)
}
