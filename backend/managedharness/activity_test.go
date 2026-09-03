// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package managedharness

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	paimosdb "github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/models"
)

func registerActivitySession(t *testing.T, service *Service, projectID int64) models.HarnessSession {
	t.Helper()
	session, _, err := service.Register(context.Background(), RegisterInput{
		ProjectID: projectID, AgentName: "worker", Harness: "codex", Host: "activity-host", SessionRef: "activity-ref", WorkerLease: testWorkerLease,
		ManagementMode: ManagementManaged, Role: RoleWorker, SteerMode: SteerNone,
		Capabilities: models.HarnessCapabilities{Status: true, Interrupt: true, Stop: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func TestHarnessActivityRequiresTypedCurrentEvidence(t *testing.T) {
	projectID, _ := openManagedHarnessTestDB(t)
	service := NewService(paimosdb.DB)
	session := registerActivitySession(t, service, projectID)
	if session.ActivityState != ActivityUnknown || session.ActivityReason != ActivityUnreported || session.ActivityAge != nil {
		t.Fatalf("initial activity=%+v", session)
	}

	unreported, err := service.Heartbeat(context.Background(), session.ID, PhaseWorking)
	if err != nil || unreported.ActivityState != ActivityUnknown || unreported.ActivityReason != ActivityUnreported {
		t.Fatalf("unreported heartbeat=%+v err=%v", unreported, err)
	}
	busy, err := service.HeartbeatWithActivity(context.Background(), session.ID, PhaseWorking, ActivityEvidence{Sequence: 1, Kind: "turn_started"})
	if err != nil || busy.ActivityState != ActivityBusy || busy.ActivityReason != ActivityAdapter || busy.ActivitySequence != 1 || busy.ActivityAt == "" || busy.ActivityAge == nil {
		t.Fatalf("busy heartbeat=%+v err=%v", busy, err)
	}
	retry, err := service.HeartbeatWithActivity(context.Background(), session.ID, PhaseWorking, ActivityEvidence{Sequence: 1, Kind: "turn_started"})
	if err != nil || retry.ActivityState != ActivityBusy || retry.ActivityAt != busy.ActivityAt || retry.ActivitySequence != 1 {
		t.Fatalf("idempotent evidence=%+v err=%v", retry, err)
	}
	malformed, err := service.HeartbeatWithActivity(context.Background(), session.ID, PhaseWorking, ActivityEvidence{Sequence: 1, Kind: "turn_completed"})
	if err != nil || malformed.ActivityState != ActivityUnknown || malformed.ActivityReason != ActivityMalformed || malformed.ActivitySequence != 1 {
		t.Fatalf("malformed evidence=%+v err=%v", malformed, err)
	}
	recovered, err := service.HeartbeatWithActivity(context.Background(), session.ID, PhaseWorking, ActivityEvidence{Sequence: 1, Kind: "turn_started"})
	if err != nil || recovered.ActivityState != ActivityBusy || recovered.ActivityReason != ActivityAdapter || recovered.ActivityAt != busy.ActivityAt {
		t.Fatalf("recovered current evidence=%+v err=%v", recovered, err)
	}
	idle, err := service.HeartbeatWithActivity(context.Background(), session.ID, PhaseWorking, ActivityEvidence{Sequence: 2, Kind: "turn_completed"})
	if err != nil || idle.ActivityState != ActivityIdle || idle.ActivityReason != ActivityCompleted || idle.ActivitySequence != 2 {
		t.Fatalf("completed evidence=%+v err=%v", idle, err)
	}
	stale, err := service.HeartbeatWithActivity(context.Background(), session.ID, PhaseWorking, ActivityEvidence{Sequence: 1, Kind: "turn_started"})
	if err != nil || stale.ActivityState != ActivityIdle || stale.ActivityReason != ActivityCompleted || stale.ActivitySequence != 2 {
		t.Fatalf("stale evidence=%+v err=%v", stale, err)
	}
	recovered, err = service.HeartbeatWithActivity(context.Background(), session.ID, PhaseWorking, ActivityEvidence{Sequence: 2, Kind: "turn_completed"})
	if err != nil || recovered.ActivityState != ActivityIdle || recovered.ActivityReason != ActivityCompleted || recovered.ActivityAt != idle.ActivityAt {
		t.Fatalf("recovered latest evidence=%+v err=%v", recovered, err)
	}
	var heartbeatEvents int
	if err := paimosdb.DB.QueryRow(`SELECT COUNT(*) FROM harness_session_events WHERE harness_session_id=? AND operation='heartbeat'`, session.ID).Scan(&heartbeatEvents); err != nil || heartbeatEvents != 4 {
		t.Fatalf("projection heartbeat events=%d err=%v", heartbeatEvents, err)
	}
	closed, err := service.StopWithReason(context.Background(), session.ID, ClosedProcessExited)
	if err != nil || closed.ActivityState != ActivityDead || closed.ClosedReason != ClosedProcessExited {
		t.Fatalf("closed session=%+v err=%v", closed, err)
	}
	replayed, err := service.StopWithReason(context.Background(), session.ID, ClosedOwnershipLost)
	if err != nil || replayed.ClosedReason != ClosedProcessExited || replayed.ActivityReason != ClosedProcessExited {
		t.Fatalf("terminal retry changed first reason: session=%+v err=%v", replayed, err)
	}
	var stopEvents int
	if err := paimosdb.DB.QueryRow(`SELECT COUNT(*) FROM harness_session_events WHERE harness_session_id=? AND operation='stop'`, session.ID).Scan(&stopEvents); err != nil || stopEvents != 1 {
		t.Fatalf("stop events=%d err=%v", stopEvents, err)
	}
}

func TestRoutineHeartbeatYieldCyclesDoNotGrowActivityLog(t *testing.T) {
	projectID, _ := openManagedHarnessTestDB(t)
	service := NewService(paimosdb.DB)
	session := registerActivitySession(t, service, projectID)
	evidence := ActivityEvidence{Sequence: 1, Kind: "turn_started"}
	for range 3 {
		if _, err := service.HeartbeatWithActivity(context.Background(), session.ID, PhaseWorking, evidence); err != nil {
			t.Fatal(err)
		}
		yielded, err := service.Yield(context.Background(), session.ID)
		if err != nil || len(yielded.Controls) != 0 {
			t.Fatalf("no-control yield=%+v err=%v", yielded, err)
		}
	}
	var events int
	if err := paimosdb.DB.QueryRow(`SELECT COUNT(*) FROM harness_session_events WHERE harness_session_id=?`, session.ID).Scan(&events); err != nil || events != 2 {
		t.Fatalf("routine cycle events=%d err=%v", events, err)
	}
}

func TestSessionStartedEvidenceDoesNotInventBusyOrIdle(t *testing.T) {
	projectID, _ := openManagedHarnessTestDB(t)
	service := NewService(paimosdb.DB)
	session := registerActivitySession(t, service, projectID)
	reported, err := service.HeartbeatWithActivity(context.Background(), session.ID, PhaseWorking, ActivityEvidence{Sequence: 1, Kind: "session_started"})
	if err != nil || reported.ActivityState != ActivityUnknown || reported.ActivityReason != ActivityUnreported ||
		reported.ActivitySequence != 1 || reported.ActivityKind != "session_started" || reported.ActivityAt != "" || reported.ActivityAge != nil {
		t.Fatalf("session-start evidence=%+v err=%v", reported, err)
	}
}

func TestRegistrationAppendsGenesisEventAtomically(t *testing.T) {
	projectID, _ := openManagedHarnessTestDB(t)
	service := NewService(paimosdb.DB)
	managed := registerActivitySession(t, service, projectID)

	assertGenesis := func(session models.HarnessSession, wantReason string) {
		t.Helper()
		var operation, phase, state, reason, kind, closed string
		var eventSequence, activitySequence int64
		if err := paimosdb.DB.QueryRow(`SELECT event_sequence,operation,phase,activity_state,activity_reason,activity_event_kind,activity_sequence,closed_reason
			FROM harness_session_events WHERE harness_session_id=?`, session.ID).Scan(
			&eventSequence, &operation, &phase, &state, &reason, &kind, &activitySequence, &closed); err != nil {
			t.Fatal(err)
		}
		if eventSequence != session.Revision || eventSequence != 1 || operation != "register" || phase != PhaseStarting || state != ActivityUnknown ||
			reason != wantReason || kind != "" || activitySequence != 0 || closed != "" {
			t.Fatalf("genesis event session=%s sequence=%d operation=%s phase=%s state=%s reason=%s kind=%s activity_sequence=%d closed=%s",
				session.ID, eventSequence, operation, phase, state, reason, kind, activitySequence, closed)
		}
	}
	assertGenesis(managed, ActivityUnreported)

	unmanagedInput := RegisterInput{
		ProjectID: projectID, AgentName: "worker", Harness: "claude", Host: "external-host", SessionRef: "external-ref", WorkerLease: testWorkerLease,
		ManagementMode: ManagementUnmanaged, Role: RoleWorker, SteerMode: SteerNone, Capabilities: models.HarnessCapabilities{Status: true},
	}
	unmanaged, created, err := service.Register(context.Background(), unmanagedInput)
	if err != nil || !created {
		t.Fatalf("unmanaged register created=%v err=%v", created, err)
	}
	assertGenesis(unmanaged, ActivityUnmanaged)
	replay, created, err := service.Register(context.Background(), unmanagedInput)
	if err != nil || created || replay.ID != unmanaged.ID {
		t.Fatalf("register replay session=%+v created=%v err=%v", replay, created, err)
	}
	var replayEvents int
	if err := paimosdb.DB.QueryRow(`SELECT COUNT(*) FROM harness_session_events WHERE harness_session_id=?`, unmanaged.ID).Scan(&replayEvents); err != nil || replayEvents != 1 {
		t.Fatalf("register replay events=%d err=%v", replayEvents, err)
	}

	if _, err := paimosdb.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'worker2')`, projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := paimosdb.DB.Exec(`CREATE TRIGGER fixture_reject_register_event BEFORE INSERT ON harness_session_events
		WHEN NEW.operation='register' BEGIN SELECT RAISE(ABORT,'fixture register event reject'); END`); err != nil {
		t.Fatal(err)
	}
	_, created, err = service.Register(context.Background(), RegisterInput{
		ProjectID: projectID, AgentName: "worker2", Harness: "codex", Host: "rollback-host", SessionRef: "rollback-ref", WorkerLease: testWorkerLease,
		ManagementMode: ManagementManaged, Role: RoleWorker, SteerMode: SteerNone, Capabilities: models.HarnessCapabilities{Status: true},
	})
	if err == nil || created || !strings.Contains(err.Error(), "fixture register event reject") {
		t.Fatalf("register event failure created=%v err=%v", created, err)
	}
	var rolledBack int
	if err := paimosdb.DB.QueryRow(`SELECT COUNT(*) FROM harness_sessions WHERE project_id=? AND agent_name='worker2'`, projectID).Scan(&rolledBack); err != nil || rolledBack != 0 {
		t.Fatalf("rolled-back sessions=%d err=%v", rolledBack, err)
	}
	var registerEvents int
	if err := paimosdb.DB.QueryRow(`SELECT COUNT(*) FROM harness_session_events WHERE operation='register'`).Scan(&registerEvents); err != nil || registerEvents != 2 {
		t.Fatalf("register events after rollback=%d err=%v", registerEvents, err)
	}
}

func TestHarnessSessionEventsAreTransactionalImmutableAndPhaseIndependent(t *testing.T) {
	projectID, _ := openManagedHarnessTestDB(t)
	service := NewService(paimosdb.DB)
	session := registerActivitySession(t, service, projectID)
	if _, err := service.HeartbeatWithActivity(context.Background(), session.ID, PhaseWorking, ActivityEvidence{Sequence: 1, Kind: "tool_started"}); err != nil {
		t.Fatal(err)
	}
	control, err := service.RequestControl(context.Background(), session.ID, ControlInterrupt, 1)
	if err != nil {
		t.Fatal(err)
	}
	yielded, err := service.Yield(context.Background(), session.ID)
	if err != nil || yielded.Session.Phase != PhaseYielded || yielded.Session.ActivityState != ActivityBusy || len(yielded.Controls) != 1 {
		t.Fatalf("yield=%+v err=%v", yielded, err)
	}
	if _, err := service.CompleteControl(context.Background(), session.ID, control.ID, ControlApplied, ReasonApplied); err != nil {
		t.Fatal(err)
	}
	if _, err := service.StopWithReason(context.Background(), session.ID, ClosedStopped); err != nil {
		t.Fatal(err)
	}

	rows, err := paimosdb.DB.Query(`SELECT operation FROM harness_session_events WHERE harness_session_id=? ORDER BY event_sequence`, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var operations []string
	for rows.Next() {
		var operation string
		if err := rows.Scan(&operation); err != nil {
			t.Fatal(err)
		}
		operations = append(operations, operation)
	}
	if got := strings.Join(operations, ","); got != "register,heartbeat,yield,control_completed,stop" {
		t.Fatalf("operations=%s", got)
	}
	if _, err := paimosdb.DB.Exec(`UPDATE harness_session_events SET phase='working' WHERE harness_session_id=?`, session.ID); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("event update error=%v", err)
	}
	if _, err := paimosdb.DB.Exec(`DELETE FROM harness_session_events WHERE harness_session_id=?`, session.ID); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("event delete error=%v", err)
	}
	if _, err := paimosdb.DB.Exec(`DELETE FROM harness_sessions WHERE id=?`, session.ID); err != nil {
		t.Fatalf("parent cascade delete: %v", err)
	}
	var remaining int
	if err := paimosdb.DB.QueryRow(`SELECT COUNT(*) FROM harness_session_events WHERE harness_session_id=?`, session.ID).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("cascade event count=%d err=%v", remaining, err)
	}
}

func TestUnmanagedHeartbeatCannotAssertAdapterActivity(t *testing.T) {
	projectID, _ := openManagedHarnessTestDB(t)
	service := NewService(paimosdb.DB)
	session, _, err := service.Register(context.Background(), RegisterInput{
		ProjectID: projectID, AgentName: "worker", Harness: "codex", Host: "external-host", SessionRef: "external-ref", WorkerLease: testWorkerLease,
		ManagementMode: ManagementUnmanaged, Role: RoleWorker, SteerMode: SteerNone,
		Capabilities: models.HarnessCapabilities{Status: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if session.ActivityState != ActivityUnknown || session.ActivityReason != ActivityUnmanaged {
		t.Fatalf("registered unmanaged activity=%+v", session)
	}
	reported, err := service.HeartbeatWithActivity(context.Background(), session.ID, PhaseWorking, ActivityEvidence{Sequence: 99, Kind: "tool_started"})
	if err != nil || reported.ActivityState != ActivityUnknown || reported.ActivityReason != ActivityUnmanaged || reported.ActivitySequence != 0 || reported.ActivityAt != "" {
		t.Fatalf("unmanaged evidence=%+v err=%v", reported, err)
	}
	if _, err := paimosdb.DB.Exec(`UPDATE harness_sessions SET activity_state='busy',activity_reason='adapter_activity',activity_event_kind='tool_started',activity_sequence=1,activity_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, session.ID); err == nil || !strings.Contains(err.Error(), "inconsistent") {
		t.Fatalf("unmanaged busy invariant error=%v", err)
	}
}

func TestConcurrentActivityTimeoutAppendsOneTransition(t *testing.T) {
	projectID, _ := openManagedHarnessTestDB(t)
	service := NewService(paimosdb.DB)
	session := registerActivitySession(t, service, projectID)
	if _, err := service.HeartbeatWithActivity(context.Background(), session.ID, PhaseWorking, ActivityEvidence{Sequence: 1, Kind: "turn_started"}); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-5 * time.Minute).Format("2006-01-02T15:04:05.000Z")
	if _, err := paimosdb.DB.Exec(`UPDATE harness_sessions SET heartbeat_at=? WHERE id=?`, old, session.ID); err != nil {
		t.Fatal(err)
	}

	const evaluators = 8
	results := make(chan int, evaluators)
	errors := make(chan error, evaluators)
	var ready sync.WaitGroup
	ready.Add(evaluators)
	start := make(chan struct{})
	for range evaluators {
		go func() {
			ready.Done()
			<-start
			count, err := service.ReconcileStaleActivity(context.Background(), time.Now().UTC(), DefaultActivityHeartbeatTimeout)
			results <- count
			errors <- err
		}()
	}
	ready.Wait()
	close(start)
	total := 0
	for range evaluators {
		total += <-results
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}
	if total != 1 {
		t.Fatalf("timeout transitions=%d want 1", total)
	}
	current, err := service.Get(context.Background(), projectID, session.ID)
	if err != nil || current.ActivityState != ActivityUnknown || current.ActivityReason != ActivityStale {
		t.Fatalf("current=%+v err=%v", current, err)
	}
	var count int
	if err := paimosdb.DB.QueryRow(`SELECT COUNT(*) FROM harness_session_events WHERE harness_session_id=? AND operation='activity_timeout'`, session.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("timeout events=%d err=%v", count, err)
	}
	recovered, err := service.HeartbeatWithActivity(context.Background(), session.ID, PhaseWorking, ActivityEvidence{Sequence: 1, Kind: "turn_started"})
	if err != nil || recovered.ActivityState != ActivityBusy || recovered.ActivityReason != ActivityAdapter || recovered.ActivityAt != current.ActivityAt || recovered.ActivitySequence != 1 {
		t.Fatalf("same-sequence recovery=%+v stale=%+v err=%v", recovered, current, err)
	}
	if err := paimosdb.DB.QueryRow(`SELECT COUNT(*) FROM harness_session_events WHERE harness_session_id=? AND operation='heartbeat'`, session.ID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("projection heartbeat events=%d err=%v", count, err)
	}
}

func TestActivityTimeoutSkipsUnmanagedAndContinuesToManagedSession(t *testing.T) {
	projectID, agentID := openManagedHarnessTestDB(t)
	service := NewService(paimosdb.DB)
	const unmanagedID = "11111111-1111-4111-8111-111111111111"
	const managedID = "22222222-2222-4222-8222-222222222222"
	for _, fixture := range []struct {
		id, harness, mode, reason string
	}{
		{unmanagedID, "claude", ManagementUnmanaged, ActivityUnmanaged},
		{managedID, "codex", ManagementManaged, ActivityUnreported},
	} {
		if _, err := paimosdb.DB.Exec(`INSERT INTO harness_sessions(
			id,project_id,project_agent_id,agent_name,harness,host,session_ref_digest,worker_lease_digest,
			management_mode,role,steer_mode,advertised_inbox,advertised_status,advertised_steer,advertised_interrupt,advertised_stop,phase,activity_reason)
			VALUES(?,?,?,'worker',?,'timeout-fixture',zeroblob(32),zeroblob(32),?,'worker','none',0,1,0,0,0,'starting',?)`,
			fixture.id, projectID, agentID, fixture.harness, fixture.mode, fixture.reason); err != nil {
			t.Fatal(err)
		}
		if _, err := paimosdb.DB.Exec(`INSERT INTO harness_session_events(
			harness_session_id,event_sequence,operation,phase,activity_state,activity_reason,activity_event_kind,activity_sequence,closed_reason)
			VALUES(?,1,'register','starting','unknown',?,'',0,'')`, fixture.id, fixture.reason); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.HeartbeatWithActivity(context.Background(), unmanagedID, PhaseWorking, ActivityEvidence{Sequence: 1, Kind: "turn_started"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.HeartbeatWithActivity(context.Background(), managedID, PhaseWorking, ActivityEvidence{Sequence: 1, Kind: "turn_started"}); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-5 * time.Minute).Format("2006-01-02T15:04:05.000Z")
	if _, err := paimosdb.DB.Exec(`UPDATE harness_sessions SET heartbeat_at=? WHERE id IN (?,?)`, old, unmanagedID, managedID); err != nil {
		t.Fatal(err)
	}
	count, err := service.ReconcileStaleActivity(context.Background(), time.Now().UTC(), DefaultActivityHeartbeatTimeout)
	if err != nil || count != 1 {
		t.Fatalf("reconcile count=%d err=%v", count, err)
	}
	unmanagedAfter, err := service.Get(context.Background(), projectID, unmanagedID)
	if err != nil || unmanagedAfter.ActivityState != ActivityUnknown || unmanagedAfter.ActivityReason != ActivityUnmanaged {
		t.Fatalf("unmanaged=%+v err=%v", unmanagedAfter, err)
	}
	managedAfter, err := service.Get(context.Background(), projectID, managedID)
	if err != nil || managedAfter.ActivityState != ActivityUnknown || managedAfter.ActivityReason != ActivityStale {
		t.Fatalf("managed=%+v err=%v", managedAfter, err)
	}
}
