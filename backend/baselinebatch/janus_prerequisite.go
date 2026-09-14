// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package baselinebatch

import (
	"context"
	"database/sql"
	"time"

	"github.com/inspr-at/paimos/backend/delivery"
	"github.com/inspr-at/paimos/backend/externalstage"
)

// Project only a current, owned, completed execution. The external-stage
// producer already requires every declared required dependency to succeed
// before accepting the owner's terminal report. Its current registration set
// must still agree with that seal; an empty or optional-only set proves nothing.
func projectJanusPrerequisite(ctx context.Context, tx *sql.Tx, stored storedBatch, snapshot delivery.Snapshot, progress *Progress, state string) error {
	progress.JanusPrerequisite = nil
	if (state != BatchActive && state != BatchCompleted) || stored.DeliveryID == nil {
		return nil
	}
	stageKey := delivery.StageDeployment
	verification := snapshotStage(snapshot, delivery.StageVerification)
	if verification.ExecutionStartEventID != nil || verification.Performed {
		stageKey = delivery.StageVerification
	}
	stage := snapshotStage(snapshot, stageKey)
	if !stage.Performed || !stage.PolicySatisfied || stage.Stale || stage.LastSemanticAt == nil {
		return nil
	}
	if _, err := time.Parse(time.RFC3339Nano, *stage.LastSemanticAt); err != nil {
		return nil
	}
	gen := currentGeneration(snapshot, stageKey)
	owner, err := loadLiveOwnerHandoff(ctx, tx, stored, gen, stageKey)
	if err != nil {
		return err
	}
	if owner.HandoffID == "" || owner.State != string(externalstage.HandoffStateSucceeded) {
		return nil
	}
	seal, err := loadSealedPrerequisiteSet(ctx, tx, stored, gen, stageKey)
	if err != nil {
		return err
	}
	if seal.SealedAt == "" || seal.DeclaredCount == 0 || seal.PlanRevision != gen.PlanRevision {
		return nil
	}
	count, err := externalstage.CurrentRequiredJanusPrerequisiteCount(ctx, tx, *stored.DeliveryID, gen.AttemptID, stageKey, gen.ExecutionNumber, gen.AuthorityEpoch)
	if err != nil {
		return err
	}
	if count > 0 {
		progress.JanusPrerequisite = &JanusPrerequisiteEvidence{
			HandoffID: owner.HandoffID, StageKey: stageKey, ObservedAt: *stage.LastSemanticAt,
		}
	}
	return nil
}
