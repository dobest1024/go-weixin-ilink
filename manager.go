package ilink

import (
	"context"
	"fmt"
	"sync"
)

// BotInfo holds the runtime state of a managed bot.
type BotInfo struct {
	ID     string
	Bot    *Bot
	Status BotStatus
	Err    error // last error from Run, if any
}

// BotStatus represents the lifecycle state of a managed bot.
type BotStatus int

const (
	BotStatusCreated BotStatus = iota
	BotStatusRunning
	BotStatusStopped
	BotStatusError
)

func (s BotStatus) String() string {
	switch s {
	case BotStatusCreated:
		return "created"
	case BotStatusRunning:
		return "running"
	case BotStatusStopped:
		return "stopped"
	case BotStatusError:
		return "error"
	}
	return "unknown"
}

// BotManager coordinates multiple Bot instances.
// Each bot is identified by a unique string ID (e.g. user account ID).
type BotManager struct {
	mu   sync.RWMutex
	bots map[string]*managedBot
}

type managedBot struct {
	id     string
	bot    *Bot
	cancel context.CancelFunc
	status BotStatus
	err    error
	done   chan struct{}
}

// NewBotManager creates an empty BotManager.
func NewBotManager() *BotManager {
	return &BotManager{bots: make(map[string]*managedBot)}
}

// Add creates and registers a new Bot with the given ID and options.
// Returns an error if a bot with the same ID already exists.
func (m *BotManager) Add(id string, opts ...Option) (*Bot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.bots[id]; exists {
		return nil, fmt.Errorf("ilink: bot %q already exists", id)
	}

	bot := NewBot(opts...)
	m.bots[id] = &managedBot{
		id:     id,
		bot:    bot,
		status: BotStatusCreated,
		done:   make(chan struct{}),
	}
	return bot, nil
}

// Get returns the Bot for the given ID, or nil if not found.
func (m *BotManager) Get(id string) *Bot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if mb, ok := m.bots[id]; ok {
		return mb.bot
	}
	return nil
}

// Start runs the bot in a background goroutine. The bot must be logged in first.
func (m *BotManager) Start(ctx context.Context, id string) error {
	m.mu.Lock()
	mb, ok := m.bots[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("ilink: bot %q not found", id)
	}
	if mb.status == BotStatusRunning {
		m.mu.Unlock()
		return fmt.Errorf("ilink: bot %q is already running", id)
	}

	runCtx, cancel := context.WithCancel(ctx)
	mb.cancel = cancel
	mb.status = BotStatusRunning
	mb.err = nil
	mb.done = make(chan struct{})
	m.mu.Unlock()

	go func() {
		err := mb.bot.Run(runCtx)

		m.mu.Lock()
		mb.err = err
		if err != nil && err != context.Canceled && err != ErrPollerStopped {
			mb.status = BotStatusError
		} else {
			mb.status = BotStatusStopped
		}
		m.mu.Unlock()

		close(mb.done)
	}()

	return nil
}

// Stop gracefully stops a running bot.
func (m *BotManager) Stop(id string) error {
	m.mu.RLock()
	mb, ok := m.bots[id]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("ilink: bot %q not found", id)
	}
	if mb.cancel != nil {
		mb.cancel()
	}
	mb.bot.Stop()
	// Wait for the run goroutine to finish.
	<-mb.done
	return nil
}

// Remove stops (if running) and removes a bot from the manager.
func (m *BotManager) Remove(id string) error {
	m.mu.Lock()
	mb, ok := m.bots[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("ilink: bot %q not found", id)
	}
	delete(m.bots, id)
	m.mu.Unlock()

	// Stop if running.
	if mb.status == BotStatusRunning {
		if mb.cancel != nil {
			mb.cancel()
		}
		mb.bot.Stop()
		<-mb.done
	}
	return nil
}

// List returns info for all managed bots.
func (m *BotManager) List() []BotInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	list := make([]BotInfo, 0, len(m.bots))
	for _, mb := range m.bots {
		list = append(list, BotInfo{
			ID:     mb.id,
			Bot:    mb.bot,
			Status: mb.status,
			Err:    mb.err,
		})
	}
	return list
}

// StopAll stops all running bots.
func (m *BotManager) StopAll() {
	m.mu.RLock()
	ids := make([]string, 0, len(m.bots))
	for id, mb := range m.bots {
		if mb.status == BotStatusRunning {
			ids = append(ids, id)
		}
	}
	m.mu.RUnlock()

	for _, id := range ids {
		_ = m.Stop(id)
	}
}

// Count returns the number of managed bots.
func (m *BotManager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.bots)
}
