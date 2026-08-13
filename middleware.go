package ilink

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// debugState tracks which users have debug mode switched on.
type debugState struct {
	mu    sync.RWMutex
	users map[string]bool
}

func (d *debugState) toggle(userID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.users == nil {
		d.users = make(map[string]bool)
	}
	d.users[userID] = !d.users[userID]
	return d.users[userID]
}

func (d *debugState) enabled(userID string) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.users[userID]
}

// SlashCommandFunc handles one slash command. args is everything after the
// command word, already trimmed. Return an error to have it logged and reported
// to the user.
type SlashCommandFunc func(c *Context, args string) error

// SlashCommandOptions configures the SlashCommands middleware.
type SlashCommandOptions struct {
	// Commands are extra handlers keyed by their command word, including the
	// leading slash (e.g. "/status"). They take precedence over the built-ins.
	Commands map[string]SlashCommandFunc

	// DisableBuiltins turns off /echo and /toggle-debug.
	DisableBuiltins bool
}

// receivedAtKey holds the time a message entered the handler chain.
const receivedAtKey = "ilink.received_at"

// ReceivedAt returns when this message entered the handler chain, or the zero
// time when the Timing middleware is not installed.
func (c *Context) ReceivedAt() time.Time {
	if v, ok := c.Get(receivedAtKey); ok {
		if t, ok := v.(time.Time); ok {
			return t
		}
	}
	return time.Time{}
}

// Timing records the message's arrival time so later handlers can report
// end-to-end latency. Install it first: `bot.Use(ilink.Timing())`.
func Timing() HandlerFunc {
	return func(c *Context) {
		c.Set(receivedAtKey, time.Now())
		c.Next()
	}
}

// SlashCommands returns middleware that intercepts messages beginning with "/"
// and handles them locally, so a control command never reaches the AI pipeline.
//
// Built-in commands:
//
//	/echo <text>     echo the text back, followed by channel latency figures
//	/toggle-debug    toggle per-user debug mode, reported by Context.DebugEnabled
//
// A matched command aborts the handler chain. Anything starting with "/" that
// matches no command falls through to the normal handlers.
func SlashCommands(opts ...SlashCommandOptions) HandlerFunc {
	var opt SlashCommandOptions
	if len(opts) > 0 {
		opt = opts[0]
	}
	state := &debugState{}

	return func(c *Context) {
		// Every message gets the debug state, not just commands: downstream
		// handlers ask Context.DebugEnabled on ordinary messages too.
		c.Set(debugStateKey, state)

		body := strings.TrimSpace(c.Body())
		if !strings.HasPrefix(body, "/") {
			c.Next()
			return
		}

		command, args := splitCommand(body)

		if handler, ok := opt.Commands[command]; ok {
			c.bot.cfg.logger.Info("slash command", "command", command, "user_id", c.UserID())
			if err := handler(c, args); err != nil {
				c.bot.cfg.logger.Error("slash command failed", "command", command, "error", err)
				_ = c.ReplyText("指令执行失败：" + err.Error())
			}
			c.Abort()
			return
		}

		if opt.DisableBuiltins {
			c.Next()
			return
		}

		switch command {
		case "/echo":
			c.bot.cfg.logger.Info("slash command", "command", command, "user_id", c.UserID())
			if err := handleEcho(c, args); err != nil {
				c.bot.cfg.logger.Error("slash command failed", "command", command, "error", err)
			}
			c.Abort()
		case "/toggle-debug":
			enabled := state.toggle(c.UserID())
			msg := "Debug 模式已关闭"
			if enabled {
				msg = "Debug 模式已开启"
			}
			if err := c.ReplyText(msg); err != nil {
				c.bot.cfg.logger.Error("slash command failed", "command", command, "error", err)
			}
			c.Abort()
		default:
			c.Next()
		}
	}
}

// debugStateKey holds the shared debug state in the per-request store.
const debugStateKey = "ilink.debug_state"

// DebugEnabled reports whether the sender has switched debug mode on via
// /toggle-debug. Always false when the SlashCommands middleware is absent.
func (c *Context) DebugEnabled() bool {
	v, ok := c.Get(debugStateKey)
	if !ok {
		return false
	}
	state, ok := v.(*debugState)
	if !ok {
		return false
	}
	return state.enabled(c.UserID())
}

// splitCommand separates the leading command word from its arguments.
func splitCommand(body string) (command, args string) {
	if i := strings.IndexAny(body, " \t\n"); i >= 0 {
		return strings.ToLower(body[:i]), strings.TrimSpace(body[i+1:])
	}
	return strings.ToLower(body), ""
}

// handleEcho replies with the argument, then a latency breakdown that separates
// platform delivery lag from local handling time.
func handleEcho(c *Context, args string) error {
	if args != "" {
		if err := c.ReplyText(args); err != nil {
			return err
		}
	}

	receivedAt := c.ReceivedAt()
	if receivedAt.IsZero() {
		receivedAt = time.Now()
	}

	var lines []string
	lines = append(lines, "⏱ 通道耗时")
	if c.Message.CreateTimeMs > 0 {
		eventTime := time.UnixMilli(c.Message.CreateTimeMs)
		lines = append(lines,
			fmt.Sprintf("├ 事件时间: %s", eventTime.Format(time.RFC3339)),
			fmt.Sprintf("├ 平台→SDK: %dms", receivedAt.Sub(eventTime).Milliseconds()),
		)
	} else {
		lines = append(lines, "├ 事件时间: N/A", "├ 平台→SDK: N/A")
	}
	lines = append(lines, fmt.Sprintf("└ SDK 处理: %dms", time.Since(receivedAt).Milliseconds()))

	return c.ReplyText(strings.Join(lines, "\n"))
}
