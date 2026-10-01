// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

func keyFixture(t *testing.T) (*Module, tenant.Principal) {
	t.Helper()
	d := dbtest.Open(t)
	p := tenant.Principal{Kind: tenant.Person, Name: "Owner"}
	ctx := dbtest.Seed(t.Context())
	if err := db.InTenant(ctx, d.App, "00000000-0000-0000-0000-000000000000", func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('rotation','Rotation') RETURNING id::text`).Scan(&p.TenantID)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(ctx, d.App, p.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1::uuid,'person','Owner',ARRAY['super_admin']) RETURNING id::text`, p.TenantID).Scan(&p.ID); err != nil {
			return err
		}
		return dbtest.BindLegacyTx(ctx, tx, p.TenantID, p.ID)
	}); err != nil {
		t.Fatal(err)
	}
	return &Module{pool: d.App, inTenant: db.InTenant}, p
}

func keyRequest(m *Module, p tenant.Principal, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPost, "/api/agent-keys", strings.NewReader(string(b)))
	if p.ID != "" {
		r = r.WithContext(tenant.WithPrincipal(r.Context(), p))
	}
	w := httptest.NewRecorder()
	m.handleCreateAgentKey(w, r)
	return w
}

func decodeKey(t *testing.T, w *httptest.ResponseRecorder) agentKeyCreatedJSON {
	t.Helper()
	// Never include response bodies in test failures: successful ones hold a secret.
	if w.Code != http.StatusCreated {
		t.Fatalf("create/rotate status = %d", w.Code)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("key response is cacheable")
	}
	var key agentKeyCreatedJSON
	if err := json.Unmarshal(w.Body.Bytes(), &key); err != nil {
		t.Fatal("invalid key response")
	}
	if key.Token == "" {
		t.Fatal("missing one-time token")
	}
	return key
}

func keyCounts(t *testing.T, m *Module, p tenant.Principal) (keys, events int) {
	t.Helper()
	if err := db.InTenant(dbtest.Seed(t.Context()), m.pool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM agent_keys),(SELECT count(*) FROM events)`).Scan(&keys, &events)
	}); err != nil {
		t.Fatal(err)
	}
	return
}

func TestKeyExpiryAndAtomicRotation(t *testing.T) {
	m, owner := keyFixture(t)
	ctx := dbtest.Seed(t.Context())
	for _, days := range []int{0, 30, 90, 365} {
		t.Run(fmt.Sprintf("lifetime-%d", days), func(t *testing.T) {
			var expiry any
			if days > 0 {
				expiry = time.Now().UTC().Add(time.Duration(days) * 24 * time.Hour).Truncate(time.Second)
			}
			key := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "worker", "scopes": []string{"nodes.read"}, "expires_at": expiry}))
			if days == 0 && key.ExpiresAt != nil || days > 0 && (key.ExpiresAt == nil || !key.ExpiresAt.Equal(expiry.(time.Time))) {
				t.Fatal("expiry was not preserved")
			}
		})
	}
	for _, expiry := range []any{"bad-date", "2020-01-01T00:00:00Z", 90, "2026-02-30T12:00:00Z"} {
		if w := keyRequest(m, owner, map[string]any{"name": "bad", "expires_at": expiry}); w.Code != 400 {
			t.Fatalf("bad expiry status = %d", w.Code)
		}
	}
	old := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "rotator", "scopes": []string{"nodes:read", "events.read"}}))
	beforeKeys, beforeEvents := keyCounts(t, m, owner)
	expires := time.Now().UTC().Add(90 * 24 * time.Hour).Truncate(time.Second)
	next := decodeKey(t, keyRequest(m, owner, map[string]any{"rotate_key_id": old.ID, "expires_at": expires}))
	if next.ID == old.ID || next.Token == old.Token || next.PrincipalID != old.PrincipalID || next.Name != old.Name || !slices.Equal(next.Scopes, old.Scopes) || next.ExpiresAt == nil || !next.ExpiresAt.Equal(expires) {
		t.Fatal("rotation did not preserve agent/scopes/name with a new key and expiry")
	}
	keys, events := keyCounts(t, m, owner)
	if keys != beforeKeys+1 || events != beforeEvents+2 {
		t.Fatalf("rotation count deltas: keys=%d events=%d", keys-beforeKeys, events-beforeEvents)
	}
	for _, check := range []struct {
		token string
		valid bool
	}{{old.Token, false}, {next.Token, true}} {
		prefix, secret, _ := parseBearer("Bearer " + check.token)
		p, ok, err := m.authenticateAgent(ctx, prefix, secret)
		if err != nil || ok != check.valid || ok && p.KeyCreatorID != owner.ID {
			t.Fatal("rotation authentication or creator ceiling failed")
		}
	}
	if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
		var complete bool
		if err := tx.QueryRow(ctx, `SELECT count(*)=2 FROM events WHERE type IN ('agent_key.created','agent_key.revoked') AND actor_principal_id=$1::uuid AND
			((type='agent_key.created' AND after->>'key_id'=$2 AND after->>'expires_at' IS NOT NULL)
			OR (type='agent_key.revoked' AND before->>'key_id'=$3 AND before->>'revoked_at' IS NULL AND after->>'revoked_at' IS NOT NULL))`, owner.ID, next.ID, old.ID).Scan(&complete); err != nil {
			return err
		}
		if !complete {
			return errors.New("missing complete attributed audit snapshots")
		}
		var leaked bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE (COALESCE(before::text,'') || COALESCE(after::text,'')) LIKE '%"token"%' OR (COALESCE(before::text,'') || COALESCE(after::text,'')) LIKE '%"hash"%' OR strpos((COALESCE(before::text,'') || COALESCE(after::text,'')),$1)>0 OR strpos((COALESCE(before::text,'') || COALESCE(after::text,'')),$2)>0)`, old.Token, next.Token).Scan(&leaked); err != nil {
			return err
		}
		if leaked {
			return errors.New("key material entered events")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if w := keyRequest(m, owner, map[string]any{"rotate_key_id": old.ID}); w.Code != 409 {
		t.Fatalf("repeated rotation status = %d", w.Code)
	}
	listed, err := m.listAgentKeys(ctx, owner.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range listed {
		if key.Token != "" {
			t.Fatal("list leaked a secret")
		}
	}
	// Expired keys can be replaced, with a fresh expiry or explicitly never.
	if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE agent_keys SET expires_at=now()-interval '1 minute' WHERE id=$1::uuid`, next.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	final := decodeKey(t, keyRequest(m, owner, map[string]any{"rotate_key_id": next.ID, "expires_at": nil}))
	if final.ExpiresAt != nil {
		t.Fatal("never expiry not preserved on rotation")
	}
}

func TestRotationScopeProposalIsAtomicAndCannotExtendRole(t *testing.T) {
	m, owner := keyFixture(t)
	old := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "proposal-worker", "scopes": []string{"nodes.read"}}))
	beforeKeys, beforeEvents := keyCounts(t, m, owner)
	for _, tc := range []struct {
		body   map[string]any
		status int
	}{
		{map[string]any{"rotation_scopes": []string{"nodes.read"}, "principal_id": old.PrincipalID}, 400},
		{map[string]any{"rotate_key_id": old.ID, "rotation_scopes": []string{"keys.manage"}}, 400},
		{map[string]any{"rotate_key_id": old.ID, "rotation_scopes": []string{"unknown.scope"}}, 400},
		// Generated private roles also cannot be extended by a pasted proposal.
		{map[string]any{"rotate_key_id": old.ID, "rotation_scopes": []string{"nodes.write"}}, 403},
	} {
		if w := keyRequest(m, owner, tc.body); w.Code != tc.status {
			t.Fatalf("proposal status = %d, want %d", w.Code, tc.status)
		}
		keys, events := keyCounts(t, m, owner)
		if keys != beforeKeys || events != beforeEvents {
			t.Fatal("rejected proposal left changes")
		}
	}
	ctx := dbtest.Seed(t.Context())
	if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission)
		SELECT tenant_id,role_id,'events.read' FROM role_bindings WHERE principal_id=$1::uuid AND scope_type='workspace'`, old.PrincipalID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	limited := keyScopeEditor(t, m, owner, "keys.manage", "nodes.read")
	beforeKeys, beforeEvents = keyCounts(t, m, owner)
	if w := keyRequest(m, limited, map[string]any{"rotate_key_id": old.ID, "rotation_scopes": []string{"events.read"}}); w.Code != 403 {
		t.Fatal("creator ceiling bypassed")
	}
	keys, events := keyCounts(t, m, owner)
	if keys != beforeKeys || events != beforeEvents {
		t.Fatal("denied actor changed key or audit")
	}
	next := decodeKey(t, keyRequest(m, owner, map[string]any{"rotate_key_id": old.ID, "rotation_scopes": []string{"events:read"}}))
	if !slices.Equal(next.Scopes, []string{"events.read"}) || next.PrincipalID != old.PrincipalID {
		t.Fatal("replacement did not use explicit proposal")
	}
	keys, events = keyCounts(t, m, owner)
	if keys != beforeKeys+1 || events != beforeEvents+2 {
		t.Fatal("replacement and revocation were not atomic or changed a role")
	}
	listed, err := m.listAgentKeys(ctx, owner.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range listed {
		if key.ID == old.ID && (key.RevokedAt == nil || !slices.Equal(key.Scopes, old.Scopes)) {
			t.Fatal("original key was not revoked intact")
		}
	}
	empty := decodeKey(t, keyRequest(m, owner, map[string]any{"rotate_key_id": next.ID, "rotation_scopes": []string{}}))
	if len(empty.Scopes) != 0 {
		t.Fatal("explicit empty proposal inherited scopes")
	}
}

func TestKeyRotationAuthorizationAndRollback(t *testing.T) {
	m, owner := keyFixture(t)
	ctx := dbtest.Seed(t.Context())
	old := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "worker", "scopes": []string{"nodes.read", "nodes.write"}}))
	beforeKeys, beforeEvents := keyCounts(t, m, owner)
	for _, body := range []map[string]any{
		{"rotate_key_id": "not-a-uuid"},
		{"rotate_key_id": old.ID, "scopes": []string{}},
		{"rotate_key_id": old.ID, "name": "different"},
		{"rotate_key_id": old.ID, "principal_id": old.PrincipalID},
		{"rotate_key_id": old.ID, "expires_at": "2020-01-01T00:00:00Z"},
	} {
		if w := keyRequest(m, owner, body); w.Code != 400 {
			t.Fatalf("invalid rotation status = %d", w.Code)
		}
	}
	if w := keyRequest(m, owner, map[string]any{"rotate_key_id": "00000000-0000-0000-0000-000000000000"}); w.Code != 404 {
		t.Fatalf("missing key status = %d", w.Code)
	}
	if w := keyRequest(m, tenant.Principal{}, map[string]any{"rotate_key_id": old.ID}); w.Code != 401 {
		t.Fatalf("anonymous status = %d", w.Code)
	}
	agent := owner
	agent.Kind = tenant.Agent
	if w := keyRequest(m, agent, map[string]any{"rotate_key_id": old.ID}); w.Code != 403 {
		t.Fatalf("agent status = %d", w.Code)
	}
	// Actor can manage keys but cannot regrant the old write scope.
	limited := tenant.Principal{TenantID: owner.TenantID, Kind: tenant.Person}
	if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1::uuid,'person','Limited') RETURNING id::text`, owner.TenantID).Scan(&limited.ID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `WITH r AS (INSERT INTO roles(tenant_id,key,name) VALUES($1::uuid,'limited','Limited') RETURNING id), perms AS
			(INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT $1::uuid,id,unnest(ARRAY['keys.manage','nodes.read']) FROM r)
			INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) SELECT $1::uuid,$2::uuid,id,'workspace' FROM r`, owner.TenantID, limited.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if w := keyRequest(m, limited, map[string]any{"rotate_key_id": old.ID}); w.Code != 403 {
		t.Fatalf("creator grant ceiling status = %d", w.Code)
	}
	// Commit failure after both writes must roll back replacement, revocation and events.
	m.inTenant = func(ctx context.Context, pool *pgxpool.Pool, tid string, f func(pgx.Tx) error) error {
		return db.InTenant(ctx, pool, tid, func(tx pgx.Tx) error {
			if err := f(tx); err != nil {
				return err
			}
			return errors.New("injected transaction failure")
		})
	}
	if w := keyRequest(m, owner, map[string]any{"rotate_key_id": old.ID}); w.Code != 500 || strings.Contains(w.Body.String(), "token") {
		t.Fatal("failed transaction returned success or key material")
	}
	m.inTenant = db.InTenant
	keys, events := keyCounts(t, m, owner)
	if keys != beforeKeys || events != beforeEvents {
		t.Fatal("failed rotation left rows or audit events")
	}
	listed, err := m.listAgentKeys(ctx, owner.TenantID)
	if err != nil || len(listed) != 1 || listed[0].RevokedAt != nil {
		t.Fatal("failed rotation revoked the old key")
	}
	// Current agent role is also an outer ceiling.
	if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE key='viewer') WHERE principal_id=$1::uuid AND scope_type='workspace'`, old.PrincipalID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if w := keyRequest(m, owner, map[string]any{"rotate_key_id": old.ID}); w.Code != 403 {
		t.Fatalf("agent role ceiling status = %d", w.Code)
	}
	// RLS hides an existing key from a different tenant's owner.
	other := tenant.Principal{Kind: tenant.Person}
	if err := db.InTenant(ctx, m.pool, "00000000-0000-0000-0000-000000000000", func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('other','Other') RETURNING id::text`).Scan(&other.TenantID)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(ctx, m.pool, other.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1::uuid,'person','Other',ARRAY['super_admin']) RETURNING id::text`, other.TenantID).Scan(&other.ID); err != nil {
			return err
		}
		return dbtest.BindLegacyTx(ctx, tx, other.TenantID, other.ID)
	}); err != nil {
		t.Fatal(err)
	}
	if w := keyRequest(m, other, map[string]any{"rotate_key_id": old.ID}); w.Code != 404 {
		t.Fatalf("cross tenant status = %d", w.Code)
	}
}

func TestKeyRotationConcurrentConfirmation(t *testing.T) {
	m, owner := keyFixture(t)
	old := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "worker", "scopes": []string{"nodes.read"}}))
	beforeKeys, beforeEvents := keyCounts(t, m, owner)
	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	for range 2 {
		wg.Go(func() { statuses <- keyRequest(m, owner, map[string]any{"rotate_key_id": old.ID}).Code })
	}
	wg.Wait()
	close(statuses)
	got := []int{}
	for status := range statuses {
		got = append(got, status)
	}
	slices.Sort(got)
	if !slices.Equal(got, []int{201, 409}) {
		t.Fatalf("concurrent statuses = %v", got)
	}
	keys, events := keyCounts(t, m, owner)
	if keys != beforeKeys+1 || events != beforeEvents+2 {
		t.Fatal("concurrent confirmation created multiple replacements")
	}
}
