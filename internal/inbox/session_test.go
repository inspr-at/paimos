// SPDX-License-Identifier: AGPL-3.0-only

package inbox

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func messageTestSession(t *testing.T, w *world, project string, p tenant.Principal, label string) string {
	t.Helper()
	var id string
	err := db.InTenant(dbtest.Seed(t.Context()), w.db.App, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,harness,host,management,role,ref_digest,lease_digest,display_label) VALUES($1,$2,$3,'codex','test','managed','worker',decode(replace(gen_random_uuid()::text,'-',''),'hex'),decode(replace(gen_random_uuid()::text,'-',''),'hex'),$4) RETURNING id::text`, p.TenantID, project, p.ID, label).Scan(&id)
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestSessionMessagesIsolationAndLegacyWire(t *testing.T) {
	w, m, project, srv := messagingWorld(t)
	first := messageTestSession(t, w, project, w.agent, "First worker")
	second := messageTestSession(t, w, project, w.agent, "Second worker")
	path := "/api/projects/" + project + "/messages"
	legacy := mustCompatSend(t, m, w.sender, project, compatInput("codex:worker", "legacy"))
	for i, id := range []string{first, second} {
		in := compatInput("codex:worker", fmt.Sprint(i))
		in.RecipientSessionID = &id
		in.ExpectsReply = true
		status, body := compatPost(t, srv, w.sender, path, in)
		if status != 201 {
			t.Fatalf("send %d: %s", status, body)
		}
		msg := mustJSON[CompatMessage](t, body)
		if msg.RecipientSessionID == nil || *msg.RecipientSessionID != id || msg.DeliveryTarget != nil {
			t.Fatalf("wrong binding/transport: %+v", msg)
		}
	}
	for _, base := range []string{path + "/listen?to=codex:worker", "/api/inbox/messages?wait_ms=0"} {
		for _, id := range []string{first, second} {
			status, body := do(t, srv, w.agent.ID, "GET", base+"&session="+id, "", nil)
			page := mustJSON[compatPage](t, body)
			if status != 200 || len(page.Items) != 2 {
				t.Fatalf("listen %d items=%d: %s", status, len(page.Items), body)
			}
			if page.Items[0].ID != legacy.ID || page.Items[1].RecipientSessionID == nil || *page.Items[1].RecipientSessionID != id {
				t.Fatal("wrong generation received message")
			}
		}
		status, body := do(t, srv, w.agent.ID, "GET", base, "", nil)
		if status != 200 || len(mustJSON[compatPage](t, body).Items) != 1 {
			t.Fatalf("legacy read changed: %s", body)
		}
		for _, field := range []string{"recipient_session_id", "sender_session_id", "sender_label"} {
			if bytes.Contains(body, []byte(field)) {
				t.Fatalf("legacy field added: %s", field)
			}
		}
	}
	// Compare the complete legacy compatibility page byte-for-byte, including order.
	status, body := do(t, srv, w.agent.ID, "GET", path+"/listen?to=codex:worker", "", nil)
	expected, _ := json.Marshal(compatPage{Items: []CompatMessage{legacy}, NextAfter: legacy.SentEventID, Preamble: untrustedMessagePreamble})
	if status != 200 || !bytes.Equal(bytes.TrimSpace(body), expected) {
		t.Fatalf("legacy bytes changed\ngot %s\nwant %s", body, expected)
	}
	// A principal-wide delivery worker must skip the targeted messages entirely.
	work, err := m.claim(t.Context(), w.agent, project, claimInput{To: "codex:worker", Adapter: "codex"})
	if err != nil || work == nil || work.Cursor != legacy.SentEventID {
		t.Fatalf("legacy claim %v %v", work, err)
	}
	for _, bad := range []string{"bad-id", "00000000-0000-4000-8000-000000000001"} {
		status, _ := do(t, srv, w.agent.ID, "GET", path+"/listen?session="+bad, "", nil)
		if status != 400 && status != 404 {
			t.Fatalf("invalid session accepted %d", status)
		}
	}
	status, _ = do(t, srv, w.recipient.ID, "GET", path+"/listen?session="+first, "", nil)
	if status != 404 {
		t.Fatalf("foreign principal session status %d", status)
	}
}

func TestSessionMessageHistoryAndLifecycle(t *testing.T) {
	w, m, project, srv := messagingWorld(t)
	first := messageTestSession(t, w, project, w.agent, "Original label")
	second := messageTestSession(t, w, project, w.agent, "New generation")
	path := "/api/projects/" + project + "/messages"
	outgoing := compatInput(w.sender.ID, "outgoing")
	outgoing.SenderSessionID = &first
	outgoing.ExpectsReply = true
	sent := mustCompatSend(t, m, w.agent, project, outgoing)
	incoming := compatInput("codex:worker", "incoming")
	incoming.RecipientSessionID = &first
	incoming.ExpectsReply = true
	targeted := mustCompatSend(t, m, w.sender, project, incoming)
	replyable := incoming
	replyable.Key = "replyable"
	question := mustCompatSend(t, m, w.sender, project, replyable)
	correct := compatInput(w.sender.ID, "correct-generation")
	correct.SenderSessionID = &first
	correct.ReplyTo = &question.ID
	mustCompatSend(t, m, w.agent, project, correct)
	other := incoming
	other.RecipientSessionID = &second
	other.Key = "other"
	otherMsg := mustCompatSend(t, m, w.sender, project, other)
	broadcast := compatInput("codex:worker", "broadcast")
	broadcast.ExpectsReply = true
	unbound := mustCompatSend(t, m, w.sender, project, broadcast)
	reply := compatInput(w.sender.ID, "wrong-generation")
	reply.SenderSessionID = &second
	reply.ReplyTo = &targeted.ID
	if _, err := m.commitMessage(t.Context(), w.agent, project, reply); err != errNotFound {
		t.Fatalf("wrong generation reply: %v", err)
	}
	err := db.InTenant(dbtest.Seed(t.Context()), w.db.App, w.agent.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET display_label='Renamed',phase='stopped',stopped_at=now() WHERE id=$1`, first)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	status, body := do(t, srv, w.admin.ID, "GET", path+"?session="+first, "", nil)
	page := mustJSON[compatPage](t, body)
	if status != 200 || len(page.Items) != 4 {
		t.Fatalf("inspect %d %s", status, body)
	}
	for _, msg := range page.Items {
		if msg.ExpectsReply && msg.ReplyObligation != "closed" {
			t.Fatalf("obligation remains open: %s", msg.ID)
		}
		if msg.ID == sent.ID && msg.SenderLabel != "Original label" {
			t.Fatalf("label changed: %s", msg.SenderLabel)
		}
	}
	assertSessionClosureEvents(t, w, []string{sent.ID, targeted.ID}, 2)
	incoming.Key = "after-stop"
	status, body = compatPost(t, srv, w.sender, path, incoming)
	if status != 409 || !bytes.Contains(body, []byte("This session has ended.")) {
		t.Fatalf("stopped send: %d %s", status, body)
	}
	for _, base := range []string{path + "/listen?session=", "/api/inbox/messages?session="} {
		status, _ = do(t, srv, w.agent.ID, "GET", base+first, "", nil)
		if status != 409 {
			t.Fatalf("ended listen %d", status)
		}
	}
	// Other generations and legacy obligations remain untouched until their own end.
	err = db.InTenant(dbtest.Seed(t.Context()), w.db.App, w.agent.TenantID, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM inbox_reply_obligations WHERE message_id=ANY($1::uuid[]) AND closed_at IS NULL`, []string{otherMsg.ID, unbound.ID}).Scan(&n); err != nil {
			return err
		}
		if n != 2 {
			return fmt.Errorf("unrelated obligations closed")
		}
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET phase='stopped',stopped_at=now(),archived_at=now(),recovery_process_state='unknown',recovery_request_id=gen_random_uuid(),recovery_request_digest='fixture'::bytea,recovery_actor_id=agent_principal_id,recovery_reason='fixture' WHERE id=$1`, second)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	assertSessionClosureEvents(t, w, []string{sent.ID, targeted.ID, otherMsg.ID}, 3)
	// Archiving an already stopped generation must not emit duplicate closures.
	err = db.InTenant(dbtest.Seed(t.Context()), w.db.App, w.agent.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET archived_at=now(),recovery_process_state='unknown',recovery_request_id=gen_random_uuid(),recovery_request_digest='fixture'::bytea,recovery_actor_id=agent_principal_id,recovery_reason='fixture' WHERE id=$1`, first)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	assertSessionClosureEvents(t, w, []string{sent.ID, targeted.ID, otherMsg.ID}, 3)
	other.Key = "after-archive"
	status, _ = compatPost(t, srv, w.sender, path, other)
	if status != 409 {
		t.Fatalf("archive send %d", status)
	}
	status, body = do(t, srv, w.admin.ID, "GET", path+"?session="+second, "", nil)
	if status != 200 || mustJSON[compatPage](t, body).Items[0].ReplyObligation != "closed" {
		t.Fatalf("archive obligation: %s", body)
	}
}

func TestDirectSessionSendBindingAndLegacyShape(t *testing.T) {
	w, _, project, srv := messagingWorld(t)
	id := messageTestSession(t, w, project, w.agent, "Worker")
	payload := map[string]any{"recipient_principal_id": w.agent.ID, "body": "direct", "idempotency_key": "direct", "recipient_session_id": id}
	status, body := compatPost(t, srv, w.sender, "/api/inbox/messages", payload)
	if status != 201 {
		t.Fatalf("direct send %d %s", status, body)
	}
	direct := mustJSON[Message](t, body)
	status, body = do(t, srv, w.agent.ID, "GET", "/api/inbox/messages?session="+id, "", nil)
	if status != 200 || len(mustJSON[Page](t, body).Items) != 1 {
		t.Fatalf("direct listen %d %s", status, body)
	}
	status, body = do(t, srv, w.agent.ID, "POST", "/api/inbox/messages/"+direct.ID+"/ack", "", nil)
	if status != 200 {
		t.Fatalf("session ack %d %s", status, body)
	}
	status, body = do(t, srv, w.sender.ID, "GET", "/api/inbox/messages/"+direct.ID+"/receipt", "", nil)
	if status != 200 || mustJSON[Receipt](t, body).State != "handed_off" {
		t.Fatalf("session receipt %d %s", status, body)
	}
	payload["recipient_session_id"] = nil
	status, _ = compatPost(t, srv, w.sender, "/api/inbox/messages", payload)
	if status != 409 {
		t.Fatalf("retargeted retry %d", status)
	}
	payload["idempotency_key"] = "unbound"
	status, body = compatPost(t, srv, w.sender, "/api/inbox/messages", payload)
	if status != 201 || bytes.Contains(body, []byte("sender_label")) || bytes.Contains(body, []byte("recipient_session_id")) {
		t.Fatalf("legacy direct shape %d %s", status, body)
	}
}

// AEON-369: a session-bound send needs no registered target and is not given
// a principal-wide one, even when that target exists. The other generation of
// the same principal does not receive it. An unbound send still needs a target.
func TestSessionBoundSendNeedsNoTarget(t *testing.T) {
	w, m, project, srv := messagingWorld(t)
	stored, err := m.storeTarget(t.Context(), w.admin, project, targetInput{Address: "codex:worker", Adapter: "codex", Kind: "codex_thread", Ref: "fixture-private-thread", Role: "primary", MaximumLevel: "simple"})
	if err != nil || !stored.Enabled || stored.PrincipalID != w.agent.ID {
		t.Fatalf("target: %+v %v", stored, err)
	}
	first := messageTestSession(t, w, project, w.agent, "First worker")
	second := messageTestSession(t, w, project, w.agent, "Second worker")
	path := "/api/projects/" + project + "/messages"
	byPrincipal := compatInput(w.agent.ID, "bound-principal")
	byPrincipal.RecipientSessionID = &first
	status, body := compatPost(t, srv, w.sender, path, byPrincipal)
	if status != 201 {
		t.Fatalf("principal send %d %s", status, body)
	}
	bound := mustJSON[CompatMessage](t, body)
	if bound.RecipientPrincipalID != w.agent.ID || bound.RecipientSessionID == nil || *bound.RecipientSessionID != first || bound.DeliveryTarget != nil {
		t.Fatalf("principal binding: %+v", bound)
	}
	assertDelivery(t, w, bound.ID, nil, "pending", "")
	byAddress := compatInput("codex:worker", "bound-address")
	byAddress.RecipientSessionID = &first
	status, body = compatPost(t, srv, w.sender, path, byAddress)
	if status != 201 {
		t.Fatalf("address send %d %s", status, body)
	}
	addressed := mustJSON[CompatMessage](t, body)
	if addressed.DeliveryTarget != nil || addressed.RecipientSessionID == nil || *addressed.RecipientSessionID != first {
		t.Fatalf("address binding used a target: %+v", addressed)
	}
	assertDelivery(t, w, addressed.ID, nil, "pending", "")
	work, err := m.claim(t.Context(), w.agent, project, claimInput{To: "codex:worker", Adapter: "codex"})
	if err != nil || work != nil {
		t.Fatalf("session-bound claim %v %v", work, err)
	}
	status, body = do(t, srv, w.agent.ID, "GET", "/api/inbox/messages?wait_ms=0&exact_session=true&session="+first, "", nil)
	if status != 200 || !containsMessage(t, body, bound.ID) || !containsMessage(t, body, addressed.ID) {
		t.Fatalf("first session %d %s", status, body)
	}
	status, body = do(t, srv, w.agent.ID, "GET", "/api/inbox/messages?wait_ms=0&exact_session=true&session="+second, "", nil)
	if status != 200 || containsMessage(t, body, bound.ID) || containsMessage(t, body, addressed.ID) {
		t.Fatalf("second session received it: %d %s", status, body)
	}
	unbound := compatInput(w.agent.ID, "unbound-principal")
	status, body = compatPost(t, srv, w.sender, path, unbound)
	if status != 201 || bytes.Contains(body, []byte("recipient_session_id")) {
		t.Fatalf("unbound send %d %s", status, body)
	}
	missing := mustJSON[CompatMessage](t, body)
	if missing.RecipientSessionID != nil || missing.DeliveryTarget != nil {
		t.Fatalf("unbound binding: %+v", missing)
	}
	assertDelivery(t, w, missing.ID, nil, "blocked", "target_missing")
	status, body = do(t, srv, w.agent.ID, "GET", "/api/inbox/messages?wait_ms=0&exact_session=true&session="+first, "", nil)
	if status != 200 || containsMessage(t, body, missing.ID) {
		t.Fatalf("unbound message became session-bound: %d %s", status, body)
	}
}

func assertDelivery(t *testing.T, w *world, messageID string, targetID *string, state, reason string) {
	t.Helper()
	var gotTarget *string
	var gotState, gotReason string
	var gotSession *string
	guaranteeRow(t, w, `SELECT d.target_id::text,d.state,d.reason,c.recipient_session_id::text FROM inbox_message_deliveries d JOIN inbox_compat_messages c ON c.id=d.message_id WHERE d.message_id=$1::uuid`, []any{messageID}, &gotTarget, &gotState, &gotReason, &gotSession)
	if (gotTarget == nil) != (targetID == nil) || (gotTarget != nil && targetID != nil && *gotTarget != *targetID) || gotState != state || gotReason != reason {
		t.Fatalf("delivery %s: target=%v state=%s reason=%s", messageID, gotTarget, gotState, gotReason)
	}
	if state == "pending" && gotSession == nil {
		t.Fatal("session-bound delivery lost its session")
	}
	if state == "blocked" && gotSession != nil {
		t.Fatalf("unbound delivery gained session %s", *gotSession)
	}
}

func containsMessage(t *testing.T, body []byte, id string) bool {
	t.Helper()
	for _, item := range mustJSON[Page](t, body).Items {
		if item.ID == id {
			return true
		}
	}
	return false
}

func TestEndedSessionCannotAcknowledge(t *testing.T) {
	for _, ending := range []string{"stop", "archive"} {
		t.Run(ending, func(t *testing.T) {
			w, m, project, srv := messagingWorld(t)
			id := messageTestSession(t, w, project, w.agent, "Ending worker")
			in := compatInput("codex:worker", "pending")
			in.RecipientSessionID = &id
			pending := mustCompatSend(t, m, w.sender, project, in)
			in.Key = "already-acked"
			acked := mustCompatSend(t, m, w.sender, project, in)
			ackPath := func(messageID string) string { return "/api/inbox/messages/" + messageID + "/ack" }
			status, body := do(t, srv, w.agent.ID, "POST", ackPath(acked.ID), "", nil)
			if status != 200 {
				t.Fatalf("active ack: %d %s", status, body)
			}
			unbound := mustCompatSend(t, m, w.sender, project, compatInput("codex:worker", "broadcast"))
			err := db.InTenant(dbtest.Seed(t.Context()), w.db.App, w.agent.TenantID, func(tx pgx.Tx) error {
				stmt := `UPDATE harness_sessions SET phase='stopped',stopped_at=now() WHERE id=$1`
				if ending == "archive" {
					stmt = `UPDATE harness_sessions SET phase='stopped',stopped_at=now(),archived_at=now(),recovery_process_state='unknown',recovery_request_id=gen_random_uuid(),recovery_request_digest='fixture'::bytea,recovery_actor_id=agent_principal_id,recovery_reason='fixture' WHERE id=$1`
				}
				_, err := tx.Exec(t.Context(), stmt, id)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, messageID := range []string{pending.ID, acked.ID} {
				status, body := do(t, srv, w.agent.ID, "POST", ackPath(messageID), "", nil)
				if status != 409 || !bytes.Contains(body, []byte(`"code":"session_ended"`)) {
					t.Fatalf("ended ack: %d %s", status, body)
				}
			}
			status, body = do(t, srv, w.agent.ID, "POST", ackPath(unbound.ID), "", nil)
			if status != 200 {
				t.Fatalf("broadcast ack: %d %s", status, body)
			}
			err = db.InTenant(dbtest.Seed(t.Context()), w.db.App, w.agent.TenantID, func(tx pgx.Tx) error {
				var unchanged bool
				if err := tx.QueryRow(t.Context(), `SELECT acked_at IS NULL AND NOT EXISTS(SELECT 1 FROM inbox_message_deliveries WHERE message_id=$1 AND state='delivered') AND NOT EXISTS(SELECT 1 FROM events WHERE type='inbox.acked' AND after->>'id'=$1::text) FROM inbox_messages WHERE id=$1`, pending.ID).Scan(&unchanged); err != nil {
					return err
				}
				if !unchanged {
					return fmt.Errorf("ended ack changed message, delivery or event")
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func assertSessionClosureEvents(t *testing.T, w *world, messageIDs []string, expected int) {
	t.Helper()
	err := db.InTenant(dbtest.Seed(t.Context()), w.db.App, w.agent.TenantID, func(tx pgx.Tx) error {
		var total, matching int
		if err := tx.QueryRow(t.Context(), `SELECT count(*),count(*) FILTER (WHERE after->>'message_id'=ANY($1::text[]) AND NOT (after ? 'reply_message_id')) FROM events WHERE type='inbox.reply_obligation_closed' AND after->>'closed_reason'='session_ended'`, messageIDs).Scan(&total, &matching); err != nil {
			return err
		}
		if total != expected || matching != expected {
			return fmt.Errorf("closure events: total=%d matching=%d expected=%d", total, matching, expected)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// AEON-279 integration: a managed hand-off settles through the one terminal
// failure path of AEON-280. Failure (without a prior ack) fails the delivery and
// receipt with a mapped reason, hides the message and notifies the sender
// exactly once, also on replay; confirmation hands off bound and unbound alike.
func TestManagedSettlementUsesTheOneFailurePath(t *testing.T) {
	w, m, project, srv := messagingWorld(t)
	session := messageTestSession(t, w, project, w.agent, "Managed worker")
	for _, targeted := range []bool{false, true} {
		for _, reason := range []string{"", "outcome_unconfirmed", "child_unavailable"} {
			failed := reason != ""
			in := compatInput("codex:worker", fmt.Sprintf("settle-%t-%s", targeted, reason))
			if targeted {
				in.RecipientSessionID = &session
			}
			message := mustCompatSend(t, m, w.sender, project, in)
			settle := func() error {
				return db.InTenant(dbtest.Seed(t.Context()), w.db.App, w.agent.TenantID, func(tx pgx.Tx) error {
					if failed {
						return FailSessionMessage(t.Context(), tx, w.agent, message.ID, reason)
					}
					if _, err := tx.Exec(t.Context(), `UPDATE inbox_messages SET acked_at=clock_timestamp(),acked_by_principal_id=$2 WHERE id=$1`, message.ID, w.agent.ID); err != nil {
						return err
					}
					return ConfirmSessionMessage(t.Context(), tx, w.agent, message.ID)
				})
			}
			if err := settle(); err != nil {
				t.Fatal(err)
			}
			if failed {
				// A replayed failure changes nothing and notifies nobody again.
				if err := settle(); err != nil {
					t.Fatal(err)
				}
			}
			mapped := map[string]string{"outcome_unconfirmed": ReasonTransportError, "child_unavailable": ReasonUnavailable}[reason]
			var delivery, deliveryReason string
			guaranteeRow(t, w, `SELECT state,reason FROM inbox_message_deliveries WHERE message_id=$1::uuid`, []any{message.ID}, &delivery, &deliveryReason)
			state, receiptReason := receiptState(t, w, message.ID)
			notices := noticesFor(t, w, message.ID)
			var acked bool
			guaranteeRow(t, w, `SELECT acked_at IS NOT NULL FROM inbox_messages WHERE id=$1::uuid`, []any{message.ID}, &acked)
			if failed {
				sender, ok := notices[""]
				if delivery != "dead" || deliveryReason != mapped || state != "failed" || receiptReason != mapped || acked ||
					len(notices) != 1 || !ok || sender.recipient != w.sender.ID || !strings.Contains(sender.body, "Not delivered") {
					t.Fatalf("targeted=%t reason=%s: delivery=%s/%s receipt=%s/%s acked=%t notices=%+v", targeted, reason, delivery, deliveryReason, state, receiptReason, acked, notices)
				}
				status, body := do(t, srv, w.agent.ID, "GET", "/api/inbox/messages?wait_ms=0", "", nil)
				if status != 200 || strings.Contains(string(body), message.ID) {
					t.Fatalf("failed message still readable: %d %s", status, body)
				}
			} else if delivery != "delivered" || state != "handed_off" || len(notices) != 0 || !acked {
				t.Fatalf("targeted=%t confirm: delivery=%s receipt=%s notices=%d acked=%t", targeted, delivery, state, len(notices), acked)
			}
		}
	}
}
