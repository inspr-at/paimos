// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package supervision

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/models"
)

type Service struct {
	db      *sql.DB
	clock   Clock
	ids     IDSource
	mutator SynchronousMutator
	changes ChangeRecorder
}

func NewService(database *sql.DB, options Options) *Service {
	clock := options.Clock
	if clock == nil {
		clock = ClockFunc(time.Now)
	}
	ids := options.IDs
	if ids == nil {
		ids = IDSourceFunc(uuid.NewString)
	}
	return &Service{db: database, clock: clock, ids: ids, mutator: options.Mutator, changes: options.Changes}
}

type authorizedTx struct {
	tx        *sql.Tx
	user      *models.User
	principal auth.Principal
}

func (s *Service) beginAuthorized(ctx context.Context, expected auth.Principal, readOnly bool, requiredScope string) (*authorizedTx, error) {
	if s == nil || s.db == nil {
		return nil, domainError(ErrUnavailable, CodeStorageUnavailable)
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: readOnly})
	if err != nil {
		return nil, storageError(ctx, err)
	}
	// This is deliberately the first database operation after BeginTx.
	user, current, err := auth.ReauthorizePrincipalTx(ctx, tx, expected, s.clock.Now().UTC())
	if err != nil {
		_ = tx.Rollback()
		if errors.Is(err, auth.ErrCredentialUnavailable) || errors.Is(err, auth.ErrPrincipalChanged) {
			return nil, domainError(ErrForbidden, CodeCredentialRevoked)
		}
		return nil, storageError(ctx, err)
	}
	if principalImpersonated(current) {
		_ = tx.Rollback()
		return nil, domainError(ErrForbidden, CodeForbidden)
	}
	if requiredScope == "" || !current.HasScope(requiredScope) {
		_ = tx.Rollback()
		return nil, domainError(ErrForbidden, CodeScopeRevoked)
	}
	return &authorizedTx{tx: tx, user: user, principal: current}, nil
}

// Impersonation cannot be represented by M147's effective-actor equality and
// is therefore denied rather than silently attributed to either account.
// Kept as a local helper so auth.Principal does not grow control-specific API.
func principalImpersonated(principal auth.Principal) bool { return principal.Impersonated() }

func (s *Service) finishRead(authz *authorizedTx) error {
	if err := authz.tx.Commit(); err != nil {
		return domainError(ErrUnavailable, CodeStorageUnavailable)
	}
	return nil
}

func commitWithWake(ctx context.Context, authz *authorizedTx, changes ChangeRecorder, wake *CommitWake) error {
	if err := authz.tx.Commit(); err != nil {
		return domainError(ErrUnavailable, CodeStorageUnavailable)
	}
	if wake != nil && changes != nil {
		changes.WakeControlChange(ctx, *wake)
	}
	return nil
}

func storageError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return domainError(ErrUnavailable, CodeStorageUnavailable)
}

func mutationError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrInvalid) || errors.Is(err, ErrForbidden) || errors.Is(err, ErrNotFound) ||
		errors.Is(err, ErrConflict) || errors.Is(err, ErrUnavailable) || errors.Is(err, ErrInvariant) {
		return err
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return domainError(ErrConflict, CodeStaleTarget)
}

func sqliteConflict(err error) error {
	if err == nil {
		return nil
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "unique constraint") || strings.Contains(message, "constraint failed") ||
		strings.Contains(message, "control ") || strings.Contains(message, "stale") || strings.Contains(message, "invalid") {
		return domainError(ErrConflict, CodeSemanticConflict)
	}
	return domainError(ErrUnavailable, CodeStorageUnavailable)
}

func parseControlTime(value string) (time.Time, error) {
	parsed, err := time.Parse("2006-01-02T15:04:05.000Z", value)
	if err != nil {
		return time.Time{}, domainError(ErrInvariant, CodeInvariant)
	}
	return parsed.UTC(), nil
}

func expiredAt(now, expiry time.Time) bool { return !expiry.After(now) }

func safeID(source IDSource) (string, error) {
	value := source.NewID()
	if !validUUID(value) {
		return "", domainError(ErrInvariant, CodeInvariant)
	}
	return value, nil
}

func credentialColumns(principal auth.Principal) (kind string, session any, apiKey any, ok bool) {
	switch principal.Kind() {
	case auth.PrincipalSession:
		return string(principal.Kind()), principal.SessionCredentialID(), nil, principal.SessionCredentialID() != ""
	case auth.PrincipalAPIKey:
		return string(principal.Kind()), nil, principal.APIKeyID(), principal.APIKeyID() > 0
	default:
		return "", nil, nil, false
	}
}

func requireProjectEdit(ctx context.Context, tx *sql.Tx, user *models.User, projectID int64) error {
	if user == nil || user.Status != "active" || projectID <= 0 || user.Role == "external" {
		return domainError(ErrForbidden, CodeForbidden)
	}
	if auth.IsAdmin(user) {
		return nil
	}
	if user.Role != "member" {
		return domainError(ErrForbidden, CodeForbidden)
	}
	var level string
	err := tx.QueryRowContext(ctx, `SELECT access_level FROM project_members WHERE user_id=? AND project_id=?`, user.ID, projectID).Scan(&level)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // internal members default to editor
	}
	if err != nil {
		return storageError(ctx, err)
	}
	if level != "editor" {
		return domainError(ErrForbidden, CodeForbidden)
	}
	return nil
}

type operationReplay struct {
	found          bool
	requestDigest  []byte
	grantID        sql.NullString
	leaseID        sql.NullString
	inputRequestID sql.NullString
	commandID      sql.NullString
}

func lookupOperation(ctx context.Context, tx *sql.Tx, principal auth.Principal, operation string, keyDigest, requestDigest [32]byte) (operationReplay, error) {
	if !knownOperationKinds[operation] {
		return operationReplay{}, domainError(ErrInvalid, CodeInvalidRequest)
	}
	kind, session, apiKey, ok := credentialColumns(principal)
	if !ok {
		return operationReplay{}, domainError(ErrForbidden, CodeCredentialRevoked)
	}
	var replay operationReplay
	err := tx.QueryRowContext(ctx, `SELECT request_digest,grant_id,lease_id,input_request_id,command_id
		FROM control_operation_keys WHERE principal_kind=?
		 AND actor_session_credential_id IS ? AND actor_api_key_id IS ?
		 AND operation_kind=? AND operation_key_digest=?`, kind, session, apiKey, operation, keyDigest[:]).
		Scan(&replay.requestDigest, &replay.grantID, &replay.leaseID, &replay.inputRequestID, &replay.commandID)
	if errors.Is(err, sql.ErrNoRows) {
		return replay, nil
	}
	if err != nil {
		return operationReplay{}, storageError(ctx, err)
	}
	if !bytes.Equal(replay.requestDigest, requestDigest[:]) {
		return operationReplay{}, domainError(ErrConflict, CodeIdempotencyConflict)
	}
	replay.found = true
	return replay, nil
}

func insertOperation(ctx context.Context, tx *sql.Tx, principal auth.Principal, operation string, keyDigest, requestDigest, resultDigest [32]byte,
	subjectColumn, subjectID string) error {
	kind, session, apiKey, ok := credentialColumns(principal)
	if !ok || !knownOperationKinds[operation] {
		return domainError(ErrForbidden, CodeCredentialRevoked)
	}
	column := map[string]string{
		"grant_id": "grant_id", "lease_id": "lease_id", "input_request_id": "input_request_id", "command_id": "command_id",
	}[subjectColumn]
	if column == "" || !validUUID(subjectID) {
		return domainError(ErrInvariant, CodeInvariant)
	}
	query := fmt.Sprintf(`INSERT INTO control_operation_keys(
		actor_user_id,user_id,principal_kind,actor_session_credential_id,actor_api_key_id,
		operation_kind,operation_key_digest,request_digest,result_digest,%s)
		VALUES(?,?,?,?,?,?,?,?,?,?)`, column)
	_, err := tx.ExecContext(ctx, query, principal.UserID(), principal.UserID(), kind, session, apiKey,
		operation, keyDigest[:], requestDigest[:], resultDigest[:], subjectID)
	if err != nil {
		return sqliteConflict(err)
	}
	return nil
}
