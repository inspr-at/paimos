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
		if d.BoostToday != nil && !now.Before(d.BoostToday.Until) {
			d.BoostToday = nil
		}
		if d.BoostToday != nil && !d.BoostToday.Until.Equal(end) {
			return errors.New("invalid stored boost midnight")
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
		if !explicit {
			d = agentplan.DefaultDaily()
		}
		item := agentplan.DailyAccount{AccountID: a.ID, Label: a.Label, Order: len(groups[a.Harness]) + 1, Freshness: "unknown", ResetPolicy: "suggest", DetailsRedacted: !privacy[a.ID], CanEdit: canManage && privacy[a.ID]}
		if !privacy[a.ID] {
			groups[a.Harness] = append(groups[a.Harness], item)
			continue
		}
		u, err := loadUsagePolicy(ctx, tx, a.ID)
		if err != nil {
			return err
		}
		if !explicit {
			d = legacyDaily(u)
		}
		if _, exists := out.Daily[a.Harness]; !exists {
			out.Daily[a.Harness] = d
		}
		var policy *string
		if err := tx.QueryRow(ctx, `SELECT daily_reset_policy FROM agent_accounts WHERE id=$1`, a.ID).Scan(&policy); err != nil {
			return err
		}
		if policy != nil {
			if *policy != "suggest" && *policy != "auto_before_expiry" {
				return errors.New("invalid reset policy")
			}
			item.ResetPolicy = *policy
		}
		item.FloorPct = agentplan.Number(float64(u.Floor))
		item.Routable = advice[a.ID].AvailableSlots > 0
		item.NoDailyLimit = a.BillingMode == "api"
		if !item.NoDailyLimit {
			schedule, err := routingSchedule(ctx, tx, a)
			if err != nil {
				return err
			}
			windows, err := overviewWindows(ctx, tx, a, now, schedule)
			if err != nil {
				return err
			}
			if w := dailyWindow(windows); w != nil {
				item.UsedPct = w.UsedPercent
				item.ResetsAt = &w.ResetsAt
				item.ReadAt = w.ReadAt
				item.Freshness = "stale"
				if !now.Before(w.ResetsAt) {
					item.Freshness = "expired"
				} else if w.ReadAt != nil && !w.ReadAt.After(now) && now.Sub(*w.ReadAt) <= ProbeFreshness {
					item.Freshness = "fresh"
				}
				baseline, err := dailyBaselineTx(ctx, tx, a, *w, p.TenantID, out.PrincipalID, start, now)
				if err != nil {
					return err
				}
				item.StartOfDayUsedPct = baseline
				if !explicit && u.Boost > 0 && u.BoostUntil != nil && now.Before(*u.BoostUntil) && baseline != nil {
					pointsForAccount := points
					if d.Pace.PointsPerDay != nil {
						pointsForAccount = *d.Pace.PointsPerDay
					}
					limit := min(100, *baseline+float64(pointsForAccount+u.Boost))
					if d.Pace.Mode == "everything" {
						limit = 100
					}
					d.BoostToday = &agentplan.DailyBoost{LimitUsedPct: limit, EnteredAs: "used", Until: end}
					if first := groups[a.Harness]; len(first) == 0 {
						out.Daily[a.Harness] = d
					}
				}
				if err := agentplan.ApplyDaily(&item, d, points, now); err != nil {
					return err
				}
			}
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

func dailyWindow(windows []overviewWindow) *overviewWindow {
	var chosen *overviewWindow
	for i := range windows {
		w := &windows[i]
		if w.Source != "vendor_reported" || w.UsedPercent == nil || w.ReadAt == nil {
			continue
		}
		long := w.Kind == "weekly" || w.Kind == "monthly" || strings.Contains(w.Bucket, "weekly") || strings.Contains(w.Bucket, "monthly")
		chosenLong := chosen != nil && (chosen.Kind == "weekly" || chosen.Kind == "monthly" || strings.Contains(chosen.Bucket, "weekly") || strings.Contains(chosen.Bucket, "monthly"))
		if chosen == nil || long && !chosenLong || long == chosenLong && w.ResetsAt.After(chosen.ResetsAt) {
			chosen = w
		}
	}
	return chosen
}

func dailyBaselineTx(ctx context.Context, tx pgx.Tx, a Account, w overviewWindow, tenantID, owner string, start, now time.Time) (*float64, error) {
	// Never count a previous binding's history, nor infer a midnight zero.
	bound := start
	if a.LinkedAt != nil && a.LinkedAt.After(bound) {
		bound = *a.LinkedAt
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
