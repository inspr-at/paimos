// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/tenant"
)

func leadRegressionTicket(t *testing.T, p tenant.Principal, root, kind, key string) nodeJSON {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"kind_id": kind, "key": key, "title": key, "state": "new", "parent_id": root})
	return mustNode(t, p, string(raw))
}

// Commit a stop on another connection immediately after the ordered list
// query returns. A second READ COMMITTED lead lookup would see Zed, even
// though the response was already ordered using Ada.
type stopAfterListQuery struct {
	hook         func()
	pending, ran bool
}

func (x *stopAfterListQuery) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	x.pending = strings.Contains(d.SQL, "child_counts AS MATERIALIZED")
	return ctx
}
func (x *stopAfterListQuery) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {
	if x.pending && !x.ran {
		x.ran = true
		x.hook()
	}
	x.pending = false
}
func TestListLeadKeepsTheSortSnapshot(t *testing.T) {
	p := newPrincipal(t, "lead-snapshot")
	project := kindBySlug(t, p, "project")
	kind := kindBySlug(t, p, "work")
	root := mustNode(t, p, `{"kind_id":"`+project.ID+`","title":"Snapshot"}`)
	mix := leadRegressionTicket(t, p, root.ID, kind.ID, "REV-1")
	only := leadRegressionTicket(t, p, root.ID, kind.ID, "REV-2")
	a := insertNamedAgent(t, p.TenantID, "Ada")
	at := time.Now().UTC()
	ada := insertLiveSessionStamp(t, p.TenantID, root.ID, a, mix.ID, "claude", "worker", "Ada", "working", "busy", at.Add(-30*time.Minute), at)
	insertLiveSessionStamp(t, p.TenantID, root.ID, a, mix.ID, "claude", "worker", "Zed", "working", "busy", at.Add(-time.Minute), at)
	insertLiveSessionStamp(t, p.TenantID, root.ID, a, only.ID, "claude", "worker", "Mia", "working", "busy", at.Add(-time.Minute), at)
	var hookErr error
	trace := &stopAfterListQuery{hook: func() {
		_, hookErr = adminPool.Exec(t.Context(), `UPDATE harness_sessions SET phase='stopped',stopped_at=clock_timestamp(),stop_reason='completed' WHERE id=$1::uuid`, ada)
	}}
	cfg := appPool.Config()
	cfg.ConnConfig.Tracer = trace
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	status, body := callAs(t, New(pool, nil), &p, http.MethodGet, "/api/nodes?within="+root.ID+"&kind=work&sort=assignee", "")
	page := decode[nodePage](t, status, body, http.StatusOK)
	if hookErr != nil {
		t.Fatal(hookErr)
	}
	if !trace.ran {
		t.Fatal("query hook did not fire")
	}
	var shown []string
	for _, item := range page.Items {
		if item.LeadWorker == nil {
			t.Fatal("missing lead")
		}
		shown = append(shown, item.LeadWorker.Name)
	}
	t.Logf("same HTTP response order: %v", shown)
	if strings.Join(shown, ",") != "Ada,Mia" {
		t.Errorf("sort/display snapshot diverged: %v", shown)
	}
	// A fresh request must see the committed stop, so this is not a stale fixture.
	next := listPage(t, p, "/api/nodes?within="+root.ID+"&kind=work&sort=assignee")
	var after []string
	for _, item := range next.Items {
		after = append(after, item.LeadWorker.Name)
	}
	if strings.Join(after, ",") != "Mia,Zed" {
		t.Errorf("new snapshot ignored stop: %v", after)
	}
}

// A stored person is the sort key, so that row's live lead is read on the
// page. It still belongs to the list statement: stopping the lead as the
// statement ends must not reveal the other worker.
func TestListAssignedLeadUsesTheSameStatement(t *testing.T) {
	p := newPrincipal(t, "lead-assigned-snapshot")
	project := kindBySlug(t, p, "project")
	kind := kindBySlug(t, p, "work")
	root := mustNode(t, p, `{"kind_id":"`+project.ID+`","title":"Assigned snapshot"}`)
	nia := addPrincipalIn(t, p.TenantID, "Nia")
	raw, _ := json.Marshal(map[string]any{"kind_id": kind.ID, "key": "REV-3", "title": "Assigned", "state": "new", "parent_id": root.ID, "fields": map[string]any{"assignee": nia.ID}})
	assigned := mustNode(t, p, string(raw))
	only := leadRegressionTicket(t, p, root.ID, kind.ID, "REV-4")
	a := insertNamedAgent(t, p.TenantID, "Ada")
	at := time.Now().UTC()
	ada := insertLiveSessionStamp(t, p.TenantID, root.ID, a, assigned.ID, "claude", "worker", "Ada", "working", "busy", at.Add(-30*time.Minute), at)
	insertLiveSessionStamp(t, p.TenantID, root.ID, a, assigned.ID, "claude", "worker", "Zed", "working", "busy", at.Add(-time.Minute), at)
	insertLiveSessionStamp(t, p.TenantID, root.ID, a, only.ID, "claude", "worker", "Mia", "working", "busy", at.Add(-time.Minute), at)
	var hookErr error
	trace := &stopAfterListQuery{hook: func() {
		_, hookErr = adminPool.Exec(t.Context(), `UPDATE harness_sessions SET phase='stopped',stopped_at=clock_timestamp(),stop_reason='completed' WHERE id=$1::uuid`, ada)
	}}
	cfg := appPool.Config()
	cfg.ConnConfig.Tracer = trace
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	status, body := callAs(t, New(pool, nil), &p, http.MethodGet, "/api/nodes?within="+root.ID+"&kind=work&sort=assignee", "")
	page := decode[nodePage](t, status, body, http.StatusOK)
	if hookErr != nil {
		t.Fatal(hookErr)
	}
	if !trace.ran {
		t.Fatal("query hook did not fire")
	}
	var shown []string
	for _, item := range page.Items {
		if item.LeadWorker == nil {
			t.Fatal("missing lead")
		}
		shown = append(shown, item.LeadWorker.Name)
	}
	// Mia is the unassigned lead and the sort key. Nia sorts after, and her
	// row still shows Ada from this statement, not Zed.
	if strings.Join(shown, ",") != "Mia,Ada" {
		t.Errorf("assigned lead left the list statement: %v", shown)
	}
	next := listPage(t, p, "/api/nodes?within="+root.ID+"&kind=work&sort=assignee")
	var after []string
	for _, item := range next.Items {
		after = append(after, item.LeadWorker.Name)
	}
	if strings.Join(after, ",") != "Mia,Zed" {
		t.Errorf("new snapshot ignored stop: %v", after)
	}
}
func TestListLeadExactPublicTiesIgnoreHiddenRenames(t *testing.T) {
	p := newPrincipal(t, "lead-exact-ties")
	project := kindBySlug(t, p, "project")
	kind := kindBySlug(t, p, "work")
	root := mustNode(t, p, `{"kind_id":"`+project.ID+`","title":"Exact ties"}`)
	ada := insertNamedAgent(t, p.TenantID, "Ada")
	zed := insertNamedAgent(t, p.TenantID, "Zed")
	viewer := insertPerson(t, p.TenantID, "Names only")
	bindNamesOnly(t, p.TenantID, viewer.ID)
	at := time.Now().UTC().Truncate(time.Microsecond)
	for n := 0; n < 30; n++ {
		ticket := leadRegressionTicket(t, p, root.ID, kind.ID, fmt.Sprintf("TIE-%d", n+1))
		insertLiveSessionStamp(t, p.TenantID, root.ID, ada, ticket.ID, "claude", "worker", "HiddenAda", "working", "busy", at, at)
		insertLiveSessionStamp(t, p.TenantID, root.ID, zed, ticket.ID, "claude", "worker", "HiddenZed", "working", "busy", at, at)
	}
	path := "/api/nodes?within=" + root.ID + "&kind=work&sort=assignee&limit=50"
	page := listPage(t, viewer, path)
	var shown []string
	bad := 0
	for i, item := range page.Items {
		if item.LeadWorker == nil {
			t.Fatal("missing lead")
		}
		if item.LeadWorker.Name != "Ada" {
			t.Errorf("projected name must break exact public ties: %s", item.LeadWorker.Name)
		}
		if strings.Contains(item.LeadWorker.Key, "Hidden") {
			t.Fatal("hidden label leaked")
		}
		shown = append(shown, item.LeadWorker.Name)
		if i > 0 && strings.Compare(shown[i-1], shown[i]) > 0 {
			bad++
		}
	}
	t.Logf("same timestamps/harness/role/phase/activity/sequence, ids withheld: %v", shown)
	if bad > 0 {
		t.Errorf("sort/display inverted at %d boundaries", bad)
	}
	// Rename only withheld session labels; the visible projection should remain identical.
	before := map[string]string{}
	for _, item := range page.Items {
		before[item.ID] = item.LeadWorker.Name
	}
	cursor := ""
	pagingChanges := 0
	seenPages := map[string]bool{}
	for n := 0; n < 35; n++ {
		paged := listPage(t, viewer, strings.Replace(path, "limit=50", "limit=1", 1)+"&cursor="+url.QueryEscape(cursor))
		for _, item := range paged.Items {
			if before[item.ID] != item.LeadWorker.Name {
				pagingChanges++
			}
			if seenPages[item.ID] {
				t.Error("duplicate page row")
			}
			seenPages[item.ID] = true
		}
		if paged.NextCursor == nil {
			break
		}
		cursor = *paged.NextCursor
	}
	if pagingChanges > 0 {
		t.Errorf("same static data, limit=1 vs limit=50 changed lead on %d tickets", pagingChanges)
	}
	if len(seenPages) != len(page.Items) {
		t.Errorf("paging saw %d rows, want %d", len(seenPages), len(page.Items))
	}
	_, err := adminPool.Exec(t.Context(), `UPDATE harness_sessions SET display_label=display_label||' renamed' WHERE tenant_id=$1::uuid AND agent_principal_id=$2::uuid`, p.TenantID, ada)
	if err != nil {
		t.Fatal(err)
	}
	again := listPage(t, viewer, path)
	changed := 0
	afterNames := []string{}
	for _, item := range again.Items {
		afterNames = append(afterNames, item.LeadWorker.Name)
		if before[item.ID] != item.LeadWorker.Name {
			changed++
		}
	}
	t.Logf("after hidden labels renamed: %v", afterNames)
	for i := 1; i < len(afterNames); i++ {
		if strings.Compare(afterNames[i-1], afterNames[i]) > 0 {
			t.Errorf("same response inverted after hidden rename at %d: %s before %s", i, afterNames[i-1], afterNames[i])
			break
		}
	}
	if changed > 0 {
		t.Errorf("hidden-only label update changed visible lead on %d tickets", changed)
	}
}

func TestListLeadPermissionProjection(t *testing.T) {
	p := newPrincipal(t, "lead-projection")
	project := kindBySlug(t, p, "project")
	kind := kindBySlug(t, p, "work")
	root := mustNode(t, p, `{"kind_id":"`+project.ID+`","title":"Projection"}`)
	ticket := leadRegressionTicket(t, p, root.ID, kind.ID, "PROJ-1")
	principal := insertNamedAgent(t, p.TenantID, "WithheldPrincipal")
	at := time.Now().UTC()
	session := insertLiveSessionStamp(t, p.TenantID, root.ID, principal, ticket.ID, "grok", "worker", "WithheldLabel", "working", "busy", at, at)
	guest := insertPerson(t, p.TenantID, "Guest")
	bindProjectRole(t, p.TenantID, guest.ID, "guest", root.ID)
	viewer := insertPerson(t, p.TenantID, "Viewer")
	bindProjectRole(t, p.TenantID, viewer.ID, "viewer", root.ID)
	names := insertPerson(t, p.TenantID, "Names")
	bindNamesOnly(t, p.TenantID, names.ID)
	for _, tc := range []struct {
		label                         string
		p                             tenant.Principal
		want                          string
		id, labelVisible, nameVisible bool
	}{
		{"workspace", p, "WithheldLabel", true, true, true},
		{"project", viewer, "WithheldLabel", false, true, true},
		{"names", names, "WithheldPrincipal", false, false, true},
		{"guest", guest, "Grok agent", false, false, false},
	} {
		lead := leadOn(t, tc.p, "/api/nodes?within="+root.ID+"&kind=work&sort=assignee", ticket.Key)
		if lead == nil {
			t.Fatalf("%s missing lead", tc.label)
		}
		raw, _ := json.Marshal(lead)
		if lead.Name != tc.want {
			t.Errorf("%s wrong projected name", tc.label)
		}
		if !tc.id && strings.Contains(string(raw), session) {
			t.Errorf("%s leaked session id", tc.label)
		}
		if !tc.labelVisible && strings.Contains(string(raw), "WithheldLabel") {
			t.Errorf("%s leaked label", tc.label)
		}
		if !tc.nameVisible && strings.Contains(string(raw), "WithheldPrincipal") {
			t.Errorf("%s leaked principal name", tc.label)
		}
		if strings.Contains(string(raw), principal) {
			t.Errorf("%s exposed principal uuid", tc.label)
		}
		t.Logf("%s projected name and key checked", tc.label)
	}
}

func TestListLeadRestrictedKeyStaysStable(t *testing.T) {
	p := newPrincipal(t, "lead-stable-key")
	project := kindBySlug(t, p, "project")
	kind := kindBySlug(t, p, "work")
	root := mustNode(t, p, `{"kind_id":"`+project.ID+`","title":"Stable keys"}`)
	principal := insertNamedAgent(t, p.TenantID, "Before")
	viewer := insertPerson(t, p.TenantID, "Project viewer")
	bindProjectRole(t, p.TenantID, viewer.ID, "viewer", root.ID)
	at := time.Now().UTC().Truncate(time.Second)
	// Whole seconds, trailing fractional zeros and all six Postgres digits must
	// match the live feed's RFC3339Nano serialization exactly.
	for i, fraction := range []time.Duration{0, 120 * time.Millisecond, 123456 * time.Microsecond} {
		ticket := leadRegressionTicket(t, p, root.ID, kind.ID, fmt.Sprintf("KEY-%d", i+1))
		created := at.Add(fraction)
		id := insertLiveSessionStamp(t, p.TenantID, root.ID, principal, ticket.ID, "grok", "worker", "Before", "working", "busy", created, at)
		path := "/api/nodes?within=" + root.ID + "&kind=work&sort=assignee"
		before := leadOn(t, viewer, path, ticket.Key)
		want := "v\x01grok\x01" + created.Format(time.RFC3339Nano)
		if before == nil || before.Key != want {
			t.Fatalf("timestamp key: %#v, want %q", before, want)
		}
		_, err := adminPool.Exec(t.Context(), `UPDATE harness_sessions SET heartbeat_at=clock_timestamp(),
			role='coordinator', phase='yielded', activity='idle', activity_sequence=activity_sequence+1,
			display_label='After' WHERE id=$1::uuid`, id)
		if err != nil {
			t.Fatal(err)
		}
		after := leadOn(t, viewer, path, ticket.Key)
		if after == nil || after.Key != before.Key || after.Name != "After" {
			t.Fatalf("telemetry or rename changed the key: before %#v, after %#v", before, after)
		}
	}
}

func TestListLeadExactNamesUseVisibleSessionKey(t *testing.T) {
	p := newPrincipal(t, "lead-visible-key")
	project := kindBySlug(t, p, "project")
	kind := kindBySlug(t, p, "work")
	root := mustNode(t, p, `{"kind_id":"`+project.ID+`","title":"Visible keys"}`)
	ticket := leadRegressionTicket(t, p, root.ID, kind.ID, "KEY-1")
	principal := insertNamedAgent(t, p.TenantID, "Ada")
	at := time.Now().UTC().Truncate(time.Microsecond)
	a := insertLiveSessionStamp(t, p.TenantID, root.ID, principal, ticket.ID, "claude", "worker", "Ada", "working", "busy", at, at)
	b := insertLiveSessionStamp(t, p.TenantID, root.ID, principal, ticket.ID, "claude", "worker", "Ada", "working", "busy", at, at)
	for _, sort := range []string{"assignee", "-assignee", "updated_at"} {
		lead := leadOn(t, p, "/api/nodes?within="+root.ID+"&kind=work&sort="+sort, ticket.Key)
		if lead == nil || lead.Name != "Ada" || lead.Key != "s:"+min(a, b) {
			t.Fatalf("%s: visible key did not break equal-name tie: %#v", sort, lead)
		}
	}
}
