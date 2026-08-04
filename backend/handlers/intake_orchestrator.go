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

// PAI-705 (epic PAI-703). The intake orchestrator: a lazy, per-session
// worker goroutine that turns transcript chunks into spec regenerations.
//
// Contract with the dispatcher (INV-INTAKE-04): every provider call made
// here replicates the POST /api/ai/action obligations — usage cap, prompt
// resolution, one audit line per attempt (metadata only), one ai_calls
// row, and usage metering. Session bodies never appear in any of those
// (INV-INTAKE-02).
//
// Degraded mode is a first-class state, not an error: capture, manual
// edits, checkpoints, restore and (later) create-issue keep working with
// AI unconfigured, the provider erroring, or budgets exhausted. The
// worker then only emits ephemeral "stage" events explaining why the
// spec is frozen.

package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"os"
	"sync"
	"time"

	"github.com/inspr-at/paimos/backend/ai"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
)

const (
	intakeSessionTokenBudget = 20_000
	intakeDebounceQuiet      = 2500 * time.Millisecond
	intakeDebounceMax        = 10 * time.Second
	intakeWorkerIdleExit     = 60 * time.Second
	intakeTranscriptTailMax  = 12 * 1024
)

type intakeWorker struct {
	sessionID int64
	poke      chan struct{}
}

type intakeOrchestrator struct {
	mu      sync.Mutex
	workers map[int64]*intakeWorker
}

var globalIntakeOrchestrator = &intakeOrchestrator{workers: map[int64]*intakeWorker{}}

// intakeOrchestratorDisabled short-circuits worker spawning in tests that
// exercise the session API without wanting background AI activity.
var intakeOrchestratorDisabled = false

// intakeWorkerAllowedInTests opts a test back in: under PAIMOS_TEST_MODE
// the workers are off by default because their debounce timers outlive the
// per-test DB teardown (a worker firing against a swapped-out db.DB is at
// best noise, at worst a nil deref). The orchestrator's own internal tests
// flip this on and call the pipeline deterministically.
var intakeWorkerAllowedInTests = false

// notifyIntakeOrchestrator pokes (or lazily spawns) the session worker.
// Call after any committed change that should trigger a regeneration:
// transcript chunks, language toggles, manual refresh, restore.
func notifyIntakeOrchestrator(sessionID int64) {
	if intakeOrchestratorDisabled {
		return
	}
	if os.Getenv("PAIMOS_TEST_MODE") == "1" && !intakeWorkerAllowedInTests {
		return
	}
	o := globalIntakeOrchestrator
	o.mu.Lock()
	w, ok := o.workers[sessionID]
	if !ok {
		w = &intakeWorker{sessionID: sessionID, poke: make(chan struct{}, 1)}
		o.workers[sessionID] = w
		go w.run()
	}
	o.mu.Unlock()
	select {
	case w.poke <- struct{}{}:
	default: // a pending poke already guarantees a follow-up run
	}
}

func (o *intakeOrchestrator) release(sessionID int64) {
	o.mu.Lock()
	delete(o.workers, sessionID)
	o.mu.Unlock()
}

// run is the worker loop: debounce pokes (quiet ≥2.5s OR 10s since the
// first pending poke), run the pipeline serialized, exit after 60s idle.
func (w *intakeWorker) run() {
	defer globalIntakeOrchestrator.release(w.sessionID)
	idle := time.NewTimer(intakeWorkerIdleExit)
	defer idle.Stop()
	for {
		select {
		case <-idle.C:
			return
		case <-w.poke:
			// Debounce window: extend on further pokes until quiet or max.
			first := time.Now()
			quiet := time.NewTimer(intakeDebounceQuiet)
		debounce:
			for {
				select {
				case <-w.poke:
					if time.Since(first) >= intakeDebounceMax {
						break debounce
					}
					if !quiet.Stop() {
						<-quiet.C
					}
					quiet.Reset(intakeDebounceQuiet)
				case <-quiet.C:
					break debounce
				}
			}
			quiet.Stop()
			runIntakePipeline(w.sessionID)
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(intakeWorkerIdleExit)
		}
	}
}

// intakeAIRuntime resolves the AI settings + provider for a pipeline run.
// A nil provider means degraded mode; the string is the reason. Swappable
// var so orchestrator tests can inject a fake provider without touching
// the settings row or the provider registry.
var intakeAIRuntime = func() (AISettings, ai.Provider, string) {
	settings, err := LoadAISettings()
	if err != nil || !settings.AvailableForOptimize() {
		return settings, nil, "unconfigured"
	}
	provider, perr := ai.Get(settings.Provider)
	if perr != nil {
		return settings, nil, "provider_missing"
	}
	return settings, provider, ""
}

func publishIntakeStage(sessionID int64, stage, state, reason string) {
	payload, _ := json.Marshal(map[string]string{"stage": stage, "state": state, "reason": reason})
	globalIntakeBroker.Publish(sessionID, intakeStreamEvent{Kind: "stage", Payload: payload})
}

// runIntakePipeline executes one regeneration cycle for the session.
func runIntakePipeline(sessionID int64) {
	if db.DB == nil {
		return // test teardown swapped the DB out from under a live worker
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	s, err := loadIntakeSession(ctx, sessionID)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			log.Printf("intake orchestrator: load session %d: %v", sessionID, err)
		}
		return
	}
	if s.Status != "active" {
		return
	}

	userID, isAdmin, err := intakeSessionOwnerRole(ctx, s.UserID)
	if err != nil {
		log.Printf("intake orchestrator: owner lookup session %d: %v", sessionID, err)
		return
	}
	transcript, err := loadIntakeTranscript(ctx, sessionID)
	if err != nil || transcript == "" {
		return
	}

	settings, provider, reason := intakeAIRuntime()
	capOK := true
	if ok, _, over, bypass := CheckUsageCap(userID, isAdmin); !ok || (over && !bypass) {
		capOK = false
	}
	budgetOK := s.sessionTokensUsed() < intakeSessionTokenBudget

	// Project detection (PAI-706) runs every cycle: deterministic Stage A
	// even in degraded mode (its scores are capped below the auto-switch
	// threshold), LLM Stage B only when healthy.
	var matchAx *aiActionContext
	if provider != nil {
		options, uerr := resolveAIActionOptions(settings, "intake_project_match", aiActionOptions{}, nil)
		if uerr == nil {
			matchAx = &aiActionContext{
				Ctx: ctx, UserID: userID, IsAdmin: isAdmin,
				Provider: provider, Settings: settings, Options: options, DB: db.DB,
			}
		}
	}
	mp, mc := runIntakeMatchStage(ctx, s, transcript, matchAx, provider != nil && capOK && budgetOK)
	if mp > 0 || mc > 0 {
		if _, err := db.DB.ExecContext(ctx,
			`UPDATE intake_sessions
			 SET session_prompt_tokens = session_prompt_tokens + ?,
			     session_completion_tokens = session_completion_tokens + ?
			 WHERE id = ?`, mp, mc, sessionID); err != nil {
			log.Printf("intake orchestrator: match budget update session %d: %v", sessionID, err)
		}
		s.SessionPromptTokens += mp
		s.SessionCompletionTokens += mc
		budgetOK = s.sessionTokensUsed() < intakeSessionTokenBudget
	}

	// Query for retrieval-based stages: the transcript tail's material.
	impactQuery := transcript
	if len(impactQuery) > 600 {
		impactQuery = impactQuery[len(impactQuery)-600:]
	}
	var candidateIssues []intakeSpecCandidateIssue
	if target := s.activeProjectID(); target != nil {
		if hits, err := intakeCandidateIssues(*target, impactQuery); err == nil {
			for _, h := range hits {
				key, _ := h["issue_key"].(string)
				title, _ := h["title"].(string)
				if key != "" && len(candidateIssues) < 8 {
					candidateIssues = append(candidateIssues, intakeSpecCandidateIssue{IssueKey: key, Title: title})
				}
			}
		}
	}

	if provider == nil || !capOK || !budgetOK {
		// Degraded: impacts still compute with heuristic categories
		// (PAI-708) before the spec stage bails out.
		runIntakeImpactsStage(ctx, s, impactQuery, nil)
		switch {
		case provider == nil:
			publishIntakeStage(sessionID, "spec", "degraded", reason)
		case !capOK:
			publishIntakeStage(sessionID, "spec", "degraded", "daily_cap")
		default:
			publishIntakeStage(sessionID, "spec", "degraded", "session_budget")
		}
		return
	}
	priorSpec := ""
	var prior struct {
		Markdown string `json:"markdown"`
	}
	var priorRaw string
	err = db.DB.QueryRowContext(ctx,
		`SELECT payload_json FROM intake_events
		 WHERE session_id = ? AND kind = 'spec' ORDER BY seq DESC LIMIT 1`, sessionID).Scan(&priorRaw)
	if err == nil && priorRaw != "" {
		if json.Unmarshal([]byte(priorRaw), &prior) == nil {
			priorSpec = prior.Markdown
		}
	}

	tail := transcript
	if len(tail) > intakeTranscriptTailMax {
		tail = tail[len(tail)-intakeTranscriptTailMax:]
	}

	options, uerr := resolveAIActionOptions(settings, "intake_spec", aiActionOptions{}, nil)
	if uerr != nil {
		publishIntakeStage(sessionID, "spec", "degraded", "options")
		return
	}
	params, _ := json.Marshal(intakeSpecParams{
		PriorSpec: priorSpec, Language: s.Language, CandidateIssues: candidateIssues,
	})

	requestID := newAIRequestID()
	ax := &aiActionContext{
		Ctx:      ctx,
		UserID:   userID,
		IsAdmin:  isAdmin,
		Provider: provider,
		Settings: settings,
		Text:     tail,
		Params:   params,
		Options:  options,
		DB:       db.DB,
	}

	publishIntakeStage(sessionID, "spec", "running", "")
	started := time.Now()
	desc, ok := actionRegistry["intake_spec"]
	if !ok || desc.Handler == nil {
		publishIntakeStage(sessionID, "spec", "error", "unregistered")
		return
	}
	body, model, ptok, ctok, finish, err := desc.Handler(ax)
	latency := time.Since(started)

	outcome := "ok"
	errorClass := ""
	if err != nil {
		outcome = "fail_upstream"
		errorClass = "upstream"
		if errors.Is(err, context.DeadlineExceeded) {
			outcome = "fail_timeout"
			errorClass = "timeout"
		} else if errors.Is(err, errAIActionJSONParse) {
			errorClass = "response_parse"
		}
	}
	// Dispatcher obligations (INV-INTAKE-04): audit line + paper trail +
	// meter, exactly once per attempt, metadata only.
	auditAction(requestID, userID, "intake_spec", "", "", 0, model, outcome, latency, ptok, ctok, options)
	recordAICall(ctx, aiCallArgs{
		RequestID:        requestID,
		UserID:           &userID,
		ActionKey:        "intake_spec",
		Surface:          "intake",
		ProjectID:        s.activeProjectID(),
		Provider:         settings.Provider,
		Model:            model,
		ProfileID:        options.ProfileID,
		Effort:           options.Effort,
		PromptPresetRef:  options.PromptPresetRef,
		ContextPack:      options.ContextPack,
		PromptTokens:     ptok,
		CompletionTokens: ctok,
		Outcome:          outcome,
		ErrorClass:       errorClass,
		LatencyMs:        latency.Milliseconds(),
	})
	if ptok > 0 || ctok > 0 {
		RecordUsage(userID, ptok, ctok)
		if _, err := db.DB.ExecContext(ctx,
			`UPDATE intake_sessions
			 SET session_prompt_tokens = session_prompt_tokens + ?,
			     session_completion_tokens = session_completion_tokens + ?
			 WHERE id = ?`, ptok, ctok, sessionID); err != nil {
			log.Printf("intake orchestrator: budget update session %d: %v", sessionID, err)
		}
	}
	if err != nil {
		publishIntakeStage(sessionID, "spec", "error", errorClass)
		return
	}

	spec, ok := body.(intakeSpecBody)
	if !ok || spec.Markdown == "" {
		publishIntakeStage(sessionID, "spec", "error", "response_parse")
		return
	}
	if spec.Markdown == priorSpec {
		publishIntakeStage(sessionID, "spec", "ok", "unchanged")
		return
	}

	if err := appendIntakeGeneration(ctx, sessionID, s.Language, spec, finish); err != nil {
		log.Printf("intake orchestrator: append session %d: %v", sessionID, err)
		publishIntakeStage(sessionID, "spec", "error", "store")
		return
	}
	publishIntakeStage(sessionID, "spec", "ok", "")

	// Impact analysis (PAI-708) with the LLM's relation categories.
	specRelations := map[string]string{}
	for _, rel := range spec.Relations {
		specRelations[rel.IssueKey] = rel.Category
	}
	runIntakeImpactsStage(ctx, s, impactQuery, specRelations)
}

// appendIntakeGeneration stores the regenerated spec + ticket preview as
// two events in one tx and fans them out.
func appendIntakeGeneration(ctx context.Context, sessionID int64, language string, spec intakeSpecBody, finishReason string) error {
	count, err := intakeEventCount(ctx, db.DB, sessionID)
	if err != nil {
		return err
	}
	if count+2 > intakeMaxEventsPerSess {
		return errors.New("event limit reached")
	}
	specPayload, _ := json.Marshal(map[string]any{
		"markdown": spec.Markdown,
		"language": language,
		"finish":   finishReason,
	})
	previewPayload, _ := json.Marshal(map[string]any{
		"title":               spec.Title,
		"issue_type":          spec.IssueType,
		"description":         spec.Description,
		"acceptance_criteria": spec.AcceptanceCriteria,
		"language":            language,
	})
	tx, err := db.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	firstSeq, err := appendIntakeEventTx(ctx, tx, sessionID, "spec", "ai", "", string(specPayload))
	if err != nil {
		return err
	}
	if _, err := appendIntakeEventTx(ctx, tx, sessionID, "ticket_preview", "ai", "", string(previewPayload)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	publishIntakeEventsFrom(ctx, sessionID, firstSeq)
	return nil
}

func (s *intakeSession) sessionTokensUsed() int {
	return s.SessionPromptTokens + s.SessionCompletionTokens
}

// activeProjectID is the pinned project when set, else the detected one.
func (s *intakeSession) activeProjectID() *int64 {
	if s.PinnedProjectID != nil {
		return s.PinnedProjectID
	}
	return s.DetectedProjectID
}

func intakeSessionOwnerRole(ctx context.Context, userID int64) (int64, bool, error) {
	var role string
	if err := db.DB.QueryRowContext(ctx,
		`SELECT role FROM users WHERE id = ?`, userID).Scan(&role); err != nil {
		return 0, false, err
	}
	return userID, auth.IsAdminRole(role), nil
}

func loadIntakeTranscript(ctx context.Context, sessionID int64) (string, error) {
	var t string
	err := db.DB.QueryRowContext(ctx,
		`SELECT transcript FROM intake_sessions WHERE id = ?`, sessionID).Scan(&t)
	return t, err
}
