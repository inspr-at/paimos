// SPDX-License-Identifier: AGPL-3.0-only
package inbox

import (
	"context"
	"net/http"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// HeldReplyInput is handed to the Decision Desk only for an authenticated person.
// The bridge must verify the exact held recipient, project and live permission
// inside its final write transaction. Returning false keeps generic reply rules.
type HeldReplyInput struct {
	To, Body, Key, Thread           string
	Parent                          string
	RecipientSession, SenderSession *string
	Action, ExpectsReply            bool
	Level                           string
}
type HeldReplyBridge func(http.ResponseWriter, *http.Request, tenant.Principal, string, HeldReplyInput) bool

// WithHeldReplyBridge preserves the original constructor's closed default.
func WithHeldReplyBridge(bridge HeldReplyBridge) func(*messaging) {
	return func(m *messaging) { m.heldReply = bridge }
}

// DeskReply writes a new exact-generation reply using the inbox delivery and
// receipt protocol. Call only after the dispatcher has locked and authorized
// the question, session, principal and pending row. All existing-row writes
// must precede this call: new message identities then acquire the event counter
// last. No wake, process start, or held-parent release is performed here.
type DeskReply struct {
	ID, RootID, ReplyToID, ProjectID, RecipientID, RecipientSessionID string
	Body, RootBody, RecipientName, RootSenderSessionID                string
}

func RecordDeskReply(ctx context.Context, tx pgx.Tx, p tenant.Principal, in DeskReply) error {
	if in.ReplyToID == "" {
		in.ReplyToID = in.RootID
	}
	var rootExists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM inbox_compat_messages WHERE tenant_id=$1 AND id=$2)`, p.TenantID, in.RootID).Scan(&rootExists); err != nil {
		return err
	}
	// Take the existing reply obligation before the event counter. A reply
	// closes it only once its correlated message is durably recorded below.
	if err := reserveReplyObligation(ctx, tx, p.TenantID, in.ReplyToID); err != nil {
		return err
	}

	var err error
	var pending []events.Change
	collect := func(c events.Change) error { pending = append(pending, c); return nil }
	if !rootExists {
		_, err = tx.Exec(ctx, `INSERT INTO inbox_compat_messages(tenant_id,id,project_id,sender_principal_id,recipient_principal_id,sender_address,recipient_address,body,key_digest,request_digest,thread_id,hop,sent_event_id,is_action_request,expects_reply,delivery_level,sender_session_id)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$9,$2::uuid::text,1,$10,true,true,'simple',$11)`, p.TenantID, in.RootID, in.ProjectID, in.RecipientID, p.ID, "paimos:"+in.RecipientName, "paimos:"+p.Name, in.RootBody, "desk-root/"+in.RootID, nil, deskNullable(in.RootSenderSessionID))
		if err != nil {
			return err
		}
		// A synthetic question root stays held forever. The desk owns its answer state.
		_, err = tx.Exec(ctx, `INSERT INTO inbox_message_deliveries(tenant_id,message_id,state,reason) VALUES($1,$2,'held','action_request')`, p.TenantID, in.RootID)
		if err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO inbox_messages(tenant_id,id,sender_principal_id,recipient_principal_id,sent_event_id,body,idempotency_key,recipient_session_id,sender_label) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, p.TenantID, in.ID, p.ID, in.RecipientID, nil, in.Body, "desk/"+in.ID, deskNullable(in.RecipientSessionID), p.Name)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO inbox_compat_messages(tenant_id,id,project_id,sender_principal_id,recipient_principal_id,sender_address,recipient_address,body,key_digest,request_digest,reply_to_id,thread_id,hop,inbox_message_id,sent_event_id,is_action_request,expects_reply,delivery_level,recipient_session_id,sender_label)
 SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$9,$10,c.thread_id,c.hop+1,$2,$11,false,false,'simple',$12,$13 FROM inbox_compat_messages c WHERE c.tenant_id=$1 AND c.id=$10`, p.TenantID, in.ID, in.ProjectID, p.ID, in.RecipientID, "paimos:"+p.Name, "paimos:"+in.RecipientName, in.Body, "desk/"+in.ID, in.ReplyToID, nil, deskNullable(in.RecipientSessionID), p.Name)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE inbox_reply_obligations SET reply_message_id=$3,closed_at=clock_timestamp() WHERE tenant_id=$1 AND message_id=$2 AND closed_at IS NULL`, p.TenantID, in.ReplyToID, in.ID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		if err := collect(events.Change{NodeID: &in.ProjectID, Type: "inbox.reply_obligation_closed", After: map[string]string{"message_id": in.ReplyToID, "reply_message_id": in.ID}}); err != nil {
			return err
		}
	}

	// Exact sessions pull their own generation. Unbound answers are readable but
	// intentionally get no process wake or arbitrary address resolution.
	_, err = tx.Exec(ctx, `INSERT INTO inbox_message_deliveries(tenant_id,message_id,state,reason) VALUES($1,$2,'pending','')`, p.TenantID, in.ID)
	if err != nil {
		return err
	}
	if err := recordAcceptanceReceiptWith(ctx, tx, p, in.ID, collect); err != nil {
		return err
	}
	meta := map[string]string{"id": in.ID, "question_reply_root_id": in.RootID, "reply_to_id": in.ReplyToID, "sender_principal_id": p.ID, "recipient_principal_id": in.RecipientID}
	ev, err := events.Append(ctx, tx, p, events.Change{NodeID: &in.ProjectID, Type: "inbox.compat_sent", After: meta})
	if err != nil {
		return err
	}
	if !rootExists {
		if err := bindMessageEvent(ctx, tx, p.TenantID, in.RootID, ev.ID, false); err != nil {
			return err
		}
		// A synthetic root stays held but is no longer an unresolved request.
		if _, err := events.Append(ctx, tx, p, events.Change{NodeID: &in.ProjectID, Type: resolutionEvent, After: HeldResolution{MessageID: in.RootID, Decision: "resolved", CreatedAt: ev.At}}); err != nil {
			return err
		}
	}
	if err := bindMessageEvent(ctx, tx, p.TenantID, in.ID, ev.ID, true); err != nil {
		return err
	}
	for _, change := range pending {
		if _, err := events.Append(ctx, tx, p, change); err != nil {
			return err
		}
	}
	return nil
}
func deskNullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// reserveReplyObligation is shared by send and desk dispatch. NO KEY UPDATE
// permits the new reply's FK shares while fencing the one-time close.
func reserveReplyObligation(ctx context.Context, tx pgx.Tx, tenantID, parent string) error {
	_, err := tx.Exec(ctx, `SELECT 1 FROM inbox_reply_obligations WHERE tenant_id=$1 AND message_id=$2 FOR NO KEY UPDATE`, tenantID, parent)
	return err
}

// bindMessageEvent touches only new rows already owned by this transaction.
// Their deferred constraint prevents any incomplete reservation from committing.
func bindMessageEvent(ctx context.Context, tx pgx.Tx, tenantID, id string, eventID int64, delivered bool) error {
	if delivered {
		if _, err := tx.Exec(ctx, `UPDATE inbox_messages SET sent_event_id=$3 WHERE tenant_id=$1 AND id=$2`, tenantID, id, eventID); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `UPDATE inbox_compat_messages SET sent_event_id=$3 WHERE tenant_id=$1 AND id=$2`, tenantID, id, eventID)
	return err
}
