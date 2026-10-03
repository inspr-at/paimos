// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestCapacityCheckQuotaWatermarkAcrossPoolResources(t *testing.T) {
	for _, healthyKind := range []string{"account", "person_confirmed"} {
		for _, transition := range []string{"recovery", "reset", "healthy-first"} {
			t.Run(healthyKind+"/"+transition, func(t *testing.T) {
				now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
				clock := &fix4Clock{at: now}
				f := readinessWorld(t, "capacity-quota-watermark", now)
				second := quotaPoolComputer(t, f)
				project, run := insertProjectRun(t, f.admin, f.runner, f.profile, "Capacity pool")
				lead := quotaSession(t, f, project, "", nil, "Lead")
				quotaSession(t, f, project, run, &lead, "Worker")
				mux := http.NewServeMux()
				New(appPool).Mount(mux)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					ctx := context.WithValue(tenant.WithPrincipal(r.Context(), f.runner), clockKey{}, clock.now())
					mux.ServeHTTP(w, r.WithContext(ctx))
				}))
				defer server.Close()
				remote := agentd.NewRemote(server.URL, f.token)
				for _, a := range []Account{f.account, second.account} {
					if err := remote.Probe(t.Context(), a.ID, a.DaemonID, "g1", true); err != nil {
						t.Fatal(err)
					}
				}
				// Select identities explicitly: UUID sort order must not determine
				// whether local and shared observations exercise separate paths.
				report := func(a Account, kinds []string, used float64, reading, reset time.Time) {
					t.Helper()
					facts := []agentd.CapacityCheckFact{}
					for _, kind := range kinds {
						var resource string
						if err := adminPool.QueryRow(t.Context(), `SELECT r.id::text FROM account_readiness_resources r
							JOIN account_readiness_memberships m ON m.tenant_id=r.tenant_id AND m.resource_id=r.id
							WHERE m.account_id=$1 AND m.binding_revision=0 AND r.identity_kind=$2 AND r.kind='subscription_quota'`, a.ID, kind).Scan(&resource); err != nil {
							t.Fatal(err)
						}
						facts = append(facts, agentd.CapacityCheckFact{ResourceID: resource, WindowKey: "5h:", Source: "agentd", ObservedAt: clock.now(), ReadingAt: &reading, ResetsAt: &reset, UsedPercent: &used, CreditState: "unknown", StopKind: "none"})
					}
					if err := remote.ReportCapacityCheck(t.Context(), a.ID, a.DaemonID, "g1", agentd.ProbeStatus{OK: true}, agentd.CapacityCheckReport{BindingRevision: 0, Result: "success", Facts: facts}); err != nil {
						t.Fatal(err)
					}
				}
				reset := now.Add(time.Hour)
				if transition != "healthy-first" {
					report(f.account, []string{"account"}, 92, now, reset)
					if got := quotaSessionWarnings(t, f, now); len(got) != 1 || got[0].RemainingPercent != 8 {
						t.Fatalf("initial early warning: %+v", got)
					}
				}
				newReset := reset
				if transition == "reset" {
					newReset = reset.Add(time.Hour)
				}
				clock.add(3 * time.Second)
				recoveryAt := clock.now()
				report(second.account, []string{healthyKind}, 80, recoveryAt, newReset)
				if got := quotaSessionWarnings(t, f, clock.now()); len(got) != 0 {
					t.Fatalf("healthy capacity check did not clear warning: %+v", got)
				}
				before := scalar(t, f.admin, `SELECT count(*) FROM inbox_messages WHERE idempotency_key LIKE 'quota-warning/%'`)
				delayedKind := "person_confirmed"
				if healthyKind == "person_confirmed" {
					delayedKind = "account"
				}
				clock.add(time.Second)
				// Replayed captures preserve reading_at even when delivered later.
				// Neither an older nor a conflicting equal-time capture may reopen
				// the episode through another representation of the same pool.
				for _, reading := range []time.Time{now.Add(2 * time.Second), recoveryAt} {
					report(f.account, []string{delayedKind}, 98, reading, reset)
					if got := quotaSessionWarnings(t, f, clock.now()); len(got) != 0 {
						t.Fatalf("delayed capacity check undid recovery: %+v", got)
					}
					if n := scalar(t, f.admin, `SELECT count(*) FROM account_quota_warning_observations`); n != 1 {
						t.Fatalf("one quota/window must have one watermark, got %d", n)
					}
					if n := scalar(t, f.admin, `SELECT count(*) FROM account_quota_warning_observations WHERE quota_key=$1 AND window_key='5h:' AND reading_at=$2 AND remaining_percent=20 AND resets_at=$3`, "pool:codex:"+strings.Repeat("b", 64), recoveryAt, newReset); n != 1 {
						t.Fatal("capacity check replaced the healthy pool watermark")
					}
					if after := scalar(t, f.admin, `SELECT count(*) FROM inbox_messages WHERE idempotency_key LIKE 'quota-warning/%'`); after != before {
						t.Fatalf("delayed capture sent a notice: before=%d after=%d", before, after)
					}
				}
				// The daemon can report both memberships in one capture. A truly
				// newer low reading must still warn, exactly once for the pool.
				clock.add(time.Second)
				report(f.account, []string{"account", "person_confirmed"}, 98, clock.now(), newReset)
				if got := quotaSessionWarnings(t, f, clock.now()); len(got) != 1 || got[0].Severity != "urgent" || got[0].RemainingPercent != 2 {
					t.Fatalf("newer capacity check must restore current warning: %+v", got)
				}
				if after := scalar(t, f.admin, `SELECT count(*) FROM inbox_messages WHERE idempotency_key LIKE 'quota-warning/%'`); after != before+1 {
					t.Fatalf("newer capture needs exactly one notice: before=%d after=%d", before, after)
				}
			})
		}
	}
}
