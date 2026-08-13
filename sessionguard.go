package ilink

import (
	"sync"
	"time"
)

// sessionPauseDuration is how long every API call is suppressed after the
// server reports a stale token (-14). It matches the official plugin's
// one-hour cooldown.
const sessionPauseDuration = time.Hour

// sessionGuard suppresses *all* API traffic for a bot after the server reports
// a stale token, not just the polling loop. Without it, proactive sends, media
// uploads and typing indicators keep hammering a token the server has already
// rejected.
type sessionGuard struct {
	mu    sync.RWMutex
	until time.Time
}

// pause starts (or extends) the cooldown and returns when it will end.
func (g *sessionGuard) pause(d time.Duration) time.Time {
	g.mu.Lock()
	defer g.mu.Unlock()
	until := time.Now().Add(d)
	if until.After(g.until) {
		g.until = until
	}
	return g.until
}

// resume clears the cooldown, e.g. after a successful re-login.
func (g *sessionGuard) resume() {
	g.mu.Lock()
	g.until = time.Time{}
	g.mu.Unlock()
}

// remaining reports how long the cooldown still has to run; zero when active.
func (g *sessionGuard) remaining() time.Duration {
	g.mu.RLock()
	until := g.until
	g.mu.RUnlock()
	if until.IsZero() {
		return 0
	}
	d := time.Until(until)
	if d <= 0 {
		return 0
	}
	return d
}

// check returns a SessionPausedError while the cooldown is in effect.
func (g *sessionGuard) check() error {
	if d := g.remaining(); d > 0 {
		return &SessionPausedError{Remaining: d}
	}
	return nil
}
