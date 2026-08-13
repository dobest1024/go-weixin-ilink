// 中间件与出站 Hook：把横切逻辑从业务 handler 里剥出来
//
//   - ilink.Timing()          记录消息进入时间，供耗时统计
//   - ilink.SlashCommands()   内置 /echo、/toggle-debug，可注册自定义指令
//   - OnBeforeSend            改写或取消每一条外发消息（含主动推送、队列）
//   - OnAfterSend             观测投递结果，接监控
//   - 自定义中间件            鉴权、限流、审计
//
// 运行：
//
//	go run ./examples/middleware
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	ilink "github.com/dobest1024/go-weixin-ilink"
)

// 简单的投递计数器，OnAfterSend 里更新。
var (
	sentOK   atomic.Int64
	sentFail atomic.Int64
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	bot := ilink.NewBot(
		ilink.WithLogger(logger),
		ilink.WithTokenFile(".mw-token.json"),
		ilink.WithContextTokenDir(".mw-ctx"),
		ilink.WithSyncBufFile(".mw-syncbuf"),

		ilink.WithHooks(ilink.Hooks{
			// 出站拦截：所有外发消息都会经过这里，包括 handler 回复、
			// 主动推送、批量发送和发送队列。
			OnBeforeSend: func(msg *ilink.Message) error {
				for i := range msg.ItemList {
					it := msg.ItemList[i].TextItem
					if it == nil {
						continue
					}
					// 命中敏感词就整条丢弃。返回 ErrSendCanceled 表示
					// 「有意不发」，不会被当成故障。
					if containsSecret(it.Text) {
						logger.Warn("outbound blocked", "to", msg.ToUserID)
						return ilink.ErrSendCanceled
					}
					// 也可以就地改写内容。
					it.Text = strings.ReplaceAll(it.Text, "TODO", "待办")
				}
				return nil
			},

			// 投递结果观测，接 Prometheus / 日志告警。
			OnAfterSend: func(msg *ilink.Message, err error) {
				if err != nil {
					sentFail.Add(1)
					logger.Warn("send failed", "to", msg.ToUserID, "error", err)
					return
				}
				sentOK.Add(1)
			},

			OnSessionExpired: func() {
				// 此刻 SDK 已冻结全部 API 调用，主动推送会立即返回
				// *SessionPausedError，不会空转。
				logger.Error("token 失效，已冻结全部调用，1 小时后自动重试")
			},
			OnSessionRecovered: func() { logger.Info("会话已恢复") },
		}),
	)

	// ── 中间件链：注册顺序即执行顺序 ──────────────────────────────────────
	bot.Use(
		ilink.Timing(),           // 1. 记录进入时间，必须最先
		auditLog(logger),         // 2. 审计日志
		allowList("*"),           // 3. 鉴权（"*" 表示放行所有）
		rateLimit(2*time.Second), // 4. 限流
		ilink.SlashCommands(ilink.SlashCommandOptions{
			Commands: map[string]ilink.SlashCommandFunc{
				"/stats": func(c *ilink.Context, _ string) error {
					return c.ReplyText(fmt.Sprintf(
						"发送成功 %d 条，失败 %d 条", sentOK.Load(), sentFail.Load()))
				},
				"/whoami": func(c *ilink.Context, _ string) error {
					return c.ReplyText("你的 user_id：" + c.UserID())
				},
			},
		}),
	)

	// 走到这里的都是非指令消息。
	bot.OnBody(func(ctx *ilink.Context) {
		reply := "你说：" + ctx.Body()

		// /toggle-debug 打开后，回复追加耗时。
		if ctx.DebugEnabled() {
			reply += fmt.Sprintf("\n\n⏱ 处理耗时 %dms", time.Since(ctx.ReceivedAt()).Milliseconds())
		}
		_ = ctx.ReplyText(reply)
	})

	if err := bot.Login(context.Background(), ilink.TerminalQR); err != nil {
		log.Fatalf("login failed: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("middleware demo started",
		"commands", "/echo /toggle-debug /stats /whoami")
	if err := bot.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("bot stopped: %v", err)
	}
}

// ─── 自定义中间件 ─────────────────────────────────────────────────────────────

// auditLog 记录每条入站消息。用 Body() 而不是 Text()，语音转写也会被记下来。
func auditLog(logger *slog.Logger) ilink.HandlerFunc {
	return func(ctx *ilink.Context) {
		logger.Info("inbound",
			"user_id", ctx.UserID(),
			"group", ctx.IsGroup(),
			"body", ctx.Body(),
			"quoted", ctx.HasQuote(),
		)
		ctx.Next()
	}
}

// allowList 只放行白名单用户；"*" 放行所有。
//
// 也可以在构造时用 ilink.WithAllowFrom(...)，那样过滤发生在分发之前，
// 连中间件都不会跑到。放在中间件里的好处是能给被拒的用户一个回复。
func allowList(userIDs ...string) ilink.HandlerFunc {
	allowed := make(map[string]bool, len(userIDs))
	passAll := false
	for _, id := range userIDs {
		if id == "*" {
			passAll = true
		}
		allowed[id] = true
	}

	return func(ctx *ilink.Context) {
		if passAll || allowed[ctx.UserID()] {
			ctx.Next()
			return
		}
		_ = ctx.ReplyText("你还没有使用权限。")
		ctx.Abort() // 中止整条链，后面的中间件和 handler 都不会执行
	}
}

// rateLimit 按用户限流。
func rateLimit(interval time.Duration) ilink.HandlerFunc {
	var mu sync.Mutex
	lastSeen := make(map[string]time.Time)

	return func(ctx *ilink.Context) {
		now := time.Now()
		mu.Lock()
		last, seen := lastSeen[ctx.UserID()]
		if seen && now.Sub(last) < interval {
			mu.Unlock()
			_ = ctx.ReplyText("发得太快了，喘口气 🙂")
			ctx.Abort()
			return
		}
		lastSeen[ctx.UserID()] = now
		mu.Unlock()
		ctx.Next()
	}
}

func containsSecret(text string) bool {
	for _, word := range []string{"password", "api_key", "私钥"} {
		if strings.Contains(strings.ToLower(text), word) {
			return true
		}
	}
	return false
}
