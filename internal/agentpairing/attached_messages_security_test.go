// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/attachedmsg"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/inbox"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Each request reaches the real pairing fence before any contender proceeds.
// A timeout guards a hang; it is never evidence that requests overlapped.
type notesFenceBarrier struct {
	arrived chan struct{}
	release chan struct{}
	gate    bool
}

func (b *notesFenceBarrier) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	if strings.Contains(d.SQL, "SELECT id FROM tenants") {
		if b.gate {
			select {
			case <-b.release:
				return ctx
			default:
			}
		}
		select {
		case b.arrived <- struct{}{}:
		case <-ctx.Done():
			return ctx
		}
		if b.gate {
			select {
			case <-b.release:
			case <-ctx.Done():
			}
		}
	}
	return ctx
}
func (*notesFenceBarrier) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func fencedNotesHandler(t *testing.T, n notesFixture, participants int, gate bool) (http.Handler, *notesFenceBarrier) {
	t.Helper()
	b := &notesFenceBarrier{arrived: make(chan struct{}, participants), release: make(chan struct{}), gate: gate}
	cfg := n.f.db.App.Config()
	cfg.MaxConns = int32(participants + 1)
	cfg.ConnConfig.Tracer = b
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	mux := http.NewServeMux()
	inbox.New(pool, n.s).Mount(mux)
	compat, err := inbox.NewMessaging(pool, make([]byte, 32), inbox.WithAttachedMessages(n.s))
	if err != nil {
		t.Fatal(err)
	}
	compat.Mount(mux)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := tenant.Principal{ID: n.f.person, TenantID: n.f.tenantID, Kind: tenant.Person, BrowserSession: true, Name: "Owner"}
		mux.ServeHTTP(w, r.WithContext(tenant.WithPrincipal(r.Context(), p)))
	}), b
}

func awaitNoteFences(t *testing.T, b *notesFenceBarrier, n int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	for i := 0; i < n; i++ {
		select {
		case <-b.arrived:
		case <-ctx.Done():
			t.Fatal("request did not reach authorization fence")
		}
	}
}

func TestAttachedFinalWriteSeesRevokedOwner(t *testing.T) {
	n := readyNotes(t)
	h, b := fencedNotesHandler(t, n, 1, false)
	tx, err := n.f.db.Admin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(t.Context(), `SELECT set_config('aeon.tenant_id',$1,true)`, n.f.tenantID); err != nil {
		t.Fatal(err)
	}
	if err = attachedmsg.Lock(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	canary := "revoked-owner-canary-" + attachedmsg.UUID()
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, n.f.request("POST", n.path(false), n.body("revoked-final-write", canary, false), true, ""))
		result <- w
	}()
	awaitNoteFences(t, b, 1)
	// Revocation wins while the accepted browser identity is waiting for the
	// same fence used by grants and the final send transaction.
	if _, err = tx.Exec(t.Context(), `DELETE FROM role_bindings WHERE tenant_id=$1 AND principal_id=$2`, n.f.tenantID, n.f.person); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case w := <-result:
		var refusal struct{ Code string }
		decodeResult(t, w, &refusal)
		if w.Code != 403 || refusal.Code != "owner_authority_lost" {
			t.Fatalf("wrong revoke result: %d %s", w.Code, refusal.Code)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("send stuck after fence release")
	}
	assertNoCanary(t, n.f, canary)
}

// Used by both concurrency tests so every contender is at the lock boundary
// before release, rather than relying on goroutine scheduling or sleeps.
func concurrentNoteRequests(t *testing.T, n notesFixture, count int, key func(int) string, body func(int) string) []*httptest.ResponseRecorder {
	t.Helper()
	h, b := fencedNotesHandler(t, n, count, true)
	defer close(b.release)
	out := make([]*httptest.ResponseRecorder, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w := httptest.NewRecorder()
			h.ServeHTTP(w, n.f.request("POST", n.path(i%2 == 0), n.body(key(i), body(i), i%2 == 0), true, ""))
			out[i] = w
		}(i)
	}
	awaitNoteFences(t, b, count)
	// Let all blocked traces execute their actual transaction lock together.
	for i := 0; i < count; i++ {
		b.release <- struct{}{}
	}
	wg.Wait()
	return out
}

func TestAttachedDisabledExplicitNotesNeverPersist(t *testing.T) {
	for _, mode := range []string{"nil", "disabled", "unqualified"} {
		t.Run(mode, func(t *testing.T) {
			n := readyNotes(t)
			var disabled *attachedmsg.Service
			if mode != "nil" {
				disabled = attachedmsg.New(attachedmsg.Options{Enabled: mode == "unqualified", Origin: origin})
			}
			n.f.messages = []*attachedmsg.Service{disabled}
			n.f.rebuildHandler()
			for _, compat := range []bool{false, true} {
				// Start with the detached case: weakening the explicit-note refusal
				// must prove a durable body leak, rather than a different live fence error.
				for _, route := range []string{"detached", "live", "omitted-session", "wrong-session"} {
					canary := "disabled-note-canary-" + attachedmsg.UUID()
					body := n.body(attachedmsg.UUID(), canary, compat)
					state := "active"
					if route == "detached" {
						state = "detached"
					}
					if _, err := n.f.db.Admin.Exec(t.Context(), `UPDATE harness_attach_requests SET state=$2 WHERE id=$1`, n.in.RequestID, state); err != nil {
						t.Fatal(err)
					}
					switch route {
					case "omitted-session":
						delete(body, "recipient_session_id")
					case "wrong-session":
						body["recipient_session_id"] = attachedmsg.UUID()
					}
					w := httptest.NewRecorder()
					n.f.h.ServeHTTP(w, n.f.request("POST", n.path(compat), body, true, ""))
					assertNoCanary(t, n.f, canary)
					if w.Code != 409 {
						t.Fatalf("explicit disabled note status %d want 409", w.Code)
					}
					var refusal struct{ Code string }
					decodeResult(t, w, &refusal)
					if refusal.Code != "feature_disabled" {
						t.Fatalf("wrong refusal: %s", refusal.Code)
					}
				}
			}
			// An unrelated ordinary recipient retains its durable inbox behavior.
			var recipient string
			if err := n.f.db.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Ordinary recipient') RETURNING id::text`, n.f.tenantID).Scan(&recipient); err != nil {
				t.Fatal(err)
			}
			for _, compat := range []bool{false, true} {
				body := map[string]any{"body": "ordinary message", "idempotency_key": fmt.Sprint("ordinary-", compat)}
				if compat {
					body["to"] = recipient
				} else {
					body["recipient_principal_id"] = recipient
				}
				var msg inbox.Message
				decodeResult(t, n.f.call("POST", n.path(compat), body, true, "", 201), &msg)
				if msg.Body != "ordinary message" || msg.ContentMode != "" {
					t.Fatal("ordinary message changed")
				}
			}
		})
	}
}

// Both acceptance and the exported broker contract must retain the session
// binding and active-state lock until the final transaction commits.
func TestAttachedSessionValidityLockedThroughCommit(t *testing.T) {
	for _, entry := range []string{"authorize", "validate-grant"} {
		for _, change := range []string{"archive", "stop", "project", "principal", "harness", "host", "ticket"} {
			t.Run(entry+"/"+change, func(t *testing.T) {
				n := readyNotes(t)
				mutation := noteSessionMutation(t, n, change)
				pool, barrier, ctx := dbtest.BarrierPool(t, n.f.db.App, func(sql string) bool {
					return strings.Contains(sql, "SELECT EXISTS(SELECT 1 FROM harness_sessions")
				})
				first := make(chan error, 1)
				go func() { first <- validateNoteSession(ctx, pool, n, entry) }()
				pid := barrier.Wait(t, ctx)
				second := make(chan error, 1)
				done := make(chan struct{})
				go func() {
					err := db.InTenant(dbtest.Seed(ctx), n.f.db.Admin, n.f.tenantID, func(tx pgx.Tx) error {
						_, e := tx.Exec(ctx, mutation, n.grant.Binding.SessionID)
						return e
					})
					second <- err
					close(done)
				}()
				lock := dbtest.BlockedOrDone(t, ctx, n.f.db.Admin, pid, done)
				barrier.Release()
				if err := dbtest.Await(t, ctx, first); err != nil {
					t.Fatal("valid session refused: ", err)
				}
				if err := dbtest.Await(t, ctx, second); err != nil {
					t.Fatal("session mutation failed: ", err)
				}
				if lock != "transactionid" && lock != "tuple" {
					t.Fatalf("session validity was not protected by a row lock (%q)", lock)
				}
				assertNoteSessionChanged(t, validateNoteSession(ctx, n.f.db.App, n, entry))
			})
		}
	}
}

// A mutation that owns the row first must be observed after the lock wait.
// An earlier unlocked snapshot cannot authorize acceptance or a broker offer.
func TestAttachedSessionValidityRecheckedAfterConcurrentMutation(t *testing.T) {
	for _, entry := range []string{"authorize", "validate-grant"} {
		for _, change := range []string{"archive", "host"} {
			t.Run(entry+"/"+change, func(t *testing.T) {
				n := readyNotes(t)
				ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
				defer cancel()
				tx, err := n.f.db.Admin.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback(context.Background())
				if _, err = tx.Exec(ctx, noteSessionMutation(t, n, change), n.grant.Binding.SessionID); err != nil {
					t.Fatal(err)
				}
				pid := tx.Conn().PgConn().PID()
				result := make(chan error, 1)
				done := make(chan struct{})
				go func() {
					result <- validateNoteSession(ctx, n.f.db.App, n, entry)
					close(done)
				}()
				lock := dbtest.BlockedOrDone(t, ctx, n.f.db.Admin, pid, done)
				if err = tx.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				err = dbtest.Await(t, ctx, result)
				if lock != "transactionid" && lock != "tuple" {
					t.Fatalf("validation did not wait for the session mutation (%q)", lock)
				}
				assertNoteSessionChanged(t, err)
			})
		}
	}
}

func validateNoteSession(ctx context.Context, pool *pgxpool.Pool, n notesFixture, entry string) error {
	owner := tenant.Principal{ID: n.f.person, TenantID: n.f.tenantID, Kind: tenant.Person, BrowserSession: true, Name: "Owner"}
	r := httptest.NewRequest("POST", origin+"/api/inbox/messages", nil).WithContext(tenant.WithPrincipal(ctx, owner))
	r.Header.Set("Origin", origin)
	ctx = attachedmsg.BrowserContext(r, origin, owner)
	return db.InTenant(ctx, pool, owner.TenantID, func(tx pgx.Tx) error {
		if err := attachedmsg.Lock(ctx, tx); err != nil {
			return err
		}
		if entry == "validate-grant" {
			_, err := n.s.ValidateGrant(ctx, tx, n.grant.Binding)
			return err
		}
		_, err := n.s.Authorize(ctx, tx, owner, n.in.RequestID, attachedmsg.Send{Recipient: n.recipient, Project: n.grant.Binding.ProjectID, Session: &n.grant.Binding.SessionID, Generation: n.grant.Binding.Generation, Body: "validity test"})
		return err
	})
}

func assertNoteSessionChanged(t *testing.T, err error) {
	t.Helper()
	var refusal *attachedmsg.Error
	if !errors.As(err, &refusal) || refusal.Status != 409 || refusal.Code != "attachment_binding_changed" {
		t.Fatalf("changed session was not refused for its binding: %v", err)
	}
}

func noteSessionMutation(t *testing.T, n notesFixture, change string) string {
	t.Helper()
	assignment := ""
	switch change {
	case "archive":
		assignment = "archived_at=clock_timestamp(),stopped_at=clock_timestamp(),phase='stopped',recovery_process_state='unknown',recovery_request_id=gen_random_uuid(),recovery_request_digest='fixture'::bytea,recovery_actor_id=agent_principal_id,recovery_reason='fixture'"
	case "stop":
		assignment = "stopped_at=clock_timestamp(),phase='stopped'"
	case "host":
		assignment = "host=host||'-changed'"
	case "harness":
		assignment = "harness=CASE WHEN harness='claude' THEN 'codex' ELSE 'claude' END"
	case "ticket":
		assignment = "ticket_node_id=NULL,work_shape='unknown'"
	case "principal":
		assignment = "agent_principal_id='" + n.f.person + "'::uuid"
	case "project":
		project := attachedmsg.UUID()
		err := db.InTenant(dbtest.Seed(t.Context()), n.f.db.App, n.f.tenantID, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,id,kind_id,key,title) SELECT $1,$2,id,'OTHER-1','Other project' FROM node_kinds WHERE slug='project'`, n.f.tenantID, project)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		assignment = "project_id='" + project + "'::uuid"
	default:
		t.Fatal("unknown session mutation: ", change)
	}
	return "UPDATE harness_sessions SET " + assignment + " WHERE id=$1"
}

func TestAttachedReplyCannotBypassVolatilePolicyThroughDeskBridge(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("disabled-%t", disabled), func(t *testing.T) {
			n := readyNotes(t)
			service := n.s
			if disabled {
				service = attachedmsg.New(attachedmsg.Options{Origin: origin})
			}
			called := false
			mod, err := inbox.NewMessaging(n.f.db.App, make([]byte, 32), inbox.WithAttachedMessages(service), inbox.WithHeldReplyBridge(func(w http.ResponseWriter, _ *http.Request, _ tenant.Principal, _ string, _ inbox.HeldReplyInput) bool {
				called = true
				w.WriteHeader(201)
				return true
			}))
			if err != nil {
				t.Fatal(err)
			}
			mux := http.NewServeMux()
			mod.Mount(mux)
			body := n.body(attachedmsg.UUID(), "desk-attached-canary-"+attachedmsg.UUID(), true)
			body["reply_to"] = attachedmsg.UUID()
			r := n.f.request("POST", n.path(true), body, true, "")
			r = r.WithContext(tenant.WithPrincipal(r.Context(), tenant.Principal{ID: n.f.person, TenantID: n.f.tenantID, Kind: tenant.Person, BrowserSession: true, Name: "Owner"}))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			expected := 400
			reason := "unsupported_attached_mode"
			if disabled {
				expected = 409
				reason = "feature_disabled"
			}
			if called || w.Code != expected || !strings.Contains(w.Body.String(), reason) {
				t.Fatalf("attached reply bypassed policy: bridge=%t status=%d body=%s", called, w.Code, w.Body.String())
			}
			assertNoCanary(t, n.f, body["body"].(string))
		})
	}
}

// Gate BEGIN before the transaction can acquire its authenticating-key fence.
// Tenant-lock traces identify completed message transactions, rather than
// counting unrelated admission/read transactions or relying on scheduling.
type daemonMessageBarrier struct {
	mu                  sync.Mutex
	armed               bool
	transactions, after int
	arrived, release    chan struct{}
}

func (b *daemonMessageBarrier) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	b.mu.Lock()
	gate := b.armed && d.SQL == "begin" && b.transactions == b.after
	if gate {
		b.armed = false
	}
	if b.armed && strings.Contains(d.SQL, "SELECT id FROM tenants") {
		b.transactions = 1
	}
	b.mu.Unlock()
	if gate {
		close(b.arrived)
		select {
		case <-b.release:
		case <-ctx.Done():
		}
	}
	return ctx
}
func (*daemonMessageBarrier) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestAttachedDaemonAuthorityRecheckedInEveryTransaction(t *testing.T) {
	for _, operation := range []string{"message_request", "message_activate", "message_observed", "message_offer", "message_validate", "message_receipt", "release"} {
		for _, revoke := range []string{"key", "scope", "role"} {
			t.Run(operation+"/"+revoke, func(t *testing.T) {
				d := dbtest.Open(t)
				barrier := &daemonMessageBarrier{arrived: make(chan struct{}), release: make(chan struct{})}
				cfg := d.App.Config()
				cfg.ConnConfig.Tracer = barrier
				pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
				if err != nil {
					t.Fatal(err)
				}
				d.App.Close()
				d.App = pool
				service := qualifiedNotes()
				f, key, in := watchFixtureFromFixture(t, newFixtureInTenant(t, d, "pairtest", service), "", "codex")
				n := activateNotes(t, service, f, key, in)
				msg := n.sendNote(t, false)
				request := n.deliveryRequest("message_offer")
				if operation == "message_validate" || operation == "message_receipt" {
					offered := n.offer(t).Offer
					if offered == nil {
						t.Fatal("offer prerequisite failed")
					}
					request.Operation = operation
					request.MessageReceipt, _ = json.Marshal(attachedmsg.Receipt{DeliveryID: offered.DeliveryID, MessageID: offered.MessageID, Nonce: offered.Nonce, Epoch: offered.Epoch, Outcome: "shown"})
				} else if operation != "release" && operation != "message_offer" {
					request = n.in
					request.Operation = operation
					if operation == "message_request" {
						request.MessageGeneration = ""
						request.MessageConsentDigest = ""
					}
				}
				// Fixture states make request/activation eligible mutations, rather
				// than letting a consent-state refusal satisfy the regression.
				if operation == "message_request" || operation == "message_activate" {
					state := "approved"
					if operation == "message_request" {
						state = "pending"
					}
					if _, err = d.Admin.Exec(t.Context(), `UPDATE attached_message_grants SET state=$2,hook_observed_at=NULL,expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, n.grant.ID, state); err != nil {
						t.Fatal(err)
					}
					if operation == "message_activate" {
						if _, err = d.Admin.Exec(t.Context(), `UPDATE attached_message_grants SET expires_at=clock_timestamp()+interval '1 minute' WHERE id=$1`, n.grant.ID); err != nil {
							t.Fatal(err)
						}
					}
				}
				// Prove the request is admissible before installing the race barrier.
				f.call("GET", "/api/me", nil, false, key, 200)
				var keyID, principalID string
				if err = d.Admin.QueryRow(t.Context(), `SELECT k.id::text,k.principal_id::text FROM agent_keys k JOIN agent_pairing_computers c ON c.tenant_id=k.tenant_id AND c.key_id=k.id WHERE c.id=$1`, in.ComputerID).Scan(&keyID, &principalID); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
				defer cancel()
				var once sync.Once
				release := func() { once.Do(func() { close(barrier.release) }) }
				defer release()
				result := make(chan *httptest.ResponseRecorder, 1)
				req := f.request("POST", "/api/agent-pairing/attach", request, false, key).WithContext(ctx)
				req.Body = &admittedMessageBody{ReadCloser: req.Body, ctx: ctx, barrier: barrier, releaseTransaction: operation == "release"}
				go func() {
					w := httptest.NewRecorder()
					f.h.ServeHTTP(w, req)
					result <- w
				}()
				select {
				case <-barrier.arrived:
				case <-ctx.Done():
					t.Fatal("daemon request never reached transaction barrier")
				}
				if operation == "release" {
					var claimed, released, live bool
					if err = d.Admin.QueryRow(ctx, `SELECT terminal_outcome IS NULL,body_released_at IS NOT NULL,offer_deadline>clock_timestamp() FROM harness_deliveries WHERE message_id=$1`, msg.ID).Scan(&claimed, &released, &live); err != nil || !claimed || released || !live {
						t.Fatalf("release barrier did not follow a committed, live claim: %v", err)
					}
				}
				// Revoke using the actual key-usage and tenant fences while the request
				// is admitted but has not acquired its next mutation transaction.
				tx, err := d.Admin.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback(context.Background())
				if err = authz.LockKeyScopeUseTx(ctx, tx, f.tenantID, keyID); err != nil {
					t.Fatal(err)
				}
				if _, err = tx.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR NO KEY UPDATE`, f.tenantID); err != nil {
					t.Fatal(err)
				}
				switch revoke {
				case "key":
					_, err = tx.Exec(ctx, `UPDATE agent_keys SET revoked_at=clock_timestamp() WHERE id=$1`, keyID)
				case "scope":
					_, err = tx.Exec(ctx, `UPDATE agent_keys SET scopes=array_remove(array_remove(scopes,'harness.worker'),'harness:worker') WHERE id=$1`, keyID)
				case "role":
					_, err = tx.Exec(ctx, `DELETE FROM role_bindings WHERE tenant_id=$1 AND principal_id=$2`, f.tenantID, principalID)
				}
				if err != nil {
					t.Fatal(err)
				}
				if err = tx.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				release()
				select {
				case w := <-result:
					var refusal struct {
						Code string `json:"code"`
					}
					if json.Unmarshal(w.Body.Bytes(), &refusal) != nil || w.Code != 403 || refusal.Code != "daemon_authority_lost" {
						t.Fatalf("wrong daemon revocation result: %d %s", w.Code, w.Body.String())
					}
				case <-ctx.Done():
					t.Fatal("daemon transaction stuck after revocation")
				}
				var released, completed bool
				if err = d.Admin.QueryRow(t.Context(), `SELECT coalesce(bool_or(body_released_at IS NOT NULL),false),coalesce(bool_or(completed_at IS NOT NULL),false) FROM harness_deliveries WHERE message_id=$1`, msg.ID).Scan(&released, &completed); err != nil {
					t.Fatal(err)
				}
				if completed || (operation != "message_receipt" && operation != "message_validate" && released) {
					t.Fatal("revoked daemon released or completed a note")
				}
			})
		}
	}
}

// Reading the body proves admission succeeded. Release races arm the tracer
// here, so authentication transactions cannot satisfy the regression barrier.
type admittedMessageBody struct {
	io.ReadCloser
	ctx                context.Context
	barrier            *daemonMessageBarrier
	releaseTransaction bool
	once               sync.Once
}

func (r *admittedMessageBody) Read(p []byte) (int, error) {
	r.once.Do(func() {
		if r.releaseTransaction {
			r.barrier.mu.Lock()
			r.barrier.armed = true
			r.barrier.after = 1
			r.barrier.mu.Unlock()
		} else {
			close(r.barrier.arrived)
			select {
			case <-r.barrier.release:
			case <-r.ctx.Done():
			}
		}
	})
	return r.ReadCloser.Read(p)
}

func TestAttachedOversizeHookOutputRejectedBeforeAcceptance(t *testing.T) {
	n := readyNotes(t)
	for _, compat := range []bool{false, true} {
		for _, token := range []string{"\t", "\\", "\""} {
			body := strings.Repeat(token, 3400) + "x"
			w := n.f.call("POST", n.path(compat), n.body(attachedmsg.UUID(), body, compat), true, "", 400)
			var refusal struct {
				Code string `json:"code"`
			}
			decodeResult(t, w, &refusal)
			if refusal.Code != "attached_frame_too_large" {
				t.Fatalf("wrong size refusal: %s", refusal.Code)
			}
		}
	}
	var count int
	var tokens float64
	if err := n.f.db.Admin.QueryRow(t.Context(), `SELECT count(*) FROM inbox_messages WHERE recipient_session_id=$1`, n.grant.Binding.SessionID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rejected output persisted message metadata: %d %v", count, err)
	}
	if err := n.f.db.Admin.QueryRow(t.Context(), `SELECT message_tokens FROM harness_attach_requests WHERE id=$1`, n.in.RequestID).Scan(&tokens); err != nil || tokens != 3 {
		t.Fatalf("rejected output consumed rate quota: %f %v", tokens, err)
	}
	msg := n.sendNote(t, false)
	if offered := n.offer(t).Offer; offered == nil || offered.MessageID != msg.ID {
		t.Fatal("rejected output consumed the valid note's sole attempt")
	}
}
