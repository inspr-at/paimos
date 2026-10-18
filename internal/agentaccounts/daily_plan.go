// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/accountprivacy"
	"github.com/inspr-at/paimos/internal/agentplan"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// DailyContextTx is shared by the snapshot and the final preference write.
// Defaults remain nullable for existing profile writers and rollout rollback.
func DailyContextTx(ctx context.Context, tx pgx.Tx, owner string, now time.Time) (string, int, time.Time, time.Time, error) {
	zone, points := "UTC", agentplan.DefaultDailyPoints
	var storedZone *string
	var storedPoints *int
	err := tx.QueryRow(ctx, `SELECT timezone,daily_points_per_day FROM personal_profiles WHERE principal_id=$1`, owner).Scan(&storedZone, &storedPoints)
	if err != nil && !isNoRows(err) {
		return "", 0, time.Time{}, time.Time{}, err
	}
	if storedZone != nil && *storedZone != "" {
		zone = *storedZone
	}
	if storedPoints != nil {
		points = *storedPoints
	}
	if points < 1 || points > 50 {
		return "", 0, time.Time{}, time.Time{}, errors.New("invalid stored daily default")
	}
	start, end, err := agentplan.LocalDay(now, zone)
	return zone, points, start, end, err
}

// ReadPlanTx is the complete owner-bound plan read. The base agentplan package
// deliberately has no account dependency; admission can consume this helper
// without an import cycle. This projection does not reserve or stop any work.
func ReadPlanTx(ctx context.Context, tx pgx.Tx, p tenant.Principal) (agentplan.Snapshot, error) {
	out, err := agentplan.ReadTx(ctx, tx, p)
	if err != nil {
		return out, err
	}
	now, err := dbNow(ctx, tx)
	if err != nil {
		return out, err
	}
	err = PopulateDailyTx(ctx, tx, p, &out, now)
	return out, err
}

func PopulateDailyTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, out *agentplan.Snapshot, now time.Time) error {
	zone, points, start, end, err := DailyContextTx(ctx, tx, out.PrincipalID, now)
	if err != nil {
		return err
	}
	out.DailyTimezone, out.DailyDefaultPoints, out.DailyUntil = zone, points, end
	saved := out.Daily
	out.Daily = map[string]agentplan.DailySettings{}
	out.DailyState = map[string]agentplan.DailyState{}
	for h, d := range saved {
		// A timezone change can leave a previously accepted boost before a
		// different local midnight. Keep it until that stored instant; the
		// write path still requires the current midnight.
		if d.BoostToday != nil && !now.Before(d.BoostToday.Until) {
			d.BoostToday = nil
		}
		out.Daily[h] = d
	}
	all, err := overviewAccounts(ctx, tx)
	if err != nil {
		return err
	}
	ids := make([]string, len(all))
	for i, a := range all {
		ids[i] = a.ID
	}
	owner := tenant.Principal{ID: out.PrincipalID, TenantID: p.TenantID, Kind: tenant.Person}
	privacy, err := accountprivacy.Load(ctx, tx, owner, ids)
	if err != nil {
		return err
	}
	owned, err := canonicalOwnedIDs(ctx, tx, out.PrincipalID, ids)
	if err != nil {
		return err
	}
	// Scope agents.plan.read delegates only the creator's plan. It does not
	// confer the separately scoped overview's access to other people's rows.
	accounts := []Account{}
	for _, a := range all {
		if owned[a.ID] {
			accounts = append(accounts, a)
		}
	}
	advice, err := routingAdvice(ctx, tx, accounts, "", runRow{Purpose: "managed"}, now)
	if err != nil {
		return err
	}
	sort.Slice(accounts, func(i, j int) bool {
		a, b := advice[accounts[i].ID].Rank, advice[accounts[j].ID].Rank
		if a == 0 {
			a = 1025
		}
		if b == 0 {
			b = 1025
		}
		if a != b {
			return a < b
		}
		return accounts[i].ID < accounts[j].ID
	})
	groups := map[string][]agentplan.DailyAccount{}
	canManage := p.Kind == tenant.Person && authz.RequireTx(ctx, tx, p, "account.manage", authz.Scope{}) == nil
	for _, a := range accounts {
		if agentplan.HarnessLabel(a.Harness) == "" {
			continue
		}
		d, explicit := saved[a.Harness]
		if explicit {
			d = out.Daily[a.Harness]
		} else {
			d = agentplan.DefaultDaily()
		}
		item := agentplan.DailyAccount{Contexts: a.Contexts, AccountID: a.ID, Label: a.Label, Order: len(groups[a.Harness]) + 1, Freshness: "unknown", ResetPolicy: "suggest", DetailsRedacted: !privacy[a.ID], CanEdit: canManage && privacy[a.ID]}
		if !privacy[a.ID] {
			groups[a.Harness] = append(groups[a.Harness], item)
			continue
		}
		item.Routable = advice[a.ID].AvailableSlots > 0
		item, d, err = dailyAccountTx(ctx, tx, a, item, d, explicit, p.TenantID, out.PrincipalID, points, start, end, now)
		if err != nil {
			return err
		}
		if _, exists := out.Daily[a.Harness]; !exists {
			out.Daily[a.Harness] = d
		}
		groups[a.Harness] = append(groups[a.Harness], item)
	}
	for h, accounts := range groups {
		out.DailyState[h] = agentplan.SummarizeDaily(accounts)
		if _, exists := out.Daily[h]; !exists {
			out.Daily[h] = agentplan.DefaultDaily()
		}
	}
	return nil
}

// dailyAccountTx projects one door without routing advice. Admission shares
// this calculation without recursively calling PopulateDailyTx.
func dailyAccountTx(ctx context.Context, tx pgx.Tx, a Account, item agentplan.DailyAccount, d agentplan.DailySettings, explicit bool, tenantID, owner string, points int, start, end, now time.Time) (agentplan.DailyAccount, agentplan.DailySettings, error) {
	u, err := loadUsagePolicy(ctx, tx, a.ID)
	if err != nil {
		return item, d, err
	}
	if !explicit {
		d = legacyDaily(u)
	}
	reset, err := loadResetState(ctx, tx, a, now)
	if err != nil {
		return item, d, err
	}
	if reset.Credits != nil {
		item.Resets = reset.Credits
	}
	if reset.Plan != nil {
		item.ResetPlan = reset.Plan
	}
	item.ResetPolicy = reset.Policy
	item.FloorPct = agentplan.Number(float64(u.Floor))
	item.NoDailyLimit = a.BillingMode == "api"
	if item.NoDailyLimit {
		return item, d, nil
	}
	schedule, err := routingSchedule(ctx, tx, a)
	if err != nil {
		return item, d, err
	}
	windows, err := overviewWindows(ctx, tx, a, now, schedule)
	if err != nil {
		return item, d, err
	}
	w := dailyWindow(windows, a.LinkedAt)
	if w == nil {
		// The overview omits incomplete facts. Preserve their evidence here:
		// a reported long window with missing fields is not "no reading".
		facts, err := loadReadinessFacts(ctx, tx, a, now)
		if err != nil {
			return item, d, err
		}
		if unreadableDailyWindow(windows, a.LinkedAt) || unreadableDailyFacts(facts, a.LinkedAt) {
			item.Freshness = "stale"
		}
		return item, d, nil
	}
	item.UsedPct, item.ResetsAt, item.ReadAt = w.UsedPercent, &w.ResetsAt, w.ReadAt
	item.Freshness = "stale"
	if !now.Before(w.ResetsAt) {
		item.Freshness = "expired"
	} else if w.ReadAt != nil && !w.ReadAt.After(now) && now.Sub(*w.ReadAt) <= ProbeFreshness {
		item.Freshness = "fresh"
	}
	item.StartOfDayUsedPct, err = dailyBaselineTx(ctx, tx, a, *w, tenantID, owner, start, now)
	if err != nil {
		return item, d, err
	}
	if !explicit && u.Boost > 0 && u.BoostUntil != nil && now.Before(*u.BoostUntil) && item.StartOfDayUsedPct != nil {
		accountPoints := points
		if d.Pace.PointsPerDay != nil {
			accountPoints = *d.Pace.PointsPerDay
		}
		limit := min(100, *item.StartOfDayUsedPct+float64(accountPoints+u.Boost))
		if d.Pace.Mode == "everything" {
			limit = 100
		}
		d.BoostToday = &agentplan.DailyBoost{LimitUsedPct: limit, EnteredAs: "used", Until: end}
	}
	raised, err := activeResetPace(ctx, tx, a, now)
	if err != nil {
		return item, d, err
	}
	item.ResetPacePoints = raised
	err = agentplan.ApplyDaily(&item, d, points, now)
	return item, d, err
}

func legacyDaily(u usagePolicy) agentplan.DailySettings {
	d := agentplan.DefaultDaily()
	switch u.Posture {
	case "careful":
		points := 5
		d.Pace.PointsPerDay = &points
	case "maxout":
		d.Pace.Mode = "everything"
	}
	// Translate each account's +N from its own measured baseline below.
	return d
}

func longDailyWindow(w *overviewWindow) bool {
	return w.Kind == "weekly" || w.Kind == "monthly" || strings.Contains(w.Bucket, "weekly") || strings.Contains(w.Bucket, "monthly")
}

// dailyWindow is the subscription percentage. A 5h session window is a quota
// hold, not the daily ceiling, even when it is the only vendor sample.
func dailyWindow(windows []overviewWindow, linkedAt *time.Time) *overviewWindow {
	var chosen *overviewWindow
	for i := range windows {
		w := &windows[i]
		if w.Source != "vendor_reported" || w.UsedPercent == nil || w.ReadAt == nil || w.ResetsAt.IsZero() || !longDailyWindow(w) {
			continue
		}
		// Filter the previous binding before ranking resets, so retained
		// history cannot hide a current window with an earlier reset.
		if linkedAt != nil && w.ReadAt.Before(*linkedAt) {
			continue
		}
		if chosen == nil || w.ResetsAt.After(chosen.ResetsAt) {
			chosen = w
		}
	}
	return chosen
}

func unreadableDailyWindow(windows []overviewWindow, linkedAt *time.Time) bool {
	for i := range windows {
		w := &windows[i]
		if linkedAt != nil && w.ReadAt != nil && w.ReadAt.Before(*linkedAt) {
			continue
		}
		if w.Source == "vendor_reported" && longDailyWindow(w) && (w.UsedPercent == nil || w.ReadAt == nil || w.ResetsAt.IsZero()) {
			return true
		}
	}
	return false
}

func unreadableDailyFacts(facts []ReadinessFact, linkedAt *time.Time) bool {
	for _, f := range facts {
		if f.UsedPercent != nil && f.ReadingAt != nil && f.ResetsAt != nil {
			continue
		}
		// A null capture has no reading time; its observation still binds
		// the incomplete evidence to the link active when it was reported.
		at := f.ObservedAt
		if f.ReadingAt != nil {
			at = *f.ReadingAt
		}
		if linkedAt != nil && at.Before(*linkedAt) {
			continue
		}
		if longDailyWindow(&overviewWindow{Kind: "fact", Bucket: f.WindowKey}) {
			return true
		}
	}
	return false
}

func dailyBaselineTx(ctx context.Context, tx pgx.Tx, a Account, w overviewWindow, tenantID, owner string, start, now time.Time) (*float64, error) {
	// Never count a previous binding's history, nor infer a midnight zero.
	bound := start
	if a.LinkedAt != nil && a.LinkedAt.After(bound) {
		bound = *a.LinkedAt
	}
	// A spend can refresh usage without changing the vendor's natural reset.
	// Its confirmed reading starts the new daily baseline too.
	var resetAt *time.Time
	if err := tx.QueryRow(ctx, `SELECT max((result->'window'->>'read_at')::timestamptz) FROM account_reset_actions
 WHERE account_id=$1 AND binding_revision=$2 AND state IN ('succeeded','undone') AND completed_at<=$3`, a.ID, a.LinkRevision, now).Scan(&resetAt); err != nil {
		return nil, err
	}
	if resetAt != nil && resetAt.After(bound) {
		bound = *resetAt
	}
	var value float64
	var err error
	if w.Kind == "fact" {
		parts := strings.SplitN(w.Bucket, ":", 2)
		if len(parts) != 2 {
			return nil, errors.New("invalid readiness daily window")
		}
		err = tx.QueryRow(ctx, `SELECT used_pct FROM account_daily_observations o
   JOIN agent_accounts reporter ON reporter.tenant_id=o.tenant_id AND reporter.id=o.account_id AND reporter.link_revision=o.binding_revision
   WHERE o.tenant_id=$1 AND o.resource_id=$2::uuid AND o.window_key=$3 AND o.resets_at=$4 AND o.read_at>=$5 AND o.read_at<=$6
   AND o.person_id=$7::uuid ORDER BY o.read_at,o.id LIMIT 1`, tenantID, parts[0], parts[1], w.ResetsAt, bound, minTime(now, *w.ReadAt), owner).Scan(&value)
	} else {
		err = tx.QueryRow(ctx, `SELECT used_percent::float8 FROM account_capacity_readings WHERE account_id=$1 AND window_kind=$2 AND bucket=$3 AND resets_at=$4
   AND read_at>=$5 AND read_at<=$6 AND source<>'estimate' ORDER BY read_at,CASE source WHEN 'harness' THEN 0 ELSE 1 END LIMIT 1`, a.ID, w.Kind, w.Bucket, w.ResetsAt, bound, minTime(now, *w.ReadAt)).Scan(&value)
	}
	if isNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &value, nil
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// ValidateDailyWriteTx runs under the preference writer's tenant fence, after
// live views.write was rechecked. Boost edits require the current owner's live
// authority and quota privacy; a plan preference is never a grant over accounts.
func ValidateDailyWriteTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, owner string, next, previous agentplan.Plan, now time.Time) error {
	_, _, _, end, err := DailyContextTx(ctx, tx, owner, now)
	if err != nil {
		return err
	}
	changed := map[string]bool{}
	for h, d := range next.Daily {
		if b := d.BoostToday; b != nil && now.Before(b.Until) && !b.Until.Equal(end) {
			return agentplan.ErrDailyWrite
		}
		if !sameBoost(d.BoostToday, previous.DailySetting(h).BoostToday) {
			changed[h] = true
		}
	}
	for h, d := range previous.Daily {
		if _, exists := next.Daily[h]; !exists && d.BoostToday != nil {
			changed[h] = true
		}
	}
	if len(changed) == 0 {
		return nil
	}
	if err := authz.RequireTx(ctx, tx, p, "account.manage", authz.Scope{}); err != nil {
		return authz.ErrForbidden
	}
	accounts, err := overviewAccounts(ctx, tx)
	if err != nil {
		return err
	}
	ids := []string{}
	for _, a := range accounts {
		if changed[a.Harness] {
			ids = append(ids, a.ID)
		}
	}
	owned, err := canonicalOwnedIDs(ctx, tx, owner, ids)
	if err != nil {
		return err
	}
	privacy, err := accountprivacy.Load(ctx, tx, tenant.Principal{ID: owner, TenantID: p.TenantID, Kind: tenant.Person}, ids)
	if err != nil {
		return err
	}
	// Only the owner's doors participate; another person's same-harness account
	// is never modified by this preference and remains on its owner's plan.
	for _, id := range ids {
		if owned[id] && !privacy[id] {
			return authz.ErrForbidden
		}
	}
	return nil
}
func sameBoost(a, b *agentplan.DailyBoost) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	if a == nil {
		return true
	}
	return a.LimitUsedPct == b.LimitUsedPct && a.EnteredAs == b.EnteredAs && a.Until.Equal(b.Until)
}
