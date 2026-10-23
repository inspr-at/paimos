// SPDX-License-Identifier: AGPL-3.0-only
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	guuid "github.com/google/uuid"
	"github.com/inspr-at/paimos/internal/inbox"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/jackc/pgx/v5"
)

// Keep the real paired runtime proof, but give each direct store call a fresh
// test context instead of retaining an HTTP request's cancelled context.
func inboxRecoveryFixture(t *testing.T) (*productionComposition, nativecontract.Binding, context.Context, nativecontract.DaemonActor, *atomic.Int64) {
	t.Helper()
	requests := make(chan *http.Request, 1)
	c := composeProduction(t, compositionOptions{disableRetention: true, onDaemonRequest: func(r *http.Request) {
		if r.URL.Path == "/api/agentd/native-sessions/commands" {
			select {
			case requests <- r.Clone(context.Background()):
			default:
			}
		}
	}})
	f := c.f
	clock := &atomic.Int64{}
	clock.Store(f.now.UnixNano())
	f.s.now = func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	f.begin(t)
	out, err := c.config.Host.Start(t.Context(), nativecontract.LocalStart{Intent: f.intent, Cwd: c.root, AccountKey: "real-own-account"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.config.Poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	var request *http.Request
	select {
	case request = <-requests:
	case <-time.After(15 * time.Second):
		t.Fatal("missing authenticated command poll")
	}
	ctx, d, err := f.s.DaemonRequest(request.WithContext(f.daemonCtx(t)))
	if err != nil {
		t.Fatal(err)
	}
	return c, *out.Session.CurrentBinding, ctx, d, clock
}

// Risk: an unclaimed native lease hides its durable message forever after
// expiry, a refused claim, or server queue loss, and blocks every later input.
// A successful claim is the negative control: queue loss must never release a
// possibly written input. The store clock advances without sleeping.
func TestNativeInboxUnclaimedLeaseRecovery(t *testing.T) {
	for _, scenario := range []string{"expired", "queue lost", "claim refused", "claim expired", "claimed queue lost"} {
		t.Run(scenario, func(t *testing.T) {
			c, b, ctx, d, clock := inboxRecoveryFixture(t)
			f := c.f
			send := func(body string) inbox.Message {
				t.Helper()
				var message inbox.Message
				f.seed(t, func(tx pgx.Tx) error {
					var err error
					message, err = inbox.AcceptMessageTx(t.Context(), tx, f.person, inbox.Acceptance{RecipientPrincipalID: f.agent.ID, RecipientSessionID: &b.HarnessSessionID, Body: body, IdempotencyKey: guuid.NewString()})
					return err
				})
				return message
			}
			message := send("durable message before claim")
			page, err := f.s.Commands(ctx, d, nativecontract.PageQuery{})
			if err != nil || len(page.Items) != 1 || page.Items[0].RequestID != message.ID {
				t.Fatal("message was not admitted exactly once", page, err)
			}
			item := page.Items[0]
			claim := nativecontract.Claim{RequestID: guuid.NewString(), OperationID: item.OperationID, DaemonID: d.DaemonID, DaemonGeneration: d.DaemonGeneration, LedgerGeneration: d.LedgerGeneration, CapabilityDigest: d.CapabilityDigest, ExpectedRevision: b.BindingRevision, ClaimKind: "command"}
			if scenario == "claimed queue lost" {
				if _, err := f.s.claimCommand(ctx, d, claim); err != nil {
					t.Fatal("valid claim was refused", err)
				}
			}
			if scenario == "queue lost" || scenario == "claimed queue lost" {
				f.s.commandMu.Lock()
				f.s.commands = map[string]queuedCommand{}
				f.s.commandMu.Unlock()
			} else if scenario == "claim refused" {
				f.seed(t, func(tx pgx.Tx) error {
					_, err := tx.Exec(t.Context(), `UPDATE inbox_messages SET acked_at=clock_timestamp() WHERE id=$1`, message.ID)
					return err
				})
			}
			if scenario == "expired" || scenario == "claim expired" || scenario == "claimed queue lost" {
				clock.Store(item.ExpiresAt.Add(time.Second).UnixNano())
			}
			if scenario == "claim refused" || scenario == "claim expired" {
				_, err := f.s.claimCommand(ctx, d, claim)
				want := nativecontract.ReasonPermissionRevoked
				if scenario == "claim expired" {
					want = nativecontract.ReasonGrantExpired
				}
				var refusal *Failure
				if !errors.As(err, &refusal) || refusal.Reason != want {
					t.Fatalf("claim failed for the wrong reason: %v, want %s", err, want)
				}
			} else if _, err := f.s.Commands(ctx, d, nativecontract.PageQuery{}); err != nil {
				t.Fatal(err)
			}
			var released bool
			if err := f.d.Admin.QueryRow(t.Context(), `SELECT released_at IS NOT NULL FROM harness_deliveries WHERE id=$1`, item.OperationID).Scan(&released); err != nil {
				t.Fatal(err)
			}
			if scenario == "claimed queue lost" {
				if released {
					t.Fatal("queue loss released a possibly written input")
				}
				var state string
				if err := f.d.Admin.QueryRow(t.Context(), `SELECT receipt->>'state' FROM native_session_receipts WHERE operation_id=$1`, item.OperationID).Scan(&state); err != nil || state != "accepted" {
					t.Fatal("successful claim lacks durable evidence", state, err)
				}
				return
			}
			if !released {
				t.Fatal("unclaimed native lease still hides its inbox message")
			}
			f.s.commandMu.Lock()
			_, retained := f.s.commands[item.OperationID]
			f.s.commandMu.Unlock()
			if retained {
				t.Fatal("retired input still occupies the live queue")
			}
			if scenario != "claim refused" {
				mux := http.NewServeMux()
				inbox.New(f.d.App).Mount(mux)
				r := httptest.NewRequest("GET", "/api/inbox/messages?session="+b.HarnessSessionID+"&exact_session=true", nil).WithContext(f.daemonCtx(t))
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, r)
				var fallback inbox.Page
				if err := json.Unmarshal(w.Body.Bytes(), &fallback); err != nil || w.Code != http.StatusOK || len(fallback.Items) != 1 || fallback.Items[0].ID != message.ID || fallback.Items[0].Body != message.Body {
					t.Fatalf("durable inbox fallback unavailable: %d %s (%v)", w.Code, w.Body.String(), err)
				}
			}
			next := send("next live message")
			page, err = f.s.Commands(ctx, d, nativecontract.PageQuery{})
			if err != nil || len(page.Items) != 1 || page.Items[0].RequestID != next.ID {
				t.Fatal("retired entry blocked the next message or was reinjected", page, err)
			}
		})
	}
}

// Risk: a long native turn consumes an inbox input's five-minute claim window.
// The inbox must remain available while busy, then be admitted at the observed
// completion boundary. Clock advancement and receipt channels prove both sides.
func TestNativeInboxLongTurnKeepsMessageAvailable(t *testing.T) {
	c, b, ctx, d, clock := inboxRecoveryFixture(t)
	f := c.f
	input := nativecontract.Input{RequestID: guuid.NewString(), InputID: guuid.NewString(), ExpectedBinding: b, Mode: "now", Text: "long person turn", ExpiresAt: f.now.Add(time.Minute)}
	var accepted nativecontract.OperationResult
	c.personCall(t, "/api/projects/"+f.project+"/native-sessions/"+b.OwnedSessionID+"/input", input, &accepted)
	if out, err := c.config.Host.Input(t.Context(), input); err != nil || out.Receipt.State != "write_acknowledged" {
		t.Fatal("long turn not established", out, err)
	}
	var message inbox.Message
	f.seed(t, func(tx pgx.Tx) error {
		var err error
		message, err = inbox.AcceptMessageTx(t.Context(), tx, f.person, inbox.Acceptance{RecipientPrincipalID: f.agent.ID, RecipientSessionID: &b.HarnessSessionID, Body: "answer during a long turn", IdempotencyKey: guuid.NewString()})
		return err
	})
	for range 2 {
		page, err := f.s.Commands(ctx, d, nativecontract.PageQuery{})
		if err != nil || len(page.Items) != 0 {
			t.Fatal("inbox input was admitted before the provider's safe boundary", page, err)
		}
		clock.Store(f.now.Add(6 * time.Minute).UnixNano())
	}
	var leases int
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM harness_deliveries WHERE message_id=$1`, message.ID).Scan(&leases); err != nil || leases != 0 {
		t.Fatal("busy inbox message was hidden behind a native lease", leases, err)
	}
	turn := guuid.NewString()
	c.provider.turn("turn_started", turn, 2)
	select {
	case <-c.turnStarted:
	case <-time.After(15 * time.Second):
		t.Fatal("missing correlated turn start")
	}
	c.provider.turn("turn_completed", turn, 3)
	select {
	case <-c.turnDone:
	case <-time.After(15 * time.Second):
		t.Fatal("missing correlated turn completion")
	}
	page, err := f.s.Commands(ctx, d, nativecontract.PageQuery{})
	if err != nil || len(page.Items) != 1 || page.Items[0].RequestID != message.ID || !page.Items[0].ExpiresAt.After(f.s.now()) {
		t.Fatal("safe boundary did not admit a fresh inbox claim window", page, err)
	}
}

// Risk: queue/lease acceptance is presented as delivery, input is duplicated on
// repeated hints, Desk answers wait behind ordinary messages, or an offline
// generation is stolen. Subscription and correlated receipt channels establish
// every boundary; only the provider fixture is synthetic.
func exerciseLiveInbox(t *testing.T) {
	hints, subscribed, acknowledged := make(chan struct{}, 8), make(chan struct{}, 1), make(chan struct{}, 4)
	uncertain := make(chan struct{}, 2)
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
			if r.State == "uncertain" {
				uncertain <- struct{}{}
			}
		},
	})
	f := c.f
	c.provider.written = make(chan nativecontract.Input, 4)
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
		desk = inbox.Message{ID: guuid.NewString(), Body: `{"type":"question.answer","answer":"recorded human decision"}`}
		err = inbox.RecordDeskReply(t.Context(), tx, f.person, inbox.DeskReply{ID: desk.ID, RootID: guuid.NewString(), ProjectID: f.project, RecipientID: f.agent.ID, RecipientSessionID: b.HarnessSessionID, Body: desk.Body, RootBody: "Decision Desk fixture", RootSenderSessionID: b.HarnessSessionID, RecipientName: f.agent.Name})
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
	// Lost native acknowledgement leaves an unresolved P3a hold. Neither a
	// duplicate hint nor a polling retry may inject it again; late correlated
	// turn evidence can still advance the same durable inbox delivery.
	turn = guuid.NewString()
	c.provider.turn("turn_started", turn, 5)
	wait(c.turnStarted)
	c.provider.turn("turn_completed", turn, 6)
	wait(c.turnDone)
	for len(c.provider.written) > 0 {
		<-c.provider.written
	}
	c.provider.mu.Lock()
	c.provider.unconfirmed = true
	c.provider.mu.Unlock()
	var late inbox.Message
	f.seed(t, func(tx pgx.Tx) error {
		late, err = inbox.AcceptMessageTx(t.Context(), tx, f.person, inbox.Acceptance{RecipientPrincipalID: f.agent.ID, RecipientSessionID: &b.HarnessSessionID, Body: "uncertain delivery", IdempotencyKey: guuid.NewString()})
		return err
	})
	hints <- struct{}{}
	select {
	case wrote := <-c.provider.written:
		if wrote.InputID != late.ID {
			t.Fatal("wrong uncertain input")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("uncertain input never reached its write boundary")
	}
	if err := c.config.Poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	wait(uncertain)
	check(late.ID, false, false)
	if err := c.config.Poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	c.provider.mu.Lock()
	sequence = c.provider.sequence
	c.provider.mu.Unlock()
	if sequence != 7 {
		t.Fatal("uncertain native input was replayed")
	}
	c.provider.turn("write_acknowledged", "", 8)
	wait(acknowledged)
	check(late.ID, true, false)
	turn = guuid.NewString()
	c.provider.turn("turn_started", turn, 9)
	wait(c.turnStarted)
	check(late.ID, true, true)
	var delivered, seen bool
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT body_released_at IS NOT NULL,shown_at IS NOT NULL FROM harness_deliveries WHERE message_id=$1`, late.ID).Scan(&delivered, &seen); err != nil || !delivered || !seen {
		t.Fatal("late native evidence did not resolve delivery truth", err)
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
