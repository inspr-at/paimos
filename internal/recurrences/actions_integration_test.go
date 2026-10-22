// SPDX-License-Identifier: AGPL-3.0-only
package recurrences_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/inspr-at/paimos/internal/recurrences"
	"github.com/inspr-at/paimos/internal/routineactions"
	"github.com/inspr-at/paimos/internal/routineguard"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Wire the broker outside recurrences: it imports nodes, whose Run once path
// imports agentruns and recurrences. Keep the consent and revocation assertions.
func TestRoutineActionGrantRejectsUnverifiedAndRevokedOwner(t *testing.T) {
	recurrences.CheckRoutineActionGrantRejectsUnverifiedAndRevokedOwner(t, func(mux *http.ServeMux, pool *pgxpool.Pool, m *recurrences.Module) {
		routineactions.New(pool, func(ctx context.Context, tx pgx.Tx, p tenant.Principal, b routineactions.Binding, c routineguard.Context) (tenant.Principal, routineguard.Decision, string, error) {
			return m.AuthorizeActionTx(ctx, tx, p, recurrences.ActionBinding(b), c)
		}).Mount(mux)
	})
}
