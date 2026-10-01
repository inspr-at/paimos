// SPDX-License-Identifier: AGPL-3.0-only

package modelregistry

import (
	"context"

	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Existing tenants receive additive profiles under their ordinary RLS context.
// Their saved route order and immutable pins are never replaced by this upgrade.
func ensureAdditionalCatalog(ctx context.Context, tx pgx.Tx, p tenant.Principal) error {
	slugs := []string{}
	for _, profile := range catalogProfiles() {
		if profile.Harness == "gemini" || profile.Harness == "opencode" {
			slugs = append(slugs, profile.Slug)
		}
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM model_profiles WHERE slug=ANY($1::text[]) AND version=$2`, slugs, CatalogVersion).Scan(&count); err != nil {
		return err
	}
	if count == len(slugs) {
		return nil
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('aeon-model-registry:' || current_setting('aeon.tenant_id', true), 0))`); err != nil {
		return err
	}
	added := []Profile{}
	for _, profile := range catalogProfiles() {
		if profile.Harness != "gemini" && profile.Harness != "opencode" {
			continue
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM model_profiles WHERE slug=$1 AND version=$2)`, profile.Slug, profile.Version).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		row, err := insertProfile(ctx, tx, p.TenantID, profileWrite{Slug: profile.Slug, Version: profile.Version, Harness: profile.Harness, Family: profile.Family, Model: profile.Model, Effort: profile.Effort, Tier: profile.Tier})
		if err != nil {
			return err
		}
		added = append(added, row)
	}
	if len(added) == 0 {
		return nil
	}
	return writeEvent(ctx, tx, p, evSeeded, nil, struct {
		Profiles []Profile `json:"profiles"`
	}{added})
}
