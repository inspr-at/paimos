// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"slices"
	"time"

	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/jackc/pgx/v5"
)

// CapacityWait is advisory. Reserve and claim always recheck the same policy.
type CapacityWait struct {
	Code          string     `json:"code"`
	Until         *time.Time `json:"until,omitempty"`
	ReadAt        *time.Time `json:"read_at,omitempty"`
	Timezone      string     `json:"timezone,omitempty"`
	RunNowAllowed bool       `json:"run_now_allowed"`
}

func waitFor(code string) *CapacityWait { return &CapacityWait{Code: code} }

// blindDayPolicy is the single Q3 switch: false selects unrestricted day use.
// The parallel cap and vendor stop remain authoritative in either policy.
func blindDayPolicy(s capacity.Schedule, now time.Time) bool { return s.WorkingAt(now) }

func synthetic(w Window) bool { return w.capacityKind == "refresh" || w.capacityKind == "blind" }

// admission prepares windows without writing. Only the winning account's
// provisional grant is materialized, under the account lock in selectAccount.
// claiming excludes the run's own slot and preserves its exact one-shot grant.
func admission(ctx context.Context, tx pgx.Tx, a Account, all []Window, now time.Time, slots int, run runRow, claiming bool) ([]Window, *CapacityWait, error) {
	if a.State != "available" {
		return nil, waitFor("state"), nil
	}
	if a.LastProbeOK != nil && !*a.LastProbeOK {
		var failure string
		if err := tx.QueryRow(ctx, `SELECT last_probe_failure FROM agent_accounts WHERE id=$1`, a.ID).Scan(&failure); err != nil {
			return nil, nil, err
		}
		if failure == "auth_failed" {
			return nil, waitFor("sign_in"), nil
		}
		return nil, waitFor("offline"), nil
	}
	if !probeFresh(a, now) {
		return nil, waitFor("offline"), nil
	}
	if slots >= a.MaxParallel {
		return nil, waitFor("capacity"), nil
	}
	var approved bool
	err := tx.QueryRow(ctx, `SELECT NOT EXISTS(
 SELECT 1 FROM agent_pairing_enrollments e
 JOIN agent_pairing_computers c ON c.tenant_id=e.tenant_id AND c.id=e.computer_id
 JOIN agent_pairing_requests q ON q.tenant_id=e.tenant_id AND q.id=e.request_id
 WHERE e.account_id=$1 AND (e.ongoing_approved_at IS NULL OR e.state<>'connected' OR c.state<>'connected' OR q.state<>'redeemed'))`, a.ID).Scan(&approved)
	if err != nil {
		return nil, nil, err
	}
	if !approved {
		return nil, waitFor("approval"), nil
	}
	s, err := routingSchedule(ctx, tx, a)
	if err != nil {
		return nil, nil, err
	}
	// Explicit vendor denial persists until explicit vendor recovery, including
	// across reset. A manual cap or a generation restart cannot erase it.
	var denied bool
	var reset, readAt *time.Time
	err = tx.QueryRow(ctx, `SELECT ordinary_usage_allowed=false,resets_at,read_at FROM account_capacity_readings
 WHERE account_id=$1 AND source<>'estimate' AND ordinary_usage_allowed IS NOT NULL
 ORDER BY read_at DESC,CASE source WHEN 'harness' THEN 0 ELSE 1 END,ordinary_usage_allowed ASC LIMIT 1`, a.ID).Scan(&denied, &reset, &readAt)
	if err != nil && !isNoRows(err) {
		return nil, nil, err
	}
	if denied {
		w := &CapacityWait{Code: "vendor", ReadAt: readAt, Timezone: s.Timezone}
		if reset != nil && reset.After(now) {
			w.Until = reset
		}
		return nil, w, nil
	}
	blindHarness := a.Harness == "grok" || a.Harness == "cursor" || a.Harness == "pi"
	if blindHarness {
		var stopped bool
		// A manual cap or an estimate cannot erase an actual vendor stop.
		// Only explicit later vendor recovery can make this account usable again.
		err = tx.QueryRow(ctx, `SELECT EXISTS(
 SELECT 1 FROM run_telemetry t JOIN agent_runs ar ON ar.tenant_id=t.tenant_id AND ar.id=t.run_id
 WHERE ar.account_id=$1 AND t.error_code='vendor_limit' AND NOT EXISTS(
  SELECT 1 FROM account_capacity_readings r WHERE r.account_id=$1 AND r.source<>'estimate'
  AND r.ordinary_usage_allowed=true AND r.read_at>t.at))`, a.ID).Scan(&stopped)
		if err != nil {
			return nil, nil, err
		}
		if stopped {
			return nil, &CapacityWait{Code: "vendor", Timezone: s.Timezone}, nil
		}
	}
	// Omit synthetic grants from the next job's budget. They are never quota
	// observations and a settled/released grant must not replenish itself.
	regular := make([]Window, 0, len(all))
	var own *Window
	for _, w := range all {
		if synthetic(w) {
			if claiming && w.capacityRefreshRun != nil && *w.capacityRefreshRun == run.ID && now.Before(w.EndsAt) && !w.capacityRetired {
				copy := w
				own = &copy
			}
			continue
		}
		if !w.pairingVerification {
			regular = append(regular, w)
		}
	}
	active := activeWindows(regular, now)
	// A person-scheduled manual budget must not be substituted with a first
	// reading grant before that budget opens (pairing verification is separate).
	if len(active) == 0 {
		for _, w := range regular {
			if w.capacityReadAt == nil && w.StartsAt.After(now) {
				return nil, &CapacityWait{Code: "allowance", Until: &w.StartsAt, Timezone: s.Timezone}, nil
			}
		}
	}
	if own != nil && len(active) == 0 {
		active = []Window{*own}
	}
	if len(active) == 0 {
		if refresh := expiredCapacityRefresh(a, all, now); refresh != nil {
			active = []Window{*refresh}
		}
	}
	if len(active) == 0 && (a.Harness == "codex" || a.Harness == "claude") {
		var observed, granted bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM account_capacity_readings WHERE account_id=$1),
 EXISTS(SELECT 1 FROM account_allowance_windows WHERE account_id=$1 AND capacity_kind='refresh' AND capacity_bucket=$2)`, a.ID, bootstrapBucket(a)).Scan(&observed, &granted)
		if err != nil {
			return nil, nil, err
		}
		if !observed && !granted && a.daemonGeneration != nil {
			if slots > 0 {
				return nil, waitFor("reading"), nil
			}
			w := provisionalWindow(a.ID, now, "refresh")
			w.capacityBucket = bootstrapBucket(a)
			active = []Window{w}
		}
	}
	blind := false
	if blindHarness {
		var observed bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM account_capacity_readings WHERE account_id=$1)`, a.ID).Scan(&observed); err != nil {
			return nil, nil, err
		}
		blind = !observed && (len(active) == 0 || own != nil && own.capacityKind == "blind")
		if blind {
			loc, _ := time.LoadLocation(s.Timezone)
			local := now.In(loc)
			day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
			var runs int
			// Count durable reservations, including released attempts. Polling,
			// cancelling or a daemon restart cannot mint another daily allowance.
			err := tx.QueryRow(ctx, `SELECT count(DISTINCT r.run_id)
 FROM account_reservations r JOIN account_allowance_windows w ON w.tenant_id=r.tenant_id AND w.id=r.window_id
 WHERE w.account_id=$1 AND w.capacity_kind='blind' AND w.starts_at >= $2 AND r.run_id::text<>$3`, a.ID, day, run.ID).Scan(&runs)
			if err != nil {
				return nil, nil, err
			}
			if blindDayPolicy(s, now) {
				if slots > 0 {
					return nil, waitFor("capacity"), nil
				}
				if runs >= 3 {
					d := s.Week[(int(local.Weekday())+6)%7]
					end := time.Date(local.Year(), local.Month(), local.Day(), int(d.End), int(d.End*60)%60, 0, 0, loc)
					return nil, &CapacityWait{Code: "allowance", Until: &end, Timezone: s.Timezone}, nil
				}
			}
			if len(active) == 0 {
				active = []Window{provisionalWindow(a.ID, now, "blind")}
			}
		}
	}
	if len(active) == 0 {
		return nil, &CapacityWait{Code: "reading", ReadAt: lastRead(all)}, nil
	}
	stale := false
	for _, w := range active {
		if w.capacityReadAt != nil && (w.capacityKind == "refresh" || now.Sub(*w.capacityReadAt) > 10*time.Minute) {
			stale = true
		}
		if w.capacityReadAt != nil && !w.capacityAllowed {
			return nil, &CapacityWait{Code: "vendor", Until: &w.EndsAt, Timezone: s.Timezone}, nil
		}
	}
	if stale && slots > 0 {
		return nil, &CapacityWait{Code: "reading", ReadAt: lastRead(all)}, nil
	}
	if s.Override == "hold" {
		return nil, &CapacityWait{Code: "hold", Until: s.OverrideUntil, Timezone: s.Timezone}, nil
	}
	// Blind accounts use Q3 at night/off days, not a fictitious vendor window.
	if run.CapacityOverride == "now" {
		s.Override = "sprint"
		s.OverrideUntil = nil
	}
	if !blind {
		if err := applyCapacityPacing(ctx, tx, a, active, now, s); err != nil {
			return nil, nil, err
		}
	}
	for _, w := range active {
		if w.Allowance-w.Used-w.Reserved < boolInt(!claiming) {
			return nil, &CapacityWait{Code: "allowance", Until: &w.EndsAt, Timezone: s.Timezone}, nil
		}
		if w.capacityBudget != nil && *w.capacityBudget < float64(w.Reserved+boolInt(!claiming)) {
			next := s.NextStart(now.Add(time.Second), false)
			if next != nil && !next.After(now.Add(time.Second)) {
				_, end := s.Period(now)
				next = s.NextStart(end, false)
			}
			return nil, &CapacityWait{Code: "schedule", Until: next, Timezone: s.Timezone, RunNowAllowed: true}, nil
		}
	}
	return active, nil, nil
}

func boolInt(v bool) int64 {
	if v {
		return 1
	}
	return 0
}
func timePtr(t time.Time) *time.Time { return &t }
func bootstrapBucket(a Account) string {
	if a.daemonGeneration == nil {
		return "first:"
	}
	return "first:" + *a.daemonGeneration
}
func provisionalWindow(id string, now time.Time, kind string) Window {
	return Window{AccountID: id, StartsAt: now, EndsAt: now.Add(5 * time.Minute), Unit: "percent", Allowance: 1, PaceModel: "unrestricted", capacityReadAt: &now, capacityAllowed: true, capacityKind: kind, Provisional: true}
}
func lastRead(windows []Window) *time.Time {
	var last *time.Time
	for _, w := range windows {
		if !synthetic(w) && w.capacityReadAt != nil && (last == nil || w.capacityReadAt.After(*last)) {
			last = w.capacityReadAt
		}
	}
	return last
}

func windowWait(windows []Window, now time.Time) *CapacityWait {
	for _, w := range windows {
		if _, ok := fits(w, now, 1); !ok {
			if w.capacityReadAt != nil && (synthetic(w) || now.Sub(*w.capacityReadAt) > 10*time.Minute) {
				return &CapacityWait{Code: "reading", ReadAt: w.capacityReadAt}
			}
			return &CapacityWait{Code: "allowance", Until: &w.EndsAt}
		}
	}
	return nil
}

// WaitForRun projects the routing decision for queued runs, including a pinned
// account. It never routes, reserves or reveals another agent's local keys.
func WaitForRun(ctx context.Context, tx pgx.Tx, id string) (*CapacityWait, error) {
	run, err := loadWaitRun(ctx, tx, id)
	if err != nil || run.Status != "queued" || run.Purpose != "managed" {
		return nil, err
	}
	accounts, err := listAccounts(ctx, tx)
	if err != nil {
		return nil, err
	}
	used, err := occupancy(ctx, tx)
	if err != nil {
		return nil, err
	}
	now, err := dbNow(ctx, tx)
	if err != nil {
		return nil, err
	}
	var harness string
	if err := tx.QueryRow(ctx, `SELECT harness FROM model_profiles WHERE id=$1 AND enabled`, run.ProfileID).Scan(&harness); isNoRows(err) {
		return waitFor("models"), nil
	} else if err != nil {
		return nil, err
	}
	best := waitFor("offline")
	for _, a := range accounts {
		if a.RegisteredBy != run.AgentID || a.Harness != harness || run.RequestedAccountID != nil && a.ID != *run.RequestedAccountID || run.AccountID != nil && a.ID != *run.AccountID {
			continue
		}
		if a.AllowedProfileIDs != nil && !slices.Contains(a.AllowedProfileIDs, *run.ProfileID) {
			best = waitFor("models")
			continue
		}
		claiming := run.AccountID != nil
		slots := used[a.ID]
		if claiming {
			slots--
		}
		windows, wait, err := admission(ctx, tx, a, a.Windows, now, slots, run, claiming)
		if err != nil {
			return nil, err
		}
		if wait == nil && !claiming {
			wait = windowWait(windows, now)
		}
		if wait == nil {
			return nil, nil
		}
		if best.Code == "offline" || wait.RunNowAllowed || wait.Until != nil && (best.Until == nil || wait.Until.Before(*best.Until)) {
			best = wait
		}
	}
	return best, nil
}

func loadWaitRun(ctx context.Context, tx pgx.Tx, id string) (runRow, error) {
	var r runRow
	err := tx.QueryRow(ctx, `SELECT id::text,agent_principal_id::text,model_profile_id::text,account_id::text,requested_account_id::text,status,purpose,capacity_override FROM agent_runs WHERE id=$1`, id).Scan(&r.ID, &r.AgentID, &r.ProfileID, &r.AccountID, &r.RequestedAccountID, &r.Status, &r.Purpose, &r.CapacityOverride)
	return r, err
}
