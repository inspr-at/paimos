// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestHideStatesBoundsAndNormalisation(t *testing.T) {
	for _, query := range []string{"hide_states=", "hide_states=open", "hide_states=!done", "hide_states=" + strings.Repeat("done,", 5) + "done", "hide_states=" + strings.Repeat("x", 326)} {
		_, err := parseListQuery(httptest.NewRequest("GET", "/api/nodes?"+query, nil))
		if err == nil || !strings.Contains(err.Error(), "hide_states") {
			t.Fatalf("wrong rejection for %q: %v", query, err)
		}
	}
	q, err := parseListQuery(httptest.NewRequest("GET", "/api/nodes?hide_states="+url.QueryEscape(" DONE ,canceled,Accepted")+"&hide_closed=true", nil))
	if err != nil || !reflect.DeepEqual(q.HideStates, []string{"done", "cancelled", "accepted"}) {
		t.Fatalf("normalisation: %+v %v", q, err)
	}
}

func TestHideStatesAPIBucketsFacetsAndPaging(t *testing.T) {
	p := newPrincipal(t, "hide-states")
	// Tenant-defined ticket keeps same-state, different-category coverage.
	customKind(t, p, "ticket", "ticket")
	root := mustNode(t, p, `{"kind_id":"`+kindBySlug(t, p, "project").ID+`","title":"Hide choices"}`)
	create := func(kind, state string) nodeJSON {
		t.Helper()
		body := map[string]any{"kind_id": kindBySlug(t, p, kind).ID, "parent_id": root.ID, "title": kind + " " + state, "state": state}
		if kind == "ticket" || kind == "work" {
			body["fields"] = json.RawMessage(benefitFields)
		}
		raw, _ := json.Marshal(body)
		return mustNode(t, p, string(raw))
	}
	states := map[string]nodeJSON{}
	for _, state := range []string{"open", "done", "delivered", "accepted", "cancelled", "canceled", "archived", " QA "} {
		states[state] = create("work", state)
	}
	visibleAccepted := create("ticket", "accepted")
	// Same spellings can have different buckets on different work kinds.
	task := kindBySlug(t, p, "work")
	var schema map[string]any
	if err := json.Unmarshal(task.FieldSchema, &schema); err != nil {
		t.Fatal(err)
	}
	schema["states"] = []any{map[string]string{"state": "qa", "category": "done"}}
	raw, _ := json.Marshal(map[string]any{"field_schema": schema})
	status, body := call(t, &p, "PATCH", "/api/kinds/"+task.ID, string(raw))
	decode[kindJSON](t, status, body, 200)
	ticket := kindBySlug(t, p, "ticket")
	if err := json.Unmarshal(ticket.FieldSchema, &schema); err != nil {
		t.Fatal(err)
	}
	schema["states"] = []any{map[string]string{"state": "accepted", "category": "open"}}
	raw, _ = json.Marshal(map[string]any{"field_schema": schema})
	status, body = call(t, &p, "PATCH", "/api/kinds/"+ticket.ID, string(raw))
	decode[kindJSON](t, status, body, 200)
	other := addPrincipal(t, "hide-other")
	foreign := mustNode(t, other, `{"kind_id":"`+kindBySlug(t, other, "work").ID+`","title":"Foreign accepted","state":"accepted","fields":`+benefitFields+`}`)
	base := "/api/nodes?within=" + root.ID + "&kind=ticket,task,epic&sort=key&facets=state,kind"
	for _, check := range []struct {
		query  string
		hidden []string
	}{
		{"&hide_closed=true", []string{"done", "delivered", "accepted", "cancelled", "canceled", "archived", " QA "}},
		{"&hide_closed=true&hide_states=done,delivered,accepted,cancelled,archived", []string{"done", "delivered", "accepted", "cancelled", "canceled", "archived", " QA "}},
		{"&hide_closed=true&hide_states=done,delivered,accepted", []string{"done", "delivered", "accepted", " QA "}},
		{"&hide_closed=true&hide_states=accepted", []string{"accepted"}},
		{"&hide_closed=true&hide_states=done", []string{"done", " QA "}},
		{"&hide_closed=true&hide_states=canceled,archived", []string{"cancelled", "canceled", "archived"}},
		{"&hide_closed=false&hide_states=accepted", nil},
	} {
		t.Run(check.query, func(t *testing.T) {
			hidden := map[string]bool{}
			for _, state := range check.hidden {
				hidden[states[state].ID] = true
			}
			seen := map[string]bool{}
			path := base + check.query + "&limit=2"
			status, body := call(t, &p, "GET", path, "")
			page := decode[nodePage](t, status, body, 200)
			for {
				for _, item := range page.Items {
					if hidden[item.ID] || seen[item.ID] || item.ID == foreign.ID {
						t.Fatalf("unexpected row %s: %s", item.ID, body)
					}
					seen[item.ID] = true
				}
				total := 0
				for _, count := range page.Facets["kind"] {
					total += count
				}
				if total != len(states)+1-len(hidden) {
					t.Fatalf("facets include hidden work: %d, %s", total, body)
				}
				if page.NextCursor == nil {
					break
				}
				status, body = call(t, &p, "GET", path+"&cursor="+url.QueryEscape(*page.NextCursor), "")
				page = decode[nodePage](t, status, body, 200)
			}
			if len(seen) != len(states)+1-len(hidden) || !seen[visibleAccepted.ID] {
				t.Fatalf("missing visible work: %v", seen)
			}
		})
	}
	path := base + "&hide_closed=true&hide_states=accepted&limit=1"
	status, body = call(t, &p, "GET", path, "")
	page := decode[nodePage](t, status, body, 200)
	if page.NextCursor == nil {
		t.Fatal("missing cursor")
	}
	status, body = call(t, &p, "GET", strings.Replace(path, "hide_states=accepted", "hide_states=done", 1)+"&cursor="+url.QueryEscape(*page.NextCursor), "")
	if status != 400 || !strings.Contains(string(body), "cursor does not match this query") {
		t.Fatalf("policy cursor not bound: %d %s", status, body)
	}
	status, body = call(t, &p, "GET", "/api/nodes?ids="+foreign.ID+"&hide_closed=true&hide_states=done", "")
	if got := decode[nodePage](t, status, body, 200); len(got.Items) != 0 {
		t.Fatalf("foreign row leaked: %s", body)
	}
}
