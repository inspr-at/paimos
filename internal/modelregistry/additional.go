// SPDX-License-Identifier: AGPL-3.0-only

package modelregistry

import (
	"context"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Kept for compatibility with earlier setup callers. Additional model IDs
// provide evidence through refresh; they never synthesize pins or routes.
func prepareAdditionalCatalog(ctx context.Context, tx pgx.Tx, p tenant.Principal) ([]events.Change, error) {
	return nil, nil
}
