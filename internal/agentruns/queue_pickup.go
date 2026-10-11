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
	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/escalation"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/httpapi"
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
		// A queue entry cannot mint a retry lineage or an unbounded cost hold.
		// Re-check already routed entries too, before the idempotent pickup return.
		if err := escalation.CheckLaunchTx(r.Context(), tx, t.ID, "", "", nil); err != nil {
			var refusal *workorders.Error
			if errors.As(err, &refusal) && refusal.Status == 409 {
				continue
			}
			return nil, err
		}
		target := in
		if e.Queued.Targeted {
			target = queueTarget{Agent: e.Run.AgentID, Account: e.Run.RequestedAccountID}
			if e.Run.ProfileID != nil {
				target.Profile = *e.Run.ProfileID
			}
		}
		var routineTrace struct {
			RunID string `json:"routine_run_id"`
		}
		if len(e.Run.Trace) > 262144 {
			return nil, workorders.Fail(409, "routine_trace_unavailable")
		}
		if err = json.Unmarshal(e.Run.Trace, &routineTrace); err != nil {
			return nil, err
		}
		routine := routineTrace.RunID
		if routine != "" {
			if m.routineQueue == nil {
				return map[string]any{"entry": nil, "wait_reason": "routine_executor_unavailable"}, nil
			}
			generation, _ := strconv.ParseInt(r.Header.Get("X-Aeon-Lead-Generation"), 10, 64)
			out, err := m.routineQueue(r.Context(), tx, p.TenantID, routine, r.Header.Get("X-Aeon-Lead-Session"), generation)
			if err != nil {
				return nil, err
			}
			if !out.Routed {
				return map[string]any{"entry": nil, "wait_reason": out.WaitReason}, nil
			}
			entry, err := m.queueEntryFor(r.Context(), tx, e.NodeID, leadProject, true)
			return map[string]any{"entry": entry}, err
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
	return queueTryRouteWith(ctx, tx, p, t, v, target, placement, lead, nil)
}

// The callback completes routine admission, reservations and the durable launch
// intent in the SAME savepoint, before the turn and audit event. Every refusal
// rolls back the provisional worker/handoff; ordinary pickup has no callback.
func queueTryRouteWith(ctx context.Context, tx pgx.Tx, p tenant.Principal, t queueTicket, v Run, target queueTarget, placement, lead json.RawMessage, finish RoutineRouteFinish) (bool, error) {
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
	var pending []events.Change
	if finish != nil {
		if pending, err = finish(ctx, attempt, v, target); err != nil {
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
	for _, change := range pending {
		if _, err = events.Append(ctx, attempt, p, change); err != nil {
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

// RoutineQueueDispatcher is installed only by the server-owned executor. The
// coordinator passes its observed lead identity; this callback independently
// checks intent, live consent and admission before sharing the route write.
type RoutineQueueDispatcher func(context.Context, pgx.Tx, string, string, string, int64) (RoutineQueueResult, error)
type RoutineQueueResult struct {
	RunID      string `json:"agent_run_id,omitempty"`
	Routed     bool   `json:"routed"`
	Replay     bool   `json:"replay"`
	WaitReason string `json:"wait_reason,omitempty"`
}
type RoutineRouteFinish func(context.Context, pgx.Tx, Run, workqueue.RouteTarget) ([]events.Change, error)

func BindRoutineQueue(boundary httpapi.Module, dispatch RoutineQueueDispatcher) error {
	m, ok := boundary.(*module)
	if !ok || dispatch == nil {
		return errors.New("routine queue dispatcher unavailable")
	}
	m.routineQueue = dispatch
	return nil
}

// RouteRoutineQueueTx is one final write, entered under tenant/tree/pairing and
// current lead authority. It shares readiness, manual/target order, fairness,
// frozen handoff and queueTryRouteWith with human/coordinator pickup. The finish
// callback supplies holds and outbox before any audit append. No launch occurs.
func RouteRoutineQueueTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, runID, role string, lead json.RawMessage, admission harness.LeadAdmission, finish RoutineRouteFinish) (RoutineQueueResult, error) {
	out := RoutineQueueResult{RunID: runID}
	if finish == nil {
		return out, workorders.Fail(409, "routine_executor_unavailable")
	}
	var bound bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM routine_runs WHERE agent_run_id=$1 AND execute_consent AND consent_revision=definition_revision)`, runID).Scan(&bound); err != nil {
		return out, err
	}
	if !bound {
		return out, workorders.Fail(409, "routine_intent_required")
	}
	v, err := load(ctx, tx, runID, false)
	if err != nil {
		return out, err
	}
	if v.QueueNodeID == nil || v.Status != "queued" {
		return out, workorders.Fail(409, "routine_queue_unavailable")
	}
	var bounded bool
	if err = tx.QueryRow(ctx, `SELECT octet_length(title)+octet_length(body)+octet_length(fields::text)<=65536 FROM nodes WHERE id=$1`, *v.QueueNodeID).Scan(&bounded); err != nil {
		return out, err
	}
	if !bounded {
		return out, workorders.Fail(409, "preparation_fields_partial")
	}
	t, err := queueLoadTicket(ctx, tx, *v.QueueNodeID, true)
	if err != nil {
		return out, err
	}
	if t.ProjectID == nil {
		return out, workorders.Fail(409, "routine_target_changed")
	}
	if err = queuePermission(ctx, tx, p, t.ProjectID, true); err != nil {
		return out, err
	}
	if err = escalation.CheckLaunchTx(ctx, tx, t.ID, "", "", nil); err != nil {
		return out, err
	}
	if v.QueueRoutedAt != nil {
		var saved struct {
			Lead json.RawMessage `json:"project_lead"`
		}
		if err = json.Unmarshal(v.Trace, &saved); err != nil {
			return out, err
		}
		if err = harness.RequireAssignedLeadTx(ctx, tx, p, *t.ProjectID, saved.Lead); err != nil {
			return out, err
		}
		out.Routed, out.Replay = true, true
		return out, nil
	}
	if !readiness(t).Ready || workqueue.State(t.State) != "open" {
		return out, workorders.Fail(409, "work_not_eligible")
	}
	if err = requireWriterFree(ctx, tx, t.ID, v.ID); err != nil {
		return out, err
	}
	wait, err := workqueue.BlockingTx(ctx, tx, t.ID)
	if err != nil {
		return out, err
	}
	if wait != "" {
		return out, workorders.Fail(409, wait)
	}
	// Load the same bounded project order as coordinator pickup. An eligible
	// earlier leaf retains its turn even if it belongs to another routine.
	m := module{}
	entries, err := m.queueEntriesFor(ctx, tx, *t.ProjectID, "", true)
	if err != nil {
		return out, err
	}
	ordered := []queueEntry{}
	for _, e := range entries {
		if e.Queued.Targeted {
			ordered = append(ordered, e)
		}
	}
	for _, e := range entries {
		if !e.Queued.Targeted {
			ordered = append(ordered, e)
		}
	}
	for _, e := range ordered {
		if e.Run.ID == v.ID {
			break
		}
		earlier, err := queueLoadTicket(ctx, tx, e.NodeID, false)
		if err != nil {
			return out, err
		}
		reason, err := workqueue.BlockingTx(ctx, tx, e.NodeID)
		if err != nil {
			return out, err
		}
		if reason == "blocker_scan_partial" {
			return out, workorders.Fail(409, reason)
		}
		if readiness(earlier).Ready && workqueue.State(earlier.State) == "open" && reason == "" {
			return out, workorders.Fail(409, "queue_order_wait")
		}
	}
	wait, err = harness.LeadDispatchTurnTx(ctx, tx, p, *t.ProjectID, v.ID, admission)
	if err != nil {
		return out, err
	}
	if wait != "" {
		return out, workorders.Fail(409, wait)
	}
	fields := make(map[string]any, len(t.Fields)+1)
	for k, x := range t.Fields {
		fields[k] = x
	}
	fields["route_role"] = role
	raw, err := json.Marshal(fields)
	if err != nil {
		return out, err
	}
	target := queueTarget{}
	if v.QueueTargetAgentID != nil {
		target.Agent = *v.QueueTargetAgentID
		if v.ProfileID != nil {
			target.Profile = *v.ProfileID
		}
		target.Account = v.RequestedAccountID
	}
	candidates, placement, err := workqueue.RouteCandidatesTx(ctx, tx, v.ID, raw, *t.ProjectID, readiness(t).SecurityReviewRequired, target)
	if err != nil {
		return out, err
	}
	if len(candidates) > 32 {
		return out, workorders.Fail(409, "routing_scan_partial")
	}
	out.WaitReason = "no_qualified_route"
	for _, candidate := range candidates {
		// An unattended wait cannot occupy a slot or advance a turn.
		host, err := agentpairing.UnattendedForPrincipal(ctx, tx, candidate.Agent)
		if err != nil {
			return out, err
		}
		if host.Status != "ready" {
			out.WaitReason = host.Reason
			continue
		}
		picked, err := queueTryRouteWith(ctx, tx, p, t, v, candidate, placement, lead, finish)
		if err != nil {
			return out, err
		}
		if picked {
			out.Routed = true
			out.WaitReason = ""
			return out, nil
		}
	}
	return out, nil
}
