package ilink

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
)

// UploadResult holds the CDN reference after a successful media upload.
type UploadResult struct {
	AESKey         string // hex-encoded AES-128 key
	FileKey        string // hex-encoded file key
	EncryptedParam string // x-encrypted-param from CDN response
	FileSize       int    // original plaintext size
	CipherSize     int    // encrypted (padded) size
}

type uploadURLRequest struct {
	FileKey     string    `json:"filekey,omitempty"`
	MediaType   int       `json:"media_type,omitempty"`
	ToUserID    string    `json:"to_user_id,omitempty"`
	RawSize     int       `json:"rawsize,omitempty"`
	RawFileMD5  string    `json:"rawfilemd5,omitempty"`
	FileSize    int       `json:"filesize,omitempty"`
	NoNeedThumb bool      `json:"no_need_thumb,omitempty"`
	AESKey      string    `json:"aeskey,omitempty"`
	BaseInfo    *BaseInfo `json:"base_info,omitempty"`
}

type uploadURLResponse struct {
	UploadURL   string `json:"upload_url"`
	UploadParam string `json:"upload_param"`
	Ret         int    `json:"ret"`
}

type mediaManager struct {
	c              *client
	httpClient     *http.Client
	logger         *slog.Logger
	cdnBaseURL     string
	channelVersion string
}

func newMediaManager(c *client, httpClient *http.Client, cdnBaseURL, channelVersion string, logger *slog.Logger) *mediaManager {
	return &mediaManager{
		c:              c,
		httpClient:     httpClient,
		cdnBaseURL:     cdnBaseURL,
		channelVersion: channelVersion,
		logger:         logger,
	}
}

// UploadFile encrypts and uploads raw bytes to WeChat CDN.
// fileType: "image" | "video" | "voice" | "file"
// toUserID: recipient's user ID (required by getuploadurl API).
func (m *mediaManager) UploadFile(ctx context.Context, data []byte, toUserID, fileType string) (*UploadResult, error) {
	aesKey, err := generateAESKey()
	if err != nil {
		return nil, fmt.Errorf("generate aes key: %w", err)
	}

	encrypted, err := encryptAESECB(data, aesKey)
	if err != nil {
		return nil, fmt.Errorf("encrypt: %w", err)
	}

	mediaType := 1 // image
	switch fileType {
	case "video":
		mediaType = 2
	case "file":
		mediaType = 3
	case "voice":
		mediaType = 4
	}

	md5sum := md5.Sum(data)
	fileKey := generateFileKey()

	uploadReq := &uploadURLRequest{
		FileKey:     fileKey,
		ToUserID:    toUserID,
		MediaType:   mediaType,
		RawSize:     len(data),
		RawFileMD5:  hex.EncodeToString(md5sum[:]),
		FileSize:    len(encrypted),
		NoNeedThumb: true,
		AESKey:      hex.EncodeToString(aesKey),
		BaseInfo:    &BaseInfo{ChannelVersion: m.channelVersion},
	}
	var uploadResp uploadURLResponse
	if err := m.c.post(ctx, "/ilink/bot/getuploadurl", uploadReq, &uploadResp); err != nil {
		return nil, fmt.Errorf("get upload url: %w", err)
	}
	if uploadResp.Ret != 0 {
		return nil, fmt.Errorf("getuploadurl failed: ret=%d", uploadResp.Ret)
	}

	cdnURL := fmt.Sprintf("%s/upload?encrypted_query_param=%s&filekey=%s",
		m.cdnBaseURL,
		url.QueryEscape(uploadResp.UploadParam),
		url.QueryEscape(fileKey),
	)
	encryptedParam, err := m.uploadToCDN(ctx, cdnURL, encrypted)
	if err != nil {
		return nil, fmt.Errorf("cdn upload: %w", err)
	}

	return &UploadResult{
		AESKey:         hex.EncodeToString(aesKey),
		FileKey:        fileKey,
		EncryptedParam: encryptedParam,
		FileSize:       len(data),
		CipherSize:     len(encrypted),
	}, nil
}

func generateFileKey() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (m *mediaManager) uploadToCDN(ctx context.Context, cdnURL string, data []byte) (string, error) {
	const maxRetries = 3
	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		param, err := m.doUpload(ctx, cdnURL, data)
		if err == nil {
			return param, nil
		}
		lastErr = err
		// Don't retry 4xx errors
		if he, ok := err.(*cdnError); ok && he.status >= 400 && he.status < 500 {
			return "", err
		}
		m.logger.Warn("cdn upload retry", "attempt", attempt, "error", err)
	}
	return "", fmt.Errorf("cdn upload failed after %d attempts: %w", maxRetries, lastErr)
}

type cdnError struct {
	status  int
	message string
}

func (e *cdnError) Error() string { return fmt.Sprintf("cdn http %d: %s", e.status, e.message) }

func (m *mediaManager) doUpload(ctx context.Context, cdnURL string, data []byte) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cdnURL, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return "", &cdnError{status: resp.StatusCode, message: string(body)}
	}

	param := resp.Header.Get("x-encrypted-param")
	if param == "" {
		return "", fmt.Errorf("cdn response missing x-encrypted-param")
	}
	return param, nil
}

// DownloadFile downloads and decrypts a file from CDN.
// aesKeyHex is the hex-encoded AES key (from UploadResult or message item).
func (m *mediaManager) DownloadFile(ctx context.Context, cdnURL, aesKeyHex string) ([]byte, error) {
	aesKey, err := hex.DecodeString(aesKeyHex)
	if err != nil {
		return nil, fmt.Errorf("decode aes key: %w", err)
	}
	encrypted, err := m.downloadFromCDN(ctx, cdnURL)
	if err != nil {
		return nil, err
	}
	return decryptAESECB(encrypted, aesKey)
}

// DownloadFileWithBase64Key downloads and decrypts using a base64-encoded key
// (as stored in outbound CDNMedia.AESKey fields).
func (m *mediaManager) DownloadFileWithBase64Key(ctx context.Context, cdnURL, aesKeyB64 string) ([]byte, error) {
	decoded, err := base64.StdEncoding.DecodeString(aesKeyB64)
	if err != nil {
		return nil, fmt.Errorf("decode base64 aes key: %w", err)
	}

	var aesKey []byte
	if len(decoded) == 16 {
		aesKey = decoded
	} else if len(decoded) == 32 {
		// base64(hex_string) format used by outbound messages
		aesKey, err = hex.DecodeString(string(decoded))
		if err != nil {
			return nil, fmt.Errorf("decode hex aes key: %w", err)
		}
	} else {
		return nil, fmt.Errorf("unexpected decoded key length: %d", len(decoded))
	}

	encrypted, err := m.downloadFromCDN(ctx, cdnURL)
	if err != nil {
		return nil, err
	}
	return decryptAESECB(encrypted, aesKey)
}

func (m *mediaManager) downloadFromCDN(ctx context.Context, cdnURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cdnURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("cdn download http %d: %s", resp.StatusCode, string(body))
	}
	return io.ReadAll(resp.Body)
}

// BuildDownloadURL constructs a CDN download URL from a CDNMedia.
func (m *mediaManager) BuildDownloadURL(media *CDNMedia) string {
	return fmt.Sprintf("%s/download?encrypted_query_param=%s",
		m.cdnBaseURL, url.QueryEscape(media.EncryptQueryParam))
}

// BuildImageItem creates a MessageItem for sending an uploaded image.
func BuildImageItem(result *UploadResult) MessageItem {
	// Per iLink protocol: aes_key in outbound CDNMedia = base64(hex_string)
	aesKeyB64 := base64.StdEncoding.EncodeToString([]byte(result.AESKey))
	return MessageItem{
		Type: ItemTypeImage,
		ImageItem: &ImageItem{
			Media: &CDNMedia{
				EncryptQueryParam: result.EncryptedParam,
				AESKey:            aesKeyB64,
				EncryptType:       1,
			},
			MidSize: result.CipherSize,
		},
	}
}

// BuildVoiceItem creates a MessageItem for sending an uploaded voice message.
// duration is in milliseconds.
func BuildVoiceItem(result *UploadResult, duration int) MessageItem {
	aesKeyB64 := base64.StdEncoding.EncodeToString([]byte(result.AESKey))
	return MessageItem{
		Type: ItemTypeVoice,
		VoiceItem: &VoiceItem{
			Media: &CDNMedia{
				EncryptQueryParam: result.EncryptedParam,
				AESKey:            aesKeyB64,
				EncryptType:       0,
			},
			Duration: duration,
		},
	}
}

// BuildVoiceItemFrom creates a MessageItem for sending an uploaded voice message,
// preserving the codec parameters (encode_type, sample_rate, bits_per_sample)
// from the original received VoiceItem. Use this when forwarding/mirroring a voice message.
func BuildVoiceItemFrom(result *UploadResult, original *VoiceItem) MessageItem {
	aesKeyB64 := base64.StdEncoding.EncodeToString([]byte(result.AESKey))
	return MessageItem{
		Type: ItemTypeVoice,
		VoiceItem: &VoiceItem{
			Media: &CDNMedia{
				EncryptQueryParam: result.EncryptedParam,
				AESKey:            aesKeyB64,
				EncryptType:       1,
			},
			Duration: original.Duration,
			//EncodeType:    original.EncodeType,
			//SampleRate:    original.SampleRate,
			//BitsPerSample: original.BitsPerSample,
			//FileSize:      result.FileSize,
		},
	}
}

// BuildFileItem creates a MessageItem for sending an uploaded file.
func BuildFileItem(result *UploadResult, fileName string) MessageItem {
	aesKeyB64 := base64.StdEncoding.EncodeToString([]byte(result.AESKey))
	return MessageItem{
		Type: ItemTypeFile,
		FileItem: &FileItem{
			Media: &CDNMedia{
				EncryptQueryParam: result.EncryptedParam,
				AESKey:            aesKeyB64,
				EncryptType:       1,
			},
			FileName: fileName,
			Length:   fmt.Sprintf("%d", result.FileSize),
		},
	}
}

// BuildVideoItem creates a MessageItem for sending an uploaded video.
// width/height are thumbnail dimensions, duration is in milliseconds.
func BuildVideoItem(result *UploadResult, width, height, duration int) MessageItem {
	aesKeyB64 := base64.StdEncoding.EncodeToString([]byte(result.AESKey))
	return MessageItem{
		Type: ItemTypeVideo,
		VideoItem: &VideoItem{
			Media: &CDNMedia{
				EncryptQueryParam: result.EncryptedParam,
				AESKey:            aesKeyB64,
				EncryptType:       1,
			},
			VideoSize:   result.FileSize,
			PlayLength:  duration,
			ThumbWidth:  width,
			ThumbHeight: height,
		},
	}
}
