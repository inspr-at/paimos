// SPDX-License-Identifier: AGPL-3.0-only
package harness

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Lead is a redacted project projection. Generation changes only on an explicit
// claim; revision changes on every intent/control write. Neither grants roles.
type Lead struct {
	ProjectID     string  `json:"project_id"`
	SessionID     *string `json:"session_id"`
	Generation    int64   `json:"generation"`
	Revision      int64   `json:"revision"`
	State         string  `json:"state"`
	Reason        string  `json:"reason"`
	ProcessActive bool    `json:"process_active"`
	owner         string
}

// LeadGate is supplied by the existing admission owners, never by an API body.
// State is available, full or unavailable. Fresh unknowns still mean WAIT.
type LeadGate struct {
	State     string
	CheckedAt time.Time
}
type LeadChecks struct{ Dial, Harness, Account, Host LeadGate }

// LeadAdmission must re-read the existing person agentplan, harness allowance,
// account reservation and host-load policy inside tx at every claim/dispatch.
// It must acquire all needed locks before returning and must not append events.
// A coordinator candidate is already a reporting process: count it once. A
// candidate carrying RunID is a prospective worker launch; resolve its exact
// account/host from the existing fenced reservation and include its new slot.
// Count all other unconfirmed processes, including idle/archived/lost-contact
// generations. Missing candidate identity is unavailable, never Automatic.
// Errors, missing adapters and stale observations never become permission.
type LeadAdmission func(context.Context, pgx.Tx, tenant.Principal, string, Session) (LeadChecks, error)

// NewWithLeadAdmission supplies the existing scheduler's qualified admission
// path. New intentionally leaves it absent: automatic production starts stay
// off until AEON-603 proof. This constructor itself launches no processes.
func NewWithLeadAdmission(pool *pgxpool.Pool, admission LeadAdmission, planningStart ...func(context.Context, pgx.Tx, string, string) error) httpapi.Module {
	m := New(pool, planningStart...).(*Module)
	m.leadAdmission = admission
	return m
}

func leadWait(checks LeadChecks, now time.Time) string {
	for _, gate := range []struct {
		name  string
		value LeadGate
	}{
		{"dial", checks.Dial}, {"harness", checks.Harness}, {"account", checks.Account}, {"host", checks.Host},
	} {
		if gate.value.CheckedAt.IsZero() || gate.value.CheckedAt.After(now) || now.Sub(gate.value.CheckedAt) > 30*time.Second {
			return gate.name + "_unavailable"
		}
		switch gate.value.State {
		case "available":
		case "full":
			return gate.name + "_full"
		default:
			return gate.name + "_unavailable"
		}
	}
	return ""
}

const leadColumns = `project_id::text,session_id::text,generation,revision,state,reason,owner_principal_id::text`

func scanLead(row pgx.Row) (Lead, error) {
	var l Lead
	err := row.Scan(&l.ProjectID, &l.SessionID, &l.Generation, &l.Revision, &l.State, &l.Reason, &l.owner)
	return l, err
}
func loadLead(ctx context.Context, tx pgx.Tx, projectID string, lock bool) (Lead, error) {
	q := `SELECT ` + leadColumns + ` FROM project_leads WHERE project_id=$1`
	if lock {
		q += ` FOR NO KEY UPDATE`
	}
	l, err := scanLead(tx.QueryRow(ctx, q, projectID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Lead{ProjectID: projectID, State: "none"}, nil
	}
	return l, err
}
func leadFence(ctx context.Context, tx pgx.Tx, projectID string) error {
	if !workorders.UUID(projectID) {
		return workorders.Fail(400, "invalid project id")
	}
	if err := db.LockWorkTreeTx(ctx, tx); err != nil {
		return err
	}
	return lockHierarchy(ctx, tx, projectID)
}
func leadProject(ctx context.Context, tx pgx.Tx, projectID string) (bool, error) {
	var state string
	var deleted *time.Time
	err := tx.QueryRow(ctx, `SELECT n.state,n.deleted_at FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=$1 AND k.slug='project'`, projectID).Scan(&state, &deleted)
	return state != "archived" && deleted == nil, err
}
func leadOwner(ctx context.Context, tx pgx.Tx, p tenant.Principal, projectID, owner string) error {
	var canonical string
	err := tx.QueryRow(ctx, `SELECT c.id::text FROM principals p JOIN principals c ON c.tenant_id=p.tenant_id AND c.id=coalesce(p.linked_to,p.id) WHERE p.id=$1 AND p.kind='person' AND p.status='active' AND c.kind='person' AND c.status='active'`, owner).Scan(&canonical)
	if errors.Is(err, pgx.ErrNoRows) {
		return authz.ErrForbidden
	}
	if err != nil {
		return err
	}
	person := tenant.Principal{ID: canonical, TenantID: p.TenantID, Kind: tenant.Person}
	for _, scope := range []string{"harness.control", "run.create"} {
		if err = authz.RequireTx(ctx, tx, person, scope, authz.Scope{ProjectID: projectID}); err != nil {
			return err
		}
	}
	return nil
}
func projectLead(ctx context.Context, tx pgx.Tx, p tenant.Principal, l Lead) (Lead, error) {
	active, err := leadProject(ctx, tx, l.ProjectID)
	if err != nil {
		return l, err
	}
	if l.SessionID != nil {
		if err = tx.QueryRow(ctx, `SELECT NOT aeon_work_session_stopped(stopped_at,stop_reason) FROM harness_sessions WHERE project_id=$1 AND id=$2`, l.ProjectID, *l.SessionID).Scan(&l.ProcessActive); err != nil {
			return l, err
		}
	}
	if l.State == "none" {
		return l, nil
	}
	if !active {
		l.State, l.Reason = "cannot_start", "project_archived"
		return l, nil
	}
	if err = leadOwner(ctx, tx, p, l.ProjectID, l.owner); errors.Is(err, authz.ErrForbidden) {
		l.State, l.Reason = "cannot_start", "owner_revoked"
		return l, nil
	} else if err != nil {
		return l, err
	}
	if l.SessionID != nil && (l.State == "working" || l.State == "starting") {
		var phase string
		var archived, stopped *time.Time
		var fresh bool
		var pausing bool
		err = tx.QueryRow(ctx, `SELECT phase,archived_at,stopped_at,coalesce(heartbeat_at,created_at)>clock_timestamp()-interval '30 seconds',coalesce(pause_record->>'state' IN ('requested','planned','paused','resume_requested'),false) OR coalesce((pause_record->>'stop_requested')::boolean,false) FROM harness_sessions WHERE id=$1`, *l.SessionID).Scan(&phase, &archived, &stopped, &fresh, &pausing)
		if err != nil {
			return l, err
		}
		if !l.ProcessActive {
			l.State, l.Reason = "paused", "process_stopped"
		} else if pausing {
			l.State, l.Reason = "paused", "handover_pending"
		} else if archived != nil || stopped != nil || !fresh {
			l.State, l.Reason = "waiting_for_room", "generation_unavailable"
		} else if l.State == "working" && phase == "starting" {
			l.State = "starting"
		}
	}
	return l, nil
}
func (m *Module) readLead(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	if !workorders.UUID(r.PathValue("projectId")) {
		return nil, workorders.Fail(400, "invalid project id")
	}
	if err := authz.RequireTx(r.Context(), tx, p, "harness.read", authz.Scope{ProjectID: r.PathValue("projectId")}); err != nil {
		return nil, err
	}
	l, err := loadLead(r.Context(), tx, r.PathValue("projectId"), false)
	if err != nil {
		return nil, err
	}
	return projectLead(r.Context(), tx, p, l)
}
func (m *Module) startLead(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in struct {
		Revision *int64 `json:"expected_revision"`
	}
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if in.Revision == nil || *in.Revision < 0 {
		return nil, workorders.Fail(400, "invalid revision")
	}
	if p.Kind != tenant.Person {
		return nil, workorders.Fail(403, "person required to start lead")
	}
	ctx, id := r.Context(), r.PathValue("projectId")
	if err := leadFence(ctx, tx, id); err != nil {
		return nil, err
	}
	if err := leadOwner(ctx, tx, p, id, p.ID); err != nil {
		return nil, err
	}
	active, err := leadProject(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if !active {
		return nil, workorders.Fail(409, "project archived")
	}
	l, err := loadLead(ctx, tx, id, true)
	if err != nil {
		return nil, err
	}
	if l.Revision != *in.Revision {
		return nil, workorders.Fail(409, "lead revision conflict")
	}
	var owner string
	if err = tx.QueryRow(ctx, `SELECT coalesce(linked_to,id)::text FROM principals WHERE id=$1`, p.ID).Scan(&owner); err != nil {
		return nil, err
	}
	if l.Revision > 0 && owner != l.owner {
		return nil, workorders.Fail(403, "lead owner required; explicit ownership handoff required")
	}
	before := l
	if l.SessionID != nil {
		var stopped bool
		if err = tx.QueryRow(ctx, `SELECT aeon_work_session_stopped(stopped_at,stop_reason) FROM harness_sessions WHERE id=$1`, *l.SessionID).Scan(&stopped); err != nil {
			return nil, err
		}
		if !stopped {
			return nil, workorders.Fail(409, "lead process exit is unconfirmed")
		}
	}
	l, err = scanLead(tx.QueryRow(ctx, `INSERT INTO project_leads(tenant_id,project_id,owner_principal_id,state,reason) VALUES($1,$2,$3,'waiting_for_room','start_checks_unavailable') ON CONFLICT(tenant_id,project_id) DO UPDATE SET owner_principal_id=excluded.owner_principal_id,state='waiting_for_room',reason='awaiting_generation',revision=project_leads.revision+1,updated_at=clock_timestamp() RETURNING `+leadColumns, p.TenantID, id, owner))
	if err != nil {
		return nil, err
	}
	if err = workorders.Record(ctx, tx, p, id, "lead.start_requested", before, l); err != nil {
		return nil, err
	}
	return projectLead(ctx, tx, p, l)
}
func (m *Module) checkLeadAdmission(ctx context.Context, tx pgx.Tx, p tenant.Principal, owner string, s Session) (string, error) {
	if m.leadAdmission == nil {
		return "start_checks_unavailable", nil
	}
	attempt, err := tx.Begin(ctx)
	if err != nil {
		return "", err
	}
	checks, err := m.leadAdmission(ctx, attempt, p, owner, s)
	if err != nil {
		if rollbackErr := attempt.Rollback(ctx); rollbackErr != nil {
			return "", rollbackErr
		}
		return "start_checks_unavailable", nil
	}
	if err = attempt.Commit(ctx); err != nil {
		return "", err
	}
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return "", err
	}
	return leadWait(checks, now), nil
}
func (m *Module) claimLead(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in struct {
		Revision  int64  `json:"expected_revision"`
		SessionID string `json:"session_id"`
	}
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if in.Revision < 1 || !workorders.UUID(in.SessionID) {
		return nil, workorders.Fail(400, "valid revision and session required")
	}
	ctx, id := r.Context(), r.PathValue("projectId")
	if err := leadFence(ctx, tx, id); err != nil {
		return nil, err
	}
	if err := authz.RequireTx(ctx, tx, p, "harness.worker", authz.Scope{ProjectID: id}); err != nil {
		return nil, err
	}
	l, err := loadLead(ctx, tx, id, true)
	if err != nil {
		return nil, err
	}
	if l.Revision != in.Revision {
		return nil, workorders.Fail(409, "lead revision conflict")
	}
	active, err := leadProject(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if !active {
		return nil, workorders.Fail(409, "project archived")
	}
	if err = leadOwner(ctx, tx, p, id, l.owner); err != nil {
		return nil, err
	}
	r.SetPathValue("sessionId", in.SessionID)
	s, err := worker(ctx, tx, r, p)
	if err != nil {
		return nil, err
	}
	if s.Role != "coordinator" || s.ParentID != nil || s.OwnerPrincipalID == nil || *s.OwnerPrincipalID != l.owner {
		return nil, workorders.Fail(403, "proven owned root coordinator required")
	}
	var fresh bool
	if err = tx.QueryRow(ctx, `SELECT coalesce(heartbeat_at,created_at)>clock_timestamp()-interval '30 seconds' FROM harness_sessions WHERE id=$1`, s.ID).Scan(&fresh); err != nil {
		return nil, err
	}
	if !fresh {
		return nil, workorders.Fail(409, "fresh reporting generation required")
	}
	before := l
	var predecessor *Session
	if l.SessionID != nil && *l.SessionID != s.ID {
		old, err := load(ctx, tx, id, *l.SessionID, true)
		if err != nil {
			return nil, err
		}
		var stopped bool
		if err = tx.QueryRow(ctx, `SELECT aeon_work_session_stopped(stopped_at,stop_reason) FROM harness_sessions WHERE id=$1`, old.ID).Scan(&stopped); err != nil {
			return nil, err
		}
		if !stopped {
			return nil, workorders.Fail(409, "lead process exit is unconfirmed")
		}
		if old.OwnerPrincipalID == nil || *old.OwnerPrincipalID != l.owner || subtle.ConstantTimeCompare(old.refDigest, s.refDigest) == 1 || subtle.ConstantTimeCompare(old.leaseDigest, s.leaseDigest) == 1 {
			return nil, workorders.Fail(409, "fresh same-owner successor required")
		}
		if old.StopReason != nil && *old.StopReason == "paused" && (old.Pause == nil || old.Pause.Handover == nil) {
			return nil, workorders.Fail(409, "paused lead checkpoint required")
		}
		predecessor = &old
	}
	if l.State == "paused" {
		return nil, workorders.Fail(409, "explicit start request required after pause")
	}
	reason, err := m.checkLeadAdmission(ctx, tx, p, l.owner, s)
	if err != nil {
		return nil, err
	}
	if reason != "" {
		l, err = scanLead(tx.QueryRow(ctx, `UPDATE project_leads SET state='waiting_for_room',reason=$2,revision=revision+1,updated_at=clock_timestamp() WHERE project_id=$1 RETURNING `+leadColumns, id, reason))
	} else {
		l, err = scanLead(tx.QueryRow(ctx, `UPDATE project_leads SET generation=generation+CASE WHEN session_id IS DISTINCT FROM $2::uuid THEN 1 ELSE 0 END,session_id=$2,state='working',reason='',revision=revision+1,updated_at=clock_timestamp() WHERE project_id=$1 RETURNING `+leadColumns, id, s.ID))
	}
	if err != nil {
		return nil, err
	}
	if reason == "" && (before.SessionID == nil || *before.SessionID != s.ID) {
		if _, err = tx.Exec(ctx, `INSERT INTO project_lead_generations(tenant_id,project_id,generation,session_id) VALUES($1,$2,$3,$4)`, p.TenantID, id, l.Generation, s.ID); err != nil {
			return nil, err
		}
		if predecessor != nil {
			if predecessor.StopReason != nil && *predecessor.StopReason == "paused" {
				if s, err = storeContinuation(ctx, tx, *predecessor, s); err != nil {
					return nil, err
				}
				nextPause := *predecessor.Pause
				nextPause.State, nextPause.SuccessorID = "resumed", s.ID
				if _, err = writePause(ctx, tx, *predecessor, nextPause); err != nil {
					return nil, err
				}
			}
			if _, err = tx.Exec(ctx, `UPDATE harness_sessions SET handed_over_to_id=$2,revision=revision+1 WHERE id=$1`, predecessor.ID, s.ID); err != nil {
				return nil, err
			}
		}
	}
	if err = workorders.Record(ctx, tx, p, id, "lead.claim_checked", before, l); err != nil {
		return nil, err
	}
	return projectLead(ctx, tx, p, l)
}

func (m *Module) pauseLead(r *http.Request, tx pgx.Tx, p tenant.Principal) (result any, err error) {
	r, flush := deferControlEvents(r, tx)
	defer flush(&err)
	var in struct {
		Revision   int64  `json:"expected_revision"`
		Generation *int64 `json:"generation"`
	}
	if err = workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if in.Revision < 1 || in.Generation == nil || *in.Generation < 0 {
		return nil, workorders.Fail(400, "valid revision and generation required")
	}
	ctx, id := r.Context(), r.PathValue("projectId")
	if err = leadFence(ctx, tx, id); err != nil {
		return nil, err
	}
	l, err := loadLead(ctx, tx, id, true)
	if err != nil {
		return nil, err
	}
	if l.Revision != in.Revision || l.Generation != *in.Generation || l.Revision == 0 {
		return nil, workorders.Fail(409, "lead revision or generation conflict")
	}
	var s Session
	if l.SessionID != nil {
		s, err = load(ctx, tx, id, *l.SessionID, true)
		if err != nil {
			return nil, err
		}
	}
	if p.Kind == tenant.Person {
		if err = authz.RequireTx(ctx, tx, p, "harness.control", authz.Scope{ProjectID: id}); err != nil {
			return nil, err
		}
		var owner string
		if err = tx.QueryRow(ctx, `SELECT coalesce(linked_to,id)::text FROM principals WHERE id=$1`, p.ID).Scan(&owner); err != nil {
			return nil, err
		}
		if owner != l.owner {
			return nil, workorders.Fail(403, "lead owner required")
		}
	} else {
		if err = authz.RequireTx(ctx, tx, p, "harness.worker", authz.Scope{ProjectID: id}); err != nil {
			return nil, err
		}
		if !leaseProof(s, r, p) {
			return nil, workorders.Fail(403, "current lead worker proof required")
		}
	}
	before := l
	reason := "handover_pending"
	if s.ID != "" && s.StoppedAt == nil {
		// Reuse durable cooperative controls. Lack of delivery never releases a
		// slot or fabricates exit; the dispatch fence still closes immediately.
		if cooperativePause(s) {
			if _, err = requestPause(ctx, tx, p, s, pauseRequest{Level: "wrap_up", Reason: "lead_idle", Note: "Checkpoint current work, publish a handover and confirm this generation stopped. No new dispatch.", DeadlineMinutes: 10}); err != nil {
				return nil, err
			}
		} else {
			reason = "handover_delivery_unavailable"
		}
	}
	l, err = scanLead(tx.QueryRow(ctx, `UPDATE project_leads SET state='paused',reason=$2,revision=revision+1,updated_at=clock_timestamp() WHERE project_id=$1 RETURNING `+leadColumns, id, reason))
	if err != nil {
		return nil, err
	}
	if err = record(ctx, tx, p, Session{ProjectID: id}, "lead_paused", before, l); err != nil {
		return nil, err
	}
	return projectLead(ctx, tx, p, l)
}

// RequireCurrentLeadTx is the shared fence for accepted assignments (AEON-735)
// and the existing queue scheduler. Caller holds tenant/tree locks, proves the
// session lease and rechecks mandatory admission in this SAME final write.
// It deliberately returns no execution authority from a stored role or label.
func RequireCurrentLeadTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, projectID, sessionID string, generation int64) error {
	if err := leadFence(ctx, tx, projectID); err != nil {
		return err
	}
	if err := authz.RequireTx(ctx, tx, p, "harness.worker", authz.Scope{ProjectID: projectID}); err != nil {
		return err
	}
	l, err := loadLead(ctx, tx, projectID, true)
	if err != nil {
		return err
	}
	if l.SessionID == nil || *l.SessionID != sessionID || l.Generation != generation || l.State != "working" {
		return workorders.Fail(409, "current working lead generation required")
	}
	if err = leadOwner(ctx, tx, p, projectID, l.owner); err != nil {
		return err
	}
	l, err = projectLead(ctx, tx, p, l)
	if err != nil {
		return err
	}
	if l.State != "working" {
		return workorders.Fail(409, "lead generation unavailable")
	}
	var agent string
	if err = tx.QueryRow(ctx, `SELECT agent_principal_id::text FROM harness_sessions WHERE id=$1`, sessionID).Scan(&agent); err != nil {
		return err
	}
	if p.Kind != tenant.Agent || p.ID != agent {
		return authz.ErrForbidden
	}
	return nil
}

// Existing general coordinator registrations are observational. Once explicitly
// bound to a project lead, they cannot use stale-ref or automatic child adoption
// as a shortcut around the project/generation contract.
func fenceLeadRegistration(ctx context.Context, tx pgx.Tx, projectID string, in registration, ref, lease []byte) error {
	var historical bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM project_lead_generations g JOIN harness_sessions s ON s.tenant_id=g.tenant_id AND s.id=g.session_id WHERE g.project_id=$1 AND ((s.ref_digest=$2 AND (s.stopped_at IS NOT NULL OR s.lease_digest<>$3)) OR s.id=$4::uuid))`, projectID, ref, lease, in.SucceedsID).Scan(&historical); err != nil {
		return err
	}
	if historical {
		return workorders.Fail(409, "project lead restart requires fresh reference and explicit claim")
	}
	l, err := loadLead(ctx, tx, projectID, false)
	if err != nil || l.SessionID == nil {
		return err
	}
	s, err := load(ctx, tx, projectID, *l.SessionID, true)
	if err != nil {
		return err
	}
	if in.SucceedsID != nil && *in.SucceedsID == s.ID {
		return workorders.Fail(409, "project lead succession requires explicit claim; workers retain their generation")
	}
	if subtle.ConstantTimeCompare(s.refDigest, ref) == 1 && (s.StoppedAt != nil || subtle.ConstantTimeCompare(s.leaseDigest, lease) != 1) {
		return workorders.Fail(409, "project lead restart requires fresh reference and explicit claim")
	}
	return nil
}

// RequireLeadDispatchTx binds queue pickup to this exact project generation and
// rechecks all mandatory gates in the final mutation. Existing manual queue
// clients remain compatible only in projects without an explicit lead intent.
// Once configured, an arbitrary coordinator (or a missing adapter) cannot route
// work around a paused, revoked or archived lead.
func RequireLeadDispatchTx(ctx context.Context, tx pgx.Tx, r *http.Request, p tenant.Principal, projectID string, admission LeadAdmission) error {
	l, err := loadLead(ctx, tx, projectID, false)
	if err != nil || l.State == "none" {
		return err
	}
	if p.Kind != tenant.Agent {
		return workorders.Fail(409, "project lead must dispatch configured project work")
	}
	generation, err := strconv.ParseInt(r.Header.Get("X-Aeon-Lead-Generation"), 10, 64)
	if err != nil || generation < 1 {
		return workorders.Fail(409, "lead generation proof required")
	}
	id := r.Header.Get("X-Aeon-Lead-Session")
	if !workorders.UUID(id) {
		return workorders.Fail(409, "lead session proof required")
	}
	if err = RequireCurrentLeadTx(ctx, tx, p, projectID, id, generation); err != nil {
		return err
	}
	proofRequest := r.Clone(ctx)
	proofRequest.SetPathValue("projectId", projectID)
	proofRequest.SetPathValue("sessionId", id)
	s, err := worker(ctx, tx, proofRequest, p)
	if err != nil {
		return err
	}
	m := Module{leadAdmission: admission}
	reason, err := m.checkLeadAdmission(ctx, tx, p, l.owner, s)
	if err != nil {
		return err
	}
	if reason != "" {
		return workorders.Fail(409, "lead admission wait: "+reason)
	}
	return nil
}

// RequireAssignedLeadTx rejects a queued assignment whose accepting lead has
// paused, been replaced, lost reporting, lost its owner, or lost its project.
// Worker launch admission remains owned by the existing account/daemon path;
// a generation receipt does not replace its mandatory live start checks.
func RequireAssignedLeadTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, projectID string, binding []byte) error {
	l, err := loadLead(ctx, tx, projectID, false)
	if err != nil || l.State == "none" {
		return err
	}
	if err := leadFence(ctx, tx, projectID); err != nil {
		return err
	}
	l, err = loadLead(ctx, tx, projectID, true)
	if err != nil || l.State == "none" {
		return err
	}
	var in struct {
		SessionID  string `json:"session_id"`
		Generation int64  `json:"generation"`
	}
	if json.Unmarshal(binding, &in) != nil || l.SessionID == nil || in.SessionID != *l.SessionID || in.Generation != l.Generation || l.State != "working" {
		return workorders.Fail(409, "assignment lead generation is no longer current")
	}
	l, err = projectLead(ctx, tx, p, l)
	if err != nil {
		return err
	}
	if l.State != "working" {
		return workorders.Fail(409, "assignment lead generation unavailable")
	}
	return nil
}

// RequireAssignedLeadStartTx adds mandatory live admission to the generation
// fence at the daemon's final claim. It runs before order/run row locks, under
// the same tenant/tree access fence; reservations/daemon proof are still checked
// by the existing claim path. A route's earlier checks never authorize a start.
func RequireAssignedLeadStartTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, projectID, runID string, binding []byte, admission LeadAdmission) error {
	if err := RequireAssignedLeadTx(ctx, tx, p, projectID, binding); err != nil {
		return err
	}
	l, err := loadLead(ctx, tx, projectID, true)
	if err != nil || l.State == "none" {
		return err
	}
	s := Session{ProjectID: projectID, RunID: &runID, OwnerPrincipalID: &l.owner}
	if err = tx.QueryRow(ctx, `SELECT r.agent_principal_id::text,m.harness FROM agent_runs r JOIN model_profiles m ON m.tenant_id=r.tenant_id AND m.id=r.model_profile_id WHERE r.id=$1`, runID).Scan(&s.AgentPrincipalID, &s.Harness); err != nil {
		return err
	}
	if p.Kind != tenant.Agent || p.ID != s.AgentPrincipalID {
		return authz.ErrForbidden
	}
	m := Module{leadAdmission: admission}
	reason, err := m.checkLeadAdmission(ctx, tx, p, l.owner, s)
	if err != nil {
		return err
	}
	if reason != "" {
		return workorders.Fail(409, "worker admission wait: "+reason)
	}
	return nil
}
