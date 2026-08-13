package ilink

import (
	"errors"
	"fmt"
	"time"
)

// StaleTokenErrCode is returned by the server when the bot_token is stale or
// expired. The name mirrors the official plugin's STALE_TOKEN_ERRCODE: the
// code signals a stale token rather than an ended conversation session.
const StaleTokenErrCode = -14

// APIError represents an error code returned by the iLink API.
type APIError struct {
	Code    int    `json:"errcode"`
	Message string `json:"errmsg"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("ilink: api error code=%d msg=%s", e.Code, e.Message)
}

// SessionPausedError is returned by every API call while the bot is inside the
// cooldown that follows a stale-token response.
type SessionPausedError struct {
	Remaining time.Duration
}

func (e *SessionPausedError) Error() string {
	return fmt.Sprintf("ilink: session paused after errcode %d, %d min remaining",
		StaleTokenErrCode, int(e.Remaining.Minutes())+1)
}

// IsSessionPaused reports whether err is a SessionPausedError.
func IsSessionPaused(err error) bool {
	var pe *SessionPausedError
	return errors.As(err, &pe)
}

// IsSessionExpired reports whether err is a stale-token error (code -14).
func IsSessionExpired(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Code == StaleTokenErrCode
}

// apiError builds an APIError from a response's ret/errcode/errmsg triple.
//
// The server reports failures through either field: ret carries the code on
// most CGIs, errcode on others. Reading only one of them silently turns a
// stale-token response into code 0, which then fails IsSessionExpired.
// Returns nil when both codes are zero.
func apiError(ret, errcode int, errmsg string) error {
	code := ret
	if code == 0 {
		code = errcode
	}
	if code == 0 {
		return nil
	}
	return &APIError{Code: code, Message: errmsg}
}

var (
	// ErrSessionExpired is the canonical stale-token error value.
	ErrSessionExpired = &APIError{Code: StaleTokenErrCode, Message: "session expired"}

	ErrNotLoggedIn         = errors.New("ilink: not logged in, call Login first")
	ErrNoStoredCredentials = errors.New("ilink: no stored credentials to resume")
	ErrQRCodeExpired       = errors.New("ilink: qr code expired after max retries")
	ErrPollerStopped       = errors.New("ilink: poller stopped")
	ErrNoContextToken      = errors.New("ilink: no context token for user (user must send a message first)")

	// ErrAlreadyBound is returned when the scanned bot is already bound to this
	// client (server status "binded_redirect") but no local credentials exist to
	// reuse. Clear the token store and scan again.
	ErrAlreadyBound = errors.New("ilink: bot already bound to this client, but no local credentials found")

	// ErrVerifyCodeBlocked is returned when the pair code was entered
	// incorrectly too many times and the server refuses further attempts.
	ErrVerifyCodeBlocked = errors.New("ilink: pair code blocked after too many wrong attempts")

	// ErrNoVerifyCodeFunc is returned when the server asks for a pair code but
	// no VerifyCodeFunc was configured. See WithVerifyCodeFunc.
	ErrNoVerifyCodeFunc = errors.New("ilink: server requires a pair code but no VerifyCodeFunc is configured")

	// ErrSendCanceled is returned by a send call when an OnBeforeSend hook
	// cancels delivery. It is not a failure: the message was intentionally dropped.
	ErrSendCanceled = errors.New("ilink: send canceled by OnBeforeSend hook")

	// ErrNoVoiceTranscoder is returned by DownloadVoiceWAV when the payload is
	// SILK but no VoiceTranscoder is configured. See WithVoiceTranscoder.
	ErrNoVoiceTranscoder = errors.New("ilink: voice payload is SILK but no VoiceTranscoder is configured")
)
