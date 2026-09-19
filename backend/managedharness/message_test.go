package managedharness

import (
	"context"
	"database/sql"
	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/agentmessage"
	paimosdb "github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/models"
	"testing"
)

func TestNativeMessageSenderGenerationAndLedger(t *testing.T) {
	projectID, _ := openManagedHarnessTestDB(t)
	ctx := context.Background()
	session, _, err := NewService(paimosdb.DB).Register(ctx, RegisterInput{
		ProjectID: projectID, AgentName: "worker", Harness: "codex", Host: "test", SessionRef: "owned", WorkerLease: testWorkerLease,
		ManagementMode: ManagementManaged, Role: RoleWorker, SteerMode: SteerOwned, Capabilities: models.HarnessCapabilities{Inbox: true, Status: true, Steer: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := paimosdb.DB.Exec(`UPDATE harness_sessions SET phase='working',heartbeat_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, session.ID); err != nil {
		t.Fatal(err)
	}
	authority := func(ctx context.Context, tx *sql.Tx) (agentmessage.SenderBinding, error) {
		return MessageSenderTx(ctx, tx, projectID, session.ID, "worker", testWorkerLease)
	}
	bus := agentmessage.NewService(paimosdb.DB)
	input := agentmessage.SendEnvelopeInput{ProjectID: projectID, Sender: "forged", SessionID: "forged", To: "claude:sender", Body: "status observation", Metadata: map[string]any{"reply_address": "grok:forged"}, DeliveryLevel: "simple", IdempotencyKey: uuid.NewString(), SenderAuthority: authority}
	held, err := bus.SendEnvelope(ctx, input)
	if err != nil || held.Delivered || held.From != "paimos:worker" {
		t.Fatalf("held=%+v err=%v", held, err)
	}
	if err := bus.AllowSender(ctx, projectID, "claude:sender", "paimos:worker"); err != nil {
		t.Fatal(err)
	}
	input.IdempotencyKey = uuid.NewString()
	input.ExpectsReply = true
	first, err := bus.SendEnvelope(ctx, input)
	if err != nil || !first.Delivered || first.ReplyAddress != "codex:worker" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	replay, err := bus.SendEnvelope(ctx, input)
	if err != nil || replay.MessageID != first.MessageID {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	foreign, err := bus.SendEnvelope(ctx, agentmessage.SendEnvelopeInput{ProjectID: projectID, Sender: "sender", SessionID: session.ID, To: "codex:worker", Body: "unbound session observation"})
	if err != nil || foreign.ReplyAddress != "" {
		t.Fatalf("another agent borrowed an owned return route: %+v %v", foreign, err)
	}
	other, _, err := NewService(paimosdb.DB).Register(ctx, RegisterInput{ProjectID: projectID, AgentName: "worker", Harness: "claude", Host: "test", SessionRef: "other-owned", WorkerLease: testWorkerLease, ManagementMode: ManagementManaged, Role: RoleWorker, SteerMode: SteerOwned, Capabilities: models.HarnessCapabilities{Inbox: true, Status: true, Steer: true}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := paimosdb.DB.Exec(`UPDATE harness_sessions SET phase='working',heartbeat_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, other.ID); err != nil {
		t.Fatal(err)
	}
	secondInput := input
	secondInput.SenderAuthority = func(ctx context.Context, tx *sql.Tx) (agentmessage.SenderBinding, error) {
		return MessageSenderTx(ctx, tx, projectID, other.ID, "worker", testWorkerLease)
	}
	second, err := bus.SendEnvelope(ctx, secondInput)
	if err != nil || second.MessageID == first.MessageID || second.ReplyAddress != "claude:worker" {
		t.Fatalf("native call crossed generations: second=%+v err=%v", second, err)
	}
	secondInput.ProjectID = projectID + 1
	if _, err := bus.SendEnvelope(ctx, secondInput); err == nil {
		t.Fatal("authority overrode requested project")
	}
	input.Body = "changed"
	if _, err := bus.SendEnvelope(ctx, input); err == nil {
		t.Fatal("same call key accepted changed content")
	}
	var attribution string
	if err := paimosdb.DB.QueryRow(`SELECT session_id FROM agent_messages WHERE message_id=?`, first.MessageID).Scan(&attribution); err != nil {
		t.Fatal(err)
	}
	if attribution != session.ID {
		t.Fatal("sender public session not bound")
	}
	input.IdempotencyKey = uuid.NewString()
	input.ExpectsReply = false
	input.ActionRequest = true
	action, err := bus.SendEnvelope(ctx, input)
	if err != nil || action.Delivered || !action.IsActionRequest {
		t.Fatalf("action=%+v err=%v", action, err)
	}
	for _, change := range []string{"phase='stopping'", "phase='working',heartbeat_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','-3 minutes')"} {
		if _, err := paimosdb.DB.Exec(`UPDATE harness_sessions SET `+change+` WHERE id=?`, session.ID); err != nil {
			t.Fatal(err)
		}
		input.IdempotencyKey = uuid.NewString()
		if _, err := bus.SendEnvelope(ctx, input); err == nil {
			t.Fatalf("inactive sender accepted: %s", change)
		}
	}
	if _, err := NewService(paimosdb.DB).Stop(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	input.IdempotencyKey = uuid.NewString()
	if _, err := bus.SendEnvelope(ctx, input); err == nil {
		t.Fatal("stopped generation sent a message")
	}
}

func TestNativeMessageSenderCannotSwapLeaseOrScope(t *testing.T) {
	projectID, _ := openManagedHarnessTestDB(t)
	ctx := context.Background()
	session, _, err := NewService(paimosdb.DB).Register(ctx, RegisterInput{ProjectID: projectID, AgentName: "worker", Harness: "codex", Host: "test", SessionRef: "owned", WorkerLease: testWorkerLease, ManagementMode: ManagementManaged, Role: RoleWorker, SteerMode: SteerOwned, Capabilities: models.HarnessCapabilities{Inbox: true, Status: true, Steer: true}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := paimosdb.DB.Exec(`UPDATE harness_sessions SET phase='working',heartbeat_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, session.ID); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		project               int64
		session, agent, lease string
	}{
		{projectID, session.ID, "sender", testWorkerLease}, {projectID, uuid.NewString(), "worker", testWorkerLease}, {projectID + 1, session.ID, "worker", testWorkerLease}, {projectID, session.ID, "worker", ""},
	} {
		tx, err := paimosdb.DB.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = MessageSenderTx(ctx, tx, tc.project, tc.session, tc.agent, tc.lease)
		_ = tx.Rollback()
		if err == nil {
			t.Fatal("foreign worker proof accepted")
		}
	}
}
