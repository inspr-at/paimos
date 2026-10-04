// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"gopkg.in/yaml.v3"
)

func prefDoc(t *testing.T, p tenant.Principal) preferenceDocument {
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error { return modelprefs.SeedKinds(t.Context(), tx, p.TenantID) }); err != nil {
		t.Fatal(err)
	}
	return decode[preferenceDocument](t, &p, "GET", "/api/model-preferences", "", 200)
}
func prefRowBody(revision int64, normal, complex modelprefs.Cell, locked bool) string {
	raw, _ := json.Marshal(map[string]any{"revision": revision, "normal": normal, "complex": complex, "locked": locked})
	return string(raw)
}
func kindID(t *testing.T, doc preferenceDocument, slug string) string {
	t.Helper()
	for _, k := range doc.Kinds {
		if k.Slug == slug {
			return k.ID
		}
	}
	t.Fatalf("missing kind %s", slug)
	return ""
}
func expectPrefError(t *testing.T, p tenant.Principal, method, path, body string, status int, code string) {
	t.Helper()
	got, raw := call(t, &p, method, path, body)
	if got != status || !strings.Contains(string(raw), code) {
		t.Fatalf("%s %s = %d %s; want %d %s", method, path, got, raw, status, code)
	}
}
func TestPreferenceWritesRevisionLocksAndErrors(t *testing.T) {
	reset(t)
	admin := makePrincipal(t, "prefs-api", "person", "Admin", []string{"admin"})
	member := addPrincipal(t, admin.TenantID, "person", "Member", []string{"member"})
	agent := addPrincipal(t, admin.TenantID, "agent", "Agent", []string{"admin"})
	agent.Scopes = []string{"models.read", "models.manage", "model_prefs.manage"}
	doc := prefDoc(t, admin)
	profiles := decode[[]Profile](t, &admin, "GET", "/api/models", "", 200)
	normal := modelprefs.Cell{Mode: "pinned", ProfileID: profileBySlug(profiles, "codex-sol-high").ID}
	complex := modelprefs.Cell{Mode: "latest", Family: "openai", Line: "sol", Effort: "xhigh"}
	if normal.ProfileID == "" {
		t.Fatal("missing normal profile")
	}
	backend := kindID(t, doc, "backend")
	review := kindID(t, doc, "review")
	expectPrefError(t, admin, "PUT", "/api/model-preferences/levels/default", `{}`, 400, "revision_required")
	expectPrefError(t, admin, "DELETE", "/api/model-preferences/levels/default", "", 400, "revision_required")
	oversized, err := json.Marshal(map[string]any{"revision": 0, "rows": make([]preferenceRow, maxPreferenceKinds+1)})
	if err != nil {
		t.Fatal(err)
	}
	expectPrefError(t, admin, "PUT", "/api/model-preferences/levels/default", string(oversized), 413, "too_many_rows")
	expectPrefError(t, member, "PUT", "/api/model-preferences/levels/default", `{"revision":0,"residency":"any"}`, 403, "permission")
	for _, route := range []struct{ method, path, body string }{{"PUT", "/api/model-preferences/levels/person", `{"revision":0,"residency":"any"}`}, {"DELETE", "/api/model-preferences/levels/person?revision=0", ""}, {"PUT", "/api/model-preferences/levels/person/rows/" + backend, prefRowBody(0, normal, complex, false)}, {"DELETE", "/api/model-preferences/levels/person/rows/" + backend + "?revision=0", ""}, {"POST", "/api/work-kinds", `{"label":"Agent kind"}`}, {"PATCH", "/api/work-kinds/" + backend, `{"hint":"x"}`}, {"DELETE", "/api/work-kinds/" + backend, ""}, {"POST", "/api/work-kinds/" + backend + "/restore", ""}, {"POST", "/api/models/" + normal.ProfileID + "/retire", `{"reason":"x"}`}, {"DELETE", "/api/models/" + normal.ProfileID + "/retire", ""}} {
		expectPrefError(t, agent, route.method, route.path, route.body, 403, "person_required")
	}
	saved := decode[preferenceWriteResult](t, &admin, "PUT", "/api/model-preferences/levels/default/rows/"+backend, prefRowBody(0, normal, complex, false), 200)
	if saved.Revision != 1 || len(saved.Level.Rows) != 1 {
		t.Fatal(saved)
	}
	expectPrefError(t, admin, "PUT", "/api/model-preferences/levels/default/rows/"+backend, prefRowBody(0, normal, complex, false), 409, "stale_revision")
	locked := decode[preferenceWriteResult](t, &member, "PUT", "/api/model-preferences/levels/person/rows/"+backend, `{"revision":0,"locked":true}`, 200)
	if len(locked.Level.Rows) != 1 || !locked.Level.Rows[0].Locked || locked.Level.Rows[0].Normal != normal || locked.Level.Rows[0].Complex != complex {
		t.Fatalf("copy-on-lock lost selectors: %+v", locked)
	}
	expectPrefError(t, admin, "PUT", "/api/model-preferences/levels/default/rows/"+review, prefRowBody(1, normal, normal, false), 422, "review_floor")
	unknown := normal
	unknown.ProfileID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	expectPrefError(t, admin, "PUT", "/api/model-preferences/levels/default/rows/"+backend, prefRowBody(1, unknown, complex, false), 422, "unknown_profile")
	expectPrefError(t, admin, "PUT", "/api/model-preferences/levels/default/rows/"+backend, prefRowBody(1, modelprefs.Cell{Mode: "latest", Family: "openai", Line: "unknown", Effort: "high"}, complex, false), 422, "unknown_line")
	expectPrefError(t, admin, "PUT", "/api/model-preferences/levels/default/rows/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", prefRowBody(1, normal, complex, false), 422, "unknown_kind")
	var project string
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, admin.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'PREF-1','Preferences project' FROM node_kinds WHERE slug='project' RETURNING id::text`, admin.TenantID).Scan(&project)
	}); err != nil {
		t.Fatal(err)
	}
	expectPrefError(t, member, "PUT", "/api/model-preferences/levels/project/rows/"+backend+"?project_id="+project, prefRowBody(0, normal, complex, false), 403, "permission")
	expectPrefError(t, admin, "PUT", "/api/model-preferences/levels/project/rows/"+backend+"?project_id="+project, prefRowBody(0, normal, complex, true), 422, "lock_at_project")
	// Default row lock blocks narrower model writes; provider choices remain free.
	decode[preferenceWriteResult](t, &admin, "PUT", "/api/model-preferences/levels/default/rows/"+backend, prefRowBody(1, normal, complex, true), 200)
	expectPrefError(t, member, "PUT", "/api/model-preferences/levels/person/rows/"+backend, prefRowBody(1, normal, complex, false), 422, "locked_above")
	decode[preferenceWriteResult](t, &admin, "PUT", "/api/model-preferences/levels/default", `{"revision":2,"residency":"eu","residency_locked":true}`, 200)
	lower := decode[preferenceWriteResult](t, &member, "PUT", "/api/model-preferences/levels/person", `{"revision":1,"residency":"any"}`, 200)
	if lower.Residency.Value != "any" || !lower.Residency.LoosenedLock {
		t.Fatal("looser locked choice must apply and warn", lower)
	}
	lowerDoc := prefDoc(t, member)
	if lowerDoc.ResidencyLockMode != "warn" || !lowerDoc.Views["person"].Residency.LoosenedLock || lowerDoc.Can["edit_default"] {
		t.Fatal(lowerDoc)
	}
	reset := decode[preferenceWriteResult](t, &member, "DELETE", "/api/model-preferences/levels/person?revision=2", "", 200)
	if reset.Revision != 3 || len(reset.Level.Rows) != 0 || reset.Level.Residency != nil || reset.Level.PrefsLocked {
		t.Fatal(reset)
	}
	if eventCount(t, admin, "model.preferences_changed") != 6 {
		t.Fatal("writes or rejected requests recorded wrong events")
	}
}

func TestPreferenceProjectOnlyManager(t *testing.T) {
	reset(t)
	admin := makePrincipal(t, "prefs-project-grant", "person", "Admin", []string{"admin"})
	manager := addPrincipal(t, admin.TenantID, "person", "Project manager", nil)
	prefDoc(t, admin)
	var project string
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, admin.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'PM-1','Managed project' FROM node_kinds WHERE slug='project' RETURNING id::text`, admin.TenantID).Scan(&project); err != nil {
			return err
		}
		var role string
		if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'prefs_manager','Preferences manager') RETURNING id::text`, admin.TenantID).Scan(&role); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'nodes.read'),($1,$2,'model_prefs.manage')`, admin.TenantID, role); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) VALUES($1,$2,$3,'project',$4)`, admin.TenantID, manager.ID, role, project)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	pattern := "PUT /api/model-preferences/levels/{level}"
	scope, scoped, err := authz.ResolveRouteScope(t.Context(), appPool, pattern, "/api/model-preferences/levels/project")
	if err != nil || !scoped || !scope.AnyProject {
		t.Fatal("project grant cannot reach middleware", scope, scoped, err)
	}
	if err := authz.RequirePattern(authz.BindPool(tenant.WithPrincipal(t.Context(), manager), appPool), pattern, scope); err != nil {
		t.Fatal("manager without models.read cannot reach write", err)
	}
	decode[preferenceWriteResult](t, &manager, "PUT", "/api/model-preferences/levels/project?project_id="+project, `{"revision":0,"residency":"eu"}`, 200)
	expectPrefError(t, manager, "PUT", "/api/model-preferences/levels/default", `{"revision":0,"residency":"eu"}`, 403, "permission")
	expectPrefError(t, manager, "PUT", "/api/model-preferences/levels/project?project_id=aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", `{"revision":0,"residency":"eu"}`, 403, "permission")
}
func TestWorkKindLifecyclePaginationAndTenantIsolation(t *testing.T) {
	reset(t)
	admin := makePrincipal(t, "kinds-api", "person", "Admin", []string{"admin"})
	other := makePrincipal(t, "kinds-other", "person", "Other", []string{"admin"})
	doc := prefDoc(t, admin)
	first := decode[workKind](t, &admin, "POST", "/api/work-kinds", `{"label":"Firmware","hint":"Boards"}`, 201)
	second := decode[workKind](t, &admin, "POST", "/api/work-kinds", `{"label":"Firmware"}`, 201)
	if first.Slug != "firmware" || second.Slug != "firmware-2" {
		t.Fatal(first, second)
	}
	edited := decode[workKind](t, &admin, "PATCH", "/api/work-kinds/"+first.ID, `{"label":"Device code","position":4}`, 200)
	if edited.Slug != "firmware" || edited.Label != "Device code" {
		t.Fatal(edited)
	}
	decode[workKind](t, &admin, "DELETE", "/api/work-kinds/"+first.ID, "", 200)
	reused := decode[workKind](t, &admin, "POST", "/api/work-kinds", `{"label":"Firmware"}`, 201)
	if reused.Slug != "firmware" {
		t.Fatal(reused)
	}
	expectPrefError(t, admin, "POST", "/api/work-kinds/"+first.ID+"/restore", "", 409, "slug_taken")
	decode[workKind](t, &admin, "DELETE", "/api/work-kinds/"+reused.ID, "", 200)
	restored := decode[workKind](t, &admin, "POST", "/api/work-kinds/"+first.ID+"/restore", "", 200)
	if restored.ArchivedAt != nil {
		t.Fatal(restored)
	}
	if eventCount(t, admin, "work_kind.restored") != 1 {
		t.Fatal("restore did not record its own event")
	}
	for _, method := range []string{"DELETE", "PATCH", "POST"} {
		path := "/api/work-kinds/" + kindID(t, doc, "security")
		body := ""
		if method == "PATCH" {
			body = `{"label":"Renamed"}`
		}
		if method == "POST" {
			path += "/restore"
		}
		expectPrefError(t, admin, method, path, body, 422, "system_kind")
	}
	expectPrefError(t, other, "PATCH", "/api/work-kinds/"+first.ID, `{"hint":"foreign"}`, 404, "not found")
	page := decode[workKindPage](t, &admin, "GET", "/api/work-kinds?limit=1", "", 200)
	if len(page.Items) != 1 || page.NextCursor == nil {
		t.Fatal(page)
	}
	next := decode[workKindPage](t, &admin, "GET", "/api/work-kinds?limit=1&cursor="+*page.NextCursor, "", 200)
	if len(next.Items) != 1 || next.Items[0].ID == page.Items[0].ID {
		t.Fatal(next)
	}
	expectPrefError(t, admin, "GET", "/api/work-kinds?limit=101", "", 400, "invalid_limit")
	otherDoc := prefDoc(t, other)
	for _, k := range otherDoc.Kinds {
		if k.ID == first.ID || k.Slug == "firmware" {
			t.Fatal("kind leaked")
		}
	}
	if eventCount(t, admin, "work_kind.created") != 3 || eventCount(t, admin, "work_kind.archived") != 2 || eventCount(t, other, "work_kind.created") != 0 {
		t.Fatal("kind audit mismatch")
	}
}
func TestRetirementAndResolveCompatibilityGolden(t *testing.T) {
	reset(t)
	admin := makePrincipal(t, "resolve-prefs", "person", "Admin", []string{"admin"})
	other := addPrincipal(t, admin.TenantID, "person", "Other", []string{"member"})
	agent := addPrincipal(t, admin.TenantID, "agent", "Reader", []string{"admin"})
	agent.KeyCreatorID = admin.ID
	agent.Scopes = []string{"models.read", "nodes.read"}
	dbtest.BindRole(t, testDB, admin.TenantID, agent.ID, "admin")
	doc := prefDoc(t, admin)
	profiles := decode[[]Profile](t, &admin, "GET", "/api/models", "", 200)
	// Exact existing shape, including part A's additive trace, is the golden.
	var golden []byte
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, admin.TenantID, func(tx pgx.Tx) error {
		now, err := dbNow(t.Context(), tx)
		if err != nil {
			return err
		}
		resolution, err := resolveRole(t.Context(), tx, resolveQuery{Role: "build"}, now)
		if err != nil {
			return err
		}
		_, trace, _, err := placementTrace(t.Context(), tx, WorkQuery{Role: "build", PersonID: &admin.ID})
		if err != nil {
			return err
		}
		golden, err = json.Marshal(struct {
			Resolution
			Trace PreferenceTrace `json:"trace"`
		}{resolution, trace})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	status, raw := call(t, &admin, "GET", "/api/models/resolve?role=build", "")
	if status != 200 || !bytes.Equal(bytes.TrimSpace(raw), golden) {
		t.Fatalf("legacy golden changed: %d\n%s\n%s", status, raw, golden)
	}
	chosen := profileBySlug(profiles, "codex-sol-high")
	decode[preferenceWriteResult](t, &admin, "PUT", "/api/model-preferences/levels/person/rows/"+kindID(t, doc, "backend"), prefRowBody(0, modelprefs.Cell{Mode: "pinned", ProfileID: chosen.ID}, modelprefs.Cell{Mode: "pinned", ProfileID: chosen.ID}, false), 200)
	for _, p := range []tenant.Principal{admin, agent} {
		expectPrefError(t, p, "GET", "/api/models/resolve?role=build&area=backend&person_id="+other.ID, "", 403, "person_not_caller")
	}
	operator := agent
	operator.KeyCreatorID = ""
	expectPrefError(t, operator, "GET", "/api/models/resolve?role=build&area=backend&person_id="+admin.ID, "", 403, "person_not_caller")
	resolved := decode[struct {
		WorkResolution
		Preference PreferenceDecision `json:"preference"`
	}](t, &operator, "GET", "/api/models/resolve?role=build&area=backend", "", 200)
	if resolved.Preference.PersonID != nil {
		t.Fatal("operator got You level")
	}
	decode[map[string]any](t, &admin, "POST", "/api/models/"+chosen.ID+"/retire", `{"reason":"Superseded"}`, 200)
	retired := decode[WorkResolution](t, &admin, "GET", "/api/models/resolve?role=build&area=backend", "", 200)
	if retired.Profile != nil && retired.Profile.ID == chosen.ID || !strings.Contains(retired.Trace.Fallback, "retired") {
		t.Fatal("retired pin selected", retired)
	}
	decode[map[string]any](t, &admin, "DELETE", "/api/models/"+chosen.ID+"/retire", "", 200)
	if eventCount(t, admin, "model.profile_retired") != 1 || eventCount(t, admin, "model.profile_restored") != 1 {
		t.Fatal("retirement audit")
	}
	// Calls with no new parameters stay on the same advisory ladder afterwards.
	_, raw = call(t, &admin, "GET", "/api/models/resolve?role=build", "")
	if !bytes.Equal(bytes.TrimSpace(raw), golden) {
		t.Fatal("preferences changed legacy response", string(raw))
	}
	expectPrefError(t, admin, "GET", "/api/models/resolve?role=review-gate&area=backend", "", 400, "author_family")
}

func TestPreferenceTicketResolutionUsesStoredPlacement(t *testing.T) {
	reset(t)
	admin := makePrincipal(t, "prefs-ticket", "person", "Admin", []string{"admin"})
	other := makePrincipal(t, "prefs-ticket-other", "person", "Other", []string{"admin"})
	doc := prefDoc(t, admin)
	profiles := decode[[]Profile](t, &admin, "GET", "/api/models", "", 200)
	normal := modelprefs.Cell{Mode: "pinned", ProfileID: profileBySlug(profiles, "codex-sol-high").ID}
	complex := modelprefs.Cell{Mode: "pinned", ProfileID: profileBySlug(profiles, "codex-sol-xhigh").ID}
	decode[preferenceWriteResult](t, &admin, "PUT", "/api/model-preferences/levels/person/rows/"+kindID(t, doc, "backend"), prefRowBody(0, normal, complex, false), 200)
	var project, ticket string
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, admin.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'TKT-1','Project' FROM node_kinds WHERE slug='project' RETURNING id::text`, admin.TenantID).Scan(&project); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id,project_id,fields) SELECT $1,id,'TKT-2','Complex backend',$2,$2,'{"area":"backend","complexity":"L","complexity_source":"manual","route_role":"build-hard","residency":"eu"}'::jsonb FROM node_kinds WHERE slug='ticket' RETURNING id::text`, admin.TenantID, project).Scan(&ticket)
	}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"TKT-2", ticket} {
		result := decode[struct {
			WorkResolution
			Preference PreferenceDecision `json:"preference"`
		}](t, &admin, "GET", "/api/models/resolve?ticket="+key+"&area=other&complexity=S", "", 200)
		trace := result.Preference
		if result.Role != "build-hard" || trace.Kind.Slug != "backend" || trace.Complexity.Bucket != "complex" || trace.Complexity.Source != "manual" || trace.Cell.SetBy != "person" || trace.Cell.Selector != complex || trace.PersonID == nil || *trace.PersonID != admin.ID || trace.ProjectID == nil || *trace.ProjectID != project || trace.Residency.TicketRequirement != "eu" || result.Residency != "eu" {
			t.Fatalf("ticket placement did not win: %+v", result)
		}
	}
	expectPrefError(t, other, "GET", "/api/models/resolve?ticket="+ticket, "", 404, "not found")
	expectPrefError(t, admin, "GET", "/api/models/resolve?ticket="+ticket+"&project_id=aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "", 400, "ticket_project_mismatch")
	review := decode[struct {
		WorkResolution
		Preference PreferenceDecision `json:"preference"`
	}](t, &admin, "GET", "/api/models/resolve?role=review-gate&author_family=openai&ticket="+ticket, "", 200)
	if review.Profile != nil && review.Profile.Family == "openai" || review.Preference.Kind.Slug != "review" || len(review.Preference.HardRules) < 3 {
		t.Fatal("preference route bypassed the qualified review gate", review)
	}
}

type mutationBarrier struct {
	once             sync.Once
	entered, release chan struct{}
	reader           *strings.Reader
}

func (b *mutationBarrier) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.entered); <-b.release })
	return b.reader.Read(p)
}
func TestPreferenceMutationRechecksRevokedGrant(t *testing.T) {
	reset(t)
	admin := makePrincipal(t, "prefs-demote", "person", "Admin", []string{"admin"})
	prefDoc(t, admin)
	barrier := &mutationBarrier{entered: make(chan struct{}), release: make(chan struct{}), reader: strings.NewReader(`{"revision":0,"residency":"eu"}`)}
	request := httptest.NewRequest("PUT", "/api/model-preferences/levels/default", barrier)
	request = request.WithContext(tenant.WithPrincipal(request.Context(), admin))
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	mux := http.NewServeMux()
	New(appPool).Mount(mux)
	go func() {
		defer close(done)
		if err := authz.Require(authz.BindPool(request.Context(), appPool), "model_prefs.manage", authz.Scope{}); err != nil {
			httpapi.WriteError(recorder, 403, err.Error())
			return
		}
		mux.ServeHTTP(recorder, request)
	}()
	<-barrier.entered
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, admin.TenantID, func(tx pgx.Tx) error {
		if err := preferenceFence(t.Context(), tx, admin); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE key='member') WHERE principal_id=$1 AND scope_type='workspace'`, admin.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	close(barrier.release)
	<-done
	if recorder.Code != 403 {
		t.Fatal("earlier authorization trusted", recorder.Code, recorder.Body.String())
	}
	if eventCount(t, admin, "model.preferences_changed") != 0 {
		t.Fatal("revoked write committed")
	}
}
func TestPreferenceOpenAPIContract(t *testing.T) {
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var spec map[string]any
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	paths := spec["paths"].(map[string]any)
	for _, path := range []string{"/model-preferences", "/model-preferences/levels/{level}", "/model-preferences/levels/{level}/rows/{kindId}", "/work-kinds", "/work-kinds/{kindId}", "/work-kinds/{kindId}/restore", "/models/{id}/retire"} {
		if paths[path] == nil {
			t.Errorf("missing %s", path)
		}
	}
	components := spec["components"].(map[string]any)
	schemas := components["schemas"].(map[string]any)
	if schemas["ModelPreferences"] == nil || schemas["WorkKindPage"] == nil {
		t.Fatal("missing schemas")
	}
	// Round-trip also catches duplicate keys and malformed inline declarations.
	round, err := yaml.Marshal(spec)
	if err != nil || len(round) == 0 {
		t.Fatal(err)
	}
}

func TestPreferencePickerEvidence(t *testing.T) {
	reset(t)
	admin := makePrincipal(t, "prefs-picker", "person", "Admin", []string{"admin"})
	runner := addPrincipal(t, admin.TenantID, "agent", "Picker runner", []string{"admin"})
	prefDoc(t, admin) // seed the registry before binding the allowed model
	var allowedID string
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, admin.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `SELECT id::text FROM model_profiles WHERE slug='codex-sol-high'`).Scan(&allowedID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label,allowed_model_profile_ids)
 VALUES($1,'picker-account','codex','picker-daemon',$2,'Picker account',ARRAY[$3::uuid])`, admin.TenantID, runner.ID, allowedID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	doc := prefDoc(t, admin)
	view := doc.Views["person"]
	if view == nil || len(view.Choices) == 0 || len(view.Choices) > 256 {
		t.Fatal("missing or unbounded picker choices")
	}
	total := 0
	var profileID string
	foundUnqualified := false
	for _, choice := range view.Choices {
		total += choice.ResidencyRoutes
		if choice.Profile.Slug == "codex-sol-high" {
			profileID = choice.Profile.ID
			if choice.Line != "sol" || choice.ModelVersion == "" {
				t.Fatal("picker used catalog revision instead of model version", choice)
			}
		}
		if choice.Profile.Harness == "codex" && choice.ReviewLadder && choice.Profile.Effort == "xhigh" {
			foundUnqualified = strings.Contains(choice.ReviewReason, "Codex")
		}
	}
	if total != 1 || view.Residency.QualifyingRoutes != 1 || !foundUnqualified || profileID != allowedID {
		t.Fatal("picker evidence does not match routing or qualification", total, view.Residency.QualifyingRoutes, foundUnqualified)
	}
	// The same allowed account has no EU evidence: test this before retirement.
	decode[preferenceWriteResult](t, &admin, "PUT", "/api/model-preferences/levels/person", `{"revision":0,"residency":"eu"}`, 200)
	doc = prefDoc(t, admin)
	if doc.Views["person"].Residency.QualifyingRoutes != 0 {
		t.Fatal("EU route invented")
	}
	for _, choice := range doc.Views["person"].Choices {
		if choice.ResidencyRoutes != 0 {
			t.Fatal("picker invented EU residency evidence", choice)
		}
	}
	decode[preferenceWriteResult](t, &admin, "PUT", "/api/model-preferences/levels/person", `{"revision":1,"residency":"any"}`, 200)
	if prefDoc(t, admin).Views["person"].Residency.QualifyingRoutes != 1 {
		t.Fatal("allowed route did not return")
	}
	decode[map[string]any](t, &admin, "POST", "/api/models/"+profileID+"/retire", `{"reason":"Picker regression"}`, 200)
	doc = prefDoc(t, admin)
	for _, choice := range doc.Views["person"].Choices {
		if choice.Profile.ID == profileID {
			t.Fatal("retired model still offered", choice)
		}
	}
	if doc.Views["person"].Residency.QualifyingRoutes != 0 {
		t.Fatal("retired model still has a route")
	}
}

func TestPreferencePickerTruncation(t *testing.T) {
	reset(t)
	admin := makePrincipal(t, "prefs-picker-limit", "person", "Admin", []string{"admin"})
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier)
 SELECT $1,'picker-extra-' || lpad(n::text,3,'0'),'1','codex','openai','gpt-6.1-sol','high','standard' FROM generate_series(1,257) n`, admin.TenantID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	doc := prefDoc(t, admin)
	for _, level := range []string{"default", "person"} {
		view := doc.Views[level]
		var evidence map[string]any
		raw, err := json.Marshal(view)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &evidence); err != nil {
			t.Fatal(err)
		}
		if len(view.Choices) != 256 || evidence["choices_truncated"] != true {
			t.Fatalf("%s: missing explicit truncation (%d, %v)", level, len(view.Choices), evidence["choices_truncated"])
		}
	}
}
