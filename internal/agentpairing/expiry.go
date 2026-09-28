// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// ExpireUnclaimedVerifications runs with the tenant Lock held, before account
// selection or queue projection. A queued run with no claim marker cannot have
// launched through the managed protocol. Starting/running/unknown claimed work
// is deliberately excluded: expiry is never evidence of local process exit.
// Queue polls, pairing views and subsequent routing all perform this sweep.
func ExpireUnclaimedVerifications(ctx context.Context, tx pgx.Tx) error {
	rows, err := tx.Query(ctx, `SELECT r.id::text,e.account_id::text,e.computer_id::text
 FROM agent_pairing_enrollments e JOIN agent_runs r ON r.tenant_id=e.tenant_id AND r.id=e.verification_run_id
 WHERE e.verification_claimed_at IS NULL AND e.verification_expires_at<=clock_timestamp()
 AND r.purpose='pairing_verification' AND r.status='queued'
 AND (r.account_id IS NULL OR r.account_id=e.account_id)
 ORDER BY r.id FOR UPDATE OF r`)
	if err != nil {
		return err
	}
	type expired struct{ run, account, computer string }
	var items []expired
	for rows.Next() {
		var x expired
		if err = rows.Scan(&x.run, &x.account, &x.computer); err != nil {
			rows.Close()
			return err
		}
		items = append(items, x)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, x := range items {
		if err = cancelQueuedRun(ctx, tx, x.run); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE agent_pairing_enrollments SET verification_expired_at=clock_timestamp() WHERE account_id=$1 AND verification_claimed_at IS NULL`, x.account); err != nil {
			return err
		}
		if err = finalizeDrain(ctx, tx, x.computer); err != nil {
			return err
		}
	}
	return nil
}

// The run transition and reservation release are one transaction. Repeated
// sweeps/disconnects release nothing twice; active work keeps all its holds.
func cancelQueuedRun(ctx context.Context, tx pgx.Tx, id string) error {
	tag, err := tx.Exec(ctx, `UPDATE agent_runs SET status='cancelled',ended_at=clock_timestamp() WHERE id=$1 AND status='queued'`, id)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	_, err = tx.Exec(ctx, `WITH released AS (
 UPDATE account_reservations res SET state='released',settled_at=clock_timestamp()
 FROM agent_runs r,account_allowance_windows w
 WHERE res.run_id=$1 AND res.state='active' AND r.id=res.run_id AND r.tenant_id=res.tenant_id
 AND w.id=res.window_id AND w.tenant_id=res.tenant_id AND w.account_id=r.account_id
 RETURNING res.window_id,res.reserved_units
 ), totals AS (SELECT window_id,sum(reserved_units)::bigint AS units FROM released GROUP BY window_id)
 UPDATE account_allowance_windows w SET reserved=w.reserved-t.units FROM totals t WHERE w.id=t.window_id`, id)
	return err
}
