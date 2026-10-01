// SPDX-License-Identifier: AGPL-3.0-only

package harness

import (
	"context"

	"github.com/jackc/pgx/v5"
)

type modelIdentity struct {
	Model, Effort, ProfileID *string
}

func resolveModelIdentity(ctx context.Context, tx pgx.Tx, harness string, model, effort *string) (modelIdentity, error) {
	var identity modelIdentity
	err := tx.QueryRow(ctx, `SELECT model,effort,profile_id::text FROM aeon_harness_model_identity($1,$2,$3)`, harness, model, effort).
		Scan(&identity.Model, &identity.Effort, &identity.ProfileID)
	return identity, err
}
