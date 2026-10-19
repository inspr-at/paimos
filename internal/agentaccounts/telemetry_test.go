// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/jackc/pgx/v5"
)

// Risk: routine probes grow the audit log, or suppressing ticks loses a real
// availability, error, readiness or quota-warning transition. Injected time
// proves the latest row advances independently of the event count.
func TestProbeEventsOnlyForVisibleStateChanges(t *testing.T) {
	now := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	f := readinessWorld(t, "probe-events", now)
	f.mod = accountsMod()
	path := "/api/agent-accounts/" + f.account.ID + "/probe"
	count := func() int64 { return scalar(t, f.admin, `SELECT count(*) FROM events WHERE type='account.probed'`) }
	post := func(in probeWrite, at time.Time, delta int64) {
		t.Helper()
		before := count()
		status, raw := callAt(t, f.mod, &f.runner, f.token, "POST", path, encoded(t, in), at)
		if status != 200 {
			t.Fatalf("probe %d: %s", status, raw)
		}
		if got := count() - before; got != delta {
			t.Fatalf("probe appended %d events, want %d", got, delta)
		}
	}
	in := probeWrite{DaemonID: "daemon-a", DaemonGeneration: "g1", Available: true}
	post(in, now.Add(time.Minute), 0)
	var at time.Time
	seed(t, f.admin, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT last_probe_at FROM agent_accounts WHERE id=$1`, f.account.ID).Scan(&at)
	})
	if !at.Equal(now.Add(time.Minute)) {
		t.Fatal("quiet probe did not update latest health")
	}
	in.Available = false
	post(in, now.Add(2*time.Minute), 1)
	post(in, now.Add(3*time.Minute), 0)
	in.Failure = "auth_failed"
	post(in, now.Add(4*time.Minute), 1)
	post(in, now.Add(5*time.Minute), 0)
	in.Available, in.Failure = true, ""
	post(in, now.Add(6*time.Minute), 1)
	var resource string
	seed(t, f.admin, func(tx pgx.Tx) error {
		var err error
		resource, err = localReadinessResource(t.Context(), tx, f.account)
		return err
	})
	report := func(minute int, used float64, delta int64) {
		t.Helper()
		at := now.Add(time.Duration(minute) * time.Minute)
		reset := now.Add(4 * time.Hour)
		in.Readiness = &ReadinessReport{BindingRevision: ptrRevision(0), Result: "success", Facts: []ReadinessFactWrite{{ResourceID: resource, WindowKey: "5h:codex", Source: "agentd", ObservedAt: at, ReadingAt: &at, ResetsAt: &reset, UsedPercent: &used, CreditState: "unknown", StopKind: "none"}}}
		post(in, at, delta)
	}
	// Measurement-only samples still audit real readiness/warning changes.
	in.MeasurementOnly = true
	report(7, 10, 1)
	report(8, 11, 0)
	report(9, 91, 1)
	report(10, 92, 0)
	report(11, 98, 1)
	report(12, 99, 0)
	report(13, 20, 1)
	// A pending check from an old generation may commit a health heartbeat
	// while rejecting stale facts. Exercise both the quiet and changed path.
	for i, available := range []bool{true, false} {
		at := now.Add(time.Duration(14+i*2) * time.Minute)
		seed(t, f.admin, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET last_probe_at=$2,last_probe_ok=true,last_probe_failure='' WHERE id=$1`, f.account.ID, at)
			return err
		})
		var check AccountCheck
		status, raw := callAt(t, f.mod, &f.admin, "", "POST", strings.TrimSuffix(path, "/probe")+"/check", requestBody(fmt.Sprintf("restart-%d", i), 0), at)
		if status != 202 || json.Unmarshal(raw, &check) != nil {
			t.Fatalf("restart check: %d %s", status, raw)
		}
		in.MeasurementOnly = false
		in.DaemonGeneration = fmt.Sprintf("g%d", i+2)
		in.Available = available
		in.Readiness = &ReadinessReport{CheckID: check.ID, BindingRevision: ptrRevision(0), Result: "success"}
		before := count()
		status, raw = callAt(t, f.mod, &f.runner, f.token, "POST", path, encoded(t, in), at.Add(time.Second))
		var refusal struct {
			Code string `json:"code"`
		}
		if status != 409 || json.Unmarshal(raw, &refusal) != nil || refusal.Code != "stale_binding" {
			t.Fatalf("restart refusal: %d %s", status, raw)
		}
		if got := count() - before; got != int64(i) {
			t.Fatalf("restart appended %d events, want %d", got, i)
		}
	}
}

// Risk: probe state comparison evaluates readiness, which paces fact windows.
// A fact window opens at its second-precise reading; whole-minute lengths put
// the derived open up to a minute later, after the reading and the current
// time, so Plan rejected a future window and the probe returned 500.
func TestFactWindowPacingOpensAtReading(t *testing.T) {
	person, _, _, a := learningAccount(t, "codex")
	readAt := time.Date(2026, 10, 2, 12, 0, 3, 0, time.UTC)
	reset := readAt.Truncate(time.Hour).Add(time.Hour)
	windows := []Window{{AccountID: a.ID, StartsAt: readAt, EndsAt: reset, Unit: "percent", Allowance: 100, Used: 92,
		PaceModel: "unrestricted", capacityReadAt: &readAt, capacityAllowed: true, capacityKind: "fact", capacityBucket: "local:5h:", capacitySource: "agentd"}}
	inLearning(t, person, func(tx pgx.Tx) error {
		if err := applyCapacityPacing(t.Context(), tx, a, windows, readAt, capacity.DefaultSchedule()); err != nil {
			t.Fatalf("fact window read at %s, resetting at %s: %v", readAt, reset, err)
		}
		if b := windows[0].capacityBudget; b == nil || *b < 0 || *b > 8 {
			t.Fatalf("fact window budget %v, want within the 8%% left", b)
		}
		return nil
	})
}
