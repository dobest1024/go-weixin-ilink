# 登录

扫码登录的完整状态机、异步登录和多 Bot 管理。

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

**相关**：[配置](configuration.md) · [运维](operations.md)

[← 返回文档索引](../README.md#文档)
