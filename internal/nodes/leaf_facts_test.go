// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/eta"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risk: batching must preserve all completion/ETA evidence, ordering, excluded
// leaves and visibility while sharing facts across overlapping and cyclic roots.
func TestLeafFactsBatchParityAndVisibility(t *testing.T) {
	w := planningSetup(t)
	top := aggregateNode(t, w, "FACT-1", w.root.ID, "open", 20)
	group := aggregateNode(t, w, "FACT-2", top.ID, "open", 50)
	var leaves []nodeJSON
	for i, state := range []string{"open", "open", "open", "done", "archived", "cancelled", "open", "open"} {
		leaves = append(leaves, aggregateNode(t, w, fmt.Sprintf("FACT-%d", i+3), group.ID, state, i+1))
	}
	former := mustNode(t, w.admin, `{"kind_id":"`+kindBySlug(t, w.admin, "project").ID+`","title":"Former binding"}`)
	ids := []string{w.root.ID, top.ID, group.ID, top.ID}
	for _, n := range leaves {
		ids = append(ids, n.ID)
	}
	deep := seedAggregateTree(t, w, 80, 12)
	ids = append(ids, deep)
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		// Explicit timestamps and UUID ties prove both orderings without sleeps.
		_, err := tx.Exec(t.Context(), `INSERT INTO harness_sessions
 (tenant_id,id,project_id,agent_principal_id,ticket_node_id,harness,host,management,role,work_shape,
  ref_digest,lease_digest,phase,activity,progress_pct,eta_ready_at,eta_reported_at,stopped_at,stop_reason,archived_at,
  recovery_process_state,recovery_request_id,recovery_request_digest,recovery_actor_id,recovery_reason)
 SELECT $1,v.sid::uuid,CASE WHEN v.old_project THEN $2::uuid ELSE $3::uuid END,$4,v.node::uuid,
  'codex','test','unmanaged',v.role,'ship',decode(md5(v.sid),'hex'),decode(md5(v.sid||'lease'),'hex'),
  CASE WHEN v.stopped THEN 'stopped' ELSE 'working' END,'busy',v.pct::smallint,
  CASE WHEN NOT v.stopped THEN now()+make_interval(hours=>v.pct) END,
  CASE WHEN v.report_age IS NOT NULL THEN now()-make_interval(mins=>v.report_age) END,
  CASE WHEN v.stopped THEN now()-make_interval(mins=>v.stop_age) END,
  CASE WHEN v.stopped THEN v.reason END,CASE WHEN v.archived THEN now() END,
  CASE WHEN v.archived THEN 'unknown' END,CASE WHEN v.archived THEN v.sid::uuid END,
  CASE WHEN v.archived THEN decode(md5(v.sid||'recovery'),'hex') END,
  CASE WHEN v.archived THEN $4::uuid END,CASE WHEN v.archived THEN 'fixture recovery' END
 FROM (VALUES
  ('00000000-0000-4000-8000-000000000001',$5::text,'worker',false,false,false,10,0,NULL::int,NULL::text),
  ('00000000-0000-4000-8000-000000000002',$5,'worker',false,false,false,70,0,NULL,NULL),
  ('00000000-0000-4000-8000-000000000003',$5,'worker',false,false,false,90,NULL,NULL,NULL),
  ('00000000-0000-4000-8000-000000000004',$5,'worker',true,false,false,99,-1,NULL,NULL),
  ('00000000-0000-4000-8000-000000000005',$6,'worker',false,false,true,100,NULL,20,'process_exited'),
  ('00000000-0000-4000-8000-000000000006',$6,'worker',false,false,true,60,NULL,10,'process_exited'),
  ('00000000-0000-4000-8000-000000000007',$6,'worker',false,true,true,100,NULL,0,'process_exited'),
  ('00000000-0000-4000-8000-000000000008',$7,'worker',false,false,true,60,NULL,10,'process_exited'),
  ('00000000-0000-4000-8000-000000000009',$7,'worker',false,false,true,100,NULL,10,'process_exited'),
  ('00000000-0000-4000-8000-000000000010',$8,'worker',false,false,true,100,NULL,10,'process_exited'),
  ('00000000-0000-4000-8000-000000000011',$9,'worker',false,false,true,100,NULL,10,'process_exited'),
  ('00000000-0000-4000-8000-000000000012',$10,'worker',false,false,true,100,NULL,10,'process_exited'),
  ('00000000-0000-4000-8000-000000000013',$11,'worker',false,false,true,100,NULL,10,'process_exited'),
  ('00000000-0000-4000-8000-000000000014',$11,'coordinator',true,false,false,80,0,NULL,NULL),
  ('00000000-0000-4000-8000-000000000015',$12,'coordinator',false,false,false,80,20,NULL,NULL)
 ) v(sid,node,role,old_project,archived,stopped,pct,report_age,stop_age,reason)`,
			w.admin.TenantID, former.ID, w.root.ID, w.agent,
			leaves[0].ID, leaves[1].ID, leaves[2].ID, leaves[3].ID,
			leaves[4].ID, leaves[5].ID, leaves[6].ID, leaves[7].ID)
		if err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `INSERT INTO ticket_live_eta(tenant_id,node_id,eta_live_at,reported_at,reported_by)
 SELECT $1,n.id,now()+interval '4 hours',now()-interval '20 minutes',$2
 FROM nodes n WHERE n.id=ANY($3::uuid[])`, w.admin.TenantID, w.admin.ID,
			[]string{leaves[0].ID, leaves[6].ID, leaves[7].ID})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	check := func(ctx context.Context, p tenant.Principal, wantRows int) {
		t.Helper()
		if err := db.InTenant(ctx, appPool, p.TenantID, func(tx pgx.Tx) error {
			var same bool
			var rows int
			if err := tx.QueryRow(t.Context(), `WITH old AS MATERIALIZED (SELECT * FROM aeon_work_aggregates($1::uuid[])),
 fresh AS MATERIALIZED (`+eta.AggregateSQL("$1::uuid[]")+`)
 SELECT (SELECT jsonb_agg(to_jsonb(o) ORDER BY id) FROM old o) IS NOT DISTINCT FROM
        (SELECT jsonb_agg(to_jsonb(f) ORDER BY id) FROM fresh f),(SELECT count(*) FROM fresh)`, ids).Scan(&same, &rows); err != nil {
				return err
			}
			if !same || rows != wantRows {
				return fmt.Errorf("projection parity=%v, rows=%d want %d", same, rows, wantRows)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	check(dbtest.Seed(t.Context()), w.admin, len(ids)-1)
	_, views := aggregateRead(t, w.admin, ids...)
	if v := views[leaves[0].ID]; v.Progress == nil || *v.Progress != 10 || v.ReadyAt == nil || !v.HasWorkingSession {
		t.Fatalf("latest ready worker tie/null ordering: %+v", v)
	}
	if views[leaves[1].ID].Finished || !views[leaves[2].ID].Finished ||
		!views[leaves[4].ID].Finished || !views[leaves[5].ID].Finished || views[leaves[6].ID].Finished ||
		views[leaves[6].ID].LiveAt != nil || views[leaves[7].ID].LiveAt == nil {
		t.Fatal("completion latest-stop, archived-session or any-role/project semantics changed")
	}
	// Both supported roles block a clean completion, even without an ETA report.
	for _, role := range []string{"worker", "coordinator"} {
		if _, err := adminPool.Exec(t.Context(), `UPDATE harness_sessions SET role=$1,project_id=$2,progress_pct=NULL,eta_ready_at=NULL
 WHERE id='00000000-0000-4000-8000-000000000014'`, role, w.root.ID); err != nil {
			t.Fatal(err)
		}
		check(dbtest.Seed(t.Context()), w.admin, len(ids)-1)
		_, v := aggregateRead(t, w.admin, leaves[6].ID)
		if v[leaves[6].ID].Finished || v[leaves[6].ID].LiveAt == nil {
			t.Fatalf("open %s did not block completion / allow live ETA", role)
		}
	}
	for _, minutes := range []int{1, 30} {
		if _, err := adminPool.Exec(t.Context(), `INSERT INTO eta_settings(tenant_id,interval_minutes) VALUES($1,$2)
 ON CONFLICT(tenant_id) DO UPDATE SET interval_minutes=excluded.interval_minutes`, w.admin.TenantID, minutes); err != nil {
			t.Fatal(err)
		}
		check(dbtest.Seed(t.Context()), w.admin, len(ids)-1)
	}
	check(t.Context(), w.admin, 0) // Unbound transactions cannot bypass project RLS.
	guest := insertPerson(t, w.admin.TenantID, "No project grant")
	check(tenant.WithPrincipal(t.Context(), guest), guest, 0)
	foreign := addPrincipal(t, "leaf-facts-foreign")
	check(dbtest.Seed(t.Context()), foreign, 0)
	// Corrupt legacy cycles must terminate and count reachable leaves once. Only
	// this isolated fixture bypasses the tree triggers, in an atomic transaction.
	if err := func() error {
		tx, err := adminPool.Begin(t.Context())
		if err != nil {
			return err
		}
		defer tx.Rollback(t.Context())
		if _, err := tx.Exec(t.Context(), `ALTER TABLE nodes DISABLE TRIGGER USER`); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE nodes SET parent_id=$1 WHERE id=$2`, group.ID, top.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `ALTER TABLE nodes ENABLE TRIGGER USER`); err != nil {
			return err
		}
		return tx.Commit(t.Context())
	}(); err != nil {
		t.Fatal(err)
	}
	check(dbtest.Seed(t.Context()), w.admin, len(ids)-1)
	_, cycle := aggregateRead(t, w.admin, top.ID, group.ID)
	if cycle[top.ID].LeafCount != 6 || cycle[group.ID].LeafCount != 6 {
		t.Fatalf("cycle duplicated or lost leaves: %+v", cycle)
	}
}

// Risk: a Go batch can still hide per-leaf SQL calls. Count interval calls and
// inspect executed plans, independently of elapsed time and planner costs.
func TestLeafFactsBatchExecutionCounts(t *testing.T) {
	w := planningSetup(t)
	root := seedAggregateTree(t, w, 1, 64)
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO harness_sessions
 (tenant_id,project_id,agent_principal_id,ticket_node_id,harness,host,management,role,work_shape,
  ref_digest,lease_digest,phase,activity,progress_pct,eta_ready_at,eta_reported_at)
 SELECT $1,$2,$3,n.id,'codex','test','unmanaged','worker','ship',decode(md5(n.id::text),'hex'),
 decode(md5(n.id::text||'lease'),'hex'),'working','busy',50,now()+interval '1 hour',now()
 FROM nodes n WHERE n.parent_id=$4`, w.admin.TenantID, w.root.ID, w.agent, root)
		if err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `INSERT INTO ticket_live_eta(tenant_id,node_id,eta_live_at,reported_at,reported_by)
 SELECT $1,n.id,now()+interval '2 hours',now(),$2 FROM nodes n WHERE n.parent_id=$3`, w.admin.TenantID, w.admin.ID, root)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var original, oldSQL string
	if err := adminPool.QueryRow(t.Context(), `SELECT pg_get_functiondef('aeon_eta_interval()'::regprocedure),
 (SELECT prosrc FROM pg_proc WHERE oid='aeon_work_aggregates(uuid[])'::regprocedure)`).Scan(&original, &oldSQL); err != nil {
		t.Fatal(err)
	}
	// Instrument only the package's isolated database. Keep the original stable
	// signature and settings semantics; nextval is an observable execution counter.
	t.Cleanup(func() {
		if _, err := adminPool.Exec(context.Background(), original+`; DROP SEQUENCE leafbatch_interval_calls`); err != nil {
			t.Error(err)
		}
	})
	if _, err := adminPool.Exec(t.Context(), `CREATE SEQUENCE leafbatch_interval_calls MINVALUE 0 START 0;
 GRANT USAGE ON SEQUENCE leafbatch_interval_calls TO PUBLIC;
 CREATE OR REPLACE FUNCTION aeon_eta_interval() RETURNS interval LANGUAGE plpgsql STABLE AS $$
 BEGIN
  PERFORM nextval('leafbatch_interval_calls');
  RETURN make_interval(mins=>coalesce((SELECT interval_minutes FROM eta_settings),10));
 END; $$`); err != nil {
		t.Fatal(err)
	}
	for _, width := range []int{1, 64} {
		var ids []string
		if err := adminPool.QueryRow(t.Context(), `SELECT array_agg(id::text) FROM
 (SELECT id FROM nodes WHERE parent_id=$1 ORDER BY id LIMIT $2) n`, root, width).Scan(&ids); err != nil {
			t.Fatal(err)
		}
		if width == 64 {
			ids = append(ids, root, root) // Overlapping and duplicate roots share facts.
		}
		counts := map[string]int{}
		sessionStarts := map[string]int{}
		for _, mode := range []string{"before", "after"} {
			if _, err := adminPool.Exec(t.Context(), `ALTER SEQUENCE leafbatch_interval_calls RESTART WITH 0`); err != nil {
				t.Fatal(err)
			}
			query := strings.TrimSuffix(strings.TrimSpace(oldSQL), ";")
			query = strings.ReplaceAll(query, "(roots)", "($1::uuid[])")
			if mode == "after" {
				query = eta.AggregateSQL("$1::uuid[]")
			}
			var plan []byte
			if err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
				return tx.QueryRow(t.Context(), `EXPLAIN (ANALYZE,VERBOSE,FORMAT JSON) `+query, ids).Scan(&plan)
			}); err != nil {
				t.Fatal(err)
			}
			var called bool
			var calls int
			if err := adminPool.QueryRow(t.Context(), `SELECT last_value::int,is_called FROM leafbatch_interval_calls`).Scan(&calls, &called); err != nil {
				t.Fatal(err)
			}
			if called {
				calls++
			}
			counts[mode] = calls
			sessionStarts[mode] = leafBatchSessionStarts(t, plan)
			if mode == "after" {
				assertLeafBatchPlan(t, plan, width)
			}
		}
		// S02 may already hoist the interval in the installed SQL function.
		// Require one call here, without coupling this slice to its merge order.
		if counts["after"] != 1 || counts["before"] < 1 {
			t.Fatalf("width=%d interval calls before=%d after=%d", width, counts["before"], counts["after"])
		}
		t.Logf("%d distinct leaves: interval calls before=%d after=%d; harness scan starts before=%d after=%d; correlated ETA/completion function calls after=0", width, counts["before"], counts["after"], sessionStarts["before"], sessionStarts["after"])
	}
}

func leafBatchSessionStarts(t *testing.T, raw []byte) int {
	t.Helper()
	var documents []map[string]any
	if err := json.Unmarshal(raw, &documents); err != nil || len(documents) != 1 {
		t.Fatalf("invalid executed plan: %v", err)
	}
	starts := 0
	var visit func(map[string]any)
	visit = func(n map[string]any) {
		if n["Relation Name"] == "harness_sessions" {
			starts += int(n["Actual Loops"].(float64))
		}
		for _, child := range planChildren(n) {
			visit(child)
		}
	}
	visit(documents[0]["Plan"].(map[string]any))
	return starts
}

func assertLeafBatchPlan(t *testing.T, raw []byte, leaves int) {
	t.Helper()
	var documents []map[string]any
	if err := json.Unmarshal(raw, &documents); err != nil || len(documents) != 1 {
		t.Fatalf("invalid executed plan: %v", err)
	}
	sessions, facts := false, false
	var visit func(map[string]any)
	visit = func(n map[string]any) {
		if name, _ := n["Function Name"].(string); name == "aeon_leaf_eta" || name == "aeon_node_completion" {
			t.Errorf("correlated function remains: %s", name)
		}
		if n["Subplan Name"] == "CTE sessions" || n["Subplan Name"] == "CTE leaf_facts" {
			if n["Actual Loops"] != float64(1) || n["Actual Rows"] != float64(leaves) {
				t.Errorf("facts not shared once: %v loops=%v rows=%v", n["Subplan Name"], n["Actual Loops"], n["Actual Rows"])
			}
			sessions = sessions || n["Subplan Name"] == "CTE sessions"
			facts = facts || n["Subplan Name"] == "CTE leaf_facts"
		}
		for _, child := range planChildren(n) {
			visit(child)
		}
	}
	visit(documents[0]["Plan"].(map[string]any))
	if !sessions || !facts {
		t.Fatal("executed plan did not contain shared session and leaf facts")
	}
}
