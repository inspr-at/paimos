// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

// AEON-326: a live list refetches the rows that changed through its own
// query: ids narrows the list to those nodes with every other filter still
// applied, so a row that no longer matches is simply absent.
func TestListIDsNarrowsTheQueryToTheChangedRows(t *testing.T) {
	p := newPrincipal(t, "live-list-ids")
	project, ticket := kindBySlug(t, p, "project"), kindBySlug(t, p, "ticket")
	root := mustNode(t, p, `{"kind_id":"`+project.ID+`","title":"Live"}`)
	other := mustNode(t, p, `{"kind_id":"`+project.ID+`","title":"Elsewhere"}`)
	open := mustNode(t, p, `{"kind_id":"`+ticket.ID+`","parent_id":"`+root.ID+`","title":"Open","state":"new"}`)
	closed := mustNode(t, p, `{"kind_id":"`+ticket.ID+`","parent_id":"`+root.ID+`","title":"Closed","state":"cancelled"}`)
	third := mustNode(t, p, `{"kind_id":"`+ticket.ID+`","parent_id":"`+root.ID+`","title":"Third","state":"new"}`)
	foreign := mustNode(t, p, `{"kind_id":"`+ticket.ID+`","parent_id":"`+other.ID+`","title":"Foreign","state":"new"}`)

	list := func(query string) []string {
		t.Helper()
		status, raw := call(t, &p, http.MethodGet, "/api/nodes?"+query, "")
		page := decode[nodePage](t, status, raw, http.StatusOK)
		var ids []string
		for _, item := range page.Items {
			ids = append(ids, item.ID)
		}
		slices.Sort(ids)
		return ids
	}
	sorted := func(ids ...string) []string { slices.Sort(ids); return ids }
	ids := strings.Join([]string{open.ID, closed.ID, foreign.ID}, ",")

	if got := list("within=" + root.ID + "&ids=" + ids); !slices.Equal(got, sorted(open.ID, closed.ID)) {
		t.Fatalf("within and ids: %v", got)
	}
	if got := list("within=" + root.ID + "&hide_closed=true&ids=" + ids); !slices.Equal(got, sorted(open.ID)) {
		t.Fatalf("hide closed and ids: %v", got)
	}
	if got := list("within=" + root.ID + "&ids=" + open.ID + "&ids=" + third.ID); !slices.Equal(got, sorted(open.ID, third.ID)) {
		t.Fatalf("repeated ids: %v", got)
	}
	if status, raw := call(t, &p, http.MethodDelete, "/api/nodes/"+open.ID, ""); status != http.StatusNoContent {
		t.Fatalf("delete: %d %s", status, raw)
	}
	if got := list("within=" + root.ID + "&ids=" + ids); !slices.Equal(got, sorted(closed.ID)) {
		t.Fatalf("after delete: %v", got)
	}

	// More than a page of distinct ids, or one that is not an id, is refused.
	var many []string
	for i := 0; i <= maxListIDs; i++ {
		many = append(many, fmt.Sprintf("00000000-0000-4000-8000-%012x", i+1))
	}
	for _, query := range []string{"ids=nope", "ids=" + strings.Join(many, ",")} {
		if status, raw := call(t, &p, http.MethodGet, "/api/nodes?"+query, ""); status != http.StatusBadRequest {
			t.Fatalf("%.40s: %d %s", query, status, raw)
		}
	}
}

// AEON-326: bulk changes carry the revision the caller last saw per node. A
// node changed since is skipped with code conflict and keeps its value; the
// rest of the batch applies.
func TestBulkPreconditionsSkipNodesChangedSince(t *testing.T) {
	p := newPrincipal(t, "live-bulk-precondition")
	project, ticket := kindBySlug(t, p, "project"), kindBySlug(t, p, "ticket")
	root := mustNode(t, p, `{"kind_id":"`+project.ID+`","title":"Live"}`)
	fresh := mustNode(t, p, `{"kind_id":"`+ticket.ID+`","parent_id":"`+root.ID+`","title":"Fresh","state":"new"}`)
	stale := mustNode(t, p, `{"kind_id":"`+ticket.ID+`","parent_id":"`+root.ID+`","title":"Stale","state":"new"}`)
	free := mustNode(t, p, `{"kind_id":"`+ticket.ID+`","parent_id":"`+root.ID+`","title":"Free","state":"new"}`)
	seen := stale.UpdatedAt

	// Someone else changes Stale after the caller loaded it.
	status, raw := call(t, &p, http.MethodPatch, "/api/nodes/"+stale.ID, `{"fields":{"priority":"low"}}`)
	changed := decode[nodeJSON](t, status, raw, http.StatusOK)

	at := func(when time.Time) string { return when.Format(time.RFC3339Nano) }
	body := `{"ids":["` + fresh.ID + `","` + stale.ID + `","` + free.ID + `"],"priority":"high","if_unmodified_since":{"` +
		fresh.ID + `":"` + at(fresh.UpdatedAt) + `","` + stale.ID + `":"` + at(seen) + `"}}`
	status, raw = call(t, &p, http.MethodPost, "/api/nodes/bulk", body)
	result := decode[bulkResult](t, status, raw, http.StatusOK)
	if len(result.Items) != 2 || len(result.Skipped) != 1 {
		t.Fatalf("result: %#v", result)
	}
	skip := result.Skipped[0]
	if skip.ID != stale.ID || skip.Code != codeConflict || skip.Key != stale.Key {
		t.Fatalf("skip: %#v", skip)
	}
	status, raw = call(t, &p, http.MethodGet, "/api/nodes/"+stale.ID, "")
	now := decode[nodeJSON](t, status, raw, http.StatusOK)
	if !now.UpdatedAt.Equal(changed.UpdatedAt) || !strings.Contains(string(now.Fields), `"low"`) {
		t.Fatalf("stale node was written: %s %s", now.UpdatedAt, now.Fields)
	}

	// The current revision passes.
	body = `{"ids":["` + stale.ID + `"],"priority":"high","if_unmodified_since":{"` + stale.ID + `":"` + at(changed.UpdatedAt) + `"}}`
	status, raw = call(t, &p, http.MethodPost, "/api/nodes/bulk", body)
	if result := decode[bulkResult](t, status, raw, http.StatusOK); len(result.Items) != 1 || len(result.Skipped) != 0 {
		t.Fatalf("current revision: %#v", result)
	}

	for _, bad := range []string{`{"nope":"` + at(seen) + `"}`, `{"` + stale.ID + `":"yesterday"}`} {
		body := `{"ids":["` + stale.ID + `"],"priority":"low","if_unmodified_since":` + bad + `}`
		if status, raw := call(t, &p, http.MethodPost, "/api/nodes/bulk", body); status != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", bad, status, raw)
		}
	}
}
