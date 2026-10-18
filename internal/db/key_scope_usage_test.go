// SPDX-License-Identifier: AGPL-3.0-only
package db_test

import (
	"context"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

// Risk: separate shared/exclusive batches, case aliases or a later use
// declaration could invert key order or downgrade an exclusive change fence.
func TestKeyScopeBatchModesAndOrder(t *testing.T) {
	f := newStatusFixture(t)
	const lower = "10000000-0000-4000-8000-00000000000a"
	const upper = "20000000-0000-4000-8000-00000000000b"
	pool, barrier, ctx := dbtest.BarrierPool(t, f.d.App, func(sql string) bool {
		return strings.Contains(sql, "hashtextextended($1,615)")
	})
	ctx = db.WithKeyScopeChange(ctx, f.tid, strings.ToUpper(upper))
	ctx = db.WithKeyScopeUse(ctx, f.tid, upper, strings.ToUpper(lower), lower)
	done := make(chan error, 1)
	go func() {
		done <- db.InTenant(ctx, pool, f.tid, func(tx pgx.Tx) error {
			probe, err := f.d.App.Begin(ctx)
			if err != nil {
				return err
			}
			defer probe.Rollback(context.Background())
			for _, check := range []struct {
				key, mode string
				free      bool
			}{{lower, "shared", true}, {lower, "exclusive", false}, {upper, "shared", false}, {upper, "exclusive", false}} {
				query := `SELECT pg_try_advisory_xact_lock(hashtextextended($1,615))`
				if check.mode == "shared" {
					query = `SELECT pg_try_advisory_xact_lock_shared(hashtextextended($1,615))`
				}
				var free bool
				if err := probe.QueryRow(ctx, query, f.tid+":"+check.key).Scan(&free); err != nil {
					return err
				}
				if free != check.free {
					t.Errorf("%s probe of %s: free=%v, want %v", check.mode, check.key, free, check.free)
				}
			}
			return db.LockTenant(ctx, tx, f.tid)
		})
	}()
	barrier.Wait(t, ctx)
	probe, err := f.d.App.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Rollback(context.Background())
	for _, check := range []struct {
		key  string
		free bool
	}{{lower, false}, {upper, true}} {
		var free bool
		if err := probe.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1,615))`, f.tid+":"+check.key).Scan(&free); err != nil {
			t.Fatal(err)
		}
		if free != check.free {
			t.Fatalf("unsorted batch: exclusive probe of %s free=%v, want %v", check.key, free, check.free)
		}
	}
	if err := probe.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	barrier.Release()
	if err := dbtest.Await(t, ctx, done); err != nil {
		t.Fatal(err)
	}
}
