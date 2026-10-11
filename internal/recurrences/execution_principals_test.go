// SPDX-License-Identifier: AGPL-3.0-only
package recurrences

import (
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risk: a picker could leak other tenants/owners, advertise identities refused
// by consent, omit eligible identities after sparse pages, or retain a revoked
// grant. Exercise the real GET and PUT with qualified synthetic runtime facts.
func TestRoutineExecutionPrincipalEligibilityParityAndPagination(t *testing.T) {
	f, in, q, reader := consentFixture(t)
	enableConsentProject(t, f, q, reader)
	personal := f.create(in)
	in.Definition.Scope = DefinitionScope{Kind: "project", ProjectID: f.project}
	project := f.create(in)
	in.Definition.Scope = DefinitionScope{Kind: "workspace"}
	workspace := f.create(in)
	permissions := executionPermissions(in.Definition.Assignment)
	provision := func(name, scope, projectID string, permissions []string) (string, string) {
		t.Helper()
		var id, role string
		f.tx(func(tx pgx.Tx) error {
			if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,agent_access_configured) VALUES($1,'agent',$2,true) RETURNING id::text`, f.p.TenantID, name).Scan(&id); err != nil {
				return err
			}
			if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,$2,$2) RETURNING id::text`, f.p.TenantID, name).Scan(&role); err != nil {
				return err
			}
			if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT $1,$2,unnest($3::text[])`, f.p.TenantID, role, permissions); err != nil {
				return err
			}
			_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) VALUES($1,$2,$3,$4,nullif($5,'')::uuid)`, f.p.TenantID, id, role, scope, projectID)
			return err
		})
		return id, role
	}
	projectAgent, projectRole := provision("project_execution", "project", f.project, permissions)
	secondAgent, _ := provision("second_execution", "project", f.project, permissions)
	workspaceAgent, _ := provision("workspace_execution", "workspace", "", permissions)
	otherProject := f.node("project", nil, "Other scope")
	wrongProject, _ := provision("wrong_project", "project", otherProject, permissions)
	ineligible := []string{f.p.ID, workspaceAgent, wrongProject}
	var aliasID string
	for _, reason := range []string{"inactive", "unconfigured", "linked", "keyed", "paired", "builtin", "extra_binding", "overscoped", "undergranted"} {
		id, role := provision(reason, "project", f.project, permissions)
		ineligible = append(ineligible, id)
		f.tx(func(tx pgx.Tx) error {
			var err error
			switch reason {
			case "inactive":
				_, err = tx.Exec(t.Context(), `UPDATE principals SET status='deactivated' WHERE id=$1`, id)
			case "unconfigured":
				_, err = tx.Exec(t.Context(), `UPDATE principals SET agent_access_configured=false WHERE id=$1`, id)
			case "linked":
				aliasID = id
				if _, err = tx.Exec(t.Context(), `DELETE FROM role_bindings WHERE principal_id=$1`, id); err != nil {
					return err
				}
				_, err = tx.Exec(t.Context(), `UPDATE principals SET kind='person',linked_to=$2 WHERE id=$1`, id, f.p.ID)
			case "keyed", "paired":
				var keyID string
				err = tx.QueryRow(t.Context(), `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,revoked_at) VALUES($1,$2,'Synthetic fixture',$2::text,repeat('a',64),CASE WHEN $3 THEN clock_timestamp() ELSE NULL END) RETURNING id::text`, f.p.TenantID, id, reason == "paired").Scan(&keyID)
				if err != nil || reason != "paired" {
					return err
				}
				var requestID string
				if err = tx.QueryRow(t.Context(), `INSERT INTO agent_pairing_requests(tenant_id,id,user_code,device_hash,runtime_hash,lifecycle_hash,details,request_digest) VALUES($1,gen_random_uuid(),'123456789',repeat('a',64),repeat('b',64),repeat('c',64),'{}','fixture') RETURNING id::text`, f.p.TenantID).Scan(&requestID); err != nil {
					return err
				}
				_, err = tx.Exec(t.Context(), `INSERT INTO agent_pairing_computers(tenant_id,id,request_id,principal_id,key_id,daemon_id,lifecycle_hash,state) VALUES($1,gen_random_uuid(),$2,$3,$4,'fixture',repeat('c',64),'revoked')`, f.p.TenantID, requestID, id, keyID)
			case "builtin":
				_, err = tx.Exec(t.Context(), `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE key='admin') WHERE principal_id=$1`, id)
			case "extra_binding":
				_, err = tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1,$2,$3,'workspace')`, f.p.TenantID, id, role)
			case "overscoped":
				_, err = tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'keys.manage')`, f.p.TenantID, role)
			case "undergranted":
				_, err = tx.Exec(t.Context(), `DELETE FROM role_permissions WHERE role_id=$1 AND permission='run.create'`, role)
			}
			return err
		})
	}
	// A revoked key without a pairing is allowed by the existing PUT rules.
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,revoked_at) VALUES($1,$2,'Synthetic revoked key',$2::text,repeat('d',64),clock_timestamp())`, f.p.TenantID, secondAgent)
		return err
	})
	// A full page of ineligible rows must have a cursor, even with no items.
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name)
 SELECT $1,format('00000000-0000-4000-8000-%s',lpad(i::text,12,'0'))::uuid,'person','Unrelated person' FROM generate_series(1,100) i`, f.p.TenantID)
		return err
	})
	const foreignTenant = "90000000-0000-4000-8000-000000000001"
	foreign := tenant.Principal{TenantID: foreignTenant, Kind: tenant.Person, BrowserSession: true}
	const foreignAgent = "00000000-0000-4000-8000-000000000000"
	if err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, foreignTenant, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO tenants(id,slug,name) VALUES($1,'foreign-picker','Foreign picker')`, foreignTenant); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Foreign owner') RETURNING id::text`, foreignTenant).Scan(&foreign.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name,agent_access_configured) VALUES($1,$2,'agent','Private foreign label',true)`, foreignTenant, foreignAgent); err != nil {
			return err
		}
		var role string
		if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'foreign_execution','Foreign execution') RETURNING id::text`, foreignTenant).Scan(&role); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT $1,$2,unnest($3::text[])`, foreignTenant, role, permissions); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1,$2,$3,'workspace')`, foreignTenant, foreignAgent, role)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, f.d, foreign.TenantID, foreign.ID, "owner")
	ineligible = append(ineligible, foreignAgent)
	path := func(item Recurrence) string { return "/api/recurrences/" + item.ID + "/execution-principals" }
	collect := func(item Recurrence) []executionPrincipal {
		t.Helper()
		var items []executionPrincipal
		after := ""
		for pageNumber := 0; pageNumber < 5; pageNumber++ {
			var page executionPrincipalPage
			raw := f.call(f.p, "GET", path(item)+"?after="+after, nil, 200)
			decoder := json.NewDecoder(strings.NewReader(string(raw)))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&page); err != nil {
				t.Fatalf("private or invalid response fields: %v", err)
			}
			if page.Items == nil || len(page.Items) > 100 {
				t.Fatalf("invalid bounded items: %+v", page)
			}
			for _, principal := range page.Items {
				if principal.ID <= after || principal.Label == "" || principal.ID == foreignAgent {
					t.Fatalf("invalid or foreign principal: %+v", principal)
				}
			}
			items = append(items, page.Items...)
			if page.NextCursor == nil {
				return items
			}
			if *page.NextCursor <= after {
				t.Fatal("cursor did not advance")
			}
			after = *page.NextCursor
		}
		t.Fatal("pagination did not terminate")
		return nil
	}
	var first executionPrincipalPage
	if err := json.Unmarshal(f.call(f.p, "GET", path(project), nil, 200), &first); err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 0 || first.NextCursor == nil {
		t.Fatalf("sparse page did not continue: %+v", first)
	}
	consent := func(item Recurrence, id string, status int) Recurrence {
		t.Helper()
		raw := f.call(f.p, "PUT", "/api/recurrences/"+item.ID+"/execution-consent", map[string]any{"expected_revision": item.Revision, "execute_consent": true, "execution_principal_id": id}, status)
		if status != 200 {
			if !strings.Contains(string(raw), "permission") {
				t.Fatalf("identity refused for the wrong reason: %s", raw)
			}
			return item
		}
		var out Recurrence
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		if out.ExecutionPolicy == nil || !out.ExecutionPolicy.Eligible {
			t.Fatalf("listed identity did not enable consent: %+v", out.ExecutionPolicy)
		}
		return out
	}
	for _, tc := range []struct {
		item Recurrence
		want []string
	}{
		{personal, []string{f.p.ID}},
		{project, []string{projectAgent, secondAgent}},
		{workspace, []string{workspaceAgent}},
	} {
		items := collect(tc.item)
		got := make([]string, len(items))
		for i, principal := range items {
			got[i] = principal.ID
			wantKind := "agent"
			if tc.item.ID == personal.ID {
				wantKind = "person"
			}
			if principal.Kind != wantKind {
				t.Fatalf("wrong principal kind: %+v", principal)
			}
		}
		slices.Sort(tc.want)
		if !slices.Equal(got, tc.want) {
			t.Fatalf("%s identities = %v, want %v", tc.item.Definition.Scope.Kind, got, tc.want)
		}
		if current := f.get(tc.item.ID); current.Revision != tc.item.Revision || current.ExecutionPolicy.Eligible {
			t.Fatal("listing mutated the routine or granted execution")
		}
		for _, id := range got {
			tc.item = consent(tc.item, id, 200)
		}
		if tc.item.ID == project.ID {
			project = tc.item
		}
	}
	for _, id := range ineligible {
		consent(project, id, 403)
	}
	consent(f.get(personal.ID), projectAgent, 403)
	consent(f.get(workspace.ID), projectAgent, 403)
	// Output authority alone cannot read a source in another project. The
	// same workspace identity can read it using its single workspace role.
	source := f.node("work", &otherProject, "External project source")
	in.Definition.Assignment.Sources = []SourceReference{{Kind: "node", ID: source}}
	in.Definition.Scope = DefinitionScope{Kind: "project", ProjectID: f.project}
	projectSource := f.create(in)
	if items := collect(projectSource); len(items) != 0 {
		t.Fatalf("unreadable source admitted identities: %+v", items)
	}
	consent(projectSource, projectAgent, 403)
	in.Definition.Scope = DefinitionScope{Kind: "workspace"}
	workspaceSource := f.create(in)
	if items := collect(workspaceSource); len(items) != 1 || items[0].ID != workspaceAgent {
		t.Fatalf("workspace source identities: %+v", items)
	}
	consent(workspaceSource, workspaceAgent, 200)
	// Revocation after a successful list wins in both GET and the final PUT.
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'keys.manage')`, f.p.TenantID, projectRole)
		return err
	})
	remaining := collect(project)
	if len(remaining) != 1 || remaining[0].ID != secondAgent {
		t.Fatalf("revoked identity still listed: %+v", remaining)
	}
	consent(project, projectAgent, 403)
	f.call(foreign, "GET", path(project), nil, 404)
	alias := tenant.Principal{ID: aliasID, TenantID: f.p.TenantID, Kind: tenant.Person, BrowserSession: true}
	f.call(alias, "GET", path(personal), nil, 200)
	viewer := viewer(f)
	viewer.BrowserSession = true
	f.call(viewer, "GET", path(project), nil, 403)
	admin := projectPrincipal(f, "admin")
	admin.BrowserSession = true
	f.call(admin, "GET", path(project), nil, 403)
	f.call(admin, "GET", path(workspace), nil, 403)
	f.call(admin, "GET", path(personal), nil, 404)
	agent := tenant.Principal{ID: secondAgent, TenantID: f.p.TenantID, Kind: tenant.Agent, Scopes: permissions}
	f.call(agent, "GET", path(project), nil, 403)
	f.call(f.p, "GET", path(project)+"?after=broken", nil, 400)
	f.call(f.p, "GET", "/api/recurrences/broken/execution-principals", nil, 400)
	f.call(f.p, "GET", "/api/recurrences/99999999-0000-4000-8000-000000000000/execution-principals", nil, 404)
	bearer := httptest.NewRequest("GET", path(project), nil)
	bearer.Header.Set("Authorization", "Bearer synthetic-fixture")
	bearer = bearer.WithContext(tenant.WithPrincipal(bearer.Context(), f.p))
	out := httptest.NewRecorder()
	f.handler.ServeHTTP(out, bearer)
	if out.Code != 403 || !strings.Contains(out.Body.String(), "interactive_person_required") {
		t.Fatalf("bearer picker = %d %s", out.Code, out.Body.String())
	}
	ticketOnly := f.create(f.input())
	f.call(f.p, "GET", path(ticketOnly), nil, 409)
	f.call(f.p, "DELETE", "/api/recurrences/"+project.ID, map[string]any{"expected_revision": project.Revision}, 204)
	f.call(f.p, "GET", path(project), nil, 404)
}
