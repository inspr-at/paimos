// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

const timingNodeLimit = 5000
const timingEventLimit = 100000
const lifelineEventLimit = 2000

var timingEventTypes = []string{
	"node.created", "node.updated", "import.history", "import.node_created", "import.node_updated", "import.parent_changed",
	"status_autopilot.changed", "status_autopilot.derived", "status_autopilot.undone", "status_autopilot.attention_apply",
	"status_autopilot.attention_dismiss", "status_autopilot.attention_undone", "delivery.merge_done",
	"queue.added", "queue.add_undone", "node.status_history_backfilled",
}

const timingWorkSQL = `k.slug IN ('work','ticket','task','epic') AND n.deleted_at IS NULL`
const timingLeafSQL = timingWorkSQL + ` AND (SELECT is_leaf FROM aeon_work_shape(n.id))`

func requireTiming(ctx context.Context, tx pgx.Tx, p tenant.Principal, project, permission string) error {
	s := authz.Scope{}
	if project != "" {
		s.ProjectID = project
	}
	if err := authz.RequireTx(ctx, tx, p, permission, s); err != nil {
		return err
	}
	return authz.RequireTx(ctx, tx, p, "events.read", s)
}

func timingProjectTx(ctx context.Context, tx pgx.Tx, project string) error {
	var id string
	return tx.QueryRow(ctx, `SELECT n.id::text FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
 WHERE n.tenant_id=current_setting('aeon.tenant_id')::uuid AND n.id=$1 AND k.slug='project' AND n.deleted_at IS NULL`, project).Scan(&id)
}

func loadTimingNodes(ctx context.Context, tx pgx.Tx, project, node string) ([]timingNode, error) {
	rows, err := tx.Query(ctx, `SELECT n.id::text,n.state,n.created_at,k.slug,
 left(coalesce(n.fields->>'priority',n.fields->'classic'->>'priority',''),64),
 EXISTS(SELECT 1 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(n.fields->'tags')='array' THEN n.fields->'tags' ELSE '[]'::jsonb END) tag
  WHERE lower(CASE WHEN jsonb_typeof(tag)='string' THEN tag#>>'{}' ELSE tag->>'name' END)='bug')
 FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
 WHERE n.tenant_id=current_setting('aeon.tenant_id')::uuid AND `+timingWorkSQL+`
 AND ($2::uuid IS NOT NULL OR (SELECT is_leaf FROM aeon_work_shape(n.id)))
 AND ($1::uuid IS NULL OR n.project_id=$1) AND ($2::uuid IS NULL OR n.id=$2)
 ORDER BY n.id LIMIT $3`, timingNullable(project), timingNullable(node), timingNodeLimit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []timingNode{}
	for rows.Next() {
		var n timingNode
		if err := rows.Scan(&n.ID, &n.State, &n.Created, &n.Kind, &n.Priority, &n.Bug); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) > timingNodeLimit {
		return nil, fail(503, "status timing ticket budget exceeded")
	}
	return out, nil
}

func timingNullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Extract timing fields in SQL, before transport/decode. Do not fetch event
// bodies, telemetry, credentials, unrelated metadata or complete snapshots.
func loadTimingEvents(ctx context.Context, tx pgx.Tx, nodes []timingNode, now time.Time, limit int) (map[string][]timingEvent, error) {
	out := map[string][]timingEvent{}
	if len(nodes) == 0 {
		return out, nil
	}
	ids := make([]string, len(nodes))
	for i, n := range nodes {
		ids[i] = n.ID
	}
	rows, err := tx.Query(ctx, `WITH facts AS MATERIALIZED (
 SELECT e.id,e.node_id,e.type,e.at,e.actor_principal_id,
 left(coalesce(e.before->>'state',''),64) AS before_state,
 left(coalesce(CASE WHEN e.type='import.history' THEN e.after->'record'->'snapshot'->>'status' ELSE e.after->>'state' END,''),64) AS after_state,
 left(coalesce(e.after->'record'->>'changed_by_name',''),200) AS source_name,
 coalesce(e.after->>'source_id',split_part(e.after->>'classic_ref',':',1))||':'||(e.after->'record'->>'changed_by') AS source_subject,
 left(coalesce(e.after->'fields'->>'release',''),128) AS release
 FROM events e WHERE e.tenant_id=current_setting('aeon.tenant_id')::uuid AND e.node_id=ANY($1::uuid[])
 AND e.type=ANY($2::text[]) AND e.at<=$3
 AND (e.type IN ('node.created','import.history','import.node_created','node.status_history_backfilled')
  OR e.before->>'state' IS DISTINCT FROM e.after->>'state')
 ORDER BY e.node_id,e.at,e.id LIMIT $4
 ) SELECT f.id,f.node_id::text,f.type,f.at,f.before_state,f.after_state,
 left(CASE WHEN f.type='import.history' THEN coalesce(old.name,nullif(f.source_name,''),'Unknown') ELSE coalesce(p.name,'Unknown') END,200),
 CASE WHEN f.type='import.history' THEN coalesce(old.kind,'unknown') ELSE coalesce(p.kind,'unknown') END,
 nullif(f.release,'')
 FROM facts f LEFT JOIN principals p ON p.tenant_id=current_setting('aeon.tenant_id')::uuid AND p.id=f.actor_principal_id
 LEFT JOIN LATERAL (
  SELECT left(coalesce(target.name,original.name),200) AS name,coalesce(target.kind,original.kind) AS kind
  FROM principals original JOIN identities i ON i.id=original.identity_id AND i.issuer='paimos-classic' AND i.subject=f.source_subject
  LEFT JOIN principals target ON target.tenant_id=original.tenant_id AND target.id=original.linked_to
  WHERE original.tenant_id=current_setting('aeon.tenant_id')::uuid LIMIT 1
 ) old ON f.type='import.history'
 ORDER BY f.node_id,f.at,f.id`, ids, timingEventTypes, now, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var e timingEvent
		var actor timingActor
		if err := rows.Scan(&e.ID, &e.Node, &e.Type, &e.At, &e.Before, &e.After, &actor.Name, &actor.Kind, &e.Release); err != nil {
			return nil, err
		}
		count++
		if count > limit {
			return nil, fail(503, "status timing event budget exceeded")
		}
		if actor.Kind != "agent" && actor.Kind != "person" {
			actor.Kind = "unknown"
		}
		e.Actor = &actor
		out[e.Node] = append(out[e.Node], e)
	}
	return out, rows.Err()
}

type timingBackfillResult struct {
	Recorded int     `json:"recorded"`
	Next     *string `json:"next_cursor"`
}

func backfillTimingTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, project, cursor string, limit int) (timingBackfillResult, error) {
	out := timingBackfillResult{}
	// The same tenant/tree fence serializes permission changes, node writes
	// and concurrent backfills. All rows are locked before the event counter.
	if err := authz.LockProjectWrite(ctx, tx, p.TenantID); err != nil {
		return out, err
	}
	if err := timingProjectTx(ctx, tx, project); err != nil {
		return out, err
	}
	if err := requireTiming(ctx, tx, p, project, "nodes.write"); err != nil {
		return out, err
	}
	rows, err := tx.Query(ctx, `SELECT n.id::text,n.state,n.created_at FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
 WHERE n.tenant_id=$1 AND n.project_id=$2 AND `+timingLeafSQL+`
 AND ($3::uuid IS NULL OR n.id>$3::uuid) ORDER BY n.id LIMIT $4 FOR NO KEY UPDATE OF n`, p.TenantID, project, timingNullable(cursor), limit+1)
	if err != nil {
		return out, err
	}
	nodes := []timingNode{}
	for rows.Next() {
		var n timingNode
		if err := rows.Scan(&n.ID, &n.State, &n.Created); err != nil {
			rows.Close()
			return out, err
		}
		nodes = append(nodes, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	if len(nodes) > limit {
		nodes = nodes[:limit]
		next := nodes[len(nodes)-1].ID
		out.Next = &next
	}
	histories, err := loadTimingEvents(ctx, tx, nodes, time.Now().UTC(), timingEventLimit)
	if err != nil {
		return out, err
	}
	changes := []events.Change{}
	for _, n := range nodes {
		usable := false
		for _, e := range histories[n.ID] {
			usable = usable || e.After != ""
		}
		if usable {
			continue
		}
		id, created := n.ID, n.Created
		changes = append(changes, events.Change{NodeID: &id, Type: "node.status_history_backfilled", At: &created,
			After: map[string]any{"state": n.State, "source": "inferred", "assumption": "current state since creation", "rule_version": 1}})
	}
	for _, change := range changes {
		if _, err := events.Append(ctx, tx, p, change); err != nil {
			return out, err
		}
		out.Recorded++
	}
	return out, nil
}
