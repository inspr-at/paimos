package delivery

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

type countingDBTX struct {
	inner     DBTX
	queries   int
	queryRows int
	execs     int
}

func (c *countingDBTX) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	c.execs++
	return c.inner.ExecContext(ctx, query, args...)
}

func (c *countingDBTX) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	c.queries++
	return c.inner.QueryContext(ctx, query, args...)
}

func (c *countingDBTX) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	c.queryRows++
	return c.inner.QueryRowContext(ctx, query, args...)
}

func TestExternalAuthorityActivationCutoffResetAndReplay(t *testing.T) {
	database := openDeliveryTestDB(t)
	issueID, _, _ := seedDeliveryIssue(t, database)
	store := NewStore(database, Options{})
	attempt, err := store.StartAttempt(context.Background(), AttemptRequest{IssueID: issueID,
		Actor: Actor{Type: "user", OpaqueKey: "user:1"}, ReasonCode: "instrumentation", IdempotencyKey: "external-attempt"})
	if err != nil {
		t.Fatal(err)
	}
	a := Actor{Type: "external", OpaqueKey: "agent:a"}
	b := Actor{Type: "external", OpaqueKey: "agent:b"}
	stage, err := store.StartStageRetry(context.Background(), StageStartRequest{IssueID: issueID,
		AttemptNumber: attempt.AttemptNumber, StageKey: StageSpecification, Reporter: a,
		ReasonCode: "specification_start", IdempotencyKey: "external-start"})
	if err != nil {
		t.Fatal(err)
	}
	report := func(actor Actor, epoch, sequence int64, key, kind string) error {
		r := StageReport{IssueID: issueID, AttemptNumber: attempt.AttemptNumber, StageKey: StageSpecification,
			ExecutionNumber: stage.ExecutionNumber, AuthorityEpoch: epoch, Reporter: actor,
			IdempotencyKey: key, SourceSequence: &sequence, Kind: kind}
		switch kind {
		case "semantic":
			r.State, r.Activity = "active", key
		case "estimate":
			revision, progress, confidence := sequence, float64(sequence*10), .8
			r.Estimate = EstimateEvidence{Revision: &revision, Progress: &progress, Source: "external",
				Confidence: &confidence, Basis: "bounded estimate"}
		}
		_, err := store.ReportStage(context.Background(), r)
		return err
	}
	for seq, kind := range []string{"semantic", "heartbeat", "estimate"} {
		if err := report(a, 1, int64(seq+1), fmt.Sprintf("a-%s", kind), kind); err != nil {
			t.Fatal(err)
		}
	}
	toB, err := store.RecordHandoff(context.Background(), HandoffRequest{IssueID: issueID,
		AttemptNumber: attempt.AttemptNumber, StageKey: StageSpecification, ExecutionNumber: stage.ExecutionNumber,
		AuthorityEpoch: 1, From: a, To: b, ReasonCode: "handoff", ReasonText: "A to B", IdempotencyKey: "a-to-b"})
	if err != nil {
		t.Fatal(err)
	}
	if err := report(b, toB.AuthorityEpoch, 1, "b-semantic", "semantic"); err != nil {
		t.Fatal(err)
	}
	toARequest := HandoffRequest{IssueID: issueID, AttemptNumber: attempt.AttemptNumber,
		StageKey: StageSpecification, ExecutionNumber: stage.ExecutionNumber, AuthorityEpoch: toB.AuthorityEpoch,
		From: b, To: a, ReasonCode: "handoff", ReasonText: "B to A", IdempotencyKey: "b-to-a"}
	toA, err := store.RecordHandoff(context.Background(), toARequest)
	if err != nil {
		t.Fatal(err)
	}
	for seq, kind := range []string{"semantic", "heartbeat", "estimate"} {
		if err := report(a, toA.AuthorityEpoch, int64(seq+1), fmt.Sprintf("a-replay-%s", kind), kind); !errors.Is(err, ErrConflict) {
			t.Fatalf("pre-activation %s replay error=%v", kind, err)
		}
	}
	if err := report(a, toA.AuthorityEpoch, 4, "a-new-semantic", "semantic"); err != nil {
		t.Fatal(err)
	}
	if err := report(a, toA.AuthorityEpoch, 5, "a-new-heartbeat", "heartbeat"); err != nil {
		t.Fatal(err)
	}
	if err := report(a, toA.AuthorityEpoch, 6, "a-new-estimate", "estimate"); err != nil {
		t.Fatal(err)
	}
	resetRequest := ProgressResetRequest{IssueID: issueID, AttemptNumber: attempt.AttemptNumber,
		StageKey: StageSpecification, ExecutionNumber: stage.ExecutionNumber, AuthorityEpoch: toA.AuthorityEpoch,
		Actor: Actor{Type: "user", OpaqueKey: "user:1"}, ReasonCode: "scope_reset", ReasonText: "Reset estimate",
		IdempotencyKey: "external-reset"}
	reset, err := store.AuthorizeProgressReset(context.Background(), resetRequest)
	if err != nil {
		t.Fatal(err)
	}
	before := snapshotChangeSequence(t, database, issueID)
	resetReplay, err := store.AuthorizeProgressReset(context.Background(), resetRequest)
	if err != nil || !reflect.DeepEqual(reset, resetReplay) || snapshotChangeSequence(t, database, issueID) != before {
		t.Fatalf("reset replay first=%+v replay=%+v err=%v", reset, resetReplay, err)
	}
	snapshot, err := store.SnapshotByIssue(context.Background(), issueID)
	if err != nil {
		t.Fatal(err)
	}
	got := snapshotStage(snapshot.Stages, StageSpecification)
	if got.AuthorityActivationCutoff != 3 || got.LatestEstimate != nil || got.ProgressReset == nil ||
		got.ProgressReset.StageSourceSequenceCutoff != 6 {
		t.Fatalf("external cutoff/reset projection=%+v", got)
	}
	if err := report(a, toA.AuthorityEpoch, 7, "a-post-reset-estimate", "estimate"); err != nil {
		t.Fatal(err)
	}
	beforeRebuild, err := store.SnapshotByIssue(context.Background(), issueID)
	if err != nil {
		t.Fatal(err)
	}
	var pointerBefore int64
	var latestRowBefore string
	if err := database.QueryRow(`SELECT estimate_stage_event_id FROM delivery_stage_latest WHERE attempt_id=? AND stage_key=?`,
		attempt.ID, StageSpecification).Scan(&pointerBefore); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT json_array(delivery_id,attempt_id,stage_key,execution_number,authority_epoch,
		current_reporter_id,execution_start_stage_event_id,authority_stage_event_id,
		COALESCE(semantic_stage_event_id,0),COALESCE(heartbeat_stage_event_id,0),COALESCE(estimate_stage_event_id,0),updated_at)
		FROM delivery_stage_latest WHERE attempt_id=? AND stage_key=?`, attempt.ID, StageSpecification).Scan(&latestRowBefore); err != nil {
		t.Fatal(err)
	}
	if err := store.RebuildLatest(context.Background(), issueID); err != nil {
		t.Fatal(err)
	}
	var pointerAfter int64
	var latestRowAfter string
	if err := database.QueryRow(`SELECT estimate_stage_event_id FROM delivery_stage_latest WHERE attempt_id=? AND stage_key=?`,
		attempt.ID, StageSpecification).Scan(&pointerAfter); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT json_array(delivery_id,attempt_id,stage_key,execution_number,authority_epoch,
		current_reporter_id,execution_start_stage_event_id,authority_stage_event_id,
		COALESCE(semantic_stage_event_id,0),COALESCE(heartbeat_stage_event_id,0),COALESCE(estimate_stage_event_id,0),updated_at)
		FROM delivery_stage_latest WHERE attempt_id=? AND stage_key=?`, attempt.ID, StageSpecification).Scan(&latestRowAfter); err != nil {
		t.Fatal(err)
	}
	afterRebuild, _ := store.SnapshotByIssue(context.Background(), issueID)
	if pointerBefore != pointerAfter || latestRowBefore != latestRowAfter || !reflect.DeepEqual(beforeRebuild, afterRebuild) {
		t.Fatalf("rebuild mismatch pointer %d/%d latest=%s/%s\nbefore=%+v\nafter=%+v", pointerBefore, pointerAfter,
			latestRowBefore, latestRowAfter, beforeRebuild, afterRebuild)
	}

	// Exact operation replays return the immutable original ref even after a
	// later execution superseded the targeted execution.
	retryRequest := StageStartRequest{IssueID: issueID, AttemptNumber: attempt.AttemptNumber,
		StageKey: StageSpecification, Reporter: b, ReasonCode: "specification_retry", IdempotencyKey: "retry-one"}
	retryOne, err := store.StartStageRetry(context.Background(), retryRequest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartStageRetry(context.Background(), StageStartRequest{IssueID: issueID,
		AttemptNumber: attempt.AttemptNumber, StageKey: StageSpecification, Reporter: a,
		ReasonCode: "specification_retry", IdempotencyKey: "retry-two"}); err != nil {
		t.Fatal(err)
	}
	before = snapshotChangeSequence(t, database, issueID)
	retryReplay, err := store.StartStageRetry(context.Background(), retryRequest)
	if err != nil || !reflect.DeepEqual(retryOne, retryReplay) || snapshotChangeSequence(t, database, issueID) != before {
		t.Fatalf("stage-start replay first=%+v replay=%+v err=%v", retryOne, retryReplay, err)
	}
}

type runTelemetryFact struct {
	sequence      int64
	receivedAt    time.Time
	kind          string
	heartbeat     bool
	phase         string
	activity      string
	needsInput    bool
	blocker       string
	estimate      bool
	estimateValue float64
	confidence    *float64
}

func appendRunTelemetryFact(t *testing.T, database *sql.DB, store *Store, runID int64, fact runTelemetryFact) {
	t.Helper()
	if fact.phase == "" {
		fact.phase = "unknown"
	}
	if fact.blocker == "" {
		fact.blocker = "none"
	}
	stamp := formatTime(fact.receivedAt)
	var revision, progress, etaMin, etaMax, confidence any
	source, basis := "", ""
	if fact.estimate {
		value := .75
		if fact.confidence != nil {
			value = *fact.confidence
		}
		revision, progress, etaMin, etaMax, confidence = fact.sequence, fact.estimateValue, int64(60), int64(120), value
		source, basis = "agent", "bounded run estimate"
	}
	tx, err := database.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO agent_run_telemetry(run_id,sequence,correlation_id,provider,adapter,
		agent_reported_at,server_received_at,kind,heartbeat,phase,activity,needs_input,blocker_state,
		estimate_revision,progress_percent,eta_min_seconds,eta_max_seconds,estimate_source,estimate_confidence,estimate_basis)
		VALUES(?,?,'corr','test','test',?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, runID, fact.sequence, stamp, stamp,
		fact.kind, boolInt(fact.heartbeat), fact.phase, fact.activity, boolInt(fact.needsInput), fact.blocker,
		revision, progress, etaMin, etaMax, source, confidence, basis)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	var heartbeatAt, heartbeatID, semanticID, semanticAt, estimateID, estimateAt any
	if fact.heartbeat {
		heartbeatAt, heartbeatID = stamp, id
	}
	semantic := fact.kind == "phase" || fact.kind == "needs_input" || fact.kind == "blocker" ||
		fact.phase != "unknown" || fact.activity != "" || fact.needsInput || fact.blocker != "none"
	if semantic {
		semanticID, semanticAt = id, stamp
	}
	if fact.estimate {
		estimateID, estimateAt = id, stamp
	}
	if _, err := tx.Exec(`INSERT INTO agent_run_telemetry_latest(run_id,telemetry_id,sequence,last_heartbeat_at,
		heartbeat_telemetry_id,semantic_telemetry_id,estimate_telemetry_id,latest_event_at,latest_semantic_at,latest_estimate_at)
		VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(run_id) DO UPDATE SET telemetry_id=excluded.telemetry_id,
		sequence=excluded.sequence,last_heartbeat_at=COALESCE(excluded.last_heartbeat_at,last_heartbeat_at),
		heartbeat_telemetry_id=COALESCE(excluded.heartbeat_telemetry_id,heartbeat_telemetry_id),
		semantic_telemetry_id=COALESCE(excluded.semantic_telemetry_id,semantic_telemetry_id),
		estimate_telemetry_id=COALESCE(excluded.estimate_telemetry_id,estimate_telemetry_id),
		latest_event_at=excluded.latest_event_at,latest_semantic_at=COALESCE(excluded.latest_semantic_at,latest_semantic_at),
		latest_estimate_at=COALESCE(excluded.latest_estimate_at,latest_estimate_at)`, runID, id, fact.sequence,
		heartbeatAt, heartbeatID, semanticID, estimateID, stamp, semanticAt, estimateAt); err != nil {
		t.Fatal(err)
	}
	effects := store.NewEffects()
	if err := store.RecordRunTelemetryChangeTx(context.Background(), tx, effects, runID, fact.sequence); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	effects.Dispatch(context.Background())
}

func TestExternalHeartbeatCadenceFreshnessActivationAndEstimateRefresh(t *testing.T) {
	database := openDeliveryTestDB(t)
	issueID, _, _ := seedDeliveryIssue(t, database)
	now := time.Date(2026, 8, 20, 14, 0, 0, 0, time.UTC)
	store := NewStore(database, Options{Clock: ClockFunc(func() time.Time { return now }), Freshness: FreshnessPolicy{
		FirstSignalTimeout: time.Minute, HeartbeatTimeout: 90 * time.Second, EstimateTimeout: 90 * time.Second,
	}})
	attempt, err := store.StartAttempt(context.Background(), AttemptRequest{IssueID: issueID,
		Actor: Actor{Type: "user", OpaqueKey: "user:1"}, ReasonCode: "instrumentation", IdempotencyKey: "heartbeat-attempt"})
	if err != nil {
		t.Fatal(err)
	}
	a := Actor{Type: "external", OpaqueKey: "heartbeat:a"}
	b := Actor{Type: "external", OpaqueKey: "heartbeat:b"}
	stage, err := store.StartStageRetry(context.Background(), StageStartRequest{IssueID: issueID,
		AttemptNumber: attempt.AttemptNumber, StageKey: StageSpecification, Reporter: a,
		ReasonCode: "specification_start", IdempotencyKey: "heartbeat-start"})
	if err != nil {
		t.Fatal(err)
	}
	initial, _ := store.SnapshotByIssue(context.Background(), issueID)
	initialStage := snapshotStage(initial.Stages, StageSpecification)
	if initialStage.SignalState != "awaiting_first_signal" || initialStage.NextFreshnessTransitionAt == nil {
		t.Fatalf("initial freshness=%+v", initialStage)
	}
	now = now.Add(time.Minute)
	exactBoundary, _ := store.SnapshotByIssue(context.Background(), issueID)
	if got := snapshotStage(exactBoundary.Stages, StageSpecification); got.SignalState != "no_signal" || !got.NeverSignaled {
		t.Fatalf("inclusive first-signal boundary=%+v", got)
	}
	reportHeartbeat := func(sequence int64, key string) error {
		_, err := store.ReportStage(context.Background(), StageReport{IssueID: issueID,
			AttemptNumber: attempt.AttemptNumber, StageKey: StageSpecification, ExecutionNumber: stage.ExecutionNumber,
			AuthorityEpoch: stage.AuthorityEpoch, Reporter: a, IdempotencyKey: key,
			SourceSequence: &sequence, Kind: "heartbeat"})
		return err
	}
	beforeRevision, beforeChange := exactBoundary.DeliveryRevision, exactBoundary.ChangeSequence
	if err := reportHeartbeat(1, "heartbeat-first"); err != nil {
		t.Fatal(err)
	}
	afterFirst, _ := store.SnapshotByIssue(context.Background(), issueID)
	if afterFirst.DeliveryRevision != beforeRevision || afterFirst.ChangeSequence != beforeChange+1 {
		t.Fatalf("first heartbeat structural truth rev=%d/%d change=%d/%d", beforeRevision,
			afterFirst.DeliveryRevision, beforeChange, afterFirst.ChangeSequence)
	}
	for sequence := int64(2); sequence <= 101; sequence++ {
		now = now.Add(time.Second)
		if err := reportHeartbeat(sequence, fmt.Sprintf("heartbeat-%d", sequence)); err != nil {
			t.Fatal(err)
		}
	}
	afterCadence, _ := store.SnapshotByIssue(context.Background(), issueID)
	if afterCadence.DeliveryRevision != beforeRevision || afterCadence.ChangeSequence != afterFirst.ChangeSequence {
		t.Fatalf("100 cadence heartbeats changed structural truth: first=%+v cadence=%+v", afterFirst, afterCadence)
	}
	// Exactly 90 seconds after the most recent heartbeat is a recovery boundary.
	now = now.Add(90 * time.Second)
	if err := reportHeartbeat(102, "heartbeat-recovery"); err != nil {
		t.Fatal(err)
	}
	afterRecovery, _ := store.SnapshotByIssue(context.Background(), issueID)
	if afterRecovery.DeliveryRevision != beforeRevision || afterRecovery.ChangeSequence != afterFirst.ChangeSequence+1 {
		t.Fatalf("recovery boundary rev/change=%d/%d", afterRecovery.DeliveryRevision, afterRecovery.ChangeSequence)
	}
	beforeReplay := afterRecovery.ChangeSequence
	if err := reportHeartbeat(102, "heartbeat-recovery"); err != nil {
		t.Fatal(err)
	}
	if got := snapshotChangeSequence(t, database, issueID); got != beforeReplay {
		t.Fatalf("heartbeat replay emitted change: %d -> %d", beforeReplay, got)
	}

	// Estimate refresh keeps the semantic revision but reanchors freshness only
	// when every typed value is identical and the source sequence advances.
	revision, progress, confidence := int64(1), float64(40), .8
	estimate := StageReport{IssueID: issueID, AttemptNumber: attempt.AttemptNumber, StageKey: StageSpecification,
		ExecutionNumber: stage.ExecutionNumber, AuthorityEpoch: stage.AuthorityEpoch, Reporter: a,
		IdempotencyKey: "estimate-one", SourceSequence: int64ptr(103), Kind: "estimate",
		Estimate: EstimateEvidence{Revision: &revision, Progress: &progress, Source: "external",
			Confidence: &confidence, Basis: "same revision refresh"}}
	if _, err := store.ReportStage(context.Background(), estimate); err != nil {
		t.Fatal(err)
	}
	now = now.Add(10 * time.Second)
	estimate.IdempotencyKey, estimate.SourceSequence = "estimate-refresh", int64ptr(104)
	if _, err := store.ReportStage(context.Background(), estimate); err != nil {
		t.Fatal(err)
	}
	changed := estimate
	changed.IdempotencyKey, changed.SourceSequence = "estimate-changed", int64ptr(105)
	changedProgress := float64(41)
	changed.Estimate.Progress = &changedProgress
	if _, err := store.ReportStage(context.Background(), changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed same-revision estimate error=%v", err)
	}

	// A handoff gets a fresh first-signal grace anchored to this authority
	// activation—not the old execution start.
	handoffAt := now.Add(10 * time.Minute)
	now = handoffAt
	handed, err := store.RecordHandoff(context.Background(), HandoffRequest{IssueID: issueID,
		AttemptNumber: attempt.AttemptNumber, StageKey: StageSpecification, ExecutionNumber: stage.ExecutionNumber,
		AuthorityEpoch: stage.AuthorityEpoch, From: a, To: b, ReasonCode: "handoff", ReasonText: "Fresh owner",
		IdempotencyKey: "heartbeat-handoff"})
	if err != nil {
		t.Fatal(err)
	}
	stage.AuthorityEpoch = handed.AuthorityEpoch
	justAfterHandoff, _ := store.SnapshotByIssue(context.Background(), issueID)
	if got := snapshotStage(justAfterHandoff.Stages, StageSpecification); got.SignalState != "awaiting_first_signal" || got.Stale {
		t.Fatalf("new activation inherited old staleness: %+v", got)
	}
	now = handoffAt.Add(time.Minute)
	handoffBoundary, _ := store.SnapshotByIssue(context.Background(), issueID)
	if got := snapshotStage(handoffBoundary.Stages, StageSpecification); got.SignalState != "no_signal" {
		t.Fatalf("handoff inclusive boundary=%+v", got)
	}
}

func TestExternalPendingAndUnknownRemainFreshnessEligible(t *testing.T) {
	database := openDeliveryTestDB(t)
	issueID, _, _ := seedDeliveryIssue(t, database)
	now := time.Date(2026, 8, 20, 14, 30, 0, 0, time.UTC)
	store := NewStore(database, Options{Clock: ClockFunc(func() time.Time { return now }), Freshness: FreshnessPolicy{
		FirstSignalTimeout: time.Minute, HeartbeatTimeout: 90 * time.Second, EstimateTimeout: 90 * time.Second,
	}})
	attempt, err := store.StartAttempt(context.Background(), AttemptRequest{IssueID: issueID,
		Actor: Actor{Type: "user", OpaqueKey: "user:1"}, ReasonCode: "instrumentation", IdempotencyKey: "pending-attempt"})
	if err != nil {
		t.Fatal(err)
	}
	reporter := Actor{Type: "external", OpaqueKey: "pending:owner"}
	stage, err := store.StartStageRetry(context.Background(), StageStartRequest{IssueID: issueID,
		AttemptNumber: attempt.AttemptNumber, StageKey: StageSpecification, Reporter: reporter,
		ReasonCode: "specification_start", IdempotencyKey: "pending-start"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReportStage(context.Background(), StageReport{IssueID: issueID,
		AttemptNumber: attempt.AttemptNumber, StageKey: StageSpecification, ExecutionNumber: stage.ExecutionNumber,
		AuthorityEpoch: stage.AuthorityEpoch, Reporter: reporter, IdempotencyKey: "pending-semantic",
		SourceSequence: int64ptr(1), Kind: "semantic", State: "pending"}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	snapshot, err := store.SnapshotByIssue(context.Background(), issueID)
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshotStage(snapshot.Stages, StageSpecification); got.SignalState != "no_signal" || !got.NeverSignaled {
		t.Fatalf("pending semantic bypassed first-signal policy: %+v", got)
	}
	if _, err := store.ReportStage(context.Background(), StageReport{IssueID: issueID,
		AttemptNumber: attempt.AttemptNumber, StageKey: StageSpecification, ExecutionNumber: stage.ExecutionNumber,
		AuthorityEpoch: stage.AuthorityEpoch, Reporter: reporter, IdempotencyKey: "pending-heartbeat",
		SourceSequence: int64ptr(2), Kind: "heartbeat"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReportStage(context.Background(), StageReport{IssueID: issueID,
		AttemptNumber: attempt.AttemptNumber, StageKey: StageSpecification, ExecutionNumber: stage.ExecutionNumber,
		AuthorityEpoch: stage.AuthorityEpoch, Reporter: reporter, IdempotencyKey: "unknown-semantic",
		SourceSequence: int64ptr(3), Kind: "semantic", State: "unknown"}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(90 * time.Second)
	snapshot, err = store.SnapshotByIssue(context.Background(), issueID)
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshotStage(snapshot.Stages, StageSpecification); got.SignalState != "stale" || !got.HeartbeatStale || !got.Stale {
		t.Fatalf("unknown semantic bypassed heartbeat freshness: %+v", got)
	}
}

func TestRunZeroConfidenceEstimateIsIneligible(t *testing.T) {
	database := openDeliveryTestDB(t)
	issueID, projectID, userID := seedDeliveryIssue(t, database)
	now := time.Date(2026, 8, 20, 15, 0, 0, 0, time.UTC)
	store := NewStore(database, Options{Clock: ClockFunc(func() time.Time { return now })})
	result, _ := database.Exec(`INSERT INTO agent_runs(issue_id,project_id,requested_by,status,started_at,
		delivery_instrumentation_version) VALUES(?,?,?,'running',?,1)`, issueID, projectID, userID, formatTime(now))
	runID, _ := result.LastInsertId()
	tx, _ := database.BeginTx(context.Background(), nil)
	effects := store.NewEffects()
	if _, err := store.BootstrapRunTx(context.Background(), tx, effects, RunBootstrap{IssueID: issueID,
		RunID: runID, Mode: "implementation", Actor: Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", userID)},
		IdempotencyKey: "zero-confidence"}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	zero := 0.0
	now = now.Add(time.Second)
	appendRunTelemetryFact(t, database, store, runID, runTelemetryFact{sequence: 1, receivedAt: now,
		kind: "estimate", estimate: true, estimateValue: 20, confidence: &zero})
	snapshot, _ := store.SnapshotByIssue(context.Background(), issueID)
	if got := snapshotStage(snapshot.Stages, StageImplementation); got.LatestEstimate != nil || got.EstimateStale {
		t.Fatalf("zero-confidence run estimate entered delivery truth: %+v", got)
	}
	positive := .6
	now = now.Add(time.Second)
	appendRunTelemetryFact(t, database, store, runID, runTelemetryFact{sequence: 2, receivedAt: now,
		kind: "estimate", estimate: true, estimateValue: 25, confidence: &positive})
	snapshot, _ = store.SnapshotByIssue(context.Background(), issueID)
	if got := snapshotStage(snapshot.Stages, StageImplementation); got.LatestEstimate == nil || got.LatestEstimate.Confidence != positive {
		t.Fatalf("positive-confidence estimate missing: %+v", got)
	}
}

func TestInvalidLineageSuppressesRevokedRunTruthAndChanges(t *testing.T) {
	database := openDeliveryTestDB(t)
	issueID, projectID, userID := seedDeliveryIssue(t, database)
	now := time.Date(2026, 8, 20, 15, 30, 0, 0, time.UTC)
	store := NewStore(database, Options{Clock: ClockFunc(func() time.Time { return now })})
	result, err := database.Exec(`INSERT INTO agent_runs(issue_id,project_id,requested_by,status,started_at,
		expects_supervisor_telemetry,delivery_instrumentation_version) VALUES(?,?,?,'running',?,1,1)`,
		issueID, projectID, userID, formatTime(now))
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := result.LastInsertId()
	tx, _ := database.BeginTx(context.Background(), nil)
	effects := store.NewEffects()
	stage, err := store.BootstrapRunTx(context.Background(), tx, effects, RunBootstrap{IssueID: issueID,
		RunID: runID, Mode: "implementation", Actor: Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", userID)},
		IdempotencyKey: "lineage-bootstrap"})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	appendRunTelemetryFact(t, database, store, runID, runTelemetryFact{sequence: 1, receivedAt: now,
		kind: "phase", phase: "implementing", activity: "Old implementation"})
	appendRunTelemetryFact(t, database, store, runID, runTelemetryFact{sequence: 2, receivedAt: now,
		kind: "heartbeat", heartbeat: true})
	appendRunTelemetryFact(t, database, store, runID, runTelemetryFact{sequence: 3, receivedAt: now,
		kind: "estimate", estimate: true, estimateValue: 50})
	beforeRetry, err := store.SnapshotByIssue(context.Background(), issueID)
	if err != nil {
		t.Fatal(err)
	}
	if visible := snapshotStage(beforeRetry.Stages, StageImplementation); visible.OwnerRunID == nil ||
		visible.Activity == "" || visible.LastHeartbeatAt == nil || visible.LatestEstimate == nil {
		t.Fatalf("current run truth was not visible before lineage cutover: %+v", visible)
	}
	if _, err := store.StartStageRetry(context.Background(), StageStartRequest{IssueID: issueID,
		AttemptNumber: beforeRetry.AttemptNumber, StageKey: StageSpecification,
		Reporter: Actor{Type: "external", OpaqueKey: "spec:new"}, ReasonCode: "specification_retry",
		IdempotencyKey: "lineage-spec-retry"}); err != nil {
		t.Fatal(err)
	}
	invalidated, err := store.SnapshotByIssue(context.Background(), issueID)
	if err != nil {
		t.Fatal(err)
	}
	implementation := snapshotStage(invalidated.Stages, StageImplementation)
	if implementation.CurrentLineage || implementation.ExecutionNumber != stage.ExecutionNumber ||
		implementation.ReporterType != "" || implementation.SemanticState != "pending" || implementation.Activity != "" ||
		implementation.OwnerRunID != nil || implementation.LastHeartbeatAt != nil || implementation.LastSemanticAt != nil ||
		implementation.LatestEstimate != nil || implementation.SignalState != "" || len(implementation.CurrentBlockers) != 0 ||
		len(implementation.Evidence) != 0 {
		t.Fatalf("invalid-lineage source truth leaked into current snapshot: %+v", implementation)
	}
	bulk, err := store.BulkSnapshots(context.Background(), []int64{issueID})
	if err != nil || !reflect.DeepEqual(invalidated, bulk[0]) {
		t.Fatalf("single/bulk invalid-lineage mismatch err=%v single=%+v bulk=%+v", err, invalidated, bulk)
	}
	sequenceBefore := invalidated.ChangeSequence
	appendRunTelemetryFact(t, database, store, runID, runTelemetryFact{sequence: 4, receivedAt: now,
		kind: "phase", phase: "testing", activity: "Revoked telemetry"})
	afterOldTelemetry, err := store.SnapshotByIssue(context.Background(), issueID)
	if err != nil {
		t.Fatal(err)
	}
	if afterOldTelemetry.ChangeSequence != sequenceBefore || !reflect.DeepEqual(invalidated, afterOldTelemetry) {
		t.Fatalf("revoked telemetry changed invalid-lineage snapshot:\nbefore=%+v\nafter=%+v", invalidated, afterOldTelemetry)
	}
}

func TestSnapshotRejectsUnlinkedActiveInstrumentedRun(t *testing.T) {
	database := openDeliveryTestDB(t)
	issueID, projectID, userID := seedDeliveryIssue(t, database)
	result, err := database.Exec(`INSERT INTO agent_runs(issue_id,project_id,requested_by,status,
		delivery_instrumentation_version) VALUES(?,?,?,'queued',1)`, issueID, projectID, userID)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := result.LastInsertId()
	store := NewStore(database, Options{})
	if _, err := store.SnapshotByIssue(context.Background(), issueID); !errors.Is(err, ErrInvariant) {
		t.Fatalf("single snapshot hid active unlinked instrumented run: %v", err)
	}
	if _, err := store.BulkSnapshots(context.Background(), []int64{issueID}); !errors.Is(err, ErrInvariant) {
		t.Fatalf("bulk snapshot hid active unlinked instrumented run: %v", err)
	}
	if _, err := database.Exec(`UPDATE agent_runs SET status='failed' WHERE id=?`, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SnapshotByIssue(context.Background(), issueID); !errors.Is(err, ErrInvariant) {
		t.Fatalf("terminal unlinked instrumented run was hidden: %v", err)
	}
	if _, err := database.Exec(`DELETE FROM agent_runs WHERE id=?`, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO agent_runs(issue_id,project_id,requested_by,status,
		delivery_instrumentation_version) VALUES(?,?,?,'failed',0)`, issueID, projectID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SnapshotByIssue(context.Background(), issueID); err != nil {
		t.Fatalf("version-zero terminal compatibility row poisoned snapshot: %v", err)
	}
}

func TestPersistedDeliveryValuesRejectSecretLikeMaterial(t *testing.T) {
	database := openDeliveryTestDB(t)
	issueID, _, _ := seedDeliveryIssue(t, database)
	store := NewStore(database, Options{})
	policies := DefaultPolicy()
	policies[0].PolicyReference = "sk-live-abcdefghijklmnopqrstuvwxyz"
	if _, err := store.StartAttempt(context.Background(), AttemptRequest{IssueID: issueID,
		Actor: Actor{Type: "user", OpaqueKey: "user:1"}, Policies: policies, ReasonCode: "instrumentation",
		IdempotencyKey: "secret-policy"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("secret-like policy reference error=%v", err)
	}
	attempt, err := store.StartAttempt(context.Background(), AttemptRequest{IssueID: issueID,
		Actor: Actor{Type: "user", OpaqueKey: "user:1"}, ReasonCode: "instrumentation", IdempotencyKey: "safe-attempt"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartStageRetry(context.Background(), StageStartRequest{IssueID: issueID,
		AttemptNumber: attempt.AttemptNumber, StageKey: StageSpecification,
		Reporter:   Actor{Type: "external", OpaqueKey: "ghp_123456789012345678901234567890"},
		ReasonCode: "specification_start", IdempotencyKey: "secret-reporter"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("secret-like reporter error=%v", err)
	}
	if _, err := store.StartStageRetry(context.Background(), StageStartRequest{IssueID: issueID,
		AttemptNumber: attempt.AttemptNumber, StageKey: StageSpecification,
		Reporter: Actor{Type: "external", OpaqueKey: "safe:reporter"}, ReasonCode: "specification_start",
		IdempotencyKey: "github_pat_123456789012345678901234567890"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("secret-like idempotency key error=%v", err)
	}
	reporter := Actor{Type: "external", OpaqueKey: "safe:reporter"}
	stage, err := store.StartStageRetry(context.Background(), StageStartRequest{IssueID: issueID,
		AttemptNumber: attempt.AttemptNumber, StageKey: StageSpecification, Reporter: reporter,
		ReasonCode: "specification_start", IdempotencyKey: "safe-stage"})
	if err != nil {
		t.Fatal(err)
	}
	before := snapshotChangeSequence(t, database, issueID)
	secretReports := []StageReport{
		{IssueID: issueID, AttemptNumber: attempt.AttemptNumber, StageKey: StageSpecification,
			ExecutionNumber: stage.ExecutionNumber, AuthorityEpoch: stage.AuthorityEpoch, Reporter: reporter,
			IdempotencyKey: "secret-blocker", SourceSequence: int64ptr(1), Kind: "semantic", State: "waiting",
			NeedsInput: true, Blockers: []Blocker{{Key: "token:abcdefghijklmnop", Class: "input", HumanWait: true}}},
		{IssueID: issueID, AttemptNumber: attempt.AttemptNumber, StageKey: StageSpecification,
			ExecutionNumber: stage.ExecutionNumber, AuthorityEpoch: stage.AuthorityEpoch, Reporter: reporter,
			IdempotencyKey: "secret-evidence", SourceSequence: int64ptr(1), Kind: "semantic", State: "succeeded",
			Evidence: []Evidence{{Type: "spec_acceptance", Outcome: "passed", ReferenceKind: "external_ref",
				ReferenceValue: "xoxb-12345678901234567890"}}},
		{IssueID: issueID, AttemptNumber: attempt.AttemptNumber, StageKey: StageSpecification,
			ExecutionNumber: stage.ExecutionNumber, AuthorityEpoch: stage.AuthorityEpoch, Reporter: reporter,
			IdempotencyKey: "secret-estimate", SourceSequence: int64ptr(1), Kind: "estimate",
			Estimate: EstimateEvidence{Revision: int64ptr(1), Progress: testFloat64Ptr(10), Source: "external",
				Confidence: testFloat64Ptr(.8), Basis: "api_key=abcdefghijklmnop"}},
	}
	for _, report := range secretReports {
		if _, err := store.ReportStage(context.Background(), report); !errors.Is(err, ErrInvalid) {
			t.Fatalf("secret-like report %q error=%v", report.IdempotencyKey, err)
		}
	}
	if got := snapshotChangeSequence(t, database, issueID); got != before {
		t.Fatalf("secret-like rejected reports changed delivery sequence: %d -> %d", before, got)
	}
	var secretReporterRows int
	if err := database.QueryRow(`SELECT COUNT(*) FROM delivery_reporters WHERE opaque_key LIKE 'ghp_%'`).Scan(&secretReporterRows); err != nil || secretReporterRows != 0 {
		t.Fatalf("secret-like reporter persisted count=%d err=%v", secretReporterRows, err)
	}
}

func TestDeliveryPrivacyBackstopMatchesStoreValidation(t *testing.T) {
	database := openDeliveryTestDB(t)
	issueID, _, _ := seedDeliveryIssue(t, database)
	store := NewStore(database, Options{})
	values := []string{
		"nul\x00value", "carriage\rreturn", "line\nfeed",
		"https://user@example.com/proof", "https://example.com/proof?token=redacted",
		"api_key=abcdefghijklmnop", "token:abcdefghijklmnop", "secret/abcdefghijklmnop",
		"password_abcdefghijklmnop", "credential-abcdefghijklmnop", "AKIA1234567890ABCDEF",
		"AIza123456789012345678901234", "ghp_123456789012345678901234567890",
		"gho_123456789012345678901234567890", "ghu_123456789012345678901234567890",
		"ghs_123456789012345678901234567890", "ghr_123456789012345678901234567890",
		"github_pat_123456789012345678901234567890",
		"xoxb-12345678901234567890", "xoxa-12345678901234567890", "xoxp-12345678901234567890",
		"xoxr-12345678901234567890", "xoxs-12345678901234567890",
		"eyJabcdefgh.abcdefgh.abcdefgh",
		"-----BEGIN PRIVATE KEY-----",
		"token /abcd efgh", "Bearer abcd efgh",
	}
	for _, separator := range []string{" ", "\t", "\f", "\v", "\r", "\n", "\u00a0", "\u2003", "\u202f", "\u200b"} {
		values = append(values,
			"ToKeN"+separator+"=redacted",
			"api_key"+separator+":"+separator+"redacted",
			"password"+separator+"=🔐",
			"Bearer"+separator+"abcdefgh")
	}
	for index, value := range values {
		_, err := store.StartAttempt(context.Background(), AttemptRequest{IssueID: issueID,
			Actor: Actor{Type: "external", OpaqueKey: value}, ReasonCode: "instrumentation",
			IdempotencyKey: fmt.Sprintf("privacy-store-%d", index)})
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("Store accepted forbidden value %q: %v", value, err)
		}
	}
	attempt, err := store.StartAttempt(context.Background(), AttemptRequest{IssueID: issueID,
		Actor: Actor{Type: "user", OpaqueKey: "user:1"}, ReasonCode: "instrumentation", IdempotencyKey: "privacy-safe"})
	if err != nil || attempt.DeliveryID == 0 {
		t.Fatalf("safe attempt=%+v err=%v", attempt, err)
	}
	for _, value := range values {
		if _, err := database.Exec(`INSERT INTO delivery_reporters(delivery_id,reporter_type,opaque_key,created_at)
			VALUES(?,'external',?,'2026-08-20T16:00:00Z')`, attempt.DeliveryID, value); err == nil {
			t.Fatalf("direct SQL accepted forbidden value %q", value)
		}
	}
	for _, reference := range []string{"https://user@example.com/proof", "https://example.com/proof?signature=redacted"} {
		if err := validateReferenceValue(reference, 192); !errors.Is(err, ErrInvalid) {
			t.Fatalf("reference validator accepted unsafe URL %q: %v", reference, err)
		}
	}
	allowedNearThresholds := []string{
		"token/abcdefg", "sk-live-abcdefg", "ghp_1234567890123456789", "xoxb-123456789",
		"AIza1234567890123456789", "eyJabcdefg.abcdefg.abcdefg", "AKIA1234567890ABCDEFG",
		"aiza12345678901234567890",
	}
	for index, value := range allowedNearThresholds {
		if err := rejectSecretLike(value); err != nil {
			t.Fatalf("Store over-rejected near-threshold value %q: %v", value, err)
		}
		if _, err := database.Exec(`INSERT INTO delivery_reporters(delivery_id,reporter_type,opaque_key,created_at)
			VALUES(?,'external',?,'2026-08-20T16:01:00Z')`, attempt.DeliveryID, value); err != nil {
			t.Fatalf("direct SQL over-rejected near-threshold value %d %q: %v", index, value, err)
		}
	}
}

func TestReporterScopedIdempotencyNamespacesDoNotCollide(t *testing.T) {
	database := openDeliveryTestDB(t)
	issueID, sourceProjectID, _ := seedDeliveryIssue(t, database)
	wakes := 0
	store := NewStore(database, Options{Observer: func(context.Context, ChangeHint) { wakes++ }})
	request := AttemptRequest{IssueID: issueID, Actor: Actor{Type: "user", OpaqueKey: "user:1"},
		ReasonCode: "instrumentation", IdempotencyKey: "actor-bound-attempt"}
	attempt, err := store.StartAttempt(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	request.Actor.OpaqueKey = "user:2"
	secondAttempt, err := store.StartAttempt(context.Background(), request)
	if err != nil || secondAttempt.AttemptNumber != attempt.AttemptNumber+1 {
		t.Fatalf("cross-reporter idempotency namespace collided: attempt=%+v err=%v", secondAttempt, err)
	}
	attempt = secondAttempt

	reporter := Actor{Type: "external", OpaqueKey: "reset:owner"}
	stage, err := store.StartStageRetry(context.Background(), StageStartRequest{IssueID: issueID,
		AttemptNumber: attempt.AttemptNumber, StageKey: StageSpecification, Reporter: reporter,
		ReasonCode: "specification_start", IdempotencyKey: "actor-bound-stage"})
	if err != nil {
		t.Fatal(err)
	}
	reset := ProgressResetRequest{IssueID: issueID, AttemptNumber: attempt.AttemptNumber,
		StageKey: StageSpecification, ExecutionNumber: stage.ExecutionNumber, AuthorityEpoch: stage.AuthorityEpoch,
		Actor: Actor{Type: "user", OpaqueKey: "user:1"}, ReasonCode: "scope_reset", ReasonText: "Reset progress",
		IdempotencyKey: "actor-bound-reset"}
	if _, err := store.AuthorizeProgressReset(context.Background(), reset); err != nil {
		t.Fatal(err)
	}
	reset.Actor.OpaqueKey = "user:2"
	if _, err := store.AuthorizeProgressReset(context.Background(), reset); err != nil {
		t.Fatalf("cross-reporter reset namespace collided: %v", err)
	}

	project, err := database.Exec(`INSERT INTO projects(name,key) VALUES('Actor move target','AMT')`)
	if err != nil {
		t.Fatal(err)
	}
	targetProjectID, _ := project.LastInsertId()
	tx, err := database.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE issues SET project_id=? WHERE id=?`, targetProjectID, issueID); err != nil {
		t.Fatal(err)
	}
	effects := store.NewEffects()
	if err := store.ProjectMoveTx(context.Background(), tx, effects, issueID, sourceProjectID, targetProjectID,
		Actor{Type: "user", OpaqueKey: "user:1"}, "actor-bound-move"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	effects.Dispatch(context.Background())
	tx, err = database.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	effects = store.NewEffects()
	err = store.ProjectMoveTx(context.Background(), tx, effects, issueID, sourceProjectID, targetProjectID,
		Actor{Type: "user", OpaqueKey: "user:2"}, "actor-bound-move")
	if err != nil {
		t.Fatalf("cross-reporter project-move namespace collided: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	effects.Dispatch(context.Background())
}

func TestLinkAgentRunTxReauthorizesFreshAndExactReplay(t *testing.T) {
	database := openDeliveryTestDB(t)
	issueID, projectID, userID := seedDeliveryIssue(t, database)
	allowed := true
	store := NewStore(database, Options{Authorizer: AuthorizerFunc(func(_ context.Context, req AuthorizationRequest) error {
		if !allowed {
			return fmt.Errorf("project edit access revoked for %s", req.Action)
		}
		return nil
	})})
	result, err := database.Exec(`INSERT INTO agent_runs(issue_id,project_id,requested_by,status,
		delivery_instrumentation_version) VALUES(?,?,?,'queued',1)`, issueID, projectID, userID)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := result.LastInsertId()
	tx, err := database.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	effects := store.NewEffects()
	stage, err := store.BootstrapRunTx(context.Background(), tx, effects, RunBootstrap{IssueID: issueID,
		RunID: runID, Mode: "implementation", Actor: Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", userID)},
		IdempotencyKey: "link-authorization"})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	effects.Dispatch(context.Background())
	var eventsBefore, changesBefore, reportersBefore int
	if err := database.QueryRow(`SELECT COUNT(*) FROM delivery_events`).Scan(&eventsBefore); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM delivery_change_log`).Scan(&changesBefore); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM delivery_reporters`).Scan(&reportersBefore); err != nil {
		t.Fatal(err)
	}

	allowed = false
	tx, err = database.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	effects = store.NewEffects()
	if err := store.LinkAgentRunTx(context.Background(), tx, effects, issueID, runID, stage,
		"link-authorization:link"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("exact link replay after authorization revocation error=%v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}

	result, err = database.Exec(`INSERT INTO agent_runs(issue_id,project_id,requested_by,status,
		delivery_instrumentation_version) VALUES(?,?,?,'failed',1)`, issueID, projectID, userID)
	if err != nil {
		t.Fatal(err)
	}
	freshRunID, _ := result.LastInsertId()
	tx, err = database.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	effects = store.NewEffects()
	if err := store.LinkAgentRunTx(context.Background(), tx, effects, issueID, freshRunID, stage,
		"fresh-link-after-revocation"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("fresh link after authorization revocation error=%v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var eventsAfter, changesAfter, reportersAfter, freshReporter int
	if err := database.QueryRow(`SELECT COUNT(*) FROM delivery_events`).Scan(&eventsAfter); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM delivery_change_log`).Scan(&changesAfter); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM delivery_reporters`).Scan(&reportersAfter); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM delivery_reporters WHERE opaque_key=?`,
		fmt.Sprintf("run:%d", freshRunID)).Scan(&freshReporter); err != nil {
		t.Fatal(err)
	}
	if eventsAfter != eventsBefore || changesAfter != changesBefore || reportersAfter != reportersBefore || freshReporter != 0 {
		t.Fatalf("denied link leaked mutation events=%d/%d changes=%d/%d reporters=%d/%d fresh=%d",
			eventsAfter, eventsBefore, changesAfter, changesBefore, reportersAfter, reportersBefore, freshReporter)
	}
}

func TestServerIdempotencyKeysCannotBePreemptedAcrossReporterNamespace(t *testing.T) {
	database := openDeliveryTestDB(t)
	issueID, sourceProjectID, userID := seedDeliveryIssue(t, database)
	store := NewStore(database, Options{})
	attempt, err := store.StartAttempt(context.Background(), AttemptRequest{IssueID: issueID,
		Actor: Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", userID)}, ReasonCode: "instrumentation",
		IdempotencyKey: "namespace-attempt"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := database.Exec(`INSERT INTO delivery_reporters(delivery_id,reporter_type,opaque_key,created_at)
		VALUES(?,'external','external:preemptor','2026-08-20T17:00:00Z')`, attempt.DeliveryID)
	if err != nil {
		t.Fatal(err)
	}
	preemptorID, _ := result.LastInsertId()
	preempt := func(kind, key string) {
		t.Helper()
		var revision int64
		if err := database.QueryRow(`SELECT COALESCE(MAX(delivery_revision),0)+1 FROM delivery_events
			WHERE delivery_id=?`, attempt.DeliveryID).Scan(&revision); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`INSERT INTO delivery_events(delivery_id,delivery_revision,idempotency_key,
			payload_hash,kind,reporter_id,server_received_at) VALUES(?,?,?,zeroblob(32),?,?,
			'2026-08-20T17:00:00Z')`, attempt.DeliveryID, revision, key, kind, preemptorID); err != nil {
			t.Fatalf("seed external preemption %s/%s: %v", kind, key, err)
		}
	}

	preempt("attempt_started", "spec-revision:2")
	if _, err := database.Exec(`UPDATE issues SET description='Revised specification namespace' WHERE id=?`, issueID); err != nil {
		t.Fatalf("external reporter preempted system specification event: %v", err)
	}
	preempt("project_moved", "server-move")
	project, err := database.Exec(`INSERT INTO projects(name,key) VALUES('Namespace target','NST')`)
	if err != nil {
		t.Fatal(err)
	}
	targetProjectID, _ := project.LastInsertId()
	tx, err := database.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE issues SET project_id=? WHERE id=?`, targetProjectID, issueID); err != nil {
		t.Fatal(err)
	}
	effects := store.NewEffects()
	if err := store.ProjectMoveTx(context.Background(), tx, effects, issueID, sourceProjectID, targetProjectID,
		Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", userID)}, "server-move"); err != nil {
		t.Fatalf("external reporter preempted project move: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	effects.Dispatch(context.Background())

	preempt("run_linked", "server-run:link")
	result, err = database.Exec(`INSERT INTO agent_runs(issue_id,project_id,requested_by,status,
		delivery_instrumentation_version) VALUES(?,?,?,'queued',1)`, issueID, targetProjectID, userID)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := result.LastInsertId()
	tx, err = database.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	effects = store.NewEffects()
	linked, err := store.BootstrapRunTx(context.Background(), tx, effects, RunBootstrap{IssueID: issueID,
		RunID: runID, Mode: "implementation", Actor: Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", userID)},
		IdempotencyKey: "server-run"})
	if err != nil {
		t.Fatalf("external reporter preempted run bootstrap/link: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	effects.Dispatch(context.Background())

	lifecycleKey := fmt.Sprintf("run-lifecycle:%d:failed", runID)
	preempt("run_lifecycle_observed", lifecycleKey)
	if _, err := store.StartStageRetry(context.Background(), StageStartRequest{IssueID: issueID,
		AttemptNumber: linked.AttemptNumber, StageKey: StageImplementation,
		Reporter:   Actor{Type: "external", OpaqueKey: "replacement:implementation"},
		ReasonCode: "implementation_retry", IdempotencyKey: "namespace-supersede"}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE agent_runs SET status='failed',finished_at='2026-08-20T17:01:00Z'
		WHERE id=?`, runID); err != nil {
		t.Fatal(err)
	}
	tx, err = database.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	effects = store.NewEffects()
	if err := store.NormalizeRunTx(context.Background(), tx, effects, RunNormalization{RunID: runID,
		Status: "failed", IdempotencyKey: "namespace-late-normalize"}); err != nil {
		t.Fatalf("external reporter preempted run lifecycle observation: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	effects.Dispatch(context.Background())

	for _, identity := range []struct{ kind, key string }{
		{"attempt_started", "spec-revision:2"}, {"project_moved", "server-move"},
		{"run_linked", "server-run:link"}, {"run_lifecycle_observed", lifecycleKey},
	} {
		var reporters int
		if err := database.QueryRow(`SELECT COUNT(DISTINCT reporter_id) FROM delivery_events
			WHERE delivery_id=? AND kind=? AND idempotency_key=?`, attempt.DeliveryID, identity.kind, identity.key).
			Scan(&reporters); err != nil {
			t.Fatal(err)
		}
		if reporters != 2 {
			t.Fatalf("%s/%s reporters=%d, expected external preemptor plus canonical actor",
				identity.kind, identity.key, reporters)
		}
	}
}

func TestUnsealedPartialAttemptCannotBecomeCurrentTruth(t *testing.T) {
	database := openDeliveryTestDB(t)
	issueID, _, _ := seedDeliveryIssue(t, database)
	store := NewStore(database, Options{})
	sealed, err := store.StartAttempt(context.Background(), AttemptRequest{IssueID: issueID,
		Actor: Actor{Type: "user", OpaqueKey: "user:1"}, ReasonCode: "instrumentation",
		IdempotencyKey: "sealed-attempt"})
	if err != nil {
		t.Fatal(err)
	}
	var reporterID, revision int64
	if err := database.QueryRow(`SELECT id FROM delivery_reporters WHERE delivery_id=? AND reporter_type='user'
		AND opaque_key='user:1'`, sealed.DeliveryID).Scan(&reporterID); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COALESCE(MAX(delivery_revision),0)+1 FROM delivery_events
		WHERE delivery_id=?`, sealed.DeliveryID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	result, err := database.Exec(`INSERT INTO delivery_events(delivery_id,delivery_revision,idempotency_key,
		payload_hash,kind,reporter_id,server_received_at) VALUES(?,?,'partial-attempt',zeroblob(32),
		'attempt_started',?,'2026-08-20T17:30:00Z')`, sealed.DeliveryID, revision, reporterID)
	if err != nil {
		t.Fatal(err)
	}
	eventID, _ := result.LastInsertId()
	result, err = database.Exec(`INSERT INTO delivery_attempts(delivery_id,attempt_number,plan_revision,
		previous_attempt_id,start_delivery_event_id,reason_code,created_at)
		VALUES(?,2,2,?,?,'policy_change','2026-08-20T17:30:00Z')`, sealed.DeliveryID, sealed.ID, eventID)
	if err != nil {
		t.Fatal(err)
	}
	partialID, _ := result.LastInsertId()
	for index, stage := range CanonicalStages[:4] {
		if _, err := database.Exec(`INSERT INTO delivery_attempt_stage_policy(delivery_id,attempt_id,stage_key,
			sort_order,applicability,weight,created_at) VALUES(?,?,?,?,'required',?,'2026-08-20T17:30:00Z')`,
			sealed.DeliveryID, partialID, stage, index+1, DefaultWeights[stage]); err != nil {
			t.Fatal(err)
		}
	}
	before, err := store.SnapshotByIssue(context.Background(), issueID)
	if err != nil {
		t.Fatal(err)
	}
	if before.AttemptNumber != sealed.AttemptNumber {
		t.Fatalf("unsealed partial attempt became current: %+v", before)
	}
	history, err := store.AttemptHistory(context.Background(), issueID)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].ID != sealed.ID {
		t.Fatalf("unsealed partial attempt leaked into history: %+v", history)
	}
	if err := store.RebuildLatest(context.Background(), issueID); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewStore(database, Options{}).SnapshotByIssue(context.Background(), issueID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, restarted) {
		t.Fatalf("partial attempt changed rebuild/restart truth:\nbefore=%+v\nafter=%+v", before, restarted)
	}
}

func TestDirectSQLMonotonicResetAndDurationGuards(t *testing.T) {
	database := openDeliveryTestDB(t)
	issueID, projectID, _ := seedDeliveryIssue(t, database)
	now := time.Date(2026, 8, 20, 18, 0, 0, 0, time.UTC)
	store := NewStore(database, Options{Clock: ClockFunc(func() time.Time { return now })})
	attempt, err := store.StartAttempt(context.Background(), AttemptRequest{IssueID: issueID,
		Actor: Actor{Type: "user", OpaqueKey: "user:1"}, ReasonCode: "instrumentation", IdempotencyKey: "guard-attempt"})
	if err != nil {
		t.Fatal(err)
	}
	reporter := Actor{Type: "external", OpaqueKey: "guard:owner"}
	stage, err := store.StartStageRetry(context.Background(), StageStartRequest{IssueID: issueID,
		AttemptNumber: attempt.AttemptNumber, StageKey: StageSpecification, Reporter: reporter,
		ReasonCode: "specification_start", IdempotencyKey: "guard-start"})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	baseEstimate := EstimateEvidence{Revision: int64ptr(2), Progress: testFloat64Ptr(25), Source: "external",
		Confidence: testFloat64Ptr(.8), Basis: "bounded observation"}
	if _, err := store.ReportStage(context.Background(), StageReport{IssueID: issueID,
		AttemptNumber: attempt.AttemptNumber, StageKey: StageSpecification, ExecutionNumber: stage.ExecutionNumber,
		AuthorityEpoch: stage.AuthorityEpoch, Reporter: reporter, IdempotencyKey: "guard-estimate-10",
		SourceSequence: int64ptr(10), Kind: "estimate", Estimate: baseEstimate}); err != nil {
		t.Fatal(err)
	}
	appendEnvelope := func(key, kind string, reporterID int64) int64 {
		t.Helper()
		var revision int64
		if err := database.QueryRow(`SELECT COALESCE(MAX(delivery_revision),0)+1 FROM delivery_events
			WHERE delivery_id=?`, stage.DeliveryID).Scan(&revision); err != nil {
			t.Fatal(err)
		}
		result, err := database.Exec(`INSERT INTO delivery_events(delivery_id,delivery_revision,idempotency_key,
			payload_hash,kind,reporter_id,server_received_at) VALUES(?,?,?,zeroblob(32),?,?,'2026-08-20T18:00:02Z')`,
			stage.DeliveryID, revision, key, kind, reporterID)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := result.LastInsertId()
		return id
	}
	insertEstimate := func(key string, sequence, revision int64, progress float64, basis string) error {
		eventID := appendEnvelope(key, "stage_reported", stage.ReporterID)
		var eventSequence int64
		if err := database.QueryRow(`SELECT COALESCE(MAX(event_sequence),0)+1 FROM delivery_stage_events
			WHERE attempt_id=? AND stage_key=? AND execution_number=?`, stage.AttemptID, stage.StageKey,
			stage.ExecutionNumber).Scan(&eventSequence); err != nil {
			t.Fatal(err)
		}
		_, err := database.Exec(`INSERT INTO delivery_stage_events(delivery_id,attempt_id,stage_key,execution_number,
			event_sequence,authority_epoch,delivery_event_id,event_type,reporter_id,execution_start_stage_event_id,
			source_sequence,estimate_revision,progress_percent,estimate_source,estimate_confidence,estimate_basis,
			server_received_at) VALUES(?,?,?,?,?,?,?,'estimate',?,?,?,?,?,'external',.8,?,'2026-08-20T18:00:02Z')`,
			stage.DeliveryID, stage.AttemptID, stage.StageKey, stage.ExecutionNumber, eventSequence,
			stage.AuthorityEpoch, eventID, stage.ReporterID, stage.ExecutionStartEventID, sequence, revision, progress, basis)
		return err
	}
	if err := insertEstimate("guard-seq-regress", 9, 3, 25, "bounded observation"); err == nil {
		t.Fatal("external source sequence regressed from 10 to 9")
	}
	if err := insertEstimate("guard-rev-regress", 11, 1, 25, "bounded observation"); err == nil {
		t.Fatal("estimate revision regressed from 2 to 1")
	}
	if err := insertEstimate("guard-rev-changed", 11, 2, 26, "bounded observation"); err == nil {
		t.Fatal("changed same-revision estimate was accepted")
	}
	if err := insertEstimate("guard-rev-refresh", 11, 2, 25, "bounded observation"); err != nil {
		t.Fatalf("field-exact same-revision refresh rejected: %v", err)
	}

	now = now.Add(time.Second)
	reset, err := store.AuthorizeProgressReset(context.Background(), ProgressResetRequest{IssueID: issueID,
		AttemptNumber: attempt.AttemptNumber, StageKey: StageSpecification, ExecutionNumber: stage.ExecutionNumber,
		AuthorityEpoch: stage.AuthorityEpoch, Actor: Actor{Type: "user", OpaqueKey: "user:1"},
		ReasonCode: "guard_reset", ReasonText: "Reset guarded progress", IdempotencyKey: "guard-reset"})
	if err != nil {
		t.Fatal(err)
	}
	var resetID, authorityID, actorID int64
	if err := database.QueryRow(`SELECT authority_stage_event_id FROM delivery_stage_latest
		WHERE attempt_id=? AND stage_key=?`, stage.AttemptID, stage.StageKey).Scan(&resetID); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT id FROM delivery_stage_events WHERE attempt_id=? AND stage_key=?
		AND execution_number=? AND event_type='execution_started'`, stage.AttemptID, stage.StageKey,
		stage.ExecutionNumber).Scan(&authorityID); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT id FROM delivery_reporters WHERE delivery_id=? AND reporter_type='user'
		AND opaque_key='user:1'`, stage.DeliveryID).Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	insertReset := func(key string, epoch, cutoff int64) error {
		eventID := appendEnvelope(key, "progress_reset_authorized", actorID)
		var eventSequence int64
		if err := database.QueryRow(`SELECT COALESCE(MAX(event_sequence),0)+1 FROM delivery_stage_events
			WHERE attempt_id=? AND stage_key=? AND execution_number=?`, stage.AttemptID, stage.StageKey,
			stage.ExecutionNumber).Scan(&eventSequence); err != nil {
			t.Fatal(err)
		}
		_, err := database.Exec(`INSERT INTO delivery_stage_events(delivery_id,attempt_id,stage_key,execution_number,
			event_sequence,authority_epoch,delivery_event_id,event_type,reporter_id,execution_start_stage_event_id,
			previous_stage_event_id,reset_epoch,reset_source_cutoff,reset_source_kind,
			reset_authority_anchor_stage_event_id,reset_owner_reporter_id,reason_code,reason_text,server_received_at)
			VALUES(?,?,?,?,?,?,?,'progress_reset_authorized',?,?,?,?,?,'stage_events',?,?,
			'guard_reset','Reset guarded progress','2026-08-20T18:00:03Z')`, stage.DeliveryID, stage.AttemptID,
			stage.StageKey, stage.ExecutionNumber, eventSequence, stage.AuthorityEpoch, eventID, actorID,
			stage.ExecutionStartEventID, resetID, epoch, cutoff, authorityID, stage.ReporterID)
		return err
	}
	if err := insertReset("guard-reset-low", 2, 10); err == nil {
		t.Fatal("reset below exact source high-water was accepted")
	}
	if err := insertReset("guard-reset-epoch", 3, 11); err == nil {
		t.Fatal("non-monotone reset epoch was accepted")
	}
	if err := insertReset("guard-reset-valid", 2, 11); err != nil {
		t.Fatalf("exact reset boundary rejected: %v (first reset=%+v)", err, reset)
	}

	now = now.Add(time.Second)
	retry, err := store.StartStageRetry(context.Background(), StageStartRequest{IssueID: issueID,
		AttemptNumber: attempt.AttemptNumber, StageKey: StageSpecification, Reporter: reporter,
		ReasonCode: "specification_retry", IdempotencyKey: "guard-retry"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO delivery_stage_durations(stage_execution_id,terminal_stage_event_id,
		delivery_id,root_issue_id,attempt_id,stage_key,execution_number,project_id_at_completion,
		estimator_policy_version,full_lead_seconds,active_seconds,blocked_seconds,human_wait_seconds,completed_at)
		VALUES(?,?,?,?,?,? ,?,?,1,0,0,0,0,'2026-08-20T18:00:04Z')`, retry.ExecutionStartEventID,
		retry.ExecutionStartEventID, retry.DeliveryID, issueID, retry.AttemptID, retry.StageKey,
		retry.ExecutionNumber, projectID); err == nil {
		t.Fatal("pending execution produced a duration sample")
	}
	digest, err := canonicalIssueSpecDigest(context.Background(), database, issueID)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	if _, err := store.ReportStage(context.Background(), StageReport{IssueID: issueID,
		AttemptNumber: attempt.AttemptNumber, StageKey: StageSpecification, ExecutionNumber: retry.ExecutionNumber,
		AuthorityEpoch: retry.AuthorityEpoch, Reporter: reporter, IdempotencyKey: "guard-success", SourceSequence: int64ptr(12),
		Kind: "semantic", State: "succeeded", Evidence: []Evidence{{Type: "approval", Outcome: "passed",
			ReferenceKind: "digest", DigestSHA256: digest}}}); err != nil {
		t.Fatal(err)
	}
	var durations int
	if err := database.QueryRow(`SELECT COUNT(*) FROM delivery_stage_durations WHERE stage_execution_id=?
		AND terminal_stage_event_id<>stage_execution_id`, retry.ExecutionStartEventID).Scan(&durations); err != nil || durations != 1 {
		t.Fatalf("valid exact duration count=%d err=%v", durations, err)
	}
	if _, err := database.Exec(`INSERT INTO delivery_stage_durations(stage_execution_id,terminal_stage_event_id,
		delivery_id,root_issue_id,attempt_id,stage_key,execution_number,project_id_at_completion,
		estimator_policy_version,full_lead_seconds,active_seconds,blocked_seconds,human_wait_seconds,completed_at)
		SELECT stage_execution_id,terminal_stage_event_id,delivery_id,root_issue_id,attempt_id,stage_key,
		execution_number,project_id_at_completion,estimator_policy_version,full_lead_seconds,active_seconds,
		blocked_seconds,human_wait_seconds,completed_at FROM delivery_stage_durations
		WHERE stage_execution_id=?`, retry.ExecutionStartEventID); err == nil {
		t.Fatal("successful execution accepted a second duration sample")
	}
}

func TestLateSupersededRunOutcomeIsHistoryOnly(t *testing.T) {
	for _, supersession := range []string{"retry", "upstream_retry", "spec_edit", "project_move"} {
		for _, outcome := range []string{"tests_passed", "failed", "cancelled"} {
			t.Run(supersession+"_"+outcome, func(t *testing.T) {
				database := openDeliveryTestDB(t)
				issueID, projectID, userID := seedDeliveryIssue(t, database)
				now := time.Date(2026, 8, 20, 17, 0, 0, 0, time.UTC)
				wakes := 0
				store := NewStore(database, Options{Clock: ClockFunc(func() time.Time { return now }),
					Observer: func(context.Context, ChangeHint) { wakes++ }})
				result, err := database.Exec(`INSERT INTO agent_runs(issue_id,project_id,requested_by,status,started_at,
					delivery_instrumentation_version) VALUES(?,?,?,'running',?,1)`, issueID, projectID, userID, formatTime(now))
				if err != nil {
					t.Fatal(err)
				}
				runID, _ := result.LastInsertId()
				tx, _ := database.BeginTx(context.Background(), nil)
				effects := store.NewEffects()
				linkedStage, err := store.BootstrapRunTx(context.Background(), tx, effects, RunBootstrap{IssueID: issueID,
					RunID: runID, Mode: "implementation", Actor: Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", userID)},
					IdempotencyKey: "late-bootstrap"})
				if err != nil {
					t.Fatal(err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
				effects.Dispatch(context.Background())
				switch supersession {
				case "retry":
					if _, err := store.StartStageRetry(context.Background(), StageStartRequest{IssueID: issueID,
						AttemptNumber: linkedStage.AttemptNumber, StageKey: StageImplementation,
						Reporter: Actor{Type: "external", OpaqueKey: "replacement:owner"}, ReasonCode: "implementation_retry",
						IdempotencyKey: "late-retry"}); err != nil {
						t.Fatal(err)
					}
				case "upstream_retry":
					if _, err := store.StartStageRetry(context.Background(), StageStartRequest{IssueID: issueID,
						AttemptNumber: linkedStage.AttemptNumber, StageKey: StageSpecification,
						Reporter: Actor{Type: "external", OpaqueKey: "replacement:specification"}, ReasonCode: "specification_retry",
						IdempotencyKey: "late-upstream-retry"}); err != nil {
						t.Fatal(err)
					}
				case "spec_edit":
					if _, err := database.Exec(`UPDATE issues SET description='Revised canonical specification' WHERE id=?`, issueID); err != nil {
						t.Fatal(err)
					}
				case "project_move":
					project, err := database.Exec(`INSERT INTO projects(name,key) VALUES('Late target','LTT')`)
					if err != nil {
						t.Fatal(err)
					}
					targetProjectID, _ := project.LastInsertId()
					tx, _ := database.BeginTx(context.Background(), nil)
					if _, err := tx.Exec(`UPDATE issues SET project_id=? WHERE id=?`, targetProjectID, issueID); err != nil {
						t.Fatal(err)
					}
					effects := store.NewEffects()
					if err := store.ProjectMoveTx(context.Background(), tx, effects, issueID, projectID, targetProjectID,
						Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", userID)}, "late-project-move"); err != nil {
						t.Fatal(err)
					}
					if err := tx.Commit(); err != nil {
						t.Fatal(err)
					}
					effects.Dispatch(context.Background())
				}
				before, err := store.SnapshotByIssue(context.Background(), issueID)
				if err != nil {
					t.Fatal(err)
				}
				sequenceBefore, wakesBefore := before.ChangeSequence, wakes
				tx, _ = database.BeginTx(context.Background(), nil)
				commit := ""
				if outcome == "tests_passed" {
					commit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
				}
				if _, err := tx.Exec(`UPDATE agent_runs SET status=?,commit_sha=?,finished_at=? WHERE id=?`,
					outcome, commit, formatTime(now.Add(time.Minute)), runID); err != nil {
					t.Fatal(err)
				}
				effects = store.NewEffects()
				if err := store.NormalizeRunTx(context.Background(), tx, effects, RunNormalization{RunID: runID,
					Status: outcome, IdempotencyKey: "late-terminal"}); err != nil {
					t.Fatal(err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
				effects.Dispatch(context.Background())
				after, err := store.SnapshotByIssue(context.Background(), issueID)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(before.Stages, after.Stages) || after.DeliveryRevision != before.DeliveryRevision+1 ||
					after.ChangeSequence != sequenceBefore+1 || wakes != wakesBefore+1 {
					t.Fatalf("late outcome altered current truth or invalidated incorrectly:\nbefore=%+v\nafter=%+v wakes=%d/%d",
						before, after, wakesBefore, wakes)
				}
				history, err := store.AttemptHistory(context.Background(), issueID)
				if err != nil {
					t.Fatal(err)
				}
				found := false
				var lifecycleEventID int64
				for _, attempt := range history {
					if attempt.ID != linkedStage.AttemptID {
						continue
					}
					for _, linked := range attempt.LinkedRuns {
						if linked.RunID == runID && linked.StageKey == StageImplementation &&
							linked.ExecutionNumber == linkedStage.ExecutionNumber && linked.Status == outcome && linked.FinishedAt != nil &&
							linked.LifecycleEventID != nil {
							found = true
							lifecycleEventID = *linked.LifecycleEventID
						}
					}
				}
				if !found {
					t.Fatalf("late terminal run outcome missing from bounded attempt history: %+v", history)
				}
				var eventKind string
				if err := database.QueryRow(`SELECT kind FROM delivery_events WHERE id=?`, lifecycleEventID).Scan(&eventKind); err != nil {
					t.Fatal(err)
				}
				if eventKind != "run_lifecycle_observed" {
					t.Fatalf("late lifecycle event kind=%q", eventKind)
				}

				// A direct reconciler/transport replay may use a different transport
				// key. Immutable run+status identity still makes it an exact no-op.
				tx, _ = database.BeginTx(context.Background(), nil)
				effects = store.NewEffects()
				if err := store.NormalizeRunTx(context.Background(), tx, effects, RunNormalization{RunID: runID,
					Status: outcome, IdempotencyKey: "late-terminal-replay"}); err != nil {
					t.Fatal(err)
				}
				if len(effects.Hints()) != 0 {
					t.Fatalf("exact late replay queued hints: %+v", effects.Hints())
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
				effects.Dispatch(context.Background())
				replayed, err := store.SnapshotByIssue(context.Background(), issueID)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(after, replayed) || wakes != wakesBefore+1 {
					t.Fatalf("late replay mutated delivery: after=%+v replayed=%+v wakes=%d/%d",
						after, replayed, wakesBefore+1, wakes)
				}
				replayedHistory, err := store.AttemptHistory(context.Background(), issueID)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(history, replayedHistory) {
					t.Fatalf("late replay changed history:\nfirst=%+v\nreplay=%+v", history, replayedHistory)
				}
			})
		}
	}
}

func assertNoActorReplayMutation(t *testing.T, database *sql.DB, issueID int64, reporters int, sequence int64, wakesBefore, wakesAfter int) {
	t.Helper()
	var gotReporters int
	if err := database.QueryRow(`SELECT COUNT(*) FROM delivery_reporters`).Scan(&gotReporters); err != nil {
		t.Fatal(err)
	}
	if gotReporters != reporters || snapshotChangeSequence(t, database, issueID) != sequence || wakesAfter != wakesBefore {
		t.Fatalf("cross-actor replay mutated state reporters=%d/%d sequence=%d/%d wakes=%d/%d", gotReporters,
			reporters, snapshotChangeSequence(t, database, issueID), sequence, wakesAfter, wakesBefore)
	}
}

func testFloat64Ptr(v float64) *float64 { return &v }

func TestRebuildPointerQueriesPropagateCancellation(t *testing.T) {
	database := openDeliveryTestDB(t)
	issueID, _, _ := seedDeliveryIssue(t, database)
	store := NewStore(database, Options{})
	attempt, err := store.StartAttempt(context.Background(), AttemptRequest{IssueID: issueID,
		Actor: Actor{Type: "user", OpaqueKey: "user:1"}, ReasonCode: "instrumentation", IdempotencyKey: "rebuild-cancel"})
	if err != nil {
		t.Fatal(err)
	}
	stage, err := store.StartStageRetry(context.Background(), StageStartRequest{IssueID: issueID,
		AttemptNumber: attempt.AttemptNumber, StageKey: StageSpecification,
		Reporter: Actor{Type: "external", OpaqueKey: "rebuild:owner"}, ReasonCode: "specification_start",
		IdempotencyKey: "rebuild-cancel-stage"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	key := stageExecutionKey{attempt: attempt.ID, execution: stage.ExecutionNumber, stage: StageSpecification}
	if _, _, err := latestStageEventID(ctx, database, key, stage.AuthorityEpoch, stage.ReporterID, []string{"heartbeat"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("latest stage query swallowed cancellation: %v", err)
	}
	if _, _, err := latestEstimateStageEventID(ctx, database, key, stage.AuthorityEpoch, stage.ReporterID); !errors.Is(err, context.Canceled) {
		t.Fatalf("latest estimate query swallowed cancellation: %v", err)
	}
}

func TestAgentRunActivationTelemetryDurationHeartbeatAndRetention(t *testing.T) {
	database := openDeliveryTestDB(t)
	issueID, projectID, userID := seedDeliveryIssue(t, database)
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	wakes := 0
	store := NewStore(database, Options{Clock: ClockFunc(func() time.Time { return now }),
		Freshness: FreshnessPolicy{FirstSignalTimeout: time.Minute, HeartbeatTimeout: 90 * time.Second, EstimateTimeout: 90 * time.Second},
		Observer:  func(context.Context, ChangeHint) { wakes++ }})
	result, err := database.Exec(`INSERT INTO agent_runs(issue_id,project_id,requested_by,status,started_at,
		expects_supervisor_telemetry,delivery_instrumentation_version) VALUES(?,?,?,'running',?,1,1)`,
		issueID, projectID, userID, formatTime(now))
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := result.LastInsertId()
	tx, _ := database.BeginTx(context.Background(), nil)
	effects := store.NewEffects()
	stage, err := store.BootstrapRunTx(context.Background(), tx, effects, RunBootstrap{IssueID: issueID,
		RunID: runID, Mode: "implementation", Actor: Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", userID)},
		IdempotencyKey: "run-bootstrap"})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	effects.Dispatch(context.Background())

	now = now.Add(10 * time.Second)
	appendRunTelemetryFact(t, database, store, runID, runTelemetryFact{sequence: 1, receivedAt: now,
		kind: "needs_input", phase: "waiting", activity: "Awaiting approval", needsInput: true, blocker: "input"})
	now = now.Add(20 * time.Second)
	appendRunTelemetryFact(t, database, store, runID, runTelemetryFact{sequence: 2, receivedAt: now,
		kind: "phase", phase: "implementing", activity: "Implementing"})
	now = now.Add(10 * time.Second)
	appendRunTelemetryFact(t, database, store, runID, runTelemetryFact{sequence: 3, receivedAt: now,
		kind: "blocker", phase: "waiting", activity: "Dependency", blocker: "dependency"})
	now = now.Add(10 * time.Second)
	appendRunTelemetryFact(t, database, store, runID, runTelemetryFact{sequence: 4, receivedAt: now,
		kind: "phase", phase: "testing", activity: "Testing"})
	beforeBeat := snapshotChangeSequence(t, database, issueID)
	now = now.Add(5 * time.Second)
	appendRunTelemetryFact(t, database, store, runID, runTelemetryFact{sequence: 5, receivedAt: now,
		kind: "heartbeat", heartbeat: true})
	firstBeat := snapshotChangeSequence(t, database, issueID)
	if firstBeat != beforeBeat+1 {
		t.Fatalf("first heartbeat change=%d -> %d", beforeBeat, firstBeat)
	}
	now = now.Add(15 * time.Second)
	appendRunTelemetryFact(t, database, store, runID, runTelemetryFact{sequence: 6, receivedAt: now,
		kind: "heartbeat", heartbeat: true})
	if got := snapshotChangeSequence(t, database, issueID); got != firstBeat {
		t.Fatalf("cadence heartbeat spammed change log: %d -> %d", firstBeat, got)
	}
	now = now.Add(90 * time.Second) // exactly at the inclusive stale boundary from the latest heartbeat.
	appendRunTelemetryFact(t, database, store, runID, runTelemetryFact{sequence: 7, receivedAt: now,
		kind: "heartbeat", heartbeat: true})
	if got := snapshotChangeSequence(t, database, issueID); got != firstBeat+1 {
		t.Fatalf("inclusive recovery boundary did not emit: got=%d want=%d", got, firstBeat+1)
	}
	now = now.Add(time.Second)
	appendRunTelemetryFact(t, database, store, runID, runTelemetryFact{sequence: 8, receivedAt: now,
		kind: "estimate", estimate: true, estimateValue: 55})
	snapshot, err := store.SnapshotByIssue(context.Background(), issueID)
	if err != nil {
		t.Fatal(err)
	}
	implementation := snapshotStage(snapshot.Stages, StageImplementation)
	if implementation.OwnerRunID == nil || *implementation.OwnerRunID != runID || implementation.Phase != "testing" ||
		implementation.Activity != "Testing" || implementation.LatestEstimate == nil || implementation.LastHeartbeatAt == nil {
		t.Fatalf("independent run facts did not merge: %+v", implementation)
	}

	external := Actor{Type: "external", OpaqueKey: "replacement"}
	away, err := store.RecordHandoff(context.Background(), HandoffRequest{IssueID: issueID,
		AttemptNumber: snapshot.AttemptNumber, StageKey: StageImplementation, ExecutionNumber: stage.ExecutionNumber,
		AuthorityEpoch: stage.AuthorityEpoch, From: Actor{Type: "agent_run", OpaqueKey: fmt.Sprintf("run:%d", runID)},
		To: external, ReasonCode: "handoff", ReasonText: "Run to external", IdempotencyKey: "run-away"})
	if err != nil {
		t.Fatal(err)
	}
	changeBeforeRevoked := snapshotChangeSequence(t, database, issueID)
	now = now.Add(time.Second)
	appendRunTelemetryFact(t, database, store, runID, runTelemetryFact{sequence: 9, receivedAt: now,
		kind: "phase", phase: "reviewing", activity: "Revoked activity"})
	if got := snapshotChangeSequence(t, database, issueID); got != changeBeforeRevoked {
		t.Fatalf("revoked telemetry emitted delivery invalidation: %d -> %d", changeBeforeRevoked, got)
	}
	back, err := store.RecordHandoff(context.Background(), HandoffRequest{IssueID: issueID,
		AttemptNumber: snapshot.AttemptNumber, StageKey: StageImplementation, ExecutionNumber: stage.ExecutionNumber,
		AuthorityEpoch: away.AuthorityEpoch, From: external,
		To: Actor{Type: "agent_run", OpaqueKey: fmt.Sprintf("run:%d", runID)}, ReasonCode: "handoff", ReasonText: "External to run", IdempotencyKey: "run-back"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _ = store.SnapshotByIssue(context.Background(), issueID)
	implementation = snapshotStage(snapshot.Stages, StageImplementation)
	if back.AuthorityEpoch != 3 || implementation.AuthorityActivationCutoff != 9 || implementation.LastSemanticAt != nil ||
		implementation.LastHeartbeatAt != nil || implementation.LatestEstimate != nil {
		t.Fatalf("away/back resurrected pre-activation telemetry: %+v", implementation)
	}
	now = now.Add(time.Second)
	appendRunTelemetryFact(t, database, store, runID, runTelemetryFact{sequence: 10, receivedAt: now,
		kind: "phase", phase: "reviewing", activity: "Current activity"})
	now = now.Add(time.Second)
	appendRunTelemetryFact(t, database, store, runID, runTelemetryFact{sequence: 11, receivedAt: now,
		kind: "heartbeat", heartbeat: true})
	now = now.Add(time.Second)
	appendRunTelemetryFact(t, database, store, runID, runTelemetryFact{sequence: 12, receivedAt: now,
		kind: "estimate", estimate: true, estimateValue: 80})

	reset, err := store.AuthorizeProgressReset(context.Background(), ProgressResetRequest{IssueID: issueID,
		AttemptNumber: snapshot.AttemptNumber, StageKey: StageImplementation, ExecutionNumber: stage.ExecutionNumber,
		AuthorityEpoch: back.AuthorityEpoch, Actor: Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", userID)},
		ReasonCode: "scope_reset", ReasonText: "Reset estimate", IdempotencyKey: "run-reset"})
	if err != nil || reset.ReporterID == 0 {
		t.Fatalf("run reset=%+v err=%v", reset, err)
	}
	snapshot, _ = store.SnapshotByIssue(context.Background(), issueID)
	if snapshotStage(snapshot.Stages, StageImplementation).LatestEstimate != nil {
		t.Fatal("run reset did not mask the pre-cutoff estimate")
	}
	now = now.Add(time.Second)
	appendRunTelemetryFact(t, database, store, runID, runTelemetryFact{sequence: 13, receivedAt: now,
		kind: "estimate", estimate: true, estimateValue: 90})

	// A terminal linked run cannot regain authority after a handoff.
	away, err = store.RecordHandoff(context.Background(), HandoffRequest{IssueID: issueID,
		AttemptNumber: snapshot.AttemptNumber, StageKey: StageImplementation, ExecutionNumber: stage.ExecutionNumber,
		AuthorityEpoch: back.AuthorityEpoch, From: Actor{Type: "agent_run", OpaqueKey: fmt.Sprintf("run:%d", runID)},
		To: external, ReasonCode: "handoff", ReasonText: "Run to external", IdempotencyKey: "run-away-terminal"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE agent_runs SET status='failed' WHERE id=?`, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordHandoff(context.Background(), HandoffRequest{IssueID: issueID,
		AttemptNumber: snapshot.AttemptNumber, StageKey: StageImplementation, ExecutionNumber: stage.ExecutionNumber,
		AuthorityEpoch: away.AuthorityEpoch, From: external,
		To: Actor{Type: "agent_run", OpaqueKey: fmt.Sprintf("run:%d", runID)}, ReasonCode: "handoff", ReasonText: "Terminal run cannot return",
		IdempotencyKey: "terminal-run-back"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("terminal run reactivation error=%v", err)
	}

	// Retention never regresses the durable per-delivery cursor high-water.
	beforeRetention, _ := store.SnapshotByIssue(context.Background(), issueID)
	var maxID int64
	if err := database.QueryRow(`SELECT MAX(id) FROM delivery_change_log`).Scan(&maxID); err != nil {
		t.Fatal(err)
	}
	if err := store.RetainChangesThrough(context.Background(), maxID); err != nil {
		t.Fatal(err)
	}
	if err := store.RetainChangesThrough(context.Background(), maxID); err != nil {
		t.Fatalf("exact retention retry after tail prune failed: %v", err)
	}
	restarted := NewStore(database, Options{Clock: ClockFunc(func() time.Time { return now })})
	if err := restarted.RetainChangesThrough(context.Background(), maxID); err != nil {
		t.Fatalf("exact retention retry after store restart failed: %v", err)
	}
	afterRetention, _ := store.SnapshotByIssue(context.Background(), issueID)
	if afterRetention.ChangeSequence != beforeRetention.ChangeSequence {
		t.Fatalf("retention regressed snapshot sequence: %d -> %d", beforeRetention.ChangeSequence, afterRetention.ChangeSequence)
	}
	tx, _ = database.BeginTx(context.Background(), nil)
	effects = store.NewEffects()
	if err := store.RecordRunMutationChangeTx(context.Background(), tx, effects, runID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	afterAppend, _ := store.SnapshotByIssue(context.Background(), issueID)
	if afterAppend.ChangeSequence != beforeRetention.ChangeSequence+1 {
		t.Fatalf("post-retention sequence=%d want=%d", afterAppend.ChangeSequence, beforeRetention.ChangeSequence+1)
	}
}

func TestDurationIncludesPostActivationAgentRunWaits(t *testing.T) {
	database := openDeliveryTestDB(t)
	issueID, projectID, userID := seedDeliveryIssue(t, database)
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	store := NewStore(database, Options{Clock: ClockFunc(func() time.Time { return now })})
	result, _ := database.Exec(`INSERT INTO agent_runs(issue_id,project_id,requested_by,status,started_at,
		delivery_instrumentation_version) VALUES(?,?,?,'running',?,1)`, issueID, projectID, userID, formatTime(now))
	runID, _ := result.LastInsertId()
	tx, _ := database.BeginTx(context.Background(), nil)
	effects := store.NewEffects()
	stage, err := store.BootstrapRunTx(context.Background(), tx, effects, RunBootstrap{IssueID: issueID, RunID: runID,
		Mode: "implementation", Actor: Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", userID)}, IdempotencyKey: "duration-run"})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	now = now.Add(10 * time.Second)
	appendRunTelemetryFact(t, database, store, runID, runTelemetryFact{sequence: 1, receivedAt: now,
		kind: "needs_input", phase: "waiting", needsInput: true, blocker: "input"})
	now = now.Add(20 * time.Second)
	appendRunTelemetryFact(t, database, store, runID, runTelemetryFact{sequence: 2, receivedAt: now,
		kind: "phase", phase: "implementing"})
	now = now.Add(10 * time.Second)
	appendRunTelemetryFact(t, database, store, runID, runTelemetryFact{sequence: 3, receivedAt: now,
		kind: "blocker", phase: "waiting", blocker: "dependency"})
	now = now.Add(10 * time.Second)
	appendRunTelemetryFact(t, database, store, runID, runTelemetryFact{sequence: 4, receivedAt: now,
		kind: "phase", phase: "testing"})
	now = now.Add(10 * time.Second)
	commit := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	tx, _ = database.BeginTx(context.Background(), nil)
	if _, err := tx.Exec(`UPDATE agent_runs SET status='tests_passed',commit_sha=? WHERE id=?`, commit, runID); err != nil {
		t.Fatal(err)
	}
	effects = store.NewEffects()
	if err := store.NormalizeRunTx(context.Background(), tx, effects, RunNormalization{RunID: runID,
		Status: "tests_passed", IdempotencyKey: "duration-terminal"}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var full, active, blocked, human int64
	if err := database.QueryRow(`SELECT full_lead_seconds,active_seconds,blocked_seconds,human_wait_seconds
		FROM delivery_stage_durations WHERE stage_execution_id=?`, stage.ExecutionStartEventID).
		Scan(&full, &active, &blocked, &human); err != nil {
		t.Fatal(err)
	}
	if full != 60 || active != 30 || blocked != 10 || human != 20 {
		t.Fatalf("duration full=%d active=%d blocked=%d human=%d", full, active, blocked, human)
	}
}

func TestDurationRoundingAndHandoffBlockerClosure(t *testing.T) {
	base := time.Date(2026, 8, 20, 16, 0, 0, 0, time.UTC)
	active, blocked, human, err := roundedDurationSeconds(2400*time.Millisecond, []durationInterval{
		{start: base, end: base.Add(800 * time.Millisecond), human: true},
		{start: base.Add(400 * time.Millisecond), end: base.Add(1600 * time.Millisecond)},
	})
	if err != nil || active != 0 || blocked != 1 || human != 1 || active+blocked+human != 2 {
		t.Fatalf("largest-remainder rounding active=%d blocked=%d human=%d err=%v", active, blocked, human, err)
	}

	for _, tc := range []struct {
		name       string
		blocker    Blocker
		needsInput bool
		wantActive int64
		wantBlock  int64
		wantHuman  int64
	}{
		{name: "dependency", blocker: Blocker{Key: "dependency", Class: "dependency"}, wantActive: 20, wantBlock: 10},
		{name: "human", blocker: Blocker{Key: "approval", Class: "input", HumanWait: true}, needsInput: true, wantActive: 20, wantHuman: 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			database := openDeliveryTestDB(t)
			issueID, _, _ := seedDeliveryIssue(t, database)
			now := base
			store := NewStore(database, Options{Clock: ClockFunc(func() time.Time { return now })})
			attempt, err := store.StartAttempt(context.Background(), AttemptRequest{IssueID: issueID,
				Actor: Actor{Type: "user", OpaqueKey: "user:1"}, ReasonCode: "instrumentation",
				IdempotencyKey: "handoff-duration-attempt"})
			if err != nil {
				t.Fatal(err)
			}
			a := Actor{Type: "external", OpaqueKey: "duration:a"}
			b := Actor{Type: "external", OpaqueKey: "duration:b"}
			stage, err := store.StartStageRetry(context.Background(), StageStartRequest{IssueID: issueID,
				AttemptNumber: attempt.AttemptNumber, StageKey: StageSpecification, Reporter: a,
				ReasonCode: "specification_start", IdempotencyKey: "handoff-duration-start"})
			if err != nil {
				t.Fatal(err)
			}
			now = base.Add(10 * time.Second)
			if _, err := store.ReportStage(context.Background(), StageReport{IssueID: issueID,
				AttemptNumber: attempt.AttemptNumber, StageKey: StageSpecification, ExecutionNumber: stage.ExecutionNumber,
				AuthorityEpoch: stage.AuthorityEpoch, Reporter: a, IdempotencyKey: "handoff-duration-wait",
				SourceSequence: int64ptr(1), Kind: "semantic", State: "waiting", NeedsInput: tc.needsInput,
				Blockers: []Blocker{tc.blocker}}); err != nil {
				t.Fatal(err)
			}
			now = base.Add(20 * time.Second)
			handed, err := store.RecordHandoff(context.Background(), HandoffRequest{IssueID: issueID,
				AttemptNumber: attempt.AttemptNumber, StageKey: StageSpecification, ExecutionNumber: stage.ExecutionNumber,
				AuthorityEpoch: stage.AuthorityEpoch, From: a, To: b, ReasonCode: "handoff",
				ReasonText: "Transfer stage", IdempotencyKey: "handoff-duration-transfer"})
			if err != nil {
				t.Fatal(err)
			}
			digest, err := canonicalIssueSpecDigest(context.Background(), database, issueID)
			if err != nil {
				t.Fatal(err)
			}
			now = base.Add(30 * time.Second)
			if _, err := store.ReportStage(context.Background(), StageReport{IssueID: issueID,
				AttemptNumber: attempt.AttemptNumber, StageKey: StageSpecification, ExecutionNumber: stage.ExecutionNumber,
				AuthorityEpoch: handed.AuthorityEpoch, Reporter: b, IdempotencyKey: "handoff-duration-success",
				SourceSequence: int64ptr(1), Kind: "semantic", State: "succeeded",
				Evidence: []Evidence{{Type: "spec_acceptance", Outcome: "passed", ReferenceKind: "digest", DigestSHA256: digest}}}); err != nil {
				t.Fatal(err)
			}
			var full, gotActive, gotBlocked, gotHuman int64
			if err := database.QueryRow(`SELECT full_lead_seconds,active_seconds,blocked_seconds,human_wait_seconds
				FROM delivery_stage_durations WHERE stage_execution_id=?`, stage.ExecutionStartEventID).
				Scan(&full, &gotActive, &gotBlocked, &gotHuman); err != nil {
				t.Fatal(err)
			}
			if full != 30 || gotActive != tc.wantActive || gotBlocked != tc.wantBlock || gotHuman != tc.wantHuman ||
				gotActive+gotBlocked+gotHuman != full {
				t.Fatalf("handoff duration full=%d active=%d blocked=%d human=%d", full, gotActive, gotBlocked, gotHuman)
			}
		})
	}
}

func TestBulkSnapshotsUsesOneCalculatedAtAndSupportsOneThousand(t *testing.T) {
	database := openDeliveryTestDB(t)
	issueID, projectID, userID := seedDeliveryIssue(t, database)
	ids := []int64{issueID}
	for i := 2; i <= 1000; i++ {
		result, err := database.Exec(`INSERT INTO issues(project_id,issue_number,title) VALUES(?,?,?)`, projectID, i, fmt.Sprintf("Issue %d", i))
		if err != nil {
			t.Fatal(err)
		}
		id, _ := result.LastInsertId()
		ids = append(ids, id)
	}
	broken, err := database.Exec(`INSERT INTO agent_runs(issue_id,project_id,requested_by,status,
		delivery_instrumentation_version) VALUES(?,?,?,'queued',1)`, ids[len(ids)-1], projectID, userID)
	if err != nil {
		t.Fatal(err)
	}
	brokenID, _ := broken.LastInsertId()
	clockCalls := 0
	store := NewStore(database, Options{Clock: ClockFunc(func() time.Time {
		clockCalls++
		return time.Date(2026, 8, 20, 12, 0, clockCalls, 0, time.UTC)
	})})
	if _, err := store.BulkSnapshots(context.Background(), ids); !errors.Is(err, ErrInvariant) {
		t.Fatalf("1,000-root bulk hid unlinked active instrumented run: %v", err)
	}
	if _, err := database.Exec(`DELETE FROM agent_runs WHERE id=?`, brokenID); err != nil {
		t.Fatal(err)
	}
	for _, size := range []int{1, 10, 100, 1000} {
		clockCalls = 0
		counted := &countingDBTX{inner: database}
		snapshots, err := store.BulkSnapshotsTx(context.Background(), counted, ids[:size])
		if err != nil {
			t.Fatalf("bulk size %d: %v", size, err)
		}
		if len(snapshots) != size || snapshots[0].IssueID != ids[0] || snapshots[size-1].IssueID != ids[size-1] || clockCalls != 1 {
			t.Fatalf("bulk size=%d count/order/clock len=%d first=%d last=%d calls=%d", size, len(snapshots), snapshots[0].IssueID,
				snapshots[len(snapshots)-1].IssueID, clockCalls)
		}
		if counted.queries != 1 || counted.queryRows != 0 || counted.execs != 0 {
			t.Fatalf("bulk size=%d DB round trips query=%d queryRow=%d exec=%d, want 1/0/0",
				size, counted.queries, counted.queryRows, counted.execs)
		}
	}
}
