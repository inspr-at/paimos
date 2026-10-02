// SPDX-License-Identifier: AGPL-3.0-only
package journey_test

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/journey"
	"github.com/jackc/pgx/v5"
)

func briefingNextAction(t *testing.T, f *fixture, project string) journey.JourneyNextAction {
	t.Helper()
	w := f.do(f.person, http.MethodGet, "/api/journey/next-actions?project_ids="+project, "")
	if w.Code != http.StatusOK {
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
	if len(page.Items) != 1 || page.Items[0].Project != project {
		t.Fatalf("wrong batch membership: %s", w.Body.String())
	}
	return page.Items[0].Next
}

func assertBriefingParity(t *testing.T, f *fixture, project, key string, available bool) journey.Journey {
	t.Helper()
	beforeEvents := f.eventCount(t)
	view := f.journey(t, f.person, http.MethodGet, "/api/projects/"+project+"/journey", "")
	if view.NextAction.Key != key || view.NextAction.Available != available {
		t.Fatalf("fixture does not exercise %s available=%t: %+v", key, available, view.NextAction)
	}
	next := briefingNextAction(t, f, project)
	if !reflect.DeepEqual(next, view.NextAction) {
		t.Fatalf("briefing action %+v differs from journey %+v", next, view.NextAction)
	}
	if f.eventCount(t) != beforeEvents {
		t.Fatal("next-action reads wrote events")
	}
	return view
}

func TestBriefingNextActionsIgnoreDeletedMembersAndRestoreThem(t *testing.T) {
	for _, flag := range []string{"scope", "missing-estimate", "plan-cap", "access-change"} {
		t.Run(flag, func(t *testing.T) {
			f := newFixture(t)
			project := f.node(t, "project", "PRJ-588", "Live membership")
			profile := "personal"
			if flag == "missing-estimate" || flag == "plan-cap" {
				profile = "professional"
			}
			renewalChange(t, f, `INSERT INTO journey_projects(tenant_id,project_node_id,profile,brief_confirmed_at,decision,requirements_revision,agreed_requirements_revision,agreed_requirements_digest_sha256) VALUES($1,$2,$3,now(),'go',1,1,$4)`, f.tenant, project, profile, digest)
			release := f.release(t, project, "REL-588", 1)
			f.ticket(t, project, release, "TKT-588", "2", false, false)
			f.ticket(t, project, release, "TKT-589", "20", flag == "scope", flag == "access-change")
			if flag == "missing-estimate" {
				renewalChange(t, f, `UPDATE journey_tickets SET estimated_hours=NULL WHERE ticket_node_id=(SELECT id FROM nodes WHERE tenant_id=$1 AND key='TKT-589')`, f.tenant)
			}
			if profile == "professional" {
				renewalChange(t, f, `INSERT INTO events(tenant_id,id,actor_principal_id,node_id,type,after) VALUES($1,1,$2,$3,'journey.decided','{"decision":"go","approved_cap_hours":"5"}')`, f.tenant, f.person.ID, project)
			}
			f.grant(t, f.agent.ID, f.person.ID, journey.ScopeBuild, release)
			liveKey, liveAvailable, deletedKey := "start_build", false, "start_build"
			if flag == "scope" {
				liveKey = "approve_requirements"
			}
			if flag == "access-change" {
				f.setReleaseState(t, release, "deploying")
				gate := f.grant(t, f.agent.ID, f.person.ID, journey.ScopeCandidate, release)
				renewalChange(t, f, `INSERT INTO journey_gates(tenant_id,project_node_id,release_node_id,gate,approval_request_id) VALUES($1,$2,$3,'candidate',$4)`, f.tenant, project, release, gate)
				f.handoff(t, project, release, "deploy", "deploy", 1, "succeeded", "")
				f.handoff(t, project, release, "deploy", "verify", 1, "succeeded", "")
				liveKey, deletedKey = "approve_permit", "plan_next_release"
			}
			original := assertBriefingParity(t, f, project, liveKey, liveAvailable)
			renewalChange(t, f, `UPDATE nodes SET deleted_at=now() WHERE tenant_id=$1 AND key='TKT-589'`, f.tenant)
			deleted := assertBriefingParity(t, f, project, deletedKey, true)
			if deleted.Revision != original.Revision {
				t.Fatal("read changed the project revision")
			}
			renewalChange(t, f, `UPDATE nodes SET deleted_at=NULL WHERE tenant_id=$1 AND key='TKT-589'`, f.tenant)
			restored := assertBriefingParity(t, f, project, liveKey, liveAvailable)
			if !reflect.DeepEqual(restored.NextAction, original.NextAction) {
				t.Fatal("restored ticket did not restore its action inputs")
			}
		})
	}
}

func TestBriefingNextActionsFencePermitRenewalByRevisionAndWindow(t *testing.T) {
	for _, fence := range []string{"revision", "window"} {
		t.Run(fence, func(t *testing.T) {
			f := newFixture(t)
			project, release, _, _, _ := renewalRelease(t, f)
			f.setReleaseState(t, release, "access")
			old := f.grant(t, f.agent.ID, f.person.ID, journey.ScopeAccess, release)
			renewalChange(t, f, `INSERT INTO journey_gates(tenant_id,project_node_id,release_node_id,gate,approval_request_id) VALUES($1,$2,$3,'access',$4)`, f.tenant, project, release, old)
			f.expire(t, old)
			fresh := f.grant(t, f.agent.ID, f.person.ID, journey.ScopeAccess, release)
			path := "/api/projects/" + project + "/journey"
			view := assertBriefingParity(t, f, project, "approve_permit", true)
			view = f.journey(t, f.person, http.MethodPost, path+"/actions", actionJSON("renew_permit", view.Revision, "renew", fresh, release, ""))
			// Evidence is retained. Place its creation clock explicitly on either
			// side of renewal so each fence is proved independently, without sleeps.
			f.accessApply(t, project, release, true)
			renewalChange(t, f, `UPDATE stage_handoffs SET journey_revision=$2,
				created_at=(SELECT at FROM events WHERE node_id=$3 AND type='journey.permit_renewed')+interval '1 hour'
				WHERE release_node_id=$1 AND stage='access'`, release, view.Revision-1, project)
			if fence == "window" {
				renewalChange(t, f, `UPDATE stage_handoffs SET journey_revision=$2,
					created_at=(SELECT at FROM events WHERE node_id=$3 AND type='journey.permit_renewed')-interval '1 hour'
					WHERE release_node_id=$1 AND stage='access'`, release, view.Revision, project)
			}
			assertBriefingParity(t, f, project, "approve_permit", false)
			var retained int
			if err := f.db.Admin.QueryRow(t.Context(), `SELECT count(*) FROM stage_handoffs h JOIN stage_handoff_results r ON r.handoff_id=h.id AND r.tenant_id=h.tenant_id WHERE h.release_node_id=$1 AND h.stage='access' AND r.outcome='succeeded'`, release).Scan(&retained); err != nil || retained != 1 {
				t.Fatalf("fixture lost the stale success: count=%d error=%v", retained, err)
			}
			renewalChange(t, f, `UPDATE stage_handoffs SET journey_revision=$2,
				created_at=(SELECT at FROM events WHERE node_id=$3 AND type='journey.permit_renewed')+interval '1 hour'
				WHERE release_node_id=$1 AND stage='access'`, release, view.Revision, project)
			assertBriefingParity(t, f, project, "plan_next_release", true)
		})
	}
}

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
