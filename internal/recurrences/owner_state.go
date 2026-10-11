// SPDX-License-Identifier: AGPL-3.0-only
package recurrences

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// OwnerStateTx checks the live definition under the same access/tree fence as
// final effects. Runtime/capacity unavailability is not ownership revocation.
// It returns no private definition outside this internal service boundary.
func OwnerStateTx(ctx context.Context, tx pgx.Tx, tenantID, id string) (string, error) {
	if _, err := lock(ctx, tx, tenantID, false); err != nil {
		return "", err
	}
	r, err := load(ctx, tx, id, true)
	if err != nil {
		return "", err
	}
	if r.Definition == nil || r.Definition.Assignment == nil {
		return "", nil
	}
	owner := tenant.Principal{ID: r.Definition.OwnerPrincipalID, TenantID: tenantID, Kind: tenant.Person}
	err = validateDefinition(ctx, tx, owner, r.Input)
	if err == nil {
		for _, permission := range append(executionPermissions(r.Definition.Assignment), "harness.control") {
			if err = authz.RequireTx(ctx, tx, owner, permission, authz.Scope{ProjectID: r.ProjectID}); err != nil {
				break
			}
		}
	}
	var refusal *workorders.Error
	if errors.Is(err, authz.ErrForbidden) || errors.Is(err, pgx.ErrNoRows) || errors.As(err, &refusal) && refusal.Status >= 400 && refusal.Status < 500 {
		return "owner_authority_revoked", nil
	}
	if err != nil {
		return "", err
	}
	current, err := currentOutputTx(ctx, tx, r)
	if err != nil {
		return "", err
	}
	if !current {
		return "output_project_changed", nil
	}
	return "", nil
}

func currentOutputTx(ctx context.Context, tx pgx.Tx, r Recurrence) (bool, error) {
	var current bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes parent JOIN nodes project ON project.id=$2
 WHERE parent.id=$1 AND parent.project_id=$2 AND parent.deleted_at IS NULL AND parent.state<>'archived'
 AND project.deleted_at IS NULL AND project.state<>'archived')`, r.ParentID, r.ProjectID).Scan(&current)
	return current, err
}

// PauseOwnerTx revokes consent and returns the durable notification intent as
// an existing, scope-protected recurrence.paused event. The caller appends it
// LAST, after every run/stop row; a failed inbox write cannot undo this pause.
// Only enabled projects or an actually claimed routine may enter recovery.
func PauseOwnerTx(ctx context.Context, tx pgx.Tx, tenantID, id, reason string) (*events.Change, error) {
	if reason != "owner_authority_revoked" && reason != "output_project_changed" && reason != "automatic_launch_disabled" {
		return nil, workorders.Fail(400, "invalid_owner_pause_reason")
	}
	if _, err := lock(ctx, tx, tenantID, false); err != nil {
		return nil, err
	}
	r, err := load(ctx, tx, id, true)
	if err != nil || r.Definition == nil || r.Definition.Assignment == nil {
		return nil, err
	}
	var eligible, consent bool
	if err = tx.QueryRow(ctx, `SELECT d.execute_consent,
 coalesce((SELECT automatic_launch_enabled FROM project_routine_execution_settings WHERE project_id=r.project_id),false)
 OR EXISTS(SELECT 1 FROM routine_runs rr JOIN routine_launch_intents l ON l.routine_run_id=rr.id
 WHERE rr.recurrence_id=r.id AND l.claimed_at IS NOT NULL AND l.exit_observed_at IS NULL)
 FROM recurrences r JOIN recurrence_definitions d ON d.recurrence_id=r.id WHERE r.id=$1`, id).Scan(&consent, &eligible); err != nil {
		return nil, err
	}
	if !eligible || r.Paused && !consent {
		return nil, nil
	}
	if _, err = tx.Exec(ctx, `UPDATE recurrence_definitions SET execute_consent=false,consent_revision=0,
 consented_by_principal_id=NULL,consented_at=NULL,consent_policy_digest=NULL,consent_qualification_id=NULL WHERE recurrence_id=$1`, id); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE recurrences SET paused=true,revision=revision+1,updated_at=clock_timestamp() WHERE id=$1`, id); err != nil {
		return nil, err
	}
	meta, _ := json.Marshal(map[string]any{"job": Job, "recurrence_id": id, "owner_pause": true, "reason": reason})
	return &events.Change{NodeID: &r.ProjectID, Type: "recurrence.paused", Metadata: meta,
		After: map[string]any{"id": id, "revision": r.Revision + 1, "paused": true, "definition": r.Definition}}, nil
}
