# weixin-ilink-sdk

微信 iLink 协议的 Go SDK，让对接微信 Bot 变得简单。协议层对齐官方
`openclaw-weixin` 插件 2.4.6。

```go
bot := ilink.NewBot(
    ilink.WithTokenFile(".bot-token.json"),
    ilink.WithContextTokenDir(".bot-ctx"),
    ilink.WithSyncBufFile(".bot-syncbuf"),
)

bot.OnBody(func(ctx *ilink.Context) {
    ctx.Typing()
    ctx.ReplyText("你说：" + ctx.Body())
})

bot.Login(context.Background(), ilink.TerminalQR)
bot.Run(context.Background())
```

## 安装

```bash
go get github.com/dobest1024/go-weixin-ilink
```

需要 Go 1.21+，**零外部依赖**。

## 快速开始

```go
package main

import (
    "context"
    "log"
    "os"
    "os/signal"
    "syscall"

    ilink "github.com/dobest1024/go-weixin-ilink"
)

func main() {
    bot := ilink.NewBot(
        ilink.WithTokenFile(".bot-token.json"),
        ilink.WithContextTokenDir(".bot-ctx"),
        ilink.WithSyncBufFile(".bot-syncbuf"), // 重启后从断点继续，不重放历史消息
    )

    bot.OnText(func(ctx *ilink.Context) {
        ctx.ReplyText("收到：" + ctx.Text())
    })

    if err := bot.Login(context.Background(), ilink.TerminalQR); err != nil {
        log.Fatal(err)
    }

    c, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    defer stop()

    log.Fatal(bot.Run(c))
}
```

运行后终端会渲染二维码，扫码登录。之后重启程序会自动复用已保存的 token。

---

## 特性

- **路由调度**：`OnText / OnImage / OnVoice / OnFile / OnVideo / OnTextMatch` 等，告别 `if/else`
- **中间件链**：`ctx.Next()` / `ctx.Abort()` 支持认证、限流、日志等横切关注点
- **并发处理**：`WithMaxWorkers(n)` 开启 worker pool，多条消息并行处理
- **生命周期 Hook**：`OnLogin / OnSessionExpired / OnBotStop / OnHandlerPanic` 等回调
- **异步 QR 登录**：`LoginAsync()` 返回 `QRSession`，适合 Web 平台集成
- **多 Bot 管理**：`BotManager` 统一管理多个 Bot 实例的创建/启动/停止
- **批量推送**：`BatchSendText()` 并发群发，`SendQueue` 带限速的发送队列
- **引用消息**：`ctx.HasQuote()` / `ctx.QuotedText()` 读取用户长按回复的内容
- **消息正文渲染**：`ctx.Body()` 自动拼接引用上下文、回落到语音转写文本，直接喂给 AI
- **工具调用进度**：`ctx.ToolProgress()` 推送 "正在调用 xxx"，长工具调用不再是一片空白
- **出站 Hook**：`OnBeforeSend` 可改写/取消每一条外发消息，`OnAfterSend` 观测投递结果
- **斜杠指令**：`ilink.SlashCommands()` 中间件内置 `/echo`、`/toggle-debug`，可注册自定义指令
- **媒体收发**：CDN 文件上传/下载，AES-128-ECB 自动加解密，支持图片/语音/文件/视频/缩略图
- **打字状态**：`ctx.Typing()` / `ctx.StopTyping()`，typing_ticket 按用户缓存并带退避重试
- **断点续传**：`get_updates_buf` 写盘，重启后从上次位置继续拉消息，不重复处理历史
- **凭证持久化**：扫码登录后 token 写文件，重启免扫码
- **完整扫码状态机**：配对码校验、IDC 重定向、已绑定识别、二维码自动刷新
- **会话自愈**：token 失效（-14）后**冻结全部 API 调用**并暂停 1 小时重试，主动推送不再空转
- **网络错误分类**：`ClassifyNetError` 区分 DNS / TCP / TLS / 超时，日志自动脱敏
- **优雅关闭**：监听 context 取消，等待 in-flight handler 处理完毕
- **Panic 隔离**：单条消息崩溃不影响整体轮询

---

## 核心 API 速查

### 路由

| 方法 | 匹配 |
|---|---|
| `OnBody` | 任何有正文的消息（文本 + 引用 + 语音转写）— **AI 场景首选** |
| `OnText` | 仅字面文本 item |
| `OnImage` / `OnVoice` / `OnFile` / `OnVideo` | 对应媒体类型 |
| `OnGroup` / `OnPrivate` | 群聊 / 私聊 |
| `OnTextPrefix` / `OnTextContains` / `OnTextMatch` | 前缀 / 包含 / 正则 |
| `OnUserID` / `OnGroupID` | 指定用户 / 群 |
| `On(matcher, ...)` | 自定义 Matcher |
| `Use(...)` | 全局中间件 |

### Context

| 方法 | 说明 |
|---|---|
| `ctx.Body()` | 渲染后的正文：拼引用上下文 + 语音回落转写 |
| `ctx.Text()` | 仅字面文本 |
| `ctx.ReplyText(s)` / `ctx.ReplyItems(items)` | 回复 |
| `ctx.Typing()` / `ctx.StopTyping()` | 打字状态 |
| `ctx.ToolProgress()` | 工具调用进度推送器 |
| `ctx.SetRunID(id)` / `ctx.RunID()` | 本轮 agent 运行的分组 ID |
| `ctx.Upload(data, type)` / `ctx.UploadWithOptions(...)` | 上传媒体（后者支持缩略图） |
| `ctx.DownloadImage/Voice/File/Media(...)` | 下载并解密 |
| `ctx.Next()` / `ctx.Abort()` | 中间件控制流 |
| `ctx.Bot()` | 拿到 Bot 本体 |

### 中间件

| 名称 | 作用 |
|---|---|
| `ilink.Timing()` | 记录消息进入时间，供耗时统计 |
| `ilink.SlashCommands(opts)` | 内置 `/echo`、`/toggle-debug`，可注册自定义指令 |

---

## 文档

| 文档 | 内容 |
|---|---|
| [配置](docs/configuration.md) | 构造选项、并发、存储接口 |
| [路由与中间件](docs/routing.md) | 路由注册、中间件链、Context、斜杠指令 |
| [媒体收发](docs/media.md) | 上传下载、语音转写与转码、缩略图、批量推送 |
| [登录](docs/login.md) | 扫码状态机、配对码、异步登录、多 Bot 管理 |
| [AI Agent 集成](docs/ai-agent.md) | 工具调用进度、run_id、出站与生命周期 Hook |
| [运维](docs/operations.md) | 错误处理、网络诊断、日志脱敏 |

---

## 示例

`examples/` 下每个目录都可以直接 `go run` 跑起来：

| 示例 | 演示内容 |
|---|---|
| [`echo`](examples/echo) | 最小可运行 Bot，收什么回什么 |
| [`ai_bot`](examples/ai_bot) | 一问一答：中间件、限流、打字状态、斜杠指令 |
| [`ai_agent`](examples/ai_agent) | 完整 agent 回合：工具调用进度 + run_id 归组 |
| [`mirror`](examples/mirror) | 媒体收发全流程：下载解密 → 重新上传 → 发回 |
| [`middleware`](examples/middleware) | 鉴权 / 限流 / 审计中间件 + 出站 Hook 内容过滤 |
| [`webapp`](examples/webapp) | Web 扫码登录：二维码渲染 + 配对码走表单 |
| [`platform`](examples/platform) | 多 Bot 管理、异步登录、批量推送、限速队列 |

```bash
go run ./examples/echo
```

---

## 项目结构

```
go-weixin-ilink/
├── bot.go          # Bot 主入口，路由注册，生命周期管理
├── context.go      # Context + 中间件链 + 回复/媒体辅助
├── dispatcher.go   # Matcher 接口 + 路由调度
├── middleware.go   # Timing / SlashCommands 内置中间件
├── message.go      # 协议类型（Message, MessageItem, ToolCall* 等）
├── auth.go         # 扫码登录状态机 + 凭证复用
├── auth_async.go   # 异步登录（LoginAsync / QRSession）
├── qrstatus.go     # 扫码协议：取码、状态轮询、配对码
├── poller.go       # 长轮询循环（断点续传、token 自愈、并发处理）
├── sender.go       # 底层发送 + 出站 Hook + 工具调用 item 构造
├── sendqueue.go    # 批量发送 + 限速发送队列
├── toolprogress.go # 工具调用进度推送（有序、非阻塞）
├── hooks.go        # 生命周期与出站 Hook 定义
├── manager.go      # 多 Bot 管理器（BotManager）
├── typing.go       # 打字状态 + per-user 配置缓存（带退避）
├── media.go        # CDN 上传/下载（含缩略图）+ Build* 辅助函数
├── transcode.go    # VoiceTranscoder 接口 + PCMToWAV
├── mime.go         # MIME 推断（扩展名 / Content-Type / magic bytes）
├── storage.go      # TokenStore / ContextTokenStore / SyncBufStore
├── sessionguard.go # token 失效后的全局调用冻结
├── neterr.go       # 网络错误分类（dns / tcp / tls / timeout）
├── redact.go       # 日志脱敏
├── errors.go       # 错误类型与哨兵值
├── crypto.go       # AES-128-ECB + PKCS7（内部使用）
├── client.go       # HTTP 客户端（内部使用）
├── options.go      # 函数式选项
├── qr.go           # TerminalQR 辅助
├── docs/           # 详细文档
└── examples/       # 7 个可运行示例
```

---

## 版本

遵循[语义化版本](https://semver.org/lang/zh-CN/)。`v0.x` 阶段 API 仍可能调整，
破坏性变更会在 minor 版本发布并记录在 [CHANGELOG](CHANGELOG.md)。

```bash
go get github.com/dobest1024/go-weixin-ilink@latest
```

---

## License

[MIT](LICENSE) © 2026 Gundy
