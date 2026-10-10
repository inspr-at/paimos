// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/statusautopilot"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risk: a new permission must never strand a full-access key, or expand human
// authority. Iterate the actual catalog through both single and bulk decisions.
func TestFullAccessLiveCatalogAndBoundaries(t *testing.T) {
	m, owner := keyFixture(t)
	narrow := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "catalog-worker", "scopes": []string{"nodes.read"}}))
	ctx := dbtest.Seed(t.Context())
	if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
		for _, permission := range authz.Registry {
			if _, err := tx.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission)
 SELECT tenant_id,role_id,$2 FROM role_bindings WHERE principal_id=$1 AND scope_type='workspace'
 ON CONFLICT DO NOTHING`, narrow.PrincipalID, permission.Key); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	full := decodeKey(t, keyRequest(m, owner, map[string]any{"principal_id": narrow.PrincipalID, "full_access": true}))
	if !full.FullAccess || len(full.Scopes) != 0 {
		t.Fatal("full-access creation stored or reported a snapshot")
	}
	authenticate := func(key agentKeyCreatedJSON) tenant.Principal {
		t.Helper()
		prefix, secret, _ := parseBearer("Bearer " + key.Token)
		p, ok, err := m.authenticateAgent(ctx, prefix, secret)
		if err != nil || !ok {
			t.Fatal("test key did not authenticate")
		}
		return p
	}
	p := authenticate(full)
	n := authenticate(narrow)
	for _, permission := range authz.Registry {
		t.Run(permission.Key, func(t *testing.T) {
			err := authz.Require(authz.BindPool(tenant.WithPrincipal(ctx, p), m.pool), permission.Key, authz.Scope{})
			if permission.AgentGrantable && err != nil || !permission.AgentGrantable && !errors.Is(err, authz.ErrForbidden) {
				t.Fatalf("permission %s agent-grantable=%v: %v", permission.Key, permission.AgentGrantable, err)
			}
			if authz.KeyAllows(p, permission.Key) != permission.AgentGrantable || slices.Contains(p.Scopes, permission.Key) != permission.AgentGrantable {
				t.Fatal("dynamic key ceiling differs from catalog")
			}
			err = authz.Require(authz.BindPool(tenant.WithPrincipal(ctx, n), m.pool), permission.Key, authz.Scope{})
			if permission.Key == "nodes.read" && err != nil || permission.Key != "nodes.read" && !errors.Is(err, authz.ErrForbidden) {
				t.Fatal("scoped key changed its ceiling")
			}
		})
	}
	if authz.KeyAllows(p, "future.unknown") || authz.Require(authz.BindPool(tenant.WithPrincipal(ctx, p), m.pool), "future.unknown", authz.Scope{}) == nil {
		t.Fatal("unknown permission granted")
	}
	if err := db.InTenant(tenant.WithPrincipal(ctx, p), m.pool, owner.TenantID, func(tx pgx.Tx) error {
		check, err := authz.ProjectsTx(ctx, tx, p)
		if err != nil {
			return err
		}
		for _, permission := range authz.Registry {
			if check(permission.Key, "") != permission.AgentGrantable {
				return errors.New("bulk authorization differs from catalog: " + permission.Key)
			}
		}
		var stored, audited bool
		if err := tx.QueryRow(ctx, `SELECT full_access AND cardinality(scopes)=0 FROM agent_keys WHERE id=$1`, full.ID).Scan(&stored); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE type='agent_key.created' AND after->>'key_id'=$1 AND after->>'full_access'='true')`, full.ID).Scan(&audited); err != nil {
			return err
		}
		if !stored || !audited {
			return errors.New("full-access property or audit missing")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	m.Mount(mux)
	r := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	r.Header.Set("Authorization", "Bearer "+full.Token)
	_, r.Pattern = mux.Handler(r)
	w := httptest.NewRecorder()
	m.Middleware(mux).ServeHTTP(w, r)
	var me meJSON
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &me) != nil || !me.FullAccess || me.OwnerWorkstation || w.Header().Get("Aeon-Contract") != "me/1.4" {
		t.Fatal("GET /api/me did not report the full-access key independently of workstation designation")
	}
	// The stored list is a historical snapshot. Marking the key must not make
	// the workstation filter trust that list over the live agent-grantable catalog.
	var computer string
	if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO agent_pairing_requests(tenant_id,id,user_code,device_hash,runtime_hash,lifecycle_hash,details,request_digest,state)
			VALUES($1,gen_random_uuid(),'991000991',$2,$2,$2,'{}','synthetic','redeemed') RETURNING id::text`,
			owner.TenantID, "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc").Scan(&computer); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO agent_pairing_computers(tenant_id,id,request_id,principal_id,key_id,daemon_id,lifecycle_hash,local_auth_public_key,setup_state)
			VALUES($1,$2,$2,$3,$4,'aeon-991-catalog',$5,$6,'connected')`,
			owner.TenantID, computer, full.PrincipalID, full.ID, "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE agent_keys SET owner_workstation=true,workstation_computer_id=$2,workstation_generation=1,scopes='{nodes.read}'
			WHERE id=$1 AND coalesce(full_access,false) AND cardinality(scopes)=0`, full.ID, computer)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("full-access key was not marked")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	marked := authenticate(full)
	if !marked.FullAccess || !marked.OwnerWorkstation || marked.KeyID == "" || marked.WorkstationComputerID == "" {
		t.Fatal("full-access key was not authenticated as a marked workstation key")
	}
	if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
		var snapshot bool
		if err := tx.QueryRow(ctx, `SELECT coalesce(full_access,false) AND owner_workstation AND NOT ('models.report'=ANY(scopes)) FROM agent_keys WHERE id=$1`, full.ID).Scan(&snapshot); err != nil {
			return err
		}
		if !snapshot {
			return errors.New("stored snapshot already contained the live permission")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := authz.Require(authz.BindPool(tenant.WithPrincipal(ctx, marked), m.pool), "models.report", authz.Scope{}); err != nil {
		t.Fatal("marked full-access key lost models.report from the live catalog")
	}
	if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE agent_keys SET owner_workstation=false,workstation_computer_id=NULL,scopes='{}',workstation_generation=workstation_generation+1 WHERE id=$1`, full.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM agent_pairing_computers WHERE tenant_id=$1 AND id=$2`, owner.TenantID, computer); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM agent_pairing_requests WHERE tenant_id=$1 AND id=$2`, owner.TenantID, computer)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	rotated := decodeKey(t, keyRequest(m, owner, map[string]any{"rotate_key_id": full.ID}))
	if !rotated.FullAccess || len(rotated.Scopes) != 0 || rotated.PrincipalID != full.PrincipalID {
		t.Fatal("rotation lost the dynamic mode or stored a snapshot")
	}
	p = authenticate(rotated)
	if err := activeTrimKey(trimKey{Active: true, FullAccess: true}, time.Now()); !errors.Is(err, errTrimFullAccess) {
		t.Fatal("full-access ceiling was eligible for a snapshot trim")
	}
	if w := scopesRequest(m, owner, rotated.ID, http.MethodPatch, `{"expires_at":null}`); w.Code != http.StatusOK {
		t.Fatal("expiry-only edit failed")
	}
	if w := scopesRequest(m, owner, rotated.ID, http.MethodPatch, `{"full_access":true,"add":["nodes.read"]}`); w.Code != http.StatusBadRequest {
		t.Fatal("full access accepted a scope snapshot")
	}
	if w := scopesRequest(m, owner, rotated.ID, http.MethodPatch, `{"full_access":false,"add":["nodes.read"]}`); w.Code != http.StatusOK {
		t.Fatal("explicit scoped-mode edit failed")
	}
	if err := authz.Require(authz.BindPool(tenant.WithPrincipal(ctx, p), m.pool), "models.report", authz.Scope{}); !errors.Is(err, authz.ErrForbidden) {
		t.Fatal("pre-edit authentication bypassed the current scoped ceiling")
	}
	p = authenticate(rotated)
	if p.FullAccess || authz.KeyAllows(p, "models.report") || !authz.KeyAllows(p, "nodes.read") {
		t.Fatal("explicit scoped mode did not take effect")
	}
	if w := scopesRequest(m, owner, rotated.ID, http.MethodPatch, `{"full_access":true}`); w.Code != http.StatusOK {
		t.Fatal("full-access mode could not be restored")
	}
	p = authenticate(rotated)
	rotationProjectBinding(t, m, owner, p.ID)
	var project string
	if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM role_bindings WHERE principal_id=$1 AND scope_type='workspace'`, p.ID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT scope_id::text FROM role_bindings WHERE principal_id=$1 AND scope_type='project'`, p.ID).Scan(&project)
	}); err != nil {
		t.Fatal(err)
	}
	for _, target := range []struct {
		id      string
		allowed bool
	}{
		{project, true}, {"", false}, {"11111111-1111-4111-8111-111111111111", false},
	} {
		err := authz.Require(authz.BindPool(tenant.WithPrincipal(ctx, p), m.pool), "nodes.write", authz.Scope{ProjectID: target.id})
		if target.allowed && err != nil || !target.allowed && !errors.Is(err, authz.ErrForbidden) {
			t.Fatal("full-access key changed project boundaries")
		}
		if err := db.InTenant(tenant.WithPrincipal(ctx, p), m.pool, p.TenantID, func(tx pgx.Tx) error {
			check, err := authz.ProjectsTx(ctx, tx, p)
			if err == nil && check("nodes.write", target.id) != target.allowed {
				return errors.New("full-access bulk check changed project boundaries")
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	otherTenant := p
	otherTenant.TenantID = "11111111-1111-4111-8111-111111111111"
	if err := authz.Require(authz.BindPool(tenant.WithPrincipal(ctx, otherTenant), m.pool), "nodes.write", authz.Scope{ProjectID: project}); !errors.Is(err, authz.ErrForbidden) {
		t.Fatal("full-access key crossed its tenant")
	}
	if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
		// Keep the workspace's last-owner safeguard intact while withdrawing
		// this key creator's grants; the second person remains an active owner.
		var backup string
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,roles)
 VALUES($1,'person','Remaining owner',ARRAY['super_admin']) RETURNING id::text`, owner.TenantID).Scan(&backup); err != nil {
			return err
		}
		if err := dbtest.BindLegacyTx(ctx, tx, owner.TenantID, backup); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM role_bindings WHERE principal_id=$1`, owner.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := authz.Require(authz.BindPool(tenant.WithPrincipal(ctx, p), m.pool), "nodes.write", authz.Scope{ProjectID: project}); !errors.Is(err, authz.ErrForbidden) {
		t.Fatal("full-access key escaped its creator's current permissions")
	}
}

// Risk: even a full-access key is rejected before the read handler, or the
// allowlist accidentally grants writes and unknown subpaths.
func TestStatusAutopilotFullAccessAgentReadAllowlist(t *testing.T) {
	m, owner := keyFixture(t)
	key := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "autopilot-reader", "full_access": true}))
	var project string
	if err := db.InTenant(dbtest.Seed(t.Context()), m.pool, owner.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,state) SELECT $1,id,'AUTO','Autopilot','open' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, owner.TenantID).Scan(&project)
	}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	statusautopilot.New(m.pool).Mount(mux)
	for _, path := range []string{"/api/settings/status-autopilot", "/api/projects/" + project + "/status-autopilot", "/api/status-autopilot/attention", "/api/status-autopilot/attention/groups?by=kind", "/api/status-autopilot/changes", "/api/status-autopilot/projects", "/api/status-autopilot/proposals"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if scope, ok := coreAgentScope(r); !ok || scope != "nodes.read" {
			t.Fatalf("read scope: %s %q %v", path, scope, ok)
		}
		r.Header.Set("Authorization", "Bearer "+key.Token)
		_, r.Pattern = mux.Handler(r)
		w := httptest.NewRecorder()
		m.Middleware(mux).ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
	}
	for _, route := range []struct{ method, path string }{
		{"PUT", "/api/settings/status-autopilot"}, {"PUT", "/api/projects/" + project + "/status-autopilot"},
		{"POST", "/api/status-autopilot/attention/actions"}, {"POST", "/api/status-autopilot/attention/bulk"},
		{"GET", "/api/status-autopilot/attention/bulk"}, {"GET", "/api/status-autopilot/unknown"},
		{"GET", "/api/projects/bad/status-autopilot"}, {"GET", "/api/status-autopilot/attention/groups/extra"},
	} {
		if scope, ok := coreAgentScope(httptest.NewRequest(route.method, route.path, nil)); ok || scope != "" {
			t.Fatalf("unexpected allowlist: %+v %s", route, scope)
		}
	}
}
