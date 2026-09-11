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
	RevokedAt       string
}

type generationRef struct {
	AttemptID       int64
	AttemptNumber   int64
	PlanRevision    int64
	ExecutionNumber int64
	AuthorityEpoch  int64
}

type sealedPrerequisiteSet struct {
	DeclaredCount int
	SealedAt      string
	PlanRevision  int64
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
	registrationID, err := s.currentPharosOwnerRegistration(ctx, principal, deliveryKey, stored.DelegatedLaunch)
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

func (s *Service) currentPharosOwnerRegistration(ctx context.Context, principal externalstage.Principal, deliveryKey string, delegated *DelegatedLaunchSelection) (int64, error) {
	listed, err := s.External.ListReporters(ctx, principal, deliveryKey)
	if err != nil {
		return 0, err
	}
	for _, row := range listed.Registrations {
		if row.ReporterClass == externalstage.ReporterClassPharos && row.ReporterRole == externalstage.ReporterRoleOwner && row.RevokedAt == "" &&
			(delegated == nil || (row.TargetRef == delegated.TargetRef && row.Workflow == delegated.Workflow && row.Environment == delegated.Environment)) {
			return row.RegistrationID, nil
		}
	}
	return 0, nil
}

func (s *Service) janusPrerequisites(ctx context.Context, principal externalstage.Principal, deliveryKey string) ([]externalstage.Prerequisite, error) {
	listed, err := s.External.ListReporters(ctx, principal, deliveryKey)
	if err != nil {
		return nil, err
	}
	out := make([]externalstage.Prerequisite, 0)
	for _, row := range listed.Registrations {
		if row.ReporterClass == externalstage.ReporterClassJanus && row.ReporterRole == externalstage.ReporterRoleDependency && row.RevokedAt == "" {
			out = append(out, externalstage.Prerequisite{
				DependencyKey:          row.DependencyKey,
				ReporterRegistrationID: row.RegistrationID,
				Requirement:            externalstage.PrerequisiteRequired,
			})
		}
	}
	return out, nil
}

func (s *Service) ensureStageHandoff(ctx context.Context, principal externalstage.Principal, stored storedBatch, snapshot delivery.Snapshot, stage string, registrationID int64, deliveryKey string) error {
	gen := currentGeneration(snapshot, stage)
	existing, err := loadLiveOwnerHandoff(ctx, s.DB, stored, gen, stage)
	if err != nil {
		return err
	}
	if existing.HandoffID != "" {
		if ok, err := s.stageReadyForHandoff(ctx, stored, snapshot, gen, stage); err != nil || !ok {
			return err
		}
		return nil
	}
	execution, epoch := gen.ExecutionNumber, gen.AuthorityEpoch
	if execution == 0 {
		activation, err := s.External.ActivateOwner(ctx, principal, deliveryKey, bridgeIdempotency(stored, snapshot, stage, "activate", 0, 0),
			externalstage.ActivateOwnerRequest{
				ReporterRegistrationID:        registrationID,
				StageKey:                      stage,
				ExpectedAttemptNumber:         snapshot.AttemptNumber,
				ExpectedPlanRevision:          snapshot.PlanRevision,
				ExpectedCurrentExecution:      0,
				ExpectedCurrentAuthorityEpoch: 0,
			})
		if err != nil {
			return mapExternal(err)
		}
		execution, epoch = activation.ExecutionNumber, activation.AuthorityEpoch
		if err := s.crashAfter(stage + "_activate"); err != nil {
			return err
		}
	}
	gen.ExecutionNumber, gen.AuthorityEpoch = execution, epoch
	sealed, err := loadSealedPrerequisiteSet(ctx, s.DB, stored, gen, stage)
	if err != nil {
		return err
	}
	if sealed.SealedAt == "" {
		prereqs, err := s.janusPrerequisites(ctx, principal, deliveryKey)
		if err != nil {
			return err
		}
		if len(prereqs) == 0 {
			return nil
		}
		if _, err := s.External.SealPrerequisites(ctx, principal, deliveryKey, bridgeIdempotency(stored, snapshot, stage, "prereq", execution, epoch),
			externalstage.SealPrerequisitesRequest{
				StageKey: stage, ExecutionNumber: execution, ExpectedPlanRevision: snapshot.PlanRevision,
				ExpectedAuthorityEpoch: epoch, Prerequisites: prereqs,
			}); err != nil {
			return mapExternal(err)
		}
	} else if sealed.PlanRevision != snapshot.PlanRevision {
		return fmt.Errorf("%w: sealed prerequisites belong to a different plan revision", ErrConflict)
	}
	if err := s.crashAfter(stage + "_prereq"); err != nil {
		return err
	}
	if ok, err := s.stageReadyForHandoff(ctx, stored, snapshot, gen, stage); err != nil || !ok {
		return err
	}
	revoked, err := countRevokedOwnerHandoffs(ctx, s.DB, stored, gen, stage)
	if err != nil {
		return err
	}
	step := "handoff"
	if revoked > 0 {
		step = fmt.Sprintf("handoff:r%d", revoked)
	}
	expires := s.now().Add(handoffTTL).Format(time.RFC3339Nano)
	if _, err := s.External.CreateHandoff(ctx, principal, deliveryKey, bridgeIdempotency(stored, snapshot, stage, step, execution, epoch),
		externalstage.CreateHandoffRequest{
			StageKey: stage, ExecutionNumber: execution, ExpectedPlanRevision: snapshot.PlanRevision,
			ExpectedAuthorityEpoch: epoch, ReporterRegistrationID: registrationID, ExpiresAt: expires,
		}); err != nil {
		return mapExternal(err)
	}
	return s.crashAfter(stage + "_handoff")
}

func (s *Service) stageReadyForHandoff(ctx context.Context, stored storedBatch, snapshot delivery.Snapshot, gen generationRef, stage string) (bool, error) {
	if stored.DeliveryID == nil {
		return false, nil
	}
	match, err := externalstage.SealedPrerequisitesMatchActiveJanus(ctx, s.DB, *stored.DeliveryID, gen.AttemptID, stage, gen.ExecutionNumber, gen.AuthorityEpoch)
	if err != nil || !match {
		return false, err
	}
	return s.builtIdentityComplete(ctx, stored, snapshot)
}

func (s *Service) builtIdentityComplete(ctx context.Context, stored storedBatch, snapshot delivery.Snapshot) (bool, error) {
	if stored.DeliveryID == nil || snapshot.AttemptID == nil {
		return false, nil
	}
	artifact, err := externalstage.LoadExplicitBuiltArtifact(ctx, s.DB, *stored.DeliveryID, *snapshot.AttemptID)
	if err != nil {
		if errors.Is(err, externalstage.ErrInvalid) {
			return false, nil
		}
		return false, err
	}
	return artifact.Complete(), nil
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

func currentGeneration(snapshot delivery.Snapshot, stage string) generationRef {
	stageSnap := snapshotStage(snapshot, stage)
	gen := generationRef{
		AttemptNumber:   snapshot.AttemptNumber,
		PlanRevision:    snapshot.PlanRevision,
		ExecutionNumber: stageSnap.ExecutionNumber,
		AuthorityEpoch:  stageSnap.AuthorityEpoch,
	}
	if snapshot.AttemptID != nil {
		gen.AttemptID = *snapshot.AttemptID
	}
	return gen
}

func bridgeIdempotency(stored storedBatch, snapshot delivery.Snapshot, stage, step string, execution, epoch int64) string {
	return fmt.Sprintf("%s:%s:a%d:p%d:e%d:g%d:%s", stored.BatchKey, stage, snapshot.AttemptNumber, snapshot.PlanRevision, execution, epoch, step)
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
		progress.NextAction = NextActionHumanReview
		progress.BlockingReason = "specification_requires_human_review"
		if *state != BatchPaused {
			*state = BatchBlocked
		}
		return nil
	case !impl.PolicySatisfied:
		progress.NextAction = NextActionImplementationEvidence
		return nil
	case !qa.PolicySatisfied:
		progress.NextAction = NextActionQAEvidence
		return nil
	}
	if complete, err := builtIdentityCompleteTx(ctx, tx, stored, snapshot); err != nil {
		return err
	} else if !complete {
		progress.SetupRequired = SetupRequiredBuiltArtifact
		progress.NextAction = NextActionImplementationEvidence
		progress.BlockingReason = "setup_required_" + SetupRequiredBuiltArtifact
		if *state != BatchPaused {
			*state = BatchBlocked
		}
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
		handoff, err := loadPreferredHandoff(ctx, tx, stored, snapshot, deploy, verify)
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
	handoff, err := loadPreferredHandoff(ctx, tx, stored, snapshot, deploy, verify)
	if err != nil {
		return err
	}
	target := deploy
	targetStage := delivery.StageDeployment
	if deploy.PolicySatisfied {
		target = verify
		targetStage = delivery.StageVerification
	}
	if !target.PolicySatisfied {
		live, err := loadLiveOwnerHandoff(ctx, tx, stored, currentGeneration(snapshot, targetStage), targetStage)
		if err != nil {
			return err
		}
		progress.Handoff = handoffView(live)
		if live.HandoffID == "" {
			return annotatePendingHandoff(ctx, tx, stored, snapshot, progress, state, currentGeneration(snapshot, targetStage), targetStage, deploy.PolicySatisfied)
		}
		if blocked, err := annotateDivergedPrerequisites(ctx, tx, stored, currentGeneration(snapshot, targetStage), targetStage, progress, state); err != nil || blocked {
			return err
		}
		handoff = live
	} else {
		progress.Handoff = handoffView(handoff)
	}
	switch {
	case handoff.HandoffID != "" && handoff.CredentialEpoch == 0:
		progress.SetupRequired = SetupRequiredHandoffSecretMint
		progress.NextAction = NextActionMintHandoffSecret
		progress.BlockingReason = "setup_required_" + SetupRequiredHandoffSecretMint
		if *state != BatchPaused {
			*state = BatchBlocked
		}
	case !deploy.PolicySatisfied:
		progress.SetupRequired = SetupRequiredV2Report
		progress.NextAction = NextActionDeploymentReceipt
		progress.BlockingReason = "v2_report_required"
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
		progress.SetupRequired = SetupRequiredV2Report
		progress.NextAction = NextActionVerificationObserve
		progress.BlockingReason = "v2_report_required"
	}
	return nil
}

func annotatePendingHandoff(ctx context.Context, tx *sql.Tx, stored storedBatch, snapshot delivery.Snapshot, progress *Progress, state *string, gen generationRef, stage string, deploySatisfied bool) error {
	authorize := NextActionAuthorizeHandoff
	if deploySatisfied {
		authorize = NextActionVerificationHandoff
	}
	if gen.ExecutionNumber == 0 {
		progress.NextAction = authorize
		return nil
	}
	sealed, err := loadSealedPrerequisiteSet(ctx, tx, stored, gen, stage)
	if err != nil {
		return err
	}
	if sealed.SealedAt == "" {
		progress.SetupRequired = SetupRequiredPrerequisiteSeal
		progress.NextAction = NextActionExternalStageCLI
		progress.BlockingReason = "setup_required_" + SetupRequiredPrerequisiteSeal
		if *state != BatchPaused {
			*state = BatchBlocked
		}
		return nil
	}
	if blocked, err := annotateDivergedPrerequisites(ctx, tx, stored, gen, stage, progress, state); err != nil || blocked {
		return err
	}
	revoked, err := countRevokedOwnerHandoffs(ctx, tx, stored, gen, stage)
	if err != nil {
		return err
	}
	if revoked > 0 {
		progress.SetupRequired = SetupRequiredHandoffRevoked
		progress.NextAction = NextActionRotateHandoff
		progress.BlockingReason = "setup_required_" + SetupRequiredHandoffRevoked
		if *state != BatchPaused {
			*state = BatchBlocked
		}
		return nil
	}
	progress.NextAction = authorize
	return nil
}

func annotateDivergedPrerequisites(ctx context.Context, tx *sql.Tx, stored storedBatch, gen generationRef, stage string, progress *Progress, state *string) (bool, error) {
	if stored.DeliveryID == nil {
		return false, nil
	}
	match, err := externalstage.SealedPrerequisitesMatchActiveJanus(ctx, tx, *stored.DeliveryID, gen.AttemptID, stage, gen.ExecutionNumber, gen.AuthorityEpoch)
	if err != nil {
		return false, err
	}
	if match {
		return false, nil
	}
	progress.SetupRequired = SetupRequiredPrerequisiteReview
	progress.NextAction = NextActionExternalStageCLI
	progress.BlockingReason = "setup_required_" + SetupRequiredPrerequisiteReview
	if *state != BatchPaused {
		*state = BatchBlocked
	}
	return true, nil
}

func builtIdentityCompleteTx(ctx context.Context, tx *sql.Tx, stored storedBatch, snapshot delivery.Snapshot) (bool, error) {
	if stored.DeliveryID == nil || snapshot.AttemptID == nil {
		return false, nil
	}
	artifact, err := externalstage.LoadExplicitBuiltArtifact(ctx, tx, *stored.DeliveryID, *snapshot.AttemptID)
	if err != nil {
		if errors.Is(err, externalstage.ErrInvalid) {
			return false, nil
		}
		return false, err
	}
	return artifact.Complete(), nil
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

func loadPreferredHandoff(ctx context.Context, tx *sql.Tx, stored storedBatch, snapshot delivery.Snapshot, deploy, verify delivery.StageSnapshot) (storedHandoff, error) {
	if verify.PolicySatisfied || deploy.PolicySatisfied {
		if h, err := loadLiveOwnerHandoff(ctx, tx, stored, currentGeneration(snapshot, delivery.StageVerification), delivery.StageVerification); err != nil {
			return storedHandoff{}, err
		} else if h.HandoffID != "" {
			return h, nil
		}
	}
	return loadLiveOwnerHandoff(ctx, tx, stored, currentGeneration(snapshot, delivery.StageDeployment), delivery.StageDeployment)
}

func loadLiveOwnerHandoff(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, stored storedBatch, gen generationRef, stage string) (storedHandoff, error) {
	if gen.AttemptID == 0 || gen.ExecutionNumber == 0 || gen.AuthorityEpoch == 0 || gen.PlanRevision == 0 {
		return storedHandoff{}, nil
	}
	var h storedHandoff
	err := q.QueryRowContext(ctx, `SELECT handoff_id,stage_key,lifecycle_state,credential_epoch,execution_number,authority_epoch,COALESCE(revoked_at,'')
		FROM external_stage_handoffs WHERE root_issue_id=? AND project_id=? AND stage_key=? AND attempt_id=? AND plan_revision=?
		 AND execution_number=? AND authority_epoch=? AND reporter_role='owner' AND COALESCE(revoked_at,'')=''
		ORDER BY id DESC LIMIT 1`, stored.IssueID, stored.ProjectID, stage, gen.AttemptID, gen.PlanRevision, gen.ExecutionNumber, gen.AuthorityEpoch).
		Scan(&h.HandoffID, &h.StageKey, &h.State, &h.CredentialEpoch, &h.ExecutionNumber, &h.AuthorityEpoch, &h.RevokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return storedHandoff{}, nil
	}
	return h, err
}

func countRevokedOwnerHandoffs(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, stored storedBatch, gen generationRef, stage string) (int64, error) {
	if gen.AttemptID == 0 || gen.ExecutionNumber == 0 {
		return 0, nil
	}
	var n int64
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM external_stage_handoffs
		WHERE root_issue_id=? AND project_id=? AND stage_key=? AND attempt_id=? AND plan_revision=?
		 AND execution_number=? AND authority_epoch=? AND reporter_role='owner' AND COALESCE(revoked_at,'')<>''`,
		stored.IssueID, stored.ProjectID, stage, gen.AttemptID, gen.PlanRevision, gen.ExecutionNumber, gen.AuthorityEpoch).Scan(&n)
	return n, err
}

func loadSealedPrerequisiteSet(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, stored storedBatch, gen generationRef, stage string) (sealedPrerequisiteSet, error) {
	if stored.DeliveryID == nil || gen.AttemptID == 0 || gen.ExecutionNumber == 0 {
		return sealedPrerequisiteSet{}, nil
	}
	var out sealedPrerequisiteSet
	err := q.QueryRowContext(ctx, `SELECT prerequisite_set.declared_count,COALESCE(prerequisite_set.sealed_at,''),attempt.plan_revision
		FROM external_stage_prerequisite_sets prerequisite_set
		JOIN delivery_attempts attempt ON attempt.id=prerequisite_set.attempt_id AND attempt.delivery_id=prerequisite_set.delivery_id
		WHERE prerequisite_set.delivery_id=? AND prerequisite_set.attempt_id=? AND prerequisite_set.stage_key=?
		 AND prerequisite_set.execution_number=? AND prerequisite_set.authority_epoch=?`,
		*stored.DeliveryID, gen.AttemptID, stage, gen.ExecutionNumber, gen.AuthorityEpoch).
		Scan(&out.DeclaredCount, &out.SealedAt, &out.PlanRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return sealedPrerequisiteSet{}, nil
	}
	return out, err
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
