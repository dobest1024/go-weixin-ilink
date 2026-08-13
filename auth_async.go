package ilink

import (
	"context"
	"sync"
)

// LoginStatus represents the current state of a QR code login session.
type LoginStatus int

const (
	LoginStatusPending        LoginStatus = iota // QR code generated, waiting for scan
	LoginStatusScanned                           // Scanned, waiting for phone confirmation
	LoginStatusNeedVerifyCode                    // Server is waiting for the pair code
	LoginStatusConfirmed                         // Login confirmed
	LoginStatusAlreadyBound                      // Already connected to this client; nothing to do
	LoginStatusExpired                           // QR code expired after all refresh attempts
	LoginStatusError                             // An error occurred
)

func (s LoginStatus) String() string {
	switch s {
	case LoginStatusPending:
		return "pending"
	case LoginStatusScanned:
		return "scanned"
	case LoginStatusNeedVerifyCode:
		return "need_verify_code"
	case LoginStatusConfirmed:
		return "confirmed"
	case LoginStatusAlreadyBound:
		return "already_bound"
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

	doneCh chan struct{}
}

// QRImage returns the base64-encoded QR code PNG image.
// It changes when the code is refreshed after expiry, so re-read it on a
// LoginStatusPending transition if you cache the rendering.
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

func (s *QRSession) setQR(imgContent, imgURL string) {
	s.mu.Lock()
	s.qrImgContent = imgContent
	s.qrImgURL = imgURL
	s.status = LoginStatusPending
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
//
// A pair-code challenge is answered through the configured VerifyCodeFunc;
// with a UI front-end, supply one via WithVerifyCodeFunc that blocks on your
// own input channel rather than stdin.
func (b *Bot) LoginAsync(ctx context.Context) (*QRSession, error) {
	a := b.authSvc

	if ok, _ := a.tryStoredCredentials(ctx); ok {
		b.cfg.hooks.callOnLogin()
		sess := &QRSession{doneCh: make(chan struct{})}
		sess.finish(LoginStatusConfirmed, nil)
		return sess, nil
	}

	qr, err := a.fetchQRCode(ctx)
	if err != nil {
		return nil, err
	}

	sess := &QRSession{
		status:       LoginStatusPending,
		qrImgContent: qr.QRCodeImgContent,
		qrImgURL:     qr.QRCodeImgURL,
		doneCh:       make(chan struct{}),
	}

	// Mirror refreshed codes and the scan progress into the session so a UI can
	// re-render without re-driving the protocol itself.
	onQR := func(imgContent string) { sess.setQR(imgContent, "") }

	go func() {
		res, err := a.runQRFlow(ctx, qr, onQR, sess.setStatus)
		switch {
		case err != nil:
			st := LoginStatusError
			if err == ErrQRCodeExpired {
				st = LoginStatusExpired
			}
			sess.finish(st, err)
		case res.alreadyBound:
			if ok, _ := a.tryStoredCredentials(ctx); ok {
				b.cfg.hooks.callOnLogin()
				sess.finish(LoginStatusConfirmed, nil)
				return
			}
			sess.finish(LoginStatusAlreadyBound, ErrAlreadyBound)
		default:
			a.applyCredentials(res.status)
			b.cfg.hooks.callOnLogin()
			sess.finish(LoginStatusConfirmed, nil)
		}
	}()

	return sess, nil
}
