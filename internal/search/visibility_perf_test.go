// SPDX-License-Identifier: AGPL-3.0-only

package search

import (
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

// ADR-003 P2 performance guard: search over 6000 nodes stays within the list
// budget with project visibility on, for a workspace member and for a guest
// of the large project, and the guest never gets the other project's hits.
func TestSearch6000WithProjectVisibility(t *testing.T) {
	d := dbtest.Open(t)
	const tenantID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaab1"
	seedTenant(t, d, tenantID, "search-visibility")
	member := seedPerson(t, d, tenantID, "cccccccc-cccc-4ccc-8ccc-cccccccccc01", "Member")
	var large, other string
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		for _, p := range []struct {
			key  string
			dest *string
		}{{"LRG-1", &large}, {"OTH-1", &other}} {
			if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title) SELECT $1,$2,id,'Project '||$2 FROM node_kinds WHERE slug='project' RETURNING id::text`, tenantID, p.key).Scan(p.dest); err != nil {
				return err
			}
		}
		largeRows, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title,body,parent_id,position)
		    SELECT $1,'LRG-'||(g+1),k.id,'Signal item '||g,'Searchable body text '||g,$2,g
		    FROM node_kinds k, generate_series(1,6000) g WHERE k.tenant_id=$1 AND k.slug='work'`, tenantID, large)
		if err != nil {
			return err
		}
		if largeRows.RowsAffected() != 6000 {
			t.Fatalf("large search fixture has %d nodes, want 6000", largeRows.RowsAffected())
		}
		otherRows, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title,body,parent_id,position)
		    SELECT $1,'OTH-'||(g+1),k.id,'Signal hidden '||g,'Searchable secret '||g,$2,g
		    FROM node_kinds k, generate_series(1,300) g WHERE k.tenant_id=$1 AND k.slug='work'`, tenantID, other)
		if err == nil && otherRows.RowsAffected() != 300 {
			t.Fatalf("hidden search fixture has %d nodes, want 300", otherRows.RowsAffected())
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Admin.Exec(t.Context(), `ANALYZE nodes`); err != nil {
		t.Fatal(err)
	}
	guest := tenant.Principal{ID: "dddddddd-dddd-4ddd-8ddd-dddddddddd01", TenantID: tenantID, Kind: tenant.Person, Name: "Guest"}
	if _, err := d.Admin.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'person','Guest')`, tenantID, guest.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Admin.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='guest'`, tenantID, guest.ID, large); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(d.App, nil).Mount(mux)
	limit := 150 * time.Millisecond
	if os.Getenv("CI") != "" {
		limit = 600 * time.Millisecond
	}
	for _, who := range []struct {
		name string
		p    tenant.Principal
	}{{"member", member}, {"guest", guest}} {
		var fastest time.Duration
		for i := 0; i < 3; i++ {
			start := time.Now()
			status, raw := call(mux, &who.p, "/api/search?q="+url.QueryEscape("signal searchable")+"&limit=50")
			elapsed := time.Since(start)
			page := mustOK(t, status, raw)
			if len(page.Items) != 50 {
				t.Fatalf("%s: %d hits", who.name, len(page.Items))
			}
			if who.name == "guest" && strings.Contains(raw, "OTH-") {
				t.Fatalf("guest search shows the other project")
			}
			if fastest == 0 || elapsed < fastest {
				fastest = elapsed
			}
		}
		t.Logf("%s search over 6000 nodes with visibility: fastest of 3 = %s", who.name, fastest)
		if fastest >= limit {
			t.Fatalf("%s search exceeded %s: %s", who.name, limit, fastest)
		}
	}
}
