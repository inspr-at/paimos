// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func learningAccount(t *testing.T, harness string) (tenant.Principal, tenant.Principal, string, Account) {
	t.Helper()
	reset(t)
	person := makePrincipal(t, "learn-"+harness, "person", "Ada", []string{"admin"})
	runner := addPrincipal(t, person.TenantID, "agent", "runner", nil)
	key := issueKey(t, runner, []string{"account.manage", "account.probe", "run.claim"})
	var a Account
	callStatus(t, accountsMod(), &runner, key, "POST", "/api/agent-accounts", encoded(t, map[string]any{"account_key": "learned", "harness": harness, "daemon_id": "daemon-a", "label": "Main"}), 201, &a)
	callStatus(t, accountsMod(), &runner, key, "POST", "/api/agent-accounts/"+a.ID+"/probe", `{"daemon_id":"daemon-a","daemon_generation":"g1","available":true}`, 200, &a)
	// All HTTP/routing checks pin the schedule; they cannot fail at night.
	s := capacity.DefaultSchedule()
	s.Week = capacity.Preset(7)
	for i := range s.Week {
		s.Week[i].Start = 0
		s.Week[i].End = 24
	}
	s.Reserve = capacity.ReserveOff
	callStatus(t, accountsMod(), &person, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, scheduleOverride{Scope: "account", AccountID: a.ID, Schedule: &s}), 204, nil)
	return person, runner, key, a
}
func inLearning(t *testing.T, p tenant.Principal, fn func(pgx.Tx) error) {
	t.Helper()
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, fn); err != nil {
		t.Fatal(err)
	}
}
func TestLearningRunIngestionReplayReservationAndTenantFence(t *testing.T) {
	person, runner, key, a := learningAccount(t, "codex")
	profile := codexProfile(t, person)
	now := time.Now().UTC().Add(-time.Second).Truncate(time.Microsecond)
	for i := 0; i < 12; i++ {
		id := insertRun(t, person, runner, profile)
		start := now.Add(time.Duration(i-13) * time.Hour)
		end := start.Add(30 * time.Minute)
		inLearning(t, person, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET account_id=$2,status='completed',started_at=$3,ended_at=$4,input_tokens=1000000,effective_model='fixture-model' WHERE id=$1`, id, a.ID, start, end)
			return err
		})
		reading := capacity.Reading{WindowKind: "5h", WindowMinutes: 300, Plan: "Pro", Bucket: "codex", UsedPercent: 10, ReadAt: start, ResetsAt: start.Add(5 * time.Hour), Source: "harness", RunID: id, Phase: "start"}
		path := "/api/agent-accounts/" + a.ID + "/readings"
		callStatus(t, accountsMod(), &runner, key, "POST", path, encoded(t, readingsWrite{[]capacity.Reading{reading}}), 204, nil)
		reading.ReadAt = end
		reading.UsedPercent += float64(i + 1)
		reading.Phase = "end"
		for j := 0; j < 2; j++ {
			callStatus(t, accountsMod(), &runner, key, "POST", path, encoded(t, readingsWrite{[]capacity.Reading{reading}}), 204, nil)
		}
	}
	inLearning(t, person, func(tx pgx.Tx) error {
		l, err := loadLearning(t.Context(), tx, a.ID)
		if err != nil {
			return err
		}
		if len(l.Windows) != 1 || len(l.Windows[0].Runs) != 12 || len(l.Windows[0].Own) != 0 {
			t.Fatalf("sample attribution %+v", l)
		}
		metric := l.Windows[0].Summarize(now, capacity.DefaultSchedule(), profile)
		if math.Abs(metric.HoldPercent-9) > 1 || metric.PerMillion <= 0 {
			t.Fatalf("hold %+v", metric)
		}
		return nil
	})
	reading := capacity.Reading{WindowKind: "5h", Bucket: "codex", Plan: "Pro", WindowMinutes: 300, UsedPercent: 20, ReadAt: now, ResetsAt: now.Add(time.Hour), Source: "harness"}
	callStatus(t, accountsMod(), &runner, key, "POST", "/api/agent-accounts/"+a.ID+"/readings", encoded(t, readingsWrite{[]capacity.Reading{reading}}), 204, nil)
	id := insertRun(t, person, runner, profile)
	mustRoute(t, accountsMod(), runner, key, id, "daemon-a", []Account{a}, map[string]int64{"requests": 1})
	if n := scalar(t, person, `SELECT reserved_units FROM account_reservations WHERE run_id=$1`, id); n < 9 || n > 10 {
		t.Fatalf("flat hold remains: %d", n)
	}
	foreign := makePrincipal(t, "learn-foreign", "person", "Foreign", []string{"admin"})
	if scalar(t, foreign, `SELECT count(*) FROM account_capacity_learning`) != 0 {
		t.Fatal("learning crossed tenant")
	}
}

func TestLearningOwnUsePresenceDriftAndPlanChange(t *testing.T) {
	person, runner, key, a := learningAccount(t, "codex")
	now := time.Now().UTC().Add(-time.Second).Truncate(time.Microsecond)
	reading := capacity.Reading{WindowKind: "weekly", WindowMinutes: 10080, Plan: "Pro", ReadAt: now.Add(-15 * time.Minute), UsedPercent: 10, ResetsAt: now.Add(24 * time.Hour), Source: "agentd"}
	path := "/api/agent-accounts/" + a.ID + "/readings"
	callStatus(t, accountsMod(), &runner, key, "POST", path, encoded(t, readingsWrite{[]capacity.Reading{reading}}), 204, nil)
	reading.ReadAt = now
	reading.UsedPercent = 12
	callStatus(t, accountsMod(), &runner, key, "POST", path, encoded(t, readingsWrite{[]capacity.Reading{reading}}), 204, nil)
	inLearning(t, person, func(tx pgx.Tx) error {
		l, err := loadLearning(t.Context(), tx, a.ID)
		if err != nil {
			return err
		}
		if l.PresenceUntil == nil || !l.PresenceUntil.Equal(now.Add(30*time.Minute)) {
			t.Fatal("own use presence missing")
		}
		s := capacity.DefaultSchedule()
		s.Reserve = capacity.ReserveOff
		s.Override = "sprint"
		p, _, err := readingPacing(t.Context(), tx, a.ID, reading, now.Add(30*time.Minute), s)
		if err == nil && (math.Abs(p.DriftPercent-4) > 0.01 || math.Abs(p.AvailableNowPercent-84) > 0.01) {
			t.Fatalf("aging drift %+v", p)
		}
		if l.summary(now.Add(31*time.Minute), s).PresenceUntil != nil {
			t.Fatal("presence never expired")
		}
		return err
	})
	reading.ReadAt = now.Add(time.Millisecond)
	reading.Plan = "Plus"
	callStatus(t, accountsMod(), &runner, key, "POST", path, encoded(t, readingsWrite{[]capacity.Reading{reading}}), 204, nil)
	inLearning(t, person, func(tx pgx.Tx) error {
		l, err := loadLearning(t.Context(), tx, a.ID)
		if len(l.Windows[0].Own) != 0 {
			t.Fatal("plan change retained old capacity model")
		}
		return err
	})
}

func TestLearningBlindLimitsAndFreshMeasuredWins(t *testing.T) {
	person, runner, key, a := learningAccount(t, "grok")
	now := time.Now().UTC().Add(-time.Second).Truncate(time.Microsecond)
	inLearning(t, person, func(tx pgx.Tx) error {
		l := capacityLearning{Hits: []capacity.LimitSample{{At: now.Add(-8 * 24 * time.Hour), Tokens: 1000}, {At: now.Add(-24 * time.Hour), Tokens: 1200}}, Tokens: 550, Runs: 2}
		if err := saveLearning(t.Context(), tx, a.ID, l); err != nil {
			return err
		}
		v := capacity.BlindEstimate(l.Hits, float64(l.Tokens), 0, now)
		if v == nil {
			t.Fatal("missing blind estimate")
		}
		if err := persistEstimate(t.Context(), tx, a, *v, now); err != nil {
			return err
		}
		duplicate := *v
		duplicate.UsedPercent = 1
		if err := persistEstimate(t.Context(), tx, a, duplicate, now); err != nil {
			return err
		}
		history, err := readCapacity(t.Context(), tx, a.ID, false)
		if err != nil {
			return err
		}
		if len(history) != 1 || history[0].UsedPercent != 50 || history[0].Evidence == nil || history[0].Evidence.Samples != 2 || history[0].PlusMinus < 3 {
			t.Fatal("lost estimate evidence")
		}
		return nil
	})
	// The first real vendor reading names its actual bucket, unlike the blind
	// estimate. Matching reset and duration still establish a contradiction.
	measured := capacity.Reading{WindowKind: "weekly", WindowMinutes: 10080, UsedPercent: 80, ReadAt: now.Add(time.Millisecond), ResetsAt: now.Add(6 * 24 * time.Hour), Source: "harness"}
	callStatus(t, accountsMod(), &runner, key, "POST", "/api/agent-accounts/"+a.ID+"/readings", encoded(t, readingsWrite{[]capacity.Reading{measured}}), 204, nil)
	inLearning(t, person, func(tx pgx.Tx) error {
		v := measured
		v.ReadAt = now.Add(time.Second)
		v.Source = "estimate"
		v.UsedPercent = 1
		v.Evidence = &capacity.Evidence{Kind: "runs", Samples: 3}
		if err := persistEstimate(t.Context(), tx, a, v, v.ReadAt); err != nil {
			return err
		}
		history, err := readCapacity(t.Context(), tx, a.ID, true)
		if err != nil {
			return err
		}
		if len(history) != 1 || history[0].Source != "harness" || history[0].UsedPercent != 80 {
			t.Fatalf("estimate beat measurement %+v", history)
		}
		l, err := loadLearning(t.Context(), tx, a.ID)
		if err != nil {
			return err
		}
		if l.Correction == nil || l.Windows[0].Sigma < 29 || l.Sigma < 29 {
			t.Fatal("contradiction did not widen uncertainty")
		}
		return nil
	})
	// Recording HTTP client: the public projection contains only typed evidence,
	// never internal run IDs/model identifiers or local-path fields.
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/agent-accounts/capacity", nil)
	r = r.WithContext(tenant.WithPrincipal(r.Context(), person))
	mux := http.NewServeMux()
	accountsMod().Mount(mux)
	mux.ServeHTTP(rec, r)
	if rec.Code != 200 {
		t.Fatalf("projection status %d: %s", rec.Code, rec.Body.String())
	}
	for _, bad := range []string{"/Users/", "/home/", "config_home", "TokenSamples", "fixture-model"} {
		if strings.Contains(rec.Body.String(), bad) {
			t.Fatalf("private learning leaked: %s", bad)
		}
	}
}

func TestLearningPresenceOrderAndSuggestions(t *testing.T) {
	now := time.Date(2026, 10, 2, 20, 0, 0, 0, time.UTC)
	s := capacity.DefaultSchedule()
	l := capacityLearning{}
	own := now.Add(-72 * time.Hour)
	l.LastOwn = &own
	for d := 0; d < 5; d++ {
		l.Online = append(l.Online, now.Add(-time.Duration(d)*24*time.Hour))
	}
	summary := l.summary(now, s)
	if !summary.Away || !summary.Sleeps {
		t.Fatalf("observed suggestions %+v", summary)
	}
	l.Online = nil
	if l.summary(now, s).Away {
		t.Fatal("silence invented absence")
	}
	soon := now.Add(time.Hour)
	later := now.Add(24 * time.Hour)
	picks := []ranked{{account: Account{ID: "Main"}, presence: true, reset: &soon}, {account: Account{ID: "Spare"}, reset: &later}}
	orderPicks(picks)
	if picks[0].account.ID != "Spare" {
		t.Fatal("presence did not steer ordering")
	}
}

func TestLearningBlindSettlementCalibratesAndReplayDoesNotMint(t *testing.T) {
	person, runner, _, a := learningAccount(t, "grok")
	profile := codexProfile(t, person)
	now := time.Now().UTC().Add(-time.Second).Truncate(time.Microsecond)
	var last string
	for i, tokens := range []int{1000, 1200, 100, 200, 300} {
		id := insertRun(t, person, runner, profile)
		last = id
		start := now.Add(time.Duration(i-6) * time.Hour)
		if i < 2 {
			start = now.Add(time.Duration(i-2) * 7 * 24 * time.Hour)
		}
		end := start.Add(30 * time.Minute)
		inLearning(t, person, func(tx pgx.Tx) error {
			if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET account_id=$2,created_at=$3,started_at=$3,ended_at=$4,status='completed',input_tokens=$5,effective_model='blind-model' WHERE id=$1`, id, a.ID, start, end, tokens); err != nil {
				return err
			}
			if i < 2 {
				if _, err := tx.Exec(t.Context(), `INSERT INTO run_telemetry(tenant_id,run_id,sequence,kind,error_code,at,limit_resets_at) VALUES($1,$2,1,'usage','vendor_limit',$3,$4)`, person.TenantID, id, end, end.Add(time.Hour)); err != nil {
					return err
				}
			}
			return learnRun(t.Context(), tx, a, id, now.Add(time.Duration(i-5)*time.Millisecond))
		})
	}
	inLearning(t, person, func(tx pgx.Tx) error {
		l, err := loadLearning(t.Context(), tx, a.ID)
		if err != nil {
			return err
		}
		if len(l.Hits) != 2 || l.Tokens != 600 || l.Runs != 3 {
			t.Fatalf("cycle accounting %+v", l)
		}
		readings, err := readCapacity(t.Context(), tx, a.ID, true)
		if err != nil {
			return err
		}
		if len(readings) != 1 || readings[0].Source != "estimate" || math.Abs(readings[0].UsedPercent-100*600.0/1100) > .1 {
			t.Fatalf("calibration %+v", readings)
		}
		metric := l.metric(readings[0], now, capacity.DefaultSchedule(), profile)
		if metric.HoldPercent < 20 || metric.RunCount != 5 {
			t.Fatalf("blind run cost %+v", metric)
		}
		return nil
	})
	before := scalar(t, person, `SELECT count(*) FROM account_capacity_readings WHERE account_id=$1`, a.ID)
	inLearning(t, person, func(tx pgx.Tx) error { return learnRun(t.Context(), tx, a, last, now.Add(time.Second)) })
	if after := scalar(t, person, `SELECT count(*) FROM account_capacity_readings WHERE account_id=$1`, a.ID); after != before {
		t.Fatalf("replay minted an estimate: %d -> %d", before, after)
	}
	inLearning(t, person, func(tx pgx.Tx) error {
		if err := ObserveSessionTokens(t.Context(), tx, runner.ID, a.ID, "blind-model", 200, now.Add(-time.Minute), now.Add(2*time.Second)); err != nil {
			return err
		}
		l, err := loadLearning(t.Context(), tx, a.ID)
		if err != nil {
			return err
		}
		if l.Tokens != 800 || l.Runs != 3 {
			t.Fatalf("external usage attribution %+v", l)
		}
		readings, err := readCapacity(t.Context(), tx, a.ID, true)
		if err == nil && (len(readings) != 1 || math.Abs(readings[0].UsedPercent-100*800.0/1100) > .1) {
			t.Fatalf("external calibrated estimate %+v", readings)
		}
		return err
	})
	// A calibrated estimate cannot invent a serial budget; the real cap remains.
	a.MaxParallel = 4
	inLearning(t, person, func(tx pgx.Tx) error {
		windows, err := lockAccountWindows(t.Context(), tx, []string{a.ID})
		if err != nil {
			return err
		}
		_, wait, err := admission(t.Context(), tx, a, windows[a.ID], now, 1, runRow{Purpose: "managed"}, false)
		if err != nil {
			return err
		}
		if wait != nil {
			t.Fatalf("estimate limited concurrent unknown usage: %+v", wait)
		}
		_, wait, err = admission(t.Context(), tx, a, windows[a.ID], now, 4, runRow{Purpose: "managed"}, false)
		if err == nil && (wait == nil || wait.Code != "capacity") {
			t.Fatalf("real slot cap lost: %+v", wait)
		}
		return err
	})
}

func TestLearningTokenOnlyExactModelFreshnessAndOwnership(t *testing.T) {
	person, runner, key, a := learningAccount(t, "codex")
	now := time.Now().UTC().Add(-time.Second).Truncate(time.Microsecond)
	baseline := now.Add(-30 * time.Minute)
	r := capacity.Reading{WindowKind: "weekly", WindowMinutes: 10080, Plan: "Pro", UsedPercent: 20, ReadAt: baseline, ResetsAt: now.Add(24 * time.Hour), Source: "harness"}
	callStatus(t, accountsMod(), &runner, key, "POST", "/api/agent-accounts/"+a.ID+"/readings", encoded(t, readingsWrite{[]capacity.Reading{r}}), 204, nil)
	inLearning(t, person, func(tx pgx.Tx) error {
		l, err := loadLearning(t.Context(), tx, a.ID)
		if err != nil {
			return err
		}
		w := l.window(r)
		for i := 0; i < 3; i++ {
			w.ObserveRun(capacity.RunSample{ID: string(rune('a' + i)), Model: "exact-model", At: baseline.Add(-time.Hour), Percent: 10, Tokens: 1000000, Hours: 1})
		}
		if err := saveLearning(t.Context(), tx, a.ID, l); err != nil {
			return err
		}
		if err := ObserveSessionTokens(t.Context(), tx, person.ID, a.ID, "exact-model", 9000000, baseline, now); err != nil {
			return err
		}
		if err := ObserveSessionTokens(t.Context(), tx, runner.ID, a.ID, "other-model", 9000000, baseline, now); err != nil {
			return err
		}
		if err := ObserveSessionTokens(t.Context(), tx, runner.ID, a.ID, "exact-model", 1000000, baseline, now); err != nil {
			return err
		}
		readings, err := readCapacity(t.Context(), tx, a.ID, true)
		if err != nil {
			return err
		}
		if len(readings) != 1 || readings[0].Source != "estimate" || readings[0].UsedPercent != 30 || readings[0].Evidence.Kind != "tokens" {
			t.Fatalf("token estimate %+v", readings)
		}
		return nil
	})
	// A new measured baseline supersedes token history. Even huge token deltas
	// cannot replace it while it is fresh, or count samples from before it.
	r.ReadAt, r.UsedPercent = now.Add(time.Millisecond), 34
	callStatus(t, accountsMod(), &runner, key, "POST", "/api/agent-accounts/"+a.ID+"/readings", encoded(t, readingsWrite{[]capacity.Reading{r}}), 204, nil)
	inLearning(t, person, func(tx pgx.Tx) error {
		if err := ObserveSessionTokens(t.Context(), tx, runner.ID, a.ID, "exact-model", 1000000, r.ReadAt, r.ReadAt.Add(time.Minute)); err != nil {
			return err
		}
		readings, err := readCapacity(t.Context(), tx, a.ID, true)
		if err == nil && (len(readings) != 1 || readings[0].Source != "harness" || readings[0].UsedPercent != 34) {
			t.Fatalf("fresh measured lost %+v", readings)
		}
		return err
	})
}
