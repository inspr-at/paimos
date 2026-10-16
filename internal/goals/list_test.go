// SPDX-License-Identifier: AGPL-3.0-only
package goals

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
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
