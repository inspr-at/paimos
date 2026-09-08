// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package baselinebatch

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/auth"
	appdb "github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/delivery"
	"github.com/inspr-at/paimos/backend/externalstage"
)

func calendarReceipt(key string) BuiltReceiptRequest {
	return BuiltReceiptRequest{
		IdempotencyKey: key, ExpectedAttemptID: 1, ExpectedPlanRevision: 1,
		Commit: bridgeCommit, OCIConfigDigest: bridgeConfig, ReleaseManifestDigest: bridgeReleaseSet,
		ReleaseManifestCoordinate: bridgeCoordinate, OCIIndexDigest: bridgeIndex,
		VersionScheme: string(externalstage.VersionSchemeINSPRCalendar), ReleaseChannel: bridgeChannel,
		ReleaseSequence: bridgeSequence, Version: bridgeVersion, QADigest: bridgeQADigest,
	}
}

func legacyReceipt(key string) BuiltReceiptRequest {
	req := calendarReceipt(key)
	req.VersionScheme = string(externalstage.VersionSchemeLegacy)
	req.Version = "0.1.94"
	req.ReleaseChannel = "rollback"
	req.ReleaseSequence = 94
	req.ReleaseManifestCoordinate = "ghcr:inspr-at/pharos/releases/0.1.94"
	return req
}

func TestRecordBuiltReceiptCalendarDoesNotRewriteSpecification(t *testing.T) {
	f := openBridgeFixture(t)
	got, err := f.svc.RecordBuiltReceipt(context.Background(), f.actor, f.projectID, f.batch.ID, calendarReceipt("receipt-calendar-01"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Progress.NextAction == NextActionImplementationEvidence || got.Progress.NextAction == NextActionQAEvidence {
		t.Fatalf("receipt left build/QA next_action: %+v", got.Progress)
	}
	snap := f.snapshot()
	spec := snapshotStage(snap, delivery.StageSpecification)
	if spec.ExecutionNumber != 1 || !spec.PolicySatisfied {
		t.Fatalf("specification rewritten: %+v", spec)
	}
	impl := snapshotStage(snap, delivery.StageImplementation)
	qa := snapshotStage(snap, delivery.StageQA)
	if !impl.PolicySatisfied || !qa.PolicySatisfied {
		t.Fatalf("impl/qa not satisfied: impl=%+v qa=%+v", impl, qa)
	}
	stored := f.stored()
	if stored.DeliveryID == nil || snap.AttemptID == nil {
		t.Fatal("missing delivery identity")
	}
	artifact, err := externalstage.LoadExplicitBuiltArtifact(context.Background(), appdb.DB, *stored.DeliveryID, *snap.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if !artifact.Complete() || artifact.Scheme != string(externalstage.VersionSchemeINSPRCalendar) || artifact.Version != bridgeVersion {
		t.Fatalf("calendar artifact=%+v", artifact)
	}
	if hex.EncodeToString(artifact.ReleaseManifest) == hex.EncodeToString(artifact.OCIIndex) {
		t.Fatal("release-set digest was coerced from the OCI index")
	}
	var runs int
	if err := appdb.DB.QueryRow(`SELECT COUNT(*) FROM agent_runs WHERE issue_id=?`, f.batch.IssueID).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 0 {
		t.Fatalf("receipt minted %d agent_runs", runs)
	}
}

func TestRecordBuiltReceiptLegacyTuple(t *testing.T) {
	f := openBridgeFixture(t)
	got, err := f.svc.RecordBuiltReceipt(context.Background(), f.actor, f.projectID, f.batch.ID, legacyReceipt("receipt-legacy-01"))
	if err != nil {
		t.Fatal(err)
	}
	if snapshotStage(f.snapshot(), delivery.StageImplementation).PolicySatisfied == false ||
		snapshotStage(f.snapshot(), delivery.StageQA).PolicySatisfied == false {
		t.Fatalf("legacy receipt did not satisfy impl/qa: %+v", got.Progress)
	}
	stored := f.stored()
	artifact, err := externalstage.LoadExplicitBuiltArtifact(context.Background(), appdb.DB, *stored.DeliveryID, *f.snapshot().AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Scheme != string(externalstage.VersionSchemeLegacy) || artifact.Version != "0.1.94" || !artifact.Complete() {
		t.Fatalf("legacy artifact=%+v", artifact)
	}
}

func TestRecordBuiltReceiptRejectsMalformedIdentityBeforeMutation(t *testing.T) {
	f := openBridgeFixture(t)
	bad := calendarReceipt("receipt-malformed-01")
	bad.Version = "26.13.40"
	if _, err := f.svc.RecordBuiltReceipt(context.Background(), f.actor, f.projectID, f.batch.ID, bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("malformed calendar version: %v", err)
	}
	bad = calendarReceipt("receipt-malformed-02")
	bad.ReleaseManifestCoordinate = "GHCR:NotACoordinate"
	if _, err := f.svc.RecordBuiltReceipt(context.Background(), f.actor, f.projectID, f.batch.ID, bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("malformed coordinate: %v", err)
	}
	if snapshotStage(f.snapshot(), delivery.StageImplementation).ExecutionNumber != 0 {
		t.Fatal("malformed receipt mutated implementation")
	}
}

func TestRecordBuiltReceiptReplayMatchesOriginalCASNotCurrentLedger(t *testing.T) {
	f := openBridgeFixture(t)
	original := calendarReceipt("receipt-replay-cas-01")
	if _, err := f.svc.RecordBuiltReceipt(context.Background(), f.actor, f.projectID, f.batch.ID, original); err != nil {
		t.Fatal(err)
	}
	impl := snapshotStage(f.snapshot(), delivery.StageImplementation)
	if impl.ExecutionNumber != 1 || impl.AuthorityEpoch != 1 || !impl.PolicySatisfied {
		t.Fatalf("post-receipt implementation=%+v", impl)
	}
	if _, err := f.svc.RecordBuiltReceipt(context.Background(), f.actor, f.projectID, f.batch.ID, original); err != nil {
		t.Fatalf("exact original replay: %v", err)
	}
	mutated := original
	mutated.ExpectedImplementationExecution = 7
	mutated.ExpectedImplementationAuthorityEpoch = 7
	if _, err := f.svc.RecordBuiltReceipt(context.Background(), f.actor, f.projectID, f.batch.ID, mutated); !errors.Is(err, ErrConflict) {
		t.Fatalf("mutated CAS replay: %v", err)
	}
	again := snapshotStage(f.snapshot(), delivery.StageImplementation)
	if again.ExecutionNumber != 1 || again.AuthorityEpoch != 1 || !again.PolicySatisfied {
		t.Fatalf("mutated CAS changed implementation: %+v", again)
	}
	if _, err := f.svc.RecordBuiltReceipt(context.Background(), f.actor, f.projectID, f.batch.ID, original); err != nil {
		t.Fatalf("exact replay after mutated CAS: %v", err)
	}
}

func TestLoadOwnedExecutionMissingIntentRefuses(t *testing.T) {
	f := openBridgeFixture(t)
	tx, err := appdb.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	empty, err := loadOwnedExecution(context.Background(), tx, f.projectID, "")
	if err != nil || empty.IntentState != "" {
		t.Fatalf("manual empty intent=%+v err=%v", empty, err)
	}
	_, err = loadOwnedExecution(context.Background(), tx, f.projectID, "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee")
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("missing intent: %v", err)
	}
}

func TestRecordBuiltReceiptDoesNotCoerceIndexIntoReleaseSet(t *testing.T) {
	f := openBridgeFixture(t)
	req := calendarReceipt("receipt-no-coerce-01")
	if _, err := f.svc.RecordBuiltReceipt(context.Background(), f.actor, f.projectID, f.batch.ID, req); err != nil {
		t.Fatal(err)
	}
	stored := f.stored()
	artifact, err := externalstage.LoadExplicitBuiltArtifact(context.Background(), appdb.DB, *stored.DeliveryID, *f.snapshot().AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	wantManifest := "6666666666666666666666666666666666666666666666666666666666666666"
	wantIndex := "4444444444444444444444444444444444444444444444444444444444444444"
	if hex.EncodeToString(artifact.ReleaseManifest) != wantManifest || hex.EncodeToString(artifact.OCIIndex) != wantIndex {
		t.Fatalf("coerced digests manifest=%s index=%s", hex.EncodeToString(artifact.ReleaseManifest), hex.EncodeToString(artifact.OCIIndex))
	}
}

func TestRecordBuiltReceiptAtomicRollback(t *testing.T) {
	f := openBridgeFixture(t)
	f.svc.failAfter = "built_receipt_impl"
	if _, err := f.svc.RecordBuiltReceipt(context.Background(), f.actor, f.projectID, f.batch.ID, calendarReceipt("receipt-atomic-01")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("injected crash: %v", err)
	}
	f.svc.failAfter = ""
	snap := f.snapshot()
	if snapshotStage(snap, delivery.StageImplementation).PolicySatisfied || snapshotStage(snap, delivery.StageQA).PolicySatisfied {
		t.Fatalf("half-written receipt survived rollback: %+v", snap.Stages)
	}
	if _, err := f.svc.RecordBuiltReceipt(context.Background(), f.actor, f.projectID, f.batch.ID, calendarReceipt("receipt-atomic-01")); err != nil {
		t.Fatalf("retry after crash: %v", err)
	}
}

func TestRecordBuiltReceiptRefusesActiveQASteal(t *testing.T) {
	f := openBridgeFixture(t)
	reporter := delivery.Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", f.actor.UserID)}
	impl, err := f.delivery.StartStageRetry(context.Background(), delivery.StageStartRequest{
		IssueID: f.batch.IssueID, AttemptNumber: 1, StageKey: delivery.StageImplementation, Reporter: reporter,
		ReasonCode: "implementation_start", IdempotencyKey: "foreign-impl-start-01",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.delivery.ReportStage(context.Background(), delivery.StageReport{
		IssueID: f.batch.IssueID, AttemptNumber: 1, StageKey: delivery.StageImplementation,
		ExecutionNumber: impl.ExecutionNumber, AuthorityEpoch: impl.AuthorityEpoch, Reporter: reporter,
		IdempotencyKey: "foreign-impl-report-01", Kind: "semantic", State: "succeeded",
		Evidence: []delivery.Evidence{
			{Type: "implementation_result", Outcome: "passed", ReferenceKind: "commit", ReferenceValue: bridgeCommit},
			{Type: "artifact", Outcome: "passed", ReferenceKind: "digest", DigestSHA256: "3333333333333333333333333333333333333333333333333333333333333333"},
			{Type: "artifact", Outcome: "passed", ReferenceKind: "external_ref", ReferenceValue: externalstage.FormatReleaseManifestRef(bridgeReleaseSet)},
			{Type: "artifact", Outcome: "passed", ReferenceKind: "external_ref", ReferenceValue: externalstage.FormatReleaseCoordinateRef(bridgeCoordinate)},
			{Type: "artifact", Outcome: "passed", ReferenceKind: "external_ref",
				ReferenceValue: externalstage.FormatReleaseIdentity(externalstage.VersionSchemeINSPRCalendar, bridgeChannel, bridgeSequence, bridgeVersion)},
		},
		ReasonCode: "implementation_result",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.delivery.StartStageRetry(context.Background(), delivery.StageStartRequest{
		IssueID: f.batch.IssueID, AttemptNumber: 1, StageKey: delivery.StageQA, Reporter: reporter,
		ReasonCode: "qa_start", IdempotencyKey: "foreign-qa-start-01",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.RecordBuiltReceipt(context.Background(), f.actor, f.projectID, f.batch.ID, calendarReceipt("receipt-qa-steal-01")); !errors.Is(err, ErrConflict) {
		t.Fatalf("stole active QA: %v", err)
	}
	if snapshotStage(f.snapshot(), delivery.StageQA).PolicySatisfied {
		t.Fatal("QA steal succeeded the foreign execution")
	}
}

func TestRecordBuiltReceiptExactReplayAndConflict(t *testing.T) {
	f := openBridgeFixture(t)
	req := calendarReceipt("receipt-replay-01")
	first, err := f.svc.RecordBuiltReceipt(context.Background(), f.actor, f.projectID, f.batch.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.svc.RecordBuiltReceipt(context.Background(), f.actor, f.projectID, f.batch.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || !snapshotStage(f.snapshot(), delivery.StageImplementation).PolicySatisfied {
		t.Fatalf("replay lost the receipt: first=%d second=%d", first.ID, second.ID)
	}
	conflict := req
	conflict.Commit = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if _, err := f.svc.RecordBuiltReceipt(context.Background(), f.actor, f.projectID, f.batch.ID, conflict); !errors.Is(err, ErrConflict) {
		t.Fatalf("same key different tuple: %v", err)
	}
	stale := calendarReceipt("receipt-replay-02")
	if _, err := f.svc.RecordBuiltReceipt(context.Background(), f.actor, f.projectID, f.batch.ID, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale replacement: %v", err)
	}
}

func TestRecordBuiltReceiptRefusesCancelledAndPaused(t *testing.T) {
	paused := openBridgeFixture(t)
	if _, err := paused.svc.Control(context.Background(), paused.actor, paused.projectID, paused.batch.ID, ControlRequest{Action: "pause"}); err != nil {
		t.Fatal(err)
	}
	if _, err := paused.svc.RecordBuiltReceipt(context.Background(), paused.actor, paused.projectID, paused.batch.ID, calendarReceipt("receipt-paused-01")); !errors.Is(err, ErrConflict) {
		t.Fatalf("paused receipt: %v", err)
	}
	cancelled := openBridgeFixture(t)
	if _, err := cancelled.svc.Control(context.Background(), cancelled.actor, cancelled.projectID, cancelled.batch.ID, ControlRequest{Action: "cancel"}); err != nil {
		t.Fatal(err)
	}
	if _, err := cancelled.svc.RecordBuiltReceipt(context.Background(), cancelled.actor, cancelled.projectID, cancelled.batch.ID, calendarReceipt("receipt-cancelled-01")); !errors.Is(err, ErrConflict) {
		t.Fatalf("cancelled receipt: %v", err)
	}
}

func TestRecordBuiltReceiptReplayDoesNotReviveCancelled(t *testing.T) {
	f := openBridgeFixture(t)
	req := calendarReceipt("receipt-revoke-01")
	if _, err := f.svc.RecordBuiltReceipt(context.Background(), f.actor, f.projectID, f.batch.ID, req); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Control(context.Background(), f.actor, f.projectID, f.batch.ID, ControlRequest{Action: "cancel"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.RecordBuiltReceipt(context.Background(), f.actor, f.projectID, f.batch.ID, req); !errors.Is(err, ErrConflict) {
		t.Fatalf("cancelled replay: %v", err)
	}
}

func TestRecordBuiltReceiptStaleCAS(t *testing.T) {
	f := openBridgeFixture(t)
	req := calendarReceipt("receipt-stale-cas-01")
	req.ExpectedAttemptID = 9
	if _, err := f.svc.RecordBuiltReceipt(context.Background(), f.actor, f.projectID, f.batch.ID, req); !errors.Is(err, ErrStale) {
		t.Fatalf("wrong attempt: %v", err)
	}
	req = calendarReceipt("receipt-stale-cas-02")
	req.ExpectedPlanRevision = 9
	if _, err := f.svc.RecordBuiltReceipt(context.Background(), f.actor, f.projectID, f.batch.ID, req); !errors.Is(err, ErrStale) {
		t.Fatalf("wrong plan: %v", err)
	}
}

func TestRecordBuiltReceiptAutomaticAfterWorkerComplete(t *testing.T) {
	f := openAgentBridgeFixture(t, ModeAutomatic)
	if f.batch.Progress.IntentState != "completed" {
		t.Fatalf("automatic start intent=%q, want completed so receipt is post-work", f.batch.Progress.IntentState)
	}
	got, err := f.svc.RecordBuiltReceipt(context.Background(), f.actor, f.projectID, f.batch.ID, calendarReceipt("receipt-auto-human-01"))
	if err != nil {
		t.Fatal(err)
	}
	if !snapshotStage(f.snapshot(), delivery.StageQA).PolicySatisfied {
		t.Fatalf("post-work human receipt failed: %+v", got.Progress)
	}
}

func TestRecordBuiltReceiptAutomaticAPIKeyBoundToStarter(t *testing.T) {
	f := openAgentBridgeFixture(t, ModeAutomatic)
	keyID := insertReceiptAPIKey(t, f.userID, "agent-controls:write")
	machine := Actor{Kind: string(auth.PrincipalAPIKey), UserID: f.userID, APIKeyID: keyID}
	bound := calendarReceipt("receipt-auto-key-01")
	bound.ExpectedAccountKey = f.batch.Worker.AccountKey
	bound.ExpectedRuntimeGeneration = f.batch.Worker.RuntimeGeneration
	if _, err := f.svc.RecordBuiltReceipt(context.Background(), machine, f.projectID, f.batch.ID, bound); err != nil {
		t.Fatal(err)
	}
	var reporterType string
	if err := appdb.DB.QueryRow(`SELECT reporter_type FROM delivery_reporters WHERE opaque_key=?`,
		fmt.Sprintf("paimos:machine:%d", keyID)).Scan(&reporterType); err != nil {
		t.Fatal(err)
	}
	if reporterType != "system" {
		t.Fatalf("machine report labelled %s", reporterType)
	}
}

func TestRecordBuiltReceiptAutomaticAPIKeyOmittingNamedAccountIsForbidden(t *testing.T) {
	f := openAgentBridgeFixture(t, ModeAutomatic)
	if f.batch.Worker.AccountKey == "" {
		t.Fatal("named fixture stored an empty account key")
	}
	keyID := insertReceiptAPIKey(t, f.userID, "agent-controls:write")
	machine := Actor{Kind: string(auth.PrincipalAPIKey), UserID: f.userID, APIKeyID: keyID}
	missing := calendarReceipt("receipt-auto-omit-named-01")
	missing.ExpectedRuntimeGeneration = f.batch.Worker.RuntimeGeneration
	if _, err := f.svc.RecordBuiltReceipt(context.Background(), machine, f.projectID, f.batch.ID, missing); !errors.Is(err, ErrForbidden) {
		t.Fatalf("omitted named account: %v", err)
	}
	if snapshotStage(f.snapshot(), delivery.StageImplementation).PolicySatisfied {
		t.Fatal("omitted named account mutated implementation")
	}
}

func TestRecordBuiltReceiptAutomaticClassOnlyAPIKey(t *testing.T) {
	f := openClassOnlyAgentBridgeFixture(t, ModeAutomatic)
	if f.batch.Worker.AccountLabel != "claude_ai_max" || f.batch.Worker.AccountKey != "" {
		t.Fatalf("class-only stored %+v", f.batch.Worker)
	}
	starter := insertReceiptAPIKey(t, f.userID, "agent-controls:write")
	machine := Actor{Kind: string(auth.PrincipalAPIKey), UserID: f.userID, APIKeyID: starter}

	invented := calendarReceipt("receipt-auto-class-invented-01")
	invented.ExpectedAccountKey = "claude-home"
	invented.ExpectedRuntimeGeneration = f.batch.Worker.RuntimeGeneration
	if _, err := f.svc.RecordBuiltReceipt(context.Background(), machine, f.projectID, f.batch.ID, invented); !errors.Is(err, ErrForbidden) {
		t.Fatalf("invented class-only key: %v", err)
	}

	wrongGen := calendarReceipt("receipt-auto-class-wrong-gen-01")
	wrongGen.ExpectedRuntimeGeneration = "not-this-generation"
	if _, err := f.svc.RecordBuiltReceipt(context.Background(), machine, f.projectID, f.batch.ID, wrongGen); !errors.Is(err, ErrForbidden) {
		t.Fatalf("wrong generation: %v", err)
	}

	missingGen := calendarReceipt("receipt-auto-class-missing-gen-01")
	if _, err := f.svc.RecordBuiltReceipt(context.Background(), machine, f.projectID, f.batch.ID, missingGen); !errors.Is(err, ErrForbidden) {
		t.Fatalf("missing generation: %v", err)
	}

	other, _ := f.secondEditor()
	otherKey := insertReceiptAPIKey(t, other.UserID, "agent-controls:write")
	foreign := calendarReceipt("receipt-auto-class-foreign-01")
	foreign.ExpectedRuntimeGeneration = f.batch.Worker.RuntimeGeneration
	if _, err := f.svc.RecordBuiltReceipt(context.Background(), Actor{Kind: string(auth.PrincipalAPIKey), UserID: other.UserID, APIKeyID: otherKey},
		f.projectID, f.batch.ID, foreign); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-owner key: %v", err)
	}

	narrow := insertReceiptAPIKey(t, f.userID, "projects:write")
	narrowReq := calendarReceipt("receipt-auto-class-narrow-01")
	narrowReq.ExpectedRuntimeGeneration = f.batch.Worker.RuntimeGeneration
	if _, err := f.svc.RecordBuiltReceipt(context.Background(), Actor{Kind: string(auth.PrincipalAPIKey), UserID: f.userID, APIKeyID: narrow},
		f.projectID, f.batch.ID, narrowReq); !errors.Is(err, ErrForbidden) {
		t.Fatalf("narrow key: %v", err)
	}

	if snapshotStage(f.snapshot(), delivery.StageImplementation).PolicySatisfied {
		t.Fatal("class-only negatives mutated implementation")
	}

	ok := calendarReceipt("receipt-auto-class-ok-01")
	ok.ExpectedRuntimeGeneration = f.batch.Worker.RuntimeGeneration
	got, err := f.svc.RecordBuiltReceipt(context.Background(), machine, f.projectID, f.batch.ID, ok)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshotStage(f.snapshot(), delivery.StageQA).PolicySatisfied {
		t.Fatalf("class-only empty assertion failed: %+v", got.Progress)
	}
}

func TestRecordBuiltReceiptAutomaticRejectsWrongOwnerWorkerGeneration(t *testing.T) {
	f := openAgentBridgeFixture(t, ModeAutomatic)
	other, _ := f.secondEditor()
	otherKey := insertReceiptAPIKey(t, other.UserID, "agent-controls:write")
	wrongOwner := calendarReceipt("receipt-auto-wrong-owner-01")
	wrongOwner.ExpectedAccountKey = f.batch.Worker.AccountKey
	wrongOwner.ExpectedRuntimeGeneration = f.batch.Worker.RuntimeGeneration
	if _, err := f.svc.RecordBuiltReceipt(context.Background(), Actor{Kind: string(auth.PrincipalAPIKey), UserID: other.UserID, APIKeyID: otherKey},
		f.projectID, f.batch.ID, wrongOwner); !errors.Is(err, ErrForbidden) {
		t.Fatalf("other editor key: %v", err)
	}
	narrow := insertReceiptAPIKey(t, f.userID, "projects:write")
	narrowReq := wrongOwner
	narrowReq.IdempotencyKey = "receipt-auto-narrow-01"
	if _, err := f.svc.RecordBuiltReceipt(context.Background(), Actor{Kind: string(auth.PrincipalAPIKey), UserID: f.userID, APIKeyID: narrow},
		f.projectID, f.batch.ID, narrowReq); !errors.Is(err, ErrForbidden) {
		t.Fatalf("narrow key: %v", err)
	}
	starterKey := insertReceiptAPIKey(t, f.userID, "agent-controls:write")
	wrongWorker := calendarReceipt("receipt-auto-wrong-worker-01")
	wrongWorker.ExpectedAccountKey = "other-account"
	wrongWorker.ExpectedRuntimeGeneration = f.batch.Worker.RuntimeGeneration
	if _, err := f.svc.RecordBuiltReceipt(context.Background(), Actor{Kind: string(auth.PrincipalAPIKey), UserID: f.userID, APIKeyID: starterKey},
		f.projectID, f.batch.ID, wrongWorker); !errors.Is(err, ErrForbidden) {
		t.Fatalf("wrong worker: %v", err)
	}
	wrongGen := calendarReceipt("receipt-auto-wrong-gen-01")
	wrongGen.ExpectedAccountKey = f.batch.Worker.AccountKey
	wrongGen.ExpectedRuntimeGeneration = "not-this-generation"
	if _, err := f.svc.RecordBuiltReceipt(context.Background(), Actor{Kind: string(auth.PrincipalAPIKey), UserID: f.userID, APIKeyID: starterKey},
		f.projectID, f.batch.ID, wrongGen); !errors.Is(err, ErrForbidden) {
		t.Fatalf("wrong generation: %v", err)
	}
}

func TestRecordBuiltReceiptAssistedRejectsAPIKey(t *testing.T) {
	f := openAgentBridgeFixture(t, ModeAssisted)
	keyID := insertReceiptAPIKey(t, f.userID, "agent-controls:write")
	req := calendarReceipt("receipt-assisted-key-01")
	req.ExpectedAccountKey = f.batch.Worker.AccountKey
	req.ExpectedRuntimeGeneration = f.batch.Worker.RuntimeGeneration
	if _, err := f.svc.RecordBuiltReceipt(context.Background(), Actor{Kind: string(auth.PrincipalAPIKey), UserID: f.userID, APIKeyID: keyID},
		f.projectID, f.batch.ID, req); !errors.Is(err, ErrForbidden) {
		t.Fatalf("assisted API key: %v", err)
	}
}

func TestRecordBuiltReceiptConcurrentConflictingKeys(t *testing.T) {
	f := openBridgeFixture(t)
	var sawOK, sawConflict int
	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		i := i
		go func() {
			defer wg.Done()
			_, err := f.svc.RecordBuiltReceipt(context.Background(), f.actor, f.projectID, f.batch.ID, calendarReceipt(fmt.Sprintf("receipt-race-%d", i+1)))
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				sawOK++
			} else if errors.Is(err, ErrConflict) {
				sawConflict++
			} else {
				t.Errorf("race err: %v", err)
			}
		}()
	}
	wg.Wait()
	if sawOK != 1 || sawConflict != 1 {
		t.Fatalf("concurrent receipts ok=%d conflict=%d", sawOK, sawConflict)
	}
}

func insertReceiptAPIKey(t *testing.T, userID int64, scopes string) int64 {
	t.Helper()
	result, err := appdb.DB.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes)
		VALUES(?,'receipt-key',?,'paimos_receipt',?)`, userID, fmt.Sprintf("receipt-hash-%d-%d", userID, time.Now().UnixNano()), scopes)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	return id
}
