package ilink

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"sync"
	"time"
)

// messageHandler is the internal callback for each received user message.
type messageHandler func(ctx context.Context, msg *Message) error

type poller struct {
	c              *client
	handler        messageHandler
	channelVersion string
	logger         *slog.Logger
	syncBuf        SyncBufStore // optional; nil means in-memory only
	hooks          *Hooks
	maxWorkers     int // 0 = serial

	mu            sync.Mutex
	getUpdatesBuf string
	cancelFn      context.CancelFunc
	stopCh        chan struct{}
	wg            sync.WaitGroup
}

func newPoller(c *client, handler messageHandler, channelVersion string, logger *slog.Logger, syncBuf SyncBufStore, maxWorkers int, hooks *Hooks) *poller {
	p := &poller{
		c:              c,
		handler:        handler,
		channelVersion: channelVersion,
		logger:         logger,
		syncBuf:        syncBuf,
		hooks:          hooks,
		maxWorkers:     maxWorkers,
		stopCh:         make(chan struct{}),
	}
	// Restore persisted cursor on startup.
	if syncBuf != nil {
		if buf, err := syncBuf.Load(); err != nil {
			logger.Warn("failed to load sync buf", "error", err)
		} else if buf != "" {
			p.getUpdatesBuf = buf
			logger.Debug("restored get_updates_buf from disk", "len", len(buf))
		}
	}
	return p
}

// Run starts the long-polling loop. Blocks until ctx is cancelled or Stop is called.
func (p *poller) Run(ctx context.Context) error {
	const (
		defaultTimeoutMs = 35000
		paddingMs        = 10000
		minTimeoutMs     = 20000
		maxConsecFails   = 3
		backoffDelay     = 30 * time.Second
		sessionPauseDur  = 1 * time.Hour
	)

	inner, cancel := context.WithCancel(ctx)
	p.mu.Lock()
	p.cancelFn = cancel
	p.mu.Unlock()
	defer cancel()

	// Worker pool semaphore (nil = serial).
	var sem chan struct{}
	if p.maxWorkers > 0 {
		sem = make(chan struct{}, p.maxWorkers)
	}

	httpTimeout := time.Duration(defaultTimeoutMs+paddingMs) * time.Millisecond
	consecFails := 0
	wasExpired := false

	for {
		select {
		case <-inner.Done():
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return ErrPollerStopped
		case <-p.stopCh:
			return ErrPollerStopped
		default:
		}

		pollCtx, pollCancel := context.WithTimeout(inner, httpTimeout)
		resp, err := p.poll(pollCtx)
		pollCancel()

		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, context.Canceled) {
				return ErrPollerStopped
			}
			if IsSessionExpired(err) {
				p.hooks.callOnSessionExpired()
				p.logger.Error("session expired, pausing for 1 hour before retry", "error", err)
				wasExpired = true
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-p.stopCh:
					return ErrPollerStopped
				case <-time.After(sessionPauseDur):
				}
				consecFails = 0
				continue
			}

			var netErr net.Error
			isTimeout := errors.Is(err, context.DeadlineExceeded) ||
				(errors.As(err, &netErr) && netErr.Timeout())
			if isTimeout {
				p.logger.Debug("poll timeout (normal), reconnecting")
				consecFails = 0
				continue
			}

			consecFails++
			p.hooks.callOnError(err)
			p.logger.Warn("poll error", "error", err, "consecutive_fails", consecFails)
			if consecFails >= maxConsecFails {
				p.logger.Info("backing off", "delay", backoffDelay)
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-p.stopCh:
					return ErrPollerStopped
				case <-time.After(backoffDelay):
				}
				consecFails = 0
			}
			continue
		}

		consecFails = 0

		// If we were in session-expired state, this successful poll means recovery.
		if wasExpired {
			wasExpired = false
			p.hooks.callOnSessionRecovered()
			p.logger.Info("session recovered after pause")
		}

		if resp.LongPollingTimeoutMs > 0 {
			t := time.Duration(resp.LongPollingTimeoutMs+paddingMs) * time.Millisecond
			if t < time.Duration(minTimeoutMs)*time.Millisecond {
				t = time.Duration(minTimeoutMs) * time.Millisecond
			}
			httpTimeout = t
		}

		for i := range resp.Messages {
			msg := &resp.Messages[i]
			if msg.MessageType != MessageTypeUser {
				continue
			}

			p.wg.Add(1)
			if sem != nil {
				// Concurrent: acquire slot, spawn goroutine.
				sem <- struct{}{}
				go func(m *Message) {
					defer func() {
						<-sem
						p.wg.Done()
					}()
					if err := p.handler(inner, m); err != nil {
						p.logger.Error("handler error", "error", err, "from_user_id", m.FromUserID)
					}
				}(msg)
			} else {
				// Serial: process in-line.
				if err := p.handler(inner, msg); err != nil {
					p.logger.Error("handler error", "error", err, "from_user_id", msg.FromUserID)
				}
				p.wg.Done()
			}
		}

		if resp.GetUpdatesBuf != "" {
			p.mu.Lock()
			p.getUpdatesBuf = resp.GetUpdatesBuf
			p.mu.Unlock()
			// Persist to disk so restarts resume from this position.
			if p.syncBuf != nil {
				if err := p.syncBuf.Save(resp.GetUpdatesBuf); err != nil {
					p.logger.Warn("failed to persist sync buf", "error", err)
				}
			}
		}
	}
}

func (p *poller) poll(ctx context.Context) (*GetUpdatesResponse, error) {
	p.mu.Lock()
	buf := p.getUpdatesBuf
	p.mu.Unlock()

	req := &GetUpdatesRequest{
		GetUpdatesBuf: buf,
		BaseInfo:      &BaseInfo{ChannelVersion: p.channelVersion},
	}
	var resp GetUpdatesResponse
	if err := p.c.post(ctx, "/ilink/bot/getupdates", req, &resp); err != nil {
		return nil, err
	}
	// Check both ret and errcode — official SDK reports errors via either field.
	code := resp.Ret
	if code == 0 {
		code = resp.ErrCode
	}
	if code != 0 {
		return nil, &APIError{Code: code, Message: resp.ErrMsg}
	}
	return &resp, nil
}

// Stop signals the poller to stop and waits for in-flight handlers.
func (p *poller) Stop() {
	select {
	case <-p.stopCh:
	default:
		close(p.stopCh)
		p.mu.Lock()
		if p.cancelFn != nil {
			p.cancelFn()
		}
		p.mu.Unlock()
	}
	p.wg.Wait()
}
