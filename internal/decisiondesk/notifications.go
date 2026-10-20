// SPDX-License-Identifier: AGPL-3.0-only
package decisiondesk

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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

// ClaimTx is the final admission in the scheduler's claim transaction. Tenant,
// tree fence, native source row and current-source authorization precede
// the insert. No later lock or event acquisition belongs in this transaction.
func ClaimTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, item Item) (bool, error) {
	return claimTx(ctx, tx, p, item, false)
}

// ClaimBrowserTx uses the same at-most-once claim and adds person routing under
// the final write fence, so a new owner cannot inherit a stale recipient scan.
func ClaimBrowserTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, item Item) (bool, error) {
	return claimTx(ctx, tx, p, item, true)
}

func claimTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, item Item, browser bool) (bool, error) {
	if p.Kind != tenant.Person || p.KeyCreatorID != "" || !validID(item.ID) || item.Revision < 1 {
		return false, nil
	}
	// Match project-access mutations: tenant NO KEY UPDATE before the tree fence.
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
	if browser && allowed {
		_, allowed, err = BrowserCurrentTx(ctx, tx, p, current)
		if err != nil {
			return false, err
		}
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
	if state != "sent" && state != "failed" && state != "skipped" && state != "summarised" {
		return errors.New("invalid desk notification result")
	}
	tag, err := tx.Exec(ctx, `UPDATE desk_notification_claims SET state=CASE WHEN $6='summarised' THEN 'sent' ELSE $6 END,completed_at=clock_timestamp(),summarised_at=CASE WHEN $6='summarised' THEN clock_timestamp() END WHERE tenant_id=$1 AND kind=$2 AND item_id=$3 AND revision=$4 AND recipient_id=$5 AND state='claimed'`, p.TenantID, item.Kind, item.ID, item.Revision, p.ID, state)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("desk notification claim already completed or missing")
	}
	return nil
}

// BrowserCurrentTx adds exact person routing to the normal source authorization.
// Direct held messages belong only to their recipient. Questions without a held
// message use the project's human owner, resolved by the existing owner policy.
func BrowserCurrentTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, item Item) (Item, bool, error) {
	current, ok, err := currentSourceTx(ctx, tx, p, item)
	if err != nil || !ok {
		return Item{}, false, err
	}
	allowed, err := canDecideTx(ctx, tx, p, current)
	if err != nil || !allowed {
		return Item{}, false, err
	}
	var recipient bool
	switch current.Kind {
	case "question":
		err = tx.QueryRow(ctx, `SELECT coalesce(CASE WHEN EXISTS(SELECT 1 FROM desk_askers a WHERE a.tenant_id=q.tenant_id AND a.question_id=q.node_id AND a.source_request_id IS NOT NULL)
   THEN EXISTS(SELECT 1 FROM desk_askers a JOIN inbox_compat_messages m ON m.tenant_id=a.tenant_id AND m.id=a.source_request_id WHERE a.tenant_id=q.tenant_id AND a.question_id=q.node_id AND m.recipient_principal_id=$3)
   ELSE aeon_person_step_owner(q.project_id,(SELECT owner_principal_id FROM project_leads WHERE tenant_id=q.tenant_id AND project_id=q.project_id),NULL)=$3 END,false)
   FROM desk_questions q WHERE q.tenant_id=$1 AND q.node_id=$2`, p.TenantID, current.ID, p.ID).Scan(&recipient)
	case "action_request":
		err = tx.QueryRow(ctx, `SELECT recipient_principal_id=$3 FROM inbox_compat_messages WHERE tenant_id=$1 AND id=$2`, p.TenantID, current.ID, p.ID).Scan(&recipient)
	case "approval":
		err = tx.QueryRow(ctx, `SELECT coalesce(aeon_person_step_owner($1::uuid,(SELECT owner_principal_id FROM project_leads WHERE tenant_id=$2 AND project_id=$1::uuid),NULL)=$3,false)`, nullableProject(current.ProjectID), p.TenantID, p.ID).Scan(&recipient)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return Item{}, false, nil
	}
	return current, recipient, err
}

func nullableProject(id string) any {
	if id == "" {
		return nil
	}
	return id
}

// BrowserPayloadTx reads only an authorized current source and its project key.
func BrowserPayloadTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, item Item) ([]byte, bool, error) {
	current, ok, err := BrowserCurrentTx(ctx, tx, p, item)
	if err != nil || !ok {
		return nil, false, err
	}
	key := ""
	if current.ProjectID != "" {
		if err = tx.QueryRow(ctx, `SELECT key FROM nodes WHERE tenant_id=$1 AND id=$2 AND deleted_at IS NULL`, p.TenantID, current.ProjectID).Scan(&key); err != nil {
			return nil, false, err
		}
	}
	payload, err := Payload(current, key)
	return payload, err == nil, err
}

// Payload contains only the source's first line and project key, never context,
// findings, comments, attachments or an answer. Limits fit encrypted Web Push.
func Payload(item Item, projectKey string) ([]byte, error) {
	if (item.Kind != "question" && item.Kind != "approval" && item.Kind != "action_request") || !validID(item.ID) || item.Revision < 1 {
		return nil, errors.New("invalid desk pointer")
	}
	prefix := map[string]string{"question": "q:", "approval": "a:", "action_request": "m:"}[item.Kind]
	line, _, _ := strings.Cut(item.Title, "\n")
	line, _, _ = strings.Cut(line, "\r")
	// Truncate bytes at UTF-8 boundaries; JSON escaping also has a hard bound.
	bounded := func(s string, n int) string {
		var b strings.Builder
		for _, r := range s {
			if b.Len()+len(string(r)) > n {
				break
			}
			if r >= 32 {
				b.WriteRune(r)
			}
		}
		return b.String()
	}
	body := bounded(strings.TrimSpace(line), 240)
	if key := bounded(projectKey, 80); key != "" {
		body += " · " + key
	}
	return json.Marshal(map[string]string{"title": "PAIMOS · Decision waiting", "body": body, "tag": "desk-" + item.Kind + "-" + item.ID, "url": "/decision-desk?item=" + prefix + item.ID})
}

// BrowserNoticesTx pages before filtering exact recipients; unrelated visible
// items cannot occupy the first page forever. Fail explicitly at the coverage
// bound instead of silently truncating a summary or dropping later sources.
func BrowserNoticesTx(ctx context.Context, tx pgx.Tx, p tenant.Principal) ([]Item, error) {
	var after *cursor
	items := []Item{}
	for pageNumber := 0; pageNumber < 10; pageNumber++ {
		page, err := readProjection(ctx, tx, p, 100, after, true, true)
		if err != nil {
			return nil, err
		}
		for _, item := range page.Items {
			current, ok, err := BrowserCurrentTx(ctx, tx, p, item)
			if err != nil {
				return nil, err
			}
			if ok {
				items = append(items, current)
			}
		}
		if !page.HasMore {
			return items, nil
		}
		after, err = decodeCursor(page.NextCursor)
		if err != nil {
			return nil, err
		}
	}
	return nil, errors.New("desk notification coverage exceeds 1000 source items")
}

// PushSource adapts the projection to the existing phone push worker without a
// dependency cycle. Startup assigns it to phoneapprovals.Module.DeskPush.
type PushSource struct{}

func (PushSource) NoticesTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, visit func(kind, id string, revision int64, created time.Time)) error {
	items, err := BrowserNoticesTx(ctx, tx, p)
	if err != nil {
		return err
	}
	for _, item := range items {
		visit(item.Kind, item.ID, item.Revision, item.CreatedAt)
	}
	return nil
}
func (PushSource) ClaimTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, kind, id string, revision int64) (bool, error) {
	return ClaimBrowserTx(ctx, tx, p, Item{Kind: kind, ID: id, Revision: revision})
}
func (PushSource) PayloadTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, kind, id string, revision int64) ([]byte, bool, error) {
	return BrowserPayloadTx(ctx, tx, p, Item{Kind: kind, ID: id, Revision: revision})
}
func (PushSource) FinishTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, kind, id string, revision int64, state string) error {
	return FinishTx(ctx, tx, p, Item{Kind: kind, ID: id, Revision: revision}, state)
}
