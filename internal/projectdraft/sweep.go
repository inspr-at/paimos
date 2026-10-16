// SPDX-License-Identifier: AGPL-3.0-only
package projectdraft

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
)

const sweepEvery = time.Minute

// Sweep deletes every expired draft, in every tenant: a draft never outlives
// its expiry by more than one sweep, whether or not anyone posts again, and a
// changed import tenant leaves nothing behind. It returns the rows deleted.
func (m *Module) Sweep(ctx context.Context) (int64, error) {
	var tenants []string
	ctx = db.NoProjects(ctx, "START handoff expiry")
	err := db.InTenant(ctx, m.pool, "00000000-0000-0000-0000-000000000000", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text FROM tenants ORDER BY id`)
		if err != nil {
			return err
		}
		tenants, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		return 0, err
	}
	var deleted int64
	for _, id := range tenants {
		err := db.InTenant(ctx, m.pool, id, func(tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, `DELETE FROM import_project_drafts WHERE expires_at <= clock_timestamp()`)
			deleted += tag.RowsAffected()
			return err
		})
		if err != nil {
			return deleted, err
		}
	}
	return deleted, nil
}

// RunExpirySweep sweeps once at startup and then every minute until ctx ends.
func (m *Module) RunExpirySweep(ctx context.Context) {
	ticker := time.NewTicker(sweepEvery)
	defer ticker.Stop()
	for {
		if _, err := m.Sweep(ctx); err != nil && ctx.Err() == nil {
			slog.Error("START handoff expiry sweep", "error_class", "database")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
