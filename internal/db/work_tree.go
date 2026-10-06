// SPDX-License-Identifier: AGPL-3.0-only
package db

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// LockWorkTreeTx follows the paired-writer protocol used by the work status
// engine: tenant -> pairing -> tree -> resource rows -> event counter last.
// Call before resource locks, then RequireTx for the current target.
func LockWorkTreeTx(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT id FROM tenants WHERE id=current_setting('aeon.tenant_id')::uuid FOR NO KEY UPDATE`)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('aeon-pairing:'||current_setting('aeon.tenant_id'),0))`); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(current_setting('aeon.tenant_id'),0))`)
	return err
}
