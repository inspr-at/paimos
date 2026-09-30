// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

const (
	evLimitSet      = "account.limit_set"
	evLimitRemoved  = "account.limit_removed"
	evWindowRemoved = "account.window_removed"
)

// LimitRule is the Advanced sentence: agents use at most Amount Unit of the
// account per Period. An account has at most one; a replaced rule stays as
// history. It caps on top of readings and never replaces them.
type LimitRule struct {
	ID           string    `json:"id"`
	AccountID    string    `json:"account_id"`
	Amount       int64     `json:"amount"`
	Unit         string    `json:"unit"`
	Period       string    `json:"period"`
	FromWindowID *string   `json:"from_window_id,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// limitUse is a rule with what it counted this period, for the detail line.
type limitUse struct {
	LimitRule
	Used      float64   `json:"used"`
	PeriodEnd time.Time `json:"period_end"`
}

type limitWrite struct {
	Amount int64  `json:"amount"`
	Unit   string `json:"unit"`
	Period string `json:"period"`
}

func (in limitWrite) validate() error {
	switch in.Unit {
	case "percent", "runs", "requests", "tokens", "cost_micros":
	default:
		return fail(http.StatusBadRequest, "invalid limit")
	}
	switch in.Period {
	case "day", "week", "month":
	default:
		return fail(http.StatusBadRequest, "invalid limit")
	}
	if in.Amount < 1 || in.Amount > 1_000_000_000_000_000 || in.Unit == "percent" && in.Amount > 100 {
		return fail(http.StatusBadRequest, "invalid limit")
	}
	return nil
}

const limitColumns = `id::text, account_id::text, amount, unit, period, from_window_id::text, created_at`

func scanLimit(row scanner) (LimitRule, error) {
	var r LimitRule
	err := row.Scan(&r.ID, &r.AccountID, &r.Amount, &r.Unit, &r.Period, &r.FromWindowID, &r.CreatedAt)
	return r, err
}

func currentLimit(ctx context.Context, tx pgx.Tx, accountID string) (*LimitRule, error) {
	r, err := scanLimit(tx.QueryRow(ctx, `SELECT `+limitColumns+` FROM account_limit_rules WHERE account_id=$1 AND removed_at IS NULL`, accountID))
	if isNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// lockForLimit locks the account row; a missing account is 404. Limits are a
// person's settings, not dispatch, so a paused or draining account takes them.
func lockForLimit(ctx context.Context, tx pgx.Tx, accountID string) error {
	if !uuidRE.MatchString(accountID) {
		return fail(http.StatusNotFound, "account not found")
	}
	_, err := lockAccount(ctx, tx, accountID)
	return err
}

func setLimit(ctx context.Context, tx pgx.Tx, p tenant.Principal, accountID string, in limitWrite, fromWindow *string) (LimitRule, error) {
	if err := in.validate(); err != nil {
		return LimitRule{}, err
	}
	if err := lockForLimit(ctx, tx, accountID); err != nil {
		return LimitRule{}, err
	}
	if in.Unit == "cost_micros" {
		supported, err := costLimitSupported(ctx, tx, accountID)
		if err != nil {
			return LimitRule{}, err
		}
		if !supported {
			return LimitRule{}, &httpError{status: http.StatusUnprocessableEntity, code: "unsupported_limit_unit", msg: "Dollar limits require priced run usage. This account does not report costs; choose runs or another supported limit."}
		}
	}
	before, err := currentLimit(ctx, tx, accountID)
	if err != nil {
		return LimitRule{}, err
	}
	// An identical sentence is a no-op, so a retried Apply stores nothing twice.
	if before != nil && fromWindow == nil && before.Amount == in.Amount && before.Unit == in.Unit && before.Period == in.Period {
		return *before, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE account_limit_rules SET removed_at=clock_timestamp() WHERE account_id=$1 AND removed_at IS NULL`, accountID); err != nil {
		return LimitRule{}, err
	}
	after, err := scanLimit(tx.QueryRow(ctx, `INSERT INTO account_limit_rules(tenant_id,account_id,amount,unit,period,from_window_id,created_by)
 VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING `+limitColumns, p.TenantID, accountID, in.Amount, in.Unit, in.Period, fromWindow, p.ID))
	if err != nil {
		return LimitRule{}, err
	}
	if err := writeEvent(ctx, tx, p, evLimitSet, before, after); err != nil {
		return LimitRule{}, err
	}
	return after, nil
}

func removeLimit(ctx context.Context, tx pgx.Tx, p tenant.Principal, accountID string) error {
	if err := lockForLimit(ctx, tx, accountID); err != nil {
		return err
	}
	before, err := currentLimit(ctx, tx, accountID)
	if err != nil || before == nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE account_limit_rules SET removed_at=clock_timestamp() WHERE id=$1`, before.ID); err != nil {
		return err
	}
	return writeEvent(ctx, tx, p, evLimitRemoved, before, nil)
}

// manualWindow is a window set by hand that still caps: not read from the
// vendor, not a pairing verification, not removed.
func manualWindow(ctx context.Context, tx pgx.Tx, accountID, windowID string) (Window, error) {
	if !uuidRE.MatchString(windowID) {
		return Window{}, fail(http.StatusNotFound, "limit not found")
	}
	var w Window
	err := tx.QueryRow(ctx, `SELECT id::text, account_id::text, starts_at, ends_at, unit, allowance, used, reserved, pace_model, burst_ratio::float8
 FROM account_allowance_windows
 WHERE id=$1 AND account_id=$2 AND capacity_read_at IS NULL AND capacity_kind IS NULL AND NOT pairing_verification AND removed_at IS NULL
 FOR UPDATE`, windowID, accountID).Scan(&w.ID, &w.AccountID, &w.StartsAt, &w.EndsAt, &w.Unit, &w.Allowance, &w.Used, &w.Reserved, &w.PaceModel, &w.BurstRatio)
	if isNoRows(err) {
		return Window{}, fail(http.StatusNotFound, "limit not found")
	}
	w.SetByYou = true
	return w, err
}

// removeWindow stops a manual window from capping. Its row, usage and
// reservations stay, so settling a run that reserved against it still works.
func removeWindow(ctx context.Context, tx pgx.Tx, p tenant.Principal, accountID, windowID string) error {
	if err := lockForLimit(ctx, tx, accountID); err != nil {
		return err
	}
	w, err := manualWindow(ctx, tx, accountID, windowID)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE account_allowance_windows SET removed_at=clock_timestamp() WHERE id=$1`, w.ID); err != nil {
		return err
	}
	return writeEvent(ctx, tx, p, evWindowRemoved, w, nil)
}

// repeatWindow is Make this repeat: the window's amount per the period its
// length matches becomes the account's rule, and the window itself is removed
// in the same transaction, so the use counted this period stays counted.
func repeatWindow(ctx context.Context, tx pgx.Tx, p tenant.Principal, accountID, windowID string) (LimitRule, error) {
	if err := lockForLimit(ctx, tx, accountID); err != nil {
		return LimitRule{}, err
	}
	w, err := manualWindow(ctx, tx, accountID, windowID)
	if err != nil {
		return LimitRule{}, err
	}
	if w.Unit == "percent" {
		return LimitRule{}, fail(http.StatusConflict, "this limit cannot repeat")
	}
	before, err := currentLimit(ctx, tx, accountID)
	if err != nil {
		return LimitRule{}, err
	}
	if before != nil {
		return LimitRule{}, fail(http.StatusConflict, "This account already has a limit. Remove it explicitly before making this window repeat.")
	}
	rule, err := setLimit(ctx, tx, p, accountID, limitWrite{Amount: w.Allowance, Unit: w.Unit, Period: periodFor(w.EndsAt.Sub(w.StartsAt))}, &w.ID)
	if err != nil {
		return LimitRule{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE account_allowance_windows SET removed_at=clock_timestamp() WHERE id=$1`, w.ID); err != nil {
		return LimitRule{}, err
	}
	return rule, writeEvent(ctx, tx, p, evWindowRemoved, w, rule)
}

// periodFor names the period a window's length is closest to.
func periodFor(length time.Duration) string {
	switch {
	case length <= 36*time.Hour:
		return "day"
	case length <= 10*24*time.Hour:
		return "week"
	default:
		return "month"
	}
}

// limitPeriod is the local day, week (Monday first) or month around now.
func limitPeriod(period, timezone string, now time.Time) (time.Time, time.Time) {
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		loc = time.UTC
	}
	l := now.In(loc)
	day := time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, loc)
	switch period {
	case "week":
		start := day.AddDate(0, 0, -((int(l.Weekday()) + 6) % 7))
		return start, start.AddDate(0, 0, 7)
	case "month":
		start := time.Date(l.Year(), l.Month(), 1, 0, 0, 0, 0, loc)
		return start, start.AddDate(0, 1, 0)
	default:
		return day, day.AddDate(0, 0, 1)
	}
}

// costLimitSupported deliberately requires positive priced run telemetry. Token
// counters, a vendor name, or session-only list prices are not cost accounting
// for routed runs. Legacy zero-default deltas cannot prove a measured zero.
func costLimitSupported(ctx context.Context, tx pgx.Tx, accountID string) (bool, error) {
	var supported bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM run_telemetry t JOIN agent_runs r
 ON r.tenant_id=t.tenant_id AND r.id=t.run_id WHERE r.account_id=$1 AND t.cost_micros_delta>0)`, accountID).Scan(&supported)
	return supported, err
}

// learnedLimitEstimates uses the largest measured total among the last twenty
// finished runs. Until there is a sample, reserve one request, 100k tokens, or
// one dollar. A caller can increase a hold, but cannot lower learned/default use.
// Persist all units, even without a rule, so adding/repeating a cap counts runs
// that were already admitted. Account locking makes this snapshot atomic.
func learnedLimitEstimates(ctx context.Context, tx pgx.Tx, accountID string, requested map[string]int64) (map[string]int64, error) {
	out := map[string]int64{"runs": 1, "percent": 1}
	var requests, tokens, cost *int64
	err := tx.QueryRow(ctx, `WITH recent AS (
 SELECT id,tenant_id FROM agent_runs WHERE account_id=$1 AND purpose='managed'
 AND status IN ('completed','failed','cancelled','ownership_lost') AND started_at IS NOT NULL
 ORDER BY started_at DESC LIMIT 20), totals AS (
 SELECT sum(t.turn_count_delta)::bigint requests, sum(t.input_tokens_delta+t.output_tokens_delta)::bigint tokens,
 sum(t.cost_micros_delta)::bigint cost FROM recent r JOIN run_telemetry t ON t.tenant_id=r.tenant_id AND t.run_id=r.id GROUP BY r.id)
 SELECT max(requests) FILTER(WHERE requests>0),max(tokens) FILTER(WHERE tokens>0),max(cost) FILTER(WHERE cost>0) FROM totals`, accountID).Scan(&requests, &tokens, &cost)
	if err != nil {
		return nil, err
	}
	for _, v := range []struct {
		unit     string
		measured *int64
		fallback int64
	}{{"requests", requests, 1}, {"tokens", tokens, 100_000}, {"cost_micros", cost, 1_000_000}} {
		n := v.fallback
		if v.measured != nil {
			n = *v.measured
		}
		out[v.unit] = max(n, requested[v.unit])
	}
	return out, nil
}

// percentUsed sums positive deltas per vendor series, across reset epochs.
// Each epoch gets its last pre-period reading as a baseline. For an epoch that
// opened during the period, its first observed usage counts too. Corrections
// never refund usage. Different overlapping limits are not added together.
func percentUsed(ctx context.Context, tx pgx.Tx, accountID string, start, now time.Time) (float64, error) {
	var used float64
	err := tx.QueryRow(ctx, `WITH samples AS (
 SELECT DISTINCT ON (window_kind,bucket,resets_at,read_at) window_kind,bucket,resets_at,read_at,window_minutes,used_percent
 FROM account_capacity_readings WHERE account_id=$1 AND source<>'estimate' AND read_at<=$3
 ORDER BY window_kind,bucket,resets_at,read_at,CASE source WHEN 'harness' THEN 0 ELSE 1 END,used_percent DESC
 ), deltas AS (
 SELECT *,lag(used_percent) OVER(PARTITION BY window_kind,bucket,resets_at ORDER BY read_at) previous FROM samples
 ), totals AS (
 SELECT window_kind,bucket,sum(greatest(0,used_percent-COALESCE(previous,
 CASE WHEN resets_at-window_minutes*interval '1 minute'>=$2 THEN 0 ELSE used_percent END))) total
 FROM deltas WHERE read_at>=$2 GROUP BY window_kind,bucket)
 SELECT COALESCE(max(total),0)::float8 FROM totals`, accountID, start, now).Scan(&used)
	return used, err
}

// limitUsed includes measured usage and remaining holds. A live run consumes
// at least its saved estimate (never twice its reported usage). Missing usage
// after a started run finishes keeps its estimate for that calendar period.
// Unstarted cancelled/released runs consume nothing. Existing runs predating
// the snapshot column use the same conservative estimate as a new admission.
func limitUsed(ctx context.Context, tx pgx.Tx, accountID string, rule LimitRule, windows []Window, start time.Time, exceptRun string, now time.Time) (float64, error) {
	if rule.Unit == "percent" {
		measured, err := percentUsed(ctx, tx, accountID, start, now)
		if err != nil {
			return 0, err
		}
		var held float64
		err = tx.QueryRow(ctx, `SELECT COALESCE(sum(COALESCE((r.account_limit_estimates->>'percent')::bigint,1)),0)::float8
 FROM agent_runs r WHERE r.account_id=$1 AND r.id::text<>$2 AND r.purpose='managed' AND (
 r.status IN ('queued','starting','running','waiting') OR
 (r.started_at>=$3 AND r.started_at<=$4 AND EXISTS(SELECT 1 FROM account_reservations ar JOIN account_allowance_windows w
 ON w.tenant_id=ar.tenant_id AND w.id=ar.window_id WHERE ar.run_id=r.id AND w.capacity_kind IN ('refresh','blind'))
 AND NOT EXISTS(SELECT 1 FROM account_capacity_readings c WHERE c.account_id=r.account_id AND c.source<>'estimate' AND c.read_at>=COALESCE(r.ended_at,r.started_at) AND c.read_at<=$4)))`, accountID, exceptRun, start, now).Scan(&held)
		return measured + held, err
	}
	if rule.Unit == "runs" {
		var n float64
		err := tx.QueryRow(ctx, `SELECT count(*)::float8 FROM agent_runs WHERE account_id=$1 AND id::text<>$2 AND purpose='managed'
 AND ((started_at>=$3 AND started_at<=$4) OR status IN ('queued','starting','running','waiting'))`, accountID, exceptRun, start, now).Scan(&n)
		return n, err
	}
	estimates, err := learnedLimitEstimates(ctx, tx, accountID, nil)
	if err != nil {
		return 0, err
	}
	var n float64
	err = tx.QueryRow(ctx, `WITH usage AS (
 SELECT r.id,r.status,r.started_at,COALESCE((r.account_limit_estimates->>$2)::bigint,$6) estimate,
 COALESCE(sum(CASE $2 WHEN 'requests' THEN t.turn_count_delta WHEN 'tokens' THEN t.input_tokens_delta+t.output_tokens_delta ELSE t.cost_micros_delta END),0) total,
 COALESCE(sum(CASE $2 WHEN 'requests' THEN t.turn_count_delta WHEN 'tokens' THEN t.input_tokens_delta+t.output_tokens_delta ELSE t.cost_micros_delta END) FILTER(WHERE t.at>=$3),0) period_use
 FROM agent_runs r LEFT JOIN run_telemetry t ON t.tenant_id=r.tenant_id AND t.run_id=r.id AND t.at<=$4
 WHERE r.account_id=$1 AND r.id::text<>$5 AND r.purpose='managed' GROUP BY r.tenant_id,r.id
 ) SELECT COALESCE(sum(period_use+CASE
 WHEN status IN ('queued','starting','running','waiting') THEN greatest(0,estimate-total)
 WHEN started_at>=$3 AND started_at<=$4 AND total=0 THEN estimate ELSE 0 END),0)::float8 FROM usage`, accountID, rule.Unit, start, now, exceptRun, estimates[rule.Unit]).Scan(&n)
	return n, err
}

// limitWait runs under the account FOR UPDATE lock during reserve/claim, in
// the same transaction that stores the account and per-run estimates. Advisory
// projections use the same arithmetic without reserving anything.
func limitWait(ctx context.Context, tx pgx.Tx, a Account, active []Window, now time.Time, run runRow, claiming bool, s capacity.Schedule) (*CapacityWait, error) {
	rule, err := currentLimit(ctx, tx, a.ID)
	if err != nil || rule == nil {
		return nil, err
	}
	start, end := limitPeriod(rule.Period, s.Timezone, now)
	wait := &CapacityWait{Code: "allowance", Until: &end, Timezone: s.Timezone}
	if rule.Unit == "cost_micros" {
		supported, err := costLimitSupported(ctx, tx, a.ID)
		if err != nil {
			return nil, err
		}
		if !supported {
			return wait, nil
		}
	}
	// A repeat does not refund old usage or release the original paced cap.
	if rule.FromWindowID != nil {
		var w Window
		err := tx.QueryRow(ctx, `SELECT starts_at,ends_at,unit,allowance,used,reserved,pace_model,burst_ratio::float8
 FROM account_allowance_windows WHERE id=$1 AND account_id=$2`, *rule.FromWindowID, a.ID).Scan(&w.StartsAt, &w.EndsAt, &w.Unit, &w.Allowance, &w.Used, &w.Reserved, &w.PaceModel, &w.BurstRatio)
		if err != nil {
			return nil, err
		}
		if !now.Before(w.StartsAt) && now.Before(w.EndsAt) {
			estimate := int64(0)
			if !claiming {
				estimates, err := learnedLimitEstimates(ctx, tx, a.ID, run.LimitEstimates)
				if err != nil {
					return nil, err
				}
				estimate = estimates[w.Unit]
			}
			if _, ok := fits(w, now, estimate); !ok {
				return &CapacityWait{Code: "allowance", Until: &w.EndsAt, Timezone: s.Timezone}, nil
			}
		}
	}
	used, err := limitUsed(ctx, tx, a.ID, *rule, active, start, run.ID, now)
	if err != nil {
		return nil, err
	}
	estimates := run.LimitEstimates
	if len(estimates) == 0 {
		estimates, err = learnedLimitEstimates(ctx, tx, a.ID, nil)
		if err != nil {
			return nil, err
		}
	}
	need := float64(estimates[rule.Unit])
	if used+need > float64(rule.Amount) {
		return wait, nil
	}
	if rule.Unit == "percent" {
		// fits includes this window's held units; account-wide holds were already
		// counted above, so put those back into its local budget exactly once.
		for i := range active {
			w := &active[i]
			if w.capacityReadAt == nil {
				continue
			}
			left := float64(rule.Amount) - used + float64(w.Reserved)
			if claiming {
				left -= need
			}
			if w.capacityBudget == nil || *w.capacityBudget > left {
				w.capacityBudget = &left
			}
		}
	}
	return nil, nil
}

// accountLimitUse projects the rule for the detail: what it counted this
// period in the owner's timezone, the same one admission uses.
func accountLimitUse(ctx context.Context, tx pgx.Tx, a Account, now time.Time) (*limitUse, error) {
	rule, err := currentLimit(ctx, tx, a.ID)
	if err != nil || rule == nil {
		return nil, err
	}
	s, err := routingSchedule(ctx, tx, a)
	if err != nil {
		return nil, err
	}
	start, end := limitPeriod(rule.Period, s.Timezone, now)
	used, err := limitUsed(ctx, tx, a.ID, *rule, a.Windows, start, "", now)
	if err != nil {
		return nil, err
	}
	return &limitUse{LimitRule: *rule, Used: used, PeriodEnd: end}, nil
}

// monthSpend is the list-price spend this local month of an account billed by
// API key, in dollars with cents. Empty when no usage was ever billed by key.
func monthSpend(ctx context.Context, tx pgx.Tx, accountID, timezone string, now time.Time) (string, error) {
	start, _ := limitPeriod("month", timezone, now)
	var api *bool
	var spend string
	err := tx.QueryRow(ctx, `SELECT bool_or(billing_mode='api'), round(COALESCE(sum(estimated_cost_usd) FILTER (WHERE billing_mode='api' AND reported_at>=$2),0),2)::text
 FROM harness_session_usage WHERE account_id=$1`, accountID, start).Scan(&api, &spend)
	if err != nil || api == nil || !*api {
		return "", err
	}
	return spend, nil
}

func (m *Module) putLimit(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if err := m.requirePermission(r, p, "account.manage"); err != nil {
		writeErr(w, err)
		return
	}
	var in limitWrite
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, err)
		return
	}
	var out LimitRule
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		var err error
		out, err = setLimit(r.Context(), tx, p, r.PathValue("accountId"), in, nil)
		return err
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, out)
}

func (m *Module) deleteLimit(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if err := m.requirePermission(r, p, "account.manage"); err != nil {
		writeErr(w, err)
		return
	}
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		return removeLimit(r.Context(), tx, p, r.PathValue("accountId"))
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m *Module) removeWindow(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if err := m.requirePermission(r, p, "account.manage"); err != nil {
		writeErr(w, err)
		return
	}
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		return removeWindow(r.Context(), tx, p, r.PathValue("accountId"), r.PathValue("windowId"))
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m *Module) repeatWindow(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if err := m.requirePermission(r, p, "account.manage"); err != nil {
		writeErr(w, err)
		return
	}
	var out LimitRule
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		var err error
		out, err = repeatWindow(r.Context(), tx, p, r.PathValue("accountId"), r.PathValue("windowId"))
		return err
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, out)
}
