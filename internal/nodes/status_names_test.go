// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/tenant"
)

// AEON-1401. The risk: a rename leaks into machine output (state values the
// API, --json and transitions use) or is saved by someone who may not manage
// settings. Names are display only.
func TestStatusNamesRoundTripKeepCanonicalStates(t *testing.T) {
	p := newPrincipal(t, "status-names")
	status, body := call(t, &p, http.MethodGet, "/api/settings/status-names", "")
	if got := decode[statusNamesJSON](t, status, body, 200); got.Revision != 0 || len(got.Names) != 0 || string(body) != `{"revision":0,"names":{}}`+"\n" {
		t.Fatalf("initial %s", body)
	}
	in := `{"revision":0,"names":{"in_progress":{"en":" Building ","de":"In Umsetzung"},"done":{"en":""}}}`
	status, body = call(t, &p, http.MethodPut, "/api/settings/status-names", in)
	saved := decode[statusNamesJSON](t, status, body, 200)
	if saved.Revision != 1 || len(saved.Names) != 1 || saved.Names["in_progress"].EN != "Building" || saved.Names["in_progress"].DE != "In Umsetzung" {
		t.Fatalf("saved %s", body)
	}
	var events int
	if err := adminPool.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE tenant_id=$1::uuid AND type=$2 AND after->'names'->'in_progress'->>'en'='Building'`, p.TenantID, StatusNamesEvent).Scan(&events); err != nil || events != 1 {
		t.Fatalf("event %d %v", events, err)
	}
	if status, body = call(t, &p, http.MethodPut, "/api/settings/status-names", in); status != 409 || !strings.Contains(string(body), "reload") {
		t.Fatalf("stale %d %s", status, body)
	}
	for _, tc := range []struct{ names, code, field string }{
		{`{"in_progress":{"en":"Done"}}`, "status_name_taken", "names.in_progress.en"},
		{`{"in_progress":{"en":"Building"},"qa":{"en":"building"}}`, "status_name_taken", "names.qa.en"},
		{`{"in_progress":{"en":"` + strings.Repeat("x", 25) + `"}}`, "status_name_too_long", "names.in_progress.en"},
		{`{"recurring":{"en":"Again"}}`, "status_name_unknown", "names.recurring"},
	} {
		status, body = call(t, &p, http.MethodPut, "/api/settings/status-names", `{"revision":1,"names":`+tc.names+`}`)
		got := decode[errorBody](t, status, body, 400)
		if got.Code != tc.code || len(got.Fields) != 1 || got.Fields[0] != tc.field {
			t.Fatalf("%s: %s", tc.names, body)
		}
	}
	for _, bad := range []string{`{"names":{}}`, `{"revision":1}`, `{"revision":1,"names":{"qa":{"fr":"Contrôle"}}}`, `{"revision":1,"names":{},"extra":1}`, `{"revision":1,"names":{}} {}`, strings.Repeat("x", 8193)} {
		if status, body = call(t, &p, http.MethodPut, "/api/settings/status-names", bad); status != 400 {
			t.Fatalf("accepted %q: %d %s", bad, status, body)
		}
	}
	agent := tenant.Principal{ID: insertNamedAgent(t, p.TenantID, "status-names-agent"), TenantID: p.TenantID, Kind: tenant.Agent}
	if status, _ = call(t, &agent, http.MethodPut, "/api/settings/status-names", `{"revision":1,"names":{}}`); status != 403 {
		t.Fatalf("agent write %d", status)
	}
	// Agents read the names for the CLI's human output.
	status, body = call(t, &agent, http.MethodGet, "/api/settings/status-names", "")
	if got := decode[statusNamesJSON](t, status, body, 200); got.Names["in_progress"].EN != "Building" {
		t.Fatalf("agent read %s", body)
	}
	reader := insertPerson(t, p.TenantID, "Reader")
	if status, body = call(t, &reader, http.MethodPut, "/api/settings/status-names", `{"revision":1,"names":{}}`); status != 403 || !strings.Contains(string(body), "permission denied") {
		t.Fatalf("unprivileged write %d %s", status, body)
	}
	other := addPrincipal(t, "other-status-names")
	status, body = call(t, &other, http.MethodGet, "/api/settings/status-names", "")
	if got := decode[statusNamesJSON](t, status, body, 200); got.Revision != 0 || len(got.Names) != 0 {
		t.Fatalf("tenant names leaked: %s", body)
	}

	// With the rename active, writes and reads keep the canonical state.
	project := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Names project"}`, kindBySlug(t, p, "project").ID))
	ticket := workNodeTest(t, p, project.ID, "open")
	status, body = call(t, &p, http.MethodPatch, "/api/nodes/"+ticket.ID, `{"state":"in_progress"}`)
	if got := decode[nodeJSON](t, status, body, 200); got.State != "in_progress" || strings.Contains(string(body), "Building") {
		t.Fatalf("write %s", body)
	}
	status, body = call(t, &p, http.MethodGet, "/api/nodes/"+ticket.ID, "")
	if got := decode[nodeJSON](t, status, body, 200); got.State != "in_progress" || strings.Contains(string(body), "Building") {
		t.Fatalf("read %s", body)
	}
	if status, body = call(t, &p, http.MethodGet, "/api/nodes?within="+project.ID+"&state=in_progress", ""); status != 200 || !strings.Contains(string(body), ticket.ID) || strings.Contains(string(body), "Building") {
		t.Fatalf("list %d %s", status, body)
	}
	if status, body = call(t, &p, http.MethodGet, "/api/status/help", ""); status != 200 || strings.Contains(string(body), "Building") {
		t.Fatalf("status help %d %s", status, body)
	}

	// Clearing every name returns to the built-ins.
	status, body = call(t, &p, http.MethodPut, "/api/settings/status-names", `{"revision":1,"names":{"in_progress":{"en":"  ","de":""}}}`)
	if got := decode[statusNamesJSON](t, status, body, 200); got.Revision != 2 || len(got.Names) != 0 {
		t.Fatalf("cleared %s", body)
	}
}
