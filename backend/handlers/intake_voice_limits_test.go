// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package handlers_test

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/inspr-at/paimos/backend/db"
)

// PAI-724 gate + metering coverage for the paid voice endpoints. The
// in-memory limiters are reset per test by newTestServer; the daily
// budgets live in the per-test DB.

// TestIntakeVoice_STTMetersUnitsAndCost: a successful transcription
// records estimated audio seconds and a non-zero cost estimate — spend
// is no longer invisible on the papertrail.
func TestIntakeVoice_STTMetersUnitsAndCost(t *testing.T) {
	ts := newTestServer(t)
	upstream := fakeScribe(t, "metered words", "test-elevenlabs-key")
	defer upstream.Close()
	configureVoice(t, ts, upstream.URL)
	s := createIntakeSession(t, ts, ts.memberCookie)

	// 8000 bytes ≈ 2 s at the 4000 B/s opus estimate.
	resp := postAudio(t, ts, s.ID, "audio/webm", bytes.Repeat([]byte("a"), 8000))
	assertStatus(t, resp, http.StatusOK)

	var units, cost int64
	if err := db.DB.QueryRow(
		`SELECT prompt_tokens, cost_micro_usd FROM ai_calls WHERE action_key='intake_stt'`).Scan(&units, &cost); err != nil {
		t.Fatal(err)
	}
	if units != 2 || cost != 2*111 {
		t.Fatalf("stt metering = (%d units, %d µUSD), want (2, 222)", units, cost)
	}
}

// TestIntakeVoice_TTSMetersUnitsAndCost: characters synthesized and an
// estimated cost land on the papertrail row.
func TestIntakeVoice_TTSMetersUnitsAndCost(t *testing.T) {
	ts := newTestServer(t)
	upstream := fakeTTS(t, "test-elevenlabs-key", fakeMPEGAudio())
	defer upstream.Close()
	configureVoice(t, ts, upstream.URL)
	s := createIntakeSession(t, ts, ts.memberCookie)
	seedSummaries(t, s.ID)

	resp := ts.post(t, "/api/intake/sessions/"+itoa(s.ID)+"/tts", ts.memberCookie,
		map[string]any{"level": "eli10"})
	assertStatus(t, resp, http.StatusOK)

	// seedSummaries eli10 text is 32 characters.
	var units, cost int64
	if err := db.DB.QueryRow(
		`SELECT prompt_tokens, cost_micro_usd FROM ai_calls WHERE action_key='intake_tts'`).Scan(&units, &cost); err != nil {
		t.Fatal(err)
	}
	if units != 32 || cost != 32*100 {
		t.Fatalf("tts metering = (%d units, %d µUSD), want (32, 3200)", units, cost)
	}
}

// TestIntakeVoice_STTBurstLimit: the 21st utterance inside a minute is
// rejected with 429 + Retry-After instead of billing the account.
func TestIntakeVoice_STTBurstLimit(t *testing.T) {
	ts := newTestServer(t)
	upstream := fakeScribe(t, "burst", "test-elevenlabs-key")
	defer upstream.Close()
	configureVoice(t, ts, upstream.URL)
	s := createIntakeSession(t, ts, ts.memberCookie)

	for range 20 {
		resp := postAudio(t, ts, s.ID, "audio/webm", []byte("tiny"))
		assertStatus(t, resp, http.StatusOK)
	}
	resp := postAudio(t, ts, s.ID, "audio/webm", []byte("tiny"))
	assertStatus(t, resp, http.StatusTooManyRequests)
	if resp.Header.Get("Retry-After") == "" {
		t.Fatal("429 without Retry-After header")
	}
}

// TestIntakeVoice_TTSBurstLimit: same for TTS at its lower per-minute cap.
func TestIntakeVoice_TTSBurstLimit(t *testing.T) {
	ts := newTestServer(t)
	upstream := fakeTTS(t, "test-elevenlabs-key", fakeMPEGAudio())
	defer upstream.Close()
	configureVoice(t, ts, upstream.URL)
	s := createIntakeSession(t, ts, ts.memberCookie)
	seedSummaries(t, s.ID)

	for range 10 {
		resp := ts.post(t, "/api/intake/sessions/"+itoa(s.ID)+"/tts", ts.memberCookie,
			map[string]any{"level": "eli10"})
		assertStatus(t, resp, http.StatusOK)
	}
	resp := ts.post(t, "/api/intake/sessions/"+itoa(s.ID)+"/tts", ts.memberCookie,
		map[string]any{"level": "eli10"})
	assertStatus(t, resp, http.StatusTooManyRequests)
	if resp.Header.Get("Retry-After") == "" {
		t.Fatal("429 without Retry-After header")
	}
}

// TestIntakeVoice_STTDailyBudget: audio-second budget blocks members
// once today's recorded units would be exceeded; admins pass (soft cap).
func TestIntakeVoice_STTDailyBudget(t *testing.T) {
	t.Setenv("PAIMOS_VOICE_STT_DAILY_SECONDS", "3")
	ts := newTestServer(t)
	upstream := fakeScribe(t, "budget", "test-elevenlabs-key")
	defer upstream.Close()
	configureVoice(t, ts, upstream.URL)
	s := createIntakeSession(t, ts, ts.memberCookie)

	blob := bytes.Repeat([]byte("a"), 8000) // ≈ 2 s
	resp := postAudio(t, ts, s.ID, "audio/webm", blob)
	assertStatus(t, resp, http.StatusOK) // 0 + 2 ≤ 3

	resp = postAudio(t, ts, s.ID, "audio/webm", blob)
	assertStatus(t, resp, http.StatusTooManyRequests) // 2 + 2 > 3
	if resp.Header.Get("Retry-After") == "" {
		t.Fatal("budget 429 without Retry-After header")
	}

	// Admin bypass on their own session.
	sa := createIntakeSession(t, ts, ts.adminCookie)
	req, err := http.NewRequest("POST",
		ts.srv.URL+"/api/intake/sessions/"+itoa(sa.ID)+"/audio", bytes.NewReader(blob))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Cookie", ts.adminCookie)
	req.Header.Set("Content-Type", "audio/webm")
	r2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	assertStatus(t, r2, http.StatusOK)
}

// TestIntakeVoice_TTSDailyBudget: character budget blocks members.
func TestIntakeVoice_TTSDailyBudget(t *testing.T) {
	t.Setenv("PAIMOS_VOICE_TTS_DAILY_CHARS", "40")
	ts := newTestServer(t)
	upstream := fakeTTS(t, "test-elevenlabs-key", fakeMPEGAudio())
	defer upstream.Close()
	configureVoice(t, ts, upstream.URL)
	s := createIntakeSession(t, ts, ts.memberCookie)
	seedSummaries(t, s.ID)

	resp := ts.post(t, "/api/intake/sessions/"+itoa(s.ID)+"/tts", ts.memberCookie,
		map[string]any{"level": "eli10"})
	assertStatus(t, resp, http.StatusOK) // 0 + 32 ≤ 40

	resp = ts.post(t, "/api/intake/sessions/"+itoa(s.ID)+"/tts", ts.memberCookie,
		map[string]any{"level": "eli10"})
	assertStatus(t, resp, http.StatusTooManyRequests) // 32 + 32 > 40
}

// TestIntakeVoice_UsageCapApplies: a user with an admin-set zero AI cap
// can no longer spend voice money either (PAI-161 gate is wired in).
func TestIntakeVoice_UsageCapApplies(t *testing.T) {
	ts := newTestServer(t)
	upstream := fakeScribe(t, "capped", "test-elevenlabs-key")
	defer upstream.Close()
	configureVoice(t, ts, upstream.URL)
	s := createIntakeSession(t, ts, ts.memberCookie)

	if _, err := db.DB.Exec(`UPDATE users SET ai_cap_override_tokens=0 WHERE username='member'`); err != nil {
		t.Fatal(err)
	}
	resp := postAudio(t, ts, s.ID, "audio/webm", []byte("x"))
	assertStatus(t, resp, http.StatusTooManyRequests)
}
