// SPDX-License-Identifier: AGPL-3.0-only

package dbtest

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// QueryBarrier pauses the first matching successful statement after PostgreSQL
// has executed it. Tests can observe real lock waits before releasing the writer.
type QueryBarrier struct {
	match  func(string) bool
	used   atomic.Bool
	ready  chan uint32
	resume chan struct{}
	once   sync.Once
}

// BarrierPool creates an isolated pool; fixture/setup queries use the original.
func BarrierPool(t *testing.T, pool *pgxpool.Pool, match func(string) bool) (*pgxpool.Pool, *QueryBarrier, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	b := &QueryBarrier{match: match, ready: make(chan uint32, 1), resume: make(chan struct{})}
	cfg := pool.Config()
	cfg.ConnConfig.Tracer = b
	p, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Release(); cancel(); p.Close() })
	return p, b, ctx
}

func (b *QueryBarrier) TraceQueryStart(ctx context.Context, _ *pgx.Conn, q pgx.TraceQueryStartData) context.Context {
	if b.match(q.SQL) && b.used.CompareAndSwap(false, true) {
		return context.WithValue(ctx, b, true)
	}
	return ctx
}

func (b *QueryBarrier) TraceQueryEnd(ctx context.Context, conn *pgx.Conn, q pgx.TraceQueryEndData) {
	if ctx.Value(b) == true && q.Err == nil {
		b.ready <- conn.PgConn().PID()
		select {
		case <-b.resume:
		case <-ctx.Done():
		}
	}
}

func (b *QueryBarrier) Release() { b.once.Do(func() { close(b.resume) }) }

func (b *QueryBarrier) Wait(t *testing.T, ctx context.Context) uint32 {
	t.Helper()
	return Await(t, ctx, b.ready)
}

func Await[T any](t *testing.T, ctx context.Context, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-ctx.Done():
		t.Fatal("concurrent operation did not reach its barrier: ", ctx.Err())
	}
	var zero T
	return zero
}

// BlockedOrDone returns the actual blocked lock type, or empty when the second
// operation completes. The deadline only bounds failure; no elapsed-time or
// scheduling assumption decides whether writers overlap.
func BlockedOrDone(t *testing.T, ctx context.Context, admin *pgxpool.Pool, holder uint32, done <-chan struct{}) string {
	t.Helper()
	for {
		var lock string
		err := admin.QueryRow(ctx, `SELECT coalesce((SELECT l.locktype FROM pg_locks l
		 WHERE NOT l.granted AND $1::int=ANY(pg_blocking_pids(l.pid)) LIMIT 1),'')`, holder).Scan(&lock)
		if err != nil {
			t.Fatal(err)
		}
		if lock != "" {
			return lock
		}
		select {
		case <-done:
			return ""
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		default:
		}
	}
}
