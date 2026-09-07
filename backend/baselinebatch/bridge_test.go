// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package baselinebatch

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
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
	"github.com/inspr-at/paimos/backend/lifecycleintents"
	"github.com/inspr-at/paimos/backend/managedharness"
)

const (
	bridgeCommit     = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	bridgeConfig     = "sha256:3333333333333333333333333333333333333333333333333333333333333333"
	bridgeIndex      = "sha256:4444444444444444444444444444444444444444444444444444444444444444"
	bridgeQADigest   = "5555555555555555555555555555555555555555555555555555555555555555"
	bridgeReleaseSet = "sha256:6666666666666666666666666666666666666666666666666666666666666666"
	bridgeVersion    = "26.09.07.12.00.00"
	bridgeChannel    = "stable"
	bridgeSequence   = int64(260907120000)
	bridgeCoordinate = "ghcr:inspr-at/pharos/releases/" + bridgeVersion
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
	userID    int64
	batch     Batch
	now       time.Time
	daemon    *ownedDaemon
}

func openBridgeFixture(t *testing.T) *bridgeFixture {
	t.Helper()
	return openBridgeFixtureMode(t, ModeManual)
}

func openAgentBridgeFixture(t *testing.T, mode string) *bridgeFixture {
	t.Helper()
	if mode != ModeAssisted && mode != ModeAutomatic {
		t.Fatalf("agent fixture mode=%s", mode)
	}
	return openBridgeFixtureMode(t, mode)
}

func openBridgeFixtureMode(t *testing.T, mode string) *bridgeFixture {
	t.Helper()
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("PAIMOS_TEST_MODE", "1")
	if err := appdb.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if appdb.DB != nil {
			_ = appdb.DB.Close()
			appdb.DB = nil
		}
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
	if _, err := appdb.DB.Exec(`UPDATE users SET is_super_admin=1 WHERE id=?`, userID); err != nil {
		t.Fatal(err)
	}
	cred := "96000000-0000-4000-8000-000000000960"
	if _, err := appdb.DB.Exec(`INSERT INTO sessions(id,user_id,expires_at,created_at,credential_id)
		VALUES('bridge-session',?,datetime('now','+1 hour'),datetime('now'),?)`, userID, cred); err != nil {
		t.Fatal(err)
	}
	f := &bridgeFixture{t: t, now: now, projectID: projectID, userID: userID,
		actor:    Actor{Kind: "session", UserID: userID, SessionCredentialID: cred},
		operator: externalstage.Principal{UserID: userID, Kind: "session", SessionCredentialID: cred}}
	clk := clockNow{now: &f.now}
	deliveryStore := delivery.NewStore(appdb.DB, delivery.Options{Clock: delivery.ClockFunc(func() time.Time { return *clk.now })})
	ext, err := externalstage.NewService(appdb.DB, externalstage.Options{
		FixtureDigest: contracts.ExternalStageV1FixtureDigest(), Random: cryptorand.Reader,
		Clock: clk,
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(appdb.DB, ClockFunc(func() time.Time { return *clk.now }), deliveryStore,
		lifecycleintents.NewService(appdb.DB), managedharness.NewService(appdb.DB), nil)
	svc.External = ext
	f.svc, f.ext, f.delivery = svc, ext, deliveryStore
	if mode != ModeManual {
		if _, err := appdb.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'codex')`, projectID); err != nil {
			t.Fatal(err)
		}
		f.daemon = newOwnedDaemon(t, projectID, userID)
	}
	f.batch = f.startReviewedBatch(mode, "bridge-start-key-01")
	return f
}

type clockNow struct{ now *time.Time }

func (c clockNow) Now() time.Time { return c.now.UTC() }

func (f *bridgeFixture) startReviewedBatch(mode, idem string) Batch {
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
	worker := WorkerSelection{}
	if mode != ModeManual {
		worker = f.daemon.worker()
		if _, err := f.svc.PatchDraft(ctx, f.actor, f.projectID, draft.ID, PatchDraftRequest{
			ExecutionMode: &mode, Worker: &worker,
		}); err != nil {
			f.t.Fatal(err)
		}
	}
	reviewed, err := f.svc.Review(ctx, f.actor, f.projectID, draft.ID, ReviewRequest{
		ExecutionMode: mode, Selected: []string{"req.login"}, Worker: worker,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	if mode != ModeManual {
		if _, err := f.svc.RequestReadiness(ctx, f.actor, f.projectID, draft.ID); err != nil {
			f.t.Fatal(err)
		}
		if f.daemon.step(nil) == nil {
			f.t.Fatal("owned daemon found no readiness intent")
		}
	}
	batch, err := f.svc.Start(ctx, f.actor, f.projectID, StartRequest{
		IdempotencyKey: idem, ReviewID: *reviewed.ReviewID, DraftRevision: reviewed.Revision, Confirm: true,
		ContentDigest: digest, RevisionSeal: seal, ExecutionMode: mode, Selected: []string{"req.login"}, Worker: worker,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	if mode != ModeManual {
		if f.daemon.step(nil) == nil {
			f.t.Fatal("owned daemon found no start intent")
		}
		got, err := f.svc.GetBatch(ctx, f.actor, f.projectID, batch.ID)
		if err != nil {
			f.t.Fatal(err)
		}
		batch = got
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

func (f *bridgeFixture) registerJanus() int64 {
	return f.registerJanusAs("cluster-admission", "primary")
}

func (f *bridgeFixture) registerJanusAs(dependencyKey, label string) int64 {
	f.t.Helper()
	result, err := appdb.DB.Exec(`INSERT INTO users(username,password,role,status) VALUES(?,'x','member','active')`, "janus-bridge-"+label)
	if err != nil {
		f.t.Fatal(err)
	}
	reporterUser, _ := result.LastInsertId()
	if _, err := appdb.DB.Exec(`INSERT INTO project_members(user_id,project_id,access_level) VALUES(?,?,'editor')`, reporterUser, f.projectID); err != nil {
		f.t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(label + dependencyKey))
	result, err = appdb.DB.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes)
		VALUES(?,?,?,'paimos_janus_bridge','*')`, reporterUser, "janus-bridge-"+label, hex.EncodeToString(sum[:]))
	if err != nil {
		f.t.Fatal(err)
	}
	keyID, _ := result.LastInsertId()
	reg, err := f.ext.RegisterReporter(context.Background(), f.operator, fmt.Sprintf("issue:%d", f.batch.IssueID), "register-janus-"+label,
		externalstage.RegisterReporterRequest{APIKeyID: keyID, ReporterClass: externalstage.ReporterClassJanus,
			ReporterRole: externalstage.ReporterRoleDependency, DependencyKey: dependencyKey})
	if err != nil {
		f.t.Fatal(err)
	}
	return reg.RegistrationID
}

func (f *bridgeFixture) reportBuildAndQA() {
	f.reportBuildAndQAOn(1, "")
}

func (f *bridgeFixture) reportBuildAndQAOn(attempt int64, suffix string) {
	f.t.Helper()
	ctx := context.Background()
	reporter := delivery.Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", f.actor.UserID)}
	impl, err := f.delivery.StartStageRetry(ctx, delivery.StageStartRequest{
		IssueID: f.batch.IssueID, AttemptNumber: attempt, StageKey: delivery.StageImplementation, Reporter: reporter,
		ReasonCode: "implementation_start", IdempotencyKey: fmt.Sprintf("%s:impl%s:start", f.batch.BatchKey, suffix),
	})
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.delivery.ReportStage(ctx, delivery.StageReport{
		IssueID: f.batch.IssueID, AttemptNumber: attempt, StageKey: delivery.StageImplementation,
		ExecutionNumber: impl.ExecutionNumber, AuthorityEpoch: impl.AuthorityEpoch, Reporter: reporter,
		IdempotencyKey: fmt.Sprintf("%s:impl%s:report", f.batch.BatchKey, suffix), Kind: "semantic", State: "succeeded",
		Evidence: []delivery.Evidence{
			{Type: "implementation_result", Outcome: "passed", ReferenceKind: "commit", ReferenceValue: bridgeCommit},
			{Type: "artifact", Outcome: "passed", ReferenceKind: "digest", DigestSHA256: strings.TrimPrefix(bridgeConfig, "sha256:")},
			{Type: "artifact", Outcome: "passed", ReferenceKind: "external_ref", ReferenceValue: externalstage.FormatOCIManifestRef(bridgeIndex)},
			{Type: "artifact", Outcome: "passed", ReferenceKind: "external_ref", ReferenceValue: externalstage.FormatReleaseManifestRef(bridgeReleaseSet)},
			{Type: "artifact", Outcome: "passed", ReferenceKind: "external_ref", ReferenceValue: externalstage.FormatReleaseCoordinateRef(bridgeCoordinate)},
			{Type: "artifact", Outcome: "passed", ReferenceKind: "external_ref",
				ReferenceValue: externalstage.FormatReleaseIdentity(externalstage.VersionSchemeINSPRCalendar, bridgeChannel, bridgeSequence, bridgeVersion)},
		},
		ReasonCode: "implementation_result",
	}); err != nil {
		f.t.Fatal(err)
	}
	qa, err := f.delivery.StartStageRetry(ctx, delivery.StageStartRequest{
		IssueID: f.batch.IssueID, AttemptNumber: attempt, StageKey: delivery.StageQA, Reporter: reporter,
		ReasonCode: "qa_start", IdempotencyKey: fmt.Sprintf("%s:qa%s:start", f.batch.BatchKey, suffix),
	})
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.delivery.ReportStage(ctx, delivery.StageReport{
		IssueID: f.batch.IssueID, AttemptNumber: attempt, StageKey: delivery.StageQA,
		ExecutionNumber: qa.ExecutionNumber, AuthorityEpoch: qa.AuthorityEpoch, Reporter: reporter,
		IdempotencyKey: fmt.Sprintf("%s:qa%s:report", f.batch.BatchKey, suffix), Kind: "semantic", State: "succeeded",
		Evidence: []delivery.Evidence{
			{Type: "test_result", Outcome: "passed", ReferenceKind: "digest", DigestSHA256: bridgeQADigest},
		},
		ReasonCode: "test_result",
	}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *bridgeFixture) reportPartialBuildAndQA(implEvidence []delivery.Evidence) {
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
		Evidence: implEvidence, ReasonCode: "implementation_result",
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
		Evidence:   []delivery.Evidence{{Type: "test_result", Outcome: "passed", ReferenceKind: "external_ref", ReferenceValue: "suite:legacy"}},
		ReasonCode: "test_result",
	}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *bridgeFixture) v1Evidence() externalstage.PharosEvidence {
	return externalstage.PharosEvidence{
		Kind: externalstage.EvidenceKindDeployment, Workflow: "deploy-production", Environment: "production-eu1",
		Artifact: externalstage.ArtifactEvidence{Version: bridgeVersion, Digest: bridgeConfig, CommitDigest: bridgeCommit},
		Result:   externalstage.EvidenceResultSucceeded,
	}
}

func (f *bridgeFixture) reportSpecificationOn(attempt int64, suffix string) {
	f.t.Helper()
	ctx := context.Background()
	reporter := delivery.Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", f.actor.UserID)}
	spec, err := f.delivery.StartStageRetry(ctx, delivery.StageStartRequest{
		IssueID: f.batch.IssueID, AttemptNumber: attempt, StageKey: delivery.StageSpecification, Reporter: reporter,
		ReasonCode: "baseline_review", IdempotencyKey: fmt.Sprintf("%s:spec%s:start", f.batch.BatchKey, suffix),
	})
	if err != nil {
		f.t.Fatal(err)
	}
	tx, err := appdb.DB.BeginTx(ctx, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	digest, err := delivery.IssueSpecDigestTx(ctx, tx, f.batch.IssueID)
	_ = tx.Rollback()
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.delivery.ReportStage(ctx, delivery.StageReport{
		IssueID: f.batch.IssueID, AttemptNumber: attempt, StageKey: delivery.StageSpecification,
		ExecutionNumber: spec.ExecutionNumber, AuthorityEpoch: spec.AuthorityEpoch, Reporter: reporter,
		IdempotencyKey: fmt.Sprintf("%s:spec%s:report", f.batch.BatchKey, suffix), Kind: "semantic", State: "succeeded",
		Evidence: []delivery.Evidence{{
			Type: "spec_acceptance", Outcome: "passed", ReferenceKind: "digest", DigestSHA256: digest,
		}},
		ReasonCode: "baseline_review",
	}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *bridgeFixture) liveHandoff(stage string) storedHandoff {
	f.t.Helper()
	h, err := loadLiveOwnerHandoff(context.Background(), appdb.DB, f.stored(), currentGeneration(f.snapshot(), stage), stage)
	if err != nil {
		f.t.Fatal(err)
	}
	return h
}

func (f *bridgeFixture) sealedCount(stage string) int {
	f.t.Helper()
	set, err := loadSealedPrerequisiteSet(context.Background(), appdb.DB, f.stored(), currentGeneration(f.snapshot(), stage), stage)
	if err != nil {
		f.t.Fatal(err)
	}
	if set.SealedAt == "" {
		return -1
	}
	return set.DeclaredCount
}

func (f *bridgeFixture) authorizeHandoff() Batch {
	f.t.Helper()
	ctx := context.Background()
	if _, err := f.svc.Reconcile(ctx, f.actor, f.projectID, f.batch.ID); err != nil {
		f.t.Fatalf("activate reconcile: %v", err)
	}
	stage := delivery.StageDeployment
	if stageSatisfied(f.snapshot(), delivery.StageDeployment) {
		stage = delivery.StageVerification
	}
	gen := currentGeneration(f.snapshot(), stage)
	if gen.ExecutionNumber == 0 {
		f.t.Fatalf("owner activation did not start %s", stage)
	}
	if f.sealedCount(stage) < 0 {
		if _, err := f.ext.SealPrerequisites(ctx, f.operator, fmt.Sprintf("issue:%d", f.batch.IssueID),
			fmt.Sprintf("%s:cli-empty-seal:%s:a%d:e%d:g%d", f.batch.BatchKey, stage, f.snapshot().AttemptNumber, gen.ExecutionNumber, gen.AuthorityEpoch),
			externalstage.SealPrerequisitesRequest{
				StageKey: stage, ExecutionNumber: gen.ExecutionNumber,
				ExpectedPlanRevision: f.snapshot().PlanRevision, ExpectedAuthorityEpoch: gen.AuthorityEpoch,
				Prerequisites: []externalstage.Prerequisite{},
			}); err != nil {
			f.t.Fatalf("operator empty seal: %v", err)
		}
	}
	got, err := f.svc.Reconcile(ctx, f.actor, f.projectID, f.batch.ID)
	if err != nil {
		f.t.Fatalf("handoff reconcile: %v", err)
	}
	return got
}

func (f *bridgeFixture) secondEditor() (Actor, externalstage.Principal) {
	f.t.Helper()
	result, err := appdb.DB.Exec(`INSERT INTO users(username,password,role,status) VALUES('bridge-editor','x','member','active')`)
	if err != nil {
		f.t.Fatal(err)
	}
	userID, _ := result.LastInsertId()
	if _, err := appdb.DB.Exec(`INSERT INTO project_members(user_id,project_id,access_level) VALUES(?,?,'editor')`, userID, f.projectID); err != nil {
		f.t.Fatal(err)
	}
	cred := "96000000-0000-4000-8000-000000000961"
	if _, err := appdb.DB.Exec(`INSERT INTO sessions(id,user_id,expires_at,created_at,credential_id)
		VALUES('bridge-editor-session',?,datetime('now','+1 hour'),datetime('now'),?)`, userID, cred); err != nil {
		f.t.Fatal(err)
	}
	return Actor{Kind: "session", UserID: userID, SessionCredentialID: cred},
		externalstage.Principal{UserID: userID, Kind: "session", SessionCredentialID: cred}
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
			ReleaseChannel: bridgeChannel, ReleaseSequence: bridgeSequence, Digest: bridgeConfig,
			CommitDigest: bridgeCommit, ReleaseManifestCoordinate: bridgeCoordinate,
			ReleaseManifestDigest: bridgeReleaseSet,
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
	if h := f.liveHandoff(delivery.StageDeployment); h.HandoffID != "" {
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
	f := openAgentBridgeFixture(t, ModeAssisted)
	f.registerPharos()
	f.reportBuildAndQA()
	ctx := context.Background()
	got := f.authorizeHandoff()
	if snapshotStage(f.snapshot(), delivery.StageDeployment).PolicySatisfied {
		t.Fatal("deployment satisfied before Pharos receipt")
	}
	handoff := f.liveHandoff(delivery.StageDeployment)
	if handoff.HandoffID == "" || handoff.CredentialEpoch != 0 {
		t.Fatalf("handoff=%+v", handoff)
	}
	if got.Progress.NextAction != NextActionMintHandoffSecret {
		t.Fatalf("next_action=%q", got.Progress.NextAction)
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
	wrong := f.artifact(externalstage.EvidenceKindDeployment)
	wrong.ObservedAt = f.now.Add(2 * time.Second).Format(time.RFC3339Nano)
	wrong.Artifact.Digest = "sha256:7777777777777777777777777777777777777777777777777777777777777777"
	wrong.Artifact.CommitDigest = strings.Repeat("b", 40)
	wrong.Artifact.Version = "26.09.07.12.00.01"
	wrong.Artifact.ReleaseManifestDigest = "sha256:8888888888888888888888888888888888888888888888888888888888888888"
	if _, err := f.ext.ReportV2(ctx, f.reporter, handoff.HandoffID, "wrong-built-artifact", secret,
		externalstage.ReportRequestV2{Sequence: 3, State: externalstage.HandoffStateSucceeded, ObservedAt: wrong.ObservedAt,
			PharosEvidence: &wrong}); !errors.Is(err, externalstage.ErrInvalid) {
		t.Fatalf("wrong built artifact err=%v", err)
	}
	if snapshotStage(f.snapshot(), delivery.StageDeployment).PolicySatisfied {
		t.Fatal("wrong artifact completed deployment")
	}
	wrongEnv := f.artifact(externalstage.EvidenceKindDeployment)
	wrongEnv.ObservedAt = f.now.Add(time.Second).Format(time.RFC3339Nano)
	wrongEnv.Environment = "staging-us1"
	if _, err := f.ext.ReportV2(ctx, f.reporter, handoff.HandoffID, "wrong-environment", secret,
		externalstage.ReportRequestV2{Sequence: 3, State: externalstage.HandoffStateSucceeded, ObservedAt: wrongEnv.ObservedAt,
			PharosEvidence: &wrongEnv}); !errors.Is(err, externalstage.ErrInvalid) {
		t.Fatalf("wrong environment err=%v", err)
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
	got = f.authorizeHandoff()
	verify := f.liveHandoff(delivery.StageVerification)
	if verify.HandoffID == "" {
		t.Fatalf("verification handoff missing progress=%+v", got.Progress)
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
	wrongVerify := f.artifact(externalstage.EvidenceKindVerification)
	wrongVerify.ObservedAt = later
	wrongVerify.Artifact.Digest = "sha256:9999999999999999999999999999999999999999999999999999999999999999"
	if _, err := f.ext.ReportV2(ctx, f.reporter, verify.HandoffID, "wrong-artifact", verifySecret,
		externalstage.ReportRequestV2{Sequence: 3, State: externalstage.HandoffStateSucceeded, ObservedAt: wrongVerify.ObservedAt,
			PharosEvidence: &wrongVerify}); !errors.Is(err, externalstage.ErrInvalid) {
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
	f := openAgentBridgeFixture(t, ModeAssisted)
	f.registerPharos()
	f.reportBuildAndQA()
	ctx := context.Background()
	f.svc.failAfter = "deployment_activate"
	if _, err := f.svc.Reconcile(ctx, f.actor, f.projectID, f.batch.ID); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("crash err=%v", err)
	}
	f.svc.failAfter = ""
	got := f.authorizeHandoff()
	if got.Progress.Handoff == nil || got.Progress.Handoff.HandoffID == "" {
		t.Fatalf("handoff after crash recovery=%+v", got.Progress)
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
	manual := openBridgeFixture(t)
	manual.reportBuildAndQA()
	got, err := manual.svc.GetBatch(context.Background(), manual.actor, manual.projectID, manual.batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Progress.SetupRequired != SetupRequiredPharosRegistration || got.Progress.NextAction != NextActionExternalStageCLI {
		t.Fatalf("manual missing registration progress=%+v", got.Progress)
	}
}

func TestBridgeAssistedUnavailableHandoffConfig(t *testing.T) {
	f := openAgentBridgeFixture(t, ModeAssisted)
	f.reportBuildAndQA()
	blocked, err := f.svc.GetBatch(context.Background(), f.actor, f.projectID, f.batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if blocked.Progress.SetupRequired != SetupRequiredPharosRegistration {
		t.Fatalf("assisted missing registration progress=%+v", blocked.Progress)
	}
	f.svc.External = nil
	blocked, err = f.svc.GetBatch(context.Background(), f.actor, f.projectID, f.batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if blocked.Progress.SetupRequired != SetupRequiredHandoffConfig || blocked.Status != BatchBlocked {
		t.Fatalf("unavailable handoff config progress=%+v status=%s", blocked.Progress, blocked.Status)
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
	f := openAgentBridgeFixture(t, ModeAssisted)
	registrationID := f.registerPharos()
	f.reportBuildAndQA()
	ctx := context.Background()
	f.authorizeHandoff()
	if _, err := f.ext.ActivateOwner(ctx, f.operator, fmt.Sprintf("issue:%d", f.batch.IssueID), f.batch.BatchKey+":deployment:activate-again",
		externalstage.ActivateOwnerRequest{
			ReporterRegistrationID: registrationID, StageKey: delivery.StageDeployment,
			ExpectedAttemptNumber: 1, ExpectedPlanRevision: 1, ExpectedCurrentExecution: 0, ExpectedCurrentAuthorityEpoch: 0,
		}); !errors.Is(err, externalstage.ErrConflict) {
		t.Fatalf("second activation err=%v", err)
	}
}

func TestBridgeAssistedReconcileCreatesHandoff(t *testing.T) {
	f := openAgentBridgeFixture(t, ModeAssisted)
	f.registerPharos()
	f.reportBuildAndQA()
	ctx := context.Background()
	got := f.authorizeHandoff()
	if got.Progress.Handoff == nil || got.Progress.Handoff.HandoffID == "" {
		t.Fatalf("assisted reconcile handoff=%+v", got.Progress)
	}
	replay, err := f.svc.Reconcile(ctx, f.actor, f.projectID, f.batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if replay.Progress.Handoff == nil || replay.Progress.Handoff.HandoffID != got.Progress.Handoff.HandoffID {
		t.Fatalf("assisted replay=%+v", replay.Progress.Handoff)
	}
	keyActor := f.operatorWriteKey()
	if _, err := f.svc.Reconcile(ctx, keyActor, f.projectID, f.batch.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("assisted api key err=%v", err)
	}
}

func TestBridgeAutomaticAPIKeyReconcile(t *testing.T) {
	f := openAgentBridgeFixture(t, ModeAutomatic)
	f.registerPharos()
	f.reportBuildAndQA()
	ctx := context.Background()
	f.authorizeHandoff()
	keyActor := f.operatorWriteKey()
	got, err := f.svc.Reconcile(ctx, keyActor, f.projectID, f.batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Progress.Handoff == nil || got.Progress.Handoff.HandoffID == "" {
		t.Fatalf("automatic api-key reconcile handoff=%+v", got.Progress)
	}
	if _, err := appdb.DB.Exec(`UPDATE api_keys SET expires_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','-1 hour') WHERE id=?`, keyActor.APIKeyID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Reconcile(ctx, keyActor, f.projectID, f.batch.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("expired api key err=%v", err)
	}
}

func TestBridgeDoesNotDefaultSealEmptyJanusSet(t *testing.T) {
	f := openAgentBridgeFixture(t, ModeAssisted)
	f.registerPharos()
	janusID := f.registerJanus()
	f.reportBuildAndQA()
	ctx := context.Background()
	got, err := f.svc.Reconcile(ctx, f.actor, f.projectID, f.batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if f.sealedCount(delivery.StageDeployment) != 1 {
		t.Fatalf("declared_count=%d progress=%+v", f.sealedCount(delivery.StageDeployment), got.Progress)
	}
	if _, err := f.ext.SealPrerequisites(ctx, f.operator, fmt.Sprintf("issue:%d", f.batch.IssueID),
		"operator-empty-after-janus", externalstage.SealPrerequisitesRequest{
			StageKey: delivery.StageDeployment, ExecutionNumber: currentGeneration(f.snapshot(), delivery.StageDeployment).ExecutionNumber,
			ExpectedPlanRevision:   f.snapshot().PlanRevision,
			ExpectedAuthorityEpoch: currentGeneration(f.snapshot(), delivery.StageDeployment).AuthorityEpoch,
			Prerequisites:          []externalstage.Prerequisite{},
		}); !errors.Is(err, externalstage.ErrConflict) {
		t.Fatalf("empty reseal after Janus err=%v", err)
	}
	if janusID == 0 {
		t.Fatal("janus registration missing")
	}
}

func TestBridgeOperatorEmptySealThenHandoff(t *testing.T) {
	f := openAgentBridgeFixture(t, ModeAssisted)
	f.registerPharos()
	f.reportBuildAndQA()
	ctx := context.Background()
	if _, err := f.svc.Reconcile(ctx, f.actor, f.projectID, f.batch.ID); err != nil {
		t.Fatal(err)
	}
	got, err := f.svc.GetBatch(ctx, f.actor, f.projectID, f.batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Progress.SetupRequired != SetupRequiredPrerequisiteSeal || f.liveHandoff(delivery.StageDeployment).HandoffID != "" {
		t.Fatalf("expected setup-required empty seal, got %+v", got.Progress)
	}
	got = f.authorizeHandoff()
	if got.Progress.Handoff == nil {
		t.Fatalf("operator-decided empty seal did not unlock handoff: %+v", got.Progress)
	}
}

func TestBridgeReusesOperatorPresealAndCrossPrincipalCrash(t *testing.T) {
	f := openAgentBridgeFixture(t, ModeAssisted)
	registrationID := f.registerPharos()
	janusID := f.registerJanus()
	f.reportBuildAndQA()
	ctx := context.Background()
	deliveryKey := fmt.Sprintf("issue:%d", f.batch.IssueID)
	activation, err := f.ext.ActivateOwner(ctx, f.operator, deliveryKey, "operator-preseal-activate",
		externalstage.ActivateOwnerRequest{
			ReporterRegistrationID: registrationID, StageKey: delivery.StageDeployment,
			ExpectedAttemptNumber: 1, ExpectedPlanRevision: 1, ExpectedCurrentExecution: 0, ExpectedCurrentAuthorityEpoch: 0,
		})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.ext.SealPrerequisites(ctx, f.operator, deliveryKey, "operator-preseal",
		externalstage.SealPrerequisitesRequest{
			StageKey: delivery.StageDeployment, ExecutionNumber: activation.ExecutionNumber,
			ExpectedPlanRevision: 1, ExpectedAuthorityEpoch: activation.AuthorityEpoch,
			Prerequisites: []externalstage.Prerequisite{{
				DependencyKey: "cluster-admission", ReporterRegistrationID: janusID, Requirement: externalstage.PrerequisiteRequired,
			}},
		}); err != nil {
		t.Fatal(err)
	}
	got, err := f.svc.Reconcile(ctx, f.actor, f.projectID, f.batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Progress.Handoff == nil {
		t.Fatalf("preseal reconcile %+v", got.Progress)
	}
}

func TestBridgeCrossPrincipalCrashAfterSeal(t *testing.T) {
	g := openAgentBridgeFixture(t, ModeAssisted)
	g.registerPharos()
	g.registerJanus()
	g.reportBuildAndQA()
	ctx := context.Background()
	g.svc.failAfter = "deployment_prereq"
	if _, err := g.svc.Reconcile(ctx, g.actor, g.projectID, g.batch.ID); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("prereq crash err=%v", err)
	}
	g.svc.failAfter = ""
	editor, _ := g.secondEditor()
	recovered, err := g.svc.Reconcile(ctx, editor, g.projectID, g.batch.ID)
	if err != nil {
		t.Fatalf("cross-principal recovery: %v", err)
	}
	if recovered.Progress.Handoff == nil {
		t.Fatalf("cross-principal recovery progress=%+v", recovered.Progress)
	}
}

func TestBridgeRetryDoesNotProjectStaleHandoff(t *testing.T) {
	f := openAgentBridgeFixture(t, ModeAssisted)
	f.registerPharos()
	f.reportBuildAndQA()
	ctx := context.Background()
	first := f.authorizeHandoff()
	staleID := first.Progress.Handoff.HandoffID
	reporter := delivery.Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", f.actor.UserID)}
	if _, err := f.delivery.StartAttempt(ctx, delivery.AttemptRequest{
		IssueID: f.batch.IssueID, Actor: reporter, Policies: delivery.DefaultPolicy(),
		ReasonCode: "retry", ReasonText: "new plan", IdempotencyKey: f.batch.BatchKey + ":retry",
	}); err != nil {
		t.Fatal(err)
	}
	f.reportSpecificationOn(2, ":a2")
	f.reportBuildAndQAOn(2, ":a2")
	got, err := f.svc.GetBatch(ctx, f.actor, f.projectID, f.batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Progress.Handoff != nil && got.Progress.Handoff.HandoffID == staleID {
		t.Fatalf("projected stale handoff %s", staleID)
	}
	if got.Progress.NextAction == NextActionMintHandoffSecret {
		t.Fatal("offered mint for a superseded generation")
	}
	next := f.authorizeHandoff()
	if next.Progress.Handoff == nil || next.Progress.Handoff.HandoffID == staleID {
		t.Fatalf("retry handoff=%+v stale=%s", next.Progress.Handoff, staleID)
	}
}

func TestBridgeRevokedHandoffIsActionable(t *testing.T) {
	f := openAgentBridgeFixture(t, ModeAssisted)
	f.registerPharos()
	f.reportBuildAndQA()
	ctx := context.Background()
	got := f.authorizeHandoff()
	if _, err := f.ext.Revoke(ctx, f.operator, got.Progress.Handoff.HandoffID, "revoke-current", 0); err != nil {
		t.Fatal(err)
	}
	blocked, err := f.svc.GetBatch(ctx, f.actor, f.projectID, f.batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if blocked.Progress.SetupRequired != SetupRequiredHandoffRevoked || blocked.Progress.NextAction != NextActionRotateHandoff {
		t.Fatalf("revoked projection=%+v", blocked.Progress)
	}
	rotated, err := f.svc.Reconcile(ctx, f.actor, f.projectID, f.batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.Progress.Handoff == nil || rotated.Progress.Handoff.HandoffID == got.Progress.Handoff.HandoffID {
		t.Fatalf("rotation did not create a new handoff: %+v", rotated.Progress)
	}
}

func TestBridgeInvalidSpecificationAsksForHumanReview(t *testing.T) {
	f := openAgentBridgeFixture(t, ModeAssisted)
	if _, err := appdb.DB.Exec(`UPDATE issues SET description=? WHERE id=?`, "material scope change", f.batch.IssueID); err != nil {
		t.Fatal(err)
	}
	got, err := f.svc.GetBatch(context.Background(), f.actor, f.projectID, f.batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Progress.NextAction != NextActionHumanReview {
		t.Fatalf("next_action=%q progress=%+v", got.Progress.NextAction, got.Progress)
	}
}

func TestBridgeExpiredHandoffSecretRefusesReport(t *testing.T) {
	f := openAgentBridgeFixture(t, ModeAssisted)
	f.registerPharos()
	f.reportBuildAndQA()
	ctx := context.Background()
	got := f.authorizeHandoff()
	secret, err := f.ext.Mint(ctx, f.operator, got.Progress.Handoff.HandoffID, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	observed := f.now.Format(time.RFC3339Nano)
	if _, err := f.ext.Accept(ctx, f.reporter, got.Progress.Handoff.HandoffID, "accept-expired", secret,
		externalstage.AcceptRequest{Sequence: 1, ObservedAt: observed}); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(25 * time.Hour)
	evidence := f.artifact(externalstage.EvidenceKindDeployment)
	evidence.ObservedAt = f.now.Format(time.RFC3339Nano)
	if _, err := f.ext.ReportV2(ctx, f.reporter, got.Progress.Handoff.HandoffID, "expired-secret", secret,
		externalstage.ReportRequestV2{Sequence: 2, State: externalstage.HandoffStateSucceeded, ObservedAt: evidence.ObservedAt,
			PharosEvidence: &evidence}); err == nil {
		t.Fatal("expired secret was accepted")
	}
}

func TestBridgeEmptySealThenNewJanusRequiresReview(t *testing.T) {
	f := openAgentBridgeFixture(t, ModeAssisted)
	f.registerPharos()
	f.reportBuildAndQA()
	ctx := context.Background()
	if _, err := f.svc.Reconcile(ctx, f.actor, f.projectID, f.batch.ID); err != nil {
		t.Fatal(err)
	}
	got := f.authorizeHandoff()
	if got.Progress.Handoff == nil {
		t.Fatal("expected handoff after operator empty seal")
	}
	f.registerJanus()
	blocked, err := f.svc.GetBatch(ctx, f.actor, f.projectID, f.batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if blocked.Progress.SetupRequired != SetupRequiredPrerequisiteReview {
		t.Fatalf("after new Janus setup=%q progress=%+v", blocked.Progress.SetupRequired, blocked.Progress)
	}
	if _, err := f.svc.Reconcile(ctx, f.actor, f.projectID, f.batch.ID); err != nil {
		t.Fatal(err)
	}
	if f.liveHandoff(delivery.StageDeployment).HandoffID != got.Progress.Handoff.HandoffID {
		t.Fatal("reconcile replaced the handoff after Janus divergence")
	}
}

func TestBridgeRequiredSealThenAdditionalJanusRequiresReview(t *testing.T) {
	f := openAgentBridgeFixture(t, ModeAssisted)
	f.registerPharos()
	f.registerJanus()
	f.reportBuildAndQA()
	ctx := context.Background()
	got, err := f.svc.Reconcile(ctx, f.actor, f.projectID, f.batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Progress.Handoff == nil {
		t.Fatalf("expected required Janus handoff, got %+v", got.Progress)
	}
	f.registerJanusAs("extra-admission", "extra")
	blocked, err := f.svc.GetBatch(ctx, f.actor, f.projectID, f.batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if blocked.Progress.SetupRequired != SetupRequiredPrerequisiteReview {
		t.Fatalf("additional Janus setup=%q progress=%+v", blocked.Progress.SetupRequired, blocked.Progress)
	}
}

func TestBridgeIssuedHandoffThenJanusChangeRefusesOwner(t *testing.T) {
	f := openAgentBridgeFixture(t, ModeAssisted)
	f.registerPharos()
	f.reportBuildAndQA()
	ctx := context.Background()
	got := f.authorizeHandoff()
	janusID := f.registerJanus()
	secret, err := f.ext.Mint(ctx, f.operator, got.Progress.Handoff.HandoffID, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	observed := f.now.Format(time.RFC3339Nano)
	if _, err := f.ext.Accept(ctx, f.reporter, got.Progress.Handoff.HandoffID, "accept-divergent", secret,
		externalstage.AcceptRequest{Sequence: 1, ObservedAt: observed}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ext.Report(ctx, f.reporter, got.Progress.Handoff.HandoffID, "active-divergent", secret,
		externalstage.ReportRequest{Sequence: 2, State: externalstage.HandoffStateActive, ObservedAt: observed}); err != nil {
		t.Fatal(err)
	}
	evidence := f.artifact(externalstage.EvidenceKindDeployment)
	evidence.ObservedAt = observed
	if _, err := f.ext.ReportV2(ctx, f.reporter, got.Progress.Handoff.HandoffID, "owner-while-diverged", secret,
		externalstage.ReportRequestV2{Sequence: 3, State: externalstage.HandoffStateSucceeded, ObservedAt: observed,
			PharosEvidence: &evidence}); !errors.Is(err, externalstage.ErrConflict) {
		t.Fatalf("diverged owner success err=%v", err)
	}
	if snapshotStage(f.snapshot(), delivery.StageDeployment).PolicySatisfied {
		t.Fatal("owner success completed after Janus divergence")
	}
	if _, err := f.ext.RevokeReporter(ctx, f.operator, fmt.Sprintf("issue:%d", f.batch.IssueID), "revoke-extra-janus", janusID); err != nil {
		t.Fatal(err)
	}
	restored, err := f.svc.GetBatch(ctx, f.actor, f.projectID, f.batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Progress.SetupRequired == SetupRequiredPrerequisiteReview {
		t.Fatalf("revoke did not restore match: %+v", restored.Progress)
	}
}

func TestBridgeRetryAfterJanusDivergenceSealsNewGeneration(t *testing.T) {
	f := openAgentBridgeFixture(t, ModeAssisted)
	f.registerPharos()
	f.reportBuildAndQA()
	ctx := context.Background()
	_ = f.authorizeHandoff()
	f.registerJanus()
	reporter := delivery.Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", f.actor.UserID)}
	if _, err := f.delivery.StartAttempt(ctx, delivery.AttemptRequest{
		IssueID: f.batch.IssueID, Actor: reporter, Policies: delivery.DefaultPolicy(),
		ReasonCode: "retry", ReasonText: "new required Janus", IdempotencyKey: f.batch.BatchKey + ":retry-janus",
	}); err != nil {
		t.Fatal(err)
	}
	f.reportSpecificationOn(2, ":retry-janus")
	f.reportBuildAndQAOn(2, ":retry-janus")
	got, err := f.svc.Reconcile(ctx, f.actor, f.projectID, f.batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if f.sealedCount(delivery.StageDeployment) != 1 {
		t.Fatalf("retry did not seal current Janus declared_count=%d progress=%+v", f.sealedCount(delivery.StageDeployment), got.Progress)
	}
	if got.Progress.Handoff == nil || got.Progress.SetupRequired == SetupRequiredPrerequisiteReview {
		t.Fatalf("retry handoff %+v", got.Progress)
	}
}

func TestBridgePartialBuiltIdentityBlocksHandoff(t *testing.T) {
	f := openAgentBridgeFixture(t, ModeAssisted)
	f.registerPharos()
	f.reportPartialBuildAndQA([]delivery.Evidence{
		{Type: "implementation_result", Outcome: "passed", ReferenceKind: "commit", ReferenceValue: bridgeCommit},
		{Type: "artifact", Outcome: "passed", ReferenceKind: "external_ref", ReferenceValue: "suite:not-release-identity"},
	})
	got, err := f.svc.GetBatch(context.Background(), f.actor, f.projectID, f.batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Progress.SetupRequired != SetupRequiredBuiltArtifact || f.liveHandoff(delivery.StageDeployment).HandoffID != "" {
		t.Fatalf("partial identity progress=%+v", got.Progress)
	}
	if _, err := f.svc.Reconcile(context.Background(), f.actor, f.projectID, f.batch.ID); err != nil {
		t.Fatal(err)
	}
	if f.liveHandoff(delivery.StageDeployment).HandoffID != "" {
		t.Fatal("reconcile handed off incomplete identity")
	}
}

func TestBridgeMalformedReleaseIdentityBlocksHandoff(t *testing.T) {
	f := openAgentBridgeFixture(t, ModeAssisted)
	f.registerPharos()
	f.reportPartialBuildAndQA([]delivery.Evidence{
		{Type: "implementation_result", Outcome: "passed", ReferenceKind: "commit", ReferenceValue: bridgeCommit},
		{Type: "artifact", Outcome: "passed", ReferenceKind: "digest", DigestSHA256: strings.TrimPrefix(bridgeConfig, "sha256:")},
		{Type: "artifact", Outcome: "passed", ReferenceKind: "external_ref", ReferenceValue: externalstage.FormatOCIManifestRef(bridgeIndex)},
		{Type: "artifact", Outcome: "passed", ReferenceKind: "external_ref", ReferenceValue: externalstage.FormatReleaseCoordinateRef(bridgeCoordinate)},
		{Type: "artifact", Outcome: "passed", ReferenceKind: "external_ref", ReferenceValue: "inspr-calendar-v1:stable:260907120000:" + bridgeVersion},
	})
	got, err := f.svc.GetBatch(context.Background(), f.actor, f.projectID, f.batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Progress.SetupRequired != SetupRequiredBuiltArtifact {
		t.Fatalf("malformed identity setup=%q progress=%+v", got.Progress.SetupRequired, got.Progress)
	}
}

func TestBridgeIndexAndQADigestDoNotFillReleaseManifest(t *testing.T) {
	f := openAgentBridgeFixture(t, ModeAssisted)
	f.registerPharos()
	ctx := context.Background()
	reporter := delivery.Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", f.actor.UserID)}
	impl, err := f.delivery.StartStageRetry(ctx, delivery.StageStartRequest{
		IssueID: f.batch.IssueID, AttemptNumber: 1, StageKey: delivery.StageImplementation, Reporter: reporter,
		ReasonCode: "implementation_start", IdempotencyKey: f.batch.BatchKey + ":impl:start",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.delivery.ReportStage(ctx, delivery.StageReport{
		IssueID: f.batch.IssueID, AttemptNumber: 1, StageKey: delivery.StageImplementation,
		ExecutionNumber: impl.ExecutionNumber, AuthorityEpoch: impl.AuthorityEpoch, Reporter: reporter,
		IdempotencyKey: f.batch.BatchKey + ":impl:report", Kind: "semantic", State: "succeeded",
		Evidence: []delivery.Evidence{
			{Type: "implementation_result", Outcome: "passed", ReferenceKind: "commit", ReferenceValue: bridgeCommit},
			{Type: "artifact", Outcome: "passed", ReferenceKind: "digest", DigestSHA256: strings.TrimPrefix(bridgeConfig, "sha256:")},
			{Type: "artifact", Outcome: "passed", ReferenceKind: "external_ref", ReferenceValue: externalstage.FormatOCIManifestRef(bridgeIndex)},
			{Type: "artifact", Outcome: "passed", ReferenceKind: "external_ref", ReferenceValue: externalstage.FormatReleaseCoordinateRef(bridgeCoordinate)},
			{Type: "artifact", Outcome: "passed", ReferenceKind: "external_ref",
				ReferenceValue: externalstage.FormatReleaseIdentity(externalstage.VersionSchemeINSPRCalendar, bridgeChannel, bridgeSequence, bridgeVersion)},
		},
		ReasonCode: "implementation_result",
	}); err != nil {
		t.Fatal(err)
	}
	qa, err := f.delivery.StartStageRetry(ctx, delivery.StageStartRequest{
		IssueID: f.batch.IssueID, AttemptNumber: 1, StageKey: delivery.StageQA, Reporter: reporter,
		ReasonCode: "qa_start", IdempotencyKey: f.batch.BatchKey + ":qa:start",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.delivery.ReportStage(ctx, delivery.StageReport{
		IssueID: f.batch.IssueID, AttemptNumber: 1, StageKey: delivery.StageQA,
		ExecutionNumber: qa.ExecutionNumber, AuthorityEpoch: qa.AuthorityEpoch, Reporter: reporter,
		IdempotencyKey: f.batch.BatchKey + ":qa:report", Kind: "semantic", State: "succeeded",
		Evidence: []delivery.Evidence{
			{Type: "test_result", Outcome: "passed", ReferenceKind: "digest", DigestSHA256: bridgeQADigest},
		},
		ReasonCode: "test_result",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := f.svc.GetBatch(ctx, f.actor, f.projectID, f.batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Progress.SetupRequired != SetupRequiredBuiltArtifact || f.liveHandoff(delivery.StageDeployment).HandoffID != "" {
		t.Fatalf("index/QA filled release-set identity progress=%+v", got.Progress)
	}
	if _, err := f.svc.Reconcile(ctx, f.actor, f.projectID, f.batch.ID); err != nil {
		t.Fatal(err)
	}
	if f.liveHandoff(delivery.StageDeployment).HandoffID != "" {
		t.Fatal("reconcile handed off without release-set digest")
	}
}

func TestBridgeOwnerReportRejectsIndexAsReleaseManifest(t *testing.T) {
	f := openAgentBridgeFixture(t, ModeAssisted)
	f.registerPharos()
	f.reportBuildAndQA()
	ctx := context.Background()
	got := f.authorizeHandoff()
	secret, err := f.ext.Mint(ctx, f.operator, got.Progress.Handoff.HandoffID, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	observed := f.now.Format(time.RFC3339Nano)
	if _, err := f.ext.Accept(ctx, f.reporter, got.Progress.Handoff.HandoffID, "accept-index-as-manifest", secret,
		externalstage.AcceptRequest{Sequence: 1, ObservedAt: observed}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ext.Report(ctx, f.reporter, got.Progress.Handoff.HandoffID, "active-index-as-manifest", secret,
		externalstage.ReportRequest{Sequence: 2, State: externalstage.HandoffStateActive, ObservedAt: observed}); err != nil {
		t.Fatal(err)
	}
	wrong := f.artifact(externalstage.EvidenceKindDeployment)
	wrong.ObservedAt = observed
	wrong.Artifact.ReleaseManifestDigest = bridgeIndex
	if _, err := f.ext.ReportV2(ctx, f.reporter, got.Progress.Handoff.HandoffID, "index-as-release-set", secret,
		externalstage.ReportRequestV2{Sequence: 3, State: externalstage.HandoffStateSucceeded, ObservedAt: observed,
			PharosEvidence: &wrong}); !errors.Is(err, externalstage.ErrInvalid) {
		t.Fatalf("index as release-set err=%v", err)
	}
	if snapshotStage(f.snapshot(), delivery.StageDeployment).PolicySatisfied {
		t.Fatal("OCI index satisfied release-set binding")
	}
	evidence := f.artifact(externalstage.EvidenceKindDeployment)
	evidence.ObservedAt = observed
	if _, err := f.ext.ReportV2(ctx, f.reporter, got.Progress.Handoff.HandoffID, "release-set-match", secret,
		externalstage.ReportRequestV2{Sequence: 3, State: externalstage.HandoffStateSucceeded, ObservedAt: observed,
			PharosEvidence: &evidence}); err != nil {
		t.Fatal(err)
	}
	if !snapshotStage(f.snapshot(), delivery.StageDeployment).PolicySatisfied {
		t.Fatal("matching release-set report did not satisfy deployment")
	}
}

func TestBridgeBaselineOwnerReportRequiresV2(t *testing.T) {
	f := openAgentBridgeFixture(t, ModeAssisted)
	f.registerPharos()
	f.reportBuildAndQA()
	ctx := context.Background()
	got := f.authorizeHandoff()
	secret, err := f.ext.Mint(ctx, f.operator, got.Progress.Handoff.HandoffID, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	observed := f.now.Format(time.RFC3339Nano)
	if _, err := f.ext.Accept(ctx, f.reporter, got.Progress.Handoff.HandoffID, "accept-v1", secret,
		externalstage.AcceptRequest{Sequence: 1, ObservedAt: observed}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ext.Report(ctx, f.reporter, got.Progress.Handoff.HandoffID, "active-v1", secret,
		externalstage.ReportRequest{Sequence: 2, State: externalstage.HandoffStateActive, ObservedAt: observed}); err != nil {
		t.Fatal(err)
	}
	v1 := f.v1Evidence()
	v1.ObservedAt = observed
	if _, err := f.ext.Report(ctx, f.reporter, got.Progress.Handoff.HandoffID, "legacy-v1", secret,
		externalstage.ReportRequest{Sequence: 3, State: externalstage.HandoffStateSucceeded, ObservedAt: observed,
			PharosEvidence: &v1}); !errors.Is(err, externalstage.ErrV2Required) {
		t.Fatalf("baseline v1 report err=%v", err)
	}
	if snapshotStage(f.snapshot(), delivery.StageDeployment).PolicySatisfied {
		t.Fatal("v1 report completed a baseline deployment")
	}
	evidence := f.artifact(externalstage.EvidenceKindDeployment)
	evidence.ObservedAt = observed
	if _, err := f.ext.ReportV2(ctx, f.reporter, got.Progress.Handoff.HandoffID, "v2-match", secret,
		externalstage.ReportRequestV2{Sequence: 3, State: externalstage.HandoffStateSucceeded, ObservedAt: observed,
			PharosEvidence: &evidence}); err != nil {
		t.Fatal(err)
	}
	if !snapshotStage(f.snapshot(), delivery.StageDeployment).PolicySatisfied {
		t.Fatal("matching v2 report did not satisfy deployment")
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
