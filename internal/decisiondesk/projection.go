// SPDX-License-Identifier: AGPL-3.0-only
package decisiondesk

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// All source membership predicates live here, including P3's answered-state
// boundary and source-request correlation. Fan-in contributes one question,
// never one item per asker. No inbox reply or answer revision is a new notice.
const visibleSQL = `WITH clock AS MATERIALIZED (SELECT statement_timestamp() AS at),
questions AS (
 SELECT q.node_id::text AS id,'question'::text AS kind,q.project_id::text AS project_id,q.revision,
 q.input->>'question' AS title,q.created_at,NULL::timestamptz AS expires_at,
 EXISTS(SELECT 1 FROM desk_askers a
  CROSS JOIN LATERAL jsonb_array_elements_text(coalesce(a.input->'blocked_node_ids','[]'::jsonb)) blocked(id)
  JOIN nodes work ON work.tenant_id=a.tenant_id AND work.id::text=blocked.id AND work.project_id=a.project_id
  JOIN node_kinds wk ON wk.tenant_id=work.tenant_id AND wk.id=work.kind_id
  WHERE a.tenant_id=q.tenant_id AND a.question_id=q.node_id
  AND a.input->>'meanwhile' IN ('parked','paused','stopped')
  AND work.deleted_at IS NULL AND coalesce(work.state,'') NOT IN ('done','delivered','cancelled','canceled','closed')
  AND (wk.slug IN ('ticket','task','epic','work_order') OR wk.field_schema->>'issue_family'='true')) AS held,
 '/api/questions/'||q.node_id AS source
 FROM desk_questions q JOIN nodes n ON n.tenant_id=q.tenant_id AND n.id=q.node_id
 JOIN nodes project ON project.tenant_id=q.tenant_id AND project.id=q.project_id
 WHERE q.tenant_id=$1 AND q.state='open' AND n.deleted_at IS NULL AND project.deleted_at IS NULL
 AND q.project_id=ANY($3::uuid[])
), approvals AS (
 SELECT r.id::text,'approval'::text,coalesce(n.project_id,wn.project_id)::text,1::bigint,
 'Approval'::text,r.proposed_at,r.expires_at,
 (n.id IS NOT NULL AND n.deleted_at IS NULL AND coalesce(n.state,'') NOT IN ('done','delivered','cancelled','canceled','closed'))
 OR (ar.id IS NOT NULL AND ar.status='waiting') AS held,
 '/api/phone-approvals/approval/'||r.id
 FROM approval_requests r
 LEFT JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.resource_id AND r.resource_kind='node'
 LEFT JOIN agent_runs ar ON ar.tenant_id=r.tenant_id AND ar.id=r.resource_id AND r.resource_kind='run'
 LEFT JOIN nodes wn ON wn.tenant_id=ar.tenant_id AND wn.id=ar.work_order_id
 LEFT JOIN nodes project ON project.tenant_id=r.tenant_id AND project.id=coalesce(n.project_id,wn.project_id)
 WHERE r.tenant_id=$1 AND r.expires_at>(SELECT at FROM clock)
 AND NOT EXISTS(SELECT 1 FROM approval_decisions d WHERE d.tenant_id=r.tenant_id AND d.request_id=r.id)
 AND (coalesce(n.project_id,wn.project_id)=ANY($4::uuid[]) OR coalesce(n.project_id,wn.project_id) IS NULL AND $6)
 AND (r.resource_kind='tenant' OR r.resource_kind='node' AND n.id IS NOT NULL AND n.deleted_at IS NULL
      OR r.resource_kind='run' AND ar.id IS NOT NULL AND wn.deleted_at IS NULL)
 AND (project.id IS NULL OR project.deleted_at IS NULL)
), held_requests AS (
 SELECT m.id::text,'action_request'::text,m.project_id::text,1::bigint,'Human request'::text,
 m.created_at,NULL::timestamptz,true,'/api/projects/'||m.project_id||'/messages/'||m.id
 FROM inbox_compat_messages m JOIN nodes project ON project.tenant_id=m.tenant_id AND project.id=m.project_id
 WHERE m.tenant_id=$1 AND m.project_id=ANY($5::uuid[]) AND m.is_action_request AND project.deleted_at IS NULL
 AND NOT EXISTS(SELECT 1 FROM events e WHERE e.tenant_id=m.tenant_id AND e.type='inbox.action_resolved'
   AND e.node_id=m.project_id AND e.after->>'message_id'=m.id::text)
 AND NOT EXISTS(SELECT 1 FROM desk_askers a JOIN desk_questions q ON q.tenant_id=a.tenant_id AND q.node_id=a.question_id
   WHERE a.tenant_id=m.tenant_id AND a.source_request_id=m.id AND q.project_id=ANY($3::uuid[]))
), doctrine AS (
 SELECT p.id::text,'doctrine'::text,NULL::text,1::bigint,'Doctrine change'::text,p.created_at,
 NULL::timestamptz,false,'/api/rules/doctrine/inbox/'||p.id
 FROM doctrine_proposals p
 WHERE p.tenant_id=$1 AND $7 AND p.data->>'inbox'='true' AND p.data->>'state'='pending'
 AND coalesce((p.data->>'pr_number')::int,0)=0 AND p.created_at>(SELECT at FROM clock)-interval '30 days'
), visible AS MATERIALIZED (
 SELECT *,CASE WHEN held AND kind='approval' THEN 0 WHEN held THEN 1 ELSE 2 END AS bucket,
 CASE WHEN held AND kind='approval' THEN expires_at ELSE created_at END AS order_at
 FROM (SELECT * FROM questions UNION ALL SELECT * FROM approvals UNION ALL SELECT * FROM held_requests UNION ALL SELECT * FROM doctrine) items
), totals AS (
 SELECT count(*)::int AS open,count(*) FILTER (WHERE held)::int AS held,
 (SELECT count(*)::int FROM harness_attach_requests WHERE tenant_id=$1 AND owner_id=$2 AND state='pending' AND expires_at>(SELECT at FROM clock)) AS chores
 FROM visible
), page AS (
 SELECT * FROM visible WHERE (NOT $8 OR (bucket,order_at,id,kind)>($9,$10::timestamptz,$11,$12))
 AND (NOT $14 OR (held AND kind IN ('question','approval','action_request') OR kind='approval' AND expires_at<=(SELECT at FROM clock)+$18::int*interval '1 second'))
 AND (NOT $15 OR NOT EXISTS(SELECT 1 FROM desk_notification_claims c
  WHERE c.tenant_id=$1 AND c.kind=visible.kind AND c.item_id::text=visible.id AND c.revision=visible.revision AND c.recipient_id=$2))
 AND ($16='' OR id=$16 AND kind=$17)
 ORDER BY bucket,order_at,id,kind LIMIT $13
)
SELECT (SELECT at FROM clock),totals.open,totals.held,totals.chores,
 coalesce((SELECT jsonb_agg(to_jsonb(page) ORDER BY bucket,order_at,id,kind) FROM page),'[]'::jsonb) FROM totals`

// ReadTx is the one adapter consumed by the HTTP projection and the existing
// phone notification worker. It does not acquire source mutation locks.
func ReadTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, limit int, after *cursor) (Page, error) {
	return readProjection(ctx, tx, p, limit, after, false, false, "", "")
}

func readProjection(ctx context.Context, tx pgx.Tx, p tenant.Principal, limit int, after *cursor, eligibleOnly, unclaimedOnly bool, id, kind string) (Page, error) {
	page := Page{Items: []Item{}}
	if p.Kind != tenant.Person || p.KeyCreatorID != "" || limit < 1 || limit > 100 {
		return page, errors.New("invalid desk reader")
	}
	check, err := authz.ProjectsTx(ctx, tx, p)
	if err != nil {
		return page, err
	}
	projects := map[string][]string{"questions.read": {}, "approvals.read": {}, "inbox.manage": {}}
	rows, err := tx.Query(ctx, `SELECT n.id::text FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.tenant_id=$1 AND k.slug='project' AND n.deleted_at IS NULL ORDER BY n.id LIMIT 1001`, p.TenantID)
	if err != nil {
		return page, err
	}
	n := 0
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return page, err
		}
		n++
		for permission := range projects {
			if check(permission, id) {
				projects[permission] = append(projects[permission], id)
			}
		}
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return page, err
	}
	if n > 1000 {
		return page, errors.New("desk project coverage exceeds 1000 projects")
	}
	c := cursor{}
	if after != nil {
		c = *after
	}
	var raw []byte
	err = tx.QueryRow(ctx, visibleSQL, p.TenantID, p.ID, projects["questions.read"], projects["approvals.read"], projects["inbox.manage"], check("approvals.read", ""), check("rules.read", "") && check("rules.write", ""), after != nil, c.Bucket, c.At, c.ID, c.Kind, limit+1, eligibleOnly, unclaimedOnly, id, kind, int(NearExpiry/time.Second)).Scan(&page.AsOf, &page.Counts.Open, &page.Counts.Held, &page.Counts.Chores, &raw)
	if err != nil {
		return page, err
	}
	// The internal order fields also form the opaque keyset cursor.
	var records []struct {
		Item
		Bucket  int             `json:"bucket"`
		OrderAt json.RawMessage `json:"order_at"`
	}
	if err = json.Unmarshal(raw, &records); err != nil {
		return page, err
	}
	page.HasMore = len(records) > limit
	if page.HasMore {
		records = records[:limit]
	}
	for _, record := range records {
		i := record.Item
		i.Bucket = record.Bucket
		if err = json.Unmarshal(record.OrderAt, &i.OrderAt); err != nil {
			return page, err
		}
		if i.Kind == "doctrine" {
			i.Href = "/settings/agent-rules#doctrine-inbox"
		} else {
			prefix := map[string]string{"question": "q:", "approval": "a:", "action_request": "m:"}[i.Kind]
			i.Href = "/agents?needs=" + prefix + i.ID
		}
		page.Items = append(page.Items, i)
	}
	if page.HasMore {
		i := page.Items[len(page.Items)-1]
		b, err := json.Marshal(cursor{Bucket: i.Bucket, At: i.OrderAt, ID: i.ID, Kind: i.Kind})
		if err != nil {
			return page, fmt.Errorf("desk cursor: %w", err)
		}
		page.NextCursor = base64.RawURLEncoding.EncodeToString(b)
	}
	return page, nil
}
