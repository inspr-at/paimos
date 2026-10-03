// SPDX-License-Identifier: AGPL-3.0-only
package decisiondesk

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/inspr-at/paimos/internal/approvals"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// NoticesTx is the bounded source adapter for AEON-455's existing scheduler.
// Native project/workspace decision authority is filtered before LIMIT. Scope
// permissions checked only in Go receive a terminal skipped claim in ClaimTx.
func NoticesTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, limit int) ([]Item, error) {
	page, err := readProjection(ctx, tx, p, limit, nil, true, true)
	return page.Items, err
}

// CurrentTx rechecks just this source's access, state, revision and held-work
// links before transport. The caller reuses phone preferences and subscriptions.
func CurrentTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, item Item) (bool, error) {
	current, ok, err := currentSourceTx(ctx, tx, p, item)
	if err != nil || !ok {
		return false, err
	}
	return canDecideTx(ctx, tx, p, current)
}

func currentSourceTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, item Item) (Item, bool, error) {
	if p.Kind != tenant.Person || p.KeyCreatorID != "" || !validID(item.ID) || item.Revision < 1 {
		return Item{}, false, nil
	}
	source, permission := "", ""
	switch item.Kind {
	case "question":
		source, permission = "questions", "questions.read"
	case "approval":
		source, permission = "approvals", "approvals.read"
	case "action_request":
		source, permission = "held_requests", "inbox.manage"
	default:
		return Item{}, false, nil
	}
	// The source CTEs filter by native UUID before joins/held-work checks. Only
	// the selected source is referenced; totals, chores and project scans are absent.
	var now time.Time
	var raw []byte
	err := tx.QueryRow(ctx, sourceSQL+" SELECT clock.at,to_jsonb(s) FROM "+source+" s CROSS JOIN clock WHERE s.id=$3::uuid", p.TenantID, item.Kind, item.ID).Scan(&now, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return Item{}, false, nil
	}
	if err != nil {
		return Item{}, false, err
	}
	var current Item
	if err = json.Unmarshal(raw, &current); err != nil {
		return Item{}, false, err
	}
	if current.Revision != item.Revision || !current.PushEligible(now) {
		return Item{}, false, nil
	}
	if err = authz.RequireTx(ctx, tx, p, permission, authz.Scope{ProjectID: current.ProjectID}); err != nil {
		if errors.Is(err, authz.ErrForbidden) {
			return Item{}, false, nil
		}
		return Item{}, false, err
	}
	if item.Kind == "action_request" {
		// Keep the same canonicalization boundary as the projection, including an
		// answered canonical question. Use the source's project, never the caller hint.
		if err = authz.RequireTx(ctx, tx, p, "questions.read", authz.Scope{ProjectID: current.ProjectID}); err == nil {
			var canonical bool
			err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM desk_askers a JOIN desk_questions q ON q.tenant_id=a.tenant_id AND q.node_id=a.question_id WHERE a.tenant_id=$1 AND a.source_request_id=$2 AND q.project_id=$3)`, p.TenantID, current.ID, current.ProjectID).Scan(&canonical)
			if err != nil || canonical {
				return Item{}, false, err
			}
		} else if !errors.Is(err, authz.ErrForbidden) {
			return Item{}, false, err
		}
	}
	return current, true, nil
}

func canDecideTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, current Item) (bool, error) {
	if current.Kind == "approval" {
		return approvals.CanNotify(ctx, tx, p, current.ID)
	}
	permission := "inbox.manage"
	if current.Kind == "question" {
		permission = "questions.decide"
	}
	if err := authz.RequireTx(ctx, tx, p, permission, authz.Scope{ProjectID: current.ProjectID}); err != nil {
		if errors.Is(err, authz.ErrForbidden) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// claimExistsSQL preserves at-most-once delivery across canonical source aliases.
// Native UUID equality and the recipient index bound each durable-claim lookup.
const claimExistsSQL = `SELECT EXISTS(SELECT 1 FROM desk_notification_claims c
 WHERE c.tenant_id=$1 AND c.recipient_id=$2 AND (
 c.kind=$3 AND c.item_id=$4::uuid AND c.revision=$5
 OR $3='question' AND c.kind='action_request' AND c.revision=1 AND EXISTS(
  SELECT 1 FROM desk_askers a WHERE a.tenant_id=$1 AND a.question_id=$4::uuid AND a.source_request_id=c.item_id)
 OR $3='action_request' AND c.kind='question' AND EXISTS(
  SELECT 1 FROM desk_askers a WHERE a.tenant_id=$1 AND a.source_request_id=$4::uuid AND a.question_id=c.item_id)))`

// ClaimTx is the final admission in the scheduler's claim transaction. Tenant
// access fence, tree, native source row and current-source authorization precede
// the insert. No later lock or event acquisition belongs in this transaction.
func ClaimTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, item Item) (bool, error) {
	if p.Kind != tenant.Person || p.KeyCreatorID != "" || !validID(item.ID) || item.Revision < 1 {
		return false, nil
	}
	// Match project-access mutations: tenant authority fence before the tree.
	// LockProjectWrite retains both through the final authorization and insert.
	if err := authz.LockProjectWrite(ctx, tx, p.TenantID); err != nil {
		return false, err
	}
	var query string
	switch item.Kind {
	case "question":
		query = `SELECT node_id::text FROM desk_questions WHERE tenant_id=$1 AND node_id=$2 FOR NO KEY UPDATE`
	case "approval":
		query = `SELECT id::text FROM approval_requests WHERE tenant_id=$1 AND id=$2 FOR NO KEY UPDATE`
	case "action_request":
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,55))`, p.TenantID); err != nil {
			return false, err
		}
		query = `SELECT id::text FROM inbox_compat_messages WHERE tenant_id=$1 AND id=$2 FOR NO KEY UPDATE`
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
	current, ok, err := currentSourceTx(ctx, tx, p, item)
	if err != nil || !ok {
		return false, err
	}
	var exists bool
	if err = tx.QueryRow(ctx, claimExistsSQL, p.TenantID, p.ID, current.Kind, current.ID, current.Revision).Scan(&exists); err != nil || exists {
		return false, err
	}
	allowed, err := canDecideTx(ctx, tx, p, current)
	if err != nil {
		return false, err
	}
	state := "claimed"
	if !allowed {
		state = "skipped"
	}
	tag, err := tx.Exec(ctx, `INSERT INTO desk_notification_claims(tenant_id,kind,item_id,revision,recipient_id,state,completed_at) VALUES($1,$2,$3,$4,$5,$6,CASE WHEN $6='skipped' THEN clock_timestamp() END) ON CONFLICT DO NOTHING`, p.TenantID, current.Kind, current.ID, current.Revision, p.ID, state)
	return err == nil && allowed && tag.RowsAffected() == 1, err
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
	if !validKind(item.Kind) || item.Kind == "doctrine" || !validID(item.ID) || item.Revision < 1 {
		return nil, errors.New("invalid desk pointer")
	}
	prefix := map[string]string{"question": "q:", "action_request": "m:"}[item.Kind]
	href := "/agents?needs=" + prefix + item.ID
	if item.Kind == "approval" {
		href = "/phone-approvals/approval/" + item.ID
	}
	return json.Marshal(map[string]any{"url": href, "kind": item.Kind, "item_id": item.ID, "revision": item.Revision})
}
