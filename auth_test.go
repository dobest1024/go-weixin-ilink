package ilink

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// qrServer is a scriptable stand-in for the iLink login endpoints.
type qrServer struct {
	t *testing.T

	mu sync.Mutex
	// statuses is consumed one entry per get_qrcode_status call.
	statuses []qrCodeStatus
	// qrCodes is consumed one entry per get_bot_qrcode call.
	qrCodes []string

	// Recorded requests.
	qrRequests     []qrCodeRequest
	qrAuthHeaders  []string
	qrMethods      []string
	statusHosts    []string
	statusVerifies []string
}

func (s *qrServer) nextQRCode() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.qrCodes) == 0 {
		return "qr-default"
	}
	code := s.qrCodes[0]
	s.qrCodes = s.qrCodes[1:]
	return code
}

func (s *qrServer) nextStatus() qrCodeStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.statuses) == 0 {
		return qrCodeStatus{Status: qrStatusWait}
	}
	st := s.statuses[0]
	s.statuses = s.statuses[1:]
	return st
}

func (s *qrServer) handler(host string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ilink/bot/get_bot_qrcode":
			body, _ := io.ReadAll(r.Body)
			var req qrCodeRequest
			_ = json.Unmarshal(body, &req)
			s.mu.Lock()
			s.qrRequests = append(s.qrRequests, req)
			s.qrMethods = append(s.qrMethods, r.Method)
			s.qrAuthHeaders = append(s.qrAuthHeaders, r.Header.Get("Authorization"))
			s.mu.Unlock()
			writeJSON(w, qrCodeResponse{
				QRCode:           s.nextQRCode(),
				QRCodeImgContent: "https://example.invalid/qr.png",
			})

		case "/ilink/bot/get_qrcode_status":
			s.mu.Lock()
			s.statusHosts = append(s.statusHosts, host)
			s.statusVerifies = append(s.statusVerifies, r.URL.Query().Get("verify_code"))
			s.mu.Unlock()
			writeJSON(w, s.nextStatus())

		case "/ilink/bot/getupdates":
			writeJSON(w, GetUpdatesResponse{Ret: 0})

		default:
			s.t.Errorf("unexpected request path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// newTestAuth wires an auth against a test server, with polling fast enough
// that the tests stay quick.
func newTestAuth(t *testing.T, srv *qrServer, store TokenStore) (*auth, *httptest.Server) {
	t.Helper()
	ts := httptest.NewServer(nil)
	ts.Config.Handler = srv.handler(ts.URL)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	c := newClient(ts.URL, ts.Client(), "2.4.6", defaultAppID, "", logger)
	a := &auth{
		c:            c,
		store:        store,
		logger:       logger,
		botType:      defaultBotType,
		loginBaseURL: ts.URL,
	}
	t.Cleanup(ts.Close)
	return a, ts
}

// withFastPolling shrinks the poll interval for the duration of a test.
func withFastPolling(t *testing.T) {
	t.Helper()
	orig := testQRPollInterval
	testQRPollInterval = time.Millisecond
	t.Cleanup(func() { testQRPollInterval = orig })
}

func TestFetchQRCodeUsesPostWithLocalTokens(t *testing.T) {
	withFastPolling(t)
	srv := &qrServer{t: t}
	store := NewMemTokenStore()
	if err := store.Save("stored-token-abcdef", "https://old.example.invalid"); err != nil {
		t.Fatal(err)
	}
	a, _ := newTestAuth(t, srv, store)

	if _, err := a.fetchQRCode(context.Background()); err != nil {
		t.Fatalf("fetchQRCode: %v", err)
	}

	srv.mu.Lock()
	defer srv.mu.Unlock()
	if got := srv.qrMethods[0]; got != http.MethodPost {
		t.Errorf("get_bot_qrcode method = %q, want POST", got)
	}
	if got := srv.qrRequests[0].LocalTokenList; len(got) != 1 || got[0] != "stored-token-abcdef" {
		t.Errorf("local_token_list = %v, want [stored-token-abcdef]", got)
	}
	// The login endpoints are pre-auth: a stale bearer token must not be sent.
	if got := srv.qrAuthHeaders[0]; got != "" {
		t.Errorf("Authorization header = %q, want empty", got)
	}
}

func TestLoginFollowsIDCRedirect(t *testing.T) {
	withFastPolling(t)

	// The redirect target is a second server; only it reports "confirmed".
	redirectSrv := &qrServer{t: t, statuses: []qrCodeStatus{
		{Status: qrStatusConfirmed, BotToken: "new-token", IlinkBotID: "bot-1"},
	}}
	redirectTS := httptest.NewServer(nil)
	redirectTS.Config.Handler = redirectSrv.handler(redirectTS.URL)
	defer redirectTS.Close()

	redirectHost := redirectTS.Listener.Addr().String()

	srv := &qrServer{t: t, statuses: []qrCodeStatus{
		{Status: qrStatusWait},
		{Status: qrStatusScannedButRedirect, RedirectHost: redirectHost},
		// If the flow ignored the redirect it would keep reading from here and
		// never see a confirmation.
		{Status: qrStatusWait},
		{Status: qrStatusWait},
	}}
	store := NewMemTokenStore()
	a, _ := newTestAuth(t, srv, store)

	// The redirect host is plain HTTP in the test; rewrite the scheme the flow
	// would normally build so the request reaches the second server.
	a.redirectScheme = "http://"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.Login(ctx, func(string) {}); err != nil {
		t.Fatalf("Login: %v", err)
	}

	if got := a.c.getToken(); got != "new-token" {
		t.Errorf("token = %q, want new-token", got)
	}
	redirectSrv.mu.Lock()
	polls := len(redirectSrv.statusHosts)
	redirectSrv.mu.Unlock()
	if polls == 0 {
		t.Error("no status polls reached the redirect host; IDC redirect was ignored")
	}
}

func TestLoginSubmitsVerifyCode(t *testing.T) {
	withFastPolling(t)
	srv := &qrServer{t: t, statuses: []qrCodeStatus{
		{Status: qrStatusScanned},
		{Status: qrStatusNeedVerifyCode},
		{Status: qrStatusScanned},
		{Status: qrStatusConfirmed, BotToken: "verified-token", IlinkBotID: "bot-2"},
	}}
	a, _ := newTestAuth(t, srv, NewMemTokenStore())

	var prompts []bool
	a.verifyCode = func(retry bool) (string, error) {
		prompts = append(prompts, retry)
		return "1234", nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.Login(ctx, func(string) {}); err != nil {
		t.Fatalf("Login: %v", err)
	}

	if len(prompts) != 1 || prompts[0] {
		t.Errorf("verify code prompts = %v, want one non-retry prompt", prompts)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	var sawCode bool
	for _, v := range srv.statusVerifies {
		if v == "1234" {
			sawCode = true
		}
	}
	if !sawCode {
		t.Errorf("verify_code was never sent, saw %v", srv.statusVerifies)
	}
}

func TestLoginPromptsAgainWhenVerifyCodeRejected(t *testing.T) {
	withFastPolling(t)
	srv := &qrServer{t: t, statuses: []qrCodeStatus{
		{Status: qrStatusNeedVerifyCode},
		// Server still unhappy: the same state repeats, meaning "wrong code".
		{Status: qrStatusNeedVerifyCode},
		{Status: qrStatusConfirmed, BotToken: "t", IlinkBotID: "bot-3"},
	}}
	a, _ := newTestAuth(t, srv, NewMemTokenStore())

	var prompts []bool
	a.verifyCode = func(retry bool) (string, error) {
		prompts = append(prompts, retry)
		return "9999", nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.Login(ctx, func(string) {}); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if len(prompts) != 2 {
		t.Fatalf("prompts = %v, want 2", prompts)
	}
	if prompts[0] {
		t.Error("first prompt should not be flagged as a retry")
	}
	if !prompts[1] {
		t.Error("second prompt should be flagged as a retry")
	}
}

func TestLoginNoVerifyCodeFunc(t *testing.T) {
	withFastPolling(t)
	srv := &qrServer{t: t, statuses: []qrCodeStatus{{Status: qrStatusNeedVerifyCode}}}
	a, _ := newTestAuth(t, srv, NewMemTokenStore())
	a.verifyCode = nil

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := a.Login(ctx, func(string) {})
	if !errors.Is(err, ErrNoVerifyCodeFunc) {
		t.Errorf("err = %v, want ErrNoVerifyCodeFunc", err)
	}
}

func TestLoginBindedRedirectReusesStoredCredentials(t *testing.T) {
	withFastPolling(t)
	srv := &qrServer{t: t, statuses: []qrCodeStatus{{Status: qrStatusBindedRedirect}}}
	store := NewMemTokenStore()
	a, ts := newTestAuth(t, srv, store)
	// Save credentials pointing at the test server so validation succeeds.
	if err := store.Save("already-bound-token", ts.URL); err != nil {
		t.Fatal(err)
	}
	// Force the QR path: pretend validation failed the first time by clearing
	// the client token, leaving the store populated.
	a.c.setToken("")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := a.runQRFlow(ctx, &qrCodeResponse{QRCode: "qr-1"}, nil, nil)
	if err != nil {
		t.Fatalf("runQRFlow: %v", err)
	}
	if !res.alreadyBound {
		t.Fatal("binded_redirect should report alreadyBound, not a login failure")
	}
}

func TestLoginBindedRedirectWithoutCredentials(t *testing.T) {
	withFastPolling(t)
	srv := &qrServer{t: t, statuses: []qrCodeStatus{{Status: qrStatusBindedRedirect}}}
	a, _ := newTestAuth(t, srv, NewMemTokenStore())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := a.Login(ctx, func(string) {})
	if !errors.Is(err, ErrAlreadyBound) {
		t.Errorf("err = %v, want ErrAlreadyBound", err)
	}
}

func TestLoginRefreshesExpiredQRCode(t *testing.T) {
	withFastPolling(t)
	srv := &qrServer{
		t:       t,
		qrCodes: []string{"qr-1", "qr-2"},
		statuses: []qrCodeStatus{
			{Status: qrStatusExpired},
			{Status: qrStatusConfirmed, BotToken: "after-refresh", IlinkBotID: "bot-4"},
		},
	}
	a, _ := newTestAuth(t, srv, NewMemTokenStore())

	var shown []string
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.Login(ctx, func(img string) { shown = append(shown, img) }); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if len(shown) != 2 {
		t.Errorf("QR callback invoked %d times, want 2 (original + refreshed)", len(shown))
	}
	if got := a.c.getToken(); got != "after-refresh" {
		t.Errorf("token = %q, want after-refresh", got)
	}
}

func TestLoginGivesUpAfterMaxRefreshes(t *testing.T) {
	withFastPolling(t)
	statuses := make([]qrCodeStatus, 0, maxQRRefresh+2)
	for i := 0; i < maxQRRefresh+2; i++ {
		statuses = append(statuses, qrCodeStatus{Status: qrStatusExpired})
	}
	srv := &qrServer{t: t, statuses: statuses}
	a, _ := newTestAuth(t, srv, NewMemTokenStore())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := a.Login(ctx, func(string) {})
	if !errors.Is(err, ErrQRCodeExpired) {
		t.Errorf("err = %v, want ErrQRCodeExpired", err)
	}
}

func TestLoginVerifyCodeBlockedRefreshesThenFails(t *testing.T) {
	withFastPolling(t)
	statuses := make([]qrCodeStatus, 0, maxQRRefresh+2)
	for i := 0; i < maxQRRefresh+2; i++ {
		statuses = append(statuses, qrCodeStatus{Status: qrStatusVerifyCodeBlocked})
	}
	srv := &qrServer{t: t, statuses: statuses}
	a, _ := newTestAuth(t, srv, NewMemTokenStore())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := a.Login(ctx, func(string) {})
	if !errors.Is(err, ErrVerifyCodeBlocked) {
		t.Errorf("err = %v, want ErrVerifyCodeBlocked", err)
	}
}
