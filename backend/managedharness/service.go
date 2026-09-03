// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

// Package managedharness owns the durable control-plane contract for harness
// sessions. It deliberately contains no daemon or vendor process transport.
package managedharness

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/agentmessage"
	harnessplugin "github.com/inspr-at/paimos/backend/agentmessage/harness"
	"github.com/inspr-at/paimos/backend/models"
)

const (
	ManagementManaged   = "managed"
	ManagementUnmanaged = "unmanaged"
	RoleCoordinator     = "coordinator"
	RoleWorker          = "worker"
	SteerNone           = "none"
	SteerOwned          = "owned"
	SteerCodexExternal  = "codex_external"
	PhaseStarting       = "starting"
	PhaseWorking        = "working"
	PhaseYielded        = "yielded"
	PhaseStopping       = "stopping"
	PhaseStopped        = "stopped"
	ControlInterrupt    = "interrupt"
	ControlStop         = "stop"
	ControlPending      = "pending"
	ControlClaimed      = "claimed"
	ControlApplied      = "applied"
	ControlRejected     = "rejected"
	ReasonApplied       = "applied"
	ReasonNotRunning    = "not_running"
	ReasonUnsupported   = "unsupported"
	ReasonOwnershipLost = "ownership_lost"
	ReasonFailed        = "failed"
	ActivityBusy        = "busy"
	ActivityIdle        = "idle"
	ActivityUnknown     = "unknown"
	ActivityDead        = "dead"
	ActivityUnreported  = "unreported"
	ActivityAdapter     = "adapter_activity"
	ActivityCompleted   = "turn_completed"
	ActivityStale       = "heartbeat_stale"
	ActivityOldEvidence = "stale_evidence"
	ActivityMalformed   = "malformed_evidence"
	ActivityUnmanaged   = "unmanaged_evidence"
	ClosedStopped       = "stopped"
	ClosedProcessExited = "process_exited"
	ClosedProcessFailed = "process_failed"
	ClosedOwnershipLost = "ownership_lost"

	CodeInvalid               = "harness_session_invalid"
	CodeNotFound              = "harness_session_not_found"
	CodeConflict              = "harness_session_conflict"
	CodeCapabilityInvalid     = "harness_session_capability_invalid"
	CodeCapabilityUnavailable = "harness_session_capability_unavailable"
)

var stableValue = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)

// registerMu closes the short in-process gap between claiming an active
// harness_sessions identity and attaching its encrypted target FK. The partial
// unique indexes remain the durable cross-process authority; PAIMOS runs one
// SQLite-owning backend process, so serializing this low-volume control-plane
// operation also guarantees that idempotent HTTP replays never observe a
// half-initialized managed row.
var registerMu sync.Mutex

type Error struct {
	Code string
	Err  error
}

func (e *Error) Error() string        { return e.Err.Error() }
func (e *Error) Unwrap() error        { return e.Err }
func coded(code, detail string) error { return &Error{Code: code, Err: errors.New(detail)} }
func IsCode(err error, code string) bool {
	var target *Error
	return errors.As(err, &target) && target.Code == code
}
func ErrorCode(err error) string {
	var target *Error
	if errors.As(err, &target) {
		return target.Code
	}
	return ""
}

type RegisterInput struct {
	ProjectID       int64
	AgentName       string
	Harness         string
	Host            string
	SessionRef      string
	WorkerLease     string
	MessageTargetID string
	ManagementMode  string
	Role            string
	SteerMode       string
	Capabilities    models.HarnessCapabilities
}

type YieldResult struct {
	Session  models.HarnessSession   `json:"session"`
	Controls []models.HarnessControl `json:"controls"`
}

// ActivityEvidence is the bounded, content-free tail of one owned adapter
// generation. Sequence is monotonic in agentd and kind is a closed event set.
type ActivityEvidence struct {
	Sequence int64  `json:"sequence"`
	Kind     string `json:"kind"`
}

type Service struct{ db *sql.DB }

func NewService(database *sql.DB) *Service { return &Service{db: database} }

func normalizeRegister(in RegisterInput) RegisterInput {
	in.AgentName = strings.TrimSpace(in.AgentName)
	in.Harness = strings.ToLower(strings.TrimSpace(in.Harness))
	in.Host = strings.TrimSpace(in.Host)
	in.SessionRef = strings.TrimSpace(in.SessionRef)
	in.WorkerLease = strings.TrimSpace(in.WorkerLease)
	in.MessageTargetID = strings.TrimSpace(in.MessageTargetID)
	in.ManagementMode = strings.ToLower(strings.TrimSpace(in.ManagementMode))
	in.Role = strings.ToLower(strings.TrimSpace(in.Role))
	in.SteerMode = strings.ToLower(strings.TrimSpace(in.SteerMode))
	return in
}

func validateRegister(in RegisterInput) error {
	if in.ProjectID <= 0 || !safeStable(in.AgentName, 64) || !safeStable(in.Harness, 64) || !safeStable(in.Host, 128) {
		return coded(CodeInvalid, "project, agent, harness, and host must be safe stable identifiers")
	}
	if in.Harness == "openclaw" {
		return coded(CodeInvalid, "OpenClaw is not a harness-session transport")
	}
	if !utf8.ValidString(in.SessionRef) || len([]byte(in.SessionRef)) < 1 || len([]byte(in.SessionRef)) > 256 ||
		strings.ContainsAny(in.SessionRef, "\x00\r\n") || strings.Contains(in.SessionRef, "://") || strings.HasPrefix(in.SessionRef, "/") {
		return coded(CodeInvalid, "harness session reference must be opaque, not a URL, private socket, path, or credential")
	}
	if !ValidWorkerLease(in.WorkerLease) {
		return coded(CodeInvalid, "harness worker lease is invalid")
	}
	if in.ManagementMode != ManagementManaged && in.ManagementMode != ManagementUnmanaged {
		return coded(CodeInvalid, "management mode must be managed or unmanaged")
	}
	if in.Role != RoleCoordinator && in.Role != RoleWorker {
		return coded(CodeInvalid, "role must be coordinator or worker")
	}
	if in.SteerMode != SteerNone && in.SteerMode != SteerOwned && in.SteerMode != SteerCodexExternal {
		return coded(CodeInvalid, "steer mode must be none, owned, or codex_external")
	}
	c := in.Capabilities
	if c.Steer != (in.SteerMode != SteerNone) || (c.Steer && (!c.Inbox || !c.Status)) || ((c.Interrupt || c.Stop) && !c.Status) {
		return coded(CodeCapabilityInvalid, "advertised steer requires inbox and status; interrupt and stop require status")
	}
	if in.ManagementMode == ManagementManaged {
		if in.MessageTargetID != "" {
			return coded(CodeInvalid, "managed sessions create their own encrypted message target")
		}
		if !c.Status || (c.Steer && in.SteerMode != SteerOwned) {
			return coded(CodeCapabilityInvalid, "managed sessions require status and use owned steer")
		}
		return nil
	}
	if c.Interrupt || c.Stop {
		return coded(CodeCapabilityInvalid, "unmanaged sessions cannot advertise owned interrupt or stop")
	}
	if c.Steer && (in.Harness != "codex" || in.SteerMode != SteerCodexExternal) {
		return coded(CodeCapabilityInvalid, "unmanaged steer is supported only for documented Codex external steer")
	}
	if c.Inbox && in.MessageTargetID == "" {
		return coded(CodeCapabilityInvalid, "unmanaged inbox requires an existing encrypted message target")
	}
	return nil
}

// ValidateRegistration applies the same closed capability and ownership rules
// used by Register without touching storage. The CLI uses it to fail closed
// before making a network request.
func ValidateRegistration(in RegisterInput) error { return validateRegister(normalizeRegister(in)) }

func safeStable(value string, limit int) bool {
	return utf8.ValidString(value) && len([]byte(value)) >= 1 && len([]byte(value)) <= limit && stableValue.MatchString(value)
}
func digestRef(projectID int64, harness, host, ref string) []byte {
	sum := sha256.Sum256([]byte(fmt.Sprintf("paimos:harness-session-ref:v1\x00%d\x00%s\x00%s\x00%s", projectID, harness, host, ref)))
	return sum[:]
}

func ValidWorkerLease(value string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(raw) == 32 && base64.RawURLEncoding.EncodeToString(raw) == value
}

func digestWorkerLease(projectID int64, agent, sessionID, lease string) []byte {
	sum := sha256.Sum256([]byte(fmt.Sprintf("paimos:harness-worker-lease:v1\x00%d\x00%s\x00%s\x00%s", projectID, agent, sessionID, lease)))
	return sum[:]
}
func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

const sessionColumns = `id,project_id,project_agent_id,agent_name,harness,host,COALESCE(message_target_id,''),management_mode,role,steer_mode,
	advertised_inbox,advertised_status,advertised_steer,advertised_interrupt,advertised_stop,phase,COALESCE(heartbeat_at,''),
	activity_state,activity_reason,activity_event_kind,COALESCE(activity_at,''),activity_sequence,closed_reason,
	COALESCE(yielded_at,''),yield_sequence,revision,created_at,updated_at`

func scanSession(row interface{ Scan(...any) error }) (models.HarnessSession, error) {
	var out models.HarnessSession
	var inbox, status, steer, interrupt, stop int
	err := row.Scan(&out.ID, &out.ProjectID, &out.ProjectAgentID, &out.AgentName, &out.Harness, &out.Host, &out.MessageTargetID,
		&out.ManagementMode, &out.Role, &out.SteerMode, &inbox, &status, &steer, &interrupt, &stop, &out.Phase,
		&out.HeartbeatAt, &out.ActivityState, &out.ActivityReason, &out.ActivityKind, &out.ActivityAt, &out.ActivitySequence, &out.ClosedReason,
		&out.YieldedAt, &out.YieldSequence, &out.Revision, &out.CreatedAt, &out.UpdatedAt)
	out.Capabilities = models.HarnessCapabilities{Inbox: inbox == 1, Status: status == 1, Steer: steer == 1, Interrupt: interrupt == 1, Stop: stop == 1}
	if parsed, parseErr := time.Parse(time.RFC3339Nano, out.ActivityAt); parseErr == nil {
		age := int64(time.Since(parsed).Seconds())
		if age < 0 {
			age = 0
		}
		out.ActivityAge = &age
	}
	return out, err
}

func validActivityKind(kind string) bool {
	switch kind {
	case "session_started", "turn_started", "tool_started", "control_applied", "turn_completed":
		return true
	default:
		return false
	}
}

func deriveActivity(current models.HarnessSession, evidence ActivityEvidence) (state, reason, kind string, sequence int64, fresh bool) {
	kind = strings.ToLower(strings.TrimSpace(evidence.Kind))
	sequence = current.ActivitySequence
	if current.ManagementMode != ManagementManaged {
		return ActivityUnknown, ActivityUnmanaged, current.ActivityKind, sequence, false
	}
	if evidence.Sequence == 0 && kind == "" {
		return ActivityUnknown, ActivityUnreported, current.ActivityKind, sequence, false
	}
	if evidence.Sequence <= 0 || !validActivityKind(kind) || evidence.Sequence == current.ActivitySequence && kind != current.ActivityKind {
		return ActivityUnknown, ActivityMalformed, current.ActivityKind, sequence, false
	}
	if evidence.Sequence < current.ActivitySequence {
		return current.ActivityState, current.ActivityReason, current.ActivityKind, sequence, false
	}
	if evidence.Sequence == current.ActivitySequence {
		state, reason = activityFromKind(current.ActivityKind)
		return state, reason, current.ActivityKind, sequence, false
	}
	sequence = evidence.Sequence
	state, reason = activityFromKind(kind)
	return state, reason, kind, sequence, kind != "session_started"
}

func activityFromKind(kind string) (state, reason string) {
	switch kind {
	case "turn_completed":
		return ActivityIdle, ActivityCompleted
	case "turn_started", "tool_started", "control_applied":
		return ActivityBusy, ActivityAdapter
	default:
		return ActivityUnknown, ActivityUnreported
	}
}

func appendSessionEventTx(ctx context.Context, tx *sql.Tx, session models.HarnessSession, operation string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO harness_session_events(
		harness_session_id,event_sequence,operation,phase,activity_state,activity_reason,activity_event_kind,activity_sequence,closed_reason)
		VALUES(?,?,?,?,?,?,?,?,?)`, session.ID, session.Revision, operation, session.Phase, session.ActivityState,
		session.ActivityReason, session.ActivityKind, session.ActivitySequence, session.ClosedReason)
	return err
}

func (s *Service) validateTarget(ctx context.Context, in RegisterInput) (string, error) {
	if !in.Capabilities.Inbox {
		return "", nil
	}
	bus := agentmessage.NewService(s.db)
	if in.ManagementMode == ManagementManaged {
		level := harnessplugin.LevelSimple
		if in.Capabilities.Steer {
			level = harnessplugin.LevelSteer
		}
		address := in.Harness + ":" + in.AgentName
		targets, err := bus.ListTargets(ctx, in.ProjectID, address)
		if err != nil {
			return "", err
		}
		standby := false
		for _, target := range targets {
			if target.Enabled && target.Role == "primary" && agentmessage.IsManagedAgentdAdapter(target.Adapter) {
				standby = true
			}
			if target.Role != "primary" || target.Adapter != agentmessage.AdapterManagedHarness ||
				target.TargetKind != agentmessage.TargetKindHarnessSession {
				continue
			}
			matches, matchErr := bus.TargetRefMatches(ctx, in.ProjectID, target.ID, in.SessionRef)
			if matchErr != nil {
				return "", matchErr
			}
			if matches {
				// Stable external identities retain their encrypted target version and
				// its immutable MaximumLevel cap. Delivery rows snapshot this ID, so
				// reuse preserves FIFO across stopped harness-session generations
				// without exposing the ref or escalating prior receiver policy.
				return target.ID, nil
			}
		}
		target, err := bus.RegisterTarget(ctx, agentmessage.RegisterTargetInput{
			ProjectID: in.ProjectID, Address: address,
			Adapter: agentmessage.AdapterManagedHarness, TargetKind: agentmessage.TargetKindHarnessSession,
			TargetRef: in.SessionRef, MaximumLevel: level, Role: "primary", Standby: standby,
		})
		if err != nil {
			return "", err
		}
		return target.ID, nil
	}
	target, err := bus.GetTarget(ctx, in.ProjectID, in.MessageTargetID)
	if err != nil {
		return "", coded(CodeCapabilityInvalid, "unmanaged message target is unavailable")
	}
	if !target.Enabled || target.Address != in.Harness+":"+in.AgentName {
		return "", coded(CodeCapabilityInvalid, "unmanaged message target does not own this harness address")
	}
	matches, err := bus.TargetRefMatches(ctx, in.ProjectID, target.ID, in.SessionRef)
	if err != nil || !matches {
		return "", coded(CodeCapabilityInvalid, "unmanaged session reference does not match the encrypted message target")
	}
	plugin, err := harnessplugin.Lookup(target.Adapter)
	if err != nil {
		return "", coded(CodeCapabilityInvalid, "unmanaged message target adapter is unavailable")
	}
	if in.Capabilities.Steer && (target.Adapter != agentmessage.AdapterCodex || target.MaximumLevel != harnessplugin.LevelSteer || plugin.MaximumLevel() != harnessplugin.LevelSteer) {
		return "", coded(CodeCapabilityInvalid, "unmanaged steer exceeds the adapter or target MaximumLevel cap")
	}
	return target.ID, nil
}

func (s *Service) Register(ctx context.Context, raw RegisterInput) (models.HarnessSession, bool, error) {
	if s == nil || s.db == nil {
		return models.HarnessSession{}, false, coded(CodeInvalid, "harness session service is unavailable")
	}
	in := normalizeRegister(raw)
	if err := validateRegister(in); err != nil {
		return models.HarnessSession{}, false, err
	}
	registerMu.Lock()
	defer registerMu.Unlock()
	digest := digestRef(in.ProjectID, in.Harness, in.Host, in.SessionRef)
	existing, err := scanSession(s.db.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM harness_sessions WHERE project_id=? AND harness=? AND host=? AND session_ref_digest=? AND phase<>'stopped'`, in.ProjectID, in.Harness, in.Host, digest))
	if err == nil {
		if ok, verifyErr := s.VerifyWorkerLease(ctx, existing.ProjectID, existing.ID, in.WorkerLease); verifyErr != nil || !ok {
			return models.HarnessSession{}, false, coded(CodeInvalid, "harness worker lease is invalid")
		}
		if !sameRegistration(existing, in) {
			return models.HarnessSession{}, false, coded(CodeConflict, "stable external identity is already registered with different agent, ownership, role, or advertised capabilities")
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return models.HarnessSession{}, false, err
	}
	var active int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM harness_sessions WHERE project_id=? AND harness=? AND agent_name=? AND phase<>'stopped'`, in.ProjectID, in.Harness, in.AgentName).Scan(&active); err != nil {
		return models.HarnessSession{}, false, err
	}
	if active != 0 {
		return models.HarnessSession{}, false, coded(CodeConflict, "an active harness session already owns this agent address")
	}
	var agentID int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM project_agents WHERE project_id=? AND name=?`, in.ProjectID, in.AgentName).Scan(&agentID); err != nil {
		return models.HarnessSession{}, false, coded(CodeNotFound, "agent is not registered in this project")
	}
	targetID := ""
	if in.Capabilities.Inbox {
		targetID, err = s.validateTarget(ctx, in)
		if err != nil {
			return models.HarnessSession{}, false, err
		}
	}
	id := uuid.NewString()
	workerDigest := digestWorkerLease(in.ProjectID, in.AgentName, id, in.WorkerLease)
	c := in.Capabilities
	activityReason := ActivityUnreported
	if in.ManagementMode == ManagementUnmanaged {
		activityReason = ActivityUnmanaged
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO harness_sessions(id,project_id,project_agent_id,agent_name,harness,host,session_ref_digest,worker_lease_digest,message_target_id,management_mode,role,steer_mode,
		advertised_inbox,advertised_status,advertised_steer,advertised_interrupt,advertised_stop,phase,activity_reason) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, in.ProjectID, agentID, in.AgentName, in.Harness, in.Host, digest, workerDigest, nullString(targetID), in.ManagementMode, in.Role, in.SteerMode,
		boolInt(c.Inbox), boolInt(c.Status), boolInt(c.Steer), boolInt(c.Interrupt), boolInt(c.Stop), PhaseStarting, activityReason)
	if err != nil {
		const identityConstraint = "UNIQUE constraint failed: harness_sessions.project_id, harness_sessions.harness, harness_sessions.host, harness_sessions.session_ref_digest"
		const addressConstraint = "UNIQUE constraint failed: harness_sessions.project_id, harness_sessions.harness, harness_sessions.agent_name"
		identityConflict := strings.Contains(err.Error(), identityConstraint)
		addressConflict := strings.Contains(err.Error(), addressConstraint)
		if identityConflict || addressConflict {
			replay, replayErr := scanSession(s.db.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM harness_sessions WHERE project_id=? AND harness=? AND host=? AND session_ref_digest=? AND phase<>'stopped'`, in.ProjectID, in.Harness, in.Host, digest))
			if replayErr == nil && sameRegistration(replay, in) {
				if ok, _ := s.VerifyWorkerLease(ctx, replay.ProjectID, replay.ID, in.WorkerLease); ok {
					return replay, false, nil
				}
				return models.HarnessSession{}, false, coded(CodeInvalid, "harness worker lease is invalid")
			}
			if replayErr != nil && !errors.Is(replayErr, sql.ErrNoRows) {
				return models.HarnessSession{}, false, replayErr
			}
		}
		switch {
		case identityConflict:
			return models.HarnessSession{}, false, coded(CodeConflict, "an active harness session already owns this stable external identity")
		case addressConflict:
			return models.HarnessSession{}, false, coded(CodeConflict, "an active harness session already owns this agent address")
		}
		return models.HarnessSession{}, false, err
	}
	created, err := s.GetByID(ctx, id)
	return created, true, err
}

func (s *Service) VerifyWorkerLease(ctx context.Context, projectID int64, sessionID, lease string) (bool, error) {
	if projectID <= 0 || !ValidWorkerLease(strings.TrimSpace(lease)) {
		return false, nil
	}
	var agent string
	var stored []byte
	if err := s.db.QueryRowContext(ctx, `SELECT agent_name,worker_lease_digest FROM harness_sessions WHERE project_id=? AND id=?`, projectID, strings.TrimSpace(sessionID)).Scan(&agent, &stored); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	want := digestWorkerLease(projectID, agent, sessionID, strings.TrimSpace(lease))
	return len(stored) == sha256.Size && subtle.ConstantTimeCompare(stored, want) == 1, nil
}

func sameRegistration(s models.HarnessSession, in RegisterInput) bool {
	return s.ProjectID == in.ProjectID && s.AgentName == in.AgentName && s.Harness == in.Harness && s.Host == in.Host &&
		s.ManagementMode == in.ManagementMode && s.Role == in.Role && s.SteerMode == in.SteerMode && s.Capabilities == in.Capabilities &&
		(in.MessageTargetID == "" || s.MessageTargetID == in.MessageTargetID)
}

func (s *Service) Get(ctx context.Context, projectID int64, id string) (models.HarnessSession, error) {
	out, err := scanSession(s.db.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM harness_sessions WHERE project_id=? AND id=?`, projectID, strings.TrimSpace(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return out, coded(CodeNotFound, "harness session not found")
	}
	return out, err
}
func (s *Service) GetByID(ctx context.Context, id string) (models.HarnessSession, error) {
	out, err := scanSession(s.db.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM harness_sessions WHERE id=?`, strings.TrimSpace(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return out, coded(CodeNotFound, "harness session not found")
	}
	return out, err
}
func (s *Service) List(ctx context.Context, projectID int64) ([]models.HarnessSession, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+sessionColumns+` FROM harness_sessions WHERE project_id=? ORDER BY created_at,id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.HarnessSession{}
	for rows.Next() {
		value, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

func (s *Service) Heartbeat(ctx context.Context, id, phase string) (models.HarnessSession, error) {
	return s.HeartbeatWithActivity(ctx, id, phase, ActivityEvidence{})
}

func (s *Service) HeartbeatWithActivity(ctx context.Context, id, phase string, evidence ActivityEvidence) (models.HarnessSession, error) {
	phase = strings.ToLower(strings.TrimSpace(phase))
	if phase != PhaseStarting && phase != PhaseWorking && phase != PhaseYielded && phase != PhaseStopping {
		return models.HarnessSession{}, coded(CodeInvalid, "heartbeat phase must be starting, working, yielded, or stopping")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return models.HarnessSession{}, err
	}
	defer tx.Rollback()
	current, err := scanSession(tx.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM harness_sessions WHERE id=?`, strings.TrimSpace(id)))
	if err != nil || current.Phase == PhaseStopped || !current.Capabilities.Status {
		return models.HarnessSession{}, coded(CodeCapabilityUnavailable, "session cannot report status")
	}
	state, reason, kind, sequence, newEvidence := deriveActivity(current, evidence)
	projectionChanged := state != current.ActivityState || reason != current.ActivityReason ||
		kind != current.ActivityKind || sequence != current.ActivitySequence
	activityAt := any(nil)
	if current.ActivityAt != "" {
		activityAt = current.ActivityAt
	}
	if newEvidence {
		activityAt = time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	}
	result, err := tx.ExecContext(ctx, `UPDATE harness_sessions SET phase=?,heartbeat_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
		activity_state=?,activity_reason=?,activity_event_kind=?,activity_at=?,activity_sequence=?,closed_reason='',
		updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),revision=revision+1
		WHERE id=? AND advertised_status=1 AND phase<>'stopped' AND revision=?`, phase, state, reason, kind, activityAt, sequence, strings.TrimSpace(id), current.Revision)
	if err != nil {
		return models.HarnessSession{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return models.HarnessSession{}, coded(CodeConflict, "harness session changed during heartbeat")
	}
	out, err := scanSession(tx.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM harness_sessions WHERE id=?`, strings.TrimSpace(id)))
	if err != nil {
		return models.HarnessSession{}, err
	}
	if projectionChanged {
		if err := appendSessionEventTx(ctx, tx, out, "heartbeat"); err != nil {
			return models.HarnessSession{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return models.HarnessSession{}, err
	}
	return out, nil
}

const controlColumns = `id,harness_session_id,sequence,kind,state,reason,requested_by_user_id,requested_at,COALESCE(claimed_at,''),COALESCE(completed_at,'')`

func scanControl(row interface{ Scan(...any) error }) (models.HarnessControl, error) {
	var out models.HarnessControl
	err := row.Scan(&out.ID, &out.HarnessSessionID, &out.Sequence, &out.Kind, &out.State, &out.Reason, &out.RequestedByUserID, &out.RequestedAt, &out.ClaimedAt, &out.CompletedAt)
	return out, err
}

// GetControl returns the non-secret operator outcome for one exact
// project/session/control tuple. The join is intentionally project-scoped so a
// public UUID from another project cannot be used as an existence oracle.
func (s *Service) GetControl(ctx context.Context, projectID int64, sessionID, controlID string) (models.HarnessControlOutcome, error) {
	var out models.HarnessControlOutcome
	err := s.db.QueryRowContext(ctx, `SELECT c.id,s.project_id,c.harness_session_id,c.sequence,c.kind,c.state,c.reason,
		c.requested_at,COALESCE(c.claimed_at,''),COALESCE(c.completed_at,'')
		FROM harness_session_controls c
		JOIN harness_sessions s ON s.id=c.harness_session_id
		WHERE s.project_id=? AND s.id=? AND c.id=?`, projectID, strings.TrimSpace(sessionID), strings.TrimSpace(controlID)).
		Scan(&out.ID, &out.ProjectID, &out.HarnessSessionID, &out.Sequence, &out.Kind, &out.State, &out.Reason,
			&out.RequestedAt, &out.ClaimedAt, &out.CompletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return models.HarnessControlOutcome{}, coded(CodeNotFound, "harness control not found")
	}
	if err != nil {
		return models.HarnessControlOutcome{}, err
	}
	out.CorrelationID = out.ID
	if out.State == ControlApplied || out.State == ControlRejected {
		out.Outcome = out.State
	}
	return out, nil
}

func (s *Service) RequestControl(ctx context.Context, sessionID, kind string, actor int64) (models.HarnessControl, error) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if (kind != ControlInterrupt && kind != ControlStop) || actor <= 0 {
		return models.HarnessControl{}, coded(CodeInvalid, "control kind and actor are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return models.HarnessControl{}, err
	}
	defer tx.Rollback()
	var mode string
	var interrupt, stop int
	if err := tx.QueryRowContext(ctx, `SELECT management_mode,advertised_interrupt,advertised_stop FROM harness_sessions WHERE id=? AND phase<>'stopped'`, sessionID).Scan(&mode, &interrupt, &stop); err != nil {
		return models.HarnessControl{}, coded(CodeNotFound, "active harness session not found")
	}
	if mode != ManagementManaged || (kind == ControlInterrupt && interrupt != 1) || (kind == ControlStop && stop != 1) {
		return models.HarnessControl{}, coded(CodeCapabilityUnavailable, "owned control is unavailable for this session")
	}
	prior, priorErr := scanControl(tx.QueryRowContext(ctx, `SELECT `+controlColumns+` FROM harness_session_controls WHERE harness_session_id=? AND kind=? AND state IN ('pending','claimed')`, sessionID, kind))
	if priorErr == nil {
		return prior, nil
	}
	if !errors.Is(priorErr, sql.ErrNoRows) {
		return models.HarnessControl{}, priorErr
	}
	var sequence int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0)+1 FROM harness_session_controls WHERE harness_session_id=?`, sessionID).Scan(&sequence); err != nil {
		return models.HarnessControl{}, err
	}
	id := uuid.NewString()
	if _, err := tx.ExecContext(ctx, `INSERT INTO harness_session_controls(id,harness_session_id,sequence,kind,requested_by_user_id) VALUES(?,?,?,?,?)`, id, sessionID, sequence, kind, actor); err != nil {
		return models.HarnessControl{}, err
	}
	out, err := scanControl(tx.QueryRowContext(ctx, `SELECT `+controlColumns+` FROM harness_session_controls WHERE id=?`, id))
	if err != nil {
		return out, err
	}
	if err := tx.Commit(); err != nil {
		return out, err
	}
	return out, nil
}

func (s *Service) Yield(ctx context.Context, sessionID string) (YieldResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return YieldResult{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE harness_sessions SET phase='yielded',yield_sequence=yield_sequence+1,yielded_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),heartbeat_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),revision=revision+1 WHERE id=? AND management_mode='managed' AND advertised_status=1 AND phase<>'stopped'`, sessionID)
	if err != nil {
		return YieldResult{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		existing, getErr := scanSession(tx.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM harness_sessions WHERE id=?`, strings.TrimSpace(sessionID)))
		if getErr == nil && existing.Phase == PhaseStopped {
			return YieldResult{Session: existing, Controls: []models.HarnessControl{}}, nil
		}
		return YieldResult{}, coded(CodeCapabilityUnavailable, "only an active managed session can yield")
	}
	claimResult, err := tx.ExecContext(ctx, `UPDATE harness_session_controls SET state='claimed',claimed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE harness_session_id=? AND state='pending'`, sessionID)
	if err != nil {
		return YieldResult{}, err
	}
	claimed, err := claimResult.RowsAffected()
	if err != nil {
		return YieldResult{}, err
	}
	session, err := scanSession(tx.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM harness_sessions WHERE id=?`, sessionID))
	if err != nil {
		return YieldResult{}, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+controlColumns+` FROM harness_session_controls WHERE harness_session_id=? AND state='claimed' ORDER BY sequence`, sessionID)
	if err != nil {
		return YieldResult{}, err
	}
	controls := []models.HarnessControl{}
	for rows.Next() {
		control, err := scanControl(rows)
		if err != nil {
			rows.Close()
			return YieldResult{}, err
		}
		controls = append(controls, control)
	}
	if err := rows.Close(); err != nil {
		return YieldResult{}, err
	}
	if claimed > 0 {
		if err := appendSessionEventTx(ctx, tx, session, "yield"); err != nil {
			return YieldResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return YieldResult{}, err
	}
	return YieldResult{Session: session, Controls: controls}, nil
}

func (s *Service) CompleteControl(ctx context.Context, sessionID, controlID, outcome, reason string) (models.HarnessControl, error) {
	outcome = strings.ToLower(strings.TrimSpace(outcome))
	reason = strings.ToLower(strings.TrimSpace(reason))
	if outcome != ControlApplied && outcome != ControlRejected {
		return models.HarnessControl{}, coded(CodeInvalid, "control outcome must be applied or rejected")
	}
	valid := map[string]bool{ReasonApplied: true, ReasonNotRunning: true, ReasonUnsupported: true, ReasonOwnershipLost: true, ReasonFailed: true}
	if !valid[reason] || (outcome == ControlApplied) != (reason == ReasonApplied) {
		return models.HarnessControl{}, coded(CodeInvalid, "control outcome and closed reason do not match")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return models.HarnessControl{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE harness_session_controls SET state=?,reason=?,completed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=? AND harness_session_id=? AND state='claimed'`, outcome, reason, controlID, sessionID)
	if err != nil {
		return models.HarnessControl{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		existing, getErr := scanControl(tx.QueryRowContext(ctx, `SELECT `+controlColumns+` FROM harness_session_controls WHERE id=?`, controlID))
		if getErr == nil && existing.HarnessSessionID == sessionID && existing.State == outcome && existing.Reason == reason {
			return existing, nil
		}
		return models.HarnessControl{}, coded(CodeConflict, "control is not claimed by this harness session")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE harness_sessions SET revision=revision+1,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, sessionID); err != nil {
		return models.HarnessControl{}, err
	}
	session, err := scanSession(tx.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM harness_sessions WHERE id=?`, sessionID))
	if err != nil {
		return models.HarnessControl{}, err
	}
	if err := appendSessionEventTx(ctx, tx, session, "control_completed"); err != nil {
		return models.HarnessControl{}, err
	}
	out, err := scanControl(tx.QueryRowContext(ctx, `SELECT `+controlColumns+` FROM harness_session_controls WHERE id=?`, controlID))
	if err != nil {
		return models.HarnessControl{}, err
	}
	if err := tx.Commit(); err != nil {
		return models.HarnessControl{}, err
	}
	return out, nil
}

func (s *Service) Stop(ctx context.Context, id string) (models.HarnessSession, error) {
	return s.StopWithReason(ctx, id, ClosedStopped)
}

func (s *Service) StopWithReason(ctx context.Context, id, closedReason string) (models.HarnessSession, error) {
	closedReason = strings.ToLower(strings.TrimSpace(closedReason))
	validReason := map[string]bool{ClosedStopped: true, ClosedProcessExited: true, ClosedProcessFailed: true, ClosedOwnershipLost: true}
	if !validReason[closedReason] {
		return models.HarnessSession{}, coded(CodeInvalid, "stopped harness session requires a closed reason")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return models.HarnessSession{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE harness_sessions SET phase='stopped',activity_state='dead',activity_reason=?,closed_reason=?,
		updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),revision=revision+1 WHERE id=? AND phase<>'stopped'`, closedReason, closedReason, id)
	if err != nil {
		return models.HarnessSession{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		// Exact retries are read-only: a daemon may have committed the remote
		// stop immediately before crashing, then recover its local terminal row.
		existing, getErr := scanSession(tx.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM harness_sessions WHERE id=?`, strings.TrimSpace(id)))
		if getErr == nil && existing.Phase == PhaseStopped {
			return existing, nil
		}
		return models.HarnessSession{}, coded(CodeConflict, "harness session is already stopped or missing")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE harness_session_controls SET state='rejected',reason='ownership_lost',claimed_at=COALESCE(claimed_at,strftime('%Y-%m-%dT%H:%M:%fZ','now')),completed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE harness_session_id=? AND state IN ('pending','claimed')`, id); err != nil {
		return models.HarnessSession{}, err
	}
	out, err := scanSession(tx.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM harness_sessions WHERE id=?`, strings.TrimSpace(id)))
	if err != nil {
		return models.HarnessSession{}, err
	}
	if err := appendSessionEventTx(ctx, tx, out, "stop"); err != nil {
		return models.HarnessSession{}, err
	}
	if err := tx.Commit(); err != nil {
		return models.HarnessSession{}, err
	}
	return out, nil
}

func Address(session models.HarnessSession) string {
	return fmt.Sprintf("%s:%s", session.Harness, session.AgentName)
}
