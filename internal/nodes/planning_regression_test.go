// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
)

func TestPlanningSourceProjectCosts(t *testing.T) {
	w := planningSetup(t)
	target := w.node(t, "TARGET-1", "ticket", w.root.ID, "open", map[string]any{"route_role": "build-hard", "area": "backend", "estimate_hours": 2})
	w.session(t, target.ID, "codex", "gpt-6-astra", "xhigh", "gpt-6-astra", 60, 1000, 0, 0, "api", "")
	other := w
	other.root = mustNode(t, w.admin, `{"kind_id":"`+kindBySlug(t, w.admin, "project").ID+`","title":"Other project"}`)
	bindProjectRole(t, w.admin.TenantID, w.viewer.ID, "viewer", w.root.ID)
	bindProjectRole(t, w.admin.TenantID, w.viewer.ID, "guest", other.root.ID)
	for i := range 5 {
		n := other.node(t, fmt.Sprintf("PRIVATE-%d", i+1), "ticket", other.root.ID, "done", nil)
		other.session(t, n.ID, "codex", "gpt-6-astra", "xhigh", "gpt-6-astra", 60, 1_000_000, 0, 0, "api", "")
	}
	// Make the source's actual price unmistakable; it must not calibrate A's cost.
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_session_usage u SET estimated_cost_usd=777
            FROM harness_sessions s WHERE s.tenant_id=u.tenant_id AND s.id=u.session_id AND s.project_id=$1`, other.root.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	private := other.node(t, "PRIVATE-6", "ticket", other.root.ID, "open", nil)
	other.session(t, private.ID, "codex", "gpt-6-astra", "xhigh", "gpt-6-astra", 60, 1000, 0, 0, "subscription", "B confidential plan")
	path := "/api/nodes?within=" + w.root.ID + "&kind=ticket"
	for _, sort := range []string{"key", "tokens", "list_cost", "paid"} {
		got := planningOf(t, w.viewer, path+"&sort="+sort)[target.Key]
		if got == nil || got.Cost == nil || got.Cost.ListEstimated == nil || got.Cost.PaidEstimated == nil {
			t.Fatalf("missing allowed estimate: %+v", got)
		}
		if len(got.Cost.Plans) != 0 || *got.Cost.PaidEstimated != *got.Cost.ListEstimated {
			t.Fatalf("source billing leaked: %+v", got.Cost)
		}
		// Tokens are intentionally visible; only the public model-price mix may
		// price them without source harness.read (2M tokens at $2.70/M).
		if *got.Cost.ListEstimated != "5.400000" {
			t.Fatalf("source calibration cost leaked: %+v", got.Cost)
		}
		otherView := planningOf(t, w.viewer, "/api/nodes?within="+other.root.ID+"&sort="+sort)[private.Key]
		if otherView == nil || otherView.Cost != nil {
			t.Fatalf("B cost projection: %+v", otherView)
		}
	}
	admin := planningOf(t, w.admin, path)[target.Key]
	if !slices.Contains(admin.Cost.Plans, "B confidential plan") || !strings.HasPrefix(*admin.Cost.ListEstimated, "1553.") && !strings.HasPrefix(*admin.Cost.ListEstimated, "1554.") {
		t.Fatalf("authorized source should contribute: %+v", admin.Cost)
	}
}

// Bulk fixtures keep the regression realistic without hundreds of HTTP writes.
func planningBulk(t *testing.T, w planningWorld, count, sessions int, state, prefix string, reported bool) []string {
	t.Helper()
	kind := kindBySlug(t, w.admin, "ticket")
	var ids []string
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(t.Context(), `INSERT INTO nodes (tenant_id,key,kind_id,title,state,parent_id,position,fields)
            SELECT $1,$2::text||g,$3,$2::text||g,$4,$5,g,'{"estimate_hours":2,"route_role":"build-hard","area":"backend"}'::jsonb
            FROM generate_series(1,$6::int) g RETURNING id::text`, w.admin.TenantID, prefix, kind.ID, state, w.root.ID, count)
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
		_, err = tx.Exec(t.Context(), `INSERT INTO harness_sessions (
            tenant_id,project_id,agent_principal_id,ticket_node_id,harness,host,management,role,work_shape,
            ref_digest,lease_digest,phase,activity,model,reasoning_effort,created_at,heartbeat_at,stopped_at,stop_reason)
            SELECT $1,$2,$3,n,'codex','test','unmanaged','worker','ship',
                decode(md5(n::text||g::text),'hex'),decode(md5(g::text||n::text),'hex'),
                'stopped','busy','gpt-6-astra','xhigh',now()-interval '1 hour',now(),now(),'completed'
            FROM unnest($4::uuid[]) n CROSS JOIN generate_series(1,$5::int) g`, w.admin.TenantID, w.root.ID, w.agent, ids, sessions)
		if err != nil || !reported {
			return err
		}
		_, err = tx.Exec(t.Context(), `INSERT INTO harness_session_usage (tenant_id,session_id,model,sequence,input_tokens,output_tokens,cached_input_tokens,provisional,billing_mode,subscription_label)
            SELECT tenant_id,id,'gpt-6-astra',1,1000,0,0,false,'subscription','Test plan'
            FROM harness_sessions WHERE tenant_id=$1 AND ticket_node_id=ANY($2::uuid[])`, w.admin.TenantID, ids)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

func TestPlanningCalibrationSkipsIneligibleAndOtherRoutes(t *testing.T) {
	w := planningSetup(t)
	target := w.node(t, "CAL-1", "ticket", w.root.ID, "open", map[string]any{"route_role": "build-hard", "area": "backend", "estimate_hours": 2})
	for i := range 5 {
		n := w.node(t, fmt.Sprintf("FIN-%d", i+1), "ticket", w.root.ID, "done", nil)
		w.session(t, n.ID, "codex", "gpt-6-astra", "xhigh", "gpt-6-astra", 60, 1_000_000, 0, 0, "api", "")
	}
	planningBulk(t, w, 400, 1, "done", "UNREPORTED-", false)
	// Thirty newer eligible samples from another route must not displace Codex.
	ids := planningBulk(t, w, 30, 1, "done", "OTHER-", true)
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET harness='claude',model='opus' WHERE ticket_node_id=ANY($1::uuid[])`, ids)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	got := planningOf(t, w.admin, "/api/nodes?within="+w.root.ID+"&q=CAL-1")[target.Key]
	cal := got.Tokens.Calibration
	if cal == nil || cal.Basis != "median" || cal.Tickets != 5 || math.Abs(float64(cal.TokensPerHour)-1_000_000) > 100 {
		t.Fatalf("newer unusable samples displaced calibration: %+v", cal)
	}
}

func TestPlanningSortUsesDisplayedNumbersAndCursor(t *testing.T) {
	w := planningSetup(t)
	// Both routes have prices, but only the high route has a 100M/h calibration.
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE model_role_routes SET profile_id=(SELECT id FROM model_profiles WHERE slug='codex-sol-xhigh') WHERE role='build'`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		n := w.node(t, fmt.Sprintf("TRAIN-%d", i+1), "ticket", w.root.ID, "done", nil)
		w.session(t, n.ID, "codex", "gpt-6-astra", "xhigh", "gpt-6-astra", 60, 100_000_000, 0, 0, "api", "")
	}
	high := w.node(t, "SORT-1", "ticket", w.root.ID, "open", map[string]any{"route_role": "build-hard", "area": "backend", "estimate_hours": 1})
	low := w.node(t, "SORT-2", "ticket", w.root.ID, "open", map[string]any{"route_role": "build", "area": "backend", "estimate_hours": 2})
	tie := w.node(t, "SORT-3", "ticket", w.root.ID, "open", map[string]any{"route_role": "build", "area": "backend", "estimate_hours": 2})
	spent := w.node(t, "SORT-4", "ticket", w.root.ID, "open", map[string]any{"route_role": "build-hard", "area": "backend", "estimate_hours": 100})
	w.session(t, spent.ID, "codex", "gpt-6-astra", "xhigh", "gpt-6-astra", 60, 0, 0, 0, "api", "")
	empty := w.node(t, "SORT-5", "ticket", w.root.ID, "open", nil)
	ties := []nodeJSON{low, tie}
	slices.SortFunc(ties, func(a, b nodeJSON) int { return strings.Compare(a.ID, b.ID) })
	for _, field := range []string{"tokens", "list_cost", "paid"} {
		for _, desc := range []bool{false, true} {
			sort := field
			want := []string{spent.Key, ties[0].Key, ties[1].Key, high.Key, empty.Key}
			if desc {
				sort = "-" + field
				want = []string{high.Key, ties[0].Key, ties[1].Key, spent.Key, empty.Key}
			}
			path := "/api/nodes?within=" + w.root.ID + "&q=SORT-&sort=" + sort + "&limit=2"
			var got []string
			for cursor := ""; ; {
				page := listPage(t, w.admin, path+cursor)
				for _, item := range page.Items {
					got = append(got, item.Key)
				}
				if page.NextCursor == nil {
					break
				}
				cursor = "&cursor=" + *page.NextCursor
			}
			if !slices.Equal(got, want) {
				t.Fatalf("%s: got %v want %v", sort, got, want)
			}
		}
	}
}

func TestPlanningRoundedCostTie(t *testing.T) {
	w := planningSetup(t)
	first := w.node(t, "RND-1", "ticket", w.root.ID, "open", nil)
	second := w.node(t, "RND-2", "ticket", w.root.ID, "open", nil)
	w.session(t, first.ID, "codex", "gpt-6-astra", "xhigh", "gpt-6-astra", 60, 1000, 0, 0, "api", "")
	w.session(t, second.ID, "codex", "gpt-6-astra", "xhigh", "gpt-6-astra", 60, 1000, 0, 0, "api", "")
	// Digits past the six the API projects. The lower id holds the larger raw
	// amount, so an unrounded ascending sort would put it second.
	ordered := []nodeJSON{first, second}
	slices.SortFunc(ordered, func(a, b nodeJSON) int { return strings.Compare(a.ID, b.ID) })
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_session_usage u SET estimated_cost_usd=v.cost
			FROM harness_sessions s
			JOIN (VALUES ($2::uuid, 1.0000004::numeric), ($3::uuid, 1.0000001::numeric)) AS v(ticket, cost) ON s.ticket_node_id=v.ticket
			WHERE u.tenant_id=s.tenant_id AND u.session_id=s.id AND s.tenant_id=$1`, w.admin.TenantID, ordered[0].ID, ordered[1].ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	views := planningOf(t, w.admin, "/api/nodes?within="+w.root.ID+"&q=RND-")
	for _, n := range ordered {
		cost := views[n.Key].Cost
		if cost == nil || cost.ListSpent == nil || cost.PaidSpent == nil || *cost.ListSpent != "1.000000" || *cost.PaidSpent != "1.000000" {
			t.Fatalf("%s projected cost: %+v", n.Key, cost)
		}
	}
	want := []string{ordered[0].Key, ordered[1].Key}
	for _, sort := range []string{"list_cost", "-list_cost", "paid", "-paid"} {
		got := listKeys(t, w.admin, "/api/nodes?within="+w.root.ID+"&q=RND-&sort="+sort)
		if !slices.Equal(got, want) {
			t.Fatalf("%s rounded tie: got %v want %v", sort, got, want)
		}
	}
}

func TestPlanningBulkUsagePerformance(t *testing.T) {
	w := planningSetup(t)
	ids := planningBulk(t, w, 1000, 4, "open", "PERF-", true)
	if _, err := appPool.Exec(t.Context(), `ANALYZE nodes, harness_sessions, harness_session_usage, model_prices`); err != nil {
		t.Fatal(err)
	}
	path := "/api/nodes?within=" + w.root.ID + "&kind=ticket&sort=tokens&limit=50"
	var fastest time.Duration
	for range 3 {
		start := time.Now()
		page := listPage(t, w.admin, path)
		elapsed := time.Since(start)
		if len(page.Items) != 50 || page.Items[0].Planning.Tokens.Spent == nil || *page.Items[0].Planning.Tokens.Spent != 4000 {
			t.Fatal("bulk usage totals")
		}
		if fastest == 0 || elapsed < fastest {
			fastest = elapsed
		}
	}
	t.Logf("1000 tickets / 4000 usage rows, complete list request: %s", fastest)
	if fastest >= 200*time.Millisecond {
		t.Errorf("planning request exceeded 200ms: %s", fastest)
	}
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, w.admin.TenantID, func(tx pgx.Tx) error {
		q, err := parseListQuery(httptest.NewRequest(http.MethodGet, path, nil))
		if err != nil {
			return err
		}
		q.seen = assigneeSeen{harnessAll: true, members: true}
		if err := preparePlanningSort(t.Context(), tx, &q); err != nil {
			return err
		}
		order, args := listSQL(q, nil)
		for _, probe := range []struct {
			name, sql string
			args      []any
		}{{"bulk usage", planUsageSQL(), []any{ids}}, {"ordered page", order, args}} {
			var raw string
			if err := tx.QueryRow(t.Context(), "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+probe.sql, probe.args...).Scan(&raw); err != nil {
				return err
			}
			t.Logf("EXPLAIN %s: %s", probe.name, raw)
			var plan []map[string]any
			if err := json.Unmarshal([]byte(raw), &plan); err != nil {
				return err
			}
			var check func(map[string]any)
			check = func(p map[string]any) {
				if p["Relation Name"] == "harness_session_usage" && p["Node Type"] == "Seq Scan" && p["Actual Loops"].(float64) > 1 {
					t.Errorf("repeated full usage scan: %v", p)
				}
				children, _ := p["Plans"].([]any)
				for _, child := range children {
					check(child.(map[string]any))
				}
			}
			check(plan[0]["Plan"].(map[string]any))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
