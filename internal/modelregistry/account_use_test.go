// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/agentplan"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risk C1: the first DailyStart counts denied headroom or unknown readings;
// an all-denied harness obeys a daily wait instead of skipping context denial.
func TestResolverProjectDailyCapsAndContextLadder(t *testing.T) {
	for _, tc := range []struct {
		name                               string
		unknown, allDenied, unknownAllowed bool
		atLimit, pinned, want              string
	}{
		{"a denied headroom ladder", false, false, false, "ladder", "", "claude"},
		{"a denied headroom wait", false, false, false, "wait", "", "daily_limit"},
		{"b denied unknown", true, false, false, "ladder", "", "claude"},
		{"c all denied ignores wait", false, true, false, "wait", "", "claude"},
		{"c all denied pinned", false, true, false, "wait", "codex", "context"},
		{"d allowed unknown", false, false, true, "ladder", "", "daily_limit_unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
				var runner string
				if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Context caps runner') RETURNING id::text`, p.TenantID).Scan(&runner); err != nil {
					return err
				}
				profiles := map[string]Profile{}
				for _, spec := range []struct{ harness, family, model string }{{"codex", "openai", "gpt-6-sol"}, {"claude", "anthropic", "claude-opus-4-7"}} {
					pr, err := insertProfile(ctx, tx, p.TenantID, profileWrite{Slug: "context-" + spec.harness, Version: "1", Harness: spec.harness, Family: spec.family, Model: spec.model, Effort: "xhigh", Tier: "strong"})
					if err != nil {
						return err
					}
					profiles[spec.harness] = pr
				}
				accounts := map[string]string{}
				for _, spec := range []struct {
					key, harness string
					used         float64
				}{{"allowed", "codex", 50}, {"denied", "codex", 1}, {"next", "claude", 1}} {
					var id string
					if err := tx.QueryRow(ctx, `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,owner_person_id,label,last_probe_at,last_probe_ok,last_daemon_generation,capacity_owner,linked_at,allowed_model_profile_ids) VALUES($1,$2,$3,'context-runner',$4,$5,$2,$6,true,'context-generation',$5,$7,ARRAY[$8::uuid]) RETURNING id::text`, p.TenantID, spec.key, spec.harness, runner, p.ID, now, start.Add(-time.Hour), profiles[spec.harness].ID).Scan(&id); err != nil {
						return err
					}
					accounts[spec.key] = id
					readAt := now
					if tc.unknown && spec.key == "denied" || tc.unknownAllowed && spec.key == "allowed" {
						readAt = now.Add(-3 * time.Minute)
					}
					if _, err := tx.Exec(ctx, `INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance,used,pace_model,capacity_read_at,capacity_allowed,capacity_kind,capacity_bucket,capacity_source) VALUES($1,$2,$3,$4,'percent',100,$5,'unrestricted',$6,true,'weekly','context','harness')`, p.TenantID, id, start, end.Add(5*24*time.Hour), int(spec.used), readAt); err != nil {
						return err
					}
					for _, reading := range []struct {
						at   time.Time
						used float64
					}{{start.Add(time.Minute), 0}, {readAt, spec.used}} {
						if _, err := tx.Exec(ctx, `INSERT INTO account_capacity_readings(tenant_id,account_id,window_kind,bucket,window_minutes,used_percent,resets_at,read_at,source) VALUES($1,$2,'weekly','context',10080,$3,$4,$5,'harness')`, p.TenantID, id, reading.used, end.Add(5*24*time.Hour), reading.at); err != nil {
							return err
						}
					}
				}
				if _, err := tx.Exec(ctx, `DELETE FROM account_use_cells WHERE account_id=$1`, accounts["denied"]); err != nil {
					return err
				}
				if tc.allDenied {
					if _, err := tx.Exec(ctx, `DELETE FROM account_use_cells WHERE account_id=$1`, accounts["allowed"]); err != nil {
						return err
					}
				}
				d := agentplan.DefaultDaily()
				d.AtLimit = tc.atLimit
				d.BoostToday = &agentplan.DailyBoost{LimitUsedPct: 50, EnteredAs: "used", Until: end}
				raw, err := json.Marshal(agentplan.Plan{Total: 5, Daily: map[string]agentplan.DailySettings{"codex": d, "claude": agentplan.DefaultDaily()}})
				if err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `INSERT INTO user_preferences(tenant_id,principal_id,key,value) VALUES($1,$2,'agents.working',$3)`, p.TenantID, p.ID, raw); err != nil {
					return err
				}
				q := WorkQuery{Role: "build", PersonID: &p.ID, Harness: tc.pinned}
				resolve := func(query WorkQuery) (WorkResolution, error) {
					h := "codex"
					if slices.Contains(query.OffHarnesses, h) {
						h = "claude"
					}
					profile := profiles[h]
					return WorkResolution{Resolution: Resolution{Role: "build", Profile: &profile, CommandTemplate: "trusted", Ladder: []Candidate{{ProfileID: profile.ID, Selected: true, SkipReasons: []string{}}}}, Residency: "any", Trace: PreferenceTrace{PersonID: &p.ID, QualifyingAccountIDs: []string{accounts[map[string]string{"codex": "allowed", "claude": "next"}[h]]}}}, nil
				}
				out, err := resolveDailyWith(ctx, tx, p, q, now, resolve)
				if err != nil {
					return err
				}
				if tc.want == "claude" {
					if out.Profile == nil || out.Profile.Harness != "claude" {
						t.Fatalf("wrong successor: %+v", out)
					}
					reason := "daily_limit"
					if tc.allDenied {
						reason = agentaccounts.ContextSkipReason
					}
					found := false
					for _, c := range out.Ladder {
						if slices.Contains(c.SkipReasons, reason) {
							found = true
						}
					}
					if !found {
						t.Fatal("missing skip reason", out.Ladder)
					}
				} else if out.Profile != nil || out.Trace.Blocked != tc.want || out.CommandTemplate != "" {
					t.Fatalf("wrong block: %+v", out)
				}
				return nil
			})
		})
	}
}

// Risk 10: an absent account pool or a fully denied pool returns executable
// launch material instead of a catalog preview or a context skip.
func TestResolverNoPoolPreviewAndDeniedContext(t *testing.T) {
	prefsFixture(t, func(tx pgx.Tx, p tenant.Principal) error {
		ctx := t.Context()
		now := time.Now()
		preview, err := resolveRole(ctx, tx, resolveQuery{Role: "build", Harness: "codex"}, now)
		if err != nil {
			return err
		}
		if preview.Profile == nil || !preview.Preview || preview.CommandTemplate != "" {
			t.Fatal("empty pool became executable", preview)
		}
		var runner, account string
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Denied resolver') RETURNING id::text`, p.TenantID).Scan(&runner); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label,last_probe_at,last_probe_ok) VALUES($1,'denied-resolve','codex','test',$2,'Denied',$3,true) RETURNING id::text`, p.TenantID, runner, now).Scan(&account); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM account_use_cells WHERE account_id=$1`, account); err != nil {
			return err
		}
		denied, err := resolveRole(ctx, tx, resolveQuery{Role: "build", Harness: "codex"}, now)
		if err != nil {
			return err
		}
		if denied.Profile != nil || denied.Preview || denied.CommandTemplate != "" {
			t.Fatal("denied pool became preview/executable", denied)
		}
		found := false
		for _, candidate := range denied.Ladder {
			if slices.Contains(candidate.SkipReasons, agentaccounts.ContextSkipReason) {
				found = true
			}
		}
		if !found {
			t.Fatal("missing context reason", denied.Ladder)
		}
		return nil
	})
}
