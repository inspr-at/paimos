// SPDX-License-Identifier: AGPL-3.0-only
package decisiondesk

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/inspr-at/paimos/internal/approvals"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// NoticesTx is the bounded source adapter for AEON-455's existing scheduler.
// Already claimed recipients are removed before LIMIT, so quiet normal work
// and old sent notices cannot starve later held work or expiry warnings.
func NoticesTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, limit int) ([]Item, error) {
	page, err := readProjection(ctx, tx, p, limit, nil, true, true, "", "")
	return page.Items, err
}

// CurrentTx rechecks access, source state, revision and held-work links directly
// before transport. The caller reuses phone preferences and subscription checks.
func CurrentTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, item Item) (bool, error) {
	page, err := readProjection(ctx, tx, p, 1, nil, true, false, item.ID, item.Kind)
	if err != nil {
		return false, err
	}
	if len(page.Items) != 1 || page.Items[0].Revision != item.Revision || !page.Items[0].PushEligible(page.AsOf) {
		return false, nil
	}
	current := page.Items[0]
	if current.Kind == "approval" {
		return approvals.CanNotify(ctx, tx, p, current.ID)
	}
	permission := "inbox.manage"
	if current.Kind == "question" {
		permission = "questions.decide"
	}
	if err = authz.RequireTx(ctx, tx, p, permission, authz.Scope{ProjectID: current.ProjectID}); err != nil {
		if errors.Is(err, authz.ErrForbidden) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// ClaimTx must be the final admission in the scheduler's claim transaction.
// It serializes with access changes and decisions on the existing tree fence,
// reauthorizes inside the write, then claims once per source/revision/recipient.
// No locks or events follow it in this transaction.
func ClaimTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, item Item) (bool, error) {
	if p.Kind != tenant.Person || p.KeyCreatorID != "" {
		return false, nil
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, p.TenantID); err != nil {
		return false, err
	}
	var query, permission string
	switch item.Kind {
	case "question":
		query = `SELECT node_id::text FROM desk_questions WHERE tenant_id=$1 AND node_id=$2 FOR NO KEY UPDATE`
		permission = "questions.read"
	case "approval":
		query = `SELECT id::text FROM approval_requests WHERE tenant_id=$1 AND id=$2 FOR NO KEY UPDATE`
		permission = "approvals.read"
	case "action_request":
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,55))`, p.TenantID); err != nil {
			return false, err
		}
		query = `SELECT id::text FROM inbox_compat_messages WHERE tenant_id=$1 AND id=$2 FOR NO KEY UPDATE`
		permission = "inbox.manage"
	default:
		return false, nil
	}
	var id string
	if err := tx.QueryRow(ctx, query, p.TenantID, item.ID).Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	current, err := CurrentTx(ctx, tx, p, item)
	if err != nil || !current {
		return false, err
	}
	if err = authz.RequireTx(ctx, tx, p, permission, authz.Scope{ProjectID: item.ProjectID}); err != nil {
		if errors.Is(err, authz.ErrForbidden) {
			return false, nil
		}
		return false, err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO desk_notification_claims(tenant_id,kind,item_id,revision,recipient_id) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, p.TenantID, item.Kind, item.ID, item.Revision, p.ID)
	return err == nil && tag.RowsAffected() == 1, err
}

// FinishTx records transport evidence without private content. A failed or
// unknown delivery is not reported as sent and never resets its durable claim.
func FinishTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, item Item, state string) error {
	if state != "sent" && state != "failed" && state != "skipped" {
		return errors.New("invalid desk notification result")
	}
	tag, err := tx.Exec(ctx, `UPDATE desk_notification_claims SET state=$6,completed_at=clock_timestamp() WHERE tenant_id=$1 AND kind=$2 AND item_id=$3 AND revision=$4 AND recipient_id=$5 AND state='claimed'`, p.TenantID, item.Kind, item.ID, item.Revision, p.ID, state)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("desk notification claim already completed or missing")
	}
	return nil
}

// Payload never includes title, question, rationale, context, findings or an
// answer. Opening a pointer performs the source's normal authorization again.
func Payload(item Item) ([]byte, error) {
	if !validKind(item.Kind) || item.Kind == "doctrine" || len(item.ID) != 36 || item.Revision < 1 {
		return nil, errors.New("invalid desk pointer")
	}
	prefix := map[string]string{"question": "q:", "action_request": "m:"}[item.Kind]
	href := "/agents?needs=" + prefix + item.ID
	if item.Kind == "approval" {
		href = "/phone-approvals/approval/" + item.ID
	}
	return json.Marshal(map[string]any{"url": href, "kind": item.Kind, "item_id": item.ID, "revision": item.Revision})
}
