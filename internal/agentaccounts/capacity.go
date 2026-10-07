// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/openrouter"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type readingsWrite struct {
	Readings []capacity.Reading `json:"readings"`
}

type usageReadingsWrite struct {
	readingsWrite
	Budget     *capacity.Budget `json:"budget"`
	UsageProbe bool             `json:"usage_probe"`
	Revision   *int64           `json:"binding_revision"`
}

func (m *Module) ingestReadings(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if err := requireAgent(p); err != nil {
		writeErr(w, err)
		return
	}
	var in usageReadingsWrite
	if err := decodeJSON(w, r, &in); err != nil {
		writeErr(w, err)
		return
	}
	err := m.inReadinessWrite(r.Context(), p, func(tx pgx.Tx) error {
		if err := requireScope(r.Context(), tx, r, p, "account.probe"); err != nil {
			return err
		}
		id := r.PathValue("accountId")
		if !uuidRE.MatchString(id) {
			return fail(404, "account not found")
		}
		if len(in.Readings) > 32 || len(in.Readings) == 0 && in.Budget == nil {
			return fail(400, "readings or budget required")
		}
		if in.UsageProbe || in.Budget != nil {
			reader := p
			var err error
			reader.Scopes, err = readKeyScopes(r.Context(), tx, r, p, true)
			if err != nil {
				return err
			}
			if authz.RequireTx(r.Context(), tx, reader, "account.probe", authz.Scope{}) != nil {
				return fail(403, "account probe permission required")
			}
			if !in.UsageProbe {
				return fail(400, "budget requires a usage probe")
			}
			if err := agentpairing.AccountFence(r.Context(), tx, id, true); err != nil {
				return err
			}
			a, err := usageConsent(r.Context(), tx, id, in.Revision)
			if err != nil {
				return err
			}
			if a.RegisteredBy != p.ID {
				return fail(403, "registering agent required")
			}
			now, err := dbNow(r.Context(), tx)
			if err != nil {
				return err
			}
			for _, v := range in.Readings {
				if v.Source != "agentd" || v.RunID != "" || v.Phase != "" {
					return fail(400, "invalid usage probe reading")
				}
			}
			if in.Budget != nil {
				if a.Harness != "pi" || a.Provider != "openrouter" || in.Budget.Validate(now) != nil {
					return fail(400, "invalid capacity budget")
				}
				if err := ingestUsageBudget(tenant.WithPrincipal(r.Context(), p), tx, a, *in.Budget, now); err != nil {
					return err
				}
			}
		}
		if len(in.Readings) > 0 {
			if err := ingestReadings(r.Context(), tx, p, id, in.Readings); err != nil {
				return err
			}
		}
		a, err := getAccount(r.Context(), tx, r.PathValue("accountId"))
		if err != nil {
			return err
		}
		now, err := dbNow(r.Context(), tx)
		if err != nil {
			return err
		}
		notices, err := prepareQuotaWarnings(r.Context(), tx, p, a, now)
		if err != nil {
			return err
		}
		system, err := quotaSystemActor(r.Context(), tx, p.TenantID, len(notices) > 0)
		if err != nil {
			return err
		}
		return flushQuotaNotices(r.Context(), tx, system, notices)
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// The dollar figure and the key-cap admission fact share one observation.
// A probe that is not strictly newer than the credit snapshot changes neither.
// The snapshot is never copied into the dollar column.
func ingestUsageBudget(ctx context.Context, tx pgx.Tx, a Account, b capacity.Budget, now time.Time) error {
	if a.OpenRouterCredits != nil && !b.ReadAt.After(a.OpenRouterCredits.ObservedAt) {
		return nil
	}
	tag, err := tx.Exec(ctx, `UPDATE agent_accounts SET usage_budget=$2 WHERE id=$1 AND (usage_budget IS NULL OR (usage_budget->>'read_at')::timestamptz<$3)`, a.ID, b, b.ReadAt)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	a.OpenRouterCredits = &openrouter.Credits{ObservedAt: b.ReadAt, Usage: &b.KeyUsageUSD, Limit: b.KeyLimitUSD, Remaining: b.KeyRemainingUSD}
	a.OpenRouterCredits.Remaining = a.OpenRouterCredits.KeyRemaining()
	if err := storeLegacyKeyFact(ctx, tx, a, now); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE agent_accounts SET openrouter_credits=$2 WHERE id=$1`, a.ID, a.OpenRouterCredits)
	return err
}

func ingestReadings(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string, readings []capacity.Reading) error {
	if !uuidRE.MatchString(id) {
		return fail(404, "account not found")
	}
	if len(readings) < 1 || len(readings) > 32 {
		return fail(400, "one to 32 readings required")
	}
	if err := agentpairing.AccountFence(ctx, tx, id, true); err != nil {
		return err
	}
	a, err := lockAccount(ctx, tx, id)
	if err != nil {
		return err
	}
	if a.RegisteredBy != p.ID {
		return fail(403, "only the registering agent can report capacity")
	}
	now, err := dbNow(ctx, tx)
	if err != nil {
		return err
	}
	snapshotAt := map[string]time.Time{}
	for _, v := range readings {
		if v.ReadAt.After(snapshotAt[v.Source]) {
			snapshotAt[v.Source] = v.ReadAt
		}
	}
	for _, v := range readings {
		if err := v.Validate(now); err != nil {
			return fail(400, err.Error())
		}
		if looksLikeCredential(v.Plan) || looksLikeCredential(v.Bucket) {
			return fail(400, "invalid display metadata")
		}
		var run any
		if v.RunID != "" {
			if !uuidRE.MatchString(v.RunID) {
				return fail(400, "invalid run id")
			}
			var own bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runs WHERE id=$1 AND account_id=$2)`, v.RunID, id).Scan(&own); err != nil {
				return err
			}
			if !own {
				return fail(403, "reading run does not use this account")
			}
			run = v.RunID
		}
		tag, err := tx.Exec(ctx, `INSERT INTO account_capacity_readings(tenant_id,account_id,window_kind,bucket,window_minutes,used_percent,resets_at,read_at,source,plan,ordinary_usage_allowed,run_id,phase,plus_minus,evidence) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15) ON CONFLICT DO NOTHING`, p.TenantID, id, v.WindowKind, v.Bucket, v.WindowMinutes, v.UsedPercent, v.ResetsAt, v.ReadAt, v.Source, v.Plan, v.OrdinaryUsageAllowed, run, v.Phase, v.PlusMinus, v.Evidence)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			continue
		}
		// Estimates never replace a measured observation. Within a timestamp the
		// harness wins over the daemon; delayed/out-of-order samples stay history.
		var newer bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM account_capacity_readings WHERE account_id=$1 AND window_kind=$2 AND bucket=$3 AND source<>'estimate' AND (read_at>$4 OR (read_at=$4 AND source='harness' AND $5<>'harness'))) OR EXISTS(SELECT 1 FROM account_capacity_readings WHERE account_id=$1 AND source=$5 AND read_at>$6)`, id, v.WindowKind, v.Bucket, v.ReadAt, v.Source, snapshotAt[v.Source]).Scan(&newer); err != nil {
			return err
		}
		if newer || v.Source == "estimate" {
			continue
		}
		// Train only the winning timestamp; a higher-authority same-time sample
		// must not count the preceding interval twice.
		var sameTime bool
		if err := tx.QueryRow(ctx, `SELECT count(*)>1 FROM account_capacity_readings WHERE account_id=$1 AND window_kind=$2 AND bucket=$3 AND read_at=$4 AND source<>'estimate'`, id, v.WindowKind, v.Bucket, v.ReadAt).Scan(&sameTime); err != nil {
			return err
		}
		if !sameTime {
			if err := learnReading(ctx, tx, a, v, now); err != nil {
				return err
			}
		}

		if _, err := tx.Exec(ctx, `UPDATE account_allowance_windows SET capacity_retired=true WHERE account_id=$1 AND capacity_source='estimate' AND capacity_read_at<$2`, id, v.ReadAt); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE account_allowance_windows SET capacity_allowed=false, capacity_retired=true WHERE account_id=$1 AND capacity_kind=$2 AND capacity_bucket=$3`, id, v.WindowKind, v.Bucket); err != nil {
			return err
		}
		// A missing authority bit is not recovery from a vendor denial on this
		// window, including across its own reset. Only a later explicit
		// allowance on the same window clears it. Another window keeps its own.
		allowed := true
		var authority *bool
		err = tx.QueryRow(ctx, `SELECT ordinary_usage_allowed FROM account_capacity_readings WHERE account_id=$1 AND window_kind=$2 AND bucket=$3 AND source<>'estimate' AND ordinary_usage_allowed IS NOT NULL ORDER BY read_at DESC, CASE source WHEN 'harness' THEN 0 ELSE 1 END, ordinary_usage_allowed ASC LIMIT 1`, id, v.WindowKind, v.Bucket).Scan(&authority)
		if err != nil && !isNoRows(err) {
			return err
		}
		if authority != nil {
			allowed = *authority
		}
		_, err = tx.Exec(ctx, `INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance,used,pace_model,burst_ratio,capacity_kind,capacity_bucket,capacity_read_at,capacity_allowed,capacity_source)
   VALUES($1,$2,$3,$4,'percent',100,$5,'unrestricted',0,$6,$7,$8,$9,$10)
   ON CONFLICT(tenant_id,account_id,capacity_kind,capacity_bucket,ends_at) WHERE capacity_kind IS NOT NULL
   DO UPDATE SET starts_at=EXCLUDED.starts_at,used=EXCLUDED.used,capacity_read_at=EXCLUDED.capacity_read_at,capacity_allowed=EXCLUDED.capacity_allowed,capacity_source=EXCLUDED.capacity_source,capacity_retired=false,capacity_refresh_run=CASE WHEN EXCLUDED.capacity_read_at>account_allowance_windows.capacity_read_at THEN NULL ELSE account_allowance_windows.capacity_refresh_run END`, p.TenantID, id, v.StartsAt(), v.ResetsAt, int64(math.Ceil(v.UsedPercent)), v.WindowKind, v.Bucket, v.ReadAt, allowed, v.Source)
		if err != nil {
			return err
		}
	}
	for _, source := range []string{"harness", "agentd"} {
		var at time.Time
		keys := []string{}
		for _, v := range readings {
			if v.Source == source {
				if v.ReadAt.After(at) {
					at = v.ReadAt
				}
				keys = append(keys, v.WindowKind+"/"+v.Bucket)
			}
		}
		if at.IsZero() {
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE account_allowance_windows SET capacity_retired=true WHERE account_id=$1 AND capacity_source=$2 AND capacity_read_at<=$3 AND NOT (capacity_kind||'/'||capacity_bucket=ANY($4::text[])) AND NOT EXISTS(SELECT 1 FROM account_capacity_readings WHERE account_id=$1 AND source=$2 AND read_at>$3)`, id, source, at, keys); err != nil {
			return err
		}
	}
	return nil
}

func readCapacity(ctx context.Context, tx pgx.Tx, id string, current bool) ([]capacity.Reading, error) {
	query := `SELECT window_kind,bucket,window_minutes,used_percent::float8,resets_at,read_at,source,plan,ordinary_usage_allowed,COALESCE(run_id::text,''),phase,plus_minus::float8,evidence FROM account_capacity_readings WHERE account_id=$1 ORDER BY read_at DESC, CASE source WHEN 'harness' THEN 0 WHEN 'agentd' THEN 1 ELSE 2 END LIMIT 200`
	args := []any{id}
	if current {
		now, err := dbNow(ctx, tx)
		if err != nil {
			return nil, err
		}
		args = append(args, now)
		query = `SELECT window_kind,bucket,window_minutes,used_percent::float8,resets_at,read_at,source,plan,ordinary_usage_allowed,COALESCE(run_id::text,''),phase,plus_minus::float8,evidence FROM (SELECT DISTINCT ON(window_kind,bucket) * FROM account_capacity_readings WHERE account_id=$1 AND (EXISTS(SELECT 1 FROM account_allowance_windows w WHERE w.account_id=account_capacity_readings.account_id AND w.capacity_kind=window_kind AND w.capacity_bucket=bucket AND w.capacity_read_at=read_at AND NOT w.capacity_retired)) ORDER BY window_kind,bucket,CASE WHEN source<>'estimate' AND read_at>=$2::timestamptz-interval '10 minutes' AND resets_at>$2 THEN 0 ELSE 1 END,read_at DESC, CASE source WHEN 'harness' THEN 0 WHEN 'agentd' THEN 1 ELSE 2 END) r ORDER BY resets_at,window_kind,bucket`
	}
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []capacity.Reading{}
	for rows.Next() {
		var v capacity.Reading
		if err := rows.Scan(&v.WindowKind, &v.Bucket, &v.WindowMinutes, &v.UsedPercent, &v.ResetsAt, &v.ReadAt, &v.Source, &v.Plan, &v.OrdinaryUsageAllowed, &v.RunID, &v.Phase, &v.PlusMinus, &v.Evidence); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (m *Module) capacityHistory(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if p.Kind != tenant.Agent {
		if err := m.requirePermission(r, p, "account.read"); err != nil {
			writeErr(w, err)
			return
		}
	}
	var out []capacity.Reading
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		id := r.PathValue("accountId")
		if !uuidRE.MatchString(id) {
			return fail(404, "account not found")
		}
		account, err := getAccount(r.Context(), tx, id)
		if err != nil {
			return fail(404, "account not found")
		}
		if p.Kind == tenant.Agent {
			if err := requireScope(r.Context(), tx, r, p, "account.probe"); err != nil {
				return err
			}
			if account.RegisteredBy != p.ID {
				return fail(403, "only the registering agent can read capacity")
			}
		}
		out, err = readCapacity(r.Context(), tx, id, false)
		return err
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}

type capacityWindow struct {
	Reading         capacity.Reading `json:"reading"`
	StartsAt        time.Time        `json:"starts_at"`
	Allowance       int              `json:"allowance"`
	Remaining       float64          `json:"remaining_percent"`
	Freshness       string           `json:"freshness"`
	Pacing          capacity.Pacing  `json:"pacing"`
	UsageTodayKnown bool             `json:"usage_today_known"`
}
type accountCapacity struct {
	Budget             *capacity.Budget  `json:"budget,omitempty"`
	Routing            *CapacityRouting  `json:"routing,omitempty"`
	AccountID          string            `json:"account_id"`
	OngoingUseApproved bool              `json:"ongoing_use_approved"`
	ProbeFailure       string            `json:"probe_failure,omitempty"`
	LimitingReset      *time.Time        `json:"limiting_reset,omitempty"`
	AwaitingReading    bool              `json:"awaiting_reading,omitempty"`
	Schedule           capacity.Schedule `json:"schedule"`
	Windows            []capacityWindow  `json:"windows"`
	// Limit is the Advanced sentence with its use this period (AEON-384).
	Limit *limitUse `json:"limit,omitempty"`
	// SpendMonthUSD is list-price spend this month for an API-key account.
	SpendMonthUSD      string                    `json:"spend_month_usd,omitempty"`
	CostLimitSupported bool                      `json:"cost_limit_supported"`
	Learning           *capacity.LearningSummary `json:"learning,omitempty"`
	QuotaFingerprint   string                    `json:"quota_fingerprint,omitempty"`
	GroupID            string                    `json:"group_id,omitempty"`
	GroupName          string                    `json:"group_name,omitempty"`
	HostLabel          string                    `json:"host_label,omitempty"`
	Hosts              []string                  `json:"hosts,omitempty"`
	SameQuotaAs        string                    `json:"same_quota_as,omitempty"`
}

func (m *Module) capacityList(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if err := m.requirePermission(r, p, "account.read"); err != nil {
		writeErr(w, err)
		return
	}
	var out []accountCapacity
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		var err error
		out, err = projectCapacity(r.Context(), tx, p.ID, nil, 0)
		return err
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}

// capacityPreview answers "today with these settings": the same projection as
// capacityList with the draft as the person's schedule, inherited exactly as a
// save with carry_overrides would (scheduleWithDraft/carryDraft). Nothing is
// stored. It is rate limited per person, bounded in concurrency, time and total
// integration work, so the editors can call it on every change.
func (m *Module) capacityPreview(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if err := m.requirePermission(r, p, "account.read"); err != nil {
		writeErr(w, err)
		return
	}
	g := m.preview
	if g == nil {
		g = defaultPreviewGuard
	}
	if ok, wait := g.allow(p.TenantID+"/"+p.ID, time.Now()); !ok {
		retryAfter(w, wait)
		writeErr(w, fail(http.StatusTooManyRequests, "too many previews; try again shortly"))
		return
	}
	// One deadline covers reading the body and the work. The body is read
	// before a slot is taken, so a slow or unfinished upload never holds one.
	ctx, cancel := g.withTimeout(r.Context(), g.timeout)
	defer cancel()
	raw, err := readBodyBy(ctx, w, r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var in struct {
		Schedule *capacity.Schedule `json:"schedule"`
		Pools    []poolReserve      `json:"pool_reserves"`
	}
	if err := decodeStrict(raw, &in); err != nil {
		writeErr(w, err)
		return
	}
	if in.Schedule == nil {
		writeErr(w, fail(400, "schedule is required"))
		return
	}
	if err := in.Schedule.Validate(); err != nil {
		writeErr(w, fail(400, err.Error()))
		return
	}
	if err := validPoolReserves(in.Pools); err != nil {
		writeErr(w, err)
		return
	}
	if !g.acquire() {
		retryAfter(w, time.Second)
		writeErr(w, fail(http.StatusServiceUnavailable, "previews are busy; try again shortly"))
		return
	}
	defer g.release()
	// Sprint and Hold are pool actions with their own ends; a drafted Away
	// (Keep for you) previews until its date.
	draft := *in.Schedule
	if draft.Override != "away" {
		draft.Override, draft.OverrideUntil = "", nil
	}
	var out []accountCapacity
	err = m.in(ctx, p.TenantID, func(tx pgx.Tx) error {
		if draft.Override == "away" {
			now, err := dbNow(ctx, tx)
			if err != nil {
				return err
			}
			if err := awayUntilOK(*draft.OverrideUntil, now); err != nil {
				return err
			}
		}
		var err error
		out, err = projectCapacity(ctx, tx, p.ID, &previewDraft{draft, in.Pools}, g.budget)
		return err
	})
	if err != nil {
		if ctx.Err() != nil {
			err = fail(http.StatusServiceUnavailable, "the preview took too long; try again")
		}
		writeErr(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}

// previewDraft is the person's drafted schedule and pools' drafted reserves.
type previewDraft struct {
	schedule capacity.Schedule
	pools    []poolReserve
}

// projectCapacity builds the capacity projection for person. With a draft it
// paces every account on the schedule it would follow after saving the draft,
// walking at most budget integration steps.
func projectCapacity(ctx context.Context, tx pgx.Tx, person string, draft *previewDraft, budget int) ([]accountCapacity, error) {
	out := []accountCapacity{}
	accounts, err := listAccounts(ctx, tx)
	if err != nil {
		return nil, err
	}
	now, err := dbNow(ctx, tx)
	if err != nil {
		return nil, err
	}
	entries, err := loadScheduleEntries(ctx, tx, person)
	if err != nil {
		return nil, err
	}
	fallback, err := personDefaultSchedule(ctx, tx, person)
	if err != nil {
		return nil, err
	}
	previous := userSchedule(entries, fallback)
	steps := 0
	for _, a := range accounts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		s := resolveSchedule(entries, a, fallback, now)
		if draft != nil {
			s = scheduleWithDraft(entries, a, previous, draft.schedule, draft.pools, now)
		}
		item := accountCapacity{AccountID: a.ID, Schedule: s, Windows: []capacityWindow{}, Budget: a.UsageBudget}
		item.CostLimitSupported, err = costLimitSupported(ctx, tx, a.ID)
		if err != nil {
			return nil, err
		}
		learned, err := loadLearning(ctx, tx, a.ID)
		if err != nil {
			return nil, err
		}
		summary := learned.summary(now, s)
		item.Learning = &summary
		if err := tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM agent_pairing_enrollments WHERE account_id=$1 AND (ongoing_approved_at IS NULL OR state<>'connected'))`, a.ID).Scan(&item.OngoingUseApproved); err != nil {
			return nil, err
		}
		if err := tx.QueryRow(ctx, `SELECT a.last_probe_failure,(SELECT min(w.ends_at) FROM account_allowance_windows w WHERE w.account_id=a.id AND `+currentCapacityWindows+`) FROM agent_accounts a WHERE a.id=$1`, a.ID).Scan(&item.ProbeFailure, &item.LimitingReset); err != nil {
			return nil, err
		}
		readings, err := readCapacity(ctx, tx, a.ID, true)
		if err != nil {
			return nil, err
		}
		item.AwaitingReading = true
		for _, v := range readings {
			if !now.Before(v.ResetsAt) {
				continue
			}
			if v.Source != "estimate" && v.Freshness(now) == "fresh" {
				item.AwaitingReading = false
			}
			if budget > 0 {
				if steps += previewSteps(v.StartsAt(), v.ResetsAt); steps > budget {
					return nil, fail(http.StatusRequestEntityTooLarge, "too many long capacity windows to preview at once")
				}
			}
			left := 100 - v.UsedPercent
			pace, known, err := readingPacing(ctx, tx, a.ID, v, now, s)
			if err != nil {
				return nil, err
			}
			item.Windows = append(item.Windows, capacityWindow{v, v.StartsAt(), 100, left, v.Freshness(now), pace, known})
		}
		if len(item.Windows) == 0 {
			item.LimitingReset = nil
		}
		if item.Limit, err = accountLimitUse(ctx, tx, a, now); err != nil {
			return nil, err
		}
		if item.SpendMonthUSD, err = monthSpend(ctx, tx, a.ID, s.Timezone, now); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	// The live plan consumes the exact admission and ordering rules as routing.
	// Draft previews do not claim to predict current dispatch eligibility.
	if draft == nil {
		advice, err := routingAdvice(ctx, tx, accounts, "", runRow{Purpose: "managed"}, now)
		if err != nil {
			return nil, err
		}
		for i := range out {
			value := advice[out[i].AccountID]
			out[i].Routing = &value
		}
	}
	annotateQuotas(out, accounts)
	return out, nil
}

type scheduleOverride struct {
	Scope     string             `json:"scope"`
	Pool      string             `json:"pool,omitempty"`
	AccountID string             `json:"account_id,omitempty"`
	Schedule  *capacity.Schedule `json:"schedule"`
	// CarryOverrides (user scope only) moves pool and account entries that only
	// carry Sprint/Hold onto the new schedule in the same transaction.
	CarryOverrides bool   `json:"carry_overrides,omitempty"`
	GroupID        string `json:"group_id,omitempty"`
}

func (m *Module) capacitySchedule(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	permission := "account.read"
	if r.Method == "PUT" {
		permission = "account.manage"
	}
	if err := m.requirePermission(r, p, permission); err != nil {
		writeErr(w, err)
		return
	}
	var in scheduleOverride
	if r.Method == "PUT" {
		if err := decodeJSON(w, r, &in); err != nil {
			writeErr(w, err)
			return
		}
	}
	out := []scheduleOverride{}
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		if r.Method == "GET" {
			rows, err := tx.Query(r.Context(), `SELECT scope,scope_key,schedule FROM account_capacity_schedules WHERE principal_id=$1 ORDER BY scope,scope_key`, p.ID)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var v scheduleOverride
				var key string
				var raw []byte
				if err := rows.Scan(&v.Scope, &key, &raw); err != nil {
					return err
				}
				if v.Scope == "pool" {
					v.Pool = key
				}
				if v.Scope == "account" {
					v.AccountID = key
				}
				if v.Scope == "group" {
					v.GroupID = key
				}
				if err := json.Unmarshal(raw, &v.Schedule); err != nil {
					return err
				}
				out = append(out, v)
			}
			return rows.Err()
		}
		key := ""
		var account any
		if in.CarryOverrides && (in.Scope != "user" || in.Schedule == nil) {
			return fail(400, "carry_overrides needs a user schedule")
		}
		switch in.Scope {
		case "user":
			if in.Pool != "" || in.AccountID != "" || in.GroupID != "" {
				return fail(400, "invalid schedule scope")
			}
		case "pool":
			if !validHarness(in.Pool) || in.AccountID != "" || in.GroupID != "" {
				return fail(400, "invalid pool")
			}
			key = in.Pool
		case "account":
			if !uuidRE.MatchString(in.AccountID) || in.Pool != "" || in.GroupID != "" {
				return fail(400, "invalid account")
			}
			key = strings.ToLower(in.AccountID)
			if _, err := getAccount(r.Context(), tx, key); err != nil {
				return fail(404, "account not found")
			}
			account = key
		case "group":
			if !uuidRE.MatchString(in.GroupID) || in.Pool != "" || in.AccountID != "" {
				return fail(400, "invalid group")
			}
			key = strings.ToLower(in.GroupID)
			var exists bool
			if err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM account_groups WHERE id=$1::uuid)`, key).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return fail(404, "group not found")
			}
		default:
			return fail(400, "invalid schedule scope")
		}
		if in.Schedule == nil {
			_, err := tx.Exec(r.Context(), `DELETE FROM account_capacity_schedules WHERE principal_id=$1 AND scope=$2 AND scope_key=$3`, p.ID, in.Scope, key)
			return err
		}
		if err := in.Schedule.Validate(); err != nil {
			return fail(400, err.Error())
		}
		now, err := dbNow(r.Context(), tx)
		if err != nil {
			return err
		}
		switch in.Schedule.Override {
		case "sprint":
			// Sprint is literal and bounded to the next limiting reset in scope.
			var until *time.Time
			if err := tx.QueryRow(r.Context(), `SELECT min(w.ends_at) FROM account_allowance_windows w JOIN agent_accounts a ON a.tenant_id=w.tenant_id AND a.id=w.account_id WHERE `+currentCapacityWindows+` AND ($1='user' OR ($1='pool' AND a.harness=$2) OR ($1='account' AND a.id::text=$2) OR ($1='group' AND a.group_id::text=$2))`, in.Scope, key).Scan(&until); err != nil {
				return err
			}
			if until == nil {
				return fail(400, "Sprint requires a current capacity reset")
			}
			in.Schedule.OverrideUntil = until
		case "hold":
			// Hold for a while, until tomorrow's hours, or until you resume (no date).
			if u := in.Schedule.OverrideUntil; u != nil && (!u.After(now) || u.After(now.Add(maxHold))) {
				return fail(400, "Hold must end within 31 days")
			}
		case "away":
			// I'm away until…: the person's own schedule, every pool, until the date.
			if in.Scope != "user" {
				return fail(400, "Away is set on your own schedule")
			}
			if err := awayUntilOK(*in.Schedule.OverrideUntil, now); err != nil {
				return err
			}
		default:
			in.Schedule.OverrideUntil = nil
		}
		if in.Scope == "account" {
			if _, err := tx.Exec(r.Context(), `UPDATE agent_accounts SET capacity_owner=COALESCE(capacity_owner,$2::uuid) WHERE id=$1`, key, p.ID); err != nil {
				return err
			}
		}
		if in.CarryOverrides {
			return saveCarried(r.Context(), tx, p.TenantID, p.ID, *in.Schedule)
		}
		raw, _ := json.Marshal(in.Schedule)
		_, err = tx.Exec(r.Context(), `INSERT INTO account_capacity_schedules(tenant_id,principal_id,scope,scope_key,account_id,schedule) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(tenant_id,principal_id,scope,scope_key) DO UPDATE SET schedule=EXCLUDED.schedule`, p.TenantID, p.ID, in.Scope, key, account, raw)
		return err
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	if r.Method == "PUT" {
		w.WriteHeader(204)
	} else {
		httpapi.WriteJSON(w, 200, out)
	}
}

// maxHold and maxAway bound dated overrides: a hold is a pause, Away a trip.
const (
	maxHold = 31 * 24 * time.Hour
	maxAway = 366 * 24 * time.Hour
)

func awayUntilOK(until, now time.Time) error {
	if !until.After(now) || until.After(now.Add(maxAway)) {
		return fail(400, "Away must end within a year")
	}
	return nil
}

func validPoolReserves(pools []poolReserve) error {
	if len(pools) > 8 {
		return fail(400, "too many pool reserves")
	}
	seen := map[string]bool{}
	for _, r := range pools {
		probe := capacity.DefaultSchedule()
		probe.Reserve, probe.ReservePercent = r.Reserve, r.ReservePercent
		if !validHarness(r.Pool) || seen[r.Pool] || probe.Validate() != nil {
			return fail(400, "invalid pool reserve")
		}
		seen[r.Pool] = true
	}
	return nil
}

func effectiveSchedule(ctx context.Context, tx pgx.Tx, person string, a Account) (capacity.Schedule, error) {
	entries, err := loadScheduleEntries(ctx, tx, person)
	if err != nil {
		return capacity.Schedule{}, err
	}
	fallback, err := personDefaultSchedule(ctx, tx, person)
	if err != nil {
		return capacity.Schedule{}, err
	}
	now, err := dbNow(ctx, tx)
	if err != nil {
		return capacity.Schedule{}, err
	}
	return resolveSchedule(entries, a, fallback, now), nil
}

// Approval belongs to a person, never to a quota reporter. This preserves the
// existing one-shot verification boundary while removing manual unit setup.
func (m *Module) approveCapacity(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if err := m.requirePermission(r, p, "account.manage"); err != nil {
		writeErr(w, err)
		return
	}
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		id := r.PathValue("accountId")
		if !uuidRE.MatchString(id) {
			return fail(404, "account not found")
		}
		// A person may opt in during pairing review, before the helper redeems
		// setup. RunFence still requires redeemed setup before any dispatch.
		var blocked bool
		if err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM agent_pairing_enrollments e
         JOIN agent_pairing_requests q ON q.tenant_id=e.tenant_id AND q.id=e.request_id
         JOIN agent_pairing_computers c ON c.tenant_id=e.tenant_id AND c.id=e.computer_id
         WHERE e.account_id=$1 AND (e.state<>'connected' OR c.state<>'connected' OR q.state NOT IN ('approved','redeemed')))`, id).Scan(&blocked); err != nil {
			return err
		}
		if blocked {
			return fail(409, "account is not available for approval")
		}
		if _, err := lockAccount(r.Context(), tx, id); err != nil {
			return err
		}
		tag, err := tx.Exec(r.Context(), `UPDATE agent_pairing_enrollments SET ongoing_approved_at=clock_timestamp() WHERE account_id=$1 AND state='connected' AND ongoing_approved_at IS NULL`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		return writeEvent(r.Context(), tx, p, "account.capacity_approved", nil, map[string]string{"account_id": id})
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(204)
}

func (s *scheduleOverride) UnmarshalJSON(raw []byte) error {
	type wire scheduleOverride
	var v wire
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return err
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return err
	}
	if _, ok := obj["schedule"]; !ok {
		return errors.New("schedule is required; use null to remove an override")
	}
	*s = scheduleOverride(v)
	return nil
}

// The schedule period determines the day's share. When its usage baseline is
// missing, count use since the first actual sample and keep it marked unknown.
// Anchoring the share at that sample would starve accounts first read late in
// the day. Never move the usage baseline forward on every route.
func readingPacing(ctx context.Context, tx pgx.Tx, id string, v capacity.Reading, now time.Time, s capacity.Schedule) (capacity.Pacing, bool, error) {
	period, _ := s.Period(now)
	start, used, known := v.StartsAt(), v.UsedPercent, !v.StartsAt().Before(period)
	if !known {
		var baseline float64
		var at time.Time
		err := tx.QueryRow(ctx, `SELECT used_percent::float8,read_at FROM account_capacity_readings WHERE account_id=$1 AND window_kind=$2 AND bucket=$3 AND resets_at=$4 AND read_at>=$5 AND read_at<=$6 AND source<>'estimate' ORDER BY read_at,CASE source WHEN 'harness' THEN 0 ELSE 1 END LIMIT 1`, id, v.WindowKind, v.Bucket, v.ResetsAt, period, v.ReadAt).Scan(&baseline, &at)
		if err != nil && !isNoRows(err) {
			return capacity.Pacing{}, false, err
		}
		if err == nil && baseline <= v.UsedPercent {
			start, used, known = period, v.UsedPercent-baseline, at.Equal(period)
		} else {
			start, used = period, 0
		}
	}
	learned, err := loadLearning(ctx, tx, id)
	if err != nil {
		return capacity.Pacing{}, false, err
	}
	metric := learned.metric(v, now, s, "")
	var parallel int
	if err := tx.QueryRow(ctx, `SELECT max_parallel_runs FROM agent_accounts WHERE id=$1`, id).Scan(&parallel); err != nil {
		return capacity.Pacing{}, false, err
	}
	drift := 0.0
	if v.Source != "estimate" && v.Freshness(now) == "aging" {
		drift = math.Min(100-v.UsedPercent, metric.BurnRate*math.Max(0, now.Sub(v.ReadAt).Hours()))
	}
	p, err := capacity.Plan(capacity.PlanInput{Now: now, Reset: v.ResetsAt, WindowStart: start, Remaining: 100 - v.UsedPercent - drift, UsedToday: used, WindowLength: time.Duration(v.WindowMinutes) * time.Minute, AutoReserve: metric.AutoReserve, Throughput: metric.PerHour * float64(parallel)}, s)
	p.DriftPercent = drift
	return p, known, err
}

func routingSchedule(ctx context.Context, tx pgx.Tx, a Account) (capacity.Schedule, error) {
	var person *string
	// Pairing and an explicit account owner take precedence. An unpaired agent
	// with one human key creator can use that person's pool/user defaults before
	// an account override has ever been saved. Ambiguous ownership stays unset.
	err := tx.QueryRow(ctx, `SELECT COALESCE(
 (SELECT p.approved_by FROM agent_pairing_enrollments e JOIN agent_pairing_requests p ON p.tenant_id=e.tenant_id AND p.id=e.request_id WHERE e.account_id=a.id),
 a.capacity_owner,
 (SELECT (array_agg(DISTINCT k.created_by_principal_id))[1] FROM agent_keys k JOIN principals p ON p.tenant_id=k.tenant_id AND p.id=k.created_by_principal_id WHERE k.tenant_id=a.tenant_id AND k.principal_id=a.registered_by_principal_id AND p.kind='person' HAVING count(DISTINCT k.created_by_principal_id)=1)
)::text FROM agent_accounts a WHERE a.id=$1`, a.ID).Scan(&person)
	if err != nil {
		return capacity.Schedule{}, err
	}
	if person == nil {
		return capacity.DefaultSchedule(), nil
	}
	return effectiveSchedule(ctx, tx, *person, a)
}

// applyCapacityPacing sets each measured window's derived allowance (§2.3):
// cap = min(today's paced share, left − R_eff); fits subtracts reservations.
// Synthetic one-run grants are not vendor windows and keep no reserve.
func applyCapacityPacing(ctx context.Context, tx pgx.Tx, a Account, windows []Window, now time.Time, s capacity.Schedule) error {
	for i := range windows {
		w := &windows[i]
		if w.capacityReadAt == nil || synthetic(*w) {
			continue
		}
		ws := s
		v := capacity.Reading{WindowKind: w.capacityKind, Bucket: w.capacityBucket, WindowMinutes: int(w.EndsAt.Sub(w.StartsAt) / time.Minute), ResetsAt: w.EndsAt, ReadAt: *w.capacityReadAt, UsedPercent: float64(w.Used), Source: w.capacitySource}
		p, _, err := readingPacing(ctx, tx, w.AccountID, v, now, ws)
		if err != nil {
			return err
		}
		w.capacityBudget = &p.AvailableNowPercent
		w.capacityShare, w.capacityReserveUntil = nil, nil
		if share, binds := p.ReserveBinds(); binds {
			w.capacityShare, w.capacityReserveUntil = &share, p.ReserveUntil
		}
	}
	return nil
}
