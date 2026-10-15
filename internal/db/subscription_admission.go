// SPDX-License-Identifier: AGPL-3.0-only

package db

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Subscription admission is session state: a single serialized session holds
// the two advisory slots per key across replicas. It runs no tenant queries or
// transactions. On connection loss every lease is canceled; clients reconnect
// and reacquire before replay. The session closes with its final lease.
type subscriptionAdmission struct {
	mu     sync.Mutex
	conn   *pgx.Conn
	leases map[*SubscriptionLease]struct{}
	opened time.Time
}
type SubscriptionLease struct {
	owner  *subscriptionAdmission
	key    string
	ctx    context.Context
	cancel context.CancelFunc
	once   sync.Once
}

var ErrSubscriptionLimit = errors.New("subscription connection limit reached")

func AdmitSubscription(ctx context.Context, pool *pgxpool.Pool, key string) (*SubscriptionLease, error) {
	m := monitor(pool)
	if m == nil {
		return nil, errors.New("subscription pool not configured")
	}
	a := &m.admission
	a.mu.Lock()
	defer a.mu.Unlock()
	if m.listenerShutdown.Load() {
		return nil, context.Canceled
	}
	op, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	if a.conn != nil && time.Since(a.opened) >= ListenerMaxLifetime {
		a.closeLocked()
	}
	if a.conn == nil {
		conn, err := pgx.ConnectConfig(op, dedicatedConfig(pool, "aeon-events-admission"))
		if err != nil {
			return nil, err
		}
		a.conn = conn
		a.opened = time.Now()
		a.leases = make(map[*SubscriptionLease]struct{})
	}
	if len(a.leases) >= 64 {
		return nil, ErrSubscriptionLimit
	}
	for slot := 0; slot < 2; slot++ {
		name := fmt.Sprintf("%s:%d", key, slot)
		// Advisory locks are reentrant on one session, so reject locally held keys.
		held := false
		for lease := range a.leases {
			if lease.key == name {
				held = true
				break
			}
		}
		if held {
			continue
		}
		var acquired bool
		if err := a.conn.QueryRow(op, `SELECT pg_try_advisory_lock(hashtextextended($1,885))`, name).Scan(&acquired); err != nil {
			a.closeLocked()
			return nil, err
		}
		if acquired {
			leaseCtx, cancel := context.WithCancel(ctx)
			lease := &SubscriptionLease{owner: a, key: name, ctx: leaseCtx, cancel: cancel}
			a.leases[lease] = struct{}{}
			return lease, nil
		}
	}
	if len(a.leases) == 0 {
		a.closeLocked()
	}
	return nil, ErrSubscriptionLimit
}
func (l *SubscriptionLease) Context() context.Context { return l.ctx }

// Check runs at the stream heartbeat, detecting lost admission even in silence.
func (l *SubscriptionLease) Check(ctx context.Context) error {
	a := l.owner
	a.mu.Lock()
	defer a.mu.Unlock()
	if l.ctx.Err() != nil {
		return l.ctx.Err()
	}
	if time.Since(a.opened) >= ListenerMaxLifetime {
		a.closeLocked()
		return context.Canceled
	}
	op, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	if err := a.conn.Ping(op); err != nil {
		a.closeLocked()
		return err
	}
	return nil
}
func (l *SubscriptionLease) Close() {
	l.once.Do(func() {
		l.cancel()
		a := l.owner
		a.mu.Lock()
		defer a.mu.Unlock()
		if _, ok := a.leases[l]; !ok {
			return
		}
		delete(a.leases, l)
		op, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if _, err := a.conn.Exec(op, `SELECT pg_advisory_unlock(hashtextextended($1,885))`, l.key); err != nil || len(a.leases) == 0 {
			a.closeLocked()
		}
	})
}
func (a *subscriptionAdmission) closeLocked() {
	for lease := range a.leases {
		lease.cancel()
	}
	a.leases = nil
	if a.conn != nil {
		op, stop := context.WithTimeout(context.Background(), 5*time.Second)
		_ = a.conn.Close(op)
		stop()
		a.conn = nil
	}
}
