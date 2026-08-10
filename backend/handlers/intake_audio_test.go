// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/backend/db"
)

// fakeScribe stands in for the ElevenLabs batch STT endpoint. The
// voice_base_url setting points at it, so the whole path — settings load,
// key header, multipart forward, transcript append — runs for real
// without credentials or network.
func fakeScribe(t *testing.T, text string, wantKey string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/speech-to-text" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("xi-api-key") != wantKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if err := r.ParseMultipartForm(16 << 20); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.MultipartForm.Value["model_id"][0] == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"text": text, "language_probability": 0.99})
	}))
}

type voiceRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f voiceRoundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func routeVoiceUpstreamForTest(t *testing.T, baseURL string) {
	t.Helper()
	target, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	previous := http.DefaultTransport
	http.DefaultTransport = voiceRoundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if strings.EqualFold(r.URL.Hostname(), "api.elevenlabs.io") {
			clone := r.Clone(r.Context())
			rewritten := *r.URL
			rewritten.Scheme = target.Scheme
			rewritten.Host = target.Host
			clone.URL = &rewritten
			return previous.RoundTrip(clone)
		}
		return previous.RoundTrip(r)
	})
	t.Cleanup(func() { http.DefaultTransport = previous })
}

func configureVoice(t *testing.T, ts *testServer, baseURL string) {
	t.Helper()
	routeVoiceUpstreamForTest(t, baseURL)
	key := "test-elevenlabs-key"
	resp := ts.put(t, "/api/ai/voice-settings", ts.adminCookie, map[string]any{
		"provider": "elevenlabs", "api_key": key,
		"base_url": "https://api.elevenlabs.io", "stt_model": "scribe_v1",
	})
	assertStatus(t, resp, http.StatusOK)
	var body map[string]any
	decode(t, resp, &body)
	if body["has_api_key"] != true {
		t.Fatalf("voice settings = %v", body)
	}
	if _, ok := body["api_key"]; ok {
		t.Fatal("voice settings response leaked the api key field")
	}
}

func postAudio(t *testing.T, ts *testServer, sessionID int64, contentType string, body []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequest("POST",
		ts.srv.URL+"/api/intake/sessions/"+itoa(sessionID)+"/audio", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Cookie", ts.memberCookie)
	req.Header.Set("Content-Type", contentType)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// TestIntakeAudio_TranscribesAndAppends: an utterance blob becomes a
// normal transcript chunk (spoken:true) via the fake Scribe upstream, and
// the paper trail records metadata only.
func TestIntakeAudio_TranscribesAndAppends(t *testing.T) {
	ts := newTestServer(t)
	upstream := fakeScribe(t, "We should add saved filters to the portal.", "test-elevenlabs-key")
	defer upstream.Close()
	configureVoice(t, ts, upstream.URL)

	s := createIntakeSession(t, ts, ts.memberCookie)
	resp := postAudio(t, ts, s.ID, "audio/webm;codecs=opus", []byte("fake-webm-bytes"))
	assertStatus(t, resp, http.StatusOK)
	var out struct {
		Seq  int64  `json:"seq"`
		Text string `json:"text"`
	}
	decode(t, resp, &out)
	if out.Seq != 1 || !strings.Contains(out.Text, "saved filters") {
		t.Fatalf("audio response = %+v", out)
	}

	// The chunk is a regular transcript event with spoken provenance.
	resp = ts.get(t, "/api/intake/sessions/"+itoa(s.ID), ts.memberCookie)
	assertStatus(t, resp, http.StatusOK)
	var head intakeHeadResp
	decode(t, resp, &head)
	if !strings.Contains(head.State.Transcript, "saved filters") {
		t.Fatalf("transcript = %q", head.State.Transcript)
	}
	var payload string
	if err := db.DB.QueryRow(
		`SELECT payload_json FROM intake_events WHERE session_id=? AND kind='transcript_chunk'`,
		s.ID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload, `"spoken":true`) {
		t.Fatalf("payload = %s, want spoken:true", payload)
	}

	// Paper trail: intake_stt row, metadata only (no audio, no text).
	var actionKey, outcome string
	if err := db.DB.QueryRow(
		`SELECT action_key, outcome FROM ai_calls WHERE action_key='intake_stt'`).Scan(&actionKey, &outcome); err != nil {
		t.Fatalf("ai_calls intake_stt row: %v", err)
	}
	if outcome != "ok" {
		t.Fatalf("outcome = %s", outcome)
	}
	var leak int
	if err := db.DB.QueryRow(
		`SELECT COUNT(*) FROM ai_calls WHERE action_key='intake_stt'
		   AND (sub_action LIKE '%saved filters%' OR error_class LIKE '%saved filters%')`).Scan(&leak); err != nil {
		t.Fatal(err)
	}
	if leak != 0 {
		t.Fatal("transcript text leaked into ai_calls")
	}
}

// TestIntakeAudio_Guards: unconfigured 503, wrong media type 415,
// upstream failure 502 (metered as fail), non-owner 404.
func TestIntakeAudio_Guards(t *testing.T) {
	ts := newTestServer(t)
	s := createIntakeSession(t, ts, ts.memberCookie)

	// No voice provider configured → 503; capture by text still works.
	resp := postAudio(t, ts, s.ID, "audio/webm", []byte("x"))
	assertStatus(t, resp, http.StatusServiceUnavailable)
	postChunk(t, ts, ts.memberCookie, s.ID, "typed fallback still works")

	upstream := fakeScribe(t, "irrelevant", "test-elevenlabs-key")
	defer upstream.Close()
	configureVoice(t, ts, upstream.URL)

	// Wrong media type → 415.
	resp = postAudio(t, ts, s.ID, "application/json", []byte("{}"))
	assertStatus(t, resp, http.StatusUnsupportedMediaType)

	// Upstream 401 (wrong key on the upstream side) → 502 to the client.
	badUpstream := fakeScribe(t, "x", "a-different-key")
	defer badUpstream.Close()
	routeVoiceUpstreamForTest(t, badUpstream.URL)
	resp = postAudio(t, ts, s.ID, "audio/webm", []byte("x"))
	assertStatus(t, resp, http.StatusBadGateway)

	// Non-owner → 404 (INV-INTAKE-01 holds for the audio route too).
	resp2 := ts.post(t, "/api/users", ts.adminCookie, map[string]string{
		"username": "intake-audio-other", "password": "otherpass123", "role": "member",
	})
	assertStatus(t, resp2, http.StatusCreated)
	if _, err := db.DB.Exec(`UPDATE users SET must_change_password=0 WHERE username='intake-audio-other'`); err != nil {
		t.Fatal(err)
	}
	otherCookie := ts.login(t, "intake-audio-other", "otherpass123")
	req, _ := http.NewRequest("POST", ts.srv.URL+"/api/intake/sessions/"+itoa(s.ID)+"/audio", bytes.NewReader([]byte("x")))
	req.Header.Set("Cookie", otherCookie)
	req.Header.Set("Content-Type", "audio/webm")
	r3, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if r3.StatusCode != http.StatusNotFound {
		t.Fatalf("non-owner audio post = %d, want 404", r3.StatusCode)
	}
}
