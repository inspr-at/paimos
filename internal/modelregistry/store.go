// SPDX-License-Identifier: AGPL-3.0-only

package modelregistry

import (
	"context"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/harnesslaunch"
	"github.com/inspr-at/paimos/internal/modelactivation"
	"github.com/inspr-at/paimos/internal/tenant"
)

const (
	evSeeded  = "model.registry_seeded"
	evProfile = "model.profile_created"
	evRoutes  = "model.routes_replaced"
)

// Display is model identity for presentation; Version on Profile is the pin revision.
type Display struct {
	DisplayName  string `json:"display_name,omitempty"`
	ShortName    string `json:"short_name,omitempty"`
	ModelVersion string `json:"model_version,omitempty"`
}

func (d Display) FullName() string { return strings.TrimSpace(d.DisplayName + " " + d.ModelVersion) }

// Profile is one immutable model pin.
type Profile struct {
	Source   string     `json:"source"`
	Origin   string     `json:"origin"`
	Note     string     `json:"note"`
	RetireAt *time.Time `json:"retire_at"`
	Retired  bool       `json:"retired"`
	Display
	EffortLevel *int      `json:"effort_level"`
	Provider    string    `json:"provider"`
	ID          string    `json:"id"`
	Slug        string    `json:"slug"`
	Version     string    `json:"version"`
	Harness     string    `json:"harness"`
	Family      string    `json:"family"`
	Model       string    `json:"model"`
	Effort      string    `json:"effort"`
	Tier        string    `json:"tier"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
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
	Source                string `json:"-"`
	Note                  string `json:"note"`
	RegisteredEffortLevel *int   `json:"-"`
	Display
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

func prepareCatalogDeferred(ctx context.Context, tx pgx.Tx, p tenant.Principal) ([]events.Change, error) {
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM model_profiles`).Scan(&n); err != nil {
		return nil, err
	}
	if n > 0 {
		changes, err := prepareAdditionalCatalog(ctx, tx, p)
		if err != nil {
			return nil, err
		}
		upgraded, err := prepareCatalogUpgrade(ctx, tx, p)
		return append(changes, upgraded...), err
	}
	if err := catalogLock(ctx, tx); err != nil {
		return nil, err
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM model_profiles`).Scan(&n); err != nil {
		return nil, err
	}
	if n > 0 {
		changes, err := prepareAdditionalCatalog(ctx, tx, p)
		if err != nil {
			return nil, err
		}
		upgraded, err := prepareCatalogUpgrade(ctx, tx, p)
		return append(changes, upgraded...), err
	}
	profiles := catalogProfiles()
	ids := make(map[string]string, len(profiles))
	disabled := make(map[string]bool, len(profiles))
	seeded := make([]Profile, 0, len(profiles))
	for _, profile := range profiles {
		row, err := insertActivatedProfile(ctx, tx, p, profileWrite{
			Slug: profile.Slug, Version: profile.Version, Harness: profile.Harness,
			Family: profile.Family, Model: profile.Model, Effort: profile.Effort, Tier: profile.Tier,
		}, true, modelactivation.ShippedCatalog)
		if err != nil {
			return nil, err
		}
		ids[profile.Slug] = row.ID
		disabled[profile.Slug] = !row.Enabled
		seeded = append(seeded, row)
	}
	routes := make([]Route, 0)
	for _, route := range defaultRoutes(profiles) {
		id := ids[route.Slug]
		if id == "" {
			return nil, fail(http.StatusInternalServerError, "catalog route is missing its profile")
		}
		if disabled[route.Slug] {
			continue
		}
		stored := Route{Role: route.Role, Priority: route.Priority, ProfileID: id, State: "available"}
		if err := insertRoute(ctx, tx, p.TenantID, stored); err != nil {
			return nil, err
		}
		routes = append(routes, stored)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO model_refresh_settings(tenant_id,catalog_version) VALUES($1,$2) ON CONFLICT (tenant_id) DO UPDATE SET catalog_version=EXCLUDED.catalog_version`, p.TenantID, CatalogVersion); err != nil {
		return nil, err
	}
	return []events.Change{{Type: evSeeded, After: struct {
		Profiles []Profile `json:"profiles"`
		Routes   []Route   `json:"routes"`
	}{seeded, routes}}}, nil
}

// Discovery records a pin without granting it to wildcard accounts.
func insertObservedProfile(ctx context.Context, tx pgx.Tx, tenantID string, in profileWrite) (Profile, error) {
	return insertActivatedProfile(ctx, tx, tenant.Principal{TenantID: tenantID}, in, false, "")
}

func insertActivatedProfile(ctx context.Context, tx pgx.Tx, p tenant.Principal, in profileWrite, enabled bool, cause modelactivation.Cause) (Profile, error) {
	var out Profile
	overrides := map[string]string{}
	if in.DisplayName != "" {
		overrides["display_name"] = in.DisplayName
	}
	if in.ShortName != "" {
		overrides["short_name"] = in.ShortName
	}
	if in.ModelVersion != "" {
		overrides["model_version"] = in.ModelVersion
	}
	if cause == modelactivation.ShippedCatalog {
		in.Source = "shipped"
	}
	// Keep the stored auto/manual contract readable by older servers. Origin
	// is server-owned metadata, ignored by the existing display projection.
	switch in.Source {
	case "manual", "shipped", "provider", "harness":
		overrides["origin"] = in.Source
	}
	stored, err := modelactivation.Activate(ctx, tx, p, modelactivation.Pin{
		Slug: in.Slug, Version: in.Version, Harness: in.Harness, Family: in.Family,
		Model: in.Model, Effort: in.Effort, Tier: in.Tier, Enabled: enabled,
		DisplayOverrides: overrides, Source: profileSource(in.Source), Note: in.Note,
		RegisteredEffortLevel: in.RegisteredEffortLevel, Permission: "models.manage",
	}, cause)
	out = Profile{ID: stored.ID, Slug: stored.Slug, Version: stored.Version, Harness: stored.Harness,
		Family: stored.Family, Model: stored.Model, Effort: stored.Effort, Tier: stored.Tier,
		Enabled: stored.Enabled, CreatedAt: stored.CreatedAt}
	if err == nil {
		err = tx.QueryRow(ctx, `SELECT model_display->>'display_name',model_display->>'short_name',model_display->>'model_version',effort_level,provider
			FROM model_profile_display WHERE tenant_id=$1::uuid AND profile_id=$2::uuid`, p.TenantID, out.ID).
			Scan(&out.DisplayName, &out.ShortName, &out.ModelVersion, &out.EffortLevel, &out.Provider)
	}
	if out.Harness == "gemini" {
		out.EffortLevel = harnesslaunch.GeminiEffortLevel(out.Effort)
	}
	out.Source, out.Note = profileSource(in.Source), in.Note
	out.Origin = profileOrigin(out, in.Source)
	if in.RegisteredEffortLevel != nil {
		out.EffortLevel = in.RegisteredEffortLevel
	}
	return out, err
}
func profileSource(source string) string {
	if source == "manual" {
		return "manual"
	}
	return "auto"
}

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

func listProfiles(ctx context.Context, tx pgx.Tx) ([]Profile, error) {
	return listProfilesLimit(ctx, tx, 0)
}

func listProfilesLimit(ctx context.Context, tx pgx.Tx, limit int) ([]Profile, error) {
	query := `
		SELECT p.id::text, p.slug, p.version, p.harness, p.family, p.model, p.effort, p.tier, p.enabled, p.created_at, d.model_display->>'display_name', d.model_display->>'short_name', d.model_display->>'model_version', coalesce(p.registered_effort_level,d.effort_level), d.provider, coalesce(p.source,'auto'), coalesce(p.display_overrides->>'origin',''), coalesce(p.note,''),
 (SELECT r.retire_at FROM model_profile_retirements r WHERE r.tenant_id=p.tenant_id AND r.profile_id=p.id),
 EXISTS(SELECT 1 FROM model_profile_retirements r WHERE r.tenant_id=p.tenant_id AND r.profile_id=p.id AND (r.retire_at IS NULL OR r.retire_at<=now()))
		FROM model_profiles p JOIN model_profile_display d ON d.tenant_id=p.tenant_id AND d.profile_id=p.id
		ORDER BY p.slug, p.version, p.id`
	if limit > 0 {
		return readProfiles(ctx, tx, query+` LIMIT $1`, limit)
	}
	return readProfiles(ctx, tx, query)
}

// listPickerProfiles bounds editor metadata before decoding and excludes retired revisions.
func listPickerProfiles(ctx context.Context, tx pgx.Tx) ([]Profile, error) {
	return readProfiles(ctx, tx, `
		SELECT p.id::text, p.slug, p.version, p.harness, p.family, p.model, p.effort, p.tier, p.enabled, p.created_at, d.model_display->>'display_name', d.model_display->>'short_name', d.model_display->>'model_version', coalesce(p.registered_effort_level,d.effort_level), d.provider, coalesce(p.source,'auto'), coalesce(p.display_overrides->>'origin',''), coalesce(p.note,''),
 (SELECT r.retire_at FROM model_profile_retirements r WHERE r.tenant_id=p.tenant_id AND r.profile_id=p.id),
 EXISTS(SELECT 1 FROM model_profile_retirements r WHERE r.tenant_id=p.tenant_id AND r.profile_id=p.id AND (r.retire_at IS NULL OR r.retire_at<=now()))
		FROM model_profiles p JOIN model_profile_display d ON d.tenant_id=p.tenant_id AND d.profile_id=p.id
		WHERE NOT EXISTS (SELECT 1 FROM model_profile_retirements r WHERE r.tenant_id=p.tenant_id AND r.profile_id=p.id AND (r.retire_at IS NULL OR r.retire_at<=now()))
		ORDER BY p.slug, p.version, p.id LIMIT 257`)
}

func readProfiles(ctx context.Context, tx pgx.Tx, query string, args ...any) ([]Profile, error) {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Profile{}
	for rows.Next() {
		var profile Profile
		if err := rows.Scan(&profile.ID, &profile.Slug, &profile.Version, &profile.Harness, &profile.Family, &profile.Model, &profile.Effort, &profile.Tier, &profile.Enabled, &profile.CreatedAt, &profile.DisplayName, &profile.ShortName, &profile.ModelVersion, &profile.EffortLevel, &profile.Provider, &profile.Source, &profile.Origin, &profile.Note, &profile.RetireAt, &profile.Retired); err != nil {
			return nil, err
		}
		profile.Source = profileSource(profile.Source)
		profile.Origin = profileOrigin(profile, profile.Origin)
		if profile.Harness == "gemini" {
			profile.EffortLevel = harnesslaunch.GeminiEffortLevel(profile.Effort)
		}
		out = append(out, profile)
	}
	return out, rows.Err()
}

func listRoutes(ctx context.Context, tx pgx.Tx) ([]Route, error) {
	rows, err := tx.Query(ctx, `
		SELECT role, priority, profile_id::text, state, reason, valid_until
		FROM (`+agentaccounts.ModelRoleRoutesSQL+`) routes
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
	if !boundedText(in.Note, 80) || strings.ContainsAny(in.Note, "\x00\r\n") {
		return fail(400, "invalid model note")
	}

	for _, field := range []struct {
		value string
		max   int
	}{{in.DisplayName, 128}, {in.ShortName, 128}, {in.ModelVersion, 64}} {
		if len(field.value) > field.max || strings.ContainsAny(field.value, "\x00\r\n") {
			return fail(http.StatusBadRequest, "invalid model display metadata")
		}
	}
	if (in.DisplayName == "") != (in.ShortName == "") {
		return fail(http.StatusBadRequest, "display_name and short_name must be supplied together")
	}

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
	if !validHarness(in.Harness) || (!validFamily(in.Family) && !((in.Harness == "pi" || in.Harness == "opencode") && in.Family == "unknown")) || !validTier(in.Tier) {
		return fail(http.StatusBadRequest, "invalid harness, family or tier")
	}
	if len(in.Model) > 128 || !modelRE.MatchString(in.Model) {
		return fail(http.StatusBadRequest, "invalid model")
	}
	if harnesslaunch.ModelFamily(in.Harness, in.Model) != in.Family {
		return fail(http.StatusBadRequest, "profile family does not match its harness/provider binding")
	}
	if len(in.Effort) > 32 || !effortRE.MatchString(in.Effort) {
		return fail(http.StatusBadRequest, "invalid effort")
	}
	if in.Harness == "gemini" {
		if _, err := harnesslaunch.GeminiBudgetForModel(in.Model, in.Effort); err != nil {
			return fail(http.StatusBadRequest, err.Error())
		}
	}
	if in.Harness == "opencode" && !strings.Contains(in.Model, "/") {
		return fail(http.StatusBadRequest, "OpenCode requires provider/model")
	}
	return nil
}

func normalizeProfileWrite(in profileWrite) profileWrite {
	in.Slug = strings.TrimSpace(in.Slug)
	in.Version = strings.TrimSpace(in.Version)
	in.Model = strings.TrimSpace(in.Model)
	in.Effort = strings.TrimSpace(in.Effort)
	return in
}

func createProfile(ctx context.Context, tx pgx.Tx, p tenant.Principal, in profileWrite) (Profile, error) {
	in = normalizeProfileWrite(in)
	in.Source = "manual"
	if err := validateProfile(in); err != nil {
		return Profile{}, err
	}
	if err := requireCatalog(ctx, tx); err != nil {
		return Profile{}, err
	}
	out, err := insertActivatedProfile(ctx, tx, p, in, true, modelactivation.Person)
	if err != nil {
		return Profile{}, err
	}
	if err := writeEvent(ctx, tx, p, evProfile, nil, out); err != nil {
		return Profile{}, err
	}
	return out, nil
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

func normalizeRoutes(incoming []Route, now time.Time) ([]Route, error) {
	out, err := normalizeRouteStructure(incoming)
	if err != nil {
		return nil, err
	}
	for _, route := range out {
		if route.State != "available" && !route.ValidUntil.After(now) {
			return nil, fail(http.StatusBadRequest, "suppression requires a future expiry")
		}
	}
	return out, nil
}

// Structural checks run before standalone setup. Expiry and profile existence
// remain authoritative in the final transaction.
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

// flushCatalogChanges runs after the caller's final resource operation.
func flushCatalogChanges(ctx context.Context, tx pgx.Tx, p tenant.Principal, changes []events.Change) error {
	for _, change := range changes {
		if _, err := events.Append(ctx, tx, p, change); err != nil {
			return err
		}
	}
	return nil
}

// Origin preserves the legacy auto/manual source contract while exposing provenance.
// Later working observations cannot relabel an immutable shipped pin.
func profileOrigin(p Profile, stored string) string {
	switch stored {
	case "manual", "shipped", "provider", "harness":
		return stored
	}
	if p.Source == "manual" {
		return "manual"
	}
	for _, cp := range catalogProfiles() {
		if cp.Slug == p.Slug && cp.Harness == p.Harness && cp.Model == p.Model && cp.Effort == p.Effort {
			return "shipped"
		}
	}
	return "harness"
}
