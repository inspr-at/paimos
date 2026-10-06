// SPDX-License-Identifier: AGPL-3.0-only

// Package releasesettlement owns the transactional terminal release effects.
package releasesettlement

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/statusautopilot"
)

var ErrVersionConflict = errors.New("deployment artifact does not match pinned release version")

func artifactVersion(ctx context.Context, tx pgx.Tx, releaseID string) (string, string, error) {
	var scheme, version *string
	err := tx.QueryRow(ctx, `
		SELECT e.version_scheme, e.version
		FROM stage_handoff_evidence e
		JOIN stage_handoffs h ON h.tenant_id = e.tenant_id AND h.id = e.handoff_id
		WHERE h.release_node_id = $1::uuid
		  AND h.stage = 'deploy' AND h.operation = 'deploy' AND e.kind = 'deployment'
		  AND e.version_scheme IS NOT NULL AND e.version IS NOT NULL
		ORDER BY h.attempt DESC, e.sequence DESC
		LIMIT 1`, releaseID).Scan(&scheme, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	if scheme == nil || version == nil {
		return "", "", nil
	}
	return *scheme, *version, nil
}

// SettleTx pins deployment identity, supersedes prior live releases and publishes
// ticket outcomes exactly once. The caller holds the tenant tree and project
// locks before invoking it, and writes its audit event after settlement.
func SettleTx(ctx context.Context, tx pgx.Tx, projectID, releaseID string) ([]string, error) {
	var pinnedScheme, pinnedVersion *string
	if err := tx.QueryRow(ctx, `SELECT version_scheme,version FROM journey_releases WHERE project_node_id=$1::uuid AND release_node_id=$2::uuid FOR UPDATE`, projectID, releaseID).Scan(&pinnedScheme, &pinnedVersion); err != nil {
		return nil, err
	}
	scheme, version, err := artifactVersion(ctx, tx, releaseID)
	if err != nil {
		return nil, err
	}
	if (pinnedScheme == nil) != (pinnedVersion == nil) || pinnedScheme != nil && scheme != "" && (*pinnedScheme != scheme || *pinnedVersion != version) {
		return nil, ErrVersionConflict
	}
	var schemeArg, versionArg any
	if scheme != "" && version != "" {
		schemeArg, versionArg = scheme, version
	}
	tag, err := tx.Exec(ctx, `
		UPDATE journey_releases
		SET state = 'released', released_at = clock_timestamp(), revision = revision + 1,
		    version_scheme = coalesce(version_scheme, $2), version = coalesce(version, $3)
		WHERE release_node_id = $1::uuid AND state NOT IN ('released', 'superseded')`,
		releaseID, schemeArg, versionArg)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `
		UPDATE journey_releases
		SET state = 'superseded', revision = revision + 1
		WHERE project_node_id = $1::uuid AND state = 'released' AND release_node_id <> $2::uuid
		RETURNING release_node_id::text`, projectID, releaseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var tenantID string
	if err = tx.QueryRow(ctx, `SELECT current_setting('aeon.tenant_id')`).Scan(&tenantID); err != nil {
		return nil, err
	}
	if err = statusautopilot.PublishTx(ctx, tx, tenantID, releaseID); err != nil {
		return nil, err
	}
	return ids, nil
}
