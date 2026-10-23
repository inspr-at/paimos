// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/eta"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestPickerSearchAnswersWithoutWorkAggregates(t *testing.T) {
	if _, err := parseListQuery(httptest.NewRequest(http.MethodGet, "/api/nodes?rollups=nope", nil)); err == nil {
		t.Fatal("accepted invalid rollups")
	}
	leaf, err := parseListQuery(httptest.NewRequest(http.MethodGet, "/api/nodes?kind=work&shape=leaf&sort=-updated_at&rollups=omit", nil))
	if err != nil || !leaf.OmitRollups {
		t.Fatalf("omit rollups: %+v %v", leaf, err)
	}
	plain, err := parseListQuery(httptest.NewRequest(http.MethodGet, "/api/nodes?kind=work&shape=leaf&sort=-updated_at", nil))
	if err != nil || plain.OmitRollups || listFingerprint(leaf) == listFingerprint(plain) {
		t.Fatal("rollups lost from the cursor identity")
	}
	sql, _ := listFilterSQL(leaf, false)
	if !strings.Contains(sql, "aeon_work_leaf_flags") || strings.Contains(sql, "aeon_work_shape") || strings.Contains(sql, "aeon_work_scope") || !strings.Contains(sql, "matched AS MATERIALIZED") || !strings.Contains(sql, "filtered AS") {
		t.Fatalf("shape filter SQL changed the wrong walk: %s", sql)
	}
	depth, err := parseListQuery(httptest.NewRequest(http.MethodGet, "/api/nodes?depth=1", nil))
	if err != nil {
		t.Fatal(err)
	}
	depthSQL, _ := listFilterSQL(depth, false)
	if !strings.Contains(depthSQL, "aeon_work_shape") || strings.Contains(depthSQL, "aeon_work_leaf_flags") {
		t.Fatal("depth filter left the per-row shape walk")
	}
	bare, err := parseListQuery(httptest.NewRequest(http.MethodGet, "/api/nodes?limit=30", nil))
	if err != nil {
		t.Fatal(err)
	}
	bareSQL, _ := listFilterSQL(bare, false)
	if strings.Contains(bareSQL, "aeon_work_leaf_flags") || strings.Contains(bareSQL, "matched AS") {
		t.Fatal("unfiltered list grew a shape pass")
	}

	p := newPrincipal(t, "picker-search")
	projectKind := kindBySlug(t, p, "project")
	project := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Picker"}`, projectKind.ID))
	work := kindBySlug(t, p, "work")
	const leaves = 4097
	// Pin the pre-import statistics, including during the HTTP requests.
	// This lock permits normal reads/writes but holds auto-analyze off, so
	// runner timing cannot decide whether the expensive plan is exercised.
	if _, err := adminPool.Exec(t.Context(), `ANALYZE nodes`); err != nil {
		t.Fatal(err)
	}
	statistics, err := adminPool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer statistics.Rollback(t.Context())
	if _, err := statistics.Exec(t.Context(), `LOCK TABLE nodes IN SHARE UPDATE EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	var ids []string
	var parentID, childID string
	err = db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(t.Context(), `INSERT INTO nodes (tenant_id,key,kind_id,title,state,parent_id,position,fields)
            SELECT $1,'PICK-'||g,$2,'PICK-'||g,'open',$3,g,'{}'::jsonb
            FROM generate_series(1,$4::int) g RETURNING id::text`, p.TenantID, work.ID, project.ID, leaves)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes (tenant_id,key,kind_id,title,state,parent_id,position,fields)
            VALUES ($1,'PICK-4098',$2,'PICK parent','open',$3,0,'{}'::jsonb) RETURNING id::text`, p.TenantID, work.ID, project.ID).Scan(&parentID); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO nodes (tenant_id,key,kind_id,title,state,parent_id,position,fields)
            VALUES ($1,'PICK-4099',$2,'PICK child','open',$3,0,'{}'::jsonb) RETURNING id::text`, p.TenantID, work.ID, parentID).Scan(&childID)
	})
	if err != nil || len(ids) != leaves || parentID == "" || childID == "" {
		t.Fatalf("seed %d ids: %v", len(ids), err)
	}
	err = db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `SELECT * FROM aeon_work_scope($1::uuid[])`, ids)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "work scope root budget exceeded") {
		t.Fatalf("corpus did not exceed the scope root budget: %v", err)
	}
	// Risk: stale row estimates can turn a set-based leaf filter into one
	// rescan of the entire candidate set per row. Inspect actual work rather
	// than relying on the speed of the test host.
	query := leaf
	query.Q = "PICK"
	query.Limit = 30
	// Exercise fresh and cached statement plans, including the function body.
	for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
		ctx, cancel := context.WithTimeout(tenant.WithPrincipal(t.Context(), p), eta.AggregateReadTimeout)
		err = db.InTenant(ctx, appPool, p.TenantID, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, "SET LOCAL plan_cache_mode="+mode); err != nil {
				return err
			}
			// Explain the same permission-projected lead query as the HTTP path.
			var err error
			query.seen, err = assigneeAudience(ctx, tx, p)
			if err != nil {
				return err
			}
			query.leadYellow, query.leadRed, err = readLeadMinutes(ctx, tx, p.ID)
			if err != nil {
				return err
			}
			querySQL, queryArgs := listSQL(query, nil)
			if _, err := tx.Exec(ctx, "PREPARE picker_plan AS "+querySQL); err != nil {
				return err
			}
			defer tx.Exec(ctx, "DEALLOCATE picker_plan")
			params := make([]string, len(queryArgs))
			for i := range params {
				params[i] = fmt.Sprintf("$%d", i+1)
			}
			var raw []byte
			explain := "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) EXECUTE picker_plan(" + strings.Join(params, ",") + ")"
			if err := tx.QueryRow(ctx, explain, append([]any{pgx.QueryExecModeSimpleProtocol}, queryArgs...)...).Scan(&raw); err != nil {
				return err
			}
			var documents []map[string]any
			if err := json.Unmarshal(raw, &documents); err != nil {
				return err
			}
			var nodeVisits, candidateVisits float64
			var visit func(map[string]any)
			visit = func(plan map[string]any) {
				rows, _ := plan["Actual Rows"].(float64)
				filtered, _ := plan["Rows Removed by Filter"].(float64)
				loops, _ := plan["Actual Loops"].(float64)
				if plan["Relation Name"] == "nodes" {
					nodeVisits += (rows + filtered) * loops
					t.Logf("picker plan: alias=%v index=%v rows=%v loops=%v", plan["Alias"], plan["Index Name"], rows+filtered, loops)
				}
				if plan["CTE Name"] == "matched" || plan["CTE Name"] == "leaf_flags" || plan["CTE Name"] == "selected" {
					candidateVisits += (rows + filtered) * loops
				}
				for _, child := range planChildren(plan) {
					visit(child)
				}
			}
			visit(documents[0]["Plan"].(map[string]any))
			// Allow whole-corpus matching/counting plus bounded page enrichment;
			// never a whole-corpus parent/ancestor scan for every selected row.
			if budget := float64(4*(leaves+3) + 32*(query.Limit+1)); nodeVisits > budget {
				return fmt.Errorf("picker rescanned nodes per page row: %.0f visits, budget %.0f", nodeVisits, budget)
			}
			// A materialized input can still be compared once per candidate. Count
			// that work too, so a fast runner cannot hide repeated page joins.
			t.Logf("picker candidate work: %.0f input visits", candidateVisits)
			if budget := float64(4*(leaves+3) + 32*(query.Limit+1)); candidateVisits > budget {
				return fmt.Errorf("picker rescanned candidates: %.0f visits, budget %.0f", candidateVisits, budget)
			}
			// Function Scan row counts hide the canonical child probes. Bound
			// total buffer work too: stale statistics previously scanned all
			// tenant nodes once per candidate inside the leaf function.
			plan := documents[0]["Plan"].(map[string]any)
			hits, _ := plan["Shared Hit Blocks"].(float64)
			reads, _ := plan["Shared Read Blocks"].(float64)
			buffers := hits + reads
			t.Logf("picker %s: %.0f shared buffers", mode, buffers)
			if buffers <= 0 {
				return fmt.Errorf("picker plan omitted buffer work")
			}
			if budget := float64(128*(leaves+3) + 512*(query.Limit+1)); buffers > budget {
				return fmt.Errorf("picker repeated work inside leaf filter: %.0f shared buffers, budget %.0f", buffers, budget)
			}
			return nil
		})
		cancel()
		if err != nil {
			t.Fatalf("picker %s: %v", mode, err)
		}
	}

	status, body := call(t, &p, http.MethodGet, "/api/nodes?kind=work&shape=leaf&limit=30&sort=-updated_at&rollups=omit&q=PICK", "")
	var page struct {
		Items []struct {
			ID       string          `json:"id"`
			IsLeaf   bool            `json:"is_leaf"`
			Estimate json.RawMessage `json:"estimate"`
		} `json:"items"`
	}
	if status != http.StatusOK {
		t.Fatalf("picker search %d %s", status, body)
	}
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) == 0 || len(page.Items) > 30 {
		t.Fatalf("page size %d", len(page.Items))
	}
	for _, item := range page.Items {
		if item.ID == parentID || !item.IsLeaf || len(item.Estimate) != 0 {
			t.Fatalf("picker row %+v", item)
		}
	}
	status, body = call(t, &p, http.MethodGet, "/api/nodes?kind=work&shape=leaf&rollups=omit&ids="+parentID, "")
	if status != http.StatusOK || strings.Contains(string(body), parentID) {
		t.Fatalf("parent survived shape=leaf: %d %s", status, body)
	}
	status, body = call(t, &p, http.MethodGet, "/api/nodes?kind=work&shape=leaf&rollups=omit&ids="+ids[0], "")
	if status != http.StatusOK || !strings.Contains(string(body), ids[0]) || strings.Contains(string(body), `"estimate"`) {
		t.Fatalf("leaf omitted or still rolled up: %d %s", status, body)
	}
}

func TestShapeLeafExcludesHiddenChild(t *testing.T) {
	p := newPrincipal(t, "picker-hidden")
	projectKind := kindBySlug(t, p, "project")
	project := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Hidden shape"}`, projectKind.ID))
	parent := workNodeTest(t, p, project.ID, "open")
	child := workNodeTest(t, p, parent.ID, "open")
	leaf := workNodeTest(t, p, project.ID, "open")
	if _, err := appPool.Exec(t.Context(), `CREATE POLICY picker_hidden_child ON nodes AS RESTRICTIVE USING (id<>'`+child.ID+`'::uuid OR aeon_visibility_system())`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := appPool.Exec(context.Background(), `DROP POLICY IF EXISTS picker_hidden_child ON nodes`); err != nil {
			t.Error(err)
		}
	})
	// The canonical probe may see hidden children, but must restore the
	// caller's visibility before any later page enrichment in this transaction.
	err := db.InTenant(tenant.WithPrincipal(t.Context(), p), appPool, p.TenantID, func(tx pgx.Tx) error {
		settings := `SELECT coalesce(current_setting('aeon.visible_projects',true),''),coalesce(current_setting('aeon.system',true),'')`
		var beforeVisible, beforeSystem, afterVisible, afterSystem string
		if err := tx.QueryRow(t.Context(), settings).Scan(&beforeVisible, &beforeSystem); err != nil {
			return err
		}
		rows, err := tx.Query(t.Context(), `SELECT id::text,is_leaf FROM aeon_work_leaf_flags_bounded($1::uuid[])`, []string{parent.ID, leaf.ID, parent.ID, project.ID})
		if err != nil {
			return err
		}
		flags := map[string]bool{}
		count := 0
		for rows.Next() {
			var id string
			var isLeaf bool
			if err := rows.Scan(&id, &isLeaf); err != nil {
				rows.Close()
				return err
			}
			flags[id] = isLeaf
			count++
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if count != 2 || len(flags) != 2 || flags[parent.ID] || !flags[leaf.ID] {
			return fmt.Errorf("canonical flags changed hidden-child, duplicate or non-work semantics: %v", flags)
		}
		if err := tx.QueryRow(t.Context(), settings).Scan(&afterVisible, &afterSystem); err != nil {
			return err
		}
		if beforeVisible != afterVisible || beforeSystem != afterSystem {
			return fmt.Errorf("canonical probe did not restore caller visibility")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	status, body := call(t, &p, http.MethodGet, "/api/nodes?kind=work&shape=leaf&rollups=omit&within="+project.ID, "")
	if status != http.StatusOK || strings.Contains(string(body), parent.ID) || strings.Contains(string(body), child.ID) || !strings.Contains(string(body), leaf.ID) {
		t.Fatalf("hidden child changed the leaf set: %d %s", status, body)
	}
}
