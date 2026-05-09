package ilink

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// LoginStatus represents the current state of a QR code login session.
type LoginStatus int

const (
	LoginStatusPending   LoginStatus = iota // QR code generated, waiting for scan
	LoginStatusScanned                      // Scanned, waiting for phone confirmation
	LoginStatusConfirmed                    // Login confirmed
	LoginStatusExpired                      // QR code expired
	LoginStatusError                        // An error occurred
)

func (s LoginStatus) String() string {
	switch s {
	case LoginStatusPending:
		return "pending"
	case LoginStatusScanned:
		return "scanned"
	case LoginStatusConfirmed:
		return "confirmed"
	case LoginStatusExpired:
		return "expired"
	case LoginStatusError:
		return "error"
	}
	return "unknown"
}

// QRSession represents an asynchronous QR code login session.
// Create one via Bot.LoginAsync(), then poll Status() or call Wait().
type QRSession struct {
	mu     sync.RWMutex
	status LoginStatus
	err    error

	qrImgContent string // base64-encoded PNG
	qrImgURL     string
	qrCode       string // qrcode token for polling

	auth   *auth
	doneCh chan struct{}
}

// QRImage returns the base64-encoded QR code PNG image.
func (s *QRSession) QRImage() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.qrImgContent
}

// QRImageURL returns the URL of the QR code image (hosted by WeChat).
func (s *QRSession) QRImageURL() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.qrImgURL
}

// Status returns the current login status.
func (s *QRSession) Status() LoginStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status
}

// Err returns the error, if any (only meaningful when Status is LoginStatusError).
func (s *QRSession) Err() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.err
}

// Wait blocks until the login completes (confirmed or failed) or ctx is cancelled.
func (s *QRSession) Wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.doneCh:
		s.mu.RLock()
		defer s.mu.RUnlock()
		return s.err
	}
}

// Done returns a channel that is closed when the login session finishes.
func (s *QRSession) Done() <-chan struct{} {
	return s.doneCh
}

func (s *QRSession) setStatus(st LoginStatus) {
	s.mu.Lock()
	s.status = st
	s.mu.Unlock()
}

func (s *QRSession) finish(st LoginStatus, err error) {
	s.mu.Lock()
	s.status = st
	s.err = err
	s.mu.Unlock()
	close(s.doneCh)
}

// LoginAsync starts a non-blocking QR code login flow.
// It returns a QRSession immediately after the QR code is fetched.
// The caller can read QRImage()/QRImageURL() to display the code,
// poll Status(), or call Wait() to block until completion.
//
// If valid stored credentials exist, the session completes immediately
// with LoginStatusConfirmed.
func (b *Bot) LoginAsync(ctx context.Context) (*QRSession, error) {
	a := b.authSvc

	// Try existing credentials first.
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
			if valid, vErr := a.validate(ctx); valid {
				a.logger.Info("reusing stored credentials")
				b.cfg.hooks.callOnLogin()
				sess := &QRSession{doneCh: make(chan struct{})}
				sess.finish(LoginStatusConfirmed, nil)
				return sess, nil
			} else if IsSessionExpired(vErr) {
				a.logger.Info("stored credentials expired, re-login required")
				_ = a.store.Clear()
			} else {
				a.logger.Warn("credential validation failed (transient), re-login required", "error", vErr)
			}
		}
	}

	// Fetch QR code.
	var qr qrCodeResponse
	if err := a.c.get(ctx, "/ilink/bot/get_bot_qrcode?bot_type=3", &qr); err != nil {
		return nil, fmt.Errorf("get qr code: %w", err)
	}

	sess := &QRSession{
		status:       LoginStatusPending,
		qrImgContent: qr.QRCodeImgContent,
		qrImgURL:     qr.QRCodeImgURL,
		qrCode:       qr.QRCode,
		auth:         a,
		doneCh:       make(chan struct{}),
	}

	// Poll status in background.
	go sess.pollLoop(ctx, b)

	return sess, nil
}

func (s *QRSession) pollLoop(ctx context.Context, b *Bot) {
	a := s.auth
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.finish(LoginStatusError, ctx.Err())
			return
		case <-ticker.C:
			var status qrCodeStatus
			path := fmt.Sprintf("/ilink/bot/get_qrcode_status?qrcode=%s", s.qrCode)
			if err := a.c.get(ctx, path, &status); err != nil {
				a.logger.Warn("poll qr status error", "error", err)
				continue
			}
			switch status.Status {
			case "scaned":
				s.setStatus(LoginStatusScanned)
				a.logger.Info("QR code scanned, waiting for phone confirmation...")
			case "expired":
				s.finish(LoginStatusExpired, ErrQRCodeExpired)
				return
			case "confirmed":
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
				b.cfg.hooks.callOnLogin()
				s.finish(LoginStatusConfirmed, nil)
				return
			}
		}
	}
}
