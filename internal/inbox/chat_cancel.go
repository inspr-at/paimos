// SPDX-License-Identifier: AGPL-3.0-only

package inbox

import (
	"context"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/attachedmsg"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type chatCancelReceipt struct {
	MessageID    string    `json:"message_id"`
	BindingEpoch string    `json:"binding_epoch"`
	State        string    `json:"state"`
	PayloadMode  string    `json:"payload_mode"`
	Deadline     time.Time `json:"delivery_deadline"`
}
type chatCancelResult struct {
	Contract  string            `json:"contract"`
	MessageID string            `json:"message_id"`
	Result    string            `json:"result"`
	Receipt   chatCancelReceipt `json:"receipt"`
}

// This bridge owns only durable P0 session sends. Native R3 and attached
// owner notes keep their separate authority and evidence contracts.
func (m *messaging) cancelSessionMessage(w http.ResponseWriter, r *http.Request) {
	p, project, ok := m.messagingPrincipal(w, r, false)
	if !ok {
		return
	}
	id, valid := parseUUID(r.PathValue("messageId"))
	if !valid || p.Kind != tenant.Person {
		messagingFailure(w, errNotFound)
		return
	}
	ctx, cancel := context.WithTimeout(tenant.WithPrincipal(r.Context(), p), 3*time.Second)
	defer cancel()
	var out chatCancelResult
	err := db.InTenant(ctx, m.base.pool, p.TenantID, func(tx pgx.Tx) error {
		// Same tenant/access fence as sends. Claim and hook reads serialize on
		// the message row below, and recheck expiry under that lock.
		if err := attachedmsg.Lock(ctx, tx); err != nil {
			return err
		}
		for _, permission := range []string{"inbox.send", "harness.read"} {
			if err := authz.RequireTx(ctx, tx, p, permission, authz.Scope{ProjectID: project}); err != nil {
				return errNotFound
			}
		}
		if err := messagingProject(ctx, tx, project); err != nil {
			return err
		}
		var fetched, acked, cancelled *time.Time
		var state string
		var deadline time.Time
		err := tx.QueryRow(ctx, `SELECT i.fetched_at,i.acked_at,i.cancelled_at,r.state,r.deliver_by
 FROM inbox_compat_messages c JOIN inbox_messages i ON i.tenant_id=c.tenant_id AND i.id=c.inbox_message_id
 JOIN inbox_receipts r ON r.tenant_id=i.tenant_id AND r.message_id=i.id
 WHERE c.id=$1 AND c.project_id=$2 AND c.sender_principal_id=$3 AND c.recipient_session_id IS NOT NULL
 AND c.content_mode='durable' AND i.chat_thread_id IS NULL AND r.deliver_by IS NOT NULL
 FOR UPDATE OF i`, id, project, p.ID).Scan(&fetched, &acked, &cancelled, &state, &deadline)
		if err != nil {
			return err
		}
		out = chatCancelResult{"chat-v1", id, "too_late", chatCancelReceipt{id, "0", "pending", "inline", deadline}}
		if cancelled != nil {
			out.Result = "cancelled"
			out.Receipt.State = "cancelled"
			return nil
		}
		if fetched != nil || acked != nil || state == "handed_off" {
			out.Receipt.State = "handed_off"
			return nil
		}
		// Even a released/expired daemon lease might have exposed its payload.
		var offered bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM harness_deliveries WHERE message_id=$1)`, id).Scan(&offered); err != nil {
			return err
		}
		if offered {
			out.Receipt.State = "offered"
			return nil
		}
		// Expiry closes every existing recipient read path without extending the
		// legacy receipt enum or inventing a receiver confirmation.
		if _, err := tx.Exec(ctx, `UPDATE inbox_messages SET cancelled_at=clock_timestamp(),expires_at=LEAST(coalesce(expires_at,clock_timestamp()),clock_timestamp()) WHERE id=$1`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE inbox_receipts SET state='failed',failure_reason='cancelled' WHERE message_id=$1 AND state='queued'`, id); err != nil {
			return err
		}
		out.Result = "cancelled"
		out.Receipt.State = "cancelled"
		_, err = events.Append(ctx, tx, p, events.Change{Type: "inbox.message_cancelled", NodeID: &project, After: map[string]string{"message_id": id}})
		return err
	})
	if err != nil {
		messagingFailure(w, err)
		return
	}
	writeJSON(w, 200, out)
}
