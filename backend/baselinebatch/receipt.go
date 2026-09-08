// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package baselinebatch

import (
	"bytes"
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/delivery"
	"github.com/inspr-at/paimos/backend/externalstage"
)

const (
	receiptReasonHuman   = "built_receipt"
	receiptReasonMachine = "built_receipt_machine"
)

// BuiltReceiptRequest is the typed producer for baseline implementation and
// scoped QA. It is not a generic evidence array and never carries deployment
// or verification fields.
type BuiltReceiptRequest struct {
	IdempotencyKey                       string `json:"idempotency_key"`
	ExpectedAttemptID                    int64  `json:"expected_attempt_id"`
	ExpectedPlanRevision                 int64  `json:"expected_plan_revision"`
	ExpectedImplementationExecution      int64  `json:"expected_implementation_execution"`
	ExpectedImplementationAuthorityEpoch int64  `json:"expected_implementation_authority_epoch"`
	ExpectedAccountKey                   string `json:"expected_account_key,omitempty"`
	ExpectedRuntimeGeneration            string `json:"expected_runtime_generation,omitempty"`
	Commit                               string `json:"commit"`
	OCIConfigDigest                      string `json:"oci_config_digest"`
	ReleaseManifestDigest                string `json:"release_manifest_digest"`
	ReleaseManifestCoordinate            string `json:"release_manifest_coordinate"`
	OCIIndexDigest                       string `json:"oci_index_digest,omitempty"`
	VersionScheme                        string `json:"version_scheme"`
	ReleaseChannel                       string `json:"release_channel"`
	ReleaseSequence                      int64  `json:"release_sequence"`
	Version                              string `json:"version"`
	QADigest                             string `json:"qa_digest"`
}

type parsedBuiltReceipt struct {
	artifact    externalstage.BuiltOwnerArtifact
	qaDigest    string
	configHex   string
	manifestHex string
	indexHex    string
}

// RecordBuiltReceipt records the scheme-aware OCI/release tuple plus scoped QA
// onto the current sealed attempt. It never starts a specification retry and
// never exposes generic stage reporting.
func (s *Service) RecordBuiltReceipt(ctx context.Context, actor Actor, projectID, batchID int64, req BuiltReceiptRequest) (Batch, error) {
	if projectID <= 0 || batchID <= 0 {
		return Batch{}, fmt.Errorf("%w: batch", ErrInvalid)
	}
	parsed, err := parseBuiltReceipt(req)
	if err != nil {
		return Batch{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Batch{}, err
	}
	defer tx.Rollback()
	stored, err := loadBatchByID(ctx, tx, projectID, batchID)
	if err != nil {
		return Batch{}, err
	}
	if err := s.authorizeReceipt(ctx, tx, actor, stored, req); err != nil {
		return Batch{}, err
	}
	if stored.ControlState == ControlCancelled {
		return Batch{}, fmt.Errorf("%w: batch is cancelled", ErrConflict)
	}
	if stored.ControlState == ControlPaused {
		return Batch{}, fmt.Errorf("%w: batch is paused", ErrConflict)
	}
	execution, err := loadOwnedExecution(ctx, tx, stored.ProjectID, stored.LifecycleIntentID)
	if err != nil {
		return Batch{}, err
	}
	switch execution.IntentState {
	case "cancelled", "failed", "expired":
		return Batch{}, fmt.Errorf("%w: lifecycle ownership is revoked", ErrForbidden)
	}
	if s.Delivery == nil {
		return Batch{}, fmt.Errorf("%w: delivery store required", ErrUnavailable)
	}
	snapshot, err := s.Delivery.SnapshotByIssueTx(ctx, tx, stored.IssueID)
	if err != nil {
		return Batch{}, err
	}
	if snapshot.Failed || snapshot.Cancelled {
		return Batch{}, fmt.Errorf("%w: delivery attempt is %s", ErrConflict, snapshot.State)
	}
	if snapshot.AttemptNumber != req.ExpectedAttemptID || snapshot.PlanRevision != req.ExpectedPlanRevision {
		return Batch{}, fmt.Errorf("%w: expected attempt or plan revision", ErrStale)
	}
	if !stageSatisfied(snapshot, delivery.StageSpecification) {
		return Batch{}, fmt.Errorf("%w: specification is not satisfied", ErrConflict)
	}
	reporter := receiptReporter(actor)
	implSatisfied := stageSatisfied(snapshot, delivery.StageImplementation)
	qaSatisfied := stageSatisfied(snapshot, delivery.StageQA)
	if implSatisfied && qaSatisfied {
		batch, replay, err := s.replayBuiltReceiptTx(ctx, tx, actor, stored, snapshot, req, parsed, reporter)
		if err != nil || replay {
			return batch, err
		}
		return Batch{}, fmt.Errorf("%w: built receipt already recorded", ErrConflict)
	}
	if qaActive(snapshot) {
		return Batch{}, fmt.Errorf("%w: qa execution is still active", ErrConflict)
	}
	effects := s.Delivery.NewEffects()
	if err := s.recordBuiltReceiptTx(ctx, tx, effects, stored, snapshot, req, parsed, reporter); err != nil {
		return Batch{}, err
	}
	if err := tx.Commit(); err != nil {
		return Batch{}, err
	}
	effects.Dispatch(ctx)
	return s.GetBatch(ctx, actor, projectID, batchID)
}

func (s *Service) authorizeReceipt(ctx context.Context, tx *sql.Tx, actor Actor, stored storedBatch, req BuiltReceiptRequest) error {
	if _, err := s.currentAuthority(ctx, tx, actor, stored.ProjectID, true); err != nil {
		return err
	}
	if err := s.requireStreamEnabled(ctx, tx, stored.ProjectID); err != nil {
		return err
	}
	switch stored.ExecutionMode {
	case ModeManual, ModeAssisted:
		if err := requireHuman(actor); err != nil {
			return err
		}
	case ModeAutomatic:
		switch actor.Kind {
		case string(auth.PrincipalSession):
			if err := requireHuman(actor); err != nil {
				return err
			}
		case string(auth.PrincipalAPIKey):
			if actor.UserID != stored.StartedBy {
				return fmt.Errorf("%w: automatic receipt is bound to the start confirmer", ErrForbidden)
			}
			if strings.TrimSpace(req.ExpectedAccountKey) == "" || strings.TrimSpace(req.ExpectedRuntimeGeneration) == "" {
				return fmt.Errorf("%w: automatic receipt requires the selected worker account and runtime generation", ErrForbidden)
			}
		default:
			return fmt.Errorf("%w: current editor session or scoped api key required", ErrForbidden)
		}
	default:
		return fmt.Errorf("%w: execution_mode", ErrInvalid)
	}
	if key := strings.TrimSpace(req.ExpectedAccountKey); key != "" && key != stored.Worker.AccountKey {
		return fmt.Errorf("%w: selected worker account", ErrForbidden)
	}
	if gen := strings.TrimSpace(req.ExpectedRuntimeGeneration); gen != "" && gen != stored.Worker.RuntimeGeneration {
		return fmt.Errorf("%w: selected runtime generation", ErrForbidden)
	}
	return nil
}

func (s *Service) replayBuiltReceiptTx(ctx context.Context, tx *sql.Tx, actor Actor, stored storedBatch, snapshot delivery.Snapshot, req BuiltReceiptRequest, parsed parsedBuiltReceipt, reporter delivery.Actor) (Batch, bool, error) {
	if stored.DeliveryID == nil || snapshot.AttemptID == nil {
		return Batch{}, false, fmt.Errorf("%w: built receipt already recorded", ErrConflict)
	}
	got, err := externalstage.LoadExplicitBuiltArtifact(ctx, tx, *stored.DeliveryID, *snapshot.AttemptID)
	if err != nil {
		if errors.Is(err, externalstage.ErrInvalid) {
			return Batch{}, false, fmt.Errorf("%w: built artifact identity", ErrConflict)
		}
		return Batch{}, false, err
	}
	qaDigest, err := loadSucceededQADigest(ctx, tx, *stored.DeliveryID, *snapshot.AttemptID)
	if err != nil {
		return Batch{}, false, err
	}
	if !sameBuiltArtifact(got, parsed.artifact) || qaDigest != parsed.qaDigest {
		return Batch{}, false, fmt.Errorf("%w: conflicting built receipt", ErrConflict)
	}
	matched, err := s.Delivery.MatchStageStartEnvelopeTx(ctx, tx, implStartRequest(stored.IssueID, snapshot.AttemptNumber, req, reporter))
	if err != nil {
		return Batch{}, false, mapDelivery(err)
	}
	if !matched {
		return Batch{}, false, fmt.Errorf("%w: stale built receipt replacement", ErrConflict)
	}
	batch, err := s.projectBatch(ctx, tx, stored)
	if err != nil {
		return Batch{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Batch{}, false, err
	}
	return batch, true, nil
}

func (s *Service) recordBuiltReceiptTx(ctx context.Context, tx *sql.Tx, effects *delivery.Effects, stored storedBatch, snapshot delivery.Snapshot, req BuiltReceiptRequest, parsed parsedBuiltReceipt, reporter delivery.Actor) error {
	reason, activity := receiptStartMeta(reporter)
	impl, err := s.Delivery.StartStageRetryTx(ctx, tx, effects, implStartRequest(stored.IssueID, snapshot.AttemptNumber, req, reporter))
	if err != nil {
		return mapDelivery(err)
	}
	evidence := []delivery.Evidence{
		{Type: "implementation_result", Outcome: "passed", ReferenceKind: "commit", ReferenceValue: parsed.artifact.Commit},
		{Type: "artifact", Outcome: "passed", ReferenceKind: "digest", DigestSHA256: parsed.configHex},
		{Type: "artifact", Outcome: "passed", ReferenceKind: "external_ref", ReferenceValue: externalstage.FormatReleaseManifestRef(parsed.manifestHex)},
		{Type: "artifact", Outcome: "passed", ReferenceKind: "external_ref", ReferenceValue: externalstage.FormatReleaseCoordinateRef(parsed.artifact.Coordinate)},
		{Type: "artifact", Outcome: "passed", ReferenceKind: "external_ref",
			ReferenceValue: externalstage.FormatReleaseIdentity(externalstage.VersionScheme(parsed.artifact.Scheme), parsed.artifact.Channel, parsed.artifact.Sequence, parsed.artifact.Version)},
	}
	if parsed.indexHex != "" {
		evidence = append(evidence, delivery.Evidence{
			Type: "artifact", Outcome: "passed", ReferenceKind: "external_ref",
			ReferenceValue: externalstage.FormatOCIManifestRef(parsed.indexHex),
		})
	}
	if _, err := s.Delivery.ReportStageTx(ctx, tx, effects, delivery.StageReport{
		IssueID: stored.IssueID, AttemptNumber: snapshot.AttemptNumber, StageKey: delivery.StageImplementation,
		ExecutionNumber: impl.ExecutionNumber, AuthorityEpoch: impl.AuthorityEpoch, Reporter: reporter,
		IdempotencyKey: req.IdempotencyKey + ":impl:report", Kind: "semantic", State: "succeeded",
		Activity: activity, Evidence: evidence, ReasonCode: reason,
	}); err != nil {
		return mapDelivery(err)
	}
	if err := s.crashAfter("built_receipt_impl"); err != nil {
		return err
	}
	qaExpectedExec, qaExpectedEpoch := int64(0), int64(0)
	qaStage := snapshotStage(snapshot, delivery.StageQA)
	if qaStage.ExecutionNumber > 0 && stageSatisfied(snapshot, delivery.StageQA) {
		qaExpectedExec, qaExpectedEpoch = qaStage.ExecutionNumber, qaStage.AuthorityEpoch
	}
	qa, err := s.Delivery.StartStageRetryTx(ctx, tx, effects, delivery.StageStartRequest{
		IssueID: stored.IssueID, AttemptNumber: snapshot.AttemptNumber, StageKey: delivery.StageQA,
		Reporter: reporter, ReasonCode: reason, ReasonText: activity,
		IdempotencyKey:                req.IdempotencyKey + ":qa:start",
		ExpectedCurrentExecution:      &qaExpectedExec,
		ExpectedCurrentAuthorityEpoch: &qaExpectedEpoch,
	})
	if err != nil {
		return mapDelivery(err)
	}
	if _, err := s.Delivery.ReportStageTx(ctx, tx, effects, delivery.StageReport{
		IssueID: stored.IssueID, AttemptNumber: snapshot.AttemptNumber, StageKey: delivery.StageQA,
		ExecutionNumber: qa.ExecutionNumber, AuthorityEpoch: qa.AuthorityEpoch, Reporter: reporter,
		IdempotencyKey: req.IdempotencyKey + ":qa:report", Kind: "semantic", State: "succeeded",
		Activity: activity, ReasonCode: reason,
		Evidence: []delivery.Evidence{{
			Type: "test_result", Outcome: "passed", ReferenceKind: "digest", DigestSHA256: parsed.qaDigest,
		}},
	}); err != nil {
		return mapDelivery(err)
	}
	return nil
}

func ValidateBuiltReceipt(req BuiltReceiptRequest) error {
	_, err := parseBuiltReceipt(req)
	return err
}

func parseBuiltReceipt(req BuiltReceiptRequest) (parsedBuiltReceipt, error) {
	if !validIdempotency(req.IdempotencyKey) {
		return parsedBuiltReceipt{}, fmt.Errorf("%w: idempotency_key", ErrInvalid)
	}
	if req.ExpectedAttemptID <= 0 || req.ExpectedPlanRevision <= 0 {
		return parsedBuiltReceipt{}, fmt.Errorf("%w: expected attempt or plan revision", ErrInvalid)
	}
	if req.ExpectedImplementationExecution < 0 || req.ExpectedImplementationAuthorityEpoch < 0 {
		return parsedBuiltReceipt{}, fmt.Errorf("%w: expected implementation execution", ErrInvalid)
	}
	if (req.ExpectedImplementationExecution == 0) != (req.ExpectedImplementationAuthorityEpoch == 0) {
		return parsedBuiltReceipt{}, fmt.Errorf("%w: expected implementation execution", ErrInvalid)
	}
	configHex, configRaw, err := parseDigestHex(req.OCIConfigDigest)
	if err != nil {
		return parsedBuiltReceipt{}, fmt.Errorf("%w: oci_config_digest", ErrInvalid)
	}
	manifestHex, manifestRaw, err := parseDigestHex(req.ReleaseManifestDigest)
	if err != nil {
		return parsedBuiltReceipt{}, fmt.Errorf("%w: release_manifest_digest", ErrInvalid)
	}
	qaHex, _, err := parseDigestHex(req.QADigest)
	if err != nil {
		return parsedBuiltReceipt{}, fmt.Errorf("%w: qa_digest", ErrInvalid)
	}
	var indexHex string
	var indexRaw []byte
	if strings.TrimSpace(req.OCIIndexDigest) != "" {
		indexHex, indexRaw, err = parseDigestHex(req.OCIIndexDigest)
		if err != nil {
			return parsedBuiltReceipt{}, fmt.Errorf("%w: oci_index_digest", ErrInvalid)
		}
	}
	artifact := externalstage.BuiltOwnerArtifact{
		Digest:          configRaw,
		ReleaseManifest: manifestRaw,
		OCIIndex:        indexRaw,
		Commit:          strings.TrimSpace(req.Commit),
		Coordinate:      strings.TrimSpace(req.ReleaseManifestCoordinate),
		Scheme:          strings.TrimSpace(req.VersionScheme),
		Channel:         strings.TrimSpace(req.ReleaseChannel),
		Sequence:        req.ReleaseSequence,
		Version:         strings.TrimSpace(req.Version),
	}
	if !artifact.Complete() {
		return parsedBuiltReceipt{}, fmt.Errorf("%w: built artifact identity", ErrInvalid)
	}
	identity := externalstage.FormatReleaseIdentity(externalstage.VersionScheme(artifact.Scheme), artifact.Channel, artifact.Sequence, artifact.Version)
	if _, _, _, _, ok := externalstage.ParseReleaseIdentity(identity); !ok {
		return parsedBuiltReceipt{}, fmt.Errorf("%w: built artifact identity", ErrInvalid)
	}
	return parsedBuiltReceipt{artifact: artifact, qaDigest: qaHex, configHex: configHex, manifestHex: manifestHex, indexHex: indexHex}, nil
}

func parseDigestHex(raw string) (string, []byte, error) {
	value := strings.TrimPrefix(strings.TrimSpace(raw), "sha256:")
	if len(value) != 64 || value != strings.ToLower(value) {
		return "", nil, ErrInvalid
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 32 {
		return "", nil, ErrInvalid
	}
	if hex.EncodeToString(decoded) != value {
		return "", nil, ErrInvalid
	}
	return value, decoded, nil
}

func receiptReporter(actor Actor) delivery.Actor {
	if actor.Kind == string(auth.PrincipalAPIKey) && actor.APIKeyID > 0 {
		// Opaque keys cannot contain "api-key" — delivery rejects secret-like
		// reporter identities. The numeric key id is the durable machine
		// provenance; the HTTP authorizer still uses the key owner separately.
		return delivery.Actor{Type: "system", OpaqueKey: fmt.Sprintf("paimos:machine:%d", actor.APIKeyID)}
	}
	return delivery.Actor{Type: "user", OpaqueKey: fmt.Sprintf("user:%d", actor.UserID)}
}

func receiptStartMeta(reporter delivery.Actor) (reason, activity string) {
	if reporter.Type == "system" {
		return receiptReasonMachine, "Automatic built artifact and scoped QA receipt"
	}
	return receiptReasonHuman, "Typed built artifact and scoped QA receipt"
}

func implStartRequest(issueID, attemptNumber int64, req BuiltReceiptRequest, reporter delivery.Actor) delivery.StageStartRequest {
	reason, activity := receiptStartMeta(reporter)
	exec, epoch := req.ExpectedImplementationExecution, req.ExpectedImplementationAuthorityEpoch
	return delivery.StageStartRequest{
		IssueID: issueID, AttemptNumber: attemptNumber, StageKey: delivery.StageImplementation,
		Reporter: reporter, ReasonCode: reason, ReasonText: activity,
		IdempotencyKey:                req.IdempotencyKey + ":impl:start",
		ExpectedCurrentExecution:      &exec,
		ExpectedCurrentAuthorityEpoch: &epoch,
	}
}

func qaActive(snapshot delivery.Snapshot) bool {
	stage := snapshotStage(snapshot, delivery.StageQA)
	return stage.ExecutionNumber > 0 && !stage.PolicySatisfied && stage.SemanticState == "active"
}

func loadSucceededQADigest(ctx context.Context, tx *sql.Tx, deliveryID, attemptID int64) (string, error) {
	var digest string
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(evidence.digest_sha256,'')
		FROM delivery_stage_latest latest
		JOIN delivery_stage_events terminal ON terminal.id=latest.semantic_stage_event_id
		 AND terminal.semantic_state='succeeded'
		JOIN delivery_evidence evidence ON evidence.stage_event_id=latest.semantic_stage_event_id
		WHERE latest.delivery_id=? AND latest.attempt_id=? AND latest.stage_key=? AND evidence.evidence_type='test_result'
		ORDER BY evidence.ordinal LIMIT 1`, deliveryID, attemptID, delivery.StageQA).Scan(&digest)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return digest, err
}

func sameBuiltArtifact(got, want externalstage.BuiltOwnerArtifact) bool {
	return subtle.ConstantTimeCompare(got.Digest, want.Digest) == 1 &&
		subtle.ConstantTimeCompare(got.ReleaseManifest, want.ReleaseManifest) == 1 &&
		bytes.Equal(got.OCIIndex, want.OCIIndex) &&
		got.Commit == want.Commit && got.Coordinate == want.Coordinate &&
		got.Scheme == want.Scheme && got.Channel == want.Channel &&
		got.Sequence == want.Sequence && got.Version == want.Version
}

func mapDelivery(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, delivery.ErrInvalid):
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	case errors.Is(err, delivery.ErrConflict):
		return fmt.Errorf("%w: %v", ErrConflict, err)
	case errors.Is(err, delivery.ErrStaleAuthority):
		return fmt.Errorf("%w: %v", ErrStale, err)
	case errors.Is(err, delivery.ErrUnauthorized):
		return fmt.Errorf("%w: %v", ErrUnauthorized, err)
	case errors.Is(err, delivery.ErrNotFound):
		return fmt.Errorf("%w: %v", ErrNotFound, err)
	default:
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
}
