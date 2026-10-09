// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentplan"
	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

// Risk: a reservation, run-now or blind recovery bypasses a lowered cap, and
// stale or malformed plan data becomes a successful managed start.
func TestDailyAdmissionRechecksCurrentPlanAtReservationAndClaim(t *testing.T) {
	reset(t)
	owner := makePrincipal(t, "daily-admission", "person", "Owner", []string{"admin"})
	runner := addPrincipal(t, owner.TenantID, "agent", "Runner", nil)
	token := issueKey(t, runner, []string{"account.manage", "account.probe"})
	var account Account
	callStatus(t, accountsMod(), &runner, token, "POST", "/api/agent-accounts", `{"account_key":"daily","harness":"codex","daemon_id":"daily-daemon","label":"Daily","max_parallel_runs":2}`, 201, &account)
	ownFixtureAccount(t, owner, &account)
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	start, end, err := agentplan.LocalDay(now, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(dbtest.Seed(t.Context()), clockKey{}, now)
	settings := agentplan.DefaultDaily()
	setPlan := func(t *testing.T, raw []byte) {
		t.Helper()
		if _, err := adminPool.Exec(ctx, `INSERT INTO user_preferences(tenant_id,principal_id,key,value) VALUES($1,$2,'agents.working',$3) ON CONFLICT(tenant_id,principal_id,key) DO UPDATE SET value=EXCLUDED.value`, owner.TenantID, owner.ID, raw); err != nil {
			t.Fatal(err)
		}
	}
	save := func(t *testing.T) {
		t.Helper()
		raw, err := json.Marshal(agentplan.Plan{Total: 5, Daily: map[string]agentplan.DailySettings{"codex": settings}})
		if err != nil {
			t.Fatal(err)
		}
		setPlan(t, raw)
	}
	if err := db.InTenant(ctx, appPool, owner.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE agent_accounts SET linked_at=$2,last_probe_at=$3,last_probe_ok=true,last_daemon_generation='daily-generation' WHERE id=$1`, account.ID, start.Add(-time.Hour), now); err != nil {
			return err
		}
		schedule := capacity.DefaultSchedule()
		schedule.Override = "sprint"
		schedule.Reserve = "off"
		raw, _ := json.Marshal(schedule)
		if _, err := tx.Exec(ctx, `INSERT INTO account_capacity_schedules(tenant_id,principal_id,scope,scope_key,account_id,schedule) VALUES($1,$2,'account',$3::uuid::text,$3,$4)`, owner.TenantID, owner.ID, account.ID, raw); err != nil {
			return err
		}
		return ingestReadings(ctx, tx, runner, account.ID, []capacity.Reading{{WindowKind: "weekly", WindowMinutes: 10080, UsedPercent: 40, ResetsAt: end.Add(5 * 24 * time.Hour), ReadAt: start.Add(time.Minute), Source: "harness"}, {WindowKind: "weekly", WindowMinutes: 10080, UsedPercent: 49, ResetsAt: end.Add(5 * 24 * time.Hour), ReadAt: now, Source: "harness"}})
	}); err != nil {
		t.Fatal(err)
	}
	save(t)
	check := func(t *testing.T, claiming bool, reason string) {
		t.Helper()
		err := db.InTenant(ctx, appPool, owner.TenantID, func(tx pgx.Tx) error {
			a, err := lockAccount(ctx, tx, account.ID)
			if err != nil {
				return err
			}
			_, wait, err := admission(ctx, tx, a, a.Windows, now, 0, runRow{ID: "48a796a9-6c76-4fb5-86f9-c77c89118022", Purpose: "managed", CapacityOverride: "now"}, claiming)
			if err != nil {
				return err
			}
			if reason == "" {
				if wait != nil {
					t.Fatalf("refused known room: %+v", wait)
				}
			} else if wait == nil || wait.Code != reason {
				t.Fatalf("wait=%+v want=%s", wait, reason)
			}
			if reason == "daily_limit" && (wait.Until == nil || !wait.Until.Equal(end) || wait.RunNowAllowed) {
				t.Fatal("cap lost midnight or offered bypass")
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	check(t, false, "")
	// The settings change after reservation advice, before launch validation.
	settings.AtLimit = "wait"
	settings.BoostToday = &agentplan.DailyBoost{LimitUsedPct: 49, EnteredAs: "used", Until: end}
	save(t)
	check(t, false, "daily_limit")
	check(t, true, "daily_limit")
	settings.BoostToday.LimitUsedPct = 60
	save(t)
	check(t, true, "")
	setPlan(t, []byte(`{"total":5,"daily":{"codex":{"pace":{"mode":"pace","points_per_day":0},"boost_today":null,"at_limit":"ladder"}}}`))
	check(t, false, "daily_limit_unknown")
	save(t)
	ctx = context.WithValue(ctx, clockKey{}, now.Add(2*time.Minute+time.Nanosecond))
	now = now.Add(2*time.Minute + time.Nanosecond)
	check(t, false, "daily_limit_unknown")
}
