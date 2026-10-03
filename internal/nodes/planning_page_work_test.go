// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type planningPageQueryRecorder struct {
	pgx.Tx
	sql  string
	args []any
}

func (tx *planningPageQueryRecorder) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if strings.Contains(sql, "FROM planning_values") {
		tx.sql, tx.args = sql, append([]any(nil), args...)
	}
	return tx.Tx.Query(ctx, sql, args...)
}

// A page of leaf tickets must not scan the tenant's other tickets while
// collecting its child/grandchild usage. Count executed work rather than time.
func TestPlanningPageSubtreeProbesOnlySelectedParents(t *testing.T) {
	p := newPrincipal(t, "planning-page-work")
	project := kindBySlug(t, p, "project")
	ticket := kindBySlug(t, p, "ticket")
	root := mustNode(t, p, `{"kind_id":"`+project.ID+`","title":"Page work"}`)
	var ids []string
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,state,parent_id)
 SELECT $1,$2,'PAGE-'||g,'Leaf '||g,'open',$3 FROM generate_series(1,1500) g`, p.TenantID, ticket.ID, root.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `ANALYZE nodes`); err != nil {
			return err
		}
		rows, err := tx.Query(t.Context(), `SELECT id::text FROM nodes WHERE tenant_id=$1 AND parent_id=$2 ORDER BY id LIMIT 50`, p.TenantID, root.ID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 50 {
		t.Fatalf("selected page has %d rows", len(ids))
	}
	agent := insertNamedAgent(t, p.TenantID, "Page worker")
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO harness_sessions(
 tenant_id,project_id,agent_principal_id,ticket_node_id,harness,host,management,role,work_shape,
 ref_digest,lease_digest,phase,activity,created_at,heartbeat_at,stopped_at,stop_reason)
 SELECT $1,$2,$3,id,'codex','test','unmanaged','worker','ship',
 decode(replace(gen_random_uuid()::text,'-',''),'hex'),decode(replace(gen_random_uuid()::text,'-',''),'hex'),
 'stopped','busy',clock_timestamp(),clock_timestamp(),clock_timestamp(),'completed'
 FROM unnest($4::uuid[]) id`, p.TenantID, root.ID, agent, ids)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO model_prices(tenant_id,model,version,input_usd_per_million,output_usd_per_million,cached_input_usd_per_million)
 VALUES($1,'page-test-model',1,1000,0,0)`, p.TenantID); err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `INSERT INTO harness_session_usage(
 tenant_id,session_id,model,sequence,input_tokens,output_tokens,cached_input_tokens,provisional,billing_mode,price_version,estimated_cost_usd)
 SELECT tenant_id,id,'page-test-model',1,1000,0,0,false,'api',1,1
 FROM harness_sessions WHERE tenant_id=$1 AND ticket_node_id=ANY($2::uuid[])`, p.TenantID, ids)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	ctx := tenant.WithPrincipal(t.Context(), p)
	if err := New(appPool, nil).(*Module).tx(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		recorded := &planningPageQueryRecorder{Tx: tx}
		money, err := loadPlanMicros(ctx, recorded, ids, assigneeSeen{harnessAll: true, members: true})
		if err != nil {
			return err
		}
		if len(money) != len(ids) {
			t.Fatalf("planning page returned %d rows", len(money))
		}
		for _, id := range ids {
			if m, ok := money[id]; !ok || m.paidSpent == nil || m.paidSpent.String() != "1000000" {
				t.Fatal("planning page lost a selected leaf's reported dollar")
			}
		}
		if recorded.sql == "" {
			t.Fatal("no executed planning page query captured")
		}
		var raw []byte
		if err := tx.QueryRow(ctx, "EXPLAIN (ANALYZE, FORMAT JSON, TIMING OFF) "+recorded.sql, recorded.args...).Scan(&raw); err != nil {
			return err
		}
		var plan any
		if err := json.Unmarshal(raw, &plan); err != nil {
			return err
		}
		rows, probes := planningSubtreeWork(plan, false)
		t.Logf("page subtree: %.0f node rows visited, %.0f probes", rows, probes)
		if probes <= 0 || rows > float64(6*len(ids)) {
			t.Fatalf("page subtree visited %.0f node rows in %.0f probes for %d leaves", rows, probes, len(ids))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func planningSubtreeWork(plan any, subtree bool) (rows, probes float64) {
	switch node := plan.(type) {
	case []any:
		for _, child := range node {
			r, p := planningSubtreeWork(child, subtree)
			rows, probes = rows+r, probes+p
		}
	case map[string]any:
		subtree = subtree || node["Subplan Name"] == "CTE plan_sub"
		if subtree && node["Relation Name"] == "nodes" {
			n, _ := node["Actual Rows"].(float64)
			removed, _ := node["Rows Removed by Filter"].(float64)
			loops, _ := node["Actual Loops"].(float64)
			rows, probes = (n+removed)*loops, loops
		}
		for _, child := range []any{node["Plan"], node["Plans"]} {
			r, p := planningSubtreeWork(child, subtree)
			rows, probes = rows+r, probes+p
		}
	}
	return rows, probes
}

func TestPlanningUnestimatedUsageFreePageSkipsUnusedMicros(t *testing.T) {
	p := newPrincipal(t, "planning-no-unused-micros")
	project := kindBySlug(t, p, "project")
	ticket := kindBySlug(t, p, "ticket")
	root := mustNode(t, p, `{"kind_id":"`+project.ID+`","title":"Unestimated"}`)
	item := mustNode(t, p, `{"kind_id":"`+ticket.ID+`","title":"No estimate or usage","parent_id":"`+root.ID+`"}`)
	ctx := tenant.WithPrincipal(t.Context(), p)
	if err := New(appPool, nil).(*Module).tx(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		recorded := &planningPageQueryRecorder{Tx: tx}
		items := []listItem{{nodeJSON: item, KindSlug: "ticket", Project: &listProject{ID: root.ID}}}
		views, err := loadPlanning(ctx, recorded, items, assigneeSeen{harnessAll: true, members: true}, nil)
		if err != nil {
			return err
		}
		if len(views) != 0 {
			t.Fatal("unestimated, usage-free ticket invented planning data")
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes WHERE id=$1 AND deleted_at IS NULL)`, item.ID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			t.Fatal("planning fixture lost its ticket")
		}
		if recorded.sql != "" {
			t.Fatal("unestimated, usage-free page executed unused monetary planning")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
