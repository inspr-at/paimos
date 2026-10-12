// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/auth"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/principallink"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func editorProject(t *testing.T, p tenant.Principal, key string) string {
	t.Helper()
	var id string
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,$2,'Editor project' FROM node_kinds WHERE slug='project' RETURNING id::text`, p.TenantID, key).Scan(&id)
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func bindEditorRole(t *testing.T, p tenant.Principal, who tenant.Principal, key, project string, permissions []string) {
	t.Helper()
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		var role string
		if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,$2,$2) RETURNING id::text`, p.TenantID, key).Scan(&role); err != nil {
			return err
		}
		for _, permission := range permissions {
			if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,$3)`, p.TenantID, role, permission); err != nil {
				return err
			}
		}
		if project == "" {
			_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1,$2,$3,'workspace')`, p.TenantID, who.ID, role)
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) VALUES($1,$2,$3,'project',$4)`, p.TenantID, who.ID, role, project)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestEditorRetainedProjectReadBoundaryAndExplicitScopeWrites(t *testing.T) {
	p, h := editorFixture(t)
	project := editorProject(t, p, "SCOPE-1")
	other := editorProject(t, p, "SCOPE-2")
	manager := addPrincipal(t, p.TenantID, "person", "Project only", nil)
	bindEditorRole(t, p, manager, "project_model_manager", project, []string{"model_prefs.manage", "nodes.read"})
	for _, path := range []string{"/api/model-preferences?project_id=" + project, "/api/models", "/api/work-kinds?project_id=" + project, "/api/models/routes?role=build"} {
		w := h.call(t, manager, "GET", path, "", nil)
		if w.Code != 403 || !strings.Contains(w.Body.String(), "permission denied") {
			t.Fatal("project permission exposed model read", path, w.Code, w.Body.String())
		}
	}
	path := "/api/model-preferences/levels/project?project_id=" + project
	editorDecode[preferenceWriteResult](t, h.call(t, manager, "PUT", path, `{"revision":0,"residency":"eu"}`, nil))
	editorError(t, h.call(t, manager, "PUT", path, `{"revision":1,"residency":"local"}`, prefHeaders(p.ID)), 409, "preference_person_changed")
	for _, path := range []string{"/api/model-preferences/levels/default", "/api/model-preferences/levels/project?project_id=" + other} {
		w := h.call(t, manager, "PUT", path, `{"revision":0}`, nil)
		if w.Code != 403 {
			t.Fatal("target authority widened", path, w.Code, w.Body.String())
		}
	}
	reader := addPrincipal(t, p.TenantID, "person", "Visible project editor", nil)
	bindEditorRole(t, p, reader, "model_reader", "", []string{"models.read"})
	bindEditorRole(t, p, reader, "visible_project_manager", project, []string{"model_prefs.manage", "nodes.read"})
	doc := h.prefs(t, reader, project)
	kind := editorKind(t, doc, "backend")
	row := "/api/model-preferences/levels/project/rows/" + kind + "?project_id=" + project
	save := editorDecode[preferenceWriteResult](t, h.call(t, reader, "PUT", row, prefRowPayload(doc.Levels["project"].Revision, false, modelprefs.Cell{Mode: "auto"}, modelprefs.Cell{Mode: "auto"}), prefHeaders(reader.ID)))
	editorDecode[preferenceWriteResult](t, h.call(t, reader, "DELETE", fmt.Sprintf("%s&revision=%d", row, save.Revision), "", prefHeaders(reader.ID)))
	defaultOnly := addPrincipal(t, p.TenantID, "person", "Default only", nil)
	bindEditorRole(t, p, defaultOnly, "default_model_manager", "", []string{"model_prefs.manage"})
	w := h.call(t, defaultOnly, "GET", "/api/model-preferences", "", nil)
	if w.Code != 403 {
		t.Fatal("manage granted read")
	}
	editorDecode[preferenceWriteResult](t, h.call(t, defaultOnly, "PUT", "/api/model-preferences/levels/default", `{"revision":0,"residency":"eu"}`, nil))
	permission, ok := authz.Lookup("model_prefs.manage")
	if !ok || permission.AgentGrantable {
		t.Fatal("preference permission lost person-only ceiling")
	}
}

type editorHTTP struct {
	handler http.Handler
	cookies map[string]*http.Cookie
}

func newEditorHTTP(t *testing.T, m *Module) *editorHTTP {
	t.Helper()
	a, err := auth.New(auth.Config{Env: "dev", SessionKey: bytes.Repeat([]byte{1}, 32)}, appPool)
	if err != nil {
		t.Fatal(err)
	}
	s := &httpapi.Server{Pool: appPool, Modules: []httpapi.Module{legacyPreferenceTestModule{m}, authz.New(appPool)}, Middleware: []func(http.Handler) http.Handler{a.Middleware}}
	return &editorHTTP{handler: s.Handler(), cookies: map[string]*http.Cookie{}}
}

func (h *editorHTTP) cookie(t *testing.T, p tenant.Principal) *http.Cookie {
	t.Helper()
	if c := h.cookies[p.ID]; c != nil {
		return c
	}
	raw := sha256.Sum256([]byte("editor-test-session/" + p.ID))
	id := sha256.Sum256(raw[:])
	var identity string
	if err := adminPool.QueryRow(t.Context(), `INSERT INTO identities(issuer,subject) VALUES('editor-test',$1) ON CONFLICT(issuer,subject) DO UPDATE SET issuer=EXCLUDED.issuer RETURNING id::text`, p.ID).Scan(&identity); err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO sessions(id,identity_id,tenant_id,principal_id,expires_at) VALUES($1,$2,$3,$4,now()+interval '1 day') ON CONFLICT(id) DO NOTHING`, hex.EncodeToString(id[:]), identity, p.TenantID, p.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	c := &http.Cookie{Name: "aeon_session", Value: hex.EncodeToString(raw[:])}
	h.cookies[p.ID] = c
	return c
}

func (h *editorHTTP) call(t *testing.T, p tenant.Principal, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.AddCookie(h.cookie(t, p))
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.handler.ServeHTTP(w, r)
	return w
}

func editorDecode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if w.Code != 200 {
		t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}
func editorError(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if w.Code != status || out["code"] != code && out["error"] != code {
		t.Fatalf("want %d %s; got %d %s", status, code, w.Code, w.Body.String())
	}
}
func editorJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

// retiredLadderRefusal reports the retired writer's 410 by its decoded
// message: the JSON encoder escapes ">" as >, so the raw body never
// contains "Settings > Models".
func retiredLadderRefusal(status int, body []byte) bool {
	var refusal struct {
		Error string `json:"error"`
	}
	return status == http.StatusGone && json.Unmarshal(body, &refusal) == nil &&
		refusal.Error == "Role ladders moved to Settings > Models; use model preference orders and model pauses."
}

func editorFixture(t *testing.T) (tenant.Principal, *editorHTTP) {
	t.Helper()
	var p tenant.Principal
	prefsFixture(t, func(tx pgx.Tx, current tenant.Principal) error { p = current; return nil })
	return p, newEditorHTTP(t, &Module{pool: appPool})
}
func (h *editorHTTP) ladder(t *testing.T, p tenant.Principal, role string) routesRead {
	t.Helper()
	return editorDecode[routesRead](t, h.call(t, p, "GET", "/api/models/routes?role="+role, "", nil))
}
func (h *editorHTTP) prefs(t *testing.T, p tenant.Principal, project string) preferenceDocument {
	t.Helper()
	path := "/api/model-preferences"
	if project != "" {
		path += "?project_id=" + project
	}
	return editorDecode[preferenceDocument](t, h.call(t, p, "GET", path, "", nil))
}
func prefHeaders(id string) map[string]string { return map[string]string{"If-Prefs-Person": id} }
func prefRowPayload(rev int64, locked bool, normal, complex modelprefs.Cell) string {
	return editorJSON(map[string]any{"revision": rev, "locked": locked, "normal": normal, "complex": complex})
}
func editorKind(t *testing.T, doc preferenceDocument, slug string) string {
	t.Helper()
	for _, k := range doc.Kinds {
		if k.Slug == slug {
			return k.ID
		}
	}
	t.Fatal("missing kind", slug)
	return ""
}

// Risk: an equal, changed, whole-tenant or clearing write through the retired
// ladder writer changes a role, issues an edit token or writes an audit event.
func TestEditorRoleCASLegacyNoopAndAudit(t *testing.T) {
	p, h := editorFixture(t)
	build, other := h.ladder(t, p, "build"), h.ladder(t, p, "scout")
	if build.EditToken != nil || build.CanEdit || other.EditToken != nil || other.CanEdit {
		t.Fatal("compatibility read still grants editing")
	}
	path := "/api/models/routes?role=build"
	token := map[string]string{"If-Match": `"` + strings.Repeat("f", 64) + `"`}
	want := append([]Route{}, build.Routes...)
	for i := range want {
		want[i].Priority += 100
	}
	before, events := catalogSetupState(t, p), eventCount(t, p, evRoutes)
	for _, tc := range []struct {
		path, body string
		headers    map[string]string
	}{{path, editorJSON(build.Routes), token}, {path, editorJSON(want), token}, {"/api/models/routes", editorJSON(want), nil}, {path, "[]", token}} {
		w := h.call(t, p, "PUT", tc.path, tc.body, tc.headers)
		editorRetired(t, w)
		if w.Header().Get("ETag") != "" {
			t.Fatal("retired writer issued an edit token")
		}
	}
	if catalogSetupState(t, p) != before || eventCount(t, p, evRoutes) != events || !reflect.DeepEqual(h.ladder(t, p, "build").Routes, build.Routes) || !reflect.DeepEqual(h.ladder(t, p, "scout").Routes, other.Routes) {
		t.Fatal("retired writer changed a role or wrote an audit event")
	}
}

// Risk: an expiring hold or an expiry-policy compensation sent to the retired
// ladder writer still reaches its clock or storage. Holds are Models pauses
// now; retained content tokens still bind full-precision expiry and reason.
func TestEditorExpiryTokensAndCompensation(t *testing.T) {
	p, _ := editorFixture(t)
	clock := time.Date(2030, 1, 1, 0, 0, 0, 123456000, time.UTC)
	sampled := false
	h := newEditorHTTP(t, &Module{pool: appPool, validationClock: func(context.Context, pgx.Tx) (time.Time, error) { sampled = true; return clock, nil }})
	initial := h.ladder(t, p, "build")
	if initial.EditToken != nil || initial.CanEdit || len(initial.Routes) == 0 {
		t.Fatal("compatibility read still grants editing")
	}
	expiry := clock.Add(time.Second)
	held := []Route{{Role: "build", Priority: 1, ProfileID: "00000000-0000-0000-0000-000000000001", State: "conserved", Reason: "quota", ValidUntil: &expiry}}
	before, events := catalogSetupState(t, p), eventCount(t, p, evRoutes)
	token := map[string]string{"If-Match": `"` + strings.Repeat("1", 64) + `"`}
	for _, path := range []string{"/api/models/routes?role=build", "/api/models/routes?role=build&expiry_policy=clear", "/api/models/routes?role=build&expiry_policy=preserve"} {
		editorRetired(t, h.call(t, p, "PUT", path, editorJSON(held), token))
	}
	if sampled || catalogSetupState(t, p) != before || eventCount(t, p, evRoutes) != events {
		t.Fatal("retired writer sampled its clock or changed stored holds")
	}
	// The Models successor preserves the exact reason and native expiry.
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO model_pauses(tenant_id,harness,model,until_at,reason,set_by) SELECT tenant_id,harness,model,$2,'quota',$3 FROM model_profiles WHERE id=$1`, initial.Routes[0].ProfileID, expiry, p.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range h.ladder(t, p, "build").Routes {
		if row.ProfileID == initial.Routes[0].ProfileID {
			found = row.State == "unavailable" && row.Reason == "quota" && row.ValidUntil != nil && row.ValidUntil.Equal(expiry)
		}
	}
	if !found {
		t.Fatal("deprecated read lost the Models pause")
	}
	// Full-precision and reason-only changes must bind different content.
	a := held
	b := append([]Route{}, held...)
	sub := expiry.Add(time.Microsecond)
	b[0].ValidUntil = &sub
	if routeEditToken("build", a) == routeEditToken("build", b) {
		t.Fatal("subsecond expiry omitted")
	}
	b[0].ValidUntil = a[0].ValidUntil
	b[0].Reason = "other"
	if routeEditToken("build", a) == routeEditToken("build", b) {
		t.Fatal("reason omitted")
	}
}

// Risk: a cold compatibility read seeds the catalog or hands out an edit
// token, or a malformed, unconditional or over-bound write to the retired
// ladder writer prepares the catalog instead of answering 410.
func TestEditorBoundsPreconditionsAndReadOnlyCold(t *testing.T) {
	reset(t)
	p := makePrincipal(t, "editor-cold", "person", "Owner", []string{"admin"})
	h := newEditorHTTP(t, &Module{pool: appPool})
	before := catalogSetupState(t, p)
	cold := h.ladder(t, p, "build")
	if cold.Setup || cold.CanEdit || cold.EditToken != nil || catalogSetupState(t, p) != before {
		t.Fatal("cold read side effects")
	}
	for _, header := range []string{"", "*", "W/\"x\"", `"` + strings.Repeat("0", 64) + `", "` + strings.Repeat("0", 64) + `"`} {
		headers := map[string]string{"If-Match": header}
		if header == "" {
			headers = nil
		}
		editorRetired(t, h.call(t, p, "PUT", "/api/models/routes?role=build", "[]", headers))
	}
	for _, q := range []string{"?role=build&expiry_policy=invalid", "?expiry_policy=clear", "?role=build&role=scout", "?role=unknown"} {
		editorRetired(t, h.call(t, p, "PUT", "/api/models/routes"+q, "[]", nil))
	}
	rows := make([]Route, 251)
	editorRetired(t, h.call(t, p, "PUT", "/api/models/routes", editorJSON(rows), nil))
	rows = rows[:51]
	for i := range rows {
		rows[i].Role = "build"
	}
	editorRetired(t, h.call(t, p, "PUT", "/api/models/routes", editorJSON(rows), nil))
	if catalogSetupState(t, p) != before {
		t.Fatal("retired writer seeded catalog")
	}
	displayRoutesFixture(t, p, 51)
	large := h.ladder(t, p, "review-gate")
	if !large.Truncated || large.EditToken != nil || large.CanEdit || len(large.Routes) != 50 {
		t.Fatal("partial snapshot editable")
	}
}

func TestEditorMiddlewarePersonHeadersAndPermissionMatrix(t *testing.T) {
	p, h := editorFixture(t)
	reader := addPrincipal(t, p.TenantID, "person", "Reader", []string{"member"})
	doc := h.prefs(t, reader, "")
	kind := editorKind(t, doc, "backend")
	path := "/api/model-preferences/levels/person/rows/" + kind
	body := prefRowPayload(0, false, modelprefs.Cell{Mode: "auto"}, modelprefs.Cell{Mode: "auto"})
	for _, tc := range []struct{ method, path, body string }{{"PUT", "/api/model-preferences/levels/person", `{"revision":0}`}, {"DELETE", "/api/model-preferences/levels/person?revision=0", ""}, {"PUT", path, body}, {"DELETE", path + "?revision=0", ""}} {
		editorError(t, h.call(t, reader, tc.method, tc.path, tc.body, nil), 428, "person_precondition_required")
		editorError(t, h.call(t, reader, tc.method, tc.path, tc.body, prefHeaders("bad")), 400, "invalid_person_precondition")
		editorError(t, h.call(t, reader, tc.method, tc.path, tc.body, prefHeaders(p.ID)), 409, "preference_person_changed")
	}
	saved := editorDecode[preferenceWriteResult](t, h.call(t, reader, "PUT", path, body, prefHeaders(*doc.PersonID)))
	if saved.PersonID == nil || *saved.PersonID != reader.ID || saved.Revision != 1 {
		t.Fatal("identity confirmation wrong")
	}
	editorDecode[preferenceWriteResult](t, h.call(t, reader, "DELETE", path+"?revision=1", "", prefHeaders(reader.ID)))
	w := h.call(t, reader, "PUT", "/api/model-preferences/levels/default", `{"revision":0}`, prefHeaders(p.ID))
	if w.Code != 403 || strings.Contains(w.Body.String(), "preference_person_changed") {
		t.Fatal("unauthorized identity detail leaked", w.Code, w.Body.String())
	}
	editorError(t, h.call(t, p, "PUT", "/api/model-preferences/levels/default", `{"revision":0}`, prefHeaders(reader.ID)), 409, "preference_person_changed")
	// Default direct callers keep their explicit-scope write without a header.
	editorDecode[preferenceWriteResult](t, h.call(t, p, "PUT", "/api/model-preferences/levels/default", `{"revision":0}`, nil))
	agent := addPrincipal(t, p.TenantID, "agent", "Agent", []string{"admin"})
	prefix := strings.ReplaceAll(p.TenantID, "-", "") + strings.ReplaceAll(agent.ID, "-", "")[:16]
	sum := sha256.Sum256([]byte("editor-fixture"))
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes,created_by_principal_id) VALUES($1,$2,'editor',$3,$4,ARRAY['models.read','models.manage'],$5)`, p.TenantID, agent.ID, prefix, hex.EncodeToString(sum[:]), p.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ method, path, body string }{{"PUT", "/api/models/routes", "[]"}, {"GET", "/api/model-preferences", ""}, {"PUT", path, body}, {"DELETE", path + "?revision=0", ""}, {"GET", "/api/work-kinds", ""}} {
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		r.Header.Set("Authorization", "Bearer aeon_"+prefix+"_editor-fixture")
		w := httptest.NewRecorder()
		h.handler.ServeHTTP(w, r)
		editorError(t, w, 403, "agent key scope required")
	}
	// AEON-1146 permits preference reads for coordinator ceilings. Establish
	// that ceiling explicitly after proving the ordinary key's denials above.
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_keys SET scopes=$2 WHERE principal_id=$1`, agent.ID, authz.CoordinatorKeyScopes)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/model-preferences", nil)
	r.Header.Set("Authorization", "Bearer aeon_"+prefix+"_editor-fixture")
	w = httptest.NewRecorder()
	h.handler.ServeHTTP(w, r)
	editorDecode[preferenceDocument](t, w)
}

func linkEditorPerson(t *testing.T, p tenant.Principal, from, to string) {
	t.Helper()
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		if err := db.LockTenant(t.Context(), tx, p.TenantID); err != nil {
			return err
		}
		_, err := principallink.LinkTx(t.Context(), tx, p.TenantID, from, to, p.ID, "principal.linked", "principal.unlinked")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

const preferenceStorageSQL = `SELECT jsonb_build_object('counter',(SELECT to_jsonb(c) FROM event_counters c),'scopes',(SELECT jsonb_agg(to_jsonb(s) ORDER BY id) FROM model_pref_scopes s),'rows',(SELECT jsonb_agg(to_jsonb(r) ORDER BY scope_id,kind_id) FROM model_pref_rows r),'cells',(SELECT jsonb_agg(to_jsonb(c) ORDER BY scope_id,kind_id,bucket) FROM model_pref_cells c),'runs',(SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM agent_runs r),'events',(SELECT jsonb_agg(to_jsonb(e) ORDER BY id) FROM events e WHERE type IN ('model.preferences_changed','run.residency_restamped')))::text`

func preferenceStorage(t *testing.T, p tenant.Principal) string {
	t.Helper()
	var state string
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), preferenceStorageSQL).Scan(&state)
	}); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestEditorLinkEqualRevisionsAndUndoIdentity(t *testing.T) {
	for _, revision := range []int{0, 1} {
		t.Run(fmt.Sprint(revision), func(t *testing.T) {
			p, h := editorFixture(t)
			session := addPrincipal(t, p.TenantID, "person", "Session", []string{"member"})
			other := addPrincipal(t, p.TenantID, "person", "Other", []string{"member"})
			if revision > 0 {
				for _, who := range []tenant.Principal{session, other} {
					editorDecode[preferenceWriteResult](t, h.call(t, who, "PUT", "/api/model-preferences/levels/person", `{"revision":0,"residency":"eu"}`, prefHeaders(who.ID)))
				}
			}
			doc := h.prefs(t, session, "")
			kind := editorKind(t, doc, "backend")
			row := "/api/model-preferences/levels/person/rows/" + kind
			linkEditorPerson(t, p, session.ID, other.ID)
			before := preferenceStorage(t, p)
			for _, tc := range []struct{ method, path, body string }{{"PUT", "/api/model-preferences/levels/person", fmt.Sprintf(`{"revision":%d,"residency":"local"}`, revision)}, {"DELETE", fmt.Sprintf("/api/model-preferences/levels/person?revision=%d", revision), ""}, {"PUT", row, prefRowPayload(int64(revision), false, modelprefs.Cell{Mode: "auto"}, modelprefs.Cell{Mode: "auto"})}, {"DELETE", fmt.Sprintf("%s?revision=%d", row, revision), ""}} {
				editorError(t, h.call(t, session, tc.method, tc.path, tc.body, prefHeaders(session.ID)), 409, "preference_person_changed")
				if preferenceStorage(t, p) != before {
					t.Fatal("old draft mutated canonical person")
				}
			}
			fresh := h.prefs(t, session, "")
			if fresh.PersonID == nil || *fresh.PersonID != other.ID || fresh.Levels["person"].Revision != int64(revision) {
				t.Fatal("fresh linked identity incoherent")
			}
			result := editorDecode[preferenceWriteResult](t, h.call(t, session, "PUT", row, prefRowPayload(int64(revision), false, modelprefs.Cell{Mode: "auto"}, modelprefs.Cell{Mode: "auto"}), prefHeaders(other.ID)))
			linkEditorPerson(t, p, session.ID, "")
			before = preferenceStorage(t, p)
			editorError(t, h.call(t, session, "DELETE", fmt.Sprintf("%s?revision=%d", row, result.Revision), "", prefHeaders(other.ID)), 409, "preference_person_changed")
			if preferenceStorage(t, p) != before {
				t.Fatal("old Undo mutated after unlink")
			}
		})
	}
}

func TestEditorPreferenceLegacyChangeRefusesOldUndo(t *testing.T) {
	p, h := editorFixture(t)
	doc := h.prefs(t, p, "")
	kind := editorKind(t, doc, "backend")
	path := "/api/model-preferences/levels/person/rows/" + kind
	saved := editorDecode[preferenceWriteResult](t, h.call(t, p, "PUT", path, prefRowPayload(0, false, modelprefs.Cell{Mode: "auto"}, modelprefs.Cell{Mode: "auto"}), prefHeaders(p.ID)))
	editorDecode[preferenceWriteResult](t, h.call(t, p, "PUT", "/api/model-preferences/levels/person", fmt.Sprintf(`{"revision":%d,"rows":[]}`, saved.Revision), prefHeaders(p.ID)))
	before := preferenceStorage(t, p)
	editorError(t, h.call(t, p, "DELETE", fmt.Sprintf("%s?revision=%d", path, saved.Revision), "", prefHeaders(p.ID)), 409, "stale_revision")
	if preferenceStorage(t, p) != before {
		t.Fatal("old row compensation overwrote legacy change")
	}
	_ = h.prefs(t, p, "") // Reading a fresh revision confers no retry authority.
	editorError(t, h.call(t, p, "DELETE", fmt.Sprintf("%s?revision=%d", path, saved.Revision), "", prefHeaders(p.ID)), 409, "stale_revision")
	foreign := makePrincipal(t, "editor-foreign", "person", "Foreign", []string{"admin"})
	editorError(t, h.call(t, p, "PUT", "/api/model-preferences/levels/person", `{"revision":2}`, prefHeaders(foreign.ID)), 409, "preference_person_changed")
	if preferenceStorage(t, p) != before {
		t.Fatal("cross-tenant header selected a target")
	}
}

func TestEditorPersonHeaderStrictlyBoundedEquality(t *testing.T) {
	const person = "00000000-0000-0000-0000-000000000001"
	for _, values := range [][]string{{""}, {" " + person}, {person + " "}, {person + "," + person}, {person, person}, {strings.Repeat("a", 10000)}} {
		r := httptest.NewRequest("PUT", "/api/model-preferences/levels/person", nil)
		for _, value := range values {
			r.Header.Add("If-Prefs-Person", value)
		}
		_, err := expectedPreferencePerson(r, "person")
		e, ok := err.(*preferenceError)
		if !ok || e.status != 400 || e.code != "invalid_person_precondition" {
			t.Fatal("invalid or duplicate identity header accepted", err)
		}
	}
}

// Compatibility tests retain the pre-cutover writer's full security assertions.
// Public routing never uses this test-only module.
type legacyPreferenceTestModule struct{ *Module }

func (m legacyPreferenceTestModule) Mount(mux *http.ServeMux) {
	m.Module.mount(mux, m.Module.writePreferences)
}

func errorText(raw []byte) string {
	var out struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Error == "" {
		return string(raw)
	}
	return out.Error
}

func modelsState(t *testing.T, p tenant.Principal) string {
	t.Helper()
	var state string
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT jsonb_build_object(
 'profiles',(SELECT jsonb_agg(to_jsonb(p) ORDER BY id) FROM model_profiles p),
 'prefs',(SELECT jsonb_agg(to_jsonb(p) ORDER BY id) FROM model_pref_profiles p),
 'orders',(SELECT jsonb_agg(to_jsonb(o) ORDER BY profile_id,column_key,situation) FROM model_pref_orders o),
 'situations',(SELECT jsonb_agg(to_jsonb(o) ORDER BY profile_id,column_key,situation) FROM model_pref_situation_orders o),
 'pauses',(SELECT jsonb_agg(to_jsonb(o) ORDER BY harness,model) FROM model_pauses o),
 'constraints',(SELECT jsonb_agg(to_jsonb(o) ORDER BY scope,column_key,line) FROM model_row_constraints o),
 'ladders',(SELECT jsonb_agg(to_jsonb(r) ORDER BY role,priority) FROM model_role_routes r),
 'events',(SELECT count(*) FROM events WHERE type=$1),
 'counter',(SELECT to_jsonb(c) FROM event_counters c))::text`, evRoutes).Scan(&state)
	}); err != nil {
		t.Fatal(err)
	}
	return state
}

func retiredLadderWrite(t *testing.T, h *editorHTTP, p tenant.Principal, path, body string, headers map[string]string) {
	t.Helper()
	w := h.call(t, p, "PUT", path, body, headers)
	if !retiredLadderRefusal(w.Code, w.Body.Bytes()) || w.Header().Get("Deprecation") != "true" || w.Header().Get("Link") != `</api/model-preferences/orders>; rel="successor-version"` {
		t.Fatalf("retired ladder writer answered %d: %s", w.Code, w.Body.String())
	}
}
