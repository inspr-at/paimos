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
		if err := tx.QueryRow(ctx, `SELECT id::text FROM model_profiles WHERE slug='claude-opus-xhigh' AND version=$1`, CatalogVersion).Scan(&claude.ID); err != nil {
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

// Risk: the daily ceiling re-qualifies accounts with the ordinary rules, so
// headroom on a sibling that review would never use (no approved principal)
// keeps an exhausted review route selected.
func TestDailyReviewKeepsReviewQualifiedAccounts(t *testing.T) {
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
		reviewer, err := insertProfile(ctx, tx, p.TenantID, profileWrite{Slug: "daily-review-claude", Version: "1", Harness: "claude", Family: "anthropic", Model: "claude-daily-review", Effort: "xhigh", Tier: "strong"})
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM model_role_routes WHERE role='review-gate'`); err != nil {
			return err
		}
		if err := insertRoute(ctx, tx, p.TenantID, Route{Role: "review-gate", Priority: 1, ProfileID: reviewer.ID, State: "available"}); err != nil {
			return err
		}
		runner := func(name string, approved bool) (string, error) {
			var id string
			if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent',$2) RETURNING id::text`, p.TenantID, name).Scan(&id); err != nil {
				return "", err
			}
			if approved {
				_, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) SELECT $1,$2,id,'workspace' FROM roles WHERE key='admin'`, p.TenantID, id)
				return id, err
			}
			return id, nil
		}
		approved, err := runner("Daily review approved runner", true)
		if err != nil {
			return err
		}
		unapproved, err := runner("Daily review unapproved runner", false)
		if err != nil {
			return err
		}
		addAccount := func(label, registeredBy string, used float64) (string, error) {
			var id string
			if err := tx.QueryRow(ctx, `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,owner_person_id,label,last_probe_at,last_probe_ok,last_daemon_generation,capacity_owner,linked_at,allowed_model_profile_ids)
 VALUES($1,$2,'claude','daily-review-runner',$3,$4,$2,$5,true,'daily-review-generation',$4,$6,ARRAY[$7::uuid]) RETURNING id::text`, p.TenantID, label, registeredBy, p.ID, now, start.Add(-time.Hour), reviewer.ID).Scan(&id); err != nil {
				return "", err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance,used,pace_model,capacity_read_at,capacity_allowed,capacity_kind,capacity_bucket,capacity_source)
 VALUES($1,$2,$3,$4,'percent',100,$5,'unrestricted',$6,true,'weekly','daily','harness')`, p.TenantID, id, start, end.Add(5*24*time.Hour), int(used), now); err != nil {
				return "", err
			}
			for _, reading := range []struct {
				at   time.Time
				used float64
			}{{start.Add(time.Minute), 40}, {now, used}} {
				if _, err := tx.Exec(ctx, `INSERT INTO account_capacity_readings(tenant_id,account_id,window_kind,bucket,window_minutes,used_percent,resets_at,read_at,source) VALUES($1,$2,'weekly','daily',10080,$3,$4,$5,'harness')`, p.TenantID, id, reading.used, end.Add(5*24*time.Hour), reading.at); err != nil {
					return "", err
				}
			}
			schedule := capacity.DefaultSchedule()
			for i := range schedule.Week {
				schedule.Week[i] = capacity.Day{On: true, Start: 0, End: 24}
			}
			schedule.Override = "sprint"
			schedule.Reserve = capacity.ReserveOff
			raw, _ := json.Marshal(schedule)
			_, err := tx.Exec(ctx, `INSERT INTO account_capacity_schedules(tenant_id,principal_id,scope,scope_key,account_id,schedule) VALUES($1,$2,'account',$3::uuid::text,$3,$4)`, p.TenantID, p.ID, id, raw)
			return id, err
		}
		// 50 % used with 40 % at the start of the day reaches the ten-point pace.
		exhausted, err := addAccount("review-exhausted", approved, 50)
		if err != nil {
			return err
		}
		sibling, err := addAccount("review-unapproved-room", unapproved, 41)
		if err != nil {
			return err
		}
		resolve := func(atLimit string) (WorkResolution, error) {
			settings := agentplan.DefaultDaily()
			settings.AtLimit = atLimit
			raw, err := json.Marshal(agentplan.Plan{Total: 5, Limits: map[string]agentplan.Limit{}, Daily: map[string]agentplan.DailySettings{"claude": settings}})
			if err != nil {
				return WorkResolution{}, err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO user_preferences(tenant_id,principal_id,key,value) VALUES($1,$2,'agents.working',$3) ON CONFLICT(tenant_id,principal_id,key) DO UPDATE SET value=EXCLUDED.value`, p.TenantID, p.ID, raw); err != nil {
				return WorkResolution{}, err
			}
			return ResolveWork(ctx, tx, p, WorkQuery{Role: "review-gate", AuthorFamily: "openai"}, now)
		}
		// The only review-qualified account is exhausted; room on the unapproved sibling must not count.
		for _, atLimit := range []string{"ladder", "wait"} {
			got, err := resolve(atLimit)
			if err != nil {
				return err
			}
			if got.Profile != nil || got.Trace.Blocked != "daily_limit" || len(got.Trace.QualifyingAccountIDs) != 0 {
				t.Fatalf("%s: headroom on an account review does not qualify kept the route: %+v", atLimit, got)
			}
		}
		// A review-qualified account with headroom is the one that keeps the route.
		room, err := addAccount("review-approved-room", approved, 41)
		if err != nil {
			return err
		}
		got, err := resolve("wait")
		if err != nil {
			return err
		}
		if got.Profile == nil || got.Profile.ID != reviewer.ID || !slices.Equal(got.Trace.QualifyingAccountIDs, []string{room}) {
			t.Fatalf("review route is not limited to its qualified account with room: %+v (exhausted %s, sibling %s, room %s)", got, exhausted, sibling, room)
		}
		return nil
	})
}
