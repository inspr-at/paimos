// SPDX-License-Identifier: AGPL-3.0-only
package decisiondesk

import (
	"encoding/json"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/inspr-at/paimos/internal/config"
	"github.com/inspr-at/paimos/internal/tenant"
)

// AEON-1495 risks: the cross-instance summary reaches someone other than the
// workspace owner (another person, an agent or a key), it carries more than
// first lines, a sibling origin outside the configuration may read it, or the
// projection drops the asker or the person's upcoming human check.
func TestOwnerSummaryStaysWithTheOwnerAndCarriesFirstLinesOnly(t *testing.T) {
	f := setup(t)
	sibling := config.Sibling{Tag: "pma", Origin: "https://pma.example"}
	f.m.WithInstances(config.Instances{Here: "ppm", Siblings: []config.Sibling{sibling}}, nil)
	q := f.ask(t, f.project, "carries_on", nil)
	f.exec(t, `UPDATE nodes SET human_check='Hand-check the voice session',fields=jsonb_set(fields,'{assignee}',to_jsonb($2::text)) WHERE id=$1`, f.ticket, f.person.ID)
	var projectKey, ticketKey string
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT p.key,t.key FROM nodes p,nodes t WHERE p.id=$1 AND t.id=$2`, f.project, f.ticket).Scan(&projectKey, &ticketKey); err != nil {
		t.Fatal(err)
	}
	get := func(p tenant.Principal, path, origin string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("GET", path, nil).WithContext(tenant.WithPrincipal(t.Context(), p))
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		f.mux.ServeHTTP(w, r)
		return w
	}
	owner, reader, agent := f.person, f.reader, f.agent
	owner.BrowserSession, reader.BrowserSession = true, true

	// The projection names the asker and the owner's upcoming human check.
	w := get(owner, "/api/decision-desk/projection", "")
	var page Page
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil {
		t.Fatalf("projection %d %s", w.Code, w.Body.String())
	}
	at := slices.IndexFunc(page.Items, func(i Item) bool { return i.ID == q.ID })
	if at < 0 || page.Items[at].AskedBy == nil || page.Items[at].AskedBy.ID != f.agent.ID || page.Items[at].AskedBy.Name != "agent" {
		t.Fatalf("question asker missing: %+v", page.Items)
	}
	if len(page.Upcoming) != 1 || page.Upcoming[0].Label != "Hand-check the voice session" || page.Upcoming[0].TicketKey != ticketKey || page.Upcoming[0].NodeID != f.ticket {
		t.Fatalf("upcoming human check %+v", page.Upcoming)
	}
	// Another person never sees the owner's upcoming step.
	if w = get(reader, "/api/decision-desk/projection", ""); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Upcoming) != 0 {
		t.Fatalf("reader projection %d upcoming %+v", w.Code, page.Upcoming)
	}

	// Siblings are named to the owner only.
	var listed Instances
	if w = get(owner, "/api/decision-desk/instances", ""); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &listed) != nil || listed.Here != "ppm" || !slices.Equal(listed.Siblings, []config.Sibling{sibling}) {
		t.Fatalf("owner instances %d %s", w.Code, w.Body.String())
	}
	if w = get(reader, "/api/decision-desk/instances", ""); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &listed) != nil || len(listed.Siblings) != 0 {
		t.Fatalf("reader learned the siblings: %s", w.Body.String())
	}

	// The configured sibling reads the owner's first lines with credentials.
	w = get(owner, "/api/decision-desk/owner-summary", sibling.Origin)
	if w.Code != 200 || w.Header().Get("Access-Control-Allow-Origin") != sibling.Origin || w.Header().Get("Access-Control-Allow-Credentials") != "true" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("owner summary %d %v", w.Code, w.Header())
	}
	var raw struct {
		Instance string             `json:"instance"`
		Items    []map[string]any   `json:"items"`
		Counts   struct{ Open int } `json:"counts"`
		Upcoming *UpcomingStep      `json:"upcoming"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil || raw.Instance != "ppm" || raw.Counts.Open != 1 || len(raw.Items) != 1 || raw.Upcoming == nil || raw.Upcoming.Label != "Hand-check the voice session" {
		t.Fatalf("summary %s", w.Body.String())
	}
	line := raw.Items[0]
	if line["id"] != q.ID || line["title"] != q.Input.Question || line["asked_by"] != "agent" || line["project_key"] != projectKey || line["held"] != false || line["href"] != "/decision-desk?item=q:"+q.ID {
		t.Fatalf("summary line %+v", line)
	}
	keys := []string{}
	for key := range line {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	if !slices.Equal(keys, []string{"asked_by", "held", "href", "id", "kind", "project_key", "title"}) {
		t.Fatalf("summary carries more than first lines: %v", keys)
	}
	// Any other origin gets no CORS grant, so its page cannot read the answer.
	if w = get(owner, "/api/decision-desk/owner-summary", "https://elsewhere.example"); w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("unconfigured origin received a CORS grant")
	}
	// Owner only: another person and an agent are refused.
	if w = get(reader, "/api/decision-desk/owner-summary", sibling.Origin); w.Code != 403 {
		t.Fatalf("non-owner summary %d %s", w.Code, w.Body.String())
	}
	for _, path := range []string{"/api/decision-desk/owner-summary", "/api/decision-desk/instances"} {
		if w = get(agent, path, sibling.Origin); w.Code != 403 {
			t.Fatalf("agent reached %s: %d", path, w.Code)
		}
	}
	// A key acting for the owner is not the owner's browser.
	delegated := owner
	delegated.KeyCreatorID = owner.ID
	if w = get(delegated, "/api/decision-desk/owner-summary", sibling.Origin); w.Code != 403 {
		t.Fatalf("delegated key summary %d", w.Code)
	}
	// An instance without siblings answers no summary.
	f.m.WithInstances(config.Instances{Here: "ppm"}, nil)
	if w = get(owner, "/api/decision-desk/owner-summary", sibling.Origin); w.Code != 404 {
		t.Fatalf("summary without siblings %d", w.Code)
	}
}
