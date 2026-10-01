// SPDX-License-Identifier: AGPL-3.0-only

// Package workqueue projects ticket metadata from the existing agent run queue.
package workqueue

import (
	"context"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/jackc/pgx/v5"
)

type Actor struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}
type Queued struct {
	Position        int        `json:"position"`
	By              Actor      `json:"by"`
	At              time.Time  `json:"at"`
	RunID           string     `json:"run_id"`
	Targeted        bool       `json:"targeted"`
	TargetAgentID   *string    `json:"target_agent_id"`
	ExpectedAgentID *string    `json:"expected_agent_id"`
	ProfileID       *string    `json:"model_profile_id"`
	ExpectedStart   *time.Time `json:"expected_start_at"`
	Manual          bool       `json:"manual_order"`
	Waiting         bool       `json:"waiting"`
	WaitReason      string     `json:"wait_reason"`
	SecurityReview  bool       `json:"security_review_required"`
}

// CTE computes positions before a project or ID filter. The node join applies
// the caller's RLS visibility before ranking, so hidden tickets leak no counts.
const CTE = `WITH queue_source AS (
 SELECT r.*,n.key,n.title,n.state,n.fields,n.project_id,
 CASE n.fields->>'priority' WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 WHEN 'low' THEN 3 ELSE 2 END AS priority_order,
 bool_or(r.queue_rank IS NOT NULL) OVER(PARTITION BY r.queue_target_agent_id) AS manual_order
 FROM agent_runs r JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.queue_node_id
 WHERE r.status='queued' AND n.deleted_at IS NULL
 AND replace(replace(lower(btrim(n.state)),' ','_'),'-','_') IN ('new','open','backlog','blocked')
 ), queue_ordered AS (
 SELECT *,row_number() OVER(PARTITION BY queue_target_agent_id ORDER BY
 CASE WHEN queue_target_agent_id IS NOT NULL OR manual_order THEN 0 ELSE priority_order END,
 queue_rank NULLS LAST,
 CASE WHEN queue_target_agent_id IS NOT NULL THEN queue_at END DESC,
 queue_at,id)::int AS queue_position FROM queue_source
 ), queue_heads AS (
 SELECT *, min(queue_position) FILTER(WHERE lower(btrim(state))<>'blocked') OVER(PARTITION BY queue_target_agent_id) AS first_ready_position FROM queue_ordered
 ) `

func Load(ctx context.Context, tx pgx.Tx, ids []string) (map[string]*Queued, error) {
	rows, err := tx.Query(ctx, CTE+`SELECT q.queue_node_id::text,q.queue_position,
 p.id::text,p.name,p.kind,q.queue_at,q.id::text,q.queue_target_agent_id::text,
 CASE WHEN q.queue_target_agent_id IS NOT NULL OR q.queue_routed_at IS NOT NULL THEN q.agent_principal_id::text END,
 q.model_profile_id::text,q.manual_order,q.state,coalesce(q.queue_security_review_required,false),q.fields,
 (q.queue_position=q.first_ready_position)
 FROM queue_heads q JOIN principals p ON p.tenant_id=q.tenant_id AND p.id=q.queue_by_principal_id
 WHERE ($1::uuid[] IS NULL OR q.queue_node_id=ANY($1::uuid[]))`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*Queued{}
	for rows.Next() {
		var id, state string
		var fields []byte
		var head *bool
		q := &Queued{}
		if err := rows.Scan(&id, &q.Position, &q.By.ID, &q.By.Name, &q.By.Kind, &q.At, &q.RunID, &q.TargetAgentID, &q.ExpectedAgentID, &q.ProfileID, &q.Manual, &state, &q.SecurityReview, &fields, &head); err != nil {
			return nil, err
		}
		q.Targeted = q.TargetAgentID != nil
		q.Waiting = head != nil && *head
		if q.Waiting {
			q.WaitReason = "Autopilot is off; a coordinator takes it"
		}
		if State(state) == "blocked" {
			q.Waiting = true
			q.WaitReason = "Blocked; waits until unblocked"
			f := Fields(fields)
			for _, key := range []string{"blocker", "blocked_by"} {
				if b, ok := f[key].(string); ok && b != "" {
					q.WaitReason = "Blocked by " + b + "; waits until unblocked"
				}
			}
		}
		out[id] = q
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	for _, q := range out {
		if q.ExpectedAgentID != nil && q.Waiting && q.WaitReason != "Blocked; waits until unblocked" && !strings.HasPrefix(q.WaitReason, "Blocked by ") {
			wait, err := agentaccounts.WaitForRun(ctx, tx, q.RunID)
			if err != nil {
				return nil, err
			}
			if wait == nil {
				now := time.Now().UTC()
				q.ExpectedStart = &now
				q.Waiting = false
				q.WaitReason = ""
			} else {
				q.WaitReason = "Waiting for agent capacity: " + wait.Code
				q.ExpectedStart = wait.Until
			}
		}
	}
	return out, nil
}
