// SPDX-License-Identifier: AGPL-3.0-only
package releases

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestWorkParentPlacementRechecksRevokedGrantUnderFence(t *testing.T) {
	f := ticketSetup(t)
	f.existing("work", f.feature, "Leaf", "open")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	held, err := f.db.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Rollback(ctx)
	if _, err = held.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('aeon-pairing:'||$1,0))`, f.person.TenantID); err != nil {
		t.Fatal(err)
	}
	result := make(chan *httptest.ResponseRecorder, 1)
	raw, _ := json.Marshal(membershipInput{Revision: 1, IDs: []string{f.feature}})
	go func() {
		req := httptest.NewRequest(http.MethodPost, f.membershipPath(), strings.NewReader(string(raw))).WithContext(tenant.WithPrincipal(ctx, f.person))
		w := httptest.NewRecorder()
		f.mux.ServeHTTP(w, req)
		result <- w
	}()
	for {
		var waiting bool
		err = held.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks mine JOIN pg_locks waiter ON waiter.locktype=mine.locktype AND waiter.database IS NOT DISTINCT FROM mine.database AND waiter.classid=mine.classid AND waiter.objid=mine.objid AND waiter.objsubid=mine.objsubid WHERE mine.pid=pg_backend_pid() AND mine.locktype='advisory' AND mine.granted AND waiter.pid<>mine.pid AND NOT waiter.granted)`).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case w := <-result:
			t.Fatalf("request bypassed lock barrier: %d %s", w.Code, w.Body.String())
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		default:
		}
	}
	if _, err = held.Exec(ctx, `DELETE FROM role_bindings WHERE tenant_id=$1 AND principal_id=$2`, f.person.TenantID, f.person.ID); err != nil {
		t.Fatal(err)
	}
	if err = held.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case w := <-result:
		if w.Code != 403 || !strings.Contains(w.Body.String(), "project access required") {
			t.Fatalf("wrong rejection after revocation: %d %s", w.Code, w.Body.String())
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var count int
	if err = f.db.Admin.QueryRow(ctx, `SELECT (SELECT count(*) FROM journey_tickets WHERE tenant_id=$1)+(SELECT count(*) FROM work_parent_releases WHERE tenant_id=$1)`, f.person.TenantID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("revoked placement left writes: count=%d err=%v", count, err)
	}
}

func TestWorkParentPlacementBoundAndWorkEdges(t *testing.T) {
	f := ticketSetup(t)
	leaf := f.existing("work", f.feature, "Leaf", "open")
	memory := f.existing("memory", f.feature, "Notes", "open")
	hiddenUnderMemory := f.existing("work", memory, "Separate work", "open")
	out := membershipOK(t, f.addExisting([]string{f.feature}, 1, false))
	if len(out.LeafIDs) != 1 || out.LeafIDs[0] != leaf {
		t.Fatalf("non-work edge changed scope: %v", out.LeafIDs)
	}
	if row := f.readMembership(hiddenUnderMemory)[0]; row.ReleaseCount != 0 {
		t.Fatalf("placement crossed non-work edge: %+v", row)
	}
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,parent_id,title) SELECT $1,k.id,aeon_next_node_key($1,k.short_prefix),$2,'Wide leaf '||i FROM node_kinds k CROSS JOIN generate_series(1,1000) i WHERE k.slug='work'`, f.person.TenantID, f.feature)
		return err
	})
	// Each inherited create changed the release revision. Obtain the real fence
	// so the assertion cannot pass merely because the request was stale.
	var rev int64
	f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT revision FROM journey_releases WHERE release_node_id=$1`, f.release).Scan(&rev)
	})
	raw, _ := json.Marshal(membershipInput{Revision: rev, IDs: []string{f.feature}})
	w := f.request(f.person, http.MethodPost, f.membershipPath(), string(raw))
	if w.Code != 409 || !strings.Contains(w.Body.String(), "exceeds 1000") {
		t.Fatalf("wrong expansion failure: %d %s", w.Code, w.Body.String())
	}
}

func TestWorkParentPlacementAndFutureLeaves(t *testing.T) {
	f := ticketSetup(t)
	nested := f.existing("work", f.feature, "Nested parent", "open")
	a := f.existing("work", nested, "First leaf", "open")
	b := f.existing("work", f.feature, "Second leaf", "open")
	f.existing("memory", f.feature, "Non-work child", "open")
	out := membershipOK(t, f.addExisting([]string{f.feature, a}, 1, false))
	if len(out.Walker.Tickets) != 2 {
		t.Fatalf("leaves counted: %+v", out.Walker.Tickets)
	}
	rows := f.readMembership(f.feature, nested, a, b)
	for _, row := range rows {
		if row.ReleaseCount != 1 || row.ReleaseID == nil || *row.ReleaseID != f.release {
			t.Fatalf("ships in: %+v", row)
		}
	}
	if !rows[0].IsParent || !rows[1].IsParent || rows[2].IsParent {
		t.Fatalf("shape: %+v", rows)
	}
	fresh := f.existing("work", nested, "New leaf", "open")
	got := f.readMembership(fresh)[0]
	if got.ReleaseID == nil || *got.ReleaseID != f.release {
		t.Fatalf("new leaf did not inherit: %+v", got)
	}
	f.tx(func(tx pgx.Tx) error {
		var count, parents int
		err := tx.QueryRow(t.Context(), `SELECT count(*),count(*) FILTER(WHERE ticket_node_id=ANY($2::uuid[])) FROM journey_tickets WHERE release_node_id=$1`, f.release, []string{f.feature, nested}).Scan(&count, &parents)
		if count != 3 || parents != 0 {
			t.Errorf("scope count=%d parents=%d", count, parents)
		}
		return err
	})
	events.New(f.db.App, events.WithUndoHandlers(UndoHandlers())).Mount(f.mux)
	if w := f.request(f.person, http.MethodPost, fmt.Sprintf("/api/events/%d/undo", out.EventID), ""); w.Code != 409 {
		t.Fatalf("stale undo after new leaf: %d %s", w.Code, w.Body.String())
	}
}

func TestWorkParentPlacementUndoRestoresFutureLeafIntent(t *testing.T) {
	f := ticketSetup(t)
	leaf := f.existing("work", f.feature, "Leaf", "open")
	out := membershipOK(t, f.addExisting([]string{f.feature}, 1, false))
	events.New(f.db.App, events.WithUndoHandlers(UndoHandlers())).Mount(f.mux)
	if w := f.request(f.person, http.MethodPost, fmt.Sprintf("/api/events/%d/undo", out.EventID), ""); w.Code != 201 {
		t.Fatalf("undo parent: %d %s", w.Code, w.Body.String())
	}
	fresh := f.existing("work", f.feature, "After undo", "open")
	for _, row := range f.readMembership(leaf, fresh, f.feature) {
		if row.ReleaseCount != 0 {
			t.Fatalf("undo left intent or membership: %+v", row)
		}
	}
}

func TestWorkParentFrozenReleaseLeavesScopeUnchanged(t *testing.T) {
	for _, state := range []string{"building", "candidate", "deploying", "access", "released", "superseded"} {
		t.Run(state, func(t *testing.T) {
			f := ticketSetup(t)
			original := f.existing("work", f.feature, "Original", "open")
			membershipOK(t, f.addExisting([]string{f.feature}, 1, false))
			f.tx(func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE journey_releases SET state=$2,released_at=CASE WHEN $2 IN ('released','superseded') THEN now() END WHERE release_node_id=$1`, f.release, state)
				return err
			})
			fresh := f.existing("work", f.feature, "Later leaf", "open")
			got := f.readMembership(fresh)[0]
			if got.ReleaseCount != 0 || got.InheritanceNote == nil || *got.InheritanceNote != "parent_release_closed" {
				t.Fatalf("backlog note: %+v", got)
			}
			f.tx(func(tx pgx.Tx) error {
				var nodeState string
				var count int
				if err := tx.QueryRow(t.Context(), `SELECT state FROM nodes WHERE id=$1`, fresh).Scan(&nodeState); err != nil {
					return err
				}
				if nodeState != "backlog" {
					t.Errorf("state=%s", nodeState)
				}
				if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM journey_tickets WHERE release_node_id=$1 AND ticket_node_id=$2`, f.release, original).Scan(&count); err != nil {
					return err
				}
				if count != 1 {
					t.Error("original scope changed")
				}
				return nil
			})
		})
	}
}

func TestWorkParentSplitShipsInAndAtomicFrozenRejection(t *testing.T) {
	f := ticketSetup(t)
	a := f.existing("work", f.feature, "A", "open")
	b := f.existing("work", f.feature, "B", "open")
	second := f.existing("release", f.project, "Other marketing name", "open")
	f.tx(func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO journey_releases(tenant_id,release_node_id,project_node_id,number,state,released_at) VALUES($1,$2,$3,2,'released',now())`, f.person.TenantID, second, f.project); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO journey_tickets(tenant_id,ticket_node_id,project_node_id,release_node_id,walker_position,source) VALUES($1,$2,$4,$5,0,'manual'),($1,$3,$4,$6,1,'manual')`, f.person.TenantID, a, b, f.project, f.release, second)
		return err
	})
	row := f.readMembership(f.feature)[0]
	if !row.IsParent || row.ReleaseCount != 2 || row.ReleaseID != nil || len(row.Releases) != 2 {
		t.Fatalf("split summary: %+v", row)
	}
	w := f.addExisting([]string{f.feature}, 1, true)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "released or active") {
		t.Fatalf("wrong frozen failure: %d %s", w.Code, w.Body.String())
	}
	row = f.readMembership(f.feature)[0]
	if row.ReleaseCount != 2 {
		t.Fatal("rejected placement changed leaves")
	}
	f.tx(func(tx pgx.Tx) error {
		var count int
		err := tx.QueryRow(t.Context(), `SELECT count(*) FROM work_parent_releases`).Scan(&count)
		if count != 0 {
			t.Error("rejected batch left intent")
		}
		return err
	})
}

// A selected parent may include its done descendants, but must not relax the
// closed-leaf rule for an unrelated directly selected leaf in the same batch.
func TestWorkParentPlacementDoesNotPermitUnrelatedClosedLeaf(t *testing.T) {
	f := ticketSetup(t)
	child := f.existing("work", f.feature, "Closed descendant", "done")
	other := f.existing("work", f.project, "Unrelated closed leaf", "done")
	w := f.addExisting([]string{f.feature, other}, 1, false)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "closed tickets") {
		t.Fatalf("wrong rejection: %d %s", w.Code, w.Body.String())
	}
	for _, row := range f.readMembership(f.feature, child, other) {
		if row.ReleaseCount != 0 {
			t.Fatalf("rejected batch wrote membership: %+v", row)
		}
	}
	out := membershipOK(t, f.addExisting([]string{f.feature}, 1, false))
	if len(out.Walker.Tickets) != 1 || out.Walker.Tickets[0].NodeID != child {
		t.Fatalf("parent placement missed its closed descendant: %+v", out.Walker.Tickets)
	}
}
