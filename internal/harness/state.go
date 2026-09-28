// SPDX-License-Identifier: AGPL-3.0-only

package harness

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
)

// StateEvidence is shared by detail, list and live reads. Nil means unloaded,
// not false/empty (mutation responses and event snapshots omit this projection).
// Only exact session/run bindings may block a session. Legacy principal-only
// requests remain shared inbox attention; neither age nor labels imply ownership.
// All queries use the caller's tenant transaction and project visibility.
type StateEvidence struct {
	RunStatus        *string            `json:"run_status,omitempty"`
	NeedsAttention   *bool              `json:"needs_attention,omitempty"`
	HasProblem       *bool              `json:"has_problem,omitempty"`
	AttentionReasons *[]AttentionReason `json:"attention_reasons,omitempty"`
}

// AttentionReason intentionally carries no IDs, bodies, rationale or routing.
// Actor describes who must act, never claims that the viewer may take that action.
// Location names an existing authorized UI surface, not a permission grant.
type AttentionReason struct {
	Kind     string `json:"kind"`
	Scope    string `json:"scope"`
	Actor    string `json:"actor"`
	Count    int    `json:"count"`
	Blocking bool   `json:"blocking"`
	Location string `json:"location"`
}

// Resource scope matches approvals.approvalFrom. A run resource itself is an
// explicit binding when run_id is absent; a different explicit run_id wins.
const attentionApprovals = `SELECT s.id,s.project_id,s.agent_principal_id,
    coalesce(n.project_id,wn.project_id)::text AS permission_project,
    CASE WHEN coalesce(a.run_id,CASE WHEN a.resource_kind='run' THEN a.resource_id END)=s.run_id
      THEN 'run' ELSE 'shared' END AS scope
    FROM active s JOIN approval_requests a ON a.tenant_id=s.tenant_id AND a.agent_principal_id=s.agent_principal_id
    LEFT JOIN nodes n ON n.tenant_id=a.tenant_id AND n.id=a.resource_id AND a.resource_kind='node'
    LEFT JOIN agent_runs ar ON ar.tenant_id=a.tenant_id AND ar.id=a.resource_id AND a.resource_kind='run'
    LEFT JOIN nodes wn ON wn.tenant_id=ar.tenant_id AND wn.id=ar.work_order_id
    WHERE (coalesce(n.project_id,wn.project_id)=s.project_id
      OR (coalesce(n.project_id,wn.project_id) IS NULL AND (SELECT aeon_visible_all())))
    AND (a.resource_kind NOT IN ('node','run') OR n.id IS NOT NULL OR wn.id IS NOT NULL)
    AND (coalesce(a.run_id,CASE WHEN a.resource_kind='run' THEN a.resource_id END) IS NULL
      OR coalesce(a.run_id,CASE WHEN a.resource_kind='run' THEN a.resource_id END)=s.run_id)
    AND a.expires_at>now()
    AND NOT EXISTS(SELECT 1 FROM approval_decisions d WHERE d.tenant_id=a.tenant_id AND d.request_id=a.id)`

func readStateEvidence(ctx context.Context, tx pgx.Tx, ids []string) (map[string]StateEvidence, error) {
	out := make(map[string]StateEvidence, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	p, _ := tenant.PrincipalFrom(ctx)
	allowed, err := authz.ProjectsTx(ctx, tx, p)
	if err != nil {
		return nil, err
	}
	// Flags and reasons share one statement snapshot: a concurrent resolution
	// cannot leave a new false flag paired with an old pending reason.
	// Explicit sender generations isolate outgoing obligations. Incoming delivery leases
	// identify work explicitly; released leases never imply ownership. Receiving
	// a reply obligation is work in progress, not proof of a blocked worker.
	rows, err := tx.Query(ctx, `WITH active AS (SELECT tenant_id,id,project_id,agent_principal_id,run_id FROM harness_sessions WHERE id=ANY($1::uuid[]) AND stopped_at IS NULL AND archived_at IS NULL),
    approvals AS (`+attentionApprovals+`), reasons AS (
      SELECT id,project_id,agent_principal_id,'approval' AS kind,scope,'person' AS actor,
        scope='run' AS blocking,'approvals' AS location,coalesce(permission_project,'') AS permission_project
      FROM approvals
      UNION ALL
      SELECT s.id,s.project_id,s.agent_principal_id,
        CASE WHEN m.is_action_request THEN 'held_action' ELSE 'reply' END,CASE WHEN m.sender_session_id=s.id THEN 'session' ELSE 'shared' END,
        CASE WHEN m.is_action_request THEN 'person' WHEN recipient.kind IN ('agent','person') THEN recipient.kind ELSE 'unknown' END,
        false,'messages',s.project_id::text
      FROM active s JOIN inbox_compat_messages m ON m.tenant_id=s.tenant_id AND m.project_id=s.project_id AND m.sender_principal_id=s.agent_principal_id
      LEFT JOIN principals recipient ON recipient.tenant_id=m.tenant_id AND recipient.id=m.recipient_principal_id
      LEFT JOIN inbox_messages i ON i.tenant_id=m.tenant_id AND i.id=m.inbox_message_id
      WHERE (m.sender_session_id IS NULL OR m.sender_session_id=s.id) AND ((m.is_action_request AND NOT EXISTS(SELECT 1 FROM events e WHERE e.type='inbox.action_resolved'
          AND e.tenant_id=m.tenant_id AND e.node_id=m.project_id AND e.after->>'message_id'=m.id::text))
        OR (NOT m.is_action_request AND (i.expires_at IS NULL OR i.expires_at>now())
          AND EXISTS(SELECT 1 FROM inbox_reply_obligations o WHERE o.tenant_id=m.tenant_id AND o.message_id=m.id AND o.closed_at IS NULL)))
      UNION ALL
      SELECT s.id,s.project_id,s.agent_principal_id,'reply_due','session','agent',false,'messages',s.project_id::text
      FROM active s JOIN inbox_messages i ON i.tenant_id=s.tenant_id AND i.recipient_principal_id=s.agent_principal_id
        AND (i.recipient_session_id=s.id OR (i.recipient_session_id IS NULL AND EXISTS(SELECT 1 FROM harness_deliveries d WHERE d.tenant_id=s.tenant_id AND d.session_id=s.id AND d.message_id=i.id AND d.released_at IS NULL)))
      JOIN inbox_compat_messages m ON m.tenant_id=i.tenant_id AND m.inbox_message_id=i.id AND m.project_id=s.project_id
      JOIN inbox_reply_obligations o ON o.tenant_id=m.tenant_id AND o.message_id=m.id AND o.closed_at IS NULL
      WHERE (i.expires_at IS NULL OR i.expires_at>now())
    ), grouped AS (
      SELECT id,kind,scope,actor,blocking,location,permission_project,count(*) AS count
      FROM reasons GROUP BY id,kind,scope,actor,blocking,location,permission_project
    ) SELECT s.id::text,s.project_id::text,s.agent_principal_id::text,s.phase,r.status,
      (s.stopped_at IS NULL AND (coalesce(r.status='waiting',false)
        OR EXISTS(SELECT 1 FROM approvals a WHERE a.id=s.id AND a.scope='run'))),
      (coalesce(r.status IN ('failed','ownership_lost'),false)
        OR coalesce(replace(replace(s.stop_reason,'_',' '),'-',' ') ~* '\m(error|errored|failed|failure|blocked|crash(ed)?|ownership lost|heartbeat lost|timeout|timed out)\M',false)),
      coalesce(q.kind,''),coalesce(q.scope,''),coalesce(q.actor,''),coalesce(q.blocking,false),
      coalesce(q.location,''),coalesce(q.permission_project,''),coalesce(q.count,0)
    FROM harness_sessions s LEFT JOIN agent_runs r ON r.tenant_id=s.tenant_id AND r.id=s.run_id
    LEFT JOIN grouped q ON q.id=s.id
    WHERE s.id=ANY($1::uuid[])
    ORDER BY s.id,q.scope,q.kind,q.actor,q.permission_project`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, project, principal, phase, permissionProject string
		var evidence StateEvidence
		var reason AttentionReason
		if err := rows.Scan(&id, &project, &principal, &phase, &evidence.RunStatus, &evidence.NeedsAttention, &evidence.HasProblem,
			&reason.Kind, &reason.Scope, &reason.Actor, &reason.Blocking, &reason.Location, &permissionProject, &reason.Count); err != nil {
			return nil, err
		}
		if existing, found := out[id]; found {
			evidence = existing
		} else {
			if allowed("harness.read", project) {
				reasons := []AttentionReason{}
				if phase != "stopped" {
					if evidence.RunStatus != nil && *evidence.RunStatus == "waiting" {
						reasons = append(reasons, AttentionReason{"run_waiting", "run", "unknown", 1, true, "session"})
					}
					if phase == "yielded" {
						reasons = append(reasons, AttentionReason{"session_yielded", "session", "unknown", 1, true, "session"})
					}
				}
				evidence.AttentionReasons = &reasons
			}
			out[id] = evidence
		}
		if evidence.AttentionReasons == nil || reason.Count == 0 {
			continue
		}
		if reason.Location == "approvals" {
			if !allowed("approvals.read", permissionProject) || (p.Kind == tenant.Agent && p.ID != principal) {
				continue
			}
		} else if p.Kind != tenant.Person || !allowed("inbox.manage", project) {
			continue
		}
		// Combine workspace/project approval buckets only after authorization.
		merged := false
		for i := range *evidence.AttentionReasons {
			previous := &(*evidence.AttentionReasons)[i]
			if previous.Kind == reason.Kind && previous.Scope == reason.Scope && previous.Actor == reason.Actor {
				previous.Count += reason.Count
				merged = true
				break
			}
		}
		if !merged {
			*evidence.AttentionReasons = append(*evidence.AttentionReasons, reason)
		}
	}
	return out, rows.Err()
}
