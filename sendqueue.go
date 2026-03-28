package ilink

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// ─── Batch Send ──────────────────────────────────────────────────────────────

// BatchResult holds the per-user result of a batch send operation.
type BatchResult struct {
	UserID string
	Err    error
}

// BatchSendText sends a text message to multiple users.
// Returns results for each user (nil error = success).
func (b *Bot) BatchSendText(ctx context.Context, userIDs []string, text string) []BatchResult {
	results := make([]BatchResult, len(userIDs))
	var wg sync.WaitGroup
	for i, uid := range userIDs {
		wg.Add(1)
		go func(idx int, userID string) {
			defer wg.Done()
			results[idx] = BatchResult{
				UserID: userID,
				Err:    b.SendText(ctx, userID, text),
			}
		}(i, uid)
	}
	wg.Wait()
	return results
}

// BatchSendItems sends custom message items to multiple users.
func (b *Bot) BatchSendItems(ctx context.Context, userIDs []string, items []MessageItem) []BatchResult {
	results := make([]BatchResult, len(userIDs))
	var wg sync.WaitGroup
	for i, uid := range userIDs {
		wg.Add(1)
		go func(idx int, userID string) {
			defer wg.Done()
			token, err := b.ctxStore.Load(userID)
			if err != nil {
				results[idx] = BatchResult{UserID: userID, Err: fmt.Errorf("load context token: %w", err)}
				return
			}
			if token == "" {
				results[idx] = BatchResult{UserID: userID, Err: ErrNoContextToken}
				return
			}
			msg := newBotMsg(userID, token, items)
			results[idx] = BatchResult{
				UserID: userID,
				Err:    sendRaw(ctx, b.c, b.cfg.channelVersion, msg),
			}
		}(i, uid)
	}
	wg.Wait()
	return results
}

// ─── Send Queue ──────────────────────────────────────────────────────────────

// SendTask represents a single message to be sent via the SendQueue.
type SendTask struct {
	UserID string
	Items  []MessageItem
}

// SendQueue is a rate-limited, buffered message send queue.
// Use it for mass push scenarios (stock alerts, notifications) to avoid
// hitting WeChat API rate limits.
type SendQueue struct {
	bot      *Bot
	ch       chan *sendJob
	interval time.Duration
	wg       sync.WaitGroup
	stopCh   chan struct{}
}

type sendJob struct {
	task   SendTask
	result chan<- error
}

// NewSendQueue creates a queue that sends at most one message per interval.
// bufferSize controls the channel buffer (pending tasks).
func NewSendQueue(bot *Bot, interval time.Duration, bufferSize int) *SendQueue {
	return &SendQueue{
		bot:      bot,
		ch:       make(chan *sendJob, bufferSize),
		interval: interval,
		stopCh:   make(chan struct{}),
	}
}

// Start begins processing the queue. Blocks until Stop is called or ctx is cancelled.
func (q *SendQueue) Start(ctx context.Context) {
	ticker := time.NewTicker(q.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-q.stopCh:
			return
		case job := <-q.ch:
			q.send(ctx, job)
			// Wait for next tick to enforce rate limit.
			select {
			case <-ctx.Done():
				return
			case <-q.stopCh:
				return
			case <-ticker.C:
			}
		}
	}
}

// Enqueue adds a send task to the queue. Returns a channel that receives the send result.
// Returns nil immediately if the queue is full.
func (q *SendQueue) Enqueue(task SendTask) <-chan error {
	result := make(chan error, 1)
	select {
	case q.ch <- &sendJob{task: task, result: result}:
		return result
	default:
		result <- fmt.Errorf("ilink: send queue full")
		return result
	}
}

// EnqueueText is a convenience method to enqueue a text message.
func (q *SendQueue) EnqueueText(userID, text string) <-chan error {
	return q.Enqueue(SendTask{
		UserID: userID,
		Items:  []MessageItem{{Type: ItemTypeText, TextItem: &TextItem{Text: text}}},
	})
}

// Stop signals the queue to stop processing.
func (q *SendQueue) Stop() {
	select {
	case <-q.stopCh:
	default:
		close(q.stopCh)
	}
}

// Pending returns the number of tasks waiting in the queue.
func (q *SendQueue) Pending() int {
	return len(q.ch)
}

func (q *SendQueue) send(ctx context.Context, job *sendJob) {
	token, err := q.bot.ctxStore.Load(job.task.UserID)
	if err != nil {
		job.result <- fmt.Errorf("load context token: %w", err)
		return
	}
	if token == "" {
		job.result <- ErrNoContextToken
		return
	}
	msg := newBotMsg(job.task.UserID, token, job.task.Items)
	job.result <- sendRaw(ctx, q.bot.c, q.bot.cfg.channelVersion, msg)
}
