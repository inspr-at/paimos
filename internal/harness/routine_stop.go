// SPDX-License-Identifier: AGPL-3.0-only
package harness

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/inspr-at/paimos/internal/ownedprocess"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// QueueRoutineStopTx consumes an exact recovery outbox intent under the caller's
// tenant/tree fence. Only a claimed routine's current managed process may get a
// cooperative stop. Unknown ownership keeps pending; no forced signal, guessed
// PID, transferred session, freed hold, or cleared launch is possible here.
func QueueRoutineStopTx(ctx context.Context, tx pgx.Tx, actor tenant.Principal, launch, outbox string) error {
	var session string
	var ownership *ownedprocess.Identity
	var heartbeat *time.Time
	var now time.Time
	var raw []byte
	var state string
	err := tx.QueryRow(ctx, `SELECT h.id::text,h.process_ownership,h.heartbeat_at,clock_timestamp(),o.result,o.state
  FROM routine_launch_intents l JOIN routine_runs r ON r.id=l.routine_run_id
  JOIN recurrences c ON c.id=r.recurrence_id
  JOIN harness_sessions h ON h.project_id=l.project_id AND h.agent_principal_id=l.worker_principal_id
   AND (l.kind='worker' AND h.run_id=l.agent_run_id OR l.kind='lead' AND h.id=aeon_uuid_or_null(l.binding#>>'{launch,session_id}'))
  JOIN routine_effect_outbox o ON o.id=$2 AND o.run_id=r.id AND o.kind='routine.stop' AND o.payload->>'launch_id'=l.id::text
  JOIN principals p ON p.id=$3 AND p.kind='agent' AND p.name='System' AND p.roles @> ARRAY['system'] AND p.status='active'
  WHERE l.id=$1 AND l.claimed_at IS NOT NULL AND l.exit_observed_at IS NULL
   AND (c.paused OR NOT coalesce((SELECT automatic_launch_enabled FROM project_routine_execution_settings WHERE project_id=l.project_id),false))
   AND h.management='managed' AND h.stopped_at IS NULL AND h.archived_at IS NULL
   AND NOT EXISTS(SELECT 1 FROM harness_sessions other WHERE other.id<>h.id
    AND other.project_id=l.project_id AND other.agent_principal_id=l.worker_principal_id
    AND (l.kind='worker' AND other.run_id=l.agent_run_id OR l.kind='lead' AND other.id=aeon_uuid_or_null(l.binding#>>'{launch,session_id}'))
    AND other.management='managed' AND other.stopped_at IS NULL AND other.archived_at IS NULL)
  ORDER BY h.id LIMIT 1 FOR NO KEY UPDATE OF h,o`, launch, outbox, actor.ID).Scan(&session, &ownership, &heartbeat, &now, &raw, &state)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if state != "pending" {
		return nil
	}
	var result struct {
		ControlID string `json:"control_id"`
	}
	if len(raw) > 65536 || len(raw) > 0 && json.Unmarshal(raw, &result) != nil {
		return errors.New("routine stop receipt invalid")
	}
	if result.ControlID != "" {
		return nil
	} // The same control is reconciled, never reissued.
	if ownership == nil || ownership.ProcessID == "" || ownership.RootPID < 1 || ownership.GroupID < 1 || ownership.StartedAt.IsZero() || heartbeat == nil || heartbeat.After(now) || now.Sub(*heartbeat) > time.Minute {
		return nil
	}
	var matches bool
	if err := tx.QueryRow(ctx, `SELECT CASE WHEN kind='lead' THEN binding#>>'{launch,grant,daemon_id}' ELSE binding->>'daemon_id' END=$2
  AND CASE WHEN kind='lead' THEN binding#>>'{launch,grant,daemon_generation}' ELSE binding->>'daemon_generation' END=$3 FROM routine_launch_intents WHERE id=$1`, launch, ownership.DaemonID, ownership.Generation).Scan(&matches); err != nil {
		return err
	}
	if !matches {
		return nil
	}
	var id string
	expires := now.Add(45 * time.Second)
	if err := tx.QueryRow(ctx, `INSERT INTO harness_controls(tenant_id,session_id,kind,sequence,requested_by_principal_id,expected_ownership,expected_generation,expires_at)
  SELECT $1,$2,'stop',coalesce(max(sequence),0)+1,$3,$4,$2::uuid,$5 FROM harness_controls WHERE session_id=$2 RETURNING id::text`, actor.TenantID, session, actor.ID, ownership, expires).Scan(&id); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE routine_effect_outbox SET result=jsonb_build_object('control_id',$2::text,'session_id',$3::text),updated_at=clock_timestamp() WHERE id=$1`, outbox, id, session)
	return err
}

// The System exception is tied to persisted recovery evidence and exact
// ownership, not a role/key grant. Ordinary person controls retain their guard.
func routineStopAuthorizedTx(ctx context.Context, tx pgx.Tx, s Session, c Control) (bool, error) {
	if c.Kind != "stop" || c.ExpectedOwnership == nil || c.ExpectedGeneration == nil || *c.ExpectedGeneration != s.ID || s.ProcessOwnership == nil || *c.ExpectedOwnership != *s.ProcessOwnership {
		return false, nil
	}
	// Personal routine history is hidden from the worker's ordinary view.
	// Broaden only this exact internal decision, then restore the caller view.
	var visibility, system string
	if err := tx.QueryRow(ctx, `SELECT coalesce(current_setting('aeon.visible_projects',true),''),coalesce(current_setting('aeon.system',true),'')`).Scan(&visibility, &system); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('aeon.visible_projects','*',true),set_config('aeon.system','on',true)`); err != nil {
		return false, err
	}
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM routine_effect_outbox o JOIN routine_launch_intents l ON l.id=aeon_uuid_or_null(o.payload->>'launch_id') AND l.routine_run_id=o.run_id
  JOIN routine_runs r ON r.id=o.run_id JOIN recurrences rc ON rc.id=r.recurrence_id
  JOIN harness_controls hc ON hc.id=$1 AND hc.session_id=$2 AND hc.kind='stop'
  JOIN principals p ON p.id=hc.requested_by_principal_id AND p.kind='agent' AND p.name='System' AND p.roles @> ARRAY['system'] AND p.status='active'
  WHERE o.kind='routine.stop' AND o.result->>'control_id'=hc.id::text AND o.result->>'session_id'=$2::text
  AND l.worker_principal_id=$3 AND l.project_id=$4 AND l.claimed_at IS NOT NULL AND l.exit_observed_at IS NULL
  AND (l.kind='worker' AND l.agent_run_id=$5::uuid OR l.kind='lead' AND l.binding#>>'{launch,session_id}'=$2::text)
  AND (rc.paused OR NOT coalesce((SELECT automatic_launch_enabled FROM project_routine_execution_settings WHERE project_id=l.project_id),false)))`, c.ID, s.ID, s.AgentPrincipalID, s.ProjectID, s.RunID).Scan(&allowed)
	if err != nil {
		return false, err // caller rolls back
	}
	_, err = tx.Exec(ctx, `SELECT set_config('aeon.visible_projects',$1,true),set_config('aeon.system',$2,true)`, visibility, system)
	return allowed, err
}
