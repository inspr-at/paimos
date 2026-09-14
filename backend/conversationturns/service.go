// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package conversationturns

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/dispatchprofile"
	"github.com/inspr-at/paimos/backend/lifecycleintents"
)

const orphanGrace = 30 * time.Second

var mutationMu sync.Mutex

type Service struct {
	db  *sql.DB
	now func() time.Time
}

func NewService(database *sql.DB) *Service {
	return &Service{db: database, now: time.Now}
}

func timestamp(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000Z")
}

func resolveAccountLabel(runtime lifecycleintents.Runtime, accountKey, profileID, profileVersion string, revision int64) (string, bool) {
	if runtime.SchemaVersion != lifecycleintents.AccountLifecycleSchemaV4 {
		return "", false
	}
	match := ""
	for _, scope := range runtime.AccountScopes {
		if scope.AccountAvailability != lifecycleintents.AccountAvailabilityAvailable ||
			!runtime.MatchScopeAtRevision(scope.AccountLabel, accountKey, profileID, profileVersion, true, revision) {
			continue
		}
		if match != "" {
			return "", false
		}
		match = scope.AccountLabel
	}
	return match, match != ""
}

func bindingMatchesRuntime(binding Binding, runtime lifecycleintents.Runtime) bool {
	if runtime.ID != binding.RuntimeID || runtime.Generation != binding.RuntimeGeneration || runtime.MachineID != binding.HostID {
		return false
	}
	_, ok := resolveAccountLabel(runtime, binding.AccountKey, binding.DispatchProfileID, binding.DispatchProfileVersion, binding.AttachmentRevision)
	return ok
}

// EnrollTx atomically binds an already-minted, still-uncommitted API-key row to
// one current runtime/account/profile and the explicit Aithema actor map.
func (s *Service) EnrollTx(ctx context.Context, tx *sql.Tx, operator auth.Principal, apiKeyID int64, in Enrollment) (Binding, error) {
	if tx == nil || apiKeyID < 1 || validateEnrollment(in) != nil {
		return Binding{}, ErrInvalid
	}
	limits, err := normalizeLimits(in.Limits)
	if err != nil {
		return Binding{}, err
	}
	user, current, err := auth.ReauthorizePrincipalTx(ctx, tx, operator, s.now().UTC())
	if err != nil || current.Kind() != auth.PrincipalSession || current.Impersonated() || !auth.IsAdmin(user) {
		return Binding{}, ErrUnavailable
	}
	profile, err := dispatchprofile.Resolve(in.DispatchProfileID, in.DispatchProfileVersion, "codex")
	if err != nil || profile.Harness != "codex" {
		return Binding{}, ErrUnavailable
	}
	runtime, err := lifecycleintents.NewService(s.db).CurrentRuntimeTx(ctx, tx, in.ProjectID, in.RuntimeID, in.RuntimeGeneration)
	if err != nil || runtime.MachineID != in.HostID {
		if err == lifecycleintents.ErrStorage {
			return Binding{}, ErrStorage
		}
		return Binding{}, ErrUnavailable
	}
	accountLabel, ok := resolveAccountLabel(runtime, in.AccountKey, in.DispatchProfileID, in.DispatchProfileVersion, in.AttachmentRevision)
	if !ok {
		return Binding{}, ErrUnavailable
	}
	for _, actor := range in.Actors {
		if !auth.CanViewProjectTx(ctx, tx, actor.UserID, in.ProjectID) {
			return Binding{}, ErrUnavailable
		}
	}
	binding := Binding{
		ID: uuid.NewString(), Revision: 1, ProjectID: in.ProjectID, HostID: in.HostID, ProjectRef: in.ProjectRef,
		RuntimeID: in.RuntimeID, RuntimeGeneration: in.RuntimeGeneration, AccountLabel: accountLabel,
		AccountKey: in.AccountKey, AttachmentRevision: in.AttachmentRevision,
		DispatchProfileID: in.DispatchProfileID, DispatchProfileVersion: in.DispatchProfileVersion,
		ExecutionPolicyID: ExecutionPolicyID, Limits: limits,
	}
	at := timestamp(s.now())
	_, err = tx.ExecContext(ctx, `INSERT INTO conversation_service_bindings(
		binding_id,revision,api_key_id,project_id,host_id,project_ref,runtime_id,runtime_generation,
		account_label,account_key,attachment_revision,dispatch_profile_id,dispatch_profile_version,
		execution_policy_id,max_input_bytes,max_messages,max_output_bytes,max_event_bytes,max_events,
		max_timeout_ms,created_by,session_credential_id,created_at)
		VALUES(?,1,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		binding.ID, apiKeyID, binding.ProjectID, binding.HostID, binding.ProjectRef, binding.RuntimeID,
		binding.RuntimeGeneration, binding.AccountLabel, binding.AccountKey, binding.AttachmentRevision,
		binding.DispatchProfileID, binding.DispatchProfileVersion, binding.ExecutionPolicyID,
		limits.MaxInputBytes, limits.MaxMessages, limits.MaxOutputBytes, limits.MaxEventBytes,
		limits.MaxEvents, limits.MaxTimeoutMS, user.ID, current.SessionCredentialID(), at)
	if err != nil {
		return Binding{}, ErrStorage
	}
	for _, actor := range in.Actors {
		if _, err := tx.ExecContext(ctx, `INSERT INTO conversation_service_actors(binding_id,issuer,subject,user_id,created_at)
			VALUES(?,?,?,?,?)`, binding.ID, actor.Issuer, actor.Subject, actor.UserID, at); err != nil {
			return Binding{}, ErrStorage
		}
	}
	return binding, nil
}

func scanBinding(scanner interface{ Scan(...any) error }) (Binding, error) {
	var out Binding
	err := scanner.Scan(&out.ID, &out.Revision, &out.ProjectID, &out.HostID, &out.ProjectRef,
		&out.RuntimeID, &out.RuntimeGeneration, &out.AccountLabel, &out.AccountKey, &out.AttachmentRevision,
		&out.DispatchProfileID, &out.DispatchProfileVersion, &out.ExecutionPolicyID,
		&out.Limits.MaxInputBytes, &out.Limits.MaxMessages, &out.Limits.MaxOutputBytes,
		&out.Limits.MaxEventBytes, &out.Limits.MaxEvents, &out.Limits.MaxTimeoutMS)
	return out, err
}

const bindingColumns = `binding_id,revision,project_id,host_id,project_ref,runtime_id,runtime_generation,
	account_label,account_key,attachment_revision,dispatch_profile_id,dispatch_profile_version,execution_policy_id,
	max_input_bytes,max_messages,max_output_bytes,max_event_bytes,max_events,max_timeout_ms`

func loadBinding(ctx context.Context, tx *sql.Tx, project, apiKeyID int64, bindingID string, revision int64) (Binding, error) {
	binding, err := scanBinding(tx.QueryRowContext(ctx, `SELECT `+bindingColumns+` FROM conversation_service_bindings
		WHERE project_id=? AND api_key_id=? AND binding_id=? AND revision=?`, project, apiKeyID, bindingID, revision))
	if err != nil {
		return Binding{}, ErrUnavailable
	}
	return binding, nil
}

func loadBindingForCredential(ctx context.Context, tx *sql.Tx, project, apiKeyID int64) (Binding, error) {
	binding, err := scanBinding(tx.QueryRowContext(ctx, `SELECT `+bindingColumns+` FROM conversation_service_bindings
		WHERE project_id=? AND api_key_id=?`, project, apiKeyID))
	if err != nil {
		return Binding{}, ErrUnavailable
	}
	return binding, nil
}

func (s *Service) authorizeBinding(ctx context.Context, tx *sql.Tx, principal auth.Principal, project int64, actor Actor, bindingID string, revision int64) (Binding, int64, error) {
	owner, current, err := auth.ReauthorizePrincipalTx(ctx, tx, principal, s.now().UTC())
	if err != nil || current.Kind() != auth.PrincipalConversationService || current.Impersonated() || !auth.IsAdmin(owner) {
		return Binding{}, 0, ErrUnavailable
	}
	binding, err := loadBinding(ctx, tx, project, current.APIKeyID(), bindingID, revision)
	if err != nil || !actor.Valid() {
		return Binding{}, 0, ErrUnavailable
	}
	var actorUserID int64
	if err := tx.QueryRowContext(ctx, `SELECT user_id FROM conversation_service_actors
		WHERE binding_id=? AND issuer=? AND subject=?`, binding.ID, actor.Issuer, actor.Subject).Scan(&actorUserID); err != nil ||
		!auth.CanViewProjectTx(ctx, tx, actorUserID, project) {
		return Binding{}, 0, ErrUnavailable
	}
	runtime, err := lifecycleintents.NewService(s.db).CurrentRuntimeTx(ctx, tx, project, binding.RuntimeID, binding.RuntimeGeneration)
	if err != nil || !bindingMatchesRuntime(binding, runtime) {
		if err == lifecycleintents.ErrStorage {
			return Binding{}, 0, ErrStorage
		}
		return Binding{}, 0, ErrUnavailable
	}
	return binding, actorUserID, nil
}

func (s *Service) beginContext(ctx context.Context) (*sql.Tx, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, ErrStorage
	}
	return tx, nil
}

func scanCall(scanner interface{ Scan(...any) error }) (row, error) {
	var out row
	var execution sql.NullString
	err := scanner.Scan(&out.CallID, &out.BindingID, &out.BindingRevision, &out.APIKeyID, &out.ProjectID,
		&out.RequestID, &out.RequestDigest, &out.RequestJSON, &out.Actor.Issuer, &out.Actor.Subject,
		&out.ActorUserID, &out.ProjectRef, &out.ConversationID, &out.TurnID, &out.Purpose, &out.State,
		&out.DeadlineAt, &out.LastSequence, &execution, &out.RuntimeID, &out.RuntimeGeneration,
		&out.AccountKey, &out.AttachmentRevision, &out.DispatchProfileID, &out.DispatchProfileVersion,
		&out.ExecutionPolicyID, &out.NativeThreadID, &out.NativeTurnID, &out.AssembledText,
		&out.OutputText, &out.OutputSHA256, &out.ErrorCode, &out.CreatedAt, &out.UpdatedAt)
	if err != nil {
		return row{}, err
	}
	out.SchemaVersion = SchemaVersion
	out.ExecutionGeneration = execution.String
	return out, nil
}

const callColumns = `call_id,binding_id,binding_revision,api_key_id,project_id,request_id,request_digest,request_json,
	actor_issuer,actor_subject,actor_user_id,project_ref,conversation_id,turn_id,purpose,state,deadline_at,last_sequence,
	execution_generation,runtime_id,runtime_generation,account_key,attachment_revision,dispatch_profile_id,
	dispatch_profile_version,execution_policy_id,native_thread_id,native_turn_id,assembled_text,output_text,
	output_sha256,error_code,created_at,updated_at`

func loadCall(ctx context.Context, tx *sql.Tx, project int64, callID string) (row, error) {
	out, err := scanCall(tx.QueryRowContext(ctx, `SELECT `+callColumns+` FROM conversation_calls
		WHERE project_id=? AND call_id=?`, project, callID))
	if err != nil {
		return row{}, ErrUnavailable
	}
	return out, nil
}

func publicCall(in row) Call {
	out := in.Call
	if out.State != "completed" {
		out.OutputText = ""
		out.OutputSHA256 = ""
	}
	if out.State != "failed" {
		out.ErrorCode = ""
	}
	return out
}

func releaseSlot(ctx context.Context, tx *sql.Tx, callID, at string) error {
	result, err := tx.ExecContext(ctx, `UPDATE conversation_account_slots SET released_at=?
		WHERE call_id=? AND released_at IS NULL`, at, callID)
	if err != nil {
		return ErrStorage
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return ErrStorage
	}
	return nil
}

func (s *Service) reserveAccount(ctx context.Context, tx *sql.Tx, call row, binding Binding) error {
	var existingCallID string
	err := tx.QueryRowContext(ctx, `SELECT call_id FROM conversation_account_slots
		WHERE project_id=? AND host_id=? AND account_label=? AND account_key=? AND released_at IS NULL`,
		call.ProjectID, binding.HostID, binding.AccountLabel, call.AccountKey).Scan(&existingCallID)
	if err == nil {
		existing, loadErr := loadCall(ctx, tx, call.ProjectID, existingCallID)
		if loadErr != nil {
			return ErrStorage
		}
		// A never-claimed call has a known empty execution outcome. If its
		// immutable authority was superseded, close it before reusing the account.
		if existing.State != "queued" || existing.ExecutionGeneration != "" || s.storedAuthorityCurrent(ctx, tx, existing) {
			return ErrConflict
		}
		if err := s.terminalize(ctx, tx, &existing, "failed", "authority_revoked", true); err != nil {
			return err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return ErrStorage
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO conversation_account_slots(
		call_id,project_id,host_id,runtime_id,runtime_generation,account_label,account_key,reserved_at)
		VALUES(?,?,?,?,?,?,?,?)`, call.CallID, call.ProjectID, binding.HostID, call.RuntimeID,
		call.RuntimeGeneration, binding.AccountLabel, call.AccountKey, call.CreatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return ErrConflict
		}
		return ErrStorage
	}
	return nil
}

func insertEvent(ctx context.Context, tx *sql.Tx, call row, executionGeneration string, event Event, digest []byte, at string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO conversation_call_events(
		call_id,sequence,execution_generation,kind,text,thread_id,turn_id,output_sha256,error_code,event_digest,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`, call.CallID, event.Sequence, nullable(executionGeneration), event.Kind,
		event.Text, event.ThreadID, event.TurnID, event.OutputSHA256, event.ErrorCode, digest, at)
	if err != nil {
		return ErrStorage
	}
	return nil
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (s *Service) terminalize(ctx context.Context, tx *sql.Tx, call *row, state, code string, release bool) error {
	at := timestamp(s.now())
	event := Event{Sequence: call.LastSequence + 1, Kind: state}
	if state == "failed" {
		event.ErrorCode = code
	}
	digest, _ := digestJSON(event, "paimos-conversation-server-event-v1")
	var maximumEvents int64
	if err := tx.QueryRowContext(ctx, `SELECT max_events FROM conversation_service_bindings
		WHERE binding_id=? AND revision=? AND api_key_id=? AND project_id=?`, call.BindingID,
		call.BindingRevision, call.APIKeyID, call.ProjectID).Scan(&maximumEvents); err != nil {
		return ErrStorage
	}
	if call.LastSequence < maximumEvents {
		if err := insertEvent(ctx, tx, *call, call.ExecutionGeneration, event, digest, at); err != nil {
			return err
		}
		call.LastSequence++
	}
	result, err := tx.ExecContext(ctx, `UPDATE conversation_calls SET state=?,last_sequence=?,error_code=?,updated_at=?
		WHERE call_id=? AND state=? AND last_sequence=?`, state, call.LastSequence, code, at, call.CallID, call.State, event.Sequence-1)
	if err != nil {
		return ErrStorage
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrConflict
	}
	call.State, call.ErrorCode, call.UpdatedAt = state, code, at
	if release {
		return releaseSlot(ctx, tx, call.CallID, at)
	}
	return nil
}

func (s *Service) refreshDeadline(ctx context.Context, tx *sql.Tx, call *row) error {
	deadline, ok := parseTime(call.DeadlineAt)
	if !ok {
		return ErrStorage
	}
	now := s.now().UTC()
	if now.Before(deadline) || call.State == "completed" || call.State == "failed" || call.State == "cancelled" {
		return nil
	}
	if call.State == "queued" {
		return s.terminalize(ctx, tx, call, "failed", "deadline_exceeded", true)
	}
	if call.State == "claimed" || call.State == "running" {
		result, err := tx.ExecContext(ctx, `UPDATE conversation_calls SET state='cancel_requested',updated_at=?
			WHERE call_id=? AND state=?`, timestamp(now), call.CallID, call.State)
		if err != nil {
			return ErrStorage
		}
		if count, _ := result.RowsAffected(); count != 1 {
			return ErrConflict
		}
		call.State = "cancel_requested"
	}
	if call.State == "cancel_requested" && !now.Before(deadline.Add(orphanGrace)) {
		// Unknown process outcome becomes terminal for callers after a bounded
		// grace, but the account slot deliberately remains held fail-closed.
		return s.terminalize(ctx, tx, call, "failed", "outcome_unknown", false)
	}
	return nil
}

func inputFitsBinding(request Request, binding Binding) bool {
	if int64(len(request.Messages)) > binding.Limits.MaxMessages {
		return false
	}
	total := int64(len([]byte(request.System)))
	for _, message := range request.Messages {
		total += int64(len([]byte(message.Content)))
	}
	return total <= binding.Limits.MaxInputBytes
}

func (s *Service) Submit(ctx context.Context, principal auth.Principal, project int64, actor Actor, request Request) (Call, bool, error) {
	if err := validateRequest(request); err != nil {
		return Call{}, false, err
	}
	digest, err := digestJSON(request, "paimos-conversation-request-v1")
	if err != nil {
		return Call{}, false, err
	}
	requestJSON, err := json.Marshal(request)
	if err != nil {
		return Call{}, false, ErrInvalid
	}
	mutationMu.Lock()
	defer mutationMu.Unlock()
	tx, err := s.beginContext(ctx)
	if err != nil {
		return Call{}, false, err
	}
	defer tx.Rollback()
	binding, actorUserID, err := s.authorizeBinding(ctx, tx, principal, project, actor, request.BindingID, request.BindingRevision)
	if err != nil || actor != request.Actor || request.ProjectRef != binding.ProjectRef {
		if err != nil {
			return Call{}, false, err
		}
		return Call{}, false, ErrUnavailable
	}
	if !inputFitsBinding(request, binding) {
		return Call{}, false, ErrTooLarge
	}
	var replayID string
	var replayDigest []byte
	err = tx.QueryRowContext(ctx, `SELECT call_id,request_digest FROM conversation_calls
		WHERE api_key_id=? AND project_id=? AND request_id=?`, principal.APIKeyID(), project, request.RequestID).Scan(&replayID, &replayDigest)
	if err == nil {
		if !sameDigest(digest, replayDigest) {
			return Call{}, false, ErrConflict
		}
		call, err := loadCall(ctx, tx, project, replayID)
		if err != nil || call.BindingID != binding.ID || call.Actor != actor {
			return Call{}, false, ErrUnavailable
		}
		if err := s.refreshDeadline(ctx, tx, &call); err != nil {
			return Call{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return Call{}, false, ErrStorage
		}
		return publicCall(call), false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Call{}, false, ErrStorage
	}
	timeout := request.TimeoutMS
	if timeout > binding.Limits.MaxTimeoutMS {
		timeout = binding.Limits.MaxTimeoutMS
	}
	now := s.now().UTC()
	call := row{
		Call: Call{SchemaVersion: SchemaVersion, CallID: uuid.NewString(), RequestID: request.RequestID,
			State: "queued", DeadlineAt: timestamp(now.Add(time.Duration(timeout) * time.Millisecond))},
		BindingID: binding.ID, BindingRevision: binding.Revision, APIKeyID: principal.APIKeyID(), ProjectID: project,
		RequestDigest: digest, RequestJSON: string(requestJSON), Actor: actor, ActorUserID: actorUserID,
		ProjectRef: request.ProjectRef, ConversationID: request.ConversationID, TurnID: request.TurnID,
		Purpose: request.Purpose, RuntimeID: binding.RuntimeID, RuntimeGeneration: binding.RuntimeGeneration,
		AccountKey: binding.AccountKey, AttachmentRevision: binding.AttachmentRevision,
		DispatchProfileID: binding.DispatchProfileID, DispatchProfileVersion: binding.DispatchProfileVersion,
		ExecutionPolicyID: binding.ExecutionPolicyID, CreatedAt: timestamp(now), UpdatedAt: timestamp(now),
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO conversation_calls(
		call_id,binding_id,binding_revision,api_key_id,project_id,request_id,request_digest,request_json,
		actor_issuer,actor_subject,actor_user_id,project_ref,conversation_id,turn_id,purpose,state,deadline_at,
		runtime_id,runtime_generation,account_key,attachment_revision,dispatch_profile_id,dispatch_profile_version,
		execution_policy_id,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'queued',?,?,?,?,?,?,?,?,?,?)`,
		call.CallID, call.BindingID, call.BindingRevision, call.APIKeyID, call.ProjectID, call.RequestID,
		call.RequestDigest, call.RequestJSON, actor.Issuer, actor.Subject, call.ActorUserID, call.ProjectRef,
		call.ConversationID, call.TurnID, call.Purpose, call.DeadlineAt, call.RuntimeID, call.RuntimeGeneration,
		call.AccountKey, call.AttachmentRevision, call.DispatchProfileID, call.DispatchProfileVersion,
		call.ExecutionPolicyID, call.CreatedAt, call.UpdatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return Call{}, false, ErrConflict
		}
		return Call{}, false, ErrStorage
	}
	if err := s.reserveAccount(ctx, tx, call, binding); err != nil {
		return Call{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Call{}, false, ErrStorage
	}
	return publicCall(call), true, nil
}

func (s *Service) serviceCall(ctx context.Context, tx *sql.Tx, principal auth.Principal, project int64, actor Actor, callID string) (row, Binding, error) {
	binding, err := loadBindingForCredential(ctx, tx, project, principal.APIKeyID())
	if err != nil {
		return row{}, Binding{}, ErrUnavailable
	}
	binding, _, err = s.authorizeBinding(ctx, tx, principal, project, actor, binding.ID, binding.Revision)
	if err != nil {
		return row{}, Binding{}, err
	}
	call, err := loadCall(ctx, tx, project, callID)
	if err != nil || call.BindingID != binding.ID || call.BindingRevision != binding.Revision || call.Actor != actor || call.APIKeyID != principal.APIKeyID() {
		return row{}, Binding{}, ErrUnavailable
	}
	return call, binding, nil
}

func (s *Service) Get(ctx context.Context, principal auth.Principal, project int64, actor Actor, callID string) (Call, error) {
	mutationMu.Lock()
	defer mutationMu.Unlock()
	tx, err := s.beginContext(ctx)
	if err != nil {
		return Call{}, err
	}
	defer tx.Rollback()
	call, _, err := s.serviceCall(ctx, tx, principal, project, actor, callID)
	if err != nil {
		return Call{}, err
	}
	if err := s.refreshDeadline(ctx, tx, &call); err != nil {
		return Call{}, err
	}
	if err := tx.Commit(); err != nil {
		return Call{}, ErrStorage
	}
	return publicCall(call), nil
}

func (s *Service) Events(ctx context.Context, principal auth.Principal, project int64, actor Actor, callID string, after int64) (EventsPage, error) {
	if after < 0 || after > MaximumEvents {
		return EventsPage{}, ErrInvalid
	}
	mutationMu.Lock()
	defer mutationMu.Unlock()
	tx, err := s.beginContext(ctx)
	if err != nil {
		return EventsPage{}, err
	}
	defer tx.Rollback()
	call, _, err := s.serviceCall(ctx, tx, principal, project, actor, callID)
	if err != nil {
		return EventsPage{}, err
	}
	if err := s.refreshDeadline(ctx, tx, &call); err != nil {
		return EventsPage{}, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT sequence,kind,text,thread_id,turn_id,output_sha256,error_code
		FROM conversation_call_events WHERE call_id=? AND sequence>? ORDER BY sequence LIMIT 512`, call.CallID, after)
	if err != nil {
		return EventsPage{}, ErrStorage
	}
	events := []Event{}
	for rows.Next() {
		var event Event
		if err := rows.Scan(&event.Sequence, &event.Kind, &event.Text, &event.ThreadID, &event.TurnID, &event.OutputSHA256, &event.ErrorCode); err != nil {
			rows.Close()
			return EventsPage{}, ErrStorage
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return EventsPage{}, ErrStorage
	}
	rows.Close()
	if err := tx.Commit(); err != nil {
		return EventsPage{}, ErrStorage
	}
	return EventsPage{SchemaVersion: SchemaVersion, CallID: call.CallID, Events: events, Call: publicCall(call)}, nil
}

func (s *Service) Cancel(ctx context.Context, principal auth.Principal, project int64, actor Actor, callID string) (Call, error) {
	mutationMu.Lock()
	defer mutationMu.Unlock()
	tx, err := s.beginContext(ctx)
	if err != nil {
		return Call{}, err
	}
	defer tx.Rollback()
	call, _, err := s.serviceCall(ctx, tx, principal, project, actor, callID)
	if err != nil {
		return Call{}, err
	}
	if err := s.refreshDeadline(ctx, tx, &call); err != nil {
		return Call{}, err
	}
	switch call.State {
	case "queued":
		if err := s.terminalize(ctx, tx, &call, "cancelled", "", true); err != nil {
			return Call{}, err
		}
	case "claimed", "running":
		result, err := tx.ExecContext(ctx, `UPDATE conversation_calls SET state='cancel_requested',updated_at=?
			WHERE call_id=? AND state=?`, timestamp(s.now()), call.CallID, call.State)
		if err != nil {
			return Call{}, ErrStorage
		}
		if count, _ := result.RowsAffected(); count != 1 {
			return Call{}, ErrConflict
		}
		call.State = "cancel_requested"
	case "cancel_requested", "cancelled":
	case "completed", "failed":
		return Call{}, ErrConflict
	default:
		return Call{}, ErrStorage
	}
	if err := tx.Commit(); err != nil {
		return Call{}, ErrStorage
	}
	return publicCall(call), nil
}

func (s *Service) authorizeRunner(ctx context.Context, tx *sql.Tx, principal auth.Principal, project int64, runtimeID, generation, lease string) (lifecycleintents.Runtime, error) {
	runtime, err := lifecycleintents.NewService(s.db).AuthorizeRuntimeTx(ctx, tx, principal, project, runtimeID, generation, lease)
	if err != nil {
		if err == lifecycleintents.ErrStorage {
			return lifecycleintents.Runtime{}, ErrStorage
		}
		return lifecycleintents.Runtime{}, ErrUnavailable
	}
	return runtime, nil
}

func (s *Service) storedAuthorityCurrent(ctx context.Context, tx *sql.Tx, call row) bool {
	var ownerID int64
	if err := tx.QueryRowContext(ctx, `SELECT user_id FROM api_keys WHERE id=?`, call.APIKeyID).Scan(&ownerID); err != nil {
		return false
	}
	principal, err := auth.NewConversationServicePrincipal(call.APIKeyID, ownerID)
	if err != nil {
		return false
	}
	_, actorUserID, err := s.authorizeBinding(ctx, tx, principal, call.ProjectID, call.Actor, call.BindingID, call.BindingRevision)
	return err == nil && actorUserID == call.ActorUserID
}

func (s *Service) Claim(ctx context.Context, principal auth.Principal, project int64, runtimeID, generation, lease string) (ClaimEnvelope, error) {
	if uuid.Validate(runtimeID) != nil || uuid.Validate(generation) != nil {
		return ClaimEnvelope{}, ErrInvalid
	}
	mutationMu.Lock()
	defer mutationMu.Unlock()
	tx, err := s.beginContext(ctx)
	if err != nil {
		return ClaimEnvelope{}, err
	}
	defer tx.Rollback()
	runtime, err := s.authorizeRunner(ctx, tx, principal, project, runtimeID, generation, lease)
	if err != nil {
		return ClaimEnvelope{}, err
	}
	for attempts := 0; attempts < 32; attempts++ {
		var callID string
		err := tx.QueryRowContext(ctx, `SELECT call_id FROM conversation_calls
			WHERE project_id=? AND runtime_id=? AND runtime_generation=? AND state='queued'
			ORDER BY created_at,call_id LIMIT 1`, project, runtimeID, generation).Scan(&callID)
		if errors.Is(err, sql.ErrNoRows) {
			if err := tx.Commit(); err != nil {
				return ClaimEnvelope{}, ErrStorage
			}
			return ClaimEnvelope{SchemaVersion: SchemaVersion}, nil
		}
		if err != nil {
			return ClaimEnvelope{}, ErrStorage
		}
		call, err := loadCall(ctx, tx, project, callID)
		if err != nil {
			return ClaimEnvelope{}, err
		}
		if err := s.refreshDeadline(ctx, tx, &call); err != nil {
			return ClaimEnvelope{}, err
		}
		if call.State != "queued" {
			continue
		}
		if !s.storedAuthorityCurrent(ctx, tx, call) || !bindingMatchesRuntime(Binding{
			RuntimeID: call.RuntimeID, RuntimeGeneration: call.RuntimeGeneration, AccountKey: call.AccountKey,
			AttachmentRevision: call.AttachmentRevision, DispatchProfileID: call.DispatchProfileID,
			DispatchProfileVersion: call.DispatchProfileVersion, HostID: runtime.MachineID,
		}, runtime) {
			if err := s.terminalize(ctx, tx, &call, "failed", "authority_revoked", true); err != nil {
				return ClaimEnvelope{}, err
			}
			continue
		}
		binding, err := loadBinding(ctx, tx, project, call.APIKeyID, call.BindingID, call.BindingRevision)
		if err != nil {
			return ClaimEnvelope{}, err
		}
		executionGeneration := uuid.NewString()
		result, err := tx.ExecContext(ctx, `UPDATE conversation_calls SET state='claimed',execution_generation=?,updated_at=?
			WHERE call_id=? AND state='queued' AND execution_generation IS NULL`, executionGeneration, timestamp(s.now()), call.CallID)
		if err != nil {
			return ClaimEnvelope{}, ErrStorage
		}
		if count, _ := result.RowsAffected(); count != 1 {
			return ClaimEnvelope{}, ErrConflict
		}
		call.State, call.ExecutionGeneration = "claimed", executionGeneration
		var request Request
		if json.Unmarshal([]byte(call.RequestJSON), &request) != nil {
			return ClaimEnvelope{}, ErrStorage
		}
		claim := &Claim{
			Call: publicCall(call), ExecutionGeneration: executionGeneration, RuntimeGeneration: generation,
			AccountKey: call.AccountKey, AttachmentRevision: call.AttachmentRevision,
			DispatchProfileID: call.DispatchProfileID, DispatchProfileVersion: call.DispatchProfileVersion,
			ExecutionPolicyID: call.ExecutionPolicyID, System: request.System, Messages: request.Messages,
			Purpose: request.Purpose, Limits: ClaimLimits{MaxOutputBytes: binding.Limits.MaxOutputBytes, MaxEvents: binding.Limits.MaxEvents},
		}
		if request.Purpose == "understand" || request.Purpose == "interpret" {
			claim.OutputSchema = UnderstandingOutputSchema()
		}
		if err := tx.Commit(); err != nil {
			return ClaimEnvelope{}, ErrStorage
		}
		return ClaimEnvelope{SchemaVersion: SchemaVersion, Claim: claim}, nil
	}
	// Persist bounded cleanup of stale queued work. The next poll continues
	// from the following rows instead of rolling the cleanup back forever.
	if err := tx.Commit(); err != nil {
		return ClaimEnvelope{}, ErrStorage
	}
	return ClaimEnvelope{SchemaVersion: SchemaVersion}, nil
}

func (s *Service) Control(ctx context.Context, principal auth.Principal, project int64, runtimeID, generation, lease, callID, executionGeneration string) (Control, error) {
	if uuid.Validate(callID) != nil || uuid.Validate(executionGeneration) != nil {
		return Control{}, ErrInvalid
	}
	mutationMu.Lock()
	defer mutationMu.Unlock()
	tx, err := s.beginContext(ctx)
	if err != nil {
		return Control{}, err
	}
	defer tx.Rollback()
	if _, err := s.authorizeRunner(ctx, tx, principal, project, runtimeID, generation, lease); err != nil {
		return Control{}, err
	}
	call, err := loadCall(ctx, tx, project, callID)
	if err != nil || call.RuntimeID != runtimeID || call.RuntimeGeneration != generation || call.ExecutionGeneration != executionGeneration {
		return Control{}, ErrUnavailable
	}
	authority := s.storedAuthorityCurrent(ctx, tx, call)
	if err := s.refreshDeadline(ctx, tx, &call); err != nil {
		return Control{}, err
	}
	continued := authority && (call.State == "claimed" || call.State == "running")
	cancel := call.State == "cancel_requested" || (!authority && call.State != "completed" && call.State != "cancelled")
	if err := tx.Commit(); err != nil {
		return Control{}, ErrStorage
	}
	return Control{SchemaVersion: SchemaVersion, CallID: call.CallID, ExecutionGeneration: executionGeneration,
		Continue: continued, CancelRequested: cancel, DeadlineAt: call.DeadlineAt}, nil
}

func eventMatchesNative(call row, event Event) bool {
	if call.NativeThreadID == "" && call.NativeTurnID == "" {
		return event.ThreadID == "" && event.TurnID == ""
	}
	return event.ThreadID == call.NativeThreadID && event.TurnID == call.NativeTurnID
}

// ReportCall is the HTTP-facing form; callID stays a path identity rather than
// being duplicated in the signed event body.
func (s *Service) ReportCall(ctx context.Context, principal auth.Principal, project int64, runtimeID, lease, callID string, report ReportRequest) (Call, error) {
	if uuid.Validate(callID) != nil || report.SchemaVersion != SchemaVersion || uuid.Validate(report.Generation) != nil ||
		uuid.Validate(report.ExecutionGeneration) != nil || validateEventShape(report.Event) != nil {
		return Call{}, ErrInvalid
	}
	digest, err := digestJSON(report, "paimos-conversation-runner-event-v1")
	if err != nil {
		return Call{}, err
	}
	mutationMu.Lock()
	defer mutationMu.Unlock()
	tx, err := s.beginContext(ctx)
	if err != nil {
		return Call{}, err
	}
	defer tx.Rollback()
	if _, err := s.authorizeRunner(ctx, tx, principal, project, runtimeID, report.Generation, lease); err != nil {
		return Call{}, err
	}
	call, err := loadCall(ctx, tx, project, callID)
	if err != nil {
		return Call{}, err
	}
	return s.reportTx(ctx, tx, call, runtimeID, report, digest)
}

func (s *Service) reportTx(ctx context.Context, tx *sql.Tx, call row, runtimeID string, report ReportRequest, digest []byte) (Call, error) {
	if call.RuntimeID != runtimeID || call.RuntimeGeneration != report.Generation || call.ExecutionGeneration != report.ExecutionGeneration {
		return Call{}, ErrUnavailable
	}
	binding, err := loadBinding(ctx, tx, call.ProjectID, call.APIKeyID, call.BindingID, call.BindingRevision)
	if err != nil {
		return Call{}, err
	}
	if int64(len([]byte(report.Event.Text))) > binding.Limits.MaxEventBytes || report.Event.Sequence > binding.Limits.MaxEvents {
		return Call{}, ErrTooLarge
	}
	var existingDigest []byte
	err = tx.QueryRowContext(ctx, `SELECT event_digest FROM conversation_call_events WHERE call_id=? AND sequence=?`,
		call.CallID, report.Event.Sequence).Scan(&existingDigest)
	if err == nil {
		if !sameDigest(existingDigest, digest) {
			return Call{}, ErrConflict
		}
		if err := tx.Commit(); err != nil {
			return Call{}, ErrStorage
		}
		return publicCall(call), nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Call{}, ErrStorage
	}
	currentAuthority := s.storedAuthorityCurrent(ctx, tx, call)
	if !currentAuthority && report.Event.Kind != "started" && report.Event.Kind != "failed" && report.Event.Kind != "cancelled" {
		return Call{}, ErrUnavailable
	}
	if report.Event.Sequence != call.LastSequence+1 {
		return Call{}, ErrConflict
	}
	// Exact failed/cancelled evidence from this owned execution is allowed to
	// close and release the account even after the deadline or actor revocation.
	// Nonterminal and successful reports must first survive deadline handling.
	if report.Event.Kind != "failed" && report.Event.Kind != "cancelled" {
		if err := s.refreshDeadline(ctx, tx, &call); err != nil {
			return Call{}, err
		}
	}
	event := report.Event
	terminal := false
	newState, newError, assembled, output, outputDigest := call.State, call.ErrorCode, call.AssembledText, call.OutputText, call.OutputSHA256
	switch event.Kind {
	case "started":
		if (call.State != "claimed" && call.State != "cancel_requested") || call.LastSequence != 0 {
			return Call{}, ErrConflict
		}
		// Cancellation or deadline expiry can race the runner's first report
		// after the owned native process has already started. Preserve that
		// exact native identity and sequence without reopening success authority.
		if call.State == "claimed" && currentAuthority {
			newState = "running"
		} else {
			newState = "cancel_requested"
		}
	case "assistant_delta":
		if call.State != "running" || !eventMatchesNative(call, event) {
			return Call{}, ErrConflict
		}
		assembled += event.Text
		if int64(len([]byte(assembled))) > binding.Limits.MaxOutputBytes {
			return Call{}, ErrTooLarge
		}
	case "completed":
		if call.State != "running" || !eventMatchesNative(call, event) || sha256Text(assembled) != event.OutputSHA256 {
			return Call{}, ErrInvalid
		}
		if (call.Purpose == "understand" || call.Purpose == "interpret") && validateUnderstandingOutput(assembled) != nil {
			return Call{}, ErrInvalid
		}
		newState, output, outputDigest, terminal = "completed", assembled, event.OutputSHA256, true
	case "failed":
		if call.State != "claimed" && call.State != "running" && call.State != "cancel_requested" {
			return Call{}, ErrConflict
		}
		if !eventMatchesNative(call, event) {
			return Call{}, ErrConflict
		}
		newState, newError, terminal = "failed", event.ErrorCode, true
	case "cancelled":
		if call.State != "claimed" && call.State != "running" && call.State != "cancel_requested" {
			return Call{}, ErrConflict
		}
		if !eventMatchesNative(call, event) {
			return Call{}, ErrConflict
		}
		newState, newError, terminal = "cancelled", "", true
	}
	at := timestamp(s.now())
	if err := insertEvent(ctx, tx, call, report.ExecutionGeneration, event, digest, at); err != nil {
		return Call{}, err
	}
	nativeThread, nativeTurn := call.NativeThreadID, call.NativeTurnID
	if event.Kind == "started" {
		nativeThread, nativeTurn = event.ThreadID, event.TurnID
	}
	result, err := tx.ExecContext(ctx, `UPDATE conversation_calls SET state=?,last_sequence=?,native_thread_id=?,native_turn_id=?,
		assembled_text=?,output_text=?,output_sha256=?,error_code=?,updated_at=?
		WHERE call_id=? AND state=? AND last_sequence=?`, newState, event.Sequence, nativeThread, nativeTurn,
		assembled, output, outputDigest, newError, at, call.CallID, call.State, call.LastSequence)
	if err != nil {
		return Call{}, ErrStorage
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return Call{}, ErrConflict
	}
	call.State, call.LastSequence, call.NativeThreadID, call.NativeTurnID = newState, event.Sequence, nativeThread, nativeTurn
	call.AssembledText, call.OutputText, call.OutputSHA256, call.ErrorCode = assembled, output, outputDigest, newError
	if terminal {
		if err := releaseSlot(ctx, tx, call.CallID, at); err != nil {
			return Call{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Call{}, ErrStorage
	}
	return publicCall(call), nil
}
