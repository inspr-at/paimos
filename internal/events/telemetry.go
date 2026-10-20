// SPDX-License-Identifier: AGPL-3.0-only
package events

import (
	"context"
	"errors"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// NotifyHarnessHeartbeat wakes the existing live view without storing a tick.
// Only opaque identities enter NOTIFY; the SSE frame contains no identities.
func NotifyHarnessHeartbeat(ctx context.Context, tx pgx.Tx, tenantID, sessionID string) error {
	_, err := tx.Exec(ctx, `SELECT pg_notify('aeon_events',json_build_object('tenant_id',$1::text,'heartbeat_session_id',$2::text)::text)`, tenantID, sessionID)
	return err
}

func (m *module) heartbeatVisible(ctx context.Context, p tenant.Principal, sessionID string) (bool, error) {
	ctx, cancel := context.WithTimeout(tenant.WithPrincipal(ctx, p), 5*time.Second)
	defer cancel()
	visible := false
	err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := authz.RequireTx(ctx, tx, p, "harness.read", authz.Scope{AnyProject: true}); err != nil {
			if errors.Is(err, authz.ErrForbidden) {
				return nil
			}
			return err
		}
		// Session RLS and the current grant are checked on every wake. Resync
		// has no identity and checks whether any readable session exists.
		return tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM harness_sessions WHERE tenant_id=$1 AND ($2::uuid IS NULL OR id=$2))`, p.TenantID, nullable(sessionID)).Scan(&visible)
	})
	return visible, err
}
