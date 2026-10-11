// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"encoding/json"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/modelactivation"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Legacy fixture writer deliberately supplies no activation cause.
func insertProfile(ctx context.Context, tx pgx.Tx, tenantID string, in profileWrite) (Profile, error) {
	// Compatibility fixtures materialize metadata as it existed before 1331.
	var raw json.RawMessage
	var scale *int
	if err := tx.QueryRow(ctx, `SELECT aeon_model_display($1,$2),aeon_model_effort_level(aeon_model_provider($3,$1,$2),$4)::integer`, in.Harness, in.Model, in.Family, in.Effort).Scan(&raw, &scale); err != nil {
		return Profile{}, err
	}
	var old Display
	if err := json.Unmarshal(raw, &old); err != nil {
		return Profile{}, err
	}
	if in.DisplayName == "" {
		in.Display = old
	}
	if in.RegisteredEffortLevel == nil {
		in.RegisteredEffortLevel = scale
	}
	return insertActivatedProfile(ctx, tx, tenant.Principal{TenantID: tenantID}, in, true, "")
}

// ensureCatalog is the explicit fixture/standalone setup wrapper. Production
// boundaries use PrepareCatalog; stores and resolvers only check readiness.
func ensureCatalog(ctx context.Context, tx pgx.Tx, p tenant.Principal) error {
	if err := legacyCatalogFixture(ctx, tx, p); err != nil {
		return err
	}
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

// Existing routing and board regressions arrange the frozen, previously shipped
// pins explicitly. New catalog tests call production setup directly so no test
// fixture can become discovery or bootstrap authority.
func legacyCatalogFixture(ctx context.Context, tx pgx.Tx, p tenant.Principal) error {
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM model_profiles`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	for _, s := range catalogProfiles() {
		var raw json.RawMessage
		var scale *int
		if err := tx.QueryRow(ctx, `SELECT aeon_model_display($1,$2),aeon_model_effort_level(aeon_model_provider($3,$1,$2),$4)::integer`, s.Harness, s.Model, s.Family, s.Effort).Scan(&raw, &scale); err != nil {
			return err
		}
		var display Display
		if err := json.Unmarshal(raw, &display); err != nil {
			return err
		}
		_, err := insertActivatedProfile(ctx, tx, p, profileWrite{Slug: s.Slug, Version: s.Version, Harness: s.Harness, Family: s.Family, Model: s.Model, Effort: s.Effort, Tier: s.Tier, Display: display, RegisteredEffortLevel: scale}, true, modelactivation.ShippedCatalog)
		if err != nil {
			return err
		}
	}
	return nil
}
