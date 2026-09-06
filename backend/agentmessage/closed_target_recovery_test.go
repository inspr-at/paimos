// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentmessage

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	paimosdb "github.com/inspr-at/paimos/backend/db"
)

type closedTargetFixture struct {
	service                *Service
	project, actor         int64
	delivery               DeliveryStatus
	oldTarget, newTarget   *Target
	oldSession, newSession string
	input                  ClosedTargetRecoveryInput
}

func newClosedTargetFixture(t *testing.T) closedTargetFixture {
	t.Helper()
	service, project := openBusTestDB(t)
	allowBusSender(t, service, project, "codex:amy")
	result, err := paimosdb.DB.Exec(`INSERT INTO users(username,password,role,status) VALUES('recovery-admin','fixture','admin','active')`)
	if err != nil {
		t.Fatal(err)
	}
	actor, _ := result.LastInsertId()
	oldTarget, err := service.RegisterTarget(context.Background(), RegisterTargetInput{
		ProjectID: project, Address: "codex:amy", Adapter: AdapterManagedHarness,
		TargetKind: TargetKindHarnessSession, TargetRef: uuid.NewString(), MaximumLevel: "steer", Role: "primary",
	})
	if err != nil {
		t.Fatal(err)
	}
	oldSession := insertRecoverySession(t, project, oldTarget.ID, "stopped", "old-machine")
	message, err := service.SendEnvelope(context.Background(), SendEnvelopeInput{
		ProjectID: project, Sender: "sender", To: "codex:amy", Body: "recoverable fixture", DeliveryLevel: "steer",
	})
	if err != nil {
		t.Fatal(err)
	}
	newTarget, err := service.RegisterTarget(context.Background(), RegisterTargetInput{
		ProjectID: project, Address: "codex:amy", Adapter: AdapterManagedHarness,
		TargetKind: TargetKindHarnessSession, TargetRef: uuid.NewString(), MaximumLevel: "steer", Role: "primary",
	})
	if err != nil {
		t.Fatal(err)
	}
	newSession := insertRecoverySession(t, project, newTarget.ID, "working", "new-machine")
	deliveries, err := service.ListDeliveryStatus(context.Background(), project)
	if err != nil || len(deliveries) != 1 || deliveries[0].MessageID != message.MessageID {
		t.Fatalf("deliveries=%+v err=%v", deliveries, err)
	}
	input := ClosedTargetRecoveryInput{ProjectID: project, DeliveryID: deliveries[0].DeliveryID,
		ExpectedClosedSessionID: oldSession, ReplacementSessionID: newSession,
		Authority: func(context.Context, *sql.Tx, int64) (int64, error) { return actor, nil }}
	return closedTargetFixture{service: service, project: project, actor: actor, delivery: deliveries[0],
		oldTarget: oldTarget, newTarget: newTarget, oldSession: oldSession, newSession: newSession, input: input}
}

func insertRecoverySession(t *testing.T, project int64, targetID, phase, machine string) string {
	t.Helper()
	var agent int64
	if err := paimosdb.DB.QueryRow(`SELECT id FROM project_agents WHERE project_id=? AND name='amy'`, project).Scan(&agent); err != nil {
		t.Fatal(err)
	}
	sessionID, runtimeID := uuid.NewString(), uuid.NewString()
	runtimeGeneration, sessionGeneration := uuid.NewString(), uuid.NewString()
	activityState, activityReason, activityKind, closedReason := "idle", "turn_completed", "turn_completed", ""
	if phase == "stopped" {
		activityState, activityReason, activityKind, closedReason = "dead", "stopped", "", "stopped"
	}
	var userID int64
	if err := paimosdb.DB.QueryRow(`SELECT id FROM users WHERE username='recovery-admin'`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if _, err := paimosdb.DB.Exec(`INSERT INTO lifecycle_runtimes(id,project_id,generation,machine_id,user_id,api_key_id,
		lease_digest,registration_json,expires_at,created_at) VALUES(?,?,?,?,?,1,zeroblob(32),?,strftime('%Y-%m-%dT%H:%M:%fZ','now','+1 hour'),strftime('%Y-%m-%dT%H:%M:%fZ','now'))`,
		runtimeID, project, runtimeGeneration, machine, userID, `{"account_label":"chatgpt"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := paimosdb.DB.Exec(`INSERT INTO harness_sessions(id,project_id,project_agent_id,agent_name,harness,host,
		session_ref_digest,worker_lease_digest,message_target_id,management_mode,role,steer_mode,
		advertised_inbox,advertised_status,advertised_steer,advertised_interrupt,advertised_stop,phase,heartbeat_at,
		account_label,workspace_identity,workspace_path,workspace_kind,workspace_mode,
		activity_state,activity_reason,activity_event_kind,closed_reason)
		VALUES(?,?,?,'amy','codex',?,zeroblob(32),zeroblob(32),?,'managed','worker','owned',1,1,1,1,1,?,
		strftime('%Y-%m-%dT%H:%M:%fZ','now'),'chatgpt',printf('%064d',1),'/fixture','directory','exclusive',?,?,?,?)`,
		sessionID, project, agent, machine, targetID, phase, activityState, activityReason, activityKind, closedReason); err != nil {
		t.Fatal(err)
	}
	if _, err := paimosdb.DB.Exec(`INSERT INTO lifecycle_runtime_sessions(session_id,runtime_id,generation,created_at)
		VALUES(?,?,?,strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, sessionID, runtimeID, sessionGeneration); err != nil {
		t.Fatal(err)
	}
	return sessionID
}

func TestClosedTargetRecoveryPreservesCanonicalEnvelopeAndDeliversOnce(t *testing.T) {
	f := newClosedTargetFixture(t)
	before, err := f.service.ListEnvelopes(context.Background(), ListFilter{ProjectID: f.project})
	if err != nil || len(before) != 1 {
		t.Fatal(err)
	}
	beforeV1, _ := json.Marshal(before[0].V1())
	var messagePrimary, deliveryPrimary string
	if err := paimosdb.DB.QueryRow(`SELECT message.delivery_primary_target_id,delivery.primary_target_id
		FROM agent_messages message JOIN agent_message_deliveries delivery ON delivery.message_row_id=message.id
		WHERE delivery.delivery_id=?`, f.delivery.DeliveryID).Scan(&messagePrimary, &deliveryPrimary); err != nil {
		t.Fatal(err)
	}
	plan, err := f.service.InspectClosedTargetRecovery(context.Background(), f.input)
	if err != nil {
		t.Fatal(err)
	}
	if plan.OriginalTarget.TargetID != f.oldTarget.ID || plan.OriginalTarget.HarnessSessionID != f.oldSession ||
		plan.EffectiveTarget.TargetID != f.oldTarget.ID || plan.ReplacementTarget.TargetID != f.newTarget.ID || plan.ConsumerFence != 0 {
		t.Fatalf("plan=%+v", plan)
	}
	f.input.ExpectedTargetID, f.input.ExpectedTargetVersion = plan.EffectiveTarget.TargetID, plan.EffectiveTarget.TargetVersion
	f.input.ExpectedConsumerFence = plan.ConsumerFence
	recovered, err := f.service.RecoverClosedTarget(context.Background(), f.input)
	if err != nil || !recovered.Recovered || recovered.EffectiveTarget.TargetID != f.newTarget.ID {
		t.Fatalf("recovered=%+v err=%v", recovered, err)
	}
	var messageAfter, deliveryAfter string
	if err := paimosdb.DB.QueryRow(`SELECT message.delivery_primary_target_id,delivery.primary_target_id
		FROM agent_messages message JOIN agent_message_deliveries delivery ON delivery.message_row_id=message.id
		WHERE delivery.delivery_id=?`, f.delivery.DeliveryID).Scan(&messageAfter, &deliveryAfter); err != nil {
		t.Fatal(err)
	}
	if messageAfter != messagePrimary || deliveryAfter != deliveryPrimary || messageAfter != f.oldTarget.ID {
		t.Fatalf("canonical target changed message=%s/%s delivery=%s/%s", messagePrimary, messageAfter, deliveryPrimary, deliveryAfter)
	}
	statuses, err := f.service.ListDeliveryStatus(context.Background(), f.project)
	if err != nil || len(statuses) != 1 || statuses[0].OriginalTargetID != f.oldTarget.ID ||
		statuses[0].EffectiveTargetID != f.newTarget.ID || statuses[0].RecoveryCount != 1 {
		t.Fatalf("delivery status=%+v err=%v", statuses, err)
	}
	after, err := f.service.ListEnvelopes(context.Background(), ListFilter{ProjectID: f.project})
	if err != nil || len(after) != 1 || after[0].DeliveryEffectiveTarget == nil || after[0].DeliveryEffectiveTarget.BindingID != f.newTarget.ID {
		t.Fatalf("after=%+v err=%v", after, err)
	}
	afterV1, _ := json.Marshal(after[0].V1())
	if string(beforeV1) != string(afterV1) {
		t.Fatal("frozen V1 envelope changed after recovery")
	}
	oldPage, err := f.service.ListInbox(context.Background(), InboxInput{ProjectID: f.project, Address: "codex:amy", Agent: "amy", WorkerAdapter: AdapterManagedHarness, TargetID: f.oldTarget.ID, Limit: 10})
	if err != nil || len(oldPage.Messages) != 0 {
		t.Fatalf("closed generation page=%+v err=%v", oldPage, err)
	}
	newPage, err := f.service.ListInbox(context.Background(), InboxInput{ProjectID: f.project, Address: "codex:amy", Agent: "amy", WorkerAdapter: AdapterManagedHarness, TargetID: f.newTarget.ID, Limit: 10})
	if err != nil || len(newPage.Messages) != 1 || newPage.Messages[0].DeliveryWork == nil || newPage.Messages[0].DeliveryWork.State != "leased" {
		t.Fatalf("replacement page=%+v err=%v", newPage, err)
	}
	if _, err := f.service.CompleteLocalDelivery(context.Background(), CompleteDeliveryInput{ProjectID: f.project,
		Address: "codex:amy", Agent: "amy", Cursor: newPage.Messages[0].Cursor, DeliveryID: f.delivery.DeliveryID,
		TargetID: f.newTarget.ID, EffectiveLevel: "steer"}); err != nil {
		t.Fatal(err)
	}
	replay, err := f.service.RecoverClosedTarget(context.Background(), f.input)
	if err != nil || !replay.Recovered || replay.RecoverySequence != 1 {
		t.Fatalf("idempotent replay=%+v err=%v", replay, err)
	}
	var recoveries int
	if err := paimosdb.DB.QueryRow(`SELECT COUNT(*) FROM agent_message_delivery_recoveries WHERE delivery_id=?`, f.delivery.DeliveryID).Scan(&recoveries); err != nil || recoveries != 1 {
		t.Fatalf("recoveries=%d err=%v", recoveries, err)
	}
	if _, err := paimosdb.DB.Exec(`UPDATE agent_message_delivery_recoveries SET actor_user_id=actor_user_id WHERE delivery_id=?`, f.delivery.DeliveryID); err == nil {
		t.Fatal("recovery audit row was mutable")
	}
	if _, err := paimosdb.DB.Exec(`DELETE FROM agent_message_delivery_recoveries WHERE delivery_id=?`, f.delivery.DeliveryID); err == nil {
		t.Fatal("recovery audit row was deletable while its delivery exists")
	}
}

func TestClosedTargetRecoveryRefusesAnyEffectOrPolicyAmbiguity(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*testing.T, closedTargetFixture)
	}{
		{"leased", func(t *testing.T, f closedTargetFixture) {
			_, _ = paimosdb.DB.Exec(`UPDATE agent_message_deliveries SET state='leased',attempt_count=1,lease_until=strftime('%Y-%m-%dT%H:%M:%fZ','now','+1 minute') WHERE delivery_id=?`, f.delivery.DeliveryID)
		}},
		{"consumer fence", func(t *testing.T, f closedTargetFixture) {
			_, _ = paimosdb.DB.Exec(`UPDATE agent_message_deliveries SET consumer_fence=1 WHERE delivery_id=?`, f.delivery.DeliveryID)
		}},
		{"held action", func(t *testing.T, f closedTargetFixture) {
			_, _ = paimosdb.DB.Exec(`UPDATE agent_messages SET is_action_request=1,delivered=0,delivered_at=NULL,held_reason='human_approval' WHERE message_id=?`, f.delivery.MessageID)
		}},
		{"fallback state", func(t *testing.T, f closedTargetFixture) {
			_, _ = paimosdb.DB.Exec(`UPDATE agent_message_deliveries SET fallback_reason='idle' WHERE delivery_id=?`, f.delivery.DeliveryID)
		}},
		{"stale replacement runtime", func(t *testing.T, f closedTargetFixture) {
			_, _ = paimosdb.DB.Exec(`UPDATE lifecycle_runtimes SET expires_at='2000-01-01T00:00:00.000Z' WHERE id=(SELECT runtime_id FROM lifecycle_runtime_sessions WHERE session_id=?)`, f.newSession)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newClosedTargetFixture(t)
			tc.mutate(t, f)
			if _, err := f.service.InspectClosedTargetRecovery(context.Background(), f.input); err == nil {
				t.Fatal("unsafe recovery inspection succeeded")
			}
			var recoveries int
			_ = paimosdb.DB.QueryRow(`SELECT COUNT(*) FROM agent_message_delivery_recoveries`).Scan(&recoveries)
			if recoveries != 0 {
				t.Fatal("unsafe recovery appended history")
			}
		})
	}
}

func TestClosedTargetRecoveryAndDrainHaveOneAuthoritativeWinner(t *testing.T) {
	f := newClosedTargetFixture(t)
	plan, err := f.service.InspectClosedTargetRecovery(context.Background(), f.input)
	if err != nil {
		t.Fatal(err)
	}
	f.input.ExpectedTargetID, f.input.ExpectedTargetVersion = plan.EffectiveTarget.TargetID, plan.EffectiveTarget.TargetVersion
	f.input.ExpectedConsumerFence = 0
	start := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(2)
	var recoverErr, drainErr error
	var oldPage *InboxPage
	go func() {
		defer wait.Done()
		<-start
		_, recoverErr = f.service.RecoverClosedTarget(context.Background(), f.input)
	}()
	go func() {
		defer wait.Done()
		<-start
		oldPage, drainErr = f.service.ListInbox(context.Background(), InboxInput{ProjectID: f.project, Address: "codex:amy", Agent: "amy",
			WorkerAdapter: AdapterManagedHarness, TargetID: f.oldTarget.ID, Limit: 10})
	}()
	close(start)
	wait.Wait()
	if recoverErr != nil && drainErr != nil {
		t.Fatalf("both operations failed recovery=%v drain=%v", recoverErr, drainErr)
	}
	var attempts, recoveries int
	if err := paimosdb.DB.QueryRow(`SELECT attempt_count FROM agent_message_deliveries WHERE delivery_id=?`, f.delivery.DeliveryID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if err := paimosdb.DB.QueryRow(`SELECT COUNT(*) FROM agent_message_delivery_recoveries WHERE delivery_id=?`, f.delivery.DeliveryID).Scan(&recoveries); err != nil {
		t.Fatal(err)
	}
	oldLeased := oldPage != nil && len(oldPage.Messages) == 1 && oldPage.Messages[0].DeliveryWork != nil && oldPage.Messages[0].DeliveryWork.State == "leased"
	if (recoveries == 1) == oldLeased || attempts > 1 || recoveries > 1 {
		t.Fatalf("ambiguous winners recoveries=%d attempts=%d old_leased=%v recovery_err=%v drain_err=%v", recoveries, attempts, oldLeased, recoverErr, drainErr)
	}
}

func TestClosedTargetRecoveryRequiresReauthorizationBeforeReplay(t *testing.T) {
	f := newClosedTargetFixture(t)
	plan, err := f.service.InspectClosedTargetRecovery(context.Background(), f.input)
	if err != nil {
		t.Fatal(err)
	}
	f.input.ExpectedTargetID, f.input.ExpectedTargetVersion = plan.EffectiveTarget.TargetID, plan.EffectiveTarget.TargetVersion
	if _, err := f.service.RecoverClosedTarget(context.Background(), f.input); err != nil {
		t.Fatal(err)
	}
	f.input.Authority = func(context.Context, *sql.Tx, int64) (int64, error) {
		return 0, coded("agent_message_unauthorized", "current request credential is unavailable")
	}
	if _, err := f.service.RecoverClosedTarget(context.Background(), f.input); err == nil {
		t.Fatal("revoked operator received idempotent recovery evidence")
	}
}

func TestClosedTargetRecoveryRejectsStaleCASAndDifferentSuccessor(t *testing.T) {
	f := newClosedTargetFixture(t)
	plan, err := f.service.InspectClosedTargetRecovery(context.Background(), f.input)
	if err != nil {
		t.Fatal(err)
	}
	f.input.ExpectedTargetID, f.input.ExpectedTargetVersion = plan.EffectiveTarget.TargetID, plan.EffectiveTarget.TargetVersion+1
	if _, err := f.service.RecoverClosedTarget(context.Background(), f.input); err == nil {
		t.Fatal("stale target revision recovered")
	}
	f.input.ExpectedTargetVersion = plan.EffectiveTarget.TargetVersion
	if _, err := f.service.RecoverClosedTarget(context.Background(), f.input); err != nil {
		t.Fatal(err)
	}
	// An exact replay remains idempotent even after the delivery state changes;
	// a stale request with a different successor is never reinterpreted.
	f.input.ReplacementSessionID = uuid.NewString()
	if _, err := f.service.RecoverClosedTarget(context.Background(), f.input); err == nil {
		t.Fatal("different successor was accepted as idempotent replay")
	}
}

func TestClosedTargetRecoveryFreshnessBoundary(t *testing.T) {
	f := newClosedTargetFixture(t)
	if _, err := paimosdb.DB.Exec(`UPDATE harness_sessions SET heartbeat_at=? WHERE id=?`, consumerStamp(time.Now().Add(-2*time.Minute)), f.newSession); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.InspectClosedTargetRecovery(context.Background(), f.input); err == nil {
		t.Fatal("stale reporter heartbeat was accepted")
	}
}

func TestClosedTargetRecoveryHistoryIsBoundedAndSelectsDeterministicTail(t *testing.T) {
	f := newClosedTargetFixture(t)
	input := f.input
	replacementTarget, replacementSession := f.newTarget, f.newSession
	var lastTarget *Target
	for sequence := 1; sequence <= maxClosedTargetRecoveries; sequence++ {
		input.ReplacementSessionID = replacementSession
		plan, err := f.service.InspectClosedTargetRecovery(context.Background(), input)
		if err != nil {
			t.Fatalf("inspect sequence %d: %v", sequence, err)
		}
		input.ExpectedTargetID, input.ExpectedTargetVersion = plan.EffectiveTarget.TargetID, plan.EffectiveTarget.TargetVersion
		input.ExpectedConsumerFence = plan.ConsumerFence
		recovered, err := f.service.RecoverClosedTarget(context.Background(), input)
		if err != nil || recovered.RecoverySequence != sequence || recovered.EffectiveTarget.TargetID != replacementTarget.ID {
			t.Fatalf("recover sequence %d plan=%+v err=%v", sequence, recovered, err)
		}
		lastTarget = replacementTarget
		if _, err := paimosdb.DB.Exec(`UPDATE harness_sessions SET phase='stopped',activity_state='dead',
			activity_reason='stopped',activity_event_kind='',closed_reason='stopped' WHERE id=?`, replacementSession); err != nil {
			t.Fatal(err)
		}
		nextTarget, err := f.service.RegisterTarget(context.Background(), RegisterTargetInput{
			ProjectID: f.project, Address: "codex:amy", Adapter: AdapterManagedHarness,
			TargetKind: TargetKindHarnessSession, TargetRef: uuid.NewString(), MaximumLevel: "steer", Role: "primary",
		})
		if err != nil {
			t.Fatal(err)
		}
		nextSession := insertRecoverySession(t, f.project, nextTarget.ID, "working", "chain-machine-"+string(rune('a'+sequence)))
		input.ExpectedClosedSessionID = replacementSession
		input.ExpectedTargetID, input.ExpectedTargetVersion = "", 0
		replacementTarget, replacementSession = nextTarget, nextSession
	}
	if _, err := f.service.InspectClosedTargetRecovery(context.Background(), input); err == nil {
		t.Fatal("ninth recovery passed the bounded-history limit")
	}
	envelope, err := f.service.GetEnvelope(context.Background(), f.project, f.delivery.MessageID)
	if err != nil || envelope.DeliveryEffectiveTarget == nil || envelope.DeliveryEffectiveTarget.BindingID != lastTarget.ID ||
		envelope.DeliveryEffectiveTarget.Sequence != maxClosedTargetRecoveries {
		t.Fatalf("effective tail=%+v err=%v", envelope.DeliveryEffectiveTarget, err)
	}
	var count int
	if err := paimosdb.DB.QueryRow(`SELECT COUNT(*) FROM agent_message_delivery_recoveries WHERE delivery_id=?`, f.delivery.DeliveryID).Scan(&count); err != nil || count != maxClosedTargetRecoveries {
		t.Fatalf("recovery history count=%d err=%v", count, err)
	}
}

func TestClosedTargetRecoveryPreservesHumanProductSessionBinding(t *testing.T) {
	service, project := openBusTestDB(t)
	allowBusSender(t, service, project, "codex:amy")
	var receiver int64
	if err := paimosdb.DB.QueryRow(`SELECT id FROM project_agents WHERE project_id=? AND name='amy'`, project).Scan(&receiver); err != nil {
		t.Fatal(err)
	}
	result, err := paimosdb.DB.Exec(`INSERT INTO users(username,password,role,status) VALUES('recovery-admin','fixture','admin','active')`)
	if err != nil {
		t.Fatal(err)
	}
	actor, _ := result.LastInsertId()
	oldTarget, err := service.RegisterTarget(context.Background(), RegisterTargetInput{
		ProjectID: project, Address: "codex:amy", Adapter: AdapterManagedHarness,
		TargetKind: TargetKindHarnessSession, TargetRef: uuid.NewString(), MaximumLevel: "steer", Role: "primary",
	})
	if err != nil {
		t.Fatal(err)
	}
	oldSession := insertRecoverySession(t, project, oldTarget.ID, "stopped", "human-old-machine")
	productSession := uuid.NewString()
	if _, err := paimosdb.DB.Exec(`INSERT INTO product_sessions(
		product_session_id,project_id,target_kind,target_project_agent_id,title,created_by_user_id,updated_by_user_id)
		VALUES(?,?,'project_agent',?,'Human recovery fixture',?,?)`, productSession, project, receiver, actor, actor); err != nil {
		t.Fatal(err)
	}
	messageID, deliveryID := uuid.NewString(), uuid.NewString()
	result, err = paimosdb.DB.Exec(`INSERT INTO agent_messages(
		to_agent_id,body,delivered,delivered_at,message_id,role,parts_json,from_address,to_address,thread_id,
		delivery_level,delivery_primary_target_id,from_user_id,product_session_id)
		VALUES(?,'human message pending at a closed generation',1,strftime('%Y-%m-%dT%H:%M:%fZ','now'),?,
		'human','[{"kind":"text","text":"human message pending at a closed generation"}]','paimos','codex:amy',?,
		'steer',?,?,?)`, receiver, messageID, messageID, oldTarget.ID, actor, productSession)
	if err != nil {
		t.Fatal(err)
	}
	messageRowID, _ := result.LastInsertId()
	if _, err := paimosdb.DB.Exec(`INSERT INTO agent_message_deliveries(
		delivery_id,message_row_id,instance,primary_target_id,requested_level,state)
		VALUES(?,?,?,?, 'steer','pending')`, deliveryID, messageRowID, instanceName(), oldTarget.ID); err != nil {
		t.Fatal(err)
	}
	newTarget, err := service.RegisterTarget(context.Background(), RegisterTargetInput{
		ProjectID: project, Address: "codex:amy", Adapter: AdapterManagedHarness,
		TargetKind: TargetKindHarnessSession, TargetRef: uuid.NewString(), MaximumLevel: "steer", Role: "primary",
	})
	if err != nil {
		t.Fatal(err)
	}
	newSession := insertRecoverySession(t, project, newTarget.ID, "working", "human-new-machine")
	input := ClosedTargetRecoveryInput{ProjectID: project, DeliveryID: deliveryID,
		ExpectedClosedSessionID: oldSession, ReplacementSessionID: newSession,
		Authority: func(context.Context, *sql.Tx, int64) (int64, error) { return actor, nil }}
	plan, err := service.InspectClosedTargetRecovery(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	input.ExpectedTargetID, input.ExpectedTargetVersion = plan.EffectiveTarget.TargetID, plan.EffectiveTarget.TargetVersion
	if _, err := service.RecoverClosedTarget(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	var gotSession, gotRole, messageTarget, deliveryTarget, effectiveTarget string
	if err := paimosdb.DB.QueryRow(`SELECT message.product_session_id,message.role,message.delivery_primary_target_id,
		delivery.primary_target_id,`+selectedDeliveryTargetSQL+`
		FROM agent_messages message JOIN agent_message_deliveries d ON d.message_row_id=message.id
		JOIN agent_message_deliveries delivery ON delivery.delivery_id=d.delivery_id WHERE d.delivery_id=?`, deliveryID).Scan(
		&gotSession, &gotRole, &messageTarget, &deliveryTarget, &effectiveTarget); err != nil {
		t.Fatal(err)
	}
	if gotSession != productSession || gotRole != "human" || messageTarget != oldTarget.ID || deliveryTarget != oldTarget.ID || effectiveTarget != newTarget.ID {
		t.Fatalf("human binding changed session=%q role=%q message_target=%q delivery_target=%q effective=%q", gotSession, gotRole, messageTarget, deliveryTarget, effectiveTarget)
	}
}
