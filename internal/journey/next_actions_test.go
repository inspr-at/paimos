// SPDX-License-Identifier: AGPL-3.0-only
package journey_test

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/journey"
	"github.com/jackc/pgx/v5"
)

func TestBriefingNextActionsMatchJourneyRegression(t *testing.T) {
	f := newFixture(t)
	ids := []string{}
	if _, err := f.db.Admin.Exec(t.Context(), `INSERT INTO events(tenant_id,id,actor_principal_id,type,after) VALUES($1,1,$2,'test.fixture','{}')`, f.tenant, f.person.ID); err != nil {
		t.Fatal(err)
	}
	for i, state := range []string{"new", "accepted", "planning", "building", "candidate", "deploying", "refused", "access", "released", "imported"} {
		project := f.node(t, "project", "BAT-"+strconv.Itoa(100+i), state)
		ids = append(ids, project)
		if state == "new" {
			continue
		}
		if state == "accepted" {
			if _, err := f.db.Admin.Exec(t.Context(), `INSERT INTO journey_projects(tenant_id,project_node_id) VALUES($1,$2)`, f.tenant, project); err != nil {
				t.Fatal(err)
			}
			f.acceptBrief(t, project)
			continue
		}
		if state == "imported" {
			f.node(t, "ticket", "BAT-999", "Imported work")
			if _, err := f.db.Admin.Exec(t.Context(), `UPDATE nodes SET parent_id=$1 WHERE key='BAT-999' AND tenant_id=$2`, project, f.tenant); err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.Admin.Exec(t.Context(), `INSERT INTO events(tenant_id,id,actor_principal_id,node_id,type,after) VALUES($1,900,$2,$3,'import.node_created','{}')`, f.tenant, f.person.ID, project); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenant, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `INSERT INTO journey_projects(tenant_id,project_node_id,brief_confirmed_at,decision,requirements_revision,agreed_requirements_revision,agreed_requirements_digest_sha256) VALUES($1,$2,now(),'go',1,1,$3)`, f.tenant, project, digest)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		release := f.release(t, project, "BAT-"+strconv.Itoa(200+i), 1)
		f.ticket(t, project, release, "BAT-"+strconv.Itoa(300+i), "1.25", false, false)
		f.setTicketState(t, "BAT-"+strconv.Itoa(300+i), "done")
		if state == "released" {
			if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenant, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE journey_releases SET state='released',released_at=now() WHERE release_node_id=$1`, release)
				return err
			}); err != nil {
				t.Fatal(err)
			}
		} else if state != "planning" {
			f.setReleaseState(t, release, state)
		}
		for _, scope := range []string{journey.ScopeBuild, journey.ScopeCandidate, journey.ScopeDeploy, journey.ScopeAccess} {
			approval := f.grant(t, f.agent.ID, f.person.ID, scope, release)
			if scope == journey.ScopeCandidate && (state == "deploying" || state == "refused") {
				if _, err := f.db.Admin.Exec(t.Context(), `INSERT INTO journey_gates(tenant_id,project_node_id,release_node_id,gate,approval_request_id) VALUES($1,$2,$3,'candidate',$4)`, f.tenant, project, release, approval); err != nil {
					t.Fatal(err)
				}
				if state == "refused" {
					f.revoke(t, approval)
				}
			}
		}
		if state == "deploying" {
			f.handoff(t, project, release, "deploy", "deploy", 1, "succeeded", "")
			f.handoff(t, project, release, "deploy", "verify", 1, "succeeded", "")
		}
	}
	want := map[string]journey.JourneyNextAction{}
	for _, id := range ids {
		want[id] = f.journey(t, f.person, http.MethodGet, "/api/projects/"+id+"/journey", "").NextAction
	}
	beforeEvents, beforeProjects := f.eventCount(t), f.journeyCount(t, f.tenant)
	w := f.do(f.person, http.MethodGet, "/api/journey/next-actions?project_ids="+strings.Join(ids, ","), "")
	if w.Code != 200 {
		t.Fatalf("batch %d: %s", w.Code, w.Body.String())
	}
	var page struct {
		Items []struct {
			Project string                    `json:"project_node_id"`
			Next    journey.JourneyNextAction `json:"next_action"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != len(ids) {
		t.Fatalf("missing projects: %s", w.Body.String())
	}
	for _, item := range page.Items {
		actual, _ := json.Marshal(item.Next)
		expected, _ := json.Marshal(want[item.Project])
		if string(actual) != string(expected) {
			t.Fatalf("action differs for %s: %s vs %s", item.Project, actual, expected)
		}
	}
	if f.eventCount(t) != beforeEvents || f.journeyCount(t, f.tenant) != beforeProjects {
		t.Fatal("snapshot changed journey state")
	}
	if w := f.do(f.outsider, http.MethodGet, "/api/journey/next-actions?project_ids="+strings.Join(ids, ","), ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Fatalf("foreign projects leaked: %d %s", w.Code, w.Body.String())
	}
	if w := f.do(f.agent, http.MethodGet, "/api/journey/next-actions?project_ids="+ids[0], ""); w.Code != 403 {
		t.Fatalf("agent admitted: %d", w.Code)
	}
	for _, query := range []string{"bad", ids[0] + "," + ids[0], strings.Join(append(ids, strings.Split(strings.Repeat(ids[0]+",", 100), ",")...), ",")} {
		if w := f.do(f.person, http.MethodGet, "/api/journey/next-actions?project_ids="+query, ""); w.Code != 400 {
			t.Fatalf("invalid list admitted: %d", w.Code)
		}
	}
}
