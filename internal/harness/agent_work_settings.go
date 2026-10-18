// SPDX-License-Identifier: AGPL-3.0-only
package harness

import (
	"context"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// The tenant fence serializes these workspace settings with permission changes
// and other settings writes. Acquire it before policy/record locks, then recheck
// the caller's current authority in this final transaction.
func lockAgentWorkSettings(ctx context.Context, tx pgx.Tx, p tenant.Principal) error {
	if _, err := tx.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR NO KEY UPDATE`, p.TenantID); err != nil {
		return err
	}
	return authz.RequireTx(ctx, tx, p, "settings.manage", authz.Scope{})
}

// Append only after the write, with no further locks taken by the caller. Undo
// uses the same setters, recording the reverse change instead of erasing history.
func recordAgentWorkSetting(ctx context.Context, tx pgx.Tx, p tenant.Principal, key string, before, after any) error {
	if before == after {
		return nil
	}
	_, err := events.Append(ctx, tx, p, events.Change{
		Type:   "tenant.agent_work_settings_changed",
		Before: map[string]any{key: before}, After: map[string]any{key: after},
	})
	return err
}
