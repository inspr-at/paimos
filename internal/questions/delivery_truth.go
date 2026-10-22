// SPDX-License-Identifier: AGPL-3.0-only
package questions

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/inbox"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// The dispatcher's tenant/tree and project hierarchy fences precede this read.
// Scope is the explicit project lead, same ticket or its immediate parent;
// neither a shared principal nor a session tree makes an unrelated agent related.
func relatedAnswerSessions(ctx context.Context, tx pgx.Tx, tid, project, question, ticket, recipient string, answer Answer) ([]inbox.DeskFYI, error) {
	rows, err := tx.Query(ctx, `SELECT h.id::text,h.agent_principal_id::text
 FROM harness_sessions h
 WHERE h.tenant_id=$1 AND h.project_id=$2 AND h.stopped_at IS NULL AND h.archived_at IS NULL
 AND coalesce(h.pause_record->>'state','') NOT IN ('paused','resume_requested','resumed')
 AND h.id::text<>$5 AND NOT EXISTS(SELECT 1 FROM desk_askers a WHERE a.tenant_id=h.tenant_id AND a.question_id=$3 AND a.session_id=h.id)
 AND (h.id=(SELECT session_id FROM project_leads WHERE tenant_id=$1 AND project_id=$2)
 OR ($4::uuid IS NOT NULL AND (h.ticket_node_id=$4::uuid OR h.ticket_node_id=(SELECT parent_id FROM nodes WHERE tenant_id=$1 AND id=$4::uuid AND project_id=$2 AND deleted_at IS NULL))))
 ORDER BY h.id LIMIT 129 FOR NO KEY UPDATE OF h`, tid, project, question, nullable(ticket), recipient)
	if err != nil {
		return nil, err
	}
	type related struct{ id, principal string }
	var candidates []related
	for rows.Next() {
		var c related
		if err := rows.Scan(&c.id, &c.principal); err != nil {
			rows.Close()
			return nil, err
		}
		candidates = append(candidates, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(candidates) > 128 {
		return nil, fail(409, "related_sessions_limit", "Too many related sessions; delivery will be retried.")
	}
	var out []inbox.DeskFYI
	for _, c := range candidates {
		p := tenant.Principal{TenantID: tid, ID: c.principal, Kind: tenant.Agent, Scopes: []string{"questions.read", "nodes.read"}}
		for _, permission := range []string{"questions.read", "nodes.read"} {
			err = authz.RequireTx(ctx, tx, p, permission, authz.Scope{ProjectID: project})
			if err != nil {
				break
			}
		}
		if errors.Is(err, authz.ErrForbidden) {
			continue
		}
		if err != nil {
			return nil, err
		}
		body, _ := json.Marshal(map[string]any{"type": "question.fyi", "question_id": question, "answer_id": answer.ID, "revision": answer.Revision, "ticket_id": ticket, "notice": "A Decision Desk answer was recorded. FYI only; no action is required."})
		key := "desk-fyi/" + answer.ID + "/" + c.id
		out = append(out, inbox.DeskFYI{ID: uuid.NewSHA1(uuid.NameSpaceOID, []byte(tid+"/"+key)).String(), SessionID: c.id, PrincipalID: c.principal, Key: key, Body: string(body)})
	}
	return out, nil
}

type DeliveryTruth struct {
	State         string     `json:"state"`
	SessionID     string     `json:"session_id,omitempty"`
	SessionLabel  string     `json:"session_label,omitempty"`
	DeliveredAt   *time.Time `json:"delivered_at,omitempty"`
	SeenAt        *time.Time `json:"seen_at,omitempty"`
	Successor     bool       `json:"successor"`
	FailureReason string     `json:"failure_reason,omitempty"`
}

func deliveryTruth(e Pending, original string, now time.Time) *DeliveryTruth {
	if e.Kind != "inbox" {
		return nil
	}
	d := &DeliveryTruth{State: "inbox", SessionID: e.DeliverySessionID, SessionLabel: e.deliveryLabel, DeliveredAt: e.deliveredAt, SeenAt: e.seenAt, Successor: e.DeliverySessionID != "" && original != "" && original != e.DeliverySessionID}
	switch {
	case e.ErrorCode == "successor_pending":
		d.State = "successor"
	case e.State == "failed" || e.ReceiptState == "failed":
		d.State, d.FailureReason = "failed", e.ErrorCode
		if e.ReceiptFailure != "" {
			d.FailureReason = e.ReceiptFailure
		}
	case e.State == "pending" && now.Before(e.DeliverAfter):
		d.State = "decided-grace"
	case d.SeenAt != nil:
		d.State = "seen"
	case d.DeliveredAt != nil:
		d.State = "sent"
	case d.Successor:
		d.State = "successor"
	}
	return d
}
