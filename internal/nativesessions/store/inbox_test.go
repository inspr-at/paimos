// SPDX-License-Identifier: AGPL-3.0-only
package store

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	guuid "github.com/google/uuid"
	"github.com/inspr-at/paimos/internal/inbox"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/jackc/pgx/v5"
)

// Risk: queue/lease acceptance is presented as delivery, input is duplicated on
// repeated hints, Desk answers wait behind ordinary messages, or an offline
// generation is stolen. Subscription and correlated receipt channels establish
// every boundary; only the provider fixture is synthetic.
func exerciseLiveInbox(t *testing.T) {
	hints, subscribed, acknowledged := make(chan struct{}, 8), make(chan struct{}, 1), make(chan struct{}, 4)
	c := composeProduction(t, compositionOptions{disableRetention: true,
		notifications: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "event: harness.wake\ndata: {}\n\n")
			http.NewResponseController(w).Flush()
			select {
			case subscribed <- struct{}{}:
			default:
			}
			for {
				select {
				case <-r.Context().Done():
					return
				case <-hints:
					fmt.Fprint(w, "event: harness.wake\ndata: {}\n\n")
					if http.NewResponseController(w).Flush() != nil {
						return
					}
				}
			}
		},
		afterReceipt: func(r nativecontract.Receipt) {
			if r.State == "write_acknowledged" {
				acknowledged <- struct{}{}
			}
		},
	})
	f := c.f
	f.begin(t)
	out, err := c.config.Host.Start(t.Context(), nativecontract.LocalStart{Intent: f.intent, Cwd: c.root, AccountKey: "real-own-account"})
	if err != nil {
		t.Fatal(err)
	}
	b := *out.Session.CurrentBinding
	wait := func(ch <-chan struct{}) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(15 * time.Second):
			t.Fatal("missing live delivery barrier")
		}
	}
	wait(subscribed)
	var ordinary, desk, offline inbox.Message
	var offlineSession string
	f.seed(t, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,harness,host,management,role,work_shape,ref_digest,lease_digest) VALUES($1,$2,$3,'claude','offline','unmanaged','worker','unknown',$4,$5) RETURNING id::text`, f.person.TenantID, f.project, f.agent.ID, []byte(guuid.NewString()), []byte(guuid.NewString())).Scan(&offlineSession); err != nil {
			return err
		}
		ordinary, err = inbox.AcceptMessageTx(t.Context(), tx, f.person, inbox.Acceptance{RecipientPrincipalID: f.agent.ID, RecipientSessionID: &b.HarnessSessionID, Body: "ordinary live message", IdempotencyKey: guuid.NewString()})
		if err != nil {
			return err
		}
		desk, err = inbox.AcceptMessageTx(t.Context(), tx, f.person, inbox.Acceptance{RecipientPrincipalID: f.agent.ID, RecipientSessionID: &b.HarnessSessionID, Body: `{"type":"question.answer","answer":"recorded human decision"}`, IdempotencyKey: "desk/" + guuid.NewString()})
		if err != nil {
			return err
		}
		offline, err = inbox.AcceptMessageTx(t.Context(), tx, f.person, inbox.Acceptance{RecipientPrincipalID: f.agent.ID, RecipientSessionID: &offlineSession, Body: "offline inbox remains", IdempotencyKey: guuid.NewString()})
		return err
	})
	hints <- struct{}{}
	wait(acknowledged)
	c.provider.mu.Lock()
	in, sequence := c.provider.input, c.provider.sequence
	c.provider.mu.Unlock()
	if in.InputID != desk.ID || in.Mode != "now" || in.Text != inbox.LiveMessageText(desk.ID, f.person.ID, desk.Body) || sequence != 1 {
		t.Fatalf("priority input mismatch: id=%s sequence=%d", in.InputID, sequence)
	}
	check := func(message string, delivered, seen bool) {
		t.Helper()
		var gotDelivered, gotSeen bool
		var state string
		if err := f.d.Admin.QueryRow(t.Context(), `SELECT m.fetched_at IS NOT NULL,m.acked_at IS NOT NULL,r.state FROM inbox_messages m JOIN inbox_receipts r ON r.message_id=m.id WHERE m.id=$1`, message).Scan(&gotDelivered, &gotSeen, &state); err != nil {
			t.Fatal(err)
		}
		want := "queued"
		if seen {
			want = "handed_off"
		}
		if gotDelivered != delivered || gotSeen != seen || state != want {
			t.Fatalf("delivery truth: delivered=%v seen=%v state=%s", gotDelivered, gotSeen, state)
		}
	}
	check(desk.ID, true, false)
	check(ordinary.ID, false, false)
	check(offline.ID, false, false)
	for range 2 {
		if err := c.config.Poll(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	c.provider.mu.Lock()
	sequence = c.provider.sequence
	c.provider.mu.Unlock()
	if sequence != 1 {
		t.Fatal("duplicate hint or poll wrote another input")
	}
	turn := guuid.NewString()
	c.provider.turn("turn_started", turn, 2)
	wait(c.turnStarted)
	check(desk.ID, true, true)
	c.provider.turn("turn_completed", turn, 3)
	wait(c.turnDone)
	wait(acknowledged) // Completion wakes queued inbox work without an idle prompt.
	c.provider.mu.Lock()
	in = c.provider.input
	c.provider.mu.Unlock()
	if in.InputID != ordinary.ID {
		t.Fatal("live backlog did not drain after its safe boundary")
	}
	var leases, operations int
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM harness_deliveries WHERE session_id=$1),(SELECT count(*) FROM native_session_operations WHERE intent->'receipt'->>'request_id'=$2)`, b.HarnessSessionID, desk.ID).Scan(&leases, &operations); err != nil || leases != 2 || operations != 1 {
		t.Fatalf("delivery duplicated: leases=%d operations=%d err=%v", leases, operations, err)
	}
	var untouched int
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM harness_deliveries WHERE session_id=$1`, offlineSession).Scan(&untouched); err != nil || untouched != 0 {
		t.Fatal("offline session was claimed", err)
	}
	var body string
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT body FROM inbox_messages WHERE id=$1`, desk.ID).Scan(&body); err != nil || body != desk.Body {
		t.Fatal("inbox lost its durable record", err)
	}
	// Independent observed absence settles this fake owned process, without a
	// message causing stop, resume, adoption or another launch.
	c.provider.mu.Lock()
	c.provider.confirmed = true
	closes := c.provider.closes
	c.provider.mu.Unlock()
	if closes != 0 {
		t.Fatal("inbox hint signalled the owned process")
	}
	if err := c.config.Poll(t.Context()); err != nil {
		t.Fatal(err)
	}
}
