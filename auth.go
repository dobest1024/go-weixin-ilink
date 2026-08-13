package ilink

import (
	"context"
	"log/slog"
	"time"
)

// QRCallback is called with the base64-encoded QR code image when a scan is needed.
// Users can render it with qrterminal or save it as a PNG file.
// It is called again with a fresh code whenever the previous one expires.
type QRCallback func(qrImgContent string)

type getConfigResponse struct {
	Ret          int    `json:"ret"`
	ErrCode      int    `json:"errcode,omitempty"`
	ErrMsg       string `json:"errmsg,omitempty"`
	TypingTicket string `json:"typing_ticket"`
}

type auth struct {
	c      *client
	store  TokenStore
	logger *slog.Logger

	botType     string
	verifyCode  VerifyCodeFunc
	localTokens func() []string

	// loginBaseURL is the fixed entry point for QR issuance. Unlike the message
	// API base URL, it never follows the per-account baseurl the server returns.
	loginBaseURL string

	// redirectScheme prefixes the bare host the server returns in
	// scaned_but_redirect. Overridden only by tests.
	redirectScheme string
}

func newAuth(c *client, store TokenStore, logger *slog.Logger, cfg *config) *auth {
	return &auth{
		c:              c,
		store:          store,
		logger:         logger,
		botType:        cfg.botType,
		verifyCode:     cfg.verifyCode,
		localTokens:    cfg.localTokens,
		loginBaseURL:   cfg.baseURL,
		redirectScheme: "https://",
	}
}

// testQRPollInterval is the delay between QR status polls. It is a var so
// tests can shrink it; production code never changes it.
var testQRPollInterval = time.Second

// Login performs the full login flow:
//  1. Load & validate existing credentials from the store.
//  2. If valid, reuse them without showing a QR code.
//  3. Otherwise, display a QR code and drive the scan state machine to completion.
func (a *auth) Login(ctx context.Context, onQR QRCallback) error {
	if ok, err := a.tryStoredCredentials(ctx); ok {
		return nil
	} else if err != nil {
		a.logger.Debug("stored credentials unusable", "error", err)
	}

	qr, err := a.fetchQRCode(ctx)
	if err != nil {
		return err
	}
	a.presentQR(qr, onQR)

	res, err := a.runQRFlow(ctx, qr, onQR, nil)
	if err != nil {
		return err
	}
	if res.alreadyBound {
		// The server recognised one of our local tokens: nothing to save, and
		// whatever the store already holds is still valid.
		if ok, _ := a.tryStoredCredentials(ctx); ok {
			return nil
		}
		return ErrAlreadyBound
	}
	a.applyCredentials(res.status)
	return nil
}

// qrResult is the outcome of a completed QR state machine run.
type qrResult struct {
	status       *qrCodeStatus
	alreadyBound bool
}

// runQRFlow drives the QR login state machine until it terminates.
//
// It owns three pieces of state the old single-branch loop had no room for:
// the host status polls go to (which the server can move mid-scan), the pair
// code awaiting submission, and how many times the code has been refreshed.
// onStatus, when non-nil, is notified of each scan-state transition so an
// asynchronous caller can drive a UI.
func (a *auth) runQRFlow(ctx context.Context, qr *qrCodeResponse, onQR QRCallback, onStatus func(LoginStatus)) (*qrResult, error) {
	notify := func(st LoginStatus) {
		if onStatus != nil {
			onStatus(st)
		}
	}
	pollBaseURL := a.loginBaseURL
	qrcode := qr.QRCode
	var pendingVerifyCode string
	var verifyCodeSubmitted bool
	refreshCount := 0
	scannedLogged := false

	// refresh swaps in a new QR code, resetting the per-code state.
	refresh := func(reason string) error {
		refreshCount++
		if refreshCount > maxQRRefresh {
			return ErrQRCodeExpired
		}
		a.logger.Info("refreshing QR code", "reason", reason,
			"attempt", refreshCount, "max", maxQRRefresh)
		next, err := a.fetchQRCode(ctx)
		if err != nil {
			return err
		}
		qrcode = next.QRCode
		pollBaseURL = a.loginBaseURL
		pendingVerifyCode = ""
		verifyCodeSubmitted = false
		scannedLogged = false
		a.presentQR(next, onQR)
		return nil
	}

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		status, err := a.pollQRStatus(ctx, pollBaseURL, qrcode, pendingVerifyCode)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			// A gateway timeout mid-long-poll is routine; keep polling.
			a.logger.Warn("poll qr status error, retrying",
				"error", err, "net_error", ClassifyNetError(err))
			if err := sleepCtx(ctx, testQRPollInterval); err != nil {
				return nil, err
			}
			continue
		}

		switch status.Status {
		case qrStatusWait:
			// Nothing to do; fall through to the inter-poll delay.

		case qrStatusScanned:
			// Reaching "scaned" after submitting a code means it was accepted.
			if pendingVerifyCode != "" {
				a.logger.Info("pair code accepted")
				pendingVerifyCode = ""
			}
			if !scannedLogged {
				a.logger.Info("QR code scanned, waiting for phone confirmation...")
				scannedLogged = true
				notify(LoginStatusScanned)
			}

		case qrStatusNeedVerifyCode:
			if a.verifyCode == nil {
				return nil, ErrNoVerifyCodeFunc
			}
			notify(LoginStatusNeedVerifyCode)
			code, err := a.verifyCode(verifyCodeSubmitted)
			if err != nil {
				return nil, err
			}
			pendingVerifyCode = code
			verifyCodeSubmitted = true
			// Submit immediately rather than waiting out the poll interval.
			continue

		case qrStatusVerifyCodeBlocked:
			a.logger.Warn("pair code blocked after repeated wrong entries")
			if err := refresh("verify_code_blocked"); err != nil {
				if err == ErrQRCodeExpired {
					return nil, ErrVerifyCodeBlocked
				}
				return nil, err
			}

		case qrStatusExpired:
			if err := refresh("expired"); err != nil {
				return nil, err
			}

		case qrStatusScannedButRedirect:
			// The account is homed in a different IDC. Every later poll for this
			// login must go to redirect_host, otherwise the server there never
			// reports "confirmed" and the login hangs until it times out.
			if status.RedirectHost != "" {
				pollBaseURL = a.scheme() + status.RedirectHost
				a.logger.Info("IDC redirect, switching QR polling host", "host", status.RedirectHost)
			} else {
				a.logger.Warn("scaned_but_redirect without redirect_host, keeping current host")
			}

		case qrStatusBindedRedirect:
			a.logger.Info("bot is already connected to this client, no re-connect needed")
			return &qrResult{alreadyBound: true}, nil

		case qrStatusConfirmed:
			return &qrResult{status: status}, nil

		default:
			a.logger.Warn("unknown qr status, continuing", "status", status.Status)
		}

		if err := sleepCtx(ctx, testQRPollInterval); err != nil {
			return nil, err
		}
	}
}

// scheme returns the URL scheme used for a redirect host.
func (a *auth) scheme() string {
	if a.redirectScheme == "" {
		return "https://"
	}
	return a.redirectScheme
}

// presentQR hands a QR code to the caller's renderer.
func (a *auth) presentQR(qr *qrCodeResponse, onQR QRCallback) {
	if onQR != nil {
		onQR(qr.QRCodeImgContent)
		return
	}
	a.logger.Info("QR code ready", "url", qr.QRCodeImgURL)
}

// tryStoredCredentials loads and validates persisted credentials.
// Returns ok=true when they were adopted and no QR scan is needed.
func (a *auth) tryStoredCredentials(ctx context.Context) (bool, error) {
	if a.store == nil {
		return false, nil
	}
	token, baseURL, err := a.store.Load()
	if err != nil {
		a.logger.Warn("failed to load stored credentials", "error", err)
		return false, err
	}
	if token == "" {
		return false, nil
	}

	a.c.setToken(token)
	if baseURL != "" {
		a.c.setBaseURL(baseURL)
	}
	a.logger.Info("validating stored credentials...")

	valid, vErr := a.validate(ctx)
	if valid {
		a.logger.Info("reusing stored credentials")
		return true, nil
	}
	if IsSessionExpired(vErr) {
		a.logger.Info("stored credentials expired, re-login required")
		_ = a.store.Clear()
		return false, vErr
	}
	// Transient failure (network, gateway): keep the token but fall back to QR.
	a.logger.Warn("credential validation failed (transient), re-login required", "error", vErr)
	return false, vErr
}

// Resume restores a session from stored credentials without validation.
// It loads the token and baseURL from the store and sets them on the client.
// No API call is made — the poller's Run loop will detect invalid tokens
// (stale token -14) and handle them automatically.
// Returns ErrNoStoredCredentials if no token is stored.
func (a *auth) Resume() error {
	if a.store == nil {
		return ErrNoStoredCredentials
	}
	token, baseURL, err := a.store.Load()
	if err != nil {
		return err
	}
	if token == "" {
		return ErrNoStoredCredentials
	}
	a.c.setToken(token)
	if baseURL != "" {
		a.c.setBaseURL(baseURL)
	}
	a.logger.Info("restored credentials from store")
	return nil
}

// validate checks current credentials via a lightweight getupdates call.
// It bypasses the stale-token cooldown so a fresh login can clear it.
func (a *auth) validate(ctx context.Context) (bool, error) {
	req := &GetUpdatesRequest{
		GetUpdatesBuf: "",
		BaseInfo:      &BaseInfo{ChannelVersion: a.c.channelVersion},
	}
	var resp GetUpdatesResponse
	err := a.c.do(ctx, request{
		method:    "POST",
		path:      "/ilink/bot/getupdates",
		body:      req,
		result:    &resp,
		skipGuard: true,
	})
	if err != nil {
		return false, err
	}
	if apiErr := apiError(resp.Ret, resp.ErrCode, resp.ErrMsg); apiErr != nil {
		return false, apiErr
	}
	return true, nil
}

// sleepCtx waits for d, aborting early if ctx is cancelled.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
