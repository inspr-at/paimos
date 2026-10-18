// SPDX-License-Identifier: AGPL-3.0-only
package decisiondesk

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/recurrences"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risk: a consent pointer could leak private routines, offer a person a PUT
// that current policy rejects, miss expired consent after project changes,
// survive revocation, or escape keyset bounds.
func TestRoutineConsentProjectionMatchesNativePolicyAndPages(t *testing.T) {
	f := setup(t)
	f.person.BrowserSession = true
	f.reader.BrowserSession = true
	dbtest.BindRole(t, f.d, f.person.TenantID, f.reader.ID, "owner")
	var kind, projectKey string
	seed := func(fn func(pgx.Tx) error) {
		t.Helper()
		if err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.person.TenantID, fn); err != nil {
			t.Fatal(err)
		}
	}
	seed(func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `SELECT key FROM nodes WHERE id=$1`, f.project).Scan(&projectKey); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `SELECT aeon_seed_work_kinds($1)`, f.person.TenantID); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT id::text FROM work_kinds WHERE slug='backend' AND project_id IS NULL`).Scan(&kind); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO project_lead_settings(tenant_id,project_id,owner_person_id,overrides,updated_by) VALUES($1,$2,$3,'{}',$3)`, f.person.TenantID, f.project, f.person.ID)
		return err
	})
	q := modelregistry.Qualification{ID: "10000000-0000-4000-8000-000000000084", ProjectID: f.project,
		Runtime: modelregistry.ExecutionRuntime{ServerDigest: strings.Repeat("a", 64), DaemonDigest: strings.Repeat("b", 64), CapabilityDigest: strings.Repeat("c", 64), HostMappingDigest: strings.Repeat("d", 64), Capabilities: []string{"routine_native_coding_v1"}, BudgetModes: []string{"off"}}, CoordinatorAcceptance: strings.Repeat("e", 64), OPSAttestation: strings.Repeat("f", 64)}
	seed(func(tx pgx.Tx) error {
		var err error
		q.OwnerPersonID, q.PolicyDigest, err = modelregistry.QualificationPolicyTx(t.Context(), tx, f.person.TenantID, f.project)
		return err
	})
	runtime := q.Runtime
	reader := func(ctx context.Context, tx pgx.Tx, _ string) (modelregistry.ExecutionRuntime, error) {
		out := runtime
		err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&out.ObservedAt)
		return out, err
	}
	f.m.WithRoutineExecutionRuntime(reader)
	recurrences.New(f.d.App).WithExecutionRuntime(reader).Mount(f.mux)
	request := func(p tenant.Principal, method, path string, body any, status int) []byte {
		t.Helper()
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
		req = req.WithContext(tenant.WithPrincipal(req.Context(), p))
		w := httptest.NewRecorder()
		f.mux.ServeHTTP(w, req)
		if w.Code != status {
			t.Fatalf("%s %s: %d %s want %d", method, path, w.Code, w.Body.String(), status)
		}
		return w.Body.Bytes()
	}
	items := map[string]recurrences.Recurrence{}
	for _, scope := range []string{"personal", "project", "workspace"} {
		d := &recurrences.Definition{Scope: recurrences.DefinitionScope{Kind: scope}, OwnerPrincipalID: f.person.ID, Assignment: &recurrences.Assignment{Goal: "Review bounded work", Role: "build", WorkKindID: kind, AllowedActions: []string{"work.create"}, RuntimeRequirements: recurrences.RuntimeRequirements{NeedsNativeHost: true, RuntimeClass: "native_coding"}, Budget: recurrences.DefinitionBudget{Mode: "off"}}}
		if scope == "project" {
			d.Scope.ProjectID = f.project
		}
		in := recurrences.Input{ProjectID: f.project, ParentID: f.ticket, Template: recurrences.Template{Name: scope + " routine", Title: "Review", EstimateHours: 1, Criteria: []string{"Review complete"}}, Trigger: recurrences.Trigger{Kind: "time", RRULE: "FREQ=DAILY", TimeOfDay: "09:00", Timezone: "Europe/Vienna"}, OverlapPolicy: "create", CatchUpPolicy: "one", QueueEach: true, Definition: d}
		var item recurrences.Recurrence
		if err := json.Unmarshal(request(f.person, "POST", "/api/recurrences", in, 201), &item); err != nil {
			t.Fatal(err)
		}
		items[scope] = item
	}
	read := func(p tenant.Principal) Page {
		t.Helper()
		page, err := f.m.Read(t.Context(), p, 100, nil)
		if err != nil {
			t.Fatal(err)
		}
		return page
	}
	consents := func(page Page) []Item {
		out := []Item{}
		for _, item := range page.Items {
			if item.Kind == "routine_consent" {
				out = append(out, item)
			}
		}
		return out
	}
	if got := consents(read(f.person)); len(got) != 0 {
		t.Fatal("disabled project projected consent", got)
	}
	seed(func(tx pgx.Tx) error { return modelregistry.RecordQualificationTx(t.Context(), tx, f.person, q) })
	enabled, rev := true, int64(0)
	seed(func(tx pgx.Tx) error {
		_, err := modelregistry.WriteExecutionSettingsTx(t.Context(), tx, f.person, f.project, modelregistry.ExecutionSettingsInput{ExpectedRevision: &rev, AutomaticLaunchEnabled: &enabled, QualificationID: &q.ID}, reader)
		return err
	})
	page := read(f.person)
	if len(consents(page)) != 3 {
		t.Fatalf("effective owner has no requests: %+v", page)
	}
	for _, item := range consents(page) {
		if !item.CanDecide || item.Held || strings.Contains(item.Title, "account") {
			t.Fatal("incorrect pointer", item)
		}
		var want string
		for scope, r := range items {
			if r.ID == item.ID {
				want = map[string]string{"personal": "/settings/personal", "workspace": "/settings/routines", "project": "/p/" + projectKey + "/settings"}[scope] + "?recurrence=" + r.ID + "&consent=1"
			}
		}
		if item.Href != want {
			t.Fatalf("scope link: %q want %q", item.Href, want)
		}
	}
	for _, p := range []tenant.Principal{f.reader, func() tenant.Principal { p := f.person; p.BrowserSession = false; return p }(), func() tenant.Principal { p := f.person; p.AuthKeyID = "synthetic-bearer"; return p }()} {
		if len(consents(read(p))) != 0 {
			t.Fatal("non-owner/session projected consent")
		}
	}
	request(f.agent, "GET", "/api/decision-desk/projection", nil, 403)
	projectRoutine := items["project"]
	request(f.reader, "PUT", "/api/recurrences/"+projectRoutine.ID+"/execution-consent", map[string]any{"expected_revision": projectRoutine.Revision, "execute_consent": true, "execution_principal_id": nil}, 403)
	// A one-row page preserves exact totals and advances through all scopes.
	seen := map[string]bool{}
	var cursor *cursor
	for n := 0; n < 10; n++ {
		part, err := f.m.Read(t.Context(), f.person, 1, cursor)
		if err != nil {
			t.Fatal(err)
		}
		if len(part.Items) > 1 || part.Counts.Open != page.Counts.Open {
			t.Fatal("page bound/count changed", part)
		}
		for _, item := range part.Items {
			if seen[item.ID] {
				t.Fatal("duplicate page", item.ID)
			}
			seen[item.ID] = true
		}
		if !part.HasMore {
			break
		}
		cursor, err = decodeCursor(part.NextCursor)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range items {
		if !seen[r.ID] {
			t.Fatal("keyset lost routine", r.ID)
		}
	}
	r := items["personal"]
	path := "/api/recurrences/" + r.ID + "/execution-consent"
	runtime.ServerDigest = strings.Repeat("0", 64)
	if len(consents(read(f.person))) != 0 {
		t.Fatal("changed runtime projected an unavailable PUT")
	}
	if raw := request(f.person, "PUT", path, map[string]any{"expected_revision": r.Revision, "execute_consent": true, "execution_principal_id": nil}, 409); !strings.Contains(string(raw), "qualification_runtime_changed") {
		t.Fatal("wrong policy rejection", string(raw))
	}
	runtime = q.Runtime
	if err := json.Unmarshal(request(f.person, "PUT", path, map[string]any{"expected_revision": r.Revision, "execute_consent": true, "execution_principal_id": nil}, 200), &r); err != nil {
		t.Fatal(err)
	}
	if r.ExecutionPolicy == nil || !r.ExecutionPolicy.Eligible {
		t.Fatal("consent did not establish eligible execution", r.ExecutionPolicy)
	}
	if got := consents(read(f.person)); len(got) != 2 {
		t.Fatal("consented routine stayed projected", got)
	}
	writeSettings := func(enabled bool, qualification string) {
		t.Helper()
		seed(func(tx pgx.Tx) error {
			var revision int64
			if err := tx.QueryRow(t.Context(), `SELECT revision FROM project_routine_execution_settings WHERE project_id=$1`, f.project).Scan(&revision); err != nil {
				return err
			}
			settings, err := modelregistry.WriteExecutionSettingsTx(t.Context(), tx, f.person, f.project, modelregistry.ExecutionSettingsInput{ExpectedRevision: &revision, AutomaticLaunchEnabled: &enabled, QualificationID: &qualification}, reader)
			if err == nil && settings.AutomaticLaunchEnabled != enabled {
				t.Fatalf("project switch did not change: %+v", settings)
			}
			return err
		})
	}
	// Project changes preserve the consent bit and routine revision. The live
	// fingerprint must still renew the Desk request, just as GET and PUT do.
	for _, change := range []string{"project off/on", "new runtime qualification"} {
		if change == "project off/on" {
			writeSettings(false, q.ID)
			if got := consents(read(f.person)); len(got) != 0 {
				t.Fatal("disabled project projected renewal requests", got)
			}
		} else {
			q.ID = "10000000-0000-4000-8000-000000000085"
			q.Runtime.ServerDigest = strings.Repeat("1", 64)
			runtime = q.Runtime
			seed(func(tx pgx.Tx) error { return modelregistry.RecordQualificationTx(t.Context(), tx, f.person, q) })
		}
		writeSettings(true, q.ID)
		seed(func(tx pgx.Tx) error {
			var saved bool
			if err := tx.QueryRow(t.Context(), `SELECT execute_consent AND consent_revision=$2 FROM recurrence_definitions WHERE recurrence_id=$1`, r.ID, r.Revision).Scan(&saved); err != nil {
				return err
			}
			if !saved {
				t.Fatalf("%s discarded the stored consent fixture", change)
			}
			return nil
		})
		var listed struct {
			Items []recurrences.Recurrence `json:"items"`
		}
		if err := json.Unmarshal(request(f.person, "GET", "/api/recurrences", nil, 200), &listed); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, item := range listed.Items {
			if item.ID == r.ID {
				found = true
				if item.Revision != r.Revision || item.ExecutionPolicy == nil || item.ExecutionPolicy.Eligible || item.ExecutionPolicy.WaitReason != "execution_consent_required" || item.ExecutionPolicy.ConsentRevision != r.Revision {
					t.Fatalf("%s: GET did not require renewed consent at the unchanged routine revision: %+v", change, item)
				}
			}
		}
		if !found {
			t.Fatalf("%s: GET lost the consented routine", change)
		}
		found = false
		for _, item := range consents(read(f.person)) {
			if item.ID == r.ID {
				found = true
				if item.Revision != r.Revision || !item.CanDecide || item.Held || item.Href != "/settings/personal?recurrence="+r.ID+"&consent=1" {
					t.Fatalf("%s: incorrect renewal pointer: %+v", change, item)
				}
			}
		}
		if !found {
			t.Fatalf("%s: Desk omitted consent required by GET", change)
		}
		if err := json.Unmarshal(request(f.person, "PUT", path, map[string]any{"expected_revision": r.Revision, "execute_consent": true, "execution_principal_id": nil}, 200), &r); err != nil {
			t.Fatal(err)
		}
		if r.ExecutionPolicy == nil || !r.ExecutionPolicy.Eligible || r.ExecutionPolicy.WaitReason != "" || r.ExecutionPolicy.ConsentRevision != r.Revision {
			t.Fatalf("%s: renewed consent did not restore eligibility: %+v", change, r)
		}
		got := consents(read(f.person))
		if len(got) != 2 {
			t.Fatalf("%s: renewed consent stayed projected: %+v", change, got)
		}
		for _, item := range got {
			if item.ID == r.ID {
				t.Fatalf("%s: renewed routine still has a consent pointer: %+v", change, item)
			}
		}
	}
	if err := json.Unmarshal(request(f.person, "POST", "/api/recurrences/"+r.ID+"/resume", map[string]any{"expected_revision": r.Revision}, 200), &r); err != nil {
		t.Fatal(err)
	}
	if len(consents(read(f.person))) != 3 {
		t.Fatal("new control revision did not renew consent request")
	}
	for _, scope := range []string{"project", "workspace"} {
		r := items[scope]
		var principal, role string
		seed(func(tx pgx.Tx) error {
			if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,agent_access_configured) VALUES($1,'agent','Restricted',true) RETURNING id::text`, f.person.TenantID).Scan(&principal); err != nil {
				return err
			}
			if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,$2,'Restricted') RETURNING id::text`, f.person.TenantID, "routine_"+scope).Scan(&role); err != nil {
				return err
			}
			if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT $1,$2,unnest(ARRAY['nodes.read','nodes.write','run.create','work_orders.write'])`, f.person.TenantID, role); err != nil {
				return err
			}
			var project any
			if scope == "project" {
				project = f.project
			}
			_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) VALUES($1,$2,$3,$4,$5)`, f.person.TenantID, principal, role, scope, project)
			return err
		})
		request(f.person, "PUT", "/api/recurrences/"+r.ID+"/execution-consent", map[string]any{"expected_revision": r.Revision, "execute_consent": true, "execution_principal_id": principal}, 200)
	}
	if len(consents(read(f.person))) != 1 {
		t.Fatal("shared consent did not disappear")
	}
	request(f.person, "DELETE", "/api/recurrences/"+r.ID, map[string]any{"expected_revision": r.Revision}, 204)
	if len(consents(read(f.person))) != 0 {
		t.Fatal("retired routine stayed projected")
	}
	// Coverage failure occurs before per-candidate policy work, never as partial success.
	r = items["project"]
	seed(func(tx pgx.Tx) error {
		// Match create's write order: RLS requires the definition to exist
		// before a marked recurrence, and its FK is deferred until commit.
		if _, err := tx.Exec(t.Context(), `INSERT INTO recurrence_definitions(tenant_id,recurrence_id,scope_type,scope_project_id,owner_principal_id,output_project_id,output_parent_id,assignment)
 SELECT r.tenant_id,format('00000000-0000-4000-8000-%s',lpad(i::text,12,'0'))::uuid,d.scope_type,d.scope_project_id,d.owner_principal_id,d.output_project_id,d.output_parent_id,d.assignment
 FROM recurrences r JOIN recurrence_definitions d ON d.tenant_id=r.tenant_id AND d.recurrence_id=r.id CROSS JOIN generate_series(1,1001) i WHERE r.id=$1`, r.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO recurrences(tenant_id,id,project_id,parent_id,template,trigger,next_at,created_by_principal_id,definition_scope)
 SELECT tenant_id,format('00000000-0000-4000-8000-%s',lpad(i::text,12,'0'))::uuid,project_id,parent_id,template,trigger,next_at,created_by_principal_id,'project' FROM recurrences CROSS JOIN generate_series(1,1001) i WHERE id=$1`, r.ID); err != nil {
			return err
		}
		return nil
	})
	ctx := tenant.WithPrincipal(t.Context(), f.person)
	if err := db.InTenantReadSnapshot(ctx, f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
		var candidates int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM recurrences r
 JOIN recurrence_definitions d ON d.tenant_id=r.tenant_id AND d.recurrence_id=r.id
 WHERE r.id::text LIKE '00000000-0000-4000-8000-%' AND r.definition_scope=d.scope_type
 AND d.owner_principal_id=$1 AND d.assignment IS NOT NULL AND r.retired_at IS NULL`, f.person.ID).Scan(&candidates); err != nil {
			return err
		}
		if candidates != 1001 {
			t.Fatalf("coverage fixture has %d owner-visible candidates, want 1001", candidates)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f.m.WithRoutineExecutionRuntime(func(context.Context, pgx.Tx, string) (modelregistry.ExecutionRuntime, error) {
		t.Fatal("coverage overflow reached per-candidate runtime policy work")
		return modelregistry.ExecutionRuntime{}, nil
	})
	if raw := request(f.person, "GET", "/api/decision-desk/projection", nil, 422); !strings.Contains(string(raw), "routine consent coverage") {
		t.Fatal(fmt.Sprintf("wrong coverage failure: %s", raw))
	}
}
