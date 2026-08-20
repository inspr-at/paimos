// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/delivery"
	"github.com/inspr-at/paimos/backend/sse"
)

const (
	telemetryActivityMaxBytes = 280
	telemetryBasisMaxBytes    = 240
	telemetryClockSkewAfter   = 5 * time.Minute
)

var telemetryIdentityPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]*$`)

var telemetrySecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bauthorization\s*:\s*bearer\s+[A-Za-z0-9._~+/=-]{8,}`),
	regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{12,}`),
	regexp.MustCompile(`(?i)\b(api[_-]?key|token|secret|password|passwd|credential)\s*[:=]\s*['"]?[A-Za-z0-9._~+/=-]{8,}`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}\b`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}\b`),
	regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}\b`),
	regexp.MustCompile(`(?i)https?://[^\s/:@]+:[^\s/@]{8,}@`),
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
}

var telemetryKinds = map[string]bool{
	"heartbeat": true, "progress": true, "phase": true,
	"needs_input": true, "blocker": true, "estimate": true,
}

var telemetryPhases = map[string]bool{
	"unknown": true, "starting": true, "planning": true,
	"implementing": true, "testing": true, "reviewing": true,
	"deploying": true, "waiting": true, "completed": true,
}

var telemetryBlockerStates = map[string]bool{
	"none": true, "input": true, "dependency": true, "permission": true,
	"environment": true, "external": true, "unknown": true,
}

var telemetryEstimateSources = map[string]bool{
	"agent": true, "adapter": true, "provider": true, "tool": true,
}

// AgentRunTelemetry is one immutable, provider-neutral fact report about a
// single run. It intentionally has no arbitrary metadata/provider-payload
// field: every persisted value is part of this allowlisted contract.
type AgentRunTelemetry struct {
	ID                 int64    `json:"id"`
	RunID              int64    `json:"run_id"`
	Sequence           int64    `json:"sequence"`
	CorrelationID      string   `json:"correlation_id"`
	Provider           string   `json:"provider"`
	Adapter            string   `json:"adapter"`
	AgentReportedAt    string   `json:"agent_reported_at"`
	ServerReceivedAt   string   `json:"server_received_at"`
	Kind               string   `json:"kind"`
	Heartbeat          bool     `json:"heartbeat"`
	Phase              string   `json:"phase"`
	Activity           string   `json:"activity,omitempty"`
	NeedsInput         bool     `json:"needs_input"`
	BlockerState       string   `json:"blocker_state"`
	EstimateRevision   *int64   `json:"estimate_revision,omitempty"`
	ProgressPercent    *float64 `json:"progress_percent,omitempty"`
	ETASeconds         *int64   `json:"eta_seconds,omitempty"`
	ETAMinSeconds      *int64   `json:"eta_min_seconds,omitempty"`
	ETAMaxSeconds      *int64   `json:"eta_max_seconds,omitempty"`
	EstimateSource     string   `json:"estimate_source,omitempty"`
	EstimateConfidence *float64 `json:"estimate_confidence,omitempty"`
	EstimateBasis      string   `json:"estimate_basis,omitempty"`
	ClockSkewSeconds   int64    `json:"clock_skew_seconds"`
	ClockSkewed        bool     `json:"clock_skewed"`
}

type agentRunTelemetryInput struct {
	Sequence           int64    `json:"sequence"`
	CorrelationID      string   `json:"correlation_id"`
	Provider           string   `json:"provider"`
	Adapter            string   `json:"adapter"`
	AgentReportedAt    string   `json:"agent_reported_at"`
	Kind               string   `json:"kind"`
	Heartbeat          bool     `json:"heartbeat"`
	Phase              string   `json:"phase"`
	Activity           string   `json:"activity"`
	NeedsInput         bool     `json:"needs_input"`
	BlockerState       string   `json:"blocker_state"`
	EstimateRevision   *int64   `json:"estimate_revision"`
	ProgressPercent    *float64 `json:"progress_percent"`
	ETASeconds         *int64   `json:"eta_seconds"`
	ETAMinSeconds      *int64   `json:"eta_min_seconds"`
	ETAMaxSeconds      *int64   `json:"eta_max_seconds"`
	EstimateSource     string   `json:"estimate_source"`
	EstimateConfidence *float64 `json:"estimate_confidence"`
	EstimateBasis      string   `json:"estimate_basis"`
}

type agentRunTelemetrySnapshot struct {
	RunID                     int64              `json:"run_id"`
	Instrumented              bool               `json:"instrumented"`
	Liveness                  string             `json:"liveness"`
	FreshnessSeconds          *int64             `json:"freshness_seconds,omitempty"` // backward-compatible latest-event freshness
	EventFreshnessSeconds     *int64             `json:"event_freshness_seconds,omitempty"`
	HeartbeatFreshnessSeconds *int64             `json:"heartbeat_freshness_seconds,omitempty"`
	SemanticFreshnessSeconds  *int64             `json:"semantic_freshness_seconds,omitempty"`
	EstimateFreshnessSeconds  *int64             `json:"estimate_freshness_seconds,omitempty"`
	LastHeartbeatAt           *string            `json:"last_heartbeat_at,omitempty"`
	Latest                    *AgentRunTelemetry `json:"latest"` // backward-compatible latest event
	LatestEvent               *AgentRunTelemetry `json:"latest_event"`
	LatestHeartbeat           *AgentRunTelemetry `json:"latest_heartbeat"`
	LatestSemantic            *AgentRunTelemetry `json:"latest_semantic"`
	LatestEstimate            *AgentRunTelemetry `json:"latest_estimate"`
}

// agentRunTelemetryIsTerminal is intentionally stricter than the run
// lifecycle predicate. A test result may still have a legal lifecycle edge to
// deployment/failure, but it closes the runner's fact stream: subsequent
// lifecycle evidence belongs on the authoritative run PATCH, not as telemetry
// appended after the test result. Exact persisted replays are resolved before
// this predicate and remain idempotent.
func agentRunTelemetryIsTerminal(status string) bool {
	switch status {
	case "completed", "tests_passed", "tests_failed", "deployed", "failed", "cancelled", "drafted":
		return true
	default:
		return false
	}
}

func canReportAgentRunTelemetry(r *http.Request, run *AgentRun) bool {
	u := auth.GetUser(r)
	if u == nil {
		return false
	}
	if u.Role == auth.RoleAdmin || u.Role == auth.RoleSuperAdmin {
		return true
	}
	return (run.RequestedBy != nil && *run.RequestedBy == u.ID) ||
		(run.ClaimedBy != nil && *run.ClaimedBy == u.ID)
}

func parseTelemetryRun(r *http.Request) (*AgentRun, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		return nil, false
	}
	run, err := getAgentRunByID(id)
	return run, err == nil
}

// IngestAgentRunTelemetry — POST /api/runs/{id}/telemetry.
func IngestAgentRunTelemetry(w http.ResponseWriter, r *http.Request) {
	run, ok := parseTelemetryRun(r)
	if !ok || !canReportAgentRunTelemetry(r, run) {
		// Run telemetry is existence-hiding: an unknown or unauthorized run has
		// the same response, including for users who can see the parent project.
		jsonError(w, "run not found", http.StatusNotFound)
		return
	}
	var input agentRunTelemetryInput
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&input); err != nil {
		jsonError(w, "invalid telemetry body", http.StatusBadRequest)
		return
	}
	if err := ensureTelemetryJSONEOF(dec); err != nil {
		jsonError(w, "invalid telemetry body", http.StatusBadRequest)
		return
	}
	receivedAt := time.Now().UTC()
	event, err := normalizeTelemetryInput(run.ID, input, receivedAt)
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

	tx, err := db.DB.BeginTx(r.Context(), nil)
	if err != nil {
		jsonError(w, "telemetry unavailable", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	// Exact persisted replays are reads, not terminal writes. Resolve them before
	// the lifecycle guard so a response-lost append can still be acknowledged
	// after another transaction finishes the run. Authorization and existence
	// hiding already happened before the body was decoded.
	if prior, err := getAgentRunTelemetryTx(tx, run.ID, event.Sequence); err == nil {
		if telemetryEventsEqual(prior, event) {
			jsonOK(w, map[string]any{"accepted": true, "duplicate": true, "telemetry": prior})
			return
		}
		jsonError(w, "sequence already exists with different telemetry", http.StatusConflict)
		return
	} else if !errors.Is(err, sql.ErrNoRows) {
		jsonError(w, "telemetry unavailable", http.StatusInternalServerError)
		return
	}

	// Recheck status inside the write transaction after ruling out an exact
	// replay. M142's INSERT trigger remains the final race-proof guard if a
	// lifecycle write overlaps a genuinely new telemetry append.
	var status string
	if err := tx.QueryRowContext(r.Context(), `SELECT status FROM agent_runs WHERE id=?`, run.ID).Scan(&status); err != nil {
		jsonError(w, "telemetry unavailable", http.StatusInternalServerError)
		return
	}
	if agentRunTelemetryIsTerminal(status) {
		jsonError(w, "terminal run telemetry is immutable", http.StatusConflict)
		return
	}

	var latestSequence int64
	var latestCorrelation, latestProvider, latestAdapter string
	latestErr := tx.QueryRowContext(r.Context(), `
		SELECT t.sequence, t.correlation_id, t.provider, t.adapter
		FROM agent_run_telemetry_latest l
		JOIN agent_run_telemetry t ON t.id=l.telemetry_id
		WHERE l.run_id=?`, run.ID).Scan(&latestSequence, &latestCorrelation, &latestProvider, &latestAdapter)
	if latestErr == nil {
		if event.Sequence <= latestSequence {
			jsonError(w, fmt.Sprintf("sequence must be greater than %d", latestSequence), http.StatusConflict)
			return
		}
		if event.CorrelationID != latestCorrelation || event.Provider != latestProvider || event.Adapter != latestAdapter {
			jsonError(w, "correlation_id, provider, and adapter are immutable for a run", http.StatusConflict)
			return
		}
	} else if !errors.Is(latestErr, sql.ErrNoRows) {
		jsonError(w, "telemetry unavailable", http.StatusInternalServerError)
		return
	}

	if event.EstimateRevision != nil {
		var maxRevision sql.NullInt64
		if err := tx.QueryRowContext(r.Context(), `SELECT MAX(estimate_revision) FROM agent_run_telemetry WHERE run_id=?`, run.ID).Scan(&maxRevision); err != nil {
			jsonError(w, "telemetry unavailable", http.StatusInternalServerError)
			return
		}
		if maxRevision.Valid && *event.EstimateRevision < maxRevision.Int64 {
			jsonError(w, fmt.Sprintf("estimate_revision must be at least %d", maxRevision.Int64), http.StatusConflict)
			return
		}
		if maxRevision.Valid && *event.EstimateRevision == maxRevision.Int64 {
			priorEstimate, err := scanAgentRunTelemetry(tx.QueryRowContext(r.Context(), `SELECT `+agentRunTelemetryCols+`
				FROM agent_run_telemetry WHERE run_id=? AND estimate_revision=? ORDER BY sequence DESC LIMIT 1`, run.ID, maxRevision.Int64))
			if err != nil {
				jsonError(w, "telemetry unavailable", http.StatusInternalServerError)
				return
			}
			if !telemetryEstimatesEqual(priorEstimate, event) {
				jsonError(w, "estimate values changed without a new estimate_revision", http.StatusConflict)
				return
			}
		}
	}

	res, err := tx.ExecContext(r.Context(), `INSERT INTO agent_run_telemetry(
		run_id, sequence, correlation_id, provider, adapter, agent_reported_at,
		server_received_at, kind, heartbeat, phase, activity, needs_input,
		blocker_state, estimate_revision, progress_percent, eta_seconds,
		eta_min_seconds, eta_max_seconds, estimate_source, estimate_confidence,
		estimate_basis) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		event.RunID, event.Sequence, event.CorrelationID, event.Provider, event.Adapter,
		event.AgentReportedAt, event.ServerReceivedAt, event.Kind, event.Heartbeat,
		event.Phase, event.Activity, event.NeedsInput, event.BlockerState,
		event.EstimateRevision, event.ProgressPercent, event.ETASeconds,
		event.ETAMinSeconds, event.ETAMaxSeconds, event.EstimateSource,
		event.EstimateConfidence, event.EstimateBasis)
	if err != nil {
		if strings.Contains(err.Error(), "terminal run telemetry") {
			jsonError(w, "terminal run telemetry is immutable", http.StatusConflict)
			return
		}
		jsonError(w, "telemetry append failed", http.StatusConflict)
		return
	}
	event.ID, _ = res.LastInsertId()
	var heartbeatAt, heartbeatID, semanticID, semanticAt, estimateID, estimateAt any
	if event.Heartbeat {
		heartbeatAt = event.ServerReceivedAt
		heartbeatID = event.ID
	}
	if isSemanticTelemetry(event) {
		semanticID = event.ID
		semanticAt = event.ServerReceivedAt
	}
	if event.EstimateRevision != nil {
		estimateID = event.ID
		estimateAt = event.ServerReceivedAt
	}
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO agent_run_telemetry_latest(
		run_id, telemetry_id, sequence, last_heartbeat_at, heartbeat_telemetry_id,
		semantic_telemetry_id, estimate_telemetry_id, latest_event_at,
		latest_semantic_at, latest_estimate_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(run_id) DO UPDATE SET
		 telemetry_id=excluded.telemetry_id,
		 sequence=excluded.sequence,
		 last_heartbeat_at=COALESCE(excluded.last_heartbeat_at, agent_run_telemetry_latest.last_heartbeat_at),
		 heartbeat_telemetry_id=COALESCE(excluded.heartbeat_telemetry_id, agent_run_telemetry_latest.heartbeat_telemetry_id),
		 semantic_telemetry_id=COALESCE(excluded.semantic_telemetry_id, agent_run_telemetry_latest.semantic_telemetry_id),
		 estimate_telemetry_id=COALESCE(excluded.estimate_telemetry_id, agent_run_telemetry_latest.estimate_telemetry_id),
		 latest_event_at=excluded.latest_event_at,
		 latest_semantic_at=COALESCE(excluded.latest_semantic_at, agent_run_telemetry_latest.latest_semantic_at),
		 latest_estimate_at=COALESCE(excluded.latest_estimate_at, agent_run_telemetry_latest.latest_estimate_at)
		WHERE excluded.sequence > agent_run_telemetry_latest.sequence`,
		run.ID, event.ID, event.Sequence, heartbeatAt, heartbeatID, semanticID,
		estimateID, event.ServerReceivedAt, semanticAt, estimateAt); err != nil {
		jsonError(w, "telemetry snapshot failed", http.StatusInternalServerError)
		return
	}
	store := deliveryStoreForRequest(r)
	effects := store.NewEffects()
	if run.DeliveryInstrumentationVersion == 1 {
		if err := store.RecordRunTelemetryChangeTx(r.Context(), tx, effects, run.ID, event.Sequence); err != nil {
			if errors.Is(err, delivery.ErrUnauthorized) {
				jsonError(w, "run not found", http.StatusNotFound)
				return
			}
			jsonError(w, "telemetry delivery hint failed", http.StatusInternalServerError)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		jsonError(w, "telemetry append failed", http.StatusConflict)
		return
	}
	effects.Dispatch(r.Context())

	if run.ProjectID != nil {
		// This is only an invalidation hint. Consumers must refetch the REST
		// snapshot; no activity or estimates travel over SSE.
		sse.GlobalBroker().PublishProject(*run.ProjectID, sse.Event{
			Type: "run_telemetry", Name: strconv.FormatInt(run.ID, 10), Rev: strconv.FormatInt(event.Sequence, 10),
		})
	}
	w.WriteHeader(http.StatusCreated)
	jsonOK(w, map[string]any{"accepted": true, "duplicate": false, "telemetry": event})
}

// ListAgentRunTelemetry — GET /api/runs/{id}/telemetry?after_sequence=&limit=.
func ListAgentRunTelemetry(w http.ResponseWriter, r *http.Request) {
	run, ok := parseTelemetryRun(r)
	if !ok || !canReadAgentRun(r, run) {
		jsonError(w, "run not found", http.StatusNotFound)
		return
	}
	after, err := parseBoundedTelemetryQuery(r, "after_sequence", 0, math.MaxInt32)
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	limit, err := parseBoundedTelemetryQuery(r, "limit", 100, 500)
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	rows, err := db.DB.QueryContext(r.Context(), `SELECT `+agentRunTelemetryCols+`
		FROM agent_run_telemetry WHERE run_id=? AND sequence>? ORDER BY sequence ASC LIMIT ?`, run.ID, after, limit)
	if err != nil {
		jsonError(w, "telemetry unavailable", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	events := make([]*AgentRunTelemetry, 0)
	for rows.Next() {
		event, err := scanAgentRunTelemetry(rows)
		if err != nil {
			jsonError(w, "telemetry unavailable", http.StatusInternalServerError)
			return
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		jsonError(w, "telemetry unavailable", http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]any{"run_id": run.ID, "events": events})
}

// GetLatestAgentRunTelemetry — GET /api/runs/{id}/telemetry/latest.
func GetLatestAgentRunTelemetry(w http.ResponseWriter, r *http.Request) {
	run, ok := parseTelemetryRun(r)
	if !ok || !canReadAgentRun(r, run) {
		jsonError(w, "run not found", http.StatusNotFound)
		return
	}
	var latestID int64
	var heartbeat sql.NullString
	var heartbeatID, semanticID, estimateID sql.NullInt64
	var eventAt, semanticAt, estimateAt sql.NullString
	err := db.DB.QueryRowContext(r.Context(), `SELECT telemetry_id, last_heartbeat_at,
		heartbeat_telemetry_id, semantic_telemetry_id, estimate_telemetry_id,
		latest_event_at, latest_semantic_at, latest_estimate_at
		FROM agent_run_telemetry_latest WHERE run_id=?`, run.ID).Scan(
		&latestID, &heartbeat, &heartbeatID, &semanticID, &estimateID,
		&eventAt, &semanticAt, &estimateAt)
	if errors.Is(err, sql.ErrNoRows) {
		jsonOK(w, agentRunTelemetrySnapshot{
			RunID: run.ID, Instrumented: false, Liveness: "unknown",
			Latest: nil, LatestEvent: nil, LatestHeartbeat: nil,
			LatestSemantic: nil, LatestEstimate: nil,
		})
		return
	}
	if err != nil {
		jsonError(w, "telemetry unavailable", http.StatusInternalServerError)
		return
	}
	event, err := getAgentRunTelemetryByID(r.Context(), latestID)
	if err != nil {
		jsonError(w, "telemetry unavailable", http.StatusInternalServerError)
		return
	}
	heartbeatEvent, err := optionalAgentRunTelemetryByID(r.Context(), heartbeatID)
	if err != nil {
		jsonError(w, "telemetry unavailable", http.StatusInternalServerError)
		return
	}
	semanticEvent, err := optionalAgentRunTelemetryByID(r.Context(), semanticID)
	if err != nil {
		jsonError(w, "telemetry unavailable", http.StatusInternalServerError)
		return
	}
	estimateEvent, err := optionalAgentRunTelemetryByID(r.Context(), estimateID)
	if err != nil {
		jsonError(w, "telemetry unavailable", http.StatusInternalServerError)
		return
	}
	now := time.Now().UTC()
	eventFreshness := telemetryFreshness(now, eventAt)
	heartbeatFreshness := telemetryFreshness(now, heartbeat)
	semanticFreshness := telemetryFreshness(now, semanticAt)
	estimateFreshness := telemetryFreshness(now, estimateAt)
	liveness := "unknown"
	if agentRunTelemetryIsTerminal(run.Status) {
		liveness = "ended"
	} else if heartbeatFreshness != nil {
		liveness = "live"
		heartbeatAt, parseErr := parseTelemetryTimestamp(heartbeat.String)
		if parseErr == nil && now.Sub(heartbeatAt) >= AgentRunReconcilerConfigFromEnv().HeartbeatTimeout {
			liveness = "stale"
		}
	}
	snapshot := agentRunTelemetrySnapshot{
		RunID: run.ID, Instrumented: true, Liveness: liveness,
		FreshnessSeconds: eventFreshness, EventFreshnessSeconds: eventFreshness,
		HeartbeatFreshnessSeconds: heartbeatFreshness,
		SemanticFreshnessSeconds:  semanticFreshness,
		EstimateFreshnessSeconds:  estimateFreshness,
		Latest:                    event, LatestEvent: event, LatestHeartbeat: heartbeatEvent,
		LatestSemantic: semanticEvent, LatestEstimate: estimateEvent,
	}
	if heartbeat.Valid {
		snapshot.LastHeartbeatAt = &heartbeat.String
	}
	jsonOK(w, snapshot)
}

func getAgentRunTelemetryByID(ctx context.Context, id int64) (*AgentRunTelemetry, error) {
	return scanAgentRunTelemetry(db.DB.QueryRowContext(ctx, `SELECT `+agentRunTelemetryCols+` FROM agent_run_telemetry WHERE id=?`, id))
}

func optionalAgentRunTelemetryByID(ctx context.Context, id sql.NullInt64) (*AgentRunTelemetry, error) {
	if !id.Valid {
		return nil, nil
	}
	return getAgentRunTelemetryByID(ctx, id.Int64)
}

func telemetryFreshness(now time.Time, value sql.NullString) *int64 {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return nil
	}
	when, err := parseTelemetryTimestamp(value.String)
	if err != nil {
		return nil
	}
	seconds := int64(now.Sub(when).Seconds())
	if seconds < 0 {
		seconds = 0
	}
	return &seconds
}

func parseTelemetryTimestamp(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return parsed, nil
	}
	return time.Parse(time.DateTime, value)
}

const agentRunTelemetryCols = `id, run_id, sequence, correlation_id, provider, adapter, agent_reported_at, server_received_at, kind, heartbeat, phase, activity, needs_input, blocker_state, estimate_revision, progress_percent, eta_seconds, eta_min_seconds, eta_max_seconds, estimate_source, estimate_confidence, estimate_basis`

func prefixedAgentRunTelemetryCols(prefix string) string {
	parts := strings.Split(agentRunTelemetryCols, ", ")
	for i := range parts {
		parts[i] = prefix + "." + parts[i]
	}
	return strings.Join(parts, ", ")
}

func scanAgentRunTelemetry(row interface{ Scan(...any) error }) (*AgentRunTelemetry, error) {
	var event AgentRunTelemetry
	var heartbeat, needsInput int
	if err := row.Scan(&event.ID, &event.RunID, &event.Sequence, &event.CorrelationID,
		&event.Provider, &event.Adapter, &event.AgentReportedAt, &event.ServerReceivedAt,
		&event.Kind, &heartbeat, &event.Phase, &event.Activity, &needsInput,
		&event.BlockerState, &event.EstimateRevision, &event.ProgressPercent,
		&event.ETASeconds, &event.ETAMinSeconds, &event.ETAMaxSeconds,
		&event.EstimateSource, &event.EstimateConfidence, &event.EstimateBasis); err != nil {
		return nil, err
	}
	event.Heartbeat = heartbeat == 1
	event.NeedsInput = needsInput == 1
	decorateTelemetryClock(&event)
	return &event, nil
}

func scanAgentRunTelemetryWithHeartbeat(row interface{ Scan(...any) error }, heartbeat *sql.NullString) (*AgentRunTelemetry, error) {
	var event AgentRunTelemetry
	var heartbeatFlag, needsInput int
	if err := row.Scan(&event.ID, &event.RunID, &event.Sequence, &event.CorrelationID,
		&event.Provider, &event.Adapter, &event.AgentReportedAt, &event.ServerReceivedAt,
		&event.Kind, &heartbeatFlag, &event.Phase, &event.Activity, &needsInput,
		&event.BlockerState, &event.EstimateRevision, &event.ProgressPercent,
		&event.ETASeconds, &event.ETAMinSeconds, &event.ETAMaxSeconds,
		&event.EstimateSource, &event.EstimateConfidence, &event.EstimateBasis, heartbeat); err != nil {
		return nil, err
	}
	event.Heartbeat = heartbeatFlag == 1
	event.NeedsInput = needsInput == 1
	decorateTelemetryClock(&event)
	return &event, nil
}

func getAgentRunTelemetryTx(tx *sql.Tx, runID, sequence int64) (*AgentRunTelemetry, error) {
	return scanAgentRunTelemetry(tx.QueryRow(`SELECT `+agentRunTelemetryCols+` FROM agent_run_telemetry WHERE run_id=? AND sequence=?`, runID, sequence))
}

func normalizeTelemetryInput(runID int64, in agentRunTelemetryInput, received time.Time) (*AgentRunTelemetry, error) {
	in.CorrelationID = strings.TrimSpace(in.CorrelationID)
	in.Provider = strings.TrimSpace(in.Provider)
	in.Adapter = strings.TrimSpace(in.Adapter)
	in.Kind = strings.TrimSpace(in.Kind)
	in.Phase = strings.TrimSpace(in.Phase)
	in.Activity = strings.TrimSpace(in.Activity)
	in.BlockerState = strings.TrimSpace(in.BlockerState)
	in.EstimateSource = strings.TrimSpace(in.EstimateSource)
	in.EstimateBasis = strings.TrimSpace(in.EstimateBasis)
	if in.Sequence <= 0 || in.Sequence > math.MaxInt32 {
		return nil, fmt.Errorf("sequence must be between 1 and %d", math.MaxInt32)
	}
	if err := validateTelemetryIdentity("correlation_id", in.CorrelationID, 128); err != nil {
		return nil, err
	}
	if err := validateTelemetryIdentity("provider", in.Provider, 64); err != nil {
		return nil, err
	}
	if err := validateTelemetryIdentity("adapter", in.Adapter, 64); err != nil {
		return nil, err
	}
	reported, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(in.AgentReportedAt))
	if err != nil || reported.Year() < 2000 || reported.Year() > 2100 {
		return nil, fmt.Errorf("agent_reported_at must be RFC3339 with a year from 2000 through 2100")
	}
	if !telemetryKinds[in.Kind] {
		return nil, fmt.Errorf("invalid kind")
	}
	if in.Kind == "heartbeat" && !in.Heartbeat {
		return nil, fmt.Errorf("kind=heartbeat requires heartbeat=true")
	}
	if in.Phase == "" {
		in.Phase = "unknown"
	}
	if !telemetryPhases[in.Phase] {
		return nil, fmt.Errorf("invalid phase")
	}
	if in.BlockerState == "" {
		in.BlockerState = "none"
	}
	if !telemetryBlockerStates[in.BlockerState] {
		return nil, fmt.Errorf("invalid blocker_state")
	}
	if in.NeedsInput && in.BlockerState != "input" {
		return nil, fmt.Errorf("needs_input requires blocker_state=input")
	}
	if !in.NeedsInput && in.BlockerState == "input" {
		return nil, fmt.Errorf("blocker_state=input requires needs_input=true")
	}
	if err := validateTelemetryText("activity", in.Activity, telemetryActivityMaxBytes); err != nil {
		return nil, err
	}
	if err := validateTelemetryText("estimate_basis", in.EstimateBasis, telemetryBasisMaxBytes); err != nil {
		return nil, err
	}
	if err := validateTelemetryEstimate(in); err != nil {
		return nil, err
	}
	event := &AgentRunTelemetry{
		RunID: runID, Sequence: in.Sequence, CorrelationID: in.CorrelationID,
		Provider: in.Provider, Adapter: in.Adapter,
		AgentReportedAt:  reported.UTC().Format(time.RFC3339Nano),
		ServerReceivedAt: received.UTC().Format(time.RFC3339Nano),
		Kind:             in.Kind, Heartbeat: in.Heartbeat, Phase: in.Phase,
		Activity: in.Activity, NeedsInput: in.NeedsInput, BlockerState: in.BlockerState,
		EstimateRevision: in.EstimateRevision, ProgressPercent: in.ProgressPercent,
		ETASeconds: in.ETASeconds, ETAMinSeconds: in.ETAMinSeconds,
		ETAMaxSeconds: in.ETAMaxSeconds, EstimateSource: in.EstimateSource,
		EstimateConfidence: in.EstimateConfidence, EstimateBasis: in.EstimateBasis,
	}
	decorateTelemetryClock(event)
	return event, nil
}

func validateTelemetryEstimate(in agentRunTelemetryInput) error {
	hasValue := in.ProgressPercent != nil || in.ETASeconds != nil || in.ETAMinSeconds != nil || in.ETAMaxSeconds != nil
	hasEvidence := in.EstimateRevision != nil || in.EstimateSource != "" || in.EstimateConfidence != nil || in.EstimateBasis != ""
	if !hasValue {
		if hasEvidence {
			return fmt.Errorf("estimate evidence requires progress_percent or ETA fields")
		}
		return nil
	}
	if in.EstimateRevision == nil || *in.EstimateRevision <= 0 || *in.EstimateRevision > math.MaxInt32 {
		return fmt.Errorf("estimate_revision must be between 1 and %d", math.MaxInt32)
	}
	if !telemetryEstimateSources[in.EstimateSource] {
		return fmt.Errorf("invalid estimate_source")
	}
	if in.EstimateConfidence == nil || *in.EstimateConfidence < 0 || *in.EstimateConfidence > 1 {
		return fmt.Errorf("estimate_confidence must be between 0 and 1")
	}
	if in.EstimateBasis == "" {
		return fmt.Errorf("estimate_basis is required for progress or ETA")
	}
	if in.ProgressPercent != nil && (*in.ProgressPercent < 0 || *in.ProgressPercent > 100) {
		return fmt.Errorf("progress_percent must be between 0 and 100")
	}
	for _, field := range []struct {
		name  string
		value *int64
	}{
		{"eta_seconds", in.ETASeconds},
		{"eta_min_seconds", in.ETAMinSeconds},
		{"eta_max_seconds", in.ETAMaxSeconds},
	} {
		if field.value != nil && (*field.value < 0 || *field.value > 31536000) {
			return fmt.Errorf("%s must be between 0 and 31536000", field.name)
		}
	}
	hasETA := in.ETASeconds != nil || in.ETAMinSeconds != nil || in.ETAMaxSeconds != nil
	if hasETA && (in.ETAMinSeconds == nil || in.ETAMaxSeconds == nil) {
		return fmt.Errorf("ETA requires eta_min_seconds and eta_max_seconds")
	}
	if in.ETAMinSeconds != nil && *in.ETAMinSeconds > *in.ETAMaxSeconds {
		return fmt.Errorf("eta_min_seconds must not exceed eta_max_seconds")
	}
	if in.ETASeconds != nil && in.ETAMinSeconds != nil && (*in.ETASeconds < *in.ETAMinSeconds || *in.ETASeconds > *in.ETAMaxSeconds) {
		return fmt.Errorf("eta_seconds must fall inside the ETA range")
	}
	return nil
}

func validateTelemetryIdentity(field, value string, max int) error {
	if value == "" || len(value) > max || !telemetryIdentityPattern.MatchString(value) {
		return fmt.Errorf("%s must be 1-%d allowlisted identifier characters", field, max)
	}
	return nil
}

func validateTelemetryText(field, value string, max int) error {
	if len(value) > max || !utf8.ValidString(value) || strings.ContainsAny(value, "\r\n\x00") {
		return fmt.Errorf("%s must be one valid UTF-8 line no longer than %d bytes", field, max)
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%s contains control characters", field)
		}
	}
	for _, pattern := range telemetrySecretPatterns {
		if pattern.MatchString(value) {
			return fmt.Errorf("%s contains an obvious secret-bearing value", field)
		}
	}
	return nil
}

func isSemanticTelemetry(event *AgentRunTelemetry) bool {
	if event == nil {
		return false
	}
	explicitKind := event.Kind == "phase" || event.Kind == "needs_input" || event.Kind == "blocker"
	return explicitKind || event.Phase != "unknown" || event.Activity != "" || event.NeedsInput || event.BlockerState != "none"
}

func decorateTelemetryClock(event *AgentRunTelemetry) {
	reported, err1 := time.Parse(time.RFC3339Nano, event.AgentReportedAt)
	received, err2 := time.Parse(time.RFC3339Nano, event.ServerReceivedAt)
	if err1 != nil || err2 != nil {
		return
	}
	skew := received.Sub(reported)
	event.ClockSkewSeconds = int64(skew.Seconds())
	event.ClockSkewed = skew > telemetryClockSkewAfter || skew < -telemetryClockSkewAfter
}

func telemetryEventsEqual(a, b *AgentRunTelemetry) bool {
	ac, _ := json.Marshal(struct {
		Sequence                                                int64 `json:"sequence"`
		CorrelationID, Provider, Adapter, AgentReportedAt, Kind string
		Heartbeat                                               bool
		Phase, Activity                                         string
		NeedsInput                                              bool
		BlockerState                                            string
		EstimateRevision                                        *int64
		ProgressPercent                                         *float64
		ETASeconds, ETAMinSeconds, ETAMaxSeconds                *int64
		EstimateSource                                          string
		EstimateConfidence                                      *float64
		EstimateBasis                                           string
	}{a.Sequence, a.CorrelationID, a.Provider, a.Adapter, a.AgentReportedAt, a.Kind, a.Heartbeat, a.Phase, a.Activity, a.NeedsInput, a.BlockerState, a.EstimateRevision, a.ProgressPercent, a.ETASeconds, a.ETAMinSeconds, a.ETAMaxSeconds, a.EstimateSource, a.EstimateConfidence, a.EstimateBasis})
	bc, _ := json.Marshal(struct {
		Sequence                                                int64 `json:"sequence"`
		CorrelationID, Provider, Adapter, AgentReportedAt, Kind string
		Heartbeat                                               bool
		Phase, Activity                                         string
		NeedsInput                                              bool
		BlockerState                                            string
		EstimateRevision                                        *int64
		ProgressPercent                                         *float64
		ETASeconds, ETAMinSeconds, ETAMaxSeconds                *int64
		EstimateSource                                          string
		EstimateConfidence                                      *float64
		EstimateBasis                                           string
	}{b.Sequence, b.CorrelationID, b.Provider, b.Adapter, b.AgentReportedAt, b.Kind, b.Heartbeat, b.Phase, b.Activity, b.NeedsInput, b.BlockerState, b.EstimateRevision, b.ProgressPercent, b.ETASeconds, b.ETAMinSeconds, b.ETAMaxSeconds, b.EstimateSource, b.EstimateConfidence, b.EstimateBasis})
	return string(ac) == string(bc)
}

func telemetryEstimatesEqual(a, b *AgentRunTelemetry) bool {
	ac, _ := json.Marshal([]any{a.EstimateRevision, a.ProgressPercent, a.ETASeconds, a.ETAMinSeconds, a.ETAMaxSeconds, a.EstimateSource, a.EstimateConfidence, a.EstimateBasis})
	bc, _ := json.Marshal([]any{b.EstimateRevision, b.ProgressPercent, b.ETASeconds, b.ETAMinSeconds, b.ETAMaxSeconds, b.EstimateSource, b.EstimateConfidence, b.EstimateBasis})
	return string(ac) == string(bc)
}

func ensureTelemetryJSONEOF(dec *json.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("trailing JSON")
	}
	return nil
}

func parseBoundedTelemetryQuery(r *http.Request, name string, defaultValue, max int64) (int64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return defaultValue, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 || n > max {
		return 0, fmt.Errorf("invalid %s", name)
	}
	return n, nil
}
