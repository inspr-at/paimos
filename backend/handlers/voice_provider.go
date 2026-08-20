// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"context"
	"fmt"
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

func synthesizeVoice(ctx context.Context, settings VoiceSettings, text, language string) ([]byte, error) {
	if settings.Provider != "elevenlabs" {
		return nil, fmt.Errorf("unsupported voice synthesis provider")
	}
	return synthesizeWithElevenLabs(ctx, settings, text, language)
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
