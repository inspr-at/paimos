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
	"github.com/inspr-at/paimos/internal/tenant"
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
		raw, err := json.Marshal(agentplan.Plan{Total: 5, Limits: map[string]agentplan.Limit{}, Daily: map[string]agentplan.DailySettings{"codex": settings}})
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
	settings.BoostToday.Until = now
	save(t)
	if err := db.InTenant(ctx, appPool, owner.TenantID, func(tx pgx.Tx) error {
		out, err := ReadDailyPolicyTx(ctx, tx, owner.TenantID, owner.ID, now)
		if err != nil {
			return err
		}
		if out.Daily["codex"].BoostToday != nil {
			t.Fatal("expired explicit boost survived projection")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	check(t, true, "")
	setPlan(t, []byte(`{"total":5,"daily":{"codex":{"pace":{"mode":"pace","points_per_day":0},"boost_today":null,"at_limit":"ladder"}}}`))
	check(t, false, "daily_limit_unknown")
	save(t)
	// Risk: a fresh sample from before the current link was refused as a
	// partial daily reading. It is the previous binding, not today's ceiling.
	if _, err := adminPool.Exec(ctx, `UPDATE agent_accounts SET linked_at=$2 WHERE id=$1`, account.ID, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	check(t, false, "")
	if _, err := adminPool.Exec(ctx, `UPDATE agent_accounts SET linked_at=$2 WHERE id=$1`, account.ID, start.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	ctx = context.WithValue(ctx, clockKey{}, now.Add(2*time.Minute+time.Nanosecond))
	now = now.Add(2*time.Minute + time.Nanosecond)
	check(t, false, "daily_limit_unknown")
}

// Risk: a weekly/monthly fact with missing fields disappears from the overview
// and is mistaken for a door that has never reported a daily percentage.
func TestDailyAdmissionIncompleteFactsFailClosed(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	for _, window := range []string{"weekly", "monthly"} {
		for _, missing := range []string{"percentage", "reading_time", "reset"} {
			t.Run(window+"/"+missing, func(t *testing.T) {
				f := readinessWorld(t, "daily-incomplete", now)
				fact := ReadinessFactWrite{ResourceID: bResource(t, f), WindowKey: window + ":", Source: "harness", ObservedAt: now, ReadingAt: &now, ResetsAt: timePtr(now.Add(5 * 24 * time.Hour)), UsedPercent: agentplan.Number(40), CreditState: "unknown", StopKind: "none"}
				switch missing {
				case "percentage":
					fact.UsedPercent = nil
					fact.Remaining = agentplan.Number(10) // Retain the measured timestamp.
				case "reading_time":
					fact.UsedPercent, fact.ReadingAt = nil, nil
				case "reset":
					fact.ResetsAt = nil
				}
				callStatus(t, f.mod, &f.runner, f.token, "POST", "/api/agent-accounts/"+f.account.ID+"/probe", encoded(t, probeWrite{DaemonID: "daemon-a", DaemonGeneration: "g1", Available: true, Readiness: &ReadinessReport{BindingRevision: ptrRevision(0), Result: "success", Facts: []ReadinessFactWrite{fact}}}), 200, nil)
				if scalar(t, f.admin, `SELECT count(*) FROM account_capacity_readings WHERE account_id=$1`, f.account.ID) != 0 || scalar(t, f.admin, `SELECT count(*) FROM account_readiness_facts WHERE resource_id=$1 AND window_key=$2`, fact.ResourceID, fact.WindowKey) != 1 {
					t.Fatal("fixture must retain a fact-only incomplete window")
				}
				ctx := context.WithValue(dbtest.Seed(t.Context()), clockKey{}, now)
				if err := db.InTenant(ctx, appPool, f.admin.TenantID, func(tx pgx.Tx) error {
					a, err := getAccount(ctx, tx, f.account.ID)
					if err != nil {
						return err
					}
					for _, claiming := range []bool{false, true} {
						_, wait, err := admission(ctx, tx, a, a.Windows, now, 0, runRow{ID: "48a796a9-6c76-4fb5-86f9-c77c89118022", Purpose: "managed", CapacityOverride: "now"}, claiming)
						if err != nil {
							return err
						}
						if wait == nil || wait.Code != "daily_limit_unknown" || wait.RunNowAllowed {
							t.Fatalf("incomplete %s fact admitted (claiming=%v): %+v", missing, claiming, wait)
						}
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

// Risk: a retained pre-link legacy window with a later reset outranks a
// current fact window, hiding exhaustion during reservation and claim.
func TestDailyAdmissionCurrentFactSurvivesPreLinkWindow(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	f := readinessWorld(t, "daily-mixed-binding", now)
	linked := now.Add(-5 * time.Minute)
	if _, err := adminPool.Exec(t.Context(), `UPDATE agent_accounts SET linked_at=$2 WHERE id=$1`, f.account.ID, linked); err != nil {
		t.Fatal(err)
	}
	resource := bResource(t, f)
	resets := now.Add(5 * 24 * time.Hour)
	for i, used := range []float64{40, 50} {
		read := now.Add(time.Duration(i-1) * time.Minute)
		fact := ReadinessFactWrite{ResourceID: resource, WindowKey: "weekly:", Source: "harness", ObservedAt: read, ReadingAt: &read, ResetsAt: &resets, UsedPercent: &used, CreditState: "unknown", StopKind: "none"}
		callStatus(t, f.mod, &f.runner, f.token, "POST", "/api/agent-accounts/"+f.account.ID+"/probe", encoded(t, probeWrite{DaemonID: "daemon-a", DaemonGeneration: "g1", Available: true, Readiness: &ReadinessReport{BindingRevision: ptrRevision(0), Result: "success", Facts: []ReadinessFactWrite{fact}}}), 200, nil)
	}
	ctx := context.WithValue(tenant.WithPrincipal(dbtest.Seed(t.Context()), f.runner), clockKey{}, now)
	if err := db.InTenant(ctx, appPool, f.admin.TenantID, func(tx pgx.Tx) error {
		return ingestReadings(ctx, tx, f.runner, f.account.ID, []capacity.Reading{{WindowKind: "weekly", WindowMinutes: 10080, UsedPercent: 10, ResetsAt: now.Add(7 * 24 * time.Hour), ReadAt: linked.Add(-time.Minute), Source: "harness"}})
	}); err != nil {
		t.Fatal(err)
	}
	if scalar(t, f.admin, `SELECT count(*) FROM account_capacity_readings WHERE account_id=$1`, f.account.ID) != 1 || scalar(t, f.admin, `SELECT count(*) FROM account_daily_observations WHERE resource_id=$1`, resource) != 2 {
		t.Fatal("fixture must retain pre-link legacy evidence and the current fact baseline")
	}
	_, end, err := agentplan.LocalDay(now, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(ctx, appPool, f.admin.TenantID, func(tx pgx.Tx) error {
		a, err := getAccount(ctx, tx, f.account.ID)
		if err != nil {
			return err
		}
		for _, claiming := range []bool{false, true} {
			_, wait, err := admission(ctx, tx, a, a.Windows, now, 0, runRow{ID: "48a796a9-6c76-4fb5-86f9-c77c89118022", Purpose: "managed", CapacityOverride: "now"}, claiming)
			if err != nil {
				return err
			}
			if wait == nil || wait.Code != "daily_limit" || wait.Until == nil || !wait.Until.Equal(end) || wait.RunNowAllowed {
				t.Fatalf("pre-link window hid current exhaustion (claiming=%v): %+v", claiming, wait)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
