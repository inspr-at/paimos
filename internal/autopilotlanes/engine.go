// SPDX-License-Identifier: AGPL-3.0-only
package autopilotlanes

import (
	"context"
	"log/slog"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Run wakes on committed events and polls after a minute for crash recovery and
// window boundaries. One pass reads at most one tenant and 50 lane policies;
// keyset cursors advance across passes. Schedule reopens each write using the
// owner's current project visibility and permissions, never service visibility.
func (m *Module) Run(ctx context.Context) {
	wake := make(chan struct{}, 1)
	go m.listen(ctx, wake)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	tenantAfter, laneAfter := "", ""
	for {
		if err := m.sweep(ctx, &tenantAfter, &laneAfter); err != nil && ctx.Err() == nil {
			slog.Error("lane scheduler", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-wake:
		}
	}
}
func (m *Module) listen(ctx context.Context, wake chan<- struct{}) {
	// LISTEN is session state. Use a separate connection, leaving pool slots free
	// for transactions. Recovery polling still works if the listener disconnects.
	connectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	conn, err := pgx.ConnectConfig(connectCtx, m.pool.Config().ConnConfig.Copy())
	cancel()
	if err != nil {
		return
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = conn.Close(closeCtx)
	}()
	if _, err = conn.Exec(ctx, "LISTEN aeon_events"); err != nil {
		return
	}
	for {
		if _, err = conn.WaitForNotification(ctx); err != nil {
			return
		}
		select {
		case wake <- struct{}{}:
		default:
		}
	}
}
func (m *Module) sweep(ctx context.Context, tenantAfter, laneAfter *string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var tid string
	err := m.pool.QueryRow(ctx, `SELECT id::text FROM tenants WHERE ($1='' OR id::text>$1) ORDER BY id::text LIMIT 1`, *tenantAfter).Scan(&tid)
	if err == pgx.ErrNoRows {
		*tenantAfter = ""
		*laneAfter = ""
		return nil
	}
	if err != nil {
		return err
	}
	type policy struct {
		id, owner string
		revision  int64
	}
	policies := []policy{}
	err = db.InTenant(db.AllProjects(ctx, "lane policy discovery only"), m.pool, tid, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT l.node_id::text,l.owner_principal_id::text,l.revision FROM autopilot_lanes l JOIN nodes n ON n.tenant_id=l.tenant_id AND n.id=l.node_id WHERE l.enabled AND NOT l.paused AND n.deleted_at IS NULL AND ($1='' OR l.node_id::text>$1) ORDER BY l.node_id::text LIMIT 50`, *laneAfter)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p policy
			if err = rows.Scan(&p.id, &p.owner, &p.revision); err != nil {
				return err
			}
			policies = append(policies, p)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	for _, policy := range policies {
		p := tenant.Principal{ID: policy.owner, TenantID: tid, Kind: tenant.Person}
		_, err := m.Schedule(tenant.WithPrincipal(ctx, p), p, policy.id, policy.revision)
		if err != nil && ctx.Err() == nil {
			slog.Warn("lane scheduling deferred", "lane_id", policy.id, "err", err)
		}
		*laneAfter = policy.id
	}
	if len(policies) < 50 {
		*tenantAfter = tid
		*laneAfter = ""
	}
	return nil
}
