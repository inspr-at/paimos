// SPDX-License-Identifier: AGPL-3.0-only
package inbox

import (
	"context"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Result is a code-owned native outcome, never user-authored message input.
// The caller owns tenant/tree/request fences and has checked the exact session
// and recipient. Call after all existing-row mutations and before audit.
type Result struct {
	ID, ProjectID, RecipientID, SessionID, Body string
}

func RecordResult(ctx context.Context, tx pgx.Tx, p tenant.Principal, in Result) error {
	_, err := tx.Exec(ctx, `INSERT INTO inbox_messages(tenant_id,id,sender_principal_id,recipient_principal_id,sent_event_id,body,idempotency_key,recipient_session_id,sender_label) VALUES($1,$2,$3,$4,NULL,$5,$6,$7,'Step-up approval')`, p.TenantID, in.ID, p.ID, in.RecipientID, in.Body, "stepup-result/"+in.ID, deskNullable(in.SessionID))
	if err != nil {
		return err
	}
	if in.ProjectID != "" {
		_, err = tx.Exec(ctx, `INSERT INTO inbox_compat_messages(tenant_id,id,project_id,sender_principal_id,recipient_principal_id,sender_address,recipient_address,body,key_digest,request_digest,thread_id,hop,inbox_message_id,sent_event_id,is_action_request,expects_reply,delivery_level,recipient_session_id,sender_label) VALUES($1,$2,$3,$4,$5,'paimos:Step-up approval','paimos:requesting-agent',$6,$7,$7,$2::uuid::text,1,$2,NULL,false,false,'simple',$8,'Step-up approval')`, p.TenantID, in.ID, in.ProjectID, p.ID, in.RecipientID, in.Body, "stepup-result/"+in.ID, deskNullable(in.SessionID))
		if err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO inbox_message_deliveries(tenant_id,message_id,state,reason) VALUES($1,$2,'pending','')`, p.TenantID, in.ID); err != nil {
		return err
	}
	var pending []events.Change
	if err = recordAcceptanceReceiptWith(ctx, tx, p, in.ID, func(c events.Change) error { pending = append(pending, c); return nil }); err != nil {
		return err
	}
	var project *string
	typ := "inbox.sent"
	if in.ProjectID != "" {
		project, typ = &in.ProjectID, "inbox.compat_sent"
	}
	ev, err := events.Append(ctx, tx, p, events.Change{NodeID: project, Type: typ, After: map[string]string{"id": in.ID, "sender_principal_id": p.ID, "recipient_principal_id": in.RecipientID, "stepup_request_id": in.ID, "outcome_line": in.Body}})
	if err != nil {
		return err
	}
	if in.ProjectID != "" {
		if err = bindMessageEvent(ctx, tx, p.TenantID, in.ID, ev.ID, true); err != nil {
			return err
		}
	} else if _, err = tx.Exec(ctx, `UPDATE inbox_messages SET sent_event_id=$3 WHERE tenant_id=$1 AND id=$2`, p.TenantID, in.ID, ev.ID); err != nil {
		return err
	}
	for _, change := range pending {
		if _, err = events.Append(ctx, tx, p, change); err != nil {
			return err
		}
	}
	return nil
}
