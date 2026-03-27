package ilink

import (
	"encoding/base64"
	"fmt"
	"os"
)

// TerminalQR is a QRCallback that prints a QR code to stdout using ASCII art.
// It requires no external dependencies — it decodes the base64 PNG and prints
// a URL users can copy, or a simple block representation.
//
// For a richer terminal display, replace with:
//
//	import "github.com/mdp/qrterminal/v3"
//	bot.Login(ctx, func(content string) {
//	    qrterminal.GenerateHalfBlock(content, qrterminal.L, os.Stdout)
//	})
func TerminalQR(qrImgContent string) {
	fmt.Fprintln(os.Stdout, "=== WeChat Login QR Code ===")

	// qrImgContent may be either the QR code URL or base64-encoded image data.
	// Try to detect: if it starts with "http" it's a URL, otherwise it's base64 PNG.
	if len(qrImgContent) > 4 && qrImgContent[:4] == "http" {
		fmt.Fprintf(os.Stdout, "Open this URL to scan: %s\n", qrImgContent)
		return
	}

	// Decode base64 and save as a temporary PNG for the user to open.
	data, err := base64.StdEncoding.DecodeString(qrImgContent)
	if err != nil {
		// Fallback: print the raw content and let the user figure it out.
		fmt.Fprintf(os.Stdout, "QR content: %s\n", qrImgContent)
		return
	}

	const tmpFile = "/tmp/ilink-qr.png"
	if err := os.WriteFile(tmpFile, data, 0600); err == nil {
		fmt.Fprintf(os.Stdout, "QR code saved to %s — open it to scan\n", tmpFile)
	} else {
		fmt.Fprintf(os.Stdout, "QR image (base64, %d bytes decoded)\n", len(data))
	}
}
