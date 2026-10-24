// SPDX-License-Identifier: AGPL-3.0-only
package auth

import (
	"context"

	"github.com/inspr-at/paimos/internal/assistants"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// The final route denial is recorded once, after alternative permissions and
// target scope resolution have finished. No resource write was admitted.
func (m *Module) recordAssistantPolicyDenial(ctx context.Context, p tenant.Principal, scope authz.Scope, denied error) error {
	reason, permission := authz.AssistantPolicyDenial(denied)
	if reason == "" {
		return nil
	}
	return db.InTenant(tenant.WithPrincipal(ctx, p), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := db.LockTenant(ctx, tx, p.TenantID); err != nil {
			return err
		}
		var id string
		if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
			return err
		}
		d, err := assistants.EvaluateTx(ctx, tx, assistants.Check{TenantID: p.TenantID, PrincipalID: p.ID, ProjectID: scope.ProjectID, Operation: "permission", Key: "permission/" + id, Scopes: []string{permission}, Refusal: reason})
		if err != nil {
			return err
		}
		return assistants.AppendDecisionsTx(ctx, tx, p, d)
	})
}
