// SPDX-License-Identifier: AGPL-3.0-only
package questions

import (
	"context"
	"errors"
	"time"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/systemactor"
	"github.com/jackc/pgx/v5"
)

// alertPickupTx runs under the dispatcher's tenant/tree, current question and
// pending-effect locks. Pickup takes the session then message lock too, so the
// durable alert cannot race past a successful pickup. The pending row owns the
// one-time marker; neither restarts nor competing dispatchers duplicate it.
func (m *Module) alertPickupTx(ctx context.Context, tx pgx.Tx, tid string, d delivery, now time.Time) error {
	if _, err := tx.Exec(ctx, `SELECT 1 FROM harness_sessions
 WHERE id=(SELECT recipient_session_id FROM inbox_messages WHERE id=$1::uuid) FOR KEY SHARE`, d.ref); err != nil {
		return err
	}
	var since time.Time
	var recipient string
	var fetched, acked *time.Time
	err := tx.QueryRow(ctx, `SELECT created_at,recipient_principal_id::text,fetched_at,acked_at
 FROM inbox_messages WHERE tenant_id=$1 AND id=$2::uuid FOR NO KEY UPDATE`, tid, d.ref).Scan(&since, &recipient, &fetched, &acked)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil || fetched != nil || acked != nil || now.Before(since.Add(30*time.Minute)) {
		return err
	}
	var state string
	if err := tx.QueryRow(ctx, `SELECT state FROM inbox_receipts WHERE tenant_id=$1 AND message_id=$2::uuid FOR UPDATE`, tid, d.ref).Scan(&state); err != nil {
		return err
	}
	if state != "queued" {
		return nil
	}
	if err := writeCapability(ctx, tx); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE desk_pending
 SET effect_data=jsonb_set(effect_data,'{pickup_stall_alerted_at}',to_jsonb($3::timestamptz))
 WHERE tenant_id=$1 AND id=$2 AND NOT effect_data ? 'pickup_stall_alerted_at'`, tid, d.id, now)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM nodes WHERE tenant_id=$1 AND id=$2 FOR KEY SHARE`, tid, d.question); err != nil {
		return err
	}
	// Ensure may create the actor and its event on first use. All existing
	// resource and FK locks must therefore already be held.
	actor, err := systemactor.Ensure(ctx, tx, tid)
	if err != nil {
		return err
	}
	_, err = events.Append(ctx, tx, actor, events.Change{Type: "delivery.stall_alerted", NodeID: &d.question, At: &now,
		After: map[string]any{"source": "decision_desk", "item_id": d.id, "question_id": d.question, "revision": d.revision,
			"asker_id": d.asker, "message_id": d.ref, "recipient_principal_id": recipient,
			"state": "answer_not_picked_up", "state_since": since, "deadline_at": since.Add(30 * time.Minute), "owner": "coordinator"}})
	return err
}
