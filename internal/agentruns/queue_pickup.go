// SPDX-License-Identifier: AGPL-3.0-only
package agentruns

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/statusautopilot"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/inspr-at/paimos/internal/workqueue"
	"github.com/jackc/pgx/v5"
)

func (m *module) queueNext(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in queueTarget
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if err := validateQueueTarget(in, true); err != nil {
		return nil, err
	}
	if in.Agent != "" {
		if err := queueValidateTarget(r.Context(), tx, in); err != nil {
			return nil, err
		}
	}
	entries, err := m.queueEntries(r.Context(), tx)
	if err != nil {
		return nil, err
	}
	// Start now work goes before the shared line for the chosen agent.
	ordered := []queueEntry{}
	for _, e := range entries {
		if e.Queued.Targeted && (in.Agent == "" || *e.Queued.TargetAgentID == in.Agent) {
			ordered = append(ordered, e)
		}
	}
	for _, e := range entries {
		if !e.Queued.Targeted {
			ordered = append(ordered, e)
		}
	}
	for _, e := range ordered {
		t, err := queueLoadTicket(r.Context(), tx, e.NodeID, true)
		if err != nil {
			return nil, err
		}
		if workqueue.State(t.State) == "blocked" || !readiness(t).Ready {
			continue
		}
		if err = queuePermission(r.Context(), tx, p, t.ProjectID, true); err != nil {
			return nil, err
		}
		target := in
		if e.Queued.Targeted {
			target = queueTarget{Agent: e.Run.AgentID, Account: e.Run.RequestedAccountID}
			if e.Run.ProfileID != nil {
				target.Profile = *e.Run.ProfileID
			}
		}
		if e.Run.QueueRoutedAt != nil {
			if target.Agent != "" && target.Agent != e.Run.AgentID || target.Profile != "" && (e.Run.ProfileID == nil || target.Profile != *e.Run.ProfileID) {
				continue
			}
			// A route is idempotent; reservation and launch stay on the daemon path.
			wait, err := agentaccounts.WaitForRun(r.Context(), tx, e.Run.ID)
			if err != nil {
				return nil, err
			}
			if wait == nil {
				return map[string]any{"entry": e}, nil
			}
			continue
		}
		if target.Profile == "" {
			role, _ := t.Fields["route_role"].(string)
			area, _ := t.Fields["area"].(string)
			if role == "" {
				role = "build"
			}
			if area == "" {
				area = "backend"
			}
			if readiness(t).SecurityReviewRequired {
				role = "build-hard"
			}
			route, err := modelregistry.ResolveTicketRoute(r.Context(), tx, role, area, time.Now().UTC())
			if err != nil {
				return nil, err
			}
			if route == nil {
				continue
			}
			target.Profile = route.Profile.ID
		}
		candidates := []queueTarget{}
		if target.Agent != "" {
			candidates = append(candidates, target)
		} else {
			rows, err := tx.Query(r.Context(), `SELECT DISTINCT a.registered_by_principal_id::text FROM agent_accounts a JOIN model_profiles m ON m.tenant_id=a.tenant_id AND m.harness=a.harness
    WHERE m.id=$1 AND m.enabled AND a.state='available' AND a.last_probe_ok AND a.last_probe_at>clock_timestamp()-interval '2 minutes'
    AND (a.allowed_model_profile_ids IS NULL OR m.id=ANY(a.allowed_model_profile_ids)) ORDER BY 1`, target.Profile)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var agent string
				if err = rows.Scan(&agent); err != nil {
					rows.Close()
					return nil, err
				}
				candidates = append(candidates, queueTarget{Agent: agent, Profile: target.Profile})
			}
			rows.Close()
			if err = rows.Err(); err != nil {
				return nil, err
			}
		}
		for _, candidate := range candidates {
			picked, err := queueTryRoute(r.Context(), tx, p, t, e.Run, candidate)
			if err != nil {
				return nil, err
			}
			if picked {
				entry, err := m.queueEntry(r.Context(), tx, e.NodeID)
				return map[string]any{"entry": entry}, err
			}
		}
	}
	return map[string]any{"entry": nil}, nil
}
func queueTryRoute(ctx context.Context, tx pgx.Tx, p tenant.Principal, t queueTicket, v Run, target queueTarget) (bool, error) {
	// Live role check is separate from the daemon's key ceiling, which is
	// authenticated again by reserve and claim. Never mint a runtime grant.
	agent := tenant.Principal{TenantID: p.TenantID, ID: target.Agent, Kind: tenant.Agent, Scopes: []string{"run.claim"}}
	project := ""
	if t.ProjectID != nil {
		project = *t.ProjectID
	}
	if err := authz.RequireTx(ctx, tx, agent, "run.claim", authz.Scope{ProjectID: project}); err != nil {
		if errors.Is(err, authz.ErrForbidden) {
			return false, nil
		}
		return false, err
	}
	var pending bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runs r JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.queue_node_id WHERE r.id<>$1 AND r.agent_principal_id=$2 AND r.status='queued' AND r.queue_routed_at IS NOT NULL AND n.state='open' AND n.deleted_at IS NULL)`, v.ID, target.Agent).Scan(&pending)
	if err != nil || pending {
		return false, err
	}
	attempt, err := tx.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = attempt.Rollback(ctx) }()
	var model string
	err = attempt.QueryRow(ctx, `SELECT model FROM model_profiles WHERE id=$1 AND enabled`, target.Profile).Scan(&model)
	if err != nil {
		return false, err
	}
	_, err = attempt.Exec(ctx, `UPDATE agent_runs SET agent_principal_id=$2,model_profile_id=$3,requested_model=$4,requested_account_id=$5 WHERE id=$1 AND status='queued'`, v.ID, target.Agent, target.Profile, model, target.Account)
	if err != nil {
		return false, err
	}
	wait, err := agentaccounts.WaitForRun(ctx, attempt, v.ID)
	if err != nil {
		return false, err
	}
	if wait != nil {
		return false, nil
	}
	_, err = attempt.Exec(ctx, `UPDATE agent_runs SET queue_routed_at=clock_timestamp() WHERE id=$1`, v.ID)
	if err != nil {
		return false, err
	}
	if err = workorders.Record(ctx, attempt, p, t.ID, "queue.routed", v, target); err != nil {
		return false, err
	}
	return true, attempt.Commit(ctx)
}
func queueClaimable(ctx context.Context, tx pgx.Tx, v Run) error {
	if v.QueueNodeID == nil || v.Status != "queued" {
		return nil
	}
	t, err := queueLoadTicket(ctx, tx, *v.QueueNodeID, true)
	if err != nil {
		return err
	}
	if v.QueueTargetAgentID == nil && v.QueueRoutedAt == nil {
		return workorders.Fail(409, "coordinator routing required")
	}
	if workqueue.State(t.State) != "open" || !readiness(t).Ready {
		return workorders.Fail(409, "ticket is not ready for pickup")
	}
	var earlier bool
	err = tx.QueryRow(ctx, workqueue.CTE+`SELECT EXISTS(SELECT 1 FROM queue_ordered q WHERE q.state='open' AND q.id<>$1 AND
 ((q.queue_target_agent_id=$2 AND ($3::uuid IS NULL OR q.queue_position<(SELECT queue_position FROM queue_ordered WHERE id=$1)))
 OR ($3::uuid IS NULL AND q.queue_target_agent_id IS NULL AND q.agent_principal_id=$2 AND q.queue_routed_at IS NOT NULL AND q.queue_position<(SELECT queue_position FROM queue_ordered WHERE id=$1))))`, v.ID, v.AgentID, v.QueueTargetAgentID).Scan(&earlier)
	if err != nil {
		return err
	}
	if earlier {
		return workorders.Fail(409, "earlier queued work for this agent must be picked up first")
	}
	return nil
}
func queuePickup(ctx context.Context, tx pgx.Tx, p tenant.Principal, id, runID string) error {
	if err := statusautopilot.PickupTx(ctx, tx, p, id, runID); err != nil {
		return err
	}
	return workorders.Record(ctx, tx, p, id, "queue.picked_up", map[string]string{"run_id": runID}, map[string]string{"state": "in_progress", "agent_principal_id": p.ID})
}
