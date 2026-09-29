// SPDX-License-Identifier: AGPL-3.0-only

package inbox

import (
	"bufio"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

// AEON-280 fixtures read through the admin pool: assertions, not app paths.
func guaranteeRow(t *testing.T, w *world, query string, args []any, dst ...any) {
	t.Helper()
	if err := w.db.Admin.QueryRow(t.Context(), query, args...).Scan(dst...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

func expireDeadline(t *testing.T, w *world, messageID string) {
	t.Helper()
	if _, err := w.db.Admin.Exec(t.Context(), `UPDATE inbox_receipts SET deliver_by=now()-interval '1 second' WHERE message_id=$1::uuid`, messageID); err != nil {
		t.Fatal(err)
	}
}

func receiptState(t *testing.T, w *world, messageID string) (state, reason string) {
	t.Helper()
	guaranteeRow(t, w, `SELECT state,failure_reason FROM inbox_receipts WHERE message_id=$1::uuid`, []any{messageID}, &state, &reason)
	return
}

type notice struct {
	recipient, body string
	session         *string
}

func noticesFor(t *testing.T, w *world, messageID string) map[string]notice {
	t.Helper()
	rows, err := w.db.Admin.Query(t.Context(), `SELECT m.idempotency_key,m.recipient_principal_id::text,m.body,m.recipient_session_id::text FROM inbox_messages m JOIN principals s ON s.tenant_id=m.tenant_id AND s.id=m.sender_principal_id
 WHERE s.name='System' AND 'system'=ANY(s.roles) AND m.idempotency_key LIKE 'delivery-failed/'||$1||'%'`, messageID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]notice{}
	for rows.Next() {
		var key string
		var n notice
		if err := rows.Scan(&key, &n.recipient, &n.body, &n.session); err != nil {
			t.Fatal(err)
		}
		out[strings.TrimPrefix(key, "delivery-failed/"+messageID)] = n
	}
	return out
}

func messageStatus(t *testing.T, w *world, srvDo func(principal, path string) (int, []byte), principalID, messageID string) *MessageStatus {
	t.Helper()
	status, body := srvDo(principalID, "/api/inbox/message-status?ids="+messageID)
	if status != 200 {
		t.Fatalf("message status %d %s", status, body)
	}
	page := mustJSON[struct {
		Items []MessageStatus `json:"items"`
	}](t, body)
	if len(page.Items) == 0 {
		return nil
	}
	return &page.Items[0]
}

func sweepNow(t *testing.T, w *world, tenantID string) int {
	t.Helper()
	n, err := NewSweeper(w.db.App).SweepTenant(t.Context(), tenantID)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSessionDeadlineDeliveredThenFailedLoudly(t *testing.T) {
	w, m, project, srv := messagingWorld(t)
	get := func(principal, path string) (int, []byte) { return do(t, srv, principal, "GET", path, "", nil) }
	session := messageTestSession(t, w, project, w.agent, "Deadline worker")
	in := compatInput("codex:worker", "bound")
	in.RecipientSessionID = &session
	msg := mustCompatSend(t, m, w.sender, project, in)

	var seconds float64
	guaranteeRow(t, w, `SELECT extract(epoch FROM r.deliver_by-m.created_at) FROM inbox_receipts r JOIN inbox_messages m ON m.id=r.message_id WHERE r.message_id=$1::uuid`, []any{msg.ID}, &seconds)
	if seconds != 300 {
		t.Fatalf("session deadline %v s, want the 5 minute default", seconds)
	}
	if s := messageStatus(t, w, get, w.sender.ID, msg.ID); s == nil || s.Status != "sent" || s.DeliverBy == nil {
		t.Fatalf("accepted status %+v", s)
	}
	// Another principal sees nothing, not an error.
	if s := messageStatus(t, w, get, w.agent.ID, msg.ID); s != nil {
		t.Fatalf("recipient read the sender status %+v", s)
	}

	status, body := get(w.agent.ID, "/api/inbox/messages?wait_ms=0&session="+session)
	if status != 200 || len(mustJSON[Page](t, body).Items) != 1 {
		t.Fatalf("session pull %d %s", status, body)
	}
	var via string
	var seen *time.Time
	guaranteeRow(t, w, `SELECT inbox_seen_via,inbox_seen_at FROM harness_sessions WHERE id=$1::uuid`, []any{session}, &via, &seen)
	if via != SeenHook || seen == nil {
		t.Fatalf("pull not recorded as listening: %q %v", via, seen)
	}
	if s := messageStatus(t, w, get, w.sender.ID, msg.ID); s == nil || s.Status != "delivered" || s.DeliveredAt == nil {
		t.Fatalf("pulled status %+v", s)
	}
	// A second pull is not a second hand-over.
	get(w.agent.ID, "/api/inbox/messages?wait_ms=0&session="+session)
	if n := countSQL(t, w.db.Admin, w.sender.TenantID, `SELECT count(*) FROM events WHERE type='inbox.message_fetched' AND after->>'message_id'=$1`, msg.ID); n != 1 {
		t.Fatalf("fetched events %d", n)
	}

	if n := sweepNow(t, w, w.sender.TenantID); n != 0 {
		t.Fatalf("swept %d before the deadline", n)
	}
	expireDeadline(t, w, msg.ID)
	if n := sweepNow(t, w, w.sender.TenantID); n != 1 {
		t.Fatalf("swept %d at the deadline", n)
	}
	if state, reason := receiptState(t, w, msg.ID); state != "failed" || reason != ReasonDeadline {
		t.Fatalf("receipt %s %s", state, reason)
	}
	var delivery, deliveryReason string
	guaranteeRow(t, w, `SELECT state,reason FROM inbox_message_deliveries WHERE message_id=$1::uuid`, []any{msg.ID}, &delivery, &deliveryReason)
	if delivery != "dead" || deliveryReason != ReasonDeadline {
		t.Fatalf("delivery %s %s", delivery, deliveryReason)
	}
	// The message leaves every read path.
	status, body = get(w.agent.ID, "/api/inbox/messages?wait_ms=0&session="+session)
	if status != 200 || len(mustJSON[Page](t, body).Items) != 0 {
		t.Fatalf("failed message still readable: %s", body)
	}
	notices := noticesFor(t, w, msg.ID)
	sender, ok := notices[""]
	if len(notices) != 1 || !ok || sender.recipient != w.sender.ID || sender.session != nil ||
		!strings.Contains(sender.body, "Not delivered") || !strings.Contains(sender.body, "Deadline worker") || !strings.Contains(sender.body, msg.ID) {
		t.Fatalf("sender notice %+v", notices)
	}
	failedEvents := eventText(t, w.db.Admin, w.sender.TenantID, "inbox.delivery_failed")
	if !strings.Contains(failedEvents, msg.ID) || strings.Contains(failedEvents, in.Body) {
		t.Fatalf("delivery_failed event %s", failedEvents)
	}
	if s := messageStatus(t, w, get, w.sender.ID, msg.ID); s == nil || s.Status != "not_delivered" || s.Reason != ReasonDeadline {
		t.Fatalf("failed status %+v", s)
	}

	// Idempotent: a second sweep neither fails nor notifies again.
	if n := sweepNow(t, w, w.sender.TenantID); n != 0 || len(noticesFor(t, w, msg.ID)) != 1 {
		t.Fatalf("replayed sweep %d", n)
	}
	// A late acknowledgement is refused, without the body: the sender was told
	// "not delivered" and that stays true.
	status, body = do(t, srv, w.agent.ID, "POST", "/api/inbox/messages/"+msg.ID+"/ack", "", nil)
	if status != 409 || !strings.Contains(string(body), "not_delivered") || strings.Contains(string(body), in.Body) {
		t.Fatalf("late ack %d %s", status, body)
	}
	guaranteeRow(t, w, `SELECT state FROM inbox_message_deliveries WHERE message_id=$1::uuid`, []any{msg.ID}, &delivery)
	if state, _ := receiptState(t, w, msg.ID); state != "failed" || delivery != "dead" {
		t.Fatalf("late ack moved failure: %s %s", state, delivery)
	}
}

func TestUnpulledMessageFailsNoListenerAndPeopleHaveNoDeadline(t *testing.T) {
	w, m, project, srv := messagingWorld(t)
	session := messageTestSession(t, w, project, w.agent, "Quiet worker")
	in := compatInput("codex:worker", "never-pulled")
	in.RecipientSessionID = &session
	msg := mustCompatSend(t, m, w.sender, project, in)
	expireDeadline(t, w, msg.ID)
	if n := sweepNow(t, w, w.sender.TenantID); n != 1 {
		t.Fatalf("swept %d", n)
	}
	if state, reason := receiptState(t, w, msg.ID); state != "failed" || reason != ReasonNoListener {
		t.Fatalf("receipt %s %s", state, reason)
	}
	if n := noticesFor(t, w, msg.ID)[""]; !strings.Contains(n.body, "nothing was listening") {
		t.Fatalf("notice %+v", n)
	}
	// A message to a person has no delivery path to guarantee.
	status, body := do(t, srv, w.sender.ID, "POST", "/api/inbox/messages", sendJSON(w.recipient.ID, "to a person", "person", nil, nil), nil)
	if status != 201 {
		t.Fatalf("send to person %d %s", status, body)
	}
	var deadline *time.Time
	guaranteeRow(t, w, `SELECT deliver_by FROM inbox_receipts WHERE message_id=$1::uuid`, []any{mustJSON[Message](t, body).ID}, &deadline)
	if deadline != nil {
		t.Fatalf("person message deadline %v", deadline)
	}
	// An agent's plain principal message takes the unbound default.
	status, body = do(t, srv, w.sender.ID, "POST", "/api/inbox/messages", sendJSON(w.agent.ID, "to an agent", "agent", nil, nil), nil)
	if status != 201 {
		t.Fatalf("send to agent %d %s", status, body)
	}
	var seconds float64
	guaranteeRow(t, w, `SELECT extract(epoch FROM r.deliver_by-m.created_at) FROM inbox_receipts r JOIN inbox_messages m ON m.id=r.message_id WHERE r.message_id=$1::uuid`, []any{mustJSON[Message](t, body).ID}, &seconds)
	if seconds != 1800 {
		t.Fatalf("unbound deadline %v", seconds)
	}
}

func TestSessionEndFailsBoundMessagesAndCopiesCoordinator(t *testing.T) {
	w, m, project, _ := messagingWorld(t)
	lead := insertPrincipal(t, w.db, w.sender.TenantID, tenant.Agent, "lead", nil)
	parent := messageTestSession(t, w, project, lead, "Lead session")
	child := messageTestSession(t, w, project, w.agent, "Child worker")
	if _, err := w.db.Admin.Exec(t.Context(), `UPDATE harness_sessions SET parent_id=$2::uuid WHERE id=$1::uuid`, child, parent); err != nil {
		t.Fatal(err)
	}
	if _, err := w.db.Admin.Exec(t.Context(), `UPDATE harness_sessions SET role='coordinator' WHERE id=$1::uuid`, parent); err != nil {
		t.Fatal(err)
	}
	in := compatInput("codex:worker", "to-child")
	in.RecipientSessionID = &child
	msg := mustCompatSend(t, m, w.sender, project, in)
	in.Key = "to-child-acked"
	acked := mustCompatSend(t, m, w.sender, project, in)
	if _, err := m.base.ack(t.Context(), w.agent, acked.ID); err != nil {
		t.Fatal(err)
	}
	// The harness calls this in the transaction that stops the generation.
	err := db.InTenant(dbtest.Seed(t.Context()), w.db.App, w.sender.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET phase='stopped',stopped_at=now() WHERE id=$1::uuid`, child); err != nil {
			return err
		}
		return FailSessionMessages(t.Context(), tx, w.sender.TenantID, child)
	})
	if err != nil {
		t.Fatal(err)
	}
	if state, reason := receiptState(t, w, msg.ID); state != "failed" || reason != ReasonSessionEnded {
		t.Fatalf("receipt %s %s", state, reason)
	}
	if state, _ := receiptState(t, w, acked.ID); state != "handed_off" {
		t.Fatalf("acked message failed: %s", state)
	}
	notices := noticesFor(t, w, msg.ID)
	coordinator, ok := notices["/coordinator"]
	if len(notices) != 2 || notices[""].recipient != w.sender.ID || !ok || coordinator.recipient != lead.ID ||
		coordinator.session == nil || *coordinator.session != parent || !strings.Contains(coordinator.body, "Child worker") || !strings.Contains(coordinator.body, "Sender") ||
		strings.Contains(coordinator.body, in.Body) {
		t.Fatalf("notices %+v", notices)
	}
	// The coordinator's session pulls its copy like any bound message.
	page, err := m.base.page(t.Context(), lead, 0, 10, &parent, false, SeenHook)
	if err != nil || len(page.Items) != 1 || !strings.HasPrefix(page.Items[0].Body, "Not delivered to your worker") || page.Items[0].SenderLabel != "System" {
		t.Fatalf("coordinator pull %+v %v", page, err)
	}

	// A generation stopped outside the harness path is caught by the sweep.
	other := messageTestSession(t, w, project, w.agent, "Second child")
	in.Key = "to-second"
	in.RecipientSessionID = &other
	late := mustCompatSend(t, m, w.sender, project, in)
	if _, err := w.db.Admin.Exec(t.Context(), `UPDATE harness_sessions SET phase='stopped',stopped_at=now() WHERE id=$1::uuid`, other); err != nil {
		t.Fatal(err)
	}
	if n := sweepNow(t, w, w.sender.TenantID); n != 1 {
		t.Fatalf("swept %d", n)
	}
	if state, reason := receiptState(t, w, late.ID); state != "failed" || reason != ReasonSessionEnded {
		t.Fatalf("late receipt %s %s", state, reason)
	}
}

func TestUnboundReceiptsAckHealAndBlockedDeadline(t *testing.T) {
	w, m, project, srv := messagingWorld(t)
	blocked := mustCompatSend(t, m, w.sender, project, compatInput("codex:worker", "blocked"))
	if state, _ := receiptState(t, w, blocked.ID); state != "queued" {
		t.Fatalf("blocked message receipt %s at acceptance, want queued until its deadline", state)
	}
	status, body := do(t, srv, w.agent.ID, "POST", "/api/projects/"+project+"/messages/"+blocked.ID+"/ack", "", nil)
	if status != 200 {
		t.Fatalf("ack %d %s", status, body)
	}
	var delivery string
	guaranteeRow(t, w, `SELECT state FROM inbox_message_deliveries WHERE message_id=$1::uuid`, []any{blocked.ID}, &delivery)
	if state, _ := receiptState(t, w, blocked.ID); state != "handed_off" || delivery != "delivered" {
		t.Fatalf("acked unbound compat: receipt %s delivery %s", state, delivery)
	}

	// Acknowledged by a path that forgot to confirm: the sweep heals it.
	stale := mustCompatSend(t, m, w.sender, project, compatInput("codex:worker", "stale"))
	if _, err := w.db.Admin.Exec(t.Context(), `UPDATE inbox_messages SET acked_at=now(),acked_by_principal_id=recipient_principal_id WHERE id=$1::uuid`, stale.ID); err != nil {
		t.Fatal(err)
	}
	// Nobody picks up the third one; it fails at its deadline.
	lost := mustCompatSend(t, m, w.sender, project, compatInput("codex:worker", "lost"))
	expireDeadline(t, w, lost.ID)
	if n := sweepNow(t, w, w.sender.TenantID); n != 1 {
		t.Fatalf("swept %d", n)
	}
	if state, _ := receiptState(t, w, stale.ID); state != "handed_off" {
		t.Fatalf("stale receipt %s", state)
	}
	if state, reason := receiptState(t, w, lost.ID); state != "failed" || reason != ReasonNoListener {
		t.Fatalf("blocked receipt %s %s", state, reason)
	}
}

func TestAttemptCapAndSettings(t *testing.T) {
	w, m, project, srv := messagingWorld(t)
	put := func(body string) (int, []byte) {
		return do(t, srv, w.admin.ID, "PUT", "/api/settings/inbox-delivery", body, nil)
	}
	if status, _ := put(`{"session_deadline_seconds":10,"unbound_deadline_seconds":1800,"max_attempts":1}`); status != 400 {
		t.Fatalf("short deadline accepted %d", status)
	}
	if status, body := put(`{"session_deadline_seconds":120,"unbound_deadline_seconds":900,"max_attempts":1}`); status != 200 {
		t.Fatalf("settings %d %s", status, body)
	}
	status, body := do(t, srv, w.admin.ID, "GET", "/api/settings/inbox-delivery", "", nil)
	if got := mustJSON[DeliverySettings](t, body); status != 200 || got != (DeliverySettings{120, 900, 1}) {
		t.Fatalf("settings read %d %+v", status, got)
	}
	if _, err := m.storeTarget(t.Context(), w.admin, project, targetInput{Address: "codex:worker", Adapter: "codex", Kind: "codex_thread", Ref: "thread-fixture", Role: "primary", MaximumLevel: "simple"}); err != nil {
		t.Fatal(err)
	}
	claim := func() *DeliveryWork {
		t.Helper()
		work, err := m.claim(t.Context(), w.agent, project, claimInput{To: "codex:worker", Adapter: "codex"})
		if err != nil {
			t.Fatal(err)
		}
		return work
	}
	expireLease := func() {
		if _, err := w.db.Admin.Exec(t.Context(), `UPDATE inbox_message_deliveries SET lease_until=now()-interval '1 second' WHERE lease_until IS NOT NULL`); err != nil {
			t.Fatal(err)
		}
	}
	first := mustCompatSend(t, m, w.sender, project, compatInput("codex:worker", "first"))
	var seconds float64
	guaranteeRow(t, w, `SELECT extract(epoch FROM r.deliver_by-m.created_at) FROM inbox_receipts r JOIN inbox_messages m ON m.id=r.message_id WHERE r.message_id=$1::uuid`, []any{first.ID}, &seconds)
	if seconds != 900 {
		t.Fatalf("tenant deadline %v", seconds)
	}
	if work := claim(); work == nil || work.Message == nil || work.Message.ID != first.ID {
		t.Fatalf("first claim %+v", work)
	}
	expireLease()
	// The sweep fails a spent delivery without waiting for its deadline.
	if n := sweepNow(t, w, w.sender.TenantID); n != 1 {
		t.Fatalf("swept %d", n)
	}
	if state, reason := receiptState(t, w, first.ID); state != "failed" || reason != ReasonAttempts {
		t.Fatalf("receipt %s %s", state, reason)
	}
	second := mustCompatSend(t, m, w.sender, project, compatInput("codex:worker", "second"))
	third := mustCompatSend(t, m, w.sender, project, compatInput("codex:worker", "third"))
	if work := claim(); work == nil || work.Message == nil || work.Message.ID != second.ID {
		t.Fatalf("dead delivery blocked the queue: %+v", work)
	}
	expireLease()
	// The claim itself enforces the cap too.
	if work := claim(); work != nil {
		t.Fatalf("over-cap claim %+v", work)
	}
	if state, reason := receiptState(t, w, second.ID); state != "failed" || reason != ReasonAttempts {
		t.Fatalf("second receipt %s %s", state, reason)
	}
	if work := claim(); work == nil || work.Message == nil || work.Message.ID != third.ID {
		t.Fatalf("third claim %+v", work)
	}
	get := func(principal, path string) (int, []byte) { return do(t, srv, principal, "GET", path, "", nil) }
	if s := messageStatus(t, w, get, w.sender.ID, third.ID); s == nil || s.Status != "delivered" {
		t.Fatalf("claimed status %+v", s)
	}
}

func TestStreamCarriesSessionMessagesAndMarksListening(t *testing.T) {
	w, m, project, srv := messagingWorld(t)
	session := messageTestSession(t, w, project, w.agent, "Streaming worker")
	in := compatInput("codex:worker", "streamed")
	in.RecipientSessionID = &session
	msg := mustCompatSend(t, m, w.sender, project, in)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/inbox/stream?session="+session, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Principal", w.agent.ID)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("stream %d", resp.StatusCode)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 4096), 1<<20)
	if kind, _ := readFrame(t, sc); kind != "comment" {
		t.Fatalf("first frame %s", kind)
	}
	kind, data := readFrame(t, sc)
	if kind != "message" || !strings.Contains(data, msg.ID) || !strings.Contains(data, `"recipient_session_id":"`+session+`"`) {
		t.Fatalf("stream frame %s %s", kind, data)
	}
	var via string
	guaranteeRow(t, w, `SELECT inbox_seen_via FROM harness_sessions WHERE id=$1::uuid`, []any{session}, &via)
	if via != SeenStream {
		t.Fatalf("stream not recorded: %q", via)
	}
	cancel()
	// An ended generation cannot open a stream.
	if _, err := w.db.Admin.Exec(t.Context(), `UPDATE harness_sessions SET phase='stopped',stopped_at=now() WHERE id=$1::uuid`, session); err != nil {
		t.Fatal(err)
	}
	status, body := do(t, srv, w.agent.ID, "GET", "/api/inbox/stream?session="+session, "", nil)
	if status != 409 || !strings.Contains(string(body), "session_ended") {
		t.Fatalf("ended stream %d %s", status, body)
	}
}

func TestSweeperIsASingleRunner(t *testing.T) {
	w, m, project, _ := messagingWorld(t)
	session := messageTestSession(t, w, project, w.agent, "Locked worker")
	in := compatInput("codex:worker", "locked")
	in.RecipientSessionID = &session
	msg := mustCompatSend(t, m, w.sender, project, in)
	expireDeadline(t, w, msg.ID)
	holder, err := w.db.App.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Release()
	if _, err := holder.Exec(t.Context(), `SELECT pg_advisory_lock($1)`, sweeperLockKey); err != nil {
		t.Fatal(err)
	}
	sweeper := NewSweeper(w.db.App)
	if n, err := sweeper.SweepLocked(t.Context()); err != nil || n != 0 {
		t.Fatalf("second runner swept %d %v", n, err)
	}
	if _, err := holder.Exec(t.Context(), `SELECT pg_advisory_unlock($1)`, sweeperLockKey); err != nil {
		t.Fatal(err)
	}
	if n, err := sweeper.SweepLocked(t.Context()); err != nil || n != 1 {
		t.Fatalf("runner swept %d %v", n, err)
	}
}

func TestExactSessionLeavesOutBroadcasts(t *testing.T) {
	w, m, project, srv := messagingWorld(t)
	session := messageTestSession(t, w, project, w.agent, "Hook worker")
	for i := range 3 {
		mustCompatSend(t, m, w.sender, project, compatInput("codex:worker", "broadcast-"+string(rune('a'+i))))
	}
	in := compatInput("codex:worker", "bound")
	in.RecipientSessionID = &session
	bound := mustCompatSend(t, m, w.sender, project, in)
	base := "/api/inbox/messages?wait_ms=0&session=" + session
	status, body := do(t, srv, w.agent.ID, "GET", base, "", nil)
	if status != 200 || len(mustJSON[Page](t, body).Items) != 4 {
		t.Fatalf("default session pull changed: %d %s", status, body)
	}
	status, body = do(t, srv, w.agent.ID, "GET", base+"&exact_session=true", "", nil)
	page := mustJSON[Page](t, body)
	if status != 200 || len(page.Items) != 1 || page.Items[0].ID != bound.ID {
		t.Fatalf("exact pull %d %s", status, body)
	}
	for _, bad := range []string{"/api/inbox/messages?exact_session=true", "/api/inbox/messages?session=" + session + "&exact_session=maybe", "/api/inbox/stream?exact_session=1"} {
		if status, body := do(t, srv, w.agent.ID, "GET", bad, "", nil); status != 400 {
			t.Fatalf("%s: %d %s", bad, status, body)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/inbox/stream?exact_session=true&session="+session, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Principal", w.agent.ID)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 4096), 1<<20)
	if kind, _ := readFrame(t, sc); kind != "comment" {
		t.Fatalf("first frame %s", kind)
	}
	// The first message frame is the bound one: no broadcast precedes it.
	if kind, data := readFrame(t, sc); kind != "message" || !strings.Contains(data, bound.ID) {
		t.Fatalf("exact stream frame %s %s", kind, data)
	}
}

// AEON-307: the hook that delivers a message is the pull path. The acknowledgement
// milliseconds later must not make /agents say the session was seen via ack.
func TestHookPullWinsOverTheFollowingAck(t *testing.T) {
	w, m, project, srv := messagingWorld(t)
	session := messageTestSession(t, w, project, w.agent, "Hook listener")
	in := compatInput("codex:worker", "hook-then-ack")
	in.RecipientSessionID = &session
	msg := mustCompatSend(t, m, w.sender, project, in)

	pull := "/api/inbox/messages?wait_ms=0&exact_session=true&session=" + session
	status, body := do(t, srv, w.agent.ID, "GET", pull, "", nil)
	if status != 200 || len(mustJSON[Page](t, body).Items) != 1 {
		t.Fatalf("hook pull %d %s", status, body)
	}
	var via string
	var seen time.Time
	guaranteeRow(t, w, `SELECT inbox_seen_via,inbox_seen_at FROM harness_sessions WHERE id=$1::uuid`, []any{session}, &via, &seen)
	if via != SeenHook || seen.IsZero() {
		t.Fatalf("hook not recorded: %q %v", via, seen)
	}
	status, body = do(t, srv, w.agent.ID, "POST", "/api/inbox/messages/"+msg.ID+"/ack", "", nil)
	if status != 200 {
		t.Fatalf("ack %d %s", status, body)
	}
	var after string
	var seenAfter time.Time
	guaranteeRow(t, w, `SELECT inbox_seen_via,inbox_seen_at FROM harness_sessions WHERE id=$1::uuid`, []any{session}, &after, &seenAfter)
	if after != SeenHook || !seenAfter.Equal(seen) {
		t.Fatalf("ack replaced the hook pull on the same beat: %q %v (was %v)", after, seenAfter, seen)
	}

	// A later beat still refreshes the timestamp and keeps the pull path.
	if _, err := w.db.Admin.Exec(t.Context(), `UPDATE harness_sessions SET inbox_seen_at=clock_timestamp()-interval '6 seconds' WHERE id=$1::uuid`, session); err != nil {
		t.Fatal(err)
	}
	in = compatInput("codex:worker", "later-ack")
	in.RecipientSessionID = &session
	later := mustCompatSend(t, m, w.sender, project, in)
	status, body = do(t, srv, w.agent.ID, "POST", "/api/inbox/messages/"+later.ID+"/ack", "", nil)
	if status != 200 {
		t.Fatalf("later ack %d %s", status, body)
	}
	guaranteeRow(t, w, `SELECT inbox_seen_via,inbox_seen_at FROM harness_sessions WHERE id=$1::uuid`, []any{session}, &after, &seenAfter)
	if after != SeenHook || !seenAfter.After(seen) {
		t.Fatalf("later ack lost the pull path or the refresh: %q %v", after, seenAfter)
	}

	// With no pull, the acknowledgement itself is the listening signal.
	quiet := messageTestSession(t, w, project, w.agent, "Ack only")
	in = compatInput("codex:worker", "ack-only")
	in.RecipientSessionID = &quiet
	only := mustCompatSend(t, m, w.sender, project, in)
	status, body = do(t, srv, w.agent.ID, "POST", "/api/inbox/messages/"+only.ID+"/ack", "", nil)
	if status != 200 {
		t.Fatalf("ack-only %d %s", status, body)
	}
	guaranteeRow(t, w, `SELECT inbox_seen_via FROM harness_sessions WHERE id=$1::uuid`, []any{quiet}, &via)
	if via != SeenAck {
		t.Fatalf("ack without a pull: %q", via)
	}
	// A following pull replaces that acknowledgement.
	status, body = do(t, srv, w.agent.ID, "GET", "/api/inbox/messages?wait_ms=0&exact_session=true&session="+quiet, "", nil)
	if status != 200 {
		t.Fatalf("pull after ack %d %s", status, body)
	}
	guaranteeRow(t, w, `SELECT inbox_seen_via FROM harness_sessions WHERE id=$1::uuid`, []any{quiet}, &via)
	if via != SeenHook {
		t.Fatalf("pull did not replace ack: %q", via)
	}
}
