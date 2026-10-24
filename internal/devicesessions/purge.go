// SPDX-License-Identifier: AGPL-3.0-only

package devicesessions

import (
	"context"
	"log/slog"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenantbootstrap"
	"github.com/jackc/pgx/v5"
)

const purgeInterval = time.Minute

// PurgeExpired removes abandoned raw app state as well as completed logins.
// Directory pages and delete batches are bounded; callers supply a deadline.
func (s *Service) PurgeExpired(ctx context.Context) error {
	ctx = db.NoProjects(ctx, "native login expiry cleanup")
	now := s.now()
	after := "00000000-0000-0000-0000-000000000000"
	for {
		ids := make([]string, 0, 100)
		if err := db.InTenant(ctx, s.pool, tenantbootstrap.LookupTenantID, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `SELECT id::text FROM tenants WHERE id>$1::uuid ORDER BY id LIMIT 100`, after)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					return err
				}
				ids = append(ids, id)
			}
			return rows.Err()
		}); err != nil {
			return err
		}
		for _, tid := range ids {
			for {
				var n int64
				err := db.InTenant(ctx, s.pool, tid, func(tx pgx.Tx) error {
					tag, err := tx.Exec(ctx, `DELETE FROM native_login_transactions WHERE tenant_id=$1::uuid AND id IN
						(SELECT id FROM native_login_transactions WHERE tenant_id=$1::uuid AND expires_at<=$2 ORDER BY expires_at,id LIMIT 1000)`, tid, now)
					n = tag.RowsAffected()
					return err
				})
				if err != nil {
					return err
				}
				if n < 1000 {
					break
				}
			}
		}
		if len(ids) < 100 {
			return nil
		}
		after = ids[len(ids)-1]
	}
}

// Run purges at startup and every minute. Only the failure class is logged.
func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(purgeInterval)
	defer ticker.Stop()
	s.runPurge(ctx, ticker.C)
}

func (s *Service) runPurge(ctx context.Context, ticks <-chan time.Time) {
	for {
		batch, cancel := context.WithTimeout(ctx, 50*time.Second)
		err := s.PurgeExpired(batch)
		cancel()
		if err != nil && ctx.Err() == nil {
			slog.WarnContext(ctx, "native login purge failed", "reason", httpapi.ErrorClass(err))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticks:
		}
	}
}
