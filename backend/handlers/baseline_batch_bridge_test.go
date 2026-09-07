// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers_test

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/baselinebatch"
	"github.com/inspr-at/paimos/backend/contracts"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/delivery"
	"github.com/inspr-at/paimos/backend/externalstage"
)

const (
	bridgeHTTPCommit     = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	bridgeHTTPConfig     = "3333333333333333333333333333333333333333333333333333333333333333"
	bridgeHTTPIndex      = "4444444444444444444444444444444444444444444444444444444444444444"
	bridgeHTTPQADigest   = "5555555555555555555555555555555555555555555555555555555555555555"
	bridgeHTTPReleaseSet = "6666666666666666666666666666666666666666666666666666666666666666"
	bridgeHTTPVersion    = "26.09.07.12.00.00"
	bridgeHTTPChannel    = "stable"
	bridgeHTTPSequence   = int64(260907120000)
	bridgeHTTPCoordinate = "ghcr:inspr-at/pharos/releases/" + bridgeHTTPVersion
)

func TestBaselineBatchAssistedReconcileHTTP(t *testing.T) {
	ts := newTestServer(t)
	userID := promoteSuperAdmin(t, "admin")
	projectID := responseID(t, ts.post(t, "/api/projects", ts.adminCookie, map[string]string{"name": "Assisted bridge", "key": "ASB"}))
	if _, err := db.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'codex')`, projectID); err != nil {
		t.Fatal(err)
	}
	optIn(t, ts, projectID)
	handover, _ := validHandover(t)
	imported := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/import", projectID),
		map[string]any{"handover": json.RawMessage(handover)})
	if imported.StatusCode != 201 {
		t.Fatalf("import=%d %s", imported.StatusCode, baselineReadBody(imported))
	}
	var draft baselinebatch.Draft
	decode(t, imported, &draft)
	daemon := newOwnedDaemon(t, projectID, userID)
	worker := daemon.workerSelection("codex")
	if resp := patchBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d", projectID, draft.ID),
		map[string]any{"execution_mode": "assisted", "worker": worker}); resp.StatusCode != 200 {
		t.Fatalf("select worker=%d %s", resp.StatusCode, baselineReadBody(resp))
	}
	review := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/review", projectID, draft.ID),
		map[string]any{"execution_mode": "assisted", "worker": worker, "selected_requirement_refs": []string{"req.login"}})
	if review.StatusCode != 200 {
		t.Fatalf("review=%d %s", review.StatusCode, baselineReadBody(review))
	}
	decode(t, review, &draft)
	if resp := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/readiness", projectID, draft.ID), nil); resp.StatusCode != 200 {
		t.Fatalf("readiness=%d %s", resp.StatusCode, baselineReadBody(resp))
	}
	if daemon.step(nil) == nil {
		t.Fatal("owned daemon found no readiness intent")
	}
	started := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/start", projectID, draft.ID), map[string]any{
		"idempotency_key": "assisted-bridge-start", "review_id": *draft.ReviewID, "draft_revision": draft.Revision,
		"confirm": true, "content_digest": draft.Baseline.ContentDigest, "revision_seal": draft.Baseline.RevisionSeal,
		"execution_mode": "assisted", "selected_requirement_refs": []string{"req.login"}, "worker": worker,
	})
	if started.StatusCode != 200 {
		t.Fatalf("start=%d %s", started.StatusCode, baselineReadBody(started))
	}
	var batch baselinebatch.Batch
	decode(t, started, &batch)
	if daemon.step(nil) == nil {
		t.Fatal("owned daemon found no start intent")
	}

	now := time.Now().UTC().Truncate(time.Millisecond)
	store := delivery.NewStore(db.DB, delivery.Options{Clock: delivery.ClockFunc(func() time.Time { return now })})
	ext, err := externalstage.NewService(db.DB, externalstage.Options{
		FixtureDigest: contracts.ExternalStageV1FixtureDigest(), Random: cryptorand.Reader,
		Clock: clockNow{now: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	var sessionCred string
	if err := db.DB.QueryRow(`SELECT credential_id FROM sessions WHERE user_id=? ORDER BY created_at DESC LIMIT 1`, userID).Scan(&sessionCred); err != nil {
		t.Fatal(err)
	}
	operator := externalstage.Principal{UserID: userID, Kind: "session", SessionCredentialID: sessionCred}
	registerHTTPPharos(t, ext, operator, projectID, batch.IssueID)
	reportHTTPBuildAndQA(t, store, userID, batch)

	activated := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/batches/%d/reconcile", projectID, batch.ID), map[string]any{})
	if activated.StatusCode != 200 {
		t.Fatalf("first reconcile=%d %s", activated.StatusCode, baselineReadBody(activated))
	}
	decode(t, activated, &batch)
	if batch.Progress.SetupRequired != baselinebatch.SetupRequiredPrerequisiteSeal {
		t.Fatalf("expected prerequisite seal setup, got %+v", batch.Progress)
	}
	snap, err := store.SnapshotByIssue(context.Background(), batch.IssueID)
	if err != nil {
		t.Fatal(err)
	}
	deploy := stageOf(snap, delivery.StageDeployment)
	if _, err := ext.SealPrerequisites(context.Background(), operator, fmt.Sprintf("issue:%d", batch.IssueID), "http-empty-seal",
		externalstage.SealPrerequisitesRequest{
			StageKey: delivery.StageDeployment, ExecutionNumber: deploy.ExecutionNumber,
			ExpectedPlanRevision: snap.PlanRevision, ExpectedAuthorityEpoch: deploy.AuthorityEpoch,
			Prerequisites: []externalstage.Prerequisite{},
		}); err != nil {
		t.Fatal(err)
	}
	handoff := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/batches/%d/reconcile", projectID, batch.ID), map[string]any{})
	if handoff.StatusCode != 200 {
		t.Fatalf("second reconcile=%d %s", handoff.StatusCode, baselineReadBody(handoff))
	}
	decode(t, handoff, &batch)
	if batch.Progress.Handoff == nil || batch.Progress.NextAction != baselinebatch.NextActionMintHandoffSecret {
		t.Fatalf("expected mint after HTTP reconcile, got %+v", batch.Progress)
	}
}

func TestBaselineBatchAssistedReconcileSealsRequiredJanusHTTP(t *testing.T) {
	ts := newTestServer(t)
	userID := promoteSuperAdmin(t, "admin")
	projectID := responseID(t, ts.post(t, "/api/projects", ts.adminCookie, map[string]string{"name": "Janus bridge", "key": "JNB"}))
	if _, err := db.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'codex')`, projectID); err != nil {
		t.Fatal(err)
	}
	optIn(t, ts, projectID)
	handover, _ := validHandover(t)
	imported := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/import", projectID),
		map[string]any{"handover": json.RawMessage(handover)})
	var draft baselinebatch.Draft
	decode(t, imported, &draft)
	daemon := newOwnedDaemon(t, projectID, userID)
	worker := daemon.workerSelection("codex")
	if resp := patchBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d", projectID, draft.ID),
		map[string]any{"execution_mode": "assisted", "worker": worker}); resp.StatusCode != 200 {
		t.Fatal(baselineReadBody(resp))
	}
	review := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/review", projectID, draft.ID),
		map[string]any{"execution_mode": "assisted", "worker": worker, "selected_requirement_refs": []string{"req.login"}})
	decode(t, review, &draft)
	if resp := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/readiness", projectID, draft.ID), nil); resp.StatusCode != 200 {
		t.Fatal(baselineReadBody(resp))
	}
	if daemon.step(nil) == nil {
		t.Fatal("no readiness intent")
	}
	started := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/start", projectID, draft.ID), map[string]any{
		"idempotency_key": "assisted-janus-start", "review_id": *draft.ReviewID, "draft_revision": draft.Revision,
		"confirm": true, "content_digest": draft.Baseline.ContentDigest, "revision_seal": draft.Baseline.RevisionSeal,
		"execution_mode": "assisted", "selected_requirement_refs": []string{"req.login"}, "worker": worker,
	})
	if started.StatusCode != 200 {
		t.Fatalf("start=%d %s", started.StatusCode, baselineReadBody(started))
	}
	var batch baselinebatch.Batch
	decode(t, started, &batch)
	if daemon.step(nil) == nil {
		t.Fatal("no start intent")
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	store := delivery.NewStore(db.DB, delivery.Options{Clock: delivery.ClockFunc(func() time.Time { return now })})
	ext, err := externalstage.NewService(db.DB, externalstage.Options{
		FixtureDigest: contracts.ExternalStageV1FixtureDigest(), Random: cryptorand.Reader,
		Clock: clockNow{now: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	var sessionCred string
	if err := db.DB.QueryRow(`SELECT credential_id FROM sessions ORDER BY created_at DESC LIMIT 1`).Scan(&sessionCred); err != nil {
		t.Fatal(err)
	}
	operator := externalstage.Principal{UserID: userID, Kind: "session", SessionCredentialID: sessionCred}
	registerHTTPPharos(t, ext, operator, projectID, batch.IssueID)
	registerHTTPJanus(t, ext, operator, projectID, batch.IssueID)
	reportHTTPBuildAndQA(t, store, userID, batch)
	resp := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/batches/%d/reconcile", projectID, batch.ID), map[string]any{})
	if resp.StatusCode != 200 {
		t.Fatalf("reconcile=%d %s", resp.StatusCode, baselineReadBody(resp))
	}
	decode(t, resp, &batch)
	var declared int
	if err := db.DB.QueryRow(`SELECT declared_count FROM external_stage_prerequisite_sets
		WHERE delivery_id=? AND stage_key='deployment' AND sealed_at IS NOT NULL`, *batch.DeliveryID).Scan(&declared); err != nil {
		t.Fatal(err)
	}
	if declared != 1 {
		t.Fatalf("HTTP reconcile sealed declared_count=%d progress=%+v", declared, batch.Progress)
	}
	if batch.Progress.Handoff == nil {
		t.Fatalf("expected handoff after required Janus seal, got %+v", batch.Progress)
	}
}

type clockNow struct{ now time.Time }

func (c clockNow) Now() time.Time { return c.now }

func registerHTTPPharos(t *testing.T, ext *externalstage.Service, operator externalstage.Principal, projectID, issueID int64) {
	t.Helper()
	result, err := db.DB.Exec(`INSERT INTO users(username,password,role,status) VALUES('pharos-http','x','member','active')`)
	if err != nil {
		t.Fatal(err)
	}
	reporterUser, _ := result.LastInsertId()
	if _, err := db.DB.Exec(`INSERT INTO project_members(user_id,project_id,access_level) VALUES(?,?,'editor')`, reporterUser, projectID); err != nil {
		t.Fatal(err)
	}
	result, err = db.DB.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes)
		VALUES(?,'pharos-http',?,'paimos_pharos_http','*')`, reporterUser, fmt.Sprintf("%064d", 970))
	if err != nil {
		t.Fatal(err)
	}
	keyID, _ := result.LastInsertId()
	if _, err := ext.RegisterReporter(context.Background(), operator, fmt.Sprintf("issue:%d", issueID), "http-register-pharos",
		externalstage.RegisterReporterRequest{APIKeyID: keyID, ReporterClass: externalstage.ReporterClassPharos,
			ReporterRole: externalstage.ReporterRoleOwner, Workflow: "deploy-production", Environment: "production-eu1"}); err != nil {
		t.Fatal(err)
	}
}

func registerHTTPJanus(t *testing.T, ext *externalstage.Service, operator externalstage.Principal, projectID, issueID int64) {
	t.Helper()
	result, err := db.DB.Exec(`INSERT INTO users(username,password,role,status) VALUES('janus-http','x','member','active')`)
	if err != nil {
		t.Fatal(err)
	}
	reporterUser, _ := result.LastInsertId()
	if _, err := db.DB.Exec(`INSERT INTO project_members(user_id,project_id,access_level) VALUES(?,?,'editor')`, reporterUser, projectID); err != nil {
		t.Fatal(err)
	}
	result, err = db.DB.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes)
		VALUES(?,'janus-http',?,'paimos_janus_http','*')`, reporterUser, fmt.Sprintf("%064d", 971))
	if err != nil {
		t.Fatal(err)
	}
	keyID, _ := result.LastInsertId()
	if _, err := ext.RegisterReporter(context.Background(), operator, fmt.Sprintf("issue:%d", issueID), "http-register-janus",
		externalstage.RegisterReporterRequest{APIKeyID: keyID, ReporterClass: externalstage.ReporterClassJanus,
			ReporterRole: externalstage.ReporterRoleDependency, DependencyKey: "cluster-admission"}); err != nil {
		t.Fatal(err)
	}
}

func reportHTTPBuildAndQA(t *testing.T, store *delivery.Store, userID int64, batch baselinebatch.Batch) {
	t.Helper()
	ctx := context.Background()
	reporter := delivery.Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", userID)}
	impl, err := store.StartStageRetry(ctx, delivery.StageStartRequest{
		IssueID: batch.IssueID, AttemptNumber: 1, StageKey: delivery.StageImplementation, Reporter: reporter,
		ReasonCode: "implementation_start", IdempotencyKey: batch.BatchKey + ":http-impl:start",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReportStage(ctx, delivery.StageReport{
		IssueID: batch.IssueID, AttemptNumber: 1, StageKey: delivery.StageImplementation,
		ExecutionNumber: impl.ExecutionNumber, AuthorityEpoch: impl.AuthorityEpoch, Reporter: reporter,
		IdempotencyKey: batch.BatchKey + ":http-impl:report", Kind: "semantic", State: "succeeded",
		Evidence: []delivery.Evidence{
			{Type: "implementation_result", Outcome: "passed", ReferenceKind: "commit", ReferenceValue: bridgeHTTPCommit},
			{Type: "artifact", Outcome: "passed", ReferenceKind: "digest", DigestSHA256: bridgeHTTPConfig},
			{Type: "artifact", Outcome: "passed", ReferenceKind: "external_ref", ReferenceValue: externalstage.FormatOCIManifestRef(bridgeHTTPIndex)},
			{Type: "artifact", Outcome: "passed", ReferenceKind: "external_ref", ReferenceValue: externalstage.FormatReleaseManifestRef(bridgeHTTPReleaseSet)},
			{Type: "artifact", Outcome: "passed", ReferenceKind: "external_ref", ReferenceValue: externalstage.FormatReleaseCoordinateRef(bridgeHTTPCoordinate)},
			{Type: "artifact", Outcome: "passed", ReferenceKind: "external_ref",
				ReferenceValue: externalstage.FormatReleaseIdentity(externalstage.VersionSchemeINSPRCalendar, bridgeHTTPChannel, bridgeHTTPSequence, bridgeHTTPVersion)},
		},
		ReasonCode: "implementation_result",
	}); err != nil {
		t.Fatal(err)
	}
	qa, err := store.StartStageRetry(ctx, delivery.StageStartRequest{
		IssueID: batch.IssueID, AttemptNumber: 1, StageKey: delivery.StageQA, Reporter: reporter,
		ReasonCode: "qa_start", IdempotencyKey: batch.BatchKey + ":http-qa:start",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReportStage(ctx, delivery.StageReport{
		IssueID: batch.IssueID, AttemptNumber: 1, StageKey: delivery.StageQA,
		ExecutionNumber: qa.ExecutionNumber, AuthorityEpoch: qa.AuthorityEpoch, Reporter: reporter,
		IdempotencyKey: batch.BatchKey + ":http-qa:report", Kind: "semantic", State: "succeeded",
		Evidence: []delivery.Evidence{{
			Type: "test_result", Outcome: "passed", ReferenceKind: "digest", DigestSHA256: bridgeHTTPQADigest,
		}},
		ReasonCode: "test_result",
	}); err != nil {
		t.Fatal(err)
	}
}

func stageOf(snap delivery.Snapshot, key string) delivery.StageSnapshot {
	for _, stage := range snap.Stages {
		if stage.StageKey == key {
			return stage
		}
	}
	return delivery.StageSnapshot{StageKey: key}
}

func TestBaselineBatchPartialIdentityHTTP(t *testing.T) {
	ts := newTestServer(t)
	userID := promoteSuperAdmin(t, "admin")
	projectID := responseID(t, ts.post(t, "/api/projects", ts.adminCookie, map[string]string{"name": "Partial identity", "key": "PID"}))
	if _, err := db.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'codex')`, projectID); err != nil {
		t.Fatal(err)
	}
	optIn(t, ts, projectID)
	handover, _ := validHandover(t)
	imported := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/import", projectID),
		map[string]any{"handover": json.RawMessage(handover)})
	var draft baselinebatch.Draft
	decode(t, imported, &draft)
	daemon := newOwnedDaemon(t, projectID, userID)
	worker := daemon.workerSelection("codex")
	if resp := patchBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d", projectID, draft.ID),
		map[string]any{"execution_mode": "assisted", "worker": worker}); resp.StatusCode != 200 {
		t.Fatal(baselineReadBody(resp))
	}
	review := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/review", projectID, draft.ID),
		map[string]any{"execution_mode": "assisted", "worker": worker, "selected_requirement_refs": []string{"req.login"}})
	decode(t, review, &draft)
	if resp := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/readiness", projectID, draft.ID), nil); resp.StatusCode != 200 {
		t.Fatal(baselineReadBody(resp))
	}
	if daemon.step(nil) == nil {
		t.Fatal("no readiness intent")
	}
	started := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/start", projectID, draft.ID), map[string]any{
		"idempotency_key": "partial-identity-start", "review_id": *draft.ReviewID, "draft_revision": draft.Revision,
		"confirm": true, "content_digest": draft.Baseline.ContentDigest, "revision_seal": draft.Baseline.RevisionSeal,
		"execution_mode": "assisted", "selected_requirement_refs": []string{"req.login"}, "worker": worker,
	})
	if started.StatusCode != 200 {
		t.Fatalf("start=%d %s", started.StatusCode, baselineReadBody(started))
	}
	var batch baselinebatch.Batch
	decode(t, started, &batch)
	if daemon.step(nil) == nil {
		t.Fatal("no start intent")
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	store := delivery.NewStore(db.DB, delivery.Options{Clock: delivery.ClockFunc(func() time.Time { return now })})
	ext, err := externalstage.NewService(db.DB, externalstage.Options{
		FixtureDigest: contracts.ExternalStageV1FixtureDigest(), Random: cryptorand.Reader,
		Clock: clockNow{now: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	var sessionCred string
	if err := db.DB.QueryRow(`SELECT credential_id FROM sessions WHERE user_id=? ORDER BY created_at DESC LIMIT 1`, userID).Scan(&sessionCred); err != nil {
		t.Fatal(err)
	}
	operator := externalstage.Principal{UserID: userID, Kind: "session", SessionCredentialID: sessionCred}
	registerHTTPPharos(t, ext, operator, projectID, batch.IssueID)
	reporter := delivery.Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", userID)}
	impl, err := store.StartStageRetry(context.Background(), delivery.StageStartRequest{
		IssueID: batch.IssueID, AttemptNumber: 1, StageKey: delivery.StageImplementation, Reporter: reporter,
		ReasonCode: "implementation_start", IdempotencyKey: batch.BatchKey + ":partial-impl:start",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReportStage(context.Background(), delivery.StageReport{
		IssueID: batch.IssueID, AttemptNumber: 1, StageKey: delivery.StageImplementation,
		ExecutionNumber: impl.ExecutionNumber, AuthorityEpoch: impl.AuthorityEpoch, Reporter: reporter,
		IdempotencyKey: batch.BatchKey + ":partial-impl:report", Kind: "semantic", State: "succeeded",
		Evidence: []delivery.Evidence{
			{Type: "implementation_result", Outcome: "passed", ReferenceKind: "commit", ReferenceValue: bridgeHTTPCommit},
			{Type: "artifact", Outcome: "passed", ReferenceKind: "external_ref", ReferenceValue: "suite:not-identity"},
		},
		ReasonCode: "implementation_result",
	}); err != nil {
		t.Fatal(err)
	}
	qa, err := store.StartStageRetry(context.Background(), delivery.StageStartRequest{
		IssueID: batch.IssueID, AttemptNumber: 1, StageKey: delivery.StageQA, Reporter: reporter,
		ReasonCode: "qa_start", IdempotencyKey: batch.BatchKey + ":partial-qa:start",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReportStage(context.Background(), delivery.StageReport{
		IssueID: batch.IssueID, AttemptNumber: 1, StageKey: delivery.StageQA,
		ExecutionNumber: qa.ExecutionNumber, AuthorityEpoch: qa.AuthorityEpoch, Reporter: reporter,
		IdempotencyKey: batch.BatchKey + ":partial-qa:report", Kind: "semantic", State: "succeeded",
		Evidence:   []delivery.Evidence{{Type: "test_result", Outcome: "passed", ReferenceKind: "external_ref", ReferenceValue: "suite:legacy"}},
		ReasonCode: "test_result",
	}); err != nil {
		t.Fatal(err)
	}
	resp := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/batches/%d/reconcile", projectID, batch.ID), map[string]any{})
	if resp.StatusCode != 200 {
		t.Fatalf("reconcile=%d %s", resp.StatusCode, baselineReadBody(resp))
	}
	decode(t, resp, &batch)
	if batch.Progress.SetupRequired != baselinebatch.SetupRequiredBuiltArtifact || batch.Progress.Handoff != nil {
		t.Fatalf("expected built artifact identity setup, got %+v", batch.Progress)
	}
}

func TestBaselineBatchJanusAfterEmptySealHTTP(t *testing.T) {
	ts := newTestServer(t)
	userID := promoteSuperAdmin(t, "admin")
	projectID := responseID(t, ts.post(t, "/api/projects", ts.adminCookie, map[string]string{"name": "Janus after seal", "key": "JAS"}))
	if _, err := db.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'codex')`, projectID); err != nil {
		t.Fatal(err)
	}
	optIn(t, ts, projectID)
	handover, _ := validHandover(t)
	imported := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/import", projectID),
		map[string]any{"handover": json.RawMessage(handover)})
	var draft baselinebatch.Draft
	decode(t, imported, &draft)
	daemon := newOwnedDaemon(t, projectID, userID)
	worker := daemon.workerSelection("codex")
	if resp := patchBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d", projectID, draft.ID),
		map[string]any{"execution_mode": "assisted", "worker": worker}); resp.StatusCode != 200 {
		t.Fatal(baselineReadBody(resp))
	}
	review := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/review", projectID, draft.ID),
		map[string]any{"execution_mode": "assisted", "worker": worker, "selected_requirement_refs": []string{"req.login"}})
	decode(t, review, &draft)
	if resp := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/readiness", projectID, draft.ID), nil); resp.StatusCode != 200 {
		t.Fatal(baselineReadBody(resp))
	}
	if daemon.step(nil) == nil {
		t.Fatal("no readiness intent")
	}
	started := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/start", projectID, draft.ID), map[string]any{
		"idempotency_key": "janus-after-seal-start", "review_id": *draft.ReviewID, "draft_revision": draft.Revision,
		"confirm": true, "content_digest": draft.Baseline.ContentDigest, "revision_seal": draft.Baseline.RevisionSeal,
		"execution_mode": "assisted", "selected_requirement_refs": []string{"req.login"}, "worker": worker,
	})
	if started.StatusCode != 200 {
		t.Fatalf("start=%d %s", started.StatusCode, baselineReadBody(started))
	}
	var batch baselinebatch.Batch
	decode(t, started, &batch)
	if daemon.step(nil) == nil {
		t.Fatal("no start intent")
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	store := delivery.NewStore(db.DB, delivery.Options{Clock: delivery.ClockFunc(func() time.Time { return now })})
	ext, err := externalstage.NewService(db.DB, externalstage.Options{
		FixtureDigest: contracts.ExternalStageV1FixtureDigest(), Random: cryptorand.Reader,
		Clock: clockNow{now: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	var sessionCred string
	if err := db.DB.QueryRow(`SELECT credential_id FROM sessions WHERE user_id=? ORDER BY created_at DESC LIMIT 1`, userID).Scan(&sessionCred); err != nil {
		t.Fatal(err)
	}
	operator := externalstage.Principal{UserID: userID, Kind: "session", SessionCredentialID: sessionCred}
	registerHTTPPharos(t, ext, operator, projectID, batch.IssueID)
	reportHTTPBuildAndQA(t, store, userID, batch)
	activated := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/batches/%d/reconcile", projectID, batch.ID), map[string]any{})
	if activated.StatusCode != 200 {
		t.Fatal(baselineReadBody(activated))
	}
	decode(t, activated, &batch)
	snap, err := store.SnapshotByIssue(context.Background(), batch.IssueID)
	if err != nil {
		t.Fatal(err)
	}
	deploy := stageOf(snap, delivery.StageDeployment)
	if _, err := ext.SealPrerequisites(context.Background(), operator, fmt.Sprintf("issue:%d", batch.IssueID), "http-empty-then-janus",
		externalstage.SealPrerequisitesRequest{
			StageKey: delivery.StageDeployment, ExecutionNumber: deploy.ExecutionNumber,
			ExpectedPlanRevision: snap.PlanRevision, ExpectedAuthorityEpoch: deploy.AuthorityEpoch,
			Prerequisites: []externalstage.Prerequisite{},
		}); err != nil {
		t.Fatal(err)
	}
	handoff := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/batches/%d/reconcile", projectID, batch.ID), map[string]any{})
	if handoff.StatusCode != 200 {
		t.Fatal(baselineReadBody(handoff))
	}
	decode(t, handoff, &batch)
	if batch.Progress.Handoff == nil {
		t.Fatalf("expected handoff before Janus, got %+v", batch.Progress)
	}
	registerHTTPJanus(t, ext, operator, projectID, batch.IssueID)
	listed := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/batches/%d/reconcile", projectID, batch.ID), map[string]any{})
	if listed.StatusCode != 200 {
		t.Fatal(baselineReadBody(listed))
	}
	decode(t, listed, &batch)
	if batch.Progress.SetupRequired != baselinebatch.SetupRequiredPrerequisiteReview {
		t.Fatalf("expected prerequisite review after Janus, got %+v", batch.Progress)
	}
}
