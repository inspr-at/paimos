// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/inspr-at/paimos/backend/db"
)

func TestVoiceSettings_BaseURLValidation(t *testing.T) {
	t.Run("accepts only canonical ElevenLabs roots", func(t *testing.T) {
		for _, tc := range []struct {
			name       string
			input      string
			wantStored string
			wantLoaded string
		}{
			{name: "blank uses default", input: "", wantStored: "", wantLoaded: "https://api.elevenlabs.io"},
			{name: "standard", input: " https://api.elevenlabs.io/ ", wantStored: "https://api.elevenlabs.io", wantLoaded: "https://api.elevenlabs.io"},
			{name: "EU residency", input: "https://api.eu.residency.elevenlabs.io/", wantStored: "https://api.eu.residency.elevenlabs.io", wantLoaded: "https://api.eu.residency.elevenlabs.io"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				ts := newTestServer(t)
				resp := ts.put(t, "/api/ai/voice-settings", ts.adminCookie, map[string]any{
					"provider": "elevenlabs", "api_key": "test-key", "base_url": tc.input,
					"stt_model": "scribe_v1",
				})
				assertStatus(t, resp, http.StatusOK)
				var body struct {
					BaseURL string `json:"base_url"`
				}
				decode(t, resp, &body)
				if body.BaseURL != tc.wantLoaded {
					t.Fatalf("loaded base_url = %q, want %q", body.BaseURL, tc.wantLoaded)
				}
				var stored string
				if err := db.DB.QueryRow(`SELECT voice_base_url FROM ai_settings WHERE id=1`).Scan(&stored); err != nil {
					t.Fatal(err)
				}
				if stored != tc.wantStored {
					t.Fatalf("stored base_url = %q, want %q", stored, tc.wantStored)
				}
			})
		}
	})

	t.Run("rejects unsafe endpoints without changing settings", func(t *testing.T) {
		ts := newTestServer(t)
		resp := ts.put(t, "/api/ai/voice-settings", ts.adminCookie, map[string]any{
			"provider": "elevenlabs", "api_key": "test-key",
			"base_url": "https://api.elevenlabs.io", "stt_model": "scribe_v1",
		})
		assertStatus(t, resp, http.StatusOK)

		for _, candidate := range []string{
			"http://api.elevenlabs.io",
			"https://attacker.example",
			"https://user:pass@api.elevenlabs.io",
			"https://api.elevenlabs.io#fragment",
			"https://api.elevenlabs.io:8443",
			"https://api.elevenlabs.io?redirect=attacker.example",
			"https://api.elevenlabs.io/proxy",
			"://not-a-url",
		} {
			t.Run(candidate, func(t *testing.T) {
				resp := ts.put(t, "/api/ai/voice-settings", ts.adminCookie, map[string]any{
					"provider": "elevenlabs", "base_url": candidate, "stt_model": "scribe_v1",
				})
				assertStatus(t, resp, http.StatusBadRequest)
				var stored string
				if err := db.DB.QueryRow(`SELECT voice_base_url FROM ai_settings WHERE id=1`).Scan(&stored); err != nil {
					t.Fatal(err)
				}
				if stored != "https://api.elevenlabs.io" {
					t.Fatalf("rejected write changed base_url to %q", stored)
				}
			})
		}
	})
}

func TestVoiceSettings_InvalidStoredURLFailsClosed(t *testing.T) {
	ts := newTestServer(t)
	resp := ts.put(t, "/api/ai/voice-settings", ts.adminCookie, map[string]any{
		"provider": "elevenlabs", "api_key": "test-key",
		"base_url": "https://api.elevenlabs.io", "stt_model": "scribe_v1",
	})
	assertStatus(t, resp, http.StatusOK)

	var calls atomic.Int32
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer attacker.Close()
	if _, err := db.DB.Exec(`UPDATE ai_settings SET voice_base_url=? WHERE id=1`, attacker.URL); err != nil {
		t.Fatal(err)
	}

	s := createIntakeSession(t, ts, ts.memberCookie)
	resp = postAudio(t, ts, s.ID, "audio/webm", []byte("must-not-leave-process"))
	assertStatus(t, resp, http.StatusInternalServerError)
	if got := calls.Load(); got != 0 {
		t.Fatalf("invalid stored voice URL received %d request(s), want 0", got)
	}
}
