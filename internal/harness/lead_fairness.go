// SPDX-License-Identifier: AGPL-3.0-only
package harness

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/inspr-at/paimos/internal/workqueue"
	"github.com/jackc/pgx/v5"
)

const maxLeadDemand = 200

type leadDemand struct {
	project, run string
	at           time.Time
	routed       bool
	fields       []byte
}

// Widen only the owner-scoped scheduling read, like workqueue's full-order
// operation. The authenticated lead and its live person owner were checked
// under the tenant/tree fence before entry. No foreign identities leave this
// savepoint; tenant RLS remains active and visibility is restored on all paths.
func ownerSchedule(ctx context.Context, tx pgx.Tx, fn func(context.Context, pgx.Tx) (string, error)) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	step, err := tx.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = step.Rollback(ctx) }()
	var prior string
	if err = step.QueryRow(ctx, `SELECT coalesce(current_setting('aeon.visible_projects',true),'')`).Scan(&prior); err != nil {
		return "", err
	}
	if _, err = step.Exec(ctx, `SELECT set_config('aeon.visible_projects','*',true)`); err != nil {
		return "", err
	}
	reason, err := fn(ctx, step)
	if err != nil {
		return "", err
	}
	if _, err = step.Exec(ctx, `SELECT set_config('aeon.visible_projects',$1,true)`, prior); err != nil {
		return "", err
	}
	return reason, step.Commit(ctx)
}

// Queue is the only work source. Read at most 201 small rows; overflow is an
// explicit wait, never permission based on a partial scheduling snapshot.
// Dependency blockers, incomplete criteria and changed/non-leaf work cannot
// win a turn. Preserve manual/target order inside a project at actual pickup.
func leadQueueDemand(ctx context.Context, tx pgx.Tx, owner string) ([]leadDemand, error) {
	rows, err := tx.Query(ctx, `SELECT n.project_id::text,r.id::text,r.queue_at,
 (r.queue_routed_at IS NOT NULL),CASE WHEN octet_length(n.fields::text)<=65536 THEN n.fields END
 FROM agent_runs r JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.queue_node_id
 JOIN project_leads l ON l.tenant_id=n.tenant_id AND l.project_id=n.project_id
 JOIN nodes project ON project.tenant_id=l.tenant_id AND project.id=l.project_id
 WHERE l.owner_principal_id=$1 AND project.deleted_at IS NULL AND project.state<>'archived'
 AND r.status='queued' AND n.deleted_at IS NULL AND n.state='open'
 AND (r.queue_routed_at IS NULL OR (r.trace->'project_lead'->>'session_id'=l.session_id::text AND r.trace->'project_lead'->>'generation'=l.generation::text))
 AND aeon_work_leaf(n.id) AND aeon_work_pending(n.id) IS NULL
 AND NOT EXISTS(SELECT 1 FROM node_relations rel JOIN nodes blocker ON blocker.tenant_id=rel.tenant_id AND blocker.id=rel.source_node_id
 JOIN node_kinds k ON k.tenant_id=blocker.tenant_id AND k.id=blocker.kind_id
 WHERE rel.target_node_id=n.id AND rel.type='blocks' AND blocker.deleted_at IS NULL
 AND aeon_work_status_category(blocker.state,k.field_schema) NOT IN ('done','accepted','delivered','cancelled','archived'))
 ORDER BY r.queue_at,r.id LIMIT 201`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	demands := []leadDemand{}
	count := 0
	for rows.Next() {
		var d leadDemand
		if err = rows.Scan(&d.project, &d.run, &d.at, &d.routed, &d.fields); err != nil {
			return nil, err
		}
		if d.fields == nil {
			return nil, workorders.Fail(409, "lead scheduling fields exceed bound")
		}
		count++
		if count > maxLeadDemand {
			return nil, workorders.Fail(409, "lead scheduling snapshot exceeds bound")
		}
		if workqueue.Check("work", "open", "", "", workqueue.Fields(d.fields), false).Ready {
			demands = append(demands, d)
		}
	}
	return demands, rows.Err()
}

// Effective arrival is max(oldest eligible work, last successful route).
// A successful route moves that project's next turn behind existing demand;
// one hot project cannot drain its queue while another qualified project waits.
func leadTurnTime(arrival time.Time, last *time.Time) time.Time {
	if last != nil && last.After(arrival) {
		return *last
	}
	return arrival
}

func (m *Module) leadScheduleWait(ctx context.Context, tx pgx.Tx, p tenant.Principal, l Lead, restart bool) (string, error) {
	return ownerSchedule(ctx, tx, func(ctx context.Context, step pgx.Tx) (string, error) {
		demands, err := leadQueueDemand(ctx, step, l.owner)
		if err != nil {
			return "", err
		}
		// Already routed workers have priority over a new lead generation. They
		// still require the exact assignment, confirmed yield exit and four gates.
		if restart {
			for _, d := range demands {
				if !d.routed {
					continue
				}
				other, err := loadLead(ctx, step, d.project, false)
				if err != nil {
					return "", err
				}
				if err = leadOwner(ctx, step, p, d.project, other.owner); errors.Is(err, authz.ErrForbidden) {
					continue
				} else if err != nil {
					return "", err
				}
				s := Session{ProjectID: d.project, RunID: &d.run, OwnerPrincipalID: &other.owner}
				if err = step.QueryRow(ctx, `SELECT r.agent_principal_id::text,m.harness FROM agent_runs r JOIN model_profiles m ON m.tenant_id=r.tenant_id AND m.id=r.model_profile_id WHERE r.id=$1`, d.run).Scan(&s.AgentPrincipalID, &s.Harness); errors.Is(err, pgx.ErrNoRows) {
					continue
				} else if err != nil {
					return "", err
				}
				projected, err := projectLead(ctx, step, p, other)
				if err != nil {
					return "", err
				}
				yielded, err := leadWorkerYieldConfirmedTx(ctx, step, projected)
				if err != nil {
					return "", err
				}
				if projected.State != "working" && !yielded {
					continue
				}
				wait, err := agentaccounts.WaitForRun(ctx, step, d.run)
				if err != nil {
					return "", err
				}
				if wait != nil {
					continue
				}
				reason, err := m.checkLeadAdmission(ctx, step, p, other.owner, s)
				if err != nil {
					return "", err
				}
				if reason == "" {
					return "worker_priority", nil
				}
			}
		}
		winner := ""
		var first time.Time
		seen := map[string]bool{}
		for _, d := range demands {
			if d.routed || seen[d.project] {
				continue
			}
			other, err := loadLead(ctx, step, d.project, false)
			if err != nil {
				return "", err
			}
			if d.project != l.ProjectID {
				// A paused intent or missing/stale start evidence is not an eligible
				// contender and cannot head-of-line block another project's workers.
				other, err = projectLead(ctx, step, p, other)
				if err != nil {
					return "", err
				}
				if other.State != "working" || other.SessionID == nil {
					continue
				}
				candidate, err := load(ctx, step, d.project, *other.SessionID, false)
				if err != nil {
					return "", err
				}
				// Actual pickup authenticates the dispatching agent's key and its
				// live queue-coordinator grants in its project (queuePermission).
				// Claiming and reporting need only harness.worker, so the lead's
				// authority is proven through the key bound to its generation at
				// claim or last proven pickup, with that key's ceiling, creator
				// and revocation bounds. Another key of the same principal is not
				// its dispatch credential. A lead that cannot dispatch is refused
				// there and cannot hold a turn.
				if err = authz.RequireQueueDispatcherTx(ctx, step, p.TenantID, candidate.AgentPrincipalID, other.dispatchKey, d.project); errors.Is(err, authz.ErrForbidden) {
					continue
				} else if err != nil {
					return "", err
				}
				ready, err := workqueue.RunRouteReadyTx(ctx, step, p.TenantID, d.project, d.run, d.fields)
				if err != nil {
					return "", err
				}
				if !ready {
					continue
				}
				reason, err := m.checkLeadAdmission(ctx, step, p, other.owner, candidate)
				if err != nil {
					return "", err
				}
				if reason != "" {
					continue
				}
			}
			seen[d.project] = true
			var last *time.Time
			if err = step.QueryRow(ctx, `SELECT last_turn_at FROM project_leads WHERE project_id=$1`, d.project).Scan(&last); err != nil {
				return "", err
			}
			at := leadTurnTime(d.at, last)
			if winner == "" || at.Before(first) || at.Equal(first) && d.project < winner {
				winner, first = d.project, at
			}
		}
		if winner != "" && winner != l.ProjectID {
			return "project_turn_wait", nil
		}
		return "", nil
	})
}

// leadWorkerYieldConfirmedTx is read-only. A cooperative request, archive or
// missed heartbeat never proves exit. Only worker-priority checkpoints retain
// previously bound work while an explicit restart waits through admission.
func leadWorkerYieldConfirmedTx(ctx context.Context, tx pgx.Tx, l Lead) (bool, error) {
	if l.SessionID == nil || !(l.State == "waiting_for_room" || l.State == "paused" && l.Reason == "worker_priority") {
		return false, nil
	}
	var confirmed bool
	err := tx.QueryRow(ctx, `SELECT aeon_work_session_stopped(stopped_at,stop_reason) AND coalesce(pause_record->>'reason'='worker_priority',false) AND pause_record->'handover' IS NOT NULL FROM harness_sessions WHERE id=$1`, *l.SessionID).Scan(&confirmed)
	return confirmed, err
}

// LeadDispatchTurnTx is scheduling advice under the final mutation fence,
// after RequireLeadDispatchTx proves the current session and mandatory gates.
// Idempotent already-routed assignments bypass turns, but never their fences.
func LeadDispatchTurnTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, project, run string, admission LeadAdmission) (string, error) {
	l, err := loadLead(ctx, tx, project, false)
	if err != nil || l.State == "none" {
		return "", err
	}
	m := Module{leadAdmission: admission}
	eligible, err := ownerSchedule(ctx, tx, func(ctx context.Context, step pgx.Tx) (string, error) {
		demands, err := leadQueueDemand(ctx, step, l.owner)
		if err != nil {
			return "", err
		}
		for _, d := range demands {
			if d.project == project && d.run == run {
				return "", nil
			}
		}
		return "work_not_eligible", nil
	})
	if err != nil || eligible != "" {
		return eligible, err
	}
	return m.leadScheduleWait(ctx, tx, p, l, false)
}

// RecordLeadTurnTx must run before queue.routed appends its event counter.
// The existing tenant/tree lock serializes routes across all projects.
func RecordLeadTurnTx(ctx context.Context, tx pgx.Tx, project string) error {
	_, err := tx.Exec(ctx, `UPDATE project_leads SET last_turn_at=clock_timestamp() WHERE project_id=$1`, project)
	return err
}

func (m *Module) yieldLead(r *http.Request, tx pgx.Tx, p tenant.Principal) (result any, err error) {
	r, flush := deferControlEvents(r, tx)
	defer flush(&err)
	var in struct {
		Revision   int64 `json:"expected_revision"`
		Generation int64 `json:"generation"`
	}
	if err = workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if in.Revision < 1 || in.Generation < 1 {
		return nil, workorders.Fail(400, "valid revision and generation required")
	}
	ctx, id := r.Context(), r.PathValue("projectId")
	if err = leadFence(ctx, tx, id); err != nil {
		return nil, err
	}
	if err = authz.RequireTx(ctx, tx, p, "harness.worker", authz.Scope{ProjectID: id}); err != nil {
		return nil, err
	}
	l, err := loadLead(ctx, tx, id, true)
	if err != nil {
		return nil, err
	}
	if l.Revision != in.Revision || l.Generation != in.Generation || l.SessionID == nil {
		return nil, workorders.Fail(409, "lead revision or generation conflict")
	}
	if err = leadOwner(ctx, tx, p, id, l.owner); err != nil {
		return nil, err
	}
	r.SetPathValue("sessionId", *l.SessionID)
	s, err := worker(ctx, tx, r, p)
	if err != nil {
		return nil, err
	}
	if p.Kind != tenant.Agent {
		return nil, authz.ErrForbidden
	}
	if l.State == "paused" && (l.Reason == "idle_yield" || l.Reason == "worker_priority") {
		return projectLead(ctx, tx, p, l)
	}
	if l.State != "working" {
		return nil, workorders.Fail(409, "current working lead required")
	}
	reason, err := ownerSchedule(ctx, tx, func(ctx context.Context, step pgx.Tx) (string, error) {
		demands, err := leadQueueDemand(ctx, step, l.owner)
		if err != nil {
			return "", err
		}
		useful := false
		for _, d := range demands {
			if d.project == id {
				if d.routed {
					return "worker_priority", nil
				}
				useful = true
			}
		}
		if !useful {
			return "idle_yield", nil
		}
		return "", nil
	})
	if err != nil {
		return nil, err
	}
	if reason == "" {
		reason, err = m.leadScheduleWait(ctx, tx, p, l, false)
		if err != nil {
			return nil, err
		}
		if reason == "project_turn_wait" {
			reason = "idle_yield"
		}
	}
	if reason == "" {
		return nil, workorders.Fail(409, "lead has useful current turn")
	}
	if !cooperativePause(s) {
		return nil, workorders.Fail(409, "lead checkpoint delivery unavailable")
	}
	if _, err = requestPause(ctx, tx, p, s, pauseRequest{Level: "wrap_up", Reason: reason, Note: "Checkpoint current work, publish a handover and confirm this generation stopped. No new dispatch; keep worker assignments unchanged.", DeadlineMinutes: 10}); err != nil {
		return nil, err
	}
	before := l
	l, err = scanLead(tx.QueryRow(ctx, `UPDATE project_leads SET state='paused',reason=$2,revision=revision+1,updated_at=clock_timestamp() WHERE project_id=$1 RETURNING `+leadColumns, id, reason))
	if err != nil {
		return nil, err
	}
	if err = record(ctx, tx, p, Session{ProjectID: id}, "lead_yielded", before, l); err != nil {
		return nil, err
	}
	return projectLead(ctx, tx, p, l)
}
