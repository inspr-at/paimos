// SPDX-License-Identifier: AGPL-3.0-only

package inbox

import (
	"bytes"
	"encoding/json"
	"fmt"
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
