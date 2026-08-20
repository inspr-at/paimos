// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"context"
	"database/sql"
	"log"
	"os"
	"strings"
	"time"

	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/sse"
)

const (
	defaultAgentRunReconcileInterval     = 30 * time.Second
	defaultAgentRunQueuedTimeout         = 15 * time.Minute
	defaultAgentRunFirstHeartbeatTimeout = 1 * time.Minute
	defaultAgentRunHeartbeatTimeout      = 90 * time.Second
	defaultAgentRunLegacyFallbackTimeout = 2 * time.Hour
)

// AgentRunReconcilerConfig keeps every active-run watchdog threshold together.
// Supervisor heartbeat liveness and semantic event freshness intentionally use
// different clocks; only server-received heartbeat timestamps feed this policy.
type AgentRunReconcilerConfig struct {
	Interval              time.Duration
	QueuedTimeout         time.Duration
	FirstHeartbeatTimeout time.Duration
	HeartbeatTimeout      time.Duration
	LegacyFallbackTimeout time.Duration
}

func AgentRunReconcilerConfigFromEnv() AgentRunReconcilerConfig {
	return AgentRunReconcilerConfig{
		Interval:              positiveDurationEnv("PAIMOS_RUN_RECONCILE_INTERVAL", defaultAgentRunReconcileInterval),
		QueuedTimeout:         positiveDurationEnv("PAIMOS_RUN_QUEUED_TIMEOUT", defaultAgentRunQueuedTimeout),
		FirstHeartbeatTimeout: positiveDurationEnv("PAIMOS_RUN_FIRST_HEARTBEAT_TIMEOUT", defaultAgentRunFirstHeartbeatTimeout),
		HeartbeatTimeout:      positiveDurationEnv("PAIMOS_RUN_HEARTBEAT_TIMEOUT", defaultAgentRunHeartbeatTimeout),
		LegacyFallbackTimeout: positiveDurationEnv("PAIMOS_RUN_LEGACY_TIMEOUT", defaultAgentRunLegacyFallbackTimeout),
	}
}

func positiveDurationEnv(name string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}

// StartAgentRunReconciler starts the process-owned active-run watchdog. It runs
// once at boot and then on the configured cadence, so cleanup never depends on
// a user opening or clicking an issue.
func StartAgentRunReconciler() {
	cfg := AgentRunReconcilerConfigFromEnv()
	go func() {
		run := func() {
			if n, err := ReconcileStaleAgentRuns(context.Background(), time.Now().UTC(), cfg); err != nil {
				log.Printf("agent run reconciler: %v", err)
			} else if n > 0 {
				log.Printf("agent run reconciler: failed %d stale active run(s)", n)
			}
		}
		run()
		ticker := time.NewTicker(cfg.Interval)
		defer ticker.Stop()
		for range ticker.C {
			run()
		}
	}()
}

type staleAgentRunCandidate struct {
	id        int64
	projectID sql.NullInt64
	kind      string
	errorText string
}

// ReconcileStaleAgentRuns applies race-safe compare-and-set failure updates.
// Each UPDATE repeats the freshness predicate used to select the candidate, so
// a heartbeat or terminal write that wins the SQLite writer lock suppresses the
// contradictory stale failure.
func ReconcileStaleAgentRuns(ctx context.Context, now time.Time, cfg AgentRunReconcilerConfig) (int, error) {
	queuedCutoff := now.Add(-cfg.QueuedTimeout).UTC().Format(time.RFC3339Nano)
	firstCutoff := now.Add(-cfg.FirstHeartbeatTimeout).UTC().Format(time.RFC3339Nano)
	heartbeatCutoff := now.Add(-cfg.HeartbeatTimeout).UTC().Format(time.RFC3339Nano)
	legacyCutoff := now.Add(-cfg.LegacyFallbackTimeout).UTC().Format(time.RFC3339Nano)
	rows, err := db.DB.QueryContext(ctx, `
		SELECT ar.id, ar.project_id,
		 CASE
		  WHEN ar.status='queued' THEN 'queued'
		  WHEN ar.expects_supervisor_telemetry=1 AND l.last_heartbeat_at IS NULL THEN 'first_heartbeat'
		  WHEN ar.expects_supervisor_telemetry=1 THEN 'heartbeat'
		  ELSE 'legacy'
		 END
		FROM agent_runs ar
		LEFT JOIN agent_run_telemetry_latest l ON l.run_id=ar.id
		WHERE (ar.status='queued' AND julianday(ar.created_at) <= julianday(?))
		   OR (ar.status='running' AND ar.expects_supervisor_telemetry=1 AND l.last_heartbeat_at IS NULL
		       AND julianday(COALESCE(NULLIF(ar.started_at,''), ar.created_at)) <= julianday(?))
		   OR (ar.status='running' AND ar.expects_supervisor_telemetry=1 AND l.last_heartbeat_at IS NOT NULL
		       AND julianday(l.last_heartbeat_at) <= julianday(?))
		   OR (ar.status='running' AND ar.expects_supervisor_telemetry=0
		       AND julianday(COALESCE(NULLIF(ar.started_at,''), ar.created_at)) <= julianday(?))
		ORDER BY ar.id`, queuedCutoff, firstCutoff, heartbeatCutoff, legacyCutoff)
	if err != nil {
		return 0, err
	}
	var candidates []staleAgentRunCandidate
	for rows.Next() {
		var c staleAgentRunCandidate
		if err := rows.Scan(&c.id, &c.projectID, &c.kind); err != nil {
			rows.Close()
			return 0, err
		}
		switch c.kind {
		case "queued":
			c.errorText = "queued timeout (no runner claimed run)"
		case "first_heartbeat":
			c.errorText = "supervisor timeout (no heartbeat received)"
		case "heartbeat":
			c.errorText = "supervisor heartbeat timeout"
		default:
			c.errorText = "abandoned legacy runner (no terminal report)"
		}
		candidates = append(candidates, c)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}

	updated := 0
	for _, c := range candidates {
		var query string
		var args []any
		switch c.kind {
		case "queued":
			query = `UPDATE agent_runs SET status='failed', error=?, finished_at=datetime('now')
			 WHERE id=? AND status='queued' AND julianday(created_at) <= julianday(?)`
			args = []any{c.errorText, c.id, queuedCutoff}
		case "first_heartbeat":
			query = `UPDATE agent_runs SET status='failed', error=?, finished_at=datetime('now')
			 WHERE id=? AND status='running' AND expects_supervisor_telemetry=1
			 AND julianday(COALESCE(NULLIF(started_at,''), created_at)) <= julianday(?)
			 AND NOT EXISTS (SELECT 1 FROM agent_run_telemetry_latest l WHERE l.run_id=agent_runs.id AND l.last_heartbeat_at IS NOT NULL)`
			args = []any{c.errorText, c.id, firstCutoff}
		case "heartbeat":
			query = `UPDATE agent_runs SET status='failed', error=?, finished_at=datetime('now')
			 WHERE id=? AND status='running' AND expects_supervisor_telemetry=1
			 AND EXISTS (SELECT 1 FROM agent_run_telemetry_latest l WHERE l.run_id=agent_runs.id
			             AND l.last_heartbeat_at IS NOT NULL AND julianday(l.last_heartbeat_at) <= julianday(?))`
			args = []any{c.errorText, c.id, heartbeatCutoff}
		default:
			query = `UPDATE agent_runs SET status='failed', error=?, finished_at=datetime('now')
			 WHERE id=? AND status='running' AND expects_supervisor_telemetry=0
			 AND julianday(COALESCE(NULLIF(started_at,''), created_at)) <= julianday(?)`
			args = []any{c.errorText, c.id, legacyCutoff}
		}
		res, err := db.DB.ExecContext(ctx, query, args...)
		if err != nil {
			return updated, err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			continue
		}
		updated++
		if run, err := getAgentRunByID(c.id); err == nil {
			postAgentRunReport(run.IssueID, nil, run)
		}
		if c.projectID.Valid {
			sse.GlobalBroker().PublishProject(c.projectID.Int64, sse.Event{Type: "implement_run", Rev: "failed"})
		}
	}
	return updated, nil
}
