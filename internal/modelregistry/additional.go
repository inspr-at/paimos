// SPDX-License-Identifier: AGPL-3.0-only

package modelregistry

import (
	"context"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Existing tenants receive additive profiles under their ordinary RLS context.
// Their saved route order and immutable pins are never replaced by this upgrade.
func prepareAdditionalCatalog(ctx context.Context, tx pgx.Tx, p tenant.Principal) ([]events.Change, error) {
	return nil, nil
}
