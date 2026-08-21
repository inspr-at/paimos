// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package delivery

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inspr-at/paimos/backend/safetext"
)

const (
	maxReasonBytes   = 280
	maxActivityBytes = 280
	maxBasisBytes    = 240
	maxChildFacts    = 16
)

var (
	safeOpaqueKey  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)
	safeReasonCode = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	safeReference  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+-]{0,191}$`)
	hexDigest      = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type Store struct {
	db         *sql.DB
	clock      Clock
	authorizer Authorizer
	observer   CommitObserver
	freshness  FreshnessPolicy
}

type Options struct {
	Clock      Clock
	Authorizer Authorizer
	Observer   CommitObserver
	Freshness  FreshnessPolicy
}

func NewStore(database *sql.DB, opts Options) *Store {
	clock := opts.Clock
	if clock == nil {
		clock = ClockFunc(time.Now)
	}
	freshness := opts.Freshness
	defaults := DefaultFreshnessPolicy()
	if freshness.FirstSignalTimeout <= 0 {
		freshness.FirstSignalTimeout = defaults.FirstSignalTimeout
	}
	if freshness.HeartbeatTimeout <= 0 {
		freshness.HeartbeatTimeout = defaults.HeartbeatTimeout
	}
	if freshness.EstimateTimeout <= 0 {
		freshness.EstimateTimeout = defaults.EstimateTimeout
	}
	return &Store{db: database, clock: clock, authorizer: opts.Authorizer, observer: opts.Observer, freshness: freshness}
}

func (s *Store) NewEffects() *Effects { return NewEffects(s.observer) }

func (s *Store) now() time.Time { return s.clock.Now().UTC() }

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func (s *Store) authorize(ctx context.Context, q DBTX, issueID int64, actor Actor, action string, policy *Policy) (*int64, error) {
	var project sql.NullInt64
	if err := q.QueryRowContext(ctx, `SELECT project_id FROM issues WHERE id=? AND deleted_at IS NULL`, issueID).Scan(&project); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var projectID *int64
	if project.Valid {
		v := project.Int64
		projectID = &v
	}
	if s.authorizer != nil {
		req := AuthorizationRequest{Action: action, Actor: actor, IssueID: issueID, ProjectID: projectID}
		if policy != nil {
			req.PolicyReference = policy.PolicyReference
			req.ReasonCode = policy.ReasonCode
			req.ReasonText = policy.ReasonText
		}
		if err := s.authorizer.Authorize(ctx, req); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrUnauthorized, err)
		}
	}
	return projectID, nil
}

type deliveryRow struct {
	ID        int64
	IssueID   int64
	Key       string
	ProjectID *int64
	// RevokedProjectID is populated only for the atomic project-move
	// envelope. It is deliberately transient: persisted deliveries always
	// describe the current root, while the append-only change row retains the
	// bounded prior audience needed to tell an old-scope stream to refetch.
	RevokedProjectID *int64
	SpecRevision     int64
}

func loadDeliveryByIssue(ctx context.Context, q DBTX, issueID int64) (deliveryRow, error) {
	var d deliveryRow
	var project sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT id,issue_id,delivery_key,project_id_hint,spec_revision FROM deliveries WHERE issue_id=?`, issueID).
		Scan(&d.ID, &d.IssueID, &d.Key, &project, &d.SpecRevision)
	if project.Valid {
		d.ProjectID = &project.Int64
	}
	return d, err
}

func (s *Store) ensureDeliveryTx(ctx context.Context, tx DBTX, effects *Effects, issueID int64) (deliveryRow, error) {
	if d, err := loadDeliveryByIssue(ctx, tx, issueID); err == nil {
		return d, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return deliveryRow{}, err
	}
	var project sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT project_id FROM issues WHERE id=? AND deleted_at IS NULL`, issueID).Scan(&project); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return deliveryRow{}, ErrNotFound
		}
		return deliveryRow{}, err
	}
	now := formatTime(s.now())
	key := fmt.Sprintf("issue:%d", issueID)
	res, err := tx.ExecContext(ctx, `INSERT INTO deliveries(issue_id,delivery_key,project_id_hint,created_at,updated_at) VALUES(?,?,?,?,?)`, issueID, key, nullableInt64(project), now, now)
	if err != nil {
		if d, loadErr := loadDeliveryByIssue(ctx, tx, issueID); loadErr == nil {
			return d, nil
		}
		return deliveryRow{}, err
	}
	id, _ := res.LastInsertId()
	d := deliveryRow{ID: id, IssueID: issueID, Key: key, SpecRevision: 1}
	if project.Valid {
		d.ProjectID = &project.Int64
	}
	reporterID, err := ensureReporterTx(ctx, tx, d.ID, Actor{Type: "system", OpaqueKey: "paimos"}, now)
	if err != nil {
		return deliveryRow{}, err
	}
	payload := struct {
		IssueID int64 `json:"issue_id"`
	}{issueID}
	event, err := s.appendEnvelopeTx(ctx, tx, effects, d, reporterID, "delivery_created", "delivery-created", payload, "", "", "delivery", "system", &issueID, nil, now)
	if err != nil || event.Duplicate {
		return deliveryRow{}, err
	}
	return d, nil
}

func nullableInt64(v sql.NullInt64) any {
	if !v.Valid {
		return nil
	}
	return v.Int64
}

func validateActor(actor Actor) error {
	if actor.Type != "user" && actor.Type != "agent_run" && actor.Type != "external" && actor.Type != "system" {
		return fmt.Errorf("%w: invalid reporter type", ErrInvalid)
	}
	if validatePersistedKey(actor.OpaqueKey, safeOpaqueKey, 128) != nil {
		return fmt.Errorf("%w: invalid opaque reporter key", ErrInvalid)
	}
	return nil
}

func ensureReporterTx(ctx context.Context, tx DBTX, deliveryID int64, actor Actor, now string) (int64, error) {
	if err := validateActor(actor); err != nil {
		return 0, err
	}
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM delivery_reporters WHERE delivery_id=? AND reporter_type=? AND opaque_key=?`, deliveryID, actor.Type, actor.OpaqueKey).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO delivery_reporters(delivery_id,reporter_type,opaque_key,created_at) VALUES(?,?,?,?)`, deliveryID, actor.Type, actor.OpaqueKey, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

type envelopeResult struct {
	ID        int64
	Revision  int64
	Duplicate bool
}

// lookupEnvelopeDuplicate is intentionally usable before currentness and
// source-sequence checks. Exact transport retries therefore return the
// original durable result even after the targeted execution was superseded.
func lookupEnvelopeDuplicate(ctx context.Context, q DBTX, d deliveryRow, reporterID int64, kind, idempotencyKey string, payload any) (envelopeResult, error) {
	if validatePersistedKey(idempotencyKey, safeOpaqueKey, 128) != nil {
		return envelopeResult{}, fmt.Errorf("%w: invalid idempotency key", ErrInvalid)
	}
	if reporterID <= 0 || kind == "" {
		return envelopeResult{}, fmt.Errorf("%w: invalid idempotency namespace", ErrInvalid)
	}
	hash, err := canonicalHash(payload)
	if err != nil {
		return envelopeResult{}, err
	}
	var prior envelopeResult
	var priorHash []byte
	err = q.QueryRowContext(ctx, `SELECT id,delivery_revision,payload_hash FROM delivery_events
		WHERE delivery_id=? AND reporter_id=? AND kind=? AND idempotency_key=?`, d.ID, reporterID, kind, idempotencyKey).
		Scan(&prior.ID, &prior.Revision, &priorHash)
	if errors.Is(err, sql.ErrNoRows) {
		return envelopeResult{}, nil
	}
	if err != nil {
		return envelopeResult{}, err
	}
	if string(priorHash) != string(hash) {
		return envelopeResult{}, fmt.Errorf("%w: idempotency key has a different canonical payload", ErrConflict)
	}
	prior.Duplicate = true
	return prior, nil
}

func lookupEnvelopeDuplicateForActor(ctx context.Context, q DBTX, d deliveryRow, actor Actor, kind, idempotencyKey string, payload any) (envelopeResult, error) {
	if err := validateActor(actor); err != nil {
		return envelopeResult{}, err
	}
	if validatePersistedKey(idempotencyKey, safeOpaqueKey, 128) != nil {
		return envelopeResult{}, fmt.Errorf("%w: invalid idempotency key", ErrInvalid)
	}
	var reporterID int64
	err := q.QueryRowContext(ctx, `SELECT id FROM delivery_reporters
		WHERE delivery_id=? AND reporter_type=? AND opaque_key=?`, d.ID, actor.Type, actor.OpaqueKey).Scan(&reporterID)
	if errors.Is(err, sql.ErrNoRows) {
		return envelopeResult{}, nil
	}
	if err != nil {
		return envelopeResult{}, err
	}
	return lookupEnvelopeDuplicate(ctx, q, d, reporterID, kind, idempotencyKey, payload)
}

func canonicalHash(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	return sum[:], nil
}

func (s *Store) appendEnvelopeTx(
	ctx context.Context,
	tx DBTX,
	effects *Effects,
	d deliveryRow,
	reporterID int64,
	kind, idempotencyKey string,
	payload any,
	reasonCode, reasonText, changeKind, sourceKind string,
	sourceID, sourceSequence *int64,
	now string,
) (envelopeResult, error) {
	return s.appendEnvelopeTxMode(ctx, tx, effects, d, reporterID, kind, idempotencyKey, payload,
		reasonCode, reasonText, changeKind, sourceKind, sourceID, sourceSequence, now, true)
}

// appendEnvelopeTxMode is used only when a linked run must advance canonical
// delivery truth while its issue is hidden. The immutable audit envelope is
// still recorded, but no audience-visible invalidation may be emitted until
// the issue is restored. The restore boundary then publishes one issue change
// carrying the latest delivery revision.
func (s *Store) appendEnvelopeTxMode(
	ctx context.Context,
	tx DBTX,
	effects *Effects,
	d deliveryRow,
	reporterID int64,
	kind, idempotencyKey string,
	payload any,
	reasonCode, reasonText, changeKind, sourceKind string,
	sourceID, sourceSequence *int64,
	now string,
	emitChange bool,
) (envelopeResult, error) {
	if validatePersistedKey(idempotencyKey, safeOpaqueKey, 128) != nil {
		return envelopeResult{}, fmt.Errorf("%w: invalid idempotency key", ErrInvalid)
	}
	if err := validateBoundedText(reasonText, maxReasonBytes, true); err != nil {
		return envelopeResult{}, err
	}
	if reasonCode != "" && validatePersistedKey(reasonCode, safeReasonCode, 64) != nil {
		return envelopeResult{}, fmt.Errorf("%w: invalid reason code", ErrInvalid)
	}
	hash, err := canonicalHash(payload)
	if err != nil {
		return envelopeResult{}, err
	}
	if prior, err := lookupEnvelopeDuplicate(ctx, tx, d, reporterID, kind, idempotencyKey, payload); err != nil || prior.Duplicate {
		return prior, err
	}
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(delivery_revision),0)+1 FROM delivery_events WHERE delivery_id=?`, d.ID).Scan(&revision); err != nil {
		return envelopeResult{}, err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO delivery_events(
		delivery_id,delivery_revision,idempotency_key,payload_hash,kind,reporter_id,reason_code,reason_text,server_received_at)
		VALUES(?,?,?,?,?,?,?,?,?)`, d.ID, revision, idempotencyKey, hash, kind, reporterID, reasonCode, reasonText, now)
	if err != nil {
		return envelopeResult{}, err
	}
	id, _ := res.LastInsertId()
	if !emitChange {
		return envelopeResult{ID: id, Revision: revision}, nil
	}
	hint, err := appendChangeTx(ctx, tx, d, revision, changeKind, sourceKind, sourceID, sourceSequence, now)
	if err != nil {
		return envelopeResult{}, err
	}
	effects.add(hint)
	return envelopeResult{ID: id, Revision: revision}, nil
}

func appendChangeTx(ctx context.Context, tx DBTX, d deliveryRow, revision int64, kind, sourceKind string, sourceID, sourceSequence *int64, now string) (ChangeHint, error) {
	var sequence int64
	if err := tx.QueryRowContext(ctx, `SELECT change_sequence_high_water+1 FROM deliveries WHERE id=?`, d.ID).Scan(&sequence); err != nil {
		return ChangeHint{}, err
	}
	var tokenBytes [16]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		return ChangeHint{}, err
	}
	token := hex.EncodeToString(tokenBytes[:])
	res, err := tx.ExecContext(ctx, `INSERT INTO delivery_change_log(
		cursor_token,delivery_id,root_issue_id,delivery_key,project_id_hint,revoked_project_id,change_sequence,
		delivery_revision,kind,source_kind,source_id,source_sequence,server_received_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, token, d.ID, d.IssueID, d.Key, d.ProjectID, d.RevokedProjectID,
		sequence, revision, kind, sourceKind, sourceID, sourceSequence, now)
	if err != nil {
		return ChangeHint{}, err
	}
	id, _ := res.LastInsertId()
	return ChangeHint{InternalID: id, CursorToken: token, DeliveryID: d.ID, RootIssueID: d.IssueID,
		DeliveryKey: d.Key, ProjectIDHint: d.ProjectID, RevokedProjectID: d.RevokedProjectID,
		ChangeSequence: sequence, DeliveryRevision: revision,
		Kind: kind, SourceKind: sourceKind, SourceID: sourceID, SourceSequence: sourceSequence, ServerReceivedAt: now}, nil
}

func validateBoundedText(v string, maxBytes int, allowEmpty bool) error {
	if !utf8.ValidString(v) || (!allowEmpty && strings.TrimSpace(v) == "") || len([]byte(v)) > maxBytes || strings.ContainsAny(v, "\x00\r\n") {
		return fmt.Errorf("%w: invalid bounded text", ErrInvalid)
	}
	if err := rejectSecretLike(v); err != nil {
		return err
	}
	return nil
}

func validatePersistedKey(v string, syntax *regexp.Regexp, maxBytes int) error {
	if !utf8.ValidString(v) || len([]byte(v)) == 0 || len([]byte(v)) > maxBytes || !syntax.MatchString(v) {
		return fmt.Errorf("%w: invalid persisted key", ErrInvalid)
	}
	return rejectSecretLike(v)
}

func validateReferenceValue(v string, maxBytes int) error {
	if err := validatePersistedKey(v, safeReference, maxBytes); err != nil {
		return err
	}
	if strings.Contains(v, "?") || (strings.Contains(v, "://") && strings.Contains(v, "@")) {
		return fmt.Errorf("%w: unsafe reference URL", ErrInvalid)
	}
	return nil
}

func rejectSecretLike(v string) error {
	if ContainsSecretLike(v) {
		return fmt.Errorf("%w: secret-like value is forbidden", ErrInvalid)
	}
	return nil
}

// ContainsSecretLike is the shared privacy boundary for text that can reach a
// delivery-facing projection. Telemetry ingestion and read-model backstops use
// this exact corpus so a value rejected at one boundary cannot be exposed by
// another.
func ContainsSecretLike(v string) bool {
	return safetext.ContainsSecretLike(v)
}

func validatePolicy(policies []Policy) error {
	if len(policies) != len(CanonicalStages) {
		return fmt.Errorf("%w: attempt policy must contain exactly five stages", ErrInvalid)
	}
	required := 0
	totalWeight := 0
	seen := map[string]bool{}
	for i, p := range policies {
		if p.StageKey != CanonicalStages[i] || seen[p.StageKey] {
			return fmt.Errorf("%w: attempt policy stage order is not canonical", ErrInvalid)
		}
		seen[p.StageKey] = true
		if p.Applicability != "required" && p.Applicability != "not_applicable" {
			return fmt.Errorf("%w: invalid applicability", ErrInvalid)
		}
		if p.Weight < 0 || p.Weight > 100 {
			return fmt.Errorf("%w: invalid stage weight", ErrInvalid)
		}
		if (p.PolicyReference != "" && validateReferenceValue(p.PolicyReference, 160) != nil) ||
			(p.ReasonCode != "" && validatePersistedKey(p.ReasonCode, safeReasonCode, 64) != nil) ||
			validateBoundedText(p.ReasonText, maxReasonBytes, true) != nil {
			return fmt.Errorf("%w: invalid bounded policy evidence", ErrInvalid)
		}
		totalWeight += p.Weight
		if p.Applicability == "required" {
			required++
		} else if p.PolicyReference == "" || validatePersistedKey(p.ReasonCode, safeReasonCode, 64) != nil || validateBoundedText(p.ReasonText, maxReasonBytes, false) != nil {
			return fmt.Errorf("%w: not-applicable stage lacks bounded policy evidence", ErrInvalid)
		}
	}
	if required == 0 || totalWeight != 100 {
		return fmt.Errorf("%w: policy must retain a required stage and total weight 100", ErrInvalid)
	}
	return nil
}

func sortedPolicies(p []Policy) []Policy {
	out := append([]Policy(nil), p...)
	sort.SliceStable(out, func(i, j int) bool {
		return stageOrder(out[i].StageKey) < stageOrder(out[j].StageKey)
	})
	return out
}

func stageOrder(stage string) int {
	for i, candidate := range CanonicalStages {
		if candidate == stage {
			return i + 1
		}
	}
	return 0
}
