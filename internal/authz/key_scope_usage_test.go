// SPDX-License-Identifier: AGPL-3.0-only
package authz

import (
	"errors"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestScopeUsageDebounceKeyIsolationAndDeniedGrant(t *testing.T) {
	d := dbtest.Open(t)
	ctx := dbtest.Seed(t.Context())
	var tid string
	if err := d.Admin.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('scope-use','Scope use') RETURNING id::text`).Scan(&tid); err != nil {
		t.Fatal(err)
	}
	p := tenant.Principal{TenantID: tid, Kind: tenant.Agent, Scopes: []string{"nodes.read"}}
	var otherKey string
	if err := db.InTenant(ctx, d.App, tid, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1,'agent','Usage',ARRAY['admin']) RETURNING id::text`, tid).Scan(&p.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type)
		 SELECT $1,$2,id,'workspace' FROM roles WHERE tenant_id=$1 AND key='admin'`, tid, p.ID); err != nil {
			return err
		}
		// Test fixture metadata only: these keys never authenticate.
		if err := tx.QueryRow(ctx, `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes) VALUES($1,$2,'Usage','scope-use-fixture-a',decode(repeat('00',32),'hex'),ARRAY['nodes.read']) RETURNING id::text`, tid, p.ID).Scan(&p.KeyID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes) VALUES($1,$2,'Other','scope-use-fixture-b',decode(repeat('00',32),'hex'),ARRAY['nodes.read']) RETURNING id::text`, tid, p.ID).Scan(&otherKey)
	}); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	write := func(key string, now time.Time) {
		t.Helper()
		principal := p
		principal.KeyID = key
		if err := db.InTenant(ctx, d.App, tid, func(tx pgx.Tx) error { return upsertKeyScopeUseTx(ctx, tx, principal, "nodes.read", now) }); err != nil {
			t.Fatal(err)
		}
	}
	read := func(key string) (time.Time, string) {
		t.Helper()
		var got time.Time
		var version string
		if err := db.InTenant(ctx, d.App, tid, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT last_used_at,xmin::text FROM agent_key_scope_usage WHERE key_id=$1 AND scope='nodes.read'`, key).Scan(&got, &version)
		}); err != nil {
			t.Fatal(err)
		}
		return got, version
	}
	write(p.KeyID, at)
	_, version := read(p.KeyID)
	write(p.KeyID, at.Add(59*time.Second))
	got, again := read(p.KeyID)
	if !got.Equal(at) || again != version {
		t.Fatal("debounce persisted another write within one minute")
	}
	write(otherKey, at.Add(30*time.Second))
	got, _ = read(otherKey)
	if !got.Equal(at.Add(30 * time.Second)) {
		t.Fatal("two keys shared scope timestamp")
	}
	write(p.KeyID, at.Add(time.Minute))
	got, _ = read(p.KeyID)
	if !got.Equal(at.Add(time.Minute)) {
		t.Fatal("debounce failed to advance at one minute")
	}
	authctx := BindPool(tenant.WithPrincipal(t.Context(), p), d.App)
	if err := Require(authctx, "nodes.read", Scope{}); err != nil {
		t.Fatalf("fixture cannot grant its retained scope: %v", err)
	}
	if err := Require(authctx, "nodes.write", Scope{}); err != nil {
		var denied *denial
		if !errors.Is(err, ErrForbidden) || !errors.As(err, &denied) || denied.reason != "missing_key_scope" {
			t.Fatalf("wrong denied-scope error: %v", err)
		}
	} else {
		t.Fatal("missing key scope was granted")
	}
	if err := db.InTenant(ctx, d.App, tid, func(tx pgx.Tx) error {
		var count int
		err := tx.QueryRow(ctx, `SELECT count(*) FROM agent_key_scope_usage WHERE scope='nodes.write'`).Scan(&count)
		if err == nil && count != 0 {
			t.Error("denied scope recorded as used")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := Require(authctx, "nodes.read", Scope{}); err != nil {
		t.Fatal(err)
	}
	// A stale authenticated ceiling cannot keep a scope after a trim commit.
	if err := db.InTenant(ctx, d.App, tid, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE agent_keys SET scopes='{}' WHERE id=$1`, p.KeyID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := Require(authctx, "nodes.read", Scope{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("stale key ceiling granted: %v", err)
	}
}
