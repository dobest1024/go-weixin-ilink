package ilink

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// SyncBufStore persists the get_updates_buf cursor so the poller can resume
// from where it left off after a restart instead of re-reading all history.
type SyncBufStore interface {
	Save(buf string) error
	Load() (string, error)
}

// FileSyncBufStore persists the cursor to a single file.
type FileSyncBufStore struct {
	path string
}

// NewFileSyncBufStore creates a FileSyncBufStore at the given path.
func NewFileSyncBufStore(path string) *FileSyncBufStore {
	return &FileSyncBufStore{path: path}
}

func (f *FileSyncBufStore) Save(buf string) error {
	return os.WriteFile(f.path, []byte(buf), 0600)
}

func (f *FileSyncBufStore) Load() (string, error) {
	data, err := os.ReadFile(f.path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}

// TokenStore persists the bot_token across restarts.
type TokenStore interface {
	Save(token, baseURL string) error
	Load() (token, baseURL string, err error)
	Clear() error
}

// ContextTokenStore persists context_token per user.
// A context_token is required to send messages and is obtained from incoming messages.
type ContextTokenStore interface {
	Save(userID, token string) error
	Load(userID string) (string, error)
	Clear(userID string) error
}

// --- FileTokenStore ---

type savedToken struct {
	Token   string `json:"token"`
	BaseURL string `json:"base_url,omitempty"`
}

// FileTokenStore persists the bot token to a JSON file.
type FileTokenStore struct {
	path string
}

// NewFileTokenStore creates a FileTokenStore at the given path.
func NewFileTokenStore(path string) *FileTokenStore {
	return &FileTokenStore{path: path}
}

func (f *FileTokenStore) Save(token, baseURL string) error {
	data, err := json.MarshalIndent(savedToken{Token: token, BaseURL: baseURL}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(f.path, data, 0600)
}

func (f *FileTokenStore) Load() (string, string, error) {
	data, err := os.ReadFile(f.path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", nil
		}
		return "", "", err
	}
	var s savedToken
	if err := json.Unmarshal(data, &s); err != nil {
		return "", "", err
	}
	return s.Token, s.BaseURL, nil
}

func (f *FileTokenStore) Clear() error {
	err := os.Remove(f.path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// --- MemTokenStore ---

// MemTokenStore is an in-memory TokenStore (not persistent, useful for tests).
type MemTokenStore struct {
	mu      sync.RWMutex
	token   string
	baseURL string
}

func NewMemTokenStore() *MemTokenStore { return &MemTokenStore{} }

func (m *MemTokenStore) Save(token, baseURL string) error {
	m.mu.Lock()
	m.token, m.baseURL = token, baseURL
	m.mu.Unlock()
	return nil
}

func (m *MemTokenStore) Load() (string, string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.token, m.baseURL, nil
}

func (m *MemTokenStore) Clear() error {
	m.mu.Lock()
	m.token, m.baseURL = "", ""
	m.mu.Unlock()
	return nil
}

// --- FileContextTokenStore ---

type ctxTokenEntry struct {
	Token     string `json:"token"`
	UpdatedAt string `json:"updated_at"`
}

// FileContextTokenStore persists per-user context tokens to a directory.
// One JSON file per user: {dir}/{userID}.json
type FileContextTokenStore struct {
	dir    string
	mu     sync.RWMutex
	cache  map[string]string
}

// NewFileContextTokenStore creates (or opens) the directory for storing context tokens.
func NewFileContextTokenStore(dir string) (*FileContextTokenStore, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create context token dir: %w", err)
	}
	s := &FileContextTokenStore{dir: dir, cache: make(map[string]string)}
	_ = s.preload() // best-effort warm cache
	return s, nil
}

func (f *FileContextTokenStore) preload() error {
	entries, err := os.ReadDir(f.dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		userID := e.Name()[:len(e.Name())-5]
		token, _ := f.readFile(userID)
		if token != "" {
			f.cache[userID] = token
		}
	}
	return nil
}

func (f *FileContextTokenStore) readFile(userID string) (string, error) {
	data, err := os.ReadFile(filepath.Join(f.dir, userID+".json"))
	if err != nil {
		return "", err
	}
	var e ctxTokenEntry
	if err := json.Unmarshal(data, &e); err != nil {
		return "", err
	}
	return e.Token, nil
}

func (f *FileContextTokenStore) Save(userID, token string) error {
	if userID == "" || token == "" {
		return fmt.Errorf("userID and token must not be empty")
	}
	entry := ctxTokenEntry{Token: token, UpdatedAt: time.Now().UTC().Format(time.RFC3339)}
	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(f.dir, userID+".json"), data, 0600); err != nil {
		return err
	}
	f.mu.Lock()
	f.cache[userID] = token
	f.mu.Unlock()
	return nil
}

func (f *FileContextTokenStore) Load(userID string) (string, error) {
	f.mu.RLock()
	if t, ok := f.cache[userID]; ok {
		f.mu.RUnlock()
		return t, nil
	}
	f.mu.RUnlock()

	token, err := f.readFile(userID)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	f.mu.Lock()
	f.cache[userID] = token
	f.mu.Unlock()
	return token, nil
}

func (f *FileContextTokenStore) Clear(userID string) error {
	f.mu.Lock()
	delete(f.cache, userID)
	f.mu.Unlock()
	err := os.Remove(filepath.Join(f.dir, userID+".json"))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// --- MemContextTokenStore ---

// MemContextTokenStore is an in-memory ContextTokenStore.
type MemContextTokenStore struct {
	mu     sync.RWMutex
	tokens map[string]string
}

func NewMemContextTokenStore() *MemContextTokenStore {
	return &MemContextTokenStore{tokens: make(map[string]string)}
}

func (m *MemContextTokenStore) Save(userID, token string) error {
	m.mu.Lock()
	m.tokens[userID] = token
	m.mu.Unlock()
	return nil
}

func (m *MemContextTokenStore) Load(userID string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.tokens[userID], nil
}

func (m *MemContextTokenStore) Clear(userID string) error {
	m.mu.Lock()
	delete(m.tokens, userID)
	m.mu.Unlock()
	return nil
}
