// SPDX-License-Identifier: AGPL-3.0-only
package releases

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/jackc/pgx/v5"
)

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
