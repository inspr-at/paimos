// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
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
				_, err = tx.Exec(t.Context(), `UPDATE model_profiles SET harness=$2 WHERE id=$1`, f.profile, harness)
				return err
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
			if scalar(t, f.admin, `SELECT count(*) FROM account_readiness_facts WHERE resource_id=$1 AND stop_kind='none' AND wait_id IS NULL AND next_attempt_at IS NULL AND backoff_step=0`, resource) != 1 {
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
	// Both callers are ready before either transaction enters admission.
	start, ready := make(chan struct{}), make(chan struct{}, 2)
	statuses := make(chan int, 2)
	var wg sync.WaitGroup
	for _, run := range runs {
		wg.Add(1)
		go func(run string) {
			defer wg.Done()
			ready <- struct{}{}
			<-start
			code, _ := call(t, f.mod, &f.runner, f.token, "POST", "/api/agent-accounts/route", routeBody(t, run, "daemon-a", []Account{f.account}, map[string]int64{"requests": 1}))
			statuses <- code
		}(run)
	}
	<-ready
	<-ready
	close(start)
	wg.Wait()
	close(statuses)
	counts := map[int]int{}
	for code := range statuses {
		counts[code]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatalf("recovery race statuses: %v", counts)
	}
	if scalar(t, f.admin, `SELECT count(*) FROM account_readiness_facts WHERE resource_id=$1 AND recovery_run_id IS NOT NULL AND early_recovery_used AND recovery_check_id=$2`, resource, c.ID) != 1 {
		t.Fatal("race did not consume exactly the snapshotted early intent")
	}
	bAt(t, &f, now.Add(time.Hour), "g2")
	f.route(t, 409)
	var held string
	if err := adminPool.QueryRow(t.Context(), `SELECT recovery_run_id::text FROM account_readiness_facts WHERE resource_id=$1`, resource).Scan(&held); err != nil {
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
