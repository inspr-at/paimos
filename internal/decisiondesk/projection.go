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
const sourceSQL = `WITH clock AS MATERIALIZED (SELECT statement_timestamp() AS at),
questions(id,kind,project_id,revision,title,created_at,expires_at,held,source) AS (
 SELECT q.node_id AS id,'question'::text AS kind,q.project_id AS project_id,q.revision,
 q.input->>'question' AS title,q.created_at,NULL::timestamptz AS expires_at,
 EXISTS(SELECT 1 FROM desk_askers a
  WHERE a.tenant_id=q.tenant_id AND a.question_id=q.node_id
  AND a.input->>'meanwhile' IN ('parked','paused','stopped')
  AND (EXISTS(SELECT 1
   FROM jsonb_array_elements_text(coalesce(a.input->'blocked_node_ids','[]'::jsonb)) blocked(id)
   JOIN nodes work ON work.tenant_id=a.tenant_id AND work.id::text=blocked.id AND work.project_id=a.project_id
   JOIN node_kinds wk ON wk.tenant_id=work.tenant_id AND wk.id=work.kind_id
   LEFT JOIN work_orders wo ON wo.tenant_id=work.tenant_id AND wo.node_id=work.id
   WHERE (wo.node_id IS NULL OR wo.status NOT IN ('done','cancelled')) AND work.deleted_at IS NULL AND coalesce(work.state,'') NOT IN ('done','delivered','cancelled','canceled','closed')
   AND (wk.slug IN ('ticket','task','epic','work_order') OR wk.field_schema->>'issue_family'='true'))
   OR EXISTS(SELECT 1 FROM inbox_compat_messages held
    WHERE held.tenant_id=a.tenant_id AND held.project_id=a.project_id AND held.id=a.source_request_id AND held.is_action_request
    AND NOT EXISTS(SELECT 1 FROM events e WHERE e.tenant_id=held.tenant_id AND e.node_id=held.project_id
     AND e.type='inbox.action_resolved' AND e.after->>'message_id'=held.id::text)))) AS held,
 '/api/questions/'||q.node_id AS source
 FROM desk_questions q JOIN nodes n ON n.tenant_id=q.tenant_id AND n.id=q.node_id
 JOIN nodes project ON project.tenant_id=q.tenant_id AND project.id=q.project_id
 WHERE q.tenant_id=$1 AND q.state='open' AND n.deleted_at IS NULL AND project.deleted_at IS NULL
 AND ($2='' OR $2='question' AND q.node_id=$3::uuid)
), approvals(id,kind,project_id,revision,title,created_at,expires_at,held,source) AS (
 SELECT r.id,'approval'::text,coalesce(n.project_id,wn.project_id),1::bigint,
 'Approval · '||r.scope,r.proposed_at,r.expires_at,
 (n.id IS NOT NULL AND n.deleted_at IS NULL AND coalesce(n.state,'') NOT IN ('done','delivered','cancelled','canceled','closed')
  AND (nw.node_id IS NULL OR nw.status NOT IN ('done','cancelled'))
  AND (nk.slug IN ('ticket','task','epic','work_order') OR nk.field_schema->>'issue_family'='true'))
 OR (ar.id IS NOT NULL AND ar.status='waiting' AND aw.status NOT IN ('done','cancelled'))
 OR (rr.id IS NOT NULL AND rr.status='waiting' AND rw.status NOT IN ('done','cancelled') AND rn.deleted_at IS NULL) AS held,
 '/agents?needs=a:'||r.id
 FROM approval_requests r
 LEFT JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.resource_id AND r.resource_kind='node'
 LEFT JOIN node_kinds nk ON nk.tenant_id=n.tenant_id AND nk.id=n.kind_id
 LEFT JOIN work_orders nw ON nw.tenant_id=n.tenant_id AND nw.node_id=n.id
 LEFT JOIN agent_runs ar ON ar.tenant_id=r.tenant_id AND ar.id=r.resource_id AND r.resource_kind='run'
 LEFT JOIN nodes wn ON wn.tenant_id=ar.tenant_id AND wn.id=ar.work_order_id
 LEFT JOIN work_orders aw ON aw.tenant_id=ar.tenant_id AND aw.node_id=ar.work_order_id
 LEFT JOIN agent_runs rr ON rr.tenant_id=r.tenant_id AND rr.id=r.run_id AND rr.agent_principal_id=r.agent_principal_id
 LEFT JOIN work_orders rw ON rw.tenant_id=rr.tenant_id AND rw.node_id=rr.work_order_id
 LEFT JOIN nodes rn ON rn.tenant_id=rr.tenant_id AND rn.id=rr.work_order_id
 LEFT JOIN nodes project ON project.tenant_id=r.tenant_id AND project.id=coalesce(n.project_id,wn.project_id)
 WHERE r.tenant_id=$1 AND r.expires_at>(SELECT at FROM clock)
 AND NOT EXISTS(SELECT 1 FROM approval_decisions d WHERE d.tenant_id=r.tenant_id AND d.request_id=r.id)
 AND ($2='' OR $2='approval' AND r.id=$3::uuid)
 AND (r.resource_kind='tenant' OR r.resource_kind='node' AND n.id IS NOT NULL AND n.deleted_at IS NULL
      OR r.resource_kind='run' AND ar.id IS NOT NULL AND wn.deleted_at IS NULL)
 AND (project.id IS NULL OR project.deleted_at IS NULL)
), held_requests(id,kind,project_id,revision,title,created_at,expires_at,held,source) AS (
 SELECT m.id,'action_request'::text,m.project_id,1::bigint,'Human request · '||project.key,
 m.created_at,NULL::timestamptz,true,'/agents?needs=m:'||m.id
 FROM inbox_compat_messages m JOIN nodes project ON project.tenant_id=m.tenant_id AND project.id=m.project_id
 WHERE m.tenant_id=$1 AND ($2='' OR $2='action_request' AND m.id=$3::uuid) AND m.is_action_request AND project.deleted_at IS NULL
 AND NOT EXISTS(SELECT 1 FROM events e WHERE e.tenant_id=m.tenant_id AND e.type='inbox.action_resolved'
   AND e.node_id=m.project_id AND e.after->>'message_id'=m.id::text)
), doctrine(id,kind,project_id,revision,title,created_at,expires_at,held,source) AS (
 SELECT p.id,'doctrine'::text,NULL::uuid,1::bigint,'Doctrine change'::text,p.created_at,
 NULL::timestamptz,false,'/settings/agent-rules#doctrine-inbox'
 FROM doctrine_proposals p
 WHERE p.tenant_id=$1 AND ($2='' OR $2='doctrine' AND p.id=$3::uuid) AND p.data->>'inbox'='true' AND p.data->>'state'='pending'
 AND coalesce((p.data->>'pr_number')::int,0)=0 AND p.created_at>(SELECT at FROM clock)-interval '30 days'
)`

const visibleSQL = sourceSQL + `, visible AS MATERIALIZED (
 SELECT *,CASE WHEN held AND kind='approval' THEN 0 WHEN held THEN 1 ELSE 2 END AS bucket,
 CASE WHEN held AND kind='approval' THEN expires_at ELSE created_at END AS order_at
 FROM (
 SELECT * FROM questions WHERE project_id=ANY($5::uuid[]) AND (NOT $16 OR project_id=ANY($20::uuid[]))
 UNION ALL SELECT * FROM approvals WHERE (project_id=ANY($6::uuid[]) OR project_id IS NULL AND $8) AND (NOT $16 OR $19)
 UNION ALL SELECT * FROM held_requests WHERE project_id=ANY($7::uuid[])
 AND NOT EXISTS(SELECT 1 FROM desk_askers a JOIN desk_questions q ON q.tenant_id=a.tenant_id AND q.node_id=a.question_id
  WHERE a.tenant_id=$1 AND a.source_request_id=held_requests.id AND q.project_id=ANY($5::uuid[]))
 UNION ALL SELECT * FROM doctrine WHERE $9
 ) items
), totals AS (
 SELECT count(*)::int AS open,count(*) FILTER (WHERE held)::int AS held,
 (SELECT count(*)::int FROM harness_attach_requests WHERE tenant_id=$1 AND owner_id=$4 AND state='pending' AND expires_at>(SELECT at FROM clock)) AS chores
 FROM visible
), page AS (
 SELECT * FROM visible WHERE (NOT $10 OR (bucket,order_at,id,kind)>($11,$12::timestamptz,nullif($13,'')::uuid,$14))
 AND (NOT $16 OR (held AND kind IN ('question','approval','action_request') OR kind='approval' AND expires_at<=(SELECT at FROM clock)+$18::int*interval '1 second'))
 AND (NOT $17 OR NOT EXISTS(SELECT 1 FROM desk_notification_claims c
  WHERE c.tenant_id=$1 AND c.recipient_id=$4 AND (
    c.kind=visible.kind AND c.item_id=visible.id AND c.revision=visible.revision
    OR visible.kind='question' AND c.kind='action_request' AND c.revision=1 AND EXISTS(
     SELECT 1 FROM desk_askers a WHERE a.tenant_id=$1 AND a.question_id=visible.id AND a.source_request_id=c.item_id)
    OR visible.kind='action_request' AND c.kind='question' AND EXISTS(
     SELECT 1 FROM desk_askers a WHERE a.tenant_id=$1 AND a.source_request_id=visible.id AND a.question_id=c.item_id))))
 ORDER BY bucket,order_at,id,kind LIMIT $15
)
SELECT (SELECT at FROM clock),totals.open,totals.held,totals.chores,
 coalesce((SELECT jsonb_agg(to_jsonb(page) ORDER BY bucket,order_at,id,kind) FROM page),'[]'::jsonb) FROM totals`

// ReadTx is the one adapter consumed by the HTTP projection and the existing
// phone notification worker. It does not acquire source mutation locks.
func ReadTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, limit int, after *cursor) (Page, error) {
	return readProjection(ctx, tx, p, limit, after, false, false)
}

func readProjection(ctx context.Context, tx pgx.Tx, p tenant.Principal, limit int, after *cursor, eligibleOnly, unclaimedOnly bool) (Page, error) {
	page := Page{Items: []Item{}}
	if p.Kind != tenant.Person || p.KeyCreatorID != "" || limit < 1 || limit > 100 {
		return page, errors.New("invalid desk reader")
	}
	check, err := authz.ProjectsTx(ctx, tx, p)
	if err != nil {
		return page, err
	}
	projects := map[string][]string{"questions.read": {}, "questions.decide": {}, "approvals.read": {}, "inbox.manage": {}}
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
		return page, ErrCoverage
	}
	c := cursor{}
	if after != nil {
		c = *after
	}
	var raw []byte
	err = tx.QueryRow(ctx, visibleSQL, p.TenantID, "", nil, p.ID, projects["questions.read"], projects["approvals.read"], projects["inbox.manage"], check("approvals.read", ""), check("rules.read", "") && check("rules.write", ""), after != nil, c.Bucket, c.At, c.ID, c.Kind, limit+1, eligibleOnly, unclaimedOnly, int(NearExpiry/time.Second), check("approvals.decide", ""), projects["questions.decide"]).Scan(&page.AsOf, &page.Counts.Open, &page.Counts.Held, &page.Counts.Chores, &raw)
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
