// SPDX-License-Identifier: AGPL-3.0-only
package harness

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Progress is a read projection, never a coordinator-authored estimate. Include
// direct worker children on this ticket which are active or cleanly finished.
// Missing progress counts as zero; failed, removed and other-ticket runs do not
// inflate the result. Query all visible children even when the list is paged.
func stampCoordinatorProgress(ctx context.Context, tx pgx.Tx, sessions []*Session) error {
	ids := []string{}
	byID := map[string]*Session{}
	for _, s := range sessions {
		if s.Role == "coordinator" && s.StoppedAt == nil && s.ArchivedAt == nil {
			ids = append(ids, s.ID)
			byID[s.ID] = s
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT c.parent_id::text, round(avg(coalesce(c.progress_pct,0)))::int
		FROM harness_sessions c JOIN harness_sessions p ON p.tenant_id=c.tenant_id AND p.id=c.parent_id
		WHERE c.parent_id=ANY($1::uuid[]) AND c.role='worker' AND c.archived_at IS NULL
		AND c.ticket_node_id IS NOT DISTINCT FROM p.ticket_node_id
		AND (c.stopped_at IS NULL OR aeon_session_finished(c.stopped_at,c.stop_reason,c.progress_pct))
		GROUP BY c.parent_id`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var progress int
		if err := rows.Scan(&id, &progress); err != nil {
			return err
		}
		byID[id].ProgressPct = &progress
	}
	return rows.Err()
}
