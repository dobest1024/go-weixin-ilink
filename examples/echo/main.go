// Echo bot：收什么回什么，SDK 最小可运行示例
//
// 运行：
//
//	go run ./examples/echo
package main

import (
	"context"
	"errors"
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

	// OnBody 覆盖文本 + 语音转写 + 引用消息，Body() 已经是渲染好的正文。
	// 只想要字面文本时才用 OnText / ctx.Text()。
	bot.OnBody(func(ctx *ilink.Context) {
		if err := ctx.ReplyText(ctx.Body()); err != nil {
			logger.Error("reply failed", "error", err)
		}
	})

	// 图片没有正文，OnBody 不会触发，单独接一下。
	bot.OnImage(func(ctx *ilink.Context) {
		if err := ctx.ReplyText("[收到你的图片]"); err != nil {
			logger.Error("reply failed", "error", err)
		}
	})

	// 登录：有可用凭证时直接复用，否则打印二维码。
	// 二维码过期会自动换新，服务端要配对码时会在终端提示输入。
	if err := bot.Login(context.Background(), ilink.TerminalQR); err != nil {
		log.Fatalf("login failed: %v", err)
	}

	// 收到 SIGINT/SIGTERM 时优雅退出，等在途消息处理完。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("bot started, waiting for messages...")
	if err := bot.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("bot stopped: %v", err)
	}
	logger.Info("bot stopped")
}
