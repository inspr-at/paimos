// SPDX-License-Identifier: AGPL-3.0-only

package inbox

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
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

func TestReplyToHeldParentStaysInvisible(t *testing.T) {
	w, m, project, srv := messagingWorld(t)
	session := messageTestSession(t, w, project, w.agent, "Held generation")
	path := "/api/projects/" + project + "/messages"
	heldIn := compatInput("codex:worker", "held-parent")
	heldIn.RecipientSessionID = &session
	heldIn.ActionRequest = true
	heldIn.ExpectsReply = true
	held := mustCompatSend(t, m, w.sender, project, heldIn)
	status, body := do(t, srv, w.agent.ID, "GET", path+"/listen?to=codex:worker&session="+session, "", nil)
	if status != 200 || bytes.Contains(body, []byte(held.ID)) {
		t.Fatalf("held parent visible on read %d %s", status, body)
	}
	token := insertKey(t, w.db, w.agent, []string{"inbox.send"}, "reply-held")
	missing := "00000000-0000-4000-8000-0000000000aa"
	absent := compatInput(w.sender.ID, "reply-to-missing")
	absent.ReplyTo = &missing
	statusAbsent, absentBody := compatPostAuth(t, srv, w.agent, path, absent, token)
	for _, key := range []string{"reply-to-held", "reply-to-held-again"} {
		reply := compatInput(w.sender.ID, key)
		reply.ReplyTo = &held.ID
		status, hidden := compatPostAuth(t, srv, w.agent, path, reply, token)
		if status != 404 || statusAbsent != 404 || !bytes.Equal(hidden, absentBody) || bytes.Contains(hidden, []byte(held.ID)) || bytes.Contains(hidden, []byte("thread_id")) {
			t.Fatalf("omitted session %s: %d %s absent %d %s", key, status, hidden, statusAbsent, absentBody)
		}
	}
	matched := compatInput(w.sender.ID, "reply-to-held-matched")
	matched.SenderSessionID = &session
	matched.ReplyTo = &held.ID
	status, hidden := compatPostAuth(t, srv, w.agent, path, matched, token)
	if status != 404 || !bytes.Equal(hidden, absentBody) || bytes.Contains(hidden, []byte(held.ID)) || bytes.Contains(hidden, []byte("thread_id")) {
		t.Fatalf("matched generation %d %s", status, hidden)
	}
	err := db.InTenant(dbtest.Seed(t.Context()), w.db.App, w.agent.TenantID, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM inbox_compat_messages WHERE reply_to_id=$1::uuid`, held.ID).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			return fmt.Errorf("held parent gained %d replies", n)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	visibleIn := compatInput("codex:worker", "acked-parent")
	visibleIn.RecipientSessionID = &session
	visibleIn.ExpectsReply = true
	visible := mustCompatSend(t, m, w.sender, project, visibleIn)
	status, body = do(t, srv, w.agent.ID, "GET", "/api/inbox/messages?wait_ms=0&exact_session=true&session="+session, "", nil)
	if status != 200 || !bytes.Contains(body, []byte(visible.ID)) {
		t.Fatalf("acked parent not visible before ack %d %s", status, body)
	}
	status, body = do(t, srv, w.agent.ID, "POST", "/api/inbox/messages/"+visible.ID+"/ack", "", nil)
	if status != 200 {
		t.Fatalf("ack %d %s", status, body)
	}
	acked := compatInput(w.sender.ID, "reply-to-acked")
	acked.ReplyTo = &visible.ID
	status, body = compatPostAuth(t, srv, w.agent, path, acked, token)
	if status != 201 {
		t.Fatalf("acked reply %d %s", status, body)
	}
	sent := mustJSON[CompatMessage](t, body)
	if sent.ReplyTo == nil || *sent.ReplyTo != visible.ID || sent.ThreadID != visible.ThreadID || sent.Hop != visible.Hop+1 {
		t.Fatalf("acked reply metadata: %+v", sent)
	}
}

func compatPostAuth(t *testing.T, srv *httptest.Server, p tenant.Principal, path string, v any, token string) (int, []byte) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return do(t, srv, p.ID, "POST", path, string(b), headerAuth(token))
}

func TestSessionThreadIncludesBothDirectionsAndUnboundReplies(t *testing.T) {
	w, m, project, srv := messagingWorld(t)
	first := messageTestSession(t, w, project, w.agent, "First generation")
	second := messageTestSession(t, w, project, w.agent, "Second generation")
	question := compatInput(w.agent.ID, "question")
	question.RecipientSessionID = &first
	question.ExpectsReply = true
	asked := mustCompatSend(t, m, w.sender, project, question)
	reply := compatInput(w.sender.ID, "unbound-answer")
	reply.ReplyTo = &asked.ID
	answered := mustCompatSend(t, m, w.agent, project, reply)
	followup := compatInput(w.agent.ID, "unbound-followup")
	followup.ReplyTo = &answered.ID
	followed := mustCompatSend(t, m, w.sender, project, followup)
	bound := compatInput(w.sender.ID, "ordinary-answer")
	bound.SenderSessionID = &first
	ordinary := mustCompatSend(t, m, w.agent, project, bound)
	question.Key, question.RecipientSessionID = "other-question", &second
	otherQuestion := mustCompatSend(t, m, w.sender, project, question)
	reply.Key, reply.ReplyTo = "other-answer", &otherQuestion.ID
	otherAnswer := mustCompatSend(t, m, w.agent, project, reply)
	// Unrelated principal history and a new explicit generation replying to an
	// unbound descendant must not migrate into the first generation's thread.
	mustCompatSend(t, m, w.agent, project, compatInput(w.sender.ID, "unrelated"))
	bound.Key, bound.SenderSessionID, bound.ReplyTo = "new-generation", &second, &followed.ID
	newGeneration := mustCompatSend(t, m, w.agent, project, bound)
	base := "/api/projects/" + project + "/messages?session="
	for _, tc := range []struct {
		session string
		want    []string
	}{
		{first, []string{asked.ID, answered.ID, followed.ID, ordinary.ID}},
		{second, []string{otherQuestion.ID, otherAnswer.ID, newGeneration.ID}},
	} {
		status, body := do(t, srv, w.admin.ID, "GET", base+tc.session+"&limit=200", "", nil)
		page := mustJSON[compatPage](t, body)
		if status != 200 || len(page.Items) != len(tc.want) {
			t.Fatalf("thread %d %s", status, body)
		}
		for i, want := range tc.want {
			if page.Items[i].ID != want {
				t.Fatalf("item %d: got %s want %s", i, page.Items[i].ID, want)
			}
		}
	}
	// Pagination is applied after session membership, in both directions.
	status, body := do(t, srv, w.admin.ID, "GET", base+first+"&newest_first=true&limit=2", "", nil)
	page := mustJSON[compatPage](t, body)
	if status != 200 || len(page.Items) != 2 || page.Items[0].ID != ordinary.ID || page.Items[1].ID != followed.ID {
		t.Fatalf("newest %d %s", status, body)
	}
	status, body = do(t, srv, w.admin.ID, "GET", fmt.Sprintf("%s%s&newest_first=true&limit=2&after=%d", base, first, page.NextAfter), "", nil)
	page = mustJSON[compatPage](t, body)
	if status != 200 || len(page.Items) != 2 || page.Items[0].ID != answered.ID || page.Items[1].ID != asked.ID || page.Items[1].ReplyObligation != "closed" {
		t.Fatalf("older %d %s", status, body)
	}
	if status, _ := do(t, srv, w.outsider.ID, "GET", base+first, "", nil); status == 200 {
		t.Fatal("foreign tenant inspected the thread")
	}
}
