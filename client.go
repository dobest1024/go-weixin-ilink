package ilink

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
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
	channelVersion string
	appID          string
	clientVersion  string // "iLink-App-ClientVersion" computed from channelVersion
}

func newClient(baseURL string, httpClient *http.Client, channelVersion, appID string) *client {
	return &client{
		baseURL:        baseURL,
		httpClient:     httpClient,
		channelVersion: channelVersion,
		appID:          appID,
		clientVersion:  buildClientVersion(channelVersion),
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

func (c *client) do(ctx context.Context, method, path string, body, result interface{}) error {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	url := c.getBaseURL() + path
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("AuthorizationType", "ilink_bot_token")
	req.Header.Set("X-WECHAT-UIN", generateUIN())
	if c.appID != "" {
		req.Header.Set("iLink-App-Id", c.appID)
	}
	if c.clientVersion != "" {
		req.Header.Set("iLink-App-ClientVersion", c.clientVersion)
	}
	if token := c.getToken(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("http %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("http %d: %s", resp.StatusCode, string(b))
	}

	if result != nil {
		if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

func (c *client) get(ctx context.Context, path string, result interface{}) error {
	return c.do(ctx, http.MethodGet, path, nil, result)
}

func (c *client) post(ctx context.Context, path string, body, result interface{}) error {
	return c.do(ctx, http.MethodPost, path, body, result)
}

// httpDo performs a raw HTTP request (used by media upload/download without iLink headers).
func (c *client) httpDo(req *http.Request) (*http.Response, error) {
	return c.httpClient.Do(req)
}
