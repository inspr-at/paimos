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
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	return db.InTenant(ctx, m.pool, tid, func(tx pgx.Tx) error {
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
		var c AgentOwner
		if len(raw) > 8192 || json.Unmarshal(raw, &c) != nil {
			return workorders.Fail(409, "invalid_agent_owner")
		}
		item, err := load(ctx, tx, recurrence, false)
		if err != nil {
			return err
		}
		if err = validateTarget(ctx, tx, item.Input, true); err != nil {
			return err
		}
		// Existing rows are all fenced before accepting inbox rows/audit.
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
			next := "pending"
			if terminal {
				next = "cancelled"
			}
			if _, err := tx.Exec(ctx, `UPDATE routine_runs SET state='held',updated_at=clock_timestamp() WHERE id=$1`, id); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `UPDATE routine_effect_outbox SET state=$2,result=jsonb_build_object('reason',$3::text),reconcile_after=clock_timestamp()+interval '1 minute',updated_at=clock_timestamp() WHERE id=$1`, outbox, next, reason)
			return err
		}
		current, err := loadAgentOwner(ctx, tx, item.ID)
		if err != nil {
			return err
		}
		if current == nil || digestJSON(current) != digestJSON(c) {
			return hold("agent_owner_grant_changed", true)
		}
		if item.Paused && !strings.HasPrefix(key, "manual:") {
			return hold("routine_paused", false)
		}
		person, _, err := agentOwnerAuthority(ctx, tx, tid, item, c)
		var refusal *workorders.Error
		if errors.Is(err, authz.ErrForbidden) || errors.Is(err, pgx.ErrNoRows) || errors.As(err, &refusal) && refusal.Status < 500 {
			return hold("agent_owner_authority_revoked", true)
		}
		if err != nil {
			return err
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
		decision, err := m.agentBudget(ctx, tx, AgentBudgetRequest{tid, c.OwnerPrincipalID, id, start, start.AddDate(0, 0, 7)})
		if err != nil {
			return err
		}
		if !decision.Allowed {
			return hold("owner_weekly_budget_exhausted", false)
		}
		// Frozen assignment is data. Only the configured result route and current
		// ordinary scopes authorize its bounded output; no outward tool authority.
		var input Input
		if len(assignment) > 131072 || json.Unmarshal(assignment, &input) != nil {
			return workorders.Fail(409, "invalid_agent_assignment")
		}
		body := fmt.Sprintf("Routine run %s\nResult: POST /api/recurrences/%s/runs/%s/agent-result\nDestination: %s\n\n%s\n\n%s", id, recurrence, id, c.Destination.Kind, input.Template.Title, input.Template.Description)
		if len(body) > 65536 {
			return hold("routine_message_too_large", true)
		}
		message, changes, err := inbox.QueueAgentRoutineTx(ctx, tx, person, c.OwnerPrincipalID, item.ProjectID, id, body)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE routine_runs SET state='created',updated_at=clock_timestamp() WHERE id=$1`, id); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE routine_effect_outbox SET state='succeeded',result=jsonb_build_object('message_id',$2::text,'reason','queued_to_owner'),updated_at=clock_timestamp() WHERE id=$1`, outbox, message); err != nil {
			return err
		}
		return inbox.AppendAgentRoutineTx(ctx, tx, person, message, changes)
	})
}
func digestJSON(v any) string { raw, _ := json.Marshal(v); return digest(raw) }
