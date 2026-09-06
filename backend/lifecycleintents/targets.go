// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
package lifecycleintents

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"fmt"
	"time"

	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/managedharness"
)

type session struct {
	id        string
	agent     string
	host      string
	account   string
	workspace string
	profile   string
	version   string
	role      string
	phase     string
	activity  string
	heartbeat string
	createdAt string
	revision  int64
	ticket    sql.NullInt64
	parent    sql.NullString
	shape     string
}

func loadSession(ctx context.Context, tx *sql.Tx, project int64, id string) (session, error) {
	var out session
	err := tx.QueryRowContext(ctx, `SELECT id,agent_name,host,account_label,COALESCE(workspace_identity,''),dispatch_profile_id,dispatch_profile_version,role,phase,activity_state,COALESCE(heartbeat_at,''),revision,ticket_id,parent_harness_session_id,COALESCE(work_shape,'unknown'),created_at FROM harness_sessions WHERE id=? AND project_id=? AND management_mode='managed'`, id, project).Scan(&out.id, &out.agent, &out.host, &out.account, &out.workspace, &out.profile, &out.version, &out.role, &out.phase, &out.activity, &out.heartbeat, &out.revision, &out.ticket, &out.parent, &out.shape, &out.createdAt)
	if err != nil {
		return session{}, ErrUnavailable
	}
	return out, nil
}
func workspaceIdentity(r Runtime, handle string) string {
	for _, w := range r.Workspaces {
		if w.Handle == handle {
			return w.Identity
		}
	}
	return ""
}
func sameSpecification(current session, r Request, runtime Runtime) bool {
	return current.agent == r.AgentName && current.host == runtime.MachineID && current.account == r.AccountLabel && current.workspace == workspaceIdentity(runtime, r.WorkspaceHandle) && current.profile == r.DispatchProfileID && current.version == r.DispatchProfileVersion && current.role == r.Role
}
func sameBinding(current session, r Request) bool {
	return current.ticket.Valid == (r.TicketID != nil) && (r.TicketID == nil || current.ticket.Int64 == *r.TicketID) && current.parent.Valid == (r.ParentSessionID != nil) && (r.ParentSessionID == nil || current.parent.String == *r.ParentSessionID) && current.shape == r.WorkShape
}
func (s *Service) validateTarget(ctx context.Context, tx *sql.Tx, project int64, r Request, runtime Runtime) error {
	return s.validateTargetForOutcome(ctx, tx, project, r, runtime, "", "")
}
func (s *Service) validateTargetForOutcome(ctx context.Context, tx *sql.Tx, project int64, r Request, runtime Runtime, result, ownIntentID string) error {
	if runtime.Generation != r.RuntimeGeneration || runtime.AccountLabel != r.AccountLabel {
		return ErrUnavailable
	}
	if r.Operation == "repair" {
		return nil
	}
	profile, err := resolveProfile(r.DispatchProfileID, r.DispatchProfileVersion)
	if err != nil {
		return err
	}
	advertised := false
	for _, p := range runtime.Profiles {
		if p.ID == profile.ID && p.Version == profile.Version {
			advertised = true
		}
	}
	if !advertised || workspaceIdentity(runtime, r.WorkspaceHandle) == "" {
		return ErrUnavailable
	}
	var agent int64
	if tx.QueryRowContext(ctx, `SELECT id FROM project_agents WHERE project_id=? AND name=?`, project, r.AgentName).Scan(&agent) != nil {
		return ErrUnavailable
	}
	if r.TicketID != nil {
		var ticket int
		if tx.QueryRowContext(ctx, `SELECT 1 FROM issues WHERE id=? AND project_id=? AND deleted_at IS NULL AND archived=0`, *r.TicketID, project).Scan(&ticket) != nil {
			return ErrUnavailable
		}
	}
	if err = validateHierarchy(ctx, tx, project, r.SessionID, r.ParentSessionID); err != nil {
		return err
	}
	if r.Operation == "start" || r.Operation == "restart" {
		// Claiming reserves the canonical agent/coordinator and exclusive workspace
		// before any process exists. Separate daemons cannot race into two spawns.
		var reserved int
		if tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM lifecycle_intents i
   JOIN lifecycle_runtimes runtime ON runtime.id=i.runtime_id
   LEFT JOIN json_each(runtime.registration_json,'$.workspaces') workspace
    ON json_extract(workspace.value,'$.handle')=json_extract(i.request_json,'$.workspace_handle')
   WHERE i.id<>? AND i.state IN ('claimed','executing')
    AND json_extract(i.request_json,'$.operation') IN ('start','restart') AND (
     (i.project_id=? AND json_extract(i.request_json,'$.agent_name')=?) OR
     (runtime.machine_id=? AND json_extract(workspace.value,'$.identity')=?) OR
     (i.project_id=? AND json_extract(i.request_json,'$.role')='coordinator' AND ?='coordinator'))`, ownIntentID, project, r.AgentName, runtime.MachineID, workspaceIdentity(runtime, r.WorkspaceHandle), project, r.Role).Scan(&reserved) != nil {
			return ErrStorage
		}
		if reserved > 0 {
			return ErrUnavailable
		}
		var occupied int
		if tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM harness_sessions WHERE phase<>'stopped' AND id<>? AND (
   (project_id=? AND harness=? AND agent_name=?) OR
   (host=? AND workspace_identity=? AND workspace_mode='exclusive') OR
   (project_id=? AND role='coordinator' AND ?='coordinator'))`, result, project, profile.Harness, r.AgentName, runtime.MachineID, workspaceIdentity(runtime, r.WorkspaceHandle), project, r.Role).Scan(&occupied) != nil {
			return ErrStorage
		}
		if occupied > 0 {
			return ErrUnavailable
		}
	}
	if r.Operation == "start" {
		return nil
	}
	current, err := loadSession(ctx, tx, project, r.SessionID)
	if err != nil {
		return err
	}
	if !sameSpecification(current, r, runtime) || current.revision != r.ExpectedRevision {
		return ErrUnavailable
	}
	var present int
	if tx.QueryRowContext(ctx, `SELECT 1 FROM lifecycle_runtime_sessions WHERE session_id=? AND runtime_id=? AND generation=?`, r.SessionID, r.RuntimeID, r.SessionGeneration).Scan(&present) != nil {
		return ErrUnavailable
	}
	if r.Operation == "restart" {
		if current.phase != "stopped" || current.activity != "dead" {
			return ErrUnavailable
		}
	} else {
		hb, e := time.Parse(time.RFC3339Nano, current.heartbeat)
		if e != nil || hb.After(s.now()) || s.now().Sub(hb) > managedharness.DefaultActivityHeartbeatTimeout || current.activity != "idle" || current.phase == "stopped" || current.phase == "stopping" || sameBinding(current, r) {
			return ErrUnavailable
		}
	}
	return nil
}

// Apply the existing same-project, acyclic, depth-16 contract, including the
// moving subtree's height. Binding event triggers retain exact before/after.
func validateHierarchy(ctx context.Context, tx *sql.Tx, project int64, id string, parent *string) error {
	if parent == nil {
		return nil
	}
	seen := map[string]bool{}
	if id != "" {
		seen[id] = true
	}
	cursor := *parent
	depth := 0
	for cursor != "" {
		depth++
		if seen[cursor] || depth > managedharness.MaxHierarchyDepth {
			return ErrUnavailable
		}
		seen[cursor] = true
		var next sql.NullString
		if tx.QueryRowContext(ctx, `SELECT parent_harness_session_id FROM harness_sessions WHERE id=? AND project_id=? AND phase<>'stopped'`, cursor, project).Scan(&next) != nil {
			return ErrUnavailable
		}
		cursor = next.String
	}
	if id != "" {
		var height int
		if tx.QueryRowContext(ctx, `WITH RECURSIVE subtree(id,depth) AS (SELECT id,0 FROM harness_sessions WHERE id=? AND project_id=? UNION ALL SELECT child.id,subtree.depth+1 FROM harness_sessions child JOIN subtree ON child.parent_harness_session_id=subtree.id WHERE child.project_id=? AND subtree.depth<?) SELECT COALESCE(MAX(depth),0) FROM subtree`, id, project, project, managedharness.MaxHierarchyDepth+1).Scan(&height) != nil {
			return ErrStorage
		}
		if height+depth > managedharness.MaxHierarchyDepth {
			return ErrUnavailable
		}
	}
	return nil
}
func (s *Service) RegisterSession(ctx context.Context, p auth.Principal, project int64, runtimeID, lease, workerLease string, in SessionRegistration) error {
	mutationMu.Lock()
	defer mutationMu.Unlock()
	tx, err := s.begin(ctx, p, project, auth.PrincipalAPIKey)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	runtime, err := s.runtime(ctx, tx, project, runtimeID, &p, lease, true)
	if err != nil {
		return err
	}
	if !validID(in.SessionID) || !validID(in.Generation) || !managedharness.ValidWorkerLease(workerLease) {
		return ErrUnavailable
	}
	current, err := loadSession(ctx, tx, project, in.SessionID)
	if err != nil {
		return err
	}
	if current.host != runtime.MachineID || current.account != runtime.AccountLabel {
		return ErrUnavailable
	}
	var stored []byte
	if tx.QueryRowContext(ctx, `SELECT worker_lease_digest FROM harness_sessions WHERE id=? AND project_id=?`, in.SessionID, project).Scan(&stored) != nil {
		return ErrUnavailable
	}
	// Same domain separation as managedharness.VerifyWorkerLease, performed
	// here inside the authority transaction rather than across a TOCTOU gap.
	digest := sha256.Sum256([]byte(fmt.Sprintf("paimos:harness-worker-lease:v1\x00%d\x00%s\x00%s\x00%s", project, current.agent, in.SessionID, workerLease)))
	if subtle.ConstantTimeCompare(stored, digest[:]) != 1 {
		return ErrUnavailable
	}
	var runtimeOwner, generation string
	err = tx.QueryRowContext(ctx, `SELECT runtime_id,generation FROM lifecycle_runtime_sessions WHERE session_id=?`, in.SessionID).Scan(&runtimeOwner, &generation)
	if err == nil {
		if runtimeOwner != runtimeID || generation != in.Generation {
			return ErrUnavailable
		}
	} else if err != sql.ErrNoRows {
		return ErrStorage
	} else {
		var count int
		if tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM lifecycle_runtime_sessions WHERE runtime_id=?`, runtimeID).Scan(&count) != nil {
			return ErrStorage
		}
		if count >= 128 {
			return ErrConflict
		}
		// One process generation can never migrate between daemon generations.
		_, err = tx.ExecContext(ctx, `INSERT INTO lifecycle_runtime_sessions(session_id,runtime_id,generation,created_at) VALUES(?,?,?,?)`, in.SessionID, runtimeID, in.Generation, stamp(s.now()))
		if err != nil {
			return ErrConflict
		}
	}
	if tx.Commit() != nil {
		return ErrStorage
	}
	return nil
}
func (s *Service) completeEffect(ctx context.Context, tx *sql.Tx, in Intent, runtime Runtime, result string) error {
	r := in.Request
	switch r.Operation {
	case "repair":
		if result != "" {
			return ErrInvalid
		}
		return nil
	case "attach", "reassign":
		if result != r.SessionID {
			return ErrUnavailable
		}
		var parent, ticket, shape any
		if r.ParentSessionID != nil {
			parent = *r.ParentSessionID
		}
		if r.TicketID != nil {
			ticket = *r.TicketID
			shape = r.WorkShape
		}
		res, err := tx.ExecContext(ctx, `UPDATE harness_sessions SET parent_harness_session_id=?,ticket_id=?,work_shape=?,revision=revision+1,updated_at=? WHERE project_id=? AND id=? AND revision=?`, parent, ticket, shape, stamp(s.now()), in.ProjectID, r.SessionID, r.ExpectedRevision)
		if err != nil {
			return ErrStorage
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrConflict
		}
		return nil
	case "start", "restart":
		if !validID(result) || result == r.SessionID {
			return ErrUnavailable
		}
		current, err := loadSession(ctx, tx, in.ProjectID, result)
		if err != nil {
			return err
		}
		if !sameSpecification(current, r, runtime) || !sameBinding(current, r) || current.phase == "stopped" || current.createdAt < in.CreatedAt {
			return ErrUnavailable
		}
		var present int
		if tx.QueryRowContext(ctx, `SELECT 1 FROM lifecycle_runtime_sessions WHERE session_id=? AND runtime_id=? AND generation=?`, result, r.RuntimeID, in.NewGeneration).Scan(&present) != nil {
			return ErrUnavailable
		}
		return nil
	}
	return ErrInvalid
}
