// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Legacy fixture writer deliberately supplies no activation cause.
func insertProfile(ctx context.Context, tx pgx.Tx, tenantID string, in profileWrite) (Profile, error) {
	return insertActivatedProfile(ctx, tx, tenant.Principal{TenantID: tenantID}, in, true, "")
}

// ensureCatalog is the explicit fixture/standalone setup wrapper. Production
// boundaries use PrepareCatalog; stores and resolvers only check readiness.
func ensureCatalog(ctx context.Context, tx pgx.Tx, p tenant.Principal) error {
	changes, err := prepareCatalogDeferred(ctx, tx, p)
	if err != nil {
		return err
	}
	for _, change := range changes {
		if _, err := events.Append(ctx, tx, p, change); err != nil {
			return err
		}
	}
	return nil
}
