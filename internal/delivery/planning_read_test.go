// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/releasehistory/codename"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func planningOpt(t *testing.T, query string) ReadOptions {
	t.Helper()
	v, e := url.ParseQuery("view=planning&" + query)
	if e != nil {
		t.Fatal(e)
	}
	o := ReadOptions{}
	if e = ParsePlanningOptions(v, &o); e != nil {
		t.Fatal(e)
	}
	return o
}
func TestPlanningSearchBeforePagesHideAndQueryIdentity(t *testing.T) {
	f := newStoreFixture(t)
	f.run(t, func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `WITH fresh AS (INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id,state) SELECT $1,k.id,'PL-'||i,'Unloaded item '||i,$2,'open' FROM generate_series(1,205) i CROSS JOIN node_kinds k WHERE k.tenant_id=$1 AND k.slug='ticket' RETURNING id,key) INSERT INTO ships_in(tenant_id,project_node_id,item_node_id,release_node_id,rank,source,placed_by) SELECT $1,$2,id,$3,'z'||lpad(split_part(key,'-',2),3,'0')||'V','person',$4 FROM fresh`, f.tenant, f.project, f.release, f.actor)
		return e
	})
	hidden := f.item(t, "ticket", "PL-206", "done", f.release, "zz")
	f.exec(t, `UPDATE nodes SET title='Hidden needle' WHERE id=$1`, hidden)
	opt := planningOpt(t, "q=needle")
	page, e := f.store.Items(t.Context(), f.person, f.project, f.release, opt)
	if e != nil {
		t.Fatal(e)
	}
	if len(page.Items) != 0 || page.Matches.Matched != 1 || page.Matches.Hidden != 1 || page.Matches.Finished != 1 {
		t.Fatalf("hidden hit: %+v", page)
	}
	releases, e := f.store.ListReleases(t.Context(), f.person, f.project, opt)
	if e != nil {
		t.Fatal(e)
	}
	if len(releases.Items) != 1 || releases.Items[0].ID != f.release || releases.Items[0].Matches.Hidden != 1 || releases.Items[0].Rollup.Units != 206 {
		t.Fatalf("release eligibility/rollup: %+v", releases)
	}
	opt = planningOpt(t, "q=Unloaded+item+205&hide_closed=false")
	page, e = f.store.Items(t.Context(), f.person, f.project, f.release, opt)
	if e != nil || len(page.Items) != 1 || page.Items[0].Key != "PL-205" {
		t.Fatalf("beyond page hit: %+v %v", page, e)
	}
	opt = planningOpt(t, "hide_closed=false")
	opt.Limit = 1
	page, e = f.store.Items(t.Context(), f.person, f.project, f.release, opt)
	if e != nil || page.NextCursor == "" {
		t.Fatalf("page: %+v %v", page, e)
	}
	opt.Cursor = page.NextCursor
	opt.Work.Q = "changed"
	if _, e = f.store.Items(t.Context(), f.person, f.project, f.release, opt); !errors.Is(e, ErrInvalidInput) {
		t.Fatalf("wrong query cursor reason: %v", e)
	}
	f.exec(t, `UPDATE nodes SET title='Name only match' WHERE id=$1`, f.release)
	opt = planningOpt(t, "q=Name+only+match")
	releases, e = f.store.ListReleases(t.Context(), f.person, f.project, opt)
	if e != nil || len(releases.Items) != 1 || releases.Items[0].Matches.Matched != 0 {
		t.Fatalf("name-only: %+v %v", releases, e)
	}
}
func TestPlanningBacklogStatusesIntersectionsAndOverview(t *testing.T) {
	f := newStoreFixture(t)
	ranked := f.item(t, "ticket", "PL-1", "cancelled", "", "V")
	tail := f.item(t, "task", "PL-2", "done", "", "")
	f.exec(t, `UPDATE nodes SET title='Needle work',fields='{"priority":"high"}' WHERE id=ANY($1::uuid[])`, []string{ranked, tail})
	for _, part := range []string{"ranked", "tail"} {
		opt := planningOpt(t, "hide_closed=false&q=needle&kind=!epic&priority=high&work_state=!open")
		opt.Part = part
		page, e := f.store.Items(t.Context(), f.person, f.project, "", opt)
		if e != nil || len(page.Items) != 1 {
			t.Fatalf("%s %+v %v", part, page, e)
		}
		opt.HideClosed = true
		page, e = f.store.Items(t.Context(), f.person, f.project, "", opt)
		if e != nil || len(page.Items) != 0 || page.Matches.Hidden != 1 {
			t.Fatalf("hidden %s %+v %v", part, page, e)
		}
	}
	overview, e := f.store.OverviewQuery(t.Context(), f.person, f.project, planningOpt(t, "q=needle"))
	if e != nil || overview.BacklogMatches["ranked"].Hidden != 1 || overview.BacklogMatches["tail"].Hidden != 1 {
		t.Fatalf("overview: %+v %v", overview, e)
	}
	legacy, e := f.store.Items(t.Context(), f.person, f.project, "", ReadOptions{Part: "tail"})
	if e != nil || len(legacy.Items) != 0 {
		t.Fatalf("legacy defaults changed: %+v %v", legacy, e)
	}
	opt := planningOpt(t, "hide_state=unknown_state")
	if _, e = f.store.Items(t.Context(), f.person, f.project, "", opt); !errors.Is(e, ErrInvalidInput) {
		t.Fatalf("taxonomy: %v", e)
	}
}
func TestPlanningReleasedNameBeyondFirstPage(t *testing.T) {
	f := newStoreFixture(t)
	f.run(t, func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `WITH fresh AS (INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id) SELECT $1,k.id,'HP-'||i,CASE WHEN i=51 THEN 'Needle historic' ELSE 'Other historic '||i END,$2 FROM generate_series(1,51) i CROSS JOIN node_kinds k WHERE k.tenant_id=$1 AND k.slug='release' RETURNING id,key) INSERT INTO project_releases(tenant_id,project_node_id,release_node_id,visibility,state,rank,origin,released_at) SELECT $1,$2,id,'internal','released','zz'||lpad(split_part(key,'-',2),3,'0')||'V','adopted_released','2026-01-01'::timestamptz-split_part(key,'-',2)::int*interval '1 day' FROM fresh`, f.tenant, f.project)
		return e
	})
	opt := planningOpt(t, "q=needle")
	opt.State = "released"
	page, e := f.store.ListReleases(t.Context(), f.person, f.project, opt)
	if e != nil || len(page.Items) != 1 || page.Items[0].Title != "Needle historic" {
		t.Fatalf("late name %+v %v", page, e)
	}
}
func TestPlanningInputBoundsAndRecoveryRefusal(t *testing.T) {
	for _, raw := range []string{"q=" + strings.Repeat("ä", 101), "kind=" + strings.Repeat("ticket,", 100) + "ticket", "hide_state=!done", "view=unknown", "hide_closed=maybe"} {
		v, _ := url.ParseQuery("view=planning&" + raw)
		o := ReadOptions{}
		if e := ParsePlanningOptions(v, &o); !errors.Is(e, ErrInvalidInput) {
			t.Errorf("%s: %v", fmt.Sprintf("%d bytes", len(raw)), e)
		}
	}
	o := ReadOptions{CompletedLater: true}
	v, _ := url.ParseQuery("view=planning")
	if e := ParsePlanningOptions(v, &o); !errors.Is(e, ErrInvalidInput) {
		t.Fatal(e)
	}
}

func TestPlanningProductNameUsesExplicitProductBinding(t *testing.T) {
	f := newStoreFixture(t)
	f.store = f.store.WithProductProject(f.tenant, f.project)
	name := codename.Codename(1)
	opt := planningOpt(t, "q="+url.QueryEscape(strings.ToLower(name)))
	page, err := f.store.ListReleases(t.Context(), f.person, f.project, opt)
	if err != nil || len(page.Items) != 1 || page.Items[0].DisplayName != name {
		t.Fatalf("marketing name %+v %v", page, err)
	}
	unbound := NewStore(f.d.App)
	page, err = unbound.ListReleases(t.Context(), f.person, f.project, opt)
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("unbound product guessed %+v %v", page, err)
	}
}

func TestPlanningCappedCountsAndScopedDenial(t *testing.T) {
	f := newStoreFixture(t)
	f.run(t, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id,state)
		 SELECT $1,k.id,'CAP-'||i,'Counted work',$2,'done' FROM generate_series(1,10002) i
		 CROSS JOIN node_kinds k WHERE k.tenant_id=$1 AND k.slug='ticket'`, f.tenant, f.project)
		return err
	})
	opt := planningOpt(t, "q=Counted+work")
	opt.Part = "tail"
	page, err := f.store.Items(t.Context(), f.person, f.project, "", opt)
	if err != nil || !page.Incomplete || page.Matches == nil || !page.Matches.Incomplete || page.Matches.Matched != 10001 || page.Matches.Hidden != 10001 || len(page.Items) != 0 {
		t.Fatalf("capped hidden population must be honest: %+v %v", page, err)
	}
	if _, err = f.store.Items(t.Context(), f.person, f.other, f.release, opt); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-project release scope: %v", err)
	}
	outsider := tenant.Principal{ID: "11111111-1111-4111-8111-111111111111", TenantID: f.tenant, Kind: tenant.Person}
	f.exec(t, `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'person','No project access')`, f.tenant, outsider.ID)
	if _, err = f.store.Items(t.Context(), outsider, f.project, "", opt); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("unauthorized scope: %v", err)
	}
}

func TestPlanningAssigneeAndEpicIntersectionsUseSharedPredicates(t *testing.T) {
	f := newStoreFixture(t)
	ep := f.item(t, "epic", "FILTER-1", "open", f.release, "V")
	child := f.item(t, "ticket", "FILTER-2", "open", f.release, "W")
	unassigned := f.item(t, "ticket", "FILTER-3", "open", f.release, "X")
	f.exec(t, `UPDATE nodes SET title='Authorized epic context' WHERE id=$1`, ep)
	f.exec(t, `UPDATE nodes SET parent_id=$2,fields=jsonb_build_object('assignee_id',$3::text) WHERE id=$1`, child, ep, f.actor)
	f.exec(t, `UPDATE nodes SET title='Authorized epic context' WHERE id=$1`, unassigned)
	for _, query := range []string{
		"q=Authorized+epic+context&kind=ticket&assignee=" + f.actor + "&work_state=open",
		"q=Authorized+epic+context&kind=!epic&assignee=!none&epic=" + ep,
	} {
		page, err := f.store.Items(t.Context(), f.person, f.project, f.release, planningOpt(t, query))
		if err != nil || len(page.Items) != 1 || page.Items[0].ItemID != child {
			t.Fatalf("intersection %s: %+v %v", query, page, err)
		}
	}
}

func TestPlanningCustomHideDoesNotMislabelOpenWorkAsExit(t *testing.T) {
	f := newStoreFixture(t)
	f.item(t, "ticket", "HIDE-1", "open", f.release, "V")
	page, err := f.store.Items(t.Context(), f.person, f.project, f.release, planningOpt(t, "hide_state=open"))
	if err != nil || page.Matches.Hidden != 1 || page.Matches.Other != 1 || page.Matches.Exit != 0 || page.Matches.Finished != 0 {
		t.Fatalf("custom Hide categories: %+v %v", page, err)
	}
}
