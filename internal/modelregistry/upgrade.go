// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"strings"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/modelactivation"
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
	return modelactivation.Lock(ctx, tx)
}

func prepareCatalogUpgrade(ctx context.Context, tx pgx.Tx, p tenant.Principal) ([]events.Change, error) {
	return prepareCatalogDeferred(ctx, tx, p)
}

func samePin(p Profile, s seedProfile) bool {
	return p.Slug == s.Slug && p.Harness == s.Harness && p.Family == s.Family && p.Model == s.Model && p.Effort == s.Effort && p.Tier == s.Tier && p.Enabled
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
		for _, route := range defaultRoutes(nil) {
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
		} else if step.Profile.Slug != expected[i] {
			return false
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
