// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

func TestProjectStatusCountsAndSelectors(t *testing.T) {
	p := newPrincipal(t, "header-status-counts")
	root := mustNode(t, p, `{"kind_id":"`+kindBySlug(t, p, "project").ID+`","title":"Project","state":"active"}`)
	folder := mustNode(t, p, `{"kind_id":"`+kindBySlug(t, p, "release").ID+`","title":"Folder","state":"done","parent_id":"`+root.ID+`"}`)
	create := func(kind, state string) nodeJSON {
		t.Helper()
		body := map[string]any{"kind_id": kindBySlug(t, p, kind).ID, "title": kind + " " + state, "state": state, "parent_id": folder.ID}
		if kind == "ticket" {
			body["fields"] = json.RawMessage(benefitFields)
		}
		raw, _ := json.Marshal(body)
		return mustNode(t, p, string(raw))
	}
	for _, state := range []string{"new", "backlog", "open", "blocked", "mystery", "in_progress", "in-progress", "in progress", "inprogress", "active", "qa", " QA ", "done", "delivered", "accepted", "cancelled", "canceled", "archived"} {
		create("ticket", state)
	}
	create("task", "qa")
	create("epic", "blocked")
	create("memory", "done")
	deleted := create("ticket", "done")
	if status, body := call(t, &p, "DELETE", "/api/nodes/"+deleted.ID, ""); status != 204 {
		t.Fatalf("delete fixture: %d %s", status, body)
	}
	// A foreign tenant's identical statuses must not contribute.
	other := addPrincipal(t, "header-foreign")
	foreign := mustNode(t, other, `{"kind_id":"`+kindBySlug(t, other, "project").ID+`","title":"Foreign","state":"active"}`)
	mustNode(t, other, `{"kind_id":"`+kindBySlug(t, other, "task").ID+`","title":"Foreign work","state":"done","parent_id":"`+foreign.ID+`"}`)
	ticket := kindBySlug(t, p, "ticket")
	var schema map[string]any
	if err := json.Unmarshal(ticket.FieldSchema, &schema); err != nil {
		t.Fatal(err)
	}
	schema["states"] = []any{map[string]string{"state": "mystery", "category": "done"}, map[string]string{"state": "qa", "category": "open"}, map[string]string{"state": "blocked", "category": "doing"}}
	raw, _ := json.Marshal(map[string]any{"field_schema": schema})
	status, body := call(t, &p, "PATCH", "/api/kinds/"+ticket.ID, string(raw))
	decode[kindJSON](t, status, body, 200)
	status, body = call(t, &p, "GET", "/api/projects", "")
	projects := decode[projectPage](t, status, body, 200)
	if len(projects.Items) != 1 {
		t.Fatalf("project scope: %s", body)
	}
	got := projects.Items[0]
	counts := map[string]int{}
	buckets := map[string]int{}
	for _, entry := range got.StatusCounts {
		counts[entry.State+":"+entry.Bucket] = entry.Count
		buckets[entry.Bucket] += entry.Count
	}
	want := map[string]int{"new:open": 1, "backlog:open": 1, "open:open": 1, "blocked:in_progress": 1, "blocked:open": 1, "mystery:done": 1, "in_progress:in_progress": 5, "qa:open": 2, "qa:in_progress": 1, "done:done": 1, "delivered:done": 1, "accepted:done": 1, "cancelled:cancelled": 2, "archived:archived": 1}
	if !reflect.DeepEqual(counts, want) {
		t.Fatalf("status detail: got %v, want %v", counts, want)
	}
	if got.StatusCountsTruncated || got.Total != 20 || buckets["open"] != got.Open || buckets["in_progress"] != got.InProgress || buckets["done"] != got.Done || buckets["cancelled"] != got.Cancelled || buckets["archived"] != got.Archived {
		t.Fatalf("bucket/detail mismatch: %+v %v", got, buckets)
	}
	for _, bucket := range []string{"open", "in_progress", "done", "cancelled", "archived"} {
		status, body = call(t, &p, "GET", "/api/nodes?within="+root.ID+"&kind=ticket,task,epic&work_bucket="+bucket+"&limit=100", "")
		page := decode[nodePage](t, status, body, 200)
		if len(page.Items) != buckets[bucket] {
			t.Fatalf("%s selector: %d, wanted %d: %s", bucket, len(page.Items), buckets[bucket], body)
		}
	}
	for _, check := range []struct {
		query string
		count int
	}{{"work_state=in_progress", 5}, {"work_state=qa", 3}, {"work_state=cancelled", 2}, {"work_state=qa&work_bucket=open", 2}, {"work_state=!qa&work_bucket=open", 4}, {"state=qa", 2}, {"work_bucket=done&hide_closed=true", 0}} {
		status, body = call(t, &p, "GET", "/api/nodes?within="+root.ID+"&kind=ticket,task,epic&limit=100&"+check.query, "")
		page := decode[nodePage](t, status, body, 200)
		if len(page.Items) != check.count {
			t.Fatalf("%s: %s", check.query, body)
		}
	}
	// Keyset paging and facet totals use the same bucket selector. A cursor
	// cannot be replayed after changing that selector.
	path := "/api/nodes?within=" + root.ID + "&kind=ticket,task,epic&work_bucket=open&facets=state&limit=2"
	status, body = call(t, &p, "GET", path, "")
	first := decode[nodePage](t, status, body, 200)
	if first.NextCursor == nil {
		t.Fatal("missing cursor for six open records")
	}
	status, body = call(t, &p, "GET", strings.Replace(path, "work_bucket=open", "work_bucket=done", 1)+"&cursor="+url.QueryEscape(*first.NextCursor), "")
	if status != 400 || !strings.Contains(string(body), "cursor does not match this query") {
		t.Fatalf("bucket cursor not bound: %d %s", status, body)
	}
	seen := map[string]bool{}
	page := first
	for {
		for _, node := range page.Items {
			if seen[node.ID] {
				t.Fatalf("duplicate page row %s", node.ID)
			}
			seen[node.ID] = true
		}
		facetTotal := 0
		for _, count := range page.Facets["state"] {
			facetTotal += count
		}
		if facetTotal != got.Open {
			t.Fatalf("open facets %d, summary %d", facetTotal, got.Open)
		}
		if page.NextCursor == nil {
			break
		}
		status, body = call(t, &p, "GET", path+"&cursor="+url.QueryEscape(*page.NextCursor), "")
		page = decode[nodePage](t, status, body, 200)
	}
	if len(seen) != got.Open {
		t.Fatalf("paged open work: %d, summary %d", len(seen), got.Open)
	}
}

func TestProjectStatusCountsBoundAndEmpty(t *testing.T) {
	p := newPrincipal(t, "header-count-bound")
	root := mustNode(t, p, `{"kind_id":"`+kindBySlug(t, p, "project").ID+`","title":"Many states"}`)
	empty := mustNode(t, p, `{"kind_id":"`+kindBySlug(t, p, "project").ID+`","title":"Empty"}`)
	kind := kindBySlug(t, p, "task")
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO nodes (tenant_id,kind_id,parent_id,key,title,state) SELECT $1,$2,$3,'COUNT-' || i,'Status ' || i,'state_' || i FROM generate_series(1,$4) i`, p.TenantID, kind.ID, root.ID, maxProjectStatusCounts+1)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	status, body := call(t, &p, "GET", "/api/projects", "")
	page := decode[projectPage](t, status, body, 200)
	if len(page.Items) != 2 {
		t.Fatalf("projects: %s", body)
	}
	for _, got := range page.Items {
		if got.ID == root.ID && (got.Total != 257 || got.Open != 257 || len(got.StatusCounts) != 256 || !got.StatusCountsTruncated) {
			t.Fatalf("bounded detail: %+v", got)
		}
		if got.ID == empty.ID && (got.Total != 0 || got.StatusCounts == nil || len(got.StatusCounts) != 0 || got.StatusCountsTruncated) {
			t.Fatalf("empty detail: %+v", got)
		}
	}
}

func TestHeaderSelectorBounds(t *testing.T) {
	for _, query := range []string{"work_bucket=unknown", "work_bucket=!done", "work_bucket=" + strings.Repeat("open,", 6) + "open", "work_state=" + strings.Repeat("x", 65), "work_state=" + strings.Join(func() []string {
		values := []string{}
		for i := 0; i < 65; i++ {
			values = append(values, fmt.Sprintf("state_%d", i))
		}
		return values
	}(), ","), "work_state=!"} {
		if _, err := parseListQuery(httptest.NewRequest("GET", "/api/nodes?"+query, nil)); err == nil {
			t.Fatalf("accepted invalid header selector %q", query)
		} else if !strings.Contains(err.Error(), strings.Split(query, "=")[0]) {
			t.Fatalf("wrong rejection for %q: %v", query, err)
		}
	}
	if _, err := parseListQuery(httptest.NewRequest("GET", "/api/nodes?work_state="+url.QueryEscape("in_progress,!qa")+"&work_bucket=done,cancelled", nil)); err != nil {
		t.Fatal(err)
	}
}
