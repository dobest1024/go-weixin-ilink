// Mirror bot：发什么还你什么
//
// 文本  → 原文回复
// 图片  → 下载后重新上传发回
// 语音  → 下载后重新上传发回（保留时长）
// 文件  → 下载后重新上传发回（保留文件名）
// 视频  → 下载后重新上传发回（保留时长、分辨率）
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
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))

	bot := ilink.NewBot(
		ilink.WithLogger(logger),
		ilink.WithTokenFile(".mirror-token.json"),
		ilink.WithContextTokenDir(".mirror-ctx"),
		ilink.WithSyncBufFile(".mirror-syncbuf"),
	)

	// ── 文本 ──────────────────────────────────────────────────────────────────
	bot.OnText(func(ctx *ilink.Context) {
		logger.Info("收到文本", "from", ctx.UserID(), "text", ctx.Text(),
			"has_quote", ctx.HasQuote(), "quoted_text", ctx.QuotedText())

		var reply string
		if ctx.HasQuote() {
			reply = fmt.Sprintf("[引用: %s]\n%s", ctx.QuotedText(), ctx.Text())
		} else {
			reply = ctx.Text()
		}
		if err := ctx.ReplyText(reply); err != nil {
			logger.Error("回复文本失败", "error", err)
		}
	})

	// ── 图片 ──────────────────────────────────────────────────────────────────
	bot.OnImage(func(ctx *ilink.Context) {
		img := ctx.Message.GetImageItem()
		logger.Info("收到图片", "from", ctx.UserID(), "mid_size", img.MidSize)

		// 1. 下载（自动解密）
		data, err := ctx.DownloadImage(img)
		if err != nil {
			logger.Error("下载图片失败", "error", err)
			ctx.ReplyText(fmt.Sprintf("下载图片失败：%v", err))
			return
		}
		logger.Info("图片下载完成", "bytes", len(data))

		// 2. 上传（重新加密）
		result, err := ctx.Upload(data, "image")
		if err != nil {
			logger.Error("上传图片失败", "error", err)
			ctx.ReplyText(fmt.Sprintf("上传图片失败：%v", err))
			return
		}

		// 3. 发回
		if err := ctx.ReplyItems([]ilink.MessageItem{ilink.BuildImageItem(result)}); err != nil {
			logger.Error("发送图片失败", "error", err)
		}
	})

	// ── 语音 ──────────────────────────────────────────────────────────────────
	bot.OnVoice(func(ctx *ilink.Context) {
		voice := ctx.Message.GetVoiceItem()
		encType := 0
		if voice.Media != nil {
			encType = voice.Media.EncryptType
		}
		logger.Info("收到语音", "from", ctx.UserID(),
			"playtime_ms", voice.Duration,
			"encode_type", voice.EncodeType,
			"sample_rate", voice.SampleRate,
			"bits_per_sample", voice.BitsPerSample,
			"file_size", voice.FileSize,
			"asr_text", voice.Text,
			"media_encrypt_type", encType,
		)

		// 1. 下载
		data, err := ctx.DownloadMedia(voice.Media)
		if err != nil {
			logger.Error("下载语音失败", "error", err)
			ctx.ReplyText(fmt.Sprintf("下载语音失败：%v", err))
			return
		}
		logger.Info("语音下载完成", "bytes", len(data),
			"header_hex", fmt.Sprintf("%x", data[:min(16, len(data))]))

		// 2. 上传
		result, err := ctx.Upload(data, "voice")
		if err != nil {
			logger.Error("上传语音失败", "error", err)
			ctx.ReplyText(fmt.Sprintf("上传语音失败：%v", err))
			return
		}
		logger.Info("语音上传完成", "file_key", result.FileKey,
			"file_size", result.FileSize, "cipher_size", result.CipherSize)

		// 3. 发回（保留原始 encode_type / sample_rate / bits_per_sample）
		// 若服务器未下发 playtime，从字节数估算（SILK 16kHz ≈ 13 kbps）
		if voice.Duration == 0 && len(data) > 0 {
			voice.Duration = len(data) * 8 * 1000 / 13000
			logger.Info("playtime 估算", "bytes", len(data), "estimated_ms", voice.Duration)
		}
		// encode_type=4(speex per protocol) but data is actually SILK (\x02#!SILK_V3)
		// Official SDK never sends voice; try encode_type=6 (silk per protocol spec)
		voice.EncodeType = 4
		item := ilink.BuildVoiceItemFrom(result, voice)
		logger.Info("发送语音", "duration_ms", item.VoiceItem.Duration,
			"encode_type", item.VoiceItem.EncodeType,
			"sample_rate", item.VoiceItem.SampleRate)
		if err := ctx.ReplyItems([]ilink.MessageItem{item}); err != nil {
			logger.Error("发送语音失败", "error", err)
		} else {
			logger.Info("语音发送成功")
		}
	})

	// ── 文件 ──────────────────────────────────────────────────────────────────
	bot.OnFile(func(ctx *ilink.Context) {
		file := ctx.Message.GetFileItem()
		logger.Info("收到文件", "from", ctx.UserID(), "name", file.FileName)

		// 1. 下载
		data, err := ctx.DownloadMedia(file.Media)
		if err != nil {
			logger.Error("下载文件失败", "error", err)
			ctx.ReplyText(fmt.Sprintf("下载文件失败：%v", err))
			return
		}
		logger.Info("文件下载完成", "bytes", len(data))

		// 2. 上传
		result, err := ctx.Upload(data, "file")
		if err != nil {
			logger.Error("上传文件失败", "error", err)
			ctx.ReplyText(fmt.Sprintf("上传文件失败：%v", err))
			return
		}

		// 3. 发回（保留文件名）
		if err := ctx.ReplyItems([]ilink.MessageItem{ilink.BuildFileItem(result, file.FileName)}); err != nil {
			logger.Error("发送文件失败", "error", err)
		}
	})

	// ── 视频 ──────────────────────────────────────────────────────────────────
	bot.OnVideo(func(ctx *ilink.Context) {
		video := ctx.Message.GetVideoItem()
		logger.Info("收到视频", "from", ctx.UserID(),
			"duration_ms", video.PlayLength,
			"thumb_w", video.ThumbWidth,
			"thumb_h", video.ThumbHeight,
		)

		// 1. 下载
		data, err := ctx.DownloadMedia(video.Media)
		if err != nil {
			logger.Error("下载视频失败", "error", err)
			ctx.ReplyText(fmt.Sprintf("下载视频失败：%v", err))
			return
		}
		logger.Info("视频下载完成", "bytes", len(data))

		// 2. 上传
		result, err := ctx.Upload(data, "video")
		if err != nil {
			logger.Error("上传视频失败", "error", err)
			ctx.ReplyText(fmt.Sprintf("上传视频失败：%v", err))
			return
		}

		// 3. 发回（保留时长和分辨率）
		if err := ctx.ReplyItems([]ilink.MessageItem{
			ilink.BuildVideoItem(result, video.ThumbWidth, video.ThumbHeight, video.PlayLength),
		}); err != nil {
			logger.Error("发送视频失败", "error", err)
		}
	})

	// ── 登录 & 启动 ───────────────────────────────────────────────────────────
	if err := bot.Login(context.Background(), ilink.TerminalQR); err != nil {
		log.Fatalf("登录失败: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("Mirror bot 已启动，发什么还你什么")
	if err := bot.Run(ctx); err != nil && err != context.Canceled {
		log.Fatalf("bot 异常退出: %v", err)
	}
	logger.Info("bot 已停止")
}
