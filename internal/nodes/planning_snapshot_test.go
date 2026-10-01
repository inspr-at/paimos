// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

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
	code, raw = call(t, &w.admin, "PATCH", "/api/nodes/"+n.ID, `{"state":"cancelled"}`)
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

func TestWorkStartSnapshotFixedProgressBuckets(t *testing.T) {
	w := planningSetup(t)
	for i, state := range []string{"in_progress", "inprogress", "active", "qa", " IN--PROGRESS "} {
		t.Run(state, func(t *testing.T) {
			n := w.node(t, fmt.Sprintf("START-%d", i), "ticket", w.root.ID, state, map[string]any{"estimate_hours": 2})
			snap := planningOf(t, w.admin, "/api/nodes?within="+w.root.ID+"&q="+n.Key)[n.Key].Snapshot
			if snap == nil || snap.Source != "status" || snap.Hours == nil || *snap.Hours != 2 {
				t.Fatalf("%q did not capture a status baseline: %+v", state, snap)
			}
		})
	}
}

func TestWorkStartSnapshotUsesKindCategoriesAndClosesSessionFirstEpisodes(t *testing.T) {
	w := planningSetup(t)
	kind := kindBySlug(t, w.admin, "ticket")
	var schema map[string]any
	if err := json.Unmarshal(kind.FieldSchema, &schema); err != nil {
		t.Fatal(err)
	}
	schema["states"] = []map[string]string{
		{"state": "building", "category": "doing"},
		{"state": "working", "category": "progress"},
		{"state": "executing", "category": "in_progress"},
		{"state": "shipped", "category": "done"},
		{"state": "discarded", "category": "canceled"},
		{"state": "retired", "category": "archived"},
		{"state": "done", "category": "open"},
		{"state": "qa", "category": "open"},
	}
	raw, err := json.Marshal(map[string]any{"field_schema": schema})
	if err != nil {
		t.Fatal(err)
	}
	code, body := call(t, &w.admin, "PATCH", "/api/kinds/"+kind.ID, string(raw))
	decode[kindJSON](t, code, body, 200)

	for i, state := range []string{"building", "working", "executing"} {
		t.Run(state, func(t *testing.T) {
			n := w.node(t, fmt.Sprintf("CATEGORY-%d", i), "ticket", w.root.ID, "open", map[string]any{"estimate_hours": 2})
			code, body := call(t, &w.admin, "PATCH", "/api/nodes/"+n.ID, fmt.Sprintf(`{"state":%q}`, state))
			decode[nodeJSON](t, code, body, 200)
			snap := planningOf(t, w.admin, "/api/nodes?within="+w.root.ID+"&q="+n.Key)[n.Key].Snapshot
			if snap == nil || snap.Source != "status" || snap.Hours == nil || *snap.Hours != 2 {
				t.Fatalf("custom doing state did not start work: %+v", snap)
			}
		})
	}

	for i, terminal := range []string{"shipped", "discarded", "retired"} {
		t.Run(terminal, func(t *testing.T) {
			n := w.node(t, fmt.Sprintf("CLOSE-%d", i), "ticket", w.root.ID, "open", map[string]any{
				"estimate_hours": 2, "pill_en": "Planning works now", "pill_de": "Planung geht jetzt",
				"benefit_en": "Planning shows real numbers.", "benefit_de": "Die Planung zeigt echte Zahlen.",
			})
			if err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
				return CapturePlanningStart(t.Context(), tx, n.ID, "session")
			}); err != nil {
				t.Fatal(err)
			}
			path := "/api/nodes?within=" + w.root.ID + "&q=" + n.Key
			first := planningOf(t, w.admin, path)[n.Key].Snapshot
			// Categories win: even the literal "done" is open for this kind.
			for _, state := range []string{"done", "qa"} {
				code, body := call(t, &w.admin, "PATCH", "/api/nodes/"+n.ID, fmt.Sprintf(`{"state":%q,"fields":{"estimate_hours":4}}`, state))
				decode[nodeJSON](t, code, body, 200)
				assertPlanningEpisodeRows(t, w.admin.TenantID, n.ID, 1, 1)
			}
			code, body := call(t, &w.admin, "PATCH", "/api/nodes/"+n.ID, fmt.Sprintf(`{"state":%q}`, terminal))
			decode[nodeJSON](t, code, body, 200)
			assertPlanningEpisodeRows(t, w.admin.TenantID, n.ID, 1, 0)
			code, body = call(t, &w.admin, "PATCH", "/api/nodes/"+n.ID, `{"state":"building"}`)
			decode[nodeJSON](t, code, body, 200)
			assertPlanningEpisodeRows(t, w.admin.TenantID, n.ID, 2, 1)
			latest := planningOf(t, w.admin, path)[n.Key].Snapshot
			if first == nil || latest == nil || latest.ID == first.ID || latest.Hours == nil || *latest.Hours != 4 {
				t.Fatalf("custom completion blocked the next episode: first=%+v latest=%+v", first, latest)
			}
		})
	}
}

func assertPlanningEpisodeRows(t *testing.T, tenantID, nodeID string, wantTotal, wantOpen int) {
	t.Helper()
	var total, open int
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*),count(*) FILTER (WHERE closed_at IS NULL) FROM ticket_estimate_snapshots WHERE ticket_node_id=$1`, nodeID).Scan(&total, &open)
	})
	if err != nil || total != wantTotal || open != wantOpen {
		t.Fatalf("episode rows total=%d open=%d, want %d/%d: %v", total, open, wantTotal, wantOpen, err)
	}
}

func TestWorkStartSnapshotProjectVisibilityFollowsTicket(t *testing.T) {
	w := planningSetup(t)
	other := mustNode(t, w.admin, `{"kind_id":"`+kindBySlug(t, w.admin, "project").ID+`","title":"Other snapshot project"}`)
	a := w.node(t, "VISIBLE-1", "ticket", w.root.ID, "in_progress", map[string]any{"estimate_hours": 2, "route_role": "build-hard"})
	b := w.node(t, "HIDDEN-1", "ticket", other.ID, "in_progress", map[string]any{"estimate_hours": 4, "route_role": "build-hard"})
	unstarted := w.node(t, "HIDDEN-2", "ticket", other.ID, "open", nil)
	for _, tc := range []struct {
		name     string
		projects []string
		want     int
	}{
		{"no projects", nil, 0},
		{"own project", []string{w.root.ID}, 1},
		{"other project", []string{other.ID}, 1},
		{"both projects", []string{w.root.ID, other.ID}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := db.OnlyProjects(t.Context(), tc.projects...)
			var count int
			err := db.InTenant(ctx, appPool, w.admin.TenantID, func(tx pgx.Tx) error {
				return tx.QueryRow(ctx, `SELECT count(*) FROM ticket_estimate_snapshots WHERE ticket_node_id=ANY($1::uuid[])`, []string{a.ID, b.ID}).Scan(&count)
			})
			if err != nil || count != tc.want {
				t.Fatalf("visible snapshots=%d, want %d: %v", count, tc.want, err)
			}
		})
	}
	ctx := db.OnlyProjects(t.Context(), w.root.ID)
	err := db.InTenant(ctx, appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO ticket_estimate_snapshots(tenant_id,ticket_node_id,snapshot) VALUES($1,$2,'{}')`, w.admin.TenantID, unstarted.ID)
		return err
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatalf("cross-project snapshot insert was not denied by RLS: %v", err)
	}
	var updated int64
	err = db.InTenant(ctx, appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE ticket_estimate_snapshots SET closed_at=clock_timestamp() WHERE ticket_node_id=$1`, b.ID)
		updated = tag.RowsAffected()
		return err
	})
	if err != nil || updated != 0 {
		t.Fatalf("cross-project snapshot update affected %d rows: %v", updated, err)
	}
	// The system path still sees both; tenant isolation remains in force.
	assertPlanningEpisodeRows(t, w.admin.TenantID, a.ID, 1, 1)
	assertPlanningEpisodeRows(t, w.admin.TenantID, b.ID, 1, 1)
	code, body := call(t, &w.admin, "POST", "/api/nodes/"+a.ID+"/move", `{"parent_id":"`+other.ID+`"}`)
	decode[nodeJSON](t, code, body, 200)
	var count int
	err = db.InTenant(ctx, appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM ticket_estimate_snapshots WHERE ticket_node_id=$1`, a.ID).Scan(&count)
	})
	if err != nil || count != 0 {
		t.Fatalf("snapshot did not follow ticket visibility after move: %d rows: %v", count, err)
	}
}
