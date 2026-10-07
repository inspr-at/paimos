// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"math"
	"net/http"
	"sort"
	"time"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// CapacityRouting is advice, never a reservation or authority to launch.
type CapacityRouting struct {
	Rank           int           `json:"rank"`
	AvailableSlots int           `json:"available_slots"`
	ResetsAt       *time.Time    `json:"resets_at,omitempty"`
	CapPercent     float64       `json:"cap_percent"`
	SameQuotaAs    string        `json:"same_quota_as,omitempty"`
	Wait           *CapacityWait `json:"wait,omitempty"`
}
type CapacityChoice struct {
	CapacityRouting
	AccountID    string `json:"account_id"`
	AccountLabel string `json:"account_label"`
	DaemonID     string `json:"daemon_id"`
	Harness      string `json:"harness"`
}
type CapacityNext struct {
	Harness      string           `json:"harness"`
	Accounts     []CapacityChoice `json:"accounts"`
	ParallelRuns int              `json:"parallel_runs"`
	Wait         *CapacityWait    `json:"wait,omitempty"`
}

// routeRank uses all binding windows for the cap, but prefers the long quota
// for expiry order. A five-hour reset cannot displace a weekly/monthly quota.
// T6 can supply presence and learned holds here; until then the shared
// windowEstimate is the conservative one-percent hold on observed windows.
func routeRank(a Account, windows []Window, slots int, estimates map[string]int64, now time.Time) ranked {
	p := ranked{account: a, windows: windows, cap: 100, slots: a.MaxParallel - slots}
	var short, long *time.Time
	for _, w := range windows {
		if w.capacityPresence {
			p.presence = true
		}
		if !synthetic(w) {
			end := w.EndsAt
			dest := &short
			if w.capacityKind == "weekly" || w.capacityKind == "monthly" {
				dest = &long
			}
			if *dest == nil || end.Before(**dest) {
				*dest = &end
			}
		}
		if synthetic(w) {
			if len(w.recoveryPermits) > 0 {
				p.slots = min(p.slots, 1)
			}
			continue
		}
		available := float64(allowedUnits(w.Allowance, paceFraction(w.PaceModel, elapsedFraction(now, w.StartsAt, w.EndsAt), w.BurstRatio)) - w.Used - w.Reserved)
		if w.capacityBudget != nil {
			available = math.Min(available, *w.capacityBudget-float64(w.Reserved))
		}
		if w.Allowance > 0 {
			p.cap = math.Min(p.cap, 100*available/float64(w.Allowance))
		}
		hold := windowEstimate(w, estimates)
		if hold <= 0 {
			p.slots = 0
			continue
		}
		p.slots = min(p.slots, int(math.Floor(available/float64(hold))))
		if w.capacityReadAt != nil && (synthetic(w) || now.Sub(*w.capacityReadAt) > 10*time.Minute && w.capacitySource != "estimate") {
			p.slots = min(p.slots, 1)
		}
	}
	p.reset = long
	if p.reset == nil {
		p.reset = short
	}
	p.cap = math.Max(0, p.cap)
	p.slots = max(0, p.slots)
	return p
}

// fingerprintPrimary is the ordered door that keeps the quota's slots.
// A later door wins only when the earlier one has none left.
func fingerprintPrimary(picks []ranked) map[string]int {
	primary := map[string]int{}
	for i, p := range picks {
		fp := p.account.QuotaPoolFingerprint
		if fp == "" {
			continue
		}
		if prev, ok := primary[fp]; !ok || picks[prev].slots == 0 && p.slots > 0 {
			primary[fp] = i
		}
	}
	return primary
}
func orderPicks(picks []ranked) {
	sort.Slice(picks, func(i, j int) bool {
		a, b := picks[i], picks[j]
		if a.presence != b.presence {
			return !a.presence
		}
		if a.reset == nil && b.reset != nil {
			return false
		}
		if a.reset != nil && b.reset == nil {
			return true
		}
		if a.reset != nil && b.reset != nil && !a.reset.Equal(*b.reset) {
			return a.reset.Before(*b.reset)
		}
		if a.cap != b.cap {
			return a.cap > b.cap
		}
		return a.account.ID < b.account.ID
	})
}

func routingAdvice(ctx context.Context, tx pgx.Tx, accounts []Account, profile string, run runRow, now time.Time) (map[string]CapacityRouting, error) {
	used, err := occupancy(ctx, tx)
	if err != nil {
		return nil, err
	}
	quotaUsed, err := quotaOccupancy(ctx, tx)
	if err != nil {
		return nil, err
	}
	out := map[string]CapacityRouting{}
	if profile != "" {
		run.ProfileID = &profile
	}
	pools := map[string][]ranked{}
	// Manual budgets have no learned unit conversion: advisory uses one unit;
	// reservation always checks the daemon's real estimates again.
	estimates := map[string]int64{"requests": 1, "tokens": 1, "cost_micros": 1}
	for _, a := range accounts {
		var allowed bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM model_profiles WHERE enabled AND harness=$1 AND ($2='' OR id::text=$2) AND ($3::uuid[] IS NULL OR id=ANY($3::uuid[])))`, a.Harness, profile, a.AllowedProfileIDs).Scan(&allowed)
		if err != nil {
			return nil, err
		}
		if !allowed {
			out[a.ID] = CapacityRouting{Wait: waitFor("models")}
			continue
		}
		slots := slotCount(a, used, quotaUsed)
		windows, wait, err := admission(ctx, tx, a, a.Windows, now, slots, run, false)
		if err != nil {
			return nil, err
		}
		if wait == nil {
			wait = windowWait(windows, now)
		}
		if wait != nil {
			out[a.ID] = CapacityRouting{Wait: wait}
			continue
		}
		p := routeRank(a, windows, slots, estimates, now)
		pools[a.Harness] = append(pools[a.Harness], p)
	}
	for _, picks := range pools {
		orderPicks(picks)
		primary := fingerprintPrimary(picks)
		for i, p := range picks {
			routing := CapacityRouting{Rank: i + 1, AvailableSlots: p.slots, ResetsAt: p.reset, CapPercent: p.cap}
			if fp := p.account.QuotaPoolFingerprint; fp != "" && primary[fp] != i {
				routing.AvailableSlots = 0
				routing.SameQuotaAs = picks[primary[fp]].account.ID
			}
			out[p.account.ID] = routing
		}
	}
	return out, nil
}

// NextForRun is also used by the vendor-stop handoff. It preserves the original
// requested-account fence and the enrolled daemon; it writes nothing. The
// registering principal is not the quota identity of an account on that daemon.
func NextForRun(ctx context.Context, tx pgx.Tx, runID, daemonID, only, exclude string, now time.Time, residency ...string) (CapacityNext, error) {
	run, err := loadWaitRun(ctx, tx, runID)
	if err != nil {
		return CapacityNext{}, err
	}
	// Keep the person's pin, not a previous automatic handoff target.
	if err = tx.QueryRow(ctx, `SELECT requested_account_id::text FROM agent_runs WHERE id=$1`, runID).Scan(&run.RequestedAccountID); err != nil {
		return CapacityNext{}, err
	}
	var harness string
	if err = tx.QueryRow(ctx, `SELECT harness FROM model_profiles WHERE id=$1 AND enabled`, run.ProfileID).Scan(&harness); err != nil {
		if isNoRows(err) {
			return CapacityNext{Accounts: []CapacityChoice{}, Wait: waitFor("models")}, nil
		}
		return CapacityNext{}, err
	}
	accounts, err := listAccounts(ctx, tx)
	if err != nil {
		return CapacityNext{}, err
	}
	kept := []Account{}
	for _, a := range accounts {
		if a.DaemonID != daemonID || a.Harness != harness || a.ID == exclude || only != "" && a.ID != only {
			continue
		}
		kept = append(kept, a)
	}
	if len(residency) > 0 {
		run.Residency = residency[0]
	}
	kept, _, err = narrowCandidates(ctx, tx, run, harness, kept)
	if err != nil {
		return CapacityNext{}, err
	}
	// Run now once belongs to the original attempt, never to automatic retries.
	advice, err := routingAdvice(ctx, tx, kept, *run.ProfileID, runRow{Purpose: "managed"}, now)
	return nextAdvice(harness, kept, advice), err
}
func nextAdvice(harness string, accounts []Account, advice map[string]CapacityRouting) CapacityNext {
	out := CapacityNext{Harness: harness, Accounts: []CapacityChoice{}}
	for _, a := range accounts {
		r := advice[a.ID]
		if r.Rank == 0 {
			if r.Wait != nil && (out.Wait == nil || r.Wait.Until != nil && (out.Wait.Until == nil || r.Wait.Until.Before(*out.Wait.Until))) {
				out.Wait = r.Wait
			}
			continue
		}
		out.Accounts = append(out.Accounts, CapacityChoice{r, a.ID, a.Label, a.DaemonID, a.Harness})
		out.ParallelRuns += r.AvailableSlots
	}
	sort.Slice(out.Accounts, func(i, j int) bool { return out.Accounts[i].Rank < out.Accounts[j].Rank })
	if len(out.Accounts) > 0 {
		out.Wait = nil
	} else if out.Wait == nil {
		out.Wait = waitFor("offline")
	}
	return out
}
func (m *Module) capacityNext(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	harness, daemon, profile := r.URL.Query().Get("harness"), r.URL.Query().Get("daemon_id"), r.URL.Query().Get("model_profile_id")
	if !validHarness(harness) || len(daemon) > 128 || profile != "" && !uuidRE.MatchString(profile) {
		writeErr(w, fail(400, "invalid capacity query"))
		return
	}
	if p.Kind != tenant.Agent {
		if err := m.requirePermission(r, p, "account.read"); err != nil {
			writeErr(w, err)
			return
		}
	}
	var out CapacityNext
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		ownOnly := p.Kind == tenant.Agent
		if p.Kind == tenant.Agent {
			scopes, err := keyScopes(r.Context(), tx, r, p)
			if err != nil {
				return err
			}
			paired, err := agentpairing.PairedPrincipal(r.Context(), tx, p.ID)
			if err != nil {
				return err
			}
			reader := p
			reader.Scopes = scopes
			if hasScope(scopes, "account.read") && authz.RequireTx(r.Context(), tx, reader, "account.read", authz.Scope{}) == nil {
				ownOnly = paired
			} else if !hasScope(scopes, "account.probe") {
				return fail(403, "account read or own-account probe authority required")
			}
		}
		all, err := listAccounts(r.Context(), tx)
		if err != nil {
			return err
		}
		accounts := []Account{}
		for _, a := range all {
			if a.Harness == harness && (daemon == "" || a.DaemonID == daemon) && (!ownOnly || a.RegisteredBy == p.ID) {
				accounts = append(accounts, a)
			}
		}
		now, err := dbNow(r.Context(), tx)
		if err != nil {
			return err
		}
		advice, err := routingAdvice(r.Context(), tx, accounts, profile, runRow{Purpose: "managed"}, now)
		if err != nil {
			return err
		}
		out = nextAdvice(harness, accounts, advice)
		return nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}

// VendorRetryAt returns vendor truth when a stop includes a reset, otherwise a
// bounded backoff. This value is frozen on the stopped run, so polling cannot
// slide a long-reset handoff into the short-wait branch.
func VendorRetryAt(ctx context.Context, tx pgx.Tx, accountID, runID string, now time.Time) (time.Time, bool, error) {
	// Retry advice follows the same persisted resource waits as reservation;
	// another daemon poll cannot restart a one-hour timer or shorten backoff.
	a, err := getAccount(ctx, tx, accountID)
	if err != nil {
		return time.Time{}, false, err
	}
	facts, err := loadReadinessFacts(ctx, tx, a, now)
	if err != nil {
		return time.Time{}, false, err
	}
	var next *time.Time
	for _, f := range facts {
		if recoverableFact(f) && f.NextAttemptAt != nil && (next == nil || f.NextAttemptAt.After(*next)) {
			next = f.NextAttemptAt
		}
	}
	if next != nil {
		return *next, false, nil
	}
	// Only this stopped run can identify its named reset. A prior run's
	// sibling reading still blocks admission, but never names this stop.
	var until *time.Time
	err = tx.QueryRow(ctx, `SELECT max(reset) FROM (
      SELECT max(resets_at) AS reset FROM (SELECT DISTINCT ON(window_kind,bucket) resets_at,used_percent,ordinary_usage_allowed FROM account_capacity_readings WHERE account_id IN (`+quotaAccounts+`) AND run_id=$3 AND source<>'estimate' ORDER BY window_kind,bucket,read_at DESC,CASE source WHEN 'harness' THEN 0 ELSE 1 END) r WHERE (ordinary_usage_allowed=false OR used_percent>=100) AND resets_at>$2
      UNION ALL SELECT max(limit_resets_at) FROM run_telemetry WHERE run_id=$3 AND error_code='vendor_limit' AND limit_resets_at>$2
    ) named`, accountID, now, runID).Scan(&until)
	if err != nil {
		return time.Time{}, false, err
	}
	if until != nil {
		return *until, true, nil
	}
	return now.Add(vendorStopBackoff), false, nil
}
