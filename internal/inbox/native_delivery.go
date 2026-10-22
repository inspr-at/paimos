// SPDX-License-Identifier: AGPL-3.0-only
package inbox

import (
	"context"
	"errors"

	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// RecordNativeInputTx projects only a P3a receipt already validated against the
// current original consent, daemon lease, runtime and correlated provider proof
// in this same fenced transaction. Queueing and possible writes acknowledge
// nothing. Ordinary native person commands have no inbox lease and are ignored.
func RecordNativeInputTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, r nativecontract.Receipt) error {
	if r.Operation != "input" || r.Binding == nil {
		return nil
	}
	if r.State != "write_acknowledged" && r.State != "turn_started" && r.State != "turn_completed" && r.State != "uncertain" && r.State != "rejected" && r.State != "expired" {
		return nil
	}
	var message string
	err := tx.QueryRow(ctx, `SELECT m.id::text FROM inbox_messages m JOIN harness_deliveries h ON h.tenant_id=m.tenant_id AND h.message_id=m.id
 WHERE h.tenant_id=$1 AND h.id=$2 AND h.session_id=$3 AND h.mode='managed' AND h.released_at IS NULL
 AND m.recipient_session_id=$3 AND m.recipient_principal_id=$4 AND m.content_mode='durable' AND m.chat_thread_id IS NULL
 FOR NO KEY UPDATE OF m`, p.TenantID, r.OperationID, r.Binding.HarnessSessionID, p.ID).Scan(&message)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if r.InputID == nil || *r.InputID != message || r.RequestID != message {
		return ErrNotDelivered
	}
	// Lock every existing projection before the first inbox/event-counter write.
	if _, err = tx.Exec(ctx, `SELECT 1 FROM harness_deliveries WHERE tenant_id=$1 AND id=$2 FOR NO KEY UPDATE`, p.TenantID, r.OperationID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SELECT 1 FROM inbox_message_deliveries WHERE tenant_id=$1 AND message_id IN (SELECT id FROM inbox_compat_messages WHERE tenant_id=$1 AND inbox_message_id=$2) FOR NO KEY UPDATE`, p.TenantID, message); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SELECT 1 FROM inbox_receipts WHERE tenant_id=$1 AND message_id=$2 FOR NO KEY UPDATE`, p.TenantID, message); err != nil {
		return err
	}
	var hasReceipt bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM inbox_receipts WHERE tenant_id=$1 AND message_id=$2)`, p.TenantID, message).Scan(&hasReceipt); err != nil {
		return err
	}
	failed := r.State == "uncertain" || r.State == "rejected" || r.State == "expired"
	seen := r.State == "turn_started" || r.State == "turn_completed"
	if failed || seen {
		if _, err = tx.Exec(ctx, `UPDATE harness_deliveries SET completed_at=coalesce(completed_at,$3),shown_at=CASE WHEN $4 THEN coalesce(shown_at,$3) ELSE shown_at END WHERE tenant_id=$1 AND id=$2`, p.TenantID, r.OperationID, r.UpdatedAt, seen); err != nil {
			return err
		}
	}
	if failed {
		if hasReceipt {
			return FailSessionMessage(ctx, tx, p, message, "outcome_unconfirmed")
		}
		return nil
	}
	// This is the first actual native acknowledgement, not the earlier lease.
	if err = MarkFetched(ctx, tx, p, SeenDrain, message); err != nil {
		return err
	}
	if seen {
		if _, err = tx.Exec(ctx, `UPDATE inbox_messages SET acked_at=coalesce(acked_at,$3),acked_by_principal_id=$4 WHERE tenant_id=$1 AND id=$2`, p.TenantID, message, r.UpdatedAt, p.ID); err != nil {
			return err
		}
		if hasReceipt {
			return ConfirmSessionMessage(ctx, tx, p, message)
		}
	}
	return nil
}
