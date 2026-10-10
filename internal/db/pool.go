// SPDX-License-Identifier: AGPL-3.0-only

package db

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultMaxConns is independent of host CPU count. Two slots are reserved for
// foreground requests and probes; session-lock workers reuse their one slot.
// Per-server connection budget (Postgres max_connections=50):
//
//	pooled:    16, including session-lock workers and 2 foreground/probe slots
//	dedicated: 1 optional phone-push listener + 1 per active event stream,
//	           agent subscription, inbox stream/long poll, harness notification
//	           stream, or delivery flow stream; chat live uses no DB listener
//	total:     16 + dedicated (not a fixed 16; CLI/admin/replicas need headroom)
//
// With phone push enabled, at most 33 client listeners fit in the remaining
// 34 server slots BEFORE Postgres reserves and other clients. Subscription and
// flow admission caps are each 64, not a global DB cap; lowering the pool alone
// cannot bound client listeners. AEON_DATABASE_URL pool_max_conns overrides 16.
const DefaultMaxConns int32 = 16
const ForegroundReserve int32 = 2
const ProbeAcquireTimeout = 100 * time.Millisecond

// ListenerMaxLifetime bounds dedicated backend caches. SSE listeners close at
// expiry: clients reconnect with their durable cursor (or an initial wake).
// Server-owned listeners reconnect and scan durable state before waiting again.
const ListenerMaxLifetime = 30 * time.Minute

type backgroundKey struct{}

// Background marks server-owned work, including goroutines spawned by a loop.
// Request contexts must never inherit this marker.
func Background(ctx context.Context) context.Context {
	return context.WithValue(ctx, backgroundKey{}, true)
}
func IsBackground(ctx context.Context) bool {
	v, _ := ctx.Value(backgroundKey{}).(bool)
	return v
}

// ParsePoolConfig preserves explicit pgx URL/keyword settings, including service
// settings. Reading the unstripped pgx config avoids guessing the CPU default.
func ParsePoolConfig(url string) (*pgxpool.Config, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, errors.New("invalid database pool configuration")
	}
	raw, err := pgx.ParseConfig(url)
	if err != nil {
		return nil, errors.New("invalid database connection configuration")
	}
	if _, explicit := raw.RuntimeParams["pool_max_conns"]; !explicit {
		cfg.MaxConns = DefaultMaxConns
	}
	if _, explicit := raw.RuntimeParams["pool_max_conn_lifetime"]; !explicit {
		cfg.MaxConnLifetime = 15 * time.Minute
	}
	if _, explicit := raw.RuntimeParams["pool_max_conn_lifetime_jitter"]; !explicit {
		cfg.MaxConnLifetimeJitter = 3 * time.Minute
	}
	if _, explicit := raw.RuntimeParams["pool_max_conn_idle_time"]; !explicit {
		cfg.MaxConnIdleTime = 3 * time.Minute
	}
	if _, err := ConfigurePool(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// ConfigurePool bounds background acquisitions on the existing shared pool, so
// modules and callbacks cannot escape a separate worker-pool limit. Permits last
// until rows close / transactions release, and are not held during loop timers.
func ConfigurePool(cfg *pgxpool.Config) (*PoolMonitor, error) {
	if cfg.MaxConns <= ForegroundReserve {
		return nil, fmt.Errorf("pool_max_conns must be at least %d to reserve request and probe headroom", ForegroundReserve+1)
	}
	if cfg.MinConns > cfg.MaxConns || cfg.MinIdleConns > cfg.MaxConns {
		return nil, errors.New("pool minimum connections exceed pool_max_conns")
	}
	next := cfg.ConnConfig.Tracer
	if prior, ok := next.(*PoolMonitor); ok {
		next = prior.next
	}
	m := &PoolMonitor{slots: make(chan struct{}, cfg.MaxConns-ForegroundReserve), held: make(map[*pgx.Conn]bool), next: next}
	cfg.ConnConfig.Tracer = m
	return m, nil
}

type acquireKey struct{}
type acquisition struct {
	start  time.Time
	permit bool
}

// PoolMonitor is also a pgx acquire/release tracer. Its bounded sample ring
// reports the p95 of the last 256 completed acquisitions (including failures
// and worker admission waits), rather than mislabelling a cumulative average.
type PoolMonitor struct {
	next         pgx.QueryTracer
	slots        chan struct{}
	waiting      atomic.Int64
	nested       atomic.Int64
	mu           sync.Mutex
	held         map[*pgx.Conn]bool
	samples      [256]time.Duration
	count        int
	cursor       int
	waitingSince time.Time
	lastWarn     time.Time
}

func (m *PoolMonitor) TraceQueryStart(ctx context.Context, c *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	if m.next != nil {
		return m.next.TraceQueryStart(ctx, c, d)
	}
	return ctx
}
func (m *PoolMonitor) TraceQueryEnd(ctx context.Context, c *pgx.Conn, d pgx.TraceQueryEndData) {
	if m.next != nil {
		m.next.TraceQueryEnd(ctx, c, d)
	}
}
func (m *PoolMonitor) TraceAcquireStart(ctx context.Context, pool *pgxpool.Pool, d pgxpool.TraceAcquireStartData) context.Context {
	a := &acquisition{start: time.Now()}
	m.waiting.Add(1)
	ctx = context.WithValue(ctx, acquireKey{}, a)
	// Direct pool queries inside a retained session must fail immediately rather
	// than consume the reserve or deadlock. Use Acquire / InTenant instead.
	if retained(ctx, pool) != nil {
		m.nested.Add(1)
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		return cancelled
	}
	if IsBackground(ctx) {
		select {
		case m.slots <- struct{}{}:
			a.permit = true
		case <-ctx.Done():
		}
	}
	if next, ok := m.next.(pgxpool.AcquireTracer); ok {
		ctx = next.TraceAcquireStart(ctx, pool, d)
	}
	return ctx
}
func (m *PoolMonitor) TraceAcquireEnd(ctx context.Context, pool *pgxpool.Pool, d pgxpool.TraceAcquireEndData) {
	if next, ok := m.next.(pgxpool.AcquireTracer); ok {
		next.TraceAcquireEnd(ctx, pool, d)
	}
	a, _ := ctx.Value(acquireKey{}).(*acquisition)
	if a == nil {
		return
	}
	m.waiting.Add(-1)
	m.mu.Lock()
	m.samples[m.cursor] = time.Since(a.start)
	m.cursor = (m.cursor + 1) % len(m.samples)
	m.count = min(m.count+1, len(m.samples))
	if a.permit && d.Err == nil {
		m.held[d.Conn] = true
	}
	m.mu.Unlock()
	if a.permit && d.Err != nil {
		<-m.slots
	}
}
func (m *PoolMonitor) TraceRelease(pool *pgxpool.Pool, d pgxpool.TraceReleaseData) {
	m.mu.Lock()
	permit := m.held[d.Conn]
	delete(m.held, d.Conn)
	m.mu.Unlock()
	if permit {
		<-m.slots
	}
	if next, ok := m.next.(pgxpool.ReleaseTracer); ok {
		next.TraceRelease(pool, d)
	}
}

type PoolStats struct {
	Max                int32   `json:"max"`
	Acquired           int32   `json:"acquired"`
	Idle               int32   `json:"idle"`
	Waiting            int64   `json:"waiting"`
	BackgroundLimit    int     `json:"background_limit"`
	BackgroundAcquired int     `json:"background_acquired"`
	AcquireP95MS       float64 `json:"acquire_duration_p95_ms"`
	AcquireSamples     int     `json:"acquire_samples"`
	NestedAcquires     int64   `json:"nested_acquires"`
}

func monitor(pool *pgxpool.Pool) *PoolMonitor {
	m, _ := pool.Config().ConnConfig.Tracer.(*PoolMonitor)
	return m
}
func Stats(pool *pgxpool.Pool) PoolStats {
	s := pool.Stat()
	out := PoolStats{Max: s.MaxConns(), Acquired: s.AcquiredConns(), Idle: s.IdleConns()}
	if m := monitor(pool); m != nil {
		out.Waiting = m.waiting.Load()
		out.BackgroundLimit = cap(m.slots)
		out.NestedAcquires = m.nested.Load()
		m.mu.Lock()
		out.BackgroundAcquired = len(m.held)
		out.AcquireSamples = m.count
		samples := append([]time.Duration(nil), m.samples[:m.count]...)
		m.mu.Unlock()
		slices.Sort(samples)
		if len(samples) > 0 {
			out.AcquireP95MS = float64(samples[(95*len(samples)+99)/100-1]) / float64(time.Millisecond)
		}
	}
	return out
}

// WarnWaiting uses the supplied clock so sustained-wait reporting is testable.
func (m *PoolMonitor) WarnWaiting(now time.Time, waiting int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if waiting == 0 {
		m.waitingSince = time.Time{}
		m.lastWarn = time.Time{}
		return false
	}
	if m.waitingSince.IsZero() {
		m.waitingSince = now
	}
	if now.Sub(m.waitingSince) <= 5*time.Second || !m.lastWarn.IsZero() && now.Sub(m.lastWarn) < 5*time.Second {
		return false
	}
	m.lastWarn = now
	return true
}
func WatchPool(ctx context.Context, pool *pgxpool.Pool) {
	m := monitor(pool)
	if m == nil {
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s := Stats(pool)
			if m.WarnWaiting(now, s.Waiting) {
				slog.Warn("database pool acquisition waiting", "pool", s)
			}
		}
	}
}

var ErrPoolExhausted = errors.New("database pool exhausted")

// Probe distinguishes acquisition saturation from a failed ping, within the
// caller's overall deadline. It never accepts a cached database health result.
func Probe(ctx context.Context, pool *pgxpool.Pool) error {
	acquire, cancel := context.WithTimeout(ctx, ProbeAcquireTimeout)
	conn, err := pool.Acquire(acquire)
	cancel()
	if err != nil {
		s := Stats(pool)
		if errors.Is(err, context.DeadlineExceeded) && s.Acquired >= s.Max {
			return fmt.Errorf("%w: %d acquired, %d waiting", ErrPoolExhausted, s.Acquired, s.Waiting)
		}
		return err
	}
	defer conn.Release()
	return conn.Ping(ctx)
}

type retainedKey struct{}
type retainedConnection struct {
	pool *pgxpool.Pool
	conn *pgxpool.Conn
}

func retained(ctx context.Context, pool *pgxpool.Pool) *pgxpool.Conn {
	r, _ := ctx.Value(retainedKey{}).(retainedConnection)
	if r.pool == pool {
		return r.conn
	}
	return nil
}

// WithConnection lets sequential tenant transactions reuse a session-lock
// connection. It does not share transactions or skip tenant/authorization fences.
// The owner must close rows before the next operation, must not use this context
// concurrently, and must release the connection after all session locks unlock.
func WithConnection(ctx context.Context, pool *pgxpool.Pool, conn *pgxpool.Conn) context.Context {
	return context.WithValue(ctx, retainedKey{}, retainedConnection{pool, conn})
}

// Acquire borrows the retained session without taking or releasing another slot.
func Acquire(ctx context.Context, pool *pgxpool.Pool) (*pgxpool.Conn, func(), error) {
	if conn := retained(ctx, pool); conn != nil {
		return conn, func() {}, nil
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, nil, err
	}
	return conn, conn.Release, nil
}
func beginTx(ctx context.Context, pool *pgxpool.Pool, options pgx.TxOptions) (pgx.Tx, error) {
	if conn := retained(ctx, pool); conn != nil {
		return conn.BeginTx(ctx, options)
	}
	return pool.BeginTx(ctx, options)
}
