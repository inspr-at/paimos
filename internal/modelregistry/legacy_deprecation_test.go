// SPDX-License-Identifier: AGPL-3.0-only

package modelregistry

import (
	"context"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
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

func profilesExist(ctx context.Context, tx pgx.Tx, routes []Route) error {
	if len(routes) == 0 {
		return nil
	}
	ids := make([]string, len(routes))
	seen := map[string]struct{}{}
	for i, route := range routes {
		ids[i] = route.ProfileID
		seen[route.ProfileID] = struct{}{}
	}
	rows, err := tx.Query(ctx, `SELECT id::text FROM model_profiles WHERE id::text = ANY($1::text[])`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	found := map[string]struct{}{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		found[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(found) != len(seen) {
		return fail(http.StatusBadRequest, "unknown profile")
	}
	return nil
}

func routesEqual(a, b []Route) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Role != b[i].Role || a[i].Priority != b[i].Priority || a[i].ProfileID != b[i].ProfileID || a[i].State != b[i].State || a[i].Reason != b[i].Reason {
			return false
		}
		if !timePtrEqual(a[i].ValidUntil, b[i].ValidUntil) {
			return false
		}
	}
	return true
}

// These helpers reconstruct historical ladder fixtures. Production uses the
// preference-order API and does not call the retired ladder writer.
func roleRoutes(role string, rows []Route) []Route {
	out := []Route{}
	for _, row := range rows {
		if row.Role == role {
			out = append(out, row)
		}
	}
	return out
}

func boundedStoredRoutes(ctx context.Context, tx pgx.Tx) ([]Route, error) {
	out, err := listRoutes(ctx, tx)
	if err != nil {
		return nil, err
	}
	if err := routeBounds(out); err != nil {
		return nil, prefFail(413, "audit_snapshot_too_large")
	}
	return out, nil
}

func normalizeStoredRoutes(incoming, stored []Route, now time.Time, clear bool) ([]Route, error) {
	out, err := normalizeRouteStructure(incoming)
	if err != nil {
		return nil, err
	}
	out = canonicalRoutes(out)
	prior := map[string]Route{}
	for _, row := range stored {
		prior[row.Role+"/"+row.ProfileID] = row
	}
	for i, row := range out {
		if row.State == "available" || row.ValidUntil.After(now) {
			continue
		}
		if clear {
			out[i].State, out[i].Reason, out[i].ValidUntil = "available", "", nil
			continue
		}
		old, exists := prior[row.Role+"/"+row.ProfileID]
		if !exists || old.State != row.State || old.Reason != row.Reason || !timePtrEqual(old.ValidUntil, row.ValidUntil) {
			return nil, prefFail(422, "suppression_expired")
		}
	}
	return out, nil
}

func samePin(p Profile, s seedProfile) bool {
	return p.Slug == s.Slug && p.Harness == s.Harness && p.Family == s.Family && p.Model == s.Model && p.Effort == s.Effort && p.Tier == s.Tier && p.Enabled
}

func routeBounds(rows []Route) error {
	if len(rows) > routesTotalLimit {
		return prefFail(413, "too_many_routes")
	}
	counts := map[string]int{}
	for _, row := range rows {
		counts[row.Role]++
		if counts[row.Role] > routesDisplayLimit {
			return prefFail(413, "too_many_routes")
		}
	}
	return nil
}

func listRoutes(ctx context.Context, tx pgx.Tx) ([]Route, error) {
	out := []Route{}
	for _, role := range roleLadder {
		steps, _, err := derivedRoleSteps(ctx, tx, nil, role.name, routesTotalLimit+1)
		if err != nil {
			return nil, err
		}
		for _, step := range steps {
			out = append(out, step.Route)
		}
		if len(out) > routesTotalLimit {
			return nil, prefFail(413, "audit_snapshot_too_large")
		}
	}
	return out, nil
}

// Historical fixture validation, retained only for the legacy seed builders.
func normalizeRouteStructure(incoming []Route) ([]Route, error) {
	if incoming == nil {
		return nil, fail(http.StatusBadRequest, "routes must be an array")
	}
	seenPriority := map[string]bool{}
	seenProfile := map[string]bool{}
	out := make([]Route, 0, len(incoming))
	for _, route := range incoming {
		if !validRole(route.Role) {
			return nil, fail(http.StatusBadRequest, "invalid role")
		}
		if route.Priority < 1 {
			return nil, fail(http.StatusBadRequest, "priority must be positive")
		}
		if route.Priority > math.MaxInt32 {
			return nil, fail(http.StatusBadRequest, "priority exceeds storage range")
		}
		if !uuidRE.MatchString(route.ProfileID) {
			return nil, fail(http.StatusBadRequest, "invalid profile id")
		}
		switch route.State {
		case "available", "unavailable", "conserved", "budget_limited":
		default:
			return nil, fail(http.StatusBadRequest, "invalid route state")
		}
		keyP := route.Role + "/" + itoa(route.Priority)
		keyID := route.Role + "/" + strings.ToLower(route.ProfileID)
		if seenPriority[keyP] || seenProfile[keyID] {
			return nil, fail(http.StatusBadRequest, "duplicate route")
		}
		seenPriority[keyP] = true
		seenProfile[keyID] = true
		route.ProfileID = strings.ToLower(route.ProfileID)
		route.Reason = strings.TrimSpace(route.Reason)
		if route.State == "available" {
			route.Reason = ""
			route.ValidUntil = nil
		} else {
			if route.Reason == "" || len(route.Reason) > 512 || strings.ContainsAny(route.Reason, "\x00\r\n") {
				return nil, fail(http.StatusBadRequest, "suppression requires a reason")
			}
			if route.ValidUntil == nil {
				return nil, fail(http.StatusBadRequest, "suppression requires a future expiry")
			}
		}
		out = append(out, route)
	}
	return out, nil
}

func timePtrEqual(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [16]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
