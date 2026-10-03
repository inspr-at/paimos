// SPDX-License-Identifier: AGPL-3.0-only

package delivery

import (
	"context"
	"github.com/jackc/pgx/v5"
)

var ErrReleasesMode = &Conflict{"releases_mode", "This project plans with releases"}
var ErrReleaseAPI = &Conflict{"release_api", "Use the release API"}

// ReleasesMode is also used by legacy writers, after their tree/access fence.
// Only project_delivery determines the mode; job state never does.
func ReleasesMode(ctx context.Context, tx pgx.Tx, project string) (bool, error) {
	var adopted bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM project_delivery WHERE project_node_id=$1::uuid)`, project).Scan(&adopted)
	return adopted, err
}

func RequireJourney(ctx context.Context, tx pgx.Tx, project string) error {
	adopted, err := ReleasesMode(ctx, tx, project)
	if err != nil {
		return err
	}
	if adopted {
		return ErrReleasesMode
	}
	return nil
}

// RefuseReleaseNodes is the common guard for generic node mutations and undo.
// Callers authorize the target in their final fenced transaction first.
func RefuseReleaseNodes(ctx context.Context, tx pgx.Tx, ids []string) error {
	var found bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM project_releases WHERE release_node_id=ANY($1::uuid[]))`, ids).Scan(&found)
	if err != nil {
		return err
	}
	if found {
		return ErrReleaseAPI
	}
	return nil
}

// QueueAdoptionForProject inherits only an already recorded instance-local
// rollout in the same tenant. Discovery repairs tenants not yet discovered.
// This does not copy credentials or borrow authority from another tenant.
func QueueAdoptionForProject(ctx context.Context, tx pgx.Tx, project string) error {
	_, err := tx.Exec(ctx, `INSERT INTO delivery_adoption_jobs(tenant_id,project_node_id,instance_id,rollout_artifact_ref,executing_principal_id,authorizing_principal_id,rollout_authorization_ref)
	 SELECT j.tenant_id,$1::uuid,j.instance_id,j.rollout_artifact_ref,j.executing_principal_id,j.authorizing_principal_id,j.rollout_authorization_ref
	 FROM delivery_adoption_jobs j WHERE j.tenant_id=current_setting('aeon.tenant_id')::uuid
	 ORDER BY j.created_at,j.project_node_id LIMIT 1 ON CONFLICT(tenant_id,project_node_id) DO NOTHING`, project)
	return err
}
