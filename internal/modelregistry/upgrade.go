// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"

	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// The historical v2 default is deliberately frozen. Custom routes and profile
// versions are never mistaken for defaults, including an intentionally empty role.
var v2Ladders = map[string][]string{
	"scout":       {"codex-luna-medium", "claude-haiku-medium"},
	"mechanical":  {"codex-luna-high", "claude-haiku-high"},
	"build":       {"codex-terra-high", "claude-sonnet-high", "pi-anthropic-sonnet-high"},
	"build-hard":  {"codex-sol-xhigh", "claude-opus-xhigh", "pi-anthropic-opus-xhigh"},
	"review-gate": {"codex-astra-xhigh", "claude-fable-xhigh", "claude-opus-xhigh", "cursor-grok-xhigh"},
}

func catalogLock(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('aeon-model-registry:' || current_setting('aeon.tenant_id', true), 0))`)
	return err
}

func upgradeCatalog(ctx context.Context, tx pgx.Tx, p tenant.Principal) error {
	if err := catalogLock(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO model_refresh_settings(tenant_id) VALUES($1) ON CONFLICT DO NOTHING`, p.TenantID); err != nil {
		return err
	}
	var version string
	if err := tx.QueryRow(ctx, `SELECT catalog_version FROM model_refresh_settings`).Scan(&version); err != nil {
		return err
	}
	if version == CatalogVersion {
		return nil
	}
	existing, err := listProfiles(ctx, tx)
	if err != nil {
		return err
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
			return err
		}
		if conflict {
			continue
		}
		profile, err := insertProfile(ctx, tx, p.TenantID, profileWrite{seed.Slug, seed.Version, seed.Harness, seed.Family, seed.Model, seed.Effort, seed.Tier})
		if err != nil {
			return err
		}
		ids[seed.Slug] = profile.ID
		added = append(added, profile)
	}
	now, err := dbNow(ctx, tx)
	if err != nil {
		return err
	}
	changed := []string{}
	for _, role := range roleLadder {
		steps, err := loadLadder(ctx, tx, role.name)
		if err != nil {
			return err
		}
		if !matchesV2(role.name, steps) {
			continue
		}
		replacements := []Route{}
		safe := true
		for _, seed := range defaultRoutes(catalogProfiles()) {
			if seed.Role != role.name {
				continue
			}
			if ids[seed.Slug] == "" {
				safe = false
				break
			}
			route := Route{Role: seed.Role, Priority: seed.Priority, ProfileID: ids[seed.Slug], State: "available"}
			for _, old := range steps {
				if old.Profile.Slug == seed.Slug {
					route.State, route.Reason, route.ValidUntil = old.State, old.Reason, old.ValidUntil
				}
			}
			replacements = append(replacements, route)
		}
		// Never discard an active override even when its old default was retired.
		for _, old := range steps {
			if old.State == "available" || old.ValidUntil == nil || !now.Before(*old.ValidUntil) {
				continue
			}
			retained := false
			for _, r := range replacements {
				if r.ProfileID == old.ProfileID {
					retained = true
				}
			}
			if !retained {
				safe = false
			}
		}
		if !safe {
			continue
		}
		if _, err := tx.Exec(ctx, `DELETE FROM `+roleRoutesTable(role.name)+` WHERE role=$1`, role.name); err != nil {
			return err
		}
		for _, r := range replacements {
			if err := insertRoute(ctx, tx, p.TenantID, r); err != nil {
				return err
			}
		}
		changed = append(changed, role.name)
	}
	if _, err := tx.Exec(ctx, `UPDATE model_refresh_settings SET catalog_version=$1`, CatalogVersion); err != nil {
		return err
	}
	return writeEvent(ctx, tx, p, "model.catalog_upgraded", nil, map[string]any{"version": CatalogVersion, "added": added, "roles": changed})
}

func samePin(p Profile, s seedProfile) bool {
	return p.Slug == s.Slug && p.Harness == s.Harness && p.Family == s.Family && p.Model == s.Model && p.Effort == s.Effort && p.Tier == s.Tier && p.Enabled
}

func matchesV2(role string, steps []ladderStep) bool {
	expected, ok := v2Ladders[role]
	if !ok {
		return role == "review-gate-security" && len(steps) == 0
	}
	if len(steps) != len(expected) {
		return false
	}
	for i, step := range steps {
		if step.Priority != i+1 || step.Profile.Slug != expected[i] || step.Profile.Version != "2" || !step.Profile.Enabled {
			return false
		}
		if step.Profile.Slug == "codex-terra-high" {
			if step.Profile.Harness != "codex" || step.Profile.Family != "openai" || step.Profile.Model != "gpt-6-terra" || step.Profile.Effort != "high" || step.Profile.Tier != "standard" {
				return false
			}
		} else {
			matched := false
			for _, seed := range catalogProfiles() {
				if samePin(step.Profile, seed) {
					matched = true
				}
			}
			if !matched {
				return false
			}
		}
	}
	return true
}

// KnownInvalid is a verified retired/invalid identifier, not a vendor response.
func KnownInvalid(model string) bool {
	switch model {
	case "gpt-6-terra", "gpt-6.1-astra", "gpt-6.1-luna", "gpt-6.1-terra":
		return true
	}
	return false
}
