// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package managedharness

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/auth"
	paimosdb "github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/models"
)

type retirementFixture struct {
	service   *Service
	principal auth.Principal
	project   int64
	agent     int64
	session   models.HarnessSession
	runtimeID string
	keyID     int64
}

func newRetirementFixture(t *testing.T) retirementFixture {
	t.Helper()
	project, agent := openManagedHarnessTestDB(t)
	ctx := context.Background()
	service := NewService(paimosdb.DB)
	var user int64
	if err := paimosdb.DB.QueryRow(`SELECT id FROM users WHERE username='harness-actor'`).Scan(&user); err != nil {
		t.Fatal(err)
	}
	credential := uuid.NewString()
	if _, err := paimosdb.DB.Exec(`INSERT INTO sessions(id,user_id,credential_id,expires_at,created_at)
		VALUES(?,?,?,datetime('now','+1 hour'),datetime('now'))`, uuid.NewString(), user, credential); err != nil {
		t.Fatal(err)
	}
	principal, err := auth.NewSessionPrincipal(credential, user, user, false)
	if err != nil {
		t.Fatal(err)
	}
	session, _, err := service.Register(ctx, RegisterInput{
		ProjectID: project, AgentName: "worker", Harness: "codex", Host: "retirement-host",
		SessionRef: uuid.NewString(), WorkerLease: testWorkerLease, ManagementMode: ManagementManaged,
		Role: RoleWorker, SteerMode: SteerNone,
		Capabilities: models.HarnessCapabilities{Status: true, Interrupt: true, Stop: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	reporter, err := paimosdb.DB.Exec(`INSERT INTO users(username,password,role,role_key,status,is_super_admin)
		VALUES('retirement-reporter','disabled','admin','super_admin','active',1)`)
	if err != nil {
		t.Fatal(err)
	}
	reporterID, _ := reporter.LastInsertId()
	key, err := paimosdb.DB.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes)
		VALUES(?,'retirement','retirement-hash','ret','agent-controls:runner')`, reporterID)
	if err != nil {
		t.Fatal(err)
	}
	keyID, _ := key.LastInsertId()
	runtimeID, runtimeGeneration, sessionGeneration := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err = paimosdb.DB.Exec(`INSERT INTO lifecycle_runtimes(
		id,project_id,generation,machine_id,user_id,api_key_id,lease_digest,registration_json,expires_at,created_at)
		VALUES(?,?,?,?,?,?,zeroblob(32),'{}',strftime('%Y-%m-%dT%H:%M:%fZ','now','+10 minutes'),strftime('%Y-%m-%dT%H:%M:%fZ','now'))`,
		runtimeID, project, runtimeGeneration, session.Host, reporterID, keyID); err != nil {
		t.Fatal(err)
	}
	if _, err = paimosdb.DB.Exec(`INSERT INTO lifecycle_runtime_sessions(session_id,runtime_id,generation,created_at)
		VALUES(?,?,?,strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, session.ID, runtimeID, sessionGeneration); err != nil {
		t.Fatal(err)
	}
	return retirementFixture{service: service, principal: principal, project: project, agent: agent,
		session: session, runtimeID: runtimeID, keyID: keyID}
}

func (f retirementFixture) addAcceptedConsumerWork(t *testing.T) string {
	t.Helper()
	streamID, attemptID := uuid.NewString(), uuid.NewString()
	var runtimeGeneration, sessionGeneration string
	if err := paimosdb.DB.QueryRow(`SELECT runtime.generation,owned.generation FROM lifecycle_runtimes runtime
		JOIN lifecycle_runtime_sessions owned ON owned.runtime_id=runtime.id WHERE runtime.id=? AND owned.session_id=?`,
		f.runtimeID, f.session.ID).Scan(&runtimeGeneration, &sessionGeneration); err != nil {
		t.Fatal(err)
	}
	if _, err := paimosdb.DB.Exec(`INSERT INTO agent_consumer_streams(
		id,project_id,agent_id,address,kind,revision,generation,registration_json,runtime_id,runtime_generation,
		session_id,session_generation,user_id,api_key_id,target_id,target_version,lease_digest,expires_at)
		VALUES(?,?,?,'codex:worker','fallback',1,?,'{}',?,?,?,?,?,?,'retirement-target',1,zeroblob(32),strftime('%Y-%m-%dT%H:%M:%fZ','now','+10 minutes'))`,
		streamID, f.project, f.agent, uuid.NewString(), f.runtimeID, runtimeGeneration, f.session.ID, sessionGeneration,
		f.principal.UserID(), f.keyID); err != nil {
		t.Fatal(err)
	}
	if _, err := paimosdb.DB.Exec(`INSERT INTO agent_consumer_attempts(
		id,stream_id,stream_revision,request_key,nonce_digest,owner_json,consumer_digest,resource_id,cursor,state,expires_at)
		VALUES(?,?,1,?,zeroblob(32),'{}',zeroblob(32),'accepted-work',1,'claimed',strftime('%Y-%m-%dT%H:%M:%fZ','now','+10 minutes'))`,
		attemptID, streamID, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	return attemptID
}

func TestRetirementIsIdempotentFencesAdmissionAndRequiresSettledCompletion(t *testing.T) {
	f := newRetirementFixture(t)
	ctx := context.Background()
	attemptID := f.addAcceptedConsumerWork(t)
	request := BrowserControlRequest{ExpectedRevision: f.session.Revision, RequestKey: uuid.NewString()}
	created, err := f.service.RequestRetirementCAS(ctx, f.principal, f.project, f.session.ID, request)
	if err != nil || created.Retirement.State != "requested" || created.Retirement.OwnedStopReceipt {
		t.Fatalf("created=%+v err=%v", created, err)
	}

	if _, err = f.service.RequestControlCAS(ctx, f.principal, f.project, f.session.ID, "stop", BrowserControlRequest{
		ExpectedRevision: f.session.Revision, RequestKey: uuid.NewString(),
	}); err != ErrBrowserConflict {
		t.Fatalf("retirement admitted competing control: %v", err)
	}
	if _, err = f.service.AssignBinding(ctx, BindingInput{ProjectID: f.project, SessionID: f.session.ID,
		ExpectedRevision: f.session.Revision, WorkShape: "unknown"}); ErrorCode(err) != CodeConflict {
		t.Fatalf("retirement admitted binding change: %v", err)
	}

	yielded, err := f.service.Yield(ctx, f.session.ID)
	if err != nil || len(yielded.Retirements) != 1 || yielded.Retirements[0].ID != created.Retirement.ID {
		t.Fatalf("yielded=%+v err=%v", yielded, err)
	}
	replayed, err := f.service.RequestRetirementCAS(ctx, f.principal, f.project, f.session.ID, request)
	if err != nil || replayed.Retirement.ID != created.Retirement.ID || replayed.Retirement.State != "finishing" {
		t.Fatalf("replayed=%+v err=%v", replayed, err)
	}
	if _, err = f.service.RequestRetirementCAS(ctx, f.principal, f.project, f.session.ID, BrowserControlRequest{
		ExpectedRevision: f.session.Revision, RequestKey: uuid.NewString(),
	}); err != ErrBrowserConflict {
		t.Fatalf("stale new request accepted: %v", err)
	}

	if _, err = f.service.PrepareRetirement(ctx, f.project, f.session.ID, created.Retirement.ID, f.keyID); ErrorCode(err) != CodeRetirementNotReady {
		t.Fatalf("unfinished owned turn returned code=%q error=%v", ErrorCode(err), err)
	}
	if _, err = f.service.HeartbeatWithActivity(ctx, f.session.ID, PhaseWorking, ActivityEvidence{Sequence: 1, Kind: ActivityCompleted}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.PrepareRetirement(ctx, f.project, f.session.ID, created.Retirement.ID, f.keyID); ErrorCode(err) != CodeRetirementNotReady {
		t.Fatalf("unsettled accepted work crossed stop boundary: %v", err)
	}
	if _, err = paimosdb.DB.Exec(`UPDATE agent_consumer_attempts SET state='released',result_json='{}' WHERE id=?`, attemptID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.PrepareRetirement(ctx, f.project, f.session.ID, created.Retirement.ID, f.keyID+1); ErrorCode(err) != CodeConflict {
		t.Fatalf("different reporter authority crossed retirement boundary: %v", err)
	}
	ready, err := f.service.PrepareRetirement(ctx, f.project, f.session.ID, created.Retirement.ID, f.keyID)
	if err != nil || ready.State != "stopping" {
		t.Fatalf("ready=%+v err=%v", ready, err)
	}
	if _, err = paimosdb.DB.Exec(`INSERT INTO agent_consumer_attempts(
		id,stream_id,stream_revision,request_key,nonce_digest,owner_json,consumer_digest,resource_id,cursor,state,expires_at)
		SELECT ?,stream_id,stream_revision,?,randomblob(32),'{}',randomblob(32),'new-work',2,'claimed',expires_at
		FROM agent_consumer_attempts WHERE id=?`, uuid.NewString(), uuid.NewString(), attemptID); err == nil {
		t.Fatal("retirement admitted new consumer work")
	}

	completed, err := f.service.CompleteRetirement(ctx, f.project, f.session.ID, created.Retirement.ID, ControlApplied, ReasonApplied, f.keyID)
	if err != nil || completed.State != "stopping" || !completed.OwnedStopReceipt || completed.StoppedGenerationProof {
		t.Fatalf("receipt must remain pending for stopped-generation proof: %+v err=%v", completed, err)
	}
	if _, err = f.service.Stop(ctx, f.session.ID); err != nil {
		t.Fatal(err)
	}
	finished, err := f.service.GetRetirement(ctx, f.project, f.session.ID, created.Retirement.ID)
	if err != nil || finished.State != "completed" || !finished.OwnedStopReceipt || !finished.StoppedGenerationProof {
		t.Fatalf("finished=%+v err=%v", finished, err)
	}
	if _, err = f.service.CompleteRetirement(ctx, f.project, f.session.ID, created.Retirement.ID, ControlApplied, ReasonApplied, f.keyID); err != nil {
		t.Fatalf("completion replay failed: %v", err)
	}
}

func TestRetirementFailureIsDurableAndPublicStoppedCannotInventReceipt(t *testing.T) {
	f := newRetirementFixture(t)
	ctx := context.Background()
	created, err := f.service.RequestRetirementCAS(ctx, f.principal, f.project, f.session.ID, BrowserControlRequest{
		ExpectedRevision: f.session.Revision, RequestKey: uuid.NewString(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.Yield(ctx, f.session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.CompleteRetirement(ctx, f.project, f.session.ID, created.Retirement.ID, ControlRejected, "outcome_unknown", f.keyID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.Stop(ctx, f.session.ID); err != nil {
		t.Fatal(err)
	}
	out, err := f.service.GetRetirement(ctx, f.project, f.session.ID, created.Retirement.ID)
	if err != nil || out.State != "outcome_unknown" || out.OwnedStopReceipt || !out.StoppedGenerationProof {
		t.Fatalf("public stop fabricated receipt: %+v err=%v", out, err)
	}
	blocked, err := f.service.RetirementAdmissionBlocked(ctx, f.session.ID)
	if err != nil || !blocked {
		t.Fatalf("uncertain retirement must remain fenced: blocked=%v err=%v", blocked, err)
	}
}

func TestRejectedRetirementAllowsFreshReviewedRetryButKeepsOldReplay(t *testing.T) {
	f := newRetirementFixture(t)
	ctx := context.Background()
	request := BrowserControlRequest{ExpectedRevision: f.session.Revision, RequestKey: uuid.NewString()}
	first, err := f.service.RequestRetirementCAS(ctx, f.principal, f.project, f.session.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	yielded, err := f.service.Yield(ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	failed, err := f.service.CompleteRetirement(ctx, f.project, f.session.ID, first.Retirement.ID, ControlRejected, ReasonFailed, f.keyID)
	if err != nil || failed.State != "failed" {
		t.Fatalf("failed=%+v err=%v", failed, err)
	}
	replayed, err := f.service.RequestRetirementCAS(ctx, f.principal, f.project, f.session.ID, request)
	if err != nil || replayed.Retirement.ID != first.Retirement.ID || replayed.Retirement.State != "failed" {
		t.Fatalf("old replay=%+v err=%v", replayed, err)
	}
	retry, err := f.service.RequestRetirementCAS(ctx, f.principal, f.project, f.session.ID, BrowserControlRequest{
		ExpectedRevision: yielded.Session.Revision, RequestKey: uuid.NewString(),
	})
	if err != nil || retry.Retirement.ID == first.Retirement.ID || retry.Retirement.State != "requested" {
		t.Fatalf("fresh retry=%+v err=%v", retry, err)
	}
	if _, err = f.service.Yield(ctx, f.session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.HeartbeatWithActivity(ctx, f.session.ID, PhaseWorking, ActivityEvidence{Sequence: 1, Kind: ActivityCompleted}); err != nil {
		t.Fatal(err)
	}
	if _, err = paimosdb.DB.Exec(`DELETE FROM sessions WHERE credential_id=(
		SELECT requested_session_credential_id FROM harness_session_retirements WHERE id=?)`, retry.Retirement.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.PrepareRetirement(ctx, f.project, f.session.ID, retry.Retirement.ID, f.keyID); ErrorCode(err) != CodeConflict {
		t.Fatalf("revoked human authority crossed retirement boundary: %v", err)
	}
}
