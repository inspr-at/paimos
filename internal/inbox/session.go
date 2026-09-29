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
	if id == nil {
		return "", nil
	}
	var label string
	var active bool
	err := tx.QueryRow(ctx, `SELECT coalesce(s.display_label,p.name),s.stopped_at IS NULL AND s.archived_at IS NULL
 FROM harness_sessions s JOIN principals p ON p.tenant_id=s.tenant_id AND p.id=s.agent_principal_id
 WHERE s.id=$1::uuid AND s.agent_principal_id=$2::uuid AND ($3='' OR s.project_id::text=$3) FOR SHARE OF s`, *id, principal, project).Scan(&label, &active)
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

// ConfirmSessionMessage records a session inbox handoff, never a shared adapter
// handoff. The caller must first authenticate and acknowledge the exact message
// in this transaction. Unbound messages retain their existing receipt behavior.
func ConfirmSessionMessage(ctx context.Context, tx pgx.Tx, p tenant.Principal, messageID string) error {
	var targeted bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM inbox_messages WHERE id=$1::uuid AND recipient_principal_id=$2::uuid AND recipient_session_id IS NOT NULL AND acked_at IS NOT NULL)`, messageID, p.ID).Scan(&targeted); err != nil {
		return err
	}
	if !targeted {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE inbox_message_deliveries d SET state='delivered',effective_level='simple' FROM inbox_compat_messages c WHERE c.tenant_id=d.tenant_id AND c.id=d.message_id AND c.inbox_message_id=$1::uuid AND c.recipient_session_id IS NOT NULL`, messageID); err != nil {
		return err
	}
	return advanceReceipt(ctx, tx, p, messageID, "handed_off", "simple", "", receiptTarget{})
}

// FailSessionMessage settles authenticated, acknowledged input with an uncertain
// or rejected handoff. The harness lease and message are closed in the same tx.
func FailSessionMessage(ctx context.Context, tx pgx.Tx, p tenant.Principal, messageID, reason string) error {
	var owned bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM inbox_messages WHERE id=$1::uuid AND recipient_principal_id=$2::uuid AND acked_at IS NOT NULL)`, messageID, p.ID).Scan(&owned); err != nil {
		return err
	}
	if !owned {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE inbox_message_deliveries d SET state='dead',reason='transport_error' FROM inbox_compat_messages c WHERE c.tenant_id=d.tenant_id AND c.id=d.message_id AND c.inbox_message_id=$1::uuid`, messageID); err != nil {
		return err
	}
	return advanceReceipt(ctx, tx, p, messageID, "failed", "", reason, receiptTarget{})
}
