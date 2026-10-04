// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestBReadinessHonorsPacedManualWindowBeforeRecovery(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	for _, recovery := range []bool{false, true} {
		name := "ordinary"
		if recovery {
			name = "recovery"
		}
		t.Run(name, func(t *testing.T) {
			f := readinessWorld(t, "b-paced-"+name, now)
			var window Window
			callStatus(t, f.mod, &f.admin, "", "POST", "/api/agent-accounts/"+f.account.ID+"/windows", windowBody(now.Add(-time.Minute), now.Add(time.Hour), "requests", 10, "steady"), 201, &window)
			var resource string
			if recovery {
				resource = bStop(t, f, now.Add(-time.Hour), "unnamed")
				// The persisted deadline, rather than a sleep, makes recovery due.
				seed(t, f.admin, func(tx pgx.Tx) error {
					_, err := tx.Exec(t.Context(), `UPDATE account_readiness_facts SET next_attempt_at=$2 WHERE resource_id=$1 AND window_key='vendor'`, resource, now)
					return err
				})
			}
			// Total headroom exists, but the paced allowance cannot spend one unit.
			seed(t, f.admin, func(tx pgx.Tx) error {
				a, err := getAccount(t.Context(), tx, f.account.ID)
				if err != nil {
					return err
				}
				windows, wait, err := admission(t.Context(), tx, a, a.Windows, now, 0, runRow{Purpose: "managed"}, false)
				if err != nil {
					return err
				}
				if wait != nil || len(windows) == 0 || windows[0].Allowance-windows[0].Used-windows[0].Reserved < 1 {
					t.Fatalf("fixture lacks total allowance: %+v %+v", windows, wait)
				}
				if _, ok := fits(windows[0], now, 1); ok {
					t.Fatal("fixture already has spendable paced allowance")
				}
				return nil
			})
			f.route(t, 409)
			var out struct{ Items []AccountReadiness }
			callStatus(t, f.mod, &f.admin, "", "GET", "/api/agent-accounts/readiness", "", 200, &out)
			if len(out.Items) != 1 || out.Items[0].CanTry || out.Items[0].State != "blocked" || !slices.Contains(out.Items[0].ReasonCodes, "manual_limit") {
				t.Fatalf("readiness ignored the paced manual cap: %+v", out.Items)
			}
			if recovery && scalar(t, f.admin, `SELECT count(*) FROM account_readiness_facts WHERE resource_id=$1 AND stop_kind='unnamed' AND recovery_run_id IS NULL AND NOT early_recovery_used AND backoff_step=0`, resource) != 1 {
				t.Fatal("readiness or failed reservation consumed recovery")
			}
			// Opening the pace, with the same window and clock, restores parity.
			seed(t, f.admin, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE account_allowance_windows SET burst_ratio=1 WHERE id=$1`, window.ID)
				return err
			})
			callStatus(t, f.mod, &f.admin, "", "GET", "/api/agent-accounts/readiness", "", 200, &out)
			if len(out.Items) != 1 || !out.Items[0].CanTry || out.Items[0].State != "unknown" {
				t.Fatalf("spendable allowance did not restore honest readiness: %+v", out.Items)
			}
			f.route(t, 200)
		})
	}
}
