# 路由与中间件

把消息分发到正确的 handler，以及横切逻辑的组织方式。

---

## 消息路由

所有路由方法均可接受多个 handler，按注册顺序组成处理链。

### 按消息类型

```go
bot.OnBody(handler)    // 任何有正文的消息：文本 + 引用 + 语音转写
bot.OnText(handler)    // 仅字面文本 item
bot.OnImage(handler)   // 图片消息
bot.OnVoice(handler)   // 语音消息
bot.OnFile(handler)    // 文件消息
bot.OnVideo(handler)   // 视频消息
```

> **接 AI 时用 `OnBody`。** `OnText` 只匹配字面文本 item，服务端已转写的语音消息
> 会被整个漏掉。`OnBody` 配 `ctx.Body()` 才是完整正文，
> 详见[消息上下文](#消息上下文)。

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
- 一条消息可能同时命中多个路由：语音消息既满足 `OnVoice`，
  带转写时也满足 `OnBody`，两个 handler 都会跑

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

**相关**：[配置](configuration.md) · [AI Agent 集成](ai-agent.md) · [媒体收发](media.md)

[← 返回文档索引](../README.md#文档)
