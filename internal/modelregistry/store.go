// SPDX-License-Identifier: AGPL-3.0-only

package modelregistry

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

const (
	evSeeded  = "model.registry_seeded"
	evProfile = "model.profile_created"
	evRoutes  = "model.routes_replaced"
)

// Profile is one immutable model pin.
type Profile struct {
	ID        string    `json:"id"`
	Slug      string    `json:"slug"`
	Version   string    `json:"version"`
	Harness   string    `json:"harness"`
	Family    string    `json:"family"`
	Model     string    `json:"model"`
	Effort    string    `json:"effort"`
	Tier      string    `json:"tier"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
}

// Route is one step in a role ladder. A non-available state suppresses the
// step until ValidUntil and does not change the profile.
type Route struct {
	Role       string     `json:"role"`
	Priority   int        `json:"priority"`
	ProfileID  string     `json:"profile_id"`
	State      string     `json:"state"`
	Reason     string     `json:"reason"`
	ValidUntil *time.Time `json:"valid_until"`
}

type profileWrite struct {
	Slug    string `json:"slug"`
	Version string `json:"version"`
	Harness string `json:"harness"`
	Family  string `json:"family"`
	Model   string `json:"model"`
	Effort  string `json:"effort"`
	Tier    string `json:"tier"`
}

func writeEvent(ctx context.Context, tx pgx.Tx, p tenant.Principal, eventType string, before, after any) error {
	_, err := events.Append(ctx, tx, p, events.Change{Type: eventType, Before: before, After: after})
	return err
}

func ensureCatalog(ctx context.Context, tx pgx.Tx, p tenant.Principal) error {
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM model_profiles`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return upgradeCatalog(ctx, tx, p)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('aeon-model-registry:' || current_setting('aeon.tenant_id', true), 0))`); err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM model_profiles`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return upgradeCatalog(ctx, tx, p)
	}
	profiles := catalogProfiles()
	ids := make(map[string]string, len(profiles))
	seeded := make([]Profile, 0, len(profiles))
	for _, profile := range profiles {
		row, err := insertProfile(ctx, tx, p.TenantID, profileWrite{
			Slug: profile.Slug, Version: profile.Version, Harness: profile.Harness,
			Family: profile.Family, Model: profile.Model, Effort: profile.Effort, Tier: profile.Tier,
		})
		if err != nil {
			return err
		}
		ids[profile.Slug] = row.ID
		seeded = append(seeded, row)
	}
	routes := make([]Route, 0)
	for _, route := range defaultRoutes(profiles) {
		id := ids[route.Slug]
		if id == "" {
			return fail(http.StatusInternalServerError, "catalog route is missing its profile")
		}
		stored := Route{Role: route.Role, Priority: route.Priority, ProfileID: id, State: "available"}
		if err := insertRoute(ctx, tx, p.TenantID, stored); err != nil {
			return err
		}
		routes = append(routes, stored)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO model_refresh_settings(tenant_id,catalog_version) VALUES($1,$2) ON CONFLICT (tenant_id) DO UPDATE SET catalog_version=EXCLUDED.catalog_version`, p.TenantID, CatalogVersion); err != nil {
		return err
	}
	return writeEvent(ctx, tx, p, evSeeded, nil, struct {
		Profiles []Profile `json:"profiles"`
		Routes   []Route   `json:"routes"`
	}{seeded, routes})
}

func insertProfile(ctx context.Context, tx pgx.Tx, tenantID string, in profileWrite) (Profile, error) {
	var out Profile
	err := tx.QueryRow(ctx, `
		INSERT INTO model_profiles
			(tenant_id, slug, version, harness, family, model, effort, tier, enabled)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, true)
		RETURNING id::text, slug, version, harness, family, model, effort, tier, enabled, created_at`,
		tenantID, in.Slug, in.Version, in.Harness, in.Family, in.Model, in.Effort, in.Tier).
		Scan(&out.ID, &out.Slug, &out.Version, &out.Harness, &out.Family, &out.Model, &out.Effort, &out.Tier, &out.Enabled, &out.CreatedAt)
	return out, err
}

func insertRoute(ctx context.Context, tx pgx.Tx, tenantID string, route Route) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO model_role_routes (tenant_id, role, priority, profile_id, state, reason, valid_until)
		VALUES ($1::uuid, $2, $3, $4::uuid, $5, $6, $7)`,
		tenantID, route.Role, route.Priority, route.ProfileID, route.State, route.Reason, route.ValidUntil)
	return err
}

func listProfiles(ctx context.Context, tx pgx.Tx) ([]Profile, error) {
	rows, err := tx.Query(ctx, `
		SELECT id::text, slug, version, harness, family, model, effort, tier, enabled, created_at
		FROM model_profiles
		ORDER BY slug, version, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Profile{}
	for rows.Next() {
		var profile Profile
		if err := rows.Scan(&profile.ID, &profile.Slug, &profile.Version, &profile.Harness, &profile.Family, &profile.Model, &profile.Effort, &profile.Tier, &profile.Enabled, &profile.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, profile)
	}
	return out, rows.Err()
}

func listRoutes(ctx context.Context, tx pgx.Tx) ([]Route, error) {
	rows, err := tx.Query(ctx, `
		SELECT role, priority, profile_id::text, state, reason, valid_until
		FROM model_role_routes
		ORDER BY role, priority, profile_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Route{}
	for rows.Next() {
		var route Route
		if err := rows.Scan(&route.Role, &route.Priority, &route.ProfileID, &route.State, &route.Reason, &route.ValidUntil); err != nil {
			return nil, err
		}
		out = append(out, route)
	}
	return out, rows.Err()
}

func validateProfile(in profileWrite) error {
	in.Slug = strings.TrimSpace(in.Slug)
	in.Version = strings.TrimSpace(in.Version)
	in.Model = strings.TrimSpace(in.Model)
	in.Effort = strings.TrimSpace(in.Effort)
	if !slugRE.MatchString(in.Slug) || len(in.Slug) > 128 {
		return fail(http.StatusBadRequest, "invalid slug")
	}
	if in.Version == "" || len(in.Version) > 64 || strings.ContainsAny(in.Version, "\x00\r\n") {
		return fail(http.StatusBadRequest, "invalid version")
	}
	if !validHarness(in.Harness) || (!validFamily(in.Family) && !(in.Harness == "pi" && in.Family == "unknown")) || !validTier(in.Tier) {
		return fail(http.StatusBadRequest, "invalid harness, family or tier")
	}
	if len(in.Model) > 128 || !modelRE.MatchString(in.Model) {
		return fail(http.StatusBadRequest, "invalid model")
	}
	if len(in.Effort) > 32 || !effortRE.MatchString(in.Effort) {
		return fail(http.StatusBadRequest, "invalid effort")
	}
	return nil
}

func createProfile(ctx context.Context, tx pgx.Tx, p tenant.Principal, in profileWrite) (Profile, error) {
	in.Slug = strings.TrimSpace(in.Slug)
	in.Version = strings.TrimSpace(in.Version)
	in.Model = strings.TrimSpace(in.Model)
	in.Effort = strings.TrimSpace(in.Effort)
	if err := validateProfile(in); err != nil {
		return Profile{}, err
	}
	if err := ensureCatalog(ctx, tx, p); err != nil {
		return Profile{}, err
	}
	out, err := insertProfile(ctx, tx, p.TenantID, in)
	if err != nil {
		return Profile{}, err
	}
	if err := writeEvent(ctx, tx, p, evProfile, nil, out); err != nil {
		return Profile{}, err
	}
	return out, nil
}

func replaceRoutes(ctx context.Context, tx pgx.Tx, p tenant.Principal, incoming []Route, now time.Time) ([]Route, error) {
	if incoming == nil {
		return nil, fail(http.StatusBadRequest, "routes must be an array")
	}
	if err := ensureCatalog(ctx, tx, p); err != nil {
		return nil, err
	}
	normalized, err := normalizeRoutes(incoming, now)
	if err != nil {
		return nil, err
	}
	if err := profilesExist(ctx, tx, normalized); err != nil {
		return nil, err
	}
	before, err := listRoutes(ctx, tx)
	if err != nil {
		return nil, err
	}
	if routesEqual(before, normalized) {
		return before, nil
	}
	if _, err := tx.Exec(ctx, `DELETE FROM model_role_routes`); err != nil {
		return nil, err
	}
	for _, route := range normalized {
		if err := insertRoute(ctx, tx, p.TenantID, route); err != nil {
			return nil, err
		}
	}
	if err := writeEvent(ctx, tx, p, evRoutes, before, normalized); err != nil {
		return nil, err
	}
	return normalized, nil
}

func normalizeRoutes(incoming []Route, now time.Time) ([]Route, error) {
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
			if route.ValidUntil == nil || !route.ValidUntil.After(now) {
				return nil, fail(http.StatusBadRequest, "suppression requires a future expiry")
			}
		}
		out = append(out, route)
	}
	return out, nil
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
