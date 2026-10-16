// SPDX-License-Identifier: AGPL-3.0-only
package goals

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func listResult(t *testing.T, f storageFixture, m *module, query string) GoalList {
	t.Helper()
	r := goalRequest(t.Context(), m, f.reader, "GET", "/api/goals"+query, "", "")
	if r.Code != 200 {
		t.Fatalf("list %d %s", r.Code, r.Body)
	}
	var out GoalList
	if err := json.Unmarshal(r.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// Risk: the page's computed sorts use a different ETA, filters omit workspace
// contributions, search misses projected text, or totals/quiet projects depend
// on the selected page. Each sort has three distinct, explicit expected values.
func TestGoalListViewFiltersAndOrdering(t *testing.T) {
	f := writerFixture(t)
	var group string
	goals := []string{}
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.writer.TenantID, func(tx pgx.Tx) error {
		third := fixtureNode(t, tx, f.writer.TenantID, "project", "PRJ-3", "", map[string]any{"project_key": "VPRJ3"})
		for i, project := range []string{f.project, f.otherProject} {
			if _, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=fields||jsonb_build_object('project_key',$2::text) WHERE id=$1`, project, fmt.Sprintf("VPRJ%d", i+1)); err != nil {
				return err
			}
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO project_groups(tenant_id,name,created_by) VALUES($1,'View group',$2) RETURNING id::text`, f.writer.TenantID, f.writer.ID).Scan(&group); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO project_group_members(tenant_id,project_id,group_id) VALUES($1,$2,$3)`, f.writer.TenantID, f.project, group); err != nil {
			return err
		}
		for i, title := range []string{"View Bravo", "View Alpha", "View Charlie"} {
			fields := map[string]any{"progress_override_pct": []int{30, 10, 20}[i]}
			if i == 0 {
				fields["steps"] = []Step{{ID: "next", Text: "NeedleStep", State: "now"}}
			}
			if i == 1 {
				fields["eta_at"] = apiClock.Add(2 * time.Hour).Format(time.RFC3339)
			}
			if i == 2 {
				fields["eta_text"] = "no clock"
			}
			g := fixtureNode(t, tx, f.writer.TenantID, GoalKind, fmt.Sprintf("VIEWG-%d", i+1), []string{f.project, f.otherProject, third}[i], fields)
			if _, err := tx.Exec(t.Context(), `UPDATE nodes SET title=$2 WHERE id=$1`, g, title); err != nil {
				return err
			}
			goals = append(goals, g)
			states := [][]string{{"moving", "moving", "moving", "queued"}, {"moving", "queued", "queued", "queued", "done"}, {"queued", "queued", "done", "done"}}[i]
			for j, state := range states {
				itemFields := map[string]any{"outcome_ref": fmt.Sprintf("part%d", j), "state_set": state}
				if i == 0 && j == 0 {
					itemFields["note"] = "NeedleNote"
				}
				o := fixtureNode(t, tx, f.writer.TenantID, OutcomeKind, fmt.Sprintf("VIEWO-%d-%d", i+1, j), g, itemFields)
				if i == 0 && j == 0 {
					if _, err := tx.Exec(t.Context(), `UPDATE nodes SET title='NeedleLabel' WHERE id=$1`, o); err != nil {
						return err
					}
					if _, err := tx.Exec(t.Context(), `INSERT INTO node_relations(tenant_id,source_node_id,target_node_id,type) VALUES($1,$2,$3,'serves')`, f.writer.TenantID, o, f.ticket); err != nil {
						return err
					}
				}
			}
		}
		for _, target := range []string{f.ticket, f.hiddenTicket} {
			if _, err := tx.Exec(t.Context(), `INSERT INTO node_relations(tenant_id,source_node_id,target_node_id,type) VALUES($1,$2,$3,'serves')`, f.writer.TenantID, f.workspaceOutcome, target); err != nil {
				return err
			}
		}
		var agent string
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','ETA source') RETURNING id::text`, f.writer.TenantID).Scan(&agent); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,ticket_node_id,harness,host,management,role,work_shape,ref_digest,lease_digest,phase,eta_ready_at) VALUES($1,$2,$3,$4,'codex','test','unmanaged','worker','ship',$5,$6,'working',$7)`, f.writer.TenantID, f.project, agent, f.ticket, []byte("view-ref"), []byte("view-lease"), apiClock.Add(time.Hour))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	m := &module{f.db.App, func() time.Time { return apiClock }}
	// Project-only readers still cannot read a contributing workspace goal.
	if r := goalRequest(t.Context(), m, f.reader, "GET", "/api/goals/"+f.workspaceGoal, "", ""); r.Code != 404 {
		t.Fatalf("workspace visibility %d %s", r.Code, r.Body)
	}
	f.reader = f.writer
	for sortBy, order := range map[string][]int{"eta": {0, 1, 2}, "name": {1, 0, 2}, "progress": {0, 2, 1}, "project": {0, 1, 2}, "moving": {0, 1, 2}, "queued": {1, 2, 0}, "done": {2, 1, 0}, "name&dir=desc": {2, 0, 1}, "eta&dir=desc": {1, 0, 2}} {
		out := listResult(t, f, m, "?q=View&sort="+sortBy)
		ids := []string{}
		for _, g := range out.Items {
			ids = append(ids, g.ID)
		}
		want := []string{goals[order[0]], goals[order[1]], goals[order[2]]}
		if !reflect.DeepEqual(ids, want) {
			t.Fatalf("sort %s got %v want %v", sortBy, ids, want)
		}
		if strings.HasPrefix(sortBy, "eta") {
			for _, g := range out.Items {
				if g.ID == goals[0] && (g.ETA == nil || g.ETA.Source != "outcomes" || g.ETA.At == nil || !g.ETA.At.Equal(apiClock.Add(time.Hour))) {
					t.Fatalf("worker fallback not displayed: %+v", g.ETA)
				}
			}
		}
	}
	for _, query := range []string{"needlelabel", "NEEDLENOTE", "NeedleStep", "VPRJ1", "TKT-1", "View Bravo"} {
		out := listResult(t, f, m, "?q="+url.QueryEscape(query))
		if !slices.ContainsFunc(out.Items, func(g GoalView) bool { return g.ID == goals[0] }) {
			t.Fatalf("search missed %q", query)
		}
	}
	for _, query := range []string{"?project=" + f.project, "?group=" + group, "?group=none"} {
		out := listResult(t, f, m, query)
		if !slices.ContainsFunc(out.Items, func(g GoalView) bool {
			return g.ID == f.workspaceGoal && len(g.Projects) == 2 && g.Projects[0].Key == "VPRJ1" && g.Projects[1].Key == "VPRJ2"
		}) {
			t.Fatalf("contributing workspace goal missing: %s %+v", query, out)
		}
		if query == "?group=none" && slices.ContainsFunc(out.Items, func(g GoalView) bool { return g.ID == goals[0] }) {
			t.Fatal("group=none included a grouped-only goal")
		}
	}
	all := listResult(t, f, m, "")
	totals := StateCounts{}
	for _, g := range all.Items {
		totals.Moving += g.States.Moving
		totals.Queued += g.States.Queued
		totals.Paused += g.States.Paused
		totals.Done += g.States.Done
	}
	if totals != all.Totals || !slices.Contains(all.QuietProjects.Keys, "VPRJ3") || slices.Contains(all.QuietProjects.Keys, "VPRJ1") || slices.Contains(all.QuietProjects.Keys, "VPRJ2") {
		t.Fatalf("full totals or quiet projects %+v", all)
	}
	cursor := ""
	seen := []string{}
	for i := 0; i < 3; i++ {
		out := listResult(t, f, m, "?q=View&sort=name&limit=1&cursor="+cursor)
		if len(out.Items) != 1 || out.Totals != all.Totals || !reflect.DeepEqual(out.QuietProjects, all.QuietProjects) {
			t.Fatal("page changed totals/footer")
		}
		seen = append(seen, out.Items[0].ID)
		cursor = out.NextCursor
	}
	if cursor != "" || !reflect.DeepEqual(seen, []string{goals[1], goals[0], goals[2]}) {
		t.Fatalf("keyset pagination %v %q", seen, cursor)
	}
	for _, filter := range []string{"?state=queued", "?q=not-found", "?hide_done=true"} {
		if out := listResult(t, f, m, filter); out.Totals != all.Totals || !reflect.DeepEqual(out.QuietProjects, all.QuietProjects) {
			t.Fatalf("filter changed totals/footer: %s", filter)
		}
	}
}

type listQueryCounter struct{ count atomic.Int32 }

func (c *listQueryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.HasPrefix(data.SQL, "WITH RECURSIVE\n page AS MATERIALIZED") {
		c.count.Add(1)
	}
	return ctx
}
func (*listQueryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// Risk: the page performs N+1 expansions, drifts from detail weighting/ETA,
// or includes a hidden outcome. A 50 x 30 page must use one fact statement.
func TestGoalListPageWideProjection(t *testing.T) {
	f := writerFixture(t)
	readWriteResult(t, apiRequest(t, f, "PATCH", "/api/goals/"+f.goal, "list-detail-eta", `{"items":{"upsert":[{"ref":"visible","eta":{"text":"authored","at":"2026-10-17T02:00:00Z"}}]}}`), 200)
	// Match the fixture's existing ref rather than create a new outcome.
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.writer.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `WITH g AS (
 INSERT INTO nodes(tenant_id,key,kind_id,title,parent_id)
 SELECT $1,'PAGEG-'||i,(SELECT kind_id FROM nodes WHERE id=$2),'Page '||i,$3::uuid FROM generate_series(1,50) i RETURNING id,key
 ), o AS (
 INSERT INTO nodes(tenant_id,key,kind_id,title,parent_id,fields)
 SELECT $1,'PAGEO-'||g.key||'-'||j,(SELECT kind_id FROM nodes WHERE id=$4),'Outcome '||j,g.id,jsonb_build_object('outcome_ref','item'||j,'state_set',CASE WHEN j%2=0 THEN 'done' ELSE 'queued' END)
 FROM g CROSS JOIN generate_series(1,30) j RETURNING id,parent_id,key
 ), t AS (
 INSERT INTO nodes(tenant_id,key,kind_id,title,parent_id,fields)
 SELECT $1,'PAGET-'||o.key,(SELECT kind_id FROM nodes WHERE id=$5),'Work', $3::uuid,'{"estimate_hours":2}'::jsonb FROM o RETURNING id,key
 ) INSERT INTO node_relations(tenant_id,source_node_id,target_node_id,type)
 SELECT $1,o.id,t.id,'serves' FROM o JOIN t ON t.key='PAGET-'||o.key`, f.writer.TenantID, f.goal, f.project, f.outcome, f.ticket)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	config := f.db.App.Config()
	counter := &listQueryCounter{}
	config.ConnConfig.Tracer = counter
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	m := &module{pool, func() time.Time { return apiClock }}
	list := listResult(t, f, m, "?project="+f.project+"&limit=100&sort=name")
	if counter.count.Load() != 1 || list.Truncated || list.NextCursor != "" {
		t.Fatalf("page query count=%d truncated=%v cursor=%q", counter.count.Load(), list.Truncated, list.NextCursor)
	}
	pages := 0
	for _, g := range list.Items {
		if strings.HasPrefix(g.Key, "PAGEG-") {
			pages++
			if g.Expansion != "full" || len(g.Items) != 30 || g.Progress == nil || g.Progress.Pct != 50 || g.Progress.Basis != "estimate" {
				t.Fatalf("page projection %+v", g)
			}
		}
		if g.ID == f.goal {
			want := detail(t, goalRequest(t.Context(), m, f.reader, "GET", "/api/goals/"+g.ID, "", ""))
			// Generation is per request. Every other public value must agree.
			g.Watermark = want.Watermark
			a, _ := json.Marshal(g)
			b, _ := json.Marshal(want)
			if !reflect.DeepEqual(a, b) {
				t.Fatalf("list/detail disagree\nlist %s\ndetail %s", a, b)
			}
		}
	}
	if pages != 50 {
		t.Fatalf("page lost goals: %d", pages)
	}
}

// Risk: independently valid goals exceed the aggregate pair budget, paging
// skips a goal, totals vary by page, or one oversized goal spoils its siblings.
func TestGoalListAggregateBudgetPaginatesWithoutLoss(t *testing.T) {
	f := writerFixture(t)
	var goals []string
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.writer.TenantID, func(tx pgx.Tx) error {
		roots := []string{}
		parent := f.project
		for i := range 200 {
			parent = fixtureNode(t, tx, f.writer.TenantID, "work", fmt.Sprintf("BROOT-%d", i+1), parent, nil)
			roots = append(roots, parent)
		}
		for i := range 260 {
			fixtureNode(t, tx, f.writer.TenantID, "work", fmt.Sprintf("BLEAF-%d", i+1), parent, nil)
		}
		for i, name := range []string{"Budget A", "Budget B", "Budget C"} {
			g := fixtureNode(t, tx, f.writer.TenantID, GoalKind, fmt.Sprintf("BGOAL-%d", i+1), f.project, nil)
			if _, err := tx.Exec(t.Context(), `UPDATE nodes SET title=$2 WHERE id=$1`, g, name); err != nil {
				return err
			}
			goals = append(goals, g)
			selected := roots
			if i < 2 {
				selected = roots[i*100 : (i+1)*100]
			}
			for j := 0; j < len(selected); j += 20 {
				o := fixtureNode(t, tx, f.writer.TenantID, OutcomeKind, fmt.Sprintf("BO-%d-%d", i+1, j), g, map[string]any{"outcome_ref": fmt.Sprintf("part%d", j)})
				for _, root := range selected[j : j+20] {
					if _, err := tx.Exec(t.Context(), `INSERT INTO node_relations(tenant_id,source_node_id,target_node_id,type) VALUES($1,$2,$3,'serves')`, f.writer.TenantID, o, root); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	m := &module{f.db.App, func() time.Time { return apiClock }}
	query := "?project=" + f.project + "&q=budget&sort=name&limit=100"
	first := listResult(t, f, m, query)
	if len(first.Items) != 1 || first.Items[0].ID != goals[0] || first.Items[0].Expansion != "full" || !first.Truncated || first.NextCursor == "" {
		t.Fatalf("aggregate page was not shrunk: %+v", first)
	}
	second := listResult(t, f, m, query+"&cursor="+first.NextCursor)
	if len(second.Items) != 2 || second.Items[0].ID != goals[1] || second.Items[0].Expansion != "full" || second.Items[1].ID != goals[2] || second.Truncated || second.NextCursor != "" {
		t.Fatalf("continuation lost a goal: %+v", second)
	}
	large := second.Items[1]
	if large.Expansion != "too_large" || large.Counts.Outcomes != 10 || large.Counts.Tickets != 200 || large.Progress != nil || large.ETA != nil || large.States != nil || len(large.Items) != 0 {
		t.Fatalf("oversized sibling leaked partial values: %+v", large)
	}
	if !first.TotalsPartial || !second.TotalsPartial || first.Totals != second.Totals || !reflect.DeepEqual(first.QuietProjects, second.QuietProjects) {
		t.Fatal("pagination changed full-filter totals")
	}
	wrong := goalRequest(t.Context(), m, f.reader, "GET", "/api/goals?sort=progress&cursor="+first.NextCursor, "", "")
	if wrong.Code != 400 {
		t.Fatalf("cursor lost filter binding: %d %s", wrong.Code, wrong.Body)
	}
}
