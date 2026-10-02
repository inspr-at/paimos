// SPDX-License-Identifier: AGPL-3.0-only

package authz

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/operatoractor"
)

func TestAccessMutationTenantBeforeTree(t *testing.T) {
	for _, name := range []string{"project membership", "operator actor"} {
		t.Run(name, func(t *testing.T) {
			d := dbtest.Open(t)
			var tid string
			if err := d.Admin.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('lock-order','Lock order') RETURNING id::text`).Scan(&tid); err != nil {
				t.Fatal(err)
			}
			dbtest.TenantBeforeTree(t, d, tid, func(ctx context.Context) error {
				return db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
					if name == "operator actor" {
						_, err := operatoractor.Ensure(ctx, tx, tid)
						return err
					}
					return lockProjectMutation(ctx, tx, tid)
				})
			})
		})
	}
}
