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

const (
	visibilitySamples = 5
	// Faceted page over the same 50-node page without facets, after one warmup.
	// Quiet agm2-ci count=10 sits at 1.36–1.53. One loaded sample on that host
	// was 8.62 (list 1.49s, baseline 173ms). 12 passes that sample. Ten times
	// the faceted page alone then lands near 14 or higher and fails. AEON-1601.
	visibilityListRatio = 12
	// Above the slowest non-regression sample on a loaded hosted runner
	// (8.66s, #798) and still a hard stop: 10× a 1.5s loaded sample, and
	// about 25× the old 600ms CI budget. Runner-wide slowness is cancelled
	// by the ratio; this catches a list that is slow even against that.
	visibilityListCeiling = 15 * time.Second
)

// ADR-003 P2 performance guard. Project visibility on, the 6000-node list
// stays within a same-run ratio of the same page without facets, for a
// workspace member and for a guest of that project. The strict 600ms budget
// lives on the nightly timing lane (TestList6000VisibilityNightlyAbsoluteBudget).
func TestList6000WithProjectVisibility(t *testing.T) {
	member, guest, root := prepareVisibilityList(t)
	listPath := visibilityListPath(root.ID, true)
	basePath := visibilityListPath(root.ID, false)
	for _, who := range []struct {
		name string
		p    tenant.Principal
	}{{"member", member}, {"guest", guest}} {
		list := timeVisibilityList(t, &who.p, listPath, true)
		baseline := timeVisibilityList(t, &who.p, basePath, false)
		if baseline <= 0 {
			t.Fatalf("%s baseline was %s", who.name, baseline)
		}
		ratio := float64(list) / float64(baseline)
		t.Logf("%s 6000-node list with visibility: warmup+best of %d = %s; same-run baseline %s; ratio %.2f; ceiling %s",
			who.name, visibilitySamples, list, baseline, ratio, visibilityListCeiling)
		if list >= time.Duration(visibilityListRatio)*baseline {
			t.Fatalf("%s 6000-node list ratio %.2f exceeded %d (list %s, baseline %s)", who.name, ratio, visibilityListRatio, list, baseline)
		}
		if list >= visibilityListCeiling {
			t.Fatalf("%s 6000-node list exceeded hard ceiling %s: %s (baseline %s)", who.name, visibilityListCeiling, list, baseline)
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
		projectLimit := 1200 * time.Millisecond
		if os.Getenv("CI") != "" {
			projectLimit = 2400 * time.Millisecond
		}
		if elapsed >= projectLimit {
			t.Fatalf("%s projects overview exceeded %s: %s", who.name, projectLimit, elapsed)
		}
	}
}

// Strict absolute budget for the same list. Classified NIGHTLY on the timing
// lane, so PR go-timing (--full --timing) does not run it and nightly-go-timing
// (--all --timing) runs it alone. The registry has no dedicated-hardware lane;
// nightly-go-timing is still the hosted runner, which is why the PR gate uses
// the ratio instead of this budget.
func TestList6000VisibilityNightlyAbsoluteBudget(t *testing.T) {
	member, _, root := prepareVisibilityList(t)
	limit := 300 * time.Millisecond
	if os.Getenv("CI") != "" {
		limit = 600 * time.Millisecond
	}
	fastest := timeVisibilityList(t, &member, visibilityListPath(root.ID, true), true)
	t.Logf("member 6000-node list nightly absolute budget: warmup+best of %d = %s (limit %s)", visibilitySamples, fastest, limit)
	if fastest >= limit {
		t.Fatalf("member 6000-node list exceeded %s: %s", limit, fastest)
	}
}

func prepareVisibilityList(t *testing.T) (member, guest tenant.Principal, root nodeJSON) {
	t.Helper()
	member, root = useList6000Fixture(t)
	project, ticket := kindBySlug(t, member, "project"), kindBySlug(t, member, "work")
	other := mustNode(t, member, `{"kind_id":"`+project.ID+`","title":"Other project"}`)
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, member.TenantID, func(tx pgx.Tx) error {
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
	guest = tenant.Principal{TenantID: member.TenantID, Kind: tenant.Person, Name: "Guest"}
	if err := testDB.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Guest') RETURNING id::text`, member.TenantID).Scan(&guest.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := testDB.Admin.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='guest'`, member.TenantID, guest.ID, root.ID); err != nil {
		t.Fatal(err)
	}
	return member, guest, root
}

func visibilityListPath(rootID string, facets bool) string {
	path := "/api/nodes?within=" + rootID + "&sort=state,-updated_at&limit=50"
	if facets {
		path += "&facets=state,kind,priority,assignee"
	}
	return path
}

// One discarded call, then the fastest of n. Correctness is checked on every
// call, including the warmup, so a wrong page cannot win the timer.
func timeVisibilityList(t *testing.T, p *tenant.Principal, path string, facets bool) time.Duration {
	t.Helper()
	once := func() time.Duration {
		t.Helper()
		start := time.Now()
		status, body := call(t, p, http.MethodGet, path, "")
		elapsed := time.Since(start)
		page := decode[nodePage](t, status, body, http.StatusOK)
		if len(page.Items) != 50 || page.NextCursor == nil {
			t.Fatalf("large list: %d items", len(page.Items))
		}
		if facets && page.Facets["kind"]["work"] != 6000 {
			t.Fatalf("large list facets: %#v", page.Facets)
		}
		return elapsed
	}
	once()
	var fastest time.Duration
	for i := 0; i < visibilitySamples; i++ {
		elapsed := once()
		if fastest == 0 || elapsed < fastest {
			fastest = elapsed
		}
	}
	return fastest
}
