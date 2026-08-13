// AI bot 骨架：中间件、打字状态、按用户限流
//
// 只做「一问一答」。需要工具调用进度、run_id 归组的完整 agent 回合，
// 见 examples/ai_agent。
//
// 把 callAI() 换成真实的模型调用（Claude / OpenAI / 自建）。
//
// 运行：
//
//	go run ./examples/ai_bot
package main

import (
	"context"
	"errors"
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

	bot.Use(
		// 记录进入时间，供 /echo 和 debug 模式统计耗时。
		ilink.Timing(),

		// 全局日志。用 Body() 而不是 Text()，语音转写也能记下来。
		func(ctx *ilink.Context) {
			logger.Info("message received",
				"user_id", ctx.UserID(),
				"is_group", ctx.IsGroup(),
				"body", ctx.Body(),
			)
			ctx.Next()
		},

		// 按用户限流：每 2 秒最多一条。
		rateLimiter(2*time.Second),

		// 内置 /echo、/toggle-debug，外加自定义指令。
		// 命中指令会中止整条链，不会流到下面的 AI handler。
		ilink.SlashCommands(ilink.SlashCommandOptions{
			Commands: map[string]ilink.SlashCommandFunc{
				"/help": func(c *ilink.Context, _ string) error {
					return c.ReplyText("可用指令：\n" +
						"/help — 显示本说明\n" +
						"/ping — 连通性测试\n" +
						"/echo <文本> — 回显并显示通道耗时\n" +
						"/toggle-debug — 开关耗时统计\n" +
						"（其他内容）— AI 回复")
				},
				"/ping": func(c *ilink.Context, _ string) error {
					return c.ReplyText("pong 🏓")
				},
			},
		}),
	)

	// 非指令消息 → AI 回复。
	bot.OnBody(func(ctx *ilink.Context) {
		// 处理期间显示「对方正在输入」。
		_ = ctx.Typing()
		defer func() { _ = ctx.StopTyping() }()

		reply, err := callAI(ctx.Ctx, ctx.UserID(), ctx.Body())
		if err != nil {
			logger.Error("AI call failed", "error", err)
			_ = ctx.ReplyText("抱歉，出错了，请稍后再试。")
			return
		}
		if ctx.DebugEnabled() {
			reply += fmt.Sprintf("\n\n⏱ 耗时 %dms", time.Since(ctx.ReceivedAt()).Milliseconds())
		}
		_ = ctx.ReplyText(reply)
	})

	if err := bot.Login(context.Background(), ilink.TerminalQR); err != nil {
		log.Fatalf("login failed: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("AI bot started")
	if err := bot.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("bot stopped: %v", err)
	}
}

// callAI 是占位实现，换成你真实的模型调用。
func callAI(_ context.Context, userID, text string) (string, error) {
	return fmt.Sprintf("[AI 回复 %s]：%s", userID, text), nil
}

// rateLimiter 返回一个按用户限流的中间件。
func rateLimiter(interval time.Duration) ilink.HandlerFunc {
	var mu sync.Mutex
	lastSeen := make(map[string]time.Time)

	return func(ctx *ilink.Context) {
		mu.Lock()
		last, ok := lastSeen[ctx.UserID()]
		now := time.Now()
		if ok && now.Sub(last) < interval {
			mu.Unlock()
			_ = ctx.ReplyText("发得太快了，一条一条来。")
			ctx.Abort()
			return
		}
		lastSeen[ctx.UserID()] = now
		mu.Unlock()
		ctx.Next()
	}
}
