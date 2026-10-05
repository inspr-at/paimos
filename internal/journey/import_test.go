// SPDX-License-Identifier: AGPL-3.0-only

package journey_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/journey"
	"github.com/inspr-at/paimos/internal/releases"
	"github.com/inspr-at/paimos/internal/requirements"
)

func importedNode(t *testing.T, f *fixture, kind, key, parent, state string) string {
	t.Helper()
	id := f.node(t, kind, key, key)
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE nodes SET parent_id=$2::uuid,state=$3 WHERE id=$1::uuid`, id, nullableTestID(parent), state); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(t.Context(), `INSERT INTO events(tenant_id,actor_principal_id,node_id,type,after) VALUES($1,$2,$3,'import.node_created',jsonb_build_object('node_id',$3::uuid))`, f.tenant, f.person.ID, id); err != nil {
		t.Fatal(err)
	}
	return id
}

func nullableTestID(id string) any {
	if id == "" {
		return nil
	}
	return id
}

func backfill(t *testing.T, f *fixture) int {
	t.Helper()
	var count int
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT aeon_backfill_journey_releases($1::uuid)`, f.tenant).Scan(&count)
	})
	if err != nil {
		t.Fatal(err)
	}
	return count
}

func TestImportedJourneyStagesAndReleaseBackfill(t *testing.T) {
	f := newFixture(t)
	releases.New(f.db.App).Mount(f.mux)
	live := importedNode(t, f, "project", "IMP-1", "", "open")
	liveRelease := importedNode(t, f, "release", "IMP-2", live, "done")
	importedNode(t, f, "work", "IMP-3", live, "done")
	build := importedNode(t, f, "project", "IMP-4", "", "open")
	buildRelease := importedNode(t, f, "release", "IMP-5", build, "open")
	openTicket := importedNode(t, f, "work", "IMP-6", build, "open")
	plan := importedNode(t, f, "project", "IMP-7", "", "open")
	importedNode(t, f, "work", "IMP-8", plan, "open")
	if got := backfill(t, f); got != 2 {
		t.Fatalf("backfilled %d releases", got)
	}
	if got := backfill(t, f); got != 0 {
		t.Fatalf("backfill replay changed %d releases", got)
	}
	if n := f.events(t, "journey.import_release_registered"); n != 2 {
		t.Fatalf("release events %d", n)
	}
	if _, err := f.db.Admin.Exec(t.Context(), `INSERT INTO journey_tickets(tenant_id,ticket_node_id,project_node_id,release_node_id,walker_position,source) VALUES($1,$2,$3,$4,0,'manual')`, f.tenant, openTicket, build, buildRelease); err != nil {
		t.Fatal(err)
	}
	before := f.eventCount(t)
	for _, tc := range []struct{ project, stage, action string }{{live, "live", "plan_next_release"}, {build, "build", "wait_for_build"}, {plan, "plan", "open_first_release"}} {
		v := f.journey(t, f.person, http.MethodGet, "/api/projects/"+tc.project+"/journey", "")
		if v.Stage != tc.stage || v.NextAction.Key != tc.action || v.StageSource != "derived" {
			t.Fatalf("%s: stage %s action %s", tc.project, v.Stage, v.NextAction.Key)
		}
		if !v.Imported {
			t.Fatalf("%s: an imported project must say so", tc.project)
		}
		if !strings.HasPrefix(v.RequirementsScope, "journey.requirements.r1.d") || len(v.RequirementsDigest) != 64 {
			t.Fatalf("missing agreement scope: %+v", v)
		}
		if v.LaunchReadiness.CanAdmit || v.LaunchReadiness.Reason == "" {
			t.Fatalf("missing launch blocker: %+v", v.LaunchReadiness)
		}
	}
	if f.eventCount(t) != before || f.journeyCount(t, f.tenant) != 2 {
		t.Fatal("GET changed imported journey rows or events")
	}
	if w := f.do(f.person, http.MethodGet, "/api/projects/"+live+"/releases/"+liveRelease+"/walker", ""); w.Code != 200 {
		t.Fatalf("backfilled walker %d %s", w.Code, w.Body.String())
	}
	if w := f.do(f.person, http.MethodGet, "/api/projects/"+build+"/releases/"+buildRelease+"/walker", ""); w.Code != 200 {
		t.Fatalf("build walker %d %s", w.Code, w.Body.String())
	}

	planView := f.journey(t, f.person, http.MethodGet, "/api/projects/"+plan+"/journey", "")
	planView = f.journey(t, f.person, http.MethodPost, "/api/projects/"+plan+"/journey/actions", actionJSON("open_first_release", planView.Revision, "first-release", "", "", ""))
	if planView.StageSource != "journey" || planView.Stage != "plan" || planView.CurrentReleaseID == nil {
		t.Fatalf("first release %+v", planView)
	}
	// Recorded history from here on; the project still came with its own.
	if !planView.Imported {
		t.Fatal("imported flag lost after the first journey action")
	}
	if f.events(t, "journey.derived") != 1 || f.events(t, "journey.release_opened") != 1 {
		t.Fatal("first action did not record derivation and release")
	}
	planView = f.journey(t, f.person, http.MethodGet, "/api/projects/"+plan+"/journey", "")
	if planView.StageSource != "journey" || planView.Stage != "plan" || f.events(t, "journey.derived") != 1 {
		t.Fatal("read changed derivation")
	}

	liveView := f.journey(t, f.person, http.MethodGet, "/api/projects/"+live+"/journey", "")
	liveView = f.journey(t, f.person, http.MethodPost, "/api/projects/"+live+"/journey/actions", actionJSON("plan_next_release", liveView.Revision, "next-release", "", liveRelease, ""))
	if liveView.Stage != "plan" || liveView.CurrentReleaseID == nil || *liveView.CurrentReleaseID == liveRelease {
		t.Fatalf("next imported release %+v", liveView)
	}
}

func TestImportedCandidateRefusal(t *testing.T) {
	f := newFixture(t)
	project := importedNode(t, f, "project", "IMP-9", "", "open")
	release := importedNode(t, f, "release", "IMP-10", project, "open")
	ticket := importedNode(t, f, "work", "IMP-11", project, "open")
	if backfill(t, f) != 1 {
		t.Fatal("release backfill missing")
	}
	if _, err := f.db.Admin.Exec(t.Context(), `INSERT INTO journey_tickets(tenant_id,ticket_node_id,project_node_id,release_node_id,walker_position,source) VALUES($1,$2,$3,$4,0,'manual')`, f.tenant, ticket, project, release); err != nil {
		t.Fatal(err)
	}
	v := f.journey(t, f.person, http.MethodGet, "/api/projects/"+project+"/journey", "")
	if v.NextAction.Available {
		t.Fatal("open ticket permitted candidate")
	}
	f.setTicketState(t, "IMP-11", "done")
	mark := f.grant(t, f.agent.ID, f.person.ID, journey.ScopeBuild, release)
	v = f.journey(t, f.person, http.MethodPost, "/api/projects/"+project+"/journey/actions", actionJSON("mark_candidate", v.Revision, "candidate", mark, release, ""))
	if v.StageSource != "journey" || f.releaseState(t, release) != "candidate" {
		t.Fatal("candidate not persisted")
	}
	reject := f.grant(t, f.agent.ID, f.person.ID, journey.ScopeCandidate, release)
	if w := f.do(f.person, http.MethodPost, "/api/projects/"+project+"/journey/actions", actionJSON("reject_candidate", v.Revision, "refuse-no-reason", reject, release, "")); w.Code != 400 {
		t.Fatalf("reason gate %d %s", w.Code, w.Body.String())
	}
	v = f.journey(t, f.person, http.MethodPost, "/api/projects/"+project+"/journey/actions", actionJSON("reject_candidate", v.Revision, "refuse", reject, release, "quality issue"))
	if v.Stage != "build" || f.releaseState(t, release) != "building" || f.events(t, "journey.candidate_rejected") != 1 {
		t.Fatalf("refusal %+v", v)
	}
}

func TestProjectedRequirementsScopeCanBeAgreed(t *testing.T) {
	f := newFixture(t)
	requirements.New(f.db.App).Mount(f.mux)
	project := f.node(t, "project", "SCP-1", "Scope")
	v := f.journey(t, f.person, http.MethodPut, "/api/projects/"+project+"/journey/profile", `{"profile":"personal","expected_revision":1}`)
	f.acceptBrief(t, project)
	v = f.journey(t, f.person, http.MethodPost, "/api/projects/"+project+"/journey/actions", actionJSON("confirm_brief", v.Revision, "brief", "", "", ""))
	body := `{"kind":"functional","title":"Use the scope","body":"Details","expected_revision":` + fmt.Sprint(v.Revision) + `,"idempotency_key":"requirement"}`
	if w := f.do(f.person, http.MethodPost, "/api/projects/"+project+"/requirements", body); w.Code != 201 {
		t.Fatalf("requirement %d %s", w.Code, w.Body.String())
	}
	v = f.journey(t, f.person, http.MethodGet, "/api/projects/"+project+"/journey", "")
	if v.RequirementsScope != requirements.ApprovalScope(v.Revision, v.RequirementsDigest) {
		t.Fatalf("scope %s", v.RequirementsScope)
	}
	approval := f.grant(t, f.agent.ID, f.person.ID, v.RequirementsScope, project)
	v = f.journey(t, f.person, http.MethodGet, "/api/projects/"+project+"/journey", "")
	if !v.NextAction.Available || v.NextAction.ApprovalRequestID == nil || *v.NextAction.ApprovalRequestID != approval {
		t.Fatalf("agreement offer %+v", v.NextAction)
	}
	agreeBody := `{"expected_revision":` + fmt.Sprint(v.Revision) + `,"approval_request_id":"` + approval + `","idempotency_key":"agree"}`
	if w := f.do(f.person, http.MethodPost, "/api/projects/"+project+"/requirements/agree", agreeBody); w.Code != 200 {
		t.Fatalf("agreement %d %s", w.Code, w.Body.String())
	}
}
