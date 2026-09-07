// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package baselinebatch

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/contracts"
	appdb "github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/delivery"
	"github.com/inspr-at/paimos/backend/externalstage"
)

const (
	bridgeCommit   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	bridgeConfig   = "sha256:3333333333333333333333333333333333333333333333333333333333333333"
	bridgeManifest = "sha256:4444444444444444444444444444444444444444444444444444444444444444"
	bridgeVersion  = "26.09.07.12.00.00"
)

type bridgeFixture struct {
	t         *testing.T
	svc       *Service
	ext       *externalstage.Service
	delivery  *delivery.Store
	actor     Actor
	operator  externalstage.Principal
	reporter  externalstage.Principal
	projectID int64
	batch     Batch
	now       time.Time
}

func openBridgeFixture(t *testing.T) *bridgeFixture {
	t.Helper()
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("PAIMOS_TEST_MODE", "1")
	if err := appdb.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = appdb.DB.Close()
		appdb.DB = nil
	})
	now := time.Now().UTC().Truncate(time.Millisecond)
	result, err := appdb.DB.Exec(`INSERT INTO projects(name,key,inspr_stream_enabled) VALUES('Bridge','BRG',1)`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := result.LastInsertId()
	result, err = appdb.DB.Exec(`INSERT INTO users(username,password,role,status) VALUES('bridge-operator','x','admin','active')`)
	if err != nil {
		t.Fatal(err)
	}
	userID, _ := result.LastInsertId()
	cred := "96000000-0000-4000-8000-000000000960"
	if _, err := appdb.DB.Exec(`INSERT INTO sessions(id,user_id,expires_at,created_at,credential_id)
		VALUES('bridge-session',?,datetime('now','+1 hour'),datetime('now'),?)`, userID, cred); err != nil {
		t.Fatal(err)
	}
	deliveryStore := delivery.NewStore(appdb.DB, delivery.Options{Clock: delivery.ClockFunc(func() time.Time { return now })})
	ext, err := externalstage.NewService(appdb.DB, externalstage.Options{
		FixtureDigest: contracts.ExternalStageV1FixtureDigest(), Random: cryptorand.Reader,
		Clock: clockNow{now: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(appdb.DB, ClockFunc(func() time.Time { return now }), deliveryStore, nil, nil, nil)
	svc.External = ext
	actor := Actor{Kind: "session", UserID: userID, SessionCredentialID: cred}
	f := &bridgeFixture{t: t, svc: svc, ext: ext, delivery: deliveryStore, actor: actor,
		operator:  externalstage.Principal{UserID: userID, Kind: "session", SessionCredentialID: cred},
		projectID: projectID, now: now}
	f.batch = f.startReviewedBatch("bridge-start-key-01")
	return f
}

type clockNow struct{ now time.Time }

func (c clockNow) Now() time.Time { return c.now }

func (f *bridgeFixture) startReviewedBatch(idem string) Batch {
	f.t.Helper()
	reqs := []Requirement{{
		Ref: "req.login", Statement: "Users sign in with email",
		AcceptanceCriteria: []string{"Magic links expire"}, ConstraintRefs: []string{},
	}}
	digest, err := ContentDigest(reqs, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	seal, err := RevisionSeal("baseline:v1", 1, digest)
	if err != nil {
		f.t.Fatal(err)
	}
	handover, err := json.Marshal(map[string]any{
		"handover_version": HandoverVersion, "stream_ref": "stream:export",
		"exported_at": "2026-09-07T11:05:00.000Z",
		"baseline": map[string]any{
			"baseline_ref": "baseline:v1", "revision": 1, "content_digest": digest, "revision_seal": seal,
			"approved_by": "party:forged", "approved_at": "2026-09-07T11:00:00.000Z",
			"requirements": reqs, "constraints": []any{},
		},
		"pending_proposals": []any{}, "decisions": []any{},
	})
	if err != nil {
		f.t.Fatal(err)
	}
	ctx := context.Background()
	draft, err := f.svc.Import(ctx, f.actor, f.projectID, ImportRequest{Handover: handover, Selected: []string{"req.login"}})
	if err != nil {
		f.t.Fatal(err)
	}
	reviewed, err := f.svc.Review(ctx, f.actor, f.projectID, draft.ID, ReviewRequest{
		ExecutionMode: ModeManual, Selected: []string{"req.login"},
	})
	if err != nil {
		f.t.Fatal(err)
	}
	batch, err := f.svc.Start(ctx, f.actor, f.projectID, StartRequest{
		IdempotencyKey: idem, ReviewID: *reviewed.ReviewID, DraftRevision: reviewed.Revision, Confirm: true,
		ContentDigest: digest, RevisionSeal: seal, ExecutionMode: ModeManual, Selected: []string{"req.login"},
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return batch
}

func (f *bridgeFixture) registerPharos() int64 {
	f.t.Helper()
	result, err := appdb.DB.Exec(`INSERT INTO users(username,password,role,status) VALUES('pharos-bridge','x','member','active')`)
	if err != nil {
		f.t.Fatal(err)
	}
	reporterUser, _ := result.LastInsertId()
	if _, err := appdb.DB.Exec(`INSERT INTO project_members(user_id,project_id,access_level) VALUES(?,?,'editor')`, reporterUser, f.projectID); err != nil {
		f.t.Fatal(err)
	}
	result, err = appdb.DB.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes)
		VALUES(?,'pharos-bridge',?,'paimos_pharos_bridge','*')`, reporterUser, fmt.Sprintf("%064d", 960))
	if err != nil {
		f.t.Fatal(err)
	}
	keyID, _ := result.LastInsertId()
	f.reporter = externalstage.Principal{UserID: reporterUser, Kind: "api_key", APIKeyID: keyID}
	reg, err := f.ext.RegisterReporter(context.Background(), f.operator, fmt.Sprintf("issue:%d", f.batch.IssueID), "register-pharos",
		externalstage.RegisterReporterRequest{APIKeyID: keyID, ReporterClass: externalstage.ReporterClassPharos,
			ReporterRole: externalstage.ReporterRoleOwner, Workflow: "deploy-production", Environment: "production-eu1"})
	if err != nil {
		f.t.Fatal(err)
	}
	return reg.RegistrationID
}

func (f *bridgeFixture) reportBuildAndQA() {
	f.t.Helper()
	ctx := context.Background()
	reporter := delivery.Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", f.actor.UserID)}
	impl, err := f.delivery.StartStageRetry(ctx, delivery.StageStartRequest{
		IssueID: f.batch.IssueID, AttemptNumber: 1, StageKey: delivery.StageImplementation, Reporter: reporter,
		ReasonCode: "implementation_start", IdempotencyKey: f.batch.BatchKey + ":impl:start",
	})
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.delivery.ReportStage(ctx, delivery.StageReport{
		IssueID: f.batch.IssueID, AttemptNumber: 1, StageKey: delivery.StageImplementation,
		ExecutionNumber: impl.ExecutionNumber, AuthorityEpoch: impl.AuthorityEpoch, Reporter: reporter,
		IdempotencyKey: f.batch.BatchKey + ":impl:report", Kind: "semantic", State: "succeeded",
		Evidence: []delivery.Evidence{
			{Type: "implementation_result", Outcome: "passed", ReferenceKind: "commit", ReferenceValue: bridgeCommit},
			{Type: "artifact", Outcome: "passed", ReferenceKind: "digest", DigestSHA256: strings.TrimPrefix(bridgeConfig, "sha256:")},
		},
		ReasonCode: "implementation_result",
	}); err != nil {
		f.t.Fatal(err)
	}
	qa, err := f.delivery.StartStageRetry(ctx, delivery.StageStartRequest{
		IssueID: f.batch.IssueID, AttemptNumber: 1, StageKey: delivery.StageQA, Reporter: reporter,
		ReasonCode: "qa_start", IdempotencyKey: f.batch.BatchKey + ":qa:start",
	})
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.delivery.ReportStage(ctx, delivery.StageReport{
		IssueID: f.batch.IssueID, AttemptNumber: 1, StageKey: delivery.StageQA,
		ExecutionNumber: qa.ExecutionNumber, AuthorityEpoch: qa.AuthorityEpoch, Reporter: reporter,
		IdempotencyKey: f.batch.BatchKey + ":qa:report", Kind: "semantic", State: "succeeded",
		Evidence: []delivery.Evidence{{
			Type: "test_result", Outcome: "passed", ReferenceKind: "digest",
			DigestSHA256: strings.TrimPrefix(bridgeManifest, "sha256:"),
		}},
		ReasonCode: "test_result",
	}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *bridgeFixture) snapshot() delivery.Snapshot {
	f.t.Helper()
	tx, err := appdb.DB.BeginTx(context.Background(), nil)
	if err != nil {
		f.t.Fatal(err)
	}
	defer tx.Rollback()
	snap, err := f.delivery.SnapshotByIssueTx(context.Background(), tx, f.batch.IssueID)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		f.t.Fatal(err)
	}
	return snap
}

func (f *bridgeFixture) stored() storedBatch {
	f.t.Helper()
	tx, err := appdb.DB.BeginTx(context.Background(), nil)
	if err != nil {
		f.t.Fatal(err)
	}
	defer tx.Rollback()
	row, err := loadBatchByID(context.Background(), tx, f.projectID, f.batch.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	_ = tx.Commit()
	return row
}

func (f *bridgeFixture) artifact(kind externalstage.EvidenceKind) externalstage.PharosEvidenceV2 {
	return externalstage.PharosEvidenceV2{
		Kind: kind, Workflow: "deploy-production", Environment: "production-eu1",
		Artifact: externalstage.ArtifactEvidenceV2{
			VersionScheme: externalstage.VersionSchemeINSPRCalendar, Version: bridgeVersion,
			ReleaseChannel: "stable", ReleaseSequence: 260907120000, Digest: bridgeConfig,
			CommitDigest: bridgeCommit, ReleaseManifestCoordinate: "ghcr:inspr-at/pharos/releases/" + bridgeVersion,
			ReleaseManifestDigest: bridgeManifest,
		},
		Result: externalstage.EvidenceResultSucceeded,
	}
}

func TestBridgeStartRecordsSpecificationOnly(t *testing.T) {
	f := openBridgeFixture(t)
	spec := snapshotStage(f.snapshot(), delivery.StageSpecification)
	if !spec.PolicySatisfied {
		t.Fatalf("review did not satisfy specification: %+v", spec)
	}
	for _, key := range []string{delivery.StageImplementation, delivery.StageQA, delivery.StageDeployment, delivery.StageVerification} {
		if snapshotStage(f.snapshot(), key).PolicySatisfied {
			t.Fatalf("%s satisfied from review alone", key)
		}
	}
	got, err := f.svc.GetBatch(context.Background(), f.actor, f.projectID, f.batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Progress.NextAction != NextActionImplementationEvidence || got.Status != BatchActive {
		t.Fatalf("progress=%+v status=%s", got.Progress, got.Status)
	}
}

func TestBridgeManualReconcileDoesNotCreateHandoff(t *testing.T) {
	f := openBridgeFixture(t)
	f.registerPharos()
	f.reportBuildAndQA()
	if _, err := f.svc.Reconcile(context.Background(), f.actor, f.projectID, f.batch.ID); err != nil {
		t.Fatal(err)
	}
	h, err := loadStageHandoff(context.Background(), appdb.DB, f.stored(), delivery.StageDeployment)
	if err != nil {
		t.Fatal(err)
	}
	if h.HandoffID != "" {
		t.Fatalf("manual mode created a handoff: %+v", h)
	}
	got, err := f.svc.GetBatch(context.Background(), f.actor, f.projectID, f.batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Progress.NextAction != NextActionExternalStageCLI {
		t.Fatalf("manual next_action=%q", got.Progress.NextAction)
	}
}

func TestBridgeReviewedBatchToPharosVerification(t *testing.T) {
	f := openBridgeFixture(t)
	registrationID := f.registerPharos()
	f.reportBuildAndQA()
	ctx := context.Background()
	if _, err := f.svc.Reconcile(ctx, f.actor, f.projectID, f.batch.ID); err != nil {
		t.Fatal(err)
	}
	if snapshotStage(f.snapshot(), delivery.StageDeployment).PolicySatisfied {
		t.Fatal("deployment satisfied before Pharos receipt")
	}
	stored := f.stored()
	if err := f.svc.ensureStageHandoff(ctx, f.operator, stored, f.snapshot(), delivery.StageDeployment, registrationID, fmt.Sprintf("issue:%d", f.batch.IssueID)); err != nil {
		t.Fatal(err)
	}
	replay := f.svc.ensureStageHandoff(ctx, f.operator, stored, f.snapshot(), delivery.StageDeployment, registrationID, fmt.Sprintf("issue:%d", f.batch.IssueID))
	if replay != nil {
		t.Fatalf("same-request replay err=%v", replay)
	}
	handoff, err := loadStageHandoff(ctx, appdb.DB, stored, delivery.StageDeployment)
	if err != nil || handoff.HandoffID == "" || handoff.CredentialEpoch != 0 {
		t.Fatalf("handoff=%+v err=%v", handoff, err)
	}
	secret, err := f.ext.Mint(ctx, f.operator, handoff.HandoffID, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	assertSecretAbsentFromBatch(t, f.svc, f.actor, f.projectID, f.batch.ID, secret)
	observed := f.now.Format(time.RFC3339Nano)
	if _, err := f.ext.Accept(ctx, f.reporter, handoff.HandoffID, "accept-deploy", secret,
		externalstage.AcceptRequest{Sequence: 1, ObservedAt: observed}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ext.ReportV2(ctx, f.reporter, handoff.HandoffID, "unconfirmed-job", secret,
		externalstage.ReportRequestV2{Sequence: 2, State: externalstage.HandoffStateActive, ObservedAt: observed}); err != nil {
		t.Fatal(err)
	}
	if snapshotStage(f.snapshot(), delivery.StageDeployment).PolicySatisfied {
		t.Fatal("unconfirmed Pharos job completed deployment")
	}
	f.now = f.now.Add(2 * time.Second)
	f.svc.Clock = ClockFunc(func() time.Time { return f.now })
	deployedAt := f.now.Format(time.RFC3339Nano)
	evidence := f.artifact(externalstage.EvidenceKindDeployment)
	evidence.ObservedAt = deployedAt
	if _, err := f.ext.ReportV2(ctx, f.reporter, handoff.HandoffID, "deploy-succeeded", secret,
		externalstage.ReportRequestV2{Sequence: 3, State: externalstage.HandoffStateSucceeded, ObservedAt: deployedAt,
			PharosEvidence: &evidence}); err != nil {
		t.Fatal(err)
	}
	if !snapshotStage(f.snapshot(), delivery.StageDeployment).PolicySatisfied {
		t.Fatal("deployment receipt did not satisfy the stage")
	}
	if snapshotStage(f.snapshot(), delivery.StageVerification).PolicySatisfied {
		t.Fatal("verification completed from deployment alone")
	}
	time.Sleep(20 * time.Millisecond)
	if err := f.svc.ensureStageHandoff(ctx, f.operator, f.stored(), f.snapshot(), delivery.StageVerification, registrationID, fmt.Sprintf("issue:%d", f.batch.IssueID)); err != nil {
		t.Fatal(err)
	}
	verify, err := loadStageHandoff(ctx, appdb.DB, f.stored(), delivery.StageVerification)
	if err != nil || verify.HandoffID == "" {
		t.Fatalf("verification handoff=%+v err=%v", verify, err)
	}
	verifySecret, err := f.ext.Mint(ctx, f.operator, verify.HandoffID, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	stale := f.artifact(externalstage.EvidenceKindVerification)
	stale.ObservedAt = observed
	if _, err := f.ext.Accept(ctx, f.reporter, verify.HandoffID, "accept-verify", verifySecret,
		externalstage.AcceptRequest{Sequence: 1, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ext.ReportV2(ctx, f.reporter, verify.HandoffID, "active-verify", verifySecret,
		externalstage.ReportRequestV2{Sequence: 2, State: externalstage.HandoffStateActive, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ext.ReportV2(ctx, f.reporter, verify.HandoffID, "stale-measurement", verifySecret,
		externalstage.ReportRequestV2{Sequence: 3, State: externalstage.HandoffStateSucceeded, ObservedAt: stale.ObservedAt,
			PharosEvidence: &stale}); err == nil {
		t.Fatal("stale verification measurement was accepted")
	}
	time.Sleep(50 * time.Millisecond)
	later := time.Now().UTC().Format(time.RFC3339Nano)
	wrong := f.artifact(externalstage.EvidenceKindVerification)
	wrong.ObservedAt = later
	wrong.Artifact.Digest = "sha256:9999999999999999999999999999999999999999999999999999999999999999"
	if _, err := f.ext.ReportV2(ctx, f.reporter, verify.HandoffID, "wrong-artifact", verifySecret,
		externalstage.ReportRequestV2{Sequence: 3, State: externalstage.HandoffStateSucceeded, ObservedAt: wrong.ObservedAt,
			PharosEvidence: &wrong}); !errors.Is(err, externalstage.ErrInvalid) {
		t.Fatalf("wrong artifact err=%v", err)
	}
	match := f.artifact(externalstage.EvidenceKindVerification)
	match.ObservedAt = later
	if _, err := f.ext.ReportV2(ctx, f.reporter, verify.HandoffID, "verify-succeeded", verifySecret,
		externalstage.ReportRequestV2{Sequence: 3, State: externalstage.HandoffStateSucceeded, ObservedAt: later,
			PharosEvidence: &match}); err != nil {
		t.Fatal(err)
	}
	done, err := f.svc.GetBatch(ctx, f.actor, f.projectID, f.batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != BatchCompleted {
		t.Fatalf("status=%s stages=%+v", done.Status, done.Progress.Stages)
	}
	assertSecretAbsentFromBatch(t, f.svc, f.actor, f.projectID, f.batch.ID, secret)
	assertSecretAbsentFromBatch(t, f.svc, f.actor, f.projectID, f.batch.ID, verifySecret)
}

func TestBridgeCrashWindowAndPermissionLoss(t *testing.T) {
	f := openBridgeFixture(t)
	registrationID := f.registerPharos()
	f.reportBuildAndQA()
	ctx := context.Background()
	f.svc.failAfter = "deployment_activate"
	err := f.svc.ensureStageHandoff(ctx, f.operator, f.stored(), f.snapshot(), delivery.StageDeployment, registrationID, fmt.Sprintf("issue:%d", f.batch.IssueID))
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("crash err=%v", err)
	}
	f.svc.failAfter = ""
	if err := f.svc.ensureStageHandoff(ctx, f.operator, f.stored(), f.snapshot(), delivery.StageDeployment, registrationID, fmt.Sprintf("issue:%d", f.batch.IssueID)); err != nil {
		t.Fatal(err)
	}
	h, err := loadStageHandoff(ctx, appdb.DB, f.stored(), delivery.StageDeployment)
	if err != nil || h.HandoffID == "" {
		t.Fatalf("handoff after crash recovery=%+v err=%v", h, err)
	}
	if _, err := f.svc.Reconcile(ctx, f.actor, f.projectID+1, f.batch.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong project err=%v", err)
	}
	if _, err := appdb.DB.Exec(`UPDATE sessions SET expires_at=datetime('now','-1 hour') WHERE credential_id=?`, f.actor.SessionCredentialID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Reconcile(ctx, f.actor, f.projectID, f.batch.ID); err == nil {
		t.Fatal("expired session still reconciled")
	}
}

func TestBridgeUnavailableHandoffConfigAndMissingRegistration(t *testing.T) {
	f := openBridgeFixture(t)
	f.reportBuildAndQA()
	got, err := f.svc.GetBatch(context.Background(), f.actor, f.projectID, f.batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Progress.SetupRequired != SetupRequiredPharosRegistration || got.Progress.NextAction != NextActionExternalStageCLI {
		t.Fatalf("manual missing registration progress=%+v", got.Progress)
	}
	stored := f.stored()
	stored.ExecutionMode = ModeAssisted
	f.svc.External = nil
	tx, err := appdb.DB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	snap, err := f.delivery.SnapshotByIssueTx(context.Background(), tx, stored.IssueID)
	if err != nil {
		t.Fatal(err)
	}
	progress := Progress{}
	state := BatchActive
	if err := f.svc.annotateBridge(context.Background(), tx, stored, snap, &progress, &state); err != nil {
		t.Fatal(err)
	}
	if progress.SetupRequired != SetupRequiredHandoffConfig || state != BatchBlocked {
		t.Fatalf("unavailable handoff config progress=%+v state=%s", progress, state)
	}
}

func TestBridgeWorkerEstimateDoesNotSatisfyBuild(t *testing.T) {
	f := openBridgeFixture(t)
	ctx := context.Background()
	reporter := delivery.Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", f.actor.UserID)}
	impl, err := f.delivery.StartStageRetry(ctx, delivery.StageStartRequest{
		IssueID: f.batch.IssueID, AttemptNumber: 1, StageKey: delivery.StageImplementation, Reporter: reporter,
		ReasonCode: "implementation_start", IdempotencyKey: f.batch.BatchKey + ":impl-est:start",
	})
	if err != nil {
		t.Fatal(err)
	}
	progress := 100.0
	rev := int64(1)
	seq := int64(1)
	confidence := 1.0
	if _, err := f.delivery.ReportStage(ctx, delivery.StageReport{
		IssueID: f.batch.IssueID, AttemptNumber: 1, StageKey: delivery.StageImplementation,
		ExecutionNumber: impl.ExecutionNumber, AuthorityEpoch: impl.AuthorityEpoch, Reporter: reporter,
		IdempotencyKey: f.batch.BatchKey + ":impl-est", Kind: "estimate", SourceSequence: &seq,
		Estimate:   delivery.EstimateEvidence{Revision: &rev, Progress: &progress, Source: "agent", Confidence: &confidence, Basis: "exited"},
		ReasonCode: "worker_estimate",
	}); err != nil {
		t.Fatal(err)
	}
	if snapshotStage(f.snapshot(), delivery.StageImplementation).PolicySatisfied {
		t.Fatal("worker estimate satisfied implementation")
	}
}

func assertSecretAbsentFromBatch(t *testing.T, svc *Service, actor Actor, projectID, batchID int64, secret []byte) {
	t.Helper()
	batch, err := svc.GetBatch(context.Background(), actor, projectID, batchID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(batch)
	if err != nil {
		t.Fatal(err)
	}
	hexValue := hex.EncodeToString(secret)
	for _, needle := range [][]byte{
		append([]byte(nil), secret...),
		[]byte(hexValue),
		[]byte(base64.RawURLEncoding.EncodeToString(secret)),
		[]byte(base64.StdEncoding.EncodeToString(secret)),
	} {
		if len(needle) > 0 && bytes.Contains(raw, needle) {
			t.Fatalf("handoff secret leaked into batch projection")
		}
	}
}

func TestBridgeConflictingReplayActivate(t *testing.T) {
	f := openBridgeFixture(t)
	registrationID := f.registerPharos()
	f.reportBuildAndQA()
	ctx := context.Background()
	if err := f.svc.ensureStageHandoff(ctx, f.operator, f.stored(), f.snapshot(), delivery.StageDeployment, registrationID, fmt.Sprintf("issue:%d", f.batch.IssueID)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ext.ActivateOwner(ctx, f.operator, fmt.Sprintf("issue:%d", f.batch.IssueID), f.batch.BatchKey+":deployment:activate-again",
		externalstage.ActivateOwnerRequest{
			ReporterRegistrationID: registrationID, StageKey: delivery.StageDeployment,
			ExpectedAttemptNumber: 1, ExpectedPlanRevision: 1, ExpectedCurrentExecution: 0, ExpectedCurrentAuthorityEpoch: 0,
		}); !errors.Is(err, externalstage.ErrConflict) {
		t.Fatalf("second activation err=%v", err)
	}
}

func TestBridgeAssistedReconcileCreatesHandoff(t *testing.T) {
	f := openBridgeFixture(t)
	f.registerPharos()
	f.reportBuildAndQA()
	ctx := context.Background()
	stored := f.stored()
	stored.ExecutionMode = ModeAssisted
	if err := f.svc.reconcileAuthorizedSteps(ctx, f.actor, stored, f.snapshot()); err != nil {
		t.Fatal(err)
	}
	h, err := loadStageHandoff(ctx, appdb.DB, stored, delivery.StageDeployment)
	if err != nil || h.HandoffID == "" {
		t.Fatalf("assisted reconcile handoff=%+v err=%v", h, err)
	}
	if err := f.svc.reconcileAuthorizedSteps(ctx, f.actor, stored, f.snapshot()); err != nil {
		t.Fatal(err)
	}
	replay, err := loadStageHandoff(ctx, appdb.DB, stored, delivery.StageDeployment)
	if err != nil || replay.HandoffID != h.HandoffID {
		t.Fatalf("assisted replay=%+v err=%v", replay, err)
	}
	keyActor := f.operatorWriteKey()
	if err := f.svc.reconcileAuthorizedSteps(ctx, keyActor, stored, f.snapshot()); !errors.Is(err, ErrForbidden) {
		t.Fatalf("assisted api key err=%v", err)
	}
}

func TestBridgeAutomaticAPIKeyReconcile(t *testing.T) {
	f := openBridgeFixture(t)
	f.registerPharos()
	f.reportBuildAndQA()
	ctx := context.Background()
	stored := f.stored()
	stored.ExecutionMode = ModeAutomatic
	keyActor := f.operatorWriteKey()
	if err := f.svc.reconcileAuthorizedSteps(ctx, keyActor, stored, f.snapshot()); err != nil {
		t.Fatal(err)
	}
	h, err := loadStageHandoff(ctx, appdb.DB, stored, delivery.StageDeployment)
	if err != nil || h.HandoffID == "" {
		t.Fatalf("automatic api-key reconcile handoff=%+v err=%v", h, err)
	}
	if _, err := appdb.DB.Exec(`UPDATE api_keys SET expires_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','-1 hour') WHERE id=?`, keyActor.APIKeyID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Reconcile(ctx, keyActor, f.projectID, f.batch.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("expired api key err=%v", err)
	}
}

func (f *bridgeFixture) operatorWriteKey() Actor {
	f.t.Helper()
	result, err := appdb.DB.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes)
		VALUES(?,'bridge-operator-key',?,'paimos_bridge_op','agent-controls:write')`, f.actor.UserID, fmt.Sprintf("%064d", 961))
	if err != nil {
		f.t.Fatal(err)
	}
	keyID, _ := result.LastInsertId()
	return Actor{Kind: string(auth.PrincipalAPIKey), UserID: f.actor.UserID, APIKeyID: keyID}
}
