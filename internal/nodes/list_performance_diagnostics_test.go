// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
)

// Only synthetic performance fixtures call this, after the measured requests.
// Keep the failed budget unchanged and collect each actual query once, bounded
// by both a statement and an overall timeout, under the request's RLS context.
func logListPerformancePlans(t *testing.T, p tenant.Principal, path string) map[string]any {
	t.Helper()
	plans := map[string]any{}
	q, err := parseListQuery(httptest.NewRequest(http.MethodGet, path, nil))
	if err != nil {
		t.Log("performance diagnostics: invalid fixture query")
		return plans
	}
	ctx, cancel := context.WithTimeout(tenant.WithPrincipal(t.Context(), p), 12*time.Second)
	defer cancel()
	ctx = db.WithReadStatementTimeout(ctx, 5*time.Second)
	t.Logf("performance diagnostics: CPUs=%d GOMAXPROCS=%d", runtime.NumCPU(), runtime.GOMAXPROCS(0))
	listText, listArgs := listSQL(q, nil)
	facetText, facetArgs := facetSQL(q)
	for _, query := range []struct {
		name string
		sql  string
		args []any
	}{
		{"list", listText, listArgs},
		{"facets", facetText, facetArgs},
	} {
		err := New(appPool, nil).(*Module).tx(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
			var settings []byte
			if err := tx.QueryRow(ctx, `SELECT jsonb_build_object(
				'server_version_num',current_setting('server_version_num'),
				'jit',current_setting('jit'),
				'work_mem',current_setting('work_mem'),
				'random_page_cost',current_setting('random_page_cost'),
				'effective_cache_size',current_setting('effective_cache_size'),
				'shared_buffers',current_setting('shared_buffers'),
				'plan_cache_mode',current_setting('plan_cache_mode'),
				'max_parallel_workers_per_gather',current_setting('max_parallel_workers_per_gather'),
				'rls',row_security_active('nodes'))`).Scan(&settings); err != nil {
				return err
			}
			t.Logf("%s planner settings: %s", query.name, settings)
			var statistics []byte
			if err := tx.QueryRow(ctx, `SELECT coalesce(jsonb_agg(jsonb_build_object(
				'table',c.relname,'estimated_rows',c.reltuples,'pages',c.relpages,
				'live_rows',s.n_live_tup,'dead_rows',s.n_dead_tup,
				'modified_since_analyze',s.n_mod_since_analyze,
				'analyzed',s.last_analyze IS NOT NULL,'autoanalyzed',s.last_autoanalyze IS NOT NULL)
				ORDER BY c.relname),'[]'::jsonb)
				FROM pg_class c JOIN pg_stat_user_tables s ON s.relid=c.oid
				WHERE c.relname IN ('nodes','node_kinds','principals','identities')`).Scan(&statistics); err != nil {
				return err
			}
			t.Logf("%s relation statistics: %s", query.name, statistics)
			var cached []byte
			var preparedName *string
			if err := tx.QueryRow(ctx, `SELECT jsonb_build_object('count',count(*),
				'generic_plans',coalesce(sum(generic_plans),0),
				'custom_plans',coalesce(sum(custom_plans),0)),min(name)
				FROM pg_prepared_statements WHERE statement=$1`, query.sql).Scan(&cached, &preparedName); err != nil {
				return err
			}
			t.Logf("%s cached plans on diagnostic connection: %s", query.name, cached)
			explain := "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, TIMING OFF) " + query.sql
			args := query.args
			if preparedName != nil {
				// EXPLAIN EXECUTE retains the cached statement's custom/generic
				// choice. pgx safely quotes its arguments via the simple protocol;
				// an ordinary EXPLAIN would plan a fresh statement instead.
				params := make([]string, len(args))
				for i := range params {
					params[i] = fmt.Sprintf("$%d", i+1)
				}
				explain = "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, TIMING OFF) EXECUTE " + pgx.Identifier{*preparedName}.Sanitize() + "(" + strings.Join(params, ",") + ")"
				args = append([]any{pgx.QueryExecModeSimpleProtocol}, args...)
			}
			var raw []byte
			if err := tx.QueryRow(ctx, explain, args...).Scan(&raw); err != nil {
				return err
			}
			var plan any
			if err := json.Unmarshal(raw, &plan); err != nil {
				return err
			}
			plans[query.name] = safePerformancePlan(plan)
			clean, err := json.Marshal(plans[query.name])
			if err != nil {
				return err
			}
			t.Logf("%s plan: %s", query.name, clean)
			return nil
		})
		if err != nil {
			// Database errors can include parameter values. The error type and
			// timeout flag suffice here; never print SQL, arguments or row data.
			t.Logf("%s diagnostics failed: %T (statement timeout=%t)", query.name, err, db.IsStatementTimeout(err))
		}
	}
	return plans
}

func filteredNodeVisits(value any, filtered bool) float64 {
	var visits float64
	switch value := value.(type) {
	case []any:
		for _, item := range value {
			visits += filteredNodeVisits(item, filtered)
		}
	case map[string]any:
		filtered = filtered || value["Subplan Name"] == "CTE filtered"
		if filtered && value["Relation Name"] == "nodes" {
			rows, _ := value["Actual Rows"].(float64)
			removed, _ := value["Rows Removed by Filter"].(float64)
			loops, _ := value["Actual Loops"].(float64)
			visits += (rows + removed) * loops
		}
		visits += filteredNodeVisits(value["Plan"], filtered)
		visits += filteredNodeVisits(value["Plans"], filtered)
	}
	return visits
}

func TestSafeListPerformancePlan(t *testing.T) {
	var raw any
	if err := json.Unmarshal([]byte(`[{"Plan":{"Node Type":"Index Scan","Relation Name":"nodes",
		"Actual Rows":51,"Actual Loops":1,"Shared Hit Blocks":153,
		"Filter":"private fixture payload","Index Cond":"private fixture identifier",
		"Output":["private fixture value"],"Plans":[{"Node Type":"Result","Actual Rows":0,
		"One-Time Filter":"private fixture setting"}]},"Execution Time":30.2,
		"JIT":{"Functions":2,"Timing":{"Total":1.5}},"Settings":{"custom":"private fixture setting"}}]`), &raw); err != nil {
		t.Fatal(err)
	}
	clean, err := json.Marshal(safePerformancePlan(raw))
	if err != nil {
		t.Fatal(err)
	}
	const want = `[{"Execution Time":30.2,"JIT":{"Functions":2,"Timing":{"Total":1.5}},"Plan":{"Actual Loops":1,"Actual Rows":51,"Node Type":"Index Scan","Plans":[{"Actual Rows":0,"Node Type":"Result"}],"Relation Name":"nodes","Shared Hit Blocks":153}}]`
	if string(clean) != want {
		t.Fatalf("unexpected sanitized plan: %s", clean)
	}
}

// Exclude expressions, outputs, conditions and settings that may embed values.
// Numeric counters plus structural labels retain the useful plan evidence.
func safePerformancePlan(value any) any {
	switch value := value.(type) {
	case []any:
		out := make([]any, len(value))
		for i, item := range value {
			out[i] = safePerformancePlan(item)
		}
		return out
	case map[string]any:
		out := map[string]any{}
		for key, item := range value {
			switch key {
			case "Plan", "Plans", "Planning", "JIT", "Options", "Timing", "Workers":
				out[key] = safePerformancePlan(item)
			case "Node Type", "Join Type", "Relation Name", "Index Name", "CTE Name", "Subplan Name", "Strategy", "Sort Method":
				out[key] = item
			default:
				switch item := item.(type) {
				case float64:
					if item != 0 || key == "Actual Rows" || key == "Actual Loops" {
						out[key] = item
					}
				case bool:
					if item {
						out[key] = item
					}
				}
			}
		}
		return out
	default:
		return nil
	}
}

// Capture the real page-money statement, with the same RLS, rates and IDs as
// loadPlanMicros. EXPLAIN runs only for this synthetic fixture, after timing.
type pagePlanningExplainTx struct {
	pgx.Tx
	plan any
}

func (tx *pagePlanningExplainTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if strings.Contains(sql, "planning_values AS MATERIALIZED") {
		var raw []byte
		if err := tx.Tx.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, TIMING OFF) "+sql, args...).Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &tx.plan); err != nil {
			return nil, err
		}
	}
	return tx.Tx.Query(ctx, sql, args...)
}
func logPagePlanningPerformancePlan(t *testing.T, p tenant.Principal, ids []string) any {
	t.Helper()
	ctx, cancel := context.WithTimeout(tenant.WithPrincipal(t.Context(), p), 12*time.Second)
	defer cancel()
	ctx = db.WithReadStatementTimeout(ctx, 5*time.Second)
	var plan any
	err := New(appPool, nil).(*Module).tx(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		seen, err := assigneeAudience(ctx, tx, p)
		if err != nil {
			return err
		}
		recording := &pagePlanningExplainTx{Tx: tx}
		money, err := loadPlanMicros(ctx, recording, ids, seen)
		if err != nil {
			return err
		}
		if len(money) != len(ids) {
			return fmt.Errorf("planning rows = %d, want %d", len(money), len(ids))
		}
		plan = safePerformancePlan(recording.plan)
		return nil
	})
	if err != nil {
		t.Fatalf("planning performance diagnostics failed: %T (statement timeout=%t)", err, db.IsStatementTimeout(err))
	}
	clean, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("page planning plan: %s", clean)
	return plan
}

// Count node visits only within the subtree CTE. The fixture has no children,
// so scanning all tenant tickets for every selected row is wasted work.
func planningSubtreeWork(value any, subtree bool) (found bool, rows, visits float64) {
	switch value := value.(type) {
	case []any:
		for _, item := range value {
			f, r, v := planningSubtreeWork(item, subtree)
			found = found || f
			rows += r
			visits += v
		}
	case map[string]any:
		if value["Subplan Name"] == "CTE plan_sub" {
			found = true
			rows, _ = value["Actual Rows"].(float64)
			subtree = true
		}
		if subtree && value["Relation Name"] == "nodes" {
			n, _ := value["Actual Rows"].(float64)
			removed, _ := value["Rows Removed by Filter"].(float64)
			loops, _ := value["Actual Loops"].(float64)
			visits += (n + removed) * loops
		}
		for _, key := range []string{"Plan", "Plans"} {
			f, r, v := planningSubtreeWork(value[key], subtree)
			found = found || f
			rows += r
			visits += v
		}
	}
	return
}
