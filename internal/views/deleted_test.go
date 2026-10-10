// SPDX-License-Identifier: AGPL-3.0-only

package views

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

// Risk: private deleted views or guessed reasons leak, and pagination drops
// ties or depends on an anchor that restoration removes. The clock is injected.
func TestDeletedViewsOwnerPaginationReasonsAndRestore(t *testing.T) {
	d := dbtest.Open(t)
	ctx := t.Context()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	var tenantID, owner, other, foreignTenant, foreignOwner string
	must(d.Admin.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('deleted-views','Views') RETURNING id::text`).Scan(&tenantID))
	must(d.Admin.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('deleted-foreign','Foreign') RETURNING id::text`).Scan(&foreignTenant))
	for _, f := range []struct {
		tenant string
		dest   *string
	}{{tenantID, &owner}, {tenantID, &other}, {foreignTenant, &foreignOwner}} {
		must(d.Admin.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','owner') RETURNING id::text`, f.tenant).Scan(f.dest))
		dbtest.BindRole(t, d, f.tenant, *f.dest, "member")
	}
	var now time.Time
	must(d.Admin.QueryRow(ctx, `SELECT clock_timestamp() + interval '1 hour'`).Scan(&now))
	m := New(d.App).(*Module)
	m.now = func() time.Time { return now }
	mux := http.NewServeMux()
	m.Mount(mux)
	request := func(tenantID, principal, method, path string, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, nil)
		if principal != "" {
			r = r.WithContext(tenant.WithPrincipal(ctx, tenant.Principal{ID: principal, TenantID: tenantID}))
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d want %d: %s", method, path, w.Code, want, w.Body.String())
		}
		return w
	}
	page := func(tenantID, principal, query string) deletedViewPage {
		t.Helper()
		w := request(tenantID, principal, http.MethodGet, "/api/views/deleted"+query, http.StatusOK)
		var out deletedViewPage
		must(json.Unmarshal(w.Body.Bytes(), &out))
		if out.Items == nil {
			t.Fatal("items must be an array, including an empty page")
		}
		return out
	}
	seed := func(tenantID, principal string, deletedAt *time.Time, shared bool, columns []string) string {
		t.Helper()
		var id string
		must(d.Admin.QueryRow(ctx, `INSERT INTO saved_views(tenant_id,owner_principal_id,name,mode,columns,shared,deleted_at)
			VALUES($1,$2,'Saved outline','outline',$3,$4,$5) RETURNING id::text`, tenantID, principal, columns, shared, deletedAt).Scan(&id))
		return id
	}
	live := seed(tenantID, owner, nil, false, []string{"title"})
	request(tenantID, owner, http.MethodDelete, "/api/views/"+live, http.StatusNoContent)
	tie := now.Add(-24 * time.Hour)
	firstTie := seed(tenantID, owner, &tie, false, []string{})
	secondTie := seed(tenantID, owner, &tie, false, []string{"title"})
	cutoff := now.Add(-deletedViewWindow)
	boundary := seed(tenantID, owner, &cutoff, false, []string{"title"})
	tooOld, future := cutoff.Add(-time.Microsecond), now.Add(time.Microsecond)
	seed(tenantID, owner, &tooOld, false, []string{"title"})
	seed(tenantID, owner, &future, false, []string{"title"})
	seed(tenantID, owner, nil, false, []string{"title"})
	shared := seed(tenantID, other, &tie, true, []string{"title"})
	foreign := seed(foreignTenant, foreignOwner, &tie, true, []string{"title"})
	// Stale owner-authored evidence from an earlier deletion is not a reason
	// for the current deletion, even when it is the latest available event.
	must(db.InTenant(dbtest.Seed(ctx), d.App, tenantID, func(tx pgx.Tx) error {
		before := savedView{ID: secondTie, OwnerPrincipal: owner, Columns: []string{"title"}}
		after := before
		old := tie.Add(-time.Hour)
		after.DeletedAt = &old
		return (sqlEventWriter{}).Append(ctx, tx, owner, "view.deleted", before, after)
	}))
	all := page(tenantID, owner, "")
	ties := []string{firstTie, secondTie}
	sort.Sort(sort.Reverse(sort.StringSlice(ties)))
	want := []string{live, ties[0], ties[1], boundary}
	if len(all.Items) != len(want) || all.NextCursor != nil {
		t.Fatalf("unpaged list = %#v", all)
	}
	for i, v := range all.Items {
		reason := "unknown"
		if i == 0 {
			reason = "owner_deleted"
		}
		if v.ID != want[i] || v.Name != "Saved outline" || v.Mode != "outline" || v.DeletionReason != reason {
			t.Fatalf("item %d = %#v, want %s / %s", i, v, want[i], reason)
		}
	}
	if got := page(tenantID, other, ""); len(got.Items) != 1 || got.Items[0].ID != shared {
		t.Fatalf("other owner's list = %#v", got)
	}
	if got := page(foreignTenant, foreignOwner, ""); len(got.Items) != 1 || got.Items[0].ID != foreign {
		t.Fatalf("foreign tenant list = %#v", got)
	}
	if got := page(foreignTenant, owner, ""); len(got.Items) != 0 {
		t.Fatalf("cross-tenant principal saw %#v", got)
	}
	request(tenantID, "", http.MethodGet, "/api/views/deleted", http.StatusUnauthorized)
	first := page(tenantID, owner, "?limit=1")
	if len(first.Items) != 1 || first.Items[0].ID != live || first.NextCursor == nil {
		t.Fatalf("first page = %#v", first)
	}
	for _, query := range []string{"?limit=0", "?limit=101", "?limit=no", "?cursor=", "?cursor=bad", "?cursor=" + strings.Repeat("a", 1025)} {
		w := request(tenantID, owner, http.MethodGet, "/api/views/deleted"+query, http.StatusBadRequest)
		if !strings.Contains(w.Body.String(), "limit") && !strings.Contains(w.Body.String(), "cursor") {
			t.Fatalf("wrong failure reason: %s", w.Body.String())
		}
	}
	query := "?limit=1&cursor=" + url.QueryEscape(*first.NextCursor)
	request(tenantID, other, http.MethodGet, "/api/views/deleted"+query, http.StatusBadRequest)
	request(foreignTenant, foreignOwner, http.MethodGet, "/api/views/deleted"+query, http.StatusBadRequest)
	// Missing binding fields must not inherit the current caller's identity.
	raw := base64.RawURLEncoding.EncodeToString([]byte(`{"at":"` + first.Items[0].DeletedAt.Format(time.RFC3339Nano) + `","id":"` + live + `"}`))
	request(tenantID, owner, http.MethodGet, "/api/views/deleted?cursor="+raw, http.StatusBadRequest)
	request(tenantID, other, http.MethodPost, "/api/views/"+live+"/restore", http.StatusNotFound)
	// An event-write failure rolls restoration back and remains a visible error.
	writer := m.eventSink
	m.eventSink = deletedTestWriter(func(context.Context, pgx.Tx, string, string, any, any) error {
		return errors.New("injected audit failure")
	})
	request(tenantID, owner, http.MethodPost, "/api/views/"+live+"/restore", http.StatusInternalServerError)
	m.eventSink = writer
	if got := page(tenantID, owner, "?limit=1"); len(got.Items) != 1 || got.Items[0].ID != live {
		t.Fatalf("failed restore hid its view: %#v", got)
	}
	request(tenantID, owner, http.MethodPost, "/api/views/"+live+"/restore", http.StatusOK)
	request(tenantID, owner, http.MethodGet, "/api/views/"+live, http.StatusOK)
	request(tenantID, owner, http.MethodPost, "/api/views/"+live+"/restore", http.StatusConflict)
	var restoredEvents int
	must(d.Admin.QueryRow(ctx, `SELECT count(*) FROM events WHERE tenant_id=$1 AND type='view.restored' AND after->>'id'=$2`, tenantID, live).Scan(&restoredEvents))
	if restoredEvents != 1 {
		t.Fatalf("restore events = %d want 1", restoredEvents)
	}
	// Restore removed the cursor anchor. Remaining equal-time rows still
	// arrive exactly once in descending UUID order and the terminal is honest.
	now = now.Add(time.Minute) // The initial window's boundary stays in later pages.
	for i := 1; i < len(want); i++ {
		next := page(tenantID, owner, query)
		if len(next.Items) != 1 || next.Items[0].ID != want[i] {
			t.Fatalf("page %d = %#v, want %s", i, next, want[i])
		}
		if i == len(want)-1 {
			if next.NextCursor != nil {
				t.Fatal("terminal page has a cursor")
			}
		} else {
			if next.NextCursor == nil {
				t.Fatal("nonterminal page has no cursor")
			}
			query = "?limit=1&cursor=" + url.QueryEscape(*next.NextCursor)
		}
	}
	_, err := d.Admin.Exec(ctx, `INSERT INTO saved_views(tenant_id,owner_principal_id,name,columns,deleted_at)
		SELECT $1::uuid,$2::uuid,'Bulk deleted',ARRAY['title']::text[],$3::timestamptz
		FROM generate_series(1,101)`, tenantID, owner, tie)
	must(err)
	for _, tc := range []struct {
		query string
		count int
	}{{"", 50}, {"?limit=100", 100}} {
		if got := page(tenantID, owner, tc.query); len(got.Items) != tc.count || got.NextCursor == nil {
			t.Fatalf("bounded page %s = %d items / %v", tc.query, len(got.Items), got.NextCursor)
		}
	}
	now = now.Add(time.Hour + time.Microsecond)
	request(tenantID, owner, http.MethodGet, "/api/views/deleted?cursor="+url.QueryEscape(*first.NextCursor), http.StatusBadRequest)
}

type deletedTestWriter func(context.Context, pgx.Tx, string, string, any, any) error

func (f deletedTestWriter) Append(ctx context.Context, tx pgx.Tx, actor, kind string, before, after any) error {
	return f(ctx, tx, actor, kind, before, after)
}
