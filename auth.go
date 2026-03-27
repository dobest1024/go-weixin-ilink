package ilink

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// QRCallback is called with the base64-encoded QR code image when a scan is needed.
// Users can render it with qrterminal or save it as a PNG file.
type QRCallback func(qrImgContent string)

type qrCodeResponse struct {
	QRCode           string `json:"qrcode"`
	QRCodeImgContent string `json:"qrcode_img_content"`
	QRCodeImgURL     string `json:"qrcode_img_url"`
}

type qrCodeStatus struct {
	Status      string `json:"status"` // wait | scaned | confirmed | expired
	BotToken    string `json:"bot_token"`
	BaseURL     string `json:"baseurl"`
	IlinkBotID  string `json:"ilink_bot_id"`
	IlinkUserID string `json:"ilink_user_id"`
}

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
}

func newAuth(c *client, store TokenStore, logger *slog.Logger) *auth {
	return &auth{c: c, store: store, logger: logger}
}

// Login performs the full login flow:
//  1. Load & validate existing credentials from the store.
//  2. If valid, reuse them without showing a QR code.
//  3. Otherwise, display QR code and wait for scan.
func (a *auth) Login(ctx context.Context, onQR QRCallback) error {
	// Try existing credentials first
	if a.store != nil {
		token, baseURL, err := a.store.Load()
		if err != nil {
			a.logger.Warn("failed to load stored credentials", "error", err)
		} else if token != "" {
			a.c.setToken(token)
			if baseURL != "" {
				a.c.setBaseURL(baseURL)
			}
			a.logger.Info("validating stored credentials...")
			if valid, _ := a.validate(ctx); valid {
				a.logger.Info("reusing stored credentials")
				return nil
			}
			a.logger.Info("stored credentials invalid, re-login required")
			if a.store != nil {
				_ = a.store.Clear()
			}
		}
	}

	// QR code login
	const maxRetries = 3
	for attempt := 0; attempt < maxRetries; attempt++ {
		var qr qrCodeResponse
		if err := a.c.get(ctx, "/ilink/bot/get_bot_qrcode?bot_type=3", &qr); err != nil {
			return fmt.Errorf("get qr code: %w", err)
		}

		if onQR != nil {
			onQR(qr.QRCodeImgContent)
		} else {
			a.logger.Info("QR code ready", "url", qr.QRCodeImgURL)
		}

		status, err := a.pollStatus(ctx, qr.QRCode)
		if err != nil {
			return err
		}
		if status.Status == "expired" {
			a.logger.Info("QR code expired, retrying", "attempt", attempt+1)
			continue
		}
		if status.Status == "confirmed" {
			a.c.setToken(status.BotToken)
			if status.BaseURL != "" {
				a.c.setBaseURL(status.BaseURL)
			}
			if a.store != nil {
				if err := a.store.Save(status.BotToken, status.BaseURL); err != nil {
					a.logger.Warn("failed to save credentials", "error", err)
				}
			}
			a.logger.Info("login successful")
			return nil
		}
	}
	return ErrQRCodeExpired
}

// validate checks current credentials via a lightweight getupdates call.
func (a *auth) validate(ctx context.Context) (bool, error) {
	var resp GetUpdatesResponse
	req := &GetUpdatesRequest{
		GetUpdatesBuf: "",
		BaseInfo:      &BaseInfo{ChannelVersion: "1.0.3"},
	}
	if err := a.c.post(ctx, "/ilink/bot/getupdates", req, &resp); err != nil {
		return false, err
	}
	if resp.Ret == 0 {
		return true, nil
	}
	if resp.Ret == -14 {
		return false, ErrSessionExpired
	}
	return false, &APIError{Code: resp.Ret}
}

// pollStatus long-polls until the QR code is scanned/confirmed/expired.
func (a *auth) pollStatus(ctx context.Context, qrcode string) (*qrCodeStatus, error) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			var status qrCodeStatus
			path := fmt.Sprintf("/ilink/bot/get_qrcode_status?qrcode=%s", qrcode)
			if err := a.c.get(ctx, path, &status); err != nil {
				a.logger.Warn("poll qr status error", "error", err)
				continue
			}
			switch status.Status {
			case "confirmed", "expired":
				return &status, nil
			case "scaned":
				a.logger.Info("QR code scanned, waiting for phone confirmation...")
			}
		}
	}
}
