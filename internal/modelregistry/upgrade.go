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
	if err := catalogLock(ctx, tx); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO model_refresh_settings(tenant_id) VALUES($1) ON CONFLICT DO NOTHING`, p.TenantID); err != nil {
		return nil, err
	}
	var version string
	if err := tx.QueryRow(ctx, `SELECT catalog_version FROM model_refresh_settings`).Scan(&version); err != nil {
		return nil, err
	}
	if version == CatalogVersion {
		return nil, nil
	}
	existing, err := listProfiles(ctx, tx)
	if err != nil {
		return nil, err
	}
	ids := map[string]string{}
	for _, profile := range existing {
		// Reuse only exact immutable seed pins. A tenant's different value is custom.
		for _, seed := range catalogProfiles() {
			if samePin(profile, seed) && (profile.Version == "2" || profile.Version == CatalogVersion) {
				ids[seed.Slug] = profile.ID
			}
		}
	}
	var added []Profile
	for _, seed := range catalogProfiles() {
		if ids[seed.Slug] != "" {
			continue
		}
		// A conflicting tenant-created v3 pin must not acquire default authority.
		var conflict bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM model_profiles WHERE slug=$1 AND version=$2)`, seed.Slug, CatalogVersion).Scan(&conflict); err != nil {
			return nil, err
		}
		if conflict {
			continue
		}
		profile, err := insertActivatedProfile(ctx, tx, p, profileWrite{Slug: seed.Slug, Version: seed.Version, Harness: seed.Harness, Family: seed.Family, Model: seed.Model, Effort: seed.Effort, Tier: seed.Tier}, true, modelactivation.ShippedCatalog)
		if err != nil {
			return nil, err
		}
		if profile.Enabled {
			ids[seed.Slug] = profile.ID
		}
		added = append(added, profile)
	}
	changed := []string{} // Catalog upgrades never add or replace Models choices.
	if _, err := tx.Exec(ctx, `UPDATE model_refresh_settings SET catalog_version=$1`, CatalogVersion); err != nil {
		return nil, err
	}
	return []events.Change{{Type: "model.catalog_upgraded", After: map[string]any{"version": CatalogVersion, "added": added, "roles": changed}}}, nil
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
