// SPDX-License-Identifier: AGPL-3.0-only

package inbox

import (
	"context"
	"errors"
	"net/http"

	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

var errSessionEnded = &httpError{409, "session_ended", "This session has ended."}

func normalizeSession(id *string) error {
	if id == nil {
		return nil
	}
	normalized, ok := parseUUID(*id)
	if !ok {
		return badRequest("invalid session id")
	}
	*id = normalized
	return nil
}

func sessionQuery(r *http.Request) (*string, error) {
	if !r.URL.Query().Has("session") {
		return nil, nil
	}
	id := r.URL.Query().Get("session")
	return &id, normalizeSession(&id)
}

// Lock the exact generation through commit so stop/archive cannot race a send.
// A supplied session is never inferred from a principal's newest registration.
func messageSession(ctx context.Context, tx pgx.Tx, id *string, principal, project string) (string, error) {
	return lockSession(ctx, tx, id, principal, project, "FOR SHARE OF s")
}

// listeningSession is messageSession for a path that records the generation as
// listening (pull, stream, ack). It takes the row lock it will write with up
// front, never upgrading a shared lock, and always before any message row: the
// inbox lock order is session, message, delivery, receipt (AEON-280).
func listeningSession(ctx context.Context, tx pgx.Tx, id *string, principal, project string) (string, error) {
	return lockSession(ctx, tx, id, principal, project, "FOR NO KEY UPDATE OF s")
}

func lockSession(ctx context.Context, tx pgx.Tx, id *string, principal, project, lock string) (string, error) {
	if id == nil {
		return "", nil
	}
	var label string
	var active bool
	err := tx.QueryRow(ctx, `SELECT coalesce(s.display_label,p.name),s.stopped_at IS NULL AND s.archived_at IS NULL
 FROM harness_sessions s JOIN principals p ON p.tenant_id=s.tenant_id AND p.id=s.agent_principal_id
 WHERE s.id=$1::uuid AND s.agent_principal_id=$2::uuid AND ($3='' OR s.project_id::text=$3) `+lock, *id, principal, project).Scan(&label, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errNotFound
	}
	if err != nil {
		return "", err
	}
	if !active {
		return "", errSessionEnded
	}
	return label, nil
}

func sameSession(a, b *string) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

// ConfirmSessionMessage records the receiver confirmation for a message the
// caller acknowledged in this transaction (a direct ack or a harness drain
// completion). Session-bound and unbound messages alike move their receipt to
// handed_off (AEON-280); a receipt that already failed stays failed.
func ConfirmSessionMessage(ctx context.Context, tx pgx.Tx, p tenant.Principal, messageID string) error {
	var acked bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM inbox_messages WHERE id=$1::uuid AND recipient_principal_id=$2::uuid AND acked_at IS NOT NULL)`, messageID, p.ID).Scan(&acked); err != nil {
		return err
	}
	if !acked {
		return nil
	}
	return confirmReceived(ctx, tx, p, messageID)
}

// FailSessionMessage settles a managed hand-off that failed or could not be
// confirmed (AEON-282) through the one terminal failure path, FailMessage
// (AEON-280): dead delivery, failed receipt, the message hidden from every read
// path and exactly one System notice to the sender. The caller must not
// acknowledge the message first (FailMessage treats acknowledged as delivered)
// and must already hold the rows in the inbox lock order: session, message,
// delivery, receipt. reason is the harness failure_reason and maps to an
// allowed delivery reason (managedFailureReason).
//
// A message accepted before deadlines existed has no failure notice by design.
// FailMessage leaves it alone, so it keeps the reviewed AEON-282 settlement:
// acknowledged, so a drain never injects it again, and session-bound receipts
// fail without a notice.
func FailSessionMessage(ctx context.Context, tx pgx.Tx, p tenant.Principal, messageID, reason string) error {
	failed, err := FailMessage(ctx, tx, messageID, managedFailureReason(reason))
	if err != nil || failed {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE inbox_messages SET acked_at=clock_timestamp(),acked_by_principal_id=$2::uuid WHERE id=$1::uuid AND recipient_principal_id=$2::uuid AND acked_at IS NULL AND (expires_at IS NULL OR expires_at>clock_timestamp())`, messageID, p.ID)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	var bound bool
	if err := tx.QueryRow(ctx, `SELECT recipient_session_id IS NOT NULL FROM inbox_messages WHERE id=$1::uuid`, messageID).Scan(&bound); err != nil || !bound {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE inbox_message_deliveries d SET state='dead',reason='transport_error' FROM inbox_compat_messages c WHERE c.tenant_id=d.tenant_id AND c.id=d.message_id AND c.inbox_message_id=$1::uuid AND c.recipient_session_id IS NOT NULL`, messageID); err != nil {
		return err
	}
	return advanceReceipt(ctx, tx, p, messageID, "failed", "", reason, receiptTarget{})
}

// managedFailureReason maps a managed hand-off failure_reason onto the reasons
// inbox_message_deliveries_reason_check allows (migration 0926). A new reason
// needs a migration first, so anything unknown is a transport error.
func managedFailureReason(reason string) string {
	switch reason {
	case "child_unavailable", ReasonUnavailable:
		return ReasonUnavailable
	case ReasonDeadline, ReasonAttempts, ReasonSessionEnded, ReasonNoListener, "unsupported":
		return reason
	}
	return ReasonTransportError // outcome_unconfirmed and anything else
}
