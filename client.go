package ilink

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// client is the low-level HTTP client for the iLink API.
type client struct {
	mu             sync.RWMutex
	baseURL        string
	token          string
	httpClient     *http.Client
	logger         *slog.Logger
	channelVersion string
	appID          string
	clientVersion  string // "iLink-App-ClientVersion" computed from channelVersion
	skRouteTag     string

	// guard suppresses every API call for an hour after a stale-token response,
	// so proactive sends stop hammering a token the server already rejected.
	guard sessionGuard
}

func newClient(baseURL string, httpClient *http.Client, channelVersion, appID, skRouteTag string, logger *slog.Logger) *client {
	return &client{
		baseURL:        baseURL,
		httpClient:     httpClient,
		logger:         logger,
		channelVersion: channelVersion,
		appID:          appID,
		clientVersion:  buildClientVersion(channelVersion),
		skRouteTag:     skRouteTag,
	}
}

// buildClientVersion encodes a semver "M.N.P" as uint32 in 0x00MMNNPP format.
func buildClientVersion(version string) string {
	parts := strings.SplitN(version, ".", 3)
	var major, minor, patch int
	if len(parts) >= 1 {
		major, _ = strconv.Atoi(parts[0])
	}
	if len(parts) >= 2 {
		minor, _ = strconv.Atoi(parts[1])
	}
	if len(parts) >= 3 {
		patch, _ = strconv.Atoi(parts[2])
	}
	encoded := (major&0xff)<<16 | (minor&0xff)<<8 | (patch & 0xff)
	return strconv.Itoa(encoded)
}

func (c *client) setToken(token string) {
	c.mu.Lock()
	c.token = token
	c.mu.Unlock()
}

func (c *client) getToken() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.token
}

func (c *client) setBaseURL(url string) {
	c.mu.Lock()
	c.baseURL = url
	c.mu.Unlock()
}

func (c *client) getBaseURL() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.baseURL
}

// generateUIN generates the X-WECHAT-UIN header value.
// Format: base64(decimal_string(random_uint32))
func generateUIN() string {
	n, _ := rand.Int(rand.Reader, new(big.Int).SetUint64(1<<32))
	return base64.StdEncoding.EncodeToString([]byte(n.String()))
}

// commonHeaders are sent with every request, authenticated or not.
func (c *client) commonHeaders(h http.Header) {
	if c.appID != "" {
		h.Set("iLink-App-Id", c.appID)
	}
	if c.clientVersion != "" {
		h.Set("iLink-App-ClientVersion", c.clientVersion)
	}
	if c.skRouteTag != "" {
		h.Set("SKRouteTag", c.skRouteTag)
	}
}

// postHeaders add the JSON body headers and, unless anonymous, the bearer
// token. They are only sent on POST: the pre-login GET endpoints take the
// common headers alone.
func (c *client) postHeaders(h http.Header, anonymous bool) {
	h.Set("Content-Type", "application/json")
	h.Set("AuthorizationType", "ilink_bot_token")
	h.Set("X-WECHAT-UIN", generateUIN())
	if anonymous {
		return
	}
	if token := c.getToken(); token != "" {
		h.Set("Authorization", "Bearer "+token)
	}
}

// request describes one API call. baseURL overrides the client's current base
// URL, which the QR login flow needs when the server redirects it to another IDC.
type request struct {
	method  string
	baseURL string
	path    string
	body    interface{}
	result  interface{}

	// skipGuard lets the poller and the login flow through while the
	// stale-token cooldown is active.
	skipGuard bool

	// anonymous omits the Authorization header. The login endpoints are
	// pre-authentication: presenting the stale token we are trying to replace
	// makes the server answer for the old session instead of issuing a new one.
	anonymous bool
}

func (c *client) do(ctx context.Context, r request) error {
	if !r.skipGuard {
		if err := c.guard.check(); err != nil {
			return err
		}
	}

	var bodyReader io.Reader
	var rawBody []byte
	if r.body != nil {
		data, err := json.Marshal(r.body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		rawBody = data
		bodyReader = bytes.NewReader(data)
	}

	base := r.baseURL
	if base == "" {
		base = c.getBaseURL()
	}
	url := strings.TrimSuffix(base, "/") + r.path

	req, err := http.NewRequestWithContext(ctx, r.method, url, bodyReader)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	c.commonHeaders(req.Header)
	if r.method == http.MethodPost {
		c.postHeaders(req.Header, r.anonymous)
	}

	if c.logger != nil {
		c.logger.Debug("api request", "method", r.method, "url", RedactURL(url),
			"body", RedactBody(string(rawBody)))
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		netErr := ClassifyNetError(err)
		if c.logger != nil {
			args := append([]any{"method", r.method, "url", RedactURL(url), "error", err}, netErr.LogArgs()...)
			c.logger.Error("api request failed", args...)
		}
		return fmt.Errorf("http %s %s (%s): %w", r.method, r.path, netErr, err)
	}
	defer resp.Body.Close()

	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return fmt.Errorf("read response: %w", readErr)
	}

	if c.logger != nil {
		c.logger.Debug("api response", "url", RedactURL(url),
			"status", resp.StatusCode, "body", RedactBody(string(respBody)))
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("http %d: %s", resp.StatusCode, RedactBody(string(respBody)))
	}

	if r.result != nil {
		if err := json.Unmarshal(respBody, r.result); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

func (c *client) get(ctx context.Context, path string, result interface{}) error {
	return c.do(ctx, request{method: http.MethodGet, path: path, result: result})
}

func (c *client) post(ctx context.Context, path string, body, result interface{}) error {
	return c.do(ctx, request{method: http.MethodPost, path: path, body: body, result: result})
}

// httpDo performs a raw HTTP request (used by media upload/download without iLink headers).
func (c *client) httpDo(req *http.Request) (*http.Response, error) {
	return c.httpClient.Do(req)
}
