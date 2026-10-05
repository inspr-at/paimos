// SPDX-License-Identifier: AGPL-3.0-only
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

// Hold the first adoption after its ownership write, before audit/commit.
// The second operation must be observed waiting in Postgres before release.
type adoptionBarrierTx struct {
	pgx.Tx
	entered chan<- uint32
	release <-chan struct{}
}

func (tx adoptionBarrierTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	tag, err := tx.Tx.Exec(ctx, sql, args...)
	if err == nil && strings.HasPrefix(sql, "UPDATE agent_keys SET created_by_principal_id=") {
		select {
		case tx.entered <- tx.Conn().PgConn().PID():
		case <-ctx.Done():
			return tag, ctx.Err()
		}
		select {
		case <-tx.release:
		case <-ctx.Done():
			return tag, ctx.Err()
		}
	}
	return tag, err
}

func pauseAdoption(m *Module, entered chan<- uint32, release <-chan struct{}) {
	original := m.inTenant
	m.inTenant = func(ctx context.Context, pool *pgxpool.Pool, tid string, fn func(pgx.Tx) error) error {
		return original(ctx, pool, tid, func(tx pgx.Tx) error {
			return fn(adoptionBarrierTx{Tx: tx, entered: entered, release: release})
		})
	}
}

func waitAdoptionContention(t *testing.T, ctx context.Context, pool *pgxpool.Pool, blocker uint32, done <-chan error) {
	t.Helper()
	for {
		select {
		case err := <-done:
			t.Fatalf("operation completed before database contention: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		default:
		}
		var waiting bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE NOT granted AND $1::int=ANY(pg_blocking_pids(pid)))`, blocker).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		runtime.Gosched()
	}
}

func TestAdoptAgentKeyConcurrentNoTakeover(t *testing.T) {
	m, owner := keyFixture(t)
	key := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "legacy", "scopes": []string{"nodes.read"}}))
	legacyKey(t, m, owner, key.ID)
	editor := keyScopeEditor(t, m, owner, "keys.manage", "nodes.read")
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	entered, release := make(chan uint32, 1), make(chan struct{}, 1)
	defer func() { cancel(); close(release) }()
	pauseAdoption(m, entered, release)
	first, second := make(chan error, 1), make(chan error, 1)
	go func() { _, err := m.adoptAgentKey(ctx, owner, key.ID); first <- err }()
	var blocker uint32
	select {
	case blocker = <-entered:
	case err := <-first:
		t.Fatalf("first adoption did not reach ownership barrier: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	go func() { _, err := m.adoptAgentKey(ctx, editor, key.ID); second <- err }()
	waitAdoptionContention(t, ctx, m.pool, blocker, second)
	release <- struct{}{}
	if err := <-first; err != nil {
		t.Fatalf("winner failed: %v", err)
	}
	if err := <-second; !errors.Is(err, errKeyOwned) {
		t.Fatalf("contending takeover result: %v", err)
	}
	if err := db.InTenant(dbtest.Seed(ctx), m.pool, owner.TenantID, func(tx pgx.Tx) error {
		var creator string
		if err := tx.QueryRow(ctx, `SELECT created_by_principal_id::text FROM agent_keys WHERE id=$1`, key.ID).Scan(&creator); err != nil {
			return err
		}
		if creator != owner.ID {
			t.Error("contending adoption replaced winner ownership")
		}
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM events WHERE type='agent_key.adopted' AND actor_principal_id=$1 AND after->>'key_id'=$2`, owner.ID, key.ID).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			t.Errorf("winner adoption events=%d, want 1", count)
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM events WHERE type='agent_key.adopted'`).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			t.Errorf("total adoption events=%d, want exactly 1", count)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
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

func authenticateLegacyAdoptionKey(t *testing.T, m *Module, key agentKeyCreatedJSON) tenant.Principal {
	t.Helper()
	prefix, secret, _ := parseBearer("Bearer " + key.Token)
	p, ok, err := m.authenticateAgent(t.Context(), prefix, secret)
	if err != nil || !ok || p.KeyID != key.ID || p.KeyCreatorID != "" {
		t.Fatal("legacy authentication fixture lost its creatorless authority")
	}
	return p
}

func TestAdoptAgentKeyRejectsStaleCreatorAuthority(t *testing.T) {
	m, owner := keyFixture(t)
	key := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "legacy", "scopes": []string{"nodes.read", "nodes.write"}}))
	legacyKey(t, m, owner, key.ID)
	agent := authenticateLegacyAdoptionKey(t, m, key)
	ctx := tenant.WithPrincipal(t.Context(), agent)
	if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
		return authz.RequireTx(ctx, tx, agent, "nodes.write", authz.Scope{})
	}); err != nil {
		t.Fatalf("legacy request lacked original authority: %v", err)
	}
	// This person can adopt keys but cannot read/write project data.
	editor := keyScopeEditor(t, m, owner, "keys.manage")
	if _, err := m.adoptAgentKey(t.Context(), editor, key.ID); err != nil {
		t.Fatal(err)
	}
	entered := false
	err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
		entered = true
		return nil
	})
	if err == nil || err.Error() != "agent key authority changed" || entered {
		t.Fatalf("stale creator reached visibility/handler: entered=%v err=%v", entered, err)
	}
	if err := authz.Require(authz.BindPool(ctx, m.pool), "nodes.write", authz.Scope{}); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("stale route authorization result: %v", err)
	}
	// Direct transaction evaluators must reject stale snapshots too, even if
	// internal code supplied a context without its authenticating principal.
	if err := db.InTenant(dbtest.Seed(t.Context()), m.pool, owner.TenantID, func(tx pgx.Tx) error {
		if err := authz.RequireTx(t.Context(), tx, agent, "nodes.write", authz.Scope{}); !errors.Is(err, authz.ErrForbidden) {
			t.Fatalf("stale transactional authorization: %v", err)
		}
		if _, err := authz.EffectiveTx(t.Context(), tx, agent, ""); !errors.Is(err, authz.ErrForbidden) {
			t.Fatalf("stale effective grants: %v", err)
		}
		check, err := authz.ProjectsTx(t.Context(), tx, agent)
		if err != nil {
			return err
		}
		if check("nodes.read", "") || check("nodes.write", "") {
			t.Fatal("stale per-project evaluator granted authority")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	prefix, secret, _ := parseBearer("Bearer " + key.Token)
	fresh, ok, err := m.authenticateAgent(t.Context(), prefix, secret)
	if err != nil || !ok || fresh.KeyCreatorID != editor.ID {
		t.Fatal("unchanged credential did not authenticate with adopted creator")
	}
	freshCtx := tenant.WithPrincipal(t.Context(), fresh)
	if err := db.InTenant(freshCtx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
		var visible string
		if err := tx.QueryRow(freshCtx, `SELECT current_setting('aeon.visible_projects')`).Scan(&visible); err != nil {
			return err
		}
		if visible == "*" || visible != "{}" && visible != "" {
			t.Fatalf("adopted creator unexpectedly retained project visibility: %q", visible)
		}
		return authz.RequireTx(freshCtx, tx, fresh, "nodes.write", authz.Scope{})
	}); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("adopted creator ceiling was bypassed: %v", err)
	}
}

func TestAdoptAgentKeyFencesQueuedCreatorlessRequest(t *testing.T) {
	m, owner := keyFixture(t)
	key := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "legacy", "scopes": []string{"nodes.write"}}))
	legacyKey(t, m, owner, key.ID)
	agent := authenticateLegacyAdoptionKey(t, m, key)
	editor := keyScopeEditor(t, m, owner, "keys.manage")
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	entered, release := make(chan uint32, 1), make(chan struct{}, 1)
	defer func() { cancel(); close(release) }()
	pauseAdoption(m, entered, release)
	adopted, used := make(chan error, 1), make(chan error, 1)
	go func() { _, err := m.adoptAgentKey(ctx, editor, key.ID); adopted <- err }()
	var blocker uint32
	select {
	case blocker = <-entered:
	case err := <-adopted:
		t.Fatalf("adoption failed before ownership barrier: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	called := false
	go func() {
		authctx := tenant.WithPrincipal(ctx, agent)
		used <- db.InTenant(authctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
			called = true
			return authz.RequireTx(authctx, tx, agent, "nodes.write", authz.Scope{})
		})
	}()
	waitAdoptionContention(t, ctx, m.pool, blocker, used)
	release <- struct{}{}
	if err := <-adopted; err != nil {
		t.Fatal(err)
	}
	if err := <-used; err == nil || err.Error() != "agent key authority changed" || called {
		t.Fatalf("queued creatorless request escaped adoption fence: entered=%v err=%v", called, err)
	}
}

func TestAdoptAgentKeyWaitsForAdmittedUse(t *testing.T) {
	m, owner := keyFixture(t)
	key := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "legacy", "scopes": []string{"nodes.write"}}))
	legacyKey(t, m, owner, key.ID)
	agent := authenticateLegacyAdoptionKey(t, m, key)
	editor := keyScopeEditor(t, m, owner, "keys.manage")
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	entered, release := make(chan uint32, 1), make(chan struct{}, 1)
	defer func() { cancel(); close(release) }()
	used, adopted := make(chan error, 1), make(chan error, 1)
	go func() {
		authctx := tenant.WithPrincipal(ctx, agent)
		used <- db.InTenant(authctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
			if err := authz.RequireTx(authctx, tx, agent, "nodes.write", authz.Scope{}); err != nil {
				return err
			}
			entered <- tx.Conn().PgConn().PID()
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
			// The admitted transaction still sees its creatorless key. Adoption
			// cannot commit until this request and its final check finish.
			var creator *string
			if err := tx.QueryRow(ctx, `SELECT created_by_principal_id::text FROM agent_keys WHERE id=$1`, key.ID).Scan(&creator); err != nil {
				return err
			}
			if creator != nil {
				t.Error("adoption changed owner while an admitted request was active")
			}
			return authz.RequireTx(authctx, tx, agent, "nodes.write", authz.Scope{})
		})
	}()
	var blocker uint32
	select {
	case blocker = <-entered:
	case err := <-used:
		t.Fatalf("request failed before admission barrier: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	go func() { _, err := m.adoptAgentKey(ctx, editor, key.ID); adopted <- err }()
	waitAdoptionContention(t, ctx, m.pool, blocker, adopted)
	release <- struct{}{}
	if err := <-used; err != nil {
		t.Fatalf("admitted request failed: %v", err)
	}
	if err := <-adopted; err != nil {
		t.Fatalf("adoption failed after request completed: %v", err)
	}
}
