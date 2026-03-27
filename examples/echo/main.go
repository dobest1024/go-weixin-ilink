// Echo bot: replies with the same text the user sends.
package main

import (
	"context"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	ilink "github.com/dobest1024/go-weixin-ilink"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))

	bot := ilink.NewBot(
		ilink.WithLogger(logger),
		ilink.WithTokenFile(".echo-bot-token.json"),
		ilink.WithContextTokenDir(".echo-bot-ctx"),
	)

	// Echo text messages back.
	bot.OnText(func(ctx *ilink.Context) {
		if err := ctx.ReplyText(ctx.Text()); err != nil {
			logger.Error("reply failed", "error", err)
		}
	})

	// Acknowledge image messages.
	bot.OnImage(func(ctx *ilink.Context) {
		if err := ctx.ReplyText("[received your image]"); err != nil {
			logger.Error("reply failed", "error", err)
		}
	})

	// Acknowledge voice messages (show ASR transcript if available).
	bot.OnVoice(func(ctx *ilink.Context) {
		voice := ctx.Message.GetVoiceItem()
		reply := "[received your voice message]"
		if voice != nil && voice.Text != "" {
			reply = "You said: " + voice.Text
		}
		if err := ctx.ReplyText(reply); err != nil {
			logger.Error("reply failed", "error", err)
		}
	})

	// Login (reuses stored credentials if available).
	loginCtx, loginCancel := context.WithCancel(context.Background())
	defer loginCancel()

	if err := bot.Login(loginCtx, ilink.TerminalQR); err != nil {
		log.Fatalf("login failed: %v", err)
	}

	// Graceful shutdown on SIGINT/SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("bot started, waiting for messages...")
	if err := bot.Run(ctx); err != nil && err != context.Canceled {
		log.Fatalf("bot stopped: %v", err)
	}
	logger.Info("bot stopped")
}
