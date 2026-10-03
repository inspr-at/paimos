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
	ids := map[string]string{}
	for _, profile := range catalogProfiles() {
		if profile.Harness != "gemini" && profile.Harness != "opencode" {
			continue
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM model_profiles WHERE slug=$1 AND version IN ('2',$2) AND harness=$3 AND family=$4 AND model=$5 AND effort=$6 AND tier=$7 AND enabled)`, profile.Slug, profile.Version, profile.Harness, profile.Family, profile.Model, profile.Effort, profile.Tier).Scan(&exists); err != nil {
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
		ids[profile.Slug] = row.ID
	}
	if len(added) == 0 {
		return nil
	}
	routes := []Route{}
	for _, route := range defaultRoutes(catalogProfiles()) {
		id := ids[route.Slug]
		if id == "" {
			continue
		}
		var priority int
		if err := tx.QueryRow(ctx, `SELECT COALESCE(max(priority),0)+1 FROM model_role_routes WHERE role=$1`, route.Role).Scan(&priority); err != nil {
			return err
		}
		stored := Route{Role: route.Role, Priority: priority, ProfileID: id, State: "available"}
		if err := insertRoute(ctx, tx, p.TenantID, stored); err != nil {
			return err
		}
		routes = append(routes, stored)
	}
	return writeEvent(ctx, tx, p, evSeeded, nil, struct {
		Profiles []Profile `json:"profiles"`
		Routes   []Route   `json:"routes"`
	}{added, routes})
}
