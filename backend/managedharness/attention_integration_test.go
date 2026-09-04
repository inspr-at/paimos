// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package managedharness

import (
	"context"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/agentmessage"
	paimosdb "github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/models"
)

func TestAttentionProjectsRealStaleAndStopTransitionsWithRetainedActivityKind(t *testing.T) {
	t.Setenv("PAIMOS_AGENT_BUS_INSTANCE", "ppm")
	projectID, _ := openManagedHarnessTestDB(t)
	var actorID int64
	if err := paimosdb.DB.QueryRow(`SELECT id FROM users WHERE username='harness-actor'`).Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	agent, err := paimosdb.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'amy')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	receiverID, _ := agent.LastInsertId()
	if _, err := paimosdb.DB.Exec(`UPDATE instance_orchestrator SET project_agent_id=?,display_label='Amy',revision=1,
		updated_by_user_id=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE singleton_id=1`, receiverID, actorID); err != nil {
		t.Fatal(err)
	}
	attention := agentmessage.NewService(paimosdb.DB)
	if _, err := attention.RegisterTarget(context.Background(), agentmessage.RegisterTargetInput{
		ProjectID: projectID, Address: "codex:amy", Adapter: agentmessage.AdapterCodex,
		TargetKind: agentmessage.TargetKindCodexThread, TargetRef: "11111111-1111-4111-8111-111111111111",
		MaximumLevel: "simple", Role: "primary",
	}); err != nil {
		t.Fatal(err)
	}
	if projected, err := attention.ProjectAttention(context.Background()); err != nil || projected != 0 {
		t.Fatalf("bootstrap attention projection=%d err=%v", projected, err)
	}

	harness := NewService(paimosdb.DB)
	session, _, err := harness.Register(context.Background(), RegisterInput{
		ProjectID: projectID, AgentName: "worker", Harness: "codex", Host: "attention-production-path",
		SessionRef: "attention-session", WorkerLease: testWorkerLease, ManagementMode: ManagementManaged,
		Role: RoleWorker, SteerMode: SteerNone, Capabilities: models.HarnessCapabilities{Status: true, Interrupt: true, Stop: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := harness.HeartbeatWithActivity(context.Background(), session.ID, PhaseWorking, ActivityEvidence{
		Sequence: 1, Kind: "turn_started",
	}); err != nil {
		t.Fatal(err)
	}
	if reconciled, err := harness.ReconcileStaleActivity(context.Background(), time.Now().UTC().Add(5*time.Minute), DefaultActivityHeartbeatTimeout); err != nil || reconciled != 1 {
		t.Fatalf("reconciled=%d err=%v", reconciled, err)
	}
	if inserted, err := attention.ProjectAttention(context.Background()); err != nil || inserted != 1 {
		t.Fatalf("stale attention inserted=%d err=%v", inserted, err)
	}

	if _, err := harness.StopWithReason(context.Background(), session.ID, ClosedProcessExited); err != nil {
		t.Fatal(err)
	}
	if inserted, err := attention.ProjectAttention(context.Background()); err != nil || inserted != 1 {
		t.Fatalf("dead attention inserted=%d err=%v", inserted, err)
	}
	var staleKind, stopKind string
	if err := paimosdb.DB.QueryRow(`SELECT activity_event_kind FROM harness_session_events
		WHERE harness_session_id=? AND operation='activity_timeout'`, session.ID).Scan(&staleKind); err != nil {
		t.Fatal(err)
	}
	if err := paimosdb.DB.QueryRow(`SELECT activity_event_kind FROM harness_session_events
		WHERE harness_session_id=? AND operation='stop'`, session.ID).Scan(&stopKind); err != nil {
		t.Fatal(err)
	}
	if staleKind != "turn_started" || stopKind != "turn_started" {
		t.Fatalf("production transitions did not retain prior activity kind: stale=%q stop=%q", staleKind, stopKind)
	}
	rows, err := paimosdb.DB.Query(`SELECT reason_code FROM agent_attention_items ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var reasons []string
	for rows.Next() {
		var reason string
		if err := rows.Scan(&reason); err != nil {
			t.Fatal(err)
		}
		reasons = append(reasons, reason)
	}
	if len(reasons) != 2 || reasons[0] != "heartbeat_stale" || reasons[1] != "process_exited" {
		t.Fatalf("attention reasons=%v", reasons)
	}
}

func TestAttentionTurnEndUsesImmutableEventTimeAssignmentSnapshot(t *testing.T) {
	t.Setenv("PAIMOS_AGENT_BUS_INSTANCE", "ppm")
	projectID, _ := openManagedHarnessTestDB(t)
	var actorID int64
	if err := paimosdb.DB.QueryRow(`SELECT id FROM users WHERE username='harness-actor'`).Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	receiver, err := paimosdb.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'amy')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	receiverID, _ := receiver.LastInsertId()
	lateAgent, err := paimosdb.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'late-worker')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	lateAgentID, _ := lateAgent.LastInsertId()
	if _, err := paimosdb.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'unassigned-worker')`, projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := paimosdb.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'parent-worker')`, projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := paimosdb.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'archived-worker')`, projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := paimosdb.DB.Exec(`UPDATE instance_orchestrator SET project_agent_id=?,display_label='Amy',revision=1,
		updated_by_user_id=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE singleton_id=1`, receiverID, actorID); err != nil {
		t.Fatal(err)
	}
	attention := agentmessage.NewService(paimosdb.DB)
	if _, err := attention.RegisterTarget(context.Background(), agentmessage.RegisterTargetInput{
		ProjectID: projectID, Address: "codex:amy", Adapter: agentmessage.AdapterCodex,
		TargetKind: agentmessage.TargetKindCodexThread, TargetRef: "11111111-1111-4111-8111-111111111111",
		MaximumLevel: "simple", Role: "primary",
	}); err != nil {
		t.Fatal(err)
	}
	if projected, err := attention.ProjectAttention(context.Background()); err != nil || projected != 0 {
		t.Fatalf("bootstrap attention projection=%d err=%v", projected, err)
	}

	issue, err := paimosdb.DB.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status,priority)
		VALUES(?,901,'ticket','Assigned work','in-progress','high')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	issueID, _ := issue.LastInsertId()
	archivedIssue, err := paimosdb.DB.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status,priority)
		VALUES(?,903,'ticket','Archived work','archived','low')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	archivedIssueID, _ := archivedIssue.LastInsertId()

	harness := NewService(paimosdb.DB)
	register := func(name, host, ref string, parentSessionID *string, ticketID *int64) models.HarnessSession {
		t.Helper()
		shape := ""
		if ticketID != nil {
			shape = "ship"
		}
		session, _, err := harness.Register(context.Background(), RegisterInput{
			ProjectID: projectID, AgentName: name, Harness: "codex", Host: host,
			SessionRef: ref, WorkerLease: testWorkerLease, ManagementMode: ManagementManaged,
			Role: RoleWorker, ParentSessionID: parentSessionID, TicketID: ticketID, WorkShape: shape,
			SteerMode: SteerNone, Capabilities: models.HarnessCapabilities{Status: true},
		})
		if err != nil {
			t.Fatal(err)
		}
		return session
	}
	parent := register("parent-worker", "assignment-snapshot-parent", "assignment-snapshot-parent", nil, nil)
	assigned := register("worker", "assignment-snapshot-assigned", "assignment-snapshot-assigned", &parent.ID, &issueID)
	unassigned := register("unassigned-worker", "assignment-snapshot-unassigned", "assignment-snapshot-unassigned", nil, nil)
	late := register("late-worker", "assignment-snapshot-late", "assignment-snapshot-late", nil, nil)
	archived := register("archived-worker", "assignment-snapshot-archived", "assignment-snapshot-archived", nil, &archivedIssueID)
	completeTurn := func(session models.HarnessSession) {
		t.Helper()
		if _, err := harness.HeartbeatWithActivity(context.Background(), session.ID, PhaseWorking, ActivityEvidence{Sequence: 1, Kind: "turn_started"}); err != nil {
			t.Fatal(err)
		}
		if _, err := harness.HeartbeatWithActivity(context.Background(), session.ID, PhaseWorking, ActivityEvidence{Sequence: 2, Kind: "turn_completed"}); err != nil {
			t.Fatal(err)
		}
	}
	completeTurn(assigned)
	completeTurn(unassigned)
	completeTurn(late)
	completeTurn(archived)

	// Delayed projection must not derive assignment truth from mutable current
	// rows. Move the still-open issue's update time strictly after the event;
	// the old projection query permanently lost this completion at its watermark.
	var completedAt string
	if err := paimosdb.DB.QueryRow(`SELECT created_at FROM harness_session_events
		WHERE harness_session_id=? AND activity_event_kind='turn_completed'`, assigned.ID).Scan(&completedAt); err != nil {
		t.Fatal(err)
	}
	eventTime, err := time.Parse(time.RFC3339Nano, completedAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := paimosdb.DB.Exec(`UPDATE issues SET title='Assigned work revised',updated_at=? WHERE id=?`,
		eventTime.Add(time.Second).UTC().Format("2006-01-02T15:04:05.000Z"), issueID); err != nil {
		t.Fatal(err)
	}

	lateIssue, err := paimosdb.DB.Exec(`INSERT INTO issues(project_id,issue_number,type,title,status,priority)
		VALUES(?,902,'ticket','Late assignment','in-progress','high')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	lateIssueID, _ := lateIssue.LastInsertId()
	if _, err := paimosdb.DB.Exec(`INSERT INTO product_sessions(
		product_session_id,project_id,target_kind,target_project_agent_id,node_id,title,created_by_user_id,updated_by_user_id)
		VALUES('22222222-2222-4222-8222-222222222902',?,'project_agent',?,?,'Late session',?,?)`,
		projectID, lateAgentID, lateIssueID, actorID, actorID); err != nil {
		t.Fatal(err)
	}

	if inserted, err := attention.ProjectAttention(context.Background()); err != nil || inserted != 1 {
		t.Fatalf("delayed projection inserted=%d err=%v", inserted, err)
	}
	if inserted, err := attention.ProjectAttention(context.Background()); err != nil || inserted != 0 {
		t.Fatalf("replayed projection inserted=%d err=%v", inserted, err)
	}
	var sourceID, kind string
	if err := paimosdb.DB.QueryRow(`SELECT source_id,attention_kind FROM agent_attention_items`).Scan(&sourceID, &kind); err != nil {
		t.Fatal(err)
	}
	if sourceID != assigned.ID || kind != "assignment_turn_ended" {
		t.Fatalf("attention source=%q kind=%q want assigned completion", sourceID, kind)
	}
	for _, check := range []struct {
		sessionID string
		want      int
	}{
		{assigned.ID, 1},
		{unassigned.ID, 0},
		{late.ID, 0},
		{archived.ID, 0},
	} {
		var snapshot int
		if err := paimosdb.DB.QueryRow(`SELECT assignment_present FROM harness_session_events
			WHERE harness_session_id=? AND activity_event_kind='turn_completed'`, check.sessionID).Scan(&snapshot); err != nil || snapshot != check.want {
			t.Fatalf("session=%s assignment snapshot=%d err=%v want=%d", check.sessionID, snapshot, err, check.want)
		}
	}
	var eventParent string
	var eventTicket int64
	if err := paimosdb.DB.QueryRow(`SELECT after_parent_harness_session_id,after_ticket_id FROM harness_session_events
		WHERE harness_session_id=? AND activity_event_kind='turn_completed'`, assigned.ID).Scan(&eventParent, &eventTicket); err != nil {
		t.Fatal(err)
	}
	if eventParent != parent.ID || eventTicket != issueID {
		t.Fatalf("bound attention event parent=%q ticket=%d want parent=%q ticket=%d", eventParent, eventTicket, parent.ID, issueID)
	}
}
