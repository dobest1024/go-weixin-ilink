# 媒体收发

图片、语音、文件、视频的上传下载，以及主动推送。

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

**相关**：[路由与中间件](routing.md) · [配置](configuration.md)

[← 返回文档索引](../README.md#文档)
