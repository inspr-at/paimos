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

// PAI-710 (epic PAI-703). Speech input for intake sessions: one utterance
// blob in, one transcript chunk out. The browser records utterances
// (MediaRecorder, client-side RMS silence gate) and POSTs each blob; the
// backend forwards it to the configured STT provider and appends the text
// through the exact same path as typed input, so the orchestrator,
// time-travel and SSE fan-out see no difference (payload carries
// spoken:true for provenance).
//
// INV-INTAKE-06: audio is transcribed and DROPPED — a voice recording is
// biometric-adjacent personal data and there is no reason to keep it once
// the words exist (amt-start research conclusion). No audio bytes are
// ever written to disk, the DB, logs, or ai_calls.

package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/inspr-at/paimos/backend/db"
)

const (
	intakeAudioMaxBytes    = 12 << 20 // matches the amt-start transcribe cap
	intakeAudioSTTTimeout  = 60 * time.Second
	intakeAudioMinTextRune = 1
)

// voiceSTTTransport is swappable in tests (points the provider call at a
// fake upstream without real credentials).
var voiceSTTHTTPClient = &http.Client{Timeout: intakeAudioSTTTimeout}

// TranscribeIntakeAudio handles POST /api/intake/sessions/{id}/audio.
func TranscribeIntakeAudio(w http.ResponseWriter, r *http.Request) {
	s, user, ok := requireIntakeSession(w, r)
	if !ok {
		return
	}
	if s.Status != "active" {
		jsonError(w, "intake session is not active", http.StatusConflict)
		return
	}
	vs, err := LoadVoiceSettings()
	if err != nil {
		log.Printf("intake audio: voice settings: %v", err)
		jsonError(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !vs.Available() {
		jsonError(w, "voice input is not configured", http.StatusServiceUnavailable)
		return
	}

	contentType := strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0])
	// Safari sometimes labels MediaRecorder output video/* — accept both.
	if !strings.HasPrefix(contentType, "audio/") && !strings.HasPrefix(contentType, "video/") {
		jsonError(w, "unsupported media type", http.StatusUnsupportedMediaType)
		return
	}
	audio, err := io.ReadAll(io.LimitReader(r.Body, intakeAudioMaxBytes+1))
	if err != nil {
		jsonError(w, "read failed", http.StatusBadRequest)
		return
	}
	if len(audio) > intakeAudioMaxBytes {
		jsonError(w, "audio exceeds 12 MiB", http.StatusRequestEntityTooLarge)
		return
	}
	if len(audio) == 0 {
		jsonError(w, "empty audio", http.StatusUnprocessableEntity)
		return
	}
	// Capacity guards identical to typed chunks — capture must fail the
	// same way regardless of input mode.
	if s.TranscriptBytes >= intakeTranscriptMaxBytes {
		jsonError(w, "session transcript is full — create the issue or start a new session", http.StatusConflict)
		return
	}
	// PAI-724: paid-call gates (concurrency, burst, daily budgets)
	// before any provider spend.
	estSeconds := voiceEstimateSeconds(len(audio))
	release, admitted := voiceAdmit(w, r, user, voiceActionIntakeSTT, estSeconds)
	if !admitted {
		return
	}
	defer release()

	started := time.Now()
	text, sttErr := transcribeVoice(r.Context(), vs, contentType, audio, s.Language)
	latency := time.Since(started)
	// Paper-trail parity with every other provider call (INV-INTAKE-04):
	// metadata only — never audio, never the transcript text. Units are
	// estimated audio seconds; the provider bills them even when the
	// transcript comes back empty (no_op).
	outcome, errorClass := "ok", ""
	if sttErr != nil {
		outcome, errorClass = "fail_upstream", "upstream"
	} else if strings.TrimSpace(text) == "" {
		outcome = "no_op"
	}
	var billedSeconds int64
	if sttErr == nil {
		billedSeconds = estSeconds
	}
	recordVoiceAICall(r.Context(), aiCallArgs{
		RequestID: newAIRequestID(), UserID: &user.ID, ActionKey: voiceActionIntakeSTT,
		Surface: "intake", ProjectID: s.activeProjectID(),
		Provider: vs.Provider, Model: vs.STTModel,
		PromptTokens: int(billedSeconds), CostMicroUSD: billedSeconds * voiceSTTMicroUSDPerSecond,
		Outcome: outcome, ErrorClass: errorClass, LatencyMs: latency.Milliseconds(),
	})
	if sttErr != nil {
		log.Printf("intake audio: stt upstream error (session=%d): %v", s.ID, sttErrSafe(sttErr))
		jsonError(w, "voice transcription failed", http.StatusBadGateway)
		return
	}
	text = strings.TrimSpace(text)
	if len([]rune(text)) < intakeAudioMinTextRune {
		jsonError(w, "no speech detected", http.StatusUnprocessableEntity)
		return
	}
	if len(text) > intakeChunkMaxBytes {
		text = truncateVoiceUTF8(text, intakeChunkMaxBytes)
	}

	// Append through the same event path as typed chunks.
	count, err := intakeEventCount(r.Context(), db.DB, s.ID)
	if handleDBError(w, err, "intake audio") {
		return
	}
	if count >= intakeMaxEventsPerSess {
		jsonError(w, "session event limit reached — create the issue or start a new session", http.StatusConflict)
		return
	}
	payload, _ := json.Marshal(map[string]any{"text": text, "spoken": true})
	tx, err := db.DB.BeginTx(r.Context(), nil)
	if handleDBError(w, err, "intake audio") {
		return
	}
	defer tx.Rollback()
	seq, err := appendIntakeEventTx(r.Context(), tx, s.ID, "transcript_chunk", "user", "", string(payload))
	if handleDBError(w, err, "intake audio") {
		return
	}
	sep := ""
	if s.TranscriptBytes > 0 {
		sep = "\n"
	}
	if _, err := tx.ExecContext(r.Context(),
		`UPDATE intake_sessions
		 SET transcript = transcript || ? || ?, transcript_bytes = transcript_bytes + ?
		 WHERE id = ?`, sep, text, len(sep)+len(text), s.ID); err != nil {
		if handleDBError(w, err, "intake audio") {
			return
		}
	}
	if handleDBError(w, tx.Commit(), "intake audio") {
		return
	}
	publishIntakeEvent(r.Context(), s.ID, seq)
	notifyIntakeOrchestrator(s.ID)
	jsonOK(w, map[string]any{"seq": seq, "text": text})
}

// transcribeWithElevenLabs calls the batch Scribe endpoint. Language
// mapping follows the amt-start adapter (de→deu, en→eng, ISO 639-3).
func transcribeWithElevenLabs(ctx context.Context, vs VoiceSettings, contentType string, audio []byte, language string) (string, error) {
	lang := map[string]string{"de": "deu", "en": "eng"}[language]

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", "utterance"+extForMime(contentType))
	if err != nil {
		return "", err
	}
	if _, err := fw.Write(audio); err != nil {
		return "", err
	}
	_ = mw.WriteField("model_id", vs.STTModel)
	if lang != "" {
		_ = mw.WriteField("language_code", lang)
	}
	if err := mw.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(vs.BaseURL, "/")+"/v1/speech-to-text", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("xi-api-key", vs.APIKey)

	resp, err := voiceSTTHTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// A DNS failure on the EU residency host means "residency not
		// provisioned", which is a different problem than a bad key —
		// keep the status visible, never the response body (it could
		// echo request details).
		return "", fmt.Errorf("stt upstream status %d", resp.StatusCode)
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return "", fmt.Errorf("stt response parse: %w", err)
	}
	return out.Text, nil
}

func extForMime(contentType string) string {
	switch contentType {
	case "audio/webm", "video/webm":
		return ".webm"
	case "audio/mp4", "video/mp4":
		return ".mp4"
	case "audio/mpeg":
		return ".mp3"
	case "audio/wav", "audio/x-wav":
		return ".wav"
	case "audio/ogg":
		return ".ogg"
	}
	return ".bin"
}

// sttErrSafe keeps upstream errors log-safe (status/class only — no
// bodies, no URLs with keys; ElevenLabs auth is header-based so URLs are
// clean, but keep the principle).
func sttErrSafe(err error) string {
	msg := err.Error()
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return msg
}
