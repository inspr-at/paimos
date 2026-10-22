// SPDX-License-Identifier: AGPL-3.0-only
package harness

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// Progress is a read projection, never a coordinator-authored estimate. Include
// direct worker children on this ticket and verified cross-project children
// which are active or cleanly finished. Cross-project work keeps its own ticket.
// Missing progress counts as zero; failed, removed and unrelated same-project
// runs do not inflate the result. All reads retain the caller's RLS visibility.
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
	rows, err := tx.Query(ctx, `SELECT p.id::text, round(avg(coalesce(c.progress_pct,0)))::int
		FROM harness_sessions p JOIN LATERAL (`+visibleChildIDs+`) edge ON true
		JOIN harness_sessions c ON c.tenant_id=p.tenant_id AND c.id=edge.id
		WHERE p.id=ANY($1::uuid[]) AND c.role='worker' AND c.archived_at IS NULL
		AND (c.ticket_node_id IS NOT DISTINCT FROM p.ticket_node_id OR c.project_id<>p.project_id)
		AND (c.stopped_at IS NULL OR aeon_session_finished(c.stopped_at,c.stop_reason,c.progress_pct))
		GROUP BY p.id`, ids)
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

// Both projections follow the same visible edges. A verified cross-project
// link takes precedence, as it does in the tree; it never extends write scope.
const visibleChildIDs = `SELECT c.id FROM harness_sessions c
 WHERE c.tenant_id=p.tenant_id AND c.parent_id=p.id
 AND NOT EXISTS(SELECT 1 FROM harness_session_lineage l WHERE l.tenant_id=c.tenant_id AND l.child_session_id=c.id)
 UNION ALL
 SELECT c.id FROM harness_session_lineage l
 JOIN harness_sessions c ON c.tenant_id=l.tenant_id AND c.id=l.child_session_id
 WHERE l.tenant_id=p.tenant_id AND l.parent_session_id=p.id`

type ProjectChildCounts struct {
	ProjectKey string `json:"project_key"`
	Current    int    `json:"current"`
	All        int    `json:"all"`
}

type ChildCounts struct {
	Current         int                  `json:"current"`
	All             int                  `json:"all"`
	CrossProject    []ProjectChildCounts `json:"cross_project"`
	OmittedProjects int                  `json:"omitted_projects"`
}

// Count the whole authorized family, even when descendants are outside the
// requested page. The 16-level hierarchy bound also protects malformed cycles.
// Current membership retains ancestors of live, paused or lingering sessions,
// matching the tree's default fold; All supports its opt-in stopped history.
func stampChildCounts(ctx context.Context, tx pgx.Tx, sessions []*Session, now time.Time) error {
	ids := make([]string, 0, len(sessions))
	byID := make(map[string]*Session, len(sessions))
	for _, s := range sessions {
		ids = append(ids, s.ID)
		byID[s.ID] = s
		s.ChildCounts = &ChildCounts{CrossProject: []ProjectChildCounts{}}
	}
	rows, err := tx.Query(ctx, `WITH RECURSIVE family AS (
 SELECT s.id AS root,s.tenant_id,s.id,ARRAY[s.id] AS path FROM harness_sessions s WHERE s.id=ANY($1::uuid[])
 UNION ALL
 SELECT f.root,f.tenant_id,edge.id,f.path||edge.id FROM family f
 JOIN harness_sessions p ON p.tenant_id=f.tenant_id AND p.id=f.id
 JOIN LATERAL (`+visibleChildIDs+`) edge ON true
 WHERE cardinality(f.path)<17 AND NOT edge.id=ANY(f.path)
), current_members AS (
 SELECT DISTINCT f.root,unnest(f.path[2:]) AS id FROM family f
 JOIN harness_sessions c ON c.tenant_id=f.tenant_id AND c.id=f.id
 WHERE c.archived_at IS NULL AND (c.stopped_at IS NULL OR c.stopped_at>$2::timestamptz-interval '10 minutes'
   OR (c.stop_reason='paused' AND c.pause_record->>'state' IN ('paused','resume_requested')))
), project_counts AS (
 SELECT f.root,c.project_id,n.key AS project_key,count(*)::int AS all_count,
 count(cm.id)::int AS current_count
 FROM family f JOIN harness_sessions c ON c.tenant_id=f.tenant_id AND c.id=f.id
 JOIN nodes n ON n.tenant_id=c.tenant_id AND n.id=c.project_id
 LEFT JOIN current_members cm ON cm.root=f.root AND cm.id=f.id
 WHERE f.id<>f.root GROUP BY f.root,c.project_id,n.key
), ranked AS (
 SELECT pc.*,sum(all_count) OVER(PARTITION BY root)::int AS all_total,
 sum(current_count) OVER(PARTITION BY root)::int AS current_total,
 count(*) OVER(PARTITION BY root)::int AS projects,
 row_number() OVER(PARTITION BY root ORDER BY project_key,project_id) AS ordinal
 FROM project_counts pc
)
 SELECT root::text,project_id::text,project_key,all_count,current_count,all_total,current_total,projects
 FROM ranked WHERE ordinal<=200 ORDER BY root,ordinal`, ids, now)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, projectID string
		var counts ProjectChildCounts
		var all, current, projects int
		if err := rows.Scan(&id, &projectID, &counts.ProjectKey, &counts.All, &counts.Current, &all, &current, &projects); err != nil {
			return err
		}
		s := byID[id]
		s.ChildCounts.All, s.ChildCounts.Current = all, current
		s.ChildCounts.OmittedProjects = max(0, projects-200)
		if projectID != s.ProjectID {
			s.ChildCounts.CrossProject = append(s.ChildCounts.CrossProject, counts)
		}
	}
	return rows.Err()
}
