// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package handlers

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/inspr-at/paimos/backend/db"
)

// PAI-734: language toggles are view switches. These tests drive the
// pipeline deterministically (same harness as the orchestrator tests)
// and assert the freshness gate + per-language state selection.

// seedIntakeEvent inserts one event and bumps the session rev, mirroring
// appendIntakeEventTx without a handler in the loop.
func seedIntakeEvent(t *testing.T, sessionID int64, seq int64, kind, source, payload string) {
	t.Helper()
	if _, err := db.DB.Exec(
		`INSERT INTO intake_events (session_id, seq, kind, source, payload_json) VALUES (?, ?, ?, ?, ?)`,
		sessionID, seq, kind, source, payload); err != nil {
		t.Fatalf("seed %s event: %v", kind, err)
	}
	if _, err := db.DB.Exec(
		`UPDATE intake_sessions SET rev = MAX(rev, ?) WHERE id = ?`, seq, sessionID); err != nil {
		t.Fatalf("bump rev: %v", err)
	}
}

// TestIntakePipeline_SkipsWhenActiveLanguageSpecFresh: a poke with a
// fresh cached spec in the active language (the language-toggle case)
// must not touch the provider at all.
func TestIntakePipeline_SkipsWhenActiveLanguageSpecFresh(t *testing.T) {
	sessionID := openIntakeTestDB(t)
	var calls atomic.Int64
	swapIntakeRuntime(t, AISettings{Enabled: true, Provider: "fake", Model: "test/intake-model"},
		fakeIntakeProvider{calls: &calls, text: fakeIntakeJSON}, "")

	seedIntakeEvent(t, sessionID, 1, "transcript_chunk", "user", `{"text":"We need a welcome email flow."}`)
	seedIntakeEvent(t, sessionID, 2, "spec", "ai", `{"markdown":"# EN Spec","language":"en"}`)

	runIntakePipeline(sessionID)
	if got := calls.Load(); got != 0 {
		t.Fatalf("provider calls = %d, want 0 (fresh en spec must skip the pipeline)", got)
	}

	// Toggling to a language with no spec regenerates — in that language.
	if _, err := db.DB.Exec(`UPDATE intake_sessions SET language='de' WHERE id=?`, sessionID); err != nil {
		t.Fatal(err)
	}
	seedIntakeEvent(t, sessionID, 3, "language", "user", `{"language":"de","from":"en"}`)
	runIntakePipeline(sessionID)
	if got := calls.Load(); got == 0 {
		t.Fatal("stale de spec did not regenerate")
	}
	var lang string
	if err := db.DB.QueryRow(
		`SELECT json_extract(payload_json,'$.language') FROM intake_events
		  WHERE session_id=? AND kind='spec' ORDER BY seq DESC LIMIT 1`, sessionID).Scan(&lang); err != nil {
		t.Fatal(err)
	}
	if lang != "de" {
		t.Fatalf("regenerated spec language = %q, want de", lang)
	}

	// Toggling back to en: its spec is still fresh — zero further calls.
	before := calls.Load()
	if _, err := db.DB.Exec(`UPDATE intake_sessions SET language='en' WHERE id=?`, sessionID); err != nil {
		t.Fatal(err)
	}
	seedIntakeEvent(t, sessionID, 6, "language", "user", `{"language":"en","from":"de"}`)
	runIntakePipeline(sessionID)
	if got := calls.Load(); got != before {
		t.Fatalf("provider calls went %d → %d on toggle-back, want unchanged", before, got)
	}
}

// TestIntakePipeline_NewInputRegeneratesActiveLanguageOnly: a new chunk
// makes the active language stale and regenerates it; the inactive
// language's cache is left untouched.
func TestIntakePipeline_NewInputRegeneratesActiveLanguageOnly(t *testing.T) {
	sessionID := openIntakeTestDB(t)
	var calls atomic.Int64
	swapIntakeRuntime(t, AISettings{Enabled: true, Provider: "fake", Model: "test/intake-model"},
		fakeIntakeProvider{calls: &calls, text: fakeIntakeJSON}, "")

	seedIntakeEvent(t, sessionID, 1, "transcript_chunk", "user", `{"text":"First thought."}`)
	seedIntakeEvent(t, sessionID, 2, "spec", "ai", `{"markdown":"# EN Spec","language":"en"}`)
	seedIntakeEvent(t, sessionID, 3, "spec", "ai", `{"markdown":"# DE Spec","language":"de"}`)
	seedIntakeEvent(t, sessionID, 4, "transcript_chunk", "user", `{"text":"Another thought."}`)

	runIntakePipeline(sessionID) // active language: en
	if calls.Load() == 0 {
		t.Fatal("stale en spec did not regenerate after new input")
	}
	var deCount int
	if err := db.DB.QueryRow(
		`SELECT COUNT(*) FROM intake_events WHERE session_id=? AND kind='spec'
		  AND json_extract(payload_json,'$.language')='de'`, sessionID).Scan(&deCount); err != nil {
		t.Fatal(err)
	}
	if deCount != 1 {
		t.Fatalf("de spec events = %d, want 1 (inactive language untouched)", deCount)
	}
}

// TestIntakePipeline_ForceRegenBypassesFreshness: manual refresh must
// regenerate even when the active language's spec is fresh.
func TestIntakePipeline_ForceRegenBypassesFreshness(t *testing.T) {
	sessionID := openIntakeTestDB(t)
	var calls atomic.Int64
	swapIntakeRuntime(t, AISettings{Enabled: true, Provider: "fake", Model: "test/intake-model"},
		fakeIntakeProvider{calls: &calls, text: fakeIntakeJSON}, "")

	seedIntakeEvent(t, sessionID, 1, "transcript_chunk", "user", `{"text":"We need a welcome email flow."}`)
	seedIntakeEvent(t, sessionID, 2, "spec", "ai", `{"markdown":"# EN Spec","language":"en"}`)

	markIntakeForceRegen(sessionID)
	runIntakePipeline(sessionID)
	if calls.Load() == 0 {
		t.Fatal("forced refresh did not regenerate a fresh spec")
	}
	// The flag is consumed: the next un-forced poke skips again.
	before := calls.Load()
	runIntakePipeline(sessionID)
	if got := calls.Load(); got != before {
		t.Fatalf("provider calls went %d → %d after consumed force flag, want unchanged", before, got)
	}
}

// TestIntakeStateAt_PerLanguageSelection: state prefers the newest
// artifact in the language active at that seq, falls back to newest-any
// when the language has no generation, and resolves pre-toggle history
// via the toggle's "from" field.
func TestIntakeStateAt_PerLanguageSelection(t *testing.T) {
	sessionID := openIntakeTestDB(t)
	ctx := context.Background()

	seedIntakeEvent(t, sessionID, 1, "transcript_chunk", "user", `{"text":"Idea."}`)
	seedIntakeEvent(t, sessionID, 2, "spec", "ai", `{"markdown":"# EN Spec","language":"en"}`)
	seedIntakeEvent(t, sessionID, 3, "language", "user", `{"language":"de","from":"en"}`)
	seedIntakeEvent(t, sessionID, 4, "spec", "ai", `{"markdown":"# DE Spec","language":"de"}`)
	seedIntakeEvent(t, sessionID, 5, "language", "user", `{"language":"en","from":"de"}`)
	if _, err := db.DB.Exec(`UPDATE intake_sessions SET language='en' WHERE id=?`, sessionID); err != nil {
		t.Fatal(err)
	}

	// Head (seq 5, active en): the older EN spec wins over the newer DE one.
	state, err := intakeStateAt(ctx, sessionID, 5)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(state.Artifacts["spec"]), "EN Spec") {
		t.Fatalf("head spec = %s, want EN", state.Artifacts["spec"])
	}

	// At seq 4 (active de): the DE spec.
	state, err = intakeStateAt(ctx, sessionID, 4)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(state.Artifacts["spec"]), "DE Spec") {
		t.Fatalf("seq-4 spec = %s, want DE", state.Artifacts["spec"])
	}

	// At seq 2 (before any toggle → "from" resolves en): the EN spec.
	state, err = intakeStateAt(ctx, sessionID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(state.Artifacts["spec"]), "EN Spec") {
		t.Fatalf("seq-2 spec = %s, want EN", state.Artifacts["spec"])
	}

	// At seq 3 (active de, no de spec yet): falls back to the EN spec.
	state, err = intakeStateAt(ctx, sessionID, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(state.Artifacts["spec"]), "EN Spec") {
		t.Fatalf("seq-3 spec = %s, want EN fallback", state.Artifacts["spec"])
	}
}
