// SPDX-License-Identifier: AGPL-3.0-only
package routineexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/inspr-at/paimos/internal/accountuse"
	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/crossreview"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/engineadmission"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/harnesslaunch"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/questions"
	"github.com/inspr-at/paimos/internal/recurrences"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/inspr-at/paimos/internal/workqueue"
	"github.com/jackc/pgx/v5"
)

type completion struct {
	AgentRunID string                  `json:"agent_run_id"`
	Range      *reviewgate.CommitRange `json:"range,omitempty"`
}

// Both initial dispatch and continuation use the same bounded routing inputs.
func routingBounds(ctx context.Context, tx pgx.Tx) error {
	for _, bound := range []struct {
		query string
		limit int
	}{
		{`SELECT count(*) FROM (SELECT 1 FROM agent_accounts LIMIT 1025) x`, 1024},
		{`SELECT count(*) FROM (SELECT 1 FROM account_allowance_windows w WHERE w.removed_at IS NULL OR EXISTS(SELECT 1 FROM account_limit_rules l WHERE l.tenant_id=w.tenant_id AND l.from_window_id=w.id AND l.removed_at IS NULL) LIMIT 4097) x`, 4096},
	} {
		var count int
		if err := tx.QueryRow(ctx, bound.query).Scan(&count); err != nil {
			return err
		}
		if count > bound.limit {
			return workorders.Fail(409, "account_inputs_unreadable")
		}
	}
	return nil
}

// Accepted daemon telemetry only persists demand. No worker requests an
// approval, selects a reviewer, creates a retry or settles guessed usage.
func (e *Executor) recordCompletion(ctx context.Context, tx pgx.Tx, p tenant.Principal, v agentruns.Run, t agentruns.Telemetry, _ *[]events.Change) error {
	if v.RoutineLaunchID == nil || t.Kind != "finished" {
		return nil
	}
	// The telemetry endpoint already fenced the exact daemon, generation and
	// sequence. Personal ledger decisions need internal visibility, not a grant
	// exposing the owner's history to that daemon.
	return internalContinuation(ctx, tx, func() error {
		var run, role string
		if err := tx.QueryRow(ctx, `SELECT a.run_id::text,a.role FROM routine_attempts a JOIN routine_launch_intents l ON l.routine_run_id=a.run_id AND l.agent_run_id=a.agent_run_id WHERE a.agent_run_id=$1 AND l.id=$2 AND a.principal_id=$3 AND a.assignment_digest=l.binding->>'assignment_digest'`, v.ID, *v.RoutineLaunchID, p.ID).Scan(&run, &role); err != nil {
			return err
		}
		if role != "build" && role != "review" && role != "fix" {
			return nil
		}
		payload, _ := json.Marshal(completion{v.ID, t.ReviewRange})
		if _, err := tx.Exec(ctx, `INSERT INTO routine_effect_outbox(tenant_id,run_id,effect_key,kind,payload) VALUES($1,$2,$3,'routine.continue',$4) ON CONFLICT(tenant_id,run_id,effect_key) DO NOTHING`, p.TenantID, run, "finish:"+v.ID, payload); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE routine_attempts SET state=$2,process_receipt=jsonb_build_object('process_state',$3::text,'sequence',$4::bigint),updated_at=clock_timestamp() WHERE agent_run_id=$1`, v.ID, v.Status, t.ProcessState, t.Sequence)
		return err
	})
}

func internalContinuation(ctx context.Context, tx pgx.Tx, fn func() error) error {
	var visibility, system string
	if err := tx.QueryRow(ctx, `SELECT coalesce(current_setting('aeon.visible_projects',true),''),coalesce(current_setting('aeon.system',true),'')`).Scan(&visibility, &system); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('aeon.visible_projects','*',true),set_config('aeon.system','on',true)`); err != nil {
		return err
	}
	if err := fn(); err != nil {
		return err
	} // caller rolls back every error
	_, err := tx.Exec(ctx, `SELECT set_config('aeon.visible_projects',$1,true),set_config('aeon.system',$2,true)`, visibility, system)
	return err
}

func (e *Executor) continueTenant(ctx context.Context, tid string) (Pass, error) {
	out := Pass{}
	var ids []string
	err := db.InTenant(ctx, e.pool, tid, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT o.id::text FROM routine_effect_outbox o JOIN routine_runs r ON r.id=o.run_id JOIN project_routine_execution_settings s ON s.project_id=r.output_project_id WHERE s.automatic_launch_enabled AND o.kind='routine.continue' AND o.state='pending' AND o.reconcile_after<=clock_timestamp() AND r.state IN ('dispatched','starting','running') ORDER BY o.reconcile_after,o.id LIMIT 33`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	if err != nil {
		return out, err
	}
	if len(ids) > batchSize {
		out.Partial = true
		ids = ids[:batchSize]
	}
	var failures []error
	for _, id := range ids {
		out.Scanned++
		var advanced bool
		err := db.InTenant(ctx, e.pool, tid, func(tx pgx.Tx) error { var err error; advanced, err = e.ContinueTx(ctx, tx, tid, id); return err })
		if err != nil {
			failures = append(failures, err)
		} else if advanced {
			out.Dispatched++
		} else {
			out.Waiting++
		}
	}
	out.Partial = out.Partial || ctx.Err() != nil
	return out, errors.Join(failures...)
}

// ContinueTx owns review/fix advancement. Savepoint rollback retains no
// provisional reviewer, run, hold or turn. The receipt survives WAIT with a
// bounded retry delay; disabled installations remain entirely read-only.
func (e *Executor) ContinueTx(ctx context.Context, tx pgx.Tx, tid, id string) (bool, error) {
	if !workorders.UUID(tid) || !workorders.UUID(id) {
		return false, workorders.Fail(400, "invalid_continuation_binding")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := agentpairing.Lock(ctx, tx); err != nil {
		return false, err
	}
	if err := accountuse.LockShared(ctx, tx); err != nil {
		return false, err
	}
	if err := db.SetLocalStatementTimeout(ctx, tx, 5*time.Second); err != nil {
		return false, err
	}
	var same bool
	if err := tx.QueryRow(ctx, `SELECT current_setting('aeon.tenant_id')=$1`, tid).Scan(&same); err != nil {
		return false, err
	}
	if !same {
		return false, authz.ErrForbidden
	}
	advanced := false
	err := internalContinuation(ctx, tx, func() error {
		var rid, state string
		var raw []byte
		if err := tx.QueryRow(ctx, `SELECT run_id::text,state,payload FROM routine_effect_outbox WHERE id=$1 AND kind='routine.continue' FOR NO KEY UPDATE`, id).Scan(&rid, &state, &raw); err != nil {
			return err
		}
		r, err := loadIntent(ctx, tx, rid)
		if err != nil {
			return err
		}
		var enabled bool
		if err := tx.QueryRow(ctx, `SELECT coalesce((SELECT automatic_launch_enabled FROM project_routine_execution_settings WHERE project_id=$1),false)`, r.project).Scan(&enabled); err != nil {
			return err
		}
		if !enabled || state != "pending" {
			return nil
		}
		var c completion
		if len(raw) > 65536 || json.Unmarshal(raw, &c) != nil || !workorders.UUID(c.AgentRunID) || c.Range != nil && !c.Range.Valid() {
			return workorders.Fail(409, "continuation_receipt_invalid")
		}
		step, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		result, err := e.advance(ctx, step, tid, r, c)
		if err != nil {
			if rollbackErr := step.Rollback(ctx); rollbackErr != nil {
				return rollbackErr
			}
			var refusal *workorders.Error
			var pairing *agentpairing.Error
			var use *accountuse.Error
			switch {
			case errors.As(err, &refusal) && refusal.Status == 409:
				result = map[string]any{"wait_reason": refusal.Message}
			case errors.As(err, &pairing) && (pairing.Status == 409 || pairing.Status == 410):
				result = map[string]any{"wait_reason": pairing.Code}
			case errors.As(err, &use) && use.Status == 409:
				result = map[string]any{"wait_reason": use.Message}
			case errors.Is(err, authz.ErrForbidden), errors.Is(err, db.ErrKeyAuthorityChanged):
				result = map[string]any{"wait_reason": "owner_or_coordinator_authority_revoked"}
			default:
				return err
			}
		} else {
			if err := step.Commit(ctx); err != nil {
				return err
			}
			advanced = true
		}
		next := "pending"
		if advanced {
			next = "succeeded"
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE routine_effect_outbox SET state=$2,result=$3,reconcile_after=clock_timestamp()+interval '30 seconds',updated_at=clock_timestamp() WHERE id=$1`, id, next, encoded)
		return err
	})
	return advanced, err
}

// exitedActor requires both the durable exit receipt and independently settled
// root grant. A completed status, turn end, zero cost or lost response cannot
// replace either proof. Unknown evaluator/lead holds remain in the run balance.
func exitedActor(ctx context.Context, tx pgx.Tx, routine, run string) (string, string, string, string, error) {
	var role, order, agent, account string
	var valid bool
	err := tx.QueryRow(ctx, `SELECT a.role,ar.work_order_id::text,ar.agent_principal_id::text,coalesce(ar.account_id::text,''),
 ar.status IN ('completed','failed') AND ar.ended_at IS NOT NULL AND l.exit_observed_at IS NOT NULL AND l.state='exit_observed'
 AND g.state='settled' AND EXISTS(SELECT 1 FROM routine_budget_settlements s WHERE s.grant_id=g.id)
 AND EXISTS(SELECT 1 FROM routine_launch_evidence e WHERE e.launch_id=l.id AND e.kind='exit_observed')
 AND NOT EXISTS(SELECT 1 FROM harness_sessions h WHERE h.run_id=ar.id AND NOT aeon_work_session_stopped(h.stopped_at,h.stop_reason))
 FROM routine_attempts a JOIN routine_runs r ON r.id=a.run_id JOIN agent_runs ar ON ar.id=a.agent_run_id
 JOIN routine_launch_intents l ON l.id=ar.routine_launch_id AND l.agent_run_id=ar.id AND l.routine_run_id=r.id
 JOIN routine_budget_grants g ON g.run_id=r.id AND g.agent_run_id=ar.id AND g.parent_id IS NULL AND g.attempt_id=a.id
 WHERE r.id=$1 AND ar.id=$2 AND a.assignment_digest=r.assignment_digest`, routine, run).Scan(&role, &order, &agent, &account, &valid)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !valid {
		err = workorders.Fail(409, "previous_actor_exit_or_usage_unconfirmed")
	}
	return role, order, agent, account, err
}

func policyPin(ctx context.Context, tx pgx.Tx, project string) (string, error) {
	p, err := reviewgate.LoadFamilyPolicyTx(ctx, tx, &project)
	if err != nil {
		return "", err
	}
	return modelregistry.RoutinePolicyDigest(p)
}

func authorProof(ctx context.Context, tx pgx.Tx, run string) error {
	var status, evidence, model, requested, effective, h, f string
	err := tx.QueryRow(ctx, `SELECT r.status,r.model_evidence,p.model,coalesce(r.requested_model,''),coalesce(r.effective_model,''),p.harness,p.family FROM agent_runs r JOIN model_profiles p ON p.id=r.model_profile_id WHERE r.id=$1`, run).Scan(&status, &evidence, &model, &requested, &effective, &h, &f)
	if err != nil {
		return err
	}
	if status != "completed" || evidence != "vendor_reported" || requested != model || !reviewgate.ModelMatches(requested, effective) || !harnesslaunch.FamilyMatches(h, model, f) {
		return workorders.Fail(409, "author_model_proof_unavailable")
	}
	return nil
}

func (e *Executor) advance(ctx context.Context, tx pgx.Tx, tid string, r intent, c completion) (map[string]any, error) {
	if r.state != "dispatched" && r.state != "running" && r.state != "starting" {
		return nil, workorders.Fail(409, "routine_run_unavailable")
	}
	principal, err := recurrences.RequireExecutionConsentTx(ctx, tx, tid, r.recurrence, r.revision, e.options.Runtime)
	if err != nil {
		return nil, err
	}
	if !r.consent || r.consentRevision != r.revision || principal != r.principal {
		return nil, workorders.Fail(409, "routine_execution_consent_required")
	}
	owner := tenant.Principal{ID: r.owner, TenantID: tid, Kind: tenant.Person}
	for _, perm := range []string{"nodes.write", "run.create", "work_orders.write"} {
		if err := authz.RequireTx(ctx, tx, owner, perm, authz.Scope{ProjectID: r.project}); err != nil {
			return nil, err
		}
	}
	var session *string
	var generation int64
	if err := tx.QueryRow(ctx, `SELECT session_id::text,generation FROM project_leads WHERE project_id=$1 FOR NO KEY UPDATE`, r.project).Scan(&session, &generation); err != nil {
		return nil, err
	}
	if session == nil {
		return nil, workorders.Fail(409, "lead_generation_unavailable")
	}
	lead, err := harness.RequireRoutineLeadDispatchTx(ctx, tx, tid, r.id, r.project, r.owner, *session, generation, e.options.LeadAdmission)
	if err != nil {
		return nil, err
	}
	role, order, _, _, err := exitedActor(ctx, tx, r.id, c.AgentRunID)
	if err != nil {
		return nil, err
	}
	var a recurrences.Assignment
	if len(r.assignment) > 131072 || json.Unmarshal(r.assignment, &a) != nil {
		return nil, workorders.Fail(409, "assignment_unavailable")
	}
	if e.options.Reviews == nil {
		return nil, workorders.Fail(409, "coordinator_review_unavailable")
	}
	if err := routingBounds(ctx, tx); err != nil {
		return nil, err
	}
	var pending []events.Change
	var result map[string]any
	if role == "review" {
		v, err := e.options.Reviews.RoutineResultTx(ctx, tx, order)
		if err != nil {
			return nil, err
		}
		var g agentsetup.RoutineGrant
		if err := loadActorGrant(ctx, tx, c.AgentRunID, &g); err != nil {
			return nil, err
		}
		if err := e.validateContinuation(ctx, tx, g); err != nil {
			return nil, err
		}
		if err := authorProof(ctx, tx, *v.AuthorRunID); err != nil {
			return nil, err
		}
		if v.Result.Verdict == "ok" {
			// Recheck CI and dial/window before handing exact-range evidence to
			// S18. This is a pending pipeline handoff, never Done or merge consent.
			if err := e.continuationFacts(ctx, tx, owner, r, c.AgentRunID); err != nil {
				return nil, err
			}
			if _, err := tx.Exec(ctx, `UPDATE routine_runs SET state='awaiting_pipeline',updated_at=clock_timestamp() WHERE id=$1`, r.id); err != nil {
				return nil, err
			}
			return map[string]any{"state": "awaiting_pipeline", "review_order_id": order, "head_sha": v.HeadSHA}, nil
		}
		result, err = e.fix(ctx, tx, owner, r, a, *v.AuthorRunID, v, &pending)
		if err != nil {
			return nil, err
		}
	} else if role == "build" || role == "fix" {
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM agent_runs WHERE id=$1`, c.AgentRunID).Scan(&status); err != nil {
			return nil, err
		}
		if status == "failed" {
			if role != "fix" {
				return e.park(ctx, tx, owner, r, "builder_failed")
			}
			// Failed writers may have left an unverified artifact. Do not guess
			// its SHA or immediately pick it up again, even below the finite cap.
			return e.park(ctx, tx, owner, r, "failed_fix_artifact_unverified")
		} else {
			if c.Range == nil {
				if role == "build" {
					ready, err := nonCodeLandingTx(ctx, tx, r.id)
					if err != nil {
						return nil, err
					}
					if ready {
						if err := authorProof(ctx, tx, c.AgentRunID); err != nil {
							return nil, err
						}
						if err := e.continuationFacts(ctx, tx, owner, r, c.AgentRunID); err != nil {
							return nil, err
						}
						if _, err := tx.Exec(ctx, `UPDATE routine_runs SET state='awaiting_pipeline',updated_at=clock_timestamp() WHERE id=$1`, r.id); err != nil {
							return nil, err
						}
						return map[string]any{"state": "awaiting_pipeline", "non_code": true}, nil
					}
				}
				return nil, workorders.Fail(409, "completed_artifact_unavailable")
			}
			if err := authorProof(ctx, tx, c.AgentRunID); err != nil {
				return nil, err
			}
			reviewRange := *c.Range
			if role == "fix" {
				var g agentsetup.RoutineGrant
				if err := loadActorGrant(ctx, tx, c.AgentRunID, &g); err != nil {
					return nil, err
				}
				if g.Lineage == nil || g.Lineage.Role != "fix" || g.AgentRunID != c.AgentRunID || g.RoutineRunID != r.id || g.ProjectID != r.project || g.TenantID != tid {
					return nil, workorders.Fail(409, "routine_lineage_unavailable")
				}
				if err := e.validateContinuation(ctx, tx, g); err != nil {
					return nil, err
				}
				if c.Range.Repository != g.Lineage.Repository || c.Range.BaseSHA != g.Lineage.HeadSHA {
					return nil, workorders.Fail(409, "fix_artifact_lineage_changed")
				}
				// A fix starts at the rejected head, but the next review must
				// include the original build and every fix in this lineage.
				reviewRange.BaseSHA = g.Lineage.BaseSHA
			}
			pin, err := policyPin(ctx, tx, r.project)
			if err != nil {
				return nil, err
			}
			v, err := e.options.Reviews.CreateRoutineTx(ctx, tx, owner, r.id, r.work, crossreview.CreateInput{RequestID: c.AgentRunID, AuthorRunID: &c.AgentRunID, Repository: reviewRange.Repository, BaseSHA: reviewRange.BaseSHA, HeadSHA: reviewRange.HeadSHA}, &pending)
			if err != nil {
				return nil, err
			}
			if v.RunID == nil || v.ProfileID == nil {
				return nil, workorders.Fail(409, "review_route_unavailable")
			}
			var agent, account string
			if err := tx.QueryRow(ctx, `SELECT agent_principal_id::text,requested_account_id::text FROM agent_runs WHERE id=$1`, *v.RunID).Scan(&agent, &account); err != nil {
				return nil, err
			}
			l := &agentsetup.RoutineLineage{Role: "review", PreviousRunID: c.AgentRunID, ReviewOrderID: v.OrderID, Repository: v.Repository, BaseSHA: v.BaseSHA, HeadSHA: v.HeadSHA, PolicyDigest: pin}
			changes, err := e.finishActor(ctx, tx, owner, r, a, agentruns.Run{ID: *v.RunID}, workqueue.RouteTarget{Agent: agent, Profile: *v.ProfileID, Account: &account}, "review:"+c.AgentRunID, "review", l)
			if err != nil {
				return nil, err
			}
			pending = append(pending, changes...)
			result = map[string]any{"review_order_id": v.OrderID, "agent_run_id": *v.RunID}
		}
	} else {
		return nil, workorders.Fail(409, "actor_role_unavailable")
	}
	if result["state"] == "awaiting_decision" {
		return result, nil
	}
	if err := harness.RecordLeadTurnTx(ctx, tx, r.project); err != nil {
		return nil, err
	}
	for _, change := range pending {
		if _, err := events.Append(ctx, tx, lead, change); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func loadActorGrant(ctx context.Context, tx pgx.Tx, run string, g *agentsetup.RoutineGrant) error {
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT trace->'routine_launch' FROM agent_runs WHERE id=$1 AND octet_length(trace::text)<=262144`, run).Scan(&raw); err != nil {
		return err
	}
	if json.Unmarshal(raw, g) != nil || g.Validate() != nil {
		return workorders.Fail(409, "routine_lineage_unavailable")
	}
	return nil
}

func (e *Executor) continuationFacts(ctx context.Context, tx pgx.Tx, owner tenant.Principal, r intent, run string) error {
	var h string
	var estimate float64
	if err := tx.QueryRow(ctx, `SELECT p.harness,(n.fields->>'estimate_hours')::float8 FROM agent_runs ar JOIN model_profiles p ON p.id=ar.model_profile_id JOIN nodes n ON n.id=$2 WHERE ar.id=$1`, run, r.work).Scan(&h, &estimate); err != nil {
		return err
	}
	return engineadmission.RequireRoutineTx(ctx, tx, owner, r.id, run, h, estimate, e.options.Facts)
}

func (e *Executor) fix(ctx context.Context, tx pgx.Tx, owner tenant.Principal, r intent, a recurrences.Assignment, previous string, v crossreview.Review, pending *[]events.Change) (map[string]any, error) {
	limits, err := modelprefs.LoadSituationLimits(ctx, tx)
	if err != nil {
		return nil, err
	}
	policy, err := modelregistry.LoadRoutineLeadPolicyTx(ctx, tx, owner.TenantID, r.project)
	if err != nil {
		return nil, err
	}
	var fixes, actors int
	if err := tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE role='fix'),count(*) FROM routine_attempts WHERE run_id=$1`, r.id).Scan(&fixes, &actors); err != nil {
		return nil, err
	}
	if limits.FixRounds < 1 || fixes >= limits.FixRounds || policy.Effective.Recovery == nil || actors >= policy.Effective.Recovery.MaxAttempts {
		return e.park(ctx, tx, owner, r, "finite_fix_or_recovery_cap")
	}
	_, order, agent, account, err := exitedActor(ctx, tx, r.id, previous)
	if err != nil {
		return nil, err
	}
	var fields []byte
	var estimate float64
	if err := tx.QueryRow(ctx, `SELECT fields,(fields->>'estimate_hours')::float8 FROM nodes WHERE id=$1`, r.work).Scan(&fields, &estimate); err != nil {
		return nil, err
	}
	f := modelprefs.PlacementFields(fields)
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return nil, err
	}
	route, err := modelregistry.ResolveWork(ctx, tx, owner, modelregistry.WorkQuery{TicketID: r.work, Role: a.Role, Area: f.Area, Complexity: f.Complexity, ComplexitySource: f.ComplexitySource, ProjectID: r.project, PersonID: &owner.ID, TicketResidency: f.Residency, Situation: "fix", FixRound: fixes + 1, EstimateHours: estimate}, now)
	if err != nil {
		return nil, err
	}
	if route.Profile == nil {
		return nil, workorders.Fail(409, "fix_route_unavailable")
	}
	// A changed account/principal cannot inherit the previous checkout. The
	// daemon and final claim enforce exact owned-process/resource exit again.
	var compatible bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_accounts WHERE id=$1 AND registered_by_principal_id=$2 AND harness=$3 AND state='available' AND aeon_account_allows_profile(harness,allowed_model_profile_ids,$4))`, account, agent, route.Profile.Harness, route.Profile.ID).Scan(&compatible); err != nil {
		return nil, err
	}
	if !compatible {
		return nil, workorders.Fail(409, "fix_checkout_route_unavailable")
	}
	trace, err := json.Marshal(map[string]any{"routine_run_id": r.id, "work_placement": modelregistry.WorkPlacement{PreferenceTrace: route.Trace, PlannedProfileID: &route.Profile.ID}})
	if err != nil {
		return nil, err
	}
	run, err := agentruns.QueueRoutineFixTx(ctx, tx, owner, r.id, previous, route.Profile.ID, account, trace)
	if err != nil {
		return nil, err
	}
	pin, err := policyPin(ctx, tx, r.project)
	if err != nil {
		return nil, err
	}
	l := &agentsetup.RoutineLineage{Role: "fix", PreviousRunID: previous, ReviewOrderID: v.OrderID, Repository: v.Repository, BaseSHA: v.BaseSHA, HeadSHA: v.HeadSHA, PolicyDigest: pin}
	changes, err := e.finishActor(ctx, tx, owner, r, a, run, workqueue.RouteTarget{Agent: agent, Profile: route.Profile.ID, Account: &account}, fmt.Sprintf("fix:%d", fixes+1), "fix", l)
	if err != nil {
		return nil, err
	}
	brief, _ := json.Marshal(v.Result)
	if len(brief) > 20000 {
		return nil, workorders.Fail(409, "fix_findings_exceed_handover_bound")
	}
	if _, err := tx.Exec(ctx, `UPDATE agent_runs SET trace=jsonb_set(trace,'{routine_fix_findings}',$2::jsonb) WHERE id=$1`, run.ID, brief); err != nil {
		return nil, err
	}
	*pending = append(*pending, changes...)
	*pending = append(*pending, events.Change{NodeID: &order, Type: "run.created", After: run})
	return map[string]any{"agent_run_id": run.ID, "review_order_id": v.OrderID, "fix_round": fixes + 1}, nil
}

func (e *Executor) validateContinuation(ctx context.Context, tx pgx.Tx, g agentsetup.RoutineGrant) error {
	l := g.Lineage
	if l == nil {
		return workorders.Fail(409, "routine_lineage_unavailable")
	}
	pin, err := policyPin(ctx, tx, g.ProjectID)
	if err != nil {
		return err
	}
	if pin != l.PolicyDigest {
		return workorders.Fail(409, "review_policy_changed")
	}
	if _, _, _, _, err := exitedActor(ctx, tx, g.RoutineRunID, l.PreviousRunID); err != nil {
		return err
	}
	if l.Role == "review" {
		if err := authorProof(ctx, tx, l.PreviousRunID); err != nil {
			return err
		}
		var exact bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM work_order_reviews v JOIN agent_runs ar ON ar.id=v.author_run_id WHERE v.work_order_id=$1 AND v.run_id=$2 AND v.author_run_id=$3 AND v.repository=$4 AND v.base_sha=$5 AND v.head_sha=$6 AND v.github_status<>'stale' AND NOT EXISTS(SELECT 1 FROM routine_attempts a WHERE a.run_id=$7 AND a.role IN ('build','fix') AND a.created_at>(SELECT created_at FROM routine_attempts WHERE agent_run_id=ar.id)))`, l.ReviewOrderID, g.AgentRunID, l.PreviousRunID, l.Repository, l.BaseSHA, l.HeadSHA, g.RoutineRunID).Scan(&exact); err != nil {
			return err
		}
		if !exact {
			return workorders.Fail(409, "review_head_changed")
		}
		var author, reviewer string
		if err := tx.QueryRow(ctx, `SELECT author_family,coalesce(reviewer_family,'') FROM work_order_reviews WHERE work_order_id=$1`, l.ReviewOrderID).Scan(&author, &reviewer); err != nil {
			return err
		}
		policy, err := reviewgate.LoadFamilyPolicyTx(ctx, tx, &g.ProjectID)
		if err != nil {
			return err
		}
		if ok, _ := policy.Effective.Decision(author, reviewer); !ok || author == reviewer {
			return workorders.Fail(409, "review_family_policy_denied")
		}
	} else {
		if e.options.Reviews == nil {
			return workorders.Fail(409, "coordinator_review_unavailable")
		}
		v, err := e.options.Reviews.RoutineResultTx(ctx, tx, l.ReviewOrderID, g.AgentRunID)
		if err != nil {
			return err
		}
		if v.Result.Verdict != "changes" || v.Repository != l.Repository || v.BaseSHA != l.BaseSHA || v.HeadSHA != l.HeadSHA {
			return workorders.Fail(409, "fix_review_changed")
		}
	}
	return nil
}

func (e *Executor) park(ctx context.Context, tx pgx.Tx, owner tenant.Principal, r intent, reason string) (map[string]any, error) {
	// Stop before creating the question, whose ordinary service appends events.
	// After it returns only the already-locked receipt is updated by ContinueTx.
	if _, err := tx.Exec(ctx, `UPDATE routine_runs SET state='awaiting_decision',updated_at=clock_timestamp() WHERE id=$1`, r.id); err != nil {
		return nil, err
	}
	q, _, err := questions.New(nil).AskTx(ctx, tx, owner, r.project, questions.Input{RequestID: r.id, TicketID: r.work, Question: "How should this routine continue?", Context: "Routine run " + r.id + " stopped: " + reason + ". Its finite recovery budget and original lineage remain retained.", Options: []questions.Option{{ID: "park", Title: "Keep parked", Answer: "Keep this routine run parked."}, {ID: "revise", Title: "Revise scope or limits", Answer: "A person will revise the routine before another authorized run."}}, Recommend: "park", Why: "Further automatic attempts require a separate person-owned decision.", Meanwhile: "parked", MeanwhileText: "No automatic retry is queued.", BlockedNodeIDs: []string{r.work}, SuggestedOutcome: "once"})
	if err != nil {
		return nil, err
	}
	return map[string]any{"state": "awaiting_decision", "question_id": q.ID, "reason": reason}, nil
}
