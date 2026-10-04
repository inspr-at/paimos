// SPDX-License-Identifier: AGPL-3.0-only
package knowledge

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/nodes"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestKnowledgeTenantBeforeTreeAndRowForDeleteAndUndo(t *testing.T) {
	for _, action := range []string{"delete", "undo-create", "undo-delete", "undo-update"} {
		t.Run(action, func(t *testing.T) {
			f := setup(t)
			e := createEntry(t, f, f.a, map[string]any{"type": "runbook", "slug": "lock-order", "title": "Lock order"})
			method, path := "DELETE", "/api/knowledge/"+e.ID
			if strings.HasPrefix(action, "undo-") {
				if action == "undo-delete" {
					expect(t, call(t, f, f.a, "DELETE", path, nil), 200)
				}
				if action == "undo-update" {
					expect(t, call(t, f, f.a, "PATCH", path, map[string]any{"title": "Changed"}), 200)
				}
				var id int64
				if err := f.db.Admin.QueryRow(t.Context(), `SELECT max(id) FROM events WHERE tenant_id=$1 AND node_id=$2`, f.a.TenantID, e.ID).Scan(&id); err != nil {
					t.Fatal(err)
				}
				method, path = "POST", fmt.Sprintf("/api/events/%d/undo", id)
			}
			pool, barrier, ctx := dbtest.BarrierPool(t, f.db.App, func(sql string) bool {
				return strings.Contains(sql, "FROM nodes WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE")
			})
			mux := http.NewServeMux()
			New(pool).Mount(mux)
			events.New(pool, events.WithUndoHandlers(UndoHandlers())).Mount(mux)
			first := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				r := httptest.NewRequest(method, path, nil).WithContext(tenant.WithPrincipal(ctx, f.a))
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, r)
				first <- w
			}()
			pid := barrier.Wait(t, ctx)
			// Tenant serialization alone would hide a missing tree lock.
			// Probe it independently at the row-lock boundary.
			if err := db.InTenant(ctx, f.db.App, f.a.TenantID, func(tx pgx.Tx) error {
				var free bool
				if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1::text, 0))`, f.a.TenantID).Scan(&free); err != nil {
					return err
				}
				if free {
					t.Error("knowledge writer reached row lock without the tree fence")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}

			second := make(chan *httptest.ResponseRecorder, 1)
			done := make(chan struct{})
			go func() {
				m := http.NewServeMux()
				nodes.New(f.db.App, nil).Mount(m)
				id := e.ID
				if action == "undo-delete" {
					// Generic PATCH rejects deleted rows during authorization;
					// another live row still shares the tenant's tree fence.
					id = f.ticket
				}
				r := httptest.NewRequest("PATCH", "/api/nodes/"+id, strings.NewReader(`{"title":"Concurrent"}`)).WithContext(tenant.WithPrincipal(ctx, f.a))
				w := httptest.NewRecorder()
				m.ServeHTTP(w, r)
				second <- w
				close(done)
			}()
			lock := dbtest.BlockedOrDone(t, ctx, f.db.Admin, pid, done)
			barrier.Release()
			if lock != "transactionid" {
				t.Errorf("generic PATCH waited for %q instead of the tenant access fence", lock)
			}
			firstStatus := 201
			if action == "delete" {
				firstStatus = 200
			}
			expect(t, dbtest.Await(t, ctx, first), firstStatus)
			w := dbtest.Await(t, ctx, second)
			want := 404
			if action == "undo-delete" || action == "undo-update" {
				want = 200
			}
			expect(t, w, want)
		})
	}
}

func TestKnowledgeLockedEligibilityAfterConcurrentDelete(t *testing.T) {
	f := setup(t)
	e := createEntry(t, f, f.a, map[string]any{"type": "runbook", "slug": "delete-race", "title": "Delete race"})
	// Call the same lockNode primitive from two transactions to isolate the
	// eligibility race, independently of the outer tree serialization.
	pool, barrier, ctx := dbtest.BarrierPool(t, f.db.App, func(sql string) bool {
		return strings.Contains(sql, "FROM nodes WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE")
	})
	remove := func(ctx context.Context, tx pgx.Tx) error {
		before, _, err := lockNode(ctx, tx, f.a.TenantID, e.ID, false)
		if err != nil {
			return err
		}
		after, err := scanSnap(tx.QueryRow(ctx, `UPDATE nodes SET deleted_at=clock_timestamp(),updated_at=`+bumpUpdated+` WHERE id=$1 RETURNING `+nodeReturning, e.ID))
		if err != nil {
			return err
		}
		_, err = events.Append(ctx, tx, f.a, events.Change{NodeID: &e.ID, Type: evDeleted, Before: before, After: after})
		return err
	}
	ctx = tenant.WithPrincipal(ctx, f.a)
	first := make(chan error, 1)
	go func() {
		first <- db.InTenant(ctx, pool, f.a.TenantID, func(tx pgx.Tx) error { return remove(ctx, tx) })
	}()
	pid := barrier.Wait(t, ctx)
	second := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		second <- db.InTenant(ctx, pool, f.a.TenantID, func(tx pgx.Tx) error { return remove(ctx, tx) })
		close(done)
	}()
	if dbtest.BlockedOrDone(t, ctx, f.db.Admin, pid, done) == "" {
		t.Fatal("second delete did not overlap")
	}
	barrier.Release()
	if err := dbtest.Await(t, ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := dbtest.Await(t, ctx, second); err != errNotFound {
		t.Fatalf("second delete = %v, want not found", err)
	}
	var count int
	var eventID int64
	if err := f.db.Admin.QueryRow(ctx, `SELECT count(*),max(id) FROM events WHERE tenant_id=$1 AND node_id=$2 AND type=$3`, f.a.TenantID, e.ID, evDeleted).Scan(&count, &eventID); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("deletion events=%d, want 1", count)
	}
	expect(t, call(t, f, f.a, "POST", fmt.Sprintf("/api/events/%d/undo", eventID), nil), 201)
	expect(t, call(t, f, f.a, "GET", "/api/knowledge/"+e.ID, nil), 200)
}
