// SPDX-License-Identifier: AGPL-3.0-only

package inbox

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/db"
)

// sweeperLockKey is the session advisory lock that makes the sweeper a single
// runner across every paimos serve process sharing one database.
const sweeperLockKey int64 = 0x0280_de11_7e2d

// SweepInterval is how often paimos serve looks for missed deadlines. With the
// five-minute session default a sender learns within 15 seconds of it.
const SweepInterval = 15 * time.Second

const sweepBatch = 100

// Sweeper enforces delivery deadlines and the adapter attempt cap (AEON-280).
// paimos serve runs it; only the process holding the advisory lock sweeps.
type Sweeper struct {
	pool     *pgxpool.Pool
	interval time.Duration
	mu       sync.Mutex
	// Tenants whose pre-AEON-280 receipts are healed; a one-time bounded job.
	legacyHealed map[string]bool
}

func NewSweeper(pool *pgxpool.Pool) *Sweeper {
	return &Sweeper{pool: pool, interval: SweepInterval, legacyHealed: map[string]bool{}}
}

// Run sweeps now and then every interval until ctx ends.
func (s *Sweeper) Run(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if _, err := s.SweepLocked(ctx); err != nil && ctx.Err() == nil {
			slog.Error("inbox delivery sweep", "err", err)
		}
		timer.Reset(s.interval)
	}
}

// SweepLocked sweeps every tenant when this process wins the advisory lock.
// It returns how many messages it failed; false-lock rounds return zero.
func (s *Sweeper) SweepLocked(ctx context.Context) (int, error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Release()
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, sweeperLockKey).Scan(&locked); err != nil || !locked {
		return 0, err
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(unlockCtx, `SELECT pg_advisory_unlock($1)`, sweeperLockKey); err != nil {
			// Never hand a pooled connection back while it still holds the lock.
			_ = conn.Conn().Close(unlockCtx)
		}
	}()
	ids, err := s.tenantIDs(ctx)
	if err != nil {
		return 0, err
	}
	total := 0
	var first error
	for _, id := range ids {
		n, err := s.SweepTenant(ctx, id)
		total += n
		if err != nil && first == nil {
			first = err
		}
	}
	return total, first
}

// tenantIDs reads the tenant registry, which has no tenant_id and no RLS.
func (s *Sweeper) tenantIDs(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT id::text FROM tenants ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

type sweepCandidate struct {
	id, reason string
}

// SweepTenant heals receipts of acknowledged messages, then fails every
// message past its deadline or over the attempt cap. Each failure commits on
// its own so one bad row never holds back the rest.
func (s *Sweeper) SweepTenant(ctx context.Context, tenantID string) (int, error) {
	// A system job: deadlines cover messages in every project (ADR-003 P2).
	ctx = db.AllProjects(ctx, "inbox delivery sweeper")
	if err := s.heal(ctx, tenantID); err != nil {
		return 0, err
	}
	var candidates []sweepCandidate
	err := db.InTenant(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		candidates = nil
		settings, err := loadDeliverySettings(ctx, tx)
		if err != nil {
			return err
		}
		// Reason: an ended session wins; then a spent attempt budget; then
		// whether anything ever picked the message up.
		rows, err := tx.Query(ctx, `SELECT m.id::text,
 CASE WHEN rs.id IS NOT NULL AND (rs.stopped_at IS NOT NULL OR rs.archived_at IS NOT NULL) THEN 'session_ended'
      WHEN d.attempts >= $1 THEN 'attempts'
      WHEN m.fetched_at IS NULL AND coalesce(d.attempts,0)=0 THEN 'no_listener'
      ELSE 'deadline' END
 FROM inbox_receipts r
 JOIN inbox_messages m ON m.tenant_id=r.tenant_id AND m.id=r.message_id
 LEFT JOIN harness_sessions rs ON rs.tenant_id=m.tenant_id AND rs.id=m.recipient_session_id
 LEFT JOIN inbox_compat_messages c ON c.tenant_id=m.tenant_id AND c.inbox_message_id=m.id
 LEFT JOIN inbox_message_deliveries d ON d.tenant_id=c.tenant_id AND d.message_id=c.id
 WHERE m.content_mode='durable' AND r.state='queued' AND r.deliver_by IS NOT NULL AND m.acked_at IS NULL
   AND NOT coalesce(d.state='pending' AND d.lease_until>clock_timestamp(),false)
   AND NOT EXISTS(SELECT 1 FROM harness_deliveries h WHERE h.message_id=m.id AND h.completed_at IS NULL AND h.released_at IS NULL AND h.leased_at>clock_timestamp()-interval '2 minutes')
   AND (r.deliver_by<=clock_timestamp()
        OR (rs.id IS NOT NULL AND (rs.stopped_at IS NOT NULL OR rs.archived_at IS NOT NULL))
        OR (d.state='pending' AND d.attempts>=$1 AND (d.lease_until IS NULL OR d.lease_until<=clock_timestamp())))
 ORDER BY r.deliver_by LIMIT $2`, settings.MaxAttempts, sweepBatch)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c sweepCandidate
			if err := rows.Scan(&c.id, &c.reason); err != nil {
				return err
			}
			candidates = append(candidates, c)
		}
		return rows.Err()
	})
	if err != nil {
		return 0, err
	}
	failed := 0
	var first error
	for _, c := range candidates {
		if ctx.Err() != nil {
			return failed, ctx.Err()
		}
		var done bool
		err := db.InTenant(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
			var e error
			// A live adapter or drain lease wins: the lease expiry is the deadline.
			done, e = failMessage(ctx, tx, c.id, c.reason, true)
			return e
		})
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		if done {
			failed++
		}
	}
	return failed, first
}

// heal moves a queued receipt whose message was acknowledged anyway to
// handed_off. Every confirm path does this now; the periodic pass is a safety
// net over open deadlines only (the partial index of 0928). Receipts from
// before AEON-280 (no deadline) are healed once per process, in bounded
// batches, until none is left: never a rescan of history every tick.
func (s *Sweeper) heal(ctx context.Context, tenantID string) error {
	if _, err := s.healBatch(ctx, tenantID, `r.deliver_by IS NOT NULL`); err != nil {
		return err
	}
	s.mu.Lock()
	done := s.legacyHealed[tenantID]
	s.mu.Unlock()
	if done {
		return nil
	}
	n, err := s.healBatch(ctx, tenantID, `r.deliver_by IS NULL`)
	if err != nil {
		return err
	}
	if n < sweepBatch {
		s.mu.Lock()
		s.legacyHealed[tenantID] = true
		s.mu.Unlock()
	}
	return nil
}

func (s *Sweeper) healBatch(ctx context.Context, tenantID, scope string) (int, error) {
	n := 0
	err := db.InTenant(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		n = 0
		rows, err := tx.Query(ctx, `SELECT m.id::text FROM inbox_receipts r JOIN inbox_messages m ON m.tenant_id=r.tenant_id AND m.id=r.message_id
 WHERE r.state='queued' AND `+scope+` AND m.acked_at IS NOT NULL LIMIT $1`, sweepBatch)
		if err != nil {
			return err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil || len(ids) == 0 {
			return err
		}
		n = len(ids)
		sys, err := systemActor(ctx, tx, tenantID)
		if err != nil {
			return err
		}
		sort.Strings(ids)
		for _, id := range ids {
			// Message row first (AEON-280 lock order), then confirm.
			if _, err := tx.Exec(ctx, `SELECT 1 FROM inbox_messages WHERE id=$1::uuid FOR UPDATE`, id); err != nil {
				return err
			}
			if err := confirmReceived(ctx, tx, sys, id); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		return nil
	})
	return n, err
}
