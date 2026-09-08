// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package externalstage

import (
	"context"
	"database/sql"
	"fmt"
)

type prerequisiteQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func assertSealedPrerequisitesMatchActiveJanusTx(ctx context.Context, tx *sql.Tx, h handoffRow) error {
	match, err := SealedPrerequisitesMatchActiveJanus(ctx, tx, h.deliveryID, h.attemptID, h.stageKey, h.executionNumber, h.authorityEpoch)
	if err != nil {
		return err
	}
	if !match {
		return fmt.Errorf("%w: sealed prerequisites diverge from current Janus registrations", ErrConflict)
	}
	return nil
}

func SealedPrerequisitesMatchActiveJanus(ctx context.Context, q prerequisiteQuery, deliveryID, attemptID int64, stage string, execution, epoch int64) (bool, error) {
	if deliveryID == 0 || attemptID == 0 || execution == 0 || epoch == 0 {
		return true, nil
	}
	sealed, err := loadSealedPrerequisiteRefs(ctx, q, attemptID, stage, execution, epoch)
	if err != nil {
		return false, err
	}
	active, err := loadActiveJanusRefs(ctx, q, deliveryID)
	if err != nil {
		return false, err
	}
	return activeJanusCoveredBySeal(sealed, active), nil
}

type prerequisiteRef struct {
	DependencyKey  string
	RegistrationID int64
}

func loadSealedPrerequisiteRefs(ctx context.Context, q prerequisiteQuery, attemptID int64, stage string, execution, epoch int64) ([]prerequisiteRef, error) {
	rows, err := q.QueryContext(ctx, `SELECT dependency_key,registration_id FROM external_stage_prerequisites
		WHERE attempt_id=? AND stage_key=? AND execution_number=? AND authority_epoch=?
		ORDER BY ordinal`, attemptID, stage, execution, epoch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]prerequisiteRef, 0)
	for rows.Next() {
		var row prerequisiteRef
		if err := rows.Scan(&row.DependencyKey, &row.RegistrationID); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func loadActiveJanusRefs(ctx context.Context, q prerequisiteQuery, deliveryID int64) ([]prerequisiteRef, error) {
	rows, err := q.QueryContext(ctx, `SELECT COALESCE(dependency_key,''),id FROM external_stage_reporter_registrations
		WHERE delivery_id=? AND reporter_class='janus' AND reporter_role='dependency' AND revoked_at IS NULL
		ORDER BY id`, deliveryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]prerequisiteRef, 0)
	for rows.Next() {
		var row prerequisiteRef
		if err := rows.Scan(&row.DependencyKey, &row.RegistrationID); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func activeJanusCoveredBySeal(sealed, active []prerequisiteRef) bool {
	index := make(map[prerequisiteRef]struct{}, len(sealed))
	for _, row := range sealed {
		index[row] = struct{}{}
	}
	for _, row := range active {
		if _, ok := index[row]; !ok {
			return false
		}
	}
	return true
}
