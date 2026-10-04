// SPDX-License-Identifier: AGPL-3.0-only

package dbtest

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// WaitForBlocked observes a specific statement waiting on a specific backend.
// Time only bounds failure; elapsed time is never evidence of contention.
// Use the admin pool so pg_stat_activity exposes the app backend's statement.
func WaitForBlocked(ctx context.Context, admin *pgxpool.Pool, waiter, blocker int, statement string) error {
	if waiter <= 0 || blocker <= 0 || waiter == blocker || statement == "" {
		return errors.New("lock observation needs distinct backend IDs and a statement")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var blocked bool
		err := admin.QueryRow(ctx, `SELECT EXISTS (
 SELECT 1 FROM pg_stat_activity
 WHERE pid=$1 AND wait_event_type='Lock' AND $2::int=ANY(pg_blocking_pids(pid))
 AND strpos(query,$3)>0)`, waiter, blocker, statement).Scan(&blocked)
		if err != nil {
			return fmt.Errorf("observe lock wait: %w", err)
		}
		if blocked {
			return nil
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			return fmt.Errorf("statement never blocked on the competing transaction: %w", ctx.Err())
		}
	}
}
