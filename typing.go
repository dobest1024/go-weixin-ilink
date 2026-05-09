package ilink

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

const (
	typingStatusStart = 1
	typingStatusStop  = 2
	ticketTTL         = 24 * time.Hour
)

type sendTypingRequest struct {
	IlinkUserID  string `json:"ilink_user_id"`
	TypingTicket string `json:"typing_ticket"`
	Status       int    `json:"status"`
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

type ticketEntry struct {
	ticket  string
	expiry  time.Time
}

type typingManager struct {
	c        *client
	logger   *slog.Logger
	botAgent string

	mu      sync.RWMutex
	tickets map[string]*ticketEntry // keyed by userID
}

func newTypingManager(c *client, logger *slog.Logger, botAgent string) *typingManager {
	return &typingManager{
		c:        c,
		logger:   logger,
		botAgent: botAgent,
		tickets:  make(map[string]*ticketEntry),
	}
}

// getTicket fetches (or returns cached) the typing_ticket for a specific user.
// contextToken should be the user's current context_token if available.
func (tm *typingManager) getTicket(ctx context.Context, userID, contextToken string) (string, error) {
	tm.mu.RLock()
	e := tm.tickets[userID]
	if e != nil && e.ticket != "" && time.Now().Before(e.expiry) {
		t := e.ticket
		tm.mu.RUnlock()
		return t, nil
	}
	tm.mu.RUnlock()

	req := &getConfigRequest{
		IlinkUserID:  userID,
		ContextToken: contextToken,
		BaseInfo:     &BaseInfo{ChannelVersion: tm.c.channelVersion, BotAgent: tm.botAgent},
	}
	var resp getConfigResponse
	if err := tm.c.post(ctx, "/ilink/bot/getconfig", req, &resp); err != nil {
		return "", err
	}
	if resp.Ret != 0 {
		return "", &APIError{Code: resp.Ret, Message: resp.ErrMsg}
	}

	tm.mu.Lock()
	tm.tickets[userID] = &ticketEntry{
		ticket: resp.TypingTicket,
		expiry: time.Now().Add(ticketTTL),
	}
	tm.mu.Unlock()
	return resp.TypingTicket, nil
}

func (tm *typingManager) send(ctx context.Context, userID, contextToken string, status int) error {
	ticket, err := tm.getTicket(ctx, userID, contextToken)
	if err != nil {
		return err
	}
	req := &sendTypingRequest{IlinkUserID: userID, TypingTicket: ticket, Status: status}
	var resp sendTypingResponse
	if err := tm.c.post(ctx, "/ilink/bot/sendtyping", req, &resp); err != nil {
		return err
	}
	if resp.Ret != 0 {
		return &APIError{Code: resp.Ret, Message: resp.ErrMsg}
	}
	return nil
}

func (tm *typingManager) StartTyping(ctx context.Context, userID, contextToken string) error {
	return tm.send(ctx, userID, contextToken, typingStatusStart)
}

func (tm *typingManager) StopTyping(ctx context.Context, userID, contextToken string) error {
	return tm.send(ctx, userID, contextToken, typingStatusStop)
}
