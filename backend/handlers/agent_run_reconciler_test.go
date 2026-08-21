// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers_test

import (
	"context"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/delivery"
	"github.com/inspr-at/paimos/backend/handlers"
)

func TestAgentRunReconcilerConfigDefaultsAndOverrides(t *testing.T) {
	for _, name := range []string{
		"PAIMOS_RUN_RECONCILE_INTERVAL", "PAIMOS_RUN_QUEUED_TIMEOUT",
		"PAIMOS_RUN_FIRST_HEARTBEAT_TIMEOUT", "PAIMOS_RUN_HEARTBEAT_TIMEOUT",
		"PAIMOS_RUN_LEGACY_TIMEOUT",
	} {
		t.Setenv(name, "")
	}
	cfg := handlers.AgentRunReconcilerConfigFromEnv()
	if cfg.Interval != 30*time.Second || cfg.QueuedTimeout != 15*time.Minute ||
		cfg.FirstHeartbeatTimeout != time.Minute || cfg.HeartbeatTimeout != 90*time.Second ||
		cfg.LegacyFallbackTimeout != 2*time.Hour {
		t.Fatalf("defaults=%+v", cfg)
	}
	t.Setenv("PAIMOS_RUN_HEARTBEAT_TIMEOUT", "45s")
	t.Setenv("PAIMOS_RUN_LEGACY_TIMEOUT", "3h")
	cfg = handlers.AgentRunReconcilerConfigFromEnv()
	if cfg.HeartbeatTimeout != 45*time.Second || cfg.LegacyFallbackTimeout != 3*time.Hour {
		t.Fatalf("overrides=%+v", cfg)
	}
}

func TestAgentRunReconcilerDistinguishesQueuedSupervisorAndLegacy(t *testing.T) {
	ts := newDirectTelemetryServer(t)
	projectID := seedBatchProject(t, "RPR", "Reaper")
	now := time.Now().UTC()
	seed := func(number int, status string, supervised bool, age string) int64 {
		t.Helper()
		res, err := db.DB.Exec(`INSERT INTO issues(project_id, issue_number, type, title, status) VALUES(?,?,'ticket',?,'in-progress')`,
			projectID, number, "reaper case")
		if err != nil {
			t.Fatal(err)
		}
		issueID, _ := res.LastInsertId()
		expects := 0
		if supervised {
			expects = 1
		}
		res, err = db.DB.Exec(`INSERT INTO agent_runs(issue_id, project_id, requested_by, status, expects_supervisor_telemetry, created_at, started_at)
			VALUES(?,?,(SELECT id FROM users WHERE username='admin'),?,?,datetime('now',?),CASE WHEN ?='running' THEN datetime('now',?) END)`,
			issueID, projectID, status, expects, age, status, age)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	queued := seed(1, "queued", false, "-20 minutes")
	firstHeartbeat := seed(2, "running", true, "-2 minutes")
	staleHeartbeat := seed(3, "running", true, "-10 minutes")
	legacy := seed(4, "running", false, "-3 hours")
	// This run is older than the legacy two-hour fallback, but its fresh
	// server-received heartbeat must keep it alive.
	fresh := seed(5, "running", true, "-3 hours")

	// Seed a real heartbeat snapshot, then age only its server-owned receipt
	// timestamp. Semantic activity is irrelevant to the watchdog.
	report := telemetryReport(1, now.Format(time.RFC3339Nano))
	for _, runID := range []int64{staleHeartbeat, fresh} {
		assertStatus(t, ts.post(t, "/api/runs/"+itoa(runID)+"/telemetry", ts.adminCookie, report), 201)
	}
	// The event ledger is append-only; age only the mutable, server-owned
	// heartbeat projection used by the watchdog.
	if _, err := db.DB.Exec(`UPDATE agent_run_telemetry_latest SET last_heartbeat_at=datetime('now','-10 minutes') WHERE run_id=?`, staleHeartbeat); err != nil {
		t.Fatal(err)
	}

	cfg := handlers.AgentRunReconcilerConfig{
		Interval: 30 * time.Second, QueuedTimeout: 15 * time.Minute,
		FirstHeartbeatTimeout: time.Minute, HeartbeatTimeout: 90 * time.Second,
		LegacyFallbackTimeout: 2 * time.Hour,
	}
	updated, err := handlers.ReconcileStaleAgentRuns(context.Background(), now, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if updated != 4 {
		t.Fatalf("updated=%d want 4", updated)
	}
	for id, wantError := range map[int64]string{
		queued:         "queued timeout (no runner claimed run)",
		firstHeartbeat: "supervisor timeout (no heartbeat received)",
		staleHeartbeat: "supervisor heartbeat timeout",
		legacy:         "abandoned legacy runner (no terminal report)",
	} {
		var status, errorText string
		if err := db.DB.QueryRow(`SELECT status,error FROM agent_runs WHERE id=?`, id).Scan(&status, &errorText); err != nil {
			t.Fatal(err)
		}
		if status != "failed" || errorText != wantError {
			t.Fatalf("run %d status=%q error=%q", id, status, errorText)
		}
	}
	var freshStatus string
	if err := db.DB.QueryRow(`SELECT status FROM agent_runs WHERE id=?`, fresh).Scan(&freshStatus); err != nil || freshStatus != "running" {
		t.Fatalf("fresh status=%q err=%v", freshStatus, err)
	}
}

func TestAgentRunReconcilerIgnoresEveryNonActiveStatus(t *testing.T) {
	_ = newDirectTelemetryServer(t)
	projectID := seedBatchProject(t, "RAT", "Reaper terminal matrix")
	statuses := []string{"completed", "tests_passed", "tests_failed", "deployed", "failed", "cancelled", "drafted"}
	for i, status := range statuses {
		res, err := db.DB.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status) VALUES(?,?,'ticket',?,'in-progress')`, projectID, i+1, status)
		if err != nil {
			t.Fatal(err)
		}
		issueID, _ := res.LastInsertId()
		if _, err := db.DB.Exec(`INSERT INTO agent_runs(issue_id,project_id,status,expects_supervisor_telemetry,created_at,started_at,finished_at)
			VALUES(?,?,?,1,datetime('now','-1 day'),datetime('now','-1 day'),datetime('now','-23 hours'))`, issueID, projectID, status); err != nil {
			t.Fatal(err)
		}
	}
	updated, err := handlers.ReconcileStaleAgentRuns(context.Background(), time.Now().UTC(), handlers.AgentRunReconcilerConfig{
		Interval: time.Second, QueuedTimeout: time.Second, FirstHeartbeatTimeout: time.Second,
		HeartbeatTimeout: time.Second, LegacyFallbackTimeout: time.Second,
	})
	if err != nil || updated != 0 {
		t.Fatalf("updated=%d err=%v", updated, err)
	}
	for _, status := range statuses {
		var count int
		if err := db.DB.QueryRow(`SELECT COUNT(*) FROM agent_runs WHERE status=?`, status).Scan(&count); err != nil || count != 1 {
			t.Fatalf("status %q count=%d err=%v", status, count, err)
		}
	}
}

func TestAgentRunReconcilerDeterministicHeartbeatRaceOrders(t *testing.T) {
	ts := newDirectTelemetryServer(t)
	projectID := seedBatchProject(t, "RRO", "Reaper ordered races")
	seed := func(number int) int64 {
		res, err := db.DB.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status) VALUES(?,?,'ticket','ordered race','in-progress')`, projectID, number)
		if err != nil {
			t.Fatal(err)
		}
		issueID, _ := res.LastInsertId()
		res, err = db.DB.Exec(`INSERT INTO agent_runs(issue_id,project_id,status,expects_supervisor_telemetry,created_at,started_at)
			VALUES(?,?,'running',1,datetime('now','-5 minutes'),datetime('now','-5 minutes'))`, issueID, projectID)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	cfg := handlers.AgentRunReconcilerConfig{Interval: time.Second, QueuedTimeout: time.Minute, FirstHeartbeatTimeout: time.Second, HeartbeatTimeout: time.Minute, LegacyFallbackTimeout: time.Hour}
	now := time.Now().UTC()

	heartbeatFirst := seed(1)
	assertStatus(t, ts.post(t, "/api/runs/"+itoa(heartbeatFirst)+"/telemetry", ts.adminCookie,
		telemetryReport(1, now.Format(time.RFC3339Nano))), 201)
	if n, err := handlers.ReconcileStaleAgentRuns(context.Background(), now, cfg); err != nil || n != 0 {
		t.Fatalf("heartbeat-first reconciled=%d err=%v", n, err)
	}

	reaperFirst := seed(2)
	if n, err := handlers.ReconcileStaleAgentRuns(context.Background(), now, cfg); err != nil || n != 1 {
		t.Fatalf("reaper-first reconciled=%d err=%v", n, err)
	}
	assertStatus(t, ts.post(t, "/api/runs/"+itoa(reaperFirst)+"/telemetry", ts.adminCookie,
		telemetryReport(1, now.Format(time.RFC3339Nano))), 409)
}

func TestAgentRunReconcilerDoesNotOverwriteTerminalWinner(t *testing.T) {
	_ = newDirectTelemetryServer(t)
	projectID := seedBatchProject(t, "RRC", "Reaper Race")
	res, err := db.DB.Exec(`INSERT INTO issues(project_id, issue_number, type, title, status) VALUES(?,1,'ticket','race','in-progress')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	issueID, _ := res.LastInsertId()
	res, err = db.DB.Exec(`INSERT INTO agent_runs(issue_id,project_id,status,expects_supervisor_telemetry,created_at,started_at)
		VALUES(?,?,'running',1,datetime('now','-5 minutes'),datetime('now','-5 minutes'))`, issueID, projectID)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := res.LastInsertId()
	if _, err := db.DB.Exec(`UPDATE agent_runs SET status='completed', finished_at=datetime('now') WHERE id=? AND status='running'`, runID); err != nil {
		t.Fatal(err)
	}
	updated, err := handlers.ReconcileStaleAgentRuns(context.Background(), time.Now().UTC(), handlers.AgentRunReconcilerConfig{
		Interval: time.Second, QueuedTimeout: time.Minute, FirstHeartbeatTimeout: time.Minute,
		HeartbeatTimeout: time.Minute, LegacyFallbackTimeout: time.Minute,
	})
	if err != nil || updated != 0 {
		t.Fatalf("updated=%d err=%v", updated, err)
	}
	var status string
	if err := db.DB.QueryRow(`SELECT status FROM agent_runs WHERE id=?`, runID).Scan(&status); err != nil || status != "completed" {
		t.Fatalf("status=%q err=%v", status, err)
	}
}

func TestAgentRunReconcilerHeartbeatRaceHasNoContradictoryWinner(t *testing.T) {
	ts := newDirectTelemetryServer(t)
	projectID := seedBatchProject(t, "RHB", "Reaper Heartbeat Race")
	res, err := db.DB.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status) VALUES(?,1,'ticket','heartbeat race','in-progress')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	issueID, _ := res.LastInsertId()
	res, err = db.DB.Exec(`INSERT INTO agent_runs(issue_id,project_id,status,expects_supervisor_telemetry,created_at,started_at)
		VALUES(?,?,'running',1,datetime('now','-5 minutes'),datetime('now','-5 minutes'))`, issueID, projectID)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := res.LastInsertId()
	now := time.Now().UTC()
	cfg := handlers.AgentRunReconcilerConfig{
		Interval: time.Second, QueuedTimeout: time.Minute, FirstHeartbeatTimeout: time.Second,
		HeartbeatTimeout: time.Minute, LegacyFallbackTimeout: time.Hour,
	}
	start := make(chan struct{})
	postStatus := make(chan int, 1)
	reconcileResult := make(chan struct {
		n   int
		err error
	}, 1)
	go func() {
		<-start
		resp := ts.post(t, "/api/runs/"+itoa(runID)+"/telemetry", ts.adminCookie,
			telemetryReport(1, now.Format(time.RFC3339Nano)))
		postStatus <- resp.StatusCode
		_ = resp.Body.Close()
	}()
	go func() {
		<-start
		n, err := handlers.ReconcileStaleAgentRuns(context.Background(), now, cfg)
		reconcileResult <- struct {
			n   int
			err error
		}{n, err}
	}()
	close(start)
	statusCode := <-postStatus
	result := <-reconcileResult
	if result.err != nil {
		t.Fatal(result.err)
	}
	var status string
	if err := db.DB.QueryRow(`SELECT status FROM agent_runs WHERE id=?`, runID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if statusCode == 201 {
		if status != "running" || result.n != 0 {
			t.Fatalf("heartbeat won but status=%q reconciled=%d", status, result.n)
		}
	} else if statusCode == 409 {
		if status != "failed" || result.n != 1 {
			t.Fatalf("reaper won but status=%q reconciled=%d", status, result.n)
		}
	} else {
		t.Fatalf("heartbeat status=%d final=%q reconciled=%d", statusCode, status, result.n)
	}
}

func TestAgentRunReconcilerMalformedInstrumentedCandidateDoesNotStarveLaterRun(t *testing.T) {
	_ = newDirectTelemetryServer(t)
	projectID := seedBatchProject(t, "RNS", "Reaper non-starvation")
	adminID := userID(t, "admin")
	now := time.Now().UTC()
	seed := func(number, instrumentation, supervised int) int64 {
		t.Helper()
		result, err := db.DB.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status)
			VALUES(?,?,'ticket','Reconciler candidate','in-progress')`, projectID, number)
		if err != nil {
			t.Fatal(err)
		}
		issueID, _ := result.LastInsertId()
		result, err = db.DB.Exec(`INSERT INTO agent_runs(issue_id,project_id,requested_by,status,
			expects_supervisor_telemetry,delivery_instrumentation_version,created_at,started_at)
			VALUES(?,?,?,'running',?,?,?,?)`, issueID, projectID, adminID, supervised, instrumentation,
			now.Add(-3*time.Hour).Format(time.RFC3339Nano), now.Add(-3*time.Hour).Format(time.RFC3339Nano))
		if err != nil {
			t.Fatal(err)
		}
		runID, _ := result.LastInsertId()
		return runID
	}
	malformed := seed(1, 1, 1) // Post-M144 marker without its mandatory link.
	healthyLegacy := seed(2, 0, 0)
	updated, err := handlers.ReconcileStaleAgentRuns(context.Background(), now, handlers.AgentRunReconcilerConfig{
		Interval: time.Second, QueuedTimeout: time.Minute, FirstHeartbeatTimeout: time.Minute,
		HeartbeatTimeout: time.Minute, LegacyFallbackTimeout: time.Minute,
	})
	if err != nil || updated != 1 {
		t.Fatalf("reconcile updated=%d err=%v, want one healthy later candidate", updated, err)
	}
	for runID, want := range map[int64]string{malformed: "running", healthyLegacy: "failed"} {
		var got string
		if err := db.DB.QueryRow(`SELECT status FROM agent_runs WHERE id=?`, runID).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("run %d status=%q want %q", runID, got, want)
		}
	}
}

func TestAgentRunFirstSignalAnchorUsesLaterClaimTime(t *testing.T) {
	ts := newDirectTelemetryServer(t)
	projectID := seedBatchProject(t, "RFA", "Run first-signal anchor")
	adminID := userID(t, "admin")
	result, err := db.DB.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status)
		VALUES(?,1,'ticket','Queue before claim','in-progress')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	issueID, _ := result.LastInsertId()
	activationAt := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Second)
	result, err = db.DB.Exec(`INSERT INTO agent_runs(issue_id,project_id,requested_by,status,
		delivery_instrumentation_version,created_at) VALUES(?,?,?,'queued',1,?)`, issueID, projectID,
		adminID, activationAt.Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := result.LastInsertId()
	bootstrapStore := delivery.NewStore(db.DB, delivery.Options{Clock: delivery.ClockFunc(func() time.Time { return activationAt })})
	tx, err := db.DB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	effects := bootstrapStore.NewEffects()
	if _, err := bootstrapStore.BootstrapRunTx(t.Context(), tx, effects, delivery.RunBootstrap{IssueID: issueID,
		RunID: runID, Mode: "implementation", Actor: delivery.Actor{Type: "user", OpaqueKey: "user:" + itoa(adminID)},
		IdempotencyKey: "claim-anchor-bootstrap"}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	effects.Dispatch(t.Context())

	resp := ts.patch(t, "/api/runs/"+itoa(runID), ts.adminCookie, map[string]any{
		"status": "running", "if_status": "queued", "expects_supervisor_telemetry": true,
	})
	assertStatus(t, resp, 200)
	resp.Body.Close()
	var startedRaw string
	if err := db.DB.QueryRow(`SELECT strftime('%Y-%m-%dT%H:%M:%fZ',started_at) FROM agent_runs WHERE id=?`, runID).
		Scan(&startedRaw); err != nil {
		t.Fatal(err)
	}
	startedAt, err := time.Parse(time.RFC3339Nano, startedRaw)
	if err != nil {
		t.Fatal(err)
	}
	cfg := handlers.AgentRunReconcilerConfig{Interval: time.Second, QueuedTimeout: 15 * time.Minute,
		FirstHeartbeatTimeout: time.Minute, HeartbeatTimeout: 90 * time.Second, LegacyFallbackTimeout: 2 * time.Hour}
	justBefore := startedAt.Add(time.Minute - time.Millisecond)
	if updated, err := handlers.ReconcileStaleAgentRuns(t.Context(), justBefore, cfg); err != nil || updated != 0 {
		t.Fatalf("freshly claimed old queued run reconciled early: updated=%d err=%v", updated, err)
	}
	readStore := delivery.NewStore(db.DB, delivery.Options{Clock: delivery.ClockFunc(func() time.Time { return justBefore }),
		Freshness: delivery.FreshnessPolicy{FirstSignalTimeout: time.Minute, HeartbeatTimeout: 90 * time.Second, EstimateTimeout: 90 * time.Second}})
	snapshot, err := readStore.SnapshotByIssue(t.Context(), issueID)
	if err != nil {
		t.Fatal(err)
	}
	var implementation delivery.StageSnapshot
	for _, stage := range snapshot.Stages {
		if stage.StageKey == delivery.StageImplementation {
			implementation = stage
			break
		}
	}
	if implementation.SignalState != "awaiting_first_signal" || implementation.Stale || implementation.NeverSignaled {
		t.Fatalf("fresh claim anchored to queued time instead of started_at: %+v", implementation)
	}
	if updated, err := handlers.ReconcileStaleAgentRuns(t.Context(), startedAt.Add(time.Minute), cfg); err != nil || updated != 1 {
		t.Fatalf("exact first-signal boundary updated=%d err=%v, want one", updated, err)
	}
	var status string
	if err := db.DB.QueryRow(`SELECT status FROM agent_runs WHERE id=?`, runID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "failed" {
		t.Fatalf("run status=%q at exact first-signal boundary", status)
	}
}
