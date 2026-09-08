// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
package lifecycleintents

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/managedharness"
)

// Serialize this low-volume authority across HTTP Service instances. SQLite
// constraints and transaction CAS remain authoritative across server restarts.
var mutationMu sync.Mutex

var listOwnedRuntimeSessionsSQL = `SELECT own.session_id,own.generation,s.workspace_identity FROM lifecycle_runtime_sessions own JOIN harness_sessions s ON s.id=own.session_id JOIN lifecycle_runtimes runtime ON runtime.id=own.runtime_id WHERE own.runtime_id=? AND s.project_id=? AND s.management_mode='managed' AND s.host=? AND (CASE
 WHEN CAST(json_extract(runtime.registration_json,'$.schema_version') AS INTEGER)=3 THEN CASE WHEN EXISTS(
  SELECT 1 FROM json_each(runtime.registration_json,'$.account_scopes') AS scope
  WHERE json_extract(scope.value,'$.account_label')=s.account_label
   AND (
    (COALESCE(json_array_length(json_extract(scope.value,'$.accounts')),0)=0 AND COALESCE(s.account_key,'')='')
    OR EXISTS(
     SELECT 1 FROM json_each(scope.value,'$.accounts') AS account
     WHERE json_extract(account.value,'$.key')=s.account_key
      AND COALESCE(s.account_key,'')<>''
    )
   )
   AND EXISTS(
    SELECT 1 FROM json_each(scope.value,'$.profiles') AS profile
    WHERE json_extract(profile.value,'$.id')=s.dispatch_profile_id
     AND json_extract(profile.value,'$.version')=s.dispatch_profile_version
   )
 ) THEN 1 ELSE 0 END
 WHEN json_extract(runtime.registration_json,'$.account_label')=s.account_label THEN 1
 ELSE 0 END) ORDER BY own.session_id LIMIT 128`

type Service struct {
	db  *sql.DB
	now func() time.Time
}

func NewService(db *sql.DB) *Service { return &Service{db: db, now: time.Now} }
func stamp(t time.Time) string       { return t.UTC().Format("2006-01-02T15:04:05.000Z") }
func expired(deadline string, now time.Time) bool {
	t, e := time.Parse(time.RFC3339Nano, deadline)
	return e != nil || !t.After(now)
}
func encode(v any) string { b, _ := json.Marshal(v); return string(b) }
func (s *Service) begin(ctx context.Context, p auth.Principal, project int64, kind auth.PrincipalKind) (*sql.Tx, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, ErrStorage
	}
	if err = s.authorize(ctx, tx, p, project, kind); err != nil {
		tx.Rollback()
		return nil, err
	}
	return tx, nil
}
func (s *Service) authorize(ctx context.Context, tx *sql.Tx, p auth.Principal, project int64, kind auth.PrincipalKind) error {
	u, current, err := auth.ReauthorizePrincipalTx(ctx, tx, p, s.now())
	if err != nil || current.Kind() != kind || current.Impersonated() || !auth.IsSuperAdmin(u) {
		return ErrUnavailable
	}
	if kind == auth.PrincipalAPIKey && !current.HasScope(auth.ScopeAgentControlsRunner) {
		return ErrUnavailable
	}
	var exists int
	if err = tx.QueryRowContext(ctx, `SELECT 1 FROM projects WHERE id=? AND status='active'`, project).Scan(&exists); err != nil {
		return ErrUnavailable
	}
	return nil
}
func leaseDigest(generation, lease string) []byte {
	d := sha256.Sum256([]byte("paimos-lifecycle-runtime-v1\x00" + generation + "\x00" + lease))
	return d[:]
}
func validateRegistration(in Registration) error {
	if in.SchemaVersion != 0 && in.SchemaVersion != RuntimeSchemaV1 && in.SchemaVersion != AccountChoiceSchemaV2 && in.SchemaVersion != AccountScopeSchemaV3 {
		return ErrInvalid
	}
	if !validID(in.Generation) || !label(in.Host, 128) || len(in.Workspaces) > 16 || in.Workspaces == nil {
		return ErrInvalid
	}
	if in.SchemaVersion == AccountScopeSchemaV3 {
		if err := ValidateAccountScopes(in); err != nil {
			return err
		}
		return validateWorkspaces(in.Workspaces)
	}
	if !validAccount(in.AccountLabel) || len(in.Profiles) > 16 || in.Profiles == nil {
		return ErrInvalid
	}
	if err := ValidateAccountScopes(in); err != nil {
		return err
	}
	if err := ValidateAdvertisedAccounts(in); err != nil {
		return err
	}
	seen := map[string]bool{}
	if err := validateWorkspacesInto(in.Workspaces, seen); err != nil {
		return err
	}
	for _, p := range in.Profiles {
		key := p.ID + ":" + p.Version
		if seen[key] {
			return ErrInvalid
		}
		seen[key] = true
		if _, err := resolveProfile(p.ID, p.Version); err != nil {
			return ErrUnavailable
		}
	}
	return nil
}

func validateWorkspaces(workspaces []Workspace) error {
	return validateWorkspacesInto(workspaces, map[string]bool{})
}

func validateWorkspacesInto(workspaces []Workspace, seen map[string]bool) error {
	for _, w := range workspaces {
		if !validID(w.Handle) || !identity.MatchString(w.Identity) || !validWorkspaceLabel(w.Label) || seen[w.Handle] || seen[w.Identity] {
			return ErrInvalid
		}
		seen[w.Handle] = true
		seen[w.Identity] = true
	}
	return nil
}

func (s *Service) RegisterRuntime(ctx context.Context, p auth.Principal, project int64, lease string, in Registration) (Runtime, error) {
	mutationMu.Lock()
	defer mutationMu.Unlock()
	tx, err := s.begin(ctx, p, project, auth.PrincipalAPIKey)
	if err != nil {
		return Runtime{}, err
	}
	defer tx.Rollback()
	if !managedharness.ValidWorkerLease(lease) {
		return Runtime{}, ErrUnavailable
	}
	if err = validateRegistration(in); err != nil {
		return Runtime{}, err
	}
	var id, body, deadline string
	var owner, key int64
	var digest []byte
	err = tx.QueryRowContext(ctx, `SELECT id,user_id,api_key_id,lease_digest,registration_json,expires_at FROM lifecycle_runtimes WHERE generation=? AND project_id=?`, in.Generation, project).Scan(&id, &owner, &key, &digest, &body, &deadline)
	if err == nil {
		if owner != p.UserID() || key != p.APIKeyID() || subtle.ConstantTimeCompare(digest, leaseDigest(in.Generation, lease)) != 1 || body != encode(in) || expired(deadline, s.now()) {
			return Runtime{}, ErrUnavailable
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Runtime{}, ErrStorage
	} else {
		var live, total int
		if tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(CASE WHEN expires_at>? THEN 1 ELSE 0 END),0) FROM lifecycle_runtimes WHERE project_id=?`, stamp(s.now()), project).Scan(&total, &live) != nil {
			return Runtime{}, ErrStorage
		}
		if total >= 1024 || live >= 32 {
			return Runtime{}, ErrConflict
		}
		var active int
		if tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM lifecycle_runtimes WHERE project_id=? AND machine_id=? AND expires_at>?`, project, in.Host, stamp(s.now())).Scan(&active) != nil {
			return Runtime{}, ErrStorage
		}
		if active > 0 {
			return Runtime{}, ErrUnavailable
		}
		id = uuid.NewString()
		deadline = stamp(s.now().Add(RuntimeTTLSeconds * time.Second))
		_, err = tx.ExecContext(ctx, `INSERT INTO lifecycle_runtimes(id,project_id,generation,machine_id,user_id,api_key_id,lease_digest,registration_json,expires_at,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, id, project, in.Generation, in.Host, p.UserID(), p.APIKeyID(), leaseDigest(in.Generation, lease), encode(in), deadline, stamp(s.now()))
		if err != nil {
			return Runtime{}, ErrStorage
		}
	}
	deadline = stamp(s.now().Add(RuntimeTTLSeconds * time.Second))
	if _, err = tx.ExecContext(ctx, `UPDATE lifecycle_runtimes SET expires_at=? WHERE id=?`, deadline, id); err != nil {
		return Runtime{}, ErrStorage
	}
	out, err := s.runtime(ctx, tx, project, id, &p, lease, true)
	if err != nil {
		return Runtime{}, err
	}
	if err = tx.Commit(); err != nil {
		return Runtime{}, ErrStorage
	}
	return out, nil
}
func runtimeProjection(id string, project int64, in Registration, deadline string) Runtime {
	return Runtime{ID: id, ProjectID: project, Generation: in.Generation, MachineID: in.Host, AccountLabel: in.AccountLabel, Accounts: in.Accounts, Workspaces: in.Workspaces, Profiles: in.Profiles, ExpiresAt: deadline, Sessions: []SessionProjection{}, SchemaVersion: in.SchemaVersion, AccountScopes: in.AccountScopes}
}
func workspaceHandleForIdentity(workspaces []Workspace, identity string) string {
	if identity == "" {
		return ""
	}
	result, matches := "", 0
	for _, workspace := range workspaces {
		if workspace.Identity == identity {
			result = workspace.Handle
			matches++
		}
	}
	if matches != 1 {
		return ""
	}
	return result
}
func (s *Service) runtime(ctx context.Context, tx *sql.Tx, project int64, id string, p *auth.Principal, lease string, live bool) (Runtime, error) {
	var body, deadline string
	var user, key int64
	var digest []byte
	err := tx.QueryRowContext(ctx, `SELECT registration_json,expires_at,user_id,api_key_id,lease_digest FROM lifecycle_runtimes WHERE id=? AND project_id=?`, id, project).Scan(&body, &deadline, &user, &key, &digest)
	if err != nil {
		return Runtime{}, ErrUnavailable
	}
	var in Registration
	if json.Unmarshal([]byte(body), &in) != nil {
		return Runtime{}, ErrStorage
	}
	if live && expired(deadline, s.now()) {
		return Runtime{}, ErrUnavailable
	}
	if p != nil && (p.UserID() != user || p.APIKeyID() != key || !managedharness.ValidWorkerLease(lease) || subtle.ConstantTimeCompare(digest, leaseDigest(in.Generation, lease)) != 1) {
		return Runtime{}, ErrUnavailable
	}
	// Advertisement discovery also reauthorizes its owner: a revoked reporter is offline.
	reporter, _ := auth.NewAPIKeyPrincipal(key, user, auth.ParseScopes("*"))
	if live && s.authorize(ctx, tx, reporter, project, auth.PrincipalAPIKey) != nil {
		return Runtime{}, ErrUnavailable
	}
	out := runtimeProjection(id, project, in, deadline)
	rows, err := tx.QueryContext(ctx, listOwnedRuntimeSessionsSQL, id, project, out.MachineID)
	if err != nil {
		return Runtime{}, ErrStorage
	}
	for rows.Next() {
		var session SessionProjection
		var workspaceIdentity string
		if rows.Scan(&session.SessionID, &session.Generation, &workspaceIdentity) != nil {
			rows.Close()
			return Runtime{}, ErrStorage
		}
		session.WorkspaceHandle = workspaceHandleForIdentity(out.Workspaces, workspaceIdentity)
		out.Sessions = append(out.Sessions, session)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Runtime{}, ErrStorage
	}
	return out, nil
}
func (s *Service) Runtimes(ctx context.Context, p auth.Principal, project int64) ([]Runtime, error) {
	tx, err := s.begin(ctx, p, project, auth.PrincipalSession)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id FROM lifecycle_runtimes WHERE project_id=? AND expires_at>? ORDER BY id LIMIT 32`, project, stamp(s.now()))
	if err != nil {
		return nil, ErrStorage
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) != nil {
			rows.Close()
			return nil, ErrStorage
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, ErrStorage
	}
	out := []Runtime{}
	for _, id := range ids {
		r, e := s.runtime(ctx, tx, project, id, nil, "", true)
		if e == nil {
			out = append(out, r)
		} else if e != ErrUnavailable {
			return nil, e
		}
	}
	if tx.Commit() != nil {
		return nil, ErrStorage
	}
	return out, nil
}

const intentColumns = `id,project_id,request_json,state,revision,created_at,expires_at,updated_at,new_generation,result_session_id,reason`

func loadIntent(ctx context.Context, tx *sql.Tx, project int64, id string) (Intent, error) {
	out := Intent{SchemaVersion: 1}
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT `+intentColumns+` FROM lifecycle_intents WHERE project_id=? AND id=?`, project, id).Scan(&out.ID, &out.ProjectID, &raw, &out.State, &out.Revision, &out.CreatedAt, &out.ExpiresAt, &out.UpdatedAt, &out.NewGeneration, &out.ResultSessionID, &out.Reason)
	if err != nil {
		return Intent{}, ErrUnavailable
	}
	if json.Unmarshal([]byte(raw), &out.Request) != nil {
		return Intent{}, ErrStorage
	}
	out.SchemaVersion = intentSchema(out.Request)
	return out, nil
}
func (s *Service) creator(ctx context.Context, tx *sql.Tx, in Intent) error {
	var credential string
	var user int64
	if tx.QueryRowContext(ctx, `SELECT session_credential_id,user_id FROM lifecycle_intents WHERE id=?`, in.ID).Scan(&credential, &user) != nil {
		return ErrUnavailable
	}
	p, err := auth.NewSessionPrincipal(credential, user, user, false)
	if err != nil {
		return ErrUnavailable
	}
	return s.authorize(ctx, tx, p, in.ProjectID, auth.PrincipalSession)
}
func owner(ctx context.Context, tx *sql.Tx, in Intent, p auth.Principal) error {
	var user int64
	if tx.QueryRowContext(ctx, `SELECT user_id FROM lifecycle_intents WHERE id=?`, in.ID).Scan(&user) != nil || user != p.UserID() {
		return ErrUnavailable
	}
	return nil
}
func (s *Service) change(ctx context.Context, tx *sql.Tx, p auth.Principal, in *Intent, state, reason, result string) error {
	at := stamp(s.now())
	r, err := tx.ExecContext(ctx, `UPDATE lifecycle_intents SET state=?,reason=?,result_session_id=?,revision=revision+1,updated_at=?,actor_kind=?,actor_user_id=?,actor_credential_id=? WHERE id=? AND revision=?`, state, reason, result, at, p.Kind(), p.UserID(), p.SafeCredentialID(), in.ID, in.Revision)
	if err != nil {
		return ErrStorage
	}
	if n, _ := r.RowsAffected(); n != 1 {
		return ErrConflict
	}
	in.State = state
	in.Reason = reason
	in.ResultSessionID = result
	in.Revision++
	in.UpdatedAt = at
	return nil
}
func (s *Service) refresh(ctx context.Context, tx *sql.Tx, p auth.Principal, in *Intent) error {
	if terminal(in.State) {
		return nil
	}
	if expired(in.ExpiresAt, s.now()) {
		reason := "expired"
		if in.State == "claimed" || in.State == "executing" {
			reason = "outcome_unknown"
		}
		return s.change(ctx, tx, p, in, "expired", reason, "")
	}
	if s.creator(ctx, tx, *in) != nil {
		return s.change(ctx, tx, p, in, "failed", "authority_revoked", "")
	}
	if _, err := s.runtime(ctx, tx, in.ProjectID, in.Request.RuntimeID, nil, "", true); err != nil {
		if err == ErrStorage {
			return err
		}
		return s.change(ctx, tx, p, in, "failed", "ownership_lost", "")
	}
	return nil
}

// LockMutations serializes lifecycle authority mutations for a caller that has
// to span its own transaction — a baseline batch commits its immutable batch
// and the start intent together. Acquire it before beginning that transaction
// so the lock is always taken before the SQLite write lock, never after.
func LockMutations() { mutationMu.Lock() }

// UnlockMutations releases LockMutations after the caller's transaction ends.
func UnlockMutations() { mutationMu.Unlock() }

func (s *Service) Submit(ctx context.Context, p auth.Principal, project int64, req Request) (Intent, bool, error) {
	mutationMu.Lock()
	defer mutationMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Intent{}, false, ErrStorage
	}
	defer tx.Rollback()
	in, created, err := s.SubmitTx(ctx, tx, p, project, req)
	if err != nil {
		return Intent{}, false, err
	}
	if tx.Commit() != nil {
		return Intent{}, false, ErrStorage
	}
	return in, created, nil
}

// SubmitTx is the single submission path: the same reauthorization, request
// validation, target and reservation checks, and caps that Submit applies,
// performed inside a caller-owned transaction. Callers must already hold
// LockMutations. There is no second way to create a lifecycle intent.
func (s *Service) SubmitTx(ctx context.Context, tx *sql.Tx, p auth.Principal, project int64, req Request) (Intent, bool, error) {
	if err := s.authorize(ctx, tx, p, project, auth.PrincipalSession); err != nil {
		return Intent{}, false, err
	}
	if err := req.validate(); err != nil {
		return Intent{}, false, err
	}
	var id, raw string
	err := tx.QueryRowContext(ctx, `SELECT id,request_json FROM lifecycle_intents WHERE project_id=? AND user_id=? AND request_key=?`, project, p.UserID(), req.RequestKey).Scan(&id, &raw)
	if err == nil {
		if raw != encode(req) {
			return Intent{}, false, ErrConflict
		}
		in, e := loadIntent(ctx, tx, project, id)
		if e != nil {
			return Intent{}, false, e
		}
		if e = s.refresh(ctx, tx, p, &in); e != nil {
			return Intent{}, false, e
		}
		return in, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Intent{}, false, ErrStorage
	}
	if err = s.expireClaims(ctx, tx, p, project); err != nil {
		return Intent{}, false, err
	}
	runtime, err := s.runtime(ctx, tx, project, req.RuntimeID, nil, "", true)
	if err != nil {
		return Intent{}, false, err
	}
	if err = s.validateTarget(ctx, tx, project, req, runtime); err != nil {
		return Intent{}, false, err
	}
	var total, active int
	if tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(CASE WHEN state IN ('requested','claimed','executing') AND expires_at>? THEN 1 ELSE 0 END),0) FROM lifecycle_intents WHERE project_id=?`, stamp(s.now()), project).Scan(&total, &active) != nil {
		return Intent{}, false, ErrStorage
	}
	if total >= 10000 || active >= 32 {
		return Intent{}, false, ErrConflict
	}
	newGeneration := ""
	if req.Operation == "start" || req.Operation == "restart" {
		newGeneration = uuid.NewString()
	}
	at := stamp(s.now())
	in := Intent{SchemaVersion: intentSchema(req), ID: uuid.NewString(), ProjectID: project, Request: req, State: "requested", Revision: 1, CreatedAt: at, ExpiresAt: stamp(s.now().Add(time.Duration(req.TTLSeconds) * time.Second)), UpdatedAt: at, NewGeneration: newGeneration}
	_, err = tx.ExecContext(ctx, `INSERT INTO lifecycle_intents(id,project_id,runtime_id,user_id,session_credential_id,request_key,request_json,new_generation,state,revision,created_at,expires_at,updated_at,actor_kind,actor_user_id,actor_credential_id) VALUES(?,?,?,?,?,?,?,?,'requested',1,?,?,?,?,?,?)`, in.ID, project, req.RuntimeID, p.UserID(), p.SessionCredentialID(), req.RequestKey, encode(req), newGeneration, at, in.ExpiresAt, at, p.Kind(), p.UserID(), p.SafeCredentialID())
	if err != nil {
		return Intent{}, false, ErrStorage
	}
	return in, true, nil
}
func (s *Service) Get(ctx context.Context, p auth.Principal, project int64, id string) (Intent, error) {
	return s.readOrCancel(ctx, p, project, id, 0)
}
func (s *Service) Cancel(ctx context.Context, p auth.Principal, project int64, id string, revision int64) (Intent, error) {
	if revision < 1 {
		return Intent{}, ErrInvalid
	}
	return s.readOrCancel(ctx, p, project, id, revision)
}
func (s *Service) readOrCancel(ctx context.Context, p auth.Principal, project int64, id string, cancel int64) (Intent, error) {
	mutationMu.Lock()
	defer mutationMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Intent{}, ErrStorage
	}
	defer tx.Rollback()
	in, conflict, err := s.ReadOrCancelTx(ctx, tx, p, project, id, cancel)
	if err != nil {
		return Intent{}, err
	}
	if tx.Commit() != nil {
		return Intent{}, ErrStorage
	}
	if conflict {
		return Intent{}, ErrConflict
	}
	return in, nil
}

// ReadOrCancelTx reads, or revision-CAS cancels, one intent inside a
// caller-owned transaction. It applies the same expiry/authority refresh and
// ownership rules as Cancel, keeps `executing` uncancellable, and attributes
// the change to the acting principal. A true conflict flag means the caller
// should commit the refresh it caused and then report the conflict. Callers
// must already hold LockMutations.
func (s *Service) ReadOrCancelTx(ctx context.Context, tx *sql.Tx, p auth.Principal, project int64, id string, cancel int64) (Intent, bool, error) {
	if err := s.authorize(ctx, tx, p, project, auth.PrincipalSession); err != nil {
		return Intent{}, false, err
	}
	in, err := loadIntent(ctx, tx, project, id)
	if err != nil {
		return Intent{}, false, err
	}
	if owner(ctx, tx, in, p) != nil {
		return Intent{}, false, ErrUnavailable
	}
	if err = s.refresh(ctx, tx, p, &in); err != nil {
		return Intent{}, false, err
	}
	if cancel > 0 && !(in.State == "cancelled" && cancel == in.Revision-1) {
		if terminal(in.State) || in.State == "executing" || in.Revision != cancel {
			return in, true, nil
		}
		if err = s.change(ctx, tx, p, &in, "cancelled", "cancelled", ""); err != nil {
			return Intent{}, false, err
		}
	}
	return in, false, nil
}
func (s *Service) Events(ctx context.Context, p auth.Principal, project int64, id string) ([]Event, error) {
	mutationMu.Lock()
	defer mutationMu.Unlock()
	// The read transaction reauthorizes independently of any preceding Get.
	tx, err := s.begin(ctx, p, project, auth.PrincipalSession)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	in, err := loadIntent(ctx, tx, project, id)
	if err != nil {
		return nil, err
	}
	if owner(ctx, tx, in, p) != nil {
		return nil, ErrUnavailable
	}
	if err = s.refresh(ctx, tx, p, &in); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT revision,state,reason,created_at FROM lifecycle_intent_events WHERE intent_id=? ORDER BY revision LIMIT 8`, id)
	if err != nil {
		return nil, ErrStorage
	}
	events := []Event{}
	for rows.Next() {
		var e Event
		if rows.Scan(&e.Revision, &e.State, &e.Reason, &e.CreatedAt) != nil {
			rows.Close()
			return nil, ErrStorage
		}
		events = append(events, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || tx.Commit() != nil {
		return nil, ErrStorage
	}
	return events, nil
}

// Release expired claim reservations in a bounded, authorized transaction.
// An expiry records unknown outcome, never a successful local cancellation.
func (s *Service) expireClaims(ctx context.Context, tx *sql.Tx, p auth.Principal, project int64) error {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM lifecycle_intents WHERE project_id=? AND state IN ('claimed','executing') AND expires_at<=? ORDER BY expires_at,id LIMIT 32`, project, stamp(s.now()))
	if err != nil {
		return ErrStorage
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) != nil {
			rows.Close()
			return ErrStorage
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return ErrStorage
	}
	for _, id := range ids {
		in, e := loadIntent(ctx, tx, project, id)
		if e != nil {
			return e
		}
		if e = s.change(ctx, tx, p, &in, "expired", "outcome_unknown", ""); e != nil {
			return e
		}
	}
	return nil
}
