// SPDX-License-Identifier: AGPL-3.0-only
package tenantbrand_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/tenantbrand"
)

func TestBrandConcurrentPreimages(t *testing.T) {
	f := setup(t)
	pool, barrier, ctx := dbtest.BarrierPool(t, f.db.App, func(sql string) bool { return strings.Contains(sql, "INSERT INTO tenant_brand (") })
	mux := http.NewServeMux()
	tenantbrand.New(pool).Mount(mux)
	write := func(name string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("PUT", "/api/settings/brand", strings.NewReader(`{"short_name":"`+name+`"}`))
		r = r.WithContext(tenant.WithPrincipal(ctx, f.admin))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	first := make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- write("First") }()
	pid := barrier.Wait(t, ctx)
	second := make(chan *httptest.ResponseRecorder, 1)
	done := make(chan struct{})
	go func() { second <- write("Second"); close(done) }()
	if dbtest.BlockedOrDone(t, ctx, f.db.Admin, pid, done) == "" {
		t.Fatal("second writer did not overlap first")
	}
	barrier.Release()
	expect(t, dbtest.Await(t, ctx, first), 200)
	expect(t, dbtest.Await(t, ctx, second), 200)
	rows, err := f.db.Admin.Query(ctx, `SELECT before,after FROM events WHERE tenant_id=$1 AND type=$2 ORDER BY id`, f.admin.TenantID, tenantbrand.EventType)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	previous := ""
	count := 0
	for rows.Next() {
		var before, after []byte
		if err := rows.Scan(&before, &after); err != nil {
			t.Fatal(err)
		}
		var b, a struct {
			Name string `json:"short_name"`
		}
		if err := json.Unmarshal(before, &b); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(after, &a); err != nil {
			t.Fatal(err)
		}
		if b.Name != previous {
			t.Fatalf("stale preimage %q, preceding postimage %q", b.Name, previous)
		}
		previous = a.Name
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if count != 2 || previous != "Second" {
		t.Fatalf("events=%d final=%q", count, previous)
	}
}
