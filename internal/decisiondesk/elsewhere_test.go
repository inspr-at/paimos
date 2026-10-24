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

// Risk: discovery remains owner-only, or projecting a person loses the asker
// and exposes another person's upcoming human check.
func TestPersonDiscoveryAndProjectionRetainOwnAskersAndUpcoming(t *testing.T) {
	f := setup(t)
	sibling := config.Sibling{Tag: "pma", Origin: "https://pma.example"}
	f.m.WithInstances(config.Instances{Here: "ppm", Siblings: []config.Sibling{sibling}})
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
	var readerPage Page
	if w = get(reader, "/api/decision-desk/projection", ""); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &readerPage) != nil || len(readerPage.Upcoming) != 0 {
		t.Fatalf("reader projection %d upcoming %+v", w.Code, readerPage.Upcoming)
	}

	// Every active signed-in person, including a project-only member, discovers siblings.
	var listed, readerListed Instances
	if w = get(owner, "/api/decision-desk/instances", ""); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &listed) != nil || !slices.Equal(listed.Siblings, []config.Sibling{sibling}) {
		t.Fatal("owner discovery failed")
	}
	if w = get(reader, "/api/decision-desk/instances", ""); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &readerListed) != nil || !slices.Equal(readerListed.Siblings, listed.Siblings) {
		t.Fatalf("member discovery failed: %d %s", w.Code, w.Body.String())
	}
	for _, p := range []tenant.Principal{owner, reader, agent} {
		if w = get(p, "/api/decision-desk/owner-summary", sibling.Origin); w.Code != 401 {
			t.Fatalf("summary accepted context without a capability: %d", w.Code)
		}
	}
	if w = get(agent, "/api/decision-desk/instances", ""); w.Code != 403 {
		t.Fatalf("agent discovered siblings: %d", w.Code)
	}
}
