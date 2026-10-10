// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/eta"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
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
	aggregateNode(t, w, "AGG-7", top.ID, "archived", 100)
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
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		if err := db.LockWorkTreeTx(t.Context(), tx); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET parent_id=$1 WHERE id=$2`, group.ID, doing.ID)
		return err
	}); err != nil {
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
	// ETA uses the latest open leaf independently for ready/live. A much later
	// report on a closed leaf must never delay its parent.
	aggregateETA(t, w, unknown.ID, 25, ready.Add(2*time.Hour), live.Add(3*time.Hour))
	aggregateETA(t, w, done.ID, 100, ready.Add(24*time.Hour), live.Add(24*time.Hour))
	_, ev = aggregateRead(t, w.admin, top.ID)
	latest := ev[top.ID]
	if latest.ReadyAt == nil || !latest.ReadyAt.Equal(ready.Add(2*time.Hour)) || latest.LiveAt == nil || !latest.LiveAt.Equal(live.Add(3*time.Hour)) || latest.ReadyPartial || latest.LivePartial {
		t.Fatalf("latest open leaves %+v", latest)
	}
	// Custom categories stay honest.
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
	// Old project bindings cannot supply a ready ETA, progress or working hint.
	oldProject := mustNode(t, w.admin, `{"kind_id":"`+kindBySlug(t, w.admin, "project").ID+`","title":"Former session project"}`)
	if _, err := adminPool.Exec(t.Context(), `UPDATE harness_sessions SET project_id=$1 WHERE ticket_node_id=$2`, oldProject.ID, doing.ID); err != nil {
		t.Fatal(err)
	}
	_, ev = aggregateRead(t, w.admin, doing.ID)
	if ev[doing.ID].ReadyAt != nil || ev[doing.ID].Progress != nil || ev[doing.ID].HasWorkingSession {
		t.Fatalf("obsolete binding contributes %+v", ev[doing.ID])
	}
}

func TestWorkAggregatesUnweightedAndHistoricalSpend(t *testing.T) {
	w := planningSetup(t)
	top := aggregateNode(t, w, "SPEND-1", w.root.ID, "open", nil)
	// This stopped session belongs to a former leaf, which now gains children.
	w.session(t, top.ID, "codex", "gpt-6-astra", "xhigh", "gpt-6-astra", 1, 100, 20, 0, "subscription", "Team")
	group := aggregateNode(t, w, "SPEND-2", top.ID, "open", nil)
	sub := aggregateNode(t, w, "SPEND-6", group.ID, "open", nil)
	a := aggregateNode(t, w, "SPEND-3", sub.ID, "done", nil)
	b := aggregateNode(t, w, "SPEND-4", sub.ID, "open", nil)
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
	var invisibleCount int
	err := db.InTenant(context.Background(), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM aeon_work_aggregates(ARRAY[$1::uuid])`, root).Scan(&invisibleCount)
	})
	if err != nil {
		t.Fatal(err)
	}
	if invisibleCount != 0 {
		t.Fatal("unbound transaction bypassed project RLS")
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
	// This benchmark initializes an isolated package database and its own tenant; it never bypasses the app role's RLS on measured reads.
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
		// Bulk-loaded fixtures need the same planner statistics autovacuum supplies
		// in production; analyze only this benchmark's isolated database.
		if _, err := adminPool.Exec(ctx, `ANALYZE nodes; ANALYZE node_kinds; ANALYZE harness_sessions; ANALYZE ticket_live_eta`); err != nil {
			b.Fatal(err)
		}
		for _, mode := range []string{"total", "progress_sort", "estimate_sort", "eta_sort"} {
			b.Run(shape.name+"/"+mode, func(b *testing.B) {
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					if err := db.InTenant(dbtest.Seed(ctx), appPool, tenantID, func(tx pgx.Tx) error {
						var leaves int
						if mode == "total" {
							return tx.QueryRow(ctx, `SELECT leaf_count FROM aeon_work_aggregates(ARRAY[$1::uuid])`, root).Scan(&leaves)
						}
						column := map[string]string{"progress_sort": "progress_pct", "estimate_sort": "hours", "eta_sort": "eta_ready_at"}[mode]
						// Scope discovery is performed once; all requested sort values are one batch.
						return tx.QueryRow(ctx, `WITH targets AS MATERIALIZED(SELECT id FROM aeon_work_scope(ARRAY[$1::uuid])),
      values AS MATERIALIZED(SELECT * FROM aeon_work_aggregates(ARRAY(SELECT id FROM targets))),
      page AS(SELECT id FROM values ORDER BY `+column+` DESC NULLS LAST,id LIMIT 50)
      SELECT count(*)::int FROM page`, root).Scan(&leaves)
					}); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

func TestWorkAggregatesCalibratedTokenEstimates(t *testing.T) {
	w := planningSetup(t)
	for i := range calibrationMinimum {
		n := aggregateNode(t, w, fmt.Sprintf("CAL-%d", i+1), w.root.ID, "done", 1)
		w.session(t, n.ID, "codex", "gpt-6-astra", "xhigh", "gpt-6-astra", 1, 1000, 0, 0, "subscription", "Team")
	}
	parent := aggregateNode(t, w, "CAL-20", w.root.ID, "open", 100)
	group := aggregateNode(t, w, "CAL-21", parent.ID, "open", 100)
	// Match the measured route explicitly; the work classifier's default build
	// route is a different model and must not borrow Codex's calibration.
	leaf := w.node(t, "CAL-22", "work", group.ID, "open", map[string]any{
		"estimate_hours": 2, "area": "backend", "route_role": "build-hard", "complexity": "M",
	})
	for _, sort := range []string{"key", "tokens", "-tokens", "list_cost", "paid"} {
		got := planningOf(t, w.admin, "/api/nodes?within="+w.root.ID+"&sort="+sort)
		a, b := got[parent.Key], got[leaf.Key]
		if a == nil || b == nil || a.Tokens.Estimated == nil || b.Tokens.Estimated == nil || *a.Tokens.Estimated != 120000 || *a.Tokens.Estimated != *b.Tokens.Estimated || a.Children == nil || a.Children.Total != 1 || a.Children.Estimated != 1 {
			detail, _ := json.Marshal(map[string]*planningView{"parent": a, "leaf": b})
			t.Fatalf("leaf token sum sort %s: %s", sort, detail)
		}
	}
	// Only leaves capture a new baseline; a former leaf's old snapshot stays put.
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		if err := CapturePlanningStart(t.Context(), tx, parent.ID, "session"); err != nil {
			return err
		}
		if err := CapturePlanningStart(t.Context(), tx, leaf.ID, "session"); err != nil {
			return err
		}
		var parents, leaves int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FILTER(WHERE ticket_node_id=$1),count(*) FILTER(WHERE ticket_node_id=$2) FROM ticket_estimate_snapshots`, parent.ID, leaf.ID).Scan(&parents, &leaves); err != nil {
			return err
		}
		if parents != 0 || leaves != 1 {
			return fmt.Errorf("snapshots parent=%d leaf=%d", parents, leaves)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWorkAggregatesClosedLeafEstimateOrder(t *testing.T) {
	w := planningSetup(t)
	top := aggregateNode(t, w, "ORDER-1", w.root.ID, "open", 99)
	aggregateNode(t, w, "ORDER-2", top.ID, "open", 3)
	cancelled := aggregateNode(t, w, "ORDER-3", top.ID, "cancelled", 1)
	archived := aggregateNode(t, w, "ORDER-4", top.ID, "archived", 5)
	estimates, _ := aggregateRead(t, w.admin, top.ID, cancelled.ID, archived.ID)
	if *estimates[top.ID].Hours != 3 {
		t.Fatal("closed leaves contributed to parent")
	}
	for _, tc := range []struct {
		sort string
		want []string
	}{
		{"estimate", []string{cancelled.ID, top.ID, archived.ID}},
		{"-estimate", []string{archived.ID, top.ID, cancelled.ID}},
	} {
		page := listPage(t, w.admin, "/api/nodes?within="+w.root.ID+"&sort="+tc.sort+"&limit=50")
		var got []string
		for _, n := range page.Items {
			if n.ID == top.ID || n.ID == cancelled.ID || n.ID == archived.ID {
				got = append(got, n.ID)
			}
		}
		if fmt.Sprint(got) != fmt.Sprint(tc.want) {
			t.Fatalf("%s actual order %v, want %v", tc.sort, got, tc.want)
		}
	}
}

func TestWorkScopeRootAndExpansionBudgets(t *testing.T) {
	w := planningSetup(t)
	root := seedAggregateTree(t, w, 316, 0)
	t.Run("roots", func(t *testing.T) {
		for _, count := range []int{4096, 4097} {
			ids := make([]string, count)
			for i := range ids {
				ids[i] = root
			}
			var n int
			err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
				return tx.QueryRow(t.Context(), `SELECT count(*) FROM aeon_work_scope($1::uuid[])`, ids).Scan(&n)
			})
			if count == 4096 {
				if err != nil || n != 316 {
					t.Fatalf("valid roots lost scope: count=%d err=%v", n, err)
				}
				continue
			}
			var pgerr *pgconn.PgError
			if !errors.As(err, &pgerr) || pgerr.Code != "54000" || pgerr.Message != "work scope root budget exceeded" {
				t.Fatalf("root budget: %v", err)
			}
		}
	})
	t.Run("expansion", func(t *testing.T) {
		for _, count := range []int{303, 304} {
			var n int
			err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
				// First N chain nodes yield N*317-N*(N+1)/2 scope pairs.
				return tx.QueryRow(t.Context(), `WITH RECURSIVE chain(id,depth) AS (
     SELECT $1::uuid,1 UNION ALL SELECT n.id,c.depth+1 FROM chain c JOIN nodes n ON n.parent_id=c.id WHERE c.depth<$2
    ) SELECT count(*) FROM aeon_work_scope(ARRAY(SELECT id FROM chain))`, root, count).Scan(&n)
			})
			if count == 303 {
				if err != nil || n != 49995 {
					t.Fatalf("valid expansion truncated: count=%d err=%v", n, err)
				}
				continue
			}
			var pgerr *pgconn.PgError
			if !errors.As(err, &pgerr) || pgerr.Code != "54000" || pgerr.Message != "work scope expansion budget exceeded" {
				t.Fatalf("expansion budget: %v", err)
			}
		}
	})
}

func TestWorkAggregateLimitsAreExplicitHTTPFailures(t *testing.T) {
	for _, err := range []error{
		&pgconn.PgError{Code: "54000", Message: "work scope root budget exceeded"},
		&pgconn.PgError{Code: "54000", Message: "work scope expansion budget exceeded"},
		fmt.Errorf("read aggregate: %w", context.DeadlineExceeded),
	} {
		r := httptest.NewRecorder()
		writeErr(r, err)
		if r.Code != 503 {
			t.Errorf("%v: status %d, want 503", err, r.Code)
		}
		var body map[string]any
		if e := json.Unmarshal(r.Body.Bytes(), &body); e != nil {
			t.Fatal(e)
		}
		if body["error"] != "Work aggregates exceeded a resource limit; narrow the scope or retry." {
			t.Errorf("opaque failure: %s", r.Body.String())
		}
	}
}

// Risk: narrowing aggregate sorts to a page can silently change ordering and
// coverage; concurrent read demand must not turn a scope limit into success.
func TestListEstimateSortBoundedConcurrentMatchesBaseline(t *testing.T) {
	w := planningSetup(t)
	planningBulk(t, w, 1000, 2, "open", "LISTLOAD-", true)
	group := aggregateNode(t, w, "LISTLOAD-1001", w.root.ID, "open", 99)
	done := aggregateNode(t, w, "LISTLOAD-1002", group.ID, "done", 3)
	cancelled := aggregateNode(t, w, "LISTLOAD-1003", group.ID, "cancelled", 100)
	unknown := aggregateNode(t, w, "LISTLOAD-1004", group.ID, "open", nil)
	archived := aggregateNode(t, w, "LISTLOAD-1005", group.ID, "archived", 4.5)
	deleted := aggregateNode(t, w, "LISTLOAD-1006", group.ID, "open", 200)
	if _, err := adminPool.Exec(t.Context(), `UPDATE nodes SET deleted_at=now() WHERE id=$1`, deleted.ID); err != nil {
		t.Fatal(err)
	}
	aggregateETA(t, w, unknown.ID, 35, time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC), time.Date(2030, 1, 1, 13, 0, 0, 0, time.UTC))
	ctx := tenant.WithPrincipal(t.Context(), w.admin)
	q := listQuery{Within: &w.root.ID, Sort: []sortKey{{Name: "estimate", Desc: true}}, Limit: 20}
	q.seen.harnessAll = true
	for _, enabled := range []bool{false, true} {
		q.boundedAggregates = enabled
		sql, args := listSQL(q, nil)
		functions := listSortPlanFunctions(t, ctx, w.admin.TenantID, sql, args)
		if enabled {
			if functions["aeon_work_scope"] != 1 || functions["aeon_work_aggregates"] != 0 || functions["aeon_leaf_eta"] != 0 || functions["aeon_node_completion"] != 0 {
				t.Fatalf("estimate sort still reads session evidence: %v", functions)
			}
		} else if functions["aeon_work_aggregates"] != 1 {
			t.Fatalf("baseline aggregate missing: %v", functions)
		}
		t.Logf("estimate sort opt-in=%v: 1005 visible work rows, 2000 historical sessions; function loops=%v", enabled, functions)
	}
	// Every root's value is compared before paging, including out-of-page
	// descendants, excluded closed leaves and null estimates.
	values := func(enabled bool) map[string]*string {
		prefix, args := listFilterSQL(q, false)
		cte := `, work_values AS MATERIALIZED (SELECT * FROM aeon_work_aggregates(ARRAY(SELECT id FROM filtered)))`
		if enabled {
			cte = listEstimateSortSQL()
		}
		out := map[string]*string{}
		err := db.InTenant(ctx, appPool, w.admin.TenantID, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, prefix+cte+` SELECT id::text,hours::text FROM work_values`, args...)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var id string
				var hours *string
				if err := rows.Scan(&id, &hours); err != nil {
					return err
				}
				out[id] = hours
			}
			return rows.Err()
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	baseline := values(false)
	if !reflect.DeepEqual(baseline, values(true)) || len(baseline) != 1005 ||
		baseline[group.ID] == nil || *baseline[group.ID] != "3" ||
		baseline[done.ID] == nil || *baseline[done.ID] != "3" ||
		baseline[cancelled.ID] == nil || *baseline[cancelled.ID] != "100" ||
		baseline[archived.ID] == nil || *baseline[archived.ID] != "4.5" || baseline[unknown.ID] != nil {
		t.Fatal("whole-scope estimate semantics changed")
	}
	plain := New(appPool, nil).(*Module)
	baselinePage, err := plain.listNodes(ctx, w.admin.TenantID, q)
	if err != nil {
		t.Fatal(err)
	}
	// Stop all four optimized reads at the actual list statement before releasing
	// them together. No sleep or elapsed-time assumption proves overlap.
	ready, release := make(chan struct{}, 4), make(chan struct{})
	cfg := appPool.Config()
	cfg.MaxConns = 4
	cfg.ConnConfig.Tracer = listReadBarrier{ready, release}
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	mod := New(pool, nil).(*Module)
	pages, failures := make([]nodePage, 4), make([]error, 4)
	var reads sync.WaitGroup
	var unblock sync.Once
	defer unblock.Do(func() { close(release) })
	for i := range 4 {
		reads.Add(1)
		go func() {
			defer reads.Done()
			pages[i], failures[i] = mod.listNodes(WithBoundedListAggregates(ctx), w.admin.TenantID, q)
		}()
	}
	guard, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for range 4 {
		select {
		case <-ready:
		case <-guard.Done():
			t.Fatal("concurrent list requests did not reach query barrier")
		}
	}
	unblock.Do(func() { close(release) })
	reads.Wait()
	for i := range 4 {
		if failures[i] != nil {
			t.Fatal(failures[i])
		}
		if len(pages[i].Items) != 20 || pages[i].NextCursor == nil || !reflect.DeepEqual(baselinePage, pages[i]) {
			t.Fatal("concurrent page/order/coverage/cursor differs from baseline")
		}
		if pages[i].Items[0].ID != cancelled.ID || pages[i].Items[1].ID != archived.ID {
			t.Fatal("closed leaf ordering changed")
		}
	}
	// A cursor minted with the default path also continues through the opt-in
	// path, with matching filters, ties, page contents and next cursor.
	q.Cursor = *pages[0].NextCursor
	basePage, err := plain.listNodes(ctx, w.admin.TenantID, q)
	if err != nil {
		t.Fatal(err)
	}
	nextPage, err := plain.listNodes(WithBoundedListAggregates(ctx), w.admin.TenantID, q)
	if err != nil || !reflect.DeepEqual(basePage, nextPage) {
		t.Fatalf("cursor parity: %v", err)
	}
	q.Cursor = ""
	// No new calculation is substituted for a compound ETA/progress sort.
	for _, key := range []string{"progress", "eta_ready"} {
		compound := q
		compound.Sort = append([]sortKey{{Name: key}}, q.Sort...)
		compound.boundedAggregates = false
		baseSQL, _ := listSQL(compound, nil)
		compound.boundedAggregates = true
		optSQL, _ := listSQL(compound, nil)
		if baseSQL != optSQL {
			t.Fatal("whole-scope ETA/progress sort changed")
		}
	}
	// RLS still applies to every filtered root and every scope descendant.
	bindProjectRole(t, w.admin.TenantID, w.viewer.ID, "viewer", w.root.ID)
	viewerCtx := tenant.WithPrincipal(t.Context(), w.viewer)
	q.States = []string{"open"}
	q.IDs = []string{group.ID, unknown.ID, archived.ID}
	viewerBase, err := plain.listNodes(viewerCtx, w.admin.TenantID, q)
	if err != nil {
		t.Fatal(err)
	}
	viewerOpt, err := plain.listNodes(WithBoundedListAggregates(viewerCtx), w.admin.TenantID, q)
	if err != nil || !reflect.DeepEqual(viewerBase, viewerOpt) || len(viewerOpt.Items) != 2 {
		t.Fatalf("filtered viewer parity: %v", err)
	}
	other := addPrincipal(t, "list-other-tenant")
	foreignPage, err := plain.listNodes(WithBoundedListAggregates(tenant.WithPrincipal(t.Context(), other)), other.TenantID, q)
	if err != nil || len(foreignPage.Items) != 0 {
		t.Fatalf("foreign tenant read: %v", err)
	}
	private := mustNode(t, w.admin, `{"kind_id":"`+kindBySlug(t, w.admin, "project").ID+`","title":"Private list"}`)
	privateWorld := w
	privateWorld.root = private
	aggregateNode(t, privateWorld, "PRIVATE-LIST-1", private.ID, "open", 200)
	q.Within, q.States, q.IDs = &private.ID, nil, nil
	privatePage, err := plain.listNodes(WithBoundedListAggregates(viewerCtx), w.admin.TenantID, q)
	if err != nil || len(privatePage.Items) != 0 {
		t.Fatalf("ungranted project read: %v", err)
	}
	// The opt-in must retain both published budgets, even on a tiny page.
	assertLimit := func(root, message string) {
		q.Within, q.States, q.IDs = &root, nil, nil
		page, err := plain.listNodes(WithBoundedListAggregates(ctx), w.admin.TenantID, q)
		var pgerr *pgconn.PgError
		if !errors.As(err, &pgerr) || pgerr.Code != "54000" || pgerr.Message != message || len(page.Items) != 0 {
			t.Fatalf("limit reason/partial read: %v", err)
		}
		r := httptest.NewRecorder()
		writeErr(r, err)
		if r.Code != 503 || !strings.Contains(r.Body.String(), "Work aggregates exceeded a resource limit") {
			t.Fatal("scope limit was hidden")
		}
	}
	deep := seedAggregateTree(t, w, 317, 0)
	assertLimit(deep, "work scope expansion budget exceeded")
	planningBulk(t, w, 3100, 0, "open", "ROOTLIMIT-", false)
	assertLimit(w.root.ID, "work scope root budget exceeded")
}

type listReadBarrier struct {
	ready   chan<- struct{}
	release <-chan struct{}
}

func (b listReadBarrier) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, "page_rows AS") {
		select {
		case b.ready <- struct{}{}:
		case <-ctx.Done():
			return ctx
		}
		select {
		case <-b.release:
		case <-ctx.Done():
		}
	}
	return ctx
}
func (listReadBarrier) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func listSortPlanFunctions(t *testing.T, ctx context.Context, tenantID, sql string, args []any) map[string]int {
	t.Helper()
	functions := map[string]int{}
	err := db.InTenant(ctx, appPool, tenantID, func(tx pgx.Tx) error {
		var raw []byte
		if err := tx.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+sql, args...).Scan(&raw); err != nil {
			return err
		}
		var documents []map[string]any
		if err := json.Unmarshal(raw, &documents); err != nil {
			return err
		}
		var visit func(map[string]any)
		visit = func(plan map[string]any) {
			if name, ok := plan["Function Name"].(string); ok {
				functions[name] += int(plan["Actual Loops"].(float64))
			}
			for _, child := range planChildren(plan) {
				visit(child)
			}
		}
		visit(documents[0]["Plan"].(map[string]any))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return functions
}
