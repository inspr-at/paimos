// SPDX-License-Identifier: AGPL-3.0-only
package authz

import (
	"context"
	"errors"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestProjectFencesPreserveTenantModes(t *testing.T) {
	for name, lock := range map[string]func(context.Context, pgx.Tx, string) error{
		"mutation": LockProjectMutation,
		"write":    LockProjectWrite,
	} {
		t.Run(name, func(t *testing.T) {
			f := newAuthorityFixture(t)
			ctx := authorityDeadline(t)
			if err := db.InTenant(ctx, f.d.App, f.actor.TenantID, func(tx pgx.Tx) error {
				if err := lock(ctx, tx, f.actor.TenantID); err != nil {
					return err
				}
				// Preserve the shipped modes: membership mutations use NO KEY UPDATE;
				// resource writes use SHARE, compatible with FK checks and other
				// readers but excluding every authority writer.
				for _, mode := range []string{"KEY SHARE", "NO KEY UPDATE", "SHARE", "UPDATE"} {
					probe, err := f.d.Admin.Begin(ctx)
					if err != nil {
						return err
					}
					_, err = probe.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR `+mode+` NOWAIT`, f.actor.TenantID)
					_ = probe.Rollback(ctx)
					if mode == "KEY SHARE" || (name == "write" && mode == "SHARE") {
						if err != nil {
							t.Errorf("compatible tenant %s check blocked by project fence: %v", mode, err)
						}
					} else {
						var pe *pgconn.PgError
						if !errors.As(err, &pe) || pe.Code != "55P03" {
							t.Errorf("project fence did not exclude tenant %s: %v", mode, err)
						}
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
