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
	if seqLoops > 1 || !slices.Contains(indexes, "harness_sessions_ticket_eta") {
		t.Fatalf("assignee sort did not probe harness_sessions_ticket_eta (seq loops %.0f, indexes %v)", seqLoops, indexes)
	}
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
