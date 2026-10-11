// SPDX-License-Identifier: AGPL-3.0-only

package db_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AEON-1228: a cancelled tenant statement must not make the next request on
// the only pooled connection fail. Production InTenant rolls back with the
// request context (internal/db/db.go). pgx 5.11 destroys a connection that
// is closed, busy, or outside an idle transaction when it is released.
func TestCancelledInTenantDoesNotPoisonTheOnlyConnection(t *testing.T) {
	d := dbtest.Open(t)
	var tid string
	if err := d.App.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('cancel-pool','Cancel pool') RETURNING id::text`).Scan(&tid); err != nil {
		t.Fatal(err)
	}
	pool := singleConnectionPool(t, d.AppURL)

	t.Run("cancel during statement", func(t *testing.T) {
		before := pool.Stat().NewConnsCount()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		started := make(chan int32, 1)
		errs := make(chan error, 1)
		go func() {
			errs <- db.InTenant(dbtest.Seed(ctx), pool, tid, func(tx pgx.Tx) error {
				var pid int32
				if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
					return err
				}
				started <- pid
				_, err := tx.Exec(ctx, `SELECT pg_sleep(30)`)
				return err
			})
		}()
		var sleeping int32
		select {
		case sleeping = <-started:
		case err := <-errs:
			t.Fatalf("tenant statement ended before the backend pid was known: %v", err)
		case <-time.After(15 * time.Second):
			t.Fatal("backend pid was not reported")
		}
		waitForQuery(t, d, sleeping, "pg_sleep")
		cancel()
		var cancelled error
		select {
		case cancelled = <-errs:
		case <-time.After(15 * time.Second):
			t.Fatal("cancelled InTenant did not return")
		}
		if !errors.Is(cancelled, context.Canceled) {
			t.Fatalf("statement did not fail for caller cancellation: %v", cancelled)
		}
		followPID, followErr, elapsed := followUp(t, pool, tid)
		after := pool.Stat().NewConnsCount()
		t.Logf("cancel_err=%v follow_err=%v follow_pid=%d slept_on=%d new_conns=%d->%d follow=%s", cancelled, followErr, followPID, sleeping, before, after, elapsed)
		if followErr != nil {
			t.Fatalf("next InTenant failed after a cancelled statement: %v", followErr)
		}
		if after <= before {
			t.Fatalf("cancelled backend %d stayed in the pool (new_conns %d -> %d); a user-request cancel must destroy it", sleeping, before, after)
		}
		if followPID == sleeping {
			t.Logf("backend pid %d was recycled for the replacement connection", sleeping)
		}
	})

	t.Run("statement timeout with a live caller", func(t *testing.T) {
		before := pool.Stat().NewConnsCount()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var timedOut int32
		err := db.InTenant(dbtest.Seed(ctx), pool, tid, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `SELECT set_config('statement_timeout','200ms',true)`); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&timedOut); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `SELECT pg_sleep(30)`)
			return err
		})
		var pg *pgconn.PgError
		if !errors.As(err, &pg) || pg.Code != "57014" {
			t.Fatalf("statement timeout error = %v", err)
		}
		followPID, followErr, elapsed := followUp(t, pool, tid)
		t.Logf("timeout_err=%v follow_err=%v follow_pid=%d timed_out_on=%d new_conns=%d->%d follow=%s", err, followErr, followPID, timedOut, before, pool.Stat().NewConnsCount(), elapsed)
		if followErr != nil {
			t.Fatalf("next InTenant failed after statement_timeout: %v", followErr)
		}
	})

	t.Run("caller context already cancelled", func(t *testing.T) {
		var warm int32
		if err := db.InTenant(dbtest.Seed(context.Background()), pool, tid, func(tx pgx.Tx) error {
			return tx.QueryRow(context.Background(), `SELECT pg_backend_pid()`).Scan(&warm)
		}); err != nil {
			t.Fatal(err)
		}
		before := pool.Stat().NewConnsCount()
		canceledAcquires := pool.Stat().CanceledAcquireCount()
		dead, cancel := context.WithCancel(context.Background())
		cancel()
		start := time.Now()
		err := db.InTenant(dbtest.Seed(dead), pool, tid, func(pgx.Tx) error {
			t.Error("query function ran on an already-cancelled context")
			return nil
		})
		elapsed := time.Since(start)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("already-cancelled InTenant error = %v", err)
		}
		if pool.Stat().CanceledAcquireCount() != canceledAcquires+1 {
			t.Fatal("already-cancelled InTenant did not fail at pool acquisition")
		}
		followPID, followErr, followElapsed := followUp(t, pool, tid)
		t.Logf("pre_cancel_err=%v pre_cancel=%s follow_err=%v warm_pid=%d follow_pid=%d new_conns=%d->%d follow=%s", err, elapsed, followErr, warm, followPID, before, pool.Stat().NewConnsCount(), followElapsed)
		if followErr != nil {
			t.Fatalf("next InTenant failed after an already-cancelled call: %v", followErr)
		}
		if followPID != warm || pool.Stat().NewConnsCount() != before {
			t.Fatalf("already-cancelled call disturbed the pool: warm %d follow %d new_conns %d -> %d", warm, followPID, before, pool.Stat().NewConnsCount())
		}
	})
}

func singleConnectionPool(t *testing.T, appURL string) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(appURL)
	if err != nil {
		t.Fatal(err)
	}
	// One slot, and no aeon admission tracer: ConfigurePool refuses fewer than
	// three connections. Release still uses pgx 5.11's closed/busy/idle rule.
	cfg.MaxConns = 1
	cfg.MinConns = 0
	cfg.MaxConnLifetime = time.Hour
	cfg.MaxConnLifetimeJitter = 0
	cfg.MaxConnIdleTime = time.Hour
	cfg.HealthCheckPeriod = time.Hour
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func waitForQuery(t *testing.T, d *dbtest.DB, pid int32, fragment string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	poll := time.NewTicker(20 * time.Millisecond)
	defer poll.Stop()
	for {
		var query string
		err := d.Admin.QueryRow(context.Background(), `SELECT query FROM pg_stat_activity WHERE pid=$1 AND state='active'`, pid).Scan(&query)
		if err == nil && strings.Contains(query, fragment) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("backend %d did not show %q as active (last err %v)", pid, fragment, err)
		}
		<-poll.C // pacing only; pg_stat_activity establishes statement execution
	}
}

func followUp(t *testing.T, pool *pgxpool.Pool, tid string) (int32, error, time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var pid int32
	start := time.Now()
	err := db.InTenant(dbtest.Seed(ctx), pool, tid, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid)
	})
	return pid, err, time.Since(start)
}
