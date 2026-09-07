// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package baselinebatch

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/delivery"
	"github.com/inspr-at/paimos/backend/externalstage"
)

const handoffTTL = 24 * time.Hour

type storedHandoff struct {
	StageKey        string
	HandoffID       string
	State           string
	CredentialEpoch int64
	ExecutionNumber int64
	AuthorityEpoch  int64
}

// Reconcile applies the next currently authorized step of the immutable
// reviewed plan. It is not an unconditional advance: specification was
// recorded at Start, implementation/QA require scoped evidence, and Pharos
// deployment/verification reuse RegisterReporter/ActivateOwner/
// SealPrerequisites/CreateHandoff under live session or scoped API-key
// authority. Credentials are never minted here.
func (s *Service) Reconcile(ctx context.Context, actor Actor, projectID, batchID int64) (Batch, error) {
	if projectID <= 0 || batchID <= 0 {
		return Batch{}, fmt.Errorf("%w: batch", ErrInvalid)
	}
	stored, snapshot, err := s.loadReconcileContext(ctx, actor, projectID, batchID)
	if err != nil {
		return Batch{}, err
	}
	if stored.ControlState == ControlCancelled {
		return s.GetBatch(ctx, actor, projectID, batchID)
	}
	if stored.ControlState == ControlPaused {
		return s.GetBatch(ctx, actor, projectID, batchID)
	}
	if err := s.reconcileAuthorizedSteps(ctx, actor, stored, snapshot); err != nil {
		return Batch{}, err
	}
	return s.GetBatch(ctx, actor, projectID, batchID)
}

func (s *Service) loadReconcileContext(ctx context.Context, actor Actor, projectID, batchID int64) (storedBatch, delivery.Snapshot, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return storedBatch{}, delivery.Snapshot{}, err
	}
	defer tx.Rollback()
	if _, err := s.currentAuthority(ctx, tx, actor, projectID, true); err != nil {
		return storedBatch{}, delivery.Snapshot{}, err
	}
	stored, err := loadBatchByID(ctx, tx, projectID, batchID)
	if err != nil {
		return storedBatch{}, delivery.Snapshot{}, err
	}
	if s.Delivery == nil {
		return storedBatch{}, delivery.Snapshot{}, fmt.Errorf("%w: delivery store required", ErrUnavailable)
	}
	snapshot, err := s.Delivery.SnapshotByIssueTx(ctx, tx, stored.IssueID)
	if err != nil {
		return storedBatch{}, delivery.Snapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return storedBatch{}, delivery.Snapshot{}, err
	}
	return stored, snapshot, nil
}

func (s *Service) reconcileAuthorizedSteps(ctx context.Context, actor Actor, stored storedBatch, snapshot delivery.Snapshot) error {
	if !stageSatisfied(snapshot, delivery.StageQA) {
		return nil
	}
	if stored.ExecutionMode == ModeManual {
		return nil
	}
	if stored.ExecutionMode == ModeAssisted {
		if err := requireHuman(actor); err != nil {
			return err
		}
	}
	if stored.ExecutionMode == ModeAutomatic && actor.Kind != string(auth.PrincipalSession) && actor.Kind != string(auth.PrincipalAPIKey) {
		return fmt.Errorf("%w: current editor session or scoped api key required", ErrForbidden)
	}
	if s.External == nil {
		return nil
	}
	principal, err := actor.externalPrincipal()
	if err != nil {
		return err
	}
	deliveryKey := fmt.Sprintf("issue:%d", stored.IssueID)
	registrationID, err := s.currentPharosOwnerRegistration(ctx, principal, deliveryKey)
	if err != nil {
		if errors.Is(err, externalstage.ErrNotFound) || errors.Is(err, ErrForbidden) {
			return fmt.Errorf("%w: current authorization cannot use the external-stage plane", ErrForbidden)
		}
		return err
	}
	if registrationID == 0 {
		return nil
	}
	if !stageSatisfied(snapshot, delivery.StageDeployment) {
		if err := s.ensureStageHandoff(ctx, principal, stored, snapshot, delivery.StageDeployment, registrationID, deliveryKey); err != nil {
			return err
		}
		return nil
	}
	if !stageSatisfied(snapshot, delivery.StageVerification) {
		return s.ensureStageHandoff(ctx, principal, stored, snapshot, delivery.StageVerification, registrationID, deliveryKey)
	}
	return nil
}

func (s *Service) currentPharosOwnerRegistration(ctx context.Context, principal externalstage.Principal, deliveryKey string) (int64, error) {
	listed, err := s.External.ListReporters(ctx, principal, deliveryKey)
	if err != nil {
		return 0, err
	}
	for _, row := range listed.Registrations {
		if row.ReporterClass == externalstage.ReporterClassPharos && row.ReporterRole == externalstage.ReporterRoleOwner && row.RevokedAt == "" {
			return row.RegistrationID, nil
		}
	}
	return 0, nil
}

func (s *Service) ensureStageHandoff(ctx context.Context, principal externalstage.Principal, stored storedBatch, snapshot delivery.Snapshot, stage string, registrationID int64, deliveryKey string) error {
	existing, err := loadStageHandoff(ctx, s.DB, stored, stage)
	if err != nil {
		return err
	}
	if existing.HandoffID != "" {
		return nil
	}
	stageSnap := snapshotStage(snapshot, stage)
	execution, epoch := stageSnap.ExecutionNumber, stageSnap.AuthorityEpoch
	if execution == 0 {
		expectedExecution, expectedEpoch := int64(0), int64(0)
		activation, err := s.External.ActivateOwner(ctx, principal, deliveryKey, stored.BatchKey+":"+stage+":activate",
			externalstage.ActivateOwnerRequest{
				ReporterRegistrationID:        registrationID,
				StageKey:                      stage,
				ExpectedAttemptNumber:         snapshot.AttemptNumber,
				ExpectedPlanRevision:          snapshot.PlanRevision,
				ExpectedCurrentExecution:      expectedExecution,
				ExpectedCurrentAuthorityEpoch: expectedEpoch,
			})
		if err != nil {
			return mapExternal(err)
		}
		execution, epoch = activation.ExecutionNumber, activation.AuthorityEpoch
		if err := s.crashAfter(stage + "_activate"); err != nil {
			return err
		}
	}
	if _, err := s.External.SealPrerequisites(ctx, principal, deliveryKey, stored.BatchKey+":"+stage+":prereq",
		externalstage.SealPrerequisitesRequest{
			StageKey: stage, ExecutionNumber: execution, ExpectedPlanRevision: snapshot.PlanRevision,
			ExpectedAuthorityEpoch: epoch, Prerequisites: []externalstage.Prerequisite{},
		}); err != nil {
		return mapExternal(err)
	}
	if err := s.crashAfter(stage + "_prereq"); err != nil {
		return err
	}
	expires := s.now().Add(handoffTTL).Format(time.RFC3339Nano)
	if _, err := s.External.CreateHandoff(ctx, principal, deliveryKey, stored.BatchKey+":"+stage+":handoff",
		externalstage.CreateHandoffRequest{
			StageKey: stage, ExecutionNumber: execution, ExpectedPlanRevision: snapshot.PlanRevision,
			ExpectedAuthorityEpoch: epoch, ReporterRegistrationID: registrationID, ExpiresAt: expires,
		}); err != nil {
		return mapExternal(err)
	}
	return s.crashAfter(stage + "_handoff")
}

func (s *Service) crashAfter(step string) error {
	if s.failAfter != "" && s.failAfter == step {
		return fmt.Errorf("%w: injected crash after %s", ErrUnavailable, step)
	}
	return nil
}

func mapExternal(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, externalstage.ErrInvalid):
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	case errors.Is(err, externalstage.ErrNotFound):
		return fmt.Errorf("%w: current authorization or handoff target unavailable", ErrForbidden)
	case errors.Is(err, externalstage.ErrConflict):
		return fmt.Errorf("%w: %v", ErrConflict, err)
	case errors.Is(err, externalstage.ErrUnavailable):
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	default:
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
}

func stageSatisfied(snapshot delivery.Snapshot, key string) bool {
	stage := snapshotStage(snapshot, key)
	return stage.PolicySatisfied
}

func snapshotStage(snapshot delivery.Snapshot, key string) delivery.StageSnapshot {
	for _, stage := range snapshot.Stages {
		if stage.StageKey == key {
			return stage
		}
	}
	return delivery.StageSnapshot{StageKey: key}
}

func (s *Service) annotateBridge(ctx context.Context, tx *sql.Tx, stored storedBatch, snapshot delivery.Snapshot, progress *Progress, state *string) error {
	if progress == nil || state == nil {
		return nil
	}
	if *state == BatchCancelled || *state == BatchCompleted {
		return nil
	}
	spec := snapshotStage(snapshot, delivery.StageSpecification)
	impl := snapshotStage(snapshot, delivery.StageImplementation)
	qa := snapshotStage(snapshot, delivery.StageQA)
	deploy := snapshotStage(snapshot, delivery.StageDeployment)
	verify := snapshotStage(snapshot, delivery.StageVerification)
	switch {
	case !spec.PolicySatisfied:
		progress.NextAction = NextActionImplementationEvidence
		return nil
	case !impl.PolicySatisfied:
		progress.NextAction = NextActionImplementationEvidence
		return nil
	case !qa.PolicySatisfied:
		progress.NextAction = NextActionQAEvidence
		return nil
	}
	registration, err := hasPharosOwnerRegistration(ctx, tx, stored)
	if err != nil {
		return err
	}
	if stored.ExecutionMode == ModeManual {
		if !registration {
			progress.SetupRequired = SetupRequiredPharosRegistration
			progress.NextAction = NextActionExternalStageCLI
			return nil
		}
		handoff, err := loadPreferredHandoff(ctx, tx, stored, deploy, verify)
		if err != nil {
			return err
		}
		progress.Handoff = handoffView(handoff)
		if handoff.HandoffID == "" {
			progress.NextAction = NextActionExternalStageCLI
			return nil
		}
		if handoff.CredentialEpoch == 0 {
			progress.SetupRequired = SetupRequiredHandoffSecretMint
			progress.NextAction = NextActionMintHandoffSecret
		} else if !deploy.PolicySatisfied {
			progress.NextAction = NextActionDeploymentReceipt
		} else if !verify.PolicySatisfied {
			progress.NextAction = NextActionVerificationObserve
		}
		return nil
	}
	if s.External == nil {
		progress.SetupRequired = SetupRequiredHandoffConfig
		progress.NextAction = NextActionPharosRegistration
		progress.BlockingReason = "setup_required_" + SetupRequiredHandoffConfig
		if *state != BatchPaused {
			*state = BatchBlocked
		}
		return nil
	}
	if !registration {
		progress.SetupRequired = SetupRequiredPharosRegistration
		progress.NextAction = NextActionPharosRegistration
		progress.BlockingReason = "setup_required_" + SetupRequiredPharosRegistration
		if *state != BatchPaused {
			*state = BatchBlocked
		}
		return nil
	}
	handoff, err := loadPreferredHandoff(ctx, tx, stored, deploy, verify)
	if err != nil {
		return err
	}
	progress.Handoff = handoffView(handoff)
	switch {
	case !deploy.PolicySatisfied && handoff.HandoffID == "":
		progress.NextAction = NextActionAuthorizeHandoff
	case handoff.HandoffID != "" && handoff.CredentialEpoch == 0:
		progress.SetupRequired = SetupRequiredHandoffSecretMint
		progress.NextAction = NextActionMintHandoffSecret
		progress.BlockingReason = "setup_required_" + SetupRequiredHandoffSecretMint
		if *state != BatchPaused {
			*state = BatchBlocked
		}
	case !deploy.PolicySatisfied:
		progress.NextAction = NextActionDeploymentReceipt
	case !verify.PolicySatisfied && (handoff.StageKey != delivery.StageVerification || handoff.HandoffID == ""):
		progress.NextAction = NextActionVerificationHandoff
	case !verify.PolicySatisfied && handoff.CredentialEpoch == 0:
		progress.SetupRequired = SetupRequiredHandoffSecretMint
		progress.NextAction = NextActionMintHandoffSecret
		progress.BlockingReason = "setup_required_" + SetupRequiredHandoffSecretMint
		if *state != BatchPaused {
			*state = BatchBlocked
		}
	case !verify.PolicySatisfied:
		progress.NextAction = NextActionVerificationObserve
	}
	return nil
}

func hasPharosOwnerRegistration(ctx context.Context, tx *sql.Tx, stored storedBatch) (bool, error) {
	if stored.DeliveryID == nil {
		return false, nil
	}
	var n int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM external_stage_reporter_registrations
		WHERE delivery_id=? AND project_id=? AND reporter_class='pharos' AND reporter_role='owner' AND revoked_at IS NULL`,
		*stored.DeliveryID, stored.ProjectID).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func loadPreferredHandoff(ctx context.Context, tx *sql.Tx, stored storedBatch, deploy, verify delivery.StageSnapshot) (storedHandoff, error) {
	if verify.PolicySatisfied || deploy.PolicySatisfied {
		if h, err := loadStageHandoff(ctx, tx, stored, delivery.StageVerification); err != nil {
			return storedHandoff{}, err
		} else if h.HandoffID != "" {
			return h, nil
		}
	}
	return loadStageHandoff(ctx, tx, stored, delivery.StageDeployment)
}

func loadStageHandoff(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, stored storedBatch, stage string) (storedHandoff, error) {
	var h storedHandoff
	err := q.QueryRowContext(ctx, `SELECT handoff_id,stage_key,lifecycle_state,credential_epoch,execution_number,authority_epoch
		FROM external_stage_handoffs WHERE root_issue_id=? AND project_id=? AND stage_key=? AND COALESCE(revoked_at,'')=''
		ORDER BY id DESC LIMIT 1`, stored.IssueID, stored.ProjectID, stage).
		Scan(&h.HandoffID, &h.StageKey, &h.State, &h.CredentialEpoch, &h.ExecutionNumber, &h.AuthorityEpoch)
	if errors.Is(err, sql.ErrNoRows) {
		return storedHandoff{}, nil
	}
	return h, err
}

func handoffView(h storedHandoff) *HandoffView {
	if h.HandoffID == "" {
		return nil
	}
	return &HandoffView{
		StageKey: h.StageKey, HandoffID: h.HandoffID, State: h.State,
		CredentialEpoch: h.CredentialEpoch, MintRequired: h.CredentialEpoch == 0,
	}
}
