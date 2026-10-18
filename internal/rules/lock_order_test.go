// SPDX-License-Identifier: AGPL-3.0-only

package rules

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestRulesMutationTenantBeforeTree(t *testing.T) {
	for _, name := range []string{"HTTP publish", "learning draft"} {
		t.Run(name, func(t *testing.T) {
			w := newBatchWorld(t, "rules-lock-order")
			p := w.principal(tenant.Person, "owner", "admin")
			layer := w.layer(p, Scope{Layer: "company"})
			s := w.set(p, layer, "Safety", lockedRule("safety", "Keep the floor."))
			dbtest.TenantBeforeTree(t, w.d, w.tid, func(ctx context.Context) error {
				if name == "learning draft" {
					return db.InTenant(dbtest.Seed(ctx), w.d.App, w.tid, func(tx pgx.Tx) error {
						return PrepareWrite(ctx, tx, p)
					})
				}
				code, body := w.send(p, "POST", "/api/rules/publish", batch("", item(s, "auto")))
				if code != 200 {
					return fmt.Errorf("publish: %d %s", code, body)
				}
				return nil
			})
		})
	}
}
