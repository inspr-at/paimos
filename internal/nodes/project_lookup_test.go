// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

// Risk: liveness resolution can invoke whole-project aggregates, resolve the
// wrong alias, hide ambiguity, or retain access after a grant is revoked.
func TestProjectLookupAliasesVisibilityAndZeroAggregates(t *testing.T) {
	p := newPrincipal(t, "project-lookup")
	kind := kindBySlug(t, p, "project")
	makeProject := func(key, alias, classic string) nodeJSON {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"kind_id": kind.ID, "key": key, "title": key, "fields": map[string]any{"project_key": alias, "classic": map[string]any{"key": classic}}})
		return mustNode(t, p, string(raw))
	}
	first := makeProject("FIRST-1", "EXACT", "Old.KΣſ")
	second := makeProject("OTHER-1", "SECOND", "OLD-OTHER")
	work := kindBySlug(t, p, "work")
	leaf := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"key":"LEAF-1","title":"Leaf","parent_id":%q,"fields":{"project_key":"LEAFALIAS"}}`, work.ID, first.ID))
	other := addPrincipal(t, "project-lookup-other")
	foreign := mustNode(t, other, fmt.Sprintf(`{"kind_id":%q,"title":"Foreign","fields":{"project_key":"FOREIGN","classic":{"key":"EXACT"}}}`, kindBySlug(t, other, "project").ID))
	trace := &ticketGraphTracer{}
	mod := New(tracedPool(t, trace), nil)
	// Positive control: the old resolution path really calls the aggregate.
	status, body := callAs(t, mod, &p, http.MethodGet, "/api/nodes?kind_id="+kind.ID, "")
	page := decode[nodePage](t, status, body, http.StatusOK)
	if len(page.Items) != 2 {
		t.Fatalf("rich baseline lost its projects: %+v", page.Items)
	}
	before := trace.snapshot()
	if n := countMarker(before, "aeon_work_aggregates("); n != 1 {
		t.Fatalf("rich baseline aggregate calls=%d, want 1", n)
	}
	lookupCalls := 0
	lookup := func(who tenant.Principal, ref string, want int) []byte {
		t.Helper()
		lookupCalls++
		status, body := callAs(t, mod, &who, http.MethodGet, "/api/projects/lookup?"+url.Values{"ref": {ref}}.Encode(), "")
		if status != want {
			t.Fatalf("lookup %q returned %d: %s, want %d", ref, status, body, want)
		}
		return body
	}
	for _, ref := range []string{first.Key, "FIRST", "EXACT", "Old.KΣſ", "old.kςS", "  EXACT  "} {
		body := lookup(p, ref, http.StatusOK)
		var got nodePreview
		if err := json.Unmarshal(body, &got); err != nil || got.ID != first.ID {
			t.Fatalf("alias %q: %s (%v)", ref, body, err)
		}
		var fields map[string]any
		if err := json.Unmarshal(body, &fields); err != nil || len(fields) != 4 {
			t.Fatalf("lookup returned a rich projection: %s", body)
		}
	}
	for _, ref := range []string{"exact", "first", "OldXKΣſ", "FOREIGN", foreign.Key, leaf.Key, "LEAFALIAS", strings.Repeat("x", 1024)} {
		lookup(p, ref, http.StatusNotFound)
	}
	// Matches on different alias forms are still ambiguous; an exact key does
	// not take precedence. A project matching several forms counts only once.
	third := makeProject("THIRD-1", first.Key, "EXACT")
	lookup(p, first.Key, http.StatusConflict)
	lookup(p, "EXACT", http.StatusConflict)
	guest := tenant.Principal{TenantID: p.TenantID, Kind: tenant.Person, Name: "Guest"}
	if err := testDB.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Guest') RETURNING id::text`, p.TenantID).Scan(&guest.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := testDB.Admin.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='guest'`, p.TenantID, guest.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	lookup(guest, "EXACT", http.StatusOK) // the hidden duplicate cannot cause ambiguity
	for _, ref := range []string{second.Key, "SECOND", "OLD-OTHER", third.Key, "FOREIGN"} {
		lookup(guest, ref, http.StatusNotFound)
	}
	if _, err := testDB.Admin.Exec(t.Context(), `DELETE FROM role_bindings WHERE tenant_id=$1 AND principal_id=$2`, p.TenantID, guest.ID); err != nil {
		t.Fatal(err)
	}
	lookup(guest, "FIRST", http.StatusNotFound)
	if _, err := testDB.Admin.Exec(t.Context(), `UPDATE nodes SET deleted_at=clock_timestamp() WHERE tenant_id=$1 AND id=$2`, p.TenantID, second.ID); err != nil {
		t.Fatal(err)
	}
	lookup(p, "SECOND", http.StatusNotFound)
	for _, query := range []string{"", "?ref=", "?ref=+", "?ref=EXACT&ref=FIRST", "?ref=%00", "?ref=%FF", "?ref=" + strings.Repeat("x", 1025), "?ref=" + strings.Repeat("x", 3*1024+1)} {
		status, body := callAs(t, mod, &p, http.MethodGet, "/api/projects/lookup"+query, "")
		if status != http.StatusBadRequest {
			t.Fatalf("invalid reference returned %d: %s", status, body)
		}
	}
	after := trace.snapshot()[len(before):]
	if n := countMarker(after, "aeon_work_aggregates("); n != 0 {
		t.Fatalf("exact lookup invoked %d work aggregates", n)
	}
	// One candidate SELECT per valid reference; no descendant or rich-list work.
	if n := countMarker(after, "LIMIT 2"); n != lookupCalls {
		t.Fatalf("exact lookup queries=%d, want %d", n, lookupCalls)
	}
	t.Logf("before: 1 rich listing, 1 aggregate call; after: %d bounded exact lookups, 0 aggregate calls", lookupCalls)
}

func TestProjectLookupCancellation(t *testing.T) {
	p := newPrincipal(t, "project-lookup-cancel")
	kind := kindBySlug(t, p, "project")
	root := mustNode(t, p, fmt.Sprintf(`{"kind_id":%q,"title":"Cancel"}`, kind.ID))
	pool, barrier, guard := dbtest.BarrierPool(t, appPool, func(sql string) bool { return strings.Contains(sql, "LIMIT 2") })
	ctx, cancel := context.WithCancel(tenant.WithPrincipal(guard, p))
	defer cancel()
	mux := http.NewServeMux()
	New(pool, nil).Mount(mux)
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/projects/lookup?ref="+root.Key, nil).WithContext(ctx))
	}()
	barrier.Wait(t, guard) // the lookup has executed, but has not returned
	cancel()
	dbtest.Await(t, guard, done)
	if recorder.Code != http.StatusRequestTimeout || !strings.Contains(recorder.Body.String(), "project lookup canceled") {
		t.Fatalf("canceled lookup: %d %s", recorder.Code, recorder.Body.String())
	}
}
