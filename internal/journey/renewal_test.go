// SPDX-License-Identifier: AGPL-3.0-only
package journey_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/journey"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func renewalChange(t *testing.T, f *fixture, query string, args ...any) {
	t.Helper()
	if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenant, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), query, args...)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func renewalRelease(t *testing.T, f *fixture) (project, release, candidate, deploy string, view journey.Journey) {
	t.Helper()
	project = f.node(t, "project", "PRJ-234", "Renewal")
	renewalChange(t, f, `INSERT INTO journey_projects(tenant_id,project_node_id,brief_confirmed_at,requirements_revision,agreed_requirements_revision,agreed_requirements_digest_sha256) VALUES($1::uuid,$2::uuid,now(),1,1,$3)`, f.tenant, project, digest)
	release = f.release(t, project, "REL-234", 1)
	f.setReleaseState(t, release, "candidate")
	candidate = f.grant(t, f.agent.ID, f.person.ID, journey.ScopeCandidate, release)
	view = f.journey(t, f.person, "GET", "/api/projects/"+project+"/journey", "")
	view = f.journey(t, f.person, "POST", "/api/projects/"+project+"/journey/actions", actionJSON("approve_candidate", view.Revision, "candidate", candidate, release, ""))
	deploy = f.grant(t, f.agent.ID, f.person.ID, journey.ScopeDeploy, release)
	view = f.journey(t, f.person, "POST", "/api/projects/"+project+"/journey/actions", actionJSON("approve_deploy", view.Revision, "deploy", deploy, release, ""))
	return
}

func approvalRecord(t *testing.T, f *fixture, id string) string {
	t.Helper()
	var record string
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT json_build_array(to_jsonb(r),to_jsonb(d),to_jsonb(g))::text
		FROM approval_requests r JOIN approval_decisions d ON d.request_id=r.id
		JOIN agent_permission_grants g ON g.approval_request_id=r.id WHERE r.id=$1::uuid`, id).Scan(&record); err != nil {
		t.Fatal(err)
	}
	return record
}

func TestJourneyStandingGateRenewalFences(t *testing.T) {
	for _, gate := range []string{"candidate", "deploy"} {
		t.Run(gate, func(t *testing.T) {
			f := newFixture(t)
			project, release, candidate, deploy, view := renewalRelease(t, f)
			path := "/api/projects/" + project + "/journey"
			old := deploy
			if gate == "candidate" {
				old = candidate
			}
			f.expire(t, old)
			fresh := f.grant(t, f.agent.ID, f.person.ID, "journey."+gate, release)
			wrongScope := f.grant(t, f.agent.ID, f.person.ID, journey.ScopeAccess, release)
			wrongResource := f.grant(t, f.agent.ID, f.person.ID, "journey."+gate, project)
			expired := f.grant(t, f.agent.ID, f.person.ID, "journey."+gate, release)
			f.expire(t, expired)
			revoked := f.grant(t, f.agent.ID, f.person.ID, "journey."+gate, release)
			f.revoke(t, revoked)
			missingGrant := f.grant(t, f.agent.ID, f.person.ID, "journey."+gate, release)
			renewalChange(t, f, `DELETE FROM agent_permission_grants WHERE approval_request_id=$1::uuid`, missingGrant)
			var pending, denied string
			for _, target := range []*string{&pending, &denied} {
				if err := f.db.Admin.QueryRow(t.Context(), `INSERT INTO approval_requests(tenant_id,proposed_by_principal_id,agent_principal_id,scope,resource_kind,resource_id,rationale,expires_at) VALUES($1::uuid,$2::uuid,$2::uuid,$3,'node',$4::uuid,'renewal fixture',now()+interval '1 hour') RETURNING id::text`, f.tenant, f.agent.ID, "journey."+gate, release).Scan(target); err != nil {
					t.Fatal(err)
				}
			}
			renewalChange(t, f, `INSERT INTO approval_decisions(tenant_id,request_id,decided_by_principal_id,decision) VALUES($1::uuid,$2::uuid,$3::uuid,'denied')`, f.tenant, denied, f.person.ID)
			for _, tc := range []struct {
				name                      string
				actor                     tenant.Principal
				action, approval, release string
				revision                  int64
				status                    int
			}{
				{"agent", f.agent, "renew_" + gate, fresh, release, view.Revision, 403},
				{"other decider", f.other, "renew_" + gate, fresh, release, view.Revision, 403},
				{"tenant", f.outsider, "renew_" + gate, fresh, release, view.Revision, 404},
				{"stale", f.person, "renew_" + gate, fresh, release, view.Revision - 1, 409},
				{"release required", f.person, "renew_" + gate, fresh, "", view.Revision, 400},
				{"approval required", f.person, "renew_" + gate, "", release, view.Revision, 400},
				{"wrong release", f.person, "renew_" + gate, fresh, project, view.Revision, 409},
				{"old client", f.person, "approve_" + gate, fresh, release, view.Revision, 409},
				{"wrong scope", f.person, "renew_" + gate, wrongScope, release, view.Revision, 403},
				{"wrong resource", f.person, "renew_" + gate, wrongResource, release, view.Revision, 403},
				{"expired", f.person, "renew_" + gate, expired, release, view.Revision, 403},
				{"revoked", f.person, "renew_" + gate, revoked, release, view.Revision, 403},
				{"missing grant", f.person, "renew_" + gate, missingGrant, release, view.Revision, 403},
				{"pending", f.person, "renew_" + gate, pending, release, view.Revision, 403},
				{"denied", f.person, "renew_" + gate, denied, release, view.Revision, 403},
				{"consumed", f.person, "renew_" + gate, old, release, view.Revision, 409},
			} {
				t.Run(tc.name, func(t *testing.T) {
					w := f.do(tc.actor, "POST", path+"/actions", actionJSON(tc.action, tc.revision, "bad-"+tc.name, tc.approval, tc.release, ""))
					if w.Code != tc.status {
						t.Fatalf("status %d want %d: %s", w.Code, tc.status, w.Body.String())
					}
				})
			}
			view = f.journey(t, f.person, "GET", path, "")
			if view.NextAction.Key != "approve_"+gate || view.NextAction.RenewalAction != "renew_"+gate || !view.NextAction.Available || testPtr(view.NextAction.ApprovalRequestID) != fresh {
				t.Fatalf("renewal projection: %+v", view.NextAction)
			}
			beforeEvents := f.eventCount(t)
			oldRecord := approvalRecord(t, f, old)
			freshRecord := approvalRecord(t, f, fresh)
			payload := actionJSON("renew_"+gate, view.Revision, "renew", fresh, release, "")
			renewed := f.journey(t, f.person, "POST", path+"/actions", payload)
			if renewed.Revision != view.Revision+1 || renewed.Stage != "deploy" || testPtr(renewed.CurrentReleaseID) != release || f.releaseState(t, release) != "deploying" {
				t.Fatalf("renewal changed release/stage: %+v", renewed)
			}
			if f.gateCount(t, project, gate) != 2 || f.eventCount(t) != beforeEvents+1 || f.events(t, "journey."+gate+"_renewed") != 1 {
				t.Fatal("renewal history missing")
			}
			if approvalRecord(t, f, old) != oldRecord || approvalRecord(t, f, fresh) != freshRecord {
				t.Fatal("renewal mutated an approval, decision, or grant")
			}
			replay := f.journey(t, f.person, "POST", path+"/actions", payload)
			if replay.Revision != renewed.Revision || f.eventCount(t) != beforeEvents+1 {
				t.Fatal("replay mutated history")
			}
			if w := f.do(f.other, "POST", path+"/actions", payload); w.Code != 403 {
				t.Fatalf("replay decider: %d", w.Code)
			}
			if w := f.do(f.person, "POST", path+"/actions", actionJSON("renew_"+gate, renewed.Revision, "renew", fresh, release, "")); w.Code != 409 {
				t.Fatalf("conflicting replay: %d", w.Code)
			}
			if w := f.do(f.person, "POST", path+"/actions", actionJSON("renew_"+gate, renewed.Revision, "already-live", fresh, release, "")); w.Code != 409 {
				t.Fatalf("live gate renewed: %d", w.Code)
			}
			f.revoke(t, fresh)
			if w := f.do(f.person, "POST", path+"/actions", actionJSON("renew_"+gate, renewed.Revision, "used-again", fresh, release, "")); w.Code != 409 {
				t.Fatalf("consumed request reused: %d", w.Code)
			}
		})
	}
}

func TestJourneyBothGatesRenewInOrderAndKeepEvidenceHistorical(t *testing.T) {
	f := newFixture(t)
	project, release, candidate, deploy, view := renewalRelease(t, f)
	path := "/api/projects/" + project + "/journey"
	f.expire(t, candidate)
	f.revoke(t, deploy)
	f.handoff(t, project, release, "deploy", "deploy", 1, "succeeded", "")
	freshCandidate := f.grant(t, f.agent.ID, f.person.ID, journey.ScopeCandidate, release)
	freshDeploy := f.grant(t, f.agent.ID, f.person.ID, journey.ScopeDeploy, release)
	if w := f.do(f.person, "POST", path+"/actions", actionJSON("renew_deploy", view.Revision, "out-of-order", freshDeploy, release, "")); w.Code != 409 {
		t.Fatalf("renewal order: %d", w.Code)
	}
	var before string
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT row_to_json(r)::text FROM journey_releases r WHERE release_node_id=$1::uuid`, release).Scan(&before); err != nil {
		t.Fatal(err)
	}
	view = f.journey(t, f.person, "POST", path+"/actions", actionJSON("renew_candidate", view.Revision, "renew-candidate", freshCandidate, release, ""))
	if view.NextAction.RenewalAction != "renew_deploy" {
		t.Fatalf("next: %+v", view.NextAction)
	}
	view = f.journey(t, f.person, "POST", path+"/actions", actionJSON("renew_deploy", view.Revision, "renew-deploy", freshDeploy, release, ""))
	if view.NextAction.RenewalAction != "" || view.NextAction.Label != "Await deployment evidence" {
		t.Fatalf("renewed: %+v", view.NextAction)
	}
	var after string
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT row_to_json(r)::text FROM journey_releases r WHERE release_node_id=$1::uuid`, release).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("renewal changed release identity/state/revision")
	}
	// A new verify result cannot complete an old deployment after renewal.
	f.handoff(t, project, release, "deploy", "verify", 1, "succeeded", "")
	renewalChange(t, f, `UPDATE stage_handoffs SET journey_revision=$2 WHERE release_node_id=$1::uuid AND operation='verify'`, release, view.Revision)
	view = f.journey(t, f.person, "GET", path, "")
	if view.Stage != "deploy" {
		t.Fatalf("old deployment reused: %s", view.Stage)
	}
	for _, stage := range view.Stages {
		if (stage.Key == "build" || stage.Key == "deploy") && !stage.GateLive {
			t.Fatalf("renewed gate not live: %+v", stage)
		}
	}
	f.handoff(t, project, release, "deploy", "deploy", 2, "succeeded", "")
	renewalChange(t, f, `UPDATE stage_handoffs SET journey_revision=$2 WHERE release_node_id=$1::uuid AND operation='deploy' AND attempt=2`, release, view.Revision)
	view = f.journey(t, f.person, "GET", path, "")
	if view.Stage != "live" {
		t.Fatalf("fresh deployment evidence did not advance: %+v", view.NextAction)
	}
	f.expire(t, freshCandidate)
	unused := f.grant(t, f.agent.ID, f.person.ID, journey.ScopeCandidate, release)
	if w := f.do(f.person, "POST", path+"/actions", actionJSON("renew_candidate", view.Revision, "completed-release", unused, release, "")); w.Code != 409 {
		t.Fatalf("completed deployment reopened: %d %s", w.Code, w.Body.String())
	}
}

func TestJourneyCandidateRenewalPreservesEnterpriseReviewer(t *testing.T) {
	f := newFixture(t)
	project, release, candidate, _, view := renewalRelease(t, f)
	f.expire(t, candidate)
	renewalChange(t, f, `UPDATE journey_projects SET profile='enterprise',decision='go' WHERE project_node_id=$1::uuid`, project)
	path := "/api/projects/" + project + "/journey"
	self := f.grant(t, f.agent.ID, f.person.ID, journey.ScopeCandidate, release)
	w := f.do(f.person, "POST", path+"/actions", actionJSON("renew_candidate", view.Revision, "missing-reviewer", self, release, ""))
	if w.Code != 409 || !strings.Contains(w.Body.String(), "recorded builder") {
		t.Fatalf("missing reviewer: %d %s", w.Code, w.Body.String())
	}
	for _, kind := range []string{"journey.brief_confirmed", "journey.build_started"} {
		renewalChange(t, f, `INSERT INTO events(tenant_id,type,node_id,actor_principal_id,after) VALUES($1::uuid,$2,$3::uuid,$4::uuid,'{}')`, f.tenant, kind, project, f.person.ID)
	}
	w = f.do(f.person, "POST", path+"/actions", actionJSON("renew_candidate", view.Revision, "self-review", self, release, ""))
	if w.Code != 409 || !strings.Contains(w.Body.String(), "different person") {
		t.Fatalf("self review: %d %s", w.Code, w.Body.String())
	}
	fresh := f.grant(t, f.agent.ID, f.other.ID, journey.ScopeCandidate, release)
	// Even when the projection offers an independent reviewer, a caller cannot
	// substitute their own still-live request and bypass separation of duties.
	w = f.do(f.person, "POST", path+"/actions", actionJSON("renew_candidate", view.Revision, "substitute-self", self, release, ""))
	if w.Code != 403 || !strings.Contains(w.Body.String(), "different person") {
		t.Fatalf("substituted self review: %d %s", w.Code, w.Body.String())
	}
	view = f.journey(t, f.other, "POST", path+"/actions", actionJSON("renew_candidate", view.Revision, "independent", fresh, release, ""))
	if view.Stage != "deploy" {
		t.Fatal(fmt.Sprintf("unexpected stage %s", view.Stage))
	}
}
