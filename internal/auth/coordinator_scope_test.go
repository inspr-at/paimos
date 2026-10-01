// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/rules"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestCoordinatorKeysThroughRealHandlers(t *testing.T) {
	reset(t)
	ctx := t.Context()
	tenantID := insertTenant(t, "coord-ceiling", "Coordinator ceiling")
	m := newMod(t, Config{})
	mux := http.NewServeMux()
	modelregistry.New(appPool).Mount(mux)
	rules.New(appPool).Mount(mux)
	handler := m.Middleware(mux)

	owner := tenant.Principal{TenantID: tenantID, Kind: tenant.Person}
	if err := adminPool.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Coordinator owner') RETURNING id::text`, tenantID).Scan(&owner.ID); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, testDB, tenantID, owner.ID, "admin")
	var projectID string
	if err := testInTenant(ctx, appPool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'CK-1','Project' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, tenantID).Scan(&projectID)
	}); err != nil {
		t.Fatal(err)
	}
	// Seed actual rule resources through the real handler as their human owner.
	seed := func(method, path, body string, out any) {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req = req.WithContext(tenant.WithPrincipal(ctx, owner))
		res := httptest.NewRecorder()
		mux.ServeHTTP(res, req)
		if res.Code != http.StatusOK {
			t.Fatalf("seed %s: %d %s", path, res.Code, res.Body.String())
		}
		if err := json.Unmarshal(res.Body.Bytes(), out); err != nil {
			t.Fatal(err)
		}
	}
	var layer rules.Layer
	seed(http.MethodPost, "/api/rules/layers", `{"layer":"project","project_id":"`+projectID+`"}`, &layer)
	var set rules.Set
	seed(http.MethodPost, "/api/rules/sets", `{"layer_id":"`+layer.ID+`","name":"Planning rules"}`, &set)
	var company rules.Layer
	seed(http.MethodPost, "/api/rules/layers", `{"layer":"company"}`, &company)
	var floor rules.Set
	seed(http.MethodPost, "/api/rules/sets", `{"layer_id":"`+company.ID+`","name":"Safety"}`, &floor)
	seed(http.MethodPut, "/api/rules/sets/"+floor.ID+"/draft", `{"expected_revision":1,"name":"Safety","rules":[{"identity":"safety","text":"Preserve scoped ownership.","why":"Safety floor","strength":"locked","enabled":true,"source":{"reference":"test","edited_here":true}}]}`, &floor)
	var published rules.Snapshot
	seed(http.MethodPost, "/api/rules/sets/"+floor.ID+"/publish", `{"expected_revision":2,"version":"260929160000.0.0"}`, &published)
	var nodeReaderRole string
	if err := adminPool.QueryRow(ctx, `INSERT INTO roles(tenant_id,key,name) VALUES($1,'node_reader','Node reader') RETURNING id::text`, tenantID).Scan(&nodeReaderRole); err != nil {
		t.Fatal(err)
	}
	if _, err := adminPool.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'nodes.read')`, tenantID, nodeReaderRole); err != nil {
		t.Fatal(err)
	}

	call := func(t *testing.T, token, method, path string, want int) []byte {
		t.Helper()
		req := httptest.NewRequest(method, path, nil)
		_, req.Pattern = mux.Handler(req)
		req.Header.Set("Authorization", "Bearer "+token)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s %s: %d %s, want %d", method, path, res.Code, res.Body.String(), want)
		}
		return res.Body.Bytes()
	}
	for _, tc := range []struct {
		name   string
		scopes []string
	}{{"historical", authz.CoordinatorBaseScopes}, {"current", authz.CoordinatorKeyScopes}} {
		t.Run(tc.name, func(t *testing.T) {
			for _, humanCreator := range []bool{false, true} {
				name, actor := tc.name+"-operator", tenant.Principal{TenantID: tenantID}
				if humanCreator {
					name, actor = tc.name+"-human", owner
				}
				key, err := m.createAgentKey(ctx, actor, name, "", tc.scopes, nil)
				if err != nil {
					t.Fatal(err)
				}
				perms := workspacePermissions(t, tenantID, key.PrincipalID)
				if slices.Contains(perms, "rules.read") || !slices.Contains(perms, "models.read") {
					t.Fatalf("%s workspace role permissions = %v", name, perms)
				}
				call(t, key.Token, http.MethodGet, "/api/models/resolve?role=build-hard", http.StatusOK)
				call(t, key.Token, http.MethodGet, "/api/models", http.StatusOK)
				paths := []string{
					"/api/rules/layers",
					"/api/rules/sets?layer_id=" + layer.ID,
					"/api/rules/sets/" + set.ID,
					"/api/rules/merged?project_id=" + projectID + "&person_id=" + owner.ID + "&agent_id=" + key.PrincipalID + "&role=coordinator&harness=codex",
					"/api/rules/comparisons?project_id=" + projectID,
				}
				for _, path := range paths {
					if humanCreator {
						call(t, key.Token, http.MethodGet, path, http.StatusOK)
					} else {
						raw := call(t, key.Token, http.MethodGet, path, http.StatusForbidden)
						var body struct {
							Code string `json:"code"`
						}
						if err := json.Unmarshal(raw, &body); err != nil || body.Code != "rules_owner_required" {
							t.Fatalf("operator refusal must explain the ownership requirement: %s", raw)
						}
					}
				}
				call(t, key.Token, http.MethodPost, "/api/rules/layers", http.StatusForbidden)
				call(t, key.Token, http.MethodPost, "/api/models", http.StatusForbidden)
				// Stored scopes remain intact. The next real request must observe
				// removal of all live bindings for either kind of coordinator key.
				if _, err := adminPool.Exec(ctx, `DELETE FROM role_bindings WHERE tenant_id=$1 AND principal_id=$2`, tenantID, key.PrincipalID); err != nil {
					t.Fatal(err)
				}
				call(t, key.Token, http.MethodGet, "/api/models/resolve?role=build-hard", http.StatusForbidden)
				call(t, key.Token, http.MethodGet, "/api/models", http.StatusForbidden)
				call(t, key.Token, http.MethodGet, "/api/rules/layers", http.StatusForbidden)
				// A different, narrower live role is not a coordinator binding.
				if _, err := adminPool.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1,$2,$3,'workspace')`, tenantID, key.PrincipalID, nodeReaderRole); err != nil {
					t.Fatal(err)
				}
				call(t, key.Token, http.MethodGet, "/api/models/resolve?role=build-hard", http.StatusForbidden)
				call(t, key.Token, http.MethodGet, "/api/rules/layers", http.StatusForbidden)
			}
		})
	}
	runtime, err := m.createAgentKey(ctx, tenant.Principal{TenantID: tenantID}, "paired-runtime", "", agentpairing.RuntimePermissions, nil)
	if err != nil {
		t.Fatal(err)
	}
	call(t, runtime.Token, http.MethodGet, "/api/models/resolve?role=scout", http.StatusOK)
	call(t, runtime.Token, http.MethodGet, "/api/rules/layers", http.StatusForbidden)
	creatorCapped, err := m.createAgentKey(ctx, owner, "creator-capped", "", authz.CoordinatorBaseScopes, nil)
	if err != nil {
		t.Fatal(err)
	}
	call(t, creatorCapped.Token, http.MethodGet, "/api/models/resolve?role=build-hard", http.StatusOK)
	if _, err := adminPool.Exec(ctx, `DELETE FROM role_bindings WHERE tenant_id=$1 AND principal_id=$2`, tenantID, owner.ID); err != nil {
		t.Fatal(err)
	}
	call(t, creatorCapped.Token, http.MethodGet, "/api/models/resolve?role=build-hard", http.StatusForbidden)
	call(t, creatorCapped.Token, http.MethodGet, "/api/rules/layers", http.StatusForbidden)
}

func workspacePermissions(t *testing.T, tenantID, principalID string) []string {
	t.Helper()
	var out []string
	err := testInTenant(t.Context(), appPool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(t.Context(), `SELECT rp.permission FROM role_permissions rp
			JOIN role_bindings b ON b.tenant_id=rp.tenant_id AND b.role_id=rp.role_id
			WHERE b.principal_id=$1::uuid AND b.scope_type='workspace' ORDER BY rp.permission`, principalID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var permission string
			if err := rows.Scan(&permission); err != nil {
				return err
			}
			out = append(out, permission)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
