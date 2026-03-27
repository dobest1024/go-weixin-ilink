// AI bot skeleton: shows middleware, typing indicators, and per-user rate limiting.
// Replace callAI() with your actual AI API call (e.g. Claude, OpenAI).
package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	ilink "github.com/dobest1024/go-weixin-ilink"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	bot := ilink.NewBot(
		ilink.WithLogger(logger),
		ilink.WithTokenFile(".ai-bot-token.json"),
		ilink.WithContextTokenDir(".ai-bot-ctx"),
	)

	// Global middleware: log every message.
	bot.Use(func(ctx *ilink.Context) {
		logger.Info("message received",
			"user_id", ctx.UserID(),
			"is_group", ctx.IsGroup(),
			"text", ctx.Text(),
		)
		ctx.Next()
	})

	// Rate limiter middleware (max 1 message per 2 seconds per user).
	bot.Use(rateLimiter(2 * time.Second))

	// /help command.
	bot.OnTextPrefix("/help", func(ctx *ilink.Context) {
		_ = ctx.ReplyText("Commands:\n/help — show this message\n/ping — test connectivity\n(anything else) — AI reply")
	})

	// /ping command.
	bot.OnTextPrefix("/ping", func(ctx *ilink.Context) {
		_ = ctx.ReplyText("pong 🏓")
	})

	// All other text → AI reply.
	bot.OnText(func(ctx *ilink.Context) {
		// Show typing indicator while processing.
		_ = ctx.Typing()
		defer func() { _ = ctx.StopTyping() }()

		reply, err := callAI(ctx.Ctx, ctx.UserID(), ctx.Text())
		if err != nil {
			logger.Error("AI call failed", "error", err)
			_ = ctx.ReplyText("Sorry, I encountered an error. Please try again.")
			return
		}
		_ = ctx.ReplyText(reply)
	})

	// Login.
	loginCtx, loginCancel := context.WithCancel(context.Background())
	defer loginCancel()

	if err := bot.Login(loginCtx, ilink.TerminalQR); err != nil {
		log.Fatalf("login failed: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("AI bot started")
	if err := bot.Run(ctx); err != nil && err != context.Canceled {
		log.Fatalf("bot stopped: %v", err)
	}
}

// callAI is a placeholder — replace with your actual AI API integration.
func callAI(_ context.Context, userID, text string) (string, error) {
	// Example: return an echo reply with the user ID.
	return fmt.Sprintf("[AI response for %s]: %s", userID, text), nil
}

// rateLimiter returns a middleware that throttles messages per user.
func rateLimiter(interval time.Duration) ilink.HandlerFunc {
	var mu sync.Mutex
	lastSeen := make(map[string]time.Time)

	return func(ctx *ilink.Context) {
		mu.Lock()
		last, ok := lastSeen[ctx.UserID()]
		now := time.Now()
		if ok && now.Sub(last) < interval {
			mu.Unlock()
			_ = ctx.ReplyText("Please slow down — one message at a time.")
			ctx.Abort()
			return
		}
		lastSeen[ctx.UserID()] = now
		mu.Unlock()
		ctx.Next()
	}
}
