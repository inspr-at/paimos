// SPDX-License-Identifier: AGPL-3.0-only
package journey_test

import (
	"testing"

	"github.com/inspr-at/paimos/internal/journey"
)

func TestAccessPermitRenewalRecoversExpiredAndRevokedAuthority(t *testing.T) {
	for _, revoked := range []bool{false, true} {
		t.Run(map[bool]string{false: "expired", true: "revoked"}[revoked], func(t *testing.T) {
			f := newFixture(t)
			project, release, _, _, view := renewalRelease(t, f)
			f.setReleaseState(t, release, "access")
			old := f.grant(t, f.agent.ID, f.person.ID, journey.ScopeAccess, release)
			renewalChange(t, f, `INSERT INTO journey_gates(tenant_id,project_node_id,release_node_id,gate,approval_request_id) VALUES($1,$2,$3,'access',$4)`, f.tenant, project, release, old)
			if revoked {
				f.revoke(t, old)
			} else {
				f.expire(t, old)
			}
			fresh := f.grant(t, f.agent.ID, f.person.ID, journey.ScopeAccess, release)
			path := "/api/projects/" + project + "/journey"
			view = f.journey(t, f.person, "GET", path, "")
			if view.NextAction.Key != "approve_permit" || view.NextAction.AccessRenewalAction != "renew_permit" || view.NextAction.RenewalAction != "" || !view.NextAction.Available || testPtr(view.NextAction.ApprovalRequestID) != fresh {
				t.Fatalf("no Access recovery: %+v", view.NextAction)
			}
			oldRecord := approvalRecord(t, f, old)
			for _, tc := range []struct {
				name, action, approval, release string
				status                          int
			}{
				{"missing release", "renew_permit", fresh, "", 400},
				{"wrong release", "renew_permit", fresh, project, 409},
				{"old action", "approve_permit", fresh, release, 409},
				{"wrong scope", "renew_permit", f.grant(t, f.agent.ID, f.person.ID, journey.ScopeDeploy, release), release, 403},
				{"wrong resource", "renew_permit", f.grant(t, f.agent.ID, f.person.ID, journey.ScopeAccess, project), release, 403},
			} {
				w := f.do(f.person, "POST", path+"/actions", actionJSON(tc.action, view.Revision, tc.name, tc.approval, tc.release, ""))
				if w.Code != tc.status {
					t.Fatalf("%s: %d %s", tc.name, w.Code, w.Body.String())
				}
			}
			payload := actionJSON("renew_permit", view.Revision, "renew", fresh, release, "")
			if w := f.do(f.agent, "POST", path+"/actions", payload); w.Code != 403 {
				t.Fatalf("agent renew: %d", w.Code)
			}
			if w := f.do(f.other, "POST", path+"/actions", payload); w.Code != 403 {
				t.Fatalf("wrong decider: %d", w.Code)
			}
			view = f.journey(t, f.person, "POST", path+"/actions", payload)
			if view.Stage != "access" || view.NextAction.Available || f.gateCount(t, project, "access") != 2 || f.events(t, "journey.permit_renewed") != 1 || approvalRecord(t, f, old) != oldRecord {
				t.Fatalf("renewal lost stage/history: %+v", view)
			}
			replay := f.journey(t, f.person, "POST", path+"/actions", payload)
			if replay.Revision != view.Revision || f.events(t, "journey.permit_renewed") != 1 {
				t.Fatal("renewal replay mutated state")
			}
			if w := f.do(f.other, "POST", path+"/actions", payload); w.Code != 403 {
				t.Fatalf("replay wrong decider: %d", w.Code)
			}
		})
	}
}
