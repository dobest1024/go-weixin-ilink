package ilink

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// ─── apiError: ret / errcode precedence ──────────────────────────────────────

func TestAPIErrorReadsBothCodeFields(t *testing.T) {
	tests := []struct {
		name     string
		ret      int
		errcode  int
		wantCode int
		wantNil  bool
	}{
		{name: "both zero", wantNil: true},
		{name: "ret carries the code", ret: -14, wantCode: -14},
		{name: "errcode carries the code", errcode: -14, wantCode: -14},
		{name: "ret wins when both set", ret: -1, errcode: -14, wantCode: -1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := apiError(tc.ret, tc.errcode, "boom")
			if tc.wantNil {
				if err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
				return
			}
			var ae *APIError
			if !errors.As(err, &ae) {
				t.Fatalf("err = %v, want *APIError", err)
			}
			if ae.Code != tc.wantCode {
				t.Errorf("code = %d, want %d", ae.Code, tc.wantCode)
			}
		})
	}
}

// A stale-token response that only sets ret must still be recognised; reading
// errcode alone produced code 0 and silently defeated IsSessionExpired.
func TestIsSessionExpiredFromRetOnly(t *testing.T) {
	err := apiError(StaleTokenErrCode, 0, "session expired")
	if !IsSessionExpired(err) {
		t.Errorf("IsSessionExpired(%v) = false, want true", err)
	}
}

func TestSendReportsStaleTokenFromRet(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, SendMessageResponse{Ret: StaleTokenErrCode, ErrMsg: "stale"})
	}))
	defer ts.Close()

	p := sendParams{
		c:              newTestClient(ts),
		channelVersion: "2.4.6",
		logger:         discardLogger(),
	}
	err := p.text(context.Background(), "user-1", "ctx-token", "hi")
	if !IsSessionExpired(err) {
		t.Errorf("err = %v, want a stale-token APIError", err)
	}
}

// ─── Session guard ───────────────────────────────────────────────────────────

func TestSessionGuardBlocksAllCalls(t *testing.T) {
	var hits int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		writeJSON(w, SendMessageResponse{})
	}))
	defer ts.Close()

	c := newTestClient(ts)
	p := sendParams{c: c, channelVersion: "2.4.6", logger: discardLogger()}

	c.guard.pause(time.Hour)
	err := p.text(context.Background(), "user-1", "ctx", "hi")
	if !IsSessionPaused(err) {
		t.Fatalf("err = %v, want SessionPausedError", err)
	}
	if hits != 0 {
		t.Errorf("server received %d requests during the cooldown, want 0", hits)
	}

	c.guard.resume()
	if err := p.text(context.Background(), "user-1", "ctx", "hi"); err != nil {
		t.Fatalf("send after resume: %v", err)
	}
	if hits != 1 {
		t.Errorf("server received %d requests after resume, want 1", hits)
	}
}

func TestSessionGuardExpiresOnItsOwn(t *testing.T) {
	var g sessionGuard
	g.pause(20 * time.Millisecond)
	if g.check() == nil {
		t.Fatal("guard should be active immediately after pause")
	}
	time.Sleep(30 * time.Millisecond)
	if err := g.check(); err != nil {
		t.Errorf("guard still active after the pause elapsed: %v", err)
	}
}

func TestSessionGuardPauseDoesNotShorten(t *testing.T) {
	var g sessionGuard
	long := g.pause(time.Hour)
	short := g.pause(time.Second)
	if !short.Equal(long) {
		t.Errorf("a shorter pause shortened the cooldown: %v vs %v", short, long)
	}
}

// ─── Network error classification ────────────────────────────────────────────

func TestClassifyNetError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want NetErrorType
	}{
		{"nil", nil, NetErrUnknown},
		{"dns not found", &net.DNSError{Err: "no such host", IsNotFound: true}, NetErrDNS},
		{"dns timeout", &net.DNSError{Err: "timeout", IsTimeout: true}, NetErrDNS},
		{"connection refused", &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, NetErrTCP},
		{"connect timeout", &net.OpError{Op: "dial", Err: syscall.ETIMEDOUT}, NetErrTCP},
		{"host unreachable", &net.OpError{Op: "dial", Err: syscall.EHOSTUNREACH}, NetErrTCP},
		{"tls record header", tls.RecordHeaderError{Msg: "first record does not look like TLS"}, NetErrTLS},
		{"deadline exceeded", context.DeadlineExceeded, NetErrTimeout},
		{"eof", io.EOF, NetErrTCP},
		{"canceled", context.Canceled, NetErrUnknown},
		{"plain error", errors.New("something else"), NetErrUnknown},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyNetError(tc.err)
			if got.Type != tc.want {
				t.Errorf("type = %q, want %q (desc %q)", got.Type, tc.want, got.Description)
			}
		})
	}
}

func TestClassifyNetErrorThroughWrapping(t *testing.T) {
	wrapped := fmt.Errorf("http POST /x: %w",
		&net.OpError{Op: "dial", Err: syscall.ECONNREFUSED})
	got := ClassifyNetError(wrapped)
	if got.Type != NetErrTCP || got.Code != "ECONNREFUSED" {
		t.Errorf("got %+v, want tcp/ECONNREFUSED", got)
	}
}

// A connect(2) timeout is a reachability problem, not a client deadline, so it
// must not be swallowed by the generic net.Error timeout branch.
func TestClassifyNetErrorPrefersSyscallOverTimeout(t *testing.T) {
	got := ClassifyNetError(&net.OpError{Op: "dial", Err: syscall.ETIMEDOUT})
	if got.Type != NetErrTCP {
		t.Errorf("type = %q, want tcp", got.Type)
	}
}

// ─── Redaction ───────────────────────────────────────────────────────────────

func TestRedactToken(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", "(empty)"},
		{"short", "***"},
		{"abcdefghijklmnop", "abcd***mnop"},
	}
	for _, tc := range tests {
		if got := RedactToken(tc.in); got != tc.want {
			t.Errorf("RedactToken(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRedactURLMasksSecretsKeepsPath(t *testing.T) {
	got := RedactURL("https://api.example.com/ilink/bot/get_qrcode_status?qrcode=SECRET&bot_type=3")
	if strings.Contains(got, "SECRET") {
		t.Errorf("qrcode leaked: %s", got)
	}
	if !strings.Contains(got, "/ilink/bot/get_qrcode_status") {
		t.Errorf("path was lost: %s", got)
	}
	if !strings.Contains(got, "bot_type=3") {
		t.Errorf("non-secret parameter was dropped: %s", got)
	}
}

func TestRedactBodyMasksTokens(t *testing.T) {
	body := `{"bot_token":"super-secret","status":"confirmed","typing_ticket":"tkt"}`
	got := RedactBody(body)
	if strings.Contains(got, "super-secret") || strings.Contains(got, "tkt") {
		t.Errorf("secret leaked: %s", got)
	}
	if !strings.Contains(got, `"status":"confirmed"`) {
		t.Errorf("non-secret field was masked: %s", got)
	}
}

func TestRedactBodyTruncates(t *testing.T) {
	got := RedactBody(strings.Repeat("x", maxLoggedBodyLen*2))
	if len(got) > maxLoggedBodyLen+len("…(truncated)") {
		t.Errorf("body not truncated, len = %d", len(got))
	}
}

// ─── AES key parsing ─────────────────────────────────────────────────────────

func TestParseBase64AESKey(t *testing.T) {
	raw := []byte("0123456789abcdef") // 16 bytes
	hexKey := hex.EncodeToString(raw) // 32 ASCII chars

	t.Run("raw 16 bytes", func(t *testing.T) {
		got, err := parseBase64AESKey(base64.StdEncoding.EncodeToString(raw))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(raw) {
			t.Errorf("key = %x, want %x", got, raw)
		}
	})

	t.Run("base64 of hex string", func(t *testing.T) {
		got, err := parseBase64AESKey(base64.StdEncoding.EncodeToString([]byte(hexKey)))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(raw) {
			t.Errorf("key = %x, want %x", got, raw)
		}
	})

	// 32 bytes that are not hex used to pass the length check and then decrypt
	// to garbage; they must be rejected instead.
	t.Run("32 non-hex bytes rejected", func(t *testing.T) {
		bad := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("z", 32)))
		if _, err := parseBase64AESKey(bad); err == nil {
			t.Error("expected an error for 32 non-hex bytes")
		}
	})

	t.Run("wrong length rejected", func(t *testing.T) {
		bad := base64.StdEncoding.EncodeToString([]byte("too-short"))
		if _, err := parseBase64AESKey(bad); err == nil {
			t.Error("expected an error for a 9-byte key")
		}
	})
}

// ─── Message body rendering ──────────────────────────────────────────────────

func TestBodyTextRendersQuote(t *testing.T) {
	msg := &Message{ItemList: []MessageItem{{
		Type:     ItemTypeText,
		TextItem: &TextItem{Text: "这个怎么解决？"},
		RefMsg: &RefMessage{
			Title:       "张三",
			MessageItem: &MessageItem{Type: ItemTypeText, TextItem: &TextItem{Text: "服务器 500 了"}},
		},
	}}}
	want := "[引用: 张三 | 服务器 500 了]\n这个怎么解决？"
	if got := msg.BodyText(); got != want {
		t.Errorf("BodyText() = %q, want %q", got, want)
	}
}

// Quoted media travels as its own attachment, so only the new text is the body.
func TestBodyTextSkipsQuotedMedia(t *testing.T) {
	msg := &Message{ItemList: []MessageItem{{
		Type:     ItemTypeText,
		TextItem: &TextItem{Text: "这张图什么意思"},
		RefMsg: &RefMessage{
			Title:       "李四",
			MessageItem: &MessageItem{Type: ItemTypeImage, ImageItem: &ImageItem{}},
		},
	}}}
	if got := msg.BodyText(); got != "这张图什么意思" {
		t.Errorf("BodyText() = %q, want the plain text", got)
	}
}

func TestBodyTextFallsBackToVoiceTranscript(t *testing.T) {
	msg := &Message{ItemList: []MessageItem{{
		Type:      ItemTypeVoice,
		VoiceItem: &VoiceItem{Text: "帮我查一下今天的天气"},
	}}}
	if got := msg.BodyText(); got != "帮我查一下今天的天气" {
		t.Errorf("BodyText() = %q, want the transcript", got)
	}
	// Text() stays strict so existing OnText routing is unchanged.
	if got := msg.Text(); got != "" {
		t.Errorf("Text() = %q, want empty for a voice message", got)
	}
	if !msg.HasBodyText() {
		t.Error("HasBodyText() = false, want true")
	}
}

func TestOnBodyMatchesVoiceWithTranscript(t *testing.T) {
	m := matchBodyText()
	voice := &Message{ItemList: []MessageItem{{Type: ItemTypeVoice, VoiceItem: &VoiceItem{Text: "hi"}}}}
	if !m(voice) {
		t.Error("matchBodyText should match a transcribed voice message")
	}
	silent := &Message{ItemList: []MessageItem{{Type: ItemTypeVoice, VoiceItem: &VoiceItem{}}}}
	if m(silent) {
		t.Error("matchBodyText should not match a voice message with no transcript")
	}
}

// ─── Tool call items ─────────────────────────────────────────────────────────

func TestNormalizeToolCallStatus(t *testing.T) {
	tests := map[string]string{
		"completed": ToolCallStatusCompleted,
		"failed":    ToolCallStatusFailed,
		"blocked":   ToolCallStatusBlocked,
		"":          ToolCallStatusUnknown,
		"weird":     ToolCallStatusUnknown,
	}
	for in, want := range tests {
		if got := NormalizeToolCallStatus(in); got != want {
			t.Errorf("NormalizeToolCallStatus(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildToolCallItems(t *testing.T) {
	start := BuildToolCallStartItem("web_search", "call-1", 1700000000000)
	if start.Type != ItemTypeToolCallStart {
		t.Errorf("start item type = %d, want ItemTypeToolCallStart", start.Type)
	}
	if ItemTypeToolCallStart != 11 {
		t.Errorf("ItemTypeToolCallStart = %d, want the protocol value 11", ItemTypeToolCallStart)
	}
	if start.IsCompleted {
		t.Error("a start item must not be marked completed")
	}
	if start.ToolCallStartItem.ToolName != "web_search" {
		t.Errorf("tool name = %q", start.ToolCallStartItem.ToolName)
	}

	result := BuildToolCallResultItem("web_search", "call-1", "boom", 1700000000001)
	if result.Type != ItemTypeToolCallResult {
		t.Errorf("result item type = %d, want ItemTypeToolCallResult", result.Type)
	}
	if ItemTypeToolCallResult != 12 {
		t.Errorf("ItemTypeToolCallResult = %d, want the protocol value 12", ItemTypeToolCallResult)
	}
	if !result.IsCompleted {
		t.Error("a result item must be marked completed")
	}
	if result.ToolCallResultItem.Status != ToolCallStatusUnknown {
		t.Errorf("status = %q, want normalized to unknown", result.ToolCallResultItem.Status)
	}
}

// ─── Outbound hooks ──────────────────────────────────────────────────────────

func TestOnBeforeSendCanRewriteContent(t *testing.T) {
	var received string
	ts := newRecordingSendServer(t, func(msg *Message) {
		received = msg.ItemList[0].TextItem.Text
	})
	defer ts.Close()

	hooks := &Hooks{OnBeforeSend: func(msg *Message) error {
		msg.ItemList[0].TextItem.Text = "rewritten"
		return nil
	}}
	p := sendParams{c: newTestClient(ts), channelVersion: "2.4.6", hooks: hooks, logger: discardLogger()}
	if err := p.text(context.Background(), "u", "ctx", "original"); err != nil {
		t.Fatal(err)
	}
	if received != "rewritten" {
		t.Errorf("server saw %q, want the rewritten text", received)
	}
}

func TestOnBeforeSendCanCancel(t *testing.T) {
	var hits int
	ts := newRecordingSendServer(t, func(*Message) { hits++ })
	defer ts.Close()

	hooks := &Hooks{OnBeforeSend: func(*Message) error { return ErrSendCanceled }}
	p := sendParams{c: newTestClient(ts), channelVersion: "2.4.6", hooks: hooks, logger: discardLogger()}
	err := p.text(context.Background(), "u", "ctx", "dropped")
	if !errors.Is(err, ErrSendCanceled) {
		t.Errorf("err = %v, want ErrSendCanceled", err)
	}
	if hits != 0 {
		t.Errorf("server received %d requests, want 0", hits)
	}
}

func TestOnAfterSendSeesResult(t *testing.T) {
	ts := newRecordingSendServer(t, func(*Message) {})
	defer ts.Close()

	var gotErr error
	var called bool
	hooks := &Hooks{OnAfterSend: func(msg *Message, err error) {
		called, gotErr = true, err
	}}
	p := sendParams{c: newTestClient(ts), channelVersion: "2.4.6", hooks: hooks, logger: discardLogger()}
	if err := p.text(context.Background(), "u", "ctx", "hi"); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("OnAfterSend was not called")
	}
	if gotErr != nil {
		t.Errorf("OnAfterSend err = %v, want nil", gotErr)
	}
}

// ─── run_id propagation ──────────────────────────────────────────────────────

func TestRunIDIsSentOnTheWire(t *testing.T) {
	var got string
	ts := newRecordingSendServer(t, func(msg *Message) { got = msg.RunID })
	defer ts.Close()

	p := sendParams{c: newTestClient(ts), channelVersion: "2.4.6", logger: discardLogger()}
	if err := p.withRunID("run-42").text(context.Background(), "u", "ctx", "hi"); err != nil {
		t.Fatal(err)
	}
	if got != "run-42" {
		t.Errorf("run_id = %q, want run-42", got)
	}
}

// ─── Typing ticket cache ─────────────────────────────────────────────────────

func TestTypingTicketCachedAcrossCalls(t *testing.T) {
	var configCalls int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "getconfig") {
			configCalls++
			writeJSON(w, getConfigResponse{TypingTicket: "ticket-1"})
			return
		}
		writeJSON(w, sendTypingResponse{})
	}))
	defer ts.Close()

	tm := newTypingManager(newTestClient(ts), discardLogger(), "")
	for i := 0; i < 3; i++ {
		if err := tm.StartTyping(context.Background(), "u", "ctx"); err != nil {
			t.Fatal(err)
		}
	}
	if configCalls != 1 {
		t.Errorf("getconfig called %d times, want 1 (cached)", configCalls)
	}
}

// A failed refresh must not throw away a ticket that still works.
func TestTypingTicketSurvivesFailedRefresh(t *testing.T) {
	var configCalls int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "getconfig") {
			configCalls++
			if configCalls == 1 {
				writeJSON(w, getConfigResponse{TypingTicket: "ticket-1"})
				return
			}
			writeJSON(w, getConfigResponse{Ret: -1, ErrMsg: "backend down"})
			return
		}
		writeJSON(w, sendTypingResponse{})
	}))
	defer ts.Close()

	tm := newTypingManager(newTestClient(ts), discardLogger(), "")
	if got := tm.getTicket(context.Background(), "u", "ctx"); got != "ticket-1" {
		t.Fatalf("first ticket = %q", got)
	}

	// Force the entry due for refresh; the refresh fails.
	tm.mu.Lock()
	tm.entries["u"].nextFetchAt = time.Now().Add(-time.Minute)
	tm.mu.Unlock()

	if got := tm.getTicket(context.Background(), "u", "ctx"); got != "ticket-1" {
		t.Errorf("ticket after a failed refresh = %q, want the cached ticket-1", got)
	}
}

func TestTypingTicketBackoffGrows(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, getConfigResponse{Ret: -1, ErrMsg: "down"})
	}))
	defer ts.Close()

	tm := newTypingManager(newTestClient(ts), discardLogger(), "")
	tm.getTicket(context.Background(), "u", "ctx")

	tm.mu.Lock()
	first := tm.entries["u"].retryDelay
	tm.entries["u"].nextFetchAt = time.Now().Add(-time.Minute)
	tm.mu.Unlock()

	tm.getTicket(context.Background(), "u", "ctx")

	tm.mu.Lock()
	second := tm.entries["u"].retryDelay
	tm.mu.Unlock()

	if second <= first {
		t.Errorf("retry delay did not grow: %v then %v", first, second)
	}
	if second > configMaxRetry {
		t.Errorf("retry delay %v exceeds the cap %v", second, configMaxRetry)
	}
}

func TestSendTypingIncludesBaseInfo(t *testing.T) {
	var sawBaseInfo bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "getconfig") {
			writeJSON(w, getConfigResponse{TypingTicket: "t"})
			return
		}
		body, _ := io.ReadAll(r.Body)
		sawBaseInfo = strings.Contains(string(body), `"base_info"`)
		writeJSON(w, sendTypingResponse{})
	}))
	defer ts.Close()

	tm := newTypingManager(newTestClient(ts), discardLogger(), "MyBot/1.0")
	if err := tm.StartTyping(context.Background(), "u", "ctx"); err != nil {
		t.Fatal(err)
	}
	if !sawBaseInfo {
		t.Error("sendtyping request carried no base_info")
	}
}

// ─── Headers ─────────────────────────────────────────────────────────────────

func TestDefaultAppIDHeaderIsSent(t *testing.T) {
	var appID, clientVersion string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		appID = r.Header.Get("iLink-App-Id")
		clientVersion = r.Header.Get("iLink-App-ClientVersion")
		writeJSON(w, SendMessageResponse{})
	}))
	defer ts.Close()

	bot := NewBot(
		WithBaseURL(ts.URL),
		WithHTTPClient(ts.Client()),
		WithLogger(discardLogger()),
		WithTokenStore(NewMemTokenStore()),
		WithContextTokenStore(NewMemContextTokenStore()),
	)
	_ = bot.SetContextToken("u", "ctx")
	if err := bot.SendText(context.Background(), "u", "hi"); err != nil {
		t.Fatal(err)
	}
	if appID != defaultAppID {
		t.Errorf("iLink-App-Id = %q, want %q", appID, defaultAppID)
	}
	if clientVersion == "" || clientVersion == "0" {
		t.Errorf("iLink-App-ClientVersion = %q, want a non-zero encoding", clientVersion)
	}
}

func TestBuildClientVersion(t *testing.T) {
	// 2.4.6 -> 0x000204 06 = 132102
	if got := buildClientVersion("2.4.6"); got != "132102" {
		t.Errorf("buildClientVersion(2.4.6) = %s, want 132102", got)
	}
	if got := buildClientVersion("1.0.11"); got != "65547" {
		t.Errorf("buildClientVersion(1.0.11) = %s, want 65547", got)
	}
}

func TestGetRequestsCarryNoAuthorization(t *testing.T) {
	var auth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		writeJSON(w, map[string]any{})
	}))
	defer ts.Close()

	c := newTestClient(ts)
	c.setToken("some-token")
	var out map[string]any
	if err := c.get(context.Background(), "/whatever", &out); err != nil {
		t.Fatal(err)
	}
	if auth != "" {
		t.Errorf("GET carried Authorization = %q, want empty", auth)
	}
}

// ─── MIME ────────────────────────────────────────────────────────────────────

func TestMIMEFromFilename(t *testing.T) {
	tests := map[string]string{
		"report.PDF":  "application/pdf",
		"photo.jpeg":  "image/jpeg",
		"clip.mp4":    "video/mp4",
		"notes":       DefaultMIME,
		"weird.xyzzy": DefaultMIME,
	}
	for name, want := range tests {
		if got := MIMEFromFilename(name); got != want {
			t.Errorf("MIMEFromFilename(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestExtensionFromContentTypeOrURL(t *testing.T) {
	if got := ExtensionFromContentTypeOrURL("image/png; charset=binary", ""); got != ".png" {
		t.Errorf("got %q, want .png", got)
	}
	if got := ExtensionFromContentTypeOrURL("", "https://cdn.example.com/a/b/file.mp4?x=1"); got != ".mp4" {
		t.Errorf("got %q, want .mp4", got)
	}
	if got := ExtensionFromContentTypeOrURL("application/unknown", "https://x/y"); got != ".bin" {
		t.Errorf("got %q, want .bin", got)
	}
}

func TestDetectMIME(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{"jpeg", []byte{0xFF, 0xD8, 0xFF, 0xE0}, "image/jpeg"},
		{"png", []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, "image/png"},
		{"silk", append([]byte{0x02}, []byte("#!SILK_V3")...), "audio/silk"},
		{"bare silk", []byte("#!SILK_V3rest"), "audio/silk"},
		{"unknown", []byte("hello world"), ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DetectMIME(tc.data); got != tc.want {
				t.Errorf("DetectMIME = %q, want %q", got, tc.want)
			}
		})
	}
}

// ─── Voice transcoding ───────────────────────────────────────────────────────

func TestIsSilkAndStripPrefix(t *testing.T) {
	withPrefix := append([]byte{0x02}, []byte("#!SILK_V3data")...)
	if !IsSilk(withPrefix) {
		t.Error("IsSilk = false for a WeChat-prefixed payload")
	}
	stripped := StripSilkPrefix(withPrefix)
	if stripped[0] != '#' {
		t.Errorf("StripSilkPrefix left %q at the head", stripped[0])
	}
	// Stripping is idempotent for an already-bare payload.
	if got := StripSilkPrefix(stripped); got[0] != '#' {
		t.Error("StripSilkPrefix mangled a bare payload")
	}
	if IsSilk([]byte("not silk at all")) {
		t.Error("IsSilk = true for a non-SILK payload")
	}
}

func TestPCMToWAVHeader(t *testing.T) {
	pcm := make([]byte, 100)
	wav := PCMToWAV(pcm, 24000)

	if len(wav) != 44+len(pcm) {
		t.Fatalf("wav length = %d, want %d", len(wav), 44+len(pcm))
	}
	if string(wav[0:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		t.Error("missing RIFF/WAVE markers")
	}
	if got := binary.LittleEndian.Uint32(wav[24:28]); got != 24000 {
		t.Errorf("sample rate = %d, want 24000", got)
	}
	if got := binary.LittleEndian.Uint16(wav[22:24]); got != 1 {
		t.Errorf("channels = %d, want 1 (mono)", got)
	}
	if got := binary.LittleEndian.Uint16(wav[34:36]); got != 16 {
		t.Errorf("bits per sample = %d, want 16", got)
	}
	if got := binary.LittleEndian.Uint32(wav[40:44]); got != uint32(len(pcm)) {
		t.Errorf("data chunk size = %d, want %d", got, len(pcm))
	}
}

func TestPCMToWAVDefaultsSampleRate(t *testing.T) {
	wav := PCMToWAV(make([]byte, 8), 0)
	if got := binary.LittleEndian.Uint32(wav[24:28]); got != DefaultVoiceSampleRate {
		t.Errorf("sample rate = %d, want the default %d", got, DefaultVoiceSampleRate)
	}
}

func TestVoiceTranscoderFuncAdapter(t *testing.T) {
	var gotRate int
	tc := VoiceTranscoderFunc(func(silk []byte, rate int) ([]byte, error) {
		gotRate = rate
		return PCMToWAV(silk, rate), nil
	})
	if _, err := tc.ToWAV([]byte{1, 2}, 16000); err != nil {
		t.Fatal(err)
	}
	if gotRate != 16000 {
		t.Errorf("sample rate = %d, want 16000", gotRate)
	}
}

// ─── Sync buf fallback ───────────────────────────────────────────────────────

func TestSyncBufFallsBackToLegacyPath(t *testing.T) {
	dir := t.TempDir()
	legacy := dir + "/legacy.json"
	current := dir + "/nested/current.json"

	if err := (&FileSyncBufStore{path: legacy}).Save("cursor-from-legacy"); err != nil {
		t.Fatal(err)
	}

	store := NewFileSyncBufStoreWithFallback(current, legacy)
	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got != "cursor-from-legacy" {
		t.Errorf("Load() = %q, want the legacy cursor", got)
	}

	// Saving migrates the value, creating the parent directory on the way.
	if err := store.Save("cursor-new"); err != nil {
		t.Fatal(err)
	}
	got, _ = store.Load()
	if got != "cursor-new" {
		t.Errorf("after Save, Load() = %q, want cursor-new", got)
	}
}

func TestSyncBufMissingEverywhereIsNotAnError(t *testing.T) {
	store := NewFileSyncBufStoreWithFallback(t.TempDir()+"/none.json", t.TempDir()+"/also-none.json")
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if got != "" {
		t.Errorf("Load() = %q, want empty", got)
	}
}

// ─── Slash commands ──────────────────────────────────────────────────────────

func TestSplitCommand(t *testing.T) {
	tests := []struct{ in, cmd, args string }{
		{"/echo hello world", "/echo", "hello world"},
		{"/toggle-debug", "/toggle-debug", ""},
		{"/ECHO Mixed", "/echo", "Mixed"},
		{"/echo   padded  ", "/echo", "padded"},
	}
	for _, tc := range tests {
		cmd, args := splitCommand(strings.TrimSpace(tc.in))
		if cmd != tc.cmd || args != tc.args {
			t.Errorf("splitCommand(%q) = (%q, %q), want (%q, %q)", tc.in, cmd, args, tc.cmd, tc.args)
		}
	}
}

func TestSlashCommandsInterceptsAndAborts(t *testing.T) {
	bot, ts, sent := newTestBot(t)
	defer ts.Close()

	var reachedAI bool
	bot.Use(Timing(), SlashCommands())
	bot.OnBody(func(c *Context) { reachedAI = true })

	dispatch(bot, &Message{
		FromUserID:   "u",
		ContextToken: "ctx",
		MessageType:  MessageTypeUser,
		ItemList:     []MessageItem{{Type: ItemTypeText, TextItem: &TextItem{Text: "/echo hi"}}},
	})

	if reachedAI {
		t.Error("a slash command reached the AI handler; it should have aborted the chain")
	}
	if len(*sent) < 2 {
		t.Fatalf("expected an echo plus a timing reply, got %d messages", len(*sent))
	}
	if (*sent)[0] != "hi" {
		t.Errorf("first reply = %q, want the echoed text", (*sent)[0])
	}
	if !strings.Contains((*sent)[1], "通道耗时") {
		t.Errorf("second reply = %q, want the timing breakdown", (*sent)[1])
	}
}

func TestSlashCommandsPassesThroughNormalText(t *testing.T) {
	bot, ts, _ := newTestBot(t)
	defer ts.Close()

	var reached bool
	bot.Use(SlashCommands())
	bot.OnBody(func(c *Context) { reached = true })

	dispatch(bot, &Message{
		FromUserID:  "u",
		MessageType: MessageTypeUser,
		ItemList:    []MessageItem{{Type: ItemTypeText, TextItem: &TextItem{Text: "今天天气怎么样"}}},
	})
	if !reached {
		t.Error("normal text did not reach the handler")
	}
}

func TestSlashCommandsUnknownCommandFallsThrough(t *testing.T) {
	bot, ts, _ := newTestBot(t)
	defer ts.Close()

	var reached bool
	bot.Use(SlashCommands())
	bot.OnBody(func(c *Context) { reached = true })

	dispatch(bot, &Message{
		FromUserID:  "u",
		MessageType: MessageTypeUser,
		ItemList:    []MessageItem{{Type: ItemTypeText, TextItem: &TextItem{Text: "/not-a-command"}}},
	})
	if !reached {
		t.Error("an unknown slash command should fall through to the handlers")
	}
}

func TestToggleDebugFlipsPerUserState(t *testing.T) {
	bot, ts, _ := newTestBot(t)
	defer ts.Close()

	var states []bool
	bot.Use(SlashCommands())
	bot.OnBody(func(c *Context) { states = append(states, c.DebugEnabled()) })

	toggle := &Message{
		FromUserID:  "u",
		MessageType: MessageTypeUser,
		ItemList:    []MessageItem{{Type: ItemTypeText, TextItem: &TextItem{Text: "/toggle-debug"}}},
	}
	normal := &Message{
		FromUserID:  "u",
		MessageType: MessageTypeUser,
		ItemList:    []MessageItem{{Type: ItemTypeText, TextItem: &TextItem{Text: "hello"}}},
	}

	dispatch(bot, normal)
	dispatch(bot, toggle)
	dispatch(bot, normal)
	dispatch(bot, toggle)
	dispatch(bot, normal)

	want := []bool{false, true, false}
	if len(states) != len(want) {
		t.Fatalf("collected %v, want %v", states, want)
	}
	for i := range want {
		if states[i] != want[i] {
			t.Errorf("state[%d] = %v, want %v", i, states[i], want[i])
		}
	}
}

func TestSlashCommandsCustomCommand(t *testing.T) {
	bot, ts, sent := newTestBot(t)
	defer ts.Close()

	bot.Use(SlashCommands(SlashCommandOptions{
		Commands: map[string]SlashCommandFunc{
			"/status": func(c *Context, args string) error {
				return c.ReplyText("ok:" + args)
			},
		},
	}))

	dispatch(bot, &Message{
		FromUserID:   "u",
		ContextToken: "ctx",
		MessageType:  MessageTypeUser,
		ItemList:     []MessageItem{{Type: ItemTypeText, TextItem: &TextItem{Text: "/status verbose"}}},
	})

	if len(*sent) != 1 || (*sent)[0] != "ok:verbose" {
		t.Errorf("replies = %v, want [ok:verbose]", *sent)
	}
}

// ─── Tool progress ───────────────────────────────────────────────────────────

func TestToolProgressSendsStartThenResultInOrder(t *testing.T) {
	var mu sync.Mutex
	var types []ItemType
	var runIDs []string
	ts := newRecordingSendServer(t, func(msg *Message) {
		mu.Lock()
		defer mu.Unlock()
		types = append(types, msg.ItemList[0].Type)
		runIDs = append(runIDs, msg.RunID)
	})
	defer ts.Close()

	bot := newBotForServer(t, ts)
	tp := bot.NewToolProgress(context.Background(), "u", "ctx", "run-7")
	tp.Start("web_search", "call-1")
	tp.End("web_search", "call-1", ToolCallStatusCompleted)
	tp.Finalize()

	mu.Lock()
	defer mu.Unlock()
	if len(types) != 2 {
		t.Fatalf("sent %d messages, want 2", len(types))
	}
	if types[0] != ItemTypeToolCallStart || types[1] != ItemTypeToolCallResult {
		t.Errorf("item types = %v, want [11 12] in order", types)
	}
	for _, id := range runIDs {
		if id != "run-7" {
			t.Errorf("run_id = %q, want run-7", id)
		}
	}
}

func TestToolProgressTrackReportsFailure(t *testing.T) {
	var mu sync.Mutex
	var status string
	ts := newRecordingSendServer(t, func(msg *Message) {
		if msg.ItemList[0].Type == ItemTypeToolCallResult {
			mu.Lock()
			status = msg.ItemList[0].ToolCallResultItem.Status
			mu.Unlock()
		}
	})
	defer ts.Close()

	bot := newBotForServer(t, ts)
	tp := bot.NewToolProgress(context.Background(), "u", "ctx", "run-8")
	wantErr := errors.New("tool blew up")
	gotErr := tp.Track("db_query", "call-2", func() error { return wantErr })
	tp.Finalize()

	if !errors.Is(gotErr, wantErr) {
		t.Errorf("Track returned %v, want the tool's error", gotErr)
	}
	mu.Lock()
	defer mu.Unlock()
	if status != ToolCallStatusFailed {
		t.Errorf("status = %q, want %q", status, ToolCallStatusFailed)
	}
}

func TestToolProgressFinalizeIsIdempotent(t *testing.T) {
	ts := newRecordingSendServer(t, func(*Message) {})
	defer ts.Close()

	bot := newBotForServer(t, ts)
	tp := bot.NewToolProgress(context.Background(), "u", "ctx", "run-9")
	tp.Finalize()
	tp.Finalize() // must not panic on a closed channel
	tp.Start("late", "call-3")
	tp.Finalize()
}

// ─── Test helpers ────────────────────────────────────────────────────────────

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTestClient(ts *httptest.Server) *client {
	return newClient(ts.URL, ts.Client(), "2.4.6", defaultAppID, "", discardLogger())
}

// newRecordingSendServer serves sendmessage, handing each decoded message to fn.
func newRecordingSendServer(t *testing.T, fn func(*Message)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req SendMessageRequest
		if err := decodeJSON(r, &req); err != nil {
			t.Errorf("decode sendmessage: %v", err)
		}
		if req.Msg != nil {
			fn(req.Msg)
		}
		writeJSON(w, SendMessageResponse{})
	}))
}

func decodeJSON(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}

func newBotForServer(t *testing.T, ts *httptest.Server) *Bot {
	t.Helper()
	return NewBot(
		WithBaseURL(ts.URL),
		WithHTTPClient(ts.Client()),
		WithLogger(discardLogger()),
		WithTokenStore(NewMemTokenStore()),
		WithContextTokenStore(NewMemContextTokenStore()),
	)
}

// newTestBot returns a bot wired to a server that records every reply's text.
func newTestBot(t *testing.T) (*Bot, *httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	sent := &[]string{}
	ts := newRecordingSendServer(t, func(msg *Message) {
		mu.Lock()
		defer mu.Unlock()
		for _, item := range msg.ItemList {
			if item.TextItem != nil {
				*sent = append(*sent, item.TextItem.Text)
			}
		}
	})
	return newBotForServer(t, ts), ts, sent
}

// dispatch runs a message through the bot's handler chain synchronously.
func dispatch(bot *Bot, msg *Message) {
	bot.dispatcher.dispatch(newContext(context.Background(), msg, bot, nil))
}
