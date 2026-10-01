// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"encoding/json"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

func TestWorkStartSnapshotBaselineHistoryAndCostScope(t *testing.T) {
	w := planningSetup(t)
	n := w.node(t, "SNAP-1", "ticket", w.root.ID, "open", map[string]any{"route_role": "build-hard", "area": "backend", "estimate_hours": 2})
	capture := func() {
		t.Helper()
		if err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error { return CapturePlanningStart(t.Context(), tx, n.ID, "session") }); err != nil {
			t.Fatal(err)
		}
	}
	capture()
	capture()
	path := "/api/nodes?within=" + w.root.ID + "&q=SNAP-1"
	first := planningOf(t, w.admin, path)[n.Key].Snapshot
	if first == nil || first.Hours == nil || *first.Hours != 2 || first.Tokens == nil || *first.Tokens != 10_000_000 || first.Route == nil || first.Cost == nil || first.RateBasis.ListPerHour == nil {
		t.Fatalf("baseline: %+v", first)
	}
	// Status start coalesces with the earlier session; re-estimation is live only.
	code, raw := call(t, &w.admin, "PATCH", "/api/nodes/"+n.ID, `{"state":"in_progress","fields":{"estimate_hours":4}}`)
	decode[nodeJSON](t, code, raw, 200)
	changed := planningOf(t, w.admin, path)[n.Key]
	if changed.Snapshot.ID != first.ID || *changed.Snapshot.Hours != 2 || *changed.Tokens.Estimated != 20_000_000 {
		t.Fatalf("reestimate replaced baseline: %+v", changed)
	}
	hidden := planningOf(t, w.viewer, path)[n.Key].Snapshot
	b, _ := json.Marshal(hidden)
	if hidden == nil || hidden.Cost != nil || hidden.RateBasis.ListPerHour != nil || hidden.RateBasis.Input != nil {
		t.Fatalf("snapshot cost leaked: %s", b)
	}
	code, raw = call(t, &w.admin, "PATCH", "/api/nodes/"+n.ID, `{"state":"open"}`)
	decode[nodeJSON](t, code, raw, 200)
	code, raw = call(t, &w.admin, "PATCH", "/api/nodes/"+n.ID, `{"state":"in_progress"}`)
	decode[nodeJSON](t, code, raw, 200)
	latest := planningOf(t, w.admin, path)[n.Key].Snapshot
	if latest.ID == first.ID || *latest.Hours != 4 {
		t.Fatalf("restart: %+v", latest)
	}
	var count int
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM ticket_estimate_snapshots WHERE ticket_node_id=$1`, n.ID).Scan(&count)
	}); err != nil || count != 2 {
		t.Fatalf("history %d: %v", count, err)
	}
}
