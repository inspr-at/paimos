// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/attachedmsg"
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
	if strings.Contains(d.SQL, "'aeon-pairing:'") {
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
	compat, err := inbox.NewMessaging(pool, make([]byte, 32), n.s)
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
				for _, route := range []string{"live", "omitted-session", "wrong-session", "detached"} {
					canary := "disabled-note-canary-" + attachedmsg.UUID()
					body := n.body(attachedmsg.UUID(), canary, compat)
					switch route {
					case "omitted-session":
						delete(body, "recipient_session_id")
					case "wrong-session":
						body["recipient_session_id"] = attachedmsg.UUID()
					case "detached":
						if _, err := n.f.db.Admin.Exec(t.Context(), `UPDATE harness_attach_requests SET state='detached' WHERE id=$1`, n.in.RequestID); err != nil {
							t.Fatal(err)
						}
					}
					var refusal struct{ Code string }
					decodeResult(t, n.f.call("POST", n.path(compat), body, true, "", 409), &refusal)
					if refusal.Code != "feature_disabled" {
						t.Fatalf("wrong refusal: %s", refusal.Code)
					}
					assertNoCanary(t, n.f, canary)
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
