// SPDX-License-Identifier: AGPL-3.0-only

package activity

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/statusautopilot"
	"github.com/inspr-at/paimos/internal/systemactor"
	"github.com/inspr-at/paimos/internal/tenant"
)

type fixture struct {
	t    *testing.T
	d    *dbtest.DB
	p    tenant.Principal
	node string
	h    http.Handler
}

func setup(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, d: dbtest.Open(t)}
	f.p = tenant.Principal{TenantID: "10000000-0000-4000-8000-000000000001", Kind: tenant.Person, Name: "Writer", Roles: []string{"member"}}
	f.tx(func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO tenants(id,slug,name) VALUES($1,'activity','Activity')`, f.p.TenantID); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1,'person','Writer',ARRAY['member']) RETURNING id::text`, f.p.TenantID).Scan(&f.p.ID); err != nil {
			return err
		}
		return dbtest.BindLegacyTx(t.Context(), tx, f.p.TenantID, f.p.ID)
	})
	f.node = f.addNode("ACT-1")
	mux := http.NewServeMux()
	New(f.d.App).Mount(mux)
	f.h = mux
	return f
}

func (f *fixture) tx(fn func(pgx.Tx) error) {
	f.t.Helper()
	if err := db.InTenant(dbtest.Seed(f.t.Context()), f.d.App, f.p.TenantID, fn); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) databaseNow() time.Time {
	f.t.Helper()
	var now time.Time
	f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(f.t.Context(), `SELECT clock_timestamp()`).Scan(&now)
	})
	return now
}

func (f *fixture) addNode(key string) string {
	f.t.Helper()
	var id string
	f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(f.t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,state) SELECT $1,id,$2,'Ticket','new' FROM node_kinds WHERE tenant_id=$1 AND slug='work' RETURNING id::text`, f.p.TenantID, key).Scan(&id)
	})
	return id
}

func (f *fixture) event(typ string, at time.Time, before, after any) string {
	f.t.Helper()
	var e events.Event
	f.tx(func(tx pgx.Tx) error {
		var err error
		e, err = events.Append(f.t.Context(), tx, f.p, events.Change{NodeID: &f.node, Type: typ, At: &at, Before: before, After: after})
		return err
	})
	return strconv.FormatInt(e.ID, 10)
}

func request(h http.Handler, p *tenant.Principal, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if p != nil {
		r = r.WithContext(tenant.WithPrincipal(r.Context(), *p))
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func (f *fixture) call(p *tenant.Principal, method, path, body string, want int) *httptest.ResponseRecorder {
	f.t.Helper()
	w := request(f.h, p, method, path, body)
	if w.Code != want {
		f.t.Fatalf("%s %s: status %d want %d: %s", method, path, w.Code, want, w.Body.String())
	}
	return w
}

func (f *fixture) page(query string) Page {
	f.t.Helper()
	w := f.call(&f.p, "GET", "/api/nodes/"+f.node+"/activity"+query, "", 200)
	var p Page
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		f.t.Fatal(err)
	}
	return p
}

func imported(typ, id string, r record) record {
	return record{"classic_ref": "source:" + typ + ":" + id, "record": r}
}
func history(id string, snapshot record) record {
	return imported("import.history", id, record{"snapshot": snapshot, "changed_by": json.Number("7"), "changed_by_name": "Classic writer"})
}

func TestTimelineMergePaginationAndImportedDiffs(t *testing.T) {
	f := setup(t)
	var mapped string
	f.tx(func(tx pgx.Tx) error {
		var identity string
		if err := tx.QueryRow(t.Context(), `INSERT INTO identities(issuer,subject,display_name) VALUES('paimos-classic','source:7','Mapped author') RETURNING id::text`).Scan(&identity); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,identity_id,name) VALUES($1,'person',$2,'Mapped author') RETURNING id::text`, f.p.TenantID, identity).Scan(&mapped)
	})
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	created := f.event("import.node_created", base.Add(24*time.Hour), nil, record{"created_at": base.Format(time.RFC3339), "fields": record{"classic": record{"source_id": "source", "created_by": json.Number("7"), "created_by_name": "Old name"}}})
	one := record{"title": "First", "status": "new", "priority": "low", "assignee_id": nil, "parent_id": nil}
	f.event("import.history", base, nil, history("1", one))
	// Invisible description/notes/time changes do not produce timeline rows.
	same := record{"title": "First", "status": "new", "priority": "low", "description": "new description", "notes": "new note", "time_total": json.Number("999")}
	f.event("import.history", base.Add(time.Minute), nil, history("2", same))
	comment := f.event("import.comment", base.Add(2*time.Minute), nil, imported("import.comment", "1:hash1", record{"id": json.Number("1"), "body": "Original", "author_id": json.Number("7"), "author": "Old name"}))
	two := record{"title": "Second", "status": "done", "priority": "high", "assignee_id": json.Number("7"), "parent_id": json.Number("9007199254740993")}
	change := f.event("import.history", base.Add(3*time.Minute), nil, history("3", two))
	f.event("import.comment", base.Add(2*time.Minute), nil, imported("import.comment", "1:hash2", record{"id": json.Number("1"), "body": "Revised", "author_id": json.Number("7"), "author": "Old name"}))
	unknown := f.event("import.comment", base.Add(3*time.Minute), nil, imported("import.comment", "2", record{"id": json.Number("2"), "body": "Unmapped", "author_id": json.Number("999"), "author": "Classic only"}))
	native := f.event("node.updated", base.Add(4*time.Minute), record{"state": "done", "title": "Second", "fields": record{"assignee": mapped}}, record{"state": "closed", "title": "Second", "fields": record{"assignee": f.p.ID}})
	move := f.event("node.moved", base.Add(5*time.Minute), record{"parent_id": nil}, record{"parent_id": f.node})
	p := f.page("?limit=200")
	want := []string{move, native, unknown, change, comment, created}
	var got []string
	for _, item := range p.Items {
		got = append(got, item.ID)
	}
	if !reflect.DeepEqual(got, want) || p.NextCursor != nil {
		t.Fatalf("order %v want %v", got, want)
	}
	if p.Items[5].At != base || p.Items[5].Author.ID == nil || *p.Items[5].Author.ID != mapped {
		t.Fatalf("imported creation: %+v", p.Items[5])
	}
	if p.Items[2].Author.ID != nil || p.Items[2].Author.Name != "Classic only" {
		t.Fatalf("unmapped author: %+v", p.Items[2])
	}
	if *p.Items[4].BodyMarkdown != "Revised" || p.Items[4].Author.Name != "Mapped author" {
		t.Fatalf("revision/author: %+v", p.Items[4])
	}
	changes := p.Items[3].Changes
	if len(changes) != 5 || *changes[0].From != "new" || *changes[0].To != "done" || *changes[2].To != "Mapped author" || *changes[4].To != "9007199254740993" {
		t.Fatalf("history diff: %+v", changes)
	}
	if len(p.Items[1].Changes) != 2 || *p.Items[1].Changes[1].From != "Mapped author" || *p.Items[1].Changes[1].To != "Writer" {
		t.Fatalf("native diff: %+v", p.Items[1])
	}
	first := f.page("?limit=2")
	if first.NextCursor == nil {
		t.Fatal("missing cursor")
	}
	// A later backdated event must not slip into subsequent pages.
	f.event("comment.created", base.Add(time.Minute), nil, commentSnapshot{Body: "Late import"})
	all := append([]Item{}, first.Items...)
	next := first.NextCursor
	for next != nil {
		page := f.page("?limit=1&cursor=" + url.QueryEscape(*next))
		all = append(all, page.Items...)
		next = page.NextCursor
	}
	if !reflect.DeepEqual(all, p.Items) {
		t.Fatalf("pagination changed result: %+v", all)
	}
	other := f.addNode("ACT-2")
	f.call(&f.p, "GET", "/api/nodes/"+other+"/activity?cursor="+*first.NextCursor, "", 400)
	if len(f.page("").Items) != 7 {
		t.Fatal("refresh did not see new event")
	}
}

func TestNativeLabelChange(t *testing.T) {
	f := setup(t)
	at := time.Date(2026, 2, 2, 9, 0, 0, 0, time.UTC)
	id := f.event("node.updated", at,
		record{"state": "done", "title": "Ticket", "fields": record{"tags": []any{"ops"}}},
		record{"state": "done", "title": "Ticket", "fields": record{"tags": []any{record{"name": "ops"}, "process-learning"}}})
	page := f.page("?limit=20")
	if len(page.Items) != 1 || page.Items[0].ID != id || page.Items[0].Author.Name != "Writer" || len(page.Items[0].Changes) != 1 {
		t.Fatalf("timeline %+v", page.Items)
	}
	change := page.Items[0].Changes[0]
	if change.Field != "tags" || change.From == nil || *change.From != "ops" || change.To == nil || *change.To != "ops, process-learning" {
		t.Fatalf("label diff %+v", change)
	}
	added := f.event("node.updated", at.Add(time.Minute),
		record{"state": "done", "title": "Ticket", "fields": record{}},
		record{"state": "done", "title": "Ticket", "fields": record{"tags": []any{"process-learning"}}})
	page = f.page("?limit=20")
	var found bool
	for _, item := range page.Items {
		if item.ID == added {
			found = len(item.Changes) == 1 && item.Changes[0].Field == "tags" && item.Changes[0].From == nil && item.Changes[0].To != nil && *item.Changes[0].To == "process-learning"
		}
	}
	if !found {
		t.Fatalf("added label %+v", page.Items)
	}
}

func TestCommentLifecycleAuthorizationIsolationAndConcurrency(t *testing.T) {
	f := setup(t)
	path := "/api/nodes/" + f.node + "/comments"
	var comment Item
	w := f.call(&f.p, "POST", path, `{"body_markdown":"**First**"}`, 201)
	if err := json.Unmarshal(w.Body.Bytes(), &comment); err != nil {
		t.Fatal(err)
	}
	if comment.Author.ID == nil || *comment.Author.ID != f.p.ID || *comment.BodyMarkdown != "**First**" {
		t.Fatalf("created %+v", comment)
	}
	itemPath := path + "/" + comment.ID
	other := f.p
	f.tx(func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1,'person','Admin',ARRAY['admin']) RETURNING id::text`, f.p.TenantID).Scan(&other.ID); err != nil {
			return err
		}
		return dbtest.BindLegacyTx(t.Context(), tx, f.p.TenantID, other.ID)
	})
	for _, method := range []string{"PATCH", "DELETE"} {
		f.call(&other, method, itemPath, `{"body_markdown":"Unauthorized"}`, 403)
	}
	w = f.call(&f.p, "PATCH", itemPath, `{"body_markdown":"**Edited**"}`, 200)
	var edited Item
	if err := json.Unmarshal(w.Body.Bytes(), &edited); err != nil {
		t.Fatal(err)
	}
	if edited.ID != comment.ID || !edited.At.Equal(comment.At) || *edited.BodyMarkdown != "**Edited**" {
		t.Fatalf("edited %+v", edited)
	}
	if *f.page("").Items[0].BodyMarkdown != "**Edited**" {
		t.Fatal("edit missing from projection")
	}
	// Save a cursor before deleting; the previous watermark still sees the old body.
	f.event("comment.created", time.Now().Add(-time.Minute), nil, commentSnapshot{Body: "Older"})
	first := f.page("?limit=1")
	f.call(&f.p, "DELETE", itemPath, "", 204)
	f.call(&f.p, "PATCH", itemPath, `{"body_markdown":"Resurrect"}`, 404)
	f.call(&f.p, "DELETE", itemPath, "", 404)
	if len(f.page("").Items) != 1 {
		t.Fatal("deleted comment remains")
	}
	if first.NextCursor == nil || len(f.page("?cursor="+*first.NextCursor).Items) != 1 {
		t.Fatal("deletion broke cursor")
	}
	f.tx(func(tx pgx.Tx) error {
		var count int
		var before, after string
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE tenant_id=$1 AND node_id=$2 AND type IN ('comment.updated','comment.deleted')`, f.p.TenantID, f.node).Scan(&count); err != nil {
			return err
		}
		if count != 2 {
			t.Errorf("mutation count %d want 2", count)
		}
		if err := tx.QueryRow(t.Context(), `SELECT before->>'body_markdown',after->>'body_markdown' FROM events WHERE tenant_id=$1 AND node_id=$2 AND type='comment.updated'`, f.p.TenantID, f.node).Scan(&before, &after); err != nil {
			return err
		}
		if before != "**First**" || after != "**Edited**" {
			t.Errorf("audit snapshots %q %q", before, after)
		}
		return nil
	})
	for _, age := range []time.Duration{15 * time.Minute, 16 * time.Minute, -time.Minute} {
		// The handler checks PostgreSQL's clock, so boundary fixtures use it too.
		id := f.event("comment.created", f.databaseNow().Add(-age), nil, commentSnapshot{Body: "Outside window"})
		for _, method := range []string{"PATCH", "DELETE"} {
			f.call(&f.p, method, path+"/"+id, `{"body_markdown":"Too late"}`, 403)
		}
	}
	// Two contested lifecycle operations cannot resurrect a deleted comment.
	for i := 0; i < 5; i++ {
		w := f.call(&f.p, "POST", path, `{"body_markdown":"Race"}`, 201)
		var item Item
		if err := json.Unmarshal(w.Body.Bytes(), &item); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		results := make(chan int, 2)
		var wg sync.WaitGroup
		for _, method := range []string{"PATCH", "DELETE"} {
			wg.Add(1)
			go func(method string) {
				defer wg.Done()
				<-start
				results <- request(f.h, &f.p, method, path+"/"+item.ID, `{"body_markdown":"Concurrent edit"}`).Code
			}(method)
		}
		close(start)
		wg.Wait()
		close(results)
		for code := range results {
			if code != 200 && code != 204 && code != 404 {
				t.Errorf("concurrent status %d", code)
			}
		}
		for _, visible := range f.page("").Items {
			if visible.ID == item.ID {
				t.Fatal("deleted comment resurrected")
			}
		}
	}
	// A second tenant uses the same event-ID space. RLS and explicit predicates
	// must keep both reads and writes bound to the caller's tenant.
	outsider := tenant.Principal{TenantID: "20000000-0000-4000-8000-000000000002", Name: "Outside", Kind: tenant.Person}
	err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, outsider.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO tenants(id,slug,name) VALUES($1,'outside','Outside')`, outsider.TenantID); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Outside') RETURNING id::text`, outsider.TenantID).Scan(&outsider.ID)
	})
	if err != nil {
		t.Fatal(err)
	}
	f.call(&outsider, "GET", "/api/nodes/"+f.node+"/activity", "", 404)
	f.call(&outsider, "POST", path, `{"body_markdown":"Cross tenant"}`, 404)
	f.call(&outsider, "PATCH", itemPath, `{"body_markdown":"Cross tenant"}`, 404)
	f.call(&outsider, "DELETE", itemPath, "", 404)
	f.call(&outsider, "GET", "/api/nodes/"+f.node+"/activity?cursor="+*first.NextCursor, "", 400)
	for _, pair := range [][2]string{{"GET", "/api/nodes/" + f.node + "/activity"}, {"POST", path}, {"PATCH", itemPath}, {"DELETE", itemPath}} {
		f.call(nil, pair[0], pair[1], `{"body_markdown":"No auth"}`, 401)
	}
	otherNode := f.addNode("ACT-2")
	f.call(&f.p, "PATCH", "/api/nodes/"+otherNode+"/comments/"+comment.ID, `{"body_markdown":"Wrong node"}`, 404)
	importID := f.event("import.comment", time.Now(), nil, imported("import.comment", "1", record{"id": json.Number("1"), "body": "Imported", "author_id": json.Number("7")}))
	f.call(&f.p, "PATCH", path+"/"+importID, `{"body_markdown":"Immutable import"}`, 404)
	// Even a 14-minute-old comment remains editable; caller can be an agent.
	agent := f.p
	agent.Kind = tenant.Agent
	allowed := f.event("comment.created", f.databaseNow().Add(-14*time.Minute), nil, commentSnapshot{Body: "Still editable"})
	f.call(&agent, "PATCH", path+"/"+allowed, `{"body_markdown":"In window"}`, 200)
	for _, body := range []string{`{}`, `null`, `{"body_markdown":null}`, `{"body_markdown":" "}`, `{"body_markdown":"ok","author_id":"fake"}`, `{"body_markdown":"ok"} {}`, `{"body_markdown":2}`, fmt.Sprintf(`{"body_markdown":%q}`, strings.Repeat("a", 65537))} {
		f.call(&f.p, "POST", path, body, 400)
	}
	for _, suffix := range []string{"?limit=0", "?limit=201", "?limit=no", "?cursor=", "?cursor=garbage"} {
		f.call(&f.p, "GET", "/api/nodes/"+f.node+"/activity"+suffix, "", 400)
	}
	f.call(&f.p, "GET", "/api/nodes/not-uuid/activity", "", 400)
	for _, id := range []string{"0", "-1", "no", "9223372036854775808"} {
		f.call(&f.p, "DELETE", path+"/"+id, "", 400)
	}
	// A soft-deleted node has no public timeline or comment writes.
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `UPDATE nodes SET deleted_at=clock_timestamp() WHERE tenant_id=$1 AND id=$2`, f.p.TenantID, f.node)
		return err
	})
	f.call(&f.p, "GET", "/api/nodes/"+f.node+"/activity", "", 404)
	f.call(&f.p, "POST", path, `{"body_markdown":"Deleted node"}`, 404)
}

func TestTimelineAtImportScale(t *testing.T) {
	f, want := setupTimelineImportScale(t)
	// Repeated first-page reads and full traversal must return exactly the
	// requested ticket's items, regardless of unrelated imported event volume.
	for range 10 {
		page := f.page("?limit=20")
		assertTimelineItems(t, page.Items, want[:20])
		if page.NextCursor == nil {
			t.Fatal("first scale page is missing its continuation cursor")
		}
	}
	query := "?limit=20"
	for offset := 0; offset < len(want); offset += 20 {
		page := f.page(query)
		end := min(offset+20, len(want))
		assertTimelineItems(t, page.Items, want[offset:end])
		if end == len(want) {
			if page.NextCursor != nil {
				t.Fatal("last scale page has a cursor")
			}
		} else {
			if page.NextCursor == nil {
				t.Fatal("scale page is missing its continuation cursor")
			}
			query = "?limit=20&cursor=" + url.QueryEscape(*page.NextCursor)
		}
	}
}

func assertTimelineItems(t *testing.T, got, want []Item) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("timeline item count: got %d, want %d", len(got), len(want))
	}
	for i := range want {
		actual, expected := got[i], want[i]
		// Postgres may serialize an equivalent timestamp with the connection's
		// timezone offset. Compare instants independently of time.Location.
		actual.At, expected.At = actual.At.UTC(), expected.At.UTC()
		if !reflect.DeepEqual(actual, expected) {
			t.Fatalf("timeline item %d: got %+v, want %+v", i, actual, expected)
		}
	}
}

// setupTimelineImportScale seeds 27,422 unrelated imported events and a
// same-ticket history of 240 imported snapshots and 30 Markdown comments.
// The first history snapshot is a baseline; the other 239 are visible changes.
func setupTimelineImportScale(t *testing.T) (*fixture, []Item) {
	t.Helper()
	f := setup(t)
	noise := f.addNode("ACT-2")
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO events(tenant_id,node_id,actor_principal_id,type,after)
		 SELECT $1,$2,$3,CASE WHEN n<=11620 THEN 'import.comment' ELSE 'import.history' END,
		 jsonb_build_object('record',jsonb_build_object('id',n,'body',repeat('Historical content ',30)))
		 FROM generate_series(1,27422) n`, f.p.TenantID, noise, f.p.ID)
		return err
	})
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	want := make([]Item, 0, 269)
	previous := "new"
	for i := range 240 {
		state := "new"
		if i%2 == 1 {
			state = "done"
		}
		at := base.Add(time.Duration(i) * time.Second)
		id := f.event("import.history", at, nil, history(strconv.Itoa(i+1), record{
			"status": state, "description": strings.Repeat("Historical content ", 30),
		}))
		if i > 0 {
			from, to := previous, state
			want = append(want, Item{ID: id, At: at, Type: "change", Author: Author{Name: "Classic writer"},
				Changes: []FieldChange{{Field: "status", From: &from, To: &to}}})
		}
		previous = state
	}
	for i := range 30 {
		at := base.Add(time.Duration(240+i) * time.Second)
		body := fmt.Sprintf("Comment %d: %s", i, strings.Repeat("A realistic Markdown comment. ", 100))
		id := f.event("comment.created", at, nil, commentSnapshot{Body: body})
		want = append(want, Item{ID: id, At: at, Type: "comment", Author: Author{ID: &f.p.ID, Name: f.p.Name}, BodyMarkdown: &body})
	}
	for i, j := 0, len(want)-1; i < j; i, j = i+1, j-1 {
		want[i], want[j] = want[j], want[i]
	}
	f.tx(func(tx pgx.Tx) error {
		var targetRows, unrelatedRows int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FILTER (WHERE node_id=$2),count(*) FILTER (WHERE node_id=$3)
		 FROM events WHERE tenant_id=$1`, f.p.TenantID, f.node, noise).Scan(&targetRows, &unrelatedRows); err != nil {
			return err
		}
		if targetRows != 270 || unrelatedRows != 27422 {
			t.Fatalf("scale fixture rows: target=%d unrelated=%d", targetRows, unrelatedRows)
		}
		// Check the available node-scoped access path without depending on
		// planner costs, machine speed or a particular EXPLAIN plan choice.
		var definition, predicate string
		if err := tx.QueryRow(t.Context(), `SELECT pg_get_indexdef(indexrelid),pg_get_expr(indpred,indrelid)
		 FROM pg_index WHERE indexrelid='events_node_idx'::regclass AND indrelid='events'::regclass
		 AND indisvalid AND indisready`).Scan(&definition, &predicate); err != nil {
			return err
		}
		if !strings.Contains(definition, "(tenant_id, node_id, id)") || predicate != "(node_id IS NOT NULL)" {
			t.Fatalf("unexpected node index: %s WHERE %s", definition, predicate)
		}
		return nil
	})
	return f, want
}

func TestHistorySourcesDoNotMix(t *testing.T) {
	// Baselines are separate even if two source instances use the same IDs.
	parse := func(s string) record {
		r, err := decodeRecord([]byte(s))
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	base := time.Now()
	rows := []activityEvent{
		{id: 1, at: base, typ: "import.history", after: parse(`{"classic_ref":"one:import.history:1","record":{"snapshot":{"status":"new"},"changed_by":7,"changed_by_name":"One"}}`)},
		{id: 2, at: base, typ: "import.history", after: parse(`{"classic_ref":"two:import.history:1","record":{"snapshot":{"status":"done"},"changed_by":7,"changed_by_name":"Two"}}`)},
		{id: 3, at: base, typ: "import.history", after: parse(`{"classic_ref":"one:import.history:2","record":{"snapshot":{"status":"done"},"changed_by":7,"changed_by_name":"One"}}`)},
	}
	items := project(rows, map[string]Author{"two:7": {Name: "Wrong source author"}})
	if len(items) != 1 || items[0].Author.Name != "One" || items[0].Author.ID != nil || *items[0].Changes[0].From != "new" || *items[0].Changes[0].To != "done" {
		t.Fatalf("source crossover: %+v", items)
	}
}

func TestStoredClassicUserNamesAndNativeAuthor(t *testing.T) {
	f := setup(t)
	var mapped string
	f.tx(func(tx pgx.Tx) error {
		var identity string
		if err := tx.QueryRow(t.Context(), `INSERT INTO identities(issuer,subject,display_name) VALUES('paimos-classic','source:7','Markus Barta') RETURNING id::text`).Scan(&identity); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,identity_id,name) VALUES($1,'person',$2,'Markus Barta') RETURNING id::text`, f.p.TenantID, identity).Scan(&mapped)
	})
	at := time.Now()
	f.event("import.user_created", at, nil, record{"principal": record{"id": mapped}, "classic": record{"source_id": "source", "id": json.Number("7"), "username": "mba"}})
	f.event("import.comment", at, nil, imported("import.comment", "1", record{"id": json.Number("1"), "author": "mba", "body": "By username"}))
	f.event("import.comment", at, nil, imported("import.comment", "2", record{"id": json.Number("2"), "author_id": json.Number("7"), "author": "mba", "body": "By ID"}))
	f.event("import.node_created", at, nil, record{"fields": record{"created_by": mapped, "classic": record{"source_id": "source", "created_by_name": "unmapped"}}})
	page := f.page("")
	if len(page.Items) != 3 {
		t.Fatalf("items: %+v", page.Items)
	}
	for _, item := range page.Items {
		if item.Author.ID == nil || *item.Author.ID != mapped || item.Author.Name != "Markus Barta" {
			t.Fatalf("mapped author: %+v", item.Author)
		}
	}
	// A username in a different source cannot claim the stored mapping.
	if got := classicAuthor(record{"author": "mba"}, "other-source", map[string]Author{"username:source:mba": {ID: &mapped, Name: "Markus Barta"}}, "author_id", "author"); got.ID != nil {
		t.Fatal("cross-source author mapping")
	}
}

func TestSystemActorMetadataIsNotANameMatch(t *testing.T) {
	f := setup(t)
	var systemID, lookalike string
	f.tx(func(tx pgx.Tx) error {
		actor, err := systemactor.Ensure(t.Context(), tx, f.p.TenantID)
		if err != nil {
			return err
		}
		systemID = actor.ID
		return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1,'person','System','{}') RETURNING id::text`, f.p.TenantID).Scan(&lookalike)
	})
	at := time.Now().UTC().Truncate(time.Microsecond)
	meta, err := json.Marshal(map[string]string{"job": "learning-tagger", "reason": "method learning tagger"})
	if err != nil {
		t.Fatal(err)
	}
	write := func(actor tenant.Principal, when time.Time, metadata json.RawMessage, from, to string) {
		t.Helper()
		f.tx(func(tx pgx.Tx) error {
			_, err := events.Append(t.Context(), tx, actor, events.Change{
				NodeID: &f.node, Type: "node.updated", At: &when, Metadata: metadata,
				Before: record{"title": "Ticket", "state": "new", "fields": record{"tags": []any{from}}},
				After:  record{"title": "Ticket", "state": "new", "fields": record{"tags": []any{to}}},
			})
			return err
		})
	}
	write(tenant.Principal{ID: systemID, TenantID: f.p.TenantID, Kind: tenant.Agent, Name: "System"}, at, meta, "ops", "process-learning")
	write(tenant.Principal{ID: lookalike, TenantID: f.p.TenantID, Kind: tenant.Person, Name: "System"}, at.Add(time.Minute), nil, "process-learning", "kept")
	page := f.page("?limit=20")
	if len(page.Items) != 2 {
		t.Fatalf("items: %+v", page.Items)
	}
	raw := f.call(&f.p, "GET", "/api/nodes/"+f.node+"/activity?limit=20", "", 200).Body.Bytes()
	var payload struct {
		Items []struct {
			Author json.RawMessage `json:"author"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	var systemAuthor, personAuthor map[string]any
	if err := json.Unmarshal(payload.Items[0].Author, &personAuthor); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(payload.Items[1].Author, &systemAuthor); err != nil {
		t.Fatal(err)
	}
	if systemAuthor["automatic"] != true || systemAuthor["job"] != "learning-tagger" || systemAuthor["reason"] != "method learning tagger" || systemAuthor["name"] != "System" {
		t.Fatalf("system author: %s", payload.Items[1].Author)
	}
	if _, ok := personAuthor["automatic"]; ok || personAuthor["name"] != "System" || personAuthor["job"] != nil {
		t.Fatalf("lookalike author: %s", payload.Items[0].Author)
	}
	if page.Items[1].Author.ID == nil || *page.Items[1].Author.ID != systemID || !page.Items[1].Author.Automatic {
		t.Fatalf("system item: %+v", page.Items[1].Author)
	}
	if page.Items[0].Author.Automatic || page.Items[0].Author.ID == nil || *page.Items[0].Author.ID != lookalike {
		t.Fatalf("lookalike item: %+v", page.Items[0].Author)
	}
}

func TestKindChangeAppearsInHistory(t *testing.T) {
	f := setup(t)
	at := f.databaseNow()
	f.tx(func(tx pgx.Tx) error {
		_, err := events.Append(f.t.Context(), tx, f.p, events.Change{
			NodeID:   &f.node,
			Type:     "node.kind_changed",
			At:       &at,
			Before:   map[string]any{"title": "Ticket", "state": "new", "kind_id": "before"},
			After:    map[string]any{"title": "Ticket", "state": "new", "kind_id": "after"},
			Metadata: json.RawMessage(`{"from":"ticket","to":"epic","by":"` + f.p.ID + `"}`),
		})
		return err
	})
	page := f.page("")
	for _, item := range page.Items {
		for _, change := range item.Changes {
			if change.Field == "kind" && change.From != nil && *change.From == "ticket" && change.To != nil && *change.To == "epic" {
				return
			}
		}
	}
	t.Fatalf("timeline %#v", page.Items)
}

func TestStatusAutopilotActivityHasReasonAndIndependentUndo(t *testing.T) {
	f := setup(t)
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET state='in_progress',created_at=clock_timestamp()-interval '5 days',updated_at=clock_timestamp()-interval '5 days' WHERE id=$1`, f.node)
		return err
	})
	if err := statusautopilot.New(f.d.App).RunTenant(t.Context(), f.p.TenantID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	w := f.call(&f.p, "GET", "/api/nodes/"+f.node+"/activity", "", 200)
	var page Page
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, item := range page.Items {
		if item.AutomaticChange != nil {
			found = true
			if !item.Author.Automatic || item.Author.Job != "status-autopilot" || item.AutomaticChange.Reason == "" || item.AutomaticChange.From != "in_progress" || item.AutomaticChange.To != "open" {
				t.Fatalf("bad automatic activity: %+v", item)
			}
		}
	}
	if !found {
		t.Fatal("automatic event disappeared from Activity")
	}
}
