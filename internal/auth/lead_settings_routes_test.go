// SPDX-License-Identifier: AGPL-3.0-only
package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestLeadSettingsAgentRoutesReadOnly(t *testing.T) {
	project := "/api/projects/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/lead-settings"
	for _, tc := range []struct{ method, path, scope string }{
		{"GET", project, "nodes.read"}, {"HEAD", project, "nodes.read"},
		{"PUT", project, ""}, {"DELETE", project, ""}, {"POST", project, ""},
		{"GET", project + "/extra", ""}, {"GET", "/api/projects/bad/lead-settings", ""},
		{"GET", "/api/settings/lead-policy", ""}, {"PUT", "/api/settings/lead-policy", ""},
	} {
		scope, allowed := coreAgentScope(httptest.NewRequest(tc.method, tc.path, nil))
		if scope != tc.scope || allowed != (tc.scope != "") {
			t.Fatalf("%s %s: scope %q allowed %v", tc.method, tc.path, scope, allowed)
		}
	}
}

// Use the real handlers behind production pattern resolution, authentication
// and authorization: direct handler calls cannot detect undeclared routes.
func TestLeadSettingsProductionMiddleware(t *testing.T) {
	reset(t)
	m := newMod(t, Config{})
	handler := (&httpapi.Server{
		Pool: appPool, Modules: []httpapi.Module{m, modelregistry.New(appPool)},
		Middleware: []func(http.Handler) http.Handler{m.Middleware},
	}).Handler()
	tid := insertTenant(t, "lead-middleware", "Lead middleware")
	var project, hidden string
	if err := testInTenant(t.Context(), appPool, tid, func(tx pgx.Tx) error {
		for i, id := range []*string{&project, &hidden} {
			if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,state)
				SELECT $1,id,$2,'Lead middleware','active' FROM node_kinds WHERE slug='project' RETURNING id::text`, tid, []string{"LEAD-1", "LEAD-2"}[i]).Scan(id); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	session := func(name, role string, projectOnly bool) string {
		t.Helper()
		id, identity := signinPerson(t, tid, name, name, name+"@example.com", name+"@example.com", "guest")
		if projectOnly {
			if err := testInTenant(t.Context(), appPool, tid, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id)
					SELECT $1,$2,id,'project',$3 FROM roles WHERE key=$4`, tid, id, project, role)
				return err
			}); err != nil {
				t.Fatal(err)
			}
		} else {
			dbtest.BindRole(t, testDB, tid, id, role)
		}
		token, err := m.startSession(t.Context(), identity, tid, id)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	owner := session("owner", "admin", false)
	manager := session("manager", "admin", true)
	member := session("member", "member", false)
	key, err := m.createAgentKey(t.Context(), tenant.Principal{TenantID: tid}, "lead-reader", "", []string{"nodes.read"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := func(t *testing.T, method, path, body, token string, agent bool, want int) []byte {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if agent {
			r.Header.Set("Authorization", "Bearer "+token)
		} else if token != "" {
			r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: got %d, want %d: %s", method, path, w.Code, want, w.Body.Bytes())
		}
		return w.Body.Bytes()
	}
	const workspace = "/api/settings/lead-policy"
	path := "/api/projects/" + project + "/lead-settings"
	for _, tc := range []struct{ method, suffix, body string }{
		{"GET", "", ""}, {"PUT", "", `{"revision":0,"overrides":{}}`}, {"DELETE", "?revision=0", ""},
	} {
		for _, target := range []string{workspace, path} {
			request(t, tc.method, target+tc.suffix, tc.body, "", false, 401)
		}
		request(t, tc.method, workspace+tc.suffix, tc.body, manager, false, 403)
		request(t, tc.method, workspace+tc.suffix, tc.body, member, false, 403)
		request(t, tc.method, workspace+tc.suffix, tc.body, key.Token, true, 403)
		if tc.method != "GET" {
			request(t, tc.method, path+tc.suffix, tc.body, member, false, 403)
			request(t, tc.method, path+tc.suffix, tc.body, key.Token, true, 403)
		}
	}
	for _, tc := range []struct{ name, path, token string }{
		{"workspace", workspace, owner}, {"project", path, manager},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request(t, "GET", tc.path, "", tc.token, false, 200)
			for _, change := range []struct {
				method, suffix, body string
				revision             int64
			}{
				{"PUT", "", `{"revision":0,"overrides":{"bucket":"complex"}}`, 1},
				{"DELETE", "?revision=1", "", 2},
			} {
				var saved modelregistry.LeadSettings
				if err := json.Unmarshal(request(t, change.method, tc.path+change.suffix, change.body, tc.token, false, 200), &saved); err != nil {
					t.Fatal(err)
				}
				if saved.Revision != change.revision {
					t.Fatalf("mutation did not commit: %+v", saved)
				}
			}
		})
	}
	for _, reader := range []struct {
		token string
		agent bool
	}{{member, false}, {key.Token, true}} {
		var view modelregistry.LeadSettings
		if err := json.Unmarshal(request(t, "GET", path, "", reader.token, reader.agent, 200), &view); err != nil {
			t.Fatal(err)
		}
		if !view.DetailsRedacted || view.Overrides != nil || view.ModelSelector != nil || view.Revision != 2 {
			t.Fatalf("nonowner view not redacted: %+v", view)
		}
	}
	request(t, "GET", "/api/projects/"+hidden+"/lead-settings", "", manager, false, 403)
	var settings, changes int
	if err := adminPool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM project_lead_settings WHERE tenant_id=$1),
		(SELECT count(*) FROM events WHERE tenant_id=$1 AND type='lead.settings_changed')`, tid).Scan(&settings, &changes); err != nil {
		t.Fatal(err)
	}
	if settings != 2 || changes != 4 {
		t.Fatalf("denied requests changed settings/history: rows=%d events=%d", settings, changes)
	}
}
