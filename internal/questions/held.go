// SPDX-License-Identifier: AGPL-3.0-only
package questions

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/inbox"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// ReplyHeld is the person-only adapter for tell --reply-to. Hidden parents keep
// the generic not-found path. Held bodies are never inserted in inbox_messages.
func (m *Module) ReplyHeld(w http.ResponseWriter, r *http.Request, p tenant.Principal, project string, in inbox.HeldReplyInput) bool {
	if p.Kind != tenant.Person || len(r.Header.Values("Authorization")) != 0 || r.Header.Get("X-Paimos-Agent-Name") != "" || r.Header.Get("X-Aeon-Agent-Name") != "" {
		return false
	}
	handled := false
	var q Question
	var sender, session, thread string
	var hop int
	var replyID string
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := treeLock(r.Context(), tx, p.TenantID); err != nil {
			return err
		}
		// Same fence as existing resolve/dismiss. They retain their own semantics.
		if _, err := tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,55))`, p.TenantID); err != nil {
			return err
		}
		var recipient string
		err := tx.QueryRow(r.Context(), `SELECT sender_principal_id::text,recipient_principal_id::text,coalesce(sender_session_id::text,''),thread_id,hop FROM inbox_compat_messages WHERE tenant_id=$1 AND project_id=$2 AND id=$3 AND is_action_request FOR NO KEY UPDATE`, p.TenantID, project, in.Parent).Scan(&sender, &recipient, &session, &thread, &hop)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		handled = true
		if recipient != p.ID {
			return missing()
		}
		for _, perm := range []string{"inbox.manage", "questions.decide", "questions.read"} {
			if err := permit(r.Context(), tx, p, project, perm); err != nil {
				return err
			}
		}
		var target string
		if uuidRE.MatchString(in.To) {
			target = in.To
		} else {
			// Accept only the sender's existing address/name, never another recipient.
			err = tx.QueryRow(r.Context(), `SELECT id::text FROM principals WHERE tenant_id=$1 AND id=$2 AND split_part($3,':',2)=name`, p.TenantID, sender, in.To).Scan(&target)
			if err != nil {
				return missing()
			}
		}
		if target != sender || in.RecipientSession != nil && *in.RecipientSession != session || in.SenderSession != nil || in.Thread != "" && in.Thread != thread {
			return missing()
		}
		if hop >= 10 {
			return fail(400, "reply_hop_limit", "message hop limit exceeded")
		}
		if in.Action || in.ExpectsReply || in.Level != "simple" || !bounded(in.Body, 8000, true) {
			return fail(400, "invalid_request", "held replies require a bounded simple answer")
		}
		var id string
		rows, err := tx.Query(r.Context(), `SELECT DISTINCT question_id::text FROM desk_askers WHERE tenant_id=$1 AND project_id=$2 AND (reply_root_id=$3 OR source_request_id=$3) LIMIT 2`, p.TenantID, project, in.Parent)
		if err != nil {
			return err
		}
		ids := []string{}
		for rows.Next() {
			var candidate string
			if err = rows.Scan(&candidate); err != nil {
				break
			}
			ids = append(ids, candidate)
		}
		rowsErr := rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if rowsErr != nil {
			return rowsErr
		}
		if len(ids) > 1 {
			return fail(409, "ambiguous_reply", "reply source belongs to multiple questions; decide the exact question")
		}
		if len(ids) == 1 {
			id = ids[0]
			err = nil
		} else {
			err = pgx.ErrNoRows
		}

		if errors.Is(err, pgx.ErrNoRows) {
			var resolved bool
			if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM events WHERE tenant_id=$1 AND node_id=$2 AND type='inbox.action_resolved' AND after->>'message_id'=$3)`, p.TenantID, project, in.Parent).Scan(&resolved); err != nil {
				return err
			}
			if resolved {
				return fail(409, "already_resolved", "held request has already been resolved or dismissed")
			}
			if err = writeCapability(r.Context(), tx); err != nil {
				return err
			}
			id, err = createNode(r.Context(), tx, p, project, "question")
			if err != nil {
				return err
			}
			input := Input{RequestID: in.Parent, Question: "Reply to held action request", Context: "Source message: " + in.Parent, Options: []Option{{ID: "reply", Title: "Reply", Answer: "Reply"}}, Meanwhile: "parked", SessionID: session, SourceRequestID: in.Parent, SuggestedOutcome: "once"}
			hash, raw, err := digest(input)
			if err != nil {
				return err
			}
			_, err = tx.Exec(r.Context(), `INSERT INTO desk_questions(tenant_id,project_id,node_id,input,suggested_outcome,suggestion_reason) VALUES($1,$2,$3,$4,'once','agent_suggestion')`, p.TenantID, project, id, raw)
			if err != nil {
				return err
			}
			_, err = tx.Exec(r.Context(), `INSERT INTO desk_askers(tenant_id,project_id,question_id,principal_id,request_id,request_digest,session_id,source_request_id,reply_root_id,comment_node_id,input) VALUES($1,$2,$3,$4,$5,$6,$7,$5,$5,$3,$8)`, p.TenantID, project, id, sender, in.Parent, hash, nullable(session), raw)
			if err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		var revision int64
		if err = tx.QueryRow(r.Context(), `SELECT revision FROM desk_questions WHERE tenant_id=$1 AND node_id=$2 FOR NO KEY UPDATE`, p.TenantID, id).Scan(&revision); err != nil {
			return err
		}
		// Bind retries to the person, parent and caller idempotency key.
		requestID := heldRequestID(p.ID, in.Parent, in.Key)
		decision := DecisionInput{RequestID: requestID, ExpectedRevision: revision, Answer: in.Body, Outcome: "once"}
		// Retries retain the original expected revision so digest comparison is stable.
		var priorRev int64
		err = tx.QueryRow(r.Context(), `SELECT revision FROM desk_answers WHERE tenant_id=$1 AND question_id=$2 AND decided_by=$3 AND request_id=$4`, p.TenantID, id, p.ID, requestID).Scan(&priorRev)
		if err == nil {
			decision.ExpectedRevision = priorRev - 1
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		q, err = m.decideTx(r.Context(), tx, p, id, decision)
		if err != nil {
			return err
		}
		original := &Answer{}
		err = tx.QueryRow(r.Context(), `SELECT r.revision,r.created_at,r.deliver_after,e.id::text FROM desk_answers r JOIN desk_pending e ON e.tenant_id=r.tenant_id AND e.question_id=r.question_id AND e.revision=r.revision AND e.kind='inbox' JOIN desk_askers a ON a.tenant_id=e.tenant_id AND a.id=e.asker_id WHERE r.tenant_id=$1 AND r.question_id=$2 AND r.decided_by=$3 AND r.request_id=$4 AND (a.reply_root_id=$5 OR a.source_request_id=$5)`, p.TenantID, id, p.ID, requestID, in.Parent).Scan(&original.Revision, &original.CreatedAt, &original.DeliverAfter, &replyID)
		if err == nil {
			q.Answer = original
			q.Revision = original.Revision
		}
		return err

	})
	if !handled && err == nil {
		return false
	}
	if err != nil {
		result(w, 0, nil, err)
		return true
	}
	after := q.Answer.DeliverAfter
	out := inbox.CompatMessage{ID: replyID, SenderPrincipalID: p.ID, RecipientPrincipalID: sender, From: "paimos:" + p.Name, To: in.To, Body: in.Body, ReplyTo: &in.Parent, ThreadID: thread, Hop: hop + 1, Status: "pending", Level: "simple", QuestionID: q.ID, AnswerRevision: q.Revision, DeliverAfter: &after, CreatedAt: q.Answer.CreatedAt, ReplyObligation: "none"}
	if session != "" {
		out.RecipientSessionID = &session
	}
	result(w, 201, out, nil)
	return true
}
func heldRequestID(person, parent, key string) string {
	h := sha256.Sum256([]byte(person + "/" + parent + "/" + key))
	h[6] = h[6]&15 | 64
	h[8] = h[8]&63 | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", h[:4], h[4:6], h[6:8], h[8:10], h[10:16])
}

// settleHeld runs only after the answer and every outbox row exist in the same
// transaction. It records resolution, never releases or executes the parent.
func settleHeld(ctx context.Context, tx pgx.Tx, p tenant.Principal, q Question, revision int64) error {
	for _, a := range q.Askers {
		if a.Input.SourceRequestID == "" {
			continue
		}
		var held bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM inbox_compat_messages WHERE tenant_id=$1 AND project_id=$2 AND id=$3 AND is_action_request AND recipient_principal_id=$4)`, p.TenantID, q.ProjectID, a.Input.SourceRequestID, p.ID).Scan(&held); err != nil {
			return err
		}
		if !held {
			continue
		}
		if err := permit(ctx, tx, p, q.ProjectID, "inbox.manage"); err != nil {
			return err
		}
		var resolved bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE tenant_id=$1 AND node_id=$2 AND type='inbox.action_resolved' AND after->>'message_id'=$3)`, p.TenantID, q.ProjectID, a.Input.SourceRequestID).Scan(&resolved); err != nil {
			return err
		}
		if resolved {
			continue
		}
		var created time.Time
		if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&created); err != nil {
			return err
		}
		raw, _ := json.Marshal(map[string]any{"created_at": created, "message_id": a.Input.SourceRequestID, "decision": "resolved", "question_id": q.ID, "answer_revision": revision, "reply_pending": true})
		if _, err := events.Append(ctx, tx, p, events.Change{NodeID: &q.ProjectID, Type: "inbox.action_resolved", After: json.RawMessage(raw)}); err != nil {
			return err
		}
	}
	return nil
}
