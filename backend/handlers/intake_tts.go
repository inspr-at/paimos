// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public
// License along with this program. If not, see <https://www.gnu.org/licenses/>.

// PAI-714 (epic PAI-703). Speak the understanding check: the client asks
// for a LEVEL (eli5|eli10|eli15), the server reads that text from the
// session's own summaries artifact and returns synthesized speech as raw
// audio/mpeg bytes with cache-control: no-store — bytes, not URLs, so
// nothing is stored and nothing needs authorizing later (amt-start
// pattern). Binding the endpoint to the session artifact (instead of
// accepting caller text) keeps it from being a free-form TTS proxy.
//
// The paper trail records metadata only (INV-INTAKE-02 discipline): no
// text, no audio.

package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

const (
	intakeTTSMaxChars = 2000 // truncate, don't reject (amt-start cap)
	intakeTTSTimeout  = 45 * time.Second
)

var intakeTTSHTTPClient = &http.Client{Timeout: intakeTTSTimeout}

// SpeakIntakeSummary handles POST /api/intake/sessions/{id}/tts
// with body {"level":"eli5"|"eli10"|"eli15"}.
func SpeakIntakeSummary(w http.ResponseWriter, r *http.Request) {
	s, user, ok := requireIntakeSession(w, r)
	if !ok {
		return
	}
	var body struct {
		Level string `json:"level"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid body", http.StatusBadRequest)
		return
	}
	switch body.Level {
	case "eli5", "eli10", "eli15":
	default:
		jsonError(w, "level must be eli5, eli10 or eli15", http.StatusBadRequest)
		return
	}
	vs, err := LoadVoiceSettings()
	if err != nil {
		log.Printf("intake tts: voice settings: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !vs.Available() {
		jsonError(w, "voice output is not configured", http.StatusServiceUnavailable)
		return
	}

	// The text comes from the session's own artifact — never the caller.
	state, serr := intakeStateAt(r.Context(), s.ID, s.Rev)
	if handleDBError(w, serr, "intake state") {
		return
	}
	var summaries struct {
		ELI5     string `json:"eli5"`
		ELI10    string `json:"eli10"`
		ELI15    string `json:"eli15"`
		Language string `json:"language"`
	}
	if raw, ok := state.Artifacts["summaries"]; ok {
		_ = json.Unmarshal(raw, &summaries)
	}
	text := map[string]string{
		"eli5": summaries.ELI5, "eli10": summaries.ELI10, "eli15": summaries.ELI15,
	}[body.Level]
	text = strings.TrimSpace(text)
	if text == "" {
		jsonError(w, "no summary to speak yet", http.StatusUnprocessableEntity)
		return
	}
	if len(text) > intakeTTSMaxChars {
		text = text[:intakeTTSMaxChars]
	}
	// PAI-724: paid-call gates (concurrency, burst, daily budgets)
	// before any provider spend. Units are the characters synthesized —
	// what ElevenLabs bills by.
	chars := int64(len([]rune(text)))
	release, admitted := voiceAdmit(w, r, user, "intake_tts", chars)
	if !admitted {
		return
	}
	defer release()

	started := time.Now()
	audio, ttsErr := synthesizeWithElevenLabs(r.Context(), vs, text, summaries.Language)
	latency := time.Since(started)
	outcome, errorClass := "ok", ""
	if ttsErr != nil {
		outcome, errorClass = "fail_upstream", "upstream"
	}
	var billedChars int64
	if ttsErr == nil {
		billedChars = chars
	}
	recordAICall(r.Context(), aiCallArgs{
		RequestID: newAIRequestID(), UserID: &user.ID, ActionKey: "intake_tts",
		SubAction: body.Level, Surface: "intake", ProjectID: s.activeProjectID(),
		Provider: vs.Provider, Model: vs.TTSModel,
		PromptTokens: int(billedChars), CostMicroUSD: billedChars * voiceTTSMicroUSDPerChar,
		Outcome: outcome, ErrorClass: errorClass, LatencyMs: latency.Milliseconds(),
	})
	if ttsErr != nil {
		log.Printf("intake tts: upstream error (session=%d): %v", s.ID, sttErrSafe(ttsErr))
		jsonError(w, "voice synthesis failed", http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(audio)))
	_, _ = w.Write(audio)
}

// synthesizeWithElevenLabs calls the batch TTS endpoint; language maps to
// ISO 639-1 for the multilingual model (de/en pass through).
func synthesizeWithElevenLabs(ctx context.Context, vs VoiceSettings, text, language string) ([]byte, error) {
	payload := map[string]any{
		"text":     text,
		"model_id": vs.TTSModel,
	}
	if language == "de" || language == "en" {
		payload["language_code"] = language
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(vs.BaseURL, "/")+"/v1/text-to-speech/"+vs.TTSVoiceID,
		strings.NewReader(string(raw)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("xi-api-key", vs.APIKey)

	resp, err := intakeTTSHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("tts upstream status %d", resp.StatusCode)
	}
	audio, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if len(audio) == 0 {
		return nil, fmt.Errorf("tts upstream returned no audio")
	}
	return audio, nil
}
