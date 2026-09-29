// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
)

// Assignee sort follows the name the cell shows: the stored person, else the
// lead live worker (worker before coordinator, earliest start). Stopped
// sessions and rows with neither sort last in both directions.
func TestListAssigneeSortUsesLiveWorkerName(t *testing.T) {
	p := newPrincipal(t, "assignee-workers")
	ada := addPrincipalIn(t, p.TenantID, "Ada")
	nia := addPrincipalIn(t, p.TenantID, "Nia")
	project := kindBySlug(t, p, "project")
	ticket := kindBySlug(t, p, "ticket")
	root := mustNode(t, p, `{"kind_id":"`+project.ID+`","title":"Workers"}`)
	create := func(key, title, assignee string) nodeJSON {
		t.Helper()
		fields := map[string]any{}
		if assignee != "" {
			fields["assignee"] = assignee
		}
		raw, _ := json.Marshal(map[string]any{"kind_id": ticket.ID, "key": key, "title": title, "state": "new", "parent_id": root.ID, "fields": fields})
		return mustNode(t, p, string(raw))
	}
	stored := create("PAI-1", "Stored Ada", ada.ID)
	workers := create("PAI-2", "Two workers", "")
	both := create("PAI-3", "Stored Nia and a worker", nia.ID)
	empty := create("PAI-4", "Nobody", "")
	stoppedOnly := create("PAI-5", "Stopped only", "")
	_ = stored
	_ = empty

	agent := func(name string) string {
		t.Helper()
		var id string
		err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `INSERT INTO principals (tenant_id, kind, name) VALUES ($1, 'agent', $2) RETURNING id::text`, p.TenantID, name).Scan(&id)
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	zed, uma, bea, aaa := agent("Zed"), agent("Uma"), agent("Bea"), agent("Aaa")
	session := func(principal, node, role, label, phase string, hoursAgo int, stopped bool) {
		t.Helper()
		err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `INSERT INTO harness_sessions (
                tenant_id, project_id, agent_principal_id, ticket_node_id, harness, host, management, role, work_shape,
                ref_digest, lease_digest, phase, activity, display_label, created_at, heartbeat_at, stopped_at, stop_reason)
                VALUES ($1, $2, $3, $4, 'claude', 'test', 'unmanaged', $5, 'ship',
                    decode(replace(gen_random_uuid()::text, '-', ''), 'hex'),
                    decode(replace(gen_random_uuid()::text, '-', ''), 'hex'),
                    $6, 'busy', nullif($7::text, ''), clock_timestamp() - make_interval(hours => $8::int), clock_timestamp(),
                    CASE WHEN $9 THEN clock_timestamp() END, CASE WHEN $9 THEN 'completed' END)`,
				p.TenantID, root.ID, principal, node, role, phase, label, hoursAgo, stopped)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	// On PAI-2 the chip's lead is Kai: the earlier worker, not the earlier
	// coordinator, not the later worker, and not the stopped session whose
	// label would sort first. The principal is named Zed; the label is Kai.
	session(bea, workers.ID, "coordinator", "Bea", "working", 4, false)
	session(zed, workers.ID, "worker", "Kai", "working", 2, false)
	session(uma, workers.ID, "worker", "Uma", "working", 1, false)
	session(aaa, workers.ID, "worker", "Aaa", "stopped", 5, true)
	// A stored name wins over a live worker who would sort earlier.
	session(aaa, both.ID, "worker", "Aaa", "working", 1, false)
	// A stopped session alone does not name the row.
	session(bea, stoppedOnly.ID, "worker", "Bea", "stopped", 1, true)

	keys := func(sort string) []string {
		t.Helper()
		status, body := call(t, &p, http.MethodGet, "/api/nodes?within="+root.ID+"&kind=ticket&sort="+sort, "")
		page := decode[nodePage](t, status, body, http.StatusOK)
		out := make([]string, 0, len(page.Items))
		for _, item := range page.Items {
			out = append(out, item.Key)
		}
		return out
	}
	asc, desc := keys("assignee"), keys("-assignee")
	if strings.Join(asc[:3], ",") != "PAI-1,PAI-2,PAI-3" {
		t.Fatalf("assignee asc: %v", asc)
	}
	if strings.Join(desc[:3], ",") != "PAI-3,PAI-2,PAI-1" {
		t.Fatalf("assignee desc: %v", desc)
	}
	for _, got := range [][]string{asc, desc} {
		tail := strings.Join(got[3:], ",")
		if !strings.Contains(tail, "PAI-4") || !strings.Contains(tail, "PAI-5") || strings.Contains(tail, "PAI-1") {
			t.Fatalf("empty rows not last: %v", got)
		}
	}

	// The worker lookup is not part of any other sort.
	plain, _ := listSQL(listQuery{Sort: []sortKey{{Name: "updated_at", Desc: true}}, Limit: 50}, nil)
	if strings.Contains(plain, "harness_sessions") {
		t.Fatal("updated sort joined harness_sessions")
	}
	sorted, _ := listSQL(listQuery{Sort: []sortKey{{Name: "assignee"}}, Limit: 50}, nil)
	if !strings.Contains(sorted, "harness_sessions") || !strings.Contains(sorted, "ap.name IS NULL") {
		t.Fatal("assignee sort missing the worker lateral")
	}

	// About 300 tickets and enough live sessions that a per-row sequential scan
	// would show up. The lateral should probe harness_sessions_ticket_eta.
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO nodes (tenant_id, key, kind_id, title, state, parent_id, position)
            SELECT $1, 'BULK-'||g, $2, 'Bulk '||g, 'new', $3, g FROM generate_series(1, 300) g`, p.TenantID, ticket.ID, root.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO harness_sessions (
            tenant_id, project_id, agent_principal_id, ticket_node_id, harness, host, management, role, work_shape,
            ref_digest, lease_digest, phase, activity, created_at, heartbeat_at)
            SELECT $1, $2, $3, n.id, 'claude', 'test', 'unmanaged', 'worker', 'ship',
                decode(md5(n.id::text||g::text), 'hex'), decode(md5(g::text||n.id::text), 'hex'),
                'working', 'busy', clock_timestamp(), clock_timestamp()
            FROM nodes n
            CROSS JOIN generate_series(1, 4) g
            WHERE n.tenant_id=$1 AND n.key LIKE 'BULK-%'`, p.TenantID, root.ID, zed)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := appPool.Exec(t.Context(), `ANALYZE nodes, harness_sessions`); err != nil {
		t.Fatal(err)
	}
	q, err := parseListQuery(httptest.NewRequest(http.MethodGet, "/api/nodes?within="+root.ID+"&kind=ticket&sort=assignee&limit=50", nil))
	if err != nil {
		t.Fatal(err)
	}
	sqlText, args := listSQL(q, nil)
	var planRaw string
	err = db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), "EXPLAIN (ANALYZE, FORMAT JSON) "+sqlText, args...).Scan(&planRaw)
	})
	if err != nil {
		t.Fatal(err)
	}
	var explained []map[string]any
	if err := json.Unmarshal([]byte(planRaw), &explained); err != nil {
		t.Fatal(err)
	}
	var indexes []string
	var seqLoops float64
	var walk func(node map[string]any)
	walk = func(node map[string]any) {
		if node["Relation Name"] == "harness_sessions" {
			if name, ok := node["Index Name"].(string); ok {
				indexes = append(indexes, name)
			}
			if node["Node Type"] == "Seq Scan" {
				if loops, ok := node["Actual Loops"].(float64); ok {
					seqLoops += loops
				}
			}
		}
		plans, _ := node["Plans"].([]any)
		for _, child := range plans {
			if next, ok := child.(map[string]any); ok {
				walk(next)
			}
		}
	}
	for _, rootPlan := range explained {
		if plan, ok := rootPlan["Plan"].(map[string]any); ok {
			walk(plan)
		}
		if ms, ok := rootPlan["Execution Time"].(float64); ok {
			t.Logf("assignee sort of ~300 tickets: %.1f ms (session indexes: %v)", ms, indexes)
			if ms > 500 {
				t.Errorf("assignee sort exceeded 500ms: %.1f", ms)
			}
		}
	}
	if seqLoops > 1 || !slices.Contains(indexes, "harness_sessions_ticket_eta") {
		t.Fatalf("assignee sort did not probe harness_sessions_ticket_eta (seq loops %.0f, indexes %v)", seqLoops, indexes)
	}
}
