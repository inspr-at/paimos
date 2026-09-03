// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentmessage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	paimosdb "github.com/inspr-at/paimos/backend/db"
)

func configureAttentionReceiver(t *testing.T, service *Service, projectID int64) int64 {
	t.Helper()
	var agentID int64
	if err := paimosdb.DB.QueryRow(`SELECT id FROM project_agents WHERE project_id=? AND name='amy'`, projectID).Scan(&agentID); err != nil {
		t.Fatal(err)
	}
	actor, err := paimosdb.DB.Exec(`INSERT INTO users(username,password,role) VALUES('attention-admin','disabled','admin')`)
	if err != nil {
		t.Fatal(err)
	}
	actorID, _ := actor.LastInsertId()
	if _, err := paimosdb.DB.Exec(`UPDATE instance_orchestrator SET project_agent_id=?,display_label='Amy',revision=1,
		updated_by_user_id=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE singleton_id=1`, agentID, actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RegisterTarget(context.Background(), RegisterTargetInput{
		ProjectID: projectID, Address: "codex:amy", Adapter: AdapterCodex, TargetKind: TargetKindCodexThread,
		TargetRef: "01a059fb-4bf4-7881-a38a-7a2e8e60af30", MaximumLevel: "simple", Role: "primary",
	}); err != nil {
		t.Fatal(err)
	}
	if projected, err := service.ProjectAttention(context.Background()); err != nil || projected != 0 {
		t.Fatalf("bootstrap attention projection=%d err=%v", projected, err)
	}
	return actorID
}

func addAttentionWorker(t *testing.T, projectID int64) (int64, string) {
	t.Helper()
	result, err := paimosdb.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'worker')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	agentID, _ := result.LastInsertId()
	sessionID := "22222222-2222-4222-8222-222222222222"
	if _, err := paimosdb.DB.Exec(`INSERT INTO harness_sessions(id,project_id,project_agent_id,agent_name,harness,host,
		session_ref_digest,worker_lease_digest,management_mode,role,steer_mode,advertised_inbox,advertised_status,
		advertised_steer,advertised_interrupt,advertised_stop,phase)
		VALUES(?,?,?,?,?,'attention-host',zeroblob(32),zeroblob(32),'managed','worker','none',0,1,0,1,1,'working')`,
		sessionID, projectID, agentID, "worker", "codex"); err != nil {
		t.Fatal(err)
	}
	return agentID, sessionID
}

func TestClassifyAttentionTransitionClosedPolicy(t *testing.T) {
	tests := []struct {
		name string
		in   AttentionTransition
		want AttentionDecision
	}{
		{"busy absorbed", AttentionTransition{ActivityState: "busy", ActivityReason: "adapter_activity", ActivityKind: "tool_started"}, AttentionDecision{Disposition: "absorbed"}},
		{"assigned turn end", AttentionTransition{ActivityState: "idle", ActivityReason: "turn_completed", ActivityKind: "turn_completed", AssignmentKnown: true, Assigned: true}, AttentionDecision{Disposition: "actionable", Kind: "assignment_turn_ended", Reason: "turn_completed_open_assignment"}},
		{"unassigned turn end", AttentionTransition{ActivityState: "idle", ActivityReason: "turn_completed", ActivityKind: "turn_completed", AssignmentKnown: true}, AttentionDecision{Disposition: "absorbed"}},
		{"historical turn end without assignment snapshot", AttentionTransition{ActivityState: "idle", ActivityReason: "turn_completed", ActivityKind: "turn_completed"}, AttentionDecision{Disposition: "deferred"}},
		{"unknown worker", AttentionTransition{ActivityState: "unknown", ActivityReason: "heartbeat_stale"}, AttentionDecision{Disposition: "actionable", Kind: "worker_unknown", Reason: "heartbeat_stale"}},
		{"stale worker retains prior tool", AttentionTransition{ActivityState: "unknown", ActivityReason: "heartbeat_stale", ActivityKind: "tool_started"}, AttentionDecision{Disposition: "actionable", Kind: "worker_unknown", Reason: "heartbeat_stale"}},
		{"unmanaged evidence deferred", AttentionTransition{ActivityState: "unknown", ActivityReason: "unmanaged_evidence"}, AttentionDecision{Disposition: "deferred"}},
		{"dead worker", AttentionTransition{ActivityState: "dead", ActivityReason: "process_failed"}, AttentionDecision{Disposition: "actionable", Kind: "worker_dead", Reason: "process_failed"}},
		{"dead worker retains prior turn", AttentionTransition{ActivityState: "dead", ActivityReason: "process_exited", ActivityKind: "turn_started"}, AttentionDecision{Disposition: "actionable", Kind: "worker_dead", Reason: "process_exited"}},
		{"unknown kind", AttentionTransition{ActivityState: "busy", ActivityReason: "adapter_activity", ActivityKind: "future_event"}, AttentionDecision{Disposition: "deferred"}},
		{"unknown state", AttentionTransition{ActivityState: "future_state", ActivityReason: "future_reason"}, AttentionDecision{Disposition: "deferred"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyAttentionTransition(tc.in); got != tc.want {
				t.Fatalf("got=%#v want=%#v", got, tc.want)
			}
		})
	}
}

func TestAttentionFirstEnableSeedsHistoryAndNoOpPollSkipsWriter(t *testing.T) {
	service, projectID := openBusTestDB(t)
	_, sessionID := addAttentionWorker(t, projectID)
	if _, err := paimosdb.DB.Exec(`INSERT INTO harness_session_events(harness_session_id,event_sequence,operation,phase,
		activity_state,activity_reason,activity_event_kind,activity_sequence)
		VALUES(?,1,'activity_timeout','working','unknown','heartbeat_stale','tool_started',1)`, sessionID); err != nil {
		t.Fatal(err)
	}
	configureAttentionReceiver(t, service, projectID)
	var items, cursors int
	if err := paimosdb.DB.QueryRow(`SELECT COUNT(*) FROM agent_attention_items`).Scan(&items); err != nil {
		t.Fatal(err)
	}
	if err := paimosdb.DB.QueryRow(`SELECT COUNT(*) FROM agent_attention_projection_cursors`).Scan(&cursors); err != nil {
		t.Fatal(err)
	}
	if items != 0 || cursors != len(attentionProjectionSources) {
		t.Fatalf("historical items=%d cursors=%d", items, cursors)
	}
	authorityCalls := 0
	inserted, err := service.projectAttention(context.Background(), func(context.Context, *sql.Tx) (bool, error) {
		authorityCalls++
		return true, nil
	})
	if err != nil || inserted != 0 || authorityCalls != 0 {
		t.Fatalf("no-op projection=%d err=%v writer_authority_calls=%d", inserted, err, authorityCalls)
	}
	if _, err := paimosdb.DB.Exec(`INSERT INTO harness_session_events(harness_session_id,event_sequence,operation,phase,
		activity_state,activity_reason,activity_event_kind,activity_sequence,closed_reason)
		VALUES(?,2,'stop','stopped','dead','process_exited','tool_started',1,'process_exited')`, sessionID); err != nil {
		t.Fatal(err)
	}
	if inserted, err := service.ProjectAttention(context.Background()); err != nil || inserted != 1 {
		t.Fatalf("new transition projection=%d err=%v", inserted, err)
	}
}

func TestAttentionTransactionRevocationLeavesProjectionLeaseAndAckUntouched(t *testing.T) {
	service, projectID := openBusTestDB(t)
	configureAttentionReceiver(t, service, projectID)
	_, sessionID := addAttentionWorker(t, projectID)
	if _, err := paimosdb.DB.Exec(`INSERT INTO harness_session_events(harness_session_id,event_sequence,operation,phase,
		activity_state,activity_reason,activity_event_kind,activity_sequence)
		VALUES(?,1,'activity_timeout','working','unknown','heartbeat_stale','tool_started',7)`, sessionID); err != nil {
		t.Fatal(err)
	}
	revoked := func(context.Context, *sql.Tx) (bool, error) {
		return false, coded("agent_message_unauthorized", "current request credential is unavailable")
	}
	_, err := service.ListAttention(context.Background(), AttentionInput{
		ProjectID: projectID, Address: "codex:amy", Agent: "amy", WorkerAdapter: AdapterCodex, Authority: revoked,
	})
	var codedErr *CodedError
	if !errors.As(err, &codedErr) || codedErr.Code != "agent_message_unauthorized" {
		t.Fatalf("list error=%v", err)
	}
	for label, check := range map[string]struct {
		query string
		want  int
	}{
		"items":   {`SELECT COUNT(*) FROM agent_attention_items`, 0},
		"batches": {`SELECT COUNT(*) FROM agent_attention_batches`, 0},
		"cursors": {`SELECT COUNT(*) FROM agent_attention_projection_cursors`, len(attentionProjectionSources)},
	} {
		var count int
		if err := paimosdb.DB.QueryRow(check.query).Scan(&count); err != nil || count != check.want {
			t.Fatalf("%s count=%d err=%v want=%d", label, count, err, check.want)
		}
	}

	page, err := service.ListAttention(context.Background(), AttentionInput{
		ProjectID: projectID, Address: "codex:amy", Agent: "amy", WorkerAdapter: AdapterCodex,
	})
	if err != nil || page.Work == nil || page.Work.State != "leased" {
		t.Fatalf("trusted list page=%#v err=%v", page, err)
	}
	_, err = service.AckAttention(context.Background(), AttentionAckInput{
		ProjectID: projectID, Address: "codex:amy", Agent: "amy", Cursor: page.NextCursor, BatchID: page.Work.BatchID, Authority: revoked,
	})
	if !errors.As(err, &codedErr) || codedErr.Code != "agent_message_unauthorized" {
		t.Fatalf("ack error=%v", err)
	}
	var state string
	if err := paimosdb.DB.QueryRow(`SELECT state FROM agent_attention_batches WHERE batch_id=?`, page.Work.BatchID).Scan(&state); err != nil || state != "leased" {
		t.Fatalf("batch state=%q err=%v", state, err)
	}
	var cursorCount int
	if err := paimosdb.DB.QueryRow(`SELECT COUNT(*) FROM agent_attention_cursors`).Scan(&cursorCount); err != nil || cursorCount != 0 {
		t.Fatalf("ack cursor count=%d err=%v", cursorCount, err)
	}
}

func TestAttentionProjectionRejectsTransactionCurrentOrchestratorReassignment(t *testing.T) {
	service, projectID := openBusTestDB(t)
	configureAttentionReceiver(t, service, projectID)
	bobResult, err := paimosdb.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'bob')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	bobID, _ := bobResult.LastInsertId()
	if _, err := service.RegisterTarget(context.Background(), RegisterTargetInput{
		ProjectID: projectID, Address: "codex:bob", Adapter: AdapterCodex, TargetKind: TargetKindCodexThread,
		TargetRef: "019d-bob-codex-thread", MaximumLevel: "simple", Role: "primary",
	}); err != nil {
		t.Fatal(err)
	}
	_, sessionID := addAttentionWorker(t, projectID)
	if _, err := paimosdb.DB.Exec(`INSERT INTO harness_session_events(harness_session_id,event_sequence,operation,phase,
		activity_state,activity_reason,activity_event_kind,activity_sequence)
		VALUES(?,1,'activity_timeout','working','unknown','heartbeat_stale','tool_started',7)`, sessionID); err != nil {
		t.Fatal(err)
	}
	_, err = service.ListAttention(context.Background(), AttentionInput{
		ProjectID: projectID, Address: "codex:amy", Agent: "amy", WorkerAdapter: AdapterCodex,
		Authority: func(ctx context.Context, tx *sql.Tx) (bool, error) {
			if _, err := tx.ExecContext(ctx, `UPDATE instance_orchestrator SET project_agent_id=?,display_label='Bob',revision=revision+1,
				updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE singleton_id=1`, bobID); err != nil {
				return false, err
			}
			return true, nil
		},
	})
	var codedErr *CodedError
	if !errors.As(err, &codedErr) || codedErr.Code != "agent_attention_receiver_changed" {
		t.Fatalf("error=%v", err)
	}
	for label, query := range map[string]string{
		"items":   `SELECT COUNT(*) FROM agent_attention_items`,
		"batches": `SELECT COUNT(*) FROM agent_attention_batches`,
	} {
		var count int
		if err := paimosdb.DB.QueryRow(query).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s count=%d err=%v", label, count, err)
		}
	}
	var name string
	if err := paimosdb.DB.QueryRow(`SELECT pa.name FROM instance_orchestrator io JOIN project_agents pa ON pa.id=io.project_agent_id`).Scan(&name); err != nil || name != "amy" {
		t.Fatalf("orchestrator=%q err=%v", name, err)
	}

	if inserted, err := service.ProjectAttention(context.Background()); err != nil || inserted != 1 {
		t.Fatalf("trusted projection inserted=%d err=%v", inserted, err)
	}
	authorityCalls := 0
	_, err = service.ListAttention(context.Background(), AttentionInput{
		ProjectID: projectID, Address: "codex:amy", Agent: "amy", WorkerAdapter: AdapterCodex,
		Authority: func(ctx context.Context, tx *sql.Tx) (bool, error) {
			authorityCalls++
			if authorityCalls == 1 {
				if _, err := tx.ExecContext(ctx, `UPDATE instance_orchestrator SET project_agent_id=?,display_label='Bob',revision=revision+1,
					updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE singleton_id=1`, bobID); err != nil {
					return false, err
				}
			}
			return true, nil
		},
	})
	if !errors.As(err, &codedErr) || codedErr.Code != "agent_attention_receiver_changed" {
		t.Fatalf("lease reassignment error=%v calls=%d", err, authorityCalls)
	}
	var batchCount int
	if err := paimosdb.DB.QueryRow(`SELECT COUNT(*) FROM agent_attention_batches`).Scan(&batchCount); err != nil || batchCount != 0 {
		t.Fatalf("batch count=%d err=%v", batchCount, err)
	}

	page, err := service.ListAttention(context.Background(), AttentionInput{
		ProjectID: projectID, Address: "codex:amy", Agent: "amy", WorkerAdapter: AdapterCodex,
	})
	if err != nil || page.Work == nil || page.Work.State != "leased" {
		t.Fatalf("trusted lease page=%#v err=%v", page, err)
	}
	_, err = service.AckAttention(context.Background(), AttentionAckInput{
		ProjectID: projectID, Address: "codex:amy", Agent: "amy", Cursor: page.NextCursor, BatchID: page.Work.BatchID,
		Authority: func(ctx context.Context, tx *sql.Tx) (bool, error) {
			if _, err := tx.ExecContext(ctx, `UPDATE instance_orchestrator SET project_agent_id=?,display_label='Bob',revision=revision+1,
				updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE singleton_id=1`, bobID); err != nil {
				return false, err
			}
			return true, nil
		},
	})
	if !errors.As(err, &codedErr) || codedErr.Code != "agent_attention_receiver_changed" {
		t.Fatalf("ack reassignment error=%v", err)
	}
	var state string
	if err := paimosdb.DB.QueryRow(`SELECT state FROM agent_attention_batches WHERE batch_id=?`, page.Work.BatchID).Scan(&state); err != nil || state != "leased" {
		t.Fatalf("batch state=%q err=%v", state, err)
	}
}

func TestAttentionProjectsRealHeldActionWithNormalizedTimestampOnce(t *testing.T) {
	service, projectID := openBusTestDB(t)
	configureAttentionReceiver(t, service, projectID)
	allowBusSender(t, service, projectID, "codex:amy")
	message, err := service.SendEnvelope(context.Background(), SendEnvelopeInput{
		ProjectID: projectID, Sender: "sender", To: "codex:amy", Body: "Please decide.", ActionRequest: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if inserted, err := service.ProjectAttention(context.Background()); err != nil || inserted != 1 {
		t.Fatalf("real action projection inserted=%d err=%v", inserted, err)
	}
	var sourceID, occurredAt string
	if err := paimosdb.DB.QueryRow(`SELECT source_id,occurred_at FROM agent_attention_items WHERE source_kind='held_agent_message'`).Scan(&sourceID, &occurredAt); err != nil {
		t.Fatal(err)
	}
	if sourceID != message.MessageID || len(occurredAt) != len("2026-09-03T01:02:03.000Z") || !strings.HasSuffix(occurredAt, "Z") {
		t.Fatalf("source=%q occurred_at=%q", sourceID, occurredAt)
	}
	if replay, err := service.ProjectAttention(context.Background()); err != nil || replay != 0 {
		t.Fatalf("replay inserted=%d err=%v", replay, err)
	}
}

func TestAttentionTransitionIdentityDeduplicatesRepeatedEventRows(t *testing.T) {
	service, projectID := openBusTestDB(t)
	configureAttentionReceiver(t, service, projectID)
	_, sessionID := addAttentionWorker(t, projectID)
	for eventSequence := 1; eventSequence <= 2; eventSequence++ {
		if _, err := paimosdb.DB.Exec(`INSERT INTO harness_session_events(harness_session_id,event_sequence,operation,phase,
			activity_state,activity_reason,activity_event_kind,activity_sequence)
			VALUES(?,?,'activity_timeout','working','unknown','heartbeat_stale','',7)`, sessionID, eventSequence); err != nil {
			t.Fatal(err)
		}
	}
	if inserted, err := service.ProjectAttention(context.Background()); err != nil || inserted != 1 {
		t.Fatalf("transition projection inserted=%d err=%v", inserted, err)
	}
	var count int
	if err := paimosdb.DB.QueryRow(`SELECT COUNT(*) FROM agent_attention_items WHERE source_id=?`, sessionID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("transition rows=%d err=%v", count, err)
	}
}

func TestAttentionProjectionExcludesCoordinatorAndReceiverSessions(t *testing.T) {
	service, projectID := openBusTestDB(t)
	configureAttentionReceiver(t, service, projectID)
	var receiverID int64
	if err := paimosdb.DB.QueryRow(`SELECT id FROM project_agents WHERE project_id=? AND name='amy'`, projectID).Scan(&receiverID); err != nil {
		t.Fatal(err)
	}
	coordinator, err := paimosdb.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'coordinator')`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	coordinatorID, _ := coordinator.LastInsertId()
	fixtures := []struct {
		id, name, harness, role string
		agentID                 int64
	}{
		{"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "amy", "codex", "worker", receiverID},
		{"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", "coordinator", "claude", "coordinator", coordinatorID},
	}
	for _, fixture := range fixtures {
		if _, err := paimosdb.DB.Exec(`INSERT INTO harness_sessions(id,project_id,project_agent_id,agent_name,harness,host,
			session_ref_digest,worker_lease_digest,management_mode,role,steer_mode,advertised_inbox,advertised_status,
			advertised_steer,advertised_interrupt,advertised_stop,phase)
			VALUES(?,?,?,?,?,'attention-host',zeroblob(32),zeroblob(32),'managed',?,'none',0,1,0,1,1,'working')`,
			fixture.id, projectID, fixture.agentID, fixture.name, fixture.harness, fixture.role); err != nil {
			t.Fatal(err)
		}
		if _, err := paimosdb.DB.Exec(`INSERT INTO harness_session_events(harness_session_id,event_sequence,operation,phase,
			activity_state,activity_reason,activity_event_kind,activity_sequence)
			VALUES(?,1,'activity_timeout','working','unknown','heartbeat_stale','',1)`, fixture.id); err != nil {
			t.Fatal(err)
		}
	}
	if inserted, err := service.ProjectAttention(context.Background()); err != nil || inserted != 0 {
		t.Fatalf("excluded projection inserted=%d err=%v", inserted, err)
	}
}

func TestAttentionSourceIdentitySurvivesReceiverAddressChange(t *testing.T) {
	service, projectID := openBusTestDB(t)
	configureAttentionReceiver(t, service, projectID)
	_, sessionID := addAttentionWorker(t, projectID)
	if _, err := paimosdb.DB.Exec(`INSERT INTO harness_session_events(harness_session_id,event_sequence,operation,phase,
		activity_state,activity_reason,activity_event_kind,activity_sequence)
		VALUES(?,1,'activity_timeout','working','unknown','heartbeat_stale','',1)`, sessionID); err != nil {
		t.Fatal(err)
	}
	if inserted, err := service.ProjectAttention(context.Background()); err != nil || inserted != 1 {
		t.Fatalf("initial inserted=%d err=%v", inserted, err)
	}
	if _, err := paimosdb.DB.Exec(`UPDATE agent_message_targets SET enabled=0 WHERE address='codex:amy'`); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RegisterTarget(context.Background(), RegisterTargetInput{
		ProjectID: projectID, Address: "claude:amy", Adapter: AdapterClaudeResume, TargetKind: TargetKindClaudeSession,
		TargetRef: "11111111-1111-4111-8111-111111111111", MaximumLevel: "simple", Role: "primary",
	}); err != nil {
		t.Fatal(err)
	}
	if replay, err := service.ProjectAttention(context.Background()); err != nil || replay != 0 {
		t.Fatalf("address-change replay=%d err=%v", replay, err)
	}
	page, err := service.ListAttention(context.Background(), AttentionInput{ProjectID: projectID, Address: "claude:amy", Agent: "amy"})
	if err != nil || len(page.Items) != 1 || page.Items[0].SourceID != sessionID {
		t.Fatalf("address-change page=%#v err=%v", page, err)
	}
	var count int
	if err := paimosdb.DB.QueryRow(`SELECT COUNT(*) FROM agent_attention_items WHERE source_id=?`, sessionID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("source rows=%d err=%v", count, err)
	}
}

func TestAttentionLeaseBoundsAndBatchlessAckCloseReachedBatch(t *testing.T) {
	if attentionLeaseDuration(AdapterCodex) != 2*time.Minute || attentionLeaseDuration(AdapterClaudeResume) != 15*time.Minute {
		t.Fatalf("lease bounds codex=%s claude=%s", attentionLeaseDuration(AdapterCodex), attentionLeaseDuration(AdapterClaudeResume))
	}
	service, projectID := openBusTestDB(t)
	configureAttentionReceiver(t, service, projectID)
	var agentID int64
	if err := paimosdb.DB.QueryRow(`SELECT id FROM project_agents WHERE project_id=? AND name='amy'`, projectID).Scan(&agentID); err != nil {
		t.Fatal(err)
	}
	if _, err := paimosdb.DB.Exec(`INSERT INTO agent_attention_items(receiver_project_id,receiver_project_agent_id,address,
		source_project_id,source_kind,source_id,source_sequence,attention_kind,reason_code,occurred_at)
		VALUES(?,?,'codex:amy',?,'harness_session_event','ack-worker',1,'worker_unknown','heartbeat_stale',
		strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, projectID, agentID, projectID); err != nil {
		t.Fatal(err)
	}
	page, err := service.ListAttention(context.Background(), AttentionInput{ProjectID: projectID, Address: "codex:amy", Agent: "amy", WorkerAdapter: AdapterCodex})
	if err != nil || page.Work == nil || page.Work.State != "leased" {
		t.Fatalf("page=%#v err=%v", page, err)
	}
	if _, err := service.AckAttention(context.Background(), AttentionAckInput{ProjectID: projectID, Address: "codex:amy", Agent: "amy", Cursor: page.NextCursor}); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := paimosdb.DB.QueryRow(`SELECT state FROM agent_attention_batches WHERE batch_id=?`, page.Work.BatchID).Scan(&state); err != nil || state != "handed_off" {
		t.Fatalf("batch state=%q err=%v", state, err)
	}
}

func TestRoutineActivitySoakProducesNoAttentionHandoff(t *testing.T) {
	service, projectID := openBusTestDB(t)
	configureAttentionReceiver(t, service, projectID)
	_, sessionID := addAttentionWorker(t, projectID)
	for sequence := 1; sequence <= 64; sequence++ {
		kind := "tool_started"
		if sequence%2 == 0 {
			kind = "control_applied"
		}
		if _, err := paimosdb.DB.Exec(`INSERT INTO harness_session_events(harness_session_id,event_sequence,operation,phase,
			activity_state,activity_reason,activity_event_kind,activity_sequence)
			VALUES(?,?,'heartbeat','working','busy','adapter_activity',?,?)`, sessionID, sequence, kind, sequence); err != nil {
			t.Fatal(err)
		}
	}
	for attempt := 0; attempt < 4; attempt++ {
		page, err := service.ListAttention(context.Background(), AttentionInput{
			ProjectID: projectID, Address: "codex:amy", Agent: "amy", WorkerAdapter: AdapterCodex,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 0 || page.Work != nil || page.Frame != "" {
			t.Fatalf("routine activity produced handoff on attempt %d: %#v", attempt, page)
		}
	}
	var itemCount, batchCount int
	if err := paimosdb.DB.QueryRow(`SELECT COUNT(*) FROM agent_attention_items`).Scan(&itemCount); err != nil {
		t.Fatal(err)
	}
	if err := paimosdb.DB.QueryRow(`SELECT COUNT(*) FROM agent_attention_batches`).Scan(&batchCount); err != nil {
		t.Fatal(err)
	}
	if itemCount != 0 || batchCount != 0 {
		t.Fatalf("routine activity persisted items=%d batches=%d", itemCount, batchCount)
	}
}

func TestAttentionProjectionCoalescedLeaseAndCrashSafeAck(t *testing.T) {
	service, projectID := openBusTestDB(t)
	actorID := configureAttentionReceiver(t, service, projectID)
	_, sessionID := addAttentionWorker(t, projectID)

	// Routine busy evidence must never create model work.
	if _, err := paimosdb.DB.Exec(`INSERT INTO harness_session_events(harness_session_id,event_sequence,operation,phase,
		activity_state,activity_reason,activity_event_kind,activity_sequence)
		VALUES(?,1,'heartbeat','working','busy','adapter_activity','tool_started',1)`, sessionID); err != nil {
		t.Fatal(err)
	}
	if inserted, err := service.ProjectAttention(context.Background()); err != nil || inserted != 0 {
		t.Fatalf("busy projection inserted=%d err=%v", inserted, err)
	}

	if _, err := paimosdb.DB.Exec(`INSERT INTO harness_session_events(harness_session_id,event_sequence,operation,phase,
		activity_state,activity_reason,activity_event_kind,activity_sequence)
		VALUES(?,2,'activity_timeout','working','unknown','heartbeat_stale','',1)`, sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := paimosdb.DB.Exec(`UPDATE harness_sessions SET phase='stopped',activity_state='dead',activity_reason='process_failed',
		activity_event_kind='',closed_reason='process_failed',revision=revision+1 WHERE id=?`, sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := paimosdb.DB.Exec(`INSERT INTO harness_session_events(harness_session_id,event_sequence,operation,phase,
		activity_state,activity_reason,activity_event_kind,activity_sequence,closed_reason)
		VALUES(?,3,'stop','stopped','dead','process_failed','',1,'process_failed')`, sessionID); err != nil {
		t.Fatal(err)
	}
	controlID := "33333333-3333-4333-8333-333333333333"
	if _, err := paimosdb.DB.Exec(`INSERT INTO harness_session_controls(id,harness_session_id,sequence,kind,state,reason,
		requested_by_user_id,claimed_at,completed_at) VALUES(?, ?,1,'interrupt','rejected','failed',?,
		strftime('%Y-%m-%dT%H:%M:%fZ','now'),strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, controlID, sessionID, actorID); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	results := make(chan int64, 2)
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := service.ProjectAttention(context.Background())
			results <- n
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	var total int64
	for n := range results {
		total += n
	}
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if total != 3 {
		t.Fatalf("concurrent projection inserted=%d want=3", total)
	}
	if replay, err := service.ProjectAttention(context.Background()); err != nil || replay != 0 {
		t.Fatalf("replay inserted=%d err=%v", replay, err)
	}

	page, err := service.ListAttention(context.Background(), AttentionInput{
		ProjectID: projectID, Address: "codex:amy", Agent: "amy", WorkerAdapter: AdapterCodex, AfterID: 999,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 3 || page.Work == nil || page.Work.State != "leased" || page.Work.TargetRef == "" {
		t.Fatalf("attention page=%#v", page)
	}
	if page.Work.MaximumLevel != "simple" || !strings.Contains(page.Frame, `delivery_level="simple"`) {
		t.Fatalf("work=%#v frame=%q", page.Work, page.Frame)
	}
	raw, err := json.Marshal(page.Items)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"requested_by", "diagnostic", "body", "parts", "secret"} {
		if strings.Contains(strings.ToLower(string(raw)), forbidden) {
			t.Fatalf("attention leaked %q: %s", forbidden, raw)
		}
	}

	// A live lease returns the same durable batch without re-disclosing the
	// target. Expiry permits the same idempotency key to be leased again.
	retry, err := service.ListAttention(context.Background(), AttentionInput{
		ProjectID: projectID, Address: "codex:amy", Agent: "amy", WorkerAdapter: AdapterCodex,
	})
	if err != nil {
		t.Fatal(err)
	}
	if retry.Work != nil || len(retry.Items) != 0 || retry.NextCursor != 0 {
		t.Fatalf("live retry=%#v", retry)
	}
	if _, err := paimosdb.DB.Exec(`UPDATE agent_attention_batches SET lease_until='2000-01-01T00:00:00.000Z' WHERE batch_id=?`, page.Work.BatchID); err != nil {
		t.Fatal(err)
	}
	recovered, err := service.ListAttention(context.Background(), AttentionInput{
		ProjectID: projectID, Address: "codex:amy", Agent: "amy", WorkerAdapter: AdapterCodex,
	})
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Work == nil || recovered.Work.BatchID != page.Work.BatchID || recovered.Work.TargetRef == "" {
		t.Fatalf("recovered=%#v", recovered)
	}
	state, err := service.AckAttention(context.Background(), AttentionAckInput{
		ProjectID: projectID, Address: "codex:amy", Agent: "amy", Cursor: page.NextCursor, BatchID: page.Work.BatchID,
	})
	if err != nil || state.Cursor != page.NextCursor {
		t.Fatalf("ack=%#v err=%v", state, err)
	}
	replayedAck, err := service.AckAttention(context.Background(), AttentionAckInput{
		ProjectID: projectID, Address: "codex:amy", Agent: "amy", Cursor: page.NextCursor, BatchID: page.Work.BatchID,
	})
	if err != nil || replayedAck.Cursor != page.NextCursor {
		t.Fatalf("replay ack=%#v err=%v", replayedAck, err)
	}
	empty, err := service.ListAttention(context.Background(), AttentionInput{ProjectID: projectID, Address: "codex:amy", Agent: "amy"})
	if err != nil || len(empty.Items) != 0 {
		t.Fatalf("after ack page=%#v err=%v", empty, err)
	}
}

func TestAttentionMissingCapabilityStaysBlockedAndUnacknowledged(t *testing.T) {
	service, projectID := openBusTestDB(t)
	configureAttentionReceiver(t, service, projectID)
	// Remove the selected target while preserving the receiver/address snapshot.
	if _, err := paimosdb.DB.Exec(`UPDATE agent_message_targets SET enabled=0 WHERE address='codex:amy'`); err != nil {
		t.Fatal(err)
	}
	// The address captured by the receiver becomes the closed paimos fallback.
	if _, err := paimosdb.DB.Exec(`INSERT INTO agent_attention_items(receiver_project_id,receiver_project_agent_id,address,
		source_project_id,source_kind,source_id,source_sequence,attention_kind,reason_code,occurred_at)
		SELECT ?,id,'paimos:amy',?,'held_agent_message','blocked-fixture',0,'held_action','action_request_held',
		strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM project_agents WHERE project_id=? AND name='amy'`, projectID, projectID, projectID); err != nil {
		t.Fatal(err)
	}
	page, err := service.ListAttention(context.Background(), AttentionInput{
		ProjectID: projectID, Address: "paimos:amy", Agent: "amy", WorkerAdapter: AdapterCodex,
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.Work == nil || page.Work.State != "blocked" || page.Work.BlockedReason != "target_missing" || page.Work.TargetRef != "" {
		t.Fatalf("blocked page=%#v", page)
	}
	if _, err := service.AckAttention(context.Background(), AttentionAckInput{
		ProjectID: projectID, Address: "paimos:amy", Agent: "amy", Cursor: page.NextCursor, BatchID: page.Work.BatchID,
	}); err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("blocked ack err=%v", err)
	}
	if _, err := service.RegisterTarget(context.Background(), RegisterTargetInput{
		ProjectID: projectID, Address: "paimos:amy", Adapter: AdapterCodex, TargetKind: TargetKindCodexThread,
		TargetRef: "01a059fb-4bf4-4881-a38a-7a2e8e60af31", MaximumLevel: "simple", Role: "primary",
	}); err != nil {
		t.Fatal(err)
	}
	if requeued, err := service.RequeueMissingTargets(context.Background(), projectID, "paimos:amy"); err != nil || requeued != 1 {
		t.Fatalf("requeued=%d err=%v", requeued, err)
	}
	recovered, err := service.ListAttention(context.Background(), AttentionInput{
		ProjectID: projectID, Address: "paimos:amy", Agent: "amy", WorkerAdapter: AdapterCodex,
	})
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Work == nil || recovered.Work.BatchID != page.Work.BatchID || recovered.Work.State != "leased" || recovered.Work.TargetRef == "" {
		t.Fatalf("recovered blocked page=%#v", recovered)
	}
}

func TestAttentionSteerOnlyTargetBlocksUntilSimpleFallbackIsRegistered(t *testing.T) {
	service, projectID := openBusTestDB(t)
	configureAttentionReceiver(t, service, projectID)
	if _, err := service.RegisterTarget(context.Background(), RegisterTargetInput{
		ProjectID: projectID, Address: "codex:amy", Adapter: AdapterAgentdCodex, TargetKind: TargetKindAgentdSession,
		TargetRef:    `{"socket":"/tmp/paimos-agentd.sock","session_id":"44444444-4444-4444-8444-444444444444"}`,
		MaximumLevel: "steer", Role: "primary",
	}); err != nil {
		t.Fatal(err)
	}
	_, sessionID := addAttentionWorker(t, projectID)
	if _, err := paimosdb.DB.Exec(`INSERT INTO harness_session_events(harness_session_id,event_sequence,operation,phase,
		activity_state,activity_reason,activity_event_kind,activity_sequence)
		VALUES(?,1,'activity_timeout','working','unknown','heartbeat_stale','tool_started',1)`, sessionID); err != nil {
		t.Fatal(err)
	}
	if inserted, err := service.ProjectAttention(context.Background()); err != nil || inserted != 1 {
		t.Fatalf("managed-only receiver projection=%d err=%v", inserted, err)
	}
	blocked, err := service.ListAttention(context.Background(), AttentionInput{
		ProjectID: projectID, Address: "codex:amy", Agent: "amy", WorkerAdapter: AdapterCodex,
	})
	if err != nil {
		t.Fatal(err)
	}
	if blocked.Work == nil || blocked.Work.State != "blocked" || blocked.Work.BlockedReason != "capability_missing" || blocked.Work.TargetRef != "" {
		t.Fatalf("steer-only target page=%#v", blocked)
	}
	if _, err := service.RegisterTarget(context.Background(), RegisterTargetInput{
		ProjectID: projectID, Address: "codex:amy", Adapter: AdapterCodex, TargetKind: TargetKindCodexThread,
		TargetRef: "01a059fb-4bf4-4881-a38a-7a2e8e60af32", MaximumLevel: "simple", Role: "simple_fallback",
	}); err != nil {
		t.Fatal(err)
	}
	if requeued, err := service.RequeueMissingTargets(context.Background(), projectID, "codex:amy"); err != nil || requeued != 1 {
		t.Fatalf("capability requeue=%d err=%v", requeued, err)
	}
	recovered, err := service.ListAttention(context.Background(), AttentionInput{
		ProjectID: projectID, Address: "codex:amy", Agent: "amy", WorkerAdapter: AdapterCodex,
	})
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Work == nil || recovered.Work.BatchID != blocked.Work.BatchID || recovered.Work.State != "leased" || recovered.Work.Adapter != AdapterCodex {
		t.Fatalf("simple fallback page=%#v", recovered)
	}
}

func TestAttentionExplicitRequeueRetargetsPendingBatchAfterSameAddressRotation(t *testing.T) {
	service, projectID := openBusTestDB(t)
	configureAttentionReceiver(t, service, projectID)
	var agentID int64
	if err := paimosdb.DB.QueryRow(`SELECT id FROM project_agents WHERE project_id=? AND name='amy'`, projectID).Scan(&agentID); err != nil {
		t.Fatal(err)
	}
	if _, err := paimosdb.DB.Exec(`INSERT INTO agent_attention_items(receiver_project_id,receiver_project_agent_id,address,
		source_project_id,source_kind,source_id,source_sequence,attention_kind,reason_code,occurred_at)
		VALUES(?,?,'codex:amy',?,'harness_session_event','rotation-pending',1,'worker_unknown','heartbeat_stale',
		strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, projectID, agentID, projectID); err != nil {
		t.Fatal(err)
	}
	pending, err := service.ListAttention(context.Background(), AttentionInput{
		ProjectID: projectID, Address: "codex:amy", Agent: "amy", WorkerAdapter: AdapterClaudeResume,
	})
	if err != nil || pending.Work == nil || pending.Work.State != "pending" {
		t.Fatalf("pending=%#v err=%v", pending, err)
	}
	var oldTargetID string
	if err := paimosdb.DB.QueryRow(`SELECT target_id FROM agent_attention_batches WHERE batch_id=?`, pending.Work.BatchID).Scan(&oldTargetID); err != nil {
		t.Fatal(err)
	}
	newTarget, err := service.RegisterTarget(context.Background(), RegisterTargetInput{
		ProjectID: projectID, Address: "codex:amy", Adapter: AdapterCodex, TargetKind: TargetKindCodexThread,
		TargetRef: "01a059fb-4bf4-4881-a38a-7a2e8e60af41", MaximumLevel: "simple", Role: "primary",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ListAttention(context.Background(), AttentionInput{
		ProjectID: projectID, Address: "codex:amy", Agent: "amy", WorkerAdapter: AdapterCodex,
	}); err == nil || !strings.Contains(err.Error(), "authorized operator must requeue") {
		t.Fatalf("stale target listen err=%v", err)
	}
	if requeued, err := service.RequeueMissingTargets(context.Background(), projectID, "codex:amy"); err != nil || requeued != 1 {
		t.Fatalf("rotation requeue=%d err=%v", requeued, err)
	}
	var batchID, targetID, state string
	if err := paimosdb.DB.QueryRow(`SELECT batch_id,target_id,state FROM agent_attention_batches WHERE batch_id=?`, pending.Work.BatchID).Scan(&batchID, &targetID, &state); err != nil {
		t.Fatal(err)
	}
	if batchID != pending.Work.BatchID || targetID != newTarget.ID || targetID == oldTargetID || state != "pending" {
		t.Fatalf("batch=%s target=%s old=%s new=%s state=%s", batchID, targetID, oldTargetID, newTarget.ID, state)
	}
	recovered, err := service.ListAttention(context.Background(), AttentionInput{
		ProjectID: projectID, Address: "codex:amy", Agent: "amy", WorkerAdapter: AdapterCodex,
	})
	if err != nil || recovered.Work == nil || recovered.Work.BatchID != batchID || recovered.Work.State != "leased" || recovered.Work.TargetRef == "" {
		t.Fatalf("recovered=%#v err=%v", recovered, err)
	}
}

func TestAttentionExplicitRequeueNeverRetargetsLiveLeaseAndRecoversExpiredTransport(t *testing.T) {
	service, projectID := openBusTestDB(t)
	configureAttentionReceiver(t, service, projectID)
	var agentID int64
	if err := paimosdb.DB.QueryRow(`SELECT id FROM project_agents WHERE project_id=? AND name='amy'`, projectID).Scan(&agentID); err != nil {
		t.Fatal(err)
	}
	if _, err := paimosdb.DB.Exec(`INSERT INTO agent_attention_items(receiver_project_id,receiver_project_agent_id,address,
		source_project_id,source_kind,source_id,source_sequence,attention_kind,reason_code,occurred_at)
		VALUES(?,?,'codex:amy',?,'harness_session_event','transport-dead',1,'worker_dead','process_failed',
		strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, projectID, agentID, projectID); err != nil {
		t.Fatal(err)
	}
	leased, err := service.ListAttention(context.Background(), AttentionInput{
		ProjectID: projectID, Address: "codex:amy", Agent: "amy", WorkerAdapter: AdapterCodex,
	})
	if err != nil || leased.Work == nil || leased.Work.State != "leased" {
		t.Fatalf("leased=%#v err=%v", leased, err)
	}
	var oldTargetID string
	if err := paimosdb.DB.QueryRow(`SELECT target_id FROM agent_attention_batches WHERE batch_id=?`, leased.Work.BatchID).Scan(&oldTargetID); err != nil {
		t.Fatal(err)
	}
	newTarget, err := service.RegisterTarget(context.Background(), RegisterTargetInput{
		ProjectID: projectID, Address: "codex:amy", Adapter: AdapterCodex, TargetKind: TargetKindCodexThread,
		TargetRef: "01a059fb-4bf4-4881-a38a-7a2e8e60af42", MaximumLevel: "simple", Role: "primary",
	})
	if err != nil {
		t.Fatal(err)
	}
	if requeued, err := service.RequeueMissingTargets(context.Background(), projectID, "codex:amy"); err != nil || requeued != 0 {
		t.Fatalf("live lease requeue=%d err=%v", requeued, err)
	}
	var targetID, state string
	if err := paimosdb.DB.QueryRow(`SELECT target_id,state FROM agent_attention_batches WHERE batch_id=?`, leased.Work.BatchID).Scan(&targetID, &state); err != nil {
		t.Fatal(err)
	}
	if targetID != oldTargetID || state != "leased" {
		t.Fatalf("live lease target=%s old=%s state=%s", targetID, oldTargetID, state)
	}
	if _, err := paimosdb.DB.Exec(`UPDATE agent_attention_batches SET lease_until='2000-01-01T00:00:00.000Z' WHERE batch_id=?`, leased.Work.BatchID); err != nil {
		t.Fatal(err)
	}
	if requeued, err := service.RequeueMissingTargets(context.Background(), projectID, "codex:amy"); err != nil || requeued != 1 {
		t.Fatalf("expired transport requeue=%d err=%v", requeued, err)
	}
	if err := paimosdb.DB.QueryRow(`SELECT target_id,state FROM agent_attention_batches WHERE batch_id=?`, leased.Work.BatchID).Scan(&targetID, &state); err != nil {
		t.Fatal(err)
	}
	if targetID != newTarget.ID || state != "pending" {
		t.Fatalf("expired recovery target=%s new=%s state=%s", targetID, newTarget.ID, state)
	}
	recovered, err := service.ListAttention(context.Background(), AttentionInput{
		ProjectID: projectID, Address: "codex:amy", Agent: "amy", WorkerAdapter: AdapterCodex,
	})
	if err != nil || recovered.Work == nil || recovered.Work.BatchID != leased.Work.BatchID || recovered.Work.State != "leased" {
		t.Fatalf("recovered=%#v err=%v", recovered, err)
	}
}

func TestAttentionExplicitRequeueRecoversExpiredTransportWithoutTargetRotation(t *testing.T) {
	service, projectID := openBusTestDB(t)
	configureAttentionReceiver(t, service, projectID)
	var agentID int64
	if err := paimosdb.DB.QueryRow(`SELECT id FROM project_agents WHERE project_id=? AND name='amy'`, projectID).Scan(&agentID); err != nil {
		t.Fatal(err)
	}
	if _, err := paimosdb.DB.Exec(`INSERT INTO agent_attention_items(receiver_project_id,receiver_project_agent_id,address,
		source_project_id,source_kind,source_id,source_sequence,attention_kind,reason_code,occurred_at)
		VALUES(?,?,'codex:amy',?,'harness_session_event','transport-retry',1,'worker_unknown','heartbeat_stale',
		strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, projectID, agentID, projectID); err != nil {
		t.Fatal(err)
	}
	leased, err := service.ListAttention(context.Background(), AttentionInput{
		ProjectID: projectID, Address: "codex:amy", Agent: "amy", WorkerAdapter: AdapterCodex,
	})
	if err != nil || leased.Work == nil || leased.Work.State != "leased" {
		t.Fatalf("leased=%#v err=%v", leased, err)
	}
	var targetBefore string
	if err := paimosdb.DB.QueryRow(`SELECT target_id FROM agent_attention_batches WHERE batch_id=?`, leased.Work.BatchID).Scan(&targetBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := paimosdb.DB.Exec(`UPDATE agent_attention_batches SET lease_until='2000-01-01T00:00:00.000Z' WHERE batch_id=?`, leased.Work.BatchID); err != nil {
		t.Fatal(err)
	}
	if requeued, err := service.RequeueMissingTargets(context.Background(), projectID, "codex:amy"); err != nil || requeued != 1 {
		t.Fatalf("transport retry requeue=%d err=%v", requeued, err)
	}
	var batchID, targetAfter, state string
	if err := paimosdb.DB.QueryRow(`SELECT batch_id,target_id,state FROM agent_attention_batches WHERE batch_id=?`, leased.Work.BatchID).Scan(&batchID, &targetAfter, &state); err != nil {
		t.Fatal(err)
	}
	if batchID != leased.Work.BatchID || targetAfter != targetBefore || state != "pending" {
		t.Fatalf("batch=%s target=%s want_target=%s state=%s", batchID, targetAfter, targetBefore, state)
	}
	recovered, err := service.ListAttention(context.Background(), AttentionInput{
		ProjectID: projectID, Address: "codex:amy", Agent: "amy", WorkerAdapter: AdapterCodex,
	})
	if err != nil || recovered.Work == nil || recovered.Work.BatchID != leased.Work.BatchID || recovered.Work.State != "leased" {
		t.Fatalf("recovered=%#v err=%v", recovered, err)
	}
}

func TestAttentionCoalescingWindowAndPageBoundAreClosed(t *testing.T) {
	service, projectID := openBusTestDB(t)
	configureAttentionReceiver(t, service, projectID)
	var agentID int64
	if err := paimosdb.DB.QueryRow(`SELECT id FROM project_agents WHERE project_id=? AND name='amy'`, projectID).Scan(&agentID); err != nil {
		t.Fatal(err)
	}
	for i, at := range []string{"2026-09-03T01:00:00.000Z", "2026-09-03T01:00:01.999Z", "2026-09-03T01:00:02.001Z"} {
		if _, err := paimosdb.DB.Exec(`INSERT INTO agent_attention_items(receiver_project_id,receiver_project_agent_id,address,
			source_project_id,source_kind,source_id,source_sequence,attention_kind,reason_code,occurred_at,created_at)
			VALUES(?,?, 'codex:amy',?,'harness_session_event',?,?,'worker_unknown','heartbeat_stale',?,?)`,
			projectID, agentID, projectID, "window-"+string(rune('a'+i)), i+1, at, at); err != nil {
			t.Fatal(err)
		}
	}
	page, err := service.ListAttention(context.Background(), AttentionInput{ProjectID: projectID, Address: "codex:amy", Agent: "amy", Limit: 99})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Items[1].SourceID != "window-b" {
		t.Fatalf("first window=%#v", page.Items)
	}
	if _, err := service.AckAttention(context.Background(), AttentionAckInput{ProjectID: projectID, Address: "codex:amy", Agent: "amy", Cursor: page.NextCursor}); err != nil {
		t.Fatal(err)
	}
	next, err := service.ListAttention(context.Background(), AttentionInput{ProjectID: projectID, Address: "codex:amy", Agent: "amy", Limit: 99})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Items) != 1 || next.Items[0].SourceID != "window-c" {
		t.Fatalf("second window=%#v", next.Items)
	}
}

func TestConcurrentAttentionListenersLeaseOneBatch(t *testing.T) {
	service, projectID := openBusTestDB(t)
	configureAttentionReceiver(t, service, projectID)
	var agentID int64
	if err := paimosdb.DB.QueryRow(`SELECT id FROM project_agents WHERE project_id=? AND name='amy'`, projectID).Scan(&agentID); err != nil {
		t.Fatal(err)
	}
	if _, err := paimosdb.DB.Exec(`INSERT INTO agent_attention_items(receiver_project_id,receiver_project_agent_id,address,
		source_project_id,source_kind,source_id,source_sequence,attention_kind,reason_code,occurred_at)
		VALUES(?,?, 'codex:amy',?,'harness_session_event','concurrent-worker',1,'worker_unknown','heartbeat_stale',
		strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, projectID, agentID, projectID); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	pages := make(chan *AttentionPage, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			page, err := service.ListAttention(context.Background(), AttentionInput{
				ProjectID: projectID, Address: "codex:amy", Agent: "amy", WorkerAdapter: AdapterCodex,
			})
			pages <- page
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(pages)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	leases := 0
	batchID := ""
	for page := range pages {
		if page.Work != nil && page.Work.TargetRef != "" {
			leases++
			batchID = page.Work.BatchID
		}
	}
	if leases != 1 {
		t.Fatalf("deliverable leases=%d want=1", leases)
	}
	var batches int
	if err := paimosdb.DB.QueryRow(`SELECT COUNT(*) FROM agent_attention_batches WHERE batch_id=?`, batchID).Scan(&batches); err != nil || batches != 1 {
		t.Fatalf("batches=%d err=%v", batches, err)
	}
}

func TestAttentionRejectsUnattributedProjectionSideEffects(t *testing.T) {
	service, projectID := openBusTestDB(t)
	configureAttentionReceiver(t, service, projectID)
	_, sessionID := addAttentionWorker(t, projectID)
	if _, err := paimosdb.DB.Exec(`INSERT INTO harness_session_events(harness_session_id,event_sequence,operation,phase,
		activity_state,activity_reason,activity_event_kind,activity_sequence)
		VALUES(?,1,'activity_timeout','working','unknown','heartbeat_stale','',1)`, sessionID); err != nil {
		t.Fatal(err)
	}

	_, err := service.ListAttention(context.Background(), AttentionInput{
		ProjectID: projectID, Address: "codex:amy", Agent: "different-agent",
	})
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("unattributed listen err=%v", err)
	}
	var projected int
	if err := paimosdb.DB.QueryRow(`SELECT COUNT(*) FROM agent_attention_items`).Scan(&projected); err != nil {
		t.Fatal(err)
	}
	if projected != 0 {
		t.Fatalf("unauthorized listen projected=%d attention items", projected)
	}
}
