package ilink

import (
	"net/url"
	"regexp"
	"strings"
)

// maxLoggedBodyLen caps how much of a request/response body reaches the log.
const maxLoggedBodyLen = 512

// sensitiveQueryKeys are URL query parameters whose values must never be logged.
var sensitiveQueryKeys = map[string]struct{}{
	"token":                 {},
	"bot_token":             {},
	"access_token":          {},
	"qrcode":                {},
	"verify_code":           {},
	"aeskey":                {},
	"aes_key":               {},
	"key":                   {},
	"encrypted_query_param": {},
	"context_token":         {},
}

// sensitiveJSONFields are JSON keys whose string values must never be logged.
var sensitiveBodyRe = regexp.MustCompile(
	`("(?:bot_token|token|access_token|context_token|typing_ticket|aeskey|aes_key|encrypt_query_param|encrypted_query_param|upload_param|thumb_upload_param|get_updates_buf|local_token_list|qrcode|qrcode_img_content|verify_code)"\s*:\s*)"[^"]*"`,
)

// RedactToken masks a secret, keeping just enough of the head and tail to
// correlate log lines without disclosing the value.
func RedactToken(s string) string {
	if s == "" {
		return "(empty)"
	}
	if len(s) <= 8 {
		return "***"
	}
	return s[:4] + "***" + s[len(s)-4:]
}

// RedactURL strips the values of sensitive query parameters from a URL,
// leaving the scheme, host, and path intact for troubleshooting.
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(unparseable url)"
	}
	q := u.Query()
	if len(q) == 0 {
		return u.String()
	}
	for k := range q {
		if _, ok := sensitiveQueryKeys[strings.ToLower(k)]; ok {
			q.Set(k, "***")
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// RedactBody masks known secret fields inside a JSON payload and truncates the
// result so a large getupdates response cannot flood the log.
func RedactBody(s string) string {
	if s == "" {
		return "(empty)"
	}
	out := sensitiveBodyRe.ReplaceAllString(s, `${1}"***"`)
	if len(out) > maxLoggedBodyLen {
		return out[:maxLoggedBodyLen] + "…(truncated)"
	}
	return out
}
