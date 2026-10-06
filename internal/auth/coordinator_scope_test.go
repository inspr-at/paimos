// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/harness"
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

// Exercise the matched production routes with real person sessions and bearer
// keys, rather than preloading a principal directly into the handler context.
func TestProjectLeadThroughProductionAuthentication(t *testing.T) {
	reset(t)
	ctx := t.Context()
	tid := insertTenant(t, "lead-auth", "Lead authorization")
	m := newMod(t, Config{Env: "prod"})
	owner := tenant.Principal{TenantID: tid, Kind: tenant.Person}
	if err := adminPool.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Lead owner') RETURNING id::text`, tid).Scan(&owner.ID); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, testDB, tid, owner.ID, "admin")
	key, err := m.createAgentKey(ctx, owner, "lead-auth", "", append(append([]string{}, authz.CoordinatorKeyScopes...), "harness.control"), nil)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := m.createAgentKey(ctx, owner, "lead-reader", "", []string{"harness.read", "nodes.read"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	unrelated, err := m.createAgentKey(ctx, owner, "unrelated-lead-worker", "", authz.CoordinatorKeyScopes, nil)
	if err != nil {
		t.Fatal(err)
	}
	controlOnly, err := m.createAgentKey(ctx, owner, "lead-control-only", "", []string{"nodes.read", "harness.control"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Prepare a control-only role to verify owner pause without worker authority.
	var project, identity, role string
	if err := testInTenant(ctx, appPool, tid, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'LA-1','Project' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, tid).Scan(&project); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO roles(tenant_id,key,name) VALUES($1,'lead_owner','Lead owner') RETURNING id::text`, tid).Scan(&role); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT $1,$2,unnest($3::text[])`, tid, role, []string{"nodes.read", "harness.read", "harness.write", "harness.control", "run.create"}); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := adminPool.QueryRow(ctx, `INSERT INTO identities(issuer,subject) VALUES('lead-auth','owner') RETURNING id::text`).Scan(&identity); err != nil {
		t.Fatal(err)
	}
	cookie, err := m.startSession(tenant.WithPrincipal(ctx, owner), identity, tid, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	otherOwner := tenant.Principal{TenantID: tid, Kind: tenant.Person}
	if err := adminPool.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Other person') RETURNING id::text`, tid).Scan(&otherOwner.ID); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, testDB, tid, otherOwner.ID, "admin")
	var otherIdentity string
	if err := adminPool.QueryRow(ctx, `INSERT INTO identities(issuer,subject) VALUES('lead-auth','other') RETURNING id::text`).Scan(&otherIdentity); err != nil {
		t.Fatal(err)
	}
	otherCookie, err := m.startSession(tenant.WithPrincipal(ctx, otherOwner), otherIdentity, tid, otherOwner.ID)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	admission := func(ctx context.Context, tx pgx.Tx, _ tenant.Principal, _ string, _ harness.Session) (harness.LeadChecks, error) {
		var now time.Time
		err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now)
		g := harness.LeadGate{State: "available", CheckedAt: now}
		return harness.LeadChecks{Dial: g, Harness: g, Account: g, Host: g}, err
	}
	harness.NewWithLeadAdmission(appPool, admission).Mount(mux)
	secured := m.Middleware(mux)
	call := func(token, method, path string, body any, lease string, want int) map[string]any {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
		if token == "other-person" {
			r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: otherCookie})
		} else if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		} else {
			r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: cookie})
		}
		r.Header.Set("X-Aeon-Worker-Lease", lease)
		_, r.Pattern = mux.Handler(r)
		w := httptest.NewRecorder()
		secured.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: got %d want %d: %s", method, path, w.Code, want, w.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	base := "/api/projects/" + project
	for _, token := range []string{"", key.Token, reader.Token} {
		if got := call(token, "GET", base+"/lead", nil, "", 200); got["state"] != "none" {
			t.Fatalf("implicit lead: %v", got)
		}
	}
	call(key.Token, "POST", base+"/lead", map[string]any{"expected_revision": 0}, "", 403)
	l := call("", "POST", base+"/lead", map[string]any{"expected_revision": 0}, "", 200)
	lease := "lead-auth-lease-000000000000000000000000"
	s := call("", "POST", base+"/harness-sessions", map[string]any{"agent_principal_id": key.PrincipalID, "harness": "codex", "host": "local", "management_mode": "unmanaged", "role": "coordinator", "harness_session_ref": "lead-auth-reference", "worker_lease": lease, "advertised_capabilities": []string{"inbox", "pause"}}, "", 201)
	claim := map[string]any{"expected_revision": l["revision"], "session_id": s["id"]}
	call(reader.Token, "POST", base+"/lead/claim", claim, lease, 403)
	if got := call(key.Token, "POST", base+"/lead/claim", claim, "wrong-proof-000000000000000000000000000", 403); got["error"] != "harness worker proof rejected" {
		t.Fatalf("claim rejected for wrong reason: %v", got)
	}
	l = call(key.Token, "POST", base+"/lead/claim", claim, lease, 200)
	if l["generation"] != float64(1) {
		t.Fatalf("claim=%v", l)
	}
	call(key.Token, "POST", base+"/harness-sessions/"+s["id"].(string)+"/heartbeat", map[string]any{"phase": "working", "activity": "idle", "activity_sequence": 1}, lease, 200)
	yield := map[string]any{"expected_revision": l["revision"], "generation": 1}
	call(reader.Token, "POST", base+"/lead/yield", yield, lease, 403)
	call(controlOnly.Token, "POST", base+"/lead/yield", yield, lease, 403)
	if got := call(key.Token, "POST", base+"/lead/yield", yield, "wrong-proof-000000000000000000000000000", 403); got["error"] != "harness worker proof rejected" {
		t.Fatalf("yield rejected for wrong reason: %v", got)
	}
	l = call(key.Token, "POST", base+"/lead/yield", yield, lease, 200)
	if l["state"] != "paused" || l["reason"] != "idle_yield" || l["process_active"] != true {
		t.Fatalf("yield=%v", l)
	}
	pause := map[string]any{"expected_revision": l["revision"], "generation": 1}
	call(reader.Token, "POST", base+"/lead/pause", pause, lease, 403)
	call(controlOnly.Token, "POST", base+"/lead/pause", pause, lease, 403)
	if got := call("other-person", "POST", base+"/lead/pause", pause, "", 403); got["error"] != "lead owner required" {
		t.Fatalf("nonowner rejected for wrong reason: %v", got)
	}
	if got := call(unrelated.Token, "POST", base+"/lead/pause", pause, lease, 403); got["error"] != "current lead worker proof required" {
		t.Fatalf("unrelated worker rejected for wrong reason: %v", got)
	}
	if got := call(key.Token, "POST", base+"/lead/pause", pause, "wrong-proof-000000000000000000000000000", 403); got["error"] != "current lead worker proof required" {
		t.Fatalf("pause rejected for wrong reason: %v", got)
	}
	l = call(key.Token, "POST", base+"/lead/pause", pause, lease, 200)
	if l["state"] != "paused" || l["process_active"] != true {
		t.Fatalf("worker pause=%v", l)
	}
	if err := testInTenant(ctx, appPool, tid, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE role_bindings SET role_id=$2 WHERE principal_id=$1`, owner.ID, role)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	l = call("", "POST", base+"/lead/pause", map[string]any{"expected_revision": l["revision"], "generation": 1}, "", 200)
	if l["state"] != "paused" || l["process_active"] != true {
		t.Fatalf("owner pause=%v", l)
	}
}

// Fair-turn scheduling proves another lead through the key bound to its
// generation, exactly as production authentication would see that key. A
// stronger key of the same principal, a key of another principal, a revoked
// or expired key, or no bound key at all never qualifies a lead.
func TestQueueDispatcherQualifiesOnlyTheBoundKey(t *testing.T) {
	reset(t)
	ctx := t.Context()
	tid := insertTenant(t, "dispatch-key", "Dispatch key binding")
	m := newMod(t, Config{Env: "prod"})
	owner := tenant.Principal{TenantID: tid, Kind: tenant.Person}
	if err := adminPool.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Dispatch owner') RETURNING id::text`, tid).Scan(&owner.ID); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, testDB, tid, owner.ID, "admin")
	strong, err := m.createAgentKey(ctx, owner, "dual-key-lead", "", authz.CoordinatorKeyScopes, nil)
	if err != nil {
		t.Fatal(err)
	}
	weak, err := m.createAgentKey(ctx, owner, "", strong.PrincipalID, []string{"harness.read", "harness.write", "harness.worker", "nodes.read"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := m.createAgentKey(ctx, owner, "other-lead", "", authz.CoordinatorKeyScopes, nil)
	if err != nil {
		t.Fatal(err)
	}
	var project string
	if err := testInTenant(ctx, appPool, tid, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'DK-1','Project' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, tid).Scan(&project); err != nil {
			return err
		}
		var role string
		if err := tx.QueryRow(ctx, `INSERT INTO roles(tenant_id,key,name) VALUES($1,'queue_lead','Queue lead') RETURNING id::text`, tid).Scan(&role); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT $1,$2,unnest($3::text[])`, tid, role, authz.CoordinatorBaseScopes); err != nil {
			return err
		}
		for _, agent := range []string{strong.PrincipalID, foreign.PrincipalID} {
			if _, err := tx.Exec(ctx, `DELETE FROM role_bindings WHERE principal_id=$1`, agent); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1,$2,$3,'workspace')`, tid, agent, role); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	qualify := func(agent, key string) error {
		t.Helper()
		var result error
		if err := testInTenant(ctx, appPool, tid, func(tx pgx.Tx) error {
			result = authz.RequireQueueDispatcherTx(ctx, tx, tid, agent, key, project)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return result
	}
	cases := []struct {
		name       string
		agent, key string
		want       error
	}{
		{"no bound key", strong.PrincipalID, "", authz.ErrForbidden},
		{"malformed key id", strong.PrincipalID, "not-a-key", authz.ErrForbidden},
		{"bound restricted key with stronger unused key", strong.PrincipalID, weak.ID, authz.ErrForbidden},
		{"bound coordinator key", strong.PrincipalID, strong.ID, nil},
		{"another principal's coordinator key", strong.PrincipalID, foreign.ID, authz.ErrForbidden},
	}
	for _, c := range cases {
		if got := qualify(c.agent, c.key); !errors.Is(got, c.want) || (c.want == nil) != (got == nil) {
			t.Fatalf("%s: got %v want %v", c.name, got, c.want)
		}
	}
	// Live constraints of the bound key apply: expiry, then revocation.
	if _, err := adminPool.Exec(ctx, `UPDATE agent_keys SET expires_at=now()-interval '1 second' WHERE id=$1::uuid`, strong.ID); err != nil {
		t.Fatal(err)
	}
	if got := qualify(strong.PrincipalID, strong.ID); !errors.Is(got, authz.ErrForbidden) {
		t.Fatalf("expired bound key qualified: %v", got)
	}
	if _, err := adminPool.Exec(ctx, `UPDATE agent_keys SET expires_at=NULL,revoked_at=now() WHERE id=$1::uuid`, strong.ID); err != nil {
		t.Fatal(err)
	}
	if got := qualify(strong.PrincipalID, strong.ID); !errors.Is(got, authz.ErrForbidden) {
		t.Fatalf("revoked bound key qualified: %v", got)
	}
	// Live grants of the principal still apply to the bound key.
	if _, err := adminPool.Exec(ctx, `UPDATE agent_keys SET revoked_at=NULL WHERE id=$1::uuid`, strong.ID); err != nil {
		t.Fatal(err)
	}
	if got := qualify(strong.PrincipalID, strong.ID); got != nil {
		t.Fatalf("restored bound key: %v", got)
	}
	if _, err := adminPool.Exec(ctx, `DELETE FROM role_bindings WHERE principal_id=$1::uuid`, strong.PrincipalID); err != nil {
		t.Fatal(err)
	}
	if got := qualify(strong.PrincipalID, strong.ID); !errors.Is(got, authz.ErrForbidden) {
		t.Fatalf("bound key without live coordinator grants qualified: %v", got)
	}
}
