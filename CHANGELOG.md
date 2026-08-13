# Changelog

本项目遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/) 格式，
版本号遵循 [语义化版本](https://semver.org/lang/zh-CN/)。

`v0.x` 阶段 API 仍可能调整，破坏性变更会在 minor 版本中发布并在此列出。

## [0.2.0] - 2026-08-13

首个正式发布的版本。协议层对齐官方 `openclaw-weixin` 插件 **2.4.6**。

### ⚠️ 破坏性变更

- **`LoginStatus` 常量值重排**。新增了 `LoginStatusNeedVerifyCode` 和
  `LoginStatusAlreadyBound`，插在中间，因此 `LoginStatusConfirmed` 从 `2`
  变成 `3`，`LoginStatusExpired` 从 `3` 变成 `5`。
  用常量名比较不受影响；把这些值**持久化成数字**或跨进程传输的需要迁移。

### 协议修复

- **`get_bot_qrcode` 改为 POST**，携带 `local_token_list`（对应上游 2.3.1 的改动），
  且不再发送 `Authorization` 头 —— 带着待替换的旧 token 会让服务端按旧会话应答。
- **补齐 4 个此前被静默忽略的扫码状态**：
  - `scaned_but_redirect` — 把后续状态轮询切到服务端指定的 IDC 主机。
    不处理的话跨 IDC 的账号永远等不到 `confirmed`，只能超时。
  - `need_verifycode` — 通过 `VerifyCodeFunc` 收取手机上显示的配对码。
  - `verify_code_blocked` — 刷新二维码重试，超限返回 `ErrVerifyCodeBlocked`。
  - `binded_redirect` — 识别「已绑定过本客户端」，复用本地凭证。

  二维码过期现在会自动换新，最多 3 次。
- **同时读取 `ret` 和 `errcode`**。服务端在两个字段中任选其一返回错误码，
  此前只读 `errcode` 会把 token 失效的 `-14` 读成 `0`，导致所有发送路径上的
  `IsSessionExpired` 静默失效。
- `iLink-App-Id` 默认值补为 `"bot"`；`channel_version` 升到 `2.4.6`；
  `sendtyping` 补上 `base_info`。

### 新增

- **工具调用进度**（item type 11/12）与 `ToolProgress`：顺序发送、不阻塞业务
  逻辑、`Finalize()` 幂等。出站消息新增 `run_id` 字段用于归组。
- **token 失效后冻结全部 API 调用**，而不只是停止轮询。此前主动推送、媒体上传
  会继续拿着已被服务端拒绝的 token 反复请求。冻结期内调用返回
  `*SessionPausedError`。
- **`Message.BodyText()`** / `ctx.Body()` / `OnBody()`：渲染引用上下文，
  并在纯语音消息上回落到服务端转写文本。`ctx.Text()` 语义不变，仍只取字面文本。
- **出站 Hook** `OnBeforeSend`（可改写或取消）/ `OnAfterSend`（观测投递结果），
  覆盖 handler 回复、主动推送、批量发送和发送队列。
- **缩略图上传**（`UploadOptions.Thumb`）、**`VoiceTranscoder`** 接口与
  `PCMToWAV`、**MIME 推断**（扩展名 / Content-Type / magic bytes）。
- **中间件**：`ilink.Timing()`、`ilink.SlashCommands()`（内置 `/echo`、
  `/toggle-debug`，支持自定义指令）。
- **`ClassifyNetError`**：把传输层故障分为 dns / tcp / tls / timeout；
  请求日志自动脱敏并截断。
- `Context.Bot()` 及 `UploadWithOptions` / `DownloadVoice` / `DownloadVoiceWAV` /
  `DownloadFile` 等 Context 包装方法。
- `SyncBufStore` 支持回退路径，迁移存储位置时不会重放历史消息。

### 修复

- **中间件链的越界 panic**：`Abort()` 把 `int8` 索引设成 127，`Next()` 尾部的
  自增溢出成 -128，随即以负数索引 handler 切片。改用布尔标志跟踪。
  任何调用 `ctx.Abort()` 的中间件都会触发。
- `parseBase64AESKey` 现在校验 32 字节形式确实是十六进制，此前直接假定，
  遇到非 hex 数据会解密出乱码。
- 打字状态的配置缓存在刷新失败时保留上一个可用 ticket，并按指数退避重试
  （2s → 封顶 1h），成功后在 24h 窗口内随机选取刷新点，避免同时惊群。

### 文档与示例

- 补上 MIT `LICENSE` 文件（README 此前只声明了协议）。
- README 精简至约 200 行，参考内容拆分到 `docs/`。
- 示例扩充到 7 个，新增 `ai_agent`、`webapp`、`middleware`。

### 测试

新增 83 个测试，覆盖扫码状态机、错误码优先级、会话冻结、网络错误分类、
日志脱敏、密钥解析、正文渲染、出站 Hook、工具进度与配置缓存。

[0.2.0]: https://github.com/dobest1024/go-weixin-ilink/releases/tag/v0.2.0
