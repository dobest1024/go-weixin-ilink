package ilink

import (
	"errors"
	"fmt"
)

// APIError represents an error code returned by the iLink API.
type APIError struct {
	Code    int    `json:"errcode"`
	Message string `json:"errmsg"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("ilink: api error code=%d msg=%s", e.Code, e.Message)
}

// IsSessionExpired reports whether err is a session-expiry error (code -14).
func IsSessionExpired(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Code == -14
}

var (
	ErrSessionExpired = &APIError{Code: -14, Message: "session expired"}
	ErrNotLoggedIn        = errors.New("ilink: not logged in, call Login first")
	ErrNoStoredCredentials = errors.New("ilink: no stored credentials to resume")
	ErrQRCodeExpired  = errors.New("ilink: qr code expired after max retries")
	ErrPollerStopped  = errors.New("ilink: poller stopped")
	ErrNoContextToken = errors.New("ilink: no context token for user (user must send a message first)")
)
