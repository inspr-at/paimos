// SPDX-License-Identifier: AGPL-3.0-only

package journey_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

func TestOpenFirstReleaseWithFiveExistingTicketsAtomic(t *testing.T) {
	f := newFixture(t)
	project := f.node(t, "project", "PRJ-227", "Membership project")
	if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenant, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO journey_projects(tenant_id,project_node_id,brief_confirmed_at,decision,requirements_revision,agreed_requirements_revision,agreed_requirements_digest_sha256) VALUES($1,$2,now(),'go',1,1,$3)`, f.tenant, project, digest)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 5)
	for i := range ids {
		if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenant, func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title,parent_id) SELECT $1,aeon_next_node_key($1,short_prefix),id,$3,$2 FROM node_kinds WHERE tenant_id=$1 AND slug='ticket' RETURNING nodes.id::text`, f.tenant, project, fmt.Sprintf("Existing %d", i+1)).Scan(&ids[i])
		}); err != nil {
			t.Fatal(err)
		}
	}
	view := f.journey(t, f.person, http.MethodGet, "/api/projects/"+project+"/journey", "")
	if view.NextAction.Key != "open_first_release" {
		t.Fatalf("next action %+v", view.NextAction)
	}
	payload := map[string]any{"action": "open_first_release", "expected_revision": view.Revision, "idempotency_key": "bulk-new", "ticket_node_ids": ids}
	body, _ := json.Marshal(payload)
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE nodes SET state='accepted' WHERE id=$1`, ids[4]); err != nil {
		t.Fatal(err)
	}
	if w := f.do(f.person, http.MethodPost, "/api/projects/"+project+"/journey/actions", string(body)); w.Code != 409 {
		t.Fatalf("closed ticket %d %s", w.Code, w.Body.String())
	}
	if f.releaseCount(t, project) != 0 {
		t.Fatal("rejected ticket left a release")
	}
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE nodes SET state='open' WHERE id=$1`, ids[4]); err != nil {
		t.Fatal(err)
	}
	opened := f.journey(t, f.person, http.MethodPost, "/api/projects/"+project+"/journey/actions", string(body))
	if opened.CurrentReleaseID == nil || opened.Stage != "requirements" {
		t.Fatalf("opened %+v", opened)
	}
	var count, scope int
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT count(*),count(*) FILTER(WHERE scope_revision_required) FROM journey_tickets WHERE project_node_id=$1 AND release_node_id=$2`, project, *opened.CurrentReleaseID).Scan(&count, &scope); err != nil {
		t.Fatal(err)
	}
	if count != 5 || scope != 5 {
		t.Fatalf("selected=%d scope=%d", count, scope)
	}
	replay := f.journey(t, f.person, http.MethodPost, "/api/projects/"+project+"/journey/actions", string(body))
	if replay.CurrentReleaseID == nil || *replay.CurrentReleaseID != *opened.CurrentReleaseID || f.releaseCount(t, project) != 1 {
		t.Fatal("replay created another release")
	}
}

func TestPlanNextReleaseWithExistingTickets(t *testing.T) {
	f := newFixture(t)
	project := f.node(t, "project", "PRJ-228", "Next release project")
	prior := f.node(t, "release", "REL-228", "Release 1")
	if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenant, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO journey_projects(tenant_id,project_node_id,brief_confirmed_at,decision,requirements_revision,agreed_requirements_revision,agreed_requirements_digest_sha256,current_release_node_id) VALUES($1,$2,now(),'go',1,1,$4,$3)`, f.tenant, project, prior, digest); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO journey_releases(tenant_id,release_node_id,project_node_id,number,state,released_at) VALUES($1,$2,$3,1,'released',now())`, f.tenant, prior, project)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 5)
	if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenant, func(tx pgx.Tx) error {
		for i := range ids {
			if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title,parent_id) SELECT $1,aeon_next_node_key($1,short_prefix),id,$3,$2 FROM node_kinds WHERE tenant_id=$1 AND slug='ticket' RETURNING nodes.id::text`, f.tenant, project, fmt.Sprintf("Next %d", i+1)).Scan(&ids[i]); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	view := f.journey(t, f.person, http.MethodGet, "/api/projects/"+project+"/journey", "")
	if view.NextAction.Key != "plan_next_release" {
		t.Fatalf("next action %+v", view.NextAction)
	}
	payload := map[string]any{"action": "plan_next_release", "expected_revision": view.Revision, "idempotency_key": "next-with-five", "release_id": prior, "ticket_node_ids": ids}
	body, _ := json.Marshal(payload)
	next := f.journey(t, f.person, http.MethodPost, "/api/projects/"+project+"/journey/actions", string(body))
	if next.CurrentReleaseID == nil || *next.CurrentReleaseID == prior {
		t.Fatalf("next release %+v", next)
	}
	var number, count int
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT r.number,(SELECT count(*) FROM journey_tickets WHERE release_node_id=r.release_node_id) FROM journey_releases r WHERE r.release_node_id=$1`, *next.CurrentReleaseID).Scan(&number, &count); err != nil {
		t.Fatal(err)
	}
	if number != 2 || count != 5 {
		t.Fatalf("number=%d selected=%d", number, count)
	}
}
