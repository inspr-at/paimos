// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package externalstage

import (
	"context"
	"database/sql"
)

// CurrentRequiredJanusPrerequisiteCount is a read-only display helper, not a
// readiness or authorization check. Callers must independently prove the
// current owned execution succeeded under the sealed prerequisite policy.
// A changed registration set, a revoked required registration, or an empty or
// optional-only declaration cannot produce positive display evidence.
func CurrentRequiredJanusPrerequisiteCount(ctx context.Context, tx *sql.Tx, deliveryID, attemptID int64, stage string, execution, epoch int64) (int, error) {
	if deliveryID <= 0 || attemptID <= 0 || execution <= 0 || epoch <= 0 {
		return 0, nil
	}
	match, err := SealedPrerequisitesMatchActiveJanus(ctx, tx, deliveryID, attemptID, stage, execution, epoch)
	if err != nil || !match {
		return 0, err
	}
	var required, active int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(CASE
		WHEN registration.revoked_at IS NULL AND registration.delivery_id=prerequisite.delivery_id
		 AND registration.reporter_class='janus' AND registration.reporter_role='dependency'
		 AND registration.dependency_key=prerequisite.dependency_key THEN 1 ELSE 0 END),0)
		FROM external_stage_prerequisites prerequisite
		LEFT JOIN external_stage_reporter_registrations registration ON registration.id=prerequisite.registration_id
		WHERE prerequisite.delivery_id=? AND prerequisite.attempt_id=? AND prerequisite.stage_key=?
		 AND prerequisite.execution_number=? AND prerequisite.authority_epoch=? AND prerequisite.requirement='required'`,
		deliveryID, attemptID, stage, execution, epoch).Scan(&required, &active)
	if err != nil || required == 0 || required != active {
		return 0, err
	}
	return required, nil
}
