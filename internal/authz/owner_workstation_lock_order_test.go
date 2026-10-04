// SPDX-License-Identifier: AGPL-3.0-only
package authz

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Probe immediately before and after tree acquisition. The tenant fence must
// already be held at both boundaries; NOWAIT proves the canonical tenant-first
// order without sleeps or a possible deadlock. Main's unchanged
// TestProjectFencesPreserveTenantModes separately guards each shipped row mode.
type projectFenceProbeTx struct {
	pgx.Tx
	probe func(treeHeld bool)
}

func (tx projectFenceProbeTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	tree := strings.Contains(sql, "pg_advisory_xact_lock")
	if tree {
		tx.probe(false)
	}
	tag, err := tx.Tx.Exec(ctx, sql, args...)
	if tree && err == nil {
		tx.probe(true)
	}
	return tag, err
}

func TestProjectFencesAcquireTenantBeforeTree(t *testing.T) {
	for _, tc := range []struct {
		name string
		lock func(context.Context, pgx.Tx, string) error
	}{
		{"mutation", LockProjectMutation},
		{"resource write", LockProjectWrite},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAuthorityFixture(t)
			ctx := authorityDeadline(t)
			probes := 0
			if err := db.InTenant(ctx, f.d.App, f.actor.TenantID, func(tx pgx.Tx) error {
				return tc.lock(ctx, projectFenceProbeTx{Tx: tx, probe: func(treeHeld bool) {
					probes++
					if err := db.InTenant(ctx, f.d.App, f.actor.TenantID, func(other pgx.Tx) error {
						_, err := other.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR UPDATE NOWAIT`, f.actor.TenantID)
						return err
					}); err == nil {
						t.Errorf("tenant fence missing at tree boundary (tree held=%t)", treeHeld)
					} else {
						var pe *pgconn.PgError
						if !errors.As(err, &pe) || pe.Code != "55P03" {
							t.Errorf("unexpected tenant probe error: %v", err)
						}
					}
					if err := db.InTenant(ctx, f.d.App, f.actor.TenantID, func(other pgx.Tx) error {
						var free bool
						if err := other.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1::text, 0))`, f.actor.TenantID).Scan(&free); err != nil {
							return err
						}
						if free == treeHeld {
							t.Errorf("tree probe free=%t, want %t", free, !treeHeld)
						}
						return nil
					}); err != nil {
						t.Errorf("tree probe failed: %v", err)
					}
				}}, f.actor.TenantID)
			}); err != nil {
				t.Fatal(err)
			}
			if probes != 2 {
				t.Fatalf("tree boundary probes: %d, want 2", probes)
			}
		})
	}
}
