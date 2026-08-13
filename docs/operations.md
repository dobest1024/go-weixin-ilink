# 运维

错误处理、网络诊断和日志。

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

**相关**：[配置](configuration.md) · [登录](login.md)

[← 返回文档索引](../README.md#文档)
