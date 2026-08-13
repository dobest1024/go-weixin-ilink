package ilink

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"syscall"
)

// NetErrorType classifies a transport-level failure so that operators can tell
// a DNS problem apart from a firewall drop or a TLS interception box without
// reading raw Go error strings.
type NetErrorType string

const (
	NetErrDNS     NetErrorType = "dns"
	NetErrTCP     NetErrorType = "tcp"
	NetErrTLS     NetErrorType = "tls"
	NetErrTimeout NetErrorType = "timeout"
	NetErrUnknown NetErrorType = "unknown"
)

// NetError is the result of ClassifyNetError: a coarse category, a
// human-readable hint, and the underlying syscall/resolver code when known.
type NetError struct {
	Type        NetErrorType
	Description string
	Code        string
}

func (n NetError) String() string {
	if n.Code == "" {
		return string(n.Type) + ": " + n.Description
	}
	return string(n.Type) + ": " + n.Description + " (" + n.Code + ")"
}

// LogArgs returns slog-style key/value pairs for structured logging.
func (n NetError) LogArgs() []any {
	args := []any{"net_error_type", string(n.Type), "net_error_desc", n.Description}
	if n.Code != "" {
		args = append(args, "net_error_code", n.Code)
	}
	return args
}

// ClassifyNetError maps a transport error onto a NetErrorType. It only covers
// fetch-level failures; HTTP status errors (4xx/5xx) and APIError are reported
// separately and classify as NetErrUnknown here.
func ClassifyNetError(err error) NetError {
	if err == nil {
		return NetError{Type: NetErrUnknown, Description: "no error"}
	}

	// DNS first: a resolver timeout is a DNS problem, not a TCP one.
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		code := "ENOTFOUND"
		switch {
		case dnsErr.IsTimeout:
			code = "ETIMEDOUT"
		case dnsErr.IsTemporary:
			code = "EAI_AGAIN"
		case dnsErr.IsNotFound:
			code = "ENOTFOUND"
		}
		return NetError{
			Type:        NetErrDNS,
			Description: "DNS resolution failed, check DNS configuration",
			Code:        code,
		}
	}

	if t, ok := classifyTLSError(err); ok {
		return t
	}

	// Syscall codes are checked before the generic timeout test because a
	// connect(2) ETIMEDOUT is a reachability problem, not a client deadline.
	var errno syscall.Errno
	if errors.As(err, &errno) {
		switch errno {
		case syscall.ECONNREFUSED:
			return NetError{Type: NetErrTCP, Description: "TCP connection refused", Code: "ECONNREFUSED"}
		case syscall.ECONNRESET:
			return NetError{Type: NetErrTCP, Description: "connection reset by peer", Code: "ECONNRESET"}
		case syscall.ETIMEDOUT:
			return NetError{Type: NetErrTCP, Description: "TCP connection timeout", Code: "ETIMEDOUT"}
		case syscall.ENETUNREACH:
			return NetError{Type: NetErrTCP, Description: "network unreachable", Code: "ENETUNREACH"}
		case syscall.EHOSTUNREACH:
			return NetError{Type: NetErrTCP, Description: "host unreachable", Code: "EHOSTUNREACH"}
		case syscall.EPIPE:
			return NetError{Type: NetErrTCP, Description: "broken pipe", Code: "EPIPE"}
		}
	}

	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return NetError{Type: NetErrTimeout, Description: "request timeout"}
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return NetError{Type: NetErrTimeout, Description: "request timeout"}
	}
	if errors.Is(err, context.Canceled) {
		return NetError{Type: NetErrUnknown, Description: "request canceled"}
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return NetError{Type: NetErrTCP, Description: "connection closed by peer", Code: "EOF"}
	}

	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return NetError{Type: NetErrTCP, Description: "TCP " + opErr.Op + " failed"}
	}

	return NetError{Type: NetErrUnknown, Description: "network request failed"}
}

func classifyTLSError(err error) (NetError, bool) {
	var unknownAuthority x509.UnknownAuthorityError
	if errors.As(err, &unknownAuthority) {
		return NetError{Type: NetErrTLS, Description: "certificate signed by unknown authority", Code: "UNABLE_TO_VERIFY"}, true
	}
	var hostnameErr x509.HostnameError
	if errors.As(err, &hostnameErr) {
		return NetError{Type: NetErrTLS, Description: "certificate hostname mismatch", Code: "CERT_HOSTNAME"}, true
	}
	var invalidCert x509.CertificateInvalidError
	if errors.As(err, &invalidCert) {
		return NetError{Type: NetErrTLS, Description: "certificate invalid or expired", Code: "CERT_INVALID"}, true
	}
	var verifyErr *tls.CertificateVerificationError
	if errors.As(err, &verifyErr) {
		return NetError{Type: NetErrTLS, Description: "certificate verification failed", Code: "CERT_VERIFY"}, true
	}
	var recordErr tls.RecordHeaderError
	if errors.As(err, &recordErr) {
		return NetError{Type: NetErrTLS, Description: "TLS handshake error (not a TLS server?)", Code: "TLS_RECORD"}, true
	}
	msg := err.Error()
	if strings.Contains(msg, "tls:") || strings.Contains(msg, "x509:") {
		return NetError{Type: NetErrTLS, Description: "TLS handshake error"}, true
	}
	return NetError{}, false
}
