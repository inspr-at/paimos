// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"reflect"
	"strconv"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

func TestPlanningDisplayedPlacementsInvariantAcrossSorts(t *testing.T) {
	w := planningSetup(t)
	// Seed current work leaves directly: the regression does not depend on
	// recreating any retired starter kinds in the older planning fixtures.
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO model_prices(tenant_id,model,version,input_usd_per_million,output_usd_per_million,cached_input_usd_per_million)
 VALUES($1,'gpt-6-astra',100,1,1,1),($1,'opus',100,1,1,1)`, w.admin.TenantID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for i := range 10 {
		harness, model, effort, tokens := "codex", "gpt-6-astra", "xhigh", int64(2_000_000)
		if i >= 5 {
			harness, model, effort, tokens = "claude", "opus", "high", 8_000_000
		}
		n := aggregateNode(t, w, "MEASURE-"+strconv.Itoa(i+1), w.root.ID, "done", nil)
		w.session(t, n.ID, harness, model, effort, model, 60, tokens, 0, 0, "api", "")
	}
	if _, err := adminPool.Exec(t.Context(), `UPDATE harness_sessions SET created_at='2026-09-30T12:00:00Z',stopped_at='2026-09-30T13:00:00Z' WHERE tenant_id=$1`, w.admin.TenantID); err != nil {
		t.Fatal(err)
	}
	fields := func(role string) map[string]any {
		return map[string]any{"area": "backend", "route_role": role, "complexity": "M", "estimate_hours": 2}
	}
	parent := w.node(t, "INVARIANT-1", "work", w.root.ID, "open", fields("build-hard"))
	w.node(t, "INVARIANT-2", "work", parent.ID, "open", fields("build"))
	for _, tc := range []struct{ key, state string }{{"INVARIANT-3", "cancelled"}, {"INVARIANT-4", "archived"}} {
		w.node(t, tc.key, "work", parent.ID, tc.state, fields("build-hard"))
	}
	path := "/api/nodes?within=" + w.root.ID + "&q=INVARIANT-&limit=100"
	want := planningOf(t, w.admin, path+"&sort=key")
	if len(want) != 4 {
		t.Fatalf("fixture must retain parent, open leaf, cancelled and archived leaves: %d", len(want))
	}
	// A parent intentionally has no single route: its price is the eligible
	// open leaf's 2h at 8M tokens/h, never the parent's typed 2h or closed leaves.
	p := want[parent.Key]
	if p == nil || p.Route != nil || p.Children == nil || p.Children.Total != 1 || p.Tokens.Estimated == nil || *p.Tokens.Estimated != 16_000_000 || p.Cost == nil || p.Cost.ListEstimated == nil || *p.Cost.ListEstimated != "16.000000" {
		t.Fatalf("parent must retain eligible leaf sum without inventing one route: %+v", p)
	}
	for _, key := range []string{"INVARIANT-3", "INVARIANT-4"} {
		p := want[key]
		if p == nil || p.Route == nil || p.Route.Profile != "codex-astra-xhigh" {
			t.Fatalf("%s must resolve its distinct displayed route: %+v", key, p)
		}
		if p.Tokens.Calibration == nil || p.Tokens.Calibration.TokensPerHour != 2_000_000 || p.Tokens.Estimated == nil || *p.Tokens.Estimated != 4_000_000 || p.Cost == nil || p.Cost.ListEstimated == nil || *p.Cost.ListEstimated != "4.000000" {
			t.Fatalf("%s needs route-specific tokens and cost: %+v", key, p)
		}
	}
	for _, order := range []string{"tokens", "-tokens", "list_cost", "-list_cost", "paid", "-paid", "model", "-model"} {
		got := planningOf(t, w.admin, path+"&sort="+order)
		for key, expected := range want {
			if !reflect.DeepEqual(got[key], expected) {
				t.Fatalf("sort %s changed route, token or cost projection for %s: got %+v, want %+v", order, key, got[key], expected)
			}
		}
	}
}
