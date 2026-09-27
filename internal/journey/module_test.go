// SPDX-License-Identifier: AGPL-3.0-only

package journey_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/approvals"
	"github.com/inspr-at/paimos/internal/auth"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/journey"
	"github.com/inspr-at/paimos/internal/plugins"
	"github.com/inspr-at/paimos/internal/stagehandoff"
	"github.com/inspr-at/paimos/internal/tenant"
)

const digest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func testPtr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func TestJourneyGateKeyPharosHandoffRoundtrip(t *testing.T) {
	f := newFixture(t)
	project := f.node(t, "project", "PRJ-36", "Disposable Pharos fixture")
	release := f.node(t, "release", "REL-1", "Release")
	ctx := t.Context()
	if err := db.InTenant(dbtest.Seed(ctx), f.db.App, f.tenant, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO journey_projects(tenant_id,project_node_id,brief_confirmed_at,requirements_revision,agreed_requirements_revision,agreed_requirements_digest_sha256,current_release_node_id)
			VALUES($1::uuid,$2::uuid,now(),1,1,$4,$3::uuid)`, f.tenant, project, release, digest); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO journey_releases(tenant_id,release_node_id,project_node_id,number,state)
			VALUES($1::uuid,$2::uuid,$3::uuid,1,'candidate')`, f.tenant, release, project); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRoleWith(t, f.db.Admin, f.tenant, f.agent.ID, "owner")
	dbtest.BindRoleWith(t, f.db.Admin, f.tenant, f.person.ID, "owner")
	keyID, agentID, token, err := auth.OperatorCreateAgentKey(ctx, f.db.App, f.tenant, "gate proposer", f.agent.ID, []string{"approvals.request", "journey.read", "stage_handoffs.write"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if agentID != f.agent.ID {
		t.Fatal("key belongs to a different agent")
	}
	if _, err := auth.OperatorAddJourneyGateScopes(ctx, f.db.App, f.tenant, keyID, []string{"candidate", "deploy"}); err != nil {
		t.Fatal(err)
	}
	registry, err := plugins.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	pharos, _ := registry.Lookup("pharos")
	if err := db.InTenant(dbtest.Seed(ctx), f.db.App, f.tenant, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO plugin_installations(tenant_id,plugin_id,version,manifest_digest_sha256,owner,enabled,permissions,updated_by_principal_id)
			VALUES($1::uuid,$2,$3,$4,$5,true,$6,$7::uuid)`, f.tenant, pharos.Manifest.ID, pharos.Manifest.Version, pharos.Manifest.DigestSHA256, pharos.Manifest.Owner, pharos.Manifest.Permissions, f.person.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	approvals.New(f.db.App).Mount(f.mux)
	stagehandoff.New(f.db.App, registry).Mount(f.mux)
	callAgent := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(tenant.WithPrincipal(req.Context(), f.agent))
		w := httptest.NewRecorder()
		f.mux.ServeHTTP(w, req)
		return w
	}
	view := f.journey(t, f.person, http.MethodGet, "/api/projects/"+project+"/journey", "")
	if view.NextAction.Key != "approve_candidate" {
		t.Fatalf("initial journey action: %+v", view.NextAction)
	}
	for _, gate := range []struct{ scope, action string }{{"journey.candidate", "approve_candidate"}, {"journey.deploy", "approve_deploy"}} {
		body := fmt.Sprintf(`{"scope":%q,"resource_kind":"node","resource_id":%q,"rationale":"Disposable Pharos roundtrip gate","expires_at":%q}`, gate.scope, release, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
		proposed := callAgent(http.MethodPost, "/api/approvals", body)
		if proposed.Code != http.StatusCreated {
			t.Fatalf("propose %s: %d %s", gate.scope, proposed.Code, proposed.Body.String())
		}
		var approval struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(proposed.Body.Bytes(), &approval); err != nil || approval.ID == "" {
			t.Fatalf("proposal: %v %s", err, proposed.Body.String())
		}
		decided := f.do(f.person, http.MethodPost, "/api/approvals/"+approval.ID+"/decision", `{"decision":"approved"}`)
		if decided.Code != http.StatusOK {
			t.Fatalf("decide %s: %d %s", gate.scope, decided.Code, decided.Body.String())
		}
		view = f.journey(t, f.person, http.MethodPost, "/api/projects/"+project+"/journey/actions", actionJSON(gate.action, view.Revision, gate.action, approval.ID, release, ""))
		if err := db.InTenant(dbtest.Seed(ctx), f.db.App, f.tenant, func(tx pgx.Tx) error {
			var proposer, bound string
			if err := tx.QueryRow(ctx, `SELECT a.proposed_by_principal_id::text,g.approval_request_id::text FROM journey_gates g
				JOIN approval_requests a ON a.id=g.approval_request_id WHERE g.release_node_id=$1::uuid AND a.scope=$2`, release, gate.scope).Scan(&proposer, &bound); err != nil {
				return err
			}
			if proposer != f.agent.ID || bound != approval.ID {
				t.Fatalf("gate %s: proposer=%s approval=%s", gate.scope, proposer, bound)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	request := fmt.Sprintf(`{"project_node_id":%q,"release_node_id":%q,"stage":"deploy","operation":"deploy","expected_journey_revision":%d,"idempotency_key":"pharos-roundtrip"}`, project, release, view.Revision)
	handoff := callAgent(http.MethodPost, "/api/stage-handoffs", request)
	if handoff.Code != http.StatusCreated {
		t.Fatalf("Pharos deploy handoff: %d %s", handoff.Code, handoff.Body.String())
	}
	var created stagehandoff.Handoff
	if err := json.Unmarshal(handoff.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.PluginID != "pharos" || created.Stage != "deploy" || created.Operation != "deploy" {
		t.Fatalf("wrong handoff: %+v", created)
	}
}

func TestJourneyStageGateLiveTracksRevocationAndExpiry(t *testing.T) {
	f := newFixture(t)
	project := f.node(t, "project", "PRJ-101", "Gate project")
	if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenant, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO journey_projects(tenant_id,project_node_id) VALUES($1::uuid,$2::uuid)`, f.tenant, project)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	release := f.release(t, project, "REL-102", 1)
	path := "/api/projects/" + project + "/journey"
	stage := func(t *testing.T) journey.JourneyStage {
		t.Helper()
		response := f.do(f.person, http.MethodGet, path, "")
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"gate_live":`) {
			t.Fatalf("journey response: %d %s", response.Code, response.Body.String())
		}
		var view journey.Journey
		if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		for _, item := range view.Stages {
			if item.Key == "deploy" {
				return item
			}
		}
		t.Fatal("deploy stage missing")
		return journey.JourneyStage{}
	}
	if got := stage(t); got.GateApprovalID != nil || got.GateLive {
		t.Fatalf("absent gate: %+v", got)
	}
	approval := f.grant(t, f.agent.ID, f.person.ID, journey.ScopeDeploy, release)
	change := func(query string, args ...any) {
		t.Helper()
		if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenant, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), query, args...)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	change(`INSERT INTO journey_gates(tenant_id,project_node_id,release_node_id,gate,approval_request_id) VALUES($1::uuid,$2::uuid,$3::uuid,'deploy',$4::uuid)`, f.tenant, project, release, approval)
	if got := stage(t); got.GateApprovalID == nil || *got.GateApprovalID != approval || !got.GateLive {
		t.Fatalf("live gate: %+v", got)
	}
	change(`UPDATE agent_permission_grants SET revoked_at=now() WHERE approval_request_id=$1::uuid`, approval)
	if got := stage(t); got.GateApprovalID == nil || *got.GateApprovalID != approval || got.GateLive {
		t.Fatalf("revoked gate: %+v", got)
	}
	change(`UPDATE agent_permission_grants SET revoked_at=NULL WHERE approval_request_id=$1::uuid`, approval)
	newer := f.grant(t, f.agent.ID, f.person.ID, journey.ScopeDeploy, release)
	change(`INSERT INTO journey_gates(tenant_id,project_node_id,release_node_id,gate,approval_request_id) VALUES($1::uuid,$2::uuid,$3::uuid,'deploy',$4::uuid)`, f.tenant, project, release, newer)
	change(`UPDATE agent_permission_grants SET revoked_at=now() WHERE approval_request_id=$1::uuid`, newer)
	if got := stage(t); got.GateApprovalID == nil || *got.GateApprovalID != approval || !got.GateLive || testPtr(got.GateOfferID) != approval || got.GateOfferState != "approved_live" {
		t.Fatalf("older live gate with newer revoked history: %+v", got)
	}
	change(`UPDATE agent_permission_grants SET valid_until=now()-interval '1 second' WHERE approval_request_id=$1::uuid`, approval)
	if got := stage(t); got.GateApprovalID == nil || *got.GateApprovalID != newer || got.GateLive || testPtr(got.GateOfferID) != newer || got.GateOfferState != "revoked" {
		t.Fatalf("expired gate: %+v", got)
	}
}

func TestJourneyMultiOfferProjectionKeepsConsumedAuthoritySeparate(t *testing.T) {
	f := newFixture(t)
	project := f.node(t, "project", "PRJ-210", "Multi-offer gate")
	if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenant, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO journey_projects(tenant_id,project_node_id,brief_confirmed_at,requirements_revision,agreed_requirements_revision,agreed_requirements_digest_sha256)
			VALUES($1::uuid,$2::uuid,now(),1,1,$3)`, f.tenant, project, digest)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	release := f.release(t, project, "REL-210", 1)
	f.setReleaseState(t, release, "deploying")
	change := func(query string, args ...any) {
		t.Helper()
		if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenant, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), query, args...)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	stage := func(view journey.Journey, key string) journey.JourneyStage {
		t.Helper()
		for _, item := range view.Stages {
			if item.Key == key {
				return item
			}
		}
		t.Fatalf("missing stage %s", key)
		return journey.JourneyStage{}
	}
	get := func() journey.Journey {
		t.Helper()
		return f.journey(t, f.person, http.MethodGet, "/api/projects/"+project+"/journey", "")
	}

	oldCandidate := f.grant(t, f.agent.ID, f.person.ID, journey.ScopeCandidate, release)
	change(`UPDATE agent_permission_grants SET valid_until=now()-interval '1 second' WHERE approval_request_id=$1::uuid`, oldCandidate)
	liveCandidate := f.grant(t, f.agent.ID, f.person.ID, journey.ScopeCandidate, release)
	change(`INSERT INTO journey_gates(tenant_id,project_node_id,release_node_id,gate,approval_request_id)
		VALUES($1::uuid,$2::uuid,$3::uuid,'candidate',$4::uuid)`, f.tenant, project, release, liveCandidate)
	view := get()
	if got := stage(view, "build"); !got.GateLive || testPtr(got.GateApprovalID) != liveCandidate || testPtr(got.GateOfferID) != liveCandidate || got.GateOfferState != "approved_live" {
		t.Fatalf("consumed live candidate must outrank expired offer: %+v", got)
	}
	if view.NextAction.Key != "approve_deploy" || view.NextAction.Available {
		t.Fatalf("deploy still awaits its own gate: %+v", view.NextAction)
	}

	oldDeploy := f.grant(t, f.agent.ID, f.person.ID, journey.ScopeDeploy, release)
	change(`UPDATE agent_permission_grants SET valid_until=now()-interval '1 second' WHERE approval_request_id=$1::uuid`, oldDeploy)
	liveDeploy := f.grant(t, f.agent.ID, f.person.ID, journey.ScopeDeploy, release)
	view = get()
	if got := stage(view, "deploy"); got.GateLive || testPtr(got.GateOfferID) != liveDeploy || got.GateOfferState != "approved_live" {
		t.Fatalf("unconsumed live deploy offer must outrank expired offer: %+v", got)
	}
	if !view.NextAction.Available || testPtr(view.NextAction.ApprovalRequestID) != liveDeploy || view.NextAction.Label != "Apply deployment approval" {
		t.Fatalf("approved gate awaits human apply: %+v", view.NextAction)
	}
	change(`INSERT INTO journey_gates(tenant_id,project_node_id,release_node_id,gate,approval_request_id)
		VALUES($1::uuid,$2::uuid,$3::uuid,'deploy',$4::uuid)`, f.tenant, project, release, liveDeploy)
	view = get()
	if got := stage(view, "deploy"); !got.GateLive || testPtr(got.GateOfferID) != liveDeploy || got.GateOfferState != "approved_live" {
		t.Fatalf("consumed live deploy gate must remain displayed: %+v", got)
	}
	if view.NextAction.Available || view.NextAction.ApprovalRequestID != nil || view.NextAction.Label != "Await deployment evidence" {
		t.Fatalf("consumed gate must not be offered for reuse: %+v", view.NextAction)
	}
}

func TestJourneyRequirementsGateUsesCurrentRevisionScope(t *testing.T) {
	f := newFixture(t)
	project := f.node(t, "project", "PRJ-211", "Requirements revision gate")
	if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenant, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO journey_projects(tenant_id,project_node_id,brief_confirmed_at,requirements_revision)
			VALUES($1::uuid,$2::uuid,now(),1)`, f.tenant, project)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	get := func() journey.Journey {
		t.Helper()
		return f.journey(t, f.person, http.MethodGet, "/api/projects/"+project+"/journey", "")
	}
	stage := func(view journey.Journey) journey.JourneyStage {
		t.Helper()
		for _, item := range view.Stages {
			if item.Key == "requirements" {
				return item
			}
		}
		t.Fatal("requirements stage missing")
		return journey.JourneyStage{}
	}
	view := get()
	approval := f.grant(t, f.agent.ID, f.person.ID, view.RequirementsScope, project)
	if err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenant, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO journey_gates(tenant_id,project_node_id,gate,approval_request_id)
			VALUES($1::uuid,$2::uuid,'requirements',$3::uuid)`, f.tenant, project, approval)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got := stage(get()); !got.GateLive || testPtr(got.GateOfferID) != approval {
		t.Fatalf("current revision gate should be live: %+v", got)
	}
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE journey_projects SET revision=revision+1 WHERE project_node_id=$1::uuid`, project); err != nil {
		t.Fatal(err)
	}
	view = get()
	if got := stage(view); got.GateLive || got.GateOfferID != nil || testPtr(got.GateApprovalID) != approval {
		t.Fatalf("previous revision remains history, not current authority: %+v", got)
	}
}

func TestJourneyActions(t *testing.T) {
	f := newFixture(t)
	project := f.node(t, "project", "PRJ-1", "Garden")
	other := f.node(t, "project", "PRJ-2", "Other")

	if w := f.anon(http.MethodGet, "/api/projects/"+project+"/journey"); w.Code != http.StatusUnauthorized {
		t.Fatalf("anon %d", w.Code)
	}
	if w := f.do(f.person, http.MethodGet, "/api/projects/not-a-uuid/journey", ""); w.Code != http.StatusBadRequest {
		t.Fatalf("bad id %d", w.Code)
	}
	if w := f.do(f.person, http.MethodGet, "/api/projects/00000000-0000-4000-8000-000000000099/journey", ""); w.Code != http.StatusNotFound {
		t.Fatalf("missing project %d %s", w.Code, w.Body.String())
	}

	view := f.journey(t, f.person, http.MethodGet, "/api/projects/"+project+"/journey", "")
	if view.StageSource != "journey" || view.Stage != "inspire" || view.NextAction.Key != "continue_intake" || !view.NextAction.Available || view.Revision != 1 {
		t.Fatalf("init %+v", view.NextAction)
	}
	if view.ProjectNodeID != project || view.NodeKey != "PRJ-1" || view.ProjectKey != "PRJ" || view.TenantSlug != "journey-a" {
		t.Fatalf("binding id=%s key=%s slug=%s", view.ProjectNodeID, view.ProjectKey, view.TenantSlug)
	}
	spoofed := f.journey(t, f.person, http.MethodGet, "/api/projects/"+project+"/journey?project_key=EVIL&tenant_slug=evil&project_node_id=00000000-0000-4000-8000-000000000099", "")
	if spoofed.ProjectNodeID != project || spoofed.NodeKey != "PRJ-1" || spoofed.ProjectKey != "PRJ" || spoofed.TenantSlug != "journey-a" {
		t.Fatalf("client binding accepted: %+v", spoofed)
	}
	if view.Imported {
		t.Fatal("a project started here is not imported")
	}
	if n := f.events(t, "journey.initialized"); n != 0 {
		t.Fatalf("read initialized a journey: %d events", n)
	}
	if n := f.kindCount(t, f.tenant, "requirement"); n != 0 {
		t.Fatalf("read seeded requirement kind: %d", n)
	}
	again := f.journey(t, f.person, http.MethodGet, "/api/projects/"+project+"/journey", "")
	if again.Revision != 1 || f.events(t, "journey.initialized") != 0 {
		t.Fatal("second read wrote a journey")
	}

	if w := f.do(f.agent, http.MethodPut, "/api/projects/"+project+"/journey/profile", `{"profile":"professional","expected_revision":1}`); w.Code != http.StatusForbidden {
		t.Fatalf("agent profile %d %s", w.Code, w.Body.String())
	}
	if w := f.do(f.person, http.MethodPut, "/api/projects/"+project+"/journey/profile", `{"profile":"professional","expected_revision":9}`); w.Code != http.StatusConflict {
		t.Fatalf("stale profile %d", w.Code)
	}
	before := f.eventCount(t)
	view = f.journey(t, f.person, http.MethodPut, "/api/projects/"+project+"/journey/profile", `{"profile":"professional","expected_revision":1}`)
	if view.Profile != "professional" || view.Revision != 2 || view.Stage != "inspire" {
		t.Fatalf("profile %+v", view)
	}
	if f.events(t, "journey.initialized") != 1 || f.kindCount(t, f.tenant, "requirement") != 1 {
		t.Fatal("first action did not initialize journey")
	}
	if f.events(t, "journey.profile_set") != 1 {
		t.Fatal("profile event missing")
	}
	same := f.journey(t, f.person, http.MethodPut, "/api/projects/"+project+"/journey/profile", `{"profile":"professional","expected_revision":2}`)
	if same.Revision != 2 || f.eventCount(t) != before+2 {
		t.Fatal("unchanged profile wrote an event")
	}

	if w := f.do(f.person, http.MethodPost, "/api/projects/"+project+"/journey/actions", actionJSON("confirm_brief", view.Revision, "brief-1", "", "", "")); w.Code != http.StatusConflict {
		t.Fatalf("confirm without acceptance %d %s", w.Code, w.Body.String())
	}
	f.acceptBrief(t, project)
	if w := f.do(f.agent, http.MethodPost, "/api/projects/"+project+"/journey/actions", actionJSON("confirm_brief", view.Revision, "brief-2", "", "", "")); w.Code != http.StatusForbidden {
		t.Fatalf("agent confirm %d", w.Code)
	}
	view = f.journey(t, f.person, http.MethodPost, "/api/projects/"+project+"/journey/actions", actionJSON("confirm_brief", view.Revision, "brief-1", "", "", ""))
	if view.Stage != "shape" || view.NextAction.Key != "decide" || view.NextAction.Available || view.NextAction.Reason == "" {
		t.Fatalf("after brief %+v", view.NextAction)
	}
	if f.events(t, "journey.brief_confirmed") != 1 {
		t.Fatal("brief event missing")
	}

	if w := f.do(f.person, http.MethodPost, "/api/projects/"+project+"/journey/actions", actionJSON("go", view.Revision, "go-0", "", "", "")); w.Code != http.StatusConflict {
		t.Fatalf("go without gate %d %s", w.Code, w.Body.String())
	}
	wrong := f.grant(t, f.agent.ID, f.person.ID, journey.ScopeShape, other)
	if w := f.do(f.person, http.MethodPost, "/api/projects/"+project+"/journey/actions", actionJSON("go", view.Revision, "go-wrong", wrong, "", "")); w.Code != http.StatusForbidden {
		t.Fatalf("wrong resource %d %s", w.Code, w.Body.String())
	}
	shape := f.grant(t, f.agent.ID, f.other.ID, journey.ScopeShape, project)
	if w := f.do(f.person, http.MethodPost, "/api/projects/"+project+"/journey/actions", actionJSON("go", view.Revision, "go-other", shape, "", "")); w.Code != http.StatusForbidden {
		t.Fatalf("other decider %d %s", w.Code, w.Body.String())
	}
	shape = f.grant(t, f.agent.ID, f.person.ID, journey.ScopeShape, project)
	if w := f.do(f.person, http.MethodPost, "/api/projects/"+project+"/journey/actions", actionJSON("go", view.Revision, "go-cap", shape, "", "")); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "cap_hours") {
		t.Fatalf("missing cap %d %s", w.Code, w.Body.String())
	}
	f.setCap(t, project, 10)
	eventsBefore := f.eventCount(t)
	replay := actionJSON("go", view.Revision, "go-1", shape, "", "")
	view = f.journey(t, f.person, http.MethodPost, "/api/projects/"+project+"/journey/actions", replay)
	if view.Stage != "requirements" || view.NextAction.Key != "approve_requirements" {
		t.Fatalf("after go %+v", view.NextAction)
	}
	if f.events(t, "journey.decided") != 1 {
		t.Fatal("decision event missing")
	}
	replayed := f.journey(t, f.person, http.MethodPost, "/api/projects/"+project+"/journey/actions", replay)
	if replayed.Revision != view.Revision || f.events(t, "journey.decided") != 1 {
		t.Fatal("idempotent go wrote again")
	}
	if w := f.do(f.person, http.MethodPost, "/api/projects/"+project+"/journey/actions", actionJSON("park", view.Revision, "go-1", shape, "", "later")); w.Code != http.StatusConflict {
		t.Fatalf("divergent replay %d %s", w.Code, w.Body.String())
	}
	if f.eventCount(t) == eventsBefore {
		t.Fatal("go did not write an event")
	}

	f.agree(t, project)
	release := f.release(t, project, "REL-8", 1)
	f.ticket(t, project, release, "TKT-1", "12", false, false)
	view = f.journey(t, f.person, http.MethodGet, "/api/projects/"+project+"/journey", "")
	if view.Stage != "plan" || view.NextAction.Key != "start_build" || view.NextAction.Reason != "The selected plan exceeds the approved cap." {
		t.Fatalf("over cap %+v", view.NextAction)
	}
	if w := f.do(f.person, http.MethodPost, "/api/projects/"+project+"/journey/actions", actionJSON("start_build", view.Revision, "build-over", "", release, "")); w.Code != http.StatusConflict {
		t.Fatalf("start over cap %d %s", w.Code, w.Body.String())
	}
	f.setTicketHours(t, "TKT-1", "4")
	f.setScope(t, "TKT-1", true)
	view = f.journey(t, f.person, http.MethodGet, "/api/projects/"+project+"/journey", "")
	if view.Stage != "requirements" || view.NextAction.Reason == "" {
		t.Fatalf("scope %+v", view.NextAction)
	}
	f.setScope(t, "TKT-1", false)
	view = f.journey(t, f.person, http.MethodGet, "/api/projects/"+project+"/journey", "")
	if view.Stage != "plan" || view.NextAction.Available {
		t.Fatalf("plan without build gate %+v", view.NextAction)
	}
	buildWrong := f.grant(t, f.agent.ID, f.person.ID, journey.ScopeBuild, project)
	if w := f.do(f.person, http.MethodPost, "/api/projects/"+project+"/journey/actions", actionJSON("start_build", view.Revision, "build-wrong", buildWrong, release, "")); w.Code != http.StatusForbidden {
		t.Fatalf("build on project %d %s", w.Code, w.Body.String())
	}
	build := f.grant(t, f.agent.ID, f.person.ID, journey.ScopeBuild, release)
	view = f.journey(t, f.person, http.MethodPost, "/api/projects/"+project+"/journey/actions", actionJSON("start_build", view.Revision, "build-1", build, release, ""))
	if view.Stage != "build" || view.NextAction.Key != "wait_for_build" || view.NextAction.Available {
		t.Fatalf("building %+v", view.NextAction)
	}
	if f.accessRequired(t, release) {
		t.Fatal("access was required without an access change")
	}
	if w := f.do(f.person, http.MethodPost, "/api/projects/"+project+"/journey/actions", actionJSON("start_build", view.Revision, "build-2", build, release, "")); w.Code != http.StatusConflict {
		t.Fatalf("start during build %d %s", w.Code, w.Body.String())
	}
	f.setTicketState(t, "TKT-1", "done")
	mark := f.grant(t, f.agent.ID, f.person.ID, journey.ScopeBuild, release)
	view = f.journey(t, f.person, http.MethodGet, "/api/projects/"+project+"/journey", "")
	view = f.journey(t, f.person, http.MethodPost, "/api/projects/"+project+"/journey/actions", actionJSON("mark_candidate", view.Revision, "mark-1", mark, release, ""))
	if f.releaseState(t, release) != "candidate" || f.events(t, "journey.candidate_marked") != 1 {
		t.Fatal("candidate transition was not recorded")
	}
	cand := f.grant(t, f.agent.ID, f.person.ID, journey.ScopeCandidate, release)
	view = f.journey(t, f.person, http.MethodGet, "/api/projects/"+project+"/journey", "")
	view = f.journey(t, f.person, http.MethodPost, "/api/projects/"+project+"/journey/actions", actionJSON("approve_candidate", view.Revision, "cand-1", cand, release, ""))
	if view.Stage != "deploy" || view.NextAction.Key != "approve_deploy" {
		t.Fatalf("after candidate %+v", view.NextAction)
	}
	deploy := f.grant(t, f.agent.ID, f.person.ID, journey.ScopeDeploy, release)
	view = f.journey(t, f.person, http.MethodPost, "/api/projects/"+project+"/journey/actions", actionJSON("approve_deploy", view.Revision, "dep-1", deploy, release, ""))
	if view.Stage != "deploy" || view.NextAction.Available || stateOf(view, "deploy") != "blocked" {
		t.Fatalf("waiting for evidence %+v state %s", view.NextAction, stateOf(view, "deploy"))
	}
	f.handoff(t, project, release, "deploy", "verify", 1, "failed", "policy_refused")
	view = f.journey(t, f.person, http.MethodGet, "/api/projects/"+project+"/journey", "")
	if view.NextAction.Key != "retry_deploy" || stateOf(view, "deploy") != "blocked" {
		t.Fatalf("retry %+v", view.NextAction)
	}
	if w := f.do(f.person, http.MethodPost, "/api/projects/"+project+"/journey/actions", actionJSON("retry_deploy", view.Revision, "retry-0", deploy, release, "")); w.Code != http.StatusConflict && w.Code != http.StatusForbidden {
		t.Fatalf("consumed deploy approval %d %s", w.Code, w.Body.String())
	}
	retry := f.grant(t, f.agent.ID, f.person.ID, journey.ScopeDeploy, release)
	view = f.journey(t, f.person, http.MethodPost, "/api/projects/"+project+"/journey/actions", actionJSON("retry_deploy", view.Revision, "retry-1", retry, release, ""))
	if view.NextAction.Key != "approve_deploy" || view.NextAction.Available {
		t.Fatalf("after retry %+v", view.NextAction)
	}
	f.handoff(t, project, release, "deploy", "deploy", 1, "succeeded", "")
	f.handoff(t, project, release, "deploy", "verify", 2, "succeeded", "")
	view = f.journey(t, f.person, http.MethodGet, "/api/projects/"+project+"/journey", "")
	for _, stage := range view.Stages {
		if stage.Key == "deploy" && (stage.HandoffID == nil || stage.HandoffAttempt == nil || *stage.HandoffAttempt != 2 || stage.HandoffAuthorityEpoch == nil || *stage.HandoffAuthorityEpoch != 1) {
			t.Fatalf("deploy stage handoff identity: %+v", stage)
		}
	}
	if view.Stage != "live" || view.NextAction.Key != "plan_next_release" || !view.NextAction.Available || stateOf(view, "access") != "skipped" {
		t.Fatalf("live %+v access %s", view.NextAction, stateOf(view, "access"))
	}
	stale := f.eventCount(t)
	if w := f.do(f.person, http.MethodPost, "/api/projects/"+project+"/journey/actions", actionJSON("plan_next_release", view.Revision-1, "plan-stale", "", release, "")); w.Code != http.StatusConflict || f.eventCount(t) != stale {
		t.Fatalf("stale plan %d events %d->%d %s", w.Code, stale, f.eventCount(t), w.Body.String())
	}
	planBody := actionJSON("plan_next_release", view.Revision, "plan-1", "", release, "")
	view = f.journey(t, f.person, http.MethodPost, "/api/projects/"+project+"/journey/actions", planBody)
	if view.Stage != "plan" || stateOf(view, "live") != "done" || view.NextAction.Key != "start_build" {
		t.Fatalf("next plan %+v live %s", view.NextAction, stateOf(view, "live"))
	}
	if f.releaseState(t, release) != "released" {
		t.Fatalf("release 1 state %s", f.releaseState(t, release))
	}
	if got := f.journey(t, f.person, http.MethodPost, "/api/projects/"+project+"/journey/actions", planBody); got.Revision != view.Revision || f.releaseCount(t, project) != 2 {
		t.Fatal("plan replay created another release")
	}
	release2 := *view.CurrentReleaseID
	f.ticket(t, project, release2, "TKT-2", "8", false, false)
	view = f.journey(t, f.person, http.MethodGet, "/api/projects/"+project+"/journey", "")
	if view.NextAction.Reason != "The selected plan exceeds the approved cap." {
		t.Fatalf("spent cap %+v", view.NextAction)
	}
	f.setTicketHours(t, "TKT-2", "4")
	build2 := f.grant(t, f.agent.ID, f.person.ID, journey.ScopeBuild, release2)
	view = f.journey(t, f.person, http.MethodGet, "/api/projects/"+project+"/journey", "")
	view = f.journey(t, f.person, http.MethodPost, "/api/projects/"+project+"/journey/actions", actionJSON("start_build", view.Revision, "build-r2", build2, release2, ""))
	f.setReleaseState(t, release2, "candidate")
	cand2 := f.grant(t, f.agent.ID, f.person.ID, journey.ScopeCandidate, release2)
	view = f.journey(t, f.person, http.MethodGet, "/api/projects/"+project+"/journey", "")
	view = f.journey(t, f.person, http.MethodPost, "/api/projects/"+project+"/journey/actions", actionJSON("approve_candidate", view.Revision, "cand-r2", cand2, release2, ""))
	dep2 := f.grant(t, f.agent.ID, f.person.ID, journey.ScopeDeploy, release2)
	view = f.journey(t, f.person, http.MethodPost, "/api/projects/"+project+"/journey/actions", actionJSON("approve_deploy", view.Revision, "dep-r2", dep2, release2, ""))
	f.handoff(t, project, release2, "deploy", "deploy", 1, "succeeded", "")
	f.handoff(t, project, release2, "deploy", "verify", 1, "succeeded", "")
	view = f.journey(t, f.person, http.MethodGet, "/api/projects/"+project+"/journey", "")
	if view.Stage != "live" {
		t.Fatalf("release 2 live %+v", view.NextAction)
	}
	view = f.journey(t, f.person, http.MethodPost, "/api/projects/"+project+"/journey/actions", actionJSON("plan_next_release", view.Revision, "plan-2", "", release2, ""))
	if f.releaseState(t, release) != "superseded" || f.releaseState(t, release2) != "released" || view.Stage != "plan" {
		t.Fatalf("history r1 %s r2 %s stage %s", f.releaseState(t, release), f.releaseState(t, release2), view.Stage)
	}

	foreign := f.do(f.outsider, http.MethodGet, "/api/projects/"+project+"/journey", "")
	if foreign.Code != http.StatusNotFound {
		t.Fatalf("cross tenant %d %s", foreign.Code, foreign.Body.String())
	}
	if n := f.kindCount(t, f.otherTenant, "requirement"); n != 0 {
		t.Fatalf("kind leaked %d", n)
	}
	if n := f.journeyCount(t, f.otherTenant); n != 0 {
		t.Fatalf("journey rows leaked %d", n)
	}
}

func TestJourneyProfileAccessAndEnterprise(t *testing.T) {
	f := newFixture(t)
	project := f.node(t, "project", "PRJ-3", "Personal")
	view := f.journey(t, f.person, http.MethodGet, "/api/projects/"+project+"/journey", "")
	view = f.journey(t, f.person, http.MethodPut, "/api/projects/"+project+"/journey/profile", `{"profile":"personal","expected_revision":1}`)
	f.acceptBrief(t, project)
	view = f.journey(t, f.person, http.MethodPost, "/api/projects/"+project+"/journey/actions", actionJSON("confirm_brief", view.Revision, "p-brief", "", "", ""))
	if view.Stage != "requirements" || stateOf(view, "shape") != "skipped" {
		t.Fatalf("personal skip %+v shape %s", view.NextAction, stateOf(view, "shape"))
	}
	view = f.journey(t, f.person, http.MethodPut, "/api/projects/"+project+"/journey/profile", `{"profile":"professional","expected_revision":`+strconv.FormatInt(view.Revision, 10)+`}`)
	if view.Stage != "shape" || view.NextAction.Key != "decide" {
		t.Fatalf("profile reevaluates %+v", view.NextAction)
	}
	shape := f.grant(t, f.agent.ID, f.person.ID, journey.ScopeShape, project)
	if w := f.do(f.person, http.MethodPost, "/api/projects/"+project+"/journey/actions", actionJSON("park", view.Revision, "park-0", shape, "", "")); w.Code != http.StatusBadRequest {
		t.Fatalf("park without reason %d %s", w.Code, w.Body.String())
	}
	view = f.journey(t, f.person, http.MethodPost, "/api/projects/"+project+"/journey/actions", actionJSON("park", view.Revision, "park-1", shape, "", "waiting"))
	if view.Stage != "shape" || view.NextAction.Key != "reopen" || stateOf(view, "shape") != "current" {
		t.Fatalf("park %+v", view.NextAction)
	}
	reopen := f.grant(t, f.agent.ID, f.person.ID, journey.ScopeShape, project)
	view = f.journey(t, f.person, http.MethodPost, "/api/projects/"+project+"/journey/actions", actionJSON("reopen", view.Revision, "reopen-1", reopen, "", ""))
	if view.NextAction.Key != "decide" || f.decision(t, project) != "pending" || f.gateCount(t, project, "shape") != 2 {
		t.Fatalf("reopen decision %s gates %d next %+v", f.decision(t, project), f.gateCount(t, project, "shape"), view.NextAction)
	}
	view = f.journey(t, f.person, http.MethodPut, "/api/projects/"+project+"/journey/profile", `{"profile":"personal","expected_revision":`+strconv.FormatInt(view.Revision, 10)+`}`)
	if view.Stage != "requirements" || stateOf(view, "shape") != "skipped" || f.gateCount(t, project, "shape") != 2 {
		t.Fatalf("personal again %+v gates %d", view.NextAction, f.gateCount(t, project, "shape"))
	}

	accessProject := f.node(t, "project", "PRJ-4", "Access")
	view = f.journey(t, f.person, http.MethodGet, "/api/projects/"+accessProject+"/journey", "")
	view = f.journey(t, f.person, http.MethodPut, "/api/projects/"+accessProject+"/journey/profile", `{"profile":"personal","expected_revision":1}`)
	f.acceptBrief(t, accessProject)
	view = f.journey(t, f.person, http.MethodPost, "/api/projects/"+accessProject+"/journey/actions", actionJSON("confirm_brief", view.Revision, "a-brief", "", "", ""))
	f.agree(t, accessProject)
	release := f.release(t, accessProject, "REL-7", 1)
	f.ticket(t, accessProject, release, "TKT-7", "2", false, true)
	build := f.grant(t, f.agent.ID, f.person.ID, journey.ScopeBuild, release)
	view = f.journey(t, f.person, http.MethodGet, "/api/projects/"+accessProject+"/journey", "")
	view = f.journey(t, f.person, http.MethodPost, "/api/projects/"+accessProject+"/journey/actions", actionJSON("start_build", view.Revision, "a-build", build, release, ""))
	if !f.accessRequired(t, release) {
		t.Fatal("access change did not mark the release")
	}
	f.setReleaseState(t, release, "candidate")
	cand := f.grant(t, f.agent.ID, f.person.ID, journey.ScopeCandidate, release)
	view = f.journey(t, f.person, http.MethodGet, "/api/projects/"+accessProject+"/journey", "")
	view = f.journey(t, f.person, http.MethodPost, "/api/projects/"+accessProject+"/journey/actions", actionJSON("approve_candidate", view.Revision, "a-cand", cand, release, ""))
	deploy := f.grant(t, f.agent.ID, f.person.ID, journey.ScopeDeploy, release)
	view = f.journey(t, f.person, http.MethodPost, "/api/projects/"+accessProject+"/journey/actions", actionJSON("approve_deploy", view.Revision, "a-dep", deploy, release, ""))
	f.handoff(t, accessProject, release, "deploy", "deploy", 1, "succeeded", "")
	f.handoff(t, accessProject, release, "deploy", "verify", 1, "succeeded", "")
	view = f.journey(t, f.person, http.MethodGet, "/api/projects/"+accessProject+"/journey", "")
	if view.Stage != "access" || view.NextAction.Key != "approve_permit" || stateOf(view, "access") == "skipped" {
		t.Fatalf("access stage %+v state %s", view.NextAction, stateOf(view, "access"))
	}
	permit := f.grant(t, f.agent.ID, f.person.ID, journey.ScopeAccess, release)
	view = f.journey(t, f.person, http.MethodPost, "/api/projects/"+accessProject+"/journey/actions", actionJSON("approve_permit", view.Revision, "a-permit", permit, release, ""))
	if view.Stage != "access" || view.NextAction.Available || stateOf(view, "access") != "blocked" {
		t.Fatalf("permit waiting %+v %s", view.NextAction, stateOf(view, "access"))
	}
	f.accessApply(t, accessProject, release, true)
	view = f.journey(t, f.person, http.MethodGet, "/api/projects/"+accessProject+"/journey", "")
	if view.Stage != "live" || view.NextAction.Key != "plan_next_release" {
		t.Fatalf("after permit %+v", view.NextAction)
	}

	ent := f.node(t, "project", "PRJ-5", "Enterprise")
	view = f.journey(t, f.person, http.MethodGet, "/api/projects/"+ent+"/journey", "")
	view = f.journey(t, f.person, http.MethodPut, "/api/projects/"+ent+"/journey/profile", `{"profile":"enterprise","expected_revision":`+strconv.FormatInt(view.Revision, 10)+`}`)
	f.acceptBrief(t, ent)
	view = f.journey(t, f.person, http.MethodPost, "/api/projects/"+ent+"/journey/actions", actionJSON("confirm_brief", view.Revision, "e-brief", "", "", ""))
	f.setCap(t, ent, 20)
	expired := f.grant(t, f.agent.ID, f.person.ID, journey.ScopeShape, ent)
	f.expire(t, expired)
	if w := f.do(f.person, http.MethodPost, "/api/projects/"+ent+"/journey/actions", actionJSON("go", view.Revision, "e-exp", expired, "", "")); w.Code != http.StatusForbidden {
		t.Fatalf("expired %d %s", w.Code, w.Body.String())
	}
	revoked := f.grant(t, f.agent.ID, f.person.ID, journey.ScopeShape, ent)
	f.revoke(t, revoked)
	if w := f.do(f.person, http.MethodPost, "/api/projects/"+ent+"/journey/actions", actionJSON("go", view.Revision, "e-rev", revoked, "", "")); w.Code != http.StatusForbidden {
		t.Fatalf("revoked %d %s", w.Code, w.Body.String())
	}
	goGate := f.grant(t, f.agent.ID, f.person.ID, journey.ScopeShape, ent)
	view = f.journey(t, f.person, http.MethodPost, "/api/projects/"+ent+"/journey/actions", actionJSON("go", view.Revision, "e-go", goGate, "", ""))
	f.agree(t, ent)
	rel := f.release(t, ent, "REL-6", 1)
	f.ticket(t, ent, rel, "TKT-6", "1", false, false)
	req := f.node(t, "requirement", "REQ-1", "Draft")
	if _, err := f.db.Admin.Exec(t.Context(), `
		INSERT INTO journey_requirements (tenant_id, requirement_node_id, project_node_id, kind, revision, status)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'functional', 1, 'draft')`, f.tenant, req, ent); err != nil {
		t.Fatal(err)
	}
	buildE := f.grant(t, f.agent.ID, f.person.ID, journey.ScopeBuild, rel)
	view = f.journey(t, f.person, http.MethodGet, "/api/projects/"+ent+"/journey", "")
	view = f.journey(t, f.person, http.MethodPost, "/api/projects/"+ent+"/journey/actions", actionJSON("start_build", view.Revision, "e-build", buildE, rel, ""))
	f.setReleaseState(t, rel, "candidate")
	self := f.grant(t, f.agent.ID, f.person.ID, journey.ScopeCandidate, rel)
	view = f.journey(t, f.person, http.MethodGet, "/api/projects/"+ent+"/journey", "")
	if view.NextAction.Reason == "" || view.NextAction.Available {
		t.Fatalf("drafts should block %+v", view.NextAction)
	}
	if w := f.do(f.person, http.MethodPost, "/api/projects/"+ent+"/journey/actions", actionJSON("approve_candidate", view.Revision, "e-self", self, rel, "")); w.Code != http.StatusConflict {
		t.Fatalf("draft candidate %d %s", w.Code, w.Body.String())
	}
	if _, err := f.db.Admin.Exec(t.Context(), `DELETE FROM journey_requirements WHERE requirement_node_id = $1::uuid`, req); err != nil {
		t.Fatal(err)
	}
	view = f.journey(t, f.person, http.MethodGet, "/api/projects/"+ent+"/journey", "")
	if w := f.do(f.person, http.MethodPost, "/api/projects/"+ent+"/journey/actions", actionJSON("approve_candidate", view.Revision, "e-self", self, rel, "")); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "different person") {
		t.Fatalf("same reviewer %d %s", w.Code, w.Body.String())
	}
	independent := f.grant(t, f.agent.ID, f.other.ID, journey.ScopeCandidate, rel)
	if w := f.do(f.person, http.MethodPost, "/api/projects/"+ent+"/journey/actions", actionJSON("approve_candidate", view.Revision, "e-ada", independent, rel, "")); w.Code != http.StatusForbidden {
		t.Fatalf("builder cannot use the reviewer's approval %d %s", w.Code, w.Body.String())
	}
	view = f.journey(t, f.other, http.MethodPost, "/api/projects/"+ent+"/journey/actions", actionJSON("approve_candidate", view.Revision, "e-bea", independent, rel, ""))
	if view.Stage != "deploy" {
		t.Fatalf("independent review %+v", view.NextAction)
	}
}

type fixture struct {
	db          *dbtest.DB
	mux         *http.ServeMux
	tenant      string
	otherTenant string
	person      tenant.Principal
	other       tenant.Principal
	agent       tenant.Principal
	outsider    tenant.Principal
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{db: dbtest.Open(t)}
	f.tenant = insertTenant(t, f.db.Admin, "journey-a")
	f.otherTenant = insertTenant(t, f.db.Admin, "journey-b")
	f.person = insertPrincipal(t, f.db.Admin, f.tenant, tenant.Person, "Ada")
	f.other = insertPrincipal(t, f.db.Admin, f.tenant, tenant.Person, "Bea")
	f.agent = insertPrincipal(t, f.db.Admin, f.tenant, tenant.Agent, "Agent")
	f.outsider = insertPrincipal(t, f.db.Admin, f.otherTenant, tenant.Person, "Cid")
	f.mux = http.NewServeMux()
	journey.New(f.db.App).Mount(f.mux)
	return f
}

func insertTenant(t *testing.T, pool *pgxpool.Pool, slug string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(t.Context(), `INSERT INTO tenants (slug, name) VALUES ($1, $1) RETURNING id::text`, slug).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func insertPrincipal(t *testing.T, pool *pgxpool.Pool, tenantID string, kind tenant.PrincipalKind, name string) tenant.Principal {
	t.Helper()
	p := tenant.Principal{TenantID: tenantID, Kind: kind, Name: name}
	if err := pool.QueryRow(t.Context(), `
		INSERT INTO principals (tenant_id, kind, name) VALUES ($1::uuid, $2, $3) RETURNING id::text`,
		tenantID, string(kind), name).Scan(&p.ID); err != nil {
		t.Fatal(err)
	}
	// Handlers see project data only through a binding (ADR-003 P2).
	dbtest.BindRoleWith(t, pool, tenantID, p.ID, "member")
	return p
}

func (f *fixture) node(t *testing.T, kind, key, title string) string {
	t.Helper()
	var id string
	if err := f.db.Admin.QueryRow(t.Context(), `
		INSERT INTO nodes (tenant_id, key, kind_id, title)
		SELECT $1::uuid, $2, id, $4 FROM node_kinds
		WHERE tenant_id = $1::uuid AND slug = $3
		RETURNING id::text`, f.tenant, key, kind, title).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *fixture) do(p tenant.Principal, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req = req.WithContext(tenant.WithPrincipal(req.Context(), p))
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, req)
	return w
}

func (f *fixture) anon(method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, req)
	return w
}

func (f *fixture) journey(t *testing.T, p tenant.Principal, method, path, body string) journey.Journey {
	t.Helper()
	w := f.do(p, method, path, body)
	if w.Code != http.StatusOK {
		t.Fatalf("%s %s %d %s", method, path, w.Code, w.Body.String())
	}
	var out journey.Journey
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func actionJSON(action string, revision int64, key, approval, release, reason string) string {
	payload := map[string]any{
		"action": action, "expected_revision": revision, "idempotency_key": key,
	}
	if approval != "" {
		payload["approval_request_id"] = approval
	}
	if release != "" {
		payload["release_id"] = release
	}
	if reason != "" {
		payload["reason"] = reason
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func (f *fixture) grant(t *testing.T, agent, person, scope, resource string) string {
	t.Helper()
	var id string
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenant, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `
			INSERT INTO approval_requests (
				tenant_id, proposed_by_principal_id, agent_principal_id,
				scope, resource_kind, resource_id, rationale, expires_at)
			VALUES ($1::uuid, $2::uuid, $2::uuid, $3, 'node', $4::uuid, 'journey gate', now() + interval '2 hours')
			RETURNING id::text`, f.tenant, agent, scope, resource).Scan(&id); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `
			INSERT INTO approval_decisions (tenant_id, request_id, decided_by_principal_id, decision)
			VALUES ($1::uuid, $2::uuid, $3::uuid, 'approved')`, f.tenant, id, person); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `
			INSERT INTO agent_permission_grants (
				tenant_id, approval_request_id, agent_principal_id, scope, resource_kind, resource_id, valid_until)
			SELECT tenant_id, id, agent_principal_id, scope, resource_kind, resource_id, expires_at
			FROM approval_requests WHERE id = $1::uuid`, id)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *fixture) acceptBrief(t *testing.T, project string) {
	t.Helper()
	var eventID int64
	if err := f.db.Admin.QueryRow(t.Context(), `
		SELECT id FROM events WHERE tenant_id = $1::uuid ORDER BY id DESC LIMIT 1`, f.tenant).Scan(&eventID); err != nil {
		t.Fatal(err)
	}
	var draft string
	if err := f.db.Admin.QueryRow(t.Context(), `
		INSERT INTO intake_drafts (
			tenant_id, project_node_id, kind, target_node_id, title, body, base_event_id,
			proposed_by_principal_id, idempotency_key)
		VALUES ($1::uuid, $2::uuid, 'brief', $2::uuid, 'Brief', 'A short brief', $3, $4::uuid, 'brief')
		RETURNING id::text`, f.tenant, project, eventID, f.agent.ID).Scan(&draft); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(t.Context(), `
		INSERT INTO intake_draft_acceptances (
			tenant_id, draft_id, project_node_id, accepted_by_principal_id, target_node_id, event_id)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $3::uuid, $5)`,
		f.tenant, draft, project, f.person.ID, eventID); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) setCap(t *testing.T, project string, hours int) {
	t.Helper()
	if _, err := f.db.Admin.Exec(t.Context(), `
		UPDATE nodes SET fields = jsonb_build_object('cap_hours', $2::int) WHERE id = $1::uuid`, project, hours); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) agree(t *testing.T, project string) {
	t.Helper()
	if _, err := f.db.Admin.Exec(t.Context(), `
		UPDATE journey_projects
		SET requirements_revision = 1, agreed_requirements_revision = 1,
		    agreed_requirements_digest_sha256 = $2
		WHERE project_node_id = $1::uuid`, project, digest); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) release(t *testing.T, project, key string, number int) string {
	t.Helper()
	id := f.node(t, "release", key, "Release")
	if _, err := f.db.Admin.Exec(t.Context(), `
		INSERT INTO journey_releases (tenant_id, release_node_id, project_node_id, number, state)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, 'planning')`, f.tenant, id, project, number); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(t.Context(), `
		UPDATE journey_projects SET current_release_node_id = $2::uuid WHERE project_node_id = $1::uuid`, project, id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *fixture) ticket(t *testing.T, project, release, key, hours string, scope, access bool) {
	t.Helper()
	id := f.node(t, "ticket", key, key)
	if _, err := f.db.Admin.Exec(t.Context(), `
		INSERT INTO journey_tickets (
			tenant_id, ticket_node_id, project_node_id, release_node_id, walker_position,
			source, scope_revision_required, access_change, estimated_hours)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 0, 'requirements', $5, $6, $7::numeric)`,
		f.tenant, id, project, release, scope, access, hours); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) setTicketHours(t *testing.T, key, hours string) {
	t.Helper()
	if _, err := f.db.Admin.Exec(t.Context(), `
		UPDATE journey_tickets SET estimated_hours = $2::numeric
		WHERE ticket_node_id = (SELECT id FROM nodes WHERE tenant_id = $1::uuid AND key = $3)`,
		f.tenant, hours, key); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) setTicketState(t *testing.T, key, state string) {
	t.Helper()
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE nodes SET state=$3 WHERE tenant_id=$1::uuid AND key=$2`, f.tenant, key, state); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) setScope(t *testing.T, key string, scope bool) {
	t.Helper()
	if _, err := f.db.Admin.Exec(t.Context(), `
		UPDATE journey_tickets SET scope_revision_required = $2
		WHERE ticket_node_id = (SELECT id FROM nodes WHERE tenant_id = $1::uuid AND key = $3)`,
		f.tenant, scope, key); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) setReleaseState(t *testing.T, release, state string) {
	t.Helper()
	if _, err := f.db.Admin.Exec(t.Context(), `
		UPDATE journey_releases SET state = $2 WHERE release_node_id = $1::uuid`, release, state); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) accessRequired(t *testing.T, release string) bool {
	t.Helper()
	var required bool
	if err := f.db.Admin.QueryRow(t.Context(), `
		SELECT access_required FROM journey_releases WHERE release_node_id = $1::uuid`, release).Scan(&required); err != nil {
		t.Fatal(err)
	}
	return required
}

func (f *fixture) releaseState(t *testing.T, release string) string {
	t.Helper()
	var state string
	if err := f.db.Admin.QueryRow(t.Context(), `
		SELECT state FROM journey_releases WHERE release_node_id = $1::uuid`, release).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

func (f *fixture) releaseCount(t *testing.T, project string) int {
	t.Helper()
	var n int
	if err := f.db.Admin.QueryRow(t.Context(), `
		SELECT count(*) FROM journey_releases WHERE project_node_id = $1::uuid`, project).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (f *fixture) handoff(t *testing.T, project, release, stage, operation string, attempt int, outcome, blocker string) {
	t.Helper()
	state := outcome
	if outcome == "succeeded" {
		state = "succeeded"
	}
	var blockerArg any
	if blocker != "" {
		blockerArg = blocker
	}
	kind := "deployment"
	ceiling := "deployment"
	if operation == "verify" {
		kind, ceiling = "verification", "verification"
	}
	var id string
	if err := f.db.Admin.QueryRow(t.Context(), `
		INSERT INTO stage_handoffs (
			tenant_id, project_node_id, release_node_id, stage, operation, plugin_id,
			requested_by_principal_id, idempotency_key, attempt, authority_epoch, journey_revision,
			plan_digest, predecessor_digest, context_digest, prerequisite_seal_sha256,
			evidence_ceiling, state, expires_at)
		VALUES (
			$1::uuid, $2::uuid, $3::uuid, $4, $5, 'pharos', $6::uuid, $7, $8, 1, 1,
			$9, $9, $9, $9, ARRAY[$10]::text[], $11, now() + interval '1 day')
		RETURNING id::text`,
		f.tenant, project, release, stage, operation, f.person.ID,
		operation+"-"+string(rune('a'+attempt))+release[:8], attempt, digest, ceiling, state).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(t.Context(), `
		INSERT INTO stage_handoff_evidence (
			tenant_id, handoff_id, sequence, authority_epoch, kind, outcome, observed_at,
			workflow, environment, version_scheme, version, release_channel, release_sequence,
			artifact_digest_sha256, commit_digest, manifest_coordinate, manifest_digest_sha256)
		VALUES (
			$1::uuid, $2::uuid, 1, 1, $3, $4, now(),
			'deploy', 'production', 'inspr-calendar-v2', '260923120000.0.0', 'stable', 1,
			$5, 'abc', 'coord', $5)`,
		f.tenant, id, kind, outcome, digest); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(t.Context(), `
		INSERT INTO stage_handoff_results (
			tenant_id, handoff_id, outcome, terminal_sequence, authority_epoch, prerequisite_seal_sha256, blocker_code)
		VALUES ($1::uuid, $2::uuid, $3, 1, 1, $4, $5)`,
		f.tenant, id, outcome, digest, blockerArg); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) events(t *testing.T, eventType string) int {
	t.Helper()
	var n int
	if err := f.db.Admin.QueryRow(t.Context(), `
		SELECT count(*) FROM events WHERE tenant_id = $1::uuid AND type = $2`, f.tenant, eventType).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (f *fixture) eventCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE tenant_id = $1::uuid`, f.tenant).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (f *fixture) kindCount(t *testing.T, tenantID, slug string) int {
	t.Helper()
	var n int
	if err := f.db.Admin.QueryRow(t.Context(), `
		SELECT count(*) FROM node_kinds WHERE tenant_id = $1::uuid AND slug = $2`, tenantID, slug).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (f *fixture) decision(t *testing.T, project string) string {
	t.Helper()
	var decision string
	if err := f.db.Admin.QueryRow(t.Context(), `
		SELECT decision FROM journey_projects WHERE project_node_id = $1::uuid`, project).Scan(&decision); err != nil {
		t.Fatal(err)
	}
	return decision
}

func (f *fixture) gateCount(t *testing.T, project, gate string) int {
	t.Helper()
	var n int
	if err := f.db.Admin.QueryRow(t.Context(), `
		SELECT count(*) FROM journey_gates WHERE project_node_id = $1::uuid AND gate = $2`, project, gate).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (f *fixture) expire(t *testing.T, approval string) {
	t.Helper()
	if _, err := f.db.Admin.Exec(t.Context(), `
		UPDATE agent_permission_grants SET valid_until = now() - interval '1 minute'
		WHERE approval_request_id = $1::uuid`, approval); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) revoke(t *testing.T, approval string) {
	t.Helper()
	if _, err := f.db.Admin.Exec(t.Context(), `
		UPDATE agent_permission_grants SET revoked_at = now()
		WHERE approval_request_id = $1::uuid`, approval); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) accessApply(t *testing.T, project, release string, ready bool) {
	t.Helper()
	outcome := "succeeded"
	state := "succeeded"
	if !ready {
		outcome, state = "failed", "failed"
	}
	var id string
	if err := f.db.Admin.QueryRow(t.Context(), `
		INSERT INTO stage_handoffs (
			tenant_id, project_node_id, release_node_id, stage, operation, plugin_id,
			requested_by_principal_id, idempotency_key, attempt, authority_epoch, journey_revision,
			plan_digest, predecessor_digest, context_digest, prerequisite_seal_sha256,
			evidence_ceiling, state, expires_at)
		VALUES (
			$1::uuid, $2::uuid, $3::uuid, 'access', 'apply', 'janus', $4::uuid, $5, 1, 1, 1,
			$6, $6, $6, $6, ARRAY['authorization','credential_handoff']::text[], $7, now() + interval '1 day')
		RETURNING id::text`,
		f.tenant, project, release, f.agent.ID, "apply-"+release[:8], digest, state).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(t.Context(), `
		INSERT INTO stage_handoff_evidence (
			tenant_id, handoff_id, sequence, authority_epoch, kind, outcome, observed_at, authorized)
		VALUES ($1::uuid, $2::uuid, 1, 1, 'authorization', $3, now(), $4)`,
		f.tenant, id, outcome, ready); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(t.Context(), `
		INSERT INTO stage_handoff_evidence (
			tenant_id, handoff_id, sequence, authority_epoch, kind, outcome, observed_at, credential_ready)
		VALUES ($1::uuid, $2::uuid, 2, 1, 'credential_handoff', $3, now(), $4)`,
		f.tenant, id, outcome, ready); err != nil {
		t.Fatal(err)
	}
	var blocker any
	if !ready {
		blocker = "policy_refused"
	}
	if _, err := f.db.Admin.Exec(t.Context(), `
		INSERT INTO stage_handoff_results (
			tenant_id, handoff_id, outcome, terminal_sequence, authority_epoch, prerequisite_seal_sha256, blocker_code)
		VALUES ($1::uuid, $2::uuid, $3, 2, 1, $4, $5)`,
		f.tenant, id, outcome, digest, blocker); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) journeyCount(t *testing.T, tenantID string) int {
	t.Helper()
	var n int
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM journey_projects`).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func stateOf(view journey.Journey, key string) string {
	for _, stage := range view.Stages {
		if stage.Key == key {
			return stage.State
		}
	}
	return ""
}
