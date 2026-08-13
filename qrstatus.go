package ilink

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// Server-reported QR login states.
const (
	// qrStatusWait — code issued, nobody has scanned it yet.
	qrStatusWait = "wait"
	// qrStatusScanned — scanned, waiting for the user to confirm on the phone.
	qrStatusScanned = "scaned"
	// qrStatusConfirmed — confirmed; credentials are in the response.
	qrStatusConfirmed = "confirmed"
	// qrStatusExpired — code timed out; fetch a new one.
	qrStatusExpired = "expired"
	// qrStatusScannedButRedirect — the account lives in another IDC; all further
	// status polls must go to redirect_host or they will never see "confirmed".
	qrStatusScannedButRedirect = "scaned_but_redirect"
	// qrStatusNeedVerifyCode — the server wants the pair code shown on the phone.
	qrStatusNeedVerifyCode = "need_verifycode"
	// qrStatusVerifyCodeBlocked — too many wrong pair codes; the code is burned.
	qrStatusVerifyCodeBlocked = "verify_code_blocked"
	// qrStatusBindedRedirect — this bot is already bound to this client; no new
	// credentials are issued and the existing local ones stay valid.
	qrStatusBindedRedirect = "binded_redirect"
)

// maxQRRefresh is how many times an expired or burned QR code is replaced
// before the login attempt is abandoned.
const maxQRRefresh = 3

// VerifyCodeFunc supplies the pair code the phone displays when the server
// challenges a QR scan. retry is true when the previous code was rejected.
// Returning an error aborts the login.
type VerifyCodeFunc func(retry bool) (string, error)

// TerminalVerifyCode is the default VerifyCodeFunc: it prompts on stdout and
// reads one line from stdin.
func TerminalVerifyCode(retry bool) (string, error) {
	if retry {
		fmt.Fprint(os.Stdout, "❌ 输入的数字不匹配，请重新输入：")
	} else {
		fmt.Fprint(os.Stdout, "输入手机微信显示的数字，以继续连接：")
	}
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("read pair code: %w", err)
	}
	return strings.TrimSpace(line), nil
}

// qrCodeRequest is the POST body for get_bot_qrcode. Sending the tokens this
// client already holds lets the server answer "binded_redirect" for a bot that
// is already connected, instead of issuing a duplicate session.
type qrCodeRequest struct {
	LocalTokenList []string `json:"local_token_list,omitempty"`
}

type qrCodeResponse struct {
	Ret              int    `json:"ret,omitempty"`
	ErrCode          int    `json:"errcode,omitempty"`
	ErrMsg           string `json:"errmsg,omitempty"`
	QRCode           string `json:"qrcode"`
	QRCodeImgContent string `json:"qrcode_img_content"`
	QRCodeImgURL     string `json:"qrcode_img_url"`
}

type qrCodeStatus struct {
	Status      string `json:"status"`
	BotToken    string `json:"bot_token"`
	BaseURL     string `json:"baseurl"`
	IlinkBotID  string `json:"ilink_bot_id"`
	IlinkUserID string `json:"ilink_user_id"`
	// RedirectHost is set with status scaned_but_redirect: the host that must
	// serve every subsequent status poll for this login.
	RedirectHost string `json:"redirect_host"`
}

// maxLocalTokens caps how many local bot_tokens are offered to the server.
const maxLocalTokens = 10

// localTokenList collects the bot_tokens this client already holds.
func (a *auth) localTokenList() []string {
	if a.localTokens != nil {
		tokens := a.localTokens()
		if len(tokens) > maxLocalTokens {
			tokens = tokens[:maxLocalTokens]
		}
		return tokens
	}
	if a.store == nil {
		return nil
	}
	token, _, err := a.store.Load()
	if err != nil || token == "" {
		return nil
	}
	return []string{token}
}

// fetchQRCode requests a fresh login QR code.
//
// This is a POST with a JSON body: the endpoint switched from GET in channel
// version 2.3.1 when local_token_list was introduced.
func (a *auth) fetchQRCode(ctx context.Context) (*qrCodeResponse, error) {
	req := &qrCodeRequest{LocalTokenList: a.localTokenList()}
	var resp qrCodeResponse
	path := "/ilink/bot/get_bot_qrcode?bot_type=" + url.QueryEscape(a.botType)
	err := a.c.do(ctx, request{
		method:    http.MethodPost,
		baseURL:   a.loginBaseURL,
		path:      path,
		body:      req,
		result:    &resp,
		skipGuard: true,
		anonymous: true,
	})
	if err != nil {
		return nil, fmt.Errorf("get qr code: %w", err)
	}
	if err := apiError(resp.Ret, resp.ErrCode, resp.ErrMsg); err != nil {
		return nil, fmt.Errorf("get qr code: %w", err)
	}
	a.logger.Debug("qr code issued", "qrcode", RedactToken(resp.QRCode),
		"local_tokens", len(req.LocalTokenList))
	return &resp, nil
}

// pollQRStatus performs one status long-poll against the given host.
func (a *auth) pollQRStatus(ctx context.Context, baseURL, qrcode, verifyCode string) (*qrCodeStatus, error) {
	path := "/ilink/bot/get_qrcode_status?qrcode=" + url.QueryEscape(qrcode)
	if verifyCode != "" {
		path += "&verify_code=" + url.QueryEscape(verifyCode)
	}
	var status qrCodeStatus
	err := a.c.do(ctx, request{
		method:    http.MethodGet,
		baseURL:   baseURL,
		path:      path,
		result:    &status,
		skipGuard: true,
	})
	if err != nil {
		return nil, err
	}
	return &status, nil
}

// applyCredentials points the client at the confirmed session and persists it.
func (a *auth) applyCredentials(status *qrCodeStatus) {
	a.c.setToken(status.BotToken)
	if status.BaseURL != "" {
		a.c.setBaseURL(status.BaseURL)
	}
	a.c.guard.resume()
	if a.store != nil {
		if err := a.store.Save(status.BotToken, status.BaseURL); err != nil {
			a.logger.Warn("failed to save credentials", "error", err)
		}
	}
	a.logger.Info("login successful",
		"ilink_bot_id", status.IlinkBotID,
		"ilink_user_id", RedactToken(status.IlinkUserID))
}
