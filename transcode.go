package ilink

import (
	"bytes"
	"encoding/binary"
)

// DefaultVoiceSampleRate is the sample rate WeChat uses for SILK voice messages.
const DefaultVoiceSampleRate = 24000

// silkMagic is the SILK v3 header. WeChat prefixes it with a single 0x02 byte.
var silkMagic = []byte("#!SILK_V3")

// IsSilk reports whether data looks like a WeChat SILK voice payload, with or
// without the leading 0x02 byte WeChat prepends.
func IsSilk(data []byte) bool {
	if bytes.HasPrefix(data, silkMagic) {
		return true
	}
	return len(data) > 1 && data[0] == 0x02 && bytes.HasPrefix(data[1:], silkMagic)
}

// StripSilkPrefix removes WeChat's leading 0x02 byte so the payload starts at
// the SILK v3 magic, which is what most decoders expect.
func StripSilkPrefix(data []byte) []byte {
	if len(data) > 1 && data[0] == 0x02 && bytes.HasPrefix(data[1:], silkMagic) {
		return data[1:]
	}
	return data
}

// VoiceTranscoder converts an inbound SILK voice payload into WAV so that
// downstream consumers (ASR, playback) can handle it.
//
// The SDK ships no SILK decoder: the codec needs cgo or a large pure-Go port,
// and forcing that dependency on every user is the wrong default. Supply an
// implementation via WithVoiceTranscoder when you need decoded audio; the raw
// SILK bytes are passed through untouched when no transcoder is configured.
//
// A typical implementation decodes to signed 16-bit little-endian mono PCM and
// wraps it with PCMToWAV.
type VoiceTranscoder interface {
	// ToWAV converts SILK bytes to a complete WAV file.
	// sampleRate is taken from the inbound VoiceItem, or DefaultVoiceSampleRate.
	ToWAV(silk []byte, sampleRate int) ([]byte, error)
}

// VoiceTranscoderFunc adapts a plain function to the VoiceTranscoder interface.
type VoiceTranscoderFunc func(silk []byte, sampleRate int) ([]byte, error)

// ToWAV implements VoiceTranscoder.
func (f VoiceTranscoderFunc) ToWAV(silk []byte, sampleRate int) ([]byte, error) {
	return f(silk, sampleRate)
}

// PCMToWAV wraps raw signed 16-bit little-endian mono PCM in a WAV container.
// Use it to finish a SILK decoder that emits bare PCM.
func PCMToWAV(pcm []byte, sampleRate int) []byte {
	if sampleRate <= 0 {
		sampleRate = DefaultVoiceSampleRate
	}
	const (
		headerSize    = 44
		numChannels   = 1
		bitsPerSample = 16
	)
	blockAlign := numChannels * bitsPerSample / 8
	byteRate := sampleRate * blockAlign

	buf := make([]byte, headerSize+len(pcm))
	copy(buf[0:4], "RIFF")
	binary.LittleEndian.PutUint32(buf[4:8], uint32(headerSize+len(pcm)-8))
	copy(buf[8:12], "WAVE")

	copy(buf[12:16], "fmt ")
	binary.LittleEndian.PutUint32(buf[16:20], 16) // PCM fmt chunk size
	binary.LittleEndian.PutUint16(buf[20:22], 1)  // format: PCM
	binary.LittleEndian.PutUint16(buf[22:24], numChannels)
	binary.LittleEndian.PutUint32(buf[24:28], uint32(sampleRate))
	binary.LittleEndian.PutUint32(buf[28:32], uint32(byteRate))
	binary.LittleEndian.PutUint16(buf[32:34], uint16(blockAlign))
	binary.LittleEndian.PutUint16(buf[34:36], bitsPerSample)

	copy(buf[36:40], "data")
	binary.LittleEndian.PutUint32(buf[40:44], uint32(len(pcm)))
	copy(buf[44:], pcm)
	return buf
}
