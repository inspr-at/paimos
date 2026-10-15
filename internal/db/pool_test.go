// SPDX-License-Identifier: AGPL-3.0-only

package db_test

import (
	"context"
	"errors"
	"net/url"
	"runtime"
	"strings"
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

// Risk: attribution hides the connection origin or overrides explicit operator
// naming. Reconfiguring a copied pool must retain the default-name provenance.
func TestPoolApplicationNameAttribution(t *testing.T) {
	for _, tc := range []struct{ url, name string }{
		{"postgres://localhost/fixture", "aeon-main"},
		{"postgres://localhost/fixture?application_name=operator", "operator"},
		{"host=localhost dbname=fixture application_name=operator", "operator"},
		{"postgres://localhost/fixture?application_name=", ""},
	} {
		cfg, err := db.ParsePoolConfig(tc.url)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ConfigurePool(cfg); err != nil {
			t.Fatal(err)
		}
		if got := cfg.ConnConfig.RuntimeParams["application_name"]; got != tc.name {
			t.Fatalf("name %q want %q", got, tc.name)
		}
	}
}

// Risk: sharing sessions makes advisory locks reentrant and accidentally lets
// more than two streams of one key through; replica admission and loss must
// retain the prior cross-process bound without per-stream backends.
func TestSharedSubscriptionAdmissionReplicaLimitAndConnectionLoss(t *testing.T) {
	d := dbtest.Open(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	open := func() *pgxpool.Pool {
		cfg, err := db.ParsePoolConfig(d.AppURL)
		if err != nil {
			t.Fatal(err)
		}
		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.CloseNotificationConnections(pool); pool.Close() })
		return pool
	}
	one, two := open(), open()
	first, err := db.AdmitSubscription(ctx, one, "tenant:key")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := db.AdmitSubscription(ctx, one, "tenant:key")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	var pid int
	if err := d.Admin.QueryRow(ctx, `SELECT pid FROM pg_stat_activity WHERE datname=current_database() AND application_name='aeon-events-admission'`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	for _, pool := range []*pgxpool.Pool{one, two} {
		if lease, err := db.AdmitSubscription(ctx, pool, "tenant:key"); !errors.Is(err, db.ErrSubscriptionLimit) {
			if lease != nil {
				lease.Close()
			}
			t.Fatalf("third stream admitted: %v", err)
		}
	}
	second.Close()
	third, err := db.AdmitSubscription(ctx, two, "tenant:key")
	if err != nil {
		t.Fatal(err)
	}
	defer third.Close()
	var terminated bool
	if err := d.Admin.QueryRow(ctx, `SELECT pg_terminate_backend($1)`, pid).Scan(&terminated); err != nil || !terminated {
		t.Fatal("failed to terminate admission session", err)
	}
	if err := first.Check(ctx); err == nil {
		t.Fatal("lost advisory session still granted admission")
	}
	select {
	case <-first.Context().Done():
	default:
		t.Fatal("lost admission did not cancel stream")
	}
	replacement, err := db.AdmitSubscription(ctx, two, "tenant:key")
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	var sessions int
	if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND application_name='aeon-events-admission'`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if sessions != 1 {
		t.Fatalf("replica retained %d admission sessions, want one", sessions)
	}
	third.Close()
	replacement.Close()
	first.Close()
	for ctx.Err() == nil {
		if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND application_name='aeon-events-admission'`).Scan(&sessions); err != nil {
			t.Fatal(err)
		}
		if sessions == 0 {
			break
		}
		runtime.Gosched()
	}
	if sessions != 0 {
		t.Fatal("admission session leaked after final lease close")
	}
}

// Risk: dedicated defaults override an explicit URL application_name, or a
// shared listener creates duplicate sessions and fails to close its last one.
func TestSharedListenerAttributionAndFinalClose(t *testing.T) {
	for _, tc := range []struct{ name, param, value, want string }{
		{"default", "", "", "aeon-events-listener"},
		{"explicit", "application_name", "operator", "operator"},
		{"explicit main", "application_name", "aeon-main", "aeon-main"},
		{"options", "options", "-c application_name=operator", "operator"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := dbtest.Open(t)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			u, err := url.Parse(d.AppURL)
			if err != nil {
				t.Fatal(err)
			}
			if tc.param != "" {
				query := u.Query()
				query.Set(tc.param, tc.value)
				u.RawQuery = strings.ReplaceAll(query.Encode(), "+", "%20")
			}
			cfg, err := db.ParsePoolConfig(u.String())
			if err != nil {
				t.Fatal(err)
			}
			pool, err := pgxpool.NewWithConfig(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			defer db.CloseNotificationConnections(pool)
			first, err := db.SubscribeNotifications(ctx, pool, "aeon_events", "one")
			if err != nil {
				t.Fatal(err)
			}
			defer first.Close(context.Background())
			second, err := db.SubscribeNotifications(ctx, pool, "aeon_events", "two")
			if err != nil {
				t.Fatal(err)
			}
			defer second.Close(context.Background())
			var name string
			var count int
			if err := d.Admin.QueryRow(ctx, `SELECT min(application_name),count(*) FROM pg_stat_activity WHERE datname=current_database() AND query='LISTEN aeon_events'`).Scan(&name, &count); err != nil {
				t.Fatal(err)
			}
			if name != tc.want || count != 1 {
				t.Fatalf("listener name=%q count=%d want %q and one", name, count, tc.want)
			}
			if err := first.Close(ctx); err != nil {
				t.Fatal(err)
			}
			if err := second.Close(ctx); err != nil {
				t.Fatal(err)
			}
			for ctx.Err() == nil {
				if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND query='LISTEN aeon_events'`).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count == 0 {
					break
				}
				runtime.Gosched()
			}
			if count != 0 {
				t.Fatal("last subscriber leaked its backend")
			}
			if _, err := db.SubscribeNotifications(ctx, pool, "unbounded-channel", "one"); err == nil {
				t.Fatal("arbitrary channel admitted")
			}
		})
	}
}
