// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

// ADR-003 P2 performance guard: with project visibility on, the 6000-node
// list, its facets and the project overview stay within the same budget as
// TestList6000Performance, both for a workspace member (every project) and
// for a guest bound to that project (an explicit project list).
func TestList6000WithProjectVisibility(t *testing.T) {
	member := newPrincipal(t, "visible-large-list")
	project := kindBySlug(t, member, "project")
	ticket := kindBySlug(t, member, "work")
	root := mustNode(t, member, `{"kind_id":"`+project.ID+`","title":"Large project"}`)
	other := mustNode(t, member, `{"kind_id":"`+project.ID+`","title":"Other project"}`)
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, member.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO nodes (tenant_id,key,kind_id,title,fields,state,parent_id,position)
            SELECT $1::uuid,'VIS-'||g,$2::uuid,'Item '||g,
                jsonb_build_object('priority',CASE g%3 WHEN 0 THEN 'high' WHEN 1 THEN 'medium' ELSE 'low' END),
                CASE g%4 WHEN 0 THEN 'new' WHEN 1 THEN 'active' WHEN 2 THEN 'qa' ELSE 'done' END,
                $3::uuid,g FROM generate_series(1,6000) AS g`, member.TenantID, ticket.ID, root.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO nodes (tenant_id,key,kind_id,title,state,parent_id,position)
            SELECT $1::uuid,'OTH-'||g,$2::uuid,'Hidden '||g,'new',$3::uuid,g FROM generate_series(1,300) AS g`, member.TenantID, ticket.ID, other.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, member.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `ANALYZE nodes`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	guest := tenant.Principal{TenantID: member.TenantID, Kind: tenant.Person, Name: "Guest"}
	if err := testDB.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Guest') RETURNING id::text`, member.TenantID).Scan(&guest.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := testDB.Admin.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='guest'`, member.TenantID, guest.ID, root.ID); err != nil {
		t.Fatal(err)
	}
	// The materialized list filter keeps bad no-statistics plans bounded while
	// adding a small predictable cost under project visibility.
	limit := 300 * time.Millisecond
	if os.Getenv("CI") != "" {
		limit = 600 * time.Millisecond
	}
	path := "/api/nodes?within=" + root.ID + "&sort=state,-updated_at&limit=50&facets=state,kind,priority,assignee"
	for _, who := range []struct {
		name string
		p    tenant.Principal
	}{{"member", member}, {"guest", guest}} {
		var fastest time.Duration
		for i := 0; i < 3; i++ {
			start := time.Now()
			status, body := call(t, &who.p, http.MethodGet, path, "")
			elapsed := time.Since(start)
			page := decode[nodePage](t, status, body, http.StatusOK)
			if len(page.Items) != 50 || page.NextCursor == nil || page.Facets["kind"]["work"] != 6000 {
				t.Fatalf("%s large list: %d, %#v", who.name, len(page.Items), page.Facets)
			}
			if fastest == 0 || elapsed < fastest {
				fastest = elapsed
			}
		}
		t.Logf("%s 6000-node list with visibility: fastest of 3 = %s", who.name, fastest)
		if fastest >= limit {
			t.Fatalf("%s 6000-node list exceeded %s: %s", who.name, limit, fastest)
		}
		start := time.Now()
		status, body := call(t, &who.p, http.MethodGet, "/api/projects", "")
		projects := decode[projectPage](t, status, body, http.StatusOK)
		elapsed := time.Since(start)
		t.Logf("%s projects with visibility: %s", who.name, elapsed)
		want := 2
		if who.name == "guest" {
			want = 1
		}
		if len(projects.Items) != want {
			t.Fatalf("%s sees %d projects, want %d", who.name, len(projects.Items), want)
		}
		if elapsed >= 4*limit {
			t.Fatalf("%s projects overview exceeded %s: %s", who.name, 4*limit, elapsed)
		}
	}
}
