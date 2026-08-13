# weixin-ilink-sdk

微信 iLink 协议的 Go SDK，让对接微信 Bot 变得简单。

```go
bot := ilink.NewBot(
    ilink.WithTokenFile(".bot-token.json"),
    ilink.WithContextTokenDir(".bot-ctx"),
    ilink.WithSyncBufFile(".bot-syncbuf"),
)

bot.OnTextPrefix("/help", func(ctx *ilink.Context) {
    ctx.ReplyText("发送任意消息即可得到回复")
})

bot.OnText(func(ctx *ilink.Context) {
    ctx.Typing()
    ctx.ReplyText("你说：" + ctx.Text())
})

bot.Login(context.Background(), ilink.TerminalQR)
bot.Run(context.Background())
```

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

## 安装

```bash
go get github.com/dobest1024/go-weixin-ilink
```

需要 Go 1.21+，SDK 零外部依赖。

## 目录

- [快速开始](#快速开始)
- [Bot 配置](#bot-配置)
- [消息路由](#消息路由)
- [中间件](#中间件)
- [消息上下文](#消息上下文)
- [回复与发送](#回复与发送)
- [媒体文件](#媒体文件)
- [主动发送消息](#主动发送消息)
- [并发处理](#并发处理)
- [生命周期 Hook](#生命周期-hook)
- [异步 QR 登录](#异步-qr-登录)
- [登录状态机与配对码](#登录状态机与配对码)
- [工具调用进度](#工具调用进度)
- [出站 Hook](#出站-hook)
- [斜杠指令与调试](#斜杠指令与调试)
- [语音转写与转码](#语音转写与转码)
- [多 Bot 管理](#多-bot-管理)
- [批量推送与发送队列](#批量推送与发送队列)
- [存储接口](#存储接口)
- [错误处理](#错误处理)
- [可观测性](#可观测性)
- [完整示例](#完整示例)

---

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

## Bot 配置

通过函数式选项配置 Bot：

```go
bot := ilink.NewBot(
    ilink.WithTokenFile(".my-bot-token.json"),    // bot_token 持久化路径，默认 .ilink-token.json
    ilink.WithContextTokenDir(".my-bot-ctx"),     // 用户 context_token 存储目录，默认 .ilink-context-tokens
    ilink.WithSyncBufFile(".my-bot-syncbuf"),     // get_updates_buf 游标持久化，重启后断点续传
    ilink.WithLogger(myLogger),                   // *slog.Logger，默认 slog.Default()
    ilink.WithHTTPClient(myHTTPClient),           // 自定义 HTTP Client（不要设置 Timeout）
    ilink.WithBaseURL("https://ilinkai.weixin.qq.com"),              // API 地址，一般无需修改
    ilink.WithCDNBaseURL("https://novac2c.cdn.weixin.qq.com/c2c"),   // 媒体 CDN 地址
    ilink.WithChannelVersion("2.4.6"),            // 协议版本，默认对齐官方插件
    ilink.WithAppID("bot"),                       // iLink-App-Id 头，默认 "bot"
    ilink.WithBotAgent("MyBot/1.0"),              // UA 风格自述标识，随每个请求上报
    ilink.WithSKRouteTag("gray"),                 // 可选路由提示头
    ilink.WithMaxWorkers(8),                      // 并发处理消息，默认 0（串行）
    ilink.WithAllowFrom("user-a", "user-b"),      // 白名单，只处理这些用户的消息
)
```

登录与媒体相关：

```go
bot := ilink.NewBot(
    ilink.WithBotType("3"),                       // 二维码类型，默认 "3"
    ilink.WithVerifyCodeFunc(myPairCodePrompt),   // 配对码输入，默认从 stdin 读
    ilink.WithLocalTokens(myTokenPool),           // 上报本地已有 token，识别已绑定的 bot
    ilink.WithVoiceTranscoder(mySilkDecoder),     // SILK → WAV 转码器
)
```

> **注意**：不要在自定义 `http.Client` 上设置 `Timeout`，长轮询连接需要保持 35 秒以上。
> SDK 内部已通过 `context.WithTimeout` 控制每次请求超时。

---

## 消息路由

所有路由方法均可接受多个 handler，按注册顺序组成处理链。

### 按消息类型

```go
bot.OnText(handler)    // 文本消息
bot.OnImage(handler)   // 图片消息
bot.OnVoice(handler)   // 语音消息
bot.OnFile(handler)    // 文件消息
bot.OnVideo(handler)   // 视频消息
```

### 按来源

```go
bot.OnGroup(handler)        // 所有群消息（任意类型）
bot.OnPrivate(handler)      // 所有私聊消息
bot.OnGroupText(handler)    // 群内文本
bot.OnPrivateText(handler)  // 私聊文本
```

### 按内容匹配

```go
bot.OnTextPrefix("/help", handler)               // 文本以 /help 开头
bot.OnTextContains("关键词", handler)             // 文本包含指定字符串
bot.OnTextMatch(`^\d{4}-\d{2}-\d{2}`, handler)  // 正则匹配（传入无效正则会 panic）
```

### 按用户/群组

```go
bot.OnUserID("user_abc123", handler)  // 指定用户
bot.OnGroupID("group_xyz", handler)   // 指定群组
```

### 自定义 Matcher

```go
bot.On(func(msg *ilink.Message) bool {
    return msg.IsText() && len(msg.Text()) > 100
}, handler)
```

### 路由匹配规则

- 所有匹配的路由都会执行（不是只执行第一个）
- 按注册顺序执行
- 全局中间件（`Use`）始终在路由 handler 之前执行
- 任意 handler 内调用 `ctx.Abort()` 可终止后续 handler

---

## 中间件

`bot.Use()` 注册的 handler 对所有消息生效，在路由 handler 之前运行。

```go
// 日志中间件
bot.Use(func(ctx *ilink.Context) {
    log.Printf("收到消息 from=%s text=%q", ctx.UserID(), ctx.Text())
    ctx.Next() // 必须调用 Next，否则后续 handler 不会执行
})

// 认证中间件：仅允许白名单用户
allowed := map[string]bool{"user_admin": true}
bot.Use(func(ctx *ilink.Context) {
    if !allowed[ctx.UserID()] {
        ctx.ReplyText("无权限")
        ctx.Abort()
        return
    }
    ctx.Next()
})
```

中间件支持洋葱模型，`ctx.Next()` 返回后可做后处理：

```go
bot.Use(func(ctx *ilink.Context) {
    start := time.Now()
    ctx.Next()
    log.Printf("处理耗时 %v", time.Since(start))
})
```

---

## 消息上下文

`*ilink.Context` 是 handler 的唯一参数，包含消息数据和处理辅助方法。

### 访问消息内容

```go
ctx.Text()       // 字面文本 item 的内容，非文本消息返回 ""
ctx.Body()       // 渲染后的正文：拼接引用上下文 + 回落到语音转写，喂 AI 用这个
ctx.UserID()     // 发送者 user ID
ctx.IsGroup()    // 是否群消息
ctx.IsPrivate()  // 是否私聊
ctx.RunID()      // 本轮 agent 运行的 run_id
ctx.Message      // 原始 *ilink.Message，含完整协议字段

// 引用/回复消息（用户长按消息引用时）
ctx.HasQuote()      // 是否携带引用
ctx.QuotedText()    // 被引用消息的文字内容（或摘要）

// 获取媒体 item
img   := ctx.Message.GetImageItem()  // *ilink.ImageItem 或 nil
voice := ctx.Message.GetVoiceItem()  // *ilink.VoiceItem 或 nil
file  := ctx.Message.GetFileItem()   // *ilink.FileItem 或 nil
video := ctx.Message.GetVideoItem()  // *ilink.VideoItem 或 nil
```

`Message` 完整字段（来自协议）：

```go
msg := ctx.Message
msg.Seq           // 序列号
msg.MessageID     // 服务端消息 ID
msg.CreateTimeMs  // 消息创建时间（毫秒时间戳）
msg.SessionID     // 会话 ID
msg.GroupID       // 群组 ID（私聊为空）
msg.ContextToken  // 当前用户的 context_token
msg.RunID         // 一轮 agent 运行的分组 ID
```

`Text()` 和 `Body()` 的区别：

| 消息 | `Text()` | `Body()` |
|---|---|---|
| 纯文本「你好」 | `你好` | `你好` |
| 引用「服务器 500 了」并问「怎么修」 | `怎么修` | `[引用: 张三 \| 服务器 500 了]\n怎么修` |
| 语音（服务端转写为「查天气」） | `""` | `查天气` |

对应的路由：`OnText` 只匹配前两种，`OnBody` 三种都匹配。

### 请求级别 KV 存储

在中间件和 handler 之间传递数据：

```go
bot.Use(func(ctx *ilink.Context) {
    ctx.Set("user", loadUserFromDB(ctx.UserID()))
    ctx.Next()
})

bot.OnText(func(ctx *ilink.Context) {
    user := ctx.MustGet("user").(*User)
    ctx.ReplyText("你好，" + user.Name)
})
```

### 控制流

```go
ctx.Next()       // 执行链中下一个 handler
ctx.Abort()      // 终止后续 handler（当前 handler 正常返回）
ctx.IsAborted()  // 检查是否已终止
```

---

## 回复与发送

### 在 handler 内回复

```go
// 文本回复
ctx.ReplyText("你好")

// 发送媒体（上传后构建 item）
data, _ := os.ReadFile("photo.jpg")
result, _ := ctx.Upload(data, "image") // fileType: image|voice|file|video
ctx.ReplyItems([]ilink.MessageItem{ilink.BuildImageItem(result)})

// 打字状态
ctx.Typing()
defer ctx.StopTyping()
// ... 处理耗时任务 ...
ctx.ReplyText(reply)
```

### 构建媒体消息 item

```go
ilink.BuildImageItem(result)                  // 图片
ilink.BuildVoiceItem(result, 3000)            // 语音，时长 3000ms
ilink.BuildVoiceItemFrom(result, original)    // 语音，保留原始编码参数（转发时使用）
ilink.BuildFileItem(result, "report.pdf")     // 文件（需传文件名）
ilink.BuildVideoItem(result, 1280, 720, 5000) // 视频，宽×高×时长(ms)
```

---

## 媒体文件

### 下载

```go
// 下载图片（入站 ImageItem 的 AES key 是 hex 字符串）
data, err := ctx.DownloadImage(ctx.Message.GetImageItem())

// 下载语音/文件/视频（入站 CDNMedia 的 AES key 是 base64 编码）
data, err := ctx.DownloadMedia(ctx.Message.GetVoiceItem().Media)
data, err := ctx.DownloadMedia(ctx.Message.GetFileItem().Media)
data, err := ctx.DownloadMedia(ctx.Message.GetVideoItem().Media)
```

### 上传

```go
// 在 handler 内用 ctx.Upload（自动填充 toUserID）
result, err := ctx.Upload(data, "image") // fileType: image|voice|file|video

// 在 handler 外用 bot.Upload（需手动传 toUserID）
result, err := bot.Upload(ctx, data, userID, "file")
```

### 镜像转发示例（下载后重新上传发回）

```go
bot.OnImage(func(ctx *ilink.Context) {
    img := ctx.Message.GetImageItem()
    data, err := ctx.DownloadImage(img)
    if err != nil { return }

    result, err := ctx.Upload(data, "image")
    if err != nil { return }

    ctx.ReplyItems([]ilink.MessageItem{ilink.BuildImageItem(result)})
})
```

---

## 主动发送消息

向用户主动发送消息需要先有该用户的 `context_token`。
SDK 会在每条入站消息处理时自动持久化 `context_token`，只要用户曾发过消息即可主动发送。

```go
// 文本
err := bot.SendText(ctx, "user_abc123", "你好！")

// 媒体（先 Upload，再 Send*）
result, _ := bot.Upload(ctx, data, userID, "image")
bot.SendImage(ctx, userID, ilink.BuildImageItem(result).ImageItem)

// 其他类型
bot.SendVoice(ctx, userID, voiceItem)
bot.SendFile(ctx, userID, fileItem)
bot.SendVideo(ctx, userID, videoItem)
```

若找不到该用户的 `context_token`，返回 `ilink.ErrNoContextToken`。

---

## 并发处理

默认消息串行处理。设置 `WithMaxWorkers(n)` 后，SDK 使用 worker pool 并行处理：

```go
bot := ilink.NewBot(
    ilink.WithMaxWorkers(10), // 最多 10 个消息并发处理
)
```

> **注意**：并发模式下 handler 必须是并发安全的（避免共享可变状态或加锁）。

---

## 生命周期 Hook

通过 `WithHooks()` 注册回调，让上层应用感知连接状态变化：

```go
bot := ilink.NewBot(
    ilink.WithHooks(ilink.Hooks{
        OnLogin: func() {
            log.Println("登录成功")
        },
        OnSessionExpired: func() {
            log.Println("会话过期，将自动暂停 1 小时后重试")
            // 通知管理员、更新平台状态等
        },
        OnSessionRecovered: func() {
            log.Println("会话已恢复")
        },
        OnBotStop: func(err error) {
            log.Printf("Bot 已停止: %v", err)
        },
        OnError: func(err error) {
            log.Printf("轮询错误: %v", err)
        },
        OnHandlerPanic: func(recovered any, msg *ilink.Message) {
            log.Printf("handler panic: %v, from: %s", recovered, msg.FromUserID)
        },

        // 出站拦截，详见「出站 Hook」章节
        OnBeforeSend: func(msg *ilink.Message) error { return nil },
        OnAfterSend:  func(msg *ilink.Message, err error) {},
    }),
)
```

所有 Hook 都是可选的，未设置的不会被调用。

> `OnSessionExpired` 触发时，SDK 已经冻结了全部 API 调用；`OnSessionRecovered`
> 在冻结解除且轮询恢复后触发。

---

## 异步 QR 登录

Web 平台场景：前端请求二维码 → 展示给用户 → 轮询扫码状态。

```go
// 1. 获取 QR 码（非阻塞）
session, err := bot.LoginAsync(ctx)
if err != nil {
    log.Fatal(err)
}

// 2. 返回给前端展示
qrImageBase64 := session.QRImage()
qrImageURL := session.QRImageURL()

// 3. 前端轮询状态
status := session.Status() // Pending → Scanned → Confirmed
// 或阻塞等待
err = session.Wait(ctx)
```

`LoginStatus` 枚举：`Pending` → `Scanned` → `Confirmed` / `Expired` / `Error`

---

## 登录状态机与配对码

扫码登录不是「等 confirmed」这么简单，服务端会返回 8 种状态，SDK 全部处理：

| 状态 | 含义 | SDK 行为 |
|---|---|---|
| `wait` / `scaned` | 等待扫码 / 等待手机确认 | 继续轮询 |
| `need_verifycode` | 需要输入手机上显示的配对码 | 调用 `VerifyCodeFunc` 取码，带 `verify_code` 重新轮询 |
| `verify_code_blocked` | 配对码错误次数过多 | 刷新二维码重来，超过 3 次返回 `ErrVerifyCodeBlocked` |
| `scaned_but_redirect` | 账号在其他 IDC | **把后续轮询切到 `redirect_host`**，否则永远等不到 confirmed |
| `binded_redirect` | 该 bot 已绑定过本客户端 | 复用本地凭证；无凭证时返回 `ErrAlreadyBound` |
| `expired` | 二维码过期 | 自动重新拉码并再次回调 `QRCallback`，最多 3 次 |
| `confirmed` | 登录成功 | 保存 token / baseurl |

获取二维码时会带上本地已有的 bot_token（最多 10 个），服务端据此识别已绑定的 bot：

```go
bot := ilink.NewBot(
    // 多 bot 场景下把所有 token 交给服务端识别
    ilink.WithLocalTokens(func() []string { return myTokenPool() }),
)
```

### 自定义配对码输入

默认从 stdin 读取。接入 Web / 桌面端时换成自己的输入通道：

```go
codeCh := make(chan string, 1)

bot := ilink.NewBot(
    ilink.WithVerifyCodeFunc(func(retry bool) (string, error) {
        if retry {
            ui.ShowError("配对码不正确，请重新输入")
        }
        ui.PromptForPairCode()
        select {
        case code := <-codeCh:
            return code, nil
        case <-time.After(2 * time.Minute):
            return "", errors.New("配对码输入超时")
        }
    }),
)
```

`LoginAsync` 会把状态变化同步到 `QRSession`，包括 `LoginStatusNeedVerifyCode`；
二维码刷新后 `QRImage()` 返回新图，前端据此重绘即可。

---

## 工具调用进度

AI bot 执行工具时如果一直沉默，用户会以为卡死。`ToolProgress` 推送
`TOOL_CALL_START` / `TOOL_CALL_RESULT`（item type 11 / 12），微信端渲染成进度条：

```go
bot.OnBody(func(ctx *ilink.Context) {
    ctx.SetRunID(uuid.NewString())   // 把这一轮的所有消息归组到同一个气泡

    tp := ctx.ToolProgress()
    defer tp.Finalize()              // 等进度消息发完，再发最终回复

    var result string
    err := tp.Track("web_search", "call-1", func() error {
        var err error
        result, err = searchWeb(ctx.Body())
        return err
    })
    if err != nil {
        ctx.ReplyText("搜索失败：" + err.Error())
        return
    }

    tp.Finalize()
    ctx.ReplyText(result)
})
```

`Track` 会自动发 Start、执行、再按返回的 error 发 `completed` / `failed`。
需要手动控制时用 `tp.Start(name, id)` 和 `tp.End(name, id, status)`。

进度消息在后台 goroutine 按顺序发送，**永远不会阻塞或失败你的业务逻辑**——
发送错误只记日志。`Finalize()` 幂等，可以安全地 `defer` 加显式调用。

### run_id

`run_id` 把一轮 agent 运行产生的所有消息（进度、中间输出、最终回复）串成一组：

```go
ctx.SetRunID("run-abc")        // 之后 ctx 的所有回复都带这个 run_id
bot.SendTextRun(c, uid, text, "run-abc")   // 主动推送时指定
```

不设置时默认沿用入站消息的 `run_id`。

---

## 出站 Hook

拦截每一条外发消息——包括 handler 回复、主动推送、批量发送、发送队列：

```go
bot := ilink.NewBot(ilink.WithHooks(ilink.Hooks{
    // 改写内容；返回 ErrSendCanceled 静默丢弃
    OnBeforeSend: func(msg *ilink.Message) error {
        for i := range msg.ItemList {
            if it := msg.ItemList[i].TextItem; it != nil {
                if containsSecret(it.Text) {
                    return ilink.ErrSendCanceled
                }
                it.Text = addDisclaimer(it.Text)
            }
        }
        return nil
    },

    // 观测投递结果（不影响返回值）
    OnAfterSend: func(msg *ilink.Message, err error) {
        metrics.RecordSend(msg.ToUserID, err)
    },
}))
```

`OnBeforeSend` 返回非 `ErrSendCanceled` 的错误会中止发送并把错误返回给调用方。

---

## 斜杠指令与调试

```go
bot.Use(
    ilink.Timing(),          // 记录消息进入时间，供耗时统计使用
    ilink.SlashCommands(ilink.SlashCommandOptions{
        Commands: map[string]ilink.SlashCommandFunc{
            "/status": func(c *ilink.Context, args string) error {
                return c.ReplyText("在线，队列 " + strconv.Itoa(queue.Pending()))
            },
        },
    }),
)
```

内置指令：

- `/echo <文本>` — 原样回显，并附带「平台→SDK」「SDK 处理」的耗时明细
- `/toggle-debug` — 按用户开关 debug 模式

命中指令会 `Abort()` 掉整条链，不会流到 AI handler。未命中的 `/xxx` 正常下发。
下游 handler 用 `ctx.DebugEnabled()` 读取 debug 开关：

```go
bot.OnBody(func(ctx *ilink.Context) {
    reply := askAI(ctx.Body())
    if ctx.DebugEnabled() {
        reply += fmt.Sprintf("\n\n⏱ 总耗时 %dms", time.Since(ctx.ReceivedAt()).Milliseconds())
    }
    ctx.ReplyText(reply)
})
```

用 `SlashCommandOptions{DisableBuiltins: true}` 只保留自定义指令。

---

## 语音转写与转码

微信语音是 SILK 编码。多数情况下**不需要解码**——服务端已经附带了转写文本：

```go
bot.OnBody(func(ctx *ilink.Context) {
    // 语音消息的 Body() 自动回落到 voice_item.text（ASR 结果）
    ctx.ReplyText("你说的是：" + ctx.Body())
})
```

> 用 `OnBody` 而不是 `OnText`：`OnText` 只匹配字面文本 item，语音消息会被漏掉。

确实需要音频数据时：

```go
data, mime, err := bot.DownloadVoice(ctx.Ctx, ctx.Message.GetVoiceItem())
```

要转成 WAV 需要自备 SILK 解码器（SDK 不绑定 cgo 依赖）。实现 `VoiceTranscoder`，
解出 PCM 后用 `ilink.PCMToWAV` 封装：

```go
bot := ilink.NewBot(
    ilink.WithVoiceTranscoder(ilink.VoiceTranscoderFunc(
        func(silk []byte, sampleRate int) ([]byte, error) {
            pcm, err := mysilk.Decode(silk)   // 你的解码器
            if err != nil {
                return nil, err
            }
            return ilink.PCMToWAV(pcm, sampleRate), nil
        },
    )),
)

wav, err := bot.DownloadVoiceWAV(ctx.Ctx, ctx.Message.GetVoiceItem())
```

未配置转码器时 `DownloadVoiceWAV` 返回 `ErrNoVoiceTranscoder`；
已经是 WAV / MP3 的负载会原样返回。

### 缩略图上传

图片和视频带缩略图时，接收方在文件下载完成前就能看到预览：

```go
res, err := bot.UploadWithOptions(ctx.Ctx, videoBytes, userID, ilink.UploadOptions{
    FileType:    "video",
    Thumb:       thumbJPEG,
    ThumbWidth:  320,
    ThumbHeight: 180,
})
ctx.ReplyItems([]ilink.MessageItem{ilink.BuildVideoItem(res, 320, 180, durationMs)})
```

缩略图与主文件共用同一个 AES key。缩略图上传失败不会导致整体失败——
主文件已经在 CDN 上，退化成无预览发送。

---

## 多 Bot 管理

平台需要为每个用户运行独立的 Bot 实例：

```go
manager := ilink.NewBotManager()

// 创建并登录
bot, _ := manager.Add("user_001",
    ilink.WithTokenFile("data/user_001.token.json"),
    ilink.WithContextTokenDir("data/user_001_ctx"),
)
bot.OnText(func(ctx *ilink.Context) { ctx.ReplyText("hi") })
bot.Login(ctx, ilink.TerminalQR) // 或 bot.LoginAsync()

// 启动
manager.Start(ctx, "user_001")

// 查看所有 Bot 状态
for _, info := range manager.List() {
    fmt.Printf("bot=%s status=%s\n", info.ID, info.Status)
}

// 停止单个
manager.Stop("user_001")

// 移除（停止并删除）
manager.Remove("user_001")

// 停止全部
manager.StopAll()
```

---

## 批量推送与发送队列

### 批量发送（并发）

```go
results := bot.BatchSendText(ctx, []string{"user_a", "user_b", "user_c"}, "通知内容")
for _, r := range results {
    if r.Err != nil {
        log.Printf("发送失败 user=%s err=%v", r.UserID, r.Err)
    }
}
```

### 发送队列（限速）

适合股价提醒、定时推送等高频场景：

```go
queue := ilink.NewSendQueue(bot, 200*time.Millisecond, 1000) // 每 200ms 发一条，缓冲 1000 条
go queue.Start(ctx)

// 入队（非阻塞）
resultCh := queue.EnqueueText("user_abc", "你关注的股票涨了！")

// 等结果（可选）
if err := <-resultCh; err != nil {
    log.Printf("发送失败: %v", err)
}

// 查看排队数
fmt.Println("排队中:", queue.Pending())

// 停止
queue.Stop()
```

---

## 存储接口

所有存储接口均可替换为自定义实现（如 Redis、数据库等）。

### TokenStore — 持久化 bot_token

```go
type TokenStore interface {
    Save(token, baseURL string) error
    Load() (token, baseURL string, err error)
    Clear() error
}
```

内置：`FileTokenStore`（文件）、`MemTokenStore`（内存）。

### ContextTokenStore — 持久化用户 context_token

```go
type ContextTokenStore interface {
    Save(userID, token string) error
    Load(userID string) (string, error)
    Clear(userID string) error
}
```

内置：`FileContextTokenStore`（每用户一个 JSON 文件）、`MemContextTokenStore`（内存）。

### SyncBufStore — 持久化轮询游标

```go
type SyncBufStore interface {
    Save(buf string) error
    Load() (string, error)
}
```

内置：`FileSyncBufStore`（单文件）。通过 `WithSyncBufFile(path)` 启用，重启后从断点继续拉消息。

```go
// 三种存储全部自定义
bot := ilink.NewBot(
    ilink.WithTokenStore(myTokenStore),
    ilink.WithContextTokenStore(myCtxStore),
    ilink.WithSyncBufStore(mySyncBufStore),
)
```

---

## 错误处理

SDK 定义的哨兵错误：

| 错误 | 说明 |
|------|------|
| `ilink.ErrNotLoggedIn` | 调用 Run 前未调用 Login |
| `ilink.ErrSessionExpired` | token 失效（`-14`），poller 会自动暂停 1 小时后重试 |
| `ilink.ErrQRCodeExpired` | 二维码多次超时，Login 返回此错误 |
| `ilink.ErrAlreadyBound` | 该 bot 已绑定过本客户端，但本地没有可复用的凭证 |
| `ilink.ErrVerifyCodeBlocked` | 配对码错误次数过多，被服务端拒绝 |
| `ilink.ErrNoVerifyCodeFunc` | 服务端要求配对码但未配置 `VerifyCodeFunc` |
| `ilink.ErrPollerStopped` | 轮询被正常停止（ctx 取消或调用 Stop） |
| `ilink.ErrNoContextToken` | 主动发送时找不到用户的 context_token |
| `ilink.ErrSendCanceled` | `OnBeforeSend` 主动取消了本次发送（不是故障） |
| `ilink.ErrNoVoiceTranscoder` | 语音是 SILK，但未配置 `VoiceTranscoder` |

> **Token 失效自愈**：`bot.Run()` 检测到 `-14` 后不会返回错误，而是**冻结全部 API 调用**
> 并暂停 1 小时再重试。冻结期内 `SendText`、`Upload`、`SendQueue` 等都会立刻返回
> `*ilink.SessionPausedError`，不会拿着已被拒绝的 token 反复打服务端。
> 用 `ilink.IsSessionPaused(err)` 判断，`err.Remaining` 是剩余时长。
> 只有主动取消 context 或调用 `bot.Stop()` 才会让 `Run()` 返回。

API 层面的错误以 `*ilink.APIError` 返回：

```go
var ae *ilink.APIError
if errors.As(err, &ae) {
    log.Printf("API 错误：code=%d msg=%s", ae.Code, ae.Message)
}

// 快捷判断 token 失效
if ilink.IsSessionExpired(err) { ... }

// 冻结期内的调用
var pe *ilink.SessionPausedError
if errors.As(err, &pe) {
    log.Printf("会话冻结中，还剩 %v", pe.Remaining)
}
```

> **注意**：服务端在 `ret` 和 `errcode` 两个字段中任选其一返回错误码，SDK 会同时读取。
> 只看其中一个会把 `-14` 读成 `0`，让 `IsSessionExpired` 静默失效。

---

## 可观测性

### 网络错误分类

`ClassifyNetError` 把传输层故障归成 DNS / TCP / TLS / 超时四类，定位问题不用再读 Go 的
原始错误串。SDK 内部（client、poller）已自动使用，也可以自己调用：

```go
if err := bot.SendText(ctx, uid, text); err != nil {
    cls := ilink.ClassifyNetError(err)
    log.Printf("发送失败 type=%s desc=%s code=%s", cls.Type, cls.Description, cls.Code)
    // cls.LogArgs() 可直接展开进 slog
    logger.Error("send failed", cls.LogArgs()...)
}
```

| Type | 典型场景 |
|---|---|
| `dns` | 域名解析失败、DNS 配置错误 |
| `tcp` | 连接被拒、网络不可达、连接超时、对端断开 |
| `tls` | 证书不受信、域名不匹配、握手失败（常见于中间盒） |
| `timeout` | 客户端 deadline 到期 |

### 日志脱敏

debug 级别日志会打印请求/响应体，SDK 自动脱敏 token、context_token、aes_key、
typing_ticket、get_updates_buf、二维码等字段，并把 body 截断到 512 字节。
需要自己打日志时直接复用：

```go
log.Printf("url=%s body=%s token=%s",
    ilink.RedactURL(u), ilink.RedactBody(body), ilink.RedactToken(tok))
```

---

## 完整示例

### Mirror Bot（收什么回什么）

```go
package main

import (
    "context"
    "fmt"
    "log"
    "log/slog"
    "os"
    "os/signal"
    "syscall"

    ilink "github.com/dobest1024/go-weixin-ilink"
)

func main() {
    logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

    bot := ilink.NewBot(
        ilink.WithLogger(logger),
        ilink.WithTokenFile(".mirror-token.json"),
        ilink.WithContextTokenDir(".mirror-ctx"),
        ilink.WithSyncBufFile(".mirror-syncbuf"),
    )

    bot.OnText(func(ctx *ilink.Context) {
        // 如果用户引用了某条消息
        if ctx.HasQuote() {
            ctx.ReplyText(fmt.Sprintf("你引用了：%q\n你说：%s", ctx.QuotedText(), ctx.Text()))
            return
        }
        ctx.ReplyText(ctx.Text())
    })

    bot.OnImage(func(ctx *ilink.Context) {
        data, err := ctx.DownloadImage(ctx.Message.GetImageItem())
        if err != nil {
            ctx.ReplyText(fmt.Sprintf("下载失败：%v", err))
            return
        }
        result, err := ctx.Upload(data, "image")
        if err != nil {
            ctx.ReplyText(fmt.Sprintf("上传失败：%v", err))
            return
        }
        ctx.ReplyItems([]ilink.MessageItem{ilink.BuildImageItem(result)})
    })

    bot.OnFile(func(ctx *ilink.Context) {
        file := ctx.Message.GetFileItem()
        data, err := ctx.DownloadMedia(file.Media)
        if err != nil {
            ctx.ReplyText(fmt.Sprintf("下载失败：%v", err))
            return
        }
        result, err := ctx.Upload(data, "file")
        if err != nil {
            ctx.ReplyText(fmt.Sprintf("上传失败：%v", err))
            return
        }
        ctx.ReplyItems([]ilink.MessageItem{ilink.BuildFileItem(result, file.FileName)})
    })

    if err := bot.Login(context.Background(), ilink.TerminalQR); err != nil {
        log.Fatal(err)
    }

    c, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    defer stop()

    logger.Info("Mirror bot 已启动")
    if err := bot.Run(c); err != nil && err != context.Canceled {
        log.Fatal(err)
    }
}
```

### AI Bot（含中间件与限流）

```go
package main

import (
    "context"
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
    logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

    bot := ilink.NewBot(
        ilink.WithLogger(logger),
        ilink.WithTokenFile(".ai-token.json"),
        ilink.WithContextTokenDir(".ai-ctx"),
        ilink.WithSyncBufFile(".ai-syncbuf"),
    )

    // 日志中间件
    bot.Use(func(ctx *ilink.Context) {
        logger.Info("收到消息", "from", ctx.UserID(), "text", ctx.Text())
        ctx.Next()
    })

    // 限流：每用户每 3 秒最多 1 条
    bot.Use(rateLimiter(3 * time.Second))

    // 命令路由
    bot.OnTextPrefix("/help", func(ctx *ilink.Context) {
        ctx.ReplyText("发送任意文字获得 AI 回复\n/help 查看帮助")
    })

    // 语音消息：优先使用 ASR 转写文字
    bot.OnVoice(func(ctx *ilink.Context) {
        voice := ctx.Message.GetVoiceItem()
        if voice != nil && voice.Text != "" {
            ctx.Typing()
            defer ctx.StopTyping()
            ctx.ReplyText("你说：" + voice.Text + "\n\nAI 回复：" + callAI(voice.Text))
        } else {
            ctx.ReplyText("[收到语音，暂不支持处理]")
        }
    })

    // AI 文字回复
    bot.OnText(func(ctx *ilink.Context) {
        ctx.Typing()
        defer ctx.StopTyping()
        ctx.ReplyText(callAI(ctx.Text()))
    })

    if err := bot.Login(context.Background(), ilink.TerminalQR); err != nil {
        log.Fatal(err)
    }

    c, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    defer stop()

    logger.Info("AI bot 已启动")
    if err := bot.Run(c); err != nil && err != context.Canceled {
        log.Fatal(err)
    }
}

func callAI(text string) string {
    // 接入 Claude / OpenAI / 其他 AI 服务
    return "AI 回复：" + text
}

func rateLimiter(interval time.Duration) ilink.HandlerFunc {
    var mu sync.Mutex
    seen := make(map[string]time.Time)
    return func(ctx *ilink.Context) {
        mu.Lock()
        last, ok := seen[ctx.UserID()]
        now := time.Now()
        if ok && now.Sub(last) < interval {
            mu.Unlock()
            ctx.ReplyText("请慢一点，稍后再试")
            ctx.Abort()
            return
        }
        seen[ctx.UserID()] = now
        mu.Unlock()
        ctx.Next()
    }
}
```

---

## 项目结构

```
go-weixin-ilink/
├── bot.go          # Bot 主入口，路由注册，生命周期管理
├── context.go      # Context + 中间件链 + 回复/媒体辅助
├── dispatcher.go   # Matcher 接口 + 路由调度
├── message.go      # 协议类型（Message, MessageItem, RefMessage 等）
├── auth.go         # QR 扫码登录 + 凭证复用
├── auth_async.go   # 异步 QR 登录（LoginAsync / QRSession）
├── poller.go       # 长轮询循环（断点续传、session 自愈、并发处理）
├── sender.go       # 底层发送函数
├── sendqueue.go    # 批量发送 + 限速发送队列
├── hooks.go        # 生命周期 Hook 定义
├── manager.go      # 多 Bot 管理器（BotManager）
├── typing.go       # 打字状态管理（per-user ticket 缓存）
├── media.go        # CDN 上传/下载 + Build* 辅助函数
├── storage.go      # TokenStore / ContextTokenStore / SyncBufStore 接口及实现
├── crypto.go       # AES-128-ECB + PKCS7（内部使用）
├── client.go       # HTTP 客户端（内部使用）
├── options.go      # 函数式选项
├── qr.go           # TerminalQR 辅助
└── examples/
    ├── mirror/     # Mirror Bot（收什么回什么）
    ├── echo/       # Echo Bot
    └── ai_bot/     # AI Bot 示例
```

## License

MIT
