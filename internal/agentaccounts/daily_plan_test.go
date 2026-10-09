// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentplan"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risk: daily GET uses a rolling baseline, the wrong allowance window, or
// another owner's private measurements; existing posture/Boost must survive.
func TestDailyPlanAccountWindowsLocalMidnightMigrationAndPrivacy(t *testing.T) {
	reset(t)
	owner := makePrincipal(t, "daily-plan", "person", "Owner", []string{"admin"})
	runner := addPrincipal(t, owner.TenantID, "agent", "Runner", nil)
	other := addPrincipal(t, owner.TenantID, "person", "Other", []string{"admin"})
	codexProfile(t, owner)
	token := issueKey(t, runner, []string{"account.manage", "account.probe"})
	mod := accountsMod()
	// A fixed local day deliberately begins on the previous UTC date.
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	ctx := context.WithValue(t.Context(), clockKey{}, now)
	start, end, err := agentplan.LocalDay(now, "Europe/Vienna")
	if err != nil {
		t.Fatal(err)
	}
	weeklyReset := end.Add(5 * 24 * time.Hour)
	if _, err := adminPool.Exec(ctx, `INSERT INTO personal_profiles(tenant_id,principal_id,timezone) VALUES($1,$2,'Europe/Vienna') ON CONFLICT(tenant_id,principal_id) DO UPDATE SET timezone=EXCLUDED.timezone`, owner.TenantID, owner.ID); err != nil {
		t.Fatal(err)
	}
	var accounts []Account
	for _, spec := range []struct {
		key, harness   string
		baseline, used float64
	}{{"first", "codex", 40, 52}, {"second", "codex", 30, 35}, {"private", "codex", 10, 12}, {"claude", "claude", 40, 42}, {"cursor", "cursor", 0, 0}} {
		var a Account
		callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts", encoded(t, map[string]any{"account_key": spec.key, "harness": spec.harness, "daemon_id": "daily-daemon", "label": spec.key, "max_parallel_runs": 2}), 201, &a)
		ownFixtureAccount(t, owner, &a)
		err := db.InTenant(dbtest.Seed(ctx), appPool, owner.TenantID, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `UPDATE agent_accounts SET linked_at=$2,last_probe_at=$3,last_probe_ok=true,last_daemon_generation='daily-generation',usage_floor_percent=20,billing_mode=CASE WHEN harness='cursor' THEN 'api' ELSE billing_mode END WHERE id=$1`, a.ID, start.Add(-time.Hour), now); err != nil {
				return err
			}
			if spec.key == "private" {
				_, err := tx.Exec(ctx, `UPDATE agent_accounts SET quota_fingerprint=repeat('f',64),quota_pool_fingerprint=repeat('f',64) WHERE id=$1`, a.ID)
				return err
			}
			if spec.key == "cursor" {
				return nil
			}
			if spec.key == "claude" {
				if _, err := tx.Exec(ctx, `UPDATE agent_accounts SET usage_posture='careful',usage_posture_person_id=$2,usage_posture_link_revision=link_revision,boost_percent=10,boost_until=$3,boost_person_id=$2,boost_link_revision=link_revision WHERE id=$1`, a.ID, owner.ID, end); err != nil {
					return err
				}
			}
			readings := []capacity.Reading{
				{WindowKind: "weekly", WindowMinutes: 10080, UsedPercent: spec.baseline - 1, ResetsAt: weeklyReset, ReadAt: start.Add(-time.Minute), Source: "harness"},
				{WindowKind: "weekly", WindowMinutes: 10080, UsedPercent: spec.baseline, ResetsAt: weeklyReset, ReadAt: start.Add(time.Minute), Source: "harness"},
				{WindowKind: "weekly", WindowMinutes: 10080, UsedPercent: spec.used, ResetsAt: weeklyReset, ReadAt: now, Source: "harness"},
				{WindowKind: "5h", WindowMinutes: 300, UsedPercent: 99, ResetsAt: now.Add(time.Hour), ReadAt: now, Source: "harness"},
			}
			return ingestReadings(ctx, tx, runner, a.ID, readings)
		})
		if err != nil {
			t.Fatal(err)
		}
		accounts = append(accounts, a)
	}
	// A different owner's shared-quota door withholds the owned door's usage.
	var peer Account
	callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts", `{"account_key":"peer","harness":"codex","daemon_id":"daily-daemon","label":"Peer"}`, 201, &peer)
	ownFixtureAccount(t, other, &peer)
	if _, err := adminPool.Exec(ctx, `UPDATE agent_accounts SET quota_fingerprint=repeat('f',64),quota_pool_fingerprint=repeat('f',64) WHERE id=$1`, peer.ID); err != nil {
		t.Fatal(err)
	}
	read := func(p tenant.Principal, at time.Time) agentplan.Snapshot {
		t.Helper()
		clockCtx := context.WithValue(dbtest.Seed(ctx), clockKey{}, at)
		var out agentplan.Snapshot
		if err := db.InTenant(clockCtx, appPool, owner.TenantID, func(tx pgx.Tx) error { var err error; out, err = ReadPlanTx(clockCtx, tx, p); return err }); err != nil {
			t.Fatal(err)
		}
		return out
	}
	out := read(owner, now)
	if out.DailyTimezone != "Europe/Vienna" || !out.DailyUntil.Equal(end) || out.DailyDefaultPoints != 10 {
		t.Fatal("lost local daily context")
	}
	byID := map[string]agentplan.DailyAccount{}
	for _, d := range out.DailyState {
		for _, a := range d.Accounts {
			byID[a.AccountID] = a
		}
	}
	first := byID[accounts[0].ID]
	if first.UsedPct == nil || first.LeftPct == nil || first.StartOfDayUsedPct == nil || first.LimitUsedPct == nil || first.ResetsAt == nil || *first.UsedPct != 52 || *first.LeftPct != 48 || *first.StartOfDayUsedPct != 40 || *first.LimitUsedPct != 50 || !first.ResetsAt.Equal(weeklyReset) {
		t.Fatalf("wrong weekly daily reading %s", encoded(t, first))
	}
	if private := byID[accounts[2].ID]; !private.DetailsRedacted || private.UsedPct != nil || private.FloorPct != nil || private.CanEdit || private.Routable {
		t.Fatalf("private quota exposed %+v", private)
	}
	if _, exists := byID[peer.ID]; exists {
		t.Fatal("plan scope disclosed another owner's account")
	}
	if out.DailyState["codex"].State != "on_pace" {
		t.Fatalf("one exhausted door exhausted harness: %+v", out.DailyState["codex"])
	}
	claude := byID[accounts[3].ID]
	if *claude.LimitUsedPct != 55 || out.Daily["claude"].Pace.PointsPerDay == nil || *out.Daily["claude"].Pace.PointsPerDay != 5 || out.Daily["claude"].BoostToday == nil {
		t.Fatal("legacy posture/Boost migration lost")
	}
	if out.DailyState["cursor"].State != "no_limit" {
		t.Fatal("payg acquired daily limit")
	}
	// Boost ownership is rechecked in the write transaction and cannot expose
	// or change a private peer's shared allowance through an owned door.
	boost := agentplan.DefaultDaily()
	boost.BoostToday = &agentplan.DailyBoost{LimitUsedPct: 80, EnteredAs: "used", Until: end}
	err = db.InTenant(dbtest.Seed(ctx), appPool, owner.TenantID, func(tx pgx.Tx) error {
		return ValidateDailyWriteTx(ctx, tx, owner, owner.ID, agentplan.Plan{Daily: map[string]agentplan.DailySettings{"codex": boost}}, agentplan.Default(), now)
	})
	if !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("private-quota boost rejection: %v", err)
	}
	// A key creator reads exactly its owner's daily facts, never the daemon's.
	reader := addPrincipal(t, owner.TenantID, "agent", "Plan reader", nil)
	reader.KeyCreatorID = owner.ID
	reader.Scopes = []string{agentplan.ReadScope}
	var role string
	if err := adminPool.QueryRow(ctx, `INSERT INTO roles(tenant_id,key,name) VALUES($1,'daily_reader','Daily reader') RETURNING id::text`, owner.TenantID).Scan(&role); err != nil {
		t.Fatal(err)
	}
	if _, err := adminPool.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,$3)`, owner.TenantID, role, agentplan.ReadScope); err != nil {
		t.Fatal(err)
	}
	if _, err := adminPool.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1,$2,$3,'workspace')`, owner.TenantID, reader.ID, role); err != nil {
		t.Fatal(err)
	}
	agentOut := read(reader, now)
	rawOwner, _ := json.Marshal(out.DailyState)
	rawAgent, _ := json.Marshal(agentOut.DailyState)
	// can_edit is intentionally false for the read-only agent.
	for h, d := range out.DailyState {
		for i := range d.Accounts {
			d.Accounts[i].CanEdit = false
		}
		out.DailyState[h] = d
	}
	rawOwner, _ = json.Marshal(out.DailyState)
	if string(rawOwner) != string(rawAgent) {
		t.Fatal("creator delegation lost daily facts")
	}
	stale := read(owner, now.Add(ProbeFreshness+time.Second))
	if stale.DailyState["codex"].State != "at_limit" {
		t.Fatal("stale usage implied headroom")
	}
	tomorrow := read(owner, end)
	if tomorrow.Daily["claude"].BoostToday != nil {
		t.Fatal("legacy boost survived midnight")
	}
	for _, a := range tomorrow.DailyState["codex"].Accounts {
		if a.StartOfDayUsedPct != nil {
			t.Fatal("previous local day baseline carried forward")
		}
	}
}
