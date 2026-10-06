// SPDX-License-Identifier: AGPL-3.0-only
package workqueue

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Progress recognizes the shipped In progress aliases.
func Progress(state string) bool {
	switch State(state) {
	case "in_progress", "progress", "active":
		return true
	}
	return false
}

// Stale returns only idle progress tickets from a bounded node set without
// revealing worker identities. Mutation callers hold the tenant/pairing/tree
// fences and the ticket row. exceptRun excludes Undo's own still-queued run.
func Stale(ctx context.Context, tx pgx.Tx, ids []string, exceptRun ...string) (map[string]bool, error) {
	if len(ids) > 1000 || len(exceptRun) > 1 {
		return nil, fmt.Errorf("stale projection input exceeds limit")
	}
	if len(ids) == 0 {
		return map[string]bool{}, nil
	}
	var excluded *string
	if len(exceptRun) == 1 {
		excluded = &exceptRun[0]
	}
	rows, err := tx.Query(ctx, `SELECT n.id::text FROM nodes n
 JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
 WHERE n.id=ANY($1::uuid[]) AND n.deleted_at IS NULL AND (k.slug IN ('ticket','task') OR (k.slug='work' AND NOT EXISTS(
 SELECT 1 FROM nodes c JOIN node_kinds ck ON ck.tenant_id=c.tenant_id AND ck.id=c.kind_id
 WHERE c.tenant_id=n.tenant_id AND c.parent_id=n.id AND c.deleted_at IS NULL AND ck.slug='work')))
 AND replace(replace(lower(btrim(n.state)),' ','_'),'-','_') IN ('in_progress','progress','active')
 AND coalesce(btrim(CASE
   WHEN n.fields ? 'assignee' THEN coalesce(n.fields->'assignee'->>'id',n.fields->>'assignee')
   WHEN n.fields ? 'assignee_id' THEN n.fields->>'assignee_id'
   ELSE n.fields->'classic'->>'assignee_id' END),'') = ''
 AND NOT EXISTS(SELECT 1 FROM agent_runs r JOIN nodes o ON o.tenant_id=r.tenant_id AND o.id=r.work_order_id
   WHERE r.tenant_id=n.tenant_id AND (r.queue_node_id=n.id OR o.parent_id=n.id)
   AND r.status IN ('queued','starting','running','waiting') AND ($2::uuid IS NULL OR r.id<>$2))
 AND NOT EXISTS(SELECT 1 FROM harness_sessions h WHERE h.tenant_id=n.tenant_id AND h.ticket_node_id=n.id
   AND h.archived_at IS NULL AND (h.stopped_at IS NULL
   OR (h.stop_reason='paused' AND h.pause_record->>'state' IN ('paused','resume_requested'))))`, ids, excluded)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
