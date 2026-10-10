// SPDX-License-Identifier: AGPL-3.0-only

package db_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Risk: backend caches grow throughout pgx's long defaults, while explicit
// deployment tuning must keep working independently for each URL/keyword key.
func TestPoolRecycleDefaultsAndExplicitOverrides(t *testing.T) {
	for _, tc := range []struct {
		name, url              string
		lifetime, jitter, idle time.Duration
	}{
		{"defaults", "postgres://localhost/fixture", 15 * time.Minute, 3 * time.Minute, 3 * time.Minute},
		{"keyword defaults", "host=localhost dbname=fixture", 15 * time.Minute, 3 * time.Minute, 3 * time.Minute},
		{"url overrides", "postgres://localhost/fixture?pool_max_conn_lifetime=1h&pool_max_conn_lifetime_jitter=6m&pool_max_conn_idle_time=10m", time.Hour, 6 * time.Minute, 10 * time.Minute},
		{"keyword overrides and zero", "host=localhost dbname=fixture pool_max_conn_lifetime=20m pool_max_conn_lifetime_jitter=0s pool_max_conn_idle_time=0s", 20 * time.Minute, 0, 0},
		{"lifetime only", "postgres://localhost/fixture?pool_max_conn_lifetime=45m", 45 * time.Minute, 3 * time.Minute, 3 * time.Minute},
		{"idle only", "postgres://localhost/fixture?pool_max_conn_idle_time=9m", 15 * time.Minute, 3 * time.Minute, 9 * time.Minute},
		{"explicit zero jitter", "postgres://localhost/fixture?pool_max_conn_lifetime_jitter=0s", 15 * time.Minute, 0, 3 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := db.ParsePoolConfig(tc.url)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.MaxConnLifetime != tc.lifetime || cfg.MaxConnLifetimeJitter != tc.jitter || cfg.MaxConnIdleTime != tc.idle || cfg.MaxConns != 16 {
				t.Fatalf("recycle config: lifetime=%v jitter=%v idle=%v max=%d", cfg.MaxConnLifetime, cfg.MaxConnLifetimeJitter, cfg.MaxConnIdleTime, cfg.MaxConns)
			}
		})
	}
}

// Risk: small machines lose all request capacity, or session-lock workers
// deadlock while holding their only permitted slot. Cancellation must not leak
// worker permits; tenant work must still execute in an isolated transaction.
func TestPoolSizingAndRetainedSessionAdmission(t *testing.T) {
	for _, tc := range []struct {
		url string
		max int32
	}{
		{"postgres://localhost/fixture", 16},
		{"postgres://localhost/fixture?pool_max_conns=4", 4},
		{"host=localhost dbname=fixture pool_max_conns=20", 20},
	} {
		cfg, err := db.ParsePoolConfig(tc.url)
		if err != nil || cfg.MaxConns != tc.max {
			t.Fatalf("sizing max=%v err=%v", tc.max, err)
		}
	}
	for _, url := range []string{"postgres://localhost/fixture?pool_max_conns=2", "postgres://localhost/fixture?pool_min_conns=17"} {
		if _, err := db.ParsePoolConfig(url); err == nil {
			t.Fatal("invalid connection budget accepted")
		}
	}
	fresh := dbtest.Open(t)
	if err := db.EnsureTenant(t.Context(), fresh.App, "pool-session", "Pool session"); err != nil {
		t.Fatal(err)
	}
	var tid string
	if err := fresh.Admin.QueryRow(t.Context(), `SELECT id::text FROM tenants WHERE slug='pool-session'`).Scan(&tid); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(fresh.AppURL)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 3
	cfg.MinConns = 0
	monitor, err := db.ConfigurePool(cfg)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(db.Background(t.Context()), 10*time.Second)
	defer cancel()
	owner, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Release()
	service := db.WithConnection(ctx, pool, owner)
	borrowed, release, err := db.Acquire(service, pool)
	if err != nil || borrowed != owner {
		t.Fatalf("retained session not reused: %v", err)
	}
	release() // Borrowed release must not return the owner's slot.
	var backend int32
	if err := db.InTenant(service, pool, tid, func(tx pgx.Tx) error {
		var tenant string
		if err := tx.QueryRow(service, `SELECT current_setting('aeon.tenant_id'),pg_backend_pid()`).Scan(&tenant, &backend); err != nil {
			return err
		}
		if tenant != tid {
			t.Errorf("tenant isolation = %s", tenant)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if backend != int32(owner.Conn().PgConn().PID()) {
		t.Fatal("tenant work acquired a second connection")
	}
	// Runtime detector rejects an accidental raw acquire without spending reserve.
	if nested, err := pool.Acquire(service); err == nil {
		nested.Release()
		t.Fatal("nested pool acquire accepted")
	} else if !errors.Is(err, context.Canceled) {
		t.Fatalf("wrong nested rejection: %v", err)
	}
	if stats := db.Stats(pool); stats.NestedAcquires != 1 || stats.Acquired != 1 || stats.BackgroundAcquired != 1 {
		t.Fatalf("nested admission stats: %+v", stats)
	}
	// Cancel admission while its only background slot is retained. The owner
	// remains usable, and foreground acquires still bypass background admission.
	waiting, stop := context.WithCancel(ctx)
	started := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		close(started)
		c, err := pool.Acquire(waiting)
		if c != nil {
			c.Release()
		}
		result <- err
	}()
	<-started
	stop()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled admission: %v", err)
	}
	foreground, err := pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	foreground.Release()
	if err := db.Probe(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	owner.Release()
	rows, err := pool.Query(ctx, `SELECT 1`)
	if err != nil {
		t.Fatal(err)
	}
	if stats := db.Stats(pool); stats.BackgroundAcquired != 1 {
		t.Fatalf("rows did not retain admission: %+v", stats)
	}
	rows.Close()
	if stats := db.Stats(pool); stats.BackgroundAcquired != 0 || stats.Waiting != 0 || stats.AcquireSamples == 0 {
		t.Fatalf("admission leaked: %+v", stats)
	}
	// Injected times establish the sustained-wait threshold and recovery.
	now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	if monitor.WarnWaiting(now, 1) || monitor.WarnWaiting(now.Add(5*time.Second), 1) || !monitor.WarnWaiting(now.Add(6*time.Second), 1) || monitor.WarnWaiting(now.Add(7*time.Second), 1) {
		t.Fatal("incorrect sustained-wait warning cadence")
	}
	if monitor.WarnWaiting(now.Add(8*time.Second), 0) || monitor.WarnWaiting(now.Add(9*time.Second), 1) {
		t.Fatal("recovery did not reset wait warning")
	}
}
