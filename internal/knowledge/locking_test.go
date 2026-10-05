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

func TestKnowledgeTenantTreeBeforeRowForDeleteAndUndo(t *testing.T) {
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
			// The tenant fence precedes the tree and record fences. The first
			// mutation holds it while stopped before locking the record.
			if lock != "transactionid" {
				t.Errorf("generic PATCH waited for %q instead of tenant fence", lock)
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

// Ordinary Knowledge Undo takes the tenant fence before advisory/event locks
// even with work-parent-status OFF. Pause a PATCH after its shared tree/pairing
// entry, observe Undo's tenant wait, and probe the owned tree and free event fence.
func TestKnowledgeUndoTenantBeforeSharedFencesWithWorkStatusOff(t *testing.T) {
	// This fixture needs only a project and knowledge.
	f := fixture{db: dbtest.Open(t)}
	f.a = tenant.Principal{Kind: tenant.Person, Name: "Markus Barta"}
	if err := f.db.Admin.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('undo-pairing','Undo pairing') RETURNING id::text`).Scan(&f.a.TenantID); err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.a.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person',$2) RETURNING id::text`, f.a.TenantID, f.a.Name).Scan(&f.a.ID); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title)
		 SELECT $1,'PRJ-1',id,'Project' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, f.a.TenantID).Scan(&f.project)
	}); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, f.db, f.a.TenantID, f.a.ID, "member")
	fixtureMux := http.NewServeMux()
	New(f.db.App).Mount(fixtureMux)
	f.handler = fixtureMux
	dbtest.EnableWorkParentStatus(t, f.db, f.a.TenantID)
	if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.a.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE features SET enabled=false WHERE tenant_id=$1 AND key='work-parent-status'`, f.a.TenantID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	entry := createEntry(t, f, f.a, map[string]any{"type": "runbook", "slug": "undo-pairing", "title": "Undo target"})
	other := createEntry(t, f, f.a, map[string]any{"type": "runbook", "slug": "write-pairing", "title": "Write target"})
	if entry.EventID == nil {
		t.Fatal("created entry has no event")
	}
	pool, barrier, ctx := dbtest.BarrierPool(t, f.db.App, func(sql string) bool {
		return sql == `SELECT pg_advisory_xact_lock(hashtextextended('aeon-pairing:'||current_setting('aeon.tenant_id'),0))`
	})
	mux := http.NewServeMux()
	New(pool).Mount(mux)
	write := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		r := httptest.NewRequest("PATCH", "/api/knowledge/"+other.ID, strings.NewReader(`{"title":"Concurrent write"}`)).WithContext(tenant.WithPrincipal(ctx, f.a))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		write <- w
	}()
	writerPID := barrier.Wait(t, ctx)
	undo := make(chan *httptest.ResponseRecorder, 1)
	done := make(chan struct{})
	go func() {
		m := http.NewServeMux()
		events.New(f.db.App, events.WithUndoHandlers(UndoHandlers())).Mount(m)
		r := httptest.NewRequest("POST", fmt.Sprintf("/api/events/%d/undo", *entry.EventID), nil).WithContext(tenant.WithPrincipal(ctx, f.a))
		w := httptest.NewRecorder()
		m.ServeHTTP(w, r)
		undo <- w
		close(done)
	}()
	lock := dbtest.BlockedOrDone(t, ctx, f.db.Admin, writerPID, done)
	var tenantWait bool
	if err := f.db.Admin.QueryRow(ctx, `SELECT EXISTS(
 SELECT 1 FROM pg_stat_activity a WHERE $1::int=ANY(pg_blocking_pids(a.pid))
 AND strpos(a.query,'SELECT id FROM tenants')>0
 AND NOT EXISTS(SELECT 1 FROM pg_locks l WHERE l.pid=a.pid AND l.locktype='advisory' AND l.granted))`, writerPID).Scan(&tenantWait); err != nil {
		t.Fatal(err)
	}
	probe, err := f.db.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var treeFree, eventFree bool
	err = probe.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1,0)),
	 pg_try_advisory_xact_lock(hashtextextended($2,12))`, f.a.TenantID, f.a.TenantID+":"+fmt.Sprint(*entry.EventID)).Scan(&treeFree, &eventFree)
	rollbackErr := probe.Rollback(ctx)
	barrier.Release()
	// Drain before assertions, also on the original lock-inverting code.
	writeResult := dbtest.Await(t, ctx, write)
	undoResult := dbtest.Await(t, ctx, undo)
	if err != nil || rollbackErr != nil {
		t.Fatalf("fence probe: %v; rollback: %v", err, rollbackErr)
	}
	if lock != "transactionid" || !tenantWait || treeFree || !eventFree {
		t.Fatalf("Undo must wait on tenant while PATCH owns tree and event remains free: lock=%q tenantWait=%v treeFree=%v eventFree=%v; PATCH=%d Undo=%d", lock, tenantWait, treeFree, eventFree, writeResult.Code, undoResult.Code)
	}
	expect(t, writeResult, http.StatusOK)
	expect(t, undoResult, http.StatusCreated)
	var deleted bool
	var title string
	var undoEvents int
	err = f.db.Admin.QueryRow(ctx, `SELECT
	 (SELECT deleted_at IS NOT NULL FROM nodes WHERE tenant_id=$1 AND id=$2),
	 (SELECT title FROM nodes WHERE tenant_id=$1 AND id=$3),
	 (SELECT count(*) FROM events WHERE tenant_id=$1 AND undo_of=$4)`, f.a.TenantID, entry.ID, other.ID, *entry.EventID).Scan(&deleted, &title, &undoEvents)
	if err != nil || !deleted || title != "Concurrent write" || undoEvents != 1 {
		t.Fatalf("deleted=%v title=%q undoEvents=%d: %v", deleted, title, undoEvents, err)
	}
}
