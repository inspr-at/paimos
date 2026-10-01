// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
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
	// A later list-price version changes live planning, never the frozen basis.
	priceVersion := first.RateBasis.PriceVersion
	if priceVersion == nil {
		t.Fatal("snapshot missing rate version")
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO model_prices(tenant_id,model,version,input_usd_per_million,output_usd_per_million,cached_input_usd_per_million)
            VALUES($1,'gpt-6-astra',$2,20,100,4)`, w.admin.TenantID, *priceVersion+1)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// Status start coalesces with the earlier session; re-estimation is live only.
	code, raw := call(t, &w.admin, "PATCH", "/api/nodes/"+n.ID, `{"state":"in_progress","fields":{"estimate_hours":4}}`)
	decode[nodeJSON](t, code, raw, 200)
	changed := planningOf(t, w.admin, path)[n.Key]
	if changed.Snapshot.RateBasis.PriceVersion == nil || *changed.Snapshot.RateBasis.PriceVersion != *priceVersion || changed.Snapshot.Cost == nil || *changed.Snapshot.Cost != *first.Cost || changed.Snapshot.ID != first.ID || *changed.Snapshot.Hours != 2 || *changed.Tokens.Estimated != 20_000_000 {
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
	destination := mustNode(t, w.admin, `{"kind_id":"`+kindBySlug(t, w.admin, "project").ID+`","title":"Snapshot destination"}`)
	bindProjectRole(t, w.admin.TenantID, w.viewer.ID, "guest", w.root.ID)
	bindProjectRole(t, w.admin.TenantID, w.viewer.ID, "viewer", destination.ID)
	code, raw = call(t, &w.admin, "POST", "/api/nodes/"+n.ID+"/move", `{"parent_id":"`+destination.ID+`"}`)
	decode[nodeJSON](t, code, raw, 200)
	afterMove := planningOf(t, w.viewer, "/api/nodes?within="+destination.ID+"&q=SNAP-1")[n.Key]
	if afterMove == nil || afterMove.Cost == nil || afterMove.Snapshot == nil || afterMove.Snapshot.ID != latest.ID || afterMove.Snapshot.Cost != nil || afterMove.Snapshot.RateBasis.ListPerHour != nil {
		t.Fatalf("saved source cost escaped project fence: %+v", afterMove)
	}
}

func TestWorkStartSnapshotConcurrentUnknownAndTenantIsolation(t *testing.T) {
	w := planningSetup(t)
	n := w.node(t, "SNAP-2", "ticket", w.root.ID, "open", nil)
	var workers sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		workers.Go(func() {
			errs <- db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error { return CapturePlanningStart(t.Context(), tx, n.ID, "session") })
		})
	}
	workers.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	view := planningOf(t, w.admin, "/api/nodes?within="+w.root.ID+"&q=SNAP-2")[n.Key]
	if view == nil || view.Snapshot == nil || view.Snapshot.Hours != nil || view.Snapshot.Tokens != nil || view.Snapshot.Cost != nil || view.Snapshot.Route != nil {
		t.Fatalf("unknown became guessed: %+v", view)
	}
	var count int
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM ticket_estimate_snapshots WHERE ticket_node_id=$1`, n.ID).Scan(&count)
	}); err != nil || count != 1 {
		t.Fatalf("concurrent starts %d: %v", count, err)
	}
	other := addPrincipal(t, "snapshot-other-tenant")
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, other.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM ticket_estimate_snapshots WHERE ticket_node_id=$1`, n.ID).Scan(&count)
	}); err != nil || count != 0 {
		t.Fatalf("cross-tenant snapshot %d: %v", count, err)
	}
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE ticket_estimate_snapshots SET snapshot='{}' WHERE ticket_node_id=$1`, n.ID)
		return err
	})
	if err == nil {
		t.Fatal("baseline is mutable")
	}
}
