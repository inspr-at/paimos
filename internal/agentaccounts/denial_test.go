// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Each checkpoint is tested before and after a daemon restart, for both
// reservation and claim, including Run now. The clock never uses wall time.
func TestDenialAdmissionInterleavings(t *testing.T) {
	type step struct {
		kind        string
		minute, end int
		wait        int
		grant       string
	}
	for _, tt := range []struct {
		name  string
		steps []step
	}{
		{"named only", []step{{"named", 0, 60, 60, ""}, {"check", 59, 0, 60, ""}, {"check", 60, 0, 0, "unknown:"}}},
		{"unnamed only", []step{{"unnamed", 0, 0, 60, ""}, {"check", 59, 0, 60, ""}, {"check", 60, 0, 0, "recover:"}}},
		{"named then unnamed", []step{{"named", 0, 180, 180, ""}, {"unnamed", 30, 0, 180, ""}, {"check", 90, 0, 180, ""}, {"check", 180, 0, 0, "recover:"}}},
		{"named then longer unnamed", []step{{"named", 0, 30, 30, ""}, {"unnamed", 20, 0, 80, ""}, {"check", 30, 0, 80, ""}, {"check", 80, 0, 0, "recover:"}}},
		{"unnamed then named", []step{{"unnamed", 0, 0, 60, ""}, {"named", 30, 50, 60, ""}, {"check", 50, 0, 60, ""}, {"check", 60, 0, 0, "recover:"}}},
		{"unnamed then longer named", []step{{"unnamed", 0, 0, 60, ""}, {"named", 30, 180, 180, ""}, {"check", 60, 0, 180, ""}, {"check", 180, 0, 0, "recover:"}}},
		{"expired named then unnamed", []step{{"named", 0, 60, 60, ""}, {"check", 60, 0, 0, "unknown:"}, {"unnamed", 181, 0, 241, ""}, {"check", 185, 0, 241, ""}, {"check", 241, 0, 0, "recover:"}}},
		// Gate reproduction: reset 21:00, recovery 23:00 fails at 23:01,
		// restart at 23:05. The required wait ends at 00:01, not 21:00.
		{"failed recovery then restart", []step{{"named", 0, 60, 60, ""}, {"failure", 180, 181, 241, ""}, {"check", 185, 0, 241, ""}, {"check", 241, 0, 0, "recover:"}}},
		{"successful recovery then unnamed", []step{{"named", 0, 60, 60, ""}, {"success", 61, 62, 0, "unknown:"}, {"unnamed", 181, 0, 241, ""}, {"check", 241, 0, 0, "recover:"}}},
		{"unnamed named recovery success", []step{{"unnamed", 0, 0, 60, ""}, {"named", 30, 90, 90, ""}, {"success", 91, 92, 0, "unknown:"}, {"unnamed", 100, 0, 160, ""}, {"check", 160, 0, 0, "recover:"}}},
		{"unnamed named recovery failure", []step{{"unnamed", 0, 0, 60, ""}, {"named", 30, 90, 90, ""}, {"failure", 91, 92, 212, ""}, {"check", 212, 0, 0, "recover:"}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newDenialFixture(t)
			base := time.Date(2026, 9, 29, 20, 0, 0, 0, time.UTC)
			at := func(minute int) time.Time { return base.Add(time.Duration(minute) * time.Minute) }
			for _, s := range tt.steps {
				now := at(s.minute)
				switch s.kind {
				case "named":
					f.named(now, at(s.end))
				case "unnamed":
					f.stop(now)
				case "success", "failure":
					outcome := "vendor_limit"
					if s.kind == "success" {
						outcome = "completed"
					}
					f.recover(now, at(s.end), outcome)
					now = at(s.end)
				}
				for restart := 0; restart < 2; restart++ {
					if restart == 1 {
						f.restart()
					}
					if s.wait != 0 {
						f.assertWait(now, at(s.wait))
					} else {
						windows, wait := f.admit(now, 0, false)
						if wait != nil || len(windows) != 1 || !strings.HasPrefix(windows[0].capacityBucket, s.grant) {
							t.Fatalf("%s at %s restart=%d: wait=%+v windows=%+v", s.kind, now, restart, wait, windows)
						}

					}
				}
			}
			// Only unnamed recovery is serialized; named resets restore normal slots.
			last := tt.steps[len(tt.steps)-1]
			now := at(last.minute)
			if last.grant == "recover:" {
				if _, w := f.admit(now, 1, false); w == nil || w.Code != "capacity" {
					t.Fatalf("concurrent recovery: %+v", w)
				}
			} else {
				if _, w := f.admit(now, 1, false); w != nil {
					t.Fatalf("unknown usage serial fence: %+v", w)
				}
			}
			f.recover(now, now.Add(time.Second), "failed")
			_, before := f.admit(now.Add(time.Second), 0, false)
			f.restart()
			_, after := f.admit(now.Add(time.Second), 0, false)
			if last.grant == "recover:" {
				if before == nil || after == nil || before.Code != "vendor" || after.Code != "vendor" || before.Until == nil || after.Until == nil || !before.Until.Equal(*after.Until) {
					t.Fatalf("restart renewed failed recovery: %+v %+v", before, after)
				}
			} else if before != nil || after != nil {
				t.Fatalf("unknown completion introduced a quota: %+v %+v", before, after)
			}

		})
	}
}

func TestAdmissionNeverGrantsDuringUnexpiredDenial(t *testing.T) {
	for seed := int64(0); seed < 12; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			f := newDenialFixture(t)
			rng := rand.New(rand.NewSource(seed))
			now := time.Date(2026, 9, 29, 20, 0, 0, 0, time.UTC)
			var deadlines []time.Time
			var retry time.Time
			step := 0
			for i := 0; i < 60; i++ {
				now = now.Add(time.Duration(1+rng.Intn(40)) * time.Minute)
				switch rng.Intn(5) {
				case 0:
					until := now.Add(time.Duration(1+rng.Intn(120)) * time.Minute)
					f.named(now, until)
					deadlines = append(deadlines, until)
				case 1:
					if retry.IsZero() {
						f.stop(now)
						retry = now.Add(time.Hour)
						step = 0
					}
				case 2:
					f.restart()
				case 3:
					windows, wait := f.admit(now, 0, false)
					if wait == nil && containsRecovery(windows) {
						for _, deadline := range append(deadlines, retry) {
							if deadline.After(now) {
								t.Fatalf("recovery before oracle deadline: %s < %s", now, deadline)
							}
						}
						success := rng.Intn(2) == 0
						outcome := "failed"
						if success {
							outcome = "completed"
						}
						f.recover(now, now.Add(time.Second), outcome)
						now = now.Add(time.Second)
						if success {
							retry = time.Time{}
							step = 0
						} else {
							step = min(step+1, 3)
							retry = now.Add(time.Hour << step)
						}
					}
				}
				latest := retry
				for _, deadline := range deadlines {
					if deadline.After(latest) {
						latest = deadline
					}
				}
				if latest.After(now) {
					f.assertWait(now, latest)
				}
			}
		})
	}
}

func TestClearingOneNamedBucketPreservesAnother(t *testing.T) {
	f := newDenialFixture(t)
	now := time.Date(2026, 9, 29, 20, 0, 0, 0, time.UTC)
	f.named(now, now.Add(3*time.Hour))
	// A run that was already in flight clears the older bucket. Its start
	// precedes the second denial, which still has to wait despite its earlier reset.
	run := insertRun(t, f.person, f.runner, f.profile)
	f.seed(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET account_id=$2,status='completed',started_at=$3 WHERE id=$1`, run, f.account.ID, now.Add(30*time.Minute))
		return err
	})
	f.named(now.Add(time.Hour), now.Add(2*time.Hour))
	f.assertWait(now.Add(61*time.Minute), now.Add(3*time.Hour))
	f.restart()
	f.assertWait(now.Add(61*time.Minute), now.Add(3*time.Hour))
	f.seed(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO account_capacity_readings(tenant_id,account_id,window_kind,bucket,window_minutes,used_percent,resets_at,read_at,source,ordinary_usage_allowed) VALUES($1,$2,'5h',$3,300,10,$4,$5,'harness',true)`, f.person.TenantID, f.account.ID, now.Format(time.RFC3339Nano), now.Add(3*time.Hour), now.Add(62*time.Minute))
		return err
	})
	f.assertWait(now.Add(63*time.Minute), now.Add(2*time.Hour))

}

type denialFixture struct {
	t              *testing.T
	person, runner tenant.Principal
	account        Account
	profile, token string
	generation     int
}

func newDenialFixture(t *testing.T) *denialFixture {
	t.Helper()
	reset(t)
	f := &denialFixture{t: t}
	f.person = makePrincipal(t, "denial-order", "person", "Ada", []string{"admin"})
	f.runner = addPrincipal(t, f.person.TenantID, "agent", "runner", nil)
	f.profile = codexProfile(t, f.person)
	f.token = issueKey(t, f.runner, []string{"account.manage", "account.probe", "run.claim"})
	callStatus(t, accountsMod(), &f.runner, f.token, "POST", "/api/agent-accounts", `{"account_key":"denials","harness":"codex","daemon_id":"daemon-a","label":"Main","max_parallel_runs":2}`, 201, &f.account)
	f.restart()
	s := capacity.DefaultSchedule()
	for i := range s.Week {
		s.Week[i] = capacity.Day{On: true, Start: 0, End: 24}
	}
	callStatus(t, accountsMod(), &f.person, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, scheduleOverride{Scope: "account", AccountID: f.account.ID, Schedule: &s}), 204, nil)
	return f
}

func (f *denialFixture) seed(fn func(pgx.Tx) error) {
	f.t.Helper()
	if err := db.InTenant(dbtest.Seed(f.t.Context()), appPool, f.person.TenantID, fn); err != nil {
		f.t.Fatal(err)
	}
}

func (f *denialFixture) restart() {
	f.t.Helper()
	f.generation++
	callStatus(f.t, accountsMod(), &f.runner, f.token, "POST", "/api/agent-accounts/"+f.account.ID+"/probe", encoded(f.t, map[string]any{"daemon_id": "daemon-a", "daemon_generation": fmt.Sprint("g", f.generation), "available": true}), 200, nil)
}

func (f *denialFixture) named(at, until time.Time) {
	f.t.Helper()
	f.seed(func(tx pgx.Tx) error {
		_, err := tx.Exec(f.t.Context(), `INSERT INTO account_capacity_readings(tenant_id,account_id,window_kind,bucket,window_minutes,used_percent,resets_at,read_at,source,ordinary_usage_allowed)
 VALUES($1,$2,'5h',$3,300,100,$4,$5,'harness',false)`, f.person.TenantID, f.account.ID, at.Format(time.RFC3339Nano), until, at)
		return err
	})
}

func (f *denialFixture) stop(at time.Time) {
	f.t.Helper()
	run := insertRun(f.t, f.person, f.runner, f.profile)
	f.seed(func(tx pgx.Tx) error {
		if _, err := tx.Exec(f.t.Context(), `UPDATE agent_runs SET account_id=$2,status='failed' WHERE id=$1`, run, f.account.ID); err != nil {
			return err
		}
		_, err := tx.Exec(f.t.Context(), `INSERT INTO run_telemetry(tenant_id,run_id,sequence,kind,error_code,at) VALUES($1,$2,1,'usage','vendor_limit',$3)`, f.person.TenantID, run, at)
		if err != nil {
			return err
		}
		a, err := getAccount(f.t.Context(), tx, f.account.ID)
		if err != nil {
			return err
		}
		return reconcileVendorStop(tenant.WithPrincipal(f.t.Context(), f.runner), tx, a, at)
	})
}

func (f *denialFixture) admit(now time.Time, slots int, claiming bool) ([]Window, *CapacityWait) {
	f.t.Helper()
	var windows []Window
	var wait *CapacityWait
	f.seed(func(tx pgx.Tx) error {
		a, err := getAccount(f.t.Context(), tx, f.account.ID)
		if err != nil {
			return err
		}
		a.LastProbeAt = &now
		windows, wait, err = admission(f.t.Context(), tx, a, a.Windows, now, slots, runRow{Purpose: "managed", CapacityOverride: "now"}, claiming)
		return err
	})
	return windows, wait
}

func (f *denialFixture) assertWait(now, until time.Time) {
	f.t.Helper()
	for _, claiming := range []bool{false, true} {
		windows, wait := f.admit(now, 0, claiming)
		if len(windows) != 0 || wait == nil || wait.Code != "vendor" || wait.RunNowAllowed || wait.Until == nil || !wait.Until.Equal(until) {
			f.t.Fatalf("at=%s generation=%d claiming=%v: windows=%+v wait=%+v, want vendor until %s", now, f.generation, claiming, windows, wait, until)
		}
	}
}

func (f *denialFixture) recover(start, finish time.Time, outcome string) {
	f.t.Helper()
	mod := fixedClockModule{Module: New(appPool), at: start}
	callStatus(f.t, mod, &f.runner, f.token, "POST", "/api/agent-accounts/"+f.account.ID+"/probe", encoded(f.t, map[string]any{"daemon_id": "daemon-a", "daemon_generation": fmt.Sprint("g", f.generation), "available": true}), 200, nil)
	run := insertRun(f.t, f.person, f.runner, f.profile)
	mustRoute(f.t, mod, f.runner, f.token, run, "daemon-a", []Account{f.account}, map[string]int64{"requests": 1})
	ctx := context.WithValue(tenant.WithPrincipal(dbtest.Seed(f.t.Context()), f.runner), clockKey{}, finish)
	if err := db.InTenant(ctx, appPool, f.person.TenantID, func(tx pgx.Tx) error {
		status := "failed"
		output := 0
		if outcome == "completed" {
			status = "completed"
			output = 1
		}
		if _, err := tx.Exec(ctx, `UPDATE agent_runs SET status=$2,started_at=$3,ended_at=$4 WHERE id=$1`, run, status, start, finish); err != nil {
			return err
		}
		var code *string
		if outcome == "vendor_limit" {
			v := "vendor_limit"
			code = &v
		}
		if _, err := tx.Exec(ctx, `INSERT INTO run_telemetry(tenant_id,run_id,sequence,kind,output_tokens_delta,error_code,at) VALUES($1,$2,1,'usage',$3,$4,$5)`, f.person.TenantID, run, output, code, finish); err != nil {
			return err
		}
		return Settle(ctx, tx, f.runner, run)
	}); err != nil {
		f.t.Fatal(err)
	}
}
