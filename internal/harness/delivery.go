// SPDX-License-Identifier: AGPL-3.0-only

package harness

import (
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/inbox"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
)

type Delivery struct {
	ID                string    `json:"delivery_id"`
	MessageID         string    `json:"message_id"`
	Cursor            int64     `json:"cursor"`
	SenderPrincipalID string    `json:"sender_principal_id"`
	Body              string    `json:"body"`
	LeasedAt          time.Time `json:"leased_at"`
}

func (m *Module) drain(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in struct{}
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	ctx := r.Context()
	s, err := worker(ctx, tx, r, p)
	if err != nil {
		return nil, err
	}
	if !has(s, "inbox") || s.Management != "managed" && !has(s, "attached_reconnect_v1") {
		return nil, workorders.Fail(409, "managed inbox or paired attached hook required")
	}
	// Every drain is this generation listening, with or without work (AEON-280).
	if err := inbox.MarkSessionSeen(ctx, tx, s.ID, inbox.SeenDrain); err != nil {
		return nil, err
	}
	// Existing uncompleted lease must be replayed before taking later work.
	var d Delivery
	err = tx.QueryRow(ctx, `SELECT d.id::text,m.id::text,d.cursor,m.sender_principal_id::text,m.body,d.leased_at FROM harness_deliveries d JOIN inbox_messages m ON m.tenant_id=d.tenant_id AND m.id=d.message_id WHERE m.content_mode='durable' AND m.chat_thread_id IS NULL AND d.session_id=$1 AND d.mode='managed' AND d.completed_at IS NULL AND d.released_at IS NULL AND m.acked_at IS NULL AND (m.expires_at IS NULL OR m.expires_at>clock_timestamp()) ORDER BY d.cursor LIMIT 1 FOR UPDATE OF m`, s.ID).Scan(&d.ID, &d.MessageID, &d.Cursor, &d.SenderPrincipalID, &d.Body, &d.LeasedAt)
	if err == nil {
		return []Delivery{d}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	// The session row lock serializes this session's drains. SKIP LOCKED lets
	// another generation for the same principal continue independently.
	var messageID, sender, body string
	var cursor int64
	err = tx.QueryRow(ctx, `SELECT m.id::text,m.sender_principal_id::text,m.body,m.sent_event_id FROM inbox_messages m WHERE m.content_mode='durable' AND m.chat_thread_id IS NULL AND m.recipient_principal_id=$1 AND ((m.recipient_session_id IS NULL AND $3) OR m.recipient_session_id=$2::uuid) AND m.acked_at IS NULL AND (m.expires_at IS NULL OR m.expires_at>clock_timestamp()) AND NOT EXISTS(SELECT 1 FROM harness_deliveries d WHERE d.message_id=m.id AND d.completed_at IS NULL AND d.released_at IS NULL) ORDER BY m.sent_event_id LIMIT 1 FOR UPDATE OF m SKIP LOCKED`, s.AgentPrincipalID, s.ID, s.Management == "managed").Scan(&messageID, &sender, &body, &cursor)
	if errors.Is(err, pgx.ErrNoRows) {
		return []Delivery{}, nil
	}
	if err != nil {
		return nil, err
	}
	err = tx.QueryRow(ctx, `INSERT INTO harness_deliveries(tenant_id,session_id,message_id,cursor) VALUES($1,$2,$3,$4) ON CONFLICT(tenant_id,session_id,message_id) DO UPDATE SET leased_at=clock_timestamp() WHERE harness_deliveries.completed_at IS NOT NULL RETURNING id::text,leased_at`, p.TenantID, s.ID, messageID, cursor).Scan(&d.ID, &d.LeasedAt)
	if err != nil {
		return nil, err
	}
	d.MessageID = messageID
	d.Cursor = cursor
	d.SenderPrincipalID = sender
	d.Body = body
	if err := inbox.MarkFetched(ctx, tx, p, inbox.SeenDrain, messageID); err != nil {
		return nil, err
	}
	return []Delivery{d}, record(ctx, tx, p, s, "delivery_leased", nil, map[string]any{"delivery_id": d.ID, "message_id": d.MessageID, "cursor": d.Cursor})
}
func (m *Module) completeDelivery(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in struct {
		Outcome        string `json:"outcome"`
		FailureReason  string `json:"failure_reason"`
		DeliveryID     string `json:"delivery_id"`
		Cursor         int64  `json:"cursor"`
		EffectiveLevel string `json:"effective_level"`
		FallbackReason string `json:"fallback_reason"`
	}
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if !workorders.UUID(in.DeliveryID) || in.Cursor < 1 {
		return nil, workorders.Fail(400, "delivery id and cursor required")
	}
	if in.EffectiveLevel == "" {
		in.EffectiveLevel = "simple"
	}
	if in.EffectiveLevel != "simple" && in.EffectiveLevel != "steer" {
		return nil, workorders.Fail(400, "invalid effective level")
	}
	if in.EffectiveLevel == "steer" {
		return nil, workorders.Fail(409, "Aeon inbox has no steer delivery")
	}
	if in.FallbackReason != "" {
		return nil, workorders.Fail(400, "fallback reason is not applicable")
	}
	if in.Outcome == "" {
		in.Outcome = "handed_off"
	}
	if (in.Outcome != "handed_off" && in.Outcome != "failed") ||
		(in.Outcome == "handed_off" && in.FailureReason != "") ||
		(in.Outcome == "failed" && in.FailureReason != "outcome_unconfirmed" && in.FailureReason != "child_unavailable") {
		return nil, workorders.Fail(400, "invalid delivery outcome")
	}
	ctx := r.Context()
	s, err := worker(ctx, tx, r, p)
	if err != nil {
		return nil, err
	}
	if s.Management != "managed" || !has(s, "inbox") {
		return nil, workorders.Fail(409, "managed inbox capability required")
	}
	var messageID string
	var cursor int64
	var completed, released *time.Time
	// Lock order (AEON-280): worker() locked the session; then the message row,
	// then the drain lease; FailMessage and receipts follow.
	if _, err = tx.Exec(ctx, `SELECT 1 FROM inbox_messages WHERE chat_thread_id IS NULL AND id=(SELECT message_id FROM harness_deliveries WHERE session_id=$1 AND id=$2 AND mode='managed') FOR UPDATE`, s.ID, in.DeliveryID); err != nil {
		return nil, err
	}
	err = tx.QueryRow(ctx, `SELECT message_id::text,cursor,completed_at,released_at FROM harness_deliveries WHERE session_id=$1 AND id=$2 AND mode='managed' FOR UPDATE`, s.ID, in.DeliveryID).Scan(&messageID, &cursor, &completed, &released)
	if err != nil {
		return nil, err
	}
	if cursor != in.Cursor {
		return nil, workorders.Fail(409, "delivery cursor mismatch")
	}
	if released != nil {
		return nil, workorders.Fail(409, "delivery lease was released")
	}
	if completed != nil {
		return map[string]any{"delivery_id": in.DeliveryID, "cursor": cursor, "completed_at": completed}, nil
	}
	if failed, err := inbox.MessageNotDelivered(ctx, tx, messageID); err != nil || failed {
		if err == nil {
			err = workorders.Fail(409, "message was not delivered")
		}
		return nil, err
	}
	if in.Outcome == "failed" {
		// AEON-279: a managed failure goes through the one terminal failure path
		// (inbox.FailMessage via FailSessionMessage), which needs the message
		// unacknowledged. The lease is completed first: FailMessage releases open
		// drain leases, and a lease is completed or released, never both.
		var acked bool
		if err = tx.QueryRow(ctx, `SELECT acked_at IS NOT NULL FROM inbox_messages WHERE chat_thread_id IS NULL AND id=$1 AND recipient_principal_id=$2`, messageID, s.AgentPrincipalID).Scan(&acked); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		if acked || errors.Is(err, pgx.ErrNoRows) {
			return nil, workorders.Fail(409, "message already acknowledged elsewhere")
		}
		if err = tx.QueryRow(ctx, `UPDATE harness_deliveries SET completed_at=clock_timestamp() WHERE id=$1 RETURNING completed_at`, in.DeliveryID).Scan(&completed); err != nil {
			return nil, err
		}
		err = inbox.FailSessionMessage(ctx, tx, p, messageID, in.FailureReason)
	} else {
		var acked *time.Time
		err = tx.QueryRow(ctx, `UPDATE inbox_messages SET acked_at=clock_timestamp(),acked_by_principal_id=$2 WHERE chat_thread_id IS NULL AND id=$1 AND recipient_principal_id=$2 AND acked_at IS NULL RETURNING acked_at`, messageID, s.AgentPrincipalID).Scan(&acked)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, workorders.Fail(409, "message already acknowledged elsewhere")
		}
		if err != nil {
			return nil, err
		}
		err = inbox.ConfirmSessionMessage(ctx, tx, p, messageID)
	}
	if err != nil {
		return nil, err
	}
	event := "delivery_acknowledged"
	if in.Outcome == "failed" {
		event = "delivery_failed"
	} else if err = inbox.MarkSessionSeen(ctx, tx, s.ID, inbox.SeenAck); err != nil {
		// Only a completed hand-off proves a live delivery path (AEON-280).
		return nil, err
	}
	if err = record(ctx, tx, p, s, event, nil, map[string]any{"message_id": messageID, "cursor": cursor, "outcome": in.Outcome, "failure_reason": in.FailureReason}); err != nil {
		return nil, err
	}
	if completed == nil {
		err = tx.QueryRow(ctx, `UPDATE harness_deliveries SET completed_at=clock_timestamp() WHERE id=$1 RETURNING completed_at`, in.DeliveryID).Scan(&completed)
		if err != nil {
			return nil, err
		}
	}
	if err = record(ctx, tx, p, s, "delivery_completed", nil, map[string]any{"delivery_id": in.DeliveryID, "cursor": cursor}); err != nil {
		return nil, err
	}
	return map[string]any{"delivery_id": in.DeliveryID, "cursor": cursor, "completed_at": completed}, nil
}
