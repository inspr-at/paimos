// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/modelactivation"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func catalogLock(ctx context.Context, tx pgx.Tx) error {
	return modelactivation.Lock(ctx, tx)
}

func prepareCatalogUpgrade(ctx context.Context, tx pgx.Tx, p tenant.Principal) ([]events.Change, error) {
	return prepareCatalogDeferred(ctx, tx, p)
}

func samePin(p Profile, s seedProfile) bool {
	return p.Slug == s.Slug && p.Harness == s.Harness && p.Family == s.Family && p.Model == s.Model && p.Effort == s.Effort && p.Tier == s.Tier && p.Enabled
}

// KnownInvalid is a verified retired/invalid identifier, not a vendor response.
func KnownInvalid(model string) bool {
	switch model {
	case "gpt-6-terra", "gpt-6.1-astra", "gpt-6.1-luna", "gpt-6.1-terra":
		return true
	}
	return false
}
