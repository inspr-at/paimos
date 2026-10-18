// SPDX-License-Identifier: AGPL-3.0-only
package inbox

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
)

// Risk: a missing listener discards a person's chat, competing readers inject
// it twice, or a stall is silent/duplicated. Real receipts and row-lock barriers
// prove the lifecycle; the injected sweeper clock proves the exact threshold.
func TestSessionChatQueuedPickupAndStall(t *testing.T) {
	for _, mode := range []string{"hook", "long poll", "project alias"} {
		t.Run(mode, func(t *testing.T) {
			w, m, project, srv := messagingWorld(t)
			session := messageTestSession(t, w, project, w.agent, "Quiet chat")
			var ids []string
			for i := range 3 {
				in := compatInput("codex:worker", fmt.Sprintf("chat-%d", i))
				in.Body, in.RecipientSessionID = fmt.Sprintf("Chat %d", i), &session
				msg := mustCompatSend(t, m, w.sender, project, in)
				if replay := mustCompatSend(t, m, w.sender, project, in); replay.ID != msg.ID {
					t.Fatal("send retry duplicated the message")
				}
				ids = append(ids, msg.ID)
				expireDeadline(t, w, msg.ID)
			}
			for range 2 {
				if n := sweepNow(t, w, w.sender.TenantID); n != 0 {
					t.Fatalf("missing listener failed %d chats", n)
				}
			}
			if n := countSQL(t, w.db.Admin, w.sender.TenantID, `SELECT count(*) FROM inbox_message_deliveries WHERE message_id=ANY($1::uuid[]) AND state='pending' AND attempts=0`, ids); n != 3 {
				t.Fatalf("no-listener queue=%d", n)
			}
			get := func(principal, path string) (int, []byte) { return do(t, srv, principal, "GET", path, "", nil) }
			if s := messageStatus(t, w, get, w.sender.ID, ids[0]); s == nil || s.Status != "sent" || !s.WaitingForPickup || s.PickupStalledAt != nil {
				t.Fatalf("waiting receipt=%+v", s)
			}
			path := "/api/inbox/messages?session=" + session + "&exact_session=true"
			if mode == "long poll" {
				path += "&wait_ms=30000"
			} else if mode == "project alias" {
				path = "/api/projects/" + project + "/messages/listen?to=codex:worker&session=" + session
			}
			status, body := get(w.agent.ID, path)
			var got []string
			if mode == "project alias" {
				for _, msg := range mustJSON[compatPage](t, body).Items {
					got = append(got, msg.ID)
				}
			} else {
				for _, msg := range mustJSON[Page](t, body).Items {
					got = append(got, msg.ID)
				}
			}
			if status != 200 || fmt.Sprint(got) != fmt.Sprint(ids) {
				t.Fatalf("pickup order=%v want %v: %d %s", got, ids, status, body)
			}
			// A second hook cannot claim the active pickup. A lost response is
			// replayable after its bounded lease, with the same message IDs.
			status, body = get(w.agent.ID, "/api/inbox/messages?session="+session)
			if status != 200 || len(mustJSON[Page](t, body).Items) != 0 {
				t.Fatalf("active pickup duplicated: %d %s", status, body)
			}
			if _, err := w.db.Admin.Exec(t.Context(), `UPDATE inbox_messages SET pickup_lease_until=clock_timestamp()-interval '1 second' WHERE id=ANY($1::uuid[])`, ids); err != nil {
				t.Fatal(err)
			}
			status, body = get(w.agent.ID, "/api/inbox/messages?session="+session)
			replay := mustJSON[Page](t, body).Items
			if status != 200 || len(replay) != len(ids) {
				t.Fatalf("lost response cannot replay: %d %s", status, body)
			}
			for i, msg := range replay {
				if msg.ID != ids[i] || msg.Body != fmt.Sprintf("Chat %d", i) {
					t.Fatalf("replayed identity/order changed: %+v", replay)
				}
				for range 2 {
					if _, err := m.base.ack(t.Context(), w.agent, msg.ID); err != nil {
						t.Fatal(err)
					}
				}
				if s := messageStatus(t, w, get, w.sender.ID, msg.ID); s == nil || s.Status != "read" || s.WaitingForPickup || s.PickupStalledAt != nil {
					t.Fatalf("confirmed receipt=%+v", s)
				}
			}
			for _, kind := range []string{"inbox.message_fetched", "inbox.acked", "inbox.receipt_handed_off"} {
				if n := countSQL(t, w.db.Admin, w.sender.TenantID, `SELECT count(*) FROM events WHERE type=$1 AND coalesce(after->>'message_id',after->>'id')=ANY($2::text[])`, kind, ids); n != 3 {
					t.Fatalf("%s count=%d want 3", kind, n)
				}
			}
		})
	}
	t.Run("explicit sender expiry stays terminal", func(t *testing.T) {
		w, m, project, _ := messagingWorld(t)
		session := messageTestSession(t, w, project, w.agent, "Expiring chat")
		expires := time.Now().Add(time.Hour)
		msg, err := m.base.send(t.Context(), w.sender, sendInput{Recipient: w.agent.ID, RecipientSessionID: &session, Body: "Expires by request", Key: "explicit-expiry", Expires: &expires})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.db.Admin.Exec(t.Context(), `UPDATE inbox_messages SET created_at=clock_timestamp()-interval '2 minutes',expires_at=clock_timestamp()-interval '1 minute' WHERE id=$1::uuid`, msg.ID); err != nil {
			t.Fatal(err)
		}
		expireDeadline(t, w, msg.ID)
		if n := sweepNow(t, w, w.sender.TenantID); n != 1 {
			t.Fatalf("explicit expiry failed %d messages", n)
		}
		if state, reason := receiptState(t, w, msg.ID); state != "failed" || reason != ReasonDeadline {
			t.Fatalf("explicit expiry receipt=%s %s", state, reason)
		}
	})
	t.Run("concurrent hooks", func(t *testing.T) {
		w, m, project, _ := messagingWorld(t)
		session := messageTestSession(t, w, project, w.agent, "Concurrent chat")
		msg := boundMessage(t, w, m, project, session, "concurrent")
		pool, barrier, ctx := dbtest.BarrierPool(t, w.db.App, func(sql string) bool {
			return strings.Contains(sql, "FROM harness_sessions s JOIN") && strings.Contains(sql, "FOR NO KEY UPDATE")
		})
		type result struct {
			items []Message
			err   error
		}
		one, two := make(chan result, 1), make(chan result, 1)
		pull := func(ctx context.Context, mod *module, resultc chan result) {
			items, err := mod.pendingVia(ctx, w.agent, 0, 10, &session, true, SeenHook)
			resultc <- result{items, err}
		}
		go pull(ctx, newModule(pool), one)
		pid := barrier.Wait(t, ctx)
		done := make(chan struct{})
		go func() { pull(ctx, m.base, two); close(done) }()
		lock := dbtest.BlockedOrDone(t, ctx, w.db.Admin, pid, done)
		barrier.Release()
		first, second := dbtest.Await(t, ctx, one), dbtest.Await(t, ctx, two)
		if lock == "" || first.err != nil || second.err != nil || len(first.items) != 1 || first.items[0].ID != msg.ID || len(second.items) != 0 {
			t.Fatalf("concurrent pickup: lock=%s first=%+v second=%+v", lock, first, second)
		}
	})
	t.Run("configured stall boundary and concurrent sweepers", func(t *testing.T) {
		w, m, project, srv := messagingWorld(t)
		session := messageTestSession(t, w, project, w.agent, "Stalled chat")
		if _, err := w.db.Admin.Exec(t.Context(), `UPDATE harness_sessions SET owner_principal_id=$2::uuid WHERE id=$1::uuid`, session, w.admin.ID); err != nil {
			t.Fatal(err)
		}
		put := func(seconds string) (int, []byte) {
			return do(t, srv, w.admin.ID, "PUT", "/api/settings/inbox-delivery", `{"session_deadline_seconds":300,"unbound_deadline_seconds":1800,"max_attempts":8`+seconds+`}`, nil)
		}
		if code, body := do(t, srv, w.admin.ID, "GET", "/api/settings/inbox-delivery", "", nil); code != 200 || mustJSON[DeliverySettings](t, body).ChatPickupStallSeconds != 600 {
			t.Fatalf("default stall setting: %d %s", code, body)
		}
		if code, _ := put(`,"chat_pickup_stall_seconds":59`); code != 400 {
			t.Fatal("unbounded stall setting accepted")
		}
		if code, body := put(`,"chat_pickup_stall_seconds":120`); code != 200 || mustJSON[DeliverySettings](t, body).ChatPickupStallSeconds != 120 {
			t.Fatalf("configured threshold: %d %s", code, body)
		}
		if code, body := put(""); code != 200 || mustJSON[DeliverySettings](t, body).ChatPickupStallSeconds != 120 {
			t.Fatalf("older settings client erased threshold: %d %s", code, body)
		}
		msg := boundMessage(t, w, m, project, session, "stall")
		var created time.Time
		guaranteeRow(t, w, `SELECT created_at FROM inbox_messages WHERE id=$1::uuid`, []any{msg.ID}, &created)
		now := created.Add(120*time.Second - time.Microsecond)
		sweep := NewSweeper(w.db.App)
		sweep.clock = func() time.Time { return now }
		if _, err := sweep.SweepTenant(t.Context(), w.sender.TenantID); err != nil {
			t.Fatal(err)
		}
		if n := countSQL(t, w.db.Admin, w.sender.TenantID, `SELECT count(*) FROM events WHERE type='delivery.stall_alerted'`); n != 0 {
			t.Fatal("stall notice preceded threshold")
		}
		now = now.Add(time.Microsecond)
		pool, barrier, ctx := dbtest.BarrierPool(t, w.db.App, func(sql string) bool {
			return strings.Contains(sql, "UPDATE inbox_receipts SET pickup_stalled_at")
		})
		one, two := make(chan error, 1), make(chan error, 1)
		go func() {
			s := NewSweeper(pool)
			s.clock = func() time.Time { return now }
			one <- s.sweepChatPickup(db.AllProjects(ctx, "chat pickup test"), w.sender.TenantID)
		}()
		pid := barrier.Wait(t, ctx)
		done := make(chan struct{})
		go func() {
			two <- sweep.sweepChatPickup(db.AllProjects(ctx, "chat pickup test"), w.sender.TenantID)
			close(done)
		}()
		lock := dbtest.BlockedOrDone(t, ctx, w.db.Admin, pid, done)
		barrier.Release()
		if err := dbtest.Await(t, ctx, one); err != nil {
			t.Fatal(err)
		}
		if err := dbtest.Await(t, ctx, two); err != nil || lock == "" {
			t.Fatalf("competing sweeper: lock=%s err=%v", lock, err)
		}
		var payload string
		guaranteeRow(t, w, `SELECT after::text FROM events WHERE type='delivery.stall_alerted' AND after->>'message_id'=$1`, []any{msg.ID}, &payload)
		if !strings.Contains(payload, w.admin.ID) || !strings.Contains(payload, "session_chat") || strings.Contains(payload, msg.Body) {
			t.Fatalf("owner alert leaked content or lost routing: %s", payload)
		}
		if n := countSQL(t, w.db.Admin, w.sender.TenantID, `SELECT count(*) FROM inbox_messages WHERE idempotency_key=$1 AND recipient_principal_id=$2::uuid AND body LIKE '%stays queued%'`, "chat-pickup-stalled/"+msg.ID, w.sender.ID); n != 1 {
			t.Fatalf("sender notices=%d", n)
		}
		get := func(principal, path string) (int, []byte) { return do(t, srv, principal, "GET", path, "", nil) }
		if s := messageStatus(t, w, get, w.sender.ID, msg.ID); s == nil || !s.WaitingForPickup || s.PickupStalledAt == nil || !s.PickupStalledAt.Equal(now) {
			t.Fatalf("stalled sender receipt=%+v", s)
		}
		if state, reason := receiptState(t, w, msg.ID); state != "queued" || reason != "" {
			t.Fatalf("stall failed message: %s %s", state, reason)
		}
		items, err := m.base.pendingVia(t.Context(), w.agent, 0, 10, &session, true, SeenHook)
		if err != nil || len(items) != 1 || items[0].ID != msg.ID {
			t.Fatalf("stalled message undeliverable: %+v %v", items, err)
		}
	})
}

// Independent wire-format probes retry after the prior pickup lease expires.
// The lifecycle test above separately rejects a retry during an active lease.
func expireChatPickupLease(t *testing.T, w *world, sessions ...string) {
	t.Helper()
	if _, err := w.db.Admin.Exec(t.Context(), `UPDATE inbox_messages SET pickup_lease_until=clock_timestamp()-interval '1 second' WHERE recipient_session_id=ANY($1::uuid[])`, sessions); err != nil {
		t.Fatal(err)
	}
}
