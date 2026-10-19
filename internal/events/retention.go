// SPDX-License-Identifier: AGPL-3.0-only
package events

import (
	"context"
	"log/slog"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	TelemetryRetention     = 14 * 24 * time.Hour
	telemetryBatchSize     = 500
	telemetryBatchDelay    = 100 * time.Millisecond
	telemetrySweepInterval = 24 * time.Hour
)

type telemetryPruned struct {
	probes, heartbeats int64
	protected          bool
}

func pruneTelemetryBatch(ctx context.Context, pool *pgxpool.Pool, tenantID string, cutoff time.Time, size int) (telemetryPruned, error) {
	ctx, cancel := context.WithTimeout(db.AllProjects(ctx, "telemetry retention"), 10*time.Second)
	defer cancel()
	var result telemetryPruned
	err := db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if err := db.SetLocalStatementTimeout(ctx, tx, 5*time.Second); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout='1s'`); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT event_type,deleted,undo_protected FROM aeon_prune_telemetry($1,$2,$3)`, tenantID, cutoff, size)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var typ string
			var count int64
			var protected bool
			if err := rows.Scan(&typ, &count, &protected); err != nil {
				return err
			}
			result.protected = result.protected || protected
			switch typ {
			case "account.probed":
				result.probes = count
			case "harness.heartbeat":
				result.heartbeats = count
			}
		}
		return rows.Err()
	})
	if err != nil {
		return telemetryPruned{}, err
	}
	return result, nil
}

// RunTelemetryRetention runs immediately after startup, then daily. The release
// flow must take its fresh backup before starting this binary. Deletes leave
// space for ordinary autovacuum/reuse; no table rewrite is requested.
func RunTelemetryRetention(ctx context.Context, pool *pgxpool.Pool) {
	for {
		sweepTelemetry(ctx, pool, time.Now().Add(-TelemetryRetention))
		if !waitTelemetry(ctx, telemetrySweepInterval) {
			return
		}
	}
}

func waitTelemetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func sweepTelemetry(ctx context.Context, pool *pgxpool.Pool, cutoff time.Time) {
	// Keyset tenant pages bound memory and release the query connection before
	// pruning or sleeping. New tenants join the next daily sweep.
	after := "00000000-0000-0000-0000-000000000000"
	for ctx.Err() == nil {
		var tenants []string
		pageCtx, cancel := context.WithTimeout(db.NoProjects(ctx, "telemetry retention tenant page"), 10*time.Second)
		err := db.InTenant(pageCtx, pool, after, func(tx pgx.Tx) error {
			rows, err := tx.Query(pageCtx, `SELECT id::text FROM tenants WHERE id>$1::uuid ORDER BY id LIMIT 100`, after)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					return err
				}
				tenants = append(tenants, id)
			}
			return rows.Err()
		})
		cancel()
		if err != nil {
			slog.Error("telemetry retention tenant scan failed", "error", err)
			return
		}
		if len(tenants) == 0 {
			return
		}
		for _, id := range tenants {
			for ctx.Err() == nil {
				batch, err := pruneTelemetryBatch(ctx, pool, id, cutoff, telemetryBatchSize)
				if err != nil {
					slog.Error("telemetry retention failed", "tenant_id", id, "error", err)
					break
				}
				slog.Info("telemetry retention batch", "tenant_id", id, "account.probed", batch.probes, "harness.heartbeat", batch.heartbeats, "undo_protected", batch.protected)
				if batch.protected {
					slog.Warn("telemetry retention skipped: undo references exist", "tenant_id", id)
					break
				}
				if batch.probes+batch.heartbeats == 0 {
					break
				}
				if !waitTelemetry(ctx, telemetryBatchDelay) {
					return
				}
				if batch.probes+batch.heartbeats < telemetryBatchSize {
					break
				}
			}
		}
		after = tenants[len(tenants)-1]
	}
}
