// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentplan"
	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risk: daily fallback invents a provider order, ignores wait or unknown
// readings, or admits a successor outside the account's shared qualification.
func TestDailyModelsUseRankedQualifiedSuccessorsAndRespectWait(t *testing.T) {
	prefsFixture(t, func(tx pgx.Tx, p tenant.Principal) error {
		ctx := t.Context()
		now, err := dbNow(ctx, tx)
		if err != nil {
			return err
		}
		start, end, err := agentplan.LocalDay(now, "UTC")
		if err != nil {
			return err
		}
		insert := func(slug, harness, family, model string) (Profile, error) {
			return insertProfile(ctx, tx, p.TenantID, profileWrite{Slug: slug, Version: "1", Harness: harness, Family: family, Model: model, Effort: "xhigh", Tier: "strong"})
		}
		pin, err := insert("daily-sol-pin", "codex", "openai", "gpt-6-sol")
		if err != nil {
			return err
		}
		next, err := insert("daily-sol-next", "codex", "openai", "gpt-6.10-sol")
		if err != nil {
			return err
		}
		claudePin, err := insert("daily-opus-pin", "claude", "anthropic", "claude-opus-4-7")
		if err != nil {
			return err
		}
		// The board prefers the CLI's registered newest-version alias. Reuse
		// that catalog pin so qualification and selection assert the same row.
		var claude Profile
		if err := tx.QueryRow(ctx, `SELECT id::text FROM model_profiles WHERE slug='claude-opus-xhigh' AND version='1'`).Scan(&claude.ID); err != nil {
			return err
		}

		var runner string
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Daily models runner') RETURNING id::text`, p.TenantID).Scan(&runner); err != nil {
			return err
		}
		accounts := map[string]string{}
		for _, spec := range []struct {
			harness, pin string
			used         float64
		}{{"codex", pin.ID, 50}, {"claude", claudePin.ID, 41}} {
			var id string
			if err := tx.QueryRow(ctx, `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,owner_person_id,label,last_probe_at,last_probe_ok,last_daemon_generation,capacity_owner,linked_at,allowed_model_profile_ids)
 VALUES($1,$2,$2,'daily-runner',$3,$4,$2,$5,true,'daily-generation',$4,$6,ARRAY[$7::uuid]) RETURNING id::text`, p.TenantID, spec.harness, runner, p.ID, now, start.Add(-time.Hour), spec.pin).Scan(&id); err != nil {
				return err
			}
			accounts[spec.harness] = id
			if _, err := tx.Exec(ctx, `INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance,used,pace_model,capacity_read_at,capacity_allowed,capacity_kind,capacity_bucket,capacity_source)
 VALUES($1,$2,$3,$4,'percent',100,$5,'unrestricted',$6,true,'weekly','daily','harness')`, p.TenantID, id, start, end.Add(5*24*time.Hour), int(spec.used), now); err != nil {
				return err
			}
			for _, reading := range []struct {
				at   time.Time
				used float64
			}{{start.Add(time.Minute), 40}, {now, spec.used}} {
				if _, err := tx.Exec(ctx, `INSERT INTO account_capacity_readings(tenant_id,account_id,window_kind,bucket,window_minutes,used_percent,resets_at,read_at,source) VALUES($1,$2,'weekly','daily',10080,$3,$4,$5,'harness')`, p.TenantID, id, reading.used, end.Add(5*24*time.Hour), reading.at); err != nil {
					return err
				}
			}
			schedule := capacity.DefaultSchedule()
			schedule.Override = "sprint"
			schedule.Reserve = "off"
			raw, _ := json.Marshal(schedule)
			if _, err := tx.Exec(ctx, `INSERT INTO account_capacity_schedules(tenant_id,principal_id,scope,scope_key,account_id,schedule) VALUES($1,$2,'account',$3::uuid::text,$3,$4)`, p.TenantID, p.ID, id, raw); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE model_role_routes SET priority=priority+100 WHERE role='build'`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO model_role_routes(tenant_id,role,priority,profile_id) VALUES($1,'build',1,$2),($1,'build',2,$3)`, p.TenantID, next.ID, claude.ID); err != nil {
			return err
		}
		settings := agentplan.DefaultDaily()
		save := func() error {
			raw, err := json.Marshal(agentplan.Plan{Total: 5, Limits: map[string]agentplan.Limit{}, Daily: map[string]agentplan.DailySettings{"codex": settings, "claude": agentplan.DefaultDaily()}})
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `INSERT INTO user_preferences(tenant_id,principal_id,key,value) VALUES($1,$2,'agents.working',$3) ON CONFLICT(tenant_id,principal_id,key) DO UPDATE SET value=EXCLUDED.value`, p.TenantID, p.ID, raw)
			return err
		}
		if err := save(); err != nil {
			return err
		}
		for _, board := range []bool{false, true} {
			if board {
				var id string
				if err := tx.QueryRow(ctx, `INSERT INTO model_pref_profiles(tenant_id,scope,template,thinking,usage,revision) VALUES($1,'workspace','balanced','deep','balanced',1) RETURNING id::text`, p.TenantID).Scan(&id); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `INSERT INTO model_pref_orders(tenant_id,profile_id,column_key,situation,rank) VALUES($1,$2,'other','first',ARRAY['openai:sol','anthropic:opus'])`, p.TenantID, id); err != nil {
					return err
				}
			}
			settings.AtLimit = "ladder"
			if err := save(); err != nil {
				return err
			}
			got, err := ResolveWork(ctx, tx, p, WorkQuery{Role: "build", Area: "other", Situation: "first"}, now)
			if err != nil {
				return err
			}
			if got.Profile == nil || got.Profile.ID != claude.ID || !slices.Equal(got.Trace.QualifyingAccountIDs, []string{accounts["claude"]}) {
				t.Fatalf("board=%v wrong ranked successor: %+v", board, got)
			}
			found := false
			for _, step := range got.Ladder {
				if step.ProfileID == next.ID && slices.Contains(step.SkipReasons, "daily_limit") {
					found = true
				}
			}
			if !found {
				t.Fatal("missing skipped-cap evidence")
			}
			settings.AtLimit = "wait"
			if err := save(); err != nil {
				return err
			}
			got, err = ResolveWork(ctx, tx, p, WorkQuery{Role: "build", Area: "other", Situation: "first"}, now)
			if err != nil {
				return err
			}
			if got.Profile != nil || got.Trace.Blocked != "daily_limit" {
				t.Fatalf("wait became successor: %+v", got)
			}
		}
		settings.AtLimit = "ladder"
		if err := save(); err != nil {
			return err
		}
		// A cap alone is not a reason to override an explicit harness filter.
		got, err := ResolveWork(ctx, tx, p, WorkQuery{Role: "build", Harness: "codex"}, now)
		if err != nil {
			return err
		}
		if got.Profile != nil || got.Trace.Blocked != "daily_limit" {
			t.Fatal("explicit harness switched")
		}
		if _, err := tx.Exec(ctx, `UPDATE agent_accounts SET allowed_model_profile_ids='{}' WHERE id=$1`, accounts["claude"]); err != nil {
			return err
		}
		got, err = ResolveWork(ctx, tx, p, WorkQuery{Role: "build", Area: "other", Situation: "first"}, now)
		if err != nil {
			return err
		}
		if got.Profile != nil {
			t.Fatal("unqualified successor selected")
		}
		if _, err := tx.Exec(ctx, `UPDATE user_preferences SET value='{"total":5,"daily":{"codex":null}}' WHERE principal_id=$1 AND key='agents.working'`, p.ID); err != nil {
			return err
		}
		got, err = ResolveWork(ctx, tx, p, WorkQuery{Role: "build", Area: "other", Situation: "first"}, now)
		if err != nil {
			return err
		}
		if got.Profile != nil || got.Trace.Blocked != "daily_limit_unknown" {
			t.Fatalf("unreadable plan selected successor: %+v", got)
		}
		return nil
	})
}
