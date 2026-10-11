// SPDX-License-Identifier: AGPL-3.0-only

package modelregistry

import (
	"context"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"strings"
	"time"
)

// Historical ladder fixture writers are intentionally test-only. Production
// keeps their tables for one deprecation release, without reading or writing.
// Only these constant identifiers enter SQL; the caller's role is never SQL.
func roleRoutesTable(role string) string {
	if role == "review-gate-security" {
		return "model_security_role_routes"
	}
	return "model_role_routes"
}

func insertRoute(ctx context.Context, tx pgx.Tx, tenantID string, route Route) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO `+roleRoutesTable(route.Role)+` (tenant_id, role, priority, profile_id, state, reason, valid_until)
		VALUES ($1::uuid, $2, $3, $4::uuid, $5, $6, $7)`,
		tenantID, route.Role, route.Priority, route.ProfileID, route.State, route.Reason, route.ValidUntil)
	return err
}

// replaceRoutes is the transaction-injected legacy fixture helper. Production
// writes enter through Module.replace with final authority and shared fences.
func replaceRoutes(ctx context.Context, tx pgx.Tx, p tenant.Principal, incoming []Route, now time.Time) ([]Route, error) {
	if err := routeBounds(incoming); err != nil {
		return nil, err
	}
	if err := requireCatalog(ctx, tx); err != nil {
		return nil, err
	}
	before, err := boundedStoredRoutes(ctx, tx)
	if err != nil {
		return nil, err
	}
	normalized, err := normalizeStoredRoutes(incoming, before, now, false)
	if err != nil {
		return nil, err
	}
	if err := profilesExist(ctx, tx, normalized); err != nil {
		return nil, err
	}
	if routesEqual(before, normalized) {
		return before, nil
	}
	if _, err := tx.Exec(ctx, `DELETE FROM model_role_routes`); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM model_security_role_routes`); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM model_security_role_routes`); err != nil {
		return nil, err
	}
	for _, route := range normalized {
		if err := insertRoute(ctx, tx, p.TenantID, route); err != nil {
			return nil, err
		}
	}
	after, err := boundedStoredRoutes(ctx, tx)
	if err != nil {
		return nil, err
	}
	if err := writeEvent(ctx, tx, p, evRoutes, before, after); err != nil {
		return nil, err
	}
	return after, nil
}

var deprecatedSeedLadders = map[string][]string{
	"build":                {"codex-6-1-sol-high", "claude-sonnet-high", "pi-anthropic-sonnet-high"},
	"build-hard":           {"codex-6-1-sol-xhigh", "codex-sol-xhigh", "claude-opus-xhigh", "pi-anthropic-opus-xhigh"},
	"review-gate":          {"codex-astra-xhigh", "claude-fable-xhigh", "claude-opus-xhigh", "grok-4-7-xhigh", "cursor-grok-xhigh"},
	"review-gate-security": {"grok-4-7-xhigh", "cursor-grok-xhigh", "codex-6-1-sol-xhigh"},
}

func defaultRoutes(profiles []seedProfile) []seedRoute {
	sorted := append([]seedProfile(nil), profiles...)
	sortProfiles(sorted)
	var out []seedRoute
	for _, role := range roleLadder {
		var slugs []string
		if ladder := deprecatedSeedLadders[role.name]; len(ladder) > 0 {
			slugs = append(slugs, ladder...)
		} else {
			for _, harness := range []string{"codex", "claude", "pi", "cursor"} {
				for _, profile := range sorted {
					if profile.Harness == harness && profile.Tier == role.tier && profile.Effort == role.effort {
						slugs = append(slugs, profile.Slug)
					}
				}
			}
		}
		// Keep the additional harnesses behind the role's original candidates.
		switch role.name {
		case "scout":
			slugs = append(slugs, "gemini-gemini-2-5-flash-4096")
		case "mechanical":
			slugs = append(slugs, "gemini-gemini-2-5-flash-16384")
		case "build":
			slugs = append(slugs, "gemini-gemini-2-5-pro-16384", "opencode-ollama-qwen3-coder-default")
		case "build-hard", "review-gate":
			slugs = append(slugs, "gemini-gemini-2-5-pro-32768", "opencode-google-gemini-2-5-pro-default")
		}
		for i, slug := range slugs {
			out = append(out, seedRoute{Role: role.name, Priority: i + 1, Slug: slug})
		}
	}
	return out
}

func sortProfiles(profiles []seedProfile) {
	for i := 1; i < len(profiles); i++ {
		j := i
		for j > 0 && profiles[j].Slug < profiles[j-1].Slug {
			profiles[j], profiles[j-1] = profiles[j-1], profiles[j]
			j--
		}
	}
}
func matchesV2(role string, steps []ladderStep) bool {
	expected, ok := v2Ladders[role]
	if !ok {
		return role == "review-gate-security" && len(steps) == 0
	}
	legacyCount := len(expected)
	if len(steps) != legacyCount {
		// Main's additive harness upgrade appends exact pins to historical
		// defaults. Recognize that suffix while retaining custom route policy.
		expected = append([]string(nil), expected...)
		for _, route := range defaultRoutes(catalogProfiles()) {
			if route.Role == role && (strings.HasPrefix(route.Slug, "gemini-") || strings.HasPrefix(route.Slug, "opencode-")) {
				expected = append(expected, route.Slug)
			}
		}
	}
	if len(steps) != len(expected) {
		return false
	}
	for i, step := range steps {
		if step.Priority != i+1 || step.Profile.Slug != expected[i] || (step.Profile.Version != "2" && (i < legacyCount || step.Profile.Version != CatalogVersion)) || !step.Profile.Enabled {
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

// The historical v2 default is deliberately frozen. Custom routes and profile
// versions are never mistaken for defaults, including an intentionally empty role.
var v2Ladders = map[string][]string{
	"scout":       {"codex-luna-medium", "claude-haiku-medium"},
	"mechanical":  {"codex-luna-high", "claude-haiku-high"},
	"build":       {"codex-terra-high", "claude-sonnet-high", "pi-anthropic-sonnet-high"},
	"build-hard":  {"codex-sol-xhigh", "claude-opus-xhigh", "pi-anthropic-opus-xhigh"},
	"review-gate": {"codex-astra-xhigh", "claude-fable-xhigh", "claude-opus-xhigh", "cursor-grok-xhigh"},
}

type seedRoute struct {
	Role     string
	Priority int
	Slug     string
}
