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

	// Thumbnail fields, set only when UploadOptions.Thumb was supplied.
	// The thumbnail shares the main file's AES key.
	ThumbEncryptedParam string
	ThumbFileSize       int // thumbnail plaintext size
	ThumbCipherSize     int // thumbnail ciphertext size
	ThumbWidth          int
	ThumbHeight         int
}

// HasThumb reports whether a thumbnail was uploaded alongside the media.
func (r *UploadResult) HasThumb() bool { return r.ThumbEncryptedParam != "" }

// UploadOptions configures a media upload.
type UploadOptions struct {
	// FileType is one of "image", "video", "voice", "file".
	FileType string

	// Thumb is an optional plaintext thumbnail (JPEG/PNG). Images and videos
	// render without one, but the chat bubble stays blank until the full file
	// downloads; supplying a thumbnail gives the recipient an instant preview.
	Thumb []byte
	// ThumbWidth / ThumbHeight describe the thumbnail in pixels.
	ThumbWidth  int
	ThumbHeight int
}

type uploadURLRequest struct {
	FileKey    string `json:"filekey,omitempty"`
	MediaType  int    `json:"media_type,omitempty"`
	ToUserID   string `json:"to_user_id,omitempty"`
	RawSize    int    `json:"rawsize,omitempty"`
	RawFileMD5 string `json:"rawfilemd5,omitempty"`
	FileSize   int    `json:"filesize,omitempty"`

	// Thumbnail parameters, required by the server when no_need_thumb is false.
	ThumbRawSize    int    `json:"thumb_rawsize,omitempty"`
	ThumbRawFileMD5 string `json:"thumb_rawfilemd5,omitempty"`
	ThumbFileSize   int    `json:"thumb_filesize,omitempty"`

	NoNeedThumb bool      `json:"no_need_thumb"`
	AESKey      string    `json:"aeskey,omitempty"`
	BaseInfo    *BaseInfo `json:"base_info,omitempty"`
}

type uploadURLResponse struct {
	UploadURL          string `json:"upload_url"`
	UploadParam        string `json:"upload_param"`
	UploadFullURL      string `json:"upload_full_url,omitempty"`
	ThumbUploadParam   string `json:"thumb_upload_param,omitempty"`
	ThumbUploadFullURL string `json:"thumb_upload_full_url,omitempty"`
	Ret                int    `json:"ret"`
	ErrCode            int    `json:"errcode,omitempty"`
	ErrMsg             string `json:"errmsg,omitempty"`
}

type mediaManager struct {
	c              *client
	httpClient     *http.Client
	logger         *slog.Logger
	cdnBaseURL     string
	channelVersion string
	botAgent       string
}

func newMediaManager(c *client, httpClient *http.Client, cdnBaseURL, channelVersion, botAgent string, logger *slog.Logger) *mediaManager {
	return &mediaManager{
		c:              c,
		httpClient:     httpClient,
		cdnBaseURL:     cdnBaseURL,
		channelVersion: channelVersion,
		botAgent:       botAgent,
		logger:         logger,
	}
}

// mediaTypeForFileType maps the SDK's file type names onto the protocol's
// UploadMediaType enum.
func mediaTypeForFileType(fileType string) int {
	switch fileType {
	case "video":
		return 2
	case "file":
		return 3
	case "voice":
		return 4
	default:
		return 1 // image
	}
}

// UploadFile encrypts and uploads raw bytes to WeChat CDN.
// fileType: "image" | "video" | "voice" | "file"
// toUserID: recipient's user ID (required by getuploadurl API).
func (m *mediaManager) UploadFile(ctx context.Context, data []byte, toUserID, fileType string) (*UploadResult, error) {
	return m.Upload(ctx, data, toUserID, UploadOptions{FileType: fileType})
}

// Upload encrypts and uploads raw bytes, optionally with a thumbnail.
// The thumbnail is encrypted with the same AES key as the main file, which is
// what the protocol expects: getuploadurl takes a single aeskey and issues two
// upload URLs against it.
func (m *mediaManager) Upload(ctx context.Context, data []byte, toUserID string, opts UploadOptions) (*UploadResult, error) {
	aesKey, err := generateAESKey()
	if err != nil {
		return nil, fmt.Errorf("generate aes key: %w", err)
	}

	encrypted, err := encryptAESECB(data, aesKey)
	if err != nil {
		return nil, fmt.Errorf("encrypt: %w", err)
	}

	md5sum := md5.Sum(data)
	fileKey := generateFileKey()
	wantThumb := len(opts.Thumb) > 0

	uploadReq := &uploadURLRequest{
		FileKey:     fileKey,
		ToUserID:    toUserID,
		MediaType:   mediaTypeForFileType(opts.FileType),
		RawSize:     len(data),
		RawFileMD5:  hex.EncodeToString(md5sum[:]),
		FileSize:    len(encrypted),
		NoNeedThumb: !wantThumb,
		AESKey:      hex.EncodeToString(aesKey),
		BaseInfo:    &BaseInfo{ChannelVersion: m.channelVersion, BotAgent: m.botAgent},
	}

	var encryptedThumb []byte
	if wantThumb {
		encryptedThumb, err = encryptAESECB(opts.Thumb, aesKey)
		if err != nil {
			return nil, fmt.Errorf("encrypt thumbnail: %w", err)
		}
		thumbMD5 := md5.Sum(opts.Thumb)
		uploadReq.ThumbRawSize = len(opts.Thumb)
		uploadReq.ThumbRawFileMD5 = hex.EncodeToString(thumbMD5[:])
		uploadReq.ThumbFileSize = len(encryptedThumb)
	}

	var uploadResp uploadURLResponse
	if err := m.c.post(ctx, "/ilink/bot/getuploadurl", uploadReq, &uploadResp); err != nil {
		return nil, fmt.Errorf("get upload url: %w", err)
	}
	if apiErr := apiError(uploadResp.Ret, uploadResp.ErrCode, uploadResp.ErrMsg); apiErr != nil {
		return nil, fmt.Errorf("getuploadurl failed: %w", apiErr)
	}

	cdnURL, err := m.buildUploadURL(uploadResp.UploadFullURL, uploadResp.UploadParam, fileKey)
	if err != nil {
		return nil, err
	}
	encryptedParam, err := m.uploadToCDN(ctx, cdnURL, encrypted)
	if err != nil {
		return nil, fmt.Errorf("cdn upload: %w", err)
	}

	result := &UploadResult{
		AESKey:         hex.EncodeToString(aesKey),
		FileKey:        fileKey,
		EncryptedParam: encryptedParam,
		FileSize:       len(data),
		CipherSize:     len(encrypted),
	}

	if wantThumb {
		// A missing thumbnail must not sink the whole upload: the media is
		// already on the CDN and is perfectly sendable without a preview.
		thumbURL, err := m.buildUploadURL(uploadResp.ThumbUploadFullURL, uploadResp.ThumbUploadParam, fileKey)
		if err != nil {
			m.logger.Warn("server issued no thumbnail upload URL, sending without preview")
			return result, nil
		}
		thumbParam, err := m.uploadToCDN(ctx, thumbURL, encryptedThumb)
		if err != nil {
			m.logger.Warn("thumbnail upload failed, sending without preview", "error", err)
			return result, nil
		}
		result.ThumbEncryptedParam = thumbParam
		result.ThumbFileSize = len(opts.Thumb)
		result.ThumbCipherSize = len(encryptedThumb)
		result.ThumbWidth = opts.ThumbWidth
		result.ThumbHeight = opts.ThumbHeight
	}

	return result, nil
}

// buildUploadURL prefers the server-provided full URL and falls back to client
// construction from the upload param.
func (m *mediaManager) buildUploadURL(fullURL, uploadParam, fileKey string) (string, error) {
	if fullURL != "" {
		return fullURL, nil
	}
	if uploadParam != "" {
		return fmt.Sprintf("%s/upload?encrypted_query_param=%s&filekey=%s",
			m.cdnBaseURL,
			url.QueryEscape(uploadParam),
			url.QueryEscape(fileKey),
		), nil
	}
	return "", fmt.Errorf("getuploadurl returned no upload URL")
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

// parseBase64AESKey decodes the two encodings CDNMedia.aes_key uses in the wild:
//
//	base64(raw 16 bytes)           — images
//	base64(32-char hex string)     — file / voice / video
//
// The hex form must be validated, not assumed: 32 arbitrary bytes decode
// "successfully" as a length check alone and then silently decrypt to garbage.
func parseBase64AESKey(aesKeyB64 string) ([]byte, error) {
	decoded, err := base64.StdEncoding.DecodeString(aesKeyB64)
	if err != nil {
		return nil, fmt.Errorf("decode base64 aes key: %w", err)
	}
	switch len(decoded) {
	case 16:
		return decoded, nil
	case 32:
		key, err := hex.DecodeString(string(decoded))
		if err != nil {
			return nil, fmt.Errorf("aes key is 32 bytes but not valid hex: %w", err)
		}
		return key, nil
	default:
		return nil, fmt.Errorf("aes key must decode to 16 raw bytes or a 32-char hex string, got %d bytes", len(decoded))
	}
}

// DownloadFileWithBase64Key downloads and decrypts using a base64-encoded key
// (as stored in outbound CDNMedia.AESKey fields).
func (m *mediaManager) DownloadFileWithBase64Key(ctx context.Context, cdnURL, aesKeyB64 string) ([]byte, error) {
	aesKey, err := parseBase64AESKey(aesKeyB64)
	if err != nil {
		return nil, err
	}
	encrypted, err := m.downloadFromCDN(ctx, cdnURL)
	if err != nil {
		return nil, err
	}
	return decryptAESECB(encrypted, aesKey)
}

// DownloadPlain downloads CDN bytes without decrypting them. Some media items
// arrive unencrypted (no aes_key at all); decrypting those corrupts the file.
func (m *mediaManager) DownloadPlain(ctx context.Context, cdnURL string) ([]byte, error) {
	return m.downloadFromCDN(ctx, cdnURL)
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
// If the server provided a full_url, it is used directly; otherwise falls back to client construction.
func (m *mediaManager) BuildDownloadURL(media *CDNMedia) string {
	if media.FullURL != "" {
		return media.FullURL
	}
	return fmt.Sprintf("%s/download?encrypted_query_param=%s",
		m.cdnBaseURL, url.QueryEscape(media.EncryptQueryParam))
}

// outboundAESKey encodes an upload's hex key the way outbound CDNMedia expects:
// base64 of the hex string, not of the raw key bytes.
func outboundAESKey(result *UploadResult) string {
	return base64.StdEncoding.EncodeToString([]byte(result.AESKey))
}

// thumbMedia builds the CDN reference for an uploaded thumbnail, or nil when
// the upload carried none.
func thumbMedia(result *UploadResult) *CDNMedia {
	if !result.HasThumb() {
		return nil
	}
	return &CDNMedia{
		EncryptQueryParam: result.ThumbEncryptedParam,
		AESKey:            outboundAESKey(result),
		EncryptType:       1,
	}
}

// BuildImageItem creates a MessageItem for sending an uploaded image.
// A thumbnail is attached when the upload produced one.
func BuildImageItem(result *UploadResult) MessageItem {
	img := &ImageItem{
		Media: &CDNMedia{
			EncryptQueryParam: result.EncryptedParam,
			AESKey:            outboundAESKey(result),
			EncryptType:       1,
		},
		MidSize: result.CipherSize,
	}
	if tm := thumbMedia(result); tm != nil {
		img.ThumbMedia = tm
		img.ThumbSize = result.ThumbCipherSize
		img.ThumbWidth = result.ThumbWidth
		img.ThumbHeight = result.ThumbHeight
	}
	return MessageItem{Type: ItemTypeImage, ImageItem: img}
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
// When the upload carried a thumbnail, it is attached and its own dimensions
// take precedence over the width/height arguments.
func BuildVideoItem(result *UploadResult, width, height, duration int) MessageItem {
	video := &VideoItem{
		Media: &CDNMedia{
			EncryptQueryParam: result.EncryptedParam,
			AESKey:            outboundAESKey(result),
			EncryptType:       1,
		},
		VideoSize:   result.FileSize,
		PlayLength:  duration,
		ThumbWidth:  width,
		ThumbHeight: height,
	}
	if tm := thumbMedia(result); tm != nil {
		video.ThumbMedia = tm
		video.ThumbSize = result.ThumbCipherSize
		if result.ThumbWidth > 0 {
			video.ThumbWidth = result.ThumbWidth
		}
		if result.ThumbHeight > 0 {
			video.ThumbHeight = result.ThumbHeight
		}
	}
	return MessageItem{Type: ItemTypeVideo, VideoItem: video}
}
