// SPDX-License-Identifier: AGPL-3.0-only
package agentruns

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/modelprefs"
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
		fields, err := json.Marshal(t.Fields)
		if err != nil {
			return nil, err
		}
		f := modelprefs.PlacementFields(fields)
		role := f.RouteRole
		if role == "" {
			role = "build"
		}
		if readiness(t).SecurityReviewRequired {
			role = "build-hard"
		}
		project := ""
		if t.ProjectID != nil {
			project = *t.ProjectID
		}
		var starter *string
		if err := tx.QueryRow(r.Context(), `SELECT prefs_person_id::text FROM agent_runs WHERE id=$1::uuid`, e.Run.ID).Scan(&starter); err != nil {
			return nil, err
		}
		placement, err := modelregistry.PlacementFor(r.Context(), tx, tenant.Principal{}, modelregistry.WorkQuery{
			Role: role, TicketRole: f.RouteRole, Area: f.Area, Complexity: f.Complexity, ComplexitySource: f.ComplexitySource, TicketResidency: f.Residency, ProjectID: project, PersonID: starter}, time.Now().UTC())
		if err != nil {
			return nil, err
		}
		rawPlacement, err := placement.JSON()
		if err != nil {
			return nil, err
		}
		if target.Profile == "" {
			if placement.PlannedProfileID == nil {
				continue
			}
			target.Profile = *placement.PlannedProfileID
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
			picked, err := queueTryRoute(r.Context(), tx, p, t, e.Run, candidate, rawPlacement, leadBinding)
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
func queueTryRoute(ctx context.Context, tx pgx.Tx, p tenant.Principal, t queueTicket, v Run, target queueTarget, placement, lead json.RawMessage) (bool, error) {
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
	if err = freezeHandoff(ctx, attempt, t, v, target.Agent, lead); err != nil {
		return false, err
	}
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
