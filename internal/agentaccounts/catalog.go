// SPDX-License-Identifier: AGPL-3.0-only

package agentaccounts

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/httpapi"
)

// ModelsForRun projects the same catalog as /agent-accounts/catalog, scoped to
// the exact enrolled account of a run. It exposes no account credentials or keys.
func ModelsForRun(ctx context.Context, tx pgx.Tx, runID, harness string) ([]CatalogModel, error) {
	var account Account
	err := tx.QueryRow(ctx, `SELECT a.harness,a.allowed_model_profile_ids::text[]
		FROM agent_runs r JOIN agent_accounts a ON a.id=r.account_id AND a.tenant_id=r.tenant_id
		WHERE r.id=$1 AND a.harness=$2 AND a.daemon_id=r.daemon_id`, runID, harness).Scan(&account.Harness, &account.AllowedProfileIDs)
	if errors.Is(err, pgx.ErrNoRows) {
		return []CatalogModel{}, nil
	}
	if err != nil {
		return nil, err
	}
	now, err := dbNow(ctx, tx)
	if err != nil {
		return nil, err
	}
	profiles, err := catalogProfiles(ctx, tx, "build", "", now)
	if err != nil {
		return nil, err
	}
	return catalogAccount(account, profiles, 0, now).Models, nil
}

// Catalog is a projection of existing enrollments and registry profiles, never
// a second source of model IDs or an assertion of provider entitlements.
type Catalog struct {
	AsOf  time.Time     `json:"as_of"`
	Role  string        `json:"role"`
	Hosts []CatalogHost `json:"hosts"`
}

type CatalogHost struct {
	DaemonID  string           `json:"daemon_id"`
	Label     string           `json:"label"`
	Harnesses []CatalogHarness `json:"harnesses"`
}

type CatalogHarness struct {
	Harness          string           `json:"harness"`
	Accounts         []CatalogAccount `json:"accounts"`
	DefaultAccountID *string          `json:"default_account_id"`
}

type CatalogAccount struct {
	ID                 string          `json:"id"`
	Label              string          `json:"label"`
	Plan               string          `json:"plan"`
	RegisteredBy       string          `json:"registered_by_principal_id"`
	State              string          `json:"state"`
	LastProbeAt        *time.Time      `json:"last_probe_at"`
	LastProbeOK        *bool           `json:"last_probe_ok"`
	Available          bool            `json:"available"`
	UnavailableReasons []string        `json:"unavailable_reasons"`
	RemainingFraction  *float64        `json:"remaining_fraction"`
	Windows            []CatalogWindow `json:"windows"`
	Models             []CatalogModel  `json:"models"`
	DefaultProfileID   *string         `json:"default_model_profile_id"`
}

type CatalogWindow struct {
	Window
	Remaining     int64 `json:"remaining"`
	PaceRemaining int64 `json:"pace_remaining"`
}

type CatalogModel struct {
	Model   string          `json:"model"`
	Family  string          `json:"family"`
	Efforts []CatalogEffort `json:"efforts"`
}

type CatalogEffort struct {
	Effort    string `json:"effort"`
	ProfileID string `json:"model_profile_id"`
	Version   string `json:"version"`
}

type catalogProfile struct {
	ID, Harness, Model, Family, Effort, Version string
	Priority                                    *int
}

func (m *Module) catalog(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if err := m.requirePermission(r, p, "account.read"); err != nil {
		writeErr(w, err)
		return
	}
	role, family := r.URL.Query().Get("role"), r.URL.Query().Get("author_family")
	if role == "" {
		role = "build"
	}
	if !slices.Contains([]string{"scout", "mechanical", "build", "build-hard", "review-gate"}, role) ||
		(family != "" && !slices.Contains([]string{"openai", "anthropic", "xai", "cursor"}, family)) ||
		(role == "review-gate" && family == "") {
		writeErr(w, fail(http.StatusBadRequest, "invalid role or author family"))
		return
	}
	var out Catalog
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		now, err := dbNow(r.Context(), tx)
		if err != nil {
			return err
		}
		accounts, err := listAccounts(r.Context(), tx)
		if err != nil {
			return err
		}
		used, err := occupancy(r.Context(), tx)
		if err != nil {
			return err
		}
		profiles, err := catalogProfiles(r.Context(), tx, role, family, now)
		if err != nil {
			return err
		}
		out = buildCatalog(accounts, profiles, used, role, now)
		return nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, out)
}

func catalogProfiles(ctx context.Context, tx pgx.Tx, role, authorFamily string, now time.Time) ([]catalogProfile, error) {
	// Use the tenant's immutable profiles and role ladder, including expiring
	// suppressions. Read-only: catalog initialization remains modelregistry's job.
	rows, err := tx.Query(ctx, `
		SELECT p.id::text,p.harness,p.model,p.family,p.effort,p.version,r.priority
		FROM model_profiles p LEFT JOIN model_role_routes r
		  ON r.tenant_id=p.tenant_id AND r.profile_id=p.id AND r.role=$1
		WHERE p.enabled
		  AND ($1 <> 'review-gate' OR (p.family <> $2 AND r.profile_id IS NOT NULL))
		  AND (r.state IS NULL OR r.state='available' OR r.valid_until <= $3)
		ORDER BY p.harness,p.model,p.family,p.effort,p.created_at DESC,p.id`, role, authorFamily, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	profiles := []catalogProfile{}
	for rows.Next() {
		var p catalogProfile
		if err := rows.Scan(&p.ID, &p.Harness, &p.Model, &p.Family, &p.Effort, &p.Version, &p.Priority); err != nil {
			return nil, err
		}
		profiles = append(profiles, p)
	}
	return profiles, rows.Err()
}

func buildCatalog(accounts []Account, profiles []catalogProfile, occupancy map[string]int, role string, now time.Time) Catalog {
	out := Catalog{AsOf: now, Role: role, Hosts: []CatalogHost{}}
	sort.Slice(accounts, func(i, j int) bool {
		if accounts[i].DaemonID != accounts[j].DaemonID {
			return accounts[i].DaemonID < accounts[j].DaemonID
		}
		if accounts[i].Harness != accounts[j].Harness {
			return accounts[i].Harness < accounts[j].Harness
		}
		return accounts[i].ID < accounts[j].ID
	})
	for _, a := range accounts {
		if len(out.Hosts) == 0 || out.Hosts[len(out.Hosts)-1].DaemonID != a.DaemonID {
			out.Hosts = append(out.Hosts, CatalogHost{DaemonID: a.DaemonID, Label: a.DaemonID, Harnesses: []CatalogHarness{}})
		}
		host := &out.Hosts[len(out.Hosts)-1]
		if host.Label == host.DaemonID && a.HostLabel != "" {
			host.Label = a.HostLabel
		}
		if len(host.Harnesses) == 0 || host.Harnesses[len(host.Harnesses)-1].Harness != a.Harness {
			host.Harnesses = append(host.Harnesses, CatalogHarness{Harness: a.Harness, Accounts: []CatalogAccount{}})
		}
		h := &host.Harnesses[len(host.Harnesses)-1]
		h.Accounts = append(h.Accounts, catalogAccount(a, profiles, occupancy[a.ID], now))
	}
	for i := range out.Hosts {
		for j := range out.Hosts[i].Harnesses {
			h := &out.Hosts[i].Harnesses[j]
			best := -1.0
			for _, a := range h.Accounts {
				if a.Available && a.RemainingFraction != nil && *a.RemainingFraction > best {
					best = *a.RemainingFraction
					id := a.ID
					h.DefaultAccountID = &id
				}
			}
		}
	}
	return out
}

func catalogAccount(a Account, profiles []catalogProfile, usedSlots int, now time.Time) CatalogAccount {
	out := CatalogAccount{ID: a.ID, Label: a.Label, Plan: a.Plan, RegisteredBy: a.RegisteredBy,
		State: a.State, LastProbeAt: a.LastProbeAt, LastProbeOK: a.LastProbeOK,
		UnavailableReasons: []string{}, Windows: []CatalogWindow{}, Models: []CatalogModel{}}
	bestPriority := int(^uint(0) >> 1)
	for _, p := range profiles {
		if p.Harness != a.Harness || (a.AllowedProfileIDs != nil && !slices.Contains(a.AllowedProfileIDs, p.ID)) {
			continue
		}
		index := -1
		for i := range out.Models {
			if out.Models[i].Model == p.Model && out.Models[i].Family == p.Family {
				index = i
				break
			}
		}
		if index < 0 {
			out.Models = append(out.Models, CatalogModel{Model: p.Model, Family: p.Family, Efforts: []CatalogEffort{}})
			index = len(out.Models) - 1
		}
		out.Models[index].Efforts = append(out.Models[index].Efforts, CatalogEffort{Effort: p.Effort, ProfileID: p.ID, Version: p.Version})
		if p.Priority != nil && *p.Priority < bestPriority {
			bestPriority = *p.Priority
			id := p.ID
			out.DefaultProfileID = &id
		}
	}
	remainingKnown := true
	for _, w := range activeWindows(a.Windows, now) {
		remaining := max(int64(0), w.Allowance-w.Used-w.Reserved)
		paceRemaining := max(int64(0), allowedUnits(w.Allowance, paceFraction(w.PaceModel, elapsedFraction(now, w.StartsAt, w.EndsAt), w.BurstRatio))-w.Used-w.Reserved)
		out.Windows = append(out.Windows, CatalogWindow{Window: w, Remaining: remaining, PaceRemaining: paceRemaining})
		if w.Provisional || w.Allowance <= 0 {
			remainingKnown = false
			continue
		}
		ratio := float64(remaining) / float64(w.Allowance)
		if out.RemainingFraction == nil || ratio < *out.RemainingFraction {
			out.RemainingFraction = &ratio
		}
	}
	// Ledger headroom still governs queue eligibility, but one unknown window
	// prevents a measured aggregate or an allowance-ranked catalog default.
	if !remainingKnown {
		out.RemainingFraction = nil
	}
	if a.State != "available" {
		out.UnavailableReasons = append(out.UnavailableReasons, "state")
	}
	if !probeFresh(a, now) {
		out.UnavailableReasons = append(out.UnavailableReasons, "probe")
	}
	if usedSlots >= a.MaxParallel {
		out.UnavailableReasons = append(out.UnavailableReasons, "capacity")
	}
	if !allowanceHeadroom(a.Windows, now) {
		out.UnavailableReasons = append(out.UnavailableReasons, "allowance")
	}
	if len(out.Models) == 0 {
		out.UnavailableReasons = append(out.UnavailableReasons, "models")
	}
	out.Available = len(out.UnavailableReasons) == 0
	return out
}
