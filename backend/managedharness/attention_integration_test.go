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
