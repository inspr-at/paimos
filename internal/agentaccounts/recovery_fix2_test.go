// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestBRecoveryPromotesReservationBeforeDenial(t *testing.T) {
	base := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	for _, kind := range []string{"unnamed", "money_402"} {
		for _, early := range []bool{false, true} {
			name := kind + "-expiry"
			if early {
				name = kind + "-early"
			}
			t.Run(name, func(t *testing.T) {
				f := readinessWorld(t, "b-promote-"+name, base)
				first, grant := f.route(t, 200)
				second, _ := f.route(t, 200)
				resource := bStop(t, f, base, kind)
				callStatus(t, f.mod, &f.runner, f.token, "POST", "/api/agent-accounts/route", routeBody(t, first, "daemon-a", []Account{f.account}, map[string]int64{"requests": 1}), 409, nil)
				now := base.Add(time.Hour)
				if early {
					now = base.Add(time.Minute)
				}
				bAt(t, &f, now, "g2")
				var check AccountCheck
				if early {
					callStatus(t, f.mod, &f.admin, "", "POST", "/api/agent-accounts/"+f.account.ID+"/check", requestBody("promote", 0), 202, &check)
				}
				// Both queued holds predate the denial. They must not deadlock
				// recovery, and only one may acquire this resource's permit.
				replay := mustRoute(t, f.mod, f.runner, f.token, first, "daemon-a", []Account{f.account}, map[string]int64{"requests": 1})
				if len(replay.Reservations) != len(grant.Reservations) || replay.Reservations[0].ReservationID != grant.Reservations[0].ReservationID {
					t.Fatal("promotion replaced the existing reservation")
				}
				if scalar(t, f.admin, `SELECT count(*) FROM account_readiness_facts WHERE resource_id=$1 AND recovery_run_id=$2 AND early_recovery_used=$3`, resource, first, early) != 1 {
					t.Fatal("claim did not atomically bind the recovery permit")
				}
				if early && scalar(t, f.admin, `SELECT count(*) FROM account_readiness_facts WHERE resource_id=$1 AND recovery_check_id=$2`, resource, check.ID) != 1 {
					t.Fatal("promotion lost the early-check identity")
				}
				// A replay retains the same permit, including its early identity.
				mustRoute(t, f.mod, f.runner, f.token, first, "daemon-a", []Account{f.account}, map[string]int64{"requests": 1})
				callStatus(t, f.mod, &f.runner, f.token, "POST", "/api/agent-accounts/route", routeBody(t, second, "daemon-a", []Account{f.account}, map[string]int64{"requests": 1}), 409, nil)
				f.route(t, 409)
				bFinish(t, f, first, now, "completed", 5)
				mustRoute(t, f.mod, f.runner, f.token, second, "daemon-a", []Account{f.account}, map[string]int64{"requests": 1})
			})
		}
	}
}

func TestBManualAllowanceDoesNotSupplyUnknownMeasurement(t *testing.T) {
	now := time.Date(2026, 10, 2, 2, 0, 0, 0, time.UTC)
	f := readinessWorld(t, "b-manual-unknown", now)
	s := capacity.DefaultSchedule("UTC")
	s.Week = capacity.Preset(7)
	s.Reserve = capacity.ReserveOff
	callStatus(t, f.mod, &f.admin, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, scheduleOverride{Scope: "account", AccountID: f.account.ID, Schedule: &s}), 204, nil)
	callStatus(t, f.mod, &f.admin, "", "POST", "/api/agent-accounts/"+f.account.ID+"/windows", windowBody(now.Add(-time.Hour), now.Add(24*time.Hour), "requests", 10, "unrestricted"), 201, nil)
	seed(t, f.admin, func(tx pgx.Tx) error {
		a, err := getAccount(t.Context(), tx, f.account.ID)
		if err != nil {
			return err
		}
		_, wait, err := admission(t.Context(), tx, a, a.Windows, now, 0, runRow{Purpose: "managed"}, false)
		if err == nil && (wait == nil || wait.Code != "schedule" || !wait.RunNowAllowed || wait.Until == nil || wait.Until.Hour() != 8) {
			t.Fatalf("manual allowance bypassed the unknown schedule: %+v", wait)
		}
		if err != nil {
			return err
		}
		_, wait, err = admission(t.Context(), tx, a, a.Windows, now, 0, runRow{Purpose: "managed", CapacityOverride: "now"}, false)
		if err == nil && wait != nil {
			t.Fatalf("Run now did not bypass the schedule: %+v", wait)
		}
		return err
	})
	f.route(t, 409)
}

func TestBRecoveryPromotesUnimportedLegacyStop(t *testing.T) {
	base := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	f := readinessWorld(t, "b-promote-legacy", base)
	first, grant := f.route(t, 200)
	second, _ := f.route(t, 200)
	cause := insertRun(t, f.admin, f.runner, f.profile)
	resource := bResource(t, f)
	// A persisted pre-upgrade denial has no canonical fact yet. Claim must
	// import its original deadline before binding the durable recovery permit.
	seed(t, f.admin, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET account_id=$2,status='failed' WHERE id=$1`, cause, f.account.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO run_telemetry(tenant_id,run_id,sequence,kind,error_code,at) VALUES($1,$2,1,'usage','vendor_limit',$3)`, f.admin.TenantID, cause, base)
		return err
	})
	bAt(t, &f, base.Add(time.Hour), "g2")
	if scalar(t, f.admin, `SELECT count(*) FROM account_readiness_facts WHERE resource_id=$1 AND window_key='vendor'`, resource) != 0 {
		t.Fatal("fixture imported the legacy stop before claim")
	}
	replay := mustRoute(t, f.mod, f.runner, f.token, first, "daemon-a", []Account{f.account}, map[string]int64{"requests": 1})
	if replay.Reservations[0].ReservationID != grant.Reservations[0].ReservationID ||
		scalar(t, f.admin, `SELECT count(*) FROM account_readiness_facts WHERE resource_id=$1 AND window_key='vendor' AND recovery_run_id=$2 AND observed_at=$3 AND next_attempt_at=$4`, resource, first, base, base.Add(time.Hour)) != 1 {
		t.Fatal("legacy promotion replaced the ledger or restarted its wait")
	}
	code, response := call(t, f.mod, &f.runner, f.token, "POST", "/api/agent-accounts/route", routeBody(t, second, "daemon-a", []Account{f.account}, map[string]int64{"requests": 1}))
	if code != 409 || !strings.Contains(string(response), "reserved capacity is not eligible: vendor") {
		t.Fatalf("legacy recovery allowed another queued run: %d %s", code, response)
	}
}

func TestBConcurrentQueuedClaimsPromoteOneRecovery(t *testing.T) {
	base := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	f := readinessWorld(t, "b-promote-race", base)
	first, _ := f.route(t, 200)
	second, _ := f.route(t, 200)
	resource := bStop(t, f, base, "unnamed")
	now := base.Add(time.Hour)
	bAt(t, &f, now, "g2")
	tracer := &bRaceTracer{held: make(chan struct{}), competing: make(chan struct{}), release: make(chan struct{})}
	config := appPool.Config()
	config.ConnConfig.Tracer = tracer
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	defer func() {
		select {
		case <-tracer.release:
		default:
			close(tracer.release)
		}
	}()
	mod := fixedClockModule{Module: New(pool), at: now}
	statuses := make(chan int, 2)
	claim := func(run string) {
		code, body := call(t, mod, &f.runner, f.token, "POST", "/api/agent-accounts/route", routeBody(t, run, "daemon-a", []Account{f.account}, map[string]int64{"requests": 1}))
		if code == 409 && !strings.Contains(string(body), "reserved capacity is not eligible: vendor") {
			t.Errorf("competing claim failed for another reason: %s", body)
		}
		statuses <- code
	}
	go claim(first)
	bBarrier(t, tracer.held)
	go claim(second)
	bBarrier(t, tracer.competing)
	close(tracer.release)
	counts := map[int]int{}
	for range 2 {
		select {
		case code := <-statuses:
			counts[code]++
		case <-time.After(20 * time.Second):
			t.Fatal("queued claims hung")
		}
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatalf("queued claim race: %v", counts)
	}
	if scalar(t, f.admin, `SELECT count(*) FROM account_readiness_facts WHERE resource_id=$1 AND recovery_run_id=ANY($2::uuid[])`, resource, []string{first, second}) != 1 ||
		scalar(t, f.admin, `SELECT count(*) FROM account_reservations WHERE run_id=ANY($1::uuid[]) AND state='active'`, []string{first, second}) != 2 {
		t.Fatal("promotion lost queued ledgers or minted duplicate permits")
	}
}

func TestBPromotionWaitsForStartedWorkAndPreservesFailedClaims(t *testing.T) {
	base := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	f := readinessWorld(t, "b-promote-started", base)
	queued, _ := f.route(t, 200)
	started, _ := f.route(t, 200)
	seed(t, f.admin, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET status='running',started_at=$2 WHERE id=$1`, started, base)
		return err
	})
	resource := bStop(t, f, base, "unnamed")
	now := base.Add(time.Hour)
	bAt(t, &f, now, "g2")
	path := "/api/agent-accounts/route"
	body := routeBody(t, queued, "daemon-a", []Account{f.account}, map[string]int64{"requests": 1})
	code, response := call(t, f.mod, &f.runner, f.token, "POST", path, body)
	if code != 409 || !strings.Contains(string(response), "reserved capacity is not eligible: vendor") {
		t.Fatalf("started work did not fence promotion: %d %s", code, response)
	}
	if scalar(t, f.admin, `SELECT count(*) FROM account_readiness_facts WHERE resource_id=$1 AND window_key='vendor' AND recovery_run_id IS NULL`, resource) != 1 {
		t.Fatal("blocked claim consumed a permit")
	}
	bFinish(t, f, started, now, "completed", 1)
	// A real manual cap still blocks promotion after started work has left.
	limitPath := "/api/agent-accounts/" + f.account.ID + "/limit"
	callStatus(t, f.mod, &f.admin, "", "PUT", limitPath, `{"amount":1,"unit":"runs","period":"day"}`, 200, nil)
	code, response = call(t, f.mod, &f.runner, f.token, "POST", path, body)
	if code != 409 || !strings.Contains(string(response), "reserved capacity is not eligible: allowance") {
		t.Fatalf("manual cap did not fence promotion: %d %s", code, response)
	}
	if scalar(t, f.admin, `SELECT count(*) FROM account_readiness_facts WHERE resource_id=$1 AND window_key='vendor' AND recovery_run_id IS NULL`, resource) != 1 {
		t.Fatal("failed manual-cap claim consumed a permit")
	}
	callStatus(t, f.mod, &f.admin, "", "DELETE", limitPath, "", 204, nil)
	mustRoute(t, f.mod, f.runner, f.token, queued, "daemon-a", []Account{f.account}, map[string]int64{"requests": 1})
}

func TestBReadinessSharesFactOnlyReserveAdmission(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Vienna")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 29, 14, 0, 0, 0, loc)
	f := readinessWorld(t, "b-ready-fact", now)
	s := capacity.DefaultSchedule(loc.String())
	s.Nights = true
	callStatus(t, f.mod, &f.admin, "", "PUT", "/api/agent-accounts/capacity/schedule", encoded(t, scheduleOverride{Scope: "account", AccountID: f.account.ID, Schedule: &s}), 204, nil)
	resource := bResource(t, f)
	reset := time.Date(2026, 9, 29, 18, 0, 0, 0, loc)
	used := 76.0
	callStatus(t, f.mod, &f.runner, f.token, "POST", "/api/agent-accounts/"+f.account.ID+"/probe", encoded(t, probeWrite{DaemonID: "daemon-a", DaemonGeneration: "g1", Available: true, Readiness: &ReadinessReport{BindingRevision: ptrRevision(0), Result: "success", Facts: []ReadinessFactWrite{{ResourceID: resource, WindowKey: "five_hour", Source: "harness", ObservedAt: now, ReadingAt: &now, ResetsAt: &reset, UsedPercent: &used, CreditState: "unknown"}}}}), 200, nil)
	seed(t, f.admin, func(tx pgx.Tx) error {
		a, err := getAccount(t.Context(), tx, f.account.ID)
		if err != nil {
			return err
		}
		_, wait, err := admission(t.Context(), tx, a, a.Windows, now, 0, runRow{Purpose: "managed"}, false)
		if err != nil {
			return err
		}
		if wait == nil || wait.Code != "reserve" {
			t.Fatalf("fixture must bind the reserve: %+v", wait)
		}
		out, err := loadReadiness(t.Context(), tx, a, now, 0)
		if err == nil && (out.CanTry || out.State != "blocked" || !slices.Contains(out.ReasonCodes, "reserve")) {
			t.Fatalf("fact-only readiness disagrees with admission: %+v", out)
		}
		return err
	})
	f.route(t, 409)
}

func TestBReadinessShowsEligibleRecoveryWithoutConsumingIt(t *testing.T) {
	base := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	for _, kind := range []string{"unnamed", "money_402"} {
		for _, early := range []bool{false, true} {
			name := kind + "-expiry"
			if early {
				name = kind + "-early"
			}
			t.Run(name, func(t *testing.T) {
				f := readinessWorld(t, "b-ready-recover-"+name, base)
				resource := bStop(t, f, base, kind)
				now := base.Add(time.Hour)
				if early {
					now = base.Add(time.Minute)
				}
				bAt(t, &f, now, "g2")
				if early {
					callStatus(t, f.mod, &f.admin, "", "POST", "/api/agent-accounts/"+f.account.ID+"/check", requestBody("ready", 0), 202, nil)
				}
				var out struct{ Items []AccountReadiness }
				callStatus(t, f.mod, &f.admin, "", "GET", "/api/agent-accounts/readiness", "", 200, &out)
				if len(out.Items) != 1 || !out.Items[0].CanTry || out.Items[0].State != "unknown" || out.Items[0].DisplayReason != UsageUnknownReason {
					t.Fatalf("eligible recovery is hidden: %+v", out.Items)
				}
				if scalar(t, f.admin, `SELECT count(*) FROM account_readiness_facts WHERE resource_id=$1 AND stop_kind=$2 AND recovery_run_id IS NULL AND NOT early_recovery_used AND backoff_step=0`, resource, kind) != 1 {
					t.Fatal("readiness consumed or cleared the stop")
				}
				f.route(t, 200)
				callStatus(t, f.mod, &f.admin, "", "GET", "/api/agent-accounts/readiness", "", 200, &out)
				if len(out.Items) != 1 || out.Items[0].CanTry {
					t.Fatalf("occupied recovery still advertised a new try: %+v", out.Items)
				}
			})
		}
	}
}
