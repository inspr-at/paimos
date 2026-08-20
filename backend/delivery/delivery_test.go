package delivery

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	appdb "github.com/inspr-at/paimos/backend/db"
)

func openDeliveryTestDB(t *testing.T) *sql.DB {
	t.Helper()
	oldDir, oldMode := os.Getenv("DATA_DIR"), os.Getenv("PAIMOS_TEST_MODE")
	if err := os.Setenv("DATA_DIR", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("PAIMOS_TEST_MODE", "1"); err != nil {
		t.Fatal(err)
	}
	if err := appdb.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = appdb.DB.Close()
		appdb.DB = nil
		_ = os.Setenv("DATA_DIR", oldDir)
		_ = os.Setenv("PAIMOS_TEST_MODE", oldMode)
	})
	return appdb.DB
}

func seedDeliveryIssue(t *testing.T, database *sql.DB) (issueID, projectID, userID int64) {
	t.Helper()
	result, err := database.Exec(`INSERT INTO projects(name,key) VALUES('Delivery','DLV')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ = result.LastInsertId()
	result, err = database.Exec(`INSERT INTO users(username,password,role) VALUES('delivery-user','x','admin')`)
	if err != nil {
		t.Fatal(err)
	}
	userID, _ = result.LastInsertId()
	result, err = database.Exec(`INSERT INTO issues(project_id,issue_number,title,description,acceptance_criteria)
		VALUES(?,1,'Ship delivery','Bounded description','Everything passes')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	issueID, _ = result.LastInsertId()
	return
}

func TestStableUninstrumentedSnapshotDoesNotWriteAndShowsLegacyActive(t *testing.T) {
	database := openDeliveryTestDB(t)
	issueID, projectID, userID := seedDeliveryIssue(t, database)
	if _, err := database.Exec(`INSERT INTO agent_runs(issue_id,project_id,requested_by,status,agent_name)
		VALUES(?,?,?,'running','legacy')`, issueID, projectID, userID); err != nil {
		t.Fatal(err)
	}
	store := NewStore(database, Options{})
	snapshot, err := store.SnapshotByIssue(context.Background(), issueID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Instrumented || snapshot.DeliveryKey != fmt.Sprintf("issue:%d", issueID) || snapshot.State != "unknown" ||
		snapshot.PrimaryAttention != "unknown_reporter" || len(snapshot.LegacyActiveRuns) != 1 {
		t.Fatalf("unexpected legacy snapshot: %+v", snapshot)
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM deliveries`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("snapshot wrote a delivery: count=%d err=%v", count, err)
	}
}

func TestRunBootstrapCompletionGatesLineageDuplicateAndDurations(t *testing.T) {
	database := openDeliveryTestDB(t)
	issueID, projectID, userID := seedDeliveryIssue(t, database)
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	callbackCount := 0
	callbackReadable := true
	store := NewStore(database, Options{Clock: ClockFunc(func() time.Time { return now }), Observer: func(_ context.Context, hint ChangeHint) {
		callbackCount++
		var found int
		if err := database.QueryRow(`SELECT COUNT(*) FROM delivery_change_log WHERE cursor_token=?`, hint.CursorToken).Scan(&found); err != nil || found != 1 {
			callbackReadable = false
		}
	}})
	rollbackTx, err := database.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	doomed, err := rollbackTx.Exec(`INSERT INTO agent_runs(issue_id,project_id,requested_by,status,delivery_instrumentation_version)
		VALUES(?,?,?,'queued',1)`, issueID, projectID, userID)
	if err != nil {
		t.Fatal(err)
	}
	doomedRunID, _ := doomed.LastInsertId()
	rollbackEffects := store.NewEffects()
	if _, err := store.BootstrapRunTx(context.Background(), rollbackTx, rollbackEffects, RunBootstrap{IssueID: issueID,
		RunID: doomedRunID, Mode: "implementation", Actor: Actor{Type: "system", OpaqueKey: "paimos"},
		IdempotencyKey: "doomed-run"}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("doomed bootstrap error=%v, want unauthorized after partial facts", err)
	}
	if err := rollbackTx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var rolledBackRuns, rolledBackDeliveries, rolledBackHints int
	if err := database.QueryRow(`SELECT COUNT(*) FROM agent_runs`).Scan(&rolledBackRuns); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM deliveries`).Scan(&rolledBackDeliveries); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM delivery_change_log`).Scan(&rolledBackHints); err != nil {
		t.Fatal(err)
	}
	if rolledBackRuns != 0 || rolledBackDeliveries != 0 || rolledBackHints != 0 || callbackCount != 0 {
		t.Fatalf("bootstrap rollback leaked run=%d delivery=%d hint=%d callback=%d",
			rolledBackRuns, rolledBackDeliveries, rolledBackHints, callbackCount)
	}
	result, err := database.Exec(`INSERT INTO agent_runs(issue_id,project_id,requested_by,status,delivery_instrumentation_version)
		VALUES(?,?,?,'queued',1)`, issueID, projectID, userID)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := result.LastInsertId()
	tx, err := database.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	effects := store.NewEffects()
	implementation, err := store.BootstrapRunTx(context.Background(), tx, effects, RunBootstrap{IssueID: issueID,
		RunID: runID, Mode: "implementation", Actor: Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", userID)},
		IdempotencyKey: fmt.Sprintf("run:%d", runID)})
	if err != nil {
		t.Fatal(err)
	}
	if callbackCount != 0 {
		t.Fatal("observer ran before commit")
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	effects.Dispatch(context.Background())
	if callbackCount == 0 || !callbackReadable {
		t.Fatalf("after-commit observer count=%d readable=%v", callbackCount, callbackReadable)
	}
	snapshot, err := store.SnapshotByIssue(context.Background(), issueID)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Instrumented || snapshot.AttemptNumber != 1 || !stageEligible(snapshot.Stages, StageSpecification) || stageEligible(snapshot.Stages, StageImplementation) {
		t.Fatalf("bad bootstrap snapshot: %+v", snapshot)
	}

	runActor := Actor{Type: "agent_run", OpaqueKey: fmt.Sprintf("run:%d", runID)}
	implReport := StageReport{IssueID: issueID, AttemptNumber: 1, StageKey: StageImplementation,
		ExecutionNumber: implementation.ExecutionNumber, AuthorityEpoch: implementation.AuthorityEpoch, Reporter: runActor,
		IdempotencyKey: "impl-success", SourceSequence: int64ptr(1), Kind: "semantic", State: "succeeded",
		Evidence: []Evidence{{Type: "implementation_result", Outcome: "passed", ReferenceKind: "commit", ReferenceValue: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}
	// A semantic success without allowlisted evidence cannot be sealed: an
	// accepted terminal fact must already carry its immutable proof.
	noEvidence := implReport
	noEvidence.IdempotencyKey = "impl-no-evidence"
	noEvidence.SourceSequence = int64ptr(1)
	noEvidence.Evidence = nil
	if _, err := store.ReportStage(context.Background(), noEvidence); !errors.Is(err, ErrInvalid) {
		t.Fatalf("evidence-less success error=%v", err)
	}
	snapshot, _ = store.SnapshotByIssue(context.Background(), issueID)
	if stageEligible(snapshot.Stages, StageImplementation) {
		t.Fatal("evidence-less implementation succeeded")
	}
	if _, err := store.ReportStage(context.Background(), implReport); err != nil {
		t.Fatal(err)
	}
	changeBeforeDuplicate := snapshotChangeSequence(t, database, issueID)
	if _, err := store.ReportStage(context.Background(), implReport); err != nil {
		t.Fatalf("exact duplicate: %v", err)
	}
	if got := snapshotChangeSequence(t, database, issueID); got != changeBeforeDuplicate {
		t.Fatalf("duplicate added a hint: before=%d after=%d", changeBeforeDuplicate, got)
	}
	conflict := implReport
	conflict.Activity = "different"
	if _, err := store.ReportStage(context.Background(), conflict); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting duplicate error=%v", err)
	}

	attempt := snapshot.AttemptNumber
	qa, err := store.StartStageRetry(context.Background(), StageStartRequest{IssueID: issueID, AttemptNumber: attempt,
		StageKey: StageQA, Reporter: Actor{Type: "external", OpaqueKey: "qa:trusted"}, ReasonCode: "qa_start", IdempotencyKey: "qa-start"})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(10 * time.Second)
	qaWait := StageReport{IssueID: issueID, AttemptNumber: attempt, StageKey: StageQA, ExecutionNumber: qa.ExecutionNumber,
		AuthorityEpoch: qa.AuthorityEpoch, Reporter: Actor{Type: "external", OpaqueKey: "qa:trusted"}, IdempotencyKey: "qa-wait",
		SourceSequence: int64ptr(1), Kind: "semantic", State: "waiting", NeedsInput: true,
		Blockers: []Blocker{{Key: "approval", Class: "input", Summary: "Await reviewer", HumanWait: true}}}
	if _, err := store.ReportStage(context.Background(), qaWait); err != nil {
		t.Fatal(err)
	}
	now = now.Add(20 * time.Second)
	qaPass := qaWait
	qaPass.IdempotencyKey, qaPass.State, qaPass.NeedsInput, qaPass.Blockers = "qa-pass", "succeeded", false, nil
	qaPass.SourceSequence = int64ptr(2)
	qaPass.Evidence = []Evidence{{Type: "test_result", Outcome: "passed", ReferenceKind: "external_ref", ReferenceValue: "suite:42"}}
	if _, err := store.ReportStage(context.Background(), qaPass); err != nil {
		t.Fatal(err)
	}
	assertResolvedBlockerHistory(t, database)

	deployment := startAndPass(t, store, &now, issueID, attempt, StageDeployment, "deploy", "deployment_result")
	_ = deployment
	snapshot, err = store.SnapshotByIssue(context.Background(), issueID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.State != "deployed_unverified" || !snapshot.Deployed || snapshot.Verified || snapshot.PrimaryAttention != "unverified" {
		t.Fatalf("deployed-unverified lost: %+v", snapshot)
	}
	verification, err := store.StartStageRetry(context.Background(), StageStartRequest{IssueID: issueID, AttemptNumber: attempt,
		StageKey: StageVerification, Reporter: Actor{Type: "external", OpaqueKey: "verify:trusted"}, ReasonCode: "verify_start", IdempotencyKey: "verify-start"})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	if _, err := store.ReportStage(context.Background(), StageReport{IssueID: issueID, AttemptNumber: attempt,
		StageKey: StageVerification, ExecutionNumber: verification.ExecutionNumber, AuthorityEpoch: verification.AuthorityEpoch,
		Reporter: Actor{Type: "external", OpaqueKey: "verify:trusted"}, IdempotencyKey: "verify-fail", SourceSequence: int64ptr(1),
		Kind: "semantic", State: "failed"}); err != nil {
		t.Fatal(err)
	}
	snapshot, _ = store.SnapshotByIssue(context.Background(), issueID)
	if snapshot.State != "deployed_unverified" {
		t.Fatalf("verification failure erased deployed truth: %s", snapshot.State)
	}
	verification, err = store.StartStageRetry(context.Background(), StageStartRequest{IssueID: issueID, AttemptNumber: attempt,
		StageKey: StageVerification, Reporter: Actor{Type: "external", OpaqueKey: "verify:trusted"}, ReasonCode: "verify_retry", IdempotencyKey: "verify-retry"})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	if _, err := store.ReportStage(context.Background(), StageReport{IssueID: issueID, AttemptNumber: attempt,
		StageKey: StageVerification, ExecutionNumber: verification.ExecutionNumber, AuthorityEpoch: verification.AuthorityEpoch,
		Reporter: Actor{Type: "external", OpaqueKey: "verify:trusted"}, IdempotencyKey: "verify-pass", SourceSequence: int64ptr(1),
		Kind: "semantic", State: "succeeded", Evidence: []Evidence{{Type: "verification_result", Outcome: "passed", ReferenceKind: "external_ref", ReferenceValue: "smoke:42"}}}); err != nil {
		t.Fatal(err)
	}
	snapshot, _ = store.SnapshotByIssue(context.Background(), issueID)
	if snapshot.State != "completed" || !snapshot.Verified {
		t.Fatalf("delivery not complete: %+v", snapshot)
	}
	changeBeforeEdit := snapshot.ChangeSequence
	if _, err := database.Exec(`UPDATE issues SET acceptance_criteria='Everything passes and is documented' WHERE id=?`, issueID); err != nil {
		t.Fatal(err)
	}
	snapshot, _ = store.SnapshotByIssue(context.Background(), issueID)
	if snapshot.State == "completed" || stageEligible(snapshot.Stages, StageSpecification) || snapshot.ChangeSequence <= changeBeforeEdit ||
		snapshot.AttemptNumber != 2 || snapshot.PlanRevision != 2 {
		t.Fatalf("spec edit retained stale approval or missed invalidation: %+v", snapshot)
	}
	attempt = snapshot.AttemptNumber

	// A current upstream retry supersedes lineage without rewriting downstream
	// facts or durations.
	if _, err := store.StartStageRetry(context.Background(), StageStartRequest{IssueID: issueID, AttemptNumber: attempt,
		StageKey: StageSpecification, Reporter: Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", userID)},
		ReasonCode: "spec_changed", IdempotencyKey: "spec-retry"}); err != nil {
		t.Fatal(err)
	}
	snapshot, _ = store.SnapshotByIssue(context.Background(), issueID)
	if snapshot.State == "completed" || stageEligible(snapshot.Stages, StageImplementation) {
		t.Fatalf("upstream retry retained downstream eligibility: %+v", snapshot)
	}
	var durationCount int
	if err := database.QueryRow(`SELECT COUNT(*) FROM delivery_stage_durations`).Scan(&durationCount); err != nil || durationCount < 4 {
		t.Fatalf("duration history count=%d err=%v", durationCount, err)
	}
	if err := store.RebuildLatest(context.Background(), issueID); err != nil {
		t.Fatal(err)
	}
	rebuilt, _ := store.SnapshotByIssue(context.Background(), issueID)
	if rebuilt.State != snapshot.State || rebuilt.ChangeSequence != snapshot.ChangeSequence {
		t.Fatalf("rebuild changed projection: before=%+v after=%+v", snapshot, rebuilt)
	}
	other, err := database.Exec(`INSERT INTO issues(project_id,issue_number,title) VALUES(?,2,'Uninstrumented')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	otherID, _ := other.LastInsertId()
	bulk, err := store.BulkSnapshots(context.Background(), []int64{otherID, issueID})
	if err != nil {
		t.Fatal(err)
	}
	if len(bulk) != 2 || bulk[0].IssueID != otherID || bulk[0].Instrumented || bulk[1].IssueID != issueID ||
		bulk[1].State != rebuilt.State || bulk[1].ChangeSequence != rebuilt.ChangeSequence ||
		len(bulk[1].Stages) != len(rebuilt.Stages) {
		t.Fatalf("bulk snapshot parity/order failed: bulk=%+v single=%+v", bulk, rebuilt)
	}
	for i := range rebuilt.Stages {
		if bulk[1].Stages[i].StageKey != rebuilt.Stages[i].StageKey ||
			bulk[1].Stages[i].EligibleSuccess != rebuilt.Stages[i].EligibleSuccess ||
			bulk[1].Stages[i].SemanticState != rebuilt.Stages[i].SemanticState {
			t.Fatalf("bulk stage %d mismatch: bulk=%+v single=%+v", i, bulk[1].Stages[i], rebuilt.Stages[i])
		}
	}
}

func TestHandoffResetPolicyAndOutOfOrder(t *testing.T) {
	database := openDeliveryTestDB(t)
	issueID, _, _ := seedDeliveryIssue(t, database)
	authorizations := 0
	store := NewStore(database, Options{Authorizer: AuthorizerFunc(func(_ context.Context, req AuthorizationRequest) error {
		authorizations++
		if req.Action == "delivery.policy.not_applicable" && req.PolicyReference != "policy:qa-optional" {
			return errors.New("bad policy")
		}
		return nil
	})})
	policies := DefaultPolicy()
	policies[2] = Policy{StageKey: StageQA, Applicability: "not_applicable", Weight: 20,
		PolicyReference: "policy:qa-optional", ReasonCode: "trusted_override", ReasonText: "QA is covered externally"}
	attempt, err := store.StartAttempt(context.Background(), AttemptRequest{IssueID: issueID,
		Actor: Actor{Type: "user", OpaqueKey: "user:1"}, Policies: policies, ReasonCode: "policy_change", IdempotencyKey: "attempt-1"})
	if err != nil || authorizations < 2 {
		t.Fatalf("authorized policy attempt=%+v authorizations=%d err=%v", attempt, authorizations, err)
	}
	stage, err := store.StartStageRetry(context.Background(), StageStartRequest{IssueID: issueID,
		AttemptNumber: attempt.AttemptNumber, StageKey: StageSpecification, Reporter: Actor{Type: "external", OpaqueKey: "agent:a"},
		ReasonCode: "spec_start", IdempotencyKey: "spec-start"})
	if err != nil {
		t.Fatal(err)
	}
	handed, err := store.RecordHandoff(context.Background(), HandoffRequest{IssueID: issueID, AttemptNumber: attempt.AttemptNumber,
		StageKey: StageSpecification, ExecutionNumber: stage.ExecutionNumber, AuthorityEpoch: stage.AuthorityEpoch,
		From: Actor{Type: "external", OpaqueKey: "agent:a"}, To: Actor{Type: "external", OpaqueKey: "agent:b"},
		ReasonCode: "handoff", ReasonText: "Transfer authority", IdempotencyKey: "handoff-1"})
	if err != nil {
		t.Fatal(err)
	}
	oldReport := StageReport{IssueID: issueID, AttemptNumber: attempt.AttemptNumber, StageKey: StageSpecification,
		ExecutionNumber: stage.ExecutionNumber, AuthorityEpoch: stage.AuthorityEpoch, Reporter: Actor{Type: "external", OpaqueKey: "agent:a"},
		IdempotencyKey: "old-report", SourceSequence: int64ptr(1), Kind: "semantic", State: "active"}
	if _, err := store.ReportStage(context.Background(), oldReport); !errors.Is(err, ErrStaleAuthority) {
		t.Fatalf("old authority report error=%v", err)
	}
	newReport := oldReport
	newReport.Reporter = Actor{Type: "external", OpaqueKey: "agent:b"}
	newReport.AuthorityEpoch = handed.AuthorityEpoch
	newReport.IdempotencyKey = "new-report"
	if _, err := store.ReportStage(context.Background(), newReport); err != nil {
		t.Fatal(err)
	}
	newReport.IdempotencyKey = "out-of-order"
	if _, err := store.ReportStage(context.Background(), newReport); !errors.Is(err, ErrConflict) {
		t.Fatalf("out-of-order report error=%v", err)
	}
	reset, err := store.AuthorizeProgressReset(context.Background(), ProgressResetRequest{IssueID: issueID,
		AttemptNumber: attempt.AttemptNumber, StageKey: StageSpecification, ExecutionNumber: handed.ExecutionNumber,
		AuthorityEpoch: handed.AuthorityEpoch, Actor: Actor{Type: "user", OpaqueKey: "user:1"},
		ReasonCode: "scope_reset", ReasonText: "Approved factual reset", IdempotencyKey: "reset-1"})
	if err != nil || reset.ExecutionNumber != handed.ExecutionNumber {
		t.Fatalf("reset=%+v err=%v", reset, err)
	}
	var cutoff int64
	if err := database.QueryRow(`SELECT reset_source_cutoff FROM delivery_stage_events WHERE event_type='progress_reset_authorized'`).Scan(&cutoff); err != nil || cutoff != 1 {
		t.Fatalf("reset cutoff=%d err=%v", cutoff, err)
	}
	second, err := store.StartAttempt(context.Background(), AttemptRequest{IssueID: issueID,
		Actor: Actor{Type: "user", OpaqueKey: "user:1"}, Policies: DefaultPolicy(), ReasonCode: "policy_change", IdempotencyKey: "attempt-2"})
	if err != nil || second.AttemptNumber != 2 || second.PreviousAttemptID == nil {
		t.Fatalf("policy change did not append attempt: %+v err=%v", second, err)
	}
}

func TestExternalFreshnessAttentionAndTerminalStatePrecedence(t *testing.T) {
	database := openDeliveryTestDB(t)
	issueID, _, _ := seedDeliveryIssue(t, database)
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	store := NewStore(database, Options{Clock: ClockFunc(func() time.Time { return now }), Freshness: FreshnessPolicy{
		FirstSignalTimeout: 60 * time.Second, HeartbeatTimeout: 90 * time.Second, EstimateTimeout: 90 * time.Second,
	}})
	attempt, err := store.StartAttempt(context.Background(), AttemptRequest{IssueID: issueID,
		Actor: Actor{Type: "user", OpaqueKey: "user:1"}, Policies: DefaultPolicy(),
		ReasonCode: "instrumentation", IdempotencyKey: "freshness-attempt"})
	if err != nil {
		t.Fatal(err)
	}
	reporter := Actor{Type: "external", OpaqueKey: "external:freshness"}
	stage, err := store.StartStageRetry(context.Background(), StageStartRequest{IssueID: issueID,
		AttemptNumber: attempt.AttemptNumber, StageKey: StageSpecification, Reporter: reporter,
		ReasonCode: "specification_start", IdempotencyKey: "freshness-start"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReportStage(context.Background(), StageReport{IssueID: issueID,
		AttemptNumber: attempt.AttemptNumber, StageKey: StageSpecification, ExecutionNumber: stage.ExecutionNumber,
		AuthorityEpoch: stage.AuthorityEpoch, Reporter: reporter, IdempotencyKey: "freshness-waiting",
		SourceSequence: int64ptr(1), Kind: "semantic", State: "waiting", NeedsInput: true,
		Activity: "Waiting for approval", Blockers: []Blocker{{Key: "approval", Class: "input", HumanWait: true}}}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(10 * time.Second)
	if _, err := store.ReportStage(context.Background(), StageReport{IssueID: issueID,
		AttemptNumber: attempt.AttemptNumber, StageKey: StageSpecification, ExecutionNumber: stage.ExecutionNumber,
		AuthorityEpoch: stage.AuthorityEpoch, Reporter: reporter, IdempotencyKey: "freshness-heartbeat",
		SourceSequence: int64ptr(2), Kind: "heartbeat"}); err != nil {
		t.Fatal(err)
	}
	heartbeatAt := now.Format(time.RFC3339Nano)
	now = now.Add(10 * time.Second)
	revision, progress, confidence := int64(1), float64(35), float64(.7)
	if _, err := store.ReportStage(context.Background(), StageReport{IssueID: issueID,
		AttemptNumber: attempt.AttemptNumber, StageKey: StageSpecification, ExecutionNumber: stage.ExecutionNumber,
		AuthorityEpoch: stage.AuthorityEpoch, Reporter: reporter, IdempotencyKey: "freshness-estimate",
		SourceSequence: int64ptr(3), Kind: "estimate", Estimate: EstimateEvidence{Revision: &revision,
			Progress: &progress, Source: "external", Confidence: &confidence, Basis: "Reporter estimate"}}); err != nil {
		t.Fatal(err)
	}
	var semanticID, heartbeatID, estimateID int64
	if err := database.QueryRow(`SELECT semantic_stage_event_id,heartbeat_stage_event_id,estimate_stage_event_id
		FROM delivery_stage_latest WHERE attempt_id=? AND stage_key=?`, attempt.ID, StageSpecification).
		Scan(&semanticID, &heartbeatID, &estimateID); err != nil {
		t.Fatal(err)
	}
	if semanticID == heartbeatID || semanticID == estimateID || heartbeatID == estimateID {
		t.Fatalf("latest facts collapsed: semantic=%d heartbeat=%d estimate=%d", semanticID, heartbeatID, estimateID)
	}
	now = now.Add(2 * time.Minute)
	snapshot, err := store.SnapshotByIssue(context.Background(), issueID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.State != "active" || len(snapshot.AttentionFlags) != 3 ||
		snapshot.AttentionFlags[0] != "waiting_on_human" || snapshot.AttentionFlags[1] != "blocked" ||
		snapshot.AttentionFlags[2] != "stale" || snapshot.Stages[0].SemanticState != "waiting" ||
		snapshot.Stages[0].LastHeartbeatAt == nil || *snapshot.Stages[0].LastHeartbeatAt != heartbeatAt {
		t.Fatalf("freshness/overlapping attention projection=%+v", snapshot)
	}

	retry, err := store.StartStageRetry(context.Background(), StageStartRequest{IssueID: issueID,
		AttemptNumber: attempt.AttemptNumber, StageKey: StageSpecification, Reporter: reporter,
		ReasonCode: "specification_retry", IdempotencyKey: "freshness-retry"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReportStage(context.Background(), StageReport{IssueID: issueID,
		AttemptNumber: attempt.AttemptNumber, StageKey: StageSpecification, ExecutionNumber: retry.ExecutionNumber,
		AuthorityEpoch: retry.AuthorityEpoch, Reporter: reporter, IdempotencyKey: "freshness-retry-failed",
		SourceSequence: int64ptr(1), Kind: "semantic", State: "failed"}); err != nil {
		t.Fatal(err)
	}
	snapshot, _ = store.SnapshotByIssue(context.Background(), issueID)
	if snapshot.State != "failed_needs_retry" {
		t.Fatalf("retry failure state=%q, want failed_needs_retry", snapshot.State)
	}

	second, err := store.StartAttempt(context.Background(), AttemptRequest{IssueID: issueID,
		Actor: Actor{Type: "user", OpaqueKey: "user:1"}, Policies: DefaultPolicy(), ReasonCode: "scope_change",
		IdempotencyKey: "freshness-second-attempt"})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := store.StartStageRetry(context.Background(), StageStartRequest{IssueID: issueID,
		AttemptNumber: second.AttemptNumber, StageKey: StageSpecification, Reporter: reporter,
		ReasonCode: "specification_start", IdempotencyKey: "freshness-cancel-start"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReportStage(context.Background(), StageReport{IssueID: issueID,
		AttemptNumber: second.AttemptNumber, StageKey: StageSpecification, ExecutionNumber: cancelled.ExecutionNumber,
		AuthorityEpoch: cancelled.AuthorityEpoch, Reporter: reporter, IdempotencyKey: "freshness-cancelled",
		SourceSequence: int64ptr(1), Kind: "semantic", State: "cancelled"}); err != nil {
		t.Fatal(err)
	}
	snapshot, _ = store.SnapshotByIssue(context.Background(), issueID)
	if snapshot.State != "cancelled" {
		t.Fatalf("cancelled state=%q", snapshot.State)
	}
}

func TestRunNormalizationProjectMoveAndDeletionSafeRetention(t *testing.T) {
	database := openDeliveryTestDB(t)
	issueID, projectID, userID := seedDeliveryIssue(t, database)
	store := NewStore(database, Options{})
	result, err := database.Exec(`INSERT INTO agent_runs(issue_id,project_id,requested_by,status,delivery_instrumentation_version)
		VALUES(?,?,?,'running',1)`, issueID, projectID, userID)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := result.LastInsertId()
	tx, _ := database.BeginTx(context.Background(), nil)
	effects := store.NewEffects()
	if _, err := store.BootstrapRunTx(context.Background(), tx, effects, RunBootstrap{IssueID: issueID, RunID: runID,
		Mode: "implementation", Actor: Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", userID)},
		IdempotencyKey: fmt.Sprintf("run:%d", runID)}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	effects.Dispatch(context.Background())

	commit := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	tx, _ = database.BeginTx(context.Background(), nil)
	if _, err := tx.Exec(`UPDATE agent_runs SET status='tests_passed',commit_sha=? WHERE id=?`, commit, runID); err != nil {
		t.Fatal(err)
	}
	effects = store.NewEffects()
	if err := store.NormalizeRunTx(context.Background(), tx, effects, RunNormalization{RunID: runID, Status: "tests_passed",
		IdempotencyKey: fmt.Sprintf("run:%d:tests_passed", runID)}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	effects.Dispatch(context.Background())
	snapshot, _ := store.SnapshotByIssue(context.Background(), issueID)
	if !stageEligible(snapshot.Stages, StageImplementation) || stageEligible(snapshot.Stages, StageQA) || stageEligible(snapshot.Stages, StageDeployment) {
		t.Fatalf("run status collapsed canonical stages: %+v", snapshot)
	}
	before := snapshotChangeSequence(t, database, issueID)
	// Exact lifecycle replay is a read even if it arrives after the fact.
	tx, _ = database.BeginTx(context.Background(), nil)
	effects = store.NewEffects()
	if err := store.NormalizeRunTx(context.Background(), tx, effects, RunNormalization{RunID: runID, Status: "tests_passed",
		IdempotencyKey: fmt.Sprintf("run:%d:tests_passed", runID)}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	effects.Dispatch(context.Background())
	if got := snapshotChangeSequence(t, database, issueID); got != before {
		t.Fatalf("lifecycle replay added hint: %d -> %d", before, got)
	}

	result, err = database.Exec(`INSERT INTO projects(name,key) VALUES('Target','TGT')`)
	if err != nil {
		t.Fatal(err)
	}
	targetProject, _ := result.LastInsertId()
	tx, _ = database.BeginTx(context.Background(), nil)
	if _, err := tx.Exec(`UPDATE issues SET project_id=? WHERE id=?`, targetProject, issueID); err != nil {
		t.Fatal(err)
	}
	effects = store.NewEffects()
	if err := store.ProjectMoveTx(context.Background(), tx, effects, issueID, projectID, targetProject,
		Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", userID)}, "move:1"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	effects.Dispatch(context.Background())
	snapshot, _ = store.SnapshotByIssue(context.Background(), issueID)
	if snapshot.AttemptNumber != 2 || snapshot.State != "pending" {
		t.Fatalf("project move did not cut authority: %+v", snapshot)
	}
	history, err := store.AttemptHistory(context.Background(), issueID)
	if err != nil || len(history) != 2 || history[1].ReasonCode != "project_move" {
		t.Fatalf("move history=%+v err=%v", history, err)
	}

	if _, err := database.Exec(`UPDATE delivery_change_log SET kind='issue'`); err == nil {
		t.Fatal("application mutated delivery change log")
	}
	if _, err := database.Exec(`DELETE FROM delivery_change_log WHERE id=(SELECT MAX(id) FROM delivery_change_log)`); err == nil {
		t.Fatal("application deleted unretained delivery change")
	}
	var deliveryID int64
	if err := database.QueryRow(`SELECT id FROM deliveries WHERE issue_id=?`, issueID).Scan(&deliveryID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`DELETE FROM issues WHERE id=?`, issueID); err != nil {
		t.Fatalf("hard delete instrumented issue: %v", err)
	}
	var tombstones int
	if err := database.QueryRow(`SELECT COUNT(*) FROM delivery_change_log WHERE delivery_id=? AND root_issue_id=? AND kind='root_deleted'`, deliveryID, issueID).Scan(&tombstones); err != nil || tombstones != 1 {
		t.Fatalf("retained tombstones=%d err=%v", tombstones, err)
	}
	var maxID int64
	if err := database.QueryRow(`SELECT MAX(id) FROM delivery_change_log`).Scan(&maxID); err != nil {
		t.Fatal(err)
	}
	if err := store.RetainChangesThrough(context.Background(), maxID); err != nil {
		t.Fatal(err)
	}
	var remaining, floor int64
	if err := database.QueryRow(`SELECT COUNT(*) FROM delivery_change_log`).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("retention remaining=%d err=%v", remaining, err)
	}
	if err := database.QueryRow(`SELECT floor_id FROM delivery_change_retention WHERE singleton=1`).Scan(&floor); err != nil || floor != maxID {
		t.Fatalf("retention floor=%d err=%v", floor, err)
	}
}

func TestDurationCapturesProjectAtCompletionAfterAuthorityMove(t *testing.T) {
	database := openDeliveryTestDB(t)
	issueID, sourceProject, userID := seedDeliveryIssue(t, database)
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	store := NewStore(database, Options{Clock: ClockFunc(func() time.Time { return now })})
	attempt, err := store.StartAttempt(context.Background(), AttemptRequest{IssueID: issueID,
		Actor: Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", userID)}, Policies: DefaultPolicy(),
		ReasonCode: "instrumentation", IdempotencyKey: "duration-move-attempt"})
	if err != nil {
		t.Fatal(err)
	}
	specReporter := Actor{Type: "external", OpaqueKey: "duration-specification"}
	specification, err := store.StartStageRetry(context.Background(), StageStartRequest{IssueID: issueID,
		AttemptNumber: attempt.AttemptNumber, StageKey: StageSpecification, Reporter: specReporter,
		ReasonCode: "specification_start", IdempotencyKey: "duration-specification-start"})
	if err != nil {
		t.Fatal(err)
	}
	digest, err := canonicalIssueSpecDigest(context.Background(), database, issueID)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	if _, err := store.ReportStage(context.Background(), StageReport{IssueID: issueID,
		AttemptNumber: attempt.AttemptNumber, StageKey: StageSpecification,
		ExecutionNumber: specification.ExecutionNumber, AuthorityEpoch: specification.AuthorityEpoch,
		Reporter: specReporter, IdempotencyKey: "duration-specification-pass", SourceSequence: int64ptr(1),
		Kind: "semantic", State: "succeeded", Evidence: []Evidence{{Type: "approval", Outcome: "passed",
			ReferenceKind: "digest", DigestSHA256: digest}}}); err != nil {
		t.Fatal(err)
	}
	reporter := Actor{Type: "external", OpaqueKey: "duration-implementation"}
	implementation, err := store.StartStageRetry(context.Background(), StageStartRequest{IssueID: issueID,
		AttemptNumber: attempt.AttemptNumber, StageKey: StageImplementation, Reporter: reporter,
		ReasonCode: "implementation_start", IdempotencyKey: "duration-implementation-start"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := database.Exec(`INSERT INTO projects(name,key) VALUES('Duration target','DUR')`)
	if err != nil {
		t.Fatal(err)
	}
	targetProject, _ := result.LastInsertId()
	tx, err := database.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	effects := store.NewEffects()
	if _, err := tx.Exec(`UPDATE issues SET project_id=? WHERE id=?`, targetProject, issueID); err != nil {
		t.Fatal(err)
	}
	if err := store.ProjectMoveTx(context.Background(), tx, effects, issueID, sourceProject, targetProject,
		Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", userID)}, "duration-move"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	effects.Dispatch(context.Background())

	// A linked reporter may close immutable old-attempt history after the move,
	// but the sample is attributed to the root's project when it actually lands.
	now = now.Add(5 * time.Minute)
	tx, err = database.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	effects = store.NewEffects()
	if _, err := store.reportStageTx(context.Background(), tx, effects, StageReport{IssueID: issueID,
		AttemptNumber: attempt.AttemptNumber, StageKey: StageImplementation,
		ExecutionNumber: implementation.ExecutionNumber, AuthorityEpoch: implementation.AuthorityEpoch,
		Reporter: reporter, IdempotencyKey: "duration-old-history-pass", SourceSequence: int64ptr(1),
		Kind: "semantic", State: "succeeded", Evidence: []Evidence{{Type: "implementation_result", Outcome: "passed",
			ReferenceKind: "external_ref", ReferenceValue: "duration:result"}}},
		"semantic_report", "stage_reported", "stage", "stage_event", nil, false); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	effects.Dispatch(context.Background())
	var capturedProject int64
	if err := database.QueryRow(`SELECT project_id_at_completion FROM delivery_stage_durations WHERE stage_execution_id=?`,
		implementation.ExecutionStartEventID).Scan(&capturedProject); err != nil {
		t.Fatal(err)
	}
	if capturedProject != targetProject {
		t.Fatalf("duration project_at_completion=%d, want current target %d (not attempt source %d)",
			capturedProject, targetProject, sourceProject)
	}
}

func TestPolicyAndSecretValidationFailClosed(t *testing.T) {
	database := openDeliveryTestDB(t)
	issueID, _, _ := seedDeliveryIssue(t, database)
	store := NewStore(database, Options{})
	policies := DefaultPolicy()
	policies[2] = Policy{StageKey: StageQA, Applicability: "not_applicable", Weight: 20,
		PolicyReference: "policy:qa", ReasonCode: "override", ReasonText: "Covered by policy"}
	_, err := store.StartAttempt(context.Background(), AttemptRequest{IssueID: issueID,
		Actor: Actor{Type: "user", OpaqueKey: "user:1"}, Policies: policies, ReasonCode: "policy_change", IdempotencyKey: "na-no-auth"})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("N/A without authorizer error=%v", err)
	}
	allNA := make([]Policy, len(CanonicalStages))
	for i, stage := range CanonicalStages {
		allNA[i] = Policy{StageKey: stage, Applicability: "not_applicable", Weight: DefaultWeights[stage],
			PolicyReference: "policy:all", ReasonCode: "override", ReasonText: "Not applicable"}
	}
	_, err = NewStore(database, Options{Authorizer: AuthorizerFunc(func(context.Context, AuthorizationRequest) error { return nil })}).
		StartAttempt(context.Background(), AttemptRequest{IssueID: issueID, Actor: Actor{Type: "user", OpaqueKey: "user:1"},
			Policies: allNA, ReasonCode: "policy_change", IdempotencyKey: "all-na"})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("all-N/A policy error=%v", err)
	}
	_, err = store.StartAttempt(context.Background(), AttemptRequest{IssueID: issueID,
		Actor: Actor{Type: "user", OpaqueKey: "user:1"}, ReasonCode: "instrumentation",
		ReasonText: "token=definitely-secret", IdempotencyKey: "secret"})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("secret-bearing reason error=%v", err)
	}
	var deliveries int
	if err := database.QueryRow(`SELECT COUNT(*) FROM deliveries`).Scan(&deliveries); err != nil || deliveries != 0 {
		t.Fatalf("rejected writes created delivery count=%d err=%v", deliveries, err)
	}
}

func startAndPass(t *testing.T, store *Store, now *time.Time, issueID, attempt int64, stage, key, evidenceType string) StageRef {
	t.Helper()
	reporter := Actor{Type: "external", OpaqueKey: key + ":trusted"}
	ref, err := store.StartStageRetry(context.Background(), StageStartRequest{IssueID: issueID, AttemptNumber: attempt,
		StageKey: stage, Reporter: reporter, ReasonCode: key + "_start", IdempotencyKey: key + "-start"})
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Second)
	if _, err := store.ReportStage(context.Background(), StageReport{IssueID: issueID, AttemptNumber: attempt,
		StageKey: stage, ExecutionNumber: ref.ExecutionNumber, AuthorityEpoch: ref.AuthorityEpoch, Reporter: reporter,
		IdempotencyKey: key + "-pass", SourceSequence: int64ptr(1), Kind: "semantic", State: "succeeded",
		Evidence: []Evidence{{Type: evidenceType, Outcome: "passed", ReferenceKind: "external_ref", ReferenceValue: key + ":42"}}}); err != nil {
		t.Fatal(err)
	}
	return ref
}

func assertResolvedBlockerHistory(t *testing.T, database *sql.DB) {
	t.Helper()
	var total, current int
	if err := database.QueryRow(`SELECT COUNT(*),COALESCE(SUM(is_current),0) FROM delivery_stage_blockers WHERE blocker_key='approval'`).Scan(&total, &current); err != nil {
		t.Fatal(err)
	}
	if total != 2 || current != 1 {
		t.Fatalf("blocker history total=%d current facts=%d", total, current)
	}
}

func snapshotChangeSequence(t *testing.T, database *sql.DB, issueID int64) int64 {
	t.Helper()
	var sequence int64
	if err := database.QueryRow(`SELECT MAX(c.change_sequence) FROM delivery_change_log c JOIN deliveries d ON d.id=c.delivery_id WHERE d.issue_id=?`, issueID).Scan(&sequence); err != nil {
		t.Fatal(err)
	}
	return sequence
}

func int64ptr(v int64) *int64 { return &v }
