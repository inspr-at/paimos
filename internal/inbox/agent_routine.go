// SPDX-License-Identifier: AGPL-3.0-only
package inbox

import (
	"context"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// QueueAgentRoutineTx accepts one authorized run through the ordinary inbox
// and compatibility delivery protocol. The caller owns tenant/tree fences,
// run idempotency, live person/owner authority and budget admission. Audit is
// returned so the caller can finish all receipt writes before the event counter.
// A single configured primary target uses its existing adapter; otherwise the
// principal's inbox remains available to agentd/listening workers.
func QueueAgentRoutineTx(ctx context.Context, tx pgx.Tx, sender tenant.Principal, owner, project, run, body string, policyKeys ...string) (string, []events.Change, error) {
	if _, err := normalizeSend(sender, owner, body, "routine-run/"+run, nil, nil); err != nil {
		return "", nil, workorders.Fail(400, err.Error())
	}
	for _, check := range []struct {
		p          tenant.Principal
		permission string
	}{{sender, "inbox.send"}, {tenant.Principal{ID: owner, TenantID: sender.TenantID, Kind: tenant.Agent, Scopes: []string{"inbox.read"}}, "inbox.read"}} {
		if err := authz.RequireTx(ctx, tx, check.p, check.permission, authz.Scope{ProjectID: project}); err != nil {
			return "", nil, err
		}
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM principals WHERE id=ANY($1::uuid[]) ORDER BY id FOR KEY SHARE`, []string{sender.ID, owner}); err != nil {
		return "", nil, err
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM nodes WHERE id=$1 FOR KEY SHARE`, project); err != nil {
		return "", nil, err
	}
	rows, err := tx.Query(ctx, `SELECT address FROM inbox_message_targets WHERE principal_id=$1 AND project_id=$2 AND role='primary' AND enabled ORDER BY address LIMIT 2 FOR KEY SHARE`, owner, project)
	if err != nil {
		return "", nil, err
	}
	var addresses []string
	for rows.Next() {
		var address string
		if err := rows.Scan(&address); err != nil {
			rows.Close()
			return "", nil, err
		}
		addresses = append(addresses, address)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return "", nil, err
	}
	if len(addresses) > 1 {
		return "", nil, workorders.Fail(409, "routine owner has ambiguous primary targets")
	}
	// Fence fallback and ordinary webhook FK parents before returning any audit.
	var targets int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT id FROM inbox_delivery_targets WHERE principal_id=$1 AND kind='webhook' AND enabled ORDER BY id LIMIT 33 FOR KEY SHARE) t`, owner).Scan(&targets); err != nil {
		return "", nil, err
	}
	if targets > 32 {
		return "", nil, workorders.Fail(409, "routine owner webhook target limit exceeded")
	}
	if len(addresses) == 1 {
		if _, err := tx.Exec(ctx, `SELECT id FROM inbox_message_targets WHERE project_id=$1 AND address=$2 AND enabled ORDER BY id FOR KEY SHARE`, project, addresses[0]); err != nil {
			return "", nil, err
		}
	}
	var id string
	policyKey := "routine-run/" + run
	if len(policyKeys) > 0 {
		policyKey = policyKeys[0]
	}
	if err := tx.QueryRow(ctx, `INSERT INTO inbox_messages(tenant_id,sender_principal_id,recipient_principal_id,body,idempotency_key,sender_label,assistant_policy_key,assistant_policy_project_id) VALUES($1,$2,$3,$4,$5,'Routines',$6,$7) RETURNING id::text`, sender.TenantID, sender.ID, owner, body, "routine-run/"+run, policyKey, project).Scan(&id); err != nil {
		return "", nil, err
	}
	pending := []events.Change{}
	collect := func(c events.Change) error { pending = append(pending, c); return nil }
	typ := "inbox.sent"
	if len(addresses) == 1 {
		typ = "inbox.compat_sent"
		_, err = tx.Exec(ctx, `INSERT INTO inbox_compat_messages(tenant_id,id,project_id,sender_principal_id,recipient_principal_id,sender_address,recipient_address,body,key_digest,request_digest,thread_id,hop,inbox_message_id,is_action_request,expects_reply,delivery_level,sender_label) VALUES($1,$2,$3,$4,$5,'paimos:Routines',$6,$7,$8,$8,$2::uuid::text,1,$2,false,false,'simple','Routines')`, sender.TenantID, id, project, sender.ID, owner, addresses[0], body, "routine-run/"+run)
		if err != nil {
			return "", nil, err
		}
		if err = queueCompatDeliveryWith(ctx, tx, sender, project, id, compatSend{To: addresses[0], Body: body, Level: "simple"}, collect); err != nil {
			return "", nil, err
		}
		if err = recordAcceptanceReceiptWith(ctx, tx, sender, id, collect); err != nil {
			return "", nil, err
		}
	} else {
		refusal, err := assistantDeliveryPolicyTx(ctx, tx, sender, owner, project, id, policyKey, collect)
		if err != nil {
			return "", nil, err
		}
		if refusal != "" {
			return "", nil, &httpError{409, "assistant_policy_denied", refusal}
		}
		if err = enqueueWakesWith(ctx, tx, sender.TenantID, Message{ID: id, RecipientPrincipalID: owner}, collect); err != nil {
			return "", nil, err
		}
		if err = insertReceiptWith(ctx, tx, sender, id, "queued", receiptTarget{}, "", "", collect); err != nil {
			return "", nil, err
		}
	}
	if err := assistantReceiptErrorTx(ctx, tx, id); err != nil {
		return "", nil, err
	}
	// The message's deferred event binding is completed by AppendAgentRoutineTx.
	pending = append([]events.Change{{NodeID: &project, Type: typ, After: map[string]string{"id": id, "sender_principal_id": sender.ID, "recipient_principal_id": owner}}}, pending...)
	return id, pending, nil
}

// QueueAgentLeadResultTx returns a pointer to the result to the original lead
// session. It cannot switch to a successor or a different principal's target.
// The caller has rechecked the live person grant and owns the access/tree fence.
func QueueAgentLeadResultTx(ctx context.Context, tx pgx.Tx, sender tenant.Principal, lead, session, project, run, body string) (string, []events.Change, error) {
	if _, err := messageSession(ctx, tx, &session, lead, project); err != nil {
		return "", nil, err
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM principals WHERE id=ANY($1::uuid[]) ORDER BY id FOR KEY SHARE`, []string{sender.ID, lead}); err != nil {
		return "", nil, err
	}
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO inbox_messages(tenant_id,sender_principal_id,recipient_principal_id,recipient_session_id,body,idempotency_key,sender_label) VALUES($1,$2,$3,$4,$5,$6,'Assistant results') RETURNING id::text`, sender.TenantID, sender.ID, lead, session, body, "assistant-result/"+run).Scan(&id); err != nil {
		return "", nil, err
	}
	changes := []events.Change{{NodeID: &project, Type: "inbox.sent", After: map[string]string{"id": id, "sender_principal_id": sender.ID, "recipient_principal_id": lead, "recipient_session_id": session}}}
	if err := insertReceiptWith(ctx, tx, sender, id, "queued", receiptTarget{}, "", "", func(c events.Change) error { changes = append(changes, c); return nil }); err != nil {
		return "", nil, err
	}
	return id, changes, nil
}

// AppendAgentRoutineTx is the final step after the caller persists run state.
func AppendAgentRoutineTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string, changes []events.Change) error {
	for i, c := range changes {
		ev, err := events.Append(ctx, tx, p, c)
		if err != nil {
			return err
		}
		if i == 0 {
			if _, err = tx.Exec(ctx, `UPDATE inbox_messages SET sent_event_id=$2 WHERE id=$1`, id, ev.ID); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `UPDATE inbox_compat_messages SET sent_event_id=$2 WHERE id=$1`, id, ev.ID); err != nil {
				return err
			}
		}
	}
	return nil
}
