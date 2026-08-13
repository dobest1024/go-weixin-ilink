package ilink

import (
	"bytes"
	"net/url"
	"path"
	"strings"
)

// extToMIME covers the file types that actually travel over the iLink channel.
// It is deliberately small: mime.TypeByExtension depends on the host's
// /etc/mime.types and returns inconsistent results across platforms.
var extToMIME = map[string]string{
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".gif":  "image/gif",
	".webp": "image/webp",
	".bmp":  "image/bmp",
	".heic": "image/heic",
	".mp4":  "video/mp4",
	".mov":  "video/quicktime",
	".webm": "video/webm",
	".mp3":  "audio/mpeg",
	".wav":  "audio/wav",
	".amr":  "audio/amr",
	".silk": "audio/silk",
	".ogg":  "audio/ogg",
	".m4a":  "audio/mp4",
	".pdf":  "application/pdf",
	".doc":  "application/msword",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".xls":  "application/vnd.ms-excel",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".ppt":  "application/vnd.ms-powerpoint",
	".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	".zip":  "application/zip",
	".txt":  "text/plain",
	".md":   "text/markdown",
	".csv":  "text/csv",
	".json": "application/json",
}

// mimeToExt is the reverse lookup, with one canonical extension per type.
var mimeToExt = map[string]string{
	"image/jpeg":      ".jpg",
	"image/png":       ".png",
	"image/gif":       ".gif",
	"image/webp":      ".webp",
	"image/bmp":       ".bmp",
	"image/heic":      ".heic",
	"video/mp4":       ".mp4",
	"video/quicktime": ".mov",
	"video/webm":      ".webm",
	"audio/mpeg":      ".mp3",
	"audio/wav":       ".wav",
	"audio/x-wav":     ".wav",
	"audio/amr":       ".amr",
	"audio/silk":      ".silk",
	"audio/ogg":       ".ogg",
	"audio/mp4":       ".m4a",
	"application/pdf": ".pdf",
	"application/zip": ".zip",
	"text/plain":      ".txt",
	"text/markdown":   ".md",
	"text/csv":        ".csv",
}

// DefaultMIME is returned when the type cannot be determined.
const DefaultMIME = "application/octet-stream"

// MIMEFromFilename guesses a content type from a file name's extension.
// Returns DefaultMIME when the extension is unknown or absent.
func MIMEFromFilename(name string) string {
	ext := strings.ToLower(path.Ext(name))
	if mt, ok := extToMIME[ext]; ok {
		return mt
	}
	return DefaultMIME
}

// ExtensionFromMIME maps a content type to a canonical file extension
// (including the leading dot). Returns "" when unknown.
func ExtensionFromMIME(contentType string) string {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	return mimeToExt[ct]
}

// ExtensionFromContentTypeOrURL picks a file extension for a downloaded media
// file, preferring the server's Content-Type and falling back to the URL path.
// Returns ".bin" when neither yields a known type.
func ExtensionFromContentTypeOrURL(contentType, rawURL string) string {
	if ext := ExtensionFromMIME(contentType); ext != "" {
		return ext
	}
	if u, err := url.Parse(rawURL); err == nil {
		if ext := strings.ToLower(path.Ext(u.Path)); ext != "" {
			if _, ok := extToMIME[ext]; ok {
				return ext
			}
		}
	}
	return ".bin"
}

// DetectMIME sniffs the leading bytes of a media file. It recognises the
// formats WeChat actually delivers, including SILK voice, which Go's
// http.DetectContentType does not know about. Returns "" when unrecognised.
func DetectMIME(data []byte) string {
	switch {
	case len(data) >= 3 && bytes.Equal(data[:3], []byte{0xFF, 0xD8, 0xFF}):
		return "image/jpeg"
	case len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}):
		return "image/png"
	case len(data) >= 6 && (bytes.Equal(data[:6], []byte("GIF87a")) || bytes.Equal(data[:6], []byte("GIF89a"))):
		return "image/gif"
	case len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return "image/webp"
	case len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WAVE")):
		return "audio/wav"
	case len(data) >= 12 && bytes.Equal(data[4:8], []byte("ftyp")):
		return "video/mp4"
	case IsSilk(data):
		return "audio/silk"
	case len(data) >= 6 && bytes.Equal(data[:6], []byte("#!AMR\n")):
		return "audio/amr"
	case len(data) >= 4 && bytes.Equal(data[:4], []byte("%PDF")):
		return "application/pdf"
	case len(data) >= 4 && bytes.Equal(data[:4], []byte{'P', 'K', 0x03, 0x04}):
		return "application/zip"
	case len(data) >= 3 && bytes.Equal(data[:3], []byte{'I', 'D', '3'}):
		return "audio/mpeg"
	}
	return ""
}
