// SPDX-License-Identifier: AGPL-3.0-only
package recurrences

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/inbox"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// DispatchAgentRuns is a bounded reconciliation pass over the existing outbox.
// All work is local transactional acceptance, never a provider/model call.
func (m *Module) DispatchAgentRuns(ctx context.Context, tid string) error {
	ctx = db.AllProjects(ctx, "agent-owned routine inbox reconciliation")
	var ids []string
	err := db.InTenant(ctx, m.pool, tid, func(tx pgx.Tx) error {
		if err := db.SetLocalStatementTimeout(ctx, tx, 5*time.Second); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT run_id::text FROM routine_effect_outbox WHERE kind='routine.agent_dispatch' AND state='pending' AND reconcile_after<=clock_timestamp() ORDER BY reconcile_after,id LIMIT 20`)
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
		return err
	}
	var failures []error
	for _, id := range ids {
		if err = m.dispatchAgentRun(ctx, tid, id); err != nil {
			failures = append(failures, fmt.Errorf("agent routine %s: %w", id, err))
		}
	}
	return errors.Join(failures...)
}

func (m *Module) dispatchAgentRun(ctx context.Context, tid, id string) error {
	attemptCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	err := db.InTenant(attemptCtx, m.pool, tid, func(tx pgx.Tx) error {
		ctx := attemptCtx
		if err := db.SetLocalStatementTimeout(ctx, tx, 5*time.Second); err != nil {
			return err
		}
		if _, err := lock(ctx, tx, tid, false); err != nil {
			return err
		}
		var recurrence, key string
		var raw, assignment []byte
		if err := tx.QueryRow(ctx, `SELECT recurrence_id::text,occurrence_key,agent_owner_config,assignment FROM routine_runs WHERE id=$1 AND agent_owner_config IS NOT NULL`, id).Scan(&recurrence, &key, &raw, &assignment); err != nil {
			return err
		}
		// Keep node locks ahead of record rows. Permanent validation refusals
		// are handled only after fencing the run and its dispatch receipt.
		item, validationErr := load(ctx, tx, recurrence, false)
		reason := "routine_unavailable"
		if validationErr == nil {
			reason = "routine_target_unavailable"
			validationErr = validateTarget(ctx, tx, item.Input, true)
		}
		if validationErr != nil && !permanentAgentDispatchError(validationErr) {
			return validationErr
		}
		// Existing rows are all fenced before accepting inbox rows/audit.
		var err error
		if _, err = tx.Exec(ctx, `SELECT id FROM routine_runs WHERE id=$1 FOR NO KEY UPDATE`, id); err != nil {
			return err
		}
		var outbox, state string
		if err = tx.QueryRow(ctx, `SELECT id::text,state FROM routine_effect_outbox WHERE run_id=$1 AND effect_key='agent-dispatch' FOR NO KEY UPDATE`, id).Scan(&outbox, &state); err != nil {
			return err
		}
		if state != "pending" {
			return nil
		}
		hold := func(reason string, terminal bool) error {
			return holdAgentDispatchTx(ctx, tx, id, outbox, reason, terminal)
		}
		if validationErr != nil {
			return hold(reason, true)
		}
		var c AgentOwner
		if len(raw) > 8192 || json.Unmarshal(raw, &c) != nil || !workorders.UUID(c.OwnerPrincipalID) || !workorders.UUID(c.GrantedByPrincipalID) || c.Destination.validate() != nil {
			return hold("invalid_agent_owner", true)
		}
		current, err := loadAgentOwner(ctx, tx, item.ID)
		if err != nil {
			if permanentAgentDispatchError(err) {
				return hold("invalid_agent_owner", true)
			}
			return err
		}
		if current == nil || digestJSON(current) != digestJSON(c) {
			return hold("agent_owner_grant_changed", true)
		}
		if item.Paused && !strings.HasPrefix(key, "manual:") {
			return hold("routine_paused", false)
		}
		person, _, err := agentOwnerAuthority(ctx, tx, tid, item, c)
		if permanentAgentDispatchError(err) {
			return hold("agent_owner_authority_revoked", true)
		}
		if err != nil {
			return err
		}
		// Frozen assignment is data. Only the configured result route and current
		// ordinary scopes authorize its bounded output; no outward tool authority.
		var input Input
		if len(assignment) > 131072 || json.Unmarshal(assignment, &input) != nil {
			return hold("invalid_agent_assignment", true)
		}
		body := fmt.Sprintf("Routine run %s\nResult: POST /api/recurrences/%s/runs/%s/agent-result\nDestination: %s\n\n%s\n\n%s", id, recurrence, id, c.Destination.Kind, input.Template.Title, input.Template.Description)
		if len(body) > 65536 {
			return hold("routine_message_too_large", true)
		}
		if m.agentBudget == nil {
			return hold("budget_policy_unavailable", false)
		}
		now, err := m.clock(ctx, tx)
		if err != nil {
			return err
		}
		now = now.UTC()
		start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -(int(now.Weekday())+6)%7)
		// Refusal must retain neither a policy reservation nor partial inbox
		// rows. The outer transaction retains the receipt and access fences.
		admission, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		decision, err := m.agentBudget(ctx, admission, AgentBudgetRequest{tid, c.OwnerPrincipalID, id, start, start.AddDate(0, 0, 7)})
		if err != nil {
			if rollbackErr := admission.Rollback(ctx); rollbackErr != nil {
				return errors.Join(err, rollbackErr)
			}
			if permanentAgentDispatchError(err) {
				return hold("agent_dispatch_refused", true)
			}
			return err
		}
		if !decision.Allowed {
			if err := admission.Commit(ctx); err != nil {
				return err
			}
			reason := decision.Reason
			if reason == "" {
				reason = "owner_weekly_budget_exhausted"
			}
			if err = hold(reason, false); err != nil {
				return err
			}
			for _, change := range decision.Changes {
				if _, err = events.Append(ctx, tx, person, change); err != nil {
					return err
				}
			}
			return nil
		}
		policyKey := "routine-run/" + id
		var delegationRaw []byte
		if err = tx.QueryRow(ctx, `SELECT agent_delegation FROM routine_runs WHERE id=$1`, id).Scan(&delegationRaw); err != nil {
			return err
		}
		if len(delegationRaw) > 0 {
			var d AgentDelegation
			if json.Unmarshal(delegationRaw, &d) != nil {
				return errInvalidAssistantGrant()
			}
			policyKey = d.BudgetKey
		}
		message, changes, err := inbox.QueueAgentRoutineTx(ctx, admission, person, c.OwnerPrincipalID, item.ProjectID, id, body, policyKey)
		if err != nil {
			if rollbackErr := admission.Rollback(ctx); rollbackErr != nil {
				return errors.Join(err, rollbackErr)
			}
			if permanentAgentDispatchError(err) {
				return hold("agent_dispatch_refused", true)
			}
			return err
		}
		if err = admission.Commit(ctx); err != nil {
			return err
		}
		changes = append(changes, decision.Changes...)
		if _, err = tx.Exec(ctx, `UPDATE routine_runs SET state='created',updated_at=clock_timestamp() WHERE id=$1`, id); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE routine_effect_outbox SET state='succeeded',result=jsonb_build_object('message_id',$2::text,'reason','queued_to_owner'),updated_at=clock_timestamp() WHERE id=$1`, outbox, message); err != nil {
			return err
		}
		return inbox.AppendAgentRoutineTx(ctx, tx, person, message, changes)
	})
	if err != nil {
		// The attempt may have aborted its SQL transaction or exhausted its
		// deadline. Defer in a fresh bounded transaction, retaining the error.
		if retryErr := m.deferAgentDispatch(ctx, tid, id); retryErr != nil {
			return errors.Join(err, fmt.Errorf("defer agent dispatch: %w", retryErr))
		}
	}
	return err
}

func permanentAgentDispatchError(err error) bool {
	var refusal *workorders.Error
	return errors.Is(err, authz.ErrForbidden) || errors.Is(err, pgx.ErrNoRows) || errors.As(err, &refusal) && refusal.Status >= 400 && refusal.Status < 500
}

func holdAgentDispatchTx(ctx context.Context, tx pgx.Tx, run, outbox, reason string, terminal bool) error {
	next := "pending"
	if terminal {
		next = "cancelled"
	}
	if _, err := tx.Exec(ctx, `UPDATE routine_runs SET state='held',updated_at=clock_timestamp() WHERE id=$1`, run); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE routine_effect_outbox SET state=$2,result=jsonb_build_object('reason',$3::text),reconcile_after=clock_timestamp()+interval '1 minute',updated_at=clock_timestamp() WHERE id=$1`, outbox, next, reason)
	return err
}

func (m *Module) deferAgentDispatch(ctx context.Context, tid, id string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return db.InTenant(ctx, m.pool, tid, func(tx pgx.Tx) error {
		if err := db.SetLocalStatementTimeout(ctx, tx, 2*time.Second); err != nil {
			return err
		}
		if _, err := lock(ctx, tx, tid, false); err != nil {
			return err
		}
		var run, outbox string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM routine_runs WHERE id=$1 AND agent_owner_config IS NOT NULL FOR NO KEY UPDATE`, id).Scan(&run); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT id::text FROM routine_effect_outbox WHERE run_id=$1 AND effect_key='agent-dispatch' AND kind='routine.agent_dispatch' AND state='pending' FOR NO KEY UPDATE`, id).Scan(&outbox); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		return holdAgentDispatchTx(ctx, tx, run, outbox, "agent_dispatch_retry", false)
	})
}

func digestJSON(v any) string { raw, _ := json.Marshal(v); return digest(raw) }
