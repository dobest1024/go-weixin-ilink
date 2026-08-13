# 配置

Bot 的构造选项、并发设置和存储接口。

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

## 并发处理

默认消息串行处理。设置 `WithMaxWorkers(n)` 后，SDK 使用 worker pool 并行处理：

```go
bot := ilink.NewBot(
    ilink.WithMaxWorkers(10), // 最多 10 个消息并发处理
)
```

> **注意**：并发模式下 handler 必须是并发安全的（避免共享可变状态或加锁）。

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

**相关**：[路由与中间件](routing.md) · [运维](operations.md)

[← 返回文档索引](../README.md#文档)
