// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/nodes"
	"github.com/inspr-at/paimos/internal/statusautopilot"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestStatusHelpCustomerSessionDenied(t *testing.T) {
	reset(t)
	tid := insertTenant(t, "status-help-customers", "Status help customers")
	m, err := New(Config{Env: envDev, SessionKey: make([]byte, 32)}, appPool)
	if err != nil {
		t.Fatal(err)
	}
	handler := (&httpapi.Server{
		Pool: appPool, Modules: []httpapi.Module{m, nodes.New(appPool, nil)},
		Middleware: []func(http.Handler) http.Handler{m.Middleware},
	}).Handler()
	settings := statusautopilot.Defaults()
	settings.Rules["accept"] = statusautopilot.Rule{Enabled: true, Days: 45}
	rules, err := json.Marshal(settings.Rules)
	if err != nil {
		t.Fatal(err)
	}
	var project string
	if err := testInTenant(t.Context(), appPool, tid, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title)
			SELECT $1::uuid,id,'SH-1','Status project' FROM node_kinds WHERE tenant_id=$1::uuid AND slug='project'
			RETURNING id::text`, tid).Scan(&project); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO status_autopilot_settings(tenant_id,enabled,rules) VALUES($1,true,$2)
			ON CONFLICT(tenant_id) DO UPDATE SET enabled=true,rules=excluded.rules`, tid, rules)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"customer", "member", "guest"} {
		t.Run(role, func(t *testing.T) {
			id, identity := signinPerson(t, tid, role, role, role+"@example.invalid", role+"@example.invalid", role)
			token, err := m.startSession(t.Context(), identity, tid, id)
			if err != nil {
				t.Fatal(err)
			}
			request := func(method, path string) *httptest.ResponseRecorder {
				t.Helper()
				r := httptest.NewRequest(method, path, nil)
				r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				return w
			}
			// Prove this is a live, correctly identified session, not a 401.
			me := request(http.MethodGet, "/api/me")
			var identityResponse struct{ Principal tenant.Principal }
			if me.Code != http.StatusOK || json.Unmarshal(me.Body.Bytes(), &identityResponse) != nil || identityResponse.Principal.ID != id || len(identityResponse.Principal.Roles) != 1 || identityResponse.Principal.Roles[0] != role {
				t.Fatalf("session identity: status %d, body %s", me.Code, me.Body.String())
			}
			for _, path := range []string{"/api/status/help", "/api/status/help?project_id=" + project} {
				for _, method := range []string{http.MethodGet, http.MethodHead} {
					w := request(method, path)
					if role == "member" {
						var help struct {
							LimitsSource string `json:"limits_source"`
							Autopilot    struct {
								Rules map[string]statusautopilot.Rule
							}
						}
						if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &help) != nil || help.LimitsSource != "workspace" || help.Autopilot.Rules["accept"].Days != 45 {
							t.Fatalf("member %s %s missed live settings: status %d, body %s", method, path, w.Code, w.Body.String())
						}
						continue
					}
					var denial struct {
						Code       string `json:"code"`
						ReasonCode string `json:"reason_code"`
					}
					if w.Code != http.StatusForbidden || json.Unmarshal(w.Body.Bytes(), &denial) != nil || denial.Code != "forbidden" || denial.ReasonCode != "missing_role_permission" {
						t.Fatalf("%s %s %s must fail at the permission gate: status %d, body %s", role, method, path, w.Code, w.Body.String())
					}
					if strings.Contains(w.Body.String(), "autopilot") || strings.Contains(w.Body.String(), "Status project") {
						t.Fatal("denial leaked live settings or project data")
					}
				}
			}
		})
	}
}

// Exercise the production middleware and handler together: handler-only tests
// cannot catch a missing agent allowlist entry or an unwanted nodes.read gate.
func TestStatusHelpAgentMetadataAndIsolation(t *testing.T) {
	m, owner := keyFixture(t)
	empty := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "status-only", "scopes": []string{}}))
	reader := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "project-status", "scopes": []string{"nodes.read"}}))
	coordinator := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "aeon-coordinator", "scopes": []string{"nodes.read", "harness.worker", "inbox.read", "inbox.send"}}))
	paired := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "paired-status", "scopes": []string{}}))
	ctx := dbtest.Seed(t.Context())
	var project, hiddenProject, otherTenant, foreignProject, computer string
	settings := statusautopilot.Defaults()
	settings.Rules["accept"] = statusautopilot.Rule{Enabled: true, Days: 45}
	rules, err := json.Marshal(settings.Rules)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
		for i, target := range []*string{&project, &hiddenProject} {
			if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title)
				SELECT $1::uuid,id,$2,$2 FROM node_kinds WHERE tenant_id=$1::uuid AND slug='project'
				RETURNING id::text`, owner.TenantID, []string{"SH-1", "SH-2"}[i]).Scan(target); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM role_bindings WHERE principal_id=ANY($1::uuid[])`, []string{empty.PrincipalID, reader.PrincipalID, paired.PrincipalID}); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id)
			SELECT $1::uuid,$2::uuid,id,'project',$3::uuid FROM roles WHERE tenant_id=$1::uuid AND key='viewer'`, owner.TenantID, reader.PrincipalID, project); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO status_autopilot_settings(tenant_id,enabled,rules) VALUES($1,true,$2)
			ON CONFLICT(tenant_id) DO UPDATE SET enabled=true,rules=excluded.rules`, owner.TenantID, rules); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO status_autopilot_projects(tenant_id,project_id,mode) VALUES($1,$2,'off')`, owner.TenantID, project)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.pool.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('foreign-status','Foreign status') RETURNING id::text`).Scan(&otherTenant); err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(ctx, m.pool, otherTenant, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title)
			SELECT $1::uuid,id,'FOREIGN-1','Foreign' FROM node_kinds WHERE tenant_id=$1::uuid AND slug='project'
			RETURNING id::text`, otherTenant).Scan(&foreignProject)
	}); err != nil {
		t.Fatal(err)
	}
	handler := (&httpapi.Server{
		Pool: m.pool, Modules: []httpapi.Module{m, nodes.New(m.pool, nil), statusautopilot.New(m.pool)},
		Middleware: []func(http.Handler) http.Handler{m.Middleware},
	}).Handler()
	request := func(token, method, path, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: status %d, want %d: %s", method, path, w.Code, want, w.Body.String())
		}
		return w
	}
	const path = "/api/status/help"
	for _, key := range []agentKeyCreatedJSON{empty, reader, coordinator} {
		body := request(key.Token, http.MethodGet, path+"?tenant_id="+otherTenant, "", http.StatusOK)
		if body.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("status metadata is cacheable")
		}
		var help struct {
			Definitions  []struct{ State, Meaning string }
			LimitsSource string `json:"limits_source"`
			Autopilot    struct {
				EffectiveEnabled bool                            `json:"effective_enabled"`
				Rules            map[string]statusautopilot.Rule `json:"rules"`
			}
		}
		if err := json.Unmarshal(body.Body.Bytes(), &help); err != nil {
			t.Fatal(err)
		}
		if help.LimitsSource != "workspace" || !help.Autopilot.EffectiveEnabled || help.Autopilot.Rules["accept"].Days != 45 || len(help.Definitions) != 11 {
			t.Fatalf("agent missed tenant's live definitions: %s", body.Body.String())
		}
		if help.Definitions[9].State != "cancelled" || !strings.Contains(help.Definitions[9].Meaning, "destination ticket") {
			t.Fatal("agent cannot resolve the merged-ticket status")
		}
		request(key.Token, http.MethodHead, path, "", http.StatusOK)
		request(key.Token, http.MethodGet, path+"?project_id="+foreignProject, "", http.StatusNotFound)
	}
	request(empty.Token, http.MethodGet, path+"?project_id="+project, "", http.StatusNotFound)
	request(reader.Token, http.MethodGet, path+"?project_id="+hiddenProject, "", http.StatusNotFound)
	request(reader.Token, http.MethodGet, path+"?project_id=broken", "", http.StatusBadRequest)
	for _, key := range []agentKeyCreatedJSON{reader, coordinator} {
		body := request(key.Token, http.MethodGet, path+"?project_id="+project, "", http.StatusOK)
		var help struct {
			ProjectID   string `json:"project_id"`
			ProjectName string `json:"project_name"`
			Autopilot   struct {
				EffectiveEnabled bool   `json:"effective_enabled"`
				ProjectMode      string `json:"project_mode"`
			}
		}
		if err := json.Unmarshal(body.Body.Bytes(), &help); err != nil {
			t.Fatal(err)
		}
		if help.ProjectID != project || help.ProjectName != "SH-1" || help.Autopilot.ProjectMode != "off" || help.Autopilot.EffectiveEnabled {
			t.Fatalf("visible project override missing: %s", body.Body.String())
		}
	}
	for _, key := range []agentKeyCreatedJSON{empty, reader, coordinator} {
		denied := request(key.Token, http.MethodPatch, "/api/nodes/"+project, `{"title":"Forbidden"}`, http.StatusForbidden)
		if !strings.Contains(denied.Body.String(), "permission denied") {
			t.Fatal("node write failed for the wrong reason")
		}
		request(key.Token, http.MethodPut, "/api/settings/status-autopilot", `{"enabled":false}`, http.StatusForbidden)
		request(key.Token, http.MethodPut, "/api/projects/"+project+"/status-autopilot", `{"mode":"on"}`, http.StatusForbidden)
	}
	request(empty.Token, http.MethodGet, "/api/nodes", "", http.StatusForbidden)
	request(empty.Token, http.MethodGet, "/api/events", "", http.StatusForbidden)
	for _, token := range []string{"", "invalid"} {
		request(token, http.MethodGet, path, "", http.StatusUnauthorized)
	}

	// Paired runtime keys retain the live-computer fence before reading metadata.
	if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO agent_pairing_requests(tenant_id,id,user_code,device_hash,runtime_hash,lifecycle_hash,details,request_digest,state)
			VALUES($1,gen_random_uuid(),'123456789',repeat('a',64),repeat('a',64),repeat('a',64),'{}','synthetic','redeemed') RETURNING id::text`, owner.TenantID).Scan(&computer); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO agent_pairing_computers(tenant_id,id,request_id,principal_id,key_id,daemon_id,lifecycle_hash,setup_state)
			VALUES($1,$2,$2,$3,$4,'status-test-daemon',repeat('b',64),'connected')`, owner.TenantID, computer, paired.PrincipalID, paired.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	request(paired.Token, http.MethodGet, path, "", http.StatusOK)
	request(paired.Token, http.MethodGet, path+"?project_id="+project, "", http.StatusNotFound)
	request(paired.Token, http.MethodPut, "/api/settings/status-autopilot", `{"enabled":false}`, http.StatusForbidden)
	if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE agent_pairing_computers SET state='revoked' WHERE id=$1`, computer); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE agent_keys SET revoked_at=now() WHERE id=$1`, empty.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	request(paired.Token, http.MethodGet, path, "", http.StatusUnauthorized)
	request(empty.Token, http.MethodGet, path, "", http.StatusUnauthorized)
}
