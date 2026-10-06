// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/jackc/pgx/v5"
)

// vendorStopBackoff bounds a vendor stop that names no reset. It is a retry
// limit, not a guessed vendor window.
const vendorStopBackoff = time.Hour

// CapacityWait is advisory. Reserve and claim always recheck the same policy.
type CapacityWait struct {
	HostReason    string     `json:"host_reason,omitempty"`
	Code          string     `json:"code"`
	Until         *time.Time `json:"until,omitempty"`
	ReadAt        *time.Time `json:"read_at,omitempty"`
	Timezone      string     `json:"timezone,omitempty"`
	RunNowAllowed bool       `json:"run_now_allowed"`
}

func waitFor(code string) *CapacityWait { return &CapacityWait{Code: code} }

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
	all, err = sharedQuotaWindows(ctx, tx, a, all, now)
	if err != nil {
		return nil, nil, err
	}
	s, err := routingSchedule(ctx, tx, a)
	if err != nil {
		return nil, nil, err
	}
	if s.ActiveOverride(now) == "hold" {
		return nil, &CapacityWait{Code: "hold", Until: s.OverrideUntil, Timezone: s.Timezone}, nil
	}
	permits, wait, err := readinessAdmission(ctx, tx, a, now, run, claiming)
	if err != nil || wait != nil {
		return nil, wait, err
	}
	if len(permits) > 0 && slots > 0 && !claiming {
		return nil, waitFor("capacity"), nil
	}
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
		// Room readings expire; their old percentages and refresh fences cannot
		// impose a fictitious budget. Hard stops were checked independently.
		if w.pairingVerification || w.capacityKind == "fact" || w.capacityReadAt != nil && (w.capacitySource == "estimate" || now.Sub(*w.capacityReadAt) > 10*time.Minute) {
			continue
		}
		regular = append(regular, w)
	}
	measuredFacts, err := factWindows(ctx, tx, a, now)
	if err != nil {
		return nil, nil, err
	}
	regular = append(regular, measuredFacts...)
	active := activeWindows(regular, now)
	if len(active) == 0 {
		for _, w := range regular {
			if w.capacityReadAt == nil && w.StartsAt.After(now) {
				return nil, &CapacityWait{Code: "allowance", Until: &w.StartsAt, Timezone: s.Timezone}, nil
			}
		}
	}
	// A manual allowance is a person's cap, not a vendor measurement.
	unknownOnly := !slices.ContainsFunc(active, func(w Window) bool { return w.capacityReadAt != nil })
	if len(active) == 0 || len(permits) > 0 {
		w := provisionalWindow(a.ID, now, "blind")
		w.capacitySource = "estimate"
		w.capacityBucket = "unknown:" + run.ID
		if len(permits) > 0 {
			w.capacityBucket = "recover:" + run.ID
		}
		if own != nil {
			w = *own
		}
		w.recoveryPermits = permits
		active = append(active, w)
	}
	if run.CapacityOverride == "now" {
		s.Override = "sprint"
		s.OverrideUntil = nil
	}
	// Unknown usage cannot enforce a numeric reserve, but the person's clock
	// and explicit Hold remain real gates. Sprint/Away/Run now retain meaning.
	if unknownOnly && s.ActiveOverride(now) != "sprint" && s.ActiveOverride(now) != "away" {
		next := s.NextStart(now, s.OffDays == "normal")
		if next == nil || next.After(now) {
			return nil, &CapacityWait{Code: "schedule", Until: next, Timezone: s.Timezone, RunNowAllowed: true}, nil
		}
	}
	if err := applyCapacityPacing(ctx, tx, a, active, now, s); err != nil {
		return nil, nil, err
	}
	learned, err := loadLearning(ctx, tx, a.ID)
	if err != nil {
		return nil, nil, err
	}
	profile := ""
	if run.ProfileID != nil {
		profile = *run.ProfileID
	}
	for i := range active {
		w := &active[i]
		if w.capacityReadAt == nil || synthetic(*w) {
			continue
		}
		v := capacity.Reading{WindowKind: w.capacityKind, Bucket: w.capacityBucket, WindowMinutes: int(w.EndsAt.Sub(w.StartsAt) / time.Minute)}
		windowLearning := learned
		if w.AccountID != a.ID {
			windowLearning, err = loadLearning(ctx, tx, w.AccountID)
			if err != nil {
				return nil, nil, err
			}
		}
		metric := windowLearning.metric(v, now, s, profile)
		// Only fresh measured windows enter this path.
		if now.Sub(*w.capacityReadAt) <= 10*time.Minute || w.capacitySource == "estimate" {
			w.capacityHold = int64(math.Ceil(metric.HoldPercent))
		}
		w.capacityPresence = learned.PresenceUntil != nil && learned.PresenceUntil.After(now)
	}
	// Hard allowance wins over a schedule wait, in a stable window order, so
	// "Run now once" is offered only when it can help.
	ordered := slices.Clone(active)
	slices.SortFunc(ordered, func(a, b Window) int {
		if c := strings.Compare(a.capacityKind, b.capacityKind); c != 0 {
			return c
		}
		if c := strings.Compare(a.capacityBucket, b.capacityBucket); c != 0 {
			return c
		}
		if a.EndsAt.Before(b.EndsAt) {
			return -1
		}
		if b.EndsAt.Before(a.EndsAt) {
			return 1
		}
		return strings.Compare(a.ID, b.ID)
	})
	need := boolInt(!claiming)
	var hardUntil *time.Time
	for _, w := range ordered {
		if w.Allowance-w.Used-w.Reserved >= max(need, w.capacityHold*need) {
			continue
		}
		end := w.EndsAt
		if hardUntil == nil || end.After(*hardUntil) {
			hardUntil = &end
		}
	}
	if hardUntil != nil {
		return nil, &CapacityWait{Code: "allowance", Until: hardUntil, Timezone: s.Timezone}, nil
	}
	// The Advanced sentence caps on top, like a window set by hand. A percent
	// rule lowers the returned windows' budgets in place, so fits agrees.
	if wait, err := limitWait(ctx, tx, a, active, now, run, claiming, s); err != nil || wait != nil {
		return nil, wait, err
	}
	// The schedule is account-wide, Keep for you per window: a run waits for the
	// schedule when any window's paced share is short, and for the reserve when
	// only the reserve stands in the way (until the person's last band before
	// that window's reset).
	var reserve *CapacityWait
	for _, w := range ordered {
		if w.capacityBudget == nil || *w.capacityBudget >= float64(w.Reserved+max(need, w.capacityHold*need)) {
			continue
		}
		if w.capacityShare != nil && *w.capacityShare >= float64(w.Reserved+max(need, w.capacityHold*need)) {
			if reserve == nil || w.capacityReserveUntil != nil && (reserve.Until == nil || w.capacityReserveUntil.After(*reserve.Until)) {
				reserve = &CapacityWait{Code: "reserve", Until: w.capacityReserveUntil, Timezone: s.Timezone, RunNowAllowed: true}
			}
			continue
		}
		next := s.NextStart(now.Add(time.Second), false)
		if next != nil && !next.After(now.Add(time.Second)) {
			_, end := s.Period(now)
			next = s.NextStart(end, false)
		}
		return nil, &CapacityWait{Code: "schedule", Until: next, Timezone: s.Timezone, RunNowAllowed: true}, nil
	}
	if reserve != nil {
		return nil, reserve, nil
	}
	return active, nil, nil
}

func boolInt(v bool) int64 {
	if v {
		return 1
	}
	return 0
}

func containsRecovery(windows []Window) bool {
	for _, w := range windows {
		if synthetic(w) && strings.HasPrefix(w.capacityBucket, "recover:") {
			return true
		}
	}
	return false
}

func timePtr(t time.Time) *time.Time { return &t }
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
		if _, ok := fits(w, now, max(1, windowEstimate(w, nil))); !ok {
			if w.capacityReadAt != nil && (synthetic(w) || now.Sub(*w.capacityReadAt) > 10*time.Minute && w.capacitySource != "estimate") {
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
	return waitForRun(ctx, tx, run)
}

// WaitForQueueRoute evaluates a prospective worker assignment without writing
// it. Fair scheduling and actual queue routing use the same account gates.
func WaitForQueueRoute(ctx context.Context, tx pgx.Tx, id, agent, profile string, account *string) (*CapacityWait, error) {
	run, err := loadWaitRun(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	// Match queue pickup's requested_account_id replacement and retry fallback.
	if account == nil {
		if err := tx.QueryRow(ctx, `SELECT retry_account_id::text FROM agent_runs WHERE id=$1`, id).Scan(&account); err != nil {
			return nil, err
		}
	}
	run.AgentID, run.ProfileID, run.RequestedAccountID = agent, &profile, account
	if run.Status != "queued" || run.Purpose != "managed" {
		return waitFor("state"), nil
	}
	return waitForRun(ctx, tx, run)
}

func waitForRun(ctx context.Context, tx pgx.Tx, run runRow) (*CapacityWait, error) {
	host, err := agentpairing.HostCapacityForPrincipal(ctx, tx, run.AgentID)
	if err != nil {
		return nil, err
	}
	if host != nil && host.Reason != "" {
		return &CapacityWait{Code: "capacity", HostReason: host.Reason}, nil
	}
	accounts, err := listAccounts(ctx, tx)
	if err != nil {
		return nil, err
	}
	used, err := occupancy(ctx, tx)
	if err != nil {
		return nil, err
	}
	quotaUsed, err := quotaOccupancy(ctx, tx)
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
	same := []Account{}
	for _, a := range accounts {
		if a.RegisteredBy == run.AgentID && a.Harness == harness && (run.AccountID == nil || a.ID == *run.AccountID) {
			same = append(same, a)
		}
	}
	same, residencyEmptied, err := narrowCandidates(ctx, tx, run, harness, same)
	if err != nil {
		return nil, err
	}
	best := waitFor("offline")
	if residencyEmptied {
		best = waitFor("residency")
	}
	for _, a := range same {
		if run.RequestedAccountID != nil && a.ID != *run.RequestedAccountID || run.AccountID != nil && a.ID != *run.AccountID {
			continue
		}
		if a.AllowedProfileIDs != nil && !slices.Contains(a.AllowedProfileIDs, *run.ProfileID) {
			best = waitFor("models")
			continue
		}
		claiming := run.AccountID != nil
		slots := slotCount(a, used, quotaUsed)
		if claiming && slots > 0 {
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
	err := tx.QueryRow(ctx, `SELECT id::text,agent_principal_id::text,model_profile_id::text,account_id::text,COALESCE(requested_account_id,retry_account_id)::text,status,purpose,capacity_override FROM agent_runs WHERE id=$1`, id).Scan(&r.ID, &r.AgentID, &r.ProfileID, &r.AccountID, &r.RequestedAccountID, &r.Status, &r.Purpose, &r.CapacityOverride)
	return r, err
}
