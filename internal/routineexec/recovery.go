// SPDX-License-Identifier: AGPL-3.0-only
package routineexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/inbox"
	"github.com/inspr-at/paimos/internal/recurrences"
	"github.com/inspr-at/paimos/internal/systemactor"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Cursor state is only scan fairness, never an authorization or effect receipt.
// Restart starts a new bounded scan; all mutations use durable idempotency.
// Limit the cursor cache as well as each page and transaction.
type recoveryCursors struct {
	mu              sync.Mutex
	owners, notices map[string]string
}

func (e *Executor) recoveryCursor(tid string, notice bool, next *string) string {
	e.cursors.mu.Lock()
	defer e.cursors.mu.Unlock()
	m := &e.cursors.owners
	if notice {
		m = &e.cursors.notices
	}
	if *m == nil || len(*m) >= 1024 {
		*m = map[string]string{}
	}
	if next != nil {
		(*m)[tid] = *next
	}
	return (*m)[tid]
}

// recoverTenant reconciles original operations only. It cannot launch, retry a
// provider call, free an unknown hold/slot, or assign another checkout.
func (e *Executor) recoverTenant(ctx context.Context, tid string) (Pass, error) {
	out := Pass{}
	ctx = db.AllProjects(ctx, "routine ownership and receipt recovery")
	after := e.recoveryCursor(tid, false, nil)
	var ids []string
	err := db.InTenant(ctx, e.pool, tid, func(tx pgx.Tx) error {
		if err := db.SetLocalStatementTimeout(ctx, tx, 5*time.Second); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT r.id::text FROM recurrences r JOIN recurrence_definitions d ON d.recurrence_id=r.id
   WHERE r.retired_at IS NULL AND d.assignment IS NOT NULL AND (NOT r.paused OR d.execute_consent)
   AND ($1='' OR r.id>nullif($1,'')::uuid) AND (
    coalesce((SELECT automatic_launch_enabled FROM project_routine_execution_settings WHERE project_id=r.project_id),false)
    OR EXISTS(SELECT 1 FROM routine_runs rr JOIN routine_launch_intents l ON l.routine_run_id=rr.id
     WHERE rr.recurrence_id=r.id AND l.claimed_at IS NOT NULL AND l.exit_observed_at IS NULL)) ORDER BY r.id LIMIT 33`, after)
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
	more := len(ids) > batchSize
	if more {
		ids = ids[:batchSize]
		out.Partial = true
	}
	var failures []error
	for _, id := range ids {
		out.Scanned++
		if err := e.recoverOwner(ctx, tid, id); err != nil {
			failures = append(failures, err)
		}
		after = id
	}
	if !more {
		after = ""
	}
	e.recoveryCursor(tid, false, &after)
	ids = nil
	err = db.InTenant(ctx, e.pool, tid, func(tx pgx.Tx) error {
		if err := db.SetLocalStatementTimeout(ctx, tx, 5*time.Second); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT r.id::text FROM routine_runs r LEFT JOIN routine_effect_outbox o ON o.run_id=r.id AND o.effect_key='recovery'
   WHERE (r.state NOT IN ('done','completed','succeeded','failed','skipped','cancelled')
    OR EXISTS(SELECT 1 FROM routine_launch_intents l WHERE l.routine_run_id=r.id AND l.claimed_at IS NOT NULL AND l.exit_observed_at IS NULL)
    OR EXISTS(SELECT 1 FROM routine_budget_grants g WHERE g.run_id=r.id AND g.state='unknown'))
   AND (o.id IS NULL OR o.reconcile_after<=clock_timestamp())
   AND (coalesce((SELECT automatic_launch_enabled FROM project_routine_execution_settings WHERE project_id=r.output_project_id),false)
    OR EXISTS(SELECT 1 FROM routine_launch_intents l WHERE l.routine_run_id=r.id AND l.claimed_at IS NOT NULL AND l.exit_observed_at IS NULL)
    OR EXISTS(SELECT 1 FROM routine_effect_outbox n WHERE n.run_id=r.id AND n.kind='routine.stop' AND n.state='pending'))
   ORDER BY coalesce(o.reconcile_after,r.created_at),r.id LIMIT 33`)
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
		failures = append(failures, err)
	} else {
		if len(ids) > batchSize {
			out.Partial = true
			ids = ids[:batchSize]
		}
		for _, id := range ids {
			out.Scanned++
			if err := e.recoverRun(ctx, tid, id); err != nil {
				failures = append(failures, err)
			}
		}
	}
	notices, err := e.recoverNotices(ctx, tid)
	out.Scanned += notices.Scanned
	out.Waiting += notices.Waiting
	out.Partial = out.Partial || notices.Partial || ctx.Err() != nil
	if err != nil {
		failures = append(failures, err)
	}
	return out, errors.Join(failures...)
}

// Prepare System before the final transaction: Ensure may append its own event.
func (e *Executor) recoveryActor(ctx context.Context, tid string) (tenant.Principal, error) {
	var p tenant.Principal
	err := db.InTenant(ctx, e.pool, tid, func(tx pgx.Tx) error {
		if err := db.SetLocalStatementTimeout(ctx, tx, 5*time.Second); err != nil {
			return err
		}
		if err := db.LockTenant(ctx, tx, tid); err != nil {
			return err
		}
		var err error
		p, err = systemactor.Ensure(ctx, tx, tid)
		return err
	})
	return p, err
}
func (e *Executor) recoverOwner(ctx context.Context, tid, id string) error {
	actor, err := e.recoveryActor(ctx, tid)
	if err != nil {
		return err
	}
	return db.InTenant(ctx, e.pool, tid, func(tx pgx.Tx) error {
		if err := agentpairing.Lock(ctx, tx); err != nil {
			return err
		}
		if err := db.SetLocalStatementTimeout(ctx, tx, 5*time.Second); err != nil {
			return err
		}
		reason, err := recurrences.OwnerStateTx(ctx, tx, tid, id)
		if err != nil {
			return err
		}
		var enabled bool
		if err = tx.QueryRow(ctx, `SELECT coalesce((SELECT s.automatic_launch_enabled FROM recurrences r JOIN project_routine_execution_settings s ON s.project_id=r.project_id WHERE r.id=$1),false)`, id).Scan(&enabled); err != nil {
			return err
		}
		if !enabled {
			reason = "automatic_launch_disabled"
		}
		if reason == "" {
			return nil
		}
		change, err := recurrences.PauseOwnerTx(ctx, tx, tid, id, reason)
		if err != nil || change == nil {
			return err
		}
		_, err = events.Append(ctx, tx, actor, *change)
		return err
	})
}

type recoveryResult struct {
	Reason          string `json:"wait_reason"`
	Reconciliations int    `json:"reconciliations"`
	Escalated       bool   `json:"escalated"`
}

func (e *Executor) recoverRun(ctx context.Context, tid, id string) error {
	actor, err := e.recoveryActor(ctx, tid)
	if err != nil {
		return err
	}
	return db.InTenant(ctx, e.pool, tid, func(tx pgx.Tx) error {
		if err := agentpairing.Lock(ctx, tx); err != nil {
			return err
		}
		if err := db.SetLocalStatementTimeout(ctx, tx, 5*time.Second); err != nil {
			return err
		}
		r, err := loadIntent(ctx, tx, id)
		if err != nil {
			return err
		}
		// Bound evidence before joins, decoding or updates; overflow remains held.
		for _, query := range []string{
			`SELECT count(*) FROM (SELECT 1 FROM routine_launch_intents WHERE routine_run_id=$1 LIMIT 257) x`,
			`SELECT count(*) FROM (SELECT 1 FROM routine_actions WHERE run_id=$1 LIMIT 257) x`,
			`SELECT count(*) FROM (SELECT 1 FROM routine_effect_outbox WHERE run_id=$1 LIMIT 257) x`,
			`SELECT count(*) FROM (SELECT 1 FROM routine_budget_grants WHERE run_id=$1 LIMIT 257) x`,
		} {
			var n int
			if err = tx.QueryRow(ctx, query, id).Scan(&n); err != nil {
				return err
			}
			if n > 256 {
				return errors.New("routine recovery evidence exceeds bound")
			}
		}
		reason, err := recurrences.OwnerStateTx(ctx, tx, tid, r.recurrence)
		if err != nil {
			return err
		}
		var outputCurrent bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM routine_runs rr JOIN nodes n ON n.id=rr.work_node_id
   JOIN nodes parent ON parent.id=rr.output_parent_id JOIN recurrences c ON c.id=rr.recurrence_id
   WHERE rr.id=$1 AND n.project_id=rr.output_project_id AND n.deleted_at IS NULL AND n.state<>'archived'
    AND parent.project_id=rr.output_project_id AND parent.deleted_at IS NULL AND parent.state<>'archived'
    AND c.project_id=rr.output_project_id AND c.parent_id=rr.output_parent_id)`, id).Scan(&outputCurrent); err != nil {
			return err
		}
		if !outputCurrent {
			reason = "output_project_changed"
		}
		var enabled, paused bool
		if err = tx.QueryRow(ctx, `SELECT coalesce((SELECT automatic_launch_enabled FROM project_routine_execution_settings WHERE project_id=$1),false),paused FROM recurrences WHERE id=$2`, r.project, r.recurrence).Scan(&enabled, &paused); err != nil {
			return err
		}
		if !enabled {
			var claimed bool
			if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM routine_launch_intents WHERE routine_run_id=$1 AND claimed_at IS NOT NULL)`, id).Scan(&claimed); err != nil {
				return err
			}
			if !claimed {
				return nil
			}
			reason = "automatic_launch_disabled"
		}
		if paused && reason == "" {
			reason = "routine_paused"
		}
		if reason != "" {
			if _, err = tx.Exec(ctx, `UPDATE routine_runs SET state='paused',updated_at=clock_timestamp() WHERE id=$1`, id); err != nil {
				return err
			}
			// Never clear an intent/hold. One stop intent per original launch survives
			// unknown PID and missing ownership, and is reconsidered after evidence.
			if _, err = tx.Exec(ctx, `INSERT INTO routine_effect_outbox(tenant_id,run_id,effect_key,kind,payload)
    SELECT tenant_id,routine_run_id,'stop:'||id::text,'routine.stop',jsonb_build_object('launch_id',id::text)
    FROM routine_launch_intents WHERE routine_run_id=$1 AND claimed_at IS NOT NULL AND exit_observed_at IS NULL
    ON CONFLICT(tenant_id,run_id,effect_key) DO NOTHING`, id); err != nil {
				return err
			}
		}
		// Keep the owner pause event for last: later stop/effect FK locks must
		// precede the event counter, including when only this occurrence moved.
		var change *events.Change
		if reason == "output_project_changed" || reason == "owner_authority_revoked" || reason == "automatic_launch_disabled" {
			change, err = recurrences.PauseOwnerTx(ctx, tx, tid, r.recurrence, reason)
			if err != nil {
				return err
			}
		}
		if _, err = tx.Exec(ctx, `UPDATE routine_effect_outbox o SET state='succeeded',result=coalesce(o.result,'{}')||'{"exit_observed":true}'::jsonb,updated_at=clock_timestamp()
 FROM routine_launch_intents l WHERE o.run_id=$1 AND o.kind='routine.stop' AND o.payload->>'launch_id'=l.id::text AND l.routine_run_id=o.run_id AND l.exit_observed_at IS NOT NULL AND o.state='pending'`, id); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id::text,payload->>'launch_id' FROM routine_effect_outbox WHERE run_id=$1 AND kind='routine.stop' AND state='pending' ORDER BY id LIMIT 257`, id)
		if err != nil {
			return err
		}
		type stop struct{ id, launch string }
		var stops []stop
		for rows.Next() {
			var v stop
			if err = rows.Scan(&v.id, &v.launch); err != nil {
				rows.Close()
				return err
			}
			stops = append(stops, v)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(stops) > 256 {
			return errors.New("routine stop evidence exceeds bound")
		}
		for _, v := range stops {
			if err = harness.QueueRoutineStopTx(ctx, tx, actor, v.launch, v.id); err != nil {
				return err
			}
		}
		// Internal actions and their effect receipts commit atomically. Only that
		// recorded success can repair a pending effect; an external unknown cannot.
		if _, err = tx.Exec(ctx, `UPDATE routine_effect_outbox o SET state='succeeded',result=a.result,updated_at=clock_timestamp()
   FROM routine_actions a WHERE o.run_id=$1 AND o.action_id=a.id AND a.run_id=o.run_id AND a.state='succeeded' AND o.state='pending'`, id); err != nil {
			return err
		}
		var unknown bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM routine_budget_grants WHERE run_id=$1 AND state='unknown')
   OR EXISTS(SELECT 1 FROM routine_launch_intents l JOIN agent_runs ar ON ar.id=l.agent_run_id
    WHERE l.routine_run_id=$1 AND l.exit_observed_at IS NULL AND ar.status IN ('ownership_lost','failed','completed','cancelled'))
   OR EXISTS(SELECT 1 FROM routine_launch_intents l WHERE l.routine_run_id=$1 AND l.claimed_at IS NOT NULL AND l.exit_observed_at IS NULL
    AND NOT EXISTS(SELECT 1 FROM harness_sessions h WHERE h.project_id=l.project_id AND h.agent_principal_id=l.worker_principal_id
     AND (l.kind='worker' AND h.run_id=l.agent_run_id OR l.kind='lead' AND h.id=aeon_uuid_or_null(l.binding#>>'{launch,session_id}'))
     AND h.management='managed' AND h.stopped_at IS NULL AND h.archived_at IS NULL AND h.process_ownership IS NOT NULL
     AND coalesce(h.process_ownership->>'process_id','')<>''
     AND coalesce(h.process_ownership->>'root_pid','0')<>'0' AND coalesce(h.process_ownership->>'group_id','0')<>'0'
     AND h.process_ownership->>'daemon_id'=CASE WHEN l.kind='lead' THEN l.binding#>>'{launch,grant,daemon_id}' ELSE l.binding->>'daemon_id' END
     AND h.process_ownership->>'generation'=CASE WHEN l.kind='lead' THEN l.binding#>>'{launch,grant,daemon_generation}' ELSE l.binding->>'daemon_generation' END
     AND h.heartbeat_at BETWEEN clock_timestamp()-interval '1 minute' AND clock_timestamp()))
   OR EXISTS(SELECT 1 FROM routine_budget_grants g JOIN routine_launch_intents l ON l.agent_run_id=g.agent_run_id
    WHERE g.run_id=$1 AND g.state<>'settled' AND l.exit_observed_at IS NOT NULL)
   OR EXISTS(SELECT 1 FROM routine_actions WHERE run_id=$1 AND state='unknown')
   OR EXISTS(SELECT 1 FROM routine_effect_outbox WHERE run_id=$1 AND state='unknown')`, id).Scan(&unknown); err != nil {
			return err
		}
		if unknown && reason == "" {
			reason = "process_or_usage_unconfirmed"
		}
		var prior []byte
		err = tx.QueryRow(ctx, `SELECT result FROM routine_effect_outbox WHERE run_id=$1 AND effect_key='recovery' FOR NO KEY UPDATE`, id).Scan(&prior)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		result := recoveryResult{}
		if len(prior) > 65536 || len(prior) > 0 && json.Unmarshal(prior, &result) != nil {
			return errors.New("routine recovery receipt invalid")
		}
		result.Reason = reason
		// Reconciliation uncertainty is distinct from failed work. Capacity waits
		// never increment this counter or the routine's finite actor-attempt budget.
		if unknown {
			result.Reconciliations = min(result.Reconciliations+1, 3)
		} else {
			result.Reconciliations = 0
		}
		if unknown && result.Reconciliations == 3 && !result.Escalated {
			result.Escalated = true
			if _, err = tx.Exec(ctx, `INSERT INTO routine_effect_outbox(tenant_id,run_id,effect_key,kind,payload)
    VALUES($1,$2,'recovery-notice','routine.notice',jsonb_build_object('reason','process_or_usage_unconfirmed')) ON CONFLICT DO NOTHING`, tid, id); err != nil {
				return err
			}
		}
		raw, _ := json.Marshal(result)
		_, err = tx.Exec(ctx, `INSERT INTO routine_effect_outbox(tenant_id,run_id,effect_key,kind,payload,result,reconcile_after)
   VALUES($1,$2,'recovery','routine.reconcile','{}',$3,clock_timestamp()+interval '30 seconds')
   ON CONFLICT(tenant_id,run_id,effect_key) DO UPDATE SET result=excluded.result,reconcile_after=excluded.reconcile_after,updated_at=clock_timestamp()`, tid, id, raw)
		if err == nil && change != nil {
			_, err = events.Append(ctx, tx, actor, *change)
		}
		return err
	})
}

// Pause events are the durable administrative notification outbox even before
// the first occurrence exists. Missing acceptance means pending, indefinitely;
// no private assignment content is included in administrator notices.
func (e *Executor) recoverNotices(ctx context.Context, tid string) (Pass, error) {
	out := Pass{}
	after := e.recoveryCursor(tid, true, nil)
	var ids []string
	err := db.InTenant(ctx, e.pool, tid, func(tx pgx.Tx) error {
		if err := db.SetLocalStatementTimeout(ctx, tx, 5*time.Second); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT e.id::text FROM events e WHERE e.type='recurrence.paused' AND e.metadata->>'owner_pause'='true'
   AND e.id>coalesce(nullif($1,'')::bigint,0) AND NOT EXISTS(SELECT 1 FROM inbox_messages m WHERE m.sender_principal_id=e.actor_principal_id AND m.idempotency_key='routine-pause:'||e.id::text)
   ORDER BY e.id LIMIT 33`, after)
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
	more := len(ids) > batchSize
	if more {
		out.Partial = true
		ids = ids[:batchSize]
	}
	var failures []error
	for _, id := range ids {
		out.Scanned++
		sent, err := e.notifyPause(ctx, tid, id)
		if err != nil {
			failures = append(failures, err)
		}
		if !sent {
			out.Waiting++
		}
		after = id
	}
	if !more {
		after = ""
	}
	e.recoveryCursor(tid, true, &after)
	// Unknown recovery gets one durable notice, independent of pause episodes.
	var notices []string
	err = db.InTenant(ctx, e.pool, tid, func(tx pgx.Tx) error {
		if err := db.SetLocalStatementTimeout(ctx, tx, 5*time.Second); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id::text FROM routine_effect_outbox WHERE kind='routine.notice' AND state='pending' AND reconcile_after<=clock_timestamp() ORDER BY reconcile_after,id LIMIT 33`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			notices = append(notices, id)
		}
		return rows.Err()
	})
	if err != nil {
		failures = append(failures, err)
	} else {
		if len(notices) > batchSize {
			out.Partial = true
			notices = notices[:batchSize]
		}
		for _, id := range notices {
			out.Scanned++
			if err := e.notifyRecovery(ctx, tid, id); err != nil {
				failures = append(failures, err)
			}
		}
	}
	return out, errors.Join(failures...)
}
func (e *Executor) notifyPause(ctx context.Context, tid, event string) (bool, error) {
	actor, err := e.recoveryActor(ctx, tid)
	if err != nil {
		return false, err
	}
	sent := false
	err = db.InTenant(ctx, e.pool, tid, func(tx pgx.Tx) error {
		if err := agentpairing.Lock(ctx, tx); err != nil {
			return err
		}
		if err := db.SetLocalStatementTimeout(ctx, tx, 5*time.Second); err != nil {
			return err
		}
		var project, id, reason, scope string
		if err := tx.QueryRow(ctx, `SELECT node_id::text,metadata->>'recurrence_id',metadata->>'reason',after#>>'{definition,scope,kind}' FROM events WHERE id=$1::bigint AND type='recurrence.paused' AND metadata->>'owner_pause'='true' AND actor_principal_id=$2`, event, actor.ID).Scan(&project, &id, &reason, &scope); err != nil {
			return err
		}
		var err error
		sent, err = inbox.RoutineAdministratorNoticeTx(ctx, tx, actor, project, scope, "routine-pause:"+event, fmt.Sprintf("Routine %s was paused: %s. Execution consent must be given again after a person reloads the routine. Unconfirmed processes and usage remain retained.", id, reason))
		return err
	})
	return sent && err == nil, err
}
func (e *Executor) notifyRecovery(ctx context.Context, tid, id string) error {
	actor, err := e.recoveryActor(ctx, tid)
	if err != nil {
		return err
	}
	return db.InTenant(ctx, e.pool, tid, func(tx pgx.Tx) error {
		if err := agentpairing.Lock(ctx, tx); err != nil {
			return err
		}
		if err := db.SetLocalStatementTimeout(ctx, tx, 5*time.Second); err != nil {
			return err
		}
		var project, scope, state string
		if err := tx.QueryRow(ctx, `SELECT r.output_project_id::text,r.scope_type,o.state FROM routine_effect_outbox o JOIN routine_runs r ON r.id=o.run_id WHERE o.id=$1 AND o.kind='routine.notice' FOR NO KEY UPDATE OF o`, id).Scan(&project, &scope, &state); err != nil {
			return err
		}
		if state != "pending" {
			return nil
		}
		// Retry scheduling precedes inbox's event counter. The post-acceptance
		// update touches only this already-locked scalar receipt, with no FK.
		if _, err := tx.Exec(ctx, `UPDATE routine_effect_outbox SET reconcile_after=clock_timestamp()+interval '30 seconds',result='{"wait_reason":"administrator_notice_pending"}',updated_at=clock_timestamp() WHERE id=$1`, id); err != nil {
			return err
		}
		sent, err := inbox.RoutineAdministratorNoticeTx(ctx, tx, actor, project, scope, "routine-recovery:"+id, "Routine recovery requires an administrator: the original process or usage outcome is unconfirmed. Its holds, slots and worktree remain retained; no replacement attempt is authorized.")
		if err != nil || !sent {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE routine_effect_outbox SET state='succeeded',result='{"notification":"accepted"}' WHERE id=$1`, id)
		return err
	})
}
