// SPDX-License-Identifier: AGPL-3.0-only
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Reconstruct a pre-migration key; the guard is restored before any assertion.
func legacyKey(t *testing.T, m *Module, owner tenant.Principal, id string) {
	t.Helper()
	err := db.InTenant(dbtest.Seed(t.Context()), m.pool, owner.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `ALTER TABLE agent_keys DISABLE TRIGGER agent_key_person_owner_guard`); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE agent_keys SET person_owner_required=false,created_by_principal_id=NULL WHERE id=$1`, id); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `ALTER TABLE agent_keys ENABLE TRIGGER agent_key_person_owner_guard`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}
func adoptionRequest(m *Module, p tenant.Principal, id string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/api/agent-keys/"+id+"/adopt", nil)
	r.SetPathValue("id", id)
	if p.ID != "" {
		r = r.WithContext(tenant.WithPrincipal(dbtest.Seed(r.Context()), p))
	}
	w := httptest.NewRecorder()
	m.handleAdoptAgentKey(w, r)
	return w
}
func TestAdoptAgentKeyPreservesSecretAndAudits(t *testing.T) {
	m, owner := keyFixture(t)
	key := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "legacy", "scopes": []string{"agents.plan.read"}}))
	legacyKey(t, m, owner, key.ID)
	count, events := keyCounts(t, m, owner)
	w := adoptionRequest(m, owner, key.ID)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("adopt status=%d", w.Code)
	}
	for _, secret := range []string{"token", "hash", key.Token} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("adoption exposed credential material")
		}
	}
	var result agentKeyJSON
	if json.Unmarshal(w.Body.Bytes(), &result) != nil || result.CreatedByPrincipalID == nil || *result.CreatedByPrincipalID != owner.ID || result.ID != key.ID || result.Prefix != key.Prefix {
		t.Fatal("incorrect metadata")
	}
	prefix, secret, _ := parseBearer("Bearer " + key.Token)
	p, ok, err := m.authenticateAgent(dbtest.Seed(t.Context()), prefix, secret)
	if err != nil || !ok || p.KeyCreatorID != owner.ID {
		t.Fatal("original key lost authentication or creator")
	}
	after, afterEvents := keyCounts(t, m, owner)
	if after != count || afterEvents != events+1 {
		t.Fatal("adoption rotated or missed audit")
	}
	err = db.InTenant(dbtest.Seed(t.Context()), m.pool, owner.TenantID, func(tx pgx.Tx) error {
		var complete bool
		err := tx.QueryRow(t.Context(), `SELECT count(*)=1 AND bool_and(actor_principal_id=$1::uuid AND before->'created_by_principal_id'='null'::jsonb AND after->>'created_by_principal_id'=$1::text AND after->>'key_id'=$2 AND NOT(after ? 'token') AND NOT(after ? 'hash')) FROM events WHERE type='agent_key.adopted'`, owner.ID, key.ID).Scan(&complete)
		if err == nil && !complete {
			t.Error("incorrect adoption audit")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if w := adoptionRequest(m, owner, key.ID); w.Code != 409 {
		t.Fatalf("takeover status=%d", w.Code)
	}
}
func TestAdoptAgentKeyAuthorizationTenantAndState(t *testing.T) {
	m, owner := keyFixture(t)
	key := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "legacy", "scopes": []string{"nodes.read"}}))
	legacyKey(t, m, owner, key.ID)
	limited := keyScopeEditor(t, m, owner, "nodes.read")
	for _, tc := range []struct {
		p      tenant.Principal
		id     string
		status int
	}{
		{tenant.Principal{}, key.ID, 401}, {limited, key.ID, 403},
		{tenant.Principal{ID: key.PrincipalID, TenantID: owner.TenantID, Kind: tenant.Agent, Scopes: []string{"keys.manage"}, OwnerWorkstation: true, KeyCreatorID: owner.ID}, key.ID, 403},
		{owner, "invalid", 400}, {owner, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", 404},
	} {
		if w := adoptionRequest(m, tc.p, tc.id); w.Code != tc.status {
			t.Fatalf("adopt status=%d want=%d", w.Code, tc.status)
		}
	}
	// A second tenant sees no key even with its own owner permission.
	var other tenant.Principal
	other.Kind = tenant.Person
	if err := m.pool.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('adopt-other','Other') RETURNING id::text`).Scan(&other.TenantID); err != nil {
		t.Fatal(err)
	}
	other.ID = dbtest.KeyPerson(t, m.pool, other.TenantID)
	if w := adoptionRequest(m, other, key.ID); w.Code != 404 {
		t.Fatalf("cross tenant status=%d", w.Code)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), m.pool, owner.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_keys SET expires_at=now()-interval '1 hour' WHERE id=$1`, key.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if w := adoptionRequest(m, owner, key.ID); w.Code != 409 {
		t.Fatalf("inactive status=%d", w.Code)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), m.pool, owner.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_keys SET expires_at=NULL,revoked_at=now() WHERE id=$1`, key.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if w := adoptionRequest(m, owner, key.ID); w.Code != 409 {
		t.Fatalf("revoked adoption status=%d", w.Code)
	}
}
func TestAdoptAgentKeyConcurrentNoTakeover(t *testing.T) {
	m, owner := keyFixture(t)
	key := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "legacy", "scopes": []string{"nodes.read"}}))
	legacyKey(t, m, owner, key.ID)
	editor := keyScopeEditor(t, m, owner, "keys.manage", "nodes.read")
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, p := range []tenant.Principal{owner, editor} {
		wg.Go(func() { <-start; _, err := m.adoptAgentKey(dbtest.Seed(t.Context()), p, key.ID); results <- err })
	}
	close(start)
	wg.Wait()
	close(results)
	won, lost := 0, 0
	for err := range results {
		if err == nil {
			won++
		} else if errors.Is(err, errKeyOwned) {
			lost++
		} else {
			t.Fatalf("unexpected result: %v", err)
		}
	}
	if won != 1 || lost != 1 {
		t.Fatalf("won=%d refused=%d", won, lost)
	}
}
func TestAgentKeyCreationRequiresPersonCreator(t *testing.T) {
	m, owner := keyFixture(t)
	for _, p := range []tenant.Principal{{TenantID: owner.TenantID}, {TenantID: owner.TenantID, KeyCreatorID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}, {ID: owner.ID, TenantID: owner.TenantID, Kind: tenant.Agent}} {
		if _, err := m.createAgentKey(dbtest.Seed(t.Context()), p, "bad", "", []string{"nodes.read"}, nil); !errors.Is(err, authz.ErrForbidden) {
			t.Fatalf("creator denial=%v", err)
		}
	}
	if _, _, _, err := OperatorCreateAgentKey(t.Context(), m.pool, owner.TenantID, "bad", "", nil, nil); err == nil {
		t.Fatal("operator accepted missing creator")
	}
	key, _, _, err := OperatorCreateAgentKey(t.Context(), m.pool, owner.TenantID, "explicit", "", []string{"nodes.read"}, nil, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), m.pool, owner.TenantID, func(tx pgx.Tx) error {
		var creator string
		err := tx.QueryRow(t.Context(), `SELECT created_by_principal_id::text FROM agent_keys WHERE id=$1`, key).Scan(&creator)
		if err == nil && creator != owner.ID {
			t.Error("operator creator missing")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAdoptAgentKeyRechecksPermissionInsideWrite(t *testing.T) {
	m, owner := keyFixture(t)
	key := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "legacy", "scopes": []string{"nodes.read"}}))
	legacyKey(t, m, owner, key.ID)
	editor := keyScopeEditor(t, m, owner, "keys.manage", "nodes.read")
	ctx := dbtest.Seed(t.Context())
	// Prior authorization succeeds, then a barrier holds the final write until
	// the current grant has been removed under the same tenant fence.
	if err := authz.Require(authz.BindPool(tenant.WithPrincipal(ctx, editor), m.pool), "keys.manage", authz.Scope{}); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	original := m.inTenant
	m.inTenant = func(ctx context.Context, pool *pgxpool.Pool, tid string, fn func(pgx.Tx) error) error {
		return original(ctx, pool, tid, func(tx pgx.Tx) error { close(entered); <-release; return fn(tx) })
	}
	result := make(chan error, 1)
	go func() { _, err := m.adoptAgentKey(ctx, editor, key.ID); result <- err }()
	<-entered
	if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR NO KEY UPDATE`, owner.TenantID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM role_permissions WHERE permission='keys.manage' AND role_id IN(SELECT role_id FROM role_bindings WHERE principal_id=$1)`, editor.ID)
		return err
	}); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	if err := <-result; !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("stale authorization accepted: %v", err)
	}
	m.inTenant = original
	keys, err := m.listAgentKeys(ctx, owner.TenantID)
	if err != nil || len(keys) != 1 || keys[0].CreatedByPrincipalID != nil {
		t.Fatal("denied adoption changed owner")
	}
}
