// SPDX-License-Identifier: AGPL-3.0-only
package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenantbootstrap"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Risk: Ping stays green while authentication admission fails; the canary must
// exercise that admission and preserve existing activity and event records.
func TestAuthenticationCanaryUsesAuthAdmissionWithoutRenewal(t *testing.T) {
	m, p := keyFixture(t)
	m.cfg.BootstrapTenantSlug = "rotation"
	ctx := dbtest.Seed(t.Context())
	var keyID string
	if err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		// A retained non-credential fixture makes accidental activity renewal
		// observable without creating, reading or displaying a usable key.
		return tx.QueryRow(ctx, `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,last_used_at)
   VALUES($1,$2,'canary fixture','canary-fixture','not-an-issued-credential','2020-01-01T00:00:00Z') RETURNING id::text`, p.TenantID, p.ID).Scan(&keyID)
	}); err != nil {
		t.Fatal(err)
	}
	snapshot := func() (time.Time, int) {
		t.Helper()
		var used time.Time
		var events int
		if err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
			if err := tx.QueryRow(ctx, `SELECT last_used_at FROM agent_keys WHERE id=$1`, keyID).Scan(&used); err != nil {
				return err
			}
			return tx.QueryRow(ctx, `SELECT count(*) FROM events`).Scan(&events)
		}); err != nil {
			t.Fatal(err)
		}
		return used, events
	}
	beforeUsed, beforeEvents := snapshot()
	original := m.inTenant
	calls := 0
	m.inTenant = func(ctx context.Context, pool *pgxpool.Pool, id string, fn func(pgx.Tx) error) error {
		calls++
		return original(ctx, pool, id, fn)
	}
	for i := 0; i < 3; i++ {
		bounded, cancel := context.WithTimeout(t.Context(), time.Second)
		err := m.ProbeAuthentication(bounded)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls != 6 {
		t.Fatalf("session and key admission not both exercised: %d calls", calls)
	}
	for _, stage := range []string{tenantbootstrap.LookupTenantID, p.TenantID} {
		failure := errors.New("injected authentication admission fault")
		m.inTenant = func(ctx context.Context, pool *pgxpool.Pool, id string, fn func(pgx.Tx) error) error {
			if id == stage {
				return failure
			}
			return original(ctx, pool, id, fn)
		}
		if err := m.pool.Ping(t.Context()); err != nil {
			t.Fatalf("fixture database Ping must be healthy: %v", err)
		}
		if err := m.ProbeAuthentication(t.Context()); !errors.Is(err, failure) {
			t.Fatalf("canary missed auth failure at %s: %v", stage, err)
		}
	}
	m.inTenant = original
	afterUsed, afterEvents := snapshot()
	if !beforeUsed.Equal(afterUsed) || beforeEvents != afterEvents {
		t.Fatal("canary changed activity or audit records")
	}
	if m.pool.Stat().AcquiredConns() != 0 {
		t.Fatal("canary retained a pooled connection")
	}
}
