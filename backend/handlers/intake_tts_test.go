// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package handlers_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/backend/db"
)

// fakeTTS stands in for the ElevenLabs text-to-speech endpoint.
func fakeTTS(t *testing.T, wantKey string, audio []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1/text-to-speech/") {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("xi-api-key") != wantKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body struct {
			Text    string `json:"text"`
			ModelID string `json:"model_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Text == "" || body.ModelID == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write(audio)
	}))
}

func fakeMPEGAudio() []byte {
	// MPEG-1 Layer III, 128 kbps, 44.1 kHz: the frame header declares a
	// 417-byte frame. The payload is intentionally synthetic; provider-boundary
	// tests need structurally valid MPEG without committing a binary fixture.
	audio := make([]byte, 417)
	copy(audio, []byte{0xff, 0xfb, 0x90, 0x64})
	return audio
}

func seedSummaries(t *testing.T, sessionID int64) {
	t.Helper()
	payload := `{"eli5":"The game counts beaten monsters.","eli10":"Kill events increment a counter.","eli15":"A combat hook emits kill events consumed by progression.","language":"en"}`
	var rev int64
	if err := db.DB.QueryRow(`SELECT rev FROM intake_sessions WHERE id=?`, sessionID).Scan(&rev); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(
		`UPDATE intake_sessions SET rev = rev + 1 WHERE id = ?`, sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(
		`INSERT INTO intake_events (session_id, seq, kind, source, payload_json) VALUES (?, ?, 'summaries', 'ai', ?)`,
		sessionID, rev+1, payload); err != nil {
		t.Fatal(err)
	}
}

// TestIntakeTTS_SpeaksSessionSummary: the endpoint reads the session's own
// summaries artifact (never caller text), returns audio bytes with
// no-store, and records a metadata-only paper-trail row.
func TestIntakeTTS_SpeaksSessionSummary(t *testing.T) {
	ts := newTestServer(t)
	audioBytes := fakeMPEGAudio()
	upstream := fakeTTS(t, "test-elevenlabs-key", audioBytes)
	defer upstream.Close()
	configureVoice(t, ts, upstream.URL)

	s := createIntakeSession(t, ts, ts.memberCookie)
	seedSummaries(t, s.ID)

	resp := ts.post(t, "/api/intake/sessions/"+itoa(s.ID)+"/tts", ts.memberCookie,
		map[string]any{"level": "eli10"})
	assertStatus(t, resp, http.StatusOK)
	if ct := resp.Header.Get("Content-Type"); ct != "audio/mpeg" {
		t.Fatalf("content-type = %q", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("cache-control = %q", cc)
	}
	got, _ := io.ReadAll(resp.Body)
	if string(got) != string(audioBytes) {
		t.Fatalf("audio bytes mismatch (%d vs %d)", len(got), len(audioBytes))
	}

	// Paper trail: metadata only, correct sub_action.
	var outcome, sub string
	if err := db.DB.QueryRow(
		`SELECT outcome, sub_action FROM ai_calls WHERE action_key='intake_tts'`).Scan(&outcome, &sub); err != nil {
		t.Fatalf("ai_calls intake_tts: %v", err)
	}
	if outcome != "ok" || sub != "eli10" {
		t.Fatalf("ai_calls = (%s, %s)", outcome, sub)
	}
	var leak int
	if err := db.DB.QueryRow(
		`SELECT COUNT(*) FROM ai_calls WHERE action_key='intake_tts' AND error_class LIKE '%counter%'`).Scan(&leak); err != nil {
		t.Fatal(err)
	}
	if leak != 0 {
		t.Fatal("summary text leaked into ai_calls")
	}
}

// TestIntakeTTS_Guards: no summaries yet 422, bad level 400, unconfigured
// 503, non-owner 404.
func TestIntakeTTS_Guards(t *testing.T) {
	ts := newTestServer(t)
	s := createIntakeSession(t, ts, ts.memberCookie)

	// Unconfigured → 503.
	resp := ts.post(t, "/api/intake/sessions/"+itoa(s.ID)+"/tts", ts.memberCookie,
		map[string]any{"level": "eli5"})
	assertStatus(t, resp, http.StatusServiceUnavailable)

	upstream := fakeTTS(t, "test-elevenlabs-key", fakeMPEGAudio())
	defer upstream.Close()
	configureVoice(t, ts, upstream.URL)

	// No summaries artifact yet → 422.
	resp = ts.post(t, "/api/intake/sessions/"+itoa(s.ID)+"/tts", ts.memberCookie,
		map[string]any{"level": "eli5"})
	assertStatus(t, resp, http.StatusUnprocessableEntity)

	// Bad level → 400.
	resp = ts.post(t, "/api/intake/sessions/"+itoa(s.ID)+"/tts", ts.memberCookie,
		map[string]any{"level": "eli42"})
	assertStatus(t, resp, http.StatusBadRequest)

	// Non-owner → 404 (INV-INTAKE-01).
	resp = ts.post(t, "/api/users", ts.adminCookie, map[string]string{
		"username": "intake-tts-other", "password": "otherpass123", "role": "member",
	})
	assertStatus(t, resp, http.StatusCreated)
	if _, err := db.DB.Exec(`UPDATE users SET must_change_password=0 WHERE username='intake-tts-other'`); err != nil {
		t.Fatal(err)
	}
	otherCookie := ts.login(t, "intake-tts-other", "otherpass123")
	resp = ts.post(t, "/api/intake/sessions/"+itoa(s.ID)+"/tts", otherCookie,
		map[string]any{"level": "eli5"})
	assertStatus(t, resp, http.StatusNotFound)
}
