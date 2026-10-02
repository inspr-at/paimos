// SPDX-License-Identifier: AGPL-3.0-only
package questions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/deskdelivery"
	"github.com/inspr-at/paimos/internal/inbox"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type deliveryFixture struct {
	*fixture
	now atomic.Int64
}

func deliveryFixtureFor(t *testing.T) *deliveryFixture {
	f := &deliveryFixture{fixture: newFixture(t)}
	var now time.Time
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT clock_timestamp()`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	f.now.Store(now.UnixNano())
	f.m.clock = func(context.Context, pgx.Tx) (time.Time, error) { return time.Unix(0, f.now.Load()).UTC(), nil }
	messaging, err := inbox.NewMessaging(f.d.App, bytes.Repeat([]byte{1}, 32), inbox.WithHeldReplyBridge(f.m.ReplyHeld))
	if err != nil {
		t.Fatal(err)
	}
	messaging.Mount(f.mux)
	inbox.New(f.d.App).Mount(f.mux)
	return f
}
func (f *deliveryFixture) advance(d time.Duration) { f.now.Add(int64(d)) }
func (f *deliveryFixture) answer(t *testing.T, q Question, text string) Question {
	t.Helper()
	return question(t, request(t.Context(), f.mux, f.person, "POST", "/api/questions/"+q.ID+"/decision", DecisionInput{RequestID: uid(), ExpectedRevision: q.Revision, Answer: text, Outcome: "once"}), 200)
}
func (f *deliveryFixture) status(t *testing.T, id string) Question {
	t.Helper()
	return question(t, request(t.Context(), f.mux, f.person, "GET", "/api/questions/"+id+"/status", nil), 200)
}
func (f *deliveryFixture) dispatch(t *testing.T) {
	t.Helper()
	if _, err := f.m.DispatchTenant(t.Context(), f.person.TenantID); err != nil {
		t.Fatal(err)
	}
}
func (f *deliveryFixture) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := f.d.Admin.QueryRow(t.Context(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func (f *deliveryFixture) expectEffects(t *testing.T, q Question, inboxState string) {
	t.Helper()
	current := f.status(t, q.ID)
	for _, e := range current.Pending {
		if e.Kind == "outcome" {
			continue
		}
		want := "delivered"
		if e.Kind == "inbox" {
			want = inboxState
		}
		if e.State != want {
			t.Fatalf("%s effect=%+v want %s", e.Kind, e, want)
		}
		if e.Kind == "inbox" && e.State == "delivered" && e.ReceiptState != "queued" {
			t.Fatalf("queued inbox mislabeled %+v", e)
		}
	}
}
func TestDeliveryGraceEditRestartAndCorrections(t *testing.T) {
	f := deliveryFixtureFor(t)
	in := input()
	in.SessionID = f.session
	in.TicketID = f.ticket
	q := f.answer(t, f.ask(t, in), "first")
	f.advance(9 * time.Second)
	f.dispatch(t)
	if n := f.count(t, `SELECT count(*) FROM inbox_messages`); n != 0 {
		t.Fatal("early dispatch")
	}
	q = f.answer(t, q, "second")
	f.advance(9 * time.Second)
	q = f.answer(t, q, "third")
	// A fresh module shares no state other than the durable database and clock.
	oldClock := f.m.clock
	f.m = New(f.d.App)
	f.m.clock = oldClock
	f.advance(9 * time.Second)
	f.dispatch(t)
	if n := f.count(t, `SELECT count(*) FROM inbox_messages`); n != 0 {
		t.Fatal("edit did not restart delay")
	}
	f.advance(time.Second)
	f.dispatch(t)
	f.expectEffects(t, q, "delivered")
	if n := f.count(t, `SELECT count(*) FROM inbox_messages WHERE body::jsonb->>'answer'='third' AND recipient_session_id=$1`, f.session); n != 1 {
		t.Fatal("latest answer not sent to exact session")
	}
	if n := f.count(t, `SELECT count(*) FROM events WHERE type='comment.created' AND node_id=$1 AND after->>'answer_id'=$2`, f.ticket, q.Answer.ID); n != 1 {
		t.Fatal("ticket comment missing")
	}
	if n := f.count(t, `SELECT count(*) FROM inbox_compat_messages c WHERE c.is_action_request AND NOT EXISTS(SELECT 1 FROM events e WHERE e.tenant_id=c.tenant_id AND e.node_id=c.project_id AND e.type='inbox.action_resolved' AND e.after->>'message_id'=c.id::text)`); n != 0 {
		t.Fatal("synthetic reply root became an unanswered action request")
	}

	f.dispatch(t)
	if n := f.count(t, `SELECT count(*) FROM inbox_messages`); n != 1 {
		t.Fatal("retry duplicated inbox")
	}
	sent := q
	q = f.answer(t, q, "correction")
	if q.Answer.Replaces != sent.Answer.ID {
		t.Fatalf("correction lineage %+v", q.Answer)
	}
	f.advance(9 * time.Second)
	q = f.answer(t, q, "correction edited")
	if q.Answer.Replaces != sent.Answer.ID {
		t.Fatal("undispatched revision used as replacement target")
	}
	f.advance(10 * time.Second)
	f.dispatch(t)
	f.expectEffects(t, q, "delivered")
	if n := f.count(t, `SELECT count(*) FROM inbox_messages WHERE body::jsonb->>'type'='question.correction' AND body::jsonb->>'replaces'=$1 AND body::jsonb->>'answer'='correction edited'`, sent.Answer.ID); n != 1 {
		t.Fatal("typed correction missing")
	}
	next := f.answer(t, q, "another correction")
	if next.Answer.Replaces != q.Answer.ID {
		t.Fatal("repeated correction lost lineage")
	}
	if n := f.count(t, `SELECT count(*) FROM desk_decisions WHERE question_id=$1 AND state='active'`, q.ID); n != 0 {
		t.Fatal("P3 published Always")
	}
}
func TestDeliveryConcurrentEditAndReplicaClaims(t *testing.T) {
	f := deliveryFixtureFor(t)
	in := input()
	in.SessionID = f.session
	q := f.answer(t, f.ask(t, in), "old")
	f.advance(10 * time.Second)
	start := make(chan struct{})
	errs := make(chan error, 2)
	result := make(chan *httptest.ResponseRecorder, 1)
	for range 2 {
		go func() { <-start; _, err := f.m.DispatchTenant(t.Context(), f.person.TenantID); errs <- err }()
	}
	go func() {
		<-start
		result <- request(t.Context(), f.mux, f.person, "POST", "/api/questions/"+q.ID+"/decision", DecisionInput{RequestID: uid(), ExpectedRevision: q.Revision, Answer: "new", Outcome: "once"})
	}()
	close(start)
	updated := question(t, <-result, 200)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	oldDeliveries := f.count(t, `SELECT count(*) FROM inbox_messages WHERE body::jsonb->>'answer_id'=$1`, q.Answer.ID)
	if oldDeliveries > 1 {
		t.Fatal("replicas duplicated old answer")
	}
	if oldDeliveries == 1 && updated.Answer.Replaces != q.Answer.ID {
		t.Fatal("dispatch escaped correction serialization")
	}
	f.advance(10 * time.Second)
	var wg sync.WaitGroup
	errs = make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := f.m.DispatchTenant(t.Context(), f.person.TenantID); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := f.count(t, `SELECT count(*) FROM inbox_messages WHERE body::jsonb->>'answer_id'=$1`, updated.Answer.ID); n != 1 {
		t.Fatal("replicas duplicated latest answer")
	}
}
func TestDeliveryFanoutFailureAndReceiptVisibility(t *testing.T) {
	f := deliveryFixtureFor(t)
	in := input()
	in.SessionID = f.session
	q := f.ask(t, in)
	err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
		if err := writeCapability(t.Context(), tx); err != nil {
			return err
		}
		raw, _ := json.Marshal(input())
		_, err := tx.Exec(t.Context(), `INSERT INTO desk_askers(tenant_id,project_id,question_id,principal_id,request_id,request_digest,session_id,comment_node_id,input) VALUES($1,$2,$3,$4,$5,'second',$6,$3,$7)`, f.person.TenantID, f.project, q.ID, f.otherAgent.ID, uid(), f.otherSession, raw)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	q = f.answer(t, q, "both askers")
	if _, err = f.d.Admin.Exec(t.Context(), `UPDATE harness_sessions SET phase='stopped',stopped_at=clock_timestamp() WHERE id=$1`, f.otherSession); err != nil {
		t.Fatal(err)
	}
	f.advance(10 * time.Second)
	f.dispatch(t)
	if n := f.count(t, `SELECT count(*) FROM events WHERE type='comment.created' AND after->>'question_id'=$1`, q.ID); n != 2 {
		t.Fatal("partial inbox failure lost a comment")
	}
	if n := f.count(t, `SELECT count(*) FROM inbox_messages WHERE recipient_session_id=$1`, f.session); n != 1 {
		t.Fatal("healthy asker not served")
	}
	if n := f.count(t, `SELECT count(*) FROM inbox_messages WHERE recipient_session_id=$1`, f.otherSession); n != 0 {
		t.Fatal("ended process received answer")
	}
	var snapshots []deskdelivery.Answer
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT desk_answers FROM harness_sessions WHERE id=$1`, f.otherSession).Scan(&snapshots); err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 1 || snapshots[0].AnswerID != q.Answer.ID {
		t.Fatal("pending handover lost answer")
	}
	var delivered Pending
	for _, e := range f.status(t, q.ID).Pending {
		if e.Kind == "inbox" && e.State == "delivered" {
			delivered = e
		}
	}
	if delivered.ReceiptState != "queued" {
		t.Fatal("queue claimed receiver confirmation")
	}
	if _, err = f.d.Admin.Exec(t.Context(), `UPDATE inbox_receipts SET state='failed',failure_reason='deadline' WHERE message_id::text=$1`, delivered.EffectRef); err != nil {
		t.Fatal(err)
	}
	for _, e := range f.status(t, q.ID).Pending {
		if e.ID == delivered.ID && (e.ReceiptState != "failed" || e.ReceiptFailure != "deadline") {
			t.Fatal("failed receipt hidden")
		}
	}
}
func TestDeliveryPausedSuccessorAndCorrection(t *testing.T) {
	f := deliveryFixtureFor(t)
	in := input()
	in.SessionID = f.session
	q := f.answer(t, f.ask(t, in), "before successor")
	if _, err := f.d.Admin.Exec(t.Context(), `UPDATE harness_sessions SET phase='stopped',stopped_at=clock_timestamp() WHERE id=$1`, f.session); err != nil {
		t.Fatal(err)
	}
	f.advance(10 * time.Second)
	f.dispatch(t)
	f.expectEffects(t, q, "failed")
	if n := f.count(t, `SELECT count(*) FROM inbox_messages`); n != 0 {
		t.Fatal("missing successor became a broadcast")
	}
	q = f.answer(t, q, "latest for successor")
	f.advance(10 * time.Second)
	f.dispatch(t)
	var successor string
	if err := f.d.Admin.QueryRow(t.Context(), `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,harness,host,management,role,work_shape,ref_digest,lease_digest,continuation_handover) VALUES($1,$2,$3,'codex','test','unmanaged','worker','unknown',$4,$5,jsonb_build_object('succeeds_session_id',$6::text)) RETURNING id::text`, f.person.TenantID, f.project, f.agent.ID, []byte(uid()), []byte(uid()), f.session).Scan(&successor); err != nil {
		t.Fatal(err)
	}
	if _, err := f.d.Admin.Exec(t.Context(), `UPDATE harness_sessions SET handed_over_to_id=$2 WHERE id=$1`, f.session, successor); err != nil {
		t.Fatal(err)
	}
	f.advance(30 * time.Second)
	f.dispatch(t)
	f.expectEffects(t, q, "delivered")
	if n := f.count(t, `SELECT count(*) FROM inbox_messages WHERE recipient_session_id=$1 AND body::jsonb->>'answer'='latest for successor'`, successor); n != 1 {
		t.Fatal("verified successor did not receive latest")
	}
	if n := f.count(t, `SELECT count(*) FROM inbox_messages WHERE recipient_session_id=$1`, f.session); n != 0 {
		t.Fatal("old process resurrected")
	}
}
func (f *deliveryFixture) held(t *testing.T) string {
	t.Helper()
	var id string
	err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
		var ev int64
		if err := tx.QueryRow(t.Context(), `INSERT INTO events(tenant_id,actor_principal_id,node_id,type,after) VALUES($1,$2,$3,'inbox.compat_sent','{}') RETURNING id`, f.person.TenantID, f.agent.ID, f.project).Scan(&ev); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO inbox_compat_messages(tenant_id,project_id,sender_principal_id,recipient_principal_id,recipient_address,body,key_digest,request_digest,thread_id,hop,sent_event_id,is_action_request,expects_reply,delivery_level,sender_session_id) VALUES($1,$2,$3,$4,'paimos:owner','held action','held-key','held-digest','held-thread',1,$5,true,true,'simple',$6) RETURNING id::text`, f.person.TenantID, f.project, f.agent.ID, f.person.ID, ev, f.session).Scan(&id)
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.d.Admin.Exec(t.Context(), `INSERT INTO inbox_reply_obligations(tenant_id,message_id) VALUES($1,$2)`, f.person.TenantID, id); err != nil {
		t.Fatal(err)
	}

	return id
}
func TestHeldPersonReplyBridgeAndReplay(t *testing.T) {
	f := deliveryFixtureFor(t)
	parent := f.held(t)
	body := map[string]any{"to": f.agent.ID, "body": "please proceed with the answer", "idempotency_key": "reply-key", "reply_to": parent}
	path := "/api/projects/" + f.project + "/messages"
	other := tenant.Principal{TenantID: f.person.TenantID, Kind: tenant.Person, Name: "other person"}
	if err := f.d.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person',$2) RETURNING id::text`, other.TenantID, other.Name).Scan(&other.ID); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, f.d, other.TenantID, other.ID, "owner")
	if w := request(t.Context(), f.mux, other, "POST", path, body); w.Code != 404 {
		t.Fatalf("other person: %d", w.Code)
	}
	w := request(t.Context(), f.mux, f.person, "POST", path, body)
	if w.Code != 201 {
		t.Fatalf("held reply %d %s", w.Code, w.Body.String())
	}
	var sent inbox.CompatMessage
	if err := json.Unmarshal(w.Body.Bytes(), &sent); err != nil {
		t.Fatal(err)
	}
	if sent.Status != "pending" || sent.QuestionID == "" {
		t.Fatal("reply pretended to deliver")
	}
	if n := f.count(t, `SELECT count(*) FROM events WHERE type='inbox.action_resolved' AND after->>'message_id'=$1`, parent); n != 1 {
		t.Fatal("durable outbox did not settle parent")
	}
	if n := f.count(t, `SELECT count(*) FROM inbox_messages`); n != 0 {
		t.Fatal("held reply ignored grace")
	}
	w = request(t.Context(), f.mux, f.person, "POST", path, body)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	body["body"] = "different"
	if w = request(t.Context(), f.mux, f.person, "POST", path, body); w.Code != 409 {
		t.Fatal("reply retry conflict accepted")
	}
	f.advance(10 * time.Second)
	f.dispatch(t)
	if n := f.count(t, `SELECT count(*) FROM inbox_compat_messages WHERE reply_to_id=$1 AND NOT is_action_request`, parent); n != 1 {
		t.Fatal("held reply not correlated")
	}
	if n := f.count(t, `SELECT count(*) FROM inbox_compat_messages WHERE id=$1 AND is_action_request AND inbox_message_id IS NULL`, parent); n != 1 {
		t.Fatal("parent released")
	}
	if n := f.count(t, `SELECT count(*) FROM inbox_reply_obligations WHERE message_id=$1 AND reply_message_id IS NOT NULL AND closed_at IS NOT NULL`, parent); n != 1 {
		t.Fatal("correlated reply left obligation open")
	}

	f.expectEffects(t, f.status(t, sent.QuestionID), "delivered")
}
func TestDeliveryRevocationStopsOnlyUnauthorizedEffect(t *testing.T) {
	f := deliveryFixtureFor(t)
	in := input()
	in.SessionID = f.session
	q := f.answer(t, f.ask(t, in), "private answer")
	if _, err := f.d.Admin.Exec(t.Context(), `DELETE FROM role_bindings WHERE principal_id=$1`, f.agent.ID); err != nil {
		t.Fatal(err)
	}
	f.advance(10 * time.Second)
	f.dispatch(t)
	if n := f.count(t, `SELECT count(*) FROM inbox_messages`); n != 0 {
		t.Fatal("revoked asker received answer")
	}
	for _, e := range f.status(t, q.ID).Pending {
		if e.Kind == "inbox" && e.ErrorCode != "asker_access_lost" {
			t.Fatal(fmt.Sprintf("unexpected effect %+v", e))
		}
	}
}

func TestDeliveryQueuedBeforeGenerationEndsRoutesLatest(t *testing.T) {
	f := deliveryFixtureFor(t)
	in := input()
	in.SessionID = f.session
	q := f.answer(t, f.ask(t, in), "queued before stop")
	f.advance(10 * time.Second)
	f.dispatch(t)
	f.expectEffects(t, q, "delivered")
	if _, err := f.d.Admin.Exec(t.Context(), `UPDATE harness_sessions SET phase='stopped',stopped_at=clock_timestamp() WHERE id=$1`, f.session); err != nil {
		t.Fatal(err)
	}
	// AEON-279's closeGeneration failure transition is the routing signal.
	if _, err := f.d.Admin.Exec(t.Context(), `UPDATE inbox_receipts SET state='failed',failure_reason='session_ended' WHERE message_id IN (SELECT id FROM inbox_messages WHERE recipient_session_id=$1)`, f.session); err != nil {
		t.Fatal(err)
	}
	f.dispatch(t)
	current := f.status(t, q.ID)
	for _, e := range current.Pending {
		if e.Kind == "inbox" && (e.State != "failed" || e.ErrorCode != "successor_pending") {
			t.Fatalf("queued-to-ended route: %+v", e)
		}
	}
	if n := f.count(t, `SELECT jsonb_array_length(desk_answers) FROM harness_sessions WHERE id=$1`, f.session); n != 1 {
		t.Fatal("ended queued answer missing in handover")
	}
}

func TestHeldReplyAtomicFailureAndKeyRefusal(t *testing.T) {
	f := deliveryFixtureFor(t)
	parent := f.held(t)
	body := map[string]any{"to": f.agent.ID, "body": "answer", "idempotency_key": "reply-key", "reply_to": parent}
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", "/api/projects/"+f.project+"/messages", bytes.NewReader(raw)).WithContext(tenant.WithPrincipal(t.Context(), f.person))
	r.Header.Set("Authorization", "Bearer fixture-credential-not-a-real-key")
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatalf("key-backed context saw held parent: %d", w.Code)
	}
	if _, err := f.d.Admin.Exec(t.Context(), `CREATE FUNCTION reject_desk_answer_test() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.type='question.answered' THEN RAISE EXCEPTION 'fixture rejection'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_desk_answer_test BEFORE INSERT ON events FOR EACH ROW EXECUTE FUNCTION reject_desk_answer_test()`); err != nil {
		t.Fatal(err)
	}
	w = request(t.Context(), f.mux, f.person, "POST", "/api/projects/"+f.project+"/messages", body)
	if w.Code != 500 {
		t.Fatalf("failed answer returned %d", w.Code)
	}
	if n := f.count(t, `SELECT count(*) FROM desk_answers`); n != 0 {
		t.Fatal("answer survived failed commit")
	}
	if n := f.count(t, `SELECT count(*) FROM desk_pending`); n != 0 {
		t.Fatal("outbox survived failed commit")
	}
	if n := f.count(t, `SELECT count(*) FROM events WHERE type='inbox.action_resolved'`); n != 0 {
		t.Fatal("parent settled without durable answer")
	}
}

func TestHeldReplyUsesExistingQuestionSourceAndKeepsRetryRevision(t *testing.T) {
	f := deliveryFixtureFor(t)
	parent := f.held(t)
	in := input()
	in.SessionID = f.session
	in.SourceRequestID = parent
	q := f.ask(t, in)
	path := "/api/projects/" + f.project + "/messages"
	body := map[string]any{"to": f.agent.ID, "body": "original reply", "idempotency_key": "original-key", "reply_to": parent}
	w := request(t.Context(), f.mux, f.person, "POST", path, body)
	if w.Code != 201 {
		t.Fatalf("existing source: %d %s", w.Code, w.Body.String())
	}
	var first inbox.CompatMessage
	if err := json.Unmarshal(w.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if first.QuestionID != q.ID {
		t.Fatal("source made duplicate question")
	}
	latest := f.answer(t, f.status(t, q.ID), "edited through desk")
	w = request(t.Context(), f.mux, f.person, "POST", path, body)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	var replay inbox.CompatMessage
	if err := json.Unmarshal(w.Body.Bytes(), &replay); err != nil {
		t.Fatal(err)
	}
	if replay.ID != first.ID || replay.AnswerRevision != first.AnswerRevision {
		t.Fatal("old retry claimed a newer revision")
	}
	f.advance(10 * time.Second)
	f.dispatch(t)
	f.expectEffects(t, latest, "delivered")
	if n := f.count(t, `SELECT count(*) FROM inbox_compat_messages WHERE reply_to_id=$1 AND NOT is_action_request AND body::jsonb->>'answer'='edited through desk'`, parent); n != 1 {
		t.Fatal("source reply correlation lost")
	}
}
