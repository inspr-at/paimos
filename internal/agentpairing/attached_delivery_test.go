// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/attachedmsg"
	"github.com/inspr-at/paimos/internal/attachwatch"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/inbox"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func (n notesFixture) deliveryRequest(op string) attachwatch.DeviceRequest {
	in := n.in
	in.Operation = op
	in.MessageEpoch = "1"
	in.AttachProtocol = attachwatch.Protocol
	in.MessageBinding, _ = json.Marshal(n.grant.Binding)
	return in
}
func (n notesFixture) sendNote(t *testing.T, compat bool) inbox.Message {
	t.Helper()
	var msg inbox.Message
	decodeResult(t, n.f.call("POST", n.path(compat), n.body(attachedmsg.UUID(), "aeon393-body-"+attachedmsg.UUID(), compat), true, "", 201), &msg)
	return msg
}
func (n notesFixture) offer(t *testing.T) attachedmsg.Exchange {
	t.Helper()
	var out attachedmsg.Exchange
	decodeResult(t, n.f.call("POST", "/api/agent-pairing/attach", n.deliveryRequest("message_offer"), false, n.key, 200), &out)
	return out
}
func (n notesFixture) settle(t *testing.T, o *attachedmsg.Offer, outcome string, code int) attachedmsg.Exchange {
	t.Helper()
	in := n.deliveryRequest("message_receipt")
	in.MessageReceipt, _ = json.Marshal(attachedmsg.Receipt{DeliveryID: o.DeliveryID, MessageID: o.MessageID, Nonce: o.Nonce, Outcome: outcome, Epoch: o.Epoch})
	var out attachedmsg.Exchange
	w := n.f.call("POST", "/api/agent-pairing/attach", in, false, n.key, code)
	if code == 200 {
		decodeResult(t, w, &out)
	}
	return out
}
func (n notesFixture) state(t *testing.T, id string) string {
	t.Helper()
	var state string
	if e := n.f.db.Admin.QueryRow(t.Context(), `SELECT attached_outcome FROM inbox_messages WHERE id=$1`, id).Scan(&state); e != nil {
		t.Fatal(e)
	}
	return state
}
func (n notesFixture) tx(ctx context.Context, fn func(pgx.Tx) error) error {
	ctx = tenant.WithPrincipal(ctx, tenant.Principal{ID: n.recipient, TenantID: n.f.tenantID, Kind: tenant.Agent})
	return db.InTenant(ctx, n.f.db.App, n.f.tenantID, fn)
}
func (n notesFixture) sweep(t *testing.T, s *attachedmsg.Service) {
	t.Helper()
	if err := s.Sweep(t.Context(), n.f.db.App, n.f.tenantID); err != nil {
		t.Fatal(err)
	}
}
func TestAttachedDeliverySingleAttemptAndHonestReceipt(t *testing.T) {
	n := readyNotes(t)
	msg := n.sendNote(t, true)
	var before time.Time
	if err := n.f.db.Admin.QueryRow(t.Context(), `SELECT lease_until FROM harness_attach_requests WHERE id=$1`, n.in.RequestID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	o := n.offer(t)
	if o.Offer == nil || o.State != "offered" || o.Offer.MessageID != msg.ID || !strings.HasPrefix(o.Offer.Body, "aeon393-body-") || len(o.Offer.Nonce) != 64 {
		t.Fatal("missing first exact offer")
	}
	// Deadlines use the database clock, which can lead the test host's clock
	// (for example, PostgreSQL in Colima). Compare in the issuing clock domain.
	var deadline, databaseNow time.Time
	if err := n.f.db.Admin.QueryRow(t.Context(), `SELECT offer_deadline,clock_timestamp() FROM harness_deliveries WHERE id=$1`, o.Offer.DeliveryID).Scan(&deadline, &databaseNow); err != nil {
		t.Fatal(err)
	}
	if !o.Offer.Deadline.Equal(deadline) || deadline.Sub(databaseNow) > attachedmsg.OfferBudget {
		t.Fatal("unbounded offer")
	}
	if next := n.offer(t); next.Offer != nil || next.State != "empty" {
		t.Fatal("automatic replay of uncompleted attempt")
	}
	done := n.settle(t, o.Offer, "shown", 200)
	if done.State != "completed" || done.ShownAt == nil || done.CompletedAt == nil || done.Offer != nil {
		t.Fatal("missing content-free settlement")
	}
	replay := n.settle(t, o.Offer, "shown", 200)
	if replay.State != "completed" || replay.Offer != nil || !replay.CompletedAt.Equal(*done.CompletedAt) {
		t.Fatal("receipt replay changed evidence")
	}
	n.settle(t, o.Offer, "uncertain", 409)
	if next := n.offer(t); next.Offer != nil {
		t.Fatal("completed note offered again")
	}
	var after time.Time
	if err := n.f.db.Admin.QueryRow(t.Context(), `SELECT lease_until FROM harness_attach_requests WHERE id=$1`, n.in.RequestID).Scan(&after); err != nil || !before.Equal(after) {
		t.Fatal("message exchange renewed lease")
	}
	var statuses struct {
		Items []inbox.MessageStatus `json:"items"`
	}
	decodeResult(t, n.f.call("GET", "/api/inbox/message-status?ids="+msg.ID, nil, true, "", 200), &statuses)
	if len(statuses.Items) != 1 {
		t.Fatal("missing sender status")
	}
	status := statuses.Items[0]
	if status.Attached == nil || status.Attached.Outcome != "completed" || status.Attached.ShownAt == nil || status.Attached.CompletedAt == nil || status.DeliveredAt != nil || status.ReadAt != nil || status.Status == "read" || status.Status == "delivered" {
		t.Fatal("misleading attached status")
	}
	var receipt inbox.Receipt
	decodeResult(t, n.f.call("GET", "/api/inbox/messages/"+msg.ID+"/receipt", nil, true, "", 200), &receipt)
	if receipt.Attached == nil || receipt.Attached.CompletedAt == nil || receipt.HandedOffAt != nil {
		t.Fatal("legacy receipt claim leaked")
	}
	assertNoCanary(t, n.f, o.Offer.Body)
}
func TestAttachedDeliveryConcurrentClaims(t *testing.T) {
	n := readyNotes(t)
	msg := n.sendNote(t, false)
	// Hold all contenders at the real tenant fence. Use a separate pool without
	// rebuilding the API module: its watch-key authority must remain in memory.
	barrier := &notesFenceBarrier{arrived: make(chan struct{}, 12), release: make(chan struct{}), gate: true}
	cfg := n.f.db.App.Config()
	cfg.MaxConns = 13
	cfg.ConnConfig.Tracer = barrier
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var release sync.Once
	defer release.Do(func() { close(barrier.release) })
	type result struct {
		out attachedmsg.Exchange
		err error
	}
	results := make(chan result, 12)
	ctx := tenant.WithPrincipal(t.Context(), tenant.Principal{ID: n.recipient, TenantID: n.f.tenantID, Kind: tenant.Agent})
	for i := 0; i < 12; i++ {
		go func() {
			var offer *attachedmsg.Offer
			r := result{}
			r.err = db.InTenant(ctx, pool, n.f.tenantID, func(tx pgx.Tx) error {
				var err error
				offer, err = n.s.Claim(ctx, tx, n.grant.Binding, "1")
				return err
			})
			if r.err == nil && offer != nil {
				r.err = db.InTenant(ctx, pool, n.f.tenantID, func(tx pgx.Tx) error {
					var err error
					r.out, err = n.s.Release(ctx, tx, offer)
					return err
				})
			}
			results <- r
		}()
	}
	awaitNoteFences(t, barrier, 12)
	release.Do(func() { close(barrier.release) })
	offered := 0
	for i := 0; i < 12; i++ {
		var r result
		select {
		case r = <-results:
		case <-ctx.Done():
			t.Fatal("claim did not finish")
		}
		if r.err != nil {
			t.Fatal(r.err)
		}
		if r.out.Offer != nil {
			offered++
			if r.out.Offer.MessageID != msg.ID {
				t.Fatal("wrong note")
			}
		}
	}
	if offered != 1 {
		t.Fatalf("body-release attempts %d", offered)
	}
	var count int
	if err := n.f.db.Admin.QueryRow(t.Context(), `SELECT count(*) FROM harness_deliveries WHERE message_id=$1`, msg.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate attempt rows")
	}
}
func TestAttachedDeliveryNonceAndTupleFences(t *testing.T) {
	for _, field := range []string{"nonce", "message", "delivery", "epoch", "generation", "daemon", "session", "tenant", "snapshot", "service", "protocol", "pins"} {
		t.Run(field, func(t *testing.T) {
			n := readyNotes(t)
			msg := n.sendNote(t, false)
			o := n.offer(t).Offer
			if o == nil {
				t.Fatal("no offer")
			}
			in := n.deliveryRequest("message_receipt")
			r := attachedmsg.Receipt{DeliveryID: o.DeliveryID, MessageID: o.MessageID, Nonce: o.Nonce, Outcome: "shown", Epoch: o.Epoch}
			b := n.grant.Binding
			code := 403
			switch field {
			case "nonce":
				r.Nonce = strings.Repeat("a", 64)
			case "message":
				r.MessageID = attachedmsg.UUID()
			case "delivery":
				r.DeliveryID = attachedmsg.UUID()
			case "epoch":
				r.Epoch = attachedmsg.UUID()
			case "generation":
				b.Generation = attachedmsg.UUID()
			case "daemon":
				b.DaemonEpoch = strings.Repeat("b", 64)
			case "session":
				b.SessionID = attachedmsg.UUID()
			case "tenant":
				b.TenantID = attachedmsg.UUID()
			case "snapshot":
				b.SnapshotDigest = hash("different")
			case "service":
				b.ServiceEpoch = attachedmsg.UUID()
			case "protocol":
				in.AttachProtocol = 1
			case "pins":
				in.HookReleaseDigest = hash("replacement")
			}
			in.MessageBinding, _ = json.Marshal(b)
			in.MessageReceipt, _ = json.Marshal(r)
			n.f.call("POST", "/api/agent-pairing/attach", in, false, n.key, code)
			if n.state(t, msg.ID) != "offered" {
				t.Fatal("forged receipt changed state")
			}
			if got := n.settle(t, o, "shown", 200); got.State != "completed" {
				t.Fatal("real receipt refused")
			}
		})
	}
}
func TestAttachedDeliveryCrashAndLoss(t *testing.T) {
	for _, when := range []string{"before_commit", "after_claim", "lost_offer", "payload_loss", "queued_restart", "offered_restart", "completed_restart"} {
		t.Run(when, func(t *testing.T) {
			n := readyNotes(t)
			msg := n.sendNote(t, false)
			var o *attachedmsg.Offer
			switch when {
			case "before_commit":
				sentinel := errors.New("simulated transaction failure")
				err := n.tx(t.Context(), func(tx pgx.Tx) error {
					var e error
					o, e = n.s.Claim(tenant.WithPrincipal(t.Context(), tenant.Principal{TenantID: n.f.tenantID}), tx, n.grant.Binding, "1")
					if e != nil {
						return e
					}
					return sentinel
				})
				if !errors.Is(err, sentinel) || o == nil {
					t.Fatalf("claim rollback: %v", err)
				}
				if n.state(t, msg.ID) != "queued" {
					t.Fatal("rolled back claim changed queue")
				}
				if n.offer(t).Offer == nil {
					t.Fatal("first release lost before commit")
				}
				return
			case "after_claim":
				err := n.tx(t.Context(), func(tx pgx.Tx) error {
					var e error
					o, e = n.s.Claim(tenant.WithPrincipal(t.Context(), tenant.Principal{TenantID: n.f.tenantID}), tx, n.grant.Binding, "1")
					return e
				})
				if err != nil || o == nil {
					t.Fatalf("claim: %v", err)
				}
			case "lost_offer", "offered_restart", "completed_restart":
				o = n.offer(t).Offer
				if o == nil {
					t.Fatal("no offer")
				}
				if when == "completed_restart" {
					n.settle(t, o, "shown", 200)
				}
			case "payload_loss":
				n.s.Discard(n.f.tenantID, msg.ID)
			}
			if when == "queued_restart" || when == "offered_restart" || when == "completed_restart" {
				n.sweep(t, qualifiedNotes())
			} else {
				if when != "payload_loss" {
					if _, err := n.f.db.Admin.Exec(t.Context(), `UPDATE inbox_messages SET message_deadline=clock_timestamp()-interval '1 second' WHERE id=$1`, msg.ID); err != nil {
						t.Fatal(err)
					}
				}
				n.sweep(t, n.s)
			}
			want := "uncertain"
			if when == "queued_restart" || when == "payload_loss" {
				want = "not_delivered"
			}
			if when == "completed_restart" {
				want = "completed"
			}
			if got := n.state(t, msg.ID); got != want {
				t.Fatalf("outcome %s want %s", got, want)
			}
			if next := n.offer(t); next.Offer != nil {
				t.Fatal("recovery replayed text")
			}
			if o != nil && want == "uncertain" {
				n.settle(t, o, "shown", 409)
			}
		})
	}
}
func TestAttachedDeliveryCancellationAndRevocationRace(t *testing.T) {
	for _, action := range []string{"cancel", "revoke", "offline_revoke", "receipt_revoke"} {
		t.Run(action, func(t *testing.T) {
			n := readyNotes(t)
			msg := n.sendNote(t, false)
			var offered *attachedmsg.Offer
			if action == "receipt_revoke" {
				offered = n.offer(t).Offer
				if offered == nil {
					t.Fatal("no offer")
				}
			}
			if action == "offline_revoke" {
				if _, e := n.f.db.Admin.Exec(t.Context(), `UPDATE harness_attach_requests SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, n.in.RequestID); e != nil {
					t.Fatal(e)
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			start := make(chan struct{})
			results := make(chan *httptest.ResponseRecorder, 2)
			for i := 0; i < 2; i++ {
				go func(i int) {
					<-start
					var req *http.Request
					if i == 0 {
						in := n.deliveryRequest("message_offer")
						if offered != nil {
							in = n.deliveryRequest("message_receipt")
							in.MessageReceipt, _ = json.Marshal(attachedmsg.Receipt{DeliveryID: offered.DeliveryID, MessageID: offered.MessageID, Nonce: offered.Nonce, Outcome: "shown", Epoch: offered.Epoch})
						}
						req = n.f.request("POST", "/api/agent-pairing/attach", in, false, n.key)
					} else {
						path := "/api/agent-pairing/attach/" + n.in.RequestID + "/messages/revoke"
						if action == "cancel" {
							path = "/api/inbox/messages/" + msg.ID + "/cancel"
						}
						req = n.f.request("POST", path, nil, true, "")
					}
					w := httptest.NewRecorder()
					n.f.h.ServeHTTP(w, req.WithContext(ctx))
					results <- w
				}(i)
			}
			close(start)
			for i := 0; i < 2; i++ {
				select {
				case w := <-results:
					if w.Code != 200 && w.Code != 409 {
						t.Fatalf("race status %d: %s", w.Code, w.Body.String())
					}
				case <-ctx.Done():
					t.Fatal("lock order deadlock")
				}
			}
			state := n.state(t, msg.ID)
			switch action {
			case "cancel":
				if state != "cancelled" && state != "offered" {
					t.Fatal(state)
				}
			case "receipt_revoke":
				if state != "completed" && state != "uncertain" {
					t.Fatal(state)
				}
			default:
				if state != "revoked" && state != "uncertain" {
					t.Fatal(state)
				}
			}
		})
	}
}
func TestAttachedDeliveryFiveMinuteWaitAndReplayExpiry(t *testing.T) {
	n := readyNotes(t)
	msg := n.sendNote(t, false)
	var remaining float64
	if e := n.f.db.Admin.QueryRow(t.Context(), `SELECT extract(epoch from message_deadline-clock_timestamp()) FROM inbox_messages WHERE id=$1`, msg.ID).Scan(&remaining); e != nil || remaining < 240 || remaining > 300 {
		t.Fatalf("waiting deadline %f: %v", remaining, e)
	}
	o := n.offer(t).Offer
	if o == nil {
		t.Fatal("no offer")
	}
	n.settle(t, o, "shown", 200)
	if _, e := n.f.db.Admin.Exec(t.Context(), `UPDATE harness_attach_requests SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, n.in.RequestID); e != nil {
		t.Fatal(e)
	}
	n.settle(t, o, "shown", 409)
	if n.state(t, msg.ID) != "completed" {
		t.Fatal("replay rewrote committed evidence")
	}
}
func TestAttachedDeliverySweepSendOfferLockOrder(t *testing.T) {
	n := readyNotes(t)
	n.sendNote(t, false)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	errs := make(chan error, 9)
	for i := 0; i < 9; i++ {
		go func(i int) {
			switch i % 3 {
			case 0:
				errs <- n.s.Sweep(ctx, n.f.db.App, n.f.tenantID)
			default:
				in := n.deliveryRequest("message_offer")
				path := "/api/agent-pairing/attach"
				body := any(in)
				owner := false
				key := n.key
				if i%3 == 1 {
					path = n.path(true)
					body = n.body(fmt.Sprint("race-send", i), "lock test", true)
					owner = true
					key = ""
				}
				w := httptest.NewRecorder()
				r := n.f.request("POST", path, body, owner, key).WithContext(ctx)
				n.f.h.ServeHTTP(w, r)
				if w.Code != 200 && w.Code != 201 && w.Code != 429 {
					errs <- fmt.Errorf("unexpected status %d", w.Code)
				} else {
					errs <- nil
				}
			}
		}(i)
	}
	for i := 0; i < 9; i++ {
		select {
		case err := <-errs:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("send/sweep/claim deadlock")
		}
	}
}

func TestAttachedDeliveryReleaseCommitAndRevocationFence(t *testing.T) {
	for _, failure := range []string{"release_rollback", "revoke_gap", "current_epoch", "deadline", "daemon_restart", "disabled", "wrong_instance", "stopped"} {
		t.Run(failure, func(t *testing.T) {
			n := readyNotes(t)
			msg := n.sendNote(t, false)
			ctx := tenant.WithPrincipal(t.Context(), tenant.Principal{TenantID: n.f.tenantID, ID: n.recipient, Kind: tenant.Agent})
			var o *attachedmsg.Offer
			err := n.tx(ctx, func(tx pgx.Tx) error { var e error; o, e = n.s.Claim(ctx, tx, n.grant.Binding, "1"); return e })
			if err != nil || o == nil {
				t.Fatalf("claim failed: %v", err)
			}
			if failure == "release_rollback" {
				sentinel := errors.New("lost release commit")
				err = n.tx(ctx, func(tx pgx.Tx) error {
					out, e := n.s.Release(ctx, tx, o)
					if e != nil {
						return e
					}
					if out.Offer == nil {
						t.Fatal("no release inside transaction")
					}
					return sentinel
				})
				if !errors.Is(err, sentinel) {
					t.Fatal(err)
				}
				if got := n.offer(t); got.Offer != nil {
					t.Fatal("rollback replayed body")
				}
				// Retrying Release itself has no payload, although its SQL rolled back.
				var out attachedmsg.Exchange
				err = n.tx(ctx, func(tx pgx.Tx) error { var e error; out, e = n.s.Release(ctx, tx, o); return e })
				if err != nil || out.Offer != nil || out.State != "uncertain" {
					t.Fatal("release replay recovered consumed payload")
				}
				return
			}
			switch failure {
			case "revoke_gap":
				n.f.call("POST", "/api/agent-pairing/attach/"+n.in.RequestID+"/messages/revoke", nil, true, "", 200)
			case "current_epoch":
				_, err = n.f.db.Admin.Exec(t.Context(), `UPDATE attached_message_grants SET hook_epoch='2' WHERE id=$1`, n.grant.ID)
			case "deadline":
				_, err = n.f.db.Admin.Exec(t.Context(), `UPDATE inbox_messages SET message_deadline=clock_timestamp()-interval '1 second' WHERE id=$1`, msg.ID)
			case "daemon_restart":
				in := attachwatch.DeviceRequest{Operation: "register", AttachProtocol: attachwatch.Protocol, LocalConsentProofVersion: attachwatch.LocalConsentProofVersion, MessageProtocol: attachedmsg.Protocol, ComputerID: n.in.ComputerID, DeviceProof: n.in.DeviceProof, PollKey: nonce()}
				n.f.call("POST", "/api/agent-pairing/attach", in, false, n.key, 200)
				n.f.call("POST", "/api/agent-pairing/attach", n.deliveryRequest("message_offer"), false, n.key, 403)
			case "stopped":
				_, err = n.f.db.Admin.Exec(t.Context(), `UPDATE harness_sessions SET stopped_at=clock_timestamp(),phase='stopped' WHERE id=$1`, n.grant.Binding.SessionID)
			case "disabled":
				n.s = attachedmsg.New(attachedmsg.Options{})
			case "wrong_instance":
				n.s = qualifiedNotes()
			}
			if err != nil {
				t.Fatal(err)
			}
			var out attachedmsg.Exchange
			err = n.tx(ctx, func(tx pgx.Tx) error { var e error; out, e = n.s.Release(ctx, tx, o); return e })
			if err != nil || out.Offer != nil || out.State != "uncertain" {
				t.Fatalf("invalid release %s: %v", out.State, err)
			}
			if n.state(t, msg.ID) != "uncertain" {
				t.Fatal("released after invalidation")
			}
			if err = n.tx(ctx, func(tx pgx.Tx) error {
				if e := attachedmsg.Lock(ctx, tx); e != nil {
					return e
				}
				if _, ok := n.s.Take(n.grant.Binding, n.grant.ID, msg.ID, attachedmsg.TakeTransaction{Context: ctx, Tx: tx}); ok {
					t.Fatal("invalidated body released")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}

		})
	}
}

func TestAttachedDeliveryExpiryWithoutSweepAndFrozenEpoch(t *testing.T) {
	n := readyNotes(t)
	msg := n.sendNote(t, false)
	o := n.offer(t).Offer
	if o == nil {
		t.Fatal("no offer")
	}
	in := n.deliveryRequest("message_offer")
	in.MessageEpoch = "2"
	n.f.call("POST", "/api/agent-pairing/attach", in, false, n.key, 409)
	if _, err := n.f.db.Admin.Exec(t.Context(), `UPDATE inbox_messages SET message_deadline=clock_timestamp()-interval '1 second' WHERE id=$1`, msg.ID); err != nil {
		t.Fatal(err)
	}
	if got := n.settle(t, o, "shown", 200); got.State != "uncertain" || got.ShownAt != nil {
		t.Fatal("late handoff marked completed")
	}
	n.settle(t, o, "shown", 409)
	if n.state(t, msg.ID) != "uncertain" {
		t.Fatal("late receipt reopened uncertainty")
	}
	var statuses struct {
		Items []inbox.MessageStatus `json:"items"`
	}
	decodeResult(t, n.f.call("GET", "/api/inbox/message-status?ids="+msg.ID, nil, true, "", 200), &statuses)
	if len(statuses.Items) != 1 || statuses.Items[0].Status != "sent" || statuses.Items[0].Attached.Outcome != "uncertain" {
		t.Fatal("uncertainty projected as definitely not delivered")
	}

}
func TestAttachedDeliveryOnlyOneInflightFIFOAndQueuedExpiry(t *testing.T) {
	n := readyNotes(t)
	first := n.sendNote(t, false)
	second := n.sendNote(t, true)
	o := n.offer(t).Offer
	if o == nil || o.MessageID != first.ID {
		t.Fatal("not FIFO")
	}
	if n.offer(t).Offer != nil {
		t.Fatal("second note offered while first in flight")
	}
	if got := n.settle(t, o, "uncertain", 200); got.State != "uncertain" {
		t.Fatal("uncertainty settlement")
	}
	if got := n.settle(t, o, "uncertain", 200); got.State != "uncertain" {
		t.Fatal("uncertainty replay")
	}
	n.settle(t, o, "shown", 409)
	if o = n.offer(t).Offer; o == nil || o.MessageID != second.ID {
		t.Fatal("FIFO did not advance after terminal attempt")
	}
	third := n.sendNote(t, false)
	if _, e := n.f.db.Admin.Exec(t.Context(), `UPDATE inbox_messages SET message_deadline=clock_timestamp()-interval '1 second' WHERE id=$1`, third.ID); e != nil {
		t.Fatal(e)
	}
	n.sweep(t, n.s)
	if n.state(t, third.ID) != "expired" {
		t.Fatal("unoffered expired note uncertain")
	}
}

func TestAttachedDeliveryTwoSessionsOnePrincipal(t *testing.T) {
	n := readyNotes(t)
	msg := n.sendNote(t, false)
	in := n.in
	in.RequestID = attachedmsg.UUID()
	in.Operation = "request"
	in.ConsentDigest = ""
	in.Sequence = 0
	in.Snapshot.Process.PID++
	in.Snapshot.Process.Started = "second-process"
	in.MessageGeneration = ""
	in.MessageConsentDigest = ""
	activateWatch(t, n.f, n.key, &in)
	in.Operation = "message_request"
	var c attachedmsg.Capability
	decodeResult(t, n.f.call("POST", "/api/agent-pairing/attach", in, false, n.key, 200), &c)
	n.f.call("POST", "/api/agent-pairing/attach/"+in.RequestID+"/messages/approve", map[string]string{"message_generation": c.Grant.Binding.Generation, "consent_digest": c.Grant.Digest}, true, "", 200)
	in.MessageGeneration = c.Grant.Binding.Generation
	in.MessageConsentDigest = c.Grant.Digest
	in.Operation = "message_activate"
	n.f.call("POST", "/api/agent-pairing/attach", in, false, n.key, 200)
	in.Operation = "message_observed"
	n.f.call("POST", "/api/agent-pairing/attach", in, false, n.key, 200)
	other := notesFixture{n.f, n.s, n.key, in, *c.Grant, n.recipient}
	if other.offer(t).Offer != nil {
		t.Fatal("principal-wide fallback stole other session")
	}
	if n.state(t, msg.ID) != "queued" {
		t.Fatal("other session advanced receipt")
	}
	if o := n.offer(t).Offer; o == nil || o.MessageID != msg.ID {
		t.Fatal("exact session lost its message")
	}
}

func TestAttachedDeliveryLegacyStreamAndDatabaseFences(t *testing.T) {
	n := readyNotes(t)
	msg := n.sendNote(t, true)
	o := n.offer(t).Offer
	if o == nil {
		t.Fatal("no offer")
	}
	mux := http.NewServeMux()
	inbox.New(n.f.db.App, n.s).Mount(mux)
	harness.New(n.f.db.App).Mount(mux)
	p := tenant.Principal{ID: n.recipient, TenantID: n.f.tenantID, Kind: tenant.Agent, Scopes: []string{"harness.worker"}}
	// A verified attached hook may complete durable drain leases, but its
	// volatile offer must still use the generation-bound receipt protocol.
	lease := "attached-delivery-legacy-fence-lease-000001"
	proof := sha256.Sum256([]byte("aeon.harness.lease\x00" + lease))
	if _, err := n.f.db.Admin.Exec(t.Context(), `UPDATE harness_sessions SET capabilities=ARRAY['inbox','attached_reconnect_v1'],lease_digest=$2 WHERE id=$1`, n.grant.Binding.SessionID, proof[:]); err != nil {
		t.Fatal(err)
	}
	var cursor int64
	if err := n.f.db.Admin.QueryRow(t.Context(), `SELECT cursor FROM harness_deliveries WHERE id=$1`, o.DeliveryID).Scan(&cursor); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{"delivery_id": o.DeliveryID, "cursor": cursor})
	if err != nil {
		t.Fatal(err)
	}
	complete := httptest.NewRequest("POST", "/api/projects/"+n.in.Snapshot.ProjectID+"/harness-sessions/"+n.grant.Binding.SessionID+"/complete-delivery", strings.NewReader(string(body))).WithContext(tenant.WithPrincipal(t.Context(), p))
	complete.Header.Set("X-Aeon-Worker-Lease", lease)
	refused := httptest.NewRecorder()
	mux.ServeHTTP(refused, complete)
	if refused.Code != http.StatusNotFound {
		t.Fatalf("volatile hook lease entered durable completion: status %d body %s", refused.Code, refused.Body.String())
	}
	var completed, released bool
	if err := n.f.db.Admin.QueryRow(t.Context(), `SELECT completed_at IS NOT NULL,released_at IS NOT NULL FROM harness_deliveries WHERE id=$1`, o.DeliveryID).Scan(&completed, &released); err != nil || completed || released {
		t.Fatalf("durable completion changed volatile lease: completed=%t released=%t err=%v", completed, released, err)
	}
	ctx, cancel := context.WithTimeout(tenant.WithPrincipal(t.Context(), p), time.Second)
	defer cancel()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/inbox/stream?exact_session=true&session="+n.grant.Binding.SessionID, nil).WithContext(ctx)
	mux.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), ": connected") || strings.Contains(w.Body.String(), msg.ID) || strings.Contains(w.Body.String(), o.Body) {
		t.Fatal("legacy SSE consumed attached note")
	}
	for _, stmt := range []string{
		`UPDATE harness_deliveries SET leased_at=clock_timestamp() WHERE message_id=$1`,
		`UPDATE harness_deliveries SET body_released_at=NULL WHERE message_id=$1`,
		`UPDATE harness_deliveries SET mode='managed' WHERE message_id=$1`,
		`INSERT INTO harness_deliveries(tenant_id,session_id,message_id,cursor) SELECT tenant_id,recipient_session_id,id,sent_event_id FROM inbox_messages WHERE id=$1`,
	} {
		if _, e := n.f.db.Admin.Exec(t.Context(), stmt, msg.ID); e == nil {
			t.Fatal("legacy replay write accepted")
		}
	}
	var fetched, acked bool
	if e := n.f.db.Admin.QueryRow(t.Context(), `SELECT fetched_at IS NOT NULL,acked_at IS NOT NULL FROM inbox_messages WHERE id=$1`, msg.ID).Scan(&fetched, &acked); e != nil || fetched || acked {
		t.Fatal("legacy authority advanced")
	}
}

func TestAttachedDeliverySettlementCommitIsAtomic(t *testing.T) {
	n := readyNotes(t)
	msg := n.sendNote(t, false)
	o := n.offer(t).Offer
	if o == nil {
		t.Fatal("no offer")
	}
	ctx := tenant.WithPrincipal(t.Context(), tenant.Principal{TenantID: n.f.tenantID, ID: n.recipient, Kind: tenant.Agent})
	sentinel := errors.New("server stopped before settlement commit")
	r := attachedmsg.Receipt{DeliveryID: o.DeliveryID, MessageID: o.MessageID, Nonce: o.Nonce, Epoch: o.Epoch, Outcome: "shown"}
	err := n.tx(ctx, func(tx pgx.Tx) error {
		out, err := n.s.Settle(ctx, tx, n.grant.Binding, r)
		if err != nil {
			return err
		}
		if out.State != "completed" {
			return errors.New("settlement did not complete in transaction")
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	var partial bool
	if err = n.f.db.Admin.QueryRow(t.Context(), `SELECT shown_at IS NOT NULL OR completed_at IS NOT NULL FROM harness_deliveries WHERE id=$1`, o.DeliveryID).Scan(&partial); err != nil || partial {
		t.Fatal("partial shown/completed evidence survived rollback")
	}
	n.sweep(t, qualifiedNotes())
	if n.state(t, msg.ID) != "uncertain" {
		t.Fatal("restart invented a committed handoff")
	}
	if n.offer(t).Offer != nil {
		t.Fatal("settlement crash replayed the body")
	}
}

func TestAttachedDisclosureRechecksConsentAfterRelease(t *testing.T) {
	n := readyNotes(t)
	msg := n.sendNote(t, false)
	offered := n.offer(t)
	if offered.Offer == nil {
		t.Fatal("offer prerequisite failed")
	}
	in := n.deliveryRequest("message_validate")
	in.MessageReceipt, _ = json.Marshal(attachedmsg.Receipt{DeliveryID: offered.Offer.DeliveryID, MessageID: msg.ID, Nonce: offered.Offer.Nonce, Epoch: offered.Offer.Epoch, Outcome: "uncertain"})
	var valid attachedmsg.Exchange
	decodeResult(t, n.f.call("POST", "/api/agent-pairing/attach", in, false, n.key, 200), &valid)
	if valid.State != "offered" || valid.Offer != nil {
		t.Fatal("fresh validation did not remain content-free")
	}
	n.f.call("POST", "/api/agent-pairing/attach/"+n.in.RequestID+"/messages/revoke", nil, true, "", 200)
	var revoked attachedmsg.Exchange
	decodeResult(t, n.f.call("POST", "/api/agent-pairing/attach", in, false, n.key, 200), &revoked)
	if revoked.State != "uncertain" || revoked.Offer != nil || n.state(t, msg.ID) != "uncertain" {
		t.Fatal("withdrawn consent remained valid for disclosure")
	}
	assertNoCanary(t, n.f, offered.Offer.Body)
}
