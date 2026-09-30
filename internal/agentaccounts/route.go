// SPDX-License-Identifier: AGPL-3.0-only

package agentaccounts

import (
	"context"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/tenant"
)

type runRow struct {
	CapacityOverride      string
	Purpose               string
	VerificationAccountID *string
	VerificationExpiresAt *time.Time
	ID                    string
	AgentID               string
	ProfileID             *string
	AccountID             *string
	RequestedAccountID    *string
	Status                string
	DaemonID              *string
	Generation            *string
}

func lockRun(ctx context.Context, tx pgx.Tx, id string) (runRow, error) {
	if !uuidRE.MatchString(id) {
		return runRow{}, fail(http.StatusNotFound, "run not found")
	}
	var run runRow
	err := tx.QueryRow(ctx, `
		SELECT r.id::text, r.agent_principal_id::text, r.model_profile_id::text, r.account_id::text,
         r.status, r.daemon_id, r.daemon_generation, COALESCE(r.requested_account_id,r.retry_account_id)::text,
         r.purpose, e.account_id::text, e.verification_expires_at, r.capacity_override
  FROM agent_runs r LEFT JOIN agent_pairing_enrollments e
   ON e.tenant_id=r.tenant_id AND e.verification_run_id=r.id
  WHERE r.id = $1::uuid FOR UPDATE OF r`, id).
		Scan(&run.ID, &run.AgentID, &run.ProfileID, &run.AccountID, &run.Status, &run.DaemonID, &run.Generation, &run.RequestedAccountID, &run.Purpose, &run.VerificationAccountID, &run.VerificationExpiresAt, &run.CapacityOverride)
	if isNoRows(err) {
		return runRow{}, fail(http.StatusNotFound, "run not found")
	}
	return run, err
}

func reserve(ctx context.Context, tx pgx.Tx, r *http.Request, p tenant.Principal, runID, daemonID string, accountIDs []string, estimates map[string]int64) (RouteResult, error) {
	if err := agentpairing.Lock(ctx, tx); err != nil {
		return RouteResult{}, err
	}
	if err := validateEstimates(estimates); err != nil {
		return RouteResult{}, err
	}
	cleanDaemonID, err := cleanText(daemonID, 128)
	if err != nil || cleanDaemonID != daemonID || len(accountIDs) == 0 || len(accountIDs) > 256 {
		return RouteResult{}, fail(http.StatusBadRequest, "daemon and enrolled accounts are required")
	}
	enrolled := make(map[string]bool, len(accountIDs))
	for i, id := range accountIDs {
		if !uuidRE.MatchString(id) {
			return RouteResult{}, fail(http.StatusBadRequest, "invalid enrolled account")
		}
		id = strings.ToLower(id)
		accountIDs[i] = id
		enrolled[id] = true
	}
	if err := agentpairing.ExpireUnclaimedVerifications(ctx, tx); err != nil {
		return RouteResult{}, err
	}
	run, err := lockRun(ctx, tx, runID)
	if err != nil {
		return RouteResult{}, err
	}
	if err := authorizeRoute(ctx, tx, r, p, run.AgentID, run.ID); err != nil {
		return RouteResult{}, err
	}
	// A person's account choice narrows the daemon's enrolled set; it never
	// bypasses ownership, probe, capacity or allowance checks. No fallback.
	if run.RequestedAccountID != nil {
		if !enrolled[*run.RequestedAccountID] {
			return RouteResult{}, fail(http.StatusConflict, "requested account is not enrolled by this daemon")
		}
		accountIDs = []string{*run.RequestedAccountID}
		enrolled = map[string]bool{*run.RequestedAccountID: true}
	}
	if existing, ok, err := activeRoute(ctx, tx, run, p.ID, daemonID, enrolled); err != nil || ok {
		// A held reservation is not authority to launch after a grant, profile
		// or account becomes unavailable. Preserve replay for already owned runs.
		if err == nil {
			err = agentpairing.AccountFence(ctx, tx, existing.AccountID, run.Status != "queued")
		}
		if err == nil && run.Status == "queued" {
			err = agentpairing.RunFence(ctx, tx, existing.AccountID, run.ID, false)
			if err == nil {
				err = validateReservedAccount(ctx, tx, run, existing.AccountID)
			}
		}
		return existing, err
	}
	if run.Status != "queued" || (run.AccountID != nil && *run.AccountID != "") {
		return RouteResult{}, fail(http.StatusConflict, "run is not awaiting an account")
	}
	if run.ProfileID == nil || *run.ProfileID == "" {
		return RouteResult{}, fail(http.StatusConflict, "run has no model profile")
	}
	var harness string
	err = tx.QueryRow(ctx, `SELECT harness FROM model_profiles WHERE id = $1::uuid AND enabled`, *run.ProfileID).Scan(&harness)
	if isNoRows(err) {
		return RouteResult{}, fail(http.StatusConflict, "run has no model profile")
	}
	if err != nil {
		return RouteResult{}, err
	}
	now, err := dbNow(ctx, tx)
	if err != nil {
		return RouteResult{}, err
	}
	account, windows, err := selectAccount(ctx, tx, run, p.ID, harness, *run.ProfileID, daemonID, accountIDs, estimates, now)
	if err != nil {
		return RouteResult{}, err
	}
	if err = agentpairing.RunFence(ctx, tx, account.ID, run.ID, false); err != nil {
		return RouteResult{}, err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE agent_runs SET account_id = $2::uuid
		WHERE id = $1::uuid AND account_id IS NULL AND status = 'queued'`, run.ID, account.ID)
	if err != nil {
		return RouteResult{}, err
	}
	if tag.RowsAffected() != 1 {
		return RouteResult{}, fail(http.StatusConflict, "run is not awaiting an account")
	}
	result := RouteResult{AccountID: account.ID, AccountKey: account.AccountKey, AccountLabel: account.Label, DaemonID: account.DaemonID, Reservations: []Reservation{}}
	sort.Slice(windows, func(i, j int) bool {
		if windows[i].Unit != windows[j].Unit {
			return windows[i].Unit < windows[j].Unit
		}
		return windows[i].ID < windows[j].ID
	})
	for _, window := range windows {
		estimate := windowEstimate(window, estimates)
		tag, err := tx.Exec(ctx, `
			UPDATE account_allowance_windows
			SET reserved = reserved + $2, capacity_refresh_run=CASE WHEN capacity_kind IN ('refresh','blind') OR (capacity_source<>'estimate' AND capacity_read_at < $4::timestamptz-interval '10 minutes') THEN $3::uuid ELSE capacity_refresh_run END
			WHERE id = $1::uuid AND used + reserved + $2 <= allowance`, window.ID, estimate, run.ID, now)
		if err != nil {
			return RouteResult{}, err
		}
		if tag.RowsAffected() != 1 {
			return RouteResult{}, fail(http.StatusConflict, "allowance exceeded")
		}
		var id string
		if err := tx.QueryRow(ctx, `
			INSERT INTO account_reservations (tenant_id, run_id, window_id, reserved_units)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4)
			RETURNING id::text`, p.TenantID, run.ID, window.ID, estimate).Scan(&id); err != nil {
			return RouteResult{}, err
		}
		result.Reservations = append(result.Reservations, Reservation{ReservationID: id, WindowID: window.ID, Unit: window.Unit})
	}
	if err := writeEvent(ctx, tx, p, evReserved, nil, result); err != nil {
		return RouteResult{}, err
	}
	return result, nil
}

// ValidateReservedCapacity rechecks the router's capacity policy at launch.
// The caller must authenticate ownership and hold the run/account locks first.
func ValidateReservedCapacity(ctx context.Context, tx pgx.Tx, runID, accountID string) error {
	run, err := lockRun(ctx, tx, runID)
	if err != nil {
		return err
	}
	return validateReservedAccount(ctx, tx, run, accountID)
}

func validateReservedAccount(ctx context.Context, tx pgx.Tx, run runRow, accountID string) error {
	a, err := lockAccount(ctx, tx, accountID)
	if err != nil {
		return err
	}
	now, err := dbNow(ctx, tx)
	if err != nil {
		return err
	}
	if run.ProfileID == nil || a.State != "available" || !probeFresh(a, now) ||
		(a.AllowedProfileIDs != nil && !slices.Contains(a.AllowedProfileIDs, *run.ProfileID)) {
		return fail(http.StatusConflict, "reserved account is not eligible")
	}
	all, err := lockAccountWindows(ctx, tx, []string{accountID})
	if err != nil {
		return err
	}
	if run.Purpose != "pairing_verification" {
		used, err := occupancy(ctx, tx)
		if err != nil {
			return err
		}
		_, wait, err := admission(ctx, tx, a, all[accountID], now, used[accountID]-1, run, true)
		if err != nil {
			return err
		}
		if wait != nil {
			return fail(http.StatusConflict, "reserved capacity is not eligible: "+wait.Code)
		}
	} else if len(activeWindows(all[accountID], now)) == 0 {
		return fail(http.StatusConflict, "reserved capacity is not eligible")
	}
	var invalidCapacity bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM account_reservations r JOIN account_allowance_windows w ON w.tenant_id=r.tenant_id AND w.id=r.window_id WHERE r.run_id=$1 AND r.state='active' AND w.capacity_read_at IS NOT NULL AND (NOT w.capacity_allowed OR w.capacity_retired OR (w.capacity_source<>'estimate' AND w.capacity_read_at<$2::timestamptz-interval '10 minutes' AND w.capacity_refresh_run IS DISTINCT FROM r.run_id) OR w.ends_at<=$2 OR w.used+w.reserved>w.allowance))`, run.ID, now).Scan(&invalidCapacity); err != nil {
		return err
	}
	if invalidCapacity {
		return fail(http.StatusConflict, "reserved capacity is not eligible")
	}
	var enabled bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM model_profiles WHERE id=$1::uuid AND harness=$2 AND enabled)`, *run.ProfileID, a.Harness).Scan(&enabled); err != nil {
		return err
	}
	if !enabled {
		return fail(http.StatusConflict, "reserved model profile is not eligible")
	}
	return nil
}

func validateEstimates(estimates map[string]int64) error {
	if len(estimates) == 0 {
		return fail(http.StatusBadRequest, "estimates are required")
	}
	for unit, estimate := range estimates {
		if !validUnit(unit) {
			return fail(http.StatusBadRequest, "unknown allowance unit")
		}
		if estimate < 1 {
			return fail(http.StatusBadRequest, "estimate must be positive")
		}
	}
	return nil
}

func authorizeRoute(ctx context.Context, tx pgx.Tx, r *http.Request, p tenant.Principal, runAgent, runID string) error {
	if err := requireAgent(p); err != nil {
		return err
	}
	if p.ID == runAgent {
		return nil
	}
	if err := requireScope(ctx, tx, r, p, "run.claim"); err != nil {
		return err
	}
	ok, err := hasClaimGrant(ctx, tx, p.ID, runID)
	if err != nil {
		return err
	}
	if !ok {
		return fail(http.StatusForbidden, "run is not assigned to this agent")
	}
	return nil
}

func activeRoute(ctx context.Context, tx pgx.Tx, run runRow, principalID, daemonID string, enrolled map[string]bool) (RouteResult, bool, error) {
	rows, err := tx.Query(ctx, `
		SELECT r.id::text, r.window_id::text, w.unit, a.id::text, a.account_key, a.daemon_id,
		       a.registered_by_principal_id::text, a.label, w.pairing_verification, w.ends_at, w.allowance
		FROM account_reservations r
		JOIN account_allowance_windows w ON w.tenant_id = r.tenant_id AND w.id = r.window_id
		JOIN agent_accounts a ON a.tenant_id = w.tenant_id AND a.id = w.account_id
		WHERE r.run_id = $1::uuid AND r.state = 'active'
		ORDER BY w.unit, r.id`, run.ID)
	if err != nil {
		return RouteResult{}, false, err
	}
	defer rows.Close()
	var result RouteResult
	for rows.Next() {
		var item Reservation
		var accountID, key, ownerDaemonID, ownerID, label string
		var window Window
		if err := rows.Scan(&item.ReservationID, &item.WindowID, &item.Unit, &accountID, &key, &ownerDaemonID, &ownerID, &label, &window.pairingVerification, &window.EndsAt, &window.Allowance); err != nil {
			return RouteResult{}, false, err
		}
		if ownerDaemonID != daemonID || ownerID != principalID || !enrolled[accountID] {
			return RouteResult{}, false, fail(http.StatusConflict, "run is routed outside daemon enrollment")
		}
		window.AccountID, window.Unit = accountID, item.Unit
		// Never replay a queued reservation against a different budget family.
		// Already-claimed work retains its original ledger for truthful settlement.
		if run.Status == "queued" && !windowForRun(window, run) {
			return RouteResult{}, false, fail(http.StatusConflict, "reservation window does not match run purpose")
		}
		if result.AccountID == "" {
			result.AccountID = accountID
			result.AccountKey = key
			result.AccountLabel = label
			result.DaemonID = ownerDaemonID
		} else if result.AccountID != accountID {
			return RouteResult{}, false, fail(http.StatusConflict, "run reservations span accounts")
		}
		result.Reservations = append(result.Reservations, item)
	}
	if err := rows.Err(); err != nil {
		return RouteResult{}, false, err
	}
	if len(result.Reservations) == 0 {
		return RouteResult{}, false, nil
	}
	if run.Status == "queued" && run.Purpose == "pairing_verification" && len(result.Reservations) != 1 {
		return RouteResult{}, false, fail(http.StatusConflict, "verification requires its sole pairing reservation")
	}
	return result, true, nil
}

type ranked struct {
	presence bool
	account  Account
	windows  []Window
	cap      float64
	reset    *time.Time
	slots    int
}

func selectAccount(ctx context.Context, tx pgx.Tx, run runRow, principalID, harness, profileID, daemonID string, accountIDs []string, estimates map[string]int64, now time.Time) (Account, []Window, error) {
	rows, err := tx.Query(ctx, `
		SELECT id::text, account_key, harness, daemon_id, label, max_parallel_runs,
		       registered_by_principal_id::text, state, last_probe_at, last_probe_ok,
		       last_daemon_generation, created_at, plan, host_label, allowed_model_profile_ids::text[], reading_support, quota_fingerprint, statusline_enabled
		FROM agent_accounts
		WHERE harness = $1 AND daemon_id = $2 AND registered_by_principal_id = $3::uuid
		  AND id::text = ANY($4::text[]) AND state = 'available'
          AND (allowed_model_profile_ids IS NULL OR $5::uuid = ANY(allowed_model_profile_ids))
		ORDER BY id
		FOR UPDATE`, harness, daemonID, principalID, accountIDs, profileID)
	if err != nil {
		return Account{}, nil, err
	}
	defer rows.Close()
	var accounts []Account
	var ids []string
	for rows.Next() {
		account, err := scanAccount(rows)
		if err != nil {
			return Account{}, nil, err
		}
		accounts = append(accounts, account)
		ids = append(ids, account.ID)
	}
	if err := rows.Err(); err != nil {
		return Account{}, nil, err
	}
	if len(accounts) == 0 {
		return Account{}, nil, fail(http.StatusConflict, "no eligible account")
	}
	windows, err := lockAccountWindows(ctx, tx, ids)
	if err != nil {
		return Account{}, nil, err
	}
	usedSlots, err := occupancy(ctx, tx)
	if err != nil {
		return Account{}, nil, err
	}
	var picks []ranked
	for _, account := range accounts {
		if !probeFresh(account, now) || usedSlots[account.ID] >= account.MaxParallel {
			continue
		}
		var active []Window
		if run.Purpose == "pairing_verification" {
			for _, w := range activeWindows(windows[account.ID], now) {
				if windowForRun(w, run) {
					active = append(active, w)
				}
			}
		} else {
			var wait *CapacityWait
			active, wait, err = admission(ctx, tx, account, windows[account.ID], now, usedSlots[account.ID], run, false)
			if err != nil {
				return Account{}, nil, err
			}
			if wait != nil {
				continue
			}
		}
		// The one-shot grant has exactly one window, never a choice among budgets.
		if run.Purpose == "pairing_verification" && len(active) != 1 {
			continue
		}
		if len(active) == 0 {
			continue
		}
		ok := true
		for _, window := range active {
			estimate := windowEstimate(window, estimates)
			exists := estimate > 0
			if !exists {
				ok = false
				break
			}
			_, fitsWindow := fits(window, now, estimate)
			if !fitsWindow || window.Allowance < 1 {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		picks = append(picks, routeRank(account, active, usedSlots[account.ID], estimates, now))
	}
	if len(picks) == 0 {
		return Account{}, nil, fail(http.StatusConflict, "no eligible account")
	}
	orderPicks(picks)
	// Materialize a provisional grant only for the selected account; losing
	// candidates must not consume their single refresh opportunity.
	for i := range picks[0].windows {
		w := &picks[0].windows[i]
		if w.ID != "" {
			continue
		}
		if err := tx.QueryRow(ctx, `INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance,used,pace_model,burst_ratio,capacity_kind,capacity_read_at,capacity_allowed,capacity_source,capacity_bucket) SELECT tenant_id,id,$2,$3,'percent',1,0,'unrestricted',0,$5,$4,true,'estimate',$6 FROM agent_accounts WHERE id=$1 RETURNING id::text`, w.AccountID, w.StartsAt, w.EndsAt, w.capacityReadAt, w.capacityKind, w.capacityBucket).Scan(&w.ID); err != nil {
			return Account{}, nil, err
		}
	}
	return picks[0].account, picks[0].windows, nil
}

func lockAccountWindows(ctx context.Context, tx pgx.Tx, accountIDs []string) (map[string][]Window, error) {
	rows, err := tx.Query(ctx, `
		SELECT id::text, account_id::text, starts_at, ends_at, unit, allowance, used, reserved,
		       pace_model, burst_ratio::float8, pairing_verification, capacity_read_at, capacity_allowed, COALESCE(capacity_kind,''), capacity_bucket, capacity_retired, capacity_refresh_run::text, COALESCE(capacity_source,'')
		FROM account_allowance_windows
		WHERE account_id::text = ANY($1::text[])
		ORDER BY id
		FOR UPDATE`, accountIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]Window{}
	for rows.Next() {
		var w Window
		if err := rows.Scan(&w.ID, &w.AccountID, &w.StartsAt, &w.EndsAt, &w.Unit, &w.Allowance, &w.Used, &w.Reserved, &w.PaceModel, &w.BurstRatio, &w.pairingVerification, &w.capacityReadAt, &w.capacityAllowed, &w.capacityKind, &w.capacityBucket, &w.capacityRetired, &w.capacityRefreshRun, &w.capacitySource); err != nil {
			return nil, err
		}
		out[w.AccountID] = append(out[w.AccountID], w)
	}
	return out, rows.Err()
}

// A pairing account is an immutable per-enrollment identity. Its verification
// run and fixed expiry bind the sole generated one-request window; another
// enrollment/request window cannot substitute, even when it has spare budget.
func windowForRun(w Window, run runRow) bool {
	switch run.Purpose {
	case "managed":
		return !w.pairingVerification
	case "pairing_verification":
		return w.pairingVerification && run.VerificationAccountID != nil && run.VerificationExpiresAt != nil &&
			w.AccountID == *run.VerificationAccountID && w.EndsAt.Equal(*run.VerificationExpiresAt) && w.Unit == "requests" && w.Allowance == 1
	default:
		return false
	}
}

// A one-percent initial hold keeps reservations atomic until learned per-run
// percentages are available. It never invents token or dollar units.
func windowEstimate(w Window, estimates map[string]int64) int64 {
	if w.capacityReadAt != nil {
		return max(1, w.capacityHold)
	}
	return estimates[w.Unit]
}

// When all measured windows have reset, Claude needs one small run to emit its
// next snapshot. This is a provisional 1% estimate, never a renewed vendor limit.
// The account row is locked; one durable grant per last observation prevents
// retries, releases or its five-minute expiry from minting more refresh jobs.
func expiredCapacityRefresh(a Account, windows []Window, now time.Time) *Window {
	var latest time.Time
	for _, w := range windows {
		if w.capacityReadAt == nil || synthetic(w) {
			continue
		}
		if !w.capacityRetired && (w.EndsAt.After(now) || !w.capacityAllowed && now.Sub(*w.capacityReadAt) <= 10*time.Minute) {
			return nil
		}
		if w.capacityReadAt.After(latest) {
			latest = *w.capacityReadAt
		}
	}
	if latest.IsZero() {
		return nil
	}
	for _, w := range windows {
		if w.capacityKind == "refresh" && w.capacityReadAt != nil && !w.capacityReadAt.Before(latest) {
			return nil
		}
	}
	w := Window{AccountID: a.ID, StartsAt: now, EndsAt: now.Add(5 * time.Minute), Unit: "percent", Allowance: 1, PaceModel: "unrestricted", capacityReadAt: &latest, capacityAllowed: true, capacityKind: "refresh", Provisional: true}
	return &w
}
