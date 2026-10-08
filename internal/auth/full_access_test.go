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
		_, err := tx.Exec(ctx, `DELETE FROM role_bindings WHERE principal_id=$1`, owner.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := authz.Require(authz.BindPool(tenant.WithPrincipal(ctx, p), m.pool), "nodes.write", authz.Scope{ProjectID: project}); !errors.Is(err, authz.ErrForbidden) {
		t.Fatal("full-access key escaped its creator's current permissions")
	}
}
