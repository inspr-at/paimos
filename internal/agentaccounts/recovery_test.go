// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func bResource(t *testing.T, f limitFixture) string {
	t.Helper()
	var id string
	seed(t, f.admin, func(tx pgx.Tx) error {
		var err error
		id, err = localReadinessResource(t.Context(), tx, f.account)
		return err
	})
	return id
}

func bStop(t *testing.T, f limitFixture, now time.Time, kind string) string {
	t.Helper()
	id := bResource(t, f)
	fact := ReadinessFactWrite{ResourceID: id, WindowKey: "vendor", Source: "provider", ObservedAt: now, CreditState: "unknown", StopKind: kind, DenialReason: "vendor_denied"}
	if kind == "money_402" {
		fact.CreditState, fact.DenialReason = "exhausted", "money_exhausted"
	}
	callStatus(t, f.mod, &f.runner, f.token, "POST", "/api/agent-accounts/"+f.account.ID+"/probe", encoded(t, probeWrite{DaemonID: "daemon-a", DaemonGeneration: "g1", Available: true, Readiness: &ReadinessReport{BindingRevision: ptrRevision(0), Result: "success", Facts: []ReadinessFactWrite{fact}}}), 200, nil)
	return id
}

func bAt(t *testing.T, f *limitFixture, at time.Time, generation string) {
	t.Helper()
	f.mod = fixedClockModule{Module: New(appPool), at: at}
	callStatus(t, f.mod, &f.runner, f.token, "POST", "/api/agent-accounts/"+f.account.ID+"/probe", encoded(t, probeWrite{DaemonID: "daemon-a", DaemonGeneration: generation, Available: true}), 200, nil)
}

func bFinish(t *testing.T, f limitFixture, run string, now time.Time, status string, output int) {
	t.Helper()
	ctx := context.WithValue(tenant.WithPrincipal(dbtest.Seed(t.Context()), f.runner), clockKey{}, now)
	if err := db.InTenant(ctx, appPool, f.admin.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE agent_runs SET status=$2,started_at=$3,ended_at=$3 WHERE id=$1`, run, status, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO run_telemetry(tenant_id,run_id,sequence,kind,output_tokens_delta,at) VALUES($1,$2,1,'usage',$3,$4)`, f.admin.TenantID, run, output, now); err != nil {
			return err
		}
		return Settle(ctx, tx, f.runner, run)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestBUnknownUsageEveryHarnessHasOnlyRealSlots(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	for _, harness := range []string{"codex", "claude", "grok", "cursor", "pi"} {
		t.Run(harness, func(t *testing.T) {
			f := readinessWorld(t, "b-unknown-"+harness, now)
			seed(t, f.admin, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET harness=$2 WHERE id=$1`, f.account.ID, harness)
				if err != nil {
					return err
				}
				return tx.QueryRow(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier) VALUES($1,$2,'1',$3,'openai','test','high','strong') RETURNING id::text`, f.admin.TenantID, "b-"+harness, harness).Scan(&f.profile)
			})
			// Test fixtures changing harness must seed the new canonical local kind.
			f.account.Harness = harness
			seed(t, f.admin, func(tx pgx.Tx) error { return ensureLocalReadinessResource(t.Context(), tx, f.runner, f.account) })
			for i := 0; i < 6; i++ {
				run, _ := f.route(t, 200)
				bFinish(t, f, run, now, "completed", 1)
			}
			first, _ := f.route(t, 200)
			second, _ := f.route(t, 200)
			third, _ := f.route(t, 200)
			f.route(t, 409)
			if scalar(t, f.admin, `SELECT count(*) FROM agent_runs WHERE id=ANY($1::uuid[]) AND account_id=$2`, []string{first, second, third}, f.account.ID) != 3 {
				t.Fatal("unknown usage imposed a serial or start-count limit")
			}
		})
	}
}

func TestBDurableRecoveryBackoffAndInferenceEvidence(t *testing.T) {
	base := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	for _, kind := range []string{"unnamed", "money_402"} {
		t.Run(kind, func(t *testing.T) {
			f := readinessWorld(t, "b-backoff-"+kind, base)
			resource := bStop(t, f, base, kind)
			now := base
			for step, hours := range []int{1, 2, 4, 8, 8} {
				due := now.Add(time.Duration(hours) * time.Hour)
				bAt(t, &f, due.Add(-time.Second), fmt.Sprintf("g%d", step+2))
				f.route(t, 409)
				bAt(t, &f, due, fmt.Sprintf("g%d", step+2))
				run, _ := f.route(t, 200)
				// Replay and restart cannot mint a second recovery or lose its hold.
				mustRoute(t, f.mod, f.runner, f.token, run, "daemon-a", []Account{f.account}, map[string]int64{"requests": 1})
				f.route(t, 409)
				if scalar(t, f.admin, `SELECT count(*) FROM account_readiness_facts WHERE resource_id=$1 AND recovery_run_id=$2 AND backoff_step=$3`, resource, run, min(step, 3)) != 1 {
					t.Fatal("recovery permit/backoff not durably bound to run")
				}
				status := "failed"
				if step == 1 {
					status = "completed" // A process exit without inference is failure.
				}
				bFinish(t, f, run, due, status, 0)
				if scalar(t, f.admin, `SELECT count(*) FROM account_readiness_facts WHERE resource_id=$1 AND stop_kind=$2 AND backoff_step=$3 AND recovery_run_id IS NULL AND NOT early_recovery_used`, resource, kind, min(step+1, 3)) != 1 {
					t.Fatal("failed or unevidenced recovery cleared stop")
				}
				now = due
			}
			bAt(t, &f, now.Add(8*time.Hour), "g8")
			run, _ := f.route(t, 200)
			bFinish(t, f, run, now.Add(8*time.Hour), "completed", 7)
			if scalar(t, f.admin, `SELECT count(*) FROM account_readiness_facts WHERE resource_id=$1 AND window_key='vendor' AND stop_kind='none' AND wait_id IS NULL AND next_attempt_at IS NULL AND backoff_step=0`, resource) != 1 {
				t.Fatal("evidenced inference did not reset the stop/backoff")
			}
			f.route(t, 200)
		})
	}
}

func TestBEarlyAndExpiryRecoveryRaceConsumesOneWait(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	f := readinessWorld(t, "b-race", now)
	resource := bStop(t, f, now, "money_402")
	var c AccountCheck
	callStatus(t, f.mod, &f.admin, "", "POST", "/api/agent-accounts/"+f.account.ID+"/check", requestBody("early", 0), 202, &c)
	if !c.EarlyRecoveryRequested {
		t.Fatal("owner check did not capture its wait")
	}
	runs := []string{insertRun(t, f.admin, f.runner, f.profile), insertRun(t, f.admin, f.runner, f.profile)}
	// Pause after tenant acquisition; PostgreSQL proves the second claim is
	// blocked on that fence before the first can continue.
	pool, barrier, ctx := dbtest.BarrierPool(t, appPool, func(sql string) bool {
		return sql == db.TenantFenceSQL
	})
	racing := fixedClockModule{Module: New(pool), at: now}
	statuses := make(chan int, 2)
	go func() {
		code, _ := bRaceRoute(ctx, racing, f.runner, f.token, routeBody(t, runs[0], "daemon-a", []Account{f.account}, map[string]int64{"requests": 1}))
		statuses <- code
	}()
	pid := barrier.Wait(t, ctx)
	secondDone := make(chan struct{})
	go func() {
		code, _ := bRaceRoute(ctx, racing, f.runner, f.token, routeBody(t, runs[1], "daemon-a", []Account{f.account}, map[string]int64{"requests": 1}))
		statuses <- code
		close(secondDone)
	}()
	if lock := dbtest.BlockedOrDone(t, ctx, adminPool, pid, secondDone); lock != "transactionid" {
		t.Fatalf("second recovery claim did not wait on tenant: %q", lock)
	}
	barrier.Release()
	counts := map[int]int{}
	for i := 0; i < 2; i++ {
		select {
		case code := <-statuses:
			counts[code]++
		case <-ctx.Done():
			t.Fatal("recovery transactions hung")
		}
	}
	close(statuses)
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatalf("recovery race statuses: %v", counts)
	}
	if scalar(t, f.admin, `SELECT count(*) FROM account_readiness_facts WHERE resource_id=$1 AND recovery_run_id IS NOT NULL AND early_recovery_used AND recovery_check_id=$2`, resource, c.ID) != 1 {
		t.Fatal("race did not consume exactly the snapshotted early intent")
	}
	bAt(t, &f, now.Add(time.Hour), "g2")
	f.route(t, 409)
	var held string
	if err := adminPool.QueryRow(t.Context(), `SELECT recovery_run_id::text FROM account_readiness_facts WHERE resource_id=$1 AND window_key='vendor'`, resource).Scan(&held); err != nil {
		t.Fatal(err)
	}
	bFinish(t, f, held, now.Add(time.Hour), "completed", 2)
	f.route(t, 200)
}

func TestBHardStopsAndStaleRoomRemainResourceSpecific(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	f := readinessWorld(t, "b-hard", now)
	reset := now.Add(3 * time.Hour)
	f.report(t, 100, now.Add(-time.Hour), reset)
	f.route(t, 409)
	// Another bucket has room but cannot clear the stale full window.
	reading := capacity.Reading{WindowKind: "5h", WindowMinutes: 300, Bucket: "other", UsedPercent: 10, ReadAt: now, ResetsAt: reset, Source: "harness"}
	callStatus(t, f.mod, &f.runner, f.token, "POST", "/api/agent-accounts/"+f.account.ID+"/readings", encoded(t, readingsWrite{[]capacity.Reading{reading}}), 204, nil)
	f.route(t, 409)
	f.report(t, 10, now, reset)
	f.route(t, 200)
}

func bRaceRoute(ctx context.Context, mod httpapi.Module, actor tenant.Principal, token, body string) (int, []byte) {
	mux := http.NewServeMux()
	mod.Mount(mux)
	r := httptest.NewRequest("POST", "/api/agent-accounts/route", strings.NewReader(body)).WithContext(tenant.WithPrincipal(ctx, actor))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w.Code, w.Body.Bytes()
}

func TestBUnstartedRecoveryRestoresEarlyIntent(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	f := readinessWorld(t, "b-cancel", now)
	resource := bStop(t, f, now, "unnamed")
	callStatus(t, f.mod, &f.admin, "", "POST", "/api/agent-accounts/"+f.account.ID+"/check", requestBody("early", 0), 202, nil)
	run, _ := f.route(t, 200)
	seed(t, f.admin, func(tx pgx.Tx) error { return Release(t.Context(), tx, f.runner, run, "", "") })
	if scalar(t, f.admin, `SELECT count(*) FROM account_readiness_facts WHERE resource_id=$1 AND window_key='vendor' AND recovery_run_id IS NULL AND NOT early_recovery_used AND backoff_step=0 AND next_attempt_at=$2`, resource, now.Add(time.Hour)) != 1 {
		t.Fatal("cancel advanced backoff or spent early intent")
	}
	f.route(t, 200)
	f.route(t, 409)
}

func TestBStaleKeyCapPersistsAcrossNullChecks(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	f := readinessWorld(t, "b-cap", now)
	resource := bResource(t, f)
	zero, room := 0.0, 10.0
	report := func(at time.Time, window string, left *float64) {
		t.Helper()
		bAt(t, &f, at, "g1")
		fact := ReadinessFactWrite{ResourceID: resource, WindowKey: window, Source: "provider", ObservedAt: at, CreditState: "unknown", Remaining: left}
		fact.ReadingAt = &at // A captured null-cap response is still no room evidence.
		callStatus(t, f.mod, &f.runner, f.token, "POST", "/api/agent-accounts/"+f.account.ID+"/probe", encoded(t, probeWrite{DaemonID: "daemon-a", DaemonGeneration: "g1", Available: true, Readiness: &ReadinessReport{BindingRevision: ptrRevision(0), Result: "success", Facts: []ReadinessFactWrite{fact}}}), 200, nil)
	}
	report(now, "key_cap", &zero)
	bAt(t, &f, now.Add(time.Hour), "g2")
	f.route(t, 409)
	report(now.Add(2*time.Hour), "key_cap", nil)
	report(now.Add(2*time.Hour+time.Minute), "other", &room)
	f.route(t, 409)
	report(now.Add(2*time.Hour+2*time.Minute), "key_cap", &room)
	f.route(t, 200)
}

func bPiWorld(t *testing.T, slug string, now time.Time) limitFixture {
	t.Helper()
	f := readinessWorld(t, slug, now)
	seed(t, f.admin, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET harness='pi',provider='openrouter' WHERE id=$1`, f.account.ID); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier) VALUES($1,'b-openrouter','1','pi','openai','test','high','strong') RETURNING id::text`, f.admin.TenantID).Scan(&f.profile)
	})
	f.account.Harness, f.account.Provider = "pi", "openrouter"
	bAt(t, &f, now, "g1")
	return f
}

func TestBUnresolvedBalanceSharesRecoveryAcrossPeople(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	f := bPiWorld(t, "b-shared-balance", now)
	peer := addPrincipal(t, f.admin.TenantID, "person", "Peer", []string{"admin"})
	var other Account
	callStatus(t, f.mod, &f.runner, f.token, "POST", "/api/agent-accounts", encoded(t, map[string]any{"account_key": "peer", "harness": "pi", "daemon_id": "daemon-a", "label": "Peer", "max_parallel_runs": 3}), 201, &other)
	seed(t, f.admin, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET owner_person_id=$2,linked_at=$3,provider='openrouter' WHERE id=$1`, other.ID, peer.ID, now)
		return err
	})
	other.OwnerPersonID = &peer.ID
	other.Provider = "openrouter"
	s := capacity.DefaultSchedule("UTC")
	for i := range s.Week {
		s.Week[i] = capacity.Day{On: true, Start: 0, End: 24}
	}
	s.Reserve = capacity.ReserveOff
	callStatus(t, f.mod, &peer, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, scheduleOverride{Scope: "account", AccountID: other.ID, Schedule: &s}), 204, nil)
	second := f
	second.account = other
	second.admin = peer
	bAt(t, &second, now, "g1")
	bStop(t, f, now, "money_402")
	var resource string
	seed(t, f.admin, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT resource_id::text FROM account_readiness_facts WHERE reported_by_account_id=$1 AND stop_kind='money_402'`, f.account.ID).Scan(&resource)
	})
	if scalar(t, f.admin, `SELECT count(*) FROM account_readiness_memberships WHERE resource_id=$1 AND account_id=ANY($2::uuid[])`, resource, []string{f.account.ID, other.ID}) != 2 {
		t.Fatal("unresolved balance was divided by person or key")
	}
	f.route(t, 409)
	second.route(t, 409)
	// Key room and null reports do not assert total money replenishment.
	local := bResource(t, second)
	room := 10.0
	at := now.Add(time.Minute)
	bAt(t, &second, at, "g1")
	callStatus(t, second.mod, &second.runner, second.token, "POST", "/api/agent-accounts/"+other.ID+"/probe", encoded(t, probeWrite{DaemonID: "daemon-a", DaemonGeneration: "g1", Available: true, Readiness: &ReadinessReport{BindingRevision: ptrRevision(0), Result: "success", Facts: []ReadinessFactWrite{{ResourceID: local, WindowKey: "key_cap", Source: "provider", ObservedAt: at, ReadingAt: &at, Remaining: &room, CreditState: "unknown"}}}}), 200, nil)
	second.route(t, 409)
	code, body := call(t, f.mod, &f.admin, "", "GET", "/api/agent-accounts/readiness", "")
	if code != 200 {
		t.Fatalf("readiness projection status %d", code)
	}
	if strings.Contains(string(body), "money_402") || strings.Contains(string(body), resource) {
		t.Fatal("shared balance detail crossed the other owner's privacy")
	}
	callStatus(t, second.mod, &peer, "", "POST", "/api/agent-accounts/"+other.ID+"/check", requestBody("early", 0), 202, nil)
	run, _ := second.route(t, 200)
	f.route(t, 409)
	bFinish(t, second, run, at, "completed", 3)
	if scalar(t, f.admin, `SELECT count(*) FROM account_readiness_facts WHERE resource_id=$1 AND stop_kind='money_402'`, resource) != 0 {
		t.Fatal("successful shared-resource inference left stop")
	}
	f.route(t, 200)
}

func TestBFreshFactBudgetsShareLedgerAndStaleRoomDoesNotFenceClaim(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	f := readinessWorld(t, "b-facts", now)
	resource := bResource(t, f)
	used := 99.0
	reset := now.Add(5 * time.Hour)
	report := func(at time.Time, amount float64) {
		t.Helper()
		bAt(t, &f, at, "g1")
		callStatus(t, f.mod, &f.runner, f.token, "POST", "/api/agent-accounts/"+f.account.ID+"/probe", encoded(t, probeWrite{DaemonID: "daemon-a", DaemonGeneration: "g1", Available: true, Readiness: &ReadinessReport{BindingRevision: ptrRevision(0), Result: "success", Facts: []ReadinessFactWrite{{ResourceID: resource, WindowKey: "five_hour", Source: "harness", ObservedAt: at, ReadingAt: &at, ResetsAt: &reset, UsedPercent: &amount, CreditState: "unknown"}}}}), 200, nil)
	}
	// The whole measured window fits today; no invented start fence applies.
	s := capacity.DefaultSchedule("UTC")
	for i := range s.Week {
		s.Week[i] = capacity.Day{On: true, Start: 0, End: 24}
	}
	s.Reserve = capacity.ReserveOff
	callStatus(t, f.mod, &f.admin, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, scheduleOverride{Scope: "account", AccountID: f.account.ID, Schedule: &s}), 204, nil)
	report(now, used)
	run, out := f.route(t, 200)
	f.route(t, 409)
	if len(out.Reservations) != 1 {
		t.Fatal("fact budget not reserved exactly once")
	}
	later := now.Add(11 * time.Minute)
	bAt(t, &f, later, "g2")
	ctx := context.WithValue(dbtest.Seed(t.Context()), clockKey{}, later)
	if err := db.InTenant(ctx, appPool, f.admin.TenantID, func(tx pgx.Tx) error { return ValidateReservedCapacity(ctx, tx, run, f.account.ID) }); err != nil {
		t.Fatalf("stale room prevented claim: %v", err)
	}
	f.route(t, 200) // Unknown room is bounded by real slots, not stale 99%.
	report(later.Add(time.Minute), 100)
	f.route(t, 409)
}

func TestBUnknownScheduleDSTNightsAndRunNow(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Vienna")
	for _, month := range []time.Month{time.March, time.October} {
		t.Run(month.String(), func(t *testing.T) {
			day := 29
			if month == time.October {
				day = 25
			}
			now := time.Date(2026, month, day, 3, 30, 0, 0, loc)
			f := readinessWorld(t, "b-dst-"+month.String(), now)
			s := capacity.DefaultSchedule(loc.String())
			s.Week = capacity.Preset(7)
			s.Reserve = capacity.ReserveOff
			save := func() {
				callStatus(t, f.mod, &f.admin, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, scheduleOverride{Scope: "account", AccountID: f.account.ID, Schedule: &s}), 204, nil)
			}
			save()
			f.route(t, 409)
			// A just-reset room reading is history. Its presence cannot bypass
			// the person's clock once admission has become unknown again.
			f.report(t, 25, now.Add(-time.Minute), now)
			f.route(t, 409)
			seed(t, f.admin, func(tx pgx.Tx) error {
				a, err := getAccount(t.Context(), tx, f.account.ID)
				if err != nil {
					return err
				}
				_, w, err := admission(t.Context(), tx, a, a.Windows, now, 0, runRow{Purpose: "managed"}, false)
				if err == nil && (w == nil || w.Code != "schedule" || w.Until == nil || w.Until.In(loc).Hour() != 8 || !w.RunNowAllowed) {
					t.Fatalf("DST schedule wait: %+v", w)
				}
				return err
			})
			s.Nights = true
			save()
			run, _ := f.route(t, 200)
			seed(t, f.admin, func(tx pgx.Tx) error { return Release(t.Context(), tx, f.runner, run, "", "") })
			s.Nights = false
			save()
			seed(t, f.admin, func(tx pgx.Tx) error {
				a, err := getAccount(t.Context(), tx, f.account.ID)
				if err != nil {
					return err
				}
				_, w, err := admission(t.Context(), tx, a, a.Windows, now, 0, runRow{Purpose: "managed", CapacityOverride: "now"}, false)
				if err == nil && w != nil {
					t.Fatalf("Run now lost across DST: %+v", w)
				}
				return err
			})
		})
	}
}

func TestBNullReadingCannotRenewStaleMeasuredRoom(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	f := readinessWorld(t, "b-null-room", now)
	resource := bResource(t, f)
	used := 10.0
	reset := now.Add(5 * time.Hour)
	report := func(at time.Time, value *float64) {
		t.Helper()
		bAt(t, &f, at, "g1")
		callStatus(t, f.mod, &f.runner, f.token, "POST", "/api/agent-accounts/"+f.account.ID+"/probe", encoded(t, probeWrite{DaemonID: "daemon-a", DaemonGeneration: "g1", Available: true, Readiness: &ReadinessReport{BindingRevision: ptrRevision(0), Result: "success", Facts: []ReadinessFactWrite{{ResourceID: resource, WindowKey: "five_hour", Source: "harness", ObservedAt: at, ReadingAt: &at, ResetsAt: &reset, UsedPercent: value, CreditState: "unknown"}}}}), 200, nil)
	}
	report(now, &used)
	later := now.Add(11 * time.Minute)
	report(later, nil)
	if scalar(t, f.admin, `SELECT count(*) FROM account_readiness_facts WHERE resource_id=$1 AND window_key='five_hour' AND reading_at=$2 AND used_percent=10`, resource, now) != 1 {
		t.Fatal("null capture renewed old room timestamp")
	}
	var out struct{ Items []AccountReadiness }
	callStatus(t, f.mod, &f.admin, "", "GET", "/api/agent-accounts/readiness", "", 200, &out)
	if len(out.Items) != 1 || out.Items[0].State != "unknown" || !out.Items[0].CanTry || len(out.Items[0].MeasuredUsage) != 0 || out.Items[0].DisplayReason != UsageUnknownReason {
		t.Fatalf("null capture claimed fresh/Ready: %+v", out.Items)
	}
}
