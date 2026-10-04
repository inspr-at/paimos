// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestLookupNodesIsBoundedAndTenantScoped(t *testing.T) {
	p := newPrincipal(t, "lookup")
	kind := kindBySlug(t, p, "work")
	first := mustNode(t, p, `{"kind_id":"`+kind.ID+`","title":"First"}`)
	second := mustNode(t, p, `{"kind_id":"`+kind.ID+`","title":"Second"}`)
	other := addPrincipal(t, "lookup-other")
	outsider := mustNode(t, other, `{"kind_id":"`+kindBySlug(t, other, "work").ID+`","title":"Outside"}`)
	missing := "00000000-0000-4000-8000-000000000099"
	ids := second.ID + "," + outsider.ID + "," + first.ID + "," + second.ID + "," + missing
	status, body := call(t, &p, http.MethodGet, "/api/nodes/lookup?ids="+url.QueryEscape(ids), "")
	page := decode[struct {
		Items []nodePreview `json:"items"`
	}](t, status, body, http.StatusOK)
	if len(page.Items) != 2 || page.Items[0].ID != second.ID || page.Items[1].ID != first.ID || page.Items[0].Title != "Second" {
		t.Fatalf("lookup order, dedupe or tenant scope: %+v", page.Items)
	}
	for _, suffix := range []string{"", "?ids=bad", "?ids=" + first.ID + ",", "?ids=" + first.ID + ",bad"} {
		status, _ := call(t, &p, http.MethodGet, "/api/nodes/lookup"+suffix, "")
		if status != http.StatusBadRequest {
			t.Fatalf("%q returned %d", suffix, status)
		}
	}
	many := make([]string, 101)
	for i := range many {
		many[i] = fmt.Sprintf("00000000-0000-4000-8000-%012x", i)
	}
	status, _ = call(t, &p, http.MethodGet, "/api/nodes/lookup?ids="+url.QueryEscape(strings.Join(many, ",")), "")
	if status != http.StatusBadRequest {
		t.Fatalf("101 IDs returned %d", status)
	}
}

func TestLookupNodesByKeyResolvesProjectAndEarlierKeys(t *testing.T) {
	p := newPrincipal(t, "lookup-keys")
	projectKind := kindBySlug(t, p, "project")
	ticketKind := kindBySlug(t, p, "work")
	taskKind := kindBySlug(t, p, "work")
	project := mustNode(t, p, `{"kind_id":"`+projectKind.ID+`","title":"Release notes","fields":{"project_key":"REL"}}`)
	ticket := mustNode(t, p, `{"kind_id":"`+ticketKind.ID+`","title":"Link tickets","parent_id":"`+project.ID+`","key_prefix":"REL"}`)
	task := mustNode(t, p, `{"kind_id":"`+taskKind.ID+`","title":"Nested task","parent_id":"`+ticket.ID+`","key_prefix":"REL"}`)
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO node_key_aliases(tenant_id,key,node_id) VALUES($1::uuid,'OLD-7',$2::uuid)`, p.TenantID, ticket.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	other := addPrincipal(t, "lookup-keys-other")
	outsider := mustNode(t, other, `{"kind_id":"`+kindBySlug(t, other, "work").ID+`","title":"Outside","key_prefix":"ELSE"}`)
	keys := strings.ToLower(task.Key) + ",NOPE-9," + outsider.Key + ",OLD-7," + ticket.Key + "," + task.Key
	status, body := call(t, &p, http.MethodGet, "/api/nodes/lookup?keys="+url.QueryEscape(keys), "")
	page := decode[struct {
		Items []nodePreview `json:"items"`
	}](t, status, body, http.StatusOK)
	want := []nodePreview{
		{ID: task.ID, Key: task.Key, Title: "Nested task", State: task.State, RequestedKey: task.Key, ProjectID: project.ID},
		{ID: ticket.ID, Key: ticket.Key, Title: "Link tickets", State: ticket.State, RequestedKey: "OLD-7", ProjectID: project.ID},
		{ID: ticket.ID, Key: ticket.Key, Title: "Link tickets", State: ticket.State, RequestedKey: ticket.Key, ProjectID: project.ID},
	}
	if fmt.Sprint(page.Items) != fmt.Sprint(want) {
		t.Fatalf("key lookup (case, dedupe, tenant, alias, project):\n got %+v\nwant %+v", page.Items, want)
	}
	// IDs and keys together: ID previews first, and only key previews name the request.
	status, body = call(t, &p, http.MethodGet, "/api/nodes/lookup?ids="+project.ID+"&keys="+ticket.Key, "")
	mixed := decode[struct {
		Items []nodePreview `json:"items"`
	}](t, status, body, http.StatusOK)
	if len(mixed.Items) != 2 || mixed.Items[0].ID != project.ID || mixed.Items[0].RequestedKey != "" || mixed.Items[1].RequestedKey != ticket.Key {
		t.Fatalf("mixed lookup: %+v", mixed.Items)
	}
	// Keys longer than the stored limit (30) are refused, however well formed.
	long := "REL-" + strings.Repeat("9", 27)
	for _, suffix := range []string{"?keys=", "?keys=REL", "?keys=REL-0", "?keys=" + url.QueryEscape("REL-1,x y"), "?keys=" + long} {
		status, _ := call(t, &p, http.MethodGet, "/api/nodes/lookup"+suffix, "")
		if status != http.StatusBadRequest {
			t.Fatalf("%q returned %d", suffix, status)
		}
	}
	if status, _ := call(t, &p, http.MethodGet, "/api/nodes/lookup?keys="+long[:30], ""); status != http.StatusOK {
		t.Fatalf("a 30-character key returned %d", status)
	}
	many := make([]string, 101)
	for i := range many {
		many[i] = fmt.Sprintf("REL-%d", i+1)
	}
	// 101 raw inputs are refused whether distinct, repeated, or split over parameters.
	repeated := strings.TrimSuffix(strings.Repeat(ticket.Key+",", 101), ",")
	for name, query := range map[string]string{
		"distinct keys":   "keys=" + strings.Join(many, ","),
		"duplicate keys":  "keys=" + repeated,
		"repeated params": "keys=" + strings.Join(many[:60], ",") + "&keys=" + strings.Join(many[60:], ","),
		"duplicate ids":   "ids=" + strings.TrimSuffix(strings.Repeat(project.ID+",", 101), ","),
	} {
		if status, _ := call(t, &p, http.MethodGet, "/api/nodes/lookup?"+query, ""); status != http.StatusBadRequest {
			t.Fatalf("101 %s returned %d", name, status)
		}
	}
	if status, _ := call(t, &p, http.MethodGet, "/api/nodes/lookup?keys="+strings.Join(many[:100], ","), ""); status != http.StatusOK {
		t.Fatalf("100 keys returned %d", status)
	}
}

// A caller in the same tenant who cannot see a project (a guest bound to
// another one) gets the same answer for that project's current and earlier
// keys as for keys that exist nowhere: no existence leak.
func TestLookupNodesByKeyHidesProjectsTheCallerCannotSee(t *testing.T) {
	member := newPrincipal(t, "lookup-keys-visibility")
	projectKind := kindBySlug(t, member, "project")
	ticketKind := kindBySlug(t, member, "work")
	open := mustNode(t, member, `{"kind_id":"`+projectKind.ID+`","title":"Open","fields":{"project_key":"OPN"}}`)
	closed := mustNode(t, member, `{"kind_id":"`+projectKind.ID+`","title":"Closed","fields":{"project_key":"HID"}}`)
	shown := mustNode(t, member, `{"kind_id":"`+ticketKind.ID+`","title":"Shown","parent_id":"`+open.ID+`","key_prefix":"OPN"}`)
	hidden := mustNode(t, member, `{"kind_id":"`+ticketKind.ID+`","title":"Hidden","parent_id":"`+closed.ID+`","key_prefix":"HID"}`)
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, member.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO node_key_aliases(tenant_id,key,node_id) VALUES($1::uuid,'GONE-3',$2::uuid)`, member.TenantID, hidden.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	guest := tenant.Principal{TenantID: member.TenantID, Kind: tenant.Person, Name: "Guest"}
	if err := testDB.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Guest') RETURNING id::text`, member.TenantID).Scan(&guest.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := testDB.Admin.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='guest'`, member.TenantID, guest.ID, open.ID); err != nil {
		t.Fatal(err)
	}
	lookup := func(who tenant.Principal, keys string) (int, string) {
		t.Helper()
		status, body := call(t, &who, http.MethodGet, "/api/nodes/lookup?keys="+url.QueryEscape(keys), "")
		return status, strings.TrimSpace(string(body))
	}
	// The member sees both, so the hidden key and alias really exist.
	if _, body := lookup(member, hidden.Key+",GONE-3"); !strings.Contains(body, hidden.ID) || !strings.Contains(body, `"requested_key":"GONE-3"`) {
		t.Fatalf("member lookup: %s", body)
	}
	unknownStatus, unknown := lookup(guest, "NOPE-41,NOPE-42")
	for _, keys := range []string{hidden.Key + ",GONE-3", "GONE-3," + hidden.Key} {
		if status, body := lookup(guest, keys); status != unknownStatus || body != unknown {
			t.Fatalf("hidden %s: %d %s, unknown keys: %d %s", keys, status, body, unknownStatus, unknown)
		}
	}
	if unknownStatus != http.StatusOK || unknown != `{"items":[]}` {
		t.Fatalf("unknown keys: %d %s", unknownStatus, unknown)
	}
	// Mixed with a visible key, only the visible one answers.
	status, body := call(t, &guest, http.MethodGet, "/api/nodes/lookup?keys="+url.QueryEscape(hidden.Key+","+shown.Key+",GONE-3"), "")
	page := decode[struct {
		Items []nodePreview `json:"items"`
	}](t, status, body, http.StatusOK)
	if len(page.Items) != 1 || page.Items[0].ID != shown.ID || page.Items[0].ProjectID != open.ID {
		t.Fatalf("guest mixed lookup: %+v", page.Items)
	}
}
