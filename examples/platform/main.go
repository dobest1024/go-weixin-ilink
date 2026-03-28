// Platform bot：展示 SDK 全部进阶能力
//
// - 多 Bot 管理器：同时管理多个 Bot 实例
// - 异步 QR 登录：非阻塞获取二维码 + 轮询状态
// - 并发消息处理：worker pool 并行处理消息
// - 生命周期 Hook：感知连接状态变化
// - 批量推送 + 发送队列：群发和限速推送
//
// 运行：
//
//	go run ./examples/platform
package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	ilink "github.com/dobest1024/go-weixin-ilink"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// ── 1. 多 Bot 管理器 ─────────────────────────────────────────────────────
	manager := ilink.NewBotManager()

	// 创建第一个 Bot，展示全部进阶配置
	bot, err := manager.Add("bot_001",
		ilink.WithLogger(logger.With("bot", "bot_001")),
		ilink.WithTokenFile(".platform-bot001-token.json"),
		ilink.WithContextTokenDir(".platform-bot001-ctx"),
		ilink.WithSyncBufFile(".platform-bot001-syncbuf"),

		// ── 2. 并发处理：最多 5 条消息并行 ──────────────────────────────
		ilink.WithMaxWorkers(5),

		// ── 3. 生命周期 Hook ────────────────────────────────────────────
		ilink.WithHooks(ilink.Hooks{
			OnLogin: func() {
				logger.Info("✅ [Hook] 登录成功")
			},
			OnSessionExpired: func() {
				logger.Warn("⚠️ [Hook] 会话过期，自动暂停 1 小时后重试")
				// 实际场景：通知管理员、更新数据库状态
			},
			OnSessionRecovered: func() {
				logger.Info("✅ [Hook] 会话已恢复")
			},
			OnBotStop: func(err error) {
				logger.Info("🛑 [Hook] Bot 已停止", "error", err)
			},
			OnError: func(err error) {
				logger.Warn("❌ [Hook] 轮询错误", "error", err)
			},
			OnHandlerPanic: func(recovered any, msg *ilink.Message) {
				logger.Error("💥 [Hook] handler panic",
					"panic", recovered, "from", msg.FromUserID)
			},
		}),
	)
	if err != nil {
		log.Fatalf("创建 bot 失败: %v", err)
	}

	// ── 订阅者列表（用于批量推送演示）────────────────────────────────────────
	var subMu sync.RWMutex
	subscribers := make(map[string]bool)

	// ── 注册消息路由 ─────────────────────────────────────────────────────────

	// 全局中间件：日志
	bot.Use(func(c *ilink.Context) {
		logger.Info("收到消息",
			"from", c.UserID(),
			"text", c.Text(),
			"is_group", c.IsGroup(),
		)
		c.Next()
	})

	// /subscribe — 订阅推送
	bot.OnTextPrefix("/subscribe", func(c *ilink.Context) {
		subMu.Lock()
		subscribers[c.UserID()] = true
		subMu.Unlock()
		c.ReplyText("已订阅推送通知，发 /unsubscribe 取消")
	})

	// /unsubscribe — 取消订阅
	bot.OnTextPrefix("/unsubscribe", func(c *ilink.Context) {
		subMu.Lock()
		delete(subscribers, c.UserID())
		subMu.Unlock()
		c.ReplyText("已取消订阅")
	})

	// /push <内容> — 管理员手动触发批量推送
	bot.OnTextPrefix("/push ", func(c *ilink.Context) {
		text := strings.TrimPrefix(c.Text(), "/push ")
		if text == "" {
			c.ReplyText("用法：/push <推送内容>")
			return
		}

		subMu.RLock()
		userIDs := make([]string, 0, len(subscribers))
		for uid := range subscribers {
			userIDs = append(userIDs, uid)
		}
		subMu.RUnlock()

		if len(userIDs) == 0 {
			c.ReplyText("当前没有订阅者")
			return
		}

		// ── 5. 批量推送 ─────────────────────────────────────────────────
		c.Typing()
		results := bot.BatchSendText(c.Ctx, userIDs, fmt.Sprintf("📢 推送通知\n\n%s", text))

		success, fail := 0, 0
		for _, r := range results {
			if r.Err != nil {
				logger.Warn("推送失败", "user", r.UserID, "error", r.Err)
				fail++
			} else {
				success++
			}
		}
		c.ReplyText(fmt.Sprintf("推送完成：成功 %d，失败 %d", success, fail))
	})

	// /status — 查看管理器状态
	bot.OnTextPrefix("/status", func(c *ilink.Context) {
		var sb strings.Builder
		sb.WriteString("📊 平台状态\n\n")
		for _, info := range manager.List() {
			sb.WriteString(fmt.Sprintf("Bot: %s  状态: %s", info.ID, info.Status))
			if info.Err != nil {
				sb.WriteString(fmt.Sprintf("  错误: %v", info.Err))
			}
			sb.WriteString("\n")
		}
		subMu.RLock()
		sb.WriteString(fmt.Sprintf("\n订阅者: %d 人", len(subscribers)))
		subMu.RUnlock()
		c.ReplyText(sb.String())
	})

	// /help — 帮助
	bot.OnTextPrefix("/help", func(c *ilink.Context) {
		c.ReplyText(`📖 Platform Bot 命令

/subscribe    — 订阅推送通知
/unsubscribe  — 取消订阅
/push <内容>  — 向所有订阅者推送消息
/status       — 查看平台状态
/queue <内容> — 通过限速队列推送（不超过 API 限制）
/help         — 显示此帮助`)
	})

	// 引用消息处理
	bot.OnText(func(c *ilink.Context) {
		if c.HasQuote() {
			c.ReplyText(fmt.Sprintf("你引用了：%s\n你说：%s", c.QuotedText(), c.Text()))
			return
		}
		// 默认回显
		c.ReplyText("你说：" + c.Text())
	})

	// ── 4. 异步 QR 登录 ─────────────────────────────────────────────────────
	logger.Info("开始异步登录...")
	session, err := bot.LoginAsync(ctx)
	if err != nil {
		log.Fatalf("获取二维码失败: %v", err)
	}

	// 模拟 Web 平台场景：轮询登录状态
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		lastStatus := ilink.LoginStatusPending
		for {
			select {
			case <-session.Done():
				return
			case <-ticker.C:
				s := session.Status()
				if s != lastStatus {
					logger.Info("登录状态变更", "status", s)
					lastStatus = s
				}
			}
		}
	}()

	// 在终端也渲染二维码方便本地测试
	if qr := session.QRImage(); qr != "" {
		ilink.TerminalQR(qr)
	}

	// 等待登录完成
	if err := session.Wait(ctx); err != nil {
		log.Fatalf("登录失败: %v", err)
	}
	logger.Info("登录完成，启动 Bot")

	// ── 5. 发送队列（限速推送）──────────────────────────────────────────────
	queue := ilink.NewSendQueue(bot, 200*time.Millisecond, 500)
	go queue.Start(ctx)
	defer queue.Stop()

	// /queue <内容> — 通过限速队列推送
	bot.OnTextPrefix("/queue ", func(c *ilink.Context) {
		text := strings.TrimPrefix(c.Text(), "/queue ")
		if text == "" {
			c.ReplyText("用法：/queue <推送内容>")
			return
		}

		subMu.RLock()
		count := 0
		for uid := range subscribers {
			queue.EnqueueText(uid, fmt.Sprintf("📢 [队列推送]\n\n%s", text))
			count++
		}
		subMu.RUnlock()

		if count == 0 {
			c.ReplyText("当前没有订阅者")
		} else {
			c.ReplyText(fmt.Sprintf("已加入发送队列：%d 条，排队中：%d", count, queue.Pending()))
		}
	})

	// ── 启动 Bot ─────────────────────────────────────────────────────────────
	if err := manager.Start(ctx, "bot_001"); err != nil {
		log.Fatalf("启动 bot 失败: %v", err)
	}

	logger.Info("Platform bot 已启动",
		"commands", "/help /subscribe /push /queue /status",
	)

	// 等待退出信号
	<-ctx.Done()
	logger.Info("收到退出信号，停止所有 Bot...")
	manager.StopAll()
	logger.Info("全部停止")
}
