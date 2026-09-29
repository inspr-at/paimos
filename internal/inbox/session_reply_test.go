// SPDX-License-Identifier: AGPL-3.0-only

package inbox

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
)

func TestTwoSessionsAndReplyAfterAck(t *testing.T) {
	w, m, project, srv := messagingWorld(t)
	first := messageTestSession(t, w, project, w.agent, "First generation")
	second := messageTestSession(t, w, project, w.agent, "Second generation")
	path := "/api/projects/" + project + "/messages"
	toFirst := compatInput("codex:worker", "first-session")
	toFirst.RecipientSessionID = &first
	toFirst.ExpectsReply = true
	firstMsg := mustCompatSend(t, m, w.sender, project, toFirst)
	toSecond := compatInput("codex:worker", "second-session")
	toSecond.RecipientSessionID = &second
	secondMsg := mustCompatSend(t, m, w.sender, project, toSecond)
	for _, tc := range []struct {
		session, want string
	}{{first, firstMsg.ID}, {second, secondMsg.ID}} {
		status, body := do(t, srv, w.agent.ID, "GET", "/api/inbox/messages?wait_ms=0&exact_session=true&session="+tc.session, "", nil)
		page := mustJSON[Page](t, body)
		if status != 200 || len(page.Items) != 1 || page.Items[0].ID != tc.want || page.Items[0].RecipientSessionID == nil || *page.Items[0].RecipientSessionID != tc.session {
			t.Fatalf("session %s saw %d %s", tc.session, status, body)
		}
	}
	status, body := do(t, srv, w.agent.ID, "POST", "/api/inbox/messages/"+firstMsg.ID+"/ack", "", nil)
	if status != 200 {
		t.Fatalf("ack %d %s", status, body)
	}
	token := insertKey(t, w.db, w.agent, []string{"inbox.send"}, "reply-after-ack")
	reply := compatInput(w.sender.ID, "reply-after-ack")
	reply.ReplyTo = &firstMsg.ID
	raw, err := json.Marshal(reply)
	if err != nil {
		t.Fatal(err)
	}
	status, body = do(t, srv, w.agent.ID, "POST", path, string(raw), headerAuth(token))
	if status != 201 || bytes.Contains(body, []byte("sender_session_id")) || bytes.Contains(body, []byte("recipient_session_id")) {
		t.Fatalf("reply after ack %d %s", status, body)
	}
	sent := mustJSON[CompatMessage](t, body)
	if sent.SenderSessionID != nil || sent.RecipientSessionID != nil || sent.ReplyTo == nil || *sent.ReplyTo != firstMsg.ID {
		t.Fatalf("reply stored an inferred session: %+v", sent)
	}
	status, again := do(t, srv, w.agent.ID, "POST", path, string(raw), headerAuth(token))
	if status != 201 || mustJSON[CompatMessage](t, again).ID != sent.ID {
		t.Fatalf("reply replay %d %s", status, again)
	}
	err = db.InTenant(dbtest.Seed(t.Context()), w.db.App, w.agent.TenantID, func(tx pgx.Tx) error {
		var closed, unbound bool
		if err := tx.QueryRow(t.Context(), `SELECT closed_at IS NOT NULL FROM inbox_reply_obligations WHERE message_id=$1::uuid`, firstMsg.ID).Scan(&closed); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT sender_session_id IS NULL AND recipient_session_id IS NULL FROM inbox_compat_messages WHERE id=$1::uuid`, sent.ID).Scan(&unbound); err != nil {
			return err
		}
		if !closed || !unbound {
			return fmt.Errorf("closed=%v unbound=%v", closed, unbound)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	wrong := compatInput(w.sender.ID, "wrong-generation-after-ack")
	wrong.SenderSessionID = &second
	wrong.ReplyTo = &firstMsg.ID
	if _, err = m.commitMessage(t.Context(), w.agent, project, wrong); err != errNotFound {
		t.Fatalf("wrong generation reply: %v", err)
	}
	other := compatInput(w.sender.ID, "third-party")
	other.ReplyTo = &firstMsg.ID
	if _, err = m.commitMessage(t.Context(), w.other, project, other); err != errNotFound {
		t.Fatalf("third party reply: %v", err)
	}
	matched := compatInput(w.sender.ID, "explicit-match")
	matched.SenderSessionID = &first
	matched.ReplyTo = &firstMsg.ID
	if out, err := m.commitMessage(t.Context(), w.agent, project, matched); err != nil || out.SenderSessionID == nil || *out.SenderSessionID != first {
		t.Fatalf("explicit match %v %+v", err, out)
	}
}
