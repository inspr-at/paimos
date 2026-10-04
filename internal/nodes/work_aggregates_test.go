// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/eta"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func aggregateNode(t *testing.T, w planningWorld, key, parent, state string, hours any) nodeJSON {
	t.Helper()
	fields := map[string]any{}
	if hours != nil {
		fields["estimate_hours"] = hours
	}
	if state == "done" {
		_ = json.Unmarshal([]byte(benefitFields), &fields)
		if hours != nil {
			fields["estimate_hours"] = hours
		}
	}
	return w.node(t, key, "work", parent, state, fields)
}
func aggregateETA(t *testing.T, w planningWorld, node string, pct int, ready, live time.Time) {
	t.Helper()
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		var sid string
		if err := tx.QueryRow(t.Context(), `INSERT INTO harness_sessions
   (tenant_id,project_id,agent_principal_id,ticket_node_id,harness,host,management,role,work_shape,ref_digest,lease_digest,phase,activity,progress_pct,eta_ready_at,eta_reported_at)
   VALUES ($1,$2,$3,$4,'codex','test','unmanaged','worker','ship',decode(replace(gen_random_uuid()::text,'-',''),'hex'),decode(replace(gen_random_uuid()::text,'-',''),'hex'),'working','busy',$5,$6,now()) RETURNING id::text`, w.admin.TenantID, w.root.ID, w.agent, node, pct, ready).Scan(&sid); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO ticket_live_eta(tenant_id,node_id,eta_live_at,reported_at,reported_by) VALUES($1,$2,$3,now(),$4)`, w.admin.TenantID, node, live, w.admin.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}
func aggregateRead(t *testing.T, p tenant.Principal, ids ...string) (map[string]*estimateView, map[string]eta.View) {
	t.Helper()
	var estimates map[string]*estimateView
	var views map[string]eta.View
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		var err error
		estimates, err = loadEstimates(t.Context(), tx, ids)
		if err != nil {
			return err
		}
		views, err = eta.Load(t.Context(), tx, ids)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return estimates, views
}
func TestWorkAggregatesCoverageRegroupingCountsAndSort(t *testing.T) {
	w := planningSetup(t)
	top := aggregateNode(t, w, "AGG-1", w.root.ID, "open", 40)
	group := aggregateNode(t, w, "AGG-2", top.ID, "open", 99)
	done := aggregateNode(t, w, "AGG-3", group.ID, "done", 2)
	doing := aggregateNode(t, w, "AGG-4", top.ID, "in_progress", 6)
	unknown := aggregateNode(t, w, "AGG-5", top.ID, "open", nil)
	aggregateNode(t, w, "AGG-6", top.ID, "cancelled", 100)
	archived := aggregateNode(t, w, "AGG-7", top.ID, "archived", 100)
	deleted := aggregateNode(t, w, "AGG-8", top.ID, "open", 100)
	if _, err := adminPool.Exec(t.Context(), `UPDATE nodes SET deleted_at=now() WHERE id=$1`, deleted.ID); err != nil {
		t.Fatal(err)
	}
	ready := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	live := ready.Add(time.Hour)
	aggregateETA(t, w, doing.ID, 50, ready, live)
	estimates, views := aggregateRead(t, w.admin, w.root.ID, top.ID, group.ID, done.ID, doing.ID, unknown.ID)
	v := views[top.ID]
	e := estimates[top.ID]
	if e == nil || e.Hours == nil || *e.Hours != 8 || e.PlannedHours == nil || *e.PlannedHours != 40 || e.LeafCount != 3 || e.EstimatedLeaves != 2 || !e.IsParent {
		t.Fatalf("estimate %+v", e)
	}
	if v.Progress == nil || *v.Progress != 63 || v.LeafCount != 3 || v.EstimatedLeaves != 2 || v.OpenLeaves != 2 || !v.ReadyPartial || !v.LivePartial || v.ReadyAt == nil || !v.ReadyAt.Equal(ready) || v.LiveAt == nil || !v.LiveAt.Equal(live) {
		t.Fatalf("eta %+v", v)
	}
	if views[w.root.ID].Progress == nil || *views[w.root.ID].Progress != 63 {
		t.Fatal("project counted grouping estimates")
	}
	if views[unknown.ID].Progress != nil {
		t.Fatal("unreported leaf invented progress")
	}
	// The same leaves in a different grouping give the same progress and sum.
	if _, err := adminPool.Exec(t.Context(), `UPDATE nodes SET parent_id=$1 WHERE id=$2`, group.ID, doing.ID); err != nil {
		t.Fatal(err)
	}
	after, ev := aggregateRead(t, w.admin, top.ID, w.root.ID)
	if *after[top.ID].Hours != 8 || *ev[top.ID].Progress != 63 || *ev[w.root.ID].Progress != 63 {
		t.Fatal("regrouping changed totals")
	}
	page := listPage(t, w.admin, "/api/nodes?within="+w.root.ID+"&facets=state&sort=-progress&limit=50")
	if page.Facets["state"]["open"] != 1 || page.Facets["state"]["done"] != 1 || page.Facets["state"]["in_progress"] != 1 {
		t.Fatalf("parent in leaf facets: %+v", page.Facets)
	}
	for _, order := range []string{"estimate", "-estimate", "progress", "-progress", "eta_ready", "-eta_ready", "tokens", "list_cost", "paid"} {
		page := listPage(t, w.admin, "/api/nodes?within="+w.root.ID+"&sort="+order+"&limit=50")
		for _, item := range page.Items {
			if item.ID == top.ID && (item.Estimate == nil || *item.Estimate.Hours != 8 || item.Eta == nil || *item.Eta.Progress != 63) {
				t.Fatalf("sort/display disagree: %s %+v", order, item)
			}
		}
	}
	status, raw := call(t, &w.admin, http.MethodGet, "/api/projects", "")
	projects := decode[projectPage](t, status, raw, 200)
	if len(projects.Items) != 1 || projects.Items[0].Open != 1 || projects.Items[0].InProgress != 1 || projects.Items[0].Done != 1 || projects.Items[0].Cancelled != 1 || projects.Items[0].Total != 5 {
		t.Fatalf("project counts %+v", projects)
	}
	// Custom categories and archived-only parents stay honest.
	if _, err := adminPool.Exec(t.Context(), `UPDATE node_kinds SET field_schema=field_schema||'{"states":[{"state":"shipped","category":"done"}]}'::jsonb WHERE tenant_id=$1 AND slug='work'`, w.admin.TenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := adminPool.Exec(t.Context(), `UPDATE nodes SET state='shipped' WHERE id=$1`, done.ID); err != nil {
		t.Fatal(err)
	}
	_, ev = aggregateRead(t, w.admin, top.ID)
	if *ev[top.ID].Progress != 63 {
		t.Fatal("custom Done not counted")
	}
	_ = archived
}

func TestWorkAggregatesUnweightedAndHistoricalSpend(t *testing.T) {
	w := planningSetup(t)
	top := aggregateNode(t, w, "SPEND-1", w.root.ID, "open", nil)
	// This stopped session belongs to a former leaf, which now gains children.
	w.session(t, top.ID, "codex", "gpt-6-astra", "xhigh", "gpt-6-astra", 1, 100, 20, 0, "subscription", "Team")
	group := aggregateNode(t, w, "SPEND-2", top.ID, "open", nil)
	a := aggregateNode(t, w, "SPEND-3", group.ID, "done", nil)
	b := aggregateNode(t, w, "SPEND-4", group.ID, "open", nil)
	w.session(t, a.ID, "codex", "gpt-6-astra", "xhigh", "gpt-6-astra", 1, 200, 30, 0, "subscription", "Team")
	aggregateNode(t, w, "SPEND-5", top.ID, "open", nil)
	_, views := aggregateRead(t, w.admin, top.ID, group.ID)
	if views[top.ID].Progress == nil || *views[top.ID].Progress != 33 || views[top.ID].ProgressBasis != "leaves" || *views[group.ID].Progress != 50 {
		t.Fatalf("fallback %+v", views)
	}
	for _, sort := range []string{"tokens", "-tokens", "paid", "list_cost", "key"} {
		p := planningOf(t, w.admin, "/api/nodes?within="+w.root.ID+"&sort="+sort)
		v := p[top.Key]
		if v == nil || v.Tokens.Spent == nil || *v.Tokens.Spent != 350 || v.Tokens.Sessions != 2 {
			t.Fatalf("session duplication/loss sort=%s %+v", sort, v)
		}
		for _, m := range v.Models {
			if len(m.Sessions) != 2 {
				t.Fatalf("identity lost %+v", m)
			}
		}
	}
	viewer := planningOf(t, w.viewer, "/api/nodes?within="+w.root.ID+"&sort=paid")[top.Key]
	if viewer == nil || viewer.Cost != nil {
		t.Fatalf("money permission %+v", viewer)
	}
	other := addPrincipal(t, "aggregate-other")
	e, v := aggregateRead(t, other, top.ID, b.ID)
	if len(e) != 0 || len(v) != 0 {
		t.Fatal("tenant aggregate leak")
	}
}

func TestWorkAggregatesDeepWideAndRLS(t *testing.T) {
	w := planningSetup(t)
	root := seedAggregateTree(t, w, 80, 200)
	e, v := aggregateRead(t, w.admin, root, w.root.ID)
	if e[root].LeafCount != 200 || *e[root].Hours != 200 || *v[root].Progress != 50 || v[root].OpenLeaves != 100 {
		t.Fatalf("deep/wide truncated: %+v %+v", e[root], v[root])
	}
	err := db.InTenant(context.Background(), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		var n int
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM aeon_work_aggregates(ARRAY[$1::uuid])`, root).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	// A principal without a project grant gets no aggregate, even with the root id.
	guest := insertPerson(t, w.admin.TenantID, "No project")
	status, raw := call(t, &guest, "GET", "/api/nodes?within="+root+"&sort=-progress", "")
	if status != 200 || len(decode[nodePage](t, status, raw, 200).Items) != 0 {
		t.Fatalf("project leak %d %s", status, raw)
	}
}

func seedAggregateTree(t testing.TB, w planningWorld, depth, width int) string {
	t.Helper()
	kind := kindBySlugTB(t, w.admin, "work")
	var root string
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		parent := w.root.ID
		for i := range depth {
			var id string
			if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id,state) VALUES($1,$2,'B'||upper(substr(replace(gen_random_uuid()::text,'-',''),1,8))||'-1','Deep parent',$3,'open') RETURNING id::text`, w.admin.TenantID, kind, parent).Scan(&id); err != nil {
				return err
			}
			if i == 0 {
				root = id
			}
			parent = id
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id,state,fields)
   SELECT $1,$2,'B'||upper(substr(replace(gen_random_uuid()::text,'-',''),1,8))||'-1','Wide leaf '||i,$3,CASE WHEN i%2=0 THEN 'done' ELSE 'open' END,'{"estimate_hours":1}'::jsonb FROM generate_series(1,$4::int) i`, w.admin.TenantID, kind, parent, width)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return root
}
func kindBySlugTB(t testing.TB, p tenant.Principal, slug string) string {
	t.Helper()
	var id string
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT id::text FROM node_kinds WHERE tenant_id=$1 AND slug=$2`, p.TenantID, slug).Scan(&id)
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func BenchmarkWorkAggregatesRLS(b *testing.B) {
	// Run after a fixture-only test initializes the package database. This benchmark
	// creates its own tenant; it never bypasses the app role's RLS on measured reads.
	ctx := context.Background()
	setupOnce.Do(func() { setupErr = setupDB() })
	if setupErr != nil {
		b.Fatal(setupErr)
	}
	var tenantID, project, kind string
	if err := appPool.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES($1,'Benchmark') RETURNING id::text`, fmt.Sprintf("agg-bench-%d", time.Now().UnixNano())).Scan(&tenantID); err != nil {
		b.Fatal(err)
	}
	if err := db.InTenant(dbtest.Seed(ctx), appPool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT id::text FROM node_kinds WHERE tenant_id=$1 AND slug='work'`, tenantID).Scan(&kind); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title,state) SELECT $1,id,'B'||upper(substr(replace(gen_random_uuid()::text,'-',''),1,8))||'-1','Benchmark project','open' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, tenantID).Scan(&project)
	}); err != nil {
		b.Fatal(err)
	}
	w := planningWorld{admin: tenant.Principal{TenantID: tenantID}, root: nodeJSON{ID: project}}
	for _, shape := range []struct {
		name         string
		depth, width int
	}{{"wide", 1, 2000}, {"deep", 256, 1}, {"mixed", 32, 1000}} {
		root := seedAggregateTree(b, w, shape.depth, shape.width)
		b.Run(shape.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if err := db.InTenant(dbtest.Seed(ctx), appPool, tenantID, func(tx pgx.Tx) error {
					var leaves int
					return tx.QueryRow(ctx, `SELECT leaf_count FROM aeon_work_aggregates(ARRAY[$1::uuid])`, root).Scan(&leaves)
				}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
