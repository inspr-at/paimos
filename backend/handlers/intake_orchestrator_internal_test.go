// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package handlers

import (
	"context"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/ai"
	"github.com/inspr-at/paimos/backend/db"

	_ "modernc.org/sqlite"
)

// fakeIntakeProvider returns a fixed intake_spec JSON body and counts calls.
type fakeIntakeProvider struct {
	calls *atomic.Int64
	text  string
}

func (p fakeIntakeProvider) Name() string { return "fake-intake-test" }

func (p fakeIntakeProvider) Optimize(context.Context, ai.OptimizeRequest) (ai.OptimizeResponse, error) {
	p.calls.Add(1)
	return ai.OptimizeResponse{
		Text: p.text, Model: "test/intake-model",
		PromptTokens: 100, CompletionTokens: 50, FinishReason: "stop",
	}, nil
}

const fakeIntakeJSON = `{"markdown":"# Generated Spec\n\n## Summary\nFrom the fake provider.","title":"Generated spec","issue_type":"ticket","description":"desc","acceptance_criteria":"- [ ] works"}`

// openIntakeTestDB points db.DB at a fully-migrated throwaway DB and seeds
// one member user + one active session with transcript material.
func openIntakeTestDB(t *testing.T) (sessionID int64) {
	t.Helper()
	os.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("PAIMOS_TEST_MODE", "1")
	if err := db.Open(); err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() {
		if db.DB != nil {
			db.DB.Close()
			db.DB = nil
		}
	})
	if _, err := db.DB.Exec(
		`INSERT INTO users(username,password,role,status) VALUES('io-member','x','member','active')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	res, err := db.DB.Exec(
		`INSERT INTO intake_sessions (user_id, transcript, transcript_bytes)
		 SELECT id, 'We need a welcome email flow.', 29 FROM users WHERE username='io-member'`)
	if err != nil {
		t.Fatalf("seed session: %v", err)
	}
	sessionID, _ = res.LastInsertId()
	return sessionID
}

func swapIntakeRuntime(t *testing.T, settings AISettings, provider ai.Provider, reason string) {
	t.Helper()
	old := intakeAIRuntime
	intakeAIRuntime = func() (AISettings, ai.Provider, string) { return settings, provider, reason }
	t.Cleanup(func() { intakeAIRuntime = old })
}

// TestIntakeOrchestrator_PipelineAppendsSpecAndMetersUsage covers the
// INV-INTAKE-04 obligations: a successful run appends spec+ticket_preview
// events, writes exactly one ai_calls row (metadata only, no bodies), and
// meters both the daily and the per-session budget.
func TestIntakeOrchestrator_PipelineAppendsSpecAndMetersUsage(t *testing.T) {
	sessionID := openIntakeTestDB(t)
	var calls atomic.Int64
	swapIntakeRuntime(t, AISettings{Enabled: true, Provider: "fake", Model: "test/intake-model"},
		fakeIntakeProvider{calls: &calls, text: fakeIntakeJSON}, "")

	runIntakePipeline(sessionID)

	// Two provider calls: intake_spec, then intake_summaries (odd spec
	// cycles run the understanding check; the fake body fails its JSON
	// parse, which is itself a metered + audited attempt).
	if got := calls.Load(); got != 2 {
		t.Fatalf("provider calls = %d, want 2 (spec + summaries)", got)
	}
	var specPayload, previewPayload string
	if err := db.DB.QueryRow(
		`SELECT payload_json FROM intake_events WHERE session_id=? AND kind='spec' ORDER BY seq DESC LIMIT 1`,
		sessionID).Scan(&specPayload); err != nil {
		t.Fatalf("spec event: %v", err)
	}
	if !strings.Contains(specPayload, "Generated Spec") {
		t.Fatalf("spec payload = %s", specPayload)
	}
	if err := db.DB.QueryRow(
		`SELECT payload_json FROM intake_events WHERE session_id=? AND kind='ticket_preview' ORDER BY seq DESC LIMIT 1`,
		sessionID).Scan(&previewPayload); err != nil {
		t.Fatalf("ticket_preview event: %v", err)
	}
	if !strings.Contains(previewPayload, `"issue_type":"ticket"`) {
		t.Fatalf("preview payload = %s", previewPayload)
	}

	// Paper trail: one row per attempt, metadata only — the transcript and
	// the spec body must not appear anywhere in it (INV-INTAKE-02).
	var count int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM ai_calls`).Scan(&count); err != nil {
		t.Fatalf("ai_calls: %v", err)
	}
	if count != 2 {
		t.Fatalf("ai_calls rows = %d, want 2 (spec + summaries)", count)
	}
	var specOutcome string
	if err := db.DB.QueryRow(
		`SELECT outcome FROM ai_calls WHERE action_key='intake_spec'`).Scan(&specOutcome); err != nil || specOutcome != "ok" {
		t.Fatalf("intake_spec outcome = %q (%v), want ok", specOutcome, err)
	}
	rows, err := db.DB.Query(`SELECT * FROM ai_calls`)
	if err != nil {
		t.Fatal(err)
	}
	cols, _ := rows.Columns()
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		for i, v := range vals {
			s, ok := v.(string)
			if !ok {
				continue
			}
			if strings.Contains(s, "welcome email") || strings.Contains(s, "Generated Spec") {
				t.Fatalf("ai_calls column %s leaked session body text: %q", cols[i], s)
			}
		}
	}
	rows.Close()

	// Budgets: session meters and daily usage both advanced.
	var ptok, ctok int
	if err := db.DB.QueryRow(
		`SELECT session_prompt_tokens, session_completion_tokens FROM intake_sessions WHERE id=?`,
		sessionID).Scan(&ptok, &ctok); err != nil {
		t.Fatal(err)
	}
	if ptok != 200 || ctok != 100 {
		t.Fatalf("session meters = (%d,%d), want (200,100) for two calls", ptok, ctok)
	}
	var usage int
	if err := db.DB.QueryRow(`SELECT COALESCE(SUM(prompt_tokens+completion_tokens),0) FROM ai_usage`).Scan(&usage); err != nil {
		t.Fatal(err)
	}
	if usage != 300 {
		t.Fatalf("ai_usage total = %d, want 300 for two calls", usage)
	}
}

// TestIntakeOrchestrator_SessionBudgetStopsGeneration: once the per-session
// token budget is exhausted the pipeline stops calling the provider but
// capture stays possible (no error, just a degraded stage).
func TestIntakeOrchestrator_SessionBudgetStopsGeneration(t *testing.T) {
	sessionID := openIntakeTestDB(t)
	if _, err := db.DB.Exec(
		`UPDATE intake_sessions SET session_prompt_tokens=?, session_completion_tokens=0 WHERE id=?`,
		intakeSessionTokenBudgetDefault, sessionID); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	swapIntakeRuntime(t, AISettings{Enabled: true, Provider: "fake", Model: "m"},
		fakeIntakeProvider{calls: &calls, text: fakeIntakeJSON}, "")

	ch := globalIntakeBroker.Subscribe(sessionID)
	defer globalIntakeBroker.Unsubscribe(sessionID, ch)

	runIntakePipeline(sessionID)

	if got := calls.Load(); got != 0 {
		t.Fatalf("provider calls = %d, want 0 (budget exhausted)", got)
	}
	select {
	case ev := <-ch:
		if ev.Kind != "stage" || !strings.Contains(string(ev.Payload), "session_budget") {
			t.Fatalf("stage event = %+v, want degraded session_budget", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no stage event published")
	}
}

// TestIntakeOrchestrator_UnconfiguredDegrades: no provider → degraded
// stage event, zero provider calls, no events appended.
func TestIntakeOrchestrator_UnconfiguredDegrades(t *testing.T) {
	sessionID := openIntakeTestDB(t)
	swapIntakeRuntime(t, AISettings{}, nil, "unconfigured")

	ch := globalIntakeBroker.Subscribe(sessionID)
	defer globalIntakeBroker.Unsubscribe(sessionID, ch)

	runIntakePipeline(sessionID)

	select {
	case ev := <-ch:
		if ev.Kind != "stage" || !strings.Contains(string(ev.Payload), "unconfigured") {
			t.Fatalf("stage event = %+v, want degraded unconfigured", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no stage event published")
	}
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM intake_events WHERE session_id=?`, sessionID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("events appended in degraded mode: %d", n)
	}
}

// TestIntakeOrchestrator_DebounceCoalesces: N rapid pokes produce exactly
// one pipeline run. Uses the real worker + debounce clock (~3s).
func TestIntakeOrchestrator_DebounceCoalesces(t *testing.T) {
	if testing.Short() {
		t.Skip("debounce test needs the real ~2.5s quiet window")
	}
	sessionID := openIntakeTestDB(t)
	var calls atomic.Int64
	swapIntakeRuntime(t, AISettings{Enabled: true, Provider: "fake", Model: "m"},
		fakeIntakeProvider{calls: &calls, text: fakeIntakeJSON}, "")
	intakeWorkerAllowedInTests = true
	t.Cleanup(func() { intakeWorkerAllowedInTests = false })

	for range 5 {
		notifyIntakeOrchestrator(sessionID)
		time.Sleep(50 * time.Millisecond)
	}
	deadline := time.Now().Add(intakeDebounceQuiet + 6*time.Second)
	for time.Now().Before(deadline) {
		if calls.Load() > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	// Give a would-be duplicate run time to fire before asserting.
	time.Sleep(500 * time.Millisecond)
	// One coalesced pipeline run = spec + summaries provider calls.
	if got := calls.Load(); got != 2 {
		t.Fatalf("provider calls = %d, want exactly 2 (one coalesced run)", got)
	}
}
