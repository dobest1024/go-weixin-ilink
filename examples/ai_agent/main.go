// AI Agent bot：完整展示一轮 agent 运行怎么和微信客户端配合
//
//   - ctx.Body()      取正文：自动拼接引用上下文、语音回落到转写文本
//   - ctx.SetRunID()  给这一轮的所有消息打上同一个 run_id，客户端归成一个气泡
//   - ToolProgress    工具执行期间推送「正在调用 xxx」，避免长时间沉默
//   - ctx.Typing()    模型思考期间显示「对方正在输入」
//
// 把 fakeSearch / fakeWeather 换成你真实的工具，把 askModel 换成真实的模型调用。
//
// 运行：
//
//	go run ./examples/ai_agent
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
	"syscall"
	"time"

	ilink "github.com/dobest1024/go-weixin-ilink"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	bot := ilink.NewBot(
		ilink.WithLogger(logger),
		ilink.WithTokenFile(".agent-token.json"),
		ilink.WithContextTokenDir(".agent-ctx"),
		ilink.WithSyncBufFile(".agent-syncbuf"),
		// 多条消息并行处理：一个用户等工具返回时，别的用户不用排队。
		ilink.WithMaxWorkers(4),
		ilink.WithBotAgent("AgentDemo/1.0"),
	)

	bot.Use(ilink.Timing())

	// OnBody 而不是 OnText：语音消息带 ASR 转写时也要进 AI 管道。
	// OnText 只匹配字面文本 item，语音会被漏掉。
	bot.OnBody(func(ctx *ilink.Context) {
		if err := handleTurn(ctx, logger); err != nil {
			logger.Error("turn failed", "user_id", ctx.UserID(), "error", err)
			_ = ctx.ReplyText("处理出错了，请稍后再试。")
		}
	})

	// 非文本媒体单独提示（图片/文件等，本示例不做多模态）。
	bot.OnImage(func(ctx *ilink.Context) {
		_ = ctx.ReplyText("我暂时还看不懂图片，描述一下你想问什么？")
	})

	if err := bot.Login(context.Background(), ilink.TerminalQR); err != nil {
		log.Fatalf("login failed: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("agent bot started")
	if err := bot.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("bot stopped: %v", err)
	}
}

// handleTurn 跑完一轮 agent：规划 → 执行工具 → 生成回复。
func handleTurn(ctx *ilink.Context, logger *slog.Logger) error {
	question := ctx.Body()
	logger.Info("turn start", "user_id", ctx.UserID(), "question", question)

	// 给这一轮生成 run_id。之后 ctx 的所有回复（含工具进度）都会带上它，
	// 客户端据此把它们归到同一个回答气泡里。
	runID := fmt.Sprintf("run-%d", time.Now().UnixNano())
	ctx.SetRunID(runID)

	// 「对方正在输入」——和工具进度是两回事，前者表示模型在思考。
	_ = ctx.Typing()
	defer func() { _ = ctx.StopTyping() }()

	tp := ctx.ToolProgress()
	// Finalize 会等所有进度消息发完。必须在发最终回复之前调用，
	// 否则最终回复可能抢在进度消息前面到达。幂等，可以安全地 defer 兜底。
	defer tp.Finalize()

	var findings []string

	// Track 自动发 Start → 执行 → 按 error 发 completed / failed。
	for _, tool := range planTools(question) {
		var out string
		err := tp.Track(tool.name, tool.callID, func() error {
			var err error
			out, err = tool.run(ctx.Ctx, question)
			return err
		})
		if err != nil {
			// 单个工具失败不必中断整轮，把失败信息一并交给模型。
			logger.Warn("tool failed", "tool", tool.name, "error", err)
			findings = append(findings, fmt.Sprintf("%s 调用失败：%v", tool.name, err))
			continue
		}
		findings = append(findings, out)
	}

	answer, err := askModel(ctx.Ctx, question, findings)
	if err != nil {
		return fmt.Errorf("model call: %w", err)
	}

	// 先把进度消息排空，再发最终回复。
	tp.Finalize()

	if ctx.DebugEnabled() {
		answer += fmt.Sprintf("\n\n⏱ 本轮耗时 %dms", time.Since(ctx.ReceivedAt()).Milliseconds())
	}
	return ctx.ReplyText(answer)
}

// ─── 工具定义 ─────────────────────────────────────────────────────────────────

type tool struct {
	name   string
	callID string
	run    func(ctx context.Context, question string) (string, error)
}

// planTools 是「模型决定调用哪些工具」的占位实现。
// 真实场景里这一步由模型的 tool_use 输出驱动。
func planTools(question string) []tool {
	var tools []tool
	if strings.Contains(question, "天气") {
		tools = append(tools, tool{name: "get_weather", callID: "call-weather", run: fakeWeather})
	}
	// 兜底：总是搜一下。
	tools = append(tools, tool{name: "web_search", callID: "call-search", run: fakeSearch})
	return tools
}

func fakeSearch(ctx context.Context, question string) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(1500 * time.Millisecond): // 模拟耗时，用户此时会看到进度条
	}
	return "搜索结果：关于「" + question + "」的三条摘要……", nil
}

func fakeWeather(ctx context.Context, _ string) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(800 * time.Millisecond):
	}
	return "天气：晴，24℃", nil
}

// askModel 换成你真实的模型调用（Claude / OpenAI / 自建）。
func askModel(_ context.Context, question string, findings []string) (string, error) {
	if len(findings) == 0 {
		return "我没查到相关信息。", nil
	}
	return fmt.Sprintf("关于「%s」：\n\n%s", question, strings.Join(findings, "\n")), nil
}
