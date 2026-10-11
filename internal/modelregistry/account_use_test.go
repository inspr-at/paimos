// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/agentplan"
	"github.com/inspr-at/paimos/internal/capacity"
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
				raw, err := json.Marshal(agentplan.Plan{Total: 5, Limits: map[string]agentplan.Limit{}, Daily: map[string]agentplan.DailySettings{"codex": d, "claude": agentplan.DefaultDaily()}})
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
					reason := "Codex account is at its daily cap until " + end.UTC().Format(time.RFC3339)
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

// Risk: an accountless first board line stops selection before a later line
// with an allowed account, leaving dispatch with an unlaunchable preview.
func TestBoardSkipsAccountlessHarnessForRunnableSuccessor(t *testing.T) {
	prefsFixture(t, func(tx pgx.Tx, p tenant.Principal) error {
		ctx := t.Context()
		now, err := dbNow(ctx, tx)
		if err != nil {
			return err
		}
		account, successor := accountlessSuccessorFixture(t, tx, p, now)
		var board string
		if err := tx.QueryRow(ctx, `INSERT INTO model_pref_profiles(tenant_id,scope,template,thinking,usage,set_by) VALUES($1,'workspace','balanced','max','balanced',$2) RETURNING id::text`, p.TenantID, p.ID).Scan(&board); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO model_pref_orders(tenant_id,profile_id,column_key,situation,rank,effort,set_by) VALUES($1,$2,'backend','first',ARRAY['anthropic:opus','openai:astra'],'xhigh',$3)`, p.TenantID, board, p.ID); err != nil {
			return err
		}
		out, err := resolveBoardWork(ctx, tx, p, WorkQuery{Role: "build", Area: "backend"}, now, nil)
		if err != nil {
			return err
		}
		if out == nil {
			t.Fatal("fixture did not adopt the board")
		}
		assertRunnableSuccessor(t, *out, account, successor, "no qualified account with available capacity")
		if out.Trace.CardIndex != 2 {
			t.Fatalf("board did not continue to the second line: %+v", out.Trace)
		}
		return nil
	})
}

// Risk: an accountless stronger route hides the next qualified escalation
// route instead of letting it proceed to live admission.
func TestEscalationSkipsAccountlessHarnessForRunnableSuccessor(t *testing.T) {
	prefsFixture(t, func(tx pgx.Tx, p tenant.Principal) error {
		ctx := t.Context()
		now, err := dbNow(ctx, tx)
		if err != nil {
			return err
		}
		account, successor := accountlessSuccessorFixture(t, tx, p, now)
		if _, err := tx.Exec(ctx, `DELETE FROM model_role_routes WHERE role='build-hard'`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO model_role_routes(tenant_id,role,profile_id,priority) SELECT $1,'build-hard',id,CASE slug WHEN 'claude-opus-xhigh' THEN 1 ELSE 2 END FROM model_profiles WHERE slug IN ('claude-opus-xhigh','codex-astra-xhigh')`, p.TenantID); err != nil {
			return err
		}
		out, err := ResolveEscalation(ctx, tx, p, WorkQuery{Role: "build", Area: "backend"}, nil, now)
		if err != nil {
			return err
		}
		assertRunnableSuccessor(t, out, account, successor, "no qualified account; wait for live admission")
		if len(out.Ladder) != 2 {
			t.Fatalf("fixture did not exercise the two-route ladder: %+v", out.Ladder)
		}
		return nil
	})
}

func accountlessSuccessorFixture(t *testing.T, tx pgx.Tx, p tenant.Principal, now time.Time) (account, profile string) {
	t.Helper()
	ctx := t.Context()
	if err := tx.QueryRow(ctx, `SELECT id::text FROM model_profiles WHERE slug='codex-astra-xhigh'`).Scan(&profile); err != nil {
		t.Fatal(err)
	}
	var runner string
	if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Successor runner') RETURNING id::text`, p.TenantID).Scan(&runner); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label,last_probe_at,last_probe_ok,last_daemon_generation,capacity_owner,allowed_model_profile_ids) VALUES($1,'successor-codex','codex','successor-runner',$2,'Successor',$3,true,'generation',$4,ARRAY[$5::uuid]) RETURNING id::text`, p.TenantID, runner, now, p.ID, profile).Scan(&account); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO account_use_cells(tenant_id,account_id,context_id,source,set_by) SELECT $1,$2,id,'person',$3 FROM work_contexts WHERE kind='default' ON CONFLICT DO NOTHING`, p.TenantID, account, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance,pace_model,capacity_kind,capacity_source,capacity_read_at) VALUES($1,$2,$3::timestamptz-interval '1 hour',$3::timestamptz+interval '1 day','requests',1000,'unrestricted','5h','agentd',$3)`, p.TenantID, account, now); err != nil {
		t.Fatal(err)
	}
	schedule := capacity.DefaultSchedule()
	schedule.Override = "sprint"
	schedule.Reserve = capacity.ReserveOff
	raw, err := json.Marshal(schedule)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO account_capacity_schedules(tenant_id,principal_id,scope,scope_key,account_id,schedule) VALUES($1,$2,'account',$3::uuid::text,$3,$4)`, p.TenantID, p.ID, account, raw); err != nil {
		t.Fatal(err)
	}
	return account, profile
}

func assertRunnableSuccessor(t *testing.T, out WorkResolution, account, profile, reason string) {
	t.Helper()
	if out.Profile == nil || out.Profile.ID != profile || out.Preview || out.CommandTemplate == "" || out.OwnerRequired || out.Trace.Blocked != "" || !slices.Equal(out.Trace.QualifyingAccountIDs, []string{account}) {
		t.Fatalf("accountless harness prevented runnable successor: %+v", out)
	}
	if len(out.Ladder) < 2 || out.Ladder[0].Selected || !slices.Contains(out.Ladder[0].SkipReasons, reason) {
		t.Fatalf("accountless first route was not skipped for missing capacity: %+v", out.Ladder)
	}
	selected := 0
	for _, candidate := range out.Ladder {
		if candidate.Selected {
			selected++
			if candidate.ProfileID != profile || len(candidate.SkipReasons) != 0 {
				t.Fatalf("selected route was not the qualified successor: %+v", candidate)
			}
		}
	}
	if selected != 1 {
		t.Fatalf("expected one selected successor, got %d: %+v", selected, out.Ladder)
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
		if err := tx.QueryRow(ctx, `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label,last_probe_at,last_probe_ok,last_daemon_generation) VALUES($1,'denied-resolve','codex','test',$2,'Denied',$3,true,'generation') RETURNING id::text`, p.TenantID, runner, now).Scan(&account); err != nil {
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
