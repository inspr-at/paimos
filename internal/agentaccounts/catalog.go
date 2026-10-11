// SPDX-License-Identifier: AGPL-3.0-only

package agentaccounts

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/accountuse"
	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/harnesslaunch"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/reviewgate"
)

// ModelsForRun projects the same catalog as /agent-accounts/catalog, scoped to
// the exact enrolled account of a run. It exposes no account credentials or keys.
func ModelsForRun(ctx context.Context, tx pgx.Tx, runID, harness string) ([]CatalogModel, error) {
	var account Account
	err := tx.QueryRow(ctx, `SELECT a.harness,aeon_account_allowed_profile_ids(a.harness,a.allowed_model_profile_ids)::text[]
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
	Wait               *CapacityWait   `json:"wait,omitempty"`
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
	line                                        string
	level                                       *int
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
	projectID := r.URL.Query().Get("project_id")
	if projectID != "" && !uuidRE.MatchString(projectID) {
		writeErr(w, fail(400, "invalid project id"))
		return
	}
	role, family := r.URL.Query().Get("role"), r.URL.Query().Get("author_family")
	if role == "" {
		role = "build"
	}
	if !slices.Contains([]string{"scout", "mechanical", "build", "build-hard", "review-gate", "review-gate-security"}, role) ||
		(family != "" && !reviewgate.ValidFamily(family)) ||
		(strings.HasPrefix(role, "review-gate") && family == "") {
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
		accounts, err = expandCatalogAllowances(r.Context(), tx, accounts)
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
		if err := catalogPriorities(r.Context(), tx, role, family, projectID, profiles); err != nil {
			return err
		}
		out = buildCatalog(accounts, profiles, used, role, now)
		byID := make(map[string]Account, len(accounts))
		for _, a := range accounts {
			byID[a.ID] = a
		}
		for hi := range out.Hosts {
			for vi := range out.Hosts[hi].Harnesses {
				h := &out.Hosts[hi].Harnesses[vi]
				h.DefaultAccountID = nil
				for ai := range h.Accounts {
					choice := &h.Accounts[ai]
					a := byID[choice.ID]
					windows, wait, err := admission(r.Context(), tx, a, a.Windows, now, used[a.ID], runRow{Purpose: "managed"}, false)
					if err != nil {
						return err
					}
					if wait == nil {
						wait = windowWait(windows, now)
					}
					if len(choice.Models) == 0 {
						wait = waitFor("models")
					}
					allowed, err := accountuse.AllowedForProject(r.Context(), tx, a.ID, projectID)
					if err != nil {
						return err
					}
					if !allowed {
						wait = waitFor("context")
						wait.Context = projectID
					}
					choice.Wait = wait
					choice.Available = wait == nil
					// Retain the legacy reason vocabulary for existing clients.
					if choice.Available {
						choice.UnavailableReasons = []string{}
					} else if len(choice.UnavailableReasons) == 0 {
						choice.UnavailableReasons = []string{"allowance"}
					}
				}
				h.DefaultAccountID = chooseDefaultAccount(h.Accounts)
			}
		}
		return nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, out)
}

func catalogProfiles(ctx context.Context, tx pgx.Tx, role, authorFamily string, now time.Time) ([]catalogProfile, error) {
	// Use current model pauses and the review qualification rule. Catalog reads
	// never initialize profiles or grant models dispatch authority.
	rows, err := tx.Query(ctx, `
		SELECT p.id::text,p.harness,p.model,p.family,p.effort,p.version,aeon_models_line(p.family,p.harness,p.model),coalesce(p.registered_effort_level,d.effort_level)::integer
		FROM model_profiles p JOIN model_profile_display d ON d.tenant_id=p.tenant_id AND d.profile_id=p.id LEFT JOIN (`+ModelPausesSQL+`) r
		  ON r.tenant_id=p.tenant_id AND r.profile_id=p.id
		LEFT JOIN model_observations o ON o.tenant_id=p.tenant_id AND o.harness=p.harness AND o.model=p.model AND o.effort=p.effort
		WHERE p.enabled
		  AND p.model NOT IN ('gpt-6-terra','gpt-6.1-astra','gpt-6.1-luna','gpt-6.1-terra')
		  AND (o.suppressed_until IS NULL OR o.suppressed_until <= $3)
		  AND ($1 <> 'review-gate-security' OR p.family <> $4)
		  AND ($1 NOT IN ('review-gate','review-gate-security') OR (p.family <> $2 AND p.family <> 'unknown' AND p.tier IN ('strong','frontier') AND coalesce(p.registered_effort_level,d.effort_level)>=4))
		  AND (r.state IS NULL OR r.state='available' OR r.valid_until <= $3)
		ORDER BY p.harness,p.model,p.family,p.effort,p.created_at DESC,p.id`, role, authorFamily, now, SecurityExcludedFamily)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	profiles := []catalogProfile{}
	for rows.Next() {
		var p catalogProfile
		if err := rows.Scan(&p.ID, &p.Harness, &p.Model, &p.Family, &p.Effort, &p.Version, &p.line, &p.level); err != nil {
			return nil, err
		}
		family := harnesslaunch.ModelFamily(p.Harness, p.Model)
		if family != p.Family || strings.HasPrefix(role, "review-gate") && (!harnesslaunch.FamilyMatches(p.Harness, p.Model, p.Family) || family == authorFamily) {
			continue
		}
		profiles = append(profiles, p)
	}
	return profiles, rows.Err()
}

// catalogPriorities ranks profiles by the workspace Models walk for the role:
// its row and situation order, then each step's route at the step's native
// effort (else the nearest level, ties up; reviews take the highest). Quick
// jobs and an Automatic review keep no saved order, so nothing is preselected.
// A board beyond its bounds preselects nothing rather than failing the catalog.
func catalogPriorities(ctx context.Context, tx pgx.Tx, role, authorFamily, project string, profiles []catalogProfile) error {
	q := modelprefs.BoardQuery{Column: "other", Situation: "first"}
	switch role {
	case "scout", "mechanical":
		return nil
	case "build-hard":
		q.Situation = "stuck"
	case "review-gate", "review-gate-security":
		q.Review, q.AuthorFamily = true, authorFamily
	}
	s, err := modelprefs.LoadBoardRouting(ctx, tx, nil, project)
	if errors.Is(err, modelprefs.ErrBoardBounds) {
		return nil
	}
	if err != nil {
		return err
	}
	d := modelprefs.ResolveModelsBoard(s, q)
	for n, step := range d.Steps {
		if slices.Contains(d.Not, step.Line) {
			continue
		}
		routes := map[string][]int{}
		keys := []string{}
		for i, p := range profiles {
			if p.line != step.Line {
				continue
			}
			key := p.Harness + "\x00" + p.Model
			if _, ok := routes[key]; !ok {
				keys = append(keys, key)
			}
			routes[key] = append(routes[key], i)
		}
		// A route lacking the step's effort name takes the nearest level to
		// the line's named effort, as dispatch does (modelsRouteCandidates).
		level := step.Level
		for _, key := range keys {
			for _, i := range routes[key] {
				if step.Effort != "" && profiles[i].Effort == step.Effort && profiles[i].level != nil {
					level = *profiles[i].level
				}
			}
		}
		for _, key := range keys {
			exact, levels := false, []int{}
			for _, i := range routes[key] {
				exact = exact || step.Effort != "" && profiles[i].Effort == step.Effort
				if profiles[i].level != nil {
					levels = append(levels, *profiles[i].level)
				}
			}
			nearest := modelprefs.NearestEffort(level, levels)
			if q.Review {
				exact, nearest = false, slices.Max(append(levels, -1))
			}
			for _, i := range routes[key] {
				p := &profiles[i]
				if exact && p.Effort != step.Effort || !exact && (p.level == nil || *p.level != nearest) || p.Priority != nil {
					continue
				}
				priority := n
				p.Priority = &priority
			}
		}
	}
	return nil
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
			h.DefaultAccountID = chooseDefaultAccount(h.Accounts)
		}
	}
	return out
}

// chooseDefaultAccount ranks measured remaining fraction, lowest account id
// breaking a tie. With no measurement, the only runnable account is the
// default. Several unread accounts stay unranked, and unknown is never zero.
func chooseDefaultAccount(accounts []CatalogAccount) *string {
	best := -1.0
	var measured, only string
	known := false
	runnable := 0
	for i := range accounts {
		a := &accounts[i]
		if !a.Available {
			continue
		}
		runnable++
		only = a.ID
		if a.RemainingFraction != nil && *a.RemainingFraction > best {
			best = *a.RemainingFraction
			measured = a.ID
			known = true
		}
	}
	if known {
		id := measured
		return &id
	}
	if runnable == 1 {
		id := only
		return &id
	}
	return nil
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
