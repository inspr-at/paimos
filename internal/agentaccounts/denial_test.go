// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
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

func TestEffectiveDenial(t *testing.T) {
	now := time.Date(2026, 9, 29, 23, 5, 0, 0, time.UTC)
	named := denial{at: now.Add(-3 * time.Hour), until: now.Add(-125 * time.Minute)}
	unnamed := denial{at: now.Add(-4 * time.Minute), until: now.Add(56 * time.Minute)}
	later := denial{at: now.Add(-time.Minute), until: now.Add(2 * time.Hour)}
	boundary := denial{at: now.Add(-time.Hour), until: now}
	for _, tt := range []struct {
		name           string
		named, unnamed []denial
		until, epoch   time.Time
		recover        bool
	}{
		{name: "none"},
		{name: "expired named", named: []denial{named}, epoch: named.at, recover: true},
		{name: "unnamed only", unnamed: []denial{unnamed}, until: unnamed.until, epoch: unnamed.at},
		{name: "expired named and fresh unnamed", named: []denial{named}, unnamed: []denial{unnamed}, until: unnamed.until, epoch: unnamed.at},
		{name: "named later", named: []denial{later}, unnamed: []denial{unnamed}, until: later.until, epoch: later.at},
		{name: "unnamed later", named: []denial{unnamed}, unnamed: []denial{later}, until: later.until, epoch: later.at},
		{name: "multiple buckets", named: []denial{named, later, unnamed}, until: later.until, epoch: later.at},
		{name: "permuted buckets", named: []denial{unnamed, later, named}, until: later.until, epoch: later.at},
		{name: "exact expiry", unnamed: []denial{boundary}, epoch: boundary.at, recover: true},
		{name: "latest epoch after all expire", named: []denial{named, boundary}, epoch: boundary.at, recover: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := effectiveDenial(now, tt.named, tt.unnamed)
			if got.waiting(now) != !tt.until.IsZero() || got.refreshDue(now) != tt.recover || !got.epoch.Equal(tt.epoch) {
				t.Fatalf("block=%+v, want until=%s epoch=%s recovery=%v", got, tt.until, tt.epoch, tt.recover)
			}
			if !tt.until.IsZero() && (got.until == nil || !got.until.Equal(tt.until)) {
				t.Fatalf("deadline=%v, want %s", got.until, tt.until)
			}
		})
	}
}

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
		{"named only", []step{{"named", 0, 60, 60, ""}, {"check", 59, 0, 60, ""}, {"check", 60, 0, 0, "recover:"}}},
		{"unnamed only", []step{{"unnamed", 0, 0, 60, ""}, {"check", 59, 0, 60, ""}, {"check", 60, 0, 0, "recover:"}}},
		{"named then unnamed", []step{{"named", 0, 180, 180, ""}, {"unnamed", 30, 0, 180, ""}, {"check", 90, 0, 180, ""}, {"check", 180, 0, 0, "recover:"}}},
		{"unnamed then named", []step{{"unnamed", 0, 0, 60, ""}, {"named", 30, 50, 60, ""}, {"check", 50, 0, 60, ""}, {"check", 60, 0, 0, "recover:"}}},
		{"expired named then unnamed", []step{{"named", 0, 60, 60, ""}, {"check", 60, 0, 0, "recover:"}, {"unnamed", 181, 0, 241, ""}, {"check", 185, 0, 241, ""}, {"check", 241, 0, 0, "recover:"}}},
		// Gate reproduction: reset 21:00, recovery 23:00 fails at 23:01,
		// restart at 23:05. The required wait ends at 00:01, not 21:00.
		{"failed recovery then restart", []step{{"named", 0, 60, 60, ""}, {"failure", 180, 181, 241, ""}, {"check", 185, 0, 241, ""}, {"check", 241, 0, 0, "recover:"}}},
		{"successful recovery then unnamed", []step{{"named", 0, 60, 60, ""}, {"success", 61, 62, 0, "reread:"}, {"unnamed", 181, 0, 241, ""}, {"check", 241, 0, 0, "recover:"}}},
		{"unnamed named recovery success", []step{{"unnamed", 0, 0, 60, ""}, {"named", 30, 90, 90, ""}, {"success", 91, 92, 0, "reread:"}, {"unnamed", 100, 0, 160, ""}, {"check", 160, 0, 0, "recover:"}}},
		{"unnamed named recovery failure", []step{{"unnamed", 0, 0, 60, ""}, {"named", 30, 90, 90, ""}, {"failure", 91, 92, 152, ""}, {"check", 152, 0, 0, "recover:"}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newDenialFixture(t)
			base := time.Date(2026, 9, 29, 20, 0, 0, 0, time.UTC)
			at := func(minute int) time.Time { return base.Add(time.Duration(minute) * time.Minute) }
			var epoch time.Time
			for _, s := range tt.steps {
				now := at(s.minute)
				switch s.kind {
				case "named":
					f.named(now, at(s.end))
					epoch = now
				case "unnamed":
					f.stop(now)
					epoch = now
				case "success", "failure":
					outcome := "vendor_limit"
					if s.kind == "success" {
						outcome = "completed"
					}
					f.recover(now, at(s.end), outcome)
					now = at(s.end)
					epoch = now
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
						if s.grant == "recover:" && windows[0].capacityBucket != fmt.Sprintf("recover:g%d:%s", f.generation, epoch.Format(time.RFC3339Nano)) {
							t.Fatalf("recovery lost the latest epoch: %s", windows[0].capacityBucket)
						}
					}
				}
			}
			// The final expired denial offers one recovery in this generation.
			// Persist it and prove a second grant and a concurrent run are fenced.
			now := at(tt.steps[len(tt.steps)-1].minute)
			if _, wait := f.admit(now, 1, false); wait == nil || wait.Code != "reading" {
				t.Fatalf("concurrent recovery: %+v", wait)
			}
			f.recover(now, now.Add(time.Second), "failed")
			if _, wait := f.admit(now.Add(time.Second), 0, false); wait == nil || wait.Code != "reading" {
				t.Fatal("spent epoch admitted twice")
			}
			f.restart()
			windows, wait := f.admit(now.Add(time.Second), 0, false)
			if wait != nil || !containsRecovery(windows) {
				t.Fatalf("new generation cannot recover expired epoch: %+v", wait)
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
			// Independent oracle: unresolved deadlines, cleared only by a
			// successful recovery. Distinct named buckets retain each denial.
			var deadlines []time.Time
			for i := 0; i < 60; i++ {
				now = now.Add(time.Duration(1+rng.Intn(40)) * time.Minute)
				switch rng.Intn(5) {
				case 0:
					until := now.Add(time.Duration(1+rng.Intn(120)) * time.Minute)
					f.named(now, until)
					deadlines = append(deadlines, until)
				case 1:
					f.stop(now)
					deadlines = append(deadlines, now.Add(time.Hour))
				case 2:
					f.restart()
				case 3:
					windows, wait := f.admit(now, 0, false)
					if wait == nil && containsRecovery(windows) {
						for _, deadline := range deadlines {
							if deadline.After(now) {
								t.Fatalf("recovery granted at %s before %s", now, deadline)
							}
						}
						success := rng.Intn(2) == 0
						outcome := "vendor_limit"
						if success {
							outcome = "completed"
						}
						f.recover(now, now.Add(time.Second), outcome)
						now = now.Add(time.Second)
						if success {
							deadlines = nil
						} else {
							deadlines = append(deadlines, now.Add(time.Hour))
						}
					}
				}
				var latest time.Time
				for _, deadline := range deadlines {
					if deadline.After(now) && deadline.After(latest) {
						latest = deadline
					}
				}
				if !latest.IsZero() {
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
	f.assertWait(now.Add(61*time.Minute), now.Add(2*time.Hour))
	f.restart()
	f.assertWait(now.Add(61*time.Minute), now.Add(2*time.Hour))
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
	callStatus(t, accountsMod(), &f.person, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, scheduleOverride{"account", "", f.account.ID, &s, false}), 204, nil)
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
		return err
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
	windows, wait := f.admit(start, 0, false)
	if wait != nil || len(windows) != 1 || !containsRecovery(windows) {
		f.t.Fatalf("recovery at %s: wait=%+v windows=%+v", start, wait, windows)
	}
	w := windows[0]
	run := insertRun(f.t, f.person, f.runner, f.profile)
	f.seed(func(tx pgx.Tx) error {
		var windowID string
		if err := tx.QueryRow(f.t.Context(), `INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance,pace_model,capacity_kind,capacity_read_at,capacity_bucket)
 VALUES($1,$2,$3,$4,'percent',1,'unrestricted','refresh',$3,$5) RETURNING id::text`, f.person.TenantID, f.account.ID, start, w.EndsAt, w.capacityBucket).Scan(&windowID); err != nil {
			return err
		}
		if _, err := tx.Exec(f.t.Context(), `INSERT INTO account_reservations(tenant_id,run_id,window_id,reserved_units,state) VALUES($1,$2,$3,1,'released')`, f.person.TenantID, run, windowID); err != nil {
			return err
		}
		status := "failed"
		if outcome == "completed" {
			status = "completed"
		}
		if _, err := tx.Exec(f.t.Context(), `UPDATE agent_runs SET account_id=$2,status=$3,started_at=$4,ended_at=$5 WHERE id=$1`, run, f.account.ID, status, start, finish); err != nil {
			return err
		}
		if outcome == "vendor_limit" {
			_, err := tx.Exec(f.t.Context(), `INSERT INTO run_telemetry(tenant_id,run_id,sequence,kind,error_code,at) VALUES($1,$2,1,'usage','vendor_limit',$3)`, f.person.TenantID, run, finish)
			return err
		}
		return nil
	})
}
