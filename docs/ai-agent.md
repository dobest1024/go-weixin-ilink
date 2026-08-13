# AI Agent 集成

工具调用进度、run_id 归组，以及出站消息的拦截点。

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

**相关**：[路由与中间件](routing.md) · [运维](operations.md)

[← 返回文档索引](../README.md#文档)
