// Web 扫码登录：把登录流程搬到浏览器，不依赖终端
//
// 终端登录用 bot.Login + TerminalQR 就够了。但做平台/桌面端时，二维码要渲染在
// 页面上、配对码要从表单收，这时需要：
//
//   - LoginAsync()            非阻塞拿到 QRSession，轮询 Status()
//   - WithVerifyCodeFunc()    配对码改从 HTTP 表单读，而不是 stdin
//   - QRSession.QRImage()     二维码刷新后返回新图，前端重绘
//
// 服务端返回的 8 种扫码状态（IDC 重定向、已绑定、配对码被封等）由 SDK 内部处理，
// 这里只需要把 QRSession 的状态映射到页面。
//
// 运行：
//
//	go run ./examples/webapp
//	打开 http://localhost:8080
package main

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"log"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	ilink "github.com/dobest1024/go-weixin-ilink"
)

// loginServer 持有一次登录流程的全部状态。
type loginServer struct {
	logger *slog.Logger

	mu      sync.Mutex
	bot     *ilink.Bot
	session *ilink.QRSession
	// codeCh 把 HTTP 表单收到的配对码交给 SDK 的 VerifyCodeFunc。
	codeCh chan string
	// needCode 为 true 时页面渲染配对码输入框。
	needCode bool
	// codeRejected 表示上一个配对码被服务端拒绝了。
	codeRejected bool
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	srv := &loginServer{logger: logger, codeCh: make(chan string, 1)}

	http.HandleFunc("/", srv.handleIndex)
	http.HandleFunc("/login", srv.handleStartLogin)
	http.HandleFunc("/verify", srv.handleVerifyCode)
	http.HandleFunc("/status", srv.handleStatus)

	logger.Info("open http://localhost:8080 to connect a bot")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

// ─── 登录流程 ─────────────────────────────────────────────────────────────────

func (s *loginServer) handleStartLogin(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	if s.session != nil && s.session.Status() == ilink.LoginStatusPending {
		s.mu.Unlock()
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	bot := ilink.NewBot(
		ilink.WithLogger(s.logger),
		ilink.WithTokenFile(".webapp-token.json"),
		ilink.WithContextTokenDir(".webapp-ctx"),
		ilink.WithSyncBufFile(".webapp-syncbuf"),
		// 关键：配对码不再走 stdin，改成等页面表单提交。
		ilink.WithVerifyCodeFunc(s.promptVerifyCode),
	)
	s.bot = bot
	s.mu.Unlock()

	bot.OnBody(func(ctx *ilink.Context) {
		_ = ctx.ReplyText("收到：" + ctx.Body())
	})

	session, err := bot.LoginAsync(context.Background())
	if err != nil {
		http.Error(w, "发起登录失败: "+err.Error(), http.StatusInternalServerError)
		return
	}

	s.mu.Lock()
	s.session = session
	s.needCode = false
	s.codeRejected = false
	s.mu.Unlock()

	// 登录完成后自动开跑。
	go s.runAfterLogin(bot, session)

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// promptVerifyCode 是 SDK 的 VerifyCodeFunc：让页面显示输入框，然后阻塞等表单。
func (s *loginServer) promptVerifyCode(retry bool) (string, error) {
	s.mu.Lock()
	s.needCode = true
	s.codeRejected = retry // retry=true 说明上次输错了
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.needCode = false
		s.mu.Unlock()
	}()

	select {
	case code := <-s.codeCh:
		return code, nil
	case <-time.After(3 * time.Minute):
		return "", errors.New("配对码输入超时")
	}
}

func (s *loginServer) handleVerifyCode(w http.ResponseWriter, r *http.Request) {
	code := r.FormValue("code")
	if code != "" {
		select {
		case s.codeCh <- code:
		default: // 没人在等，丢弃
		}
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *loginServer) runAfterLogin(bot *ilink.Bot, session *ilink.QRSession) {
	if err := session.Wait(context.Background()); err != nil {
		// ErrAlreadyBound：该 bot 已绑定过本客户端，但本地没有可复用凭证。
		// 清掉 token 文件重新扫码即可。
		s.logger.Error("login failed", "status", session.Status(), "error", err)
		return
	}
	s.logger.Info("login confirmed, starting poller")
	if err := bot.Run(context.Background()); err != nil && !errors.Is(err, context.Canceled) {
		s.logger.Error("bot stopped", "error", err)
	}
}

// ─── 页面 ────────────────────────────────────────────────────────────────────

type pageData struct {
	Started      bool
	Status       string
	QRImage      string
	NeedCode     bool
	CodeRejected bool
	Err          string
}

func (s *loginServer) snapshot() pageData {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.session == nil {
		return pageData{}
	}
	d := pageData{
		Started:      true,
		Status:       statusText(s.session.Status()),
		QRImage:      s.session.QRImage(),
		NeedCode:     s.needCode,
		CodeRejected: s.codeRejected,
	}
	if err := s.session.Err(); err != nil {
		d.Err = err.Error()
	}
	return d
}

// statusText 把 LoginStatus 映射成给用户看的说明。
func statusText(st ilink.LoginStatus) string {
	switch st {
	case ilink.LoginStatusPending:
		return "等待扫码"
	case ilink.LoginStatusScanned:
		return "已扫码，请在手机上确认"
	case ilink.LoginStatusNeedVerifyCode:
		return "请输入手机上显示的配对码"
	case ilink.LoginStatusConfirmed:
		return "✅ 已连接"
	case ilink.LoginStatusAlreadyBound:
		return "该 bot 已连接过，但本地凭证缺失，请删除 token 文件后重试"
	case ilink.LoginStatusExpired:
		return "二维码多次失效，请重新发起"
	case ilink.LoginStatusError:
		return "登录失败"
	}
	return st.String()
}

func (s *loginServer) handleStatus(w http.ResponseWriter, _ *http.Request) {
	d := s.snapshot()
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":%q,"needCode":%t,"error":%q}`, d.Status, d.NeedCode, d.Err)
}

func (s *loginServer) handleIndex(w http.ResponseWriter, _ *http.Request) {
	if err := indexTmpl.Execute(w, s.snapshot()); err != nil {
		s.logger.Error("render failed", "error", err)
	}
}

var indexTmpl = template.Must(template.New("index").Parse(`<!doctype html>
<html lang="zh">
<head><meta charset="utf-8"><title>微信 Bot 连接</title>
<style>
 body{font-family:system-ui,sans-serif;max-width:32rem;margin:4rem auto;padding:0 1rem}
 .status{padding:.75rem 1rem;background:#f4f4f5;border-radius:.5rem;margin:1rem 0}
 .err{background:#fee2e2}
 img{width:16rem;height:16rem;border:1px solid #e4e4e7;border-radius:.5rem}
 input{padding:.5rem;font-size:1rem;width:8rem}
 button{padding:.5rem 1rem;font-size:1rem;cursor:pointer}
</style>
</head>
<body>
<h1>连接微信 Bot</h1>

{{if not .Started}}
  <form method="post" action="/login"><button type="submit">获取二维码</button></form>
{{else}}
  <div class="status {{if .Err}}err{{end}}">
    状态：{{.Status}}{{if .Err}}<br>错误：{{.Err}}{{end}}
  </div>

  {{if .QRImage}}<p><img src="{{.QRImage}}" alt="登录二维码"></p>{{end}}

  {{if .NeedCode}}
    <form method="post" action="/verify">
      {{if .CodeRejected}}<p style="color:#b91c1c">数字不匹配，请重新输入</p>{{end}}
      <label>配对码：<input name="code" autofocus autocomplete="off"></label>
      <button type="submit">提交</button>
    </form>
  {{end}}

  <form method="post" action="/login"><button type="submit">重新获取二维码</button></form>
  <!-- 二维码过期后 SDK 会自动换新图，轮询刷新页面即可看到 -->
  <script>setTimeout(() => location.reload(), 2000)</script>
{{end}}
</body>
</html>`))
