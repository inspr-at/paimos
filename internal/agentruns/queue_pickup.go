// SPDX-License-Identifier: AGPL-3.0-only
package agentruns

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/harness"
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
	if err := queueLock(r.Context(), tx); err != nil {
		return nil, err
	}
	leadProject := ""
	if session := r.Header.Get("X-Aeon-Lead-Session"); session != "" {
		if !workorders.UUID(session) {
			return nil, workorders.Fail(400, "invalid lead session")
		}
		if err := tx.QueryRow(r.Context(), `SELECT project_id::text FROM harness_sessions WHERE id=$1 AND agent_principal_id=$2`, session, p.ID).Scan(&leadProject); err != nil {
			return nil, workorders.Fail(403, "owned lead session required")
		}
	}
	if in.Agent != "" {
		if err := queueValidateTarget(r.Context(), tx, in); err != nil {
			return nil, err
		}
	}
	if leadProject != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
	}
	entries, err := m.queueEntriesFor(r.Context(), tx, leadProject, "", leadProject != "")
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
		if leadProject != "" && (e.ProjectID == nil || *e.ProjectID != leadProject) {
			continue
		}
		// queueLock holds the tenant/tree access fence. Select only work this
		// caller can dispatch; another project's lead intent is not a failure
		// of the shared scan, even if its entry is blocked or not ready.
		if leadProject == "" && e.ProjectID != nil {
			var configured bool
			if err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM project_leads WHERE project_id=$1)`, *e.ProjectID).Scan(&configured); err != nil {
				return nil, err
			}
			if configured {
				continue
			}
		}
		var leadBinding json.RawMessage
		if e.ProjectID != nil {
			if err := harness.RequireLeadDispatchTx(r.Context(), tx, r, p, *e.ProjectID, m.leadAdmission); err != nil {
				return nil, err
			}
			if r.Header.Get("X-Aeon-Lead-Session") != "" {
				var generation int64
				generation, _ = strconv.ParseInt(r.Header.Get("X-Aeon-Lead-Generation"), 10, 64)
				leadBinding, _ = json.Marshal(map[string]any{"session_id": r.Header.Get("X-Aeon-Lead-Session"), "generation": generation})
			}
		}
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
			var trace struct {
				Lead json.RawMessage `json:"project_lead"`
			}
			_ = json.Unmarshal(e.Run.Trace, &trace)
			if e.ProjectID != nil {
				if err = harness.RequireAssignedLeadTx(r.Context(), tx, p, *e.ProjectID, trace.Lead); err != nil {
					return nil, err
				}
			}
			if target.Agent != "" && target.Agent != e.Run.AgentID || target.Profile != "" && (e.Run.ProfileID == nil || target.Profile != *e.Run.ProfileID) || target.Account != nil && (e.Run.RequestedAccountID == nil || *target.Account != *e.Run.RequestedAccountID) {
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
		if leadProject != "" {
			reason, err := harness.LeadDispatchTurnTx(r.Context(), tx, p, leadProject, e.Run.ID, m.leadAdmission)
			if err != nil {
				return nil, err
			}
			if reason == "work_not_eligible" {
				continue
			}
			if reason != "" {
				return map[string]any{"entry": nil, "wait_reason": reason}, nil
			}
		}
		fields, err := json.Marshal(t.Fields)
		if err != nil {
			return nil, err
		}
		project := ""
		if t.ProjectID != nil {
			project = *t.ProjectID
		}
		candidates, rawPlacement, err := workqueue.RouteCandidatesTx(r.Context(), tx, e.Run.ID, fields, project, readiness(t).SecurityReviewRequired, target)
		if err != nil {
			return nil, err
		}
		for _, candidate := range candidates {
			picked, err := queueTryRoute(r.Context(), tx, p, t, e.Run, candidate, rawPlacement, leadBinding)
			if err != nil {
				return nil, err
			}
			if picked {
				entry, err := m.queueEntryFor(r.Context(), tx, e.NodeID, leadProject, leadProject != "")
				return map[string]any{"entry": entry}, err
			}
		}
	}
	return map[string]any{"entry": nil}, nil
}
func queueTryRoute(ctx context.Context, tx pgx.Tx, p tenant.Principal, t queueTicket, v Run, target queueTarget, placement, lead json.RawMessage) (bool, error) {
	project := ""
	if t.ProjectID != nil {
		project = *t.ProjectID
	}
	ready, err := workqueue.RouteReadyTx(ctx, tx, p.TenantID, project, v.ID, target)
	if err != nil || !ready {
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
	_, err = attempt.Exec(ctx, `UPDATE agent_runs SET agent_principal_id=$2,model_profile_id=$3,requested_model=$4,requested_account_id=$5,trace=jsonb_set(coalesce(trace,'{}'::jsonb),'{work_placement}',$6::jsonb) WHERE id=$1 AND status='queued'`, v.ID, target.Agent, target.Profile, model, target.Account, placement)
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
	if lead != nil {
		if _, err = attempt.Exec(ctx, `UPDATE agent_runs SET trace=jsonb_set(coalesce(trace,'{}'::jsonb),'{project_lead}',$2::jsonb) WHERE id=$1`, v.ID, lead); err != nil {
			return false, err
		}
	}
	_, err = attempt.Exec(ctx, `UPDATE agent_runs SET queue_routed_at=clock_timestamp() WHERE id=$1`, v.ID)
	if err != nil {
		return false, err
	}
	if lead != nil {
		if err = harness.RecordLeadTurnTx(ctx, attempt, project); err != nil {
			return false, err
		}
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
