// SPDX-License-Identifier: AGPL-3.0-only

package harness

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// StateEvidence is shared by session and live reads. The viewer derives colours
// and heartbeat warnings locally; these facts never depend on a viewer palette.
// A pending gate uses the existing approval projection. Replies use obligations,
// and held actions use their immutable resolution events. No message body or
// approval rationale leaves this projection. All reads use the caller's InTenant
// transaction and its project visibility. No mutation, event or plugin is added.
// Nil booleans mean evidence was not loaded (mutation responses and snapshots),
// not that attention or problems are absent.
type StateEvidence struct {
	RunStatus      *string `json:"run_status,omitempty"`
	NeedsAttention *bool   `json:"needs_attention,omitempty"`
	HasProblem     *bool   `json:"has_problem,omitempty"`
}

func readStateEvidence(ctx context.Context, tx pgx.Tx, ids []string) (map[string]StateEvidence, error) {
	out := make(map[string]StateEvidence, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	// Resolve approval scope through its resource, as approvals.approvalFrom
	// does. A principal's no-run proposal is not evidence for another project;
	// workspace/unassigned resources require workspace visibility.
	rows, err := tx.Query(ctx, `SELECT s.id::text,r.status,
    (s.stopped_at IS NULL AND (
      coalesce(r.status='waiting',false) OR EXISTS(SELECT 1 FROM approval_requests a
        LEFT JOIN nodes n ON n.tenant_id=a.tenant_id AND n.id=a.resource_id AND a.resource_kind='node'
        LEFT JOIN agent_runs ar ON ar.tenant_id=a.tenant_id AND ar.id=a.resource_id AND a.resource_kind='run'
        LEFT JOIN nodes wn ON wn.tenant_id=ar.tenant_id AND wn.id=ar.work_order_id
        WHERE a.tenant_id=s.tenant_id
        AND a.agent_principal_id=s.agent_principal_id
        AND (coalesce(n.project_id,wn.project_id)=s.project_id
          OR (coalesce(n.project_id,wn.project_id) IS NULL AND (SELECT aeon_visible_all())))
        AND (a.run_id IS NULL OR a.run_id=s.run_id) AND a.expires_at>now()
        AND NOT EXISTS(SELECT 1 FROM approval_decisions d WHERE d.tenant_id=a.tenant_id AND d.request_id=a.id))
      OR EXISTS(SELECT 1 FROM inbox_compat_messages m
        LEFT JOIN inbox_reply_obligations o ON o.tenant_id=m.tenant_id AND o.message_id=m.id
        WHERE m.tenant_id=s.tenant_id AND m.project_id=s.project_id AND m.sender_principal_id=s.agent_principal_id
          AND ((m.is_action_request AND NOT EXISTS(SELECT 1 FROM events e WHERE e.type='inbox.action_resolved'
                AND e.tenant_id=m.tenant_id AND e.node_id=m.project_id AND e.after->>'message_id'=m.id::text))
            OR (NOT m.is_action_request AND o.message_id IS NOT NULL AND o.closed_at IS NULL))))),
    (coalesce(r.status IN ('failed','ownership_lost'),false)
      OR coalesce(replace(replace(s.stop_reason,'_',' '),'-',' ') ~* '\m(error|errored|failed|failure|blocked|crash(ed)?|ownership lost|heartbeat lost|timeout|timed out)\M',false))
    FROM harness_sessions s LEFT JOIN agent_runs r ON r.tenant_id=s.tenant_id AND r.id=s.run_id
    WHERE s.id=ANY($1::uuid[])`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var evidence StateEvidence
		if err := rows.Scan(&id, &evidence.RunStatus, &evidence.NeedsAttention, &evidence.HasProblem); err != nil {
			return nil, err
		}
		out[id] = evidence
	}
	return out, rows.Err()
}
