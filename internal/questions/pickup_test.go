// SPDX-License-Identifier: AGPL-3.0-only
package questions

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/inbox"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risk: an offline asker loses an answer, retries duplicate it, or the lead
// never learns that pickup stalled. Exercise real durable replies and receipts
// through every reader, with a fake clock and a subscribed SSE barrier.
func TestDeliveryOfflinePickupAndStall(t *testing.T) {
	for _, mode := range []string{"inbox", "long poll", "stream", "project inbox"} {
		t.Run(mode, func(t *testing.T) {
			f := deliveryFixtureFor(t)
			f.agent.Scopes = append(f.agent.Scopes, "inbox.read", "inbox.send")
			var qs []Question
			var ids []string
			for i := range 2 {
				in := input()
				in.Question = fmt.Sprintf("Offline question %d?", i)
				if mode == "long poll" || mode == "stream" {
					in.SessionID = f.session
				}
				q := f.answer(t, f.ask(t, in), fmt.Sprintf("Answer %d", i))
				f.advance(10 * time.Second)
				f.dispatch(t)
				qs = append(qs, q)
				for _, e := range f.status(t, q.ID).Pending {
					if e.Kind == "inbox" {
						if e.State != "delivered" || e.ReceiptState != "queued" || e.PickedUpAt != nil {
							t.Fatalf("offline status: %+v", e)
						}
						ids = append(ids, e.EffectRef)
					}
				}
			}
			if len(ids) != 2 {
				t.Fatalf("inbox replies=%v", ids)
			}
			if _, err := f.d.Admin.Exec(t.Context(), `UPDATE inbox_receipts SET deliver_by=clock_timestamp()-interval '1 minute'`); err != nil {
				t.Fatal(err)
			}
			if n, err := inbox.NewSweeper(f.d.App).SweepTenant(t.Context(), f.agent.TenantID); err != nil || n != 0 {
				t.Fatalf("offline Desk answers failed: n=%d err=%v", n, err)
			}
			if n := f.count(t, `SELECT count(*) FROM inbox_message_deliveries WHERE message_id=ANY($1::uuid[]) AND state='pending'`, ids); n != 2 {
				t.Fatalf("pending deliveries=%d", n)
			}
			err := db.InTenant(db.AllProjects(t.Context(), "test pending Desk failure guard"), f.d.App, f.agent.TenantID, func(tx pgx.Tx) error {
				for _, reason := range []string{inbox.ReasonNoListener, inbox.ReasonDeadline} {
					if failed, err := inbox.FailMessage(t.Context(), tx, ids[0], reason); err != nil || failed {
						return fmt.Errorf("Desk failure %s: failed=%v err=%v", reason, failed, err)
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			pull := func() []string {
				t.Helper()
				path := "/api/inbox/messages?limit=10"
				var messages []inbox.Message
				switch mode {
				case "long poll":
					path += "&session=" + f.session + "&exact_session=true&wait_ms=30000"
				case "stream":
					ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
					defer cancel()
					w := &pickupStreamWriter{ResponseRecorder: httptest.NewRecorder(), cancel: cancel, remaining: 2}
					r := httptest.NewRequest("GET", "/api/inbox/stream?session="+f.session+"&exact_session=true", nil)
					f.mux.ServeHTTP(w, r.WithContext(tenant.WithPrincipal(ctx, f.agent)))
					for _, line := range strings.Split(w.Body.String(), "\n") {
						if strings.HasPrefix(line, "data: ") {
							var msg inbox.Message
							if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &msg); err != nil {
								t.Fatal(err)
							}
							messages = append(messages, msg)
						}
					}
				case "project inbox":
					path = "/api/projects/" + f.project + "/messages/listen?limit=10"
				}
				if mode != "stream" {
					w := request(t.Context(), f.mux, f.agent, "GET", path, nil)
					var page inbox.Page
					if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil {
						t.Fatalf("pickup: %d %s", w.Code, w.Body.String())
					}
					messages = page.Items
				}
				got := []string{}
				for i, msg := range messages {
					if i >= len(qs) || !strings.Contains(msg.Body, qs[i].Answer.ID) || !strings.Contains(msg.Body, fmt.Sprintf("Answer %d", i)) {
						t.Fatalf("wrong answer/order: %+v", messages)
					}
					got = append(got, msg.ID)
				}
				return got
			}
			for range 2 { // Lost response / crash: replay the same identities, in order.
				if got := pull(); fmt.Sprint(got) != fmt.Sprint(ids) {
					t.Fatalf("pickup order/identity=%v want %v", got, ids)
				}
				for _, q := range qs {
					for _, e := range f.status(t, q.ID).Pending {
						if e.Kind == "inbox" && (e.PickedUpAt == nil || e.ReceiptState != "queued") {
							t.Fatalf("pickup missing or falsely confirmed: %+v", e)
						}
					}
				}
			}
			// A crash after pickup must not turn the pending confirmation terminal.
			if n, err := inbox.NewSweeper(f.d.App).SweepTenant(t.Context(), f.agent.TenantID); err != nil || n != 0 {
				t.Fatalf("picked up Desk answers failed: n=%d err=%v", n, err)
			}
			for _, id := range ids {
				for range 2 {
					ctx := authz.WithRouteScope(t.Context(), authz.Scope{ProjectID: f.project})
					w := request(ctx, f.mux, f.agent, "POST", "/api/inbox/messages/"+id+"/ack", nil)
					if w.Code != 200 {
						t.Fatalf("ack: %d %s", w.Code, w.Body.String())
					}
				}
			}
			for _, kind := range []string{"inbox.message_fetched", "inbox.acked", "inbox.receipt_handed_off"} {
				if n := f.count(t, `SELECT count(*) FROM events WHERE type=$1`, kind); n != 2 {
					t.Fatalf("%s events=%d want 2", kind, n)
				}
			}
			if n := f.count(t, `SELECT count(*) FROM inbox_receipts WHERE state='handed_off'`); n != 2 {
				t.Fatalf("confirmed receipts=%d", n)
			}
			if n := f.count(t, `SELECT count(*) FROM inbox_messages`); n != 2 {
				t.Fatalf("retries duplicated answers: %d messages", n)
			}
			w := request(t.Context(), f.mux, f.agent, "GET", "/api/inbox/messages?session="+f.session, nil)
			var page inbox.Page
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != 0 {
				t.Fatalf("acknowledged answers reappeared: %d %s", w.Code, w.Body.String())
			}
			f.advance(time.Hour)
			f.dispatch(t)
			if n := f.count(t, `SELECT count(*) FROM events WHERE type='delivery.stall_alerted'`); n != 0 {
				t.Fatal("picked-up answer raised a stall")
			}
		})
	}
	t.Run("durable stall at thirty minutes", func(t *testing.T) {
		f := deliveryFixtureFor(t)
		q := f.answer(t, f.ask(t, input()), "Private answer")
		f.advance(10 * time.Second)
		f.dispatch(t)
		var created time.Time
		if err := f.d.Admin.QueryRow(t.Context(), `SELECT created_at FROM inbox_messages`).Scan(&created); err != nil {
			t.Fatal(err)
		}
		f.now.Store(created.Add(30*time.Minute - time.Microsecond).UnixNano())
		f.dispatch(t)
		if n := f.count(t, `SELECT count(*) FROM events WHERE type='delivery.stall_alerted'`); n != 0 {
			t.Fatal("alert preceded the thirty-minute boundary")
		}
		f.advance(time.Microsecond)
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		barrier := &pickupDispatchBarrier{ready: make(chan struct{}, 2), release: make(chan struct{})}
		pool := deliveryTracedPool(t, f.d.App, barrier)
		start, errs := make(chan struct{}), make(chan error, 2)
		for range 2 {
			go func() {
				<-start
				m := New(pool) // Independent dispatcher / restart.
				m.clock = f.m.clock
				_, err := m.DispatchTenant(ctx, f.agent.TenantID)
				errs <- err
			}()
		}
		close(start)
		for range 2 {
			select {
			case <-barrier.ready: // Both replicas selected the same due reply.
			case err := <-errs:
				t.Fatalf("replica exited before its write barrier: %v", err)
			case <-ctx.Done():
				t.Fatal("replicas did not reach their write barrier")
			}
		}
		close(barrier.release)
		for range 2 {
			if err := <-errs; err != nil {
				t.Fatal(err)
			}
		}
		f.advance(time.Hour)
		f.dispatch(t)
		var payload []byte
		if err := f.d.Admin.QueryRow(t.Context(), `SELECT after FROM events WHERE type='delivery.stall_alerted' AND node_id=$1`, q.ID).Scan(&payload); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(payload), "answer_not_picked_up") || strings.Contains(string(payload), "Private answer") || strings.Contains(string(payload), "Private context") {
			t.Fatalf("wrong/leaking stall event: %s", payload)
		}
		if n := f.count(t, `SELECT count(*) FROM events WHERE type='delivery.stall_alerted'`); n != 1 {
			t.Fatalf("replicas/restart duplicated stall: %d", n)
		}
		if n := f.count(t, `SELECT count(*) FROM inbox_messages WHERE recipient_principal_id=$1`, f.person.ID); n != 0 {
			t.Fatal("stall pushed a notification to the person")
		}
		f.expectEffects(t, q, "delivered") // Receipt remains pending, not failed.
	})
	t.Run("live listener receives answer", func(t *testing.T) {
		f := deliveryFixtureFor(t)
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			f.mux.ServeHTTP(w, r.WithContext(tenant.WithPrincipal(r.Context(), f.agent)))
		}))
		defer srv.Close()
		r, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/inbox/stream", nil)
		response, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		scanner := bufio.NewScanner(response.Body)
		if !scanner.Scan() || scanner.Text() != ": connected" {
			t.Fatal("stream did not establish its subscription and initial read")
		}
		q := f.answer(t, f.ask(t, input()), "Live answer")
		f.advance(10 * time.Second)
		f.dispatch(t)
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "data: ") {
				if !strings.Contains(scanner.Text(), q.Answer.ID) {
					t.Fatal("live listener received the wrong answer")
				}
				return
			}
		}
		t.Fatalf("live answer missing: %v", scanner.Err())
	})
}

type pickupStreamWriter struct {
	*httptest.ResponseRecorder
	cancel    context.CancelFunc
	remaining int
}

func (w *pickupStreamWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseRecorder.Write(b)
	if strings.Contains(string(b), "\nevent: message\n") {
		w.remaining--
		if w.remaining == 0 {
			w.cancel()
		}
	}
	return n, err
}

type pickupDispatchBarrier struct {
	ready, release chan struct{}
}

func (b *pickupDispatchBarrier) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, "set_config('lock_timeout','3s'") {
		select {
		case b.ready <- struct{}{}:
		case <-ctx.Done():
		}
		select {
		case <-b.release:
		case <-ctx.Done():
		}
	}
	return ctx
}

func (*pickupDispatchBarrier) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}
