// SPDX-License-Identifier: AGPL-3.0-only

package devicesessions

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/dbtest"
)

// AEON-1731 risk: startup-only cleanup retains abandoned raw state. Drive the
// real sweep loop with a clock and one tick, never a wall-clock sleep.
func TestNativeLoginPurgeOnNextMinute(t *testing.T) {
	d := dbtest.Open(t)
	pool, barrier, guard := dbtest.BarrierPool(t, d.App, func(sql string) bool {
		return strings.Contains(sql, "DELETE FROM native_login_transactions")
	})
	ctx, cancel := context.WithCancel(guard)
	defer cancel()
	var now time.Time
	if err := d.Admin.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	var clock atomic.Int64
	clock.Store(now.UnixNano())
	s := New(pool, func() time.Time { return time.Unix(0, clock.Load()) })
	for _, slug := range []string{"native-purge-a", "native-purge-b"} {
		var tid string
		if err := d.Admin.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES($1,$1) RETURNING id::text`, slug).Scan(&tid); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Start(ctx, Start{TenantID: tid, TenantSlug: slug, Challenge: "challenge", Origin: "https://native.example", State: "abandoned-app-state"}); err != nil {
			t.Fatal(err)
		}
	}
	ticks := make(chan time.Time)
	done := make(chan struct{})
	go func() { s.runPurge(ctx, ticks); close(done) }()
	t.Cleanup(func() {
		cancel()
		barrier.Release()
		// testing cancels t.Context before running cleanup hooks. Joining the
		// worker needs its own hang guard, independent of that cancellation.
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		dbtest.Await(t, cleanup, done)
	})
	barrier.Wait(t, guard)
	var retained int
	if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM native_login_transactions WHERE state_app IS NOT NULL`).Scan(&retained); err != nil || retained != 2 {
		t.Fatalf("startup purge removed live raw state: retained=%d err=%v", retained, err)
	}
	barrier.Release()
	if purgeInterval != time.Minute {
		t.Fatal("native state purge cadence exceeds one minute")
	}
	// Both startup tenant batches captured the old clock. The next tick occurs
	// at the latest permitted cleanup instant and must delete both tenants.
	at := now.Add(LoginTTL + purgeInterval)
	clock.Store(at.UnixNano())
	select {
	case ticks <- at:
	case <-guard.Done():
		t.Fatal("purge worker did not reach the minute tick")
	}
	for {
		var remaining int
		if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM native_login_transactions`).Scan(&remaining); err != nil {
			t.Fatal(err)
		}
		if remaining == 0 {
			break
		}
	}
	cancel()
	dbtest.Await(t, guard, done)
}
