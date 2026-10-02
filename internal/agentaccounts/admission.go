// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/jackc/pgx/v5"
)

// vendorStopBackoff bounds a vendor stop that names no reset. It is a retry
// limit, not a guessed vendor window.
const vendorStopBackoff = time.Hour

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
func blindDayPolicy(_ capacity.Schedule, _ time.Time) bool { return false }

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
	if len(permits) > 0 && slots > 0 {
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
		if w.pairingVerification || w.capacityReadAt != nil && (w.capacitySource == "estimate" || now.Sub(*w.capacityReadAt) > 10*time.Minute) {
			continue
		}
		regular = append(regular, w)
	}
	active := activeWindows(regular, now)
	if len(active) == 0 {
		for _, w := range regular {
			if w.capacityReadAt == nil && w.StartsAt.After(now) {
				return nil, &CapacityWait{Code: "allowance", Until: &w.StartsAt, Timezone: s.Timezone}, nil
			}
		}
	}
	measured := false
	for _, w := range active {
		measured = measured || w.capacityReadAt != nil
	}
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
	if !measured && s.ActiveOverride(now) != "sprint" && s.ActiveOverride(now) != "away" {
		next := s.NextStart(now, s.OffDays == "normal")
		if next == nil || next.After(now) {
			return nil, &CapacityWait{Code: "schedule", Until: next, Timezone: s.Timezone, RunNowAllowed: true}, nil
		}
	}
	if err := applyCapacityPacing(ctx, tx, a, active, now, s); err != nil {
		return nil, nil, err
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
	// Re-sort after attaching learning so the advice and reservation agree.
	for i := range ordered {
		for _, w := range active {
			if w.ID == ordered[i].ID {
				ordered[i] = w
				break
			}
		}
	}
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

type vendorBlock struct {
	until  *time.Time
	readAt *time.Time
	epoch  time.Time
}

func (b vendorBlock) waiting(now time.Time) bool {
	return b.until != nil && b.until.After(now)
}

func (b vendorBlock) refreshDue(now time.Time) bool {
	return !b.epoch.IsZero() && !b.waiting(now)
}

type denial struct {
	until time.Time
	at    time.Time
}

// effectiveDenial is the sole precedence rule for unresolved vendor stops.
// Only future deadlines block admission; the latest wins regardless of source
// or arrival order. Expired stops retain only their latest epoch, so recovery
// is eligible after every wait ends and keeps its once-per-generation fence.
func effectiveDenial(now time.Time, named, unnamed []denial) vendorBlock {
	var block vendorBlock
	for _, group := range [][]denial{named, unnamed} {
		for _, d := range group {
			if d.at.After(block.epoch) {
				block.epoch = d.at
			}
			if d.until.After(now) && (block.until == nil || d.until.After(*block.until) || d.until.Equal(*block.until) && d.at.After(*block.readAt)) {
				block.until, block.readAt = timePtr(d.until), timePtr(d.at)
			}
		}
	}
	if block.readAt == nil && !block.epoch.IsZero() {
		block.readAt = timePtr(block.epoch)
	}
	return block
}

func loadVendorBlock(ctx context.Context, tx pgx.Tx, a Account, now time.Time) (vendorBlock, error) {
	named, err := readingDenials(ctx, tx, a.ID)
	if err != nil {
		return vendorBlock{}, err
	}
	unnamed, err := blindDenials(ctx, tx, a.ID)
	if err != nil {
		return vendorBlock{}, err
	}
	// Clear each denial on its own epoch before choosing. A recovery that
	// finishes a named denial must not hide a later stop that names no window:
	// that stop keeps the one-hour backoff, including across a daemon restart.
	named, err = dropClearedDenials(ctx, tx, a.ID, named)
	if err != nil {
		return vendorBlock{}, err
	}
	unnamed, err = dropClearedDenials(ctx, tx, a.ID, unnamed)
	if err != nil {
		return vendorBlock{}, err
	}
	return effectiveDenial(now, named, unnamed), nil
}

// Clear each bucket separately: clearing the one with the latest reset must
// not hide a newer denial in another bucket.
func dropClearedDenials(ctx context.Context, tx pgx.Tx, accountID string, denials []denial) ([]denial, error) {
	remaining := denials[:0]
	for _, d := range denials {
		cleared, err := denialClearedByRun(ctx, tx, accountID, d.at)
		if err != nil {
			return nil, err
		}
		if !cleared {
			remaining = append(remaining, d)
		}
	}
	return remaining, nil
}

// readingDenials keeps every denying bucket until clearing and precedence
// have been evaluated. An allowance on one bucket does not clear another.
func readingDenials(ctx context.Context, tx pgx.Tx, accountID string) ([]denial, error) {
	rows, err := tx.Query(ctx, `SELECT resets_at, read_at FROM (
 SELECT DISTINCT ON (window_kind, bucket) window_kind, bucket, resets_at, read_at, ordinary_usage_allowed
 FROM account_capacity_readings
 WHERE account_id IN (`+quotaAccounts+`) AND source<>'estimate' AND ordinary_usage_allowed IS NOT NULL
 ORDER BY window_kind, bucket, read_at DESC, CASE source WHEN 'harness' THEN 0 ELSE 1 END, ordinary_usage_allowed ASC
) latest
 WHERE ordinary_usage_allowed=false
 ORDER BY resets_at DESC, read_at DESC, window_kind, bucket`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var denials []denial
	for rows.Next() {
		var d denial
		if err := rows.Scan(&d.until, &d.at); err != nil {
			return nil, err
		}
		d.until, d.at = d.until.UTC(), d.at.UTC()
		denials = append(denials, d)
	}
	return denials, rows.Err()
}

func blindDenials(ctx context.Context, tx pgx.Tx, accountID string) ([]denial, error) {
	var at time.Time
	var reset *time.Time
	// Only a reading tied to the stopped run can identify its reset. A prior
	// run's named denial must not shorten a newer unnamed stop's backoff.
	err := tx.QueryRow(ctx, `SELECT t.at,
 (SELECT max(r.resets_at) FROM account_capacity_readings r
  WHERE r.account_id IN (`+quotaAccounts+`) AND r.run_id=t.run_id AND r.source<>'estimate'
  AND (r.ordinary_usage_allowed=false OR r.used_percent>=100) AND r.resets_at>t.at)
 FROM run_telemetry t JOIN agent_runs ar ON ar.tenant_id=t.tenant_id AND ar.id=t.run_id
 WHERE ar.account_id IN (`+quotaAccounts+`) AND t.error_code='vendor_limit' AND NOT EXISTS(
  SELECT 1 FROM account_capacity_readings r WHERE r.account_id IN (`+quotaAccounts+`) AND r.source<>'estimate'
  AND r.ordinary_usage_allowed=true AND r.read_at>t.at)
 ORDER BY t.at DESC LIMIT 1`, accountID).Scan(&at, &reset)
	if isNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	stop := at.UTC()
	until := stop.Add(vendorStopBackoff)
	if reset != nil {
		until = reset.UTC()
	}
	return []denial{{until: until, at: stop}}, nil
}

// denialClearedByRun is a run that started after the denial and finished
// without a vendor stop. The run that produced the denial started earlier.
func denialClearedByRun(ctx context.Context, tx pgx.Tx, accountID string, epoch time.Time) (bool, error) {
	var cleared bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(
 SELECT 1 FROM agent_runs ar WHERE ar.account_id IN (`+quotaAccounts+`) AND ar.status='completed' AND ar.started_at > $2
 AND NOT EXISTS(SELECT 1 FROM run_telemetry t WHERE t.tenant_id=ar.tenant_id AND t.run_id=ar.id AND t.error_code='vendor_limit'))`, accountID, epoch).Scan(&cleared)
	return cleared, err
}

func recoveryBucket(a Account, epoch time.Time) string {
	gen := ""
	if a.daemonGeneration != nil {
		gen = *a.daemonGeneration
	}
	return "recover:" + gen + ":" + epoch.UTC().Format(time.RFC3339Nano)
}

// blindAfterRecovery is the wait once a blind recovery grant is spent.
// Off hours and holds keep their own reason. During the work band the next
// real run is still waiting, which is not a capacity reading.
func blindAfterRecovery(s capacity.Schedule, now time.Time) *CapacityWait {
	if s.ActiveOverride(now) == "hold" {
		return &CapacityWait{Code: "hold", Until: s.OverrideUntil, Timezone: s.Timezone}
	}
	if !blindDayPolicy(s, now) {
		next := s.NextStart(now.Add(time.Second), false)
		return &CapacityWait{Code: "schedule", Until: next, Timezone: s.Timezone}
	}
	return &CapacityWait{Code: "reading", Timezone: s.Timezone}
}

// allowedOpenWindows is the measured windows a named denial must not hide
// once that denial's own reset has passed. A fresh denial still fences manual
// budgets through activeWindows; these peers are a different bucket.
func allowedOpenWindows(windows []Window, now time.Time) []Window {
	latest := map[string]Window{}
	for _, w := range windows {
		if w.capacityReadAt == nil || w.capacityRetired {
			continue
		}
		key := w.capacityKind + "/" + w.capacityBucket
		old, exists := latest[key]
		if !exists || w.capacityReadAt.After(*old.capacityReadAt) {
			latest[key] = w
		}
	}
	keys := make([]string, 0, len(latest))
	for key := range latest {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	out := []Window{}
	for _, key := range keys {
		w := latest[key]
		if w.capacityAllowed && !now.Before(w.StartsAt) && now.Before(w.EndsAt) {
			out = append(out, w)
		}
	}
	return out
}

// recoveryClearedAt is the latest completed recover: run that finished without
// a vendor stop and without a later measured reading. An ordinary run, or a
// run that already produced a reading, is not another first reading.
func recoveryClearedAt(ctx context.Context, tx pgx.Tx, accountID string) (time.Time, bool, error) {
	var at time.Time
	err := tx.QueryRow(ctx, `SELECT ar.started_at FROM agent_runs ar
 WHERE ar.account_id=$1 AND ar.status='completed' AND ar.started_at IS NOT NULL
 AND NOT EXISTS(SELECT 1 FROM run_telemetry t WHERE t.tenant_id=ar.tenant_id AND t.run_id=ar.id AND t.error_code='vendor_limit')
 AND EXISTS(SELECT 1 FROM account_reservations r
  JOIN account_allowance_windows w ON w.tenant_id=r.tenant_id AND w.id=r.window_id
  WHERE r.run_id=ar.id AND w.account_id=$1 AND w.capacity_kind='refresh' AND w.capacity_bucket LIKE 'recover:%')
 AND EXISTS(SELECT 1 FROM account_capacity_readings rd WHERE rd.account_id=$1 AND rd.source<>'estimate' AND rd.read_at < ar.started_at)
 AND NOT EXISTS(SELECT 1 FROM account_capacity_readings rd WHERE rd.account_id=$1 AND rd.source<>'estimate' AND rd.read_at >= ar.started_at)
 ORDER BY ar.started_at DESC LIMIT 1`, accountID).Scan(&at)
	if isNoRows(err) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	return at.UTC(), true, nil
}

func containsRecovery(windows []Window) bool {
	for _, w := range windows {
		if synthetic(w) && strings.HasPrefix(w.capacityBucket, "recover:") {
			return true
		}
	}
	return false
}

func recoveryGrant(ctx context.Context, tx pgx.Tx, a Account, s capacity.Schedule, block vendorBlock, now time.Time, slots int) ([]Window, *CapacityWait, error) {
	reading := &CapacityWait{Code: "reading", ReadAt: block.readAt, Timezone: s.Timezone}
	if a.daemonGeneration == nil || *a.daemonGeneration == "" {
		return nil, reading, nil
	}
	if slots > 0 {
		return nil, waitFor("reading"), nil
	}
	bucket := recoveryBucket(a, block.epoch)
	var granted bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM account_allowance_windows WHERE account_id IN (`+quotaAccounts+`) AND capacity_kind='refresh' AND capacity_bucket=$2)`, a.ID, bucket).Scan(&granted); err != nil {
		return nil, nil, err
	}
	if granted {
		if a.Harness == "grok" || a.Harness == "cursor" || a.Harness == "pi" {
			return nil, blindAfterRecovery(s, now), nil
		}
		return nil, reading, nil
	}
	w := provisionalWindow(a.ID, now, "refresh")
	w.capacityBucket = bucket
	return []Window{w}, nil, nil
}

// countBlindDayRuns counts today's blind attempts that started inside the
// owner's work bands. A night or off-day run does not consume the three.
func countBlindDayRuns(ctx context.Context, tx pgx.Tx, accountID, exceptRun string, s capacity.Schedule, now time.Time) (int, error) {
	loc, err := time.LoadLocation(s.Timezone)
	if err != nil {
		loc = time.UTC
	}
	local := now.In(loc)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	next := time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, loc)
	rows, err := tx.Query(ctx, `SELECT DISTINCT w.starts_at
 FROM account_reservations r JOIN account_allowance_windows w ON w.tenant_id=r.tenant_id AND w.id=r.window_id
 WHERE w.account_id IN (`+quotaAccounts+`) AND w.capacity_kind='blind' AND w.starts_at >= $2 AND w.starts_at < $3 AND r.run_id::text <> $4`, accountID, day, next, exceptRun)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var start time.Time
		if err := rows.Scan(&start); err != nil {
			return 0, err
		}
		if blindDayPolicy(s, start) {
			n++
		}
	}
	return n, rows.Err()
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
