// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Risk: a dispatcher that read open can overwrite a cancellation committed
// while its PATCH waits. PostgreSQL proves the overlap before cancel commits.
func TestConditionalStatusPreservesConcurrentCancel(t *testing.T) {
	p := newPrincipal(t, "conditional-status")
	enableWorkStatusTest(t, p)
	n := workNodeTest(t, p, "", "open")
	path := "/api/nodes/" + n.ID
	for _, raw := range []string{
		`{"expected_states":["open"],"title":"Missing state"}`,
		`{"state":"in_progress","expected_states":null}`,
		`{"state":"in_progress","expected_states":[]}`,
		`{"state":"in_progress","expected_states":"open"}`,
		`{"state":"in_progress","expected_states":[null]}`,
		`{"state":"in_progress","expected_states":[" "]}`,
		`{"state":"in_progress","expected_states":["` + strings.Repeat("s", 129) + `"]}`,
		`{"state":"in_progress","expected_states":[` + strings.Repeat(`"open",`, 32) + `"open"]}`,
	} {
		code, body := call(t, &p, "PATCH", path, raw)
		if code != 400 || !strings.Contains(string(body), "expected_states") {
			t.Fatalf("invalid precondition accepted: %d %s (%s)", code, body, raw)
		}
	}
	// Any member can match; the normal transition and audit path still runs.
	code, body := call(t, &p, "PATCH", path, `{"state":"in_progress","expected_states":["qa","open"]}`)
	started := decode[nodeJSON](t, code, body, 200)
	if started.State != "in_progress" {
		t.Fatal(started.State)
	}
	code, body = call(t, &p, "PATCH", path, `{"state":"open"}`)
	n = decode[nodeJSON](t, code, body, 200)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	m := New(appPool, nil).(*Module)
	held := make(chan uint32, 1)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	cancelDone := make(chan error, 1)
	go func() {
		cancelDone <- db.InTenant(tenant.WithPrincipal(ctx, p), appPool, p.TenantID, func(tx pgx.Tx) error {
			if _, err := m.updateNodeTx(ctx, tx, p, n.ID, map[string]json.RawMessage{"state": json.RawMessage(`"cancelled"`)}, nil); err != nil {
				return err
			}
			var pid uint32
			if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				return err
			}
			held <- pid
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	var blocker uint32
	select {
	case blocker = <-held:
	case err := <-cancelDone:
		t.Fatalf("cancel did not hold the write: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	ready := make(chan uint32, 4)
	cfg := appPool.Config()
	cfg.ConnConfig.Tracer = workStatusEntryTrace{ready: ready}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	mux := http.NewServeMux()
	New(pool, nil).Mount(mux)
	r := httptest.NewRequest("PATCH", path, strings.NewReader(`{"state":"in_progress","expected_states":["open","qa"],"title":"Must not change","fields":{"priority":"low"}}`))
	r = r.WithContext(tenant.WithPrincipal(ctx, p))
	// A stale revision must not hide the more specific status conflict.
	r.Header.Set("If-Unmodified-Since", n.UpdatedAt.Format(time.RFC3339Nano))
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		result <- w
	}()
	waiter := dbtest.Await(t, ctx, ready)
	if err := dbtest.WaitForBlocked(ctx, adminPool, int(waiter), int(blocker), "SELECT aeon_work_status_begin()"); err != nil {
		t.Fatal(err)
	}
	unblock()
	if err := dbtest.Await(t, ctx, cancelDone); err != nil {
		t.Fatal(err)
	}
	w := dbtest.Await(t, ctx, result)
	var failure errorBody
	if err := json.Unmarshal(w.Body.Bytes(), &failure); err != nil {
		t.Fatal(err)
	}
	if w.Code != 409 || failure.Code != "status_mismatch" || failure.CurrentState == nil || *failure.CurrentState != "cancelled" {
		t.Fatalf("cancel overwritten or wrong refusal: %d %s", w.Code, w.Body.String())
	}
	after := currentWorkTest(t, p, n.ID)
	if after.State != "cancelled" || after.Title != n.Title || string(after.Fields) != string(n.Fields) {
		t.Fatalf("rejected patch changed the ticket: %+v", after)
	}
	var updates int
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE node_id=$1 AND type='node.updated'`, n.ID).Scan(&updates)
	}); err != nil {
		t.Fatal(err)
	}
	if updates != 3 { // successful start, reopen, cancel; no rejected PATCH event
		t.Fatalf("conditional refusal appended an event: %d", updates)
	}
	// Even a matching status cannot bypass the independent revision check.
	r = httptest.NewRequest("PATCH", path, strings.NewReader(`{"state":"open","expected_states":["cancelled"]}`))
	r = r.WithContext(tenant.WithPrincipal(t.Context(), p))
	r.Header.Set("If-Unmodified-Since", n.UpdatedAt.Format(time.RFC3339Nano))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 412 {
		t.Fatalf("matching status bypassed revision: %d %s", w.Code, w.Body.String())
	}
	agent := estimateAgent(t, p)
	agent.Scopes = []string{"nodes.read"}
	code, body = call(t, &agent, "PATCH", path, `{"state":"in_progress","expected_states":["open"]}`)
	if code != 403 || strings.Contains(string(body), "current_state") {
		t.Fatalf("unauthorized precondition exposed status: %d %s", code, body)
	}
	other := addPrincipal(t, "conditional-status-other")
	code, body = call(t, &other, "PATCH", path, `{"state":"in_progress","expected_states":["open"]}`)
	if code != 404 || strings.Contains(string(body), "current_state") {
		t.Fatalf("cross-tenant precondition exposed status: %d %s", code, body)
	}
	// Existing clients can still intentionally reopen without the precondition.
	code, body = call(t, &p, "PATCH", path, `{"state":"open"}`)
	if got := decode[nodeJSON](t, code, body, 200); got.State != "open" {
		t.Fatal(fmt.Sprintf("unconditional behavior changed: %+v", got))
	}
}
