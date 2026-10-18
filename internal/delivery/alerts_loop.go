// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"log/slog"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/jackc/pgx/v5"
)

const alertSettingsJoin = `LEFT JOIN delivery_settings project_settings ON project_settings.project_id=i.project_id
 LEFT JOIN delivery_settings tenant_settings ON tenant_settings.project_id IS NULL`

// Read current effective settings for scheduling as well as the final write.
// Changing a duration takes effect without waiting for GitHub reconciliation.
const alertDeadlineSQL = `(CASE WHEN i.state IN ('reviewed','pushed','ci_green','in_queue','queue_failed') THEN
 i.state_since + NULLIF(coalesce((project_settings.deadlines->>i.state)::int,
 (tenant_settings.deadlines->>i.state)::int,
 CASE i.state WHEN 'reviewed' THEN 30 WHEN 'pushed' THEN 60 WHEN 'ci_green' THEN 20 WHEN 'in_queue' THEN 60 WHEN 'queue_failed' THEN 120 END),0)*interval '1 minute' END)`

// RunAlerts observes every tenant, independent of GitHub App configuration.
// Durable deadlines drive the timer; a minute safety wake discovers new items,
// settings and tenants. No volatile timer is the record of an alert episode.
func (m *Module) RunAlerts(ctx context.Context) {
	for ctx.Err() == nil {
		delay := m.alertRound(ctx)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (m *Module) alertRound(ctx context.Context) time.Duration {
	delay := time.Minute
	var cursor string
	for ctx.Err() == nil {
		ids := []string{}
		scanCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := db.InTenant(db.NoProjects(scanCtx, "delivery alert tenant scan"), m.pool, "00000000-0000-0000-0000-000000000000", func(tx pgx.Tx) error {
			rows, err := tx.Query(scanCtx, `SELECT id::text FROM tenants WHERE ($1::uuid IS NULL OR id>$1) ORDER BY id LIMIT 100`, nullable(cursor))
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var id string
				if err = rows.Scan(&id); err != nil {
					return err
				}
				ids = append(ids, id)
			}
			return rows.Err()
		})
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("delivery alert tenant scan incomplete")
			}
			return delay
		}
		for _, tid := range ids {
			if _, err := m.SweepAlerts(ctx, tid); err != nil {
				if ctx.Err() == nil {
					slog.Warn("delivery alert sweep incomplete", "tenant_id", tid)
				}
				// An unavailable recipient/inbox must not cause a busy retry loop.
				continue
			}
			wait, err := m.nextAlertDelay(ctx, tid)
			if err != nil {
				if ctx.Err() == nil {
					slog.Warn("delivery alert scheduling incomplete", "tenant_id", tid)
				}
				continue
			}
			delay = min(delay, wait)
		}
		if len(ids) < 100 {
			return delay
		}
		cursor = ids[len(ids)-1]
	}
	return delay
}

func (m *Module) nextAlertDelay(ctx context.Context, tid string) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var seconds *float64
	err := db.InTenant(db.AllProjects(ctx, "delivery alert deadline scheduling"), m.pool, tid, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT extract(epoch FROM min(`+alertDeadlineSQL+`)-$1::timestamptz)::double precision
 FROM delivery_items i `+alertSettingsJoin+` WHERE NOT EXISTS (
 SELECT 1 FROM delivery_alerts a WHERE a.item_id=i.id AND a.state=i.state AND a.state_since=i.state_since)`, m.now().UTC()).Scan(&seconds)
	})
	delay := time.Minute
	if err == nil && seconds != nil {
		delay = min(delay, max(10*time.Millisecond, time.Duration(*seconds*float64(time.Second))))
	}
	return delay, err
}
