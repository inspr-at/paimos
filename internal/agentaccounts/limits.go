// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"math"
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

// windowRise is how many points a vendor window rose since start: from the
// last reading at or before start, or the first after it; all of it when the
// window opened inside the period.
func windowRise(ctx context.Context, tx pgx.Tx, accountID string, w Window, start time.Time) (float64, error) {
	if !w.StartsAt.Before(start) {
		return float64(w.Used), nil
	}
	var base float64
	err := tx.QueryRow(ctx, `SELECT used_percent::float8 FROM account_capacity_readings
 WHERE account_id=$1 AND window_kind=$2 AND bucket=$3 AND resets_at=$4 AND source<>'estimate'
 ORDER BY read_at<=$5 DESC, CASE WHEN read_at<=$5 THEN read_at END DESC, read_at LIMIT 1`,
		accountID, w.capacityKind, w.capacityBucket, w.EndsAt, start).Scan(&base)
	if isNoRows(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return math.Max(0, float64(w.Used)-base), nil
}

// limitUsed counts a rule in its own unit since start. Percent is the largest
// rise among the current vendor windows; runs exclude exceptRun.
func limitUsed(ctx context.Context, tx pgx.Tx, accountID string, rule LimitRule, windows []Window, start time.Time, exceptRun string, now time.Time) (float64, error) {
	switch rule.Unit {
	case "percent":
		most := 0.0
		for _, w := range windows {
			if w.capacityReadAt == nil || synthetic(w) || w.capacityRetired || !now.Before(w.EndsAt) {
				continue
			}
			rise, err := windowRise(ctx, tx, accountID, w, start)
			if err != nil {
				return 0, err
			}
			most = math.Max(most, rise)
		}
		return most, nil
	case "runs":
		var n int64
		err := tx.QueryRow(ctx, `SELECT count(*) FROM agent_runs WHERE account_id=$1 AND id::text<>$2 AND purpose='managed'
 AND (started_at>=$3 OR (started_at IS NULL AND status IN ('queued','starting')))`, accountID, exceptRun, start).Scan(&n)
		return float64(n), err
	default:
		var n int64
		err := tx.QueryRow(ctx, `SELECT COALESCE(sum(CASE $2 WHEN 'requests' THEN t.turn_count_delta WHEN 'tokens' THEN t.input_tokens_delta+t.output_tokens_delta ELSE t.cost_micros_delta END),0)::bigint
 FROM run_telemetry t JOIN agent_runs r ON r.tenant_id=t.tenant_id AND r.id=t.run_id
 WHERE r.account_id=$1 AND t.at>=$3`, accountID, rule.Unit, start).Scan(&n)
		return float64(n), err
	}
}

// limitWait applies the account's rule on top of the admitted windows. A
// percent rule also lowers each vendor window's budget to what the rule has
// left, so reservation agrees. A reached rule waits for the period's end; Run
// now does not skip a limit a person set.
func limitWait(ctx context.Context, tx pgx.Tx, a Account, active []Window, now time.Time, run runRow, claiming bool, s capacity.Schedule) (*CapacityWait, error) {
	rule, err := currentLimit(ctx, tx, a.ID)
	if err != nil || rule == nil {
		return nil, err
	}
	start, end := limitPeriod(rule.Period, s.Timezone, now)
	wait := &CapacityWait{Code: "allowance", Until: &end, Timezone: s.Timezone}
	if rule.Unit != "percent" {
		used, err := limitUsed(ctx, tx, a.ID, *rule, nil, start, run.ID, now)
		if err != nil {
			return nil, err
		}
		if used >= float64(rule.Amount) {
			return wait, nil
		}
		return nil, nil
	}
	need := float64(boolInt(!claiming))
	for i := range active {
		w := &active[i]
		if w.capacityReadAt == nil || synthetic(*w) {
			continue
		}
		rise, err := windowRise(ctx, tx, a.ID, *w, start)
		if err != nil {
			return nil, err
		}
		left := float64(rule.Amount) - rise
		if left < float64(w.Reserved)+need {
			return wait, nil
		}
		if w.capacityBudget == nil || *w.capacityBudget > left {
			w.capacityBudget = &left
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
