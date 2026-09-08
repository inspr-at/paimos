// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package releaseacceptance

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

const (
	targetUnknownNoBinding = "no deployment target is bound: neither a Pharos owner environment on this batch delivery nor a single project environment"
	targetUnknownAmbiguous = "multiple deployment targets are bound; standing policy requires one explicit target"
	targetUnknownUnbound   = "deployment target is unknown; standing policy cannot apply until an explicit deployment target is bound"
)

type resolvedTarget struct {
	Ref    string
	Reason string
}

func (s *Service) annotateDeploymentTarget(ctx context.Context, tx *sql.Tx, acc *Acceptance) {
	got := resolveDeploymentTarget(ctx, tx, acc.Release.ProjectID, acc.Release.BatchID, acc.Release.ContentDigest, acc.Release.RevisionSeal)
	acc.DeploymentTarget = got.Ref
	if got.Ref == "" {
		acc.TargetUnknownReason = got.Reason
	}
}

func bindStandingPolicyScope(ctx context.Context, tx *sql.Tx, projectID int64, req PolicyRequest) (targetRef, modelRef string, err error) {
	live := resolveDeploymentTarget(ctx, tx, projectID, 0, req.ContentDigest, req.RevisionSeal)
	requestedTarget := strings.TrimSpace(req.TargetRef)
	if live.Ref == "" {
		if requestedTarget != "" {
			return "", "", fmt.Errorf("%w: target_ref is not a bound deployment target", ErrInvalid)
		}
	} else {
		if requestedTarget != "" && requestedTarget != live.Ref {
			return "", "", fmt.Errorf("%w: target_ref does not match the bound deployment target", ErrInvalid)
		}
		requestedTarget = live.Ref
	}
	requestedModel := strings.TrimSpace(req.ModelRef)
	if requestedModel != "" && requestedModel != ModeCustomerOperated {
		return "", "", fmt.Errorf("%w: model_ref must be the customer-operated arrangement", ErrInvalid)
	}
	return requestedTarget, ModeCustomerOperated, nil
}

func resolveDeploymentTarget(ctx context.Context, tx *sql.Tx, projectID, batchID int64, contentDigest, revisionSeal string) resolvedTarget {
	envs, err := pharosOwnerEnvironments(ctx, tx, projectID, batchID, contentDigest, revisionSeal)
	if err != nil {
		return resolvedTarget{Reason: targetUnknownNoBinding}
	}
	if len(envs) > 1 {
		return resolvedTarget{Reason: targetUnknownAmbiguous}
	}
	if len(envs) == 1 && validOpaqueRef(envs[0]) {
		return resolvedTarget{Ref: envs[0]}
	}
	names, err := projectEnvironmentNames(ctx, tx, projectID)
	if err != nil {
		return resolvedTarget{Reason: targetUnknownNoBinding}
	}
	if len(names) > 1 {
		return resolvedTarget{Reason: targetUnknownAmbiguous}
	}
	if len(names) == 1 && validOpaqueRef(names[0]) {
		return resolvedTarget{Ref: names[0]}
	}
	return resolvedTarget{Reason: targetUnknownNoBinding}
}

func pharosOwnerEnvironments(ctx context.Context, tx *sql.Tx, projectID, batchID int64, contentDigest, revisionSeal string) ([]string, error) {
	query := `SELECT DISTINCT registration.environment_symbol
		FROM baseline_batch_batches batch
		JOIN external_stage_reporter_registrations registration
		  ON registration.delivery_id=batch.delivery_id
		WHERE batch.project_id=? AND batch.delivery_id IS NOT NULL
		  AND registration.revoked_at IS NULL
		  AND registration.reporter_class='pharos'
		  AND registration.reporter_role='owner'
		  AND registration.environment_symbol IS NOT NULL
		  AND registration.environment_symbol<>''`
	args := []any{projectID}
	if batchID > 0 {
		query += ` AND batch.id=?`
		args = append(args, batchID)
	} else {
		query += ` AND batch.content_digest=? AND batch.revision_seal=?`
		args = append(args, contentDigest, revisionSeal)
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	seen := map[string]struct{}{}
	for rows.Next() {
		var env string
		if err := rows.Scan(&env); err != nil {
			return nil, err
		}
		env = strings.TrimSpace(env)
		if env == "" {
			continue
		}
		if _, ok := seen[env]; ok {
			continue
		}
		seen[env] = struct{}{}
		out = append(out, env)
	}
	return out, rows.Err()
}

func projectEnvironmentNames(ctx context.Context, tx *sql.Tx, projectID int64) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT name FROM project_environments WHERE project_id=? ORDER BY sort_order, id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		name = strings.TrimSpace(name)
		if name == "" || !validOpaqueRef(name) {
			continue
		}
		out = append(out, name)
	}
	return out, rows.Err()
}
