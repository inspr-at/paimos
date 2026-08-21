// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"
)

// These provider-neutral seams are shared by Intake and Agent Mode. The HTTP
// handlers intentionally remain separate because Intake persists transcripts
// and summaries while Agent Mode must be ephemeral and template-only.
func transcribeVoice(ctx context.Context, settings VoiceSettings, contentType string, audio []byte, language string) (string, error) {
	if settings.Provider != "elevenlabs" {
		return "", fmt.Errorf("unsupported voice transcription provider")
	}
	return transcribeWithElevenLabs(ctx, settings, contentType, audio, language)
}

func synthesizeVoice(ctx context.Context, settings VoiceSettings, text, language string) (voiceMPEGAudio, error) {
	if settings.Provider != "elevenlabs" {
		return voiceMPEGAudio{}, fmt.Errorf("unsupported voice synthesis provider")
	}
	return synthesizeWithElevenLabs(ctx, settings, text, language)
}

// voiceMPEGAudio is the validated binary boundary between the remote voice
// provider and HTTP output. Keeping the payload private prevents callers from
// accidentally serving arbitrary provider bytes under an executable media
// type; values can only be constructed after the MIME, size, and MPEG frame
// checks in newVoiceMPEGAudio.
type voiceMPEGAudio struct {
	payload []byte
}

func newVoiceMPEGAudio(payload []byte) (voiceMPEGAudio, error) {
	if len(payload) == 0 {
		return voiceMPEGAudio{}, fmt.Errorf("tts upstream returned no audio")
	}
	if !hasCompleteMPEGFrame(payload) {
		return voiceMPEGAudio{}, fmt.Errorf("tts upstream returned invalid MPEG audio")
	}
	return voiceMPEGAudio{payload: payload}, nil
}

// hasCompleteMPEGFrame accepts an optional ID3v2 tag followed by a complete
// MPEG-1/2/2.5 Layer III frame. The response remains opaque compressed audio;
// this is a structural type check that rejects HTML/SVG/error payloads even
// when a compromised provider labels them audio/mpeg.
func hasCompleteMPEGFrame(payload []byte) bool {
	offset := 0
	if len(payload) >= 3 && bytes.Equal(payload[:3], []byte("ID3")) {
		if len(payload) < 10 || payload[3] < 2 || payload[3] > 4 {
			return false
		}
		var tagSize int
		for _, value := range payload[6:10] {
			if value&0x80 != 0 {
				return false
			}
			tagSize = tagSize<<7 | int(value)
		}
		offset = 10 + tagSize
		if payload[3] == 4 && payload[5]&0x10 != 0 {
			offset += 10
		}
	}
	if offset < 0 || len(payload)-offset < 4 {
		return false
	}
	header := payload[offset : offset+4]
	if header[0] != 0xff || header[1]&0xe0 != 0xe0 {
		return false
	}
	version := (header[1] >> 3) & 0x03
	layer := (header[1] >> 1) & 0x03
	bitrateIndex := (header[2] >> 4) & 0x0f
	sampleRateIndex := (header[2] >> 2) & 0x03
	if version == 1 || layer != 1 || bitrateIndex == 0 || bitrateIndex == 15 || sampleRateIndex == 3 {
		return false
	}
	bitratesMPEG1 := [...]int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320}
	bitratesMPEG2 := [...]int{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160}
	sampleRates := [...]int{44100, 48000, 32000}
	bitrate := bitratesMPEG2[bitrateIndex]
	coefficient := 72000
	sampleRate := sampleRates[sampleRateIndex]
	if version == 3 {
		bitrate = bitratesMPEG1[bitrateIndex]
		coefficient = 144000
	} else if version == 0 {
		sampleRate /= 4
	} else {
		sampleRate /= 2
	}
	frameLength := coefficient*bitrate/sampleRate + int((header[2]>>1)&1)
	return frameLength >= 4 && len(payload)-offset >= frameLength
}

func writeVoiceMPEGResponse(w http.ResponseWriter, audio voiceMPEGAudio) error {
	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", strconv.Itoa(len(audio.payload)))
	_, err := io.Copy(w, bytes.NewReader(audio.payload))
	return err
}

// recordVoiceAICall survives a disconnected/cancelled HTTP request. The paid
// provider attempt already happened, so metadata-only accounting and the
// durable shared budget must not disappear with the response writer.
func recordVoiceAICall(requestContext context.Context, args aiCallArgs) {
	auditContext, cancel := context.WithTimeout(context.WithoutCancel(requestContext), 2*time.Second)
	defer cancel()
	recordAICall(auditContext, args)
}

func truncateVoiceUTF8(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	prefix := value[:maxBytes]
	for len(prefix) > 0 && !utf8.ValidString(prefix) {
		prefix = prefix[:len(prefix)-1]
	}
	return prefix
}
