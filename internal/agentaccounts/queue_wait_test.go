// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/accountuse"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

type waitQueryCounts struct{ accounts, windows, occupancy, runs, quota int }

type waitCountingTx struct {
	pgx.Tx
	counts waitQueryCounts
}

func (tx *waitCountingTx) count(query string) {
	query = strings.Join(strings.Fields(query), " ")
	switch {
	case strings.Contains(query, "WHERE $1 OR NOT") && strings.Contains(query, "FROM agent_accounts"):
		tx.counts.accounts++
	case strings.Contains(query, "FROM account_allowance_windows w WHERE w.account_id=ANY"):
		tx.counts.windows++
	case strings.Contains(query, "FROM account_allowance_windows WHERE account_id::text = ANY"):
		tx.counts.windows++
	case strings.HasPrefix(query, "SELECT sibling.id FROM agent_accounts own"):
		tx.counts.quota++
	case strings.HasPrefix(query, "SELECT id::text,agent_principal_id::text,model_profile_id::text,account_id::text,"):
		tx.counts.runs++
	case strings.Contains(query, "GROUP BY account_id"), strings.Contains(query, "GROUP BY a.harness, a.quota_pool_fingerprint"), strings.Contains(query, "GROUP BY r.account_id,a.harness,a.quota_pool_fingerprint"):
		tx.counts.occupancy++
	}
}

func (tx *waitCountingTx) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	tx.count(query)
	return tx.Tx.Query(ctx, query, args...)
}

func (tx *waitCountingTx) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	tx.count(query)
	return tx.Tx.QueryRow(ctx, query, args...)
}

// Risks: queue growth repeats tenant-wide scans; shared advice hides revoked
// grants, quota exhaustion or stale probes and becomes authority for a claim.
func TestQueueWaitBatchScansAndFreshClaimFences(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	f := limitWorldAt(t, "queue-wait-batch", 3, now)
	callStatus(t, f.mod, &f.admin, "", "POST", "/api/agent-accounts/"+f.account.ID+"/windows", windowBody(now.Add(-time.Hour), now.Add(time.Hour), "requests", 100, "unrestricted"), 201, nil)
	var sibling Account
	callStatus(t, f.mod, &f.runner, f.token, "POST", "/api/agent-accounts", `{"account_key":"sibling","harness":"codex","daemon_id":"daemon-b","label":"Sibling"}`, 201, &sibling)
	ownFixtureAccount(t, f.admin, &sibling)
	callStatus(t, f.mod, &f.runner, f.token, "POST", "/api/agent-accounts/"+sibling.ID+"/probe", `{"daemon_id":"daemon-b","daemon_generation":"g1","available":true}`, 200, nil)
	ctx := context.WithValue(dbtest.Seed(t.Context()), clockKey{}, now)
	ids := make([]string, 8)
	for i := range ids {
		ids[i] = insertRun(t, f.admin, f.runner, f.profile)
	}
	seed(t, f.admin, func(tx pgx.Tx) error {
		// Confirmed pools must match the reported login fingerprint. Keep both
		// fixture doors in the pool without bypassing the database constraint.
		if _, err := tx.Exec(ctx, `UPDATE agent_accounts SET quota_fingerprint=$1,quota_pool_fingerprint=$1 WHERE id=ANY($2::uuid[])`, strings.Repeat("ab", 32), []string{f.account.ID, sibling.ID}); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE account_allowance_windows SET used=20 WHERE account_id=$1`, f.account.ID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE agent_runs SET requested_account_id=$1 WHERE id=ANY($2::uuid[])`, f.account.ID, ids)
		return err
	})
	for _, count := range []int{1, 8} {
		t.Run(fmt.Sprintf("%d queued runs", count), func(t *testing.T) {
			err := db.InTenant(ctx, appPool, f.admin.TenantID, func(tx pgx.Tx) error {
				before := &waitCountingTx{Tx: tx}
				want := map[string]*CapacityWait{}
				for _, id := range ids[:count] {
					wait, err := WaitForRun(ctx, before, id)
					if err != nil {
						return err
					}
					if wait != nil {
						t.Fatalf("fixture must have room: %+v", wait)
					}
					want[id] = wait
				}
				after := &waitCountingTx{Tx: tx}
				got, err := WaitForRuns(ctx, after, ids[:count])
				if err != nil {
					return err
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("batch changed advice: got=%+v want=%+v", got, want)
				}
				if before.counts != (waitQueryCounts{count, 2 * count, 2 * count, count, count}) || after.counts != (waitQueryCounts{1, 2, 1, 1, 1}) {
					t.Fatalf("common scans before=%+v after=%+v", before.counts, after.counts)
				}
				t.Logf("%d runs: account/window/occupancy/run/quota reads before=%+v after=%+v", count, before.counts, after.counts)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
	// Reserve while advice is positive, then change one fact at a time. Each
	// final validation uses a new transaction; its old advice is never an input.
	mustRoute(t, f.mod, f.runner, f.token, ids[0], "daemon-a", []Account{f.account}, map[string]int64{"requests": 1})
	for _, tc := range []struct {
		name, query, wait, refusal string
	}{
		{"grant revoked", `DELETE FROM account_use_cells WHERE account_id=$1`, "context", accountuse.NotAllowed},
		// A person can reduce the spendable budget below the existing hold.
		// Overfilling a request ledger instead would violate its DB constraint.
		{"budget reduced", `UPDATE agent_accounts SET usage_floor_percent=80 WHERE id=$1`, "allowance", "reserved capacity is not eligible: allowance"},
		{"probe stale", `UPDATE agent_accounts SET last_probe_at=last_probe_at-interval '3 minutes' WHERE id=$1`, "offline", "reserved account is not eligible"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := db.InTenant(ctx, appPool, f.admin.TenantID, func(tx pgx.Tx) error {
				advice, err := WaitForRuns(ctx, tx, ids[:1])
				if err != nil {
					return err
				}
				if len(advice) != 1 || advice[ids[0]] != nil {
					t.Fatalf("expected positive advice before change: %+v", advice)
				}
				return ValidateReservedCapacity(ctx, tx, ids[0], f.account.ID)
			})
			if err != nil {
				t.Fatal(err)
			}
			err = db.InTenant(ctx, appPool, f.admin.TenantID, func(tx pgx.Tx) error {
				attempt, err := tx.Begin(ctx)
				if err != nil {
					return err
				}
				defer func() { _ = attempt.Rollback(ctx) }()
				if _, err = attempt.Exec(ctx, tc.query, f.account.ID); err != nil {
					return err
				}
				advice, err := WaitForRuns(ctx, attempt, ids[:1])
				if err != nil {
					return err
				}
				if wait := advice[ids[0]]; wait == nil || wait.Code != tc.wait {
					t.Fatalf("fresh read missed change: %+v", wait)
				}
				err = ValidateReservedCapacity(ctx, attempt, ids[0], f.account.ID)
				if err == nil || err.Error() != tc.refusal {
					t.Fatalf("old advice bypassed final claim check: %v; want %q", err, tc.refusal)
				}
				return attempt.Rollback(ctx)
			})
			if err != nil {
				t.Fatal(err)
			}
			if scalar(t, f.admin, `SELECT count(*) FROM account_reservations WHERE run_id=$1 AND state='active'`, ids[0]) != 1 {
				t.Fatal("failed claim changed the existing hold")
			}
		})
	}
	t.Run("retired pool door still occupies slots", func(t *testing.T) {
		err := db.InTenant(ctx, appPool, f.admin.TenantID, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `UPDATE agent_runs SET account_id=$1,status='running' WHERE id=ANY($2::uuid[])`, sibling.ID, ids[1:4]); err != nil {
				return err
			}
			// Retirement keeps existing runs but requires its actor and makes
			// the door unavailable for new work.
			if _, err := tx.Exec(ctx, `UPDATE agent_accounts SET archived_at=$2,archived_by_principal_id=$3,state='unavailable' WHERE id=$1`, sibling.ID, now, f.admin.ID); err != nil {
				return err
			}
			before, err := WaitForRun(ctx, tx, ids[0])
			if err != nil {
				return err
			}
			after, err := WaitForRuns(ctx, tx, ids[:1])
			if err != nil {
				return err
			}
			if before == nil || before.Code != "capacity" || !reflect.DeepEqual(before, after[ids[0]]) {
				t.Fatalf("retired shared door lost its occupied slots: before=%+v after=%+v", before, after[ids[0]])
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
	t.Run("tenant and input bounds", func(t *testing.T) {
		other := makePrincipal(t, "queue-wait-foreign", "person", "Other", []string{"admin"})
		foreignProfile := codexProfile(t, other)
		foreignAgent := addPrincipal(t, other.TenantID, "agent", "Other runner", nil)
		foreignRun := insertRun(t, other, foreignAgent, foreignProfile)
		err := db.InTenant(ctx, appPool, f.admin.TenantID, func(tx pgx.Tx) error {
			got, err := WaitForRuns(ctx, tx, []string{ids[0], foreignRun})
			if !errors.Is(err, pgx.ErrNoRows) || got != nil {
				t.Fatalf("wrong-tenant batch returned partial advice: %+v %v", got, err)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		err = db.InTenant(ctx, appPool, other.TenantID, func(tx pgx.Tx) error {
			err := ValidateReservedCapacity(ctx, tx, ids[0], f.account.ID)
			if err == nil || err.Error() != "run not found" {
				t.Fatalf("wrong-tenant claim returned wrong refusal: %v", err)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		got, err := WaitForRuns(ctx, nil, make([]string, maxWaitRuns+1))
		if err == nil || err.Error() != "queue run snapshot exceeds bound" || got != nil {
			t.Fatalf("overflow was not rejected before DB work: %+v %v", got, err)
		}
		response := httptest.NewRecorder()
		workorders.WriteError(response, err)
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "queue run snapshot exceeds bound") {
			t.Fatalf("overflow lost its safe HTTP refusal: %d %s", response.Code, response.Body.String())
		}
		if got, err := WaitForRuns(ctx, nil, []string{strings.Repeat("a", 4096)}); err == nil || err.Error() != "invalid run id" || got != nil {
			t.Fatalf("oversized identity was not rejected before DB work: %+v %v", got, err)
		}
		if got, err := WaitForRuns(ctx, nil, nil); err != nil || len(got) != 0 {
			t.Fatalf("empty batch performed DB work: %+v %v", got, err)
		}
	})
}
