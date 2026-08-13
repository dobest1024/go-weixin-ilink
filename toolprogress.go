package ilink

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// ToolProgress streams tool-call progress to the user while an agent run is in
// flight, so a long tool call shows "正在调用 xxx" instead of dead air.
//
// Sends are queued and delivered in order on a background goroutine: progress
// must never block or fail the agent run that produced it, and out-of-order
// start/result pairs would confuse the client. Errors are logged, not returned.
//
// Call Finalize before sending the final reply so all progress items land
// first. A ToolProgress is safe for concurrent use.
type ToolProgress struct {
	sender       sendParams
	toUserID     string
	contextToken string
	logger       *slog.Logger

	mu        sync.Mutex
	queue     chan func()
	finalized bool
	done      chan struct{}
}

// toolProgressQueueSize bounds the backlog before Start/End start dropping
// items rather than blocking the agent run.
const toolProgressQueueSize = 64

// toolProgressSendTimeout bounds one queued progress send.
const toolProgressSendTimeout = 15 * time.Second

// NewToolProgress creates a progress sender for one agent run.
// runID groups the progress items with the run's final reply; pass "" to skip.
func (b *Bot) NewToolProgress(ctx context.Context, toUserID, contextToken, runID string) *ToolProgress {
	tp := &ToolProgress{
		sender:       b.sender().withRunID(runID),
		toUserID:     toUserID,
		contextToken: contextToken,
		logger:       b.cfg.logger,
		queue:        make(chan func(), toolProgressQueueSize),
		done:         make(chan struct{}),
	}
	go tp.run(ctx)
	return tp
}

// ToolProgress creates a progress sender bound to this message's sender,
// context token and run ID.
func (c *Context) ToolProgress() *ToolProgress {
	return c.bot.NewToolProgress(c.Ctx, c.Message.FromUserID, c.Message.ContextToken, c.RunID())
}

func (tp *ToolProgress) run(ctx context.Context) {
	defer close(tp.done)
	for fn := range tp.queue {
		select {
		case <-ctx.Done():
			// Drain without sending: the run is over.
			continue
		default:
		}
		fn()
	}
}

func (tp *ToolProgress) enqueue(item MessageItem, label string) {
	tp.mu.Lock()
	if tp.finalized {
		tp.mu.Unlock()
		return
	}
	tp.mu.Unlock()

	send := func() {
		// Detached from the caller's context on purpose: a progress item that
		// is already queued should still reach the user even if the handler
		// returns first. The timeout keeps a hung send from stalling the queue.
		sendCtx, cancel := context.WithTimeout(context.Background(), toolProgressSendTimeout)
		defer cancel()
		err := tp.sender.items(sendCtx, tp.toUserID, tp.contextToken, []MessageItem{item})
		if err != nil {
			tp.logger.Warn("tool progress send failed",
				"label", label, "to_user_id", tp.toUserID, "error", err)
		}
	}

	select {
	case tp.queue <- send:
	default:
		tp.logger.Warn("tool progress queue full, dropping item", "label", label)
	}
}

// Start announces that a tool call began.
// toolCallID may be empty; supply one to let the client pair Start with End.
func (tp *ToolProgress) Start(toolName, toolCallID string) {
	tp.enqueue(
		BuildToolCallStartItem(toolName, toolCallID, time.Now().UnixMilli()),
		"tool_call_start",
	)
}

// End reports a tool call's outcome. status is normalized to one of the
// ToolCallStatus* constants.
func (tp *ToolProgress) End(toolName, toolCallID, status string) {
	tp.enqueue(
		BuildToolCallResultItem(toolName, toolCallID, status, time.Now().UnixMilli()),
		"tool_call_result",
	)
}

// Track wraps a tool invocation: it sends Start, runs fn, then sends End with
// the status derived from fn's error.
func (tp *ToolProgress) Track(toolName, toolCallID string, fn func() error) error {
	tp.Start(toolName, toolCallID)
	err := fn()
	status := ToolCallStatusCompleted
	if err != nil {
		status = ToolCallStatusFailed
	}
	tp.End(toolName, toolCallID, status)
	return err
}

// Finalize stops accepting new progress items and waits for the queued ones to
// be delivered. Call it before sending the run's final reply. It is idempotent.
func (tp *ToolProgress) Finalize() {
	tp.mu.Lock()
	if tp.finalized {
		tp.mu.Unlock()
		<-tp.done
		return
	}
	tp.finalized = true
	close(tp.queue)
	tp.mu.Unlock()
	<-tp.done
}
