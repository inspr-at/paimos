// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

// Assignee sort follows the name the cell shows: the stored person, else the
// lead live worker (attention state, then a worker before a coordinator, then
// the earliest start). Stopped sessions and rows with neither sort last in
// both directions.
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
	plainPage := listPage(t, p, "/api/nodes?within="+root.ID+"&kind=ticket&sort=updated_at")
	for _, item := range plainPage.Items {
		switch item.Key {
		case "PAI-2":
			if item.LeadWorker == nil || item.LeadWorker.Name != "Kai" || !strings.HasPrefix(item.LeadWorker.Key, "s:") {
				t.Fatalf("unsorted lead: %#v", item.LeadWorker)
			}
		case "PAI-4", "PAI-5":
			if item.LeadWorker != nil {
				t.Fatalf("%s named a lead: %#v", item.Key, item.LeadWorker)
			}
		}
	}
	sortedLead := leadOn(t, p, "/api/nodes?within="+root.ID+"&kind=ticket&sort=assignee", both.Key)
	plainLead := leadOn(t, p, "/api/nodes?within="+root.ID+"&kind=ticket&sort=updated_at", both.Key)
	if sortedLead == nil || plainLead == nil || sortedLead.Name != "Aaa" || sortedLead.Name != plainLead.Name || sortedLead.Key != plainLead.Key {
		t.Fatalf("assigned lead sorted %#v plain %#v", sortedLead, plainLead)
	}

	// Other sorts look up workers only after paging, in the same statement.
	plain, _ := listSQL(listQuery{Sort: []sortKey{{Name: "updated_at", Desc: true}}, Limit: 50}, nil)
	if strings.Contains(strings.Split(plain, "selected AS")[0], "harness_sessions") || !strings.Contains(plain, "harness_sessions") || strings.Contains(plain, "ap.name IS NULL") || strings.Contains(plain, "sess.") {
		t.Fatal("updated sort must project workers only after paging")
	}
	sorted, _ := listSQL(listQuery{Sort: []sortKey{{Name: "assignee"}}, Limit: 50}, nil)
	before, after, cut := strings.Cut(sorted, "selected AS")
	if !cut || !strings.Contains(before, "harness_sessions sess") || !strings.Contains(before, "ap.name IS NULL") || !strings.Contains(before, "AS lead_name") || !strings.Contains(before, "lead_evaluated") || strings.Contains(sorted, "sessess") {
		t.Fatal("assignee sort must probe a live lead only when the row has no stored person, and carry that lead")
	}
	if !strings.Contains(after, "s.lead_name") || !strings.Contains(after, "NOT s.lead_evaluated") || !strings.Contains(after, "harness_sessions sess") || strings.Contains(sorted, "s.lead_name IS NULL") {
		t.Fatal("assigned rows must take their lead from the page, and an empty lookup must stay evaluated")
	}

	// About 300 tickets and enough live sessions that a per-row sequential scan
	// would show up. The lateral should probe a ticket index: the partial
	// harness_sessions_ticket_eta, or harness_sessions_ticket_node (AEON-329).
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
	// The measured plan is the one a workspace reader runs: labels and names visible.
	q.seen = assigneeSeen{harnessAll: true, members: true}
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
	if seqLoops > 1 || (!slices.Contains(indexes, "harness_sessions_ticket_eta") && !slices.Contains(indexes, "harness_sessions_ticket_node")) {
		t.Fatalf("assignee sort did not probe a ticket session index (seq loops %.0f, indexes %v)", seqLoops, indexes)
	}
}

// Stored people are the assignee sort key, so their live leads are read with
// the page rather than once per filtered row. Sort and display stay one statement.
func TestListAssigneeSortSkipsLiveLookupForStoredPeople(t *testing.T) {
	p := newPrincipal(t, "assignee-stored-perf")
	project := kindBySlug(t, p, "project")
	ticket := kindBySlug(t, p, "ticket")
	root := mustNode(t, p, `{"kind_id":"`+project.ID+`","title":"Stored"}`)
	sam := addPrincipalIn(t, p.TenantID, "Sam")
	zed := insertNamedAgent(t, p.TenantID, "Zed")
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO nodes (tenant_id, key, kind_id, title, state, parent_id, position, fields)
            SELECT $1, 'OWN-'||g, $2, 'Owned '||g, 'new', $3, g, jsonb_build_object('assignee', $4::text)
            FROM generate_series(1, 300) g`, p.TenantID, ticket.ID, root.ID, sam.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO harness_sessions (
            tenant_id, project_id, agent_principal_id, ticket_node_id, harness, host, management, role, work_shape,
            ref_digest, lease_digest, phase, activity, display_label, created_at, heartbeat_at)
            SELECT $1, $2, $3, n.id, 'claude', 'test', 'unmanaged', 'worker', 'ship',
                decode(md5(n.id::text||g::text), 'hex'), decode(md5(g::text||n.id::text), 'hex'),
                'working', 'busy', 'Zara', clock_timestamp() - make_interval(hours => g), clock_timestamp()
            FROM nodes n
            CROSS JOIN generate_series(1, 4) g
            WHERE n.tenant_id=$1 AND n.key LIKE 'OWN-%'`, p.TenantID, root.ID, zed)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	makeOpen := func(key string) nodeJSON {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"kind_id": ticket.ID, "key": key, "title": key, "state": "new", "parent_id": root.ID})
		return mustNode(t, p, string(raw))
	}
	open1, open2 := makeOpen("OPEN-1"), makeOpen("OPEN-2")
	insertLiveSession(t, p.TenantID, root.ID, zed, open1.ID, "claude", "worker", "Zara", "working", "busy", "", 30)
	insertLiveSession(t, p.TenantID, root.ID, zed, open2.ID, "claude", "worker", "Zara", "working", "busy", "", 20)
	if _, err := appPool.Exec(t.Context(), `ANALYZE nodes, principals, harness_sessions`); err != nil {
		t.Fatal(err)
	}

	page := listPage(t, p, "/api/nodes?within="+root.ID+"&kind=ticket&sort=assignee&limit=50")
	if len(page.Items) != 50 {
		t.Fatalf("page %d", len(page.Items))
	}
	for _, item := range page.Items {
		if item.Assignee == nil || item.Assignee.Name != "Sam" || item.LeadWorker == nil || item.LeadWorker.Name != "Zara" {
			t.Fatalf("stored row %s assignee %#v lead %#v", item.Key, item.Assignee, item.LeadWorker)
		}
	}
	// "Zara" sorts after "Sam", so these two are the tail and still have a lead.
	tail := listPage(t, p, "/api/nodes?within="+root.ID+"&kind=ticket&sort=-assignee&limit=2")
	if len(tail.Items) != 2 {
		t.Fatalf("tail %d", len(tail.Items))
	}
	for _, item := range tail.Items {
		if item.Assignee != nil || item.LeadWorker == nil || item.LeadWorker.Name != "Zara" {
			t.Fatalf("open row %s assignee %#v lead %#v", item.Key, item.Assignee, item.LeadWorker)
		}
	}

	q, err := parseListQuery(httptest.NewRequest(http.MethodGet, "/api/nodes?within="+root.ID+"&kind=ticket&sort=assignee&limit=50", nil))
	if err != nil {
		t.Fatal(err)
	}
	q.seen = assigneeSeen{harnessAll: true, members: true}
	sqlText, args := listSQL(q, nil)
	var planRaw string
	err = db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), "EXPLAIN (ANALYZE, FORMAT JSON) "+sqlText, args...).Scan(&planRaw)
	})
	if err != nil {
		t.Fatal(err)
	}
	indexLoops, seqLoops, indexes := harnessProbeStats(planRaw)
	var explained []map[string]any
	if err := json.Unmarshal([]byte(planRaw), &explained); err != nil {
		t.Fatal(err)
	}
	for _, rootPlan := range explained {
		if ms, ok := rootPlan["Execution Time"].(float64); ok {
			t.Logf("assignee sort of 300 stored people: %.1f ms, %.0f index loops (indexes %v)", ms, indexLoops, indexes)
			if ms > 500 {
				t.Errorf("assignee sort exceeded 500ms: %.1f", ms)
			}
		}
	}
	// The two open rows are probed while ordering. The page is stored people.
	// A probe of all 300 before paging is at least 300 loops; the page stays well under that.
	if seqLoops > 1 || (!slices.Contains(indexes, "harness_sessions_ticket_eta") && !slices.Contains(indexes, "harness_sessions_ticket_node")) || indexLoops < 40 || indexLoops >= 300 {
		t.Fatalf("stored-person leads were not limited to the page (index loops %.0f, seq %.0f, indexes %v)", indexLoops, seqLoops, indexes)
	}
}

// An assignee sort used to inline the gated lookup and probe each unassigned
// ticket four times. Each unassigned row is probed at most once; assigned
// rows add at most one probe for the page of this same statement.
func TestListAssigneeSortProbesEachUnassignedRowOnce(t *testing.T) {
	p := newPrincipal(t, "assignee-probe-once")
	project := kindBySlug(t, p, "project")
	ticket := kindBySlug(t, p, "ticket")
	root := mustNode(t, p, `{"kind_id":"`+project.ID+`","title":"Probe once"}`)
	sam := addPrincipalIn(t, p.TenantID, "Sam")
	zed := insertNamedAgent(t, p.TenantID, "Zed")
	const tickets = 3000
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO nodes (tenant_id, key, kind_id, title, state, parent_id, position)
            SELECT $1, 'OPEN-'||g, $2, 'Open '||g, 'new', $3, g FROM generate_series(1, $4) g`,
			p.TenantID, ticket.ID, root.ID, tickets); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO harness_sessions (
            tenant_id, project_id, agent_principal_id, ticket_node_id, harness, host, management, role, work_shape,
            ref_digest, lease_digest, phase, activity, display_label, created_at, heartbeat_at)
            SELECT $1, $2, $3, n.id, 'claude', 'test', 'unmanaged', 'worker', 'ship',
                decode(md5(n.id::text), 'hex'), decode(md5('lease'||n.id::text), 'hex'),
                'working', 'busy', 'W-'||n.key, clock_timestamp(), clock_timestamp()
            FROM nodes n
            WHERE n.tenant_id=$1 AND n.key LIKE 'OPEN-%'`, p.TenantID, root.ID, zed)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := appPool.Exec(t.Context(), `ANALYZE nodes, principals, harness_sessions`); err != nil {
		t.Fatal(err)
	}

	page := listPage(t, p, "/api/nodes?within="+root.ID+"&kind=ticket&sort=assignee&limit=20")
	if len(page.Items) != 20 {
		t.Fatalf("page %d", len(page.Items))
	}
	for _, item := range page.Items {
		if item.Assignee != nil || item.LeadWorker == nil || item.LeadWorker.Name != "W-"+item.Key {
			t.Fatalf("unassigned %s assignee %#v lead %#v", item.Key, item.Assignee, item.LeadWorker)
		}
	}
	loops, seq := explainAssigneeProbes(t, p, root.ID)
	// Every row is unassigned, and the page reuses the lead this statement
	// already carried, so the index runs once per row and no more.
	if seq > 1 || loops > tickets || loops < tickets {
		t.Fatalf("unassigned probe loops %.0f, seq %.0f, want %d", loops, seq, tickets)
	}

	const assigned = 1500
	err = db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=jsonb_build_object('assignee', $2::text)
            WHERE tenant_id=$1 AND parent_id=$3 AND key LIKE 'OPEN-%' AND position<=$4`,
			p.TenantID, sam.ID, root.ID, assigned)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := appPool.Exec(t.Context(), `ANALYZE nodes, principals, harness_sessions`); err != nil {
		t.Fatal(err)
	}
	mixed := listPage(t, p, "/api/nodes?within="+root.ID+"&kind=ticket&sort=assignee&limit=50")
	if len(mixed.Items) != 50 {
		t.Fatalf("mixed page %d", len(mixed.Items))
	}
	for _, item := range mixed.Items {
		if item.Assignee == nil || item.Assignee.Name != "Sam" || item.LeadWorker == nil || item.LeadWorker.Name != "W-"+item.Key {
			t.Fatalf("assigned %s assignee %#v lead %#v", item.Key, item.Assignee, item.LeadWorker)
		}
	}
	loops, seq = explainAssigneeProbes(t, p, root.ID)
	unassigned := tickets - assigned
	// Ordering probes the open rows once. The page is stored people, one probe
	// each, and the statement asks for one extra row to decide the cursor.
	const pageRows = 50 + 1
	if seq > 1 || loops > float64(unassigned+pageRows) || loops < float64(unassigned) {
		t.Fatalf("mixed probe loops %.0f, seq %.0f, unassigned %d page %d", loops, seq, unassigned, pageRows)
	}
}

// Stopped sessions are not eligible leads. An evaluated lookup that finds
// nobody used to look like a deferred lookup, because both leave the carried
// name null, and the page probed those rows again. lead_evaluated keeps the
// empty result, so probes stay within the unassigned rows.
func TestListAssigneeSortStoppedOnlyProbesAtMostOnce(t *testing.T) {
	p := newPrincipal(t, "assignee-probe-stopped")
	project := kindBySlug(t, p, "project")
	ticket := kindBySlug(t, p, "ticket")
	root := mustNode(t, p, `{"kind_id":"`+project.ID+`","title":"Stopped probes"}`)
	sam := addPrincipalIn(t, p.TenantID, "Sam")
	zed := insertNamedAgent(t, p.TenantID, "Zed")
	const tickets = 3000
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO nodes (tenant_id, key, kind_id, title, state, parent_id, position)
            SELECT $1, 'STOP-'||g, $2, 'Stopped '||g, 'new', $3, g FROM generate_series(1, $4) g`,
			p.TenantID, ticket.ID, root.ID, tickets); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO harness_sessions (
            tenant_id, project_id, agent_principal_id, ticket_node_id, harness, host, management, role, work_shape,
            ref_digest, lease_digest, phase, activity, display_label, created_at, heartbeat_at, stopped_at, stop_reason)
            SELECT $1, $2, $3, n.id, 'claude', 'test', 'unmanaged', 'worker', 'ship',
                decode(md5(n.id::text), 'hex'), decode(md5('lease'||n.id::text), 'hex'),
                'stopped', 'busy', 'S-'||n.key, clock_timestamp(), clock_timestamp(), clock_timestamp(), 'completed'
            FROM nodes n
            WHERE n.tenant_id=$1 AND n.key LIKE 'STOP-%'`, p.TenantID, root.ID, zed)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := appPool.Exec(t.Context(), `ANALYZE nodes, principals, harness_sessions`); err != nil {
		t.Fatal(err)
	}

	page := listPage(t, p, "/api/nodes?within="+root.ID+"&kind=ticket&sort=assignee&limit=20")
	if len(page.Items) != 20 {
		t.Fatalf("page %d", len(page.Items))
	}
	for _, item := range page.Items {
		if item.Assignee != nil || item.LeadWorker != nil {
			t.Fatalf("stopped %s assignee %#v lead %#v", item.Key, item.Assignee, item.LeadWorker)
		}
	}
	loops, seq := explainAssigneeProbes(t, p, root.ID)
	// Every row was evaluated while ordering and found nobody. The page must
	// not probe them again.
	if seq > 1 || loops > tickets || loops < tickets {
		t.Fatalf("stopped probe loops %.0f, seq %.0f, want <= %d", loops, seq, tickets)
	}

	const assigned = 1500
	err = db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=jsonb_build_object('assignee', $2::text)
            WHERE tenant_id=$1 AND parent_id=$3 AND key LIKE 'STOP-%' AND position<=$4`,
			p.TenantID, sam.ID, root.ID, assigned)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := appPool.Exec(t.Context(), `ANALYZE nodes, principals, harness_sessions`); err != nil {
		t.Fatal(err)
	}
	stored := listPage(t, p, "/api/nodes?within="+root.ID+"&kind=ticket&sort=assignee&limit=50")
	if len(stored.Items) != 50 {
		t.Fatalf("stored page %d", len(stored.Items))
	}
	for _, item := range stored.Items {
		if item.Assignee == nil || item.Assignee.Name != "Sam" || item.LeadWorker != nil {
			t.Fatalf("stored stopped %s assignee %#v lead %#v", item.Key, item.Assignee, item.LeadWorker)
		}
	}
	unassigned := tickets - assigned
	const pageRows = 50 + 1
	loops, seq = explainAssigneeProbes(t, p, root.ID)
	// Ordering probes only the open rows. This page is stored people, so it
	// adds one probe per page row and no second probe of an empty result.
	if seq > 1 || loops > float64(unassigned+pageRows) || loops <= float64(unassigned) {
		t.Fatalf("stopped stored-page probes %.0f, seq %.0f, unassigned %d page %d", loops, seq, unassigned, pageRows)
	}
	var anchor string
	err = db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT id::text FROM nodes
            WHERE tenant_id=$1 AND parent_id=$2 AND key LIKE 'STOP-%' AND fields ? 'assignee'
            ORDER BY id DESC LIMIT 1`, p.TenantID, root.ID).Scan(&anchor)
	})
	if err != nil {
		t.Fatal(err)
	}
	loops, seq = explainAssigneeProbesAt(t, p, root.ID, anchor)
	// The next page is rows whose lookup already returned nobody.
	if seq > 1 || loops > float64(unassigned) || loops < float64(unassigned) {
		t.Fatalf("stopped unassigned-page probes %.0f, seq %.0f, want <= %d", loops, seq, unassigned)
	}
}

func explainAssigneeProbes(t *testing.T, p tenant.Principal, rootID string) (indexLoops, seqLoops float64) {
	t.Helper()
	return explainAssigneeProbesAt(t, p, rootID, nil)
}

func explainAssigneeProbesAt(t *testing.T, p tenant.Principal, rootID string, anchor any) (indexLoops, seqLoops float64) {
	t.Helper()
	q, err := parseListQuery(httptest.NewRequest(http.MethodGet, "/api/nodes?within="+rootID+"&kind=ticket&sort=assignee&limit=50", nil))
	if err != nil {
		t.Fatal(err)
	}
	q.seen = assigneeSeen{harnessAll: true, members: true}
	sqlText, args := listSQL(q, anchor)
	var planRaw string
	err = db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), "EXPLAIN (ANALYZE, FORMAT JSON) "+sqlText, args...).Scan(&planRaw)
	})
	if err != nil {
		t.Fatal(err)
	}
	indexLoops, seqLoops, indexes := harnessProbeStats(planRaw)
	if !slices.Contains(indexes, "harness_sessions_ticket_eta") && !slices.Contains(indexes, "harness_sessions_ticket_node") {
		t.Fatalf("assignee sort did not probe a ticket session index (index loops %.0f, seq %.0f, indexes %v)", indexLoops, seqLoops, indexes)
	}
	t.Logf("assignee sort probes: %.0f index loops, %.0f seq (indexes %v)", indexLoops, seqLoops, indexes)
	return indexLoops, seqLoops
}

func harnessProbeStats(planRaw string) (indexLoops, seqLoops float64, indexes []string) {
	var explained []map[string]any
	if json.Unmarshal([]byte(planRaw), &explained) != nil {
		return 0, 0, nil
	}
	var walk func(any)
	walk = func(node any) {
		switch n := node.(type) {
		case map[string]any:
			if n["Relation Name"] == "harness_sessions" {
				if name, ok := n["Index Name"].(string); ok {
					indexes = append(indexes, name)
					// Both indexes probe the same ticket key; 0996 also covers stopped sessions.
					if name == "harness_sessions_ticket_eta" || name == "harness_sessions_ticket_node" {
						if loops, ok := n["Actual Loops"].(float64); ok {
							indexLoops += loops
						}
					}
				}
				if n["Node Type"] == "Seq Scan" {
					if loops, ok := n["Actual Loops"].(float64); ok {
						seqLoops += loops
					}
				}
			}
			for _, child := range n {
				walk(child)
			}
		case []any:
			for _, child := range n {
				walk(child)
			}
		}
	}
	for _, rootPlan := range explained {
		walk(rootPlan)
	}
	return indexLoops, seqLoops, indexes
}

// A viewer sorts by the name the live feed would show them. harness.read
// (workspace or on the project) uses the session label, members.read uses
// the principal name, and neither uses the harness label. Hidden names must
// not decide the order.
func TestListAssigneeSortUsesTheNameTheViewerSees(t *testing.T) {
	p := newPrincipal(t, "assignee-redacted")
	project := kindBySlug(t, p, "project")
	ticketKind := kindBySlug(t, p, "ticket")
	root := mustNode(t, p, `{"kind_id":"`+project.ID+`","title":"Redacted"}`)
	makeTicket := func(key string) nodeJSON {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"kind_id": ticketKind.ID, "key": key, "title": key, "state": "new", "parent_id": root.ID})
		return mustNode(t, p, string(raw))
	}
	created := []nodeJSON{makeTicket("VIS-1"), makeTicket("VIS-2"), makeTicket("VIS-3")}
	slices.SortFunc(created, func(a, b nodeJSON) int { return strings.Compare(a.ID, b.ID) })
	least, mid, most := created[0], created[1], created[2]
	empty := makeTicket("VIS-4")

	bee := insertNamedAgent(t, p.TenantID, "Bee")
	cee := insertNamedAgent(t, p.TenantID, "Cee")
	aaa := insertNamedAgent(t, p.TenantID, "Aaa")
	// Least id: label Cee, principal Bee, Claude → guest sees "Claude agent".
	insertLiveSession(t, p.TenantID, root.ID, bee, least.ID, "claude", "worker", "Cee", "working", "busy", "", 30)
	// Middle: label Aaa, principal Cee, Grok → guest sees "Grok agent".
	insertLiveSession(t, p.TenantID, root.ID, cee, mid.ID, "grok", "worker", "Aaa", "working", "busy", "", 30)
	// Greatest id: label Bee, principal Aaa, Codex → guest sees "Codex agent".
	insertLiveSession(t, p.TenantID, root.ID, aaa, most.ID, "codex", "worker", "Bee", "working", "busy", "", 30)

	guest := insertPerson(t, p.TenantID, "Guest")
	bindProjectRole(t, p.TenantID, guest.ID, "guest", root.ID)
	viewer := insertPerson(t, p.TenantID, "Viewer")
	bindProjectRole(t, p.TenantID, viewer.ID, "viewer", root.ID)
	names := insertPerson(t, p.TenantID, "Names")
	bindNamesOnly(t, p.TenantID, names.ID)

	keys := func(who tenant.Principal, sort string) []string {
		t.Helper()
		status, body := call(t, &who, http.MethodGet, "/api/nodes?within="+root.ID+"&kind=ticket&sort="+sort, "")
		page := decode[nodePage](t, status, body, http.StatusOK)
		out := make([]string, 0, len(page.Items))
		for _, item := range page.Items {
			out = append(out, item.Key)
		}
		return out
	}
	// Labels: Aaa, Bee, Cee. Principal names: Aaa, Bee, Cee on the other
	// tickets. Harness labels: Claude, Codex, Grok.
	labelAsc := []string{mid.Key, most.Key, least.Key, empty.Key}
	labelDesc := []string{least.Key, most.Key, mid.Key, empty.Key}
	nameAsc := []string{most.Key, least.Key, mid.Key, empty.Key}
	nameDesc := []string{mid.Key, least.Key, most.Key, empty.Key}
	genericAsc := []string{least.Key, most.Key, mid.Key, empty.Key}
	genericDesc := []string{mid.Key, most.Key, least.Key, empty.Key}
	check := func(name string, who tenant.Principal, asc, desc []string) {
		t.Helper()
		if got := keys(who, "assignee"); strings.Join(got, ",") != strings.Join(asc, ",") {
			t.Fatalf("%s asc: got %v want %v", name, got, asc)
		}
		if got := keys(who, "-assignee"); strings.Join(got, ",") != strings.Join(desc, ",") {
			t.Fatalf("%s desc: got %v want %v", name, got, desc)
		}
	}
	check("admin", p, labelAsc, labelDesc)
	check("project viewer", viewer, labelAsc, labelDesc)
	check("members.read", names, nameAsc, nameDesc)
	check("guest", guest, genericAsc, genericDesc)
}

// Waiting outranks an older working session, and a problem outranks an older
// wait. The chip's lead (ticketWorkers / byLead) is the sort name.
func TestListAssigneeSortLeadsWithTheAttentionState(t *testing.T) {
	p := newPrincipal(t, "assignee-lead")
	project := kindBySlug(t, p, "project")
	ticketKind := kindBySlug(t, p, "ticket")
	root := mustNode(t, p, `{"kind_id":"`+project.ID+`","title":"Leads"}`)
	makeTicket := func(key string) nodeJSON {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"kind_id": ticketKind.ID, "key": key, "title": key, "state": "new", "parent_id": root.ID})
		return mustNode(t, p, string(raw))
	}
	old := makeTicket("LEAD-1")
	mix := makeTicket("LEAD-2")
	bad := makeTicket("LEAD-3")
	ada := insertNamedAgent(t, p.TenantID, "Ada")
	insertLiveSession(t, p.TenantID, root.ID, ada, old.ID, "claude", "worker", "Mia", "working", "busy", "", 180)
	// Older working Aaa would lead if start time won. Later yielded Zed is waiting.
	insertLiveSession(t, p.TenantID, root.ID, ada, mix.ID, "claude", "worker", "Aaa", "working", "busy", "", 120)
	insertLiveSession(t, p.TenantID, root.ID, ada, mix.ID, "claude", "worker", "Zed", "yielded", "idle", "", 60)
	// Older wait Yyy would lead if only yielded beat working. Bea has a problem.
	insertLiveSession(t, p.TenantID, root.ID, ada, bad.ID, "claude", "worker", "Yyy", "yielded", "idle", "", 90)
	insertLiveSession(t, p.TenantID, root.ID, ada, bad.ID, "claude", "worker", "Bea", "working", "busy", "error: failed", 20)

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
	// Bea, Mia, Zed.
	if got := keys("assignee"); strings.Join(got, ",") != "LEAD-3,LEAD-1,LEAD-2" {
		t.Fatalf("asc %v", got)
	}
	if got := keys("-assignee"); strings.Join(got, ",") != "LEAD-2,LEAD-1,LEAD-3" {
		t.Fatalf("desc %v", got)
	}
}

func TestLeadMinutesFollowTheWebRules(t *testing.T) {
	if y, r := leadMinutesFromJSON(nil); y != 3 || r != 10 {
		t.Fatalf("empty %d %d", y, r)
	}
	if y, r := leadMinutesFromJSON([]byte(`{"yellowMinutes":3,"redMinutes":20}`)); y != 3 || r != 20 {
		t.Fatalf("3/20 %d %d", y, r)
	}
	if y, r := leadMinutesFromJSON([]byte(`{"yellowMinutes":20}`)); y != 20 || r != 21 {
		t.Fatalf("missing red %d %d", y, r)
	}
	if y, r := leadMinutesFromJSON([]byte(`{"yellowMinutes":3,"redMinutes":2}`)); y != 3 || r != 4 {
		t.Fatalf("red below yellow %d %d", y, r)
	}
	if y, r := leadMinutesFromJSON([]byte(`{"yellowMinutes":"nope","redMinutes":null}`)); y != 3 || r != 10 {
		t.Fatalf("garbage %d %d", y, r)
	}
}

// A viewer whose red threshold is 20 minutes still treats a 12-minute-old
// working heartbeat as awaiting, so a waiting session leads. The default
// 10-minute threshold makes that same heartbeat unresponsive and it leads.
func TestListLeadUsesTheViewerHeartbeatThresholds(t *testing.T) {
	p := newPrincipal(t, "assignee-threshold")
	project := kindBySlug(t, p, "project")
	ticketKind := kindBySlug(t, p, "ticket")
	root := mustNode(t, p, `{"kind_id":"`+project.ID+`","title":"Thresholds"}`)
	makeTicket := func(key string) nodeJSON {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"kind_id": ticketKind.ID, "key": key, "title": key, "state": "new", "parent_id": root.ID})
		return mustNode(t, p, string(raw))
	}
	mix := makeTicket("THR-1")
	only := makeTicket("THR-2")
	ada := insertNamedAgent(t, p.TenantID, "Ada")
	stamp := func(node, label, phase, activity string, created, heartbeat time.Duration) {
		t.Helper()
		insertLiveSessionStamp(t, p.TenantID, root.ID, ada, node, "claude", "worker", label, phase, activity, time.Now().UTC().Add(-created), time.Now().UTC().Add(-heartbeat))
	}
	stamp(mix.ID, "Ada", "working", "busy", 30*time.Minute, 12*time.Minute)
	stamp(mix.ID, "Zed", "yielded", "idle", time.Minute, time.Second)
	stamp(only.ID, "Mia", "working", "busy", 2*time.Minute, time.Second)

	keys := func(sort string) []string {
		t.Helper()
		return listKeys(t, p, "/api/nodes?within="+root.ID+"&kind=ticket&sort="+sort)
	}
	lead := func() string {
		t.Helper()
		for _, item := range listPage(t, p, "/api/nodes?within="+root.ID+"&kind=ticket&sort=updated_at").Items {
			if item.Key == mix.Key && item.LeadWorker != nil {
				return item.LeadWorker.Name
			}
		}
		t.Fatal("missing lead")
		return ""
	}
	if got := keys("assignee"); strings.Join(got, ",") != "THR-1,THR-2" || lead() != "Ada" {
		t.Fatalf("default 3/10: order %v lead %s", keys("assignee"), lead())
	}
	setAgentState(t, p, 3, 20)
	if got := keys("assignee"); strings.Join(got, ",") != "THR-2,THR-1" {
		t.Fatalf("3/20 order %v", got)
	}
	if got := keys("-assignee"); strings.Join(got, ",") != "THR-1,THR-2" {
		t.Fatalf("3/20 desc %v", got)
	}
	if lead() != "Zed" {
		t.Fatalf("3/20 lead %s", lead())
	}
	sorted := listPage(t, p, "/api/nodes?within="+root.ID+"&kind=ticket&sort=assignee")
	for _, item := range sorted.Items {
		if item.Key == mix.Key && (item.LeadWorker == nil || item.LeadWorker.Name != "Zed") {
			t.Fatalf("sorted lead %#v", item.LeadWorker)
		}
	}
}

// A worker that reported 100% and went quiet is finished, not lost (AEON-437):
// past the red threshold it would rank unresponsive and lead, but it now ranks
// after live work. Under 100% the same silence still leads.
func TestListLeadRanksAFinishedQuietWorkerLast(t *testing.T) {
	p := newPrincipal(t, "assignee-finished")
	project := kindBySlug(t, p, "project")
	ticketKind := kindBySlug(t, p, "ticket")
	root := mustNode(t, p, `{"kind_id":"`+project.ID+`","title":"Finished"}`)
	makeTicket := func(key string) nodeJSON {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"kind_id": ticketKind.ID, "key": key, "title": key, "state": "new", "parent_id": root.ID})
		return mustNode(t, p, string(raw))
	}
	ada := insertNamedAgent(t, p.TenantID, "Ada")
	stamp := func(node, label string, progress int, created, heartbeat time.Duration) {
		t.Helper()
		id := insertLiveSessionStamp(t, p.TenantID, root.ID, ada, node, "claude", "worker", label, "working", "busy", time.Now().UTC().Add(-created), time.Now().UTC().Add(-heartbeat))
		if _, err := adminPool.Exec(t.Context(), `UPDATE harness_sessions SET progress_pct=$2 WHERE id=$1::uuid`, id, progress); err != nil {
			t.Fatal(err)
		}
	}
	done, quiet := makeTicket("FIN-1"), makeTicket("FIN-2")
	// Older and silent for 12 minutes at 100%, beside a fresh worker.
	stamp(done.ID, "Done", 100, 40*time.Minute, 12*time.Minute)
	stamp(done.ID, "Live", 30, time.Minute, time.Second)
	// The same silence under 100% is a lost worker and keeps leading.
	stamp(quiet.ID, "Lost", 99, 40*time.Minute, 12*time.Minute)
	stamp(quiet.ID, "Fresh", 30, time.Minute, time.Second)
	want := map[string]string{done.Key: "Live", quiet.Key: "Lost"}
	for _, item := range listPage(t, p, "/api/nodes?within="+root.ID+"&kind=ticket&sort=updated_at").Items {
		if item.LeadWorker == nil || item.LeadWorker.Name != want[item.Key] {
			t.Fatalf("%s lead %#v, want %s", item.Key, item.LeadWorker, want[item.Key])
		}
	}
}

// Equal start and heartbeat, session ids withheld: the lead name on the row
// is the name the sort uses. Swapping hidden labels and principal names does
// not change a guest's order or key.
func TestListLeadTieBreakIgnoresWithheldSessionIDs(t *testing.T) {
	p := newPrincipal(t, "assignee-tie")
	project := kindBySlug(t, p, "project")
	ticketKind := kindBySlug(t, p, "ticket")
	root := mustNode(t, p, `{"kind_id":"`+project.ID+`","title":"Ties"}`)
	makeTicket := func(key, assignee string) nodeJSON {
		t.Helper()
		fields := map[string]any{}
		if assignee != "" {
			fields["assignee"] = assignee
		}
		raw, _ := json.Marshal(map[string]any{"kind_id": ticketKind.ID, "key": key, "title": key, "state": "new", "parent_id": root.ID, "fields": fields})
		return mustNode(t, p, string(raw))
	}
	bee := addPrincipalIn(t, p.TenantID, "Bee")
	frank := addPrincipalIn(t, p.TenantID, "Frank")
	makeTicket("TIE-1", bee.ID)
	tie := makeTicket("TIE-2", "")
	makeTicket("TIE-3", frank.ID)
	makeTicket("TIE-4", "")
	hiddenC := insertNamedAgent(t, p.TenantID, "HiddenC")
	hiddenG := insertNamedAgent(t, p.TenantID, "HiddenG")
	at := time.Now().UTC().Truncate(time.Microsecond)
	claudeID := insertLiveSessionStamp(t, p.TenantID, root.ID, hiddenC, tie.ID, "claude", "worker", "Zed", "working", "busy", at, at)
	grokID := insertLiveSessionStamp(t, p.TenantID, root.ID, hiddenG, tie.ID, "grok", "worker", "Aaa", "working", "busy", at, at)

	guest := insertPerson(t, p.TenantID, "Guest")
	bindProjectRole(t, p.TenantID, guest.ID, "guest", root.ID)

	path := "/api/nodes?within=" + root.ID + "&kind=ticket&sort="
	adminAsc := listKeys(t, p, path+"assignee")
	if strings.Join(adminAsc, ",") != "TIE-1,TIE-3,TIE-2,TIE-4" {
		t.Fatalf("admin asc %v", adminAsc)
	}
	guestAsc := listKeys(t, guest, path+"assignee")
	if strings.Join(guestAsc, ",") != "TIE-1,TIE-2,TIE-3,TIE-4" {
		t.Fatalf("guest asc %v", guestAsc)
	}
	guestPage := listPage(t, guest, path+"assignee")
	var guestLead *leadWorker
	for _, item := range guestPage.Items {
		if item.Key != tie.Key {
			continue
		}
		guestLead = item.LeadWorker
	}
	if guestLead == nil || guestLead.Name != "Claude agent" {
		t.Fatalf("guest lead %#v", guestLead)
	}
	if strings.Contains(guestLead.Key, claudeID) || strings.Contains(guestLead.Key, grokID) {
		t.Fatalf("guest key leaked a session id: %s", guestLead.Key)
	}
	snap := sessionSnap(t, p.TenantID, claudeID)
	wantKey := strings.Join([]string{"v", snap.harness, snap.created.UTC().Format(time.RFC3339Nano)}, "\x01")
	if guestLead.Key != wantKey {
		t.Fatalf("guest key %q want %q", guestLead.Key, wantKey)
	}
	adminLead := leadOn(t, p, path+"updated_at", tie.Key)
	if adminLead == nil || adminLead.Name != "Zed" || adminLead.Key != "s:"+claudeID {
		t.Fatalf("admin lead %#v", adminLead)
	}
	if plain := leadOn(t, guest, path+"updated_at", tie.Key); plain == nil || plain.Name != guestLead.Name || plain.Key != guestLead.Key {
		t.Fatalf("unsorted guest lead %#v sorted %#v", plain, guestLead)
	}

	err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET display_label = CASE id::text WHEN $1 THEN 'Aaa' WHEN $2 THEN 'Zed' END WHERE id::text IN ($1, $2)`, claudeID, grokID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE principals SET name = CASE id::text WHEN $1 THEN 'HiddenG' WHEN $2 THEN 'HiddenC' END WHERE id::text IN ($1, $2)`, hiddenC, hiddenG)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := listKeys(t, guest, path+"assignee"); strings.Join(got, ",") != strings.Join(guestAsc, ",") {
		t.Fatalf("guest order changed after hidden rename: %v", got)
	}
	if again := leadOn(t, guest, path+"assignee", tie.Key); again == nil || again.Name != "Claude agent" || again.Key != guestLead.Key {
		t.Fatalf("guest lead changed %#v", again)
	}
	if got := listKeys(t, p, path+"assignee"); strings.Join(got, ",") != "TIE-2,TIE-1,TIE-3,TIE-4" {
		t.Fatalf("admin should now sort by Aaa: %v", got)
	}
	if again := leadOn(t, p, path+"assignee", tie.Key); again == nil || again.Name != "Aaa" {
		t.Fatalf("admin lead after rename %#v", again)
	}
}

func TestListAssigneeSortPagesStably(t *testing.T) {
	p := newPrincipal(t, "assignee-page")
	project := kindBySlug(t, p, "project")
	ticketKind := kindBySlug(t, p, "ticket")
	root := mustNode(t, p, `{"kind_id":"`+project.ID+`","title":"Pages"}`)
	makeTicket := func(key, assignee string) nodeJSON {
		t.Helper()
		fields := map[string]any{}
		if assignee != "" {
			fields["assignee"] = assignee
		}
		raw, _ := json.Marshal(map[string]any{"kind_id": ticketKind.ID, "key": key, "title": key, "state": "new", "parent_id": root.ID, "fields": fields})
		return mustNode(t, p, string(raw))
	}
	anna := addPrincipalIn(t, p.TenantID, "Anna")
	bela := addPrincipalIn(t, p.TenantID, "Bela")
	dora := addPrincipalIn(t, p.TenantID, "Dora")
	evan := addPrincipalIn(t, p.TenantID, "Evan")
	makeTicket("PG-1", anna.ID)
	makeTicket("PG-2", bela.ID)
	cara := makeTicket("PG-3", "")
	makeTicket("PG-4", dora.ID)
	makeTicket("PG-5", evan.ID)
	worker := insertNamedAgent(t, p.TenantID, "Cara")
	insertLiveSession(t, p.TenantID, root.ID, worker, cara.ID, "claude", "worker", "Cara", "working", "busy", "", 5)

	walk := func(sort string) []string {
		t.Helper()
		var out []string
		seen := map[string]bool{}
		cursor := ""
		for range 8 {
			path := "/api/nodes?within=" + root.ID + "&kind=ticket&sort=" + url.QueryEscape(sort) + "&limit=2"
			if cursor != "" {
				path += "&cursor=" + url.QueryEscape(cursor)
			}
			page := listPage(t, p, path)
			if len(page.Items) == 0 || len(page.Items) > 2 {
				t.Fatalf("page size %d", len(page.Items))
			}
			for _, item := range page.Items {
				if seen[item.Key] {
					t.Fatalf("duplicate %s", item.Key)
				}
				seen[item.Key] = true
				out = append(out, item.Key)
				if item.Key == cara.Key && (item.LeadWorker == nil || item.LeadWorker.Name != "Cara") {
					t.Fatalf("paged lead %#v", item.LeadWorker)
				}
			}
			if page.NextCursor == nil {
				return out
			}
			cursor = *page.NextCursor
		}
		t.Fatal("cursor did not end")
		return nil
	}
	wantAsc := []string{"PG-1", "PG-2", "PG-3", "PG-4", "PG-5"}
	wantDesc := []string{"PG-5", "PG-4", "PG-3", "PG-2", "PG-1"}
	if got, again := walk("assignee"), walk("assignee"); strings.Join(got, ",") != strings.Join(wantAsc, ",") || strings.Join(again, ",") != strings.Join(wantAsc, ",") {
		t.Fatalf("asc %v then %v", got, again)
	}
	if got := walk("-assignee"); strings.Join(got, ",") != strings.Join(wantDesc, ",") {
		t.Fatalf("desc %v", got)
	}
}

func listPage(t *testing.T, who tenant.Principal, path string) nodePage {
	t.Helper()
	status, body := call(t, &who, http.MethodGet, path, "")
	return decode[nodePage](t, status, body, http.StatusOK)
}

func listKeys(t *testing.T, who tenant.Principal, path string) []string {
	t.Helper()
	page := listPage(t, who, path)
	out := make([]string, 0, len(page.Items))
	for _, item := range page.Items {
		out = append(out, item.Key)
	}
	return out
}

func leadOn(t *testing.T, who tenant.Principal, path, key string) *leadWorker {
	t.Helper()
	for _, item := range listPage(t, who, path).Items {
		if item.Key == key {
			return item.LeadWorker
		}
	}
	return nil
}

func setAgentState(t *testing.T, p tenant.Principal, yellow, red int) {
	t.Helper()
	raw := fmt.Sprintf(`{"yellowMinutes":%d,"redMinutes":%d}`, yellow, red)
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO user_preferences (tenant_id, principal_id, key, value)
			VALUES ($1::uuid, $2::uuid, 'agent-state', $3::jsonb)
			ON CONFLICT (tenant_id, principal_id, key) DO UPDATE SET value = EXCLUDED.value`, p.TenantID, p.ID, raw)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

type stampedSession struct {
	id, harness, role, phase, activity, label, name string
	created                                         time.Time
	heartbeat                                       *time.Time
}

func sessionSnap(t *testing.T, tenantID, id string) stampedSession {
	t.Helper()
	var snap stampedSession
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT s.id::text, s.harness, s.role, s.phase, s.activity, coalesce(s.display_label,''), coalesce(a.name,''), s.created_at, s.heartbeat_at
			FROM harness_sessions s LEFT JOIN principals a ON a.tenant_id=s.tenant_id AND a.id=s.agent_principal_id
			WHERE s.id=$1::uuid`, id).Scan(&snap.id, &snap.harness, &snap.role, &snap.phase, &snap.activity, &snap.label, &snap.name, &snap.created, &snap.heartbeat)
	})
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func insertNamedAgent(t *testing.T, tenantID, name string) string {
	t.Helper()
	var id string
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO principals (tenant_id, kind, name) VALUES ($1, 'agent', $2) RETURNING id::text`, tenantID, name).Scan(&id)
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func insertLiveSessionStamp(t *testing.T, tenantID, projectID, principal, node, harness, role, label, phase, activity string, created, heartbeat time.Time) string {
	t.Helper()
	var id string
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO harness_sessions (
			tenant_id, project_id, agent_principal_id, ticket_node_id, harness, host, management, role, work_shape,
			ref_digest, lease_digest, phase, activity, display_label, created_at, heartbeat_at)
			VALUES ($1, $2, $3, $4, $5, 'test', 'unmanaged', $6, 'ship',
				decode(replace(gen_random_uuid()::text, '-', ''), 'hex'),
				decode(replace(gen_random_uuid()::text, '-', ''), 'hex'),
				$7, $8, nullif($9::text, ''), $10, $11)
			RETURNING id::text`,
			tenantID, projectID, principal, node, harness, role, phase, activity, label, created, heartbeat).Scan(&id)
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func insertLiveSession(t *testing.T, tenantID, projectID, principal, node, harness, role, label, phase, activity, stopReason string, minutesAgo int) {
	t.Helper()
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO harness_sessions (
			tenant_id, project_id, agent_principal_id, ticket_node_id, harness, host, management, role, work_shape,
			ref_digest, lease_digest, phase, activity, display_label, created_at, heartbeat_at, stop_reason)
			VALUES ($1, $2, $3, $4, $5, 'test', 'unmanaged', $6, 'ship',
				decode(replace(gen_random_uuid()::text, '-', ''), 'hex'),
				decode(replace(gen_random_uuid()::text, '-', ''), 'hex'),
				$7, $8, nullif($9::text, ''), clock_timestamp() - make_interval(mins => $10::int), clock_timestamp(),
				nullif($11::text, ''))`,
			tenantID, projectID, principal, node, harness, role, phase, activity, label, minutesAgo, stopReason)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func insertPerson(t *testing.T, tenantID, name string) tenant.Principal {
	t.Helper()
	person := tenant.Principal{TenantID: tenantID, Kind: tenant.Person, Name: name}
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO principals (tenant_id, kind, name) VALUES ($1, 'person', $2) RETURNING id::text`, tenantID, name).Scan(&person.ID)
	})
	if err != nil {
		t.Fatal(err)
	}
	return person
}

func bindProjectRole(t *testing.T, tenantID, principalID, roleKey, projectID string) {
	t.Helper()
	tag, err := testDB.Admin.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id)
		SELECT $1::uuid,$2::uuid,id,'project',$3::uuid FROM roles WHERE tenant_id=$1::uuid AND key=$4`,
		tenantID, principalID, projectID, roleKey)
	if err != nil {
		t.Fatal(err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("bind %s affected %d", roleKey, tag.RowsAffected())
	}
}

func bindNamesOnly(t *testing.T, tenantID, principalID string) {
	t.Helper()
	var roleID string
	if err := testDB.Admin.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES ($1::uuid,'names_only','Names only') RETURNING id::text`, tenantID).Scan(&roleID); err != nil {
		t.Fatal(err)
	}
	for _, permission := range []string{"nodes.read", "members.read"} {
		if _, err := testDB.Admin.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES ($1::uuid,$2::uuid,$3)`, tenantID, roleID, permission); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := testDB.Admin.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES ($1::uuid,$2::uuid,$3::uuid,'workspace')`, tenantID, principalID, roleID); err != nil {
		t.Fatal(err)
	}
}
