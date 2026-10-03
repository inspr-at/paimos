// SPDX-License-Identifier: AGPL-3.0-only
package inbox

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/inspr-at/paimos/internal/attachedmsg"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type attachedInput struct {
	Expires                          *time.Time
	Recipient, Body, Key, Generation string
	Session, SenderSession, Reply    *string
	Thread, Level                    string
	Action, Expects, Compat          bool
}

// tryAttached is shared by both durable send implementations, including every
// CLI/MCP/reply entrypoint. Attachment lookup, authority, quota, idempotency and
// acceptance share one transaction, serialized with pairing/revocation.
func (m *module) tryAttached(ctx context.Context, p tenant.Principal, project string, in attachedInput) (handled bool, msg Message, compat CompatMessage, err error) {
	// Explicit generation always selects volatile delivery, including when off.
	if !m.attached.Enabled() && in.Generation != "" {
		return true, msg, compat, attachedmsg.Fail(409, "feature_disabled")
	}
	var reserved string
	err = db.InTenant(tenant.WithPrincipal(ctx, p), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if e := attachedmsg.Lock(ctx, tx); e != nil {
			return e
		}
		// Compat send/reply lock precedes all message/session rows and the event
		// counter, matching commitMessage without introducing a reverse edge.
		if _, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,55))`, p.TenantID); e != nil {
			return e
		}
		recipient := in.Recipient
		var e error
		if in.Compat {
			recipient, e = resolveAddress(ctx, tx, project, in.Recipient)
			if e != nil {
				return e
			}
		}
		request, e := attachedmsg.AttachmentForSend(ctx, tx, recipient, in.Session)
		if e != nil {
			handled = true
			return e
		}
		if request == "" {
			if in.Generation != "" {
				handled = true
				return attachedmsg.Fail(409, "stale_message_generation")
			}
			return nil
		}
		handled = true
		if !m.attached.Enabled() {
			return attachedmsg.Fail(409, "feature_disabled")
		}
		// Client attribution is discarded, including a real agent sender session.
		// Replies/threads/actions are deliberately not an attached input mode in v1.
		if in.Reply != nil || in.Thread != "" || in.Action || in.Expects || (in.Level != "" && in.Level != "simple") {
			return attachedmsg.Fail(400, "unsupported_attached_mode")
		}
		if _, e = attachedmsg.Frame(p.ID, p.Name, in.Body); e != nil {
			return e
		}
		key := messageDigest(in.Key)
		fingerprintBytes, _ := json.Marshal(struct {
			Recipient, Project, Generation, Body string
			Session                              *string
		}{recipient, project, in.Generation, in.Body, in.Session})
		fingerprint := string(fingerprintBytes)
		var existingID, existingMode string
		var existingGeneration *string
		if in.Compat {
			e = tx.QueryRow(ctx, `SELECT id::text,content_mode,recipient_message_generation::text FROM inbox_compat_messages WHERE project_id=$1::uuid AND sender_principal_id=$2::uuid AND key_digest=$3`, project, p.ID, key).Scan(&existingID, &existingMode, &existingGeneration)
		} else {
			e = tx.QueryRow(ctx, `SELECT id::text,content_mode,recipient_message_generation::text FROM inbox_messages WHERE sender_principal_id=$1::uuid AND idempotency_key=$2`, p.ID, "attached/"+key).Scan(&existingID, &existingMode, &existingGeneration)
		}
		if e == nil {
			var owner, current, priorRecipient, priorProject string
			var priorSession *string
			if e = tx.QueryRow(ctx, `SELECT a.owner_id::text,q.approved_by::text,m.recipient_principal_id::text,m.recipient_session_id::text,a.project_id::text FROM inbox_messages m JOIN harness_attach_requests a ON a.tenant_id=m.tenant_id AND a.session_id=m.recipient_session_id JOIN agent_pairing_computers c ON c.tenant_id=a.tenant_id AND c.id=a.computer_id JOIN agent_pairing_requests q ON q.tenant_id=c.tenant_id AND q.id=c.request_id WHERE m.id=$1::uuid`, existingID).Scan(&owner, &current, &priorRecipient, &priorSession, &priorProject); e != nil {
				return e
			}
			if authz.RequireTx(ctx, tx, p, "inbox.send", authz.Scope{ProjectID: priorProject}) != nil {
				return errForbidden
			}
			if existingMode == attachedmsg.Volatile && (p.Kind != tenant.Person || !attachedmsg.InteractiveContext(ctx) || p.ID != owner || p.ID != current) {
				return errForbidden
			}
			if existingMode == attachedmsg.Notification && p.Kind != tenant.Agent {
				return errForbidden
			}
			if priorRecipient != recipient || !sameSession(priorSession, in.Session) || (existingGeneration != nil && *existingGeneration != in.Generation) || !m.attached.Match(p.TenantID, existingID, fingerprint) {
				return errConflict
			}
			msg, e = scanMessage(tx.QueryRow(ctx, `SELECT `+messageCols+` FROM inbox_messages WHERE id=$1::uuid`, existingID))
			if e != nil {
				return e
			}
			if in.Compat {
				compat, e = scanCompatMessage(tx.QueryRow(ctx, `SELECT `+compatMessageCols+` FROM inbox_compat_messages c `+compatObligationJoin+` WHERE c.id=$1::uuid`, existingID))
			}
			return e
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		accept, e := m.attached.Authorize(ctx, tx, p, request, attachedmsg.Send{Recipient: recipient, Project: project, Session: in.Session, Generation: in.Generation, Body: in.Body})
		if e != nil {
			return e
		}
		if in.Expires != nil && in.Expires.Before(accept.Deadline) {
			accept.Deadline = *in.Expires
		}
		if e = attachedmsg.Charge(ctx, tx, accept); e != nil {
			return e
		}
		id := attachedmsg.UUID()
		body := in.Body
		if accept.Mode == attachedmsg.Notification {
			body = ""
		} // notification text is never a hook candidate
		grant := ""
		if accept.GrantID != nil {
			grant = *accept.GrantID
		}
		if e = m.attached.Reserve(id, accept.Binding, grant, body, fingerprint, accept.Deadline); e != nil {
			return e
		}
		reserved = id
		// Only bounded metadata from here on: raw body/fingerprint never reaches SQL,
		// events, error detail, logs, notices, target queues, or persisted digests.
		ev, e := events.Append(ctx, tx, p, events.Change{Type: "inbox.attached_sent", After: messageMeta{ID: id, SenderPrincipalID: p.ID, RecipientPrincipalID: recipient, ExpiresAt: &accept.Deadline}})
		if e != nil {
			return e
		}
		var generation, epoch *string
		outcome := "notification_only"
		if accept.Mode == attachedmsg.Volatile {
			generation = &accept.Binding.Generation
			epoch = &accept.Binding.ServiceEpoch
			outcome = "queued"
		}
		msg, e = scanMessage(tx.QueryRow(ctx, `INSERT INTO inbox_messages(tenant_id,id,sender_principal_id,recipient_principal_id,sent_event_id,body,idempotency_key,recipient_session_id,sender_label,expires_at,content_mode,message_grant_id,recipient_message_generation,message_deadline,payload_bytes,payload_epoch,attached_outcome)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$10,$14,$15,$16) RETURNING `+messageCols, p.TenantID, id, p.ID, recipient, ev.ID, attachedmsg.Placeholder, "attached/"+key, in.Session, p.Name, accept.Deadline, accept.Mode, accept.GrantID, generation, accept.Bytes, epoch, outcome))
		if e != nil {
			return e
		}
		if in.Compat {
			// Normalize addresses/thread to server identifiers; sender-controlled
			// metadata cannot smuggle note content into this private projection.
			_, e = tx.Exec(ctx, `INSERT INTO inbox_compat_messages(tenant_id,id,project_id,sender_principal_id,recipient_principal_id,sender_address,recipient_address,body,key_digest,request_digest,thread_id,hop,inbox_message_id,sent_event_id,is_action_request,expects_reply,delivery_level,recipient_session_id,sender_label,content_mode,message_grant_id,recipient_message_generation,message_deadline,payload_bytes,payload_epoch,attached_outcome)
 VALUES($1,$2,$3,$4,$5,$4::uuid::text,$5::uuid::text,$6,$7,repeat('0',64),$2::uuid::text,1,$2,$8,false,false,'simple',$9,$10,$11,$12,$13,$14,$15,$16,$17)`, p.TenantID, id, project, p.ID, recipient, attachedmsg.Placeholder, key, ev.ID, in.Session, p.Name, accept.Mode, accept.GrantID, generation, accept.Deadline, accept.Bytes, epoch, outcome)
			if e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `INSERT INTO inbox_message_deliveries(tenant_id,message_id,state,reason) VALUES($1,$2,'pending','')`, p.TenantID, id); e != nil {
				return e
			}
			compat, e = scanCompatMessage(tx.QueryRow(ctx, `SELECT `+compatMessageCols+` FROM inbox_compat_messages c `+compatObligationJoin+` WHERE c.id=$1::uuid`, id))
			if e != nil {
				return e
			}
		}
		_, e = tx.Exec(ctx, `INSERT INTO inbox_receipts(tenant_id,message_id,state,deliver_by) VALUES($1,$2,'queued',$3)`, p.TenantID, id, accept.Deadline)
		return e
	})
	if reserved != "" {
		if err != nil {
			m.attached.Discard(p.TenantID, reserved)
		} else {
			m.attached.Publish(p.TenantID, reserved)
		}
	}
	return
}
