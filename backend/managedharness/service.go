// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

// Package managedharness owns the durable control-plane contract for harness
// sessions. It deliberately contains no daemon or vendor process transport.
package managedharness

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
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

type Service struct{ db *sql.DB }

func NewService(database *sql.DB) *Service { return &Service{db: database} }

func normalizeRegister(in RegisterInput) RegisterInput {
	in.AgentName = strings.TrimSpace(in.AgentName)
	in.Harness = strings.ToLower(strings.TrimSpace(in.Harness))
	in.Host = strings.TrimSpace(in.Host)
	in.SessionRef = strings.TrimSpace(in.SessionRef)
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
	advertised_inbox,advertised_status,advertised_steer,advertised_interrupt,advertised_stop,phase,COALESCE(heartbeat_at,''),COALESCE(yielded_at,''),yield_sequence,revision,created_at,updated_at`

func scanSession(row interface{ Scan(...any) error }) (models.HarnessSession, error) {
	var out models.HarnessSession
	var inbox, status, steer, interrupt, stop int
	err := row.Scan(&out.ID, &out.ProjectID, &out.ProjectAgentID, &out.AgentName, &out.Harness, &out.Host, &out.MessageTargetID,
		&out.ManagementMode, &out.Role, &out.SteerMode, &inbox, &status, &steer, &interrupt, &stop, &out.Phase,
		&out.HeartbeatAt, &out.YieldedAt, &out.YieldSequence, &out.Revision, &out.CreatedAt, &out.UpdatedAt)
	out.Capabilities = models.HarnessCapabilities{Inbox: inbox == 1, Status: status == 1, Steer: steer == 1, Interrupt: interrupt == 1, Stop: stop == 1}
	return out, err
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
	if in.ManagementMode == ManagementUnmanaged {
		targetID, err = s.validateTarget(ctx, in)
		if err != nil {
			return models.HarnessSession{}, false, err
		}
	}
	id := uuid.NewString()
	c := in.Capabilities
	_, err = s.db.ExecContext(ctx, `INSERT INTO harness_sessions(id,project_id,project_agent_id,agent_name,harness,host,session_ref_digest,message_target_id,management_mode,role,steer_mode,
		advertised_inbox,advertised_status,advertised_steer,advertised_interrupt,advertised_stop,phase) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, in.ProjectID, agentID, in.AgentName, in.Harness, in.Host, digest, nullString(targetID), in.ManagementMode, in.Role, in.SteerMode,
		boolInt(c.Inbox), boolInt(c.Status), boolInt(c.Steer), boolInt(c.Interrupt), boolInt(c.Stop), PhaseStarting)
	if err != nil {
		const identityConstraint = "UNIQUE constraint failed: harness_sessions.project_id, harness_sessions.harness, harness_sessions.host, harness_sessions.session_ref_digest"
		const addressConstraint = "UNIQUE constraint failed: harness_sessions.project_id, harness_sessions.harness, harness_sessions.agent_name"
		identityConflict := strings.Contains(err.Error(), identityConstraint)
		addressConflict := strings.Contains(err.Error(), addressConstraint)
		if identityConflict || addressConflict {
			replay, replayErr := scanSession(s.db.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM harness_sessions WHERE project_id=? AND harness=? AND host=? AND session_ref_digest=? AND phase<>'stopped'`, in.ProjectID, in.Harness, in.Host, digest))
			if replayErr == nil && sameRegistration(replay, in) {
				return replay, false, nil
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
	if in.ManagementMode == ManagementManaged && in.Capabilities.Inbox {
		targetID, err = s.validateTarget(ctx, in)
		if err != nil {
			_, _ = s.db.ExecContext(ctx, `DELETE FROM harness_sessions WHERE id=?`, id)
			return models.HarnessSession{}, false, err
		}
		if _, err = s.db.ExecContext(ctx, `UPDATE harness_sessions SET message_target_id=? WHERE id=?`, targetID, id); err != nil {
			_, _ = s.db.ExecContext(ctx, `DELETE FROM harness_sessions WHERE id=?`, id)
			return models.HarnessSession{}, false, err
		}
	}
	created, err := s.GetByID(ctx, id)
	return created, true, err
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
	phase = strings.ToLower(strings.TrimSpace(phase))
	if phase != PhaseStarting && phase != PhaseWorking && phase != PhaseYielded && phase != PhaseStopping {
		return models.HarnessSession{}, coded(CodeInvalid, "heartbeat phase must be starting, working, yielded, or stopping")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE harness_sessions SET phase=?,heartbeat_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),revision=revision+1 WHERE id=? AND advertised_status=1 AND phase<>'stopped'`, phase, strings.TrimSpace(id))
	if err != nil {
		return models.HarnessSession{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return models.HarnessSession{}, coded(CodeCapabilityUnavailable, "session cannot report status")
	}
	return s.GetByID(ctx, id)
}

const controlColumns = `id,harness_session_id,sequence,kind,state,reason,requested_by_user_id,requested_at,COALESCE(claimed_at,''),COALESCE(completed_at,'')`

func scanControl(row interface{ Scan(...any) error }) (models.HarnessControl, error) {
	var out models.HarnessControl
	err := row.Scan(&out.ID, &out.HarnessSessionID, &out.Sequence, &out.Kind, &out.State, &out.Reason, &out.RequestedByUserID, &out.RequestedAt, &out.ClaimedAt, &out.CompletedAt)
	return out, err
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
		return YieldResult{}, coded(CodeCapabilityUnavailable, "only an active managed session can yield")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE harness_session_controls SET state='claimed',claimed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE harness_session_id=? AND state='pending'`, sessionID); err != nil {
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
	result, err := s.db.ExecContext(ctx, `UPDATE harness_session_controls SET state=?,reason=?,completed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=? AND harness_session_id=? AND state='claimed'`, outcome, reason, controlID, sessionID)
	if err != nil {
		return models.HarnessControl{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return models.HarnessControl{}, coded(CodeConflict, "control is not claimed by this harness session")
	}
	return scanControl(s.db.QueryRowContext(ctx, `SELECT `+controlColumns+` FROM harness_session_controls WHERE id=?`, controlID))
}

func (s *Service) Stop(ctx context.Context, id string) (models.HarnessSession, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE harness_sessions SET phase='stopped',updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),revision=revision+1 WHERE id=? AND phase<>'stopped'`, id)
	if err != nil {
		return models.HarnessSession{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return models.HarnessSession{}, coded(CodeConflict, "harness session is already stopped or missing")
	}
	return s.GetByID(ctx, id)
}

func Address(session models.HarnessSession) string {
	return fmt.Sprintf("%s:%s", session.Harness, session.AgentName)
}
