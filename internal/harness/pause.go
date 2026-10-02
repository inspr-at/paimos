// SPDX-License-Identifier: AGPL-3.0-only

package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// Pause is a cooperative stop on the existing control channel. It never grants
// process ownership or permission to send a signal. The JSON lives in the
// session row, so reads, heartbeat, reboot recovery and audit share one state.
type Pause struct {
	Level              string     `json:"level,omitempty"`
	Note               string     `json:"note,omitempty"`
	StartsAt           *time.Time `json:"starts_at,omitempty"`
	LeavingID          string     `json:"leaving_request_id,omitempty"`
	InterruptControlID string     `json:"interrupt_control_id,omitempty"`
	StopExpiresInMS    int64      `json:"stop_expires_in_ms,omitempty"`
	StopControlID      string     `json:"stop_control_id,omitempty"`
	StopRequested      bool       `json:"stop_requested,omitempty"`
	Deliver            bool       `json:"deliver"`
	ControlID          string     `json:"control_id"`
	State              string     `json:"state"`
	RequestedBy        string     `json:"requested_by_principal_id"`
	RequestedAt        time.Time  `json:"requested_at"`
	Reason             string     `json:"reason"`
	DeadlineAt         time.Time  `json:"deadline_at"`
	HandoverPoint      string     `json:"handover_point,omitempty"`
	PlannedAt          *time.Time `json:"planned_at,omitempty"`
	PausedAt           *time.Time `json:"paused_at,omitempty"`
	Handover           *Handover  `json:"handover,omitempty"`
	ResumeRequestedAt  *time.Time `json:"resume_requested_at,omitempty"`
	SuccessorID        string     `json:"successor_session_id,omitempty"`
}

type Handover struct {
	State         string   `json:"state"`
	NextSteps     []string `json:"next_steps"`
	OpenQuestions []string `json:"open_questions"`
	WorktreeState string   `json:"worktree_state"`
	CommitSHA     string   `json:"commit_sha,omitempty"`
}

type Continuation struct {
	SucceedsID string   `json:"succeeds_session_id"`
	Handover   Handover `json:"handover"`
	Brief      string   `json:"brief"`
}

type pauseRequest struct {
	Level           string `json:"level"`
	Note            string `json:"note"`
	deadlineAt      *time.Time
	startsAt        *time.Time
	leavingID       string
	Reason          string   `json:"reason"`
	DeadlineMinutes int      `json:"deadline_minutes"`
	CoordinatorID   string   `json:"coordinator_session_id"`
	Except          []string `json:"except"`
}

func (in *pauseRequest) validate() error {
	if in.Level != "" && !ValidPauseLevel(in.Level) || !utf8.ValidString(in.Note) || utf8.RuneCountInString(in.Note) > 2000 || strings.ContainsRune(in.Note, 0) {
		return workorders.Fail(400, "valid pause level and note of at most 2000 characters required")
	}
	if !utf8.ValidString(in.Reason) || utf8.RuneCountInString(in.Reason) > 240 || strings.ContainsAny(in.Reason, "\x00\r\n") {
		return workorders.Fail(400, "pause reason must be at most 240 characters on one line")
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if in.DeadlineMinutes == 0 {
		in.DeadlineMinutes = 10
	}
	if in.DeadlineMinutes < 1 || in.DeadlineMinutes > 60 || len(in.Except) > 200 {
		return workorders.Fail(400, "invalid pause deadline or exclusions")
	}
	for i, id := range in.Except {
		if !workorders.UUID(id) {
			return workorders.Fail(400, "invalid excluded session")
		}
		in.Except[i] = strings.ToLower(id)
	}
	return nil
}

// A coordinator must prove its own live generation, not merely name a parent
// principal shared by other workers. Key scopes and live role bindings are
// checked by Endpoint in addition to the generation's lease.
func pauseController(r *http.Request, tx pgx.Tx, p tenant.Principal, coordinator string) (owner string, admin bool, parent string, err error) {
	if err = authz.RequireTx(r.Context(), tx, p, "harness.control", authz.Scope{ProjectID: r.PathValue("projectId")}); err != nil {
		return
	}
	if p.Kind == tenant.Person {
		err = tx.QueryRow(r.Context(), `SELECT coalesce(p.linked_to,p.id)::text,
 EXISTS(SELECT 1 FROM role_bindings b JOIN roles r ON r.tenant_id=b.tenant_id AND r.id=b.role_id
 WHERE b.principal_id=coalesce(p.linked_to,p.id) AND r.builtin AND r.key IN ('owner','admin')
 AND (b.scope_type='workspace' OR b.scope_type='project' AND b.scope_id=$2::uuid))
 FROM principals p WHERE p.id=$1 AND p.kind='person'`, p.ID, r.PathValue("projectId")).Scan(&owner, &admin)
		return
	}
	if !workorders.UUID(coordinator) {
		err = workorders.Fail(403, "proven parent coordinator required")
		return
	}
	proofRequest := r.Clone(r.Context())
	proofRequest.SetPathValue("sessionId", coordinator)
	s, e := worker(r.Context(), tx, proofRequest, p)
	if e != nil {
		err = e
		return
	}
	if s.Role != "coordinator" {
		err = workorders.Fail(403, "proven parent coordinator required")
		return
	}
	parent = s.ID
	return
}

func pauseAllowed(s Session, owner string, admin bool, parent string) bool {
	return admin || owner != "" && s.ownerID != nil && *s.ownerID == owner || parent != "" && s.ParentID != nil && *s.ParentID == parent
}

func savePause(ctx context.Context, tx pgx.Tx, p tenant.Principal, s Session, next Pause, event string) (Session, error) {
	before := s
	raw, err := json.Marshal(next)
	if err != nil {
		return s, err
	}
	s, err = scanSession(tx.QueryRow(ctx, `UPDATE harness_sessions SET pause_record=$2::jsonb,revision=revision+1 WHERE id=$1 RETURNING `+sessionColumns, s.ID, string(raw)))
	if err != nil {
		return s, err
	}
	return s, record(ctx, tx, p, s, event, before, s)
}

func (m *Module) requestPause(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in pauseRequest
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if err := in.validate(); err != nil {
		return nil, err
	}
	if err := lockHierarchy(r.Context(), tx, r.PathValue("projectId")); err != nil {
		return nil, err
	}
	if err := resolvePauseDefault(r.Context(), tx, p, &in); err != nil {
		return nil, err
	}
	owner, admin, parent, err := pauseController(r, tx, p, in.CoordinatorID)
	if err != nil {
		return nil, err
	}
	s, err := load(r.Context(), tx, r.PathValue("projectId"), r.PathValue("sessionId"), true)
	if err != nil {
		return nil, err
	}
	if !pauseAllowed(s, owner, admin, parent) {
		return nil, workorders.Fail(403, "session owner or proven parent coordinator required")
	}
	return requestPause(r.Context(), tx, p, s, in)
}

func requestPause(ctx context.Context, tx pgx.Tx, p tenant.Principal, s Session, in pauseRequest) (Session, error) {
	if s.ArchivedAt != nil || s.StoppedAt != nil {
		return s, workorders.Fail(409, "a stopped session cannot be paused")
	}
	// agentd forwards cooperative text only on its advertised inbox path.
	// Unmanaged workers receive the request directly in their CLI heartbeat.
	if !cooperativePause(s) && in.Level != "stop_now" {
		return s, workorders.Fail(409, "pause requires harness inbox delivery")
	}
	var err error
	if s, err = expirePause(ctx, tx, p, s); err != nil {
		return s, err
	}
	if s.Pause != nil && (s.Pause.State == "requested" || s.Pause.State == "planned" || s.Pause.StopRequested) {
		if in.Level != "stop_now" || s.Pause.StopRequested {
			return s, nil
		}
		if s, err = cancelPause(ctx, tx, p, s, "superseded_by_stop_now"); err != nil {
			return s, err
		}
	}
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return s, err
	}
	var sequence int64
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(sequence),0)+1 FROM harness_controls WHERE session_id=$1`, s.ID).Scan(&sequence); err != nil {
		return s, err
	}
	payload, _ := json.Marshal(SessionRequestPayload{Pause: true, Level: in.Level, Note: in.Note})
	c, err := scanControl(tx.QueryRow(ctx, `INSERT INTO harness_controls(tenant_id,session_id,kind,request_payload,sequence,requested_by_principal_id,expected_generation)
 VALUES($1,$2,'stop',$5::jsonb,$3,$4,$2) RETURNING `+controlColumns, p.TenantID, s.ID, sequence, p.ID, string(payload)))
	if err != nil {
		return s, err
	}
	if err = record(ctx, tx, p, s, "control_requested", nil, c); err != nil {
		return s, err
	}
	deadline := now.Add(time.Duration(in.DeadlineMinutes) * time.Minute)
	if in.Level == "pause_quickly" {
		deadline = minTime(deadline, now.Add(2*time.Minute))
	} else if in.Level == "wrap_up" || in.Level == "pause" {
		deadline = minTime(deadline, now.Add(10*time.Minute))
	}
	if in.deadlineAt != nil {
		deadline = *in.deadlineAt
	}
	next := Pause{ControlID: c.ID, State: "requested", RequestedBy: p.ID, RequestedAt: now, Reason: in.Reason, DeadlineAt: deadline, Level: in.Level, Note: in.Note, StartsAt: in.startsAt, LeavingID: in.leavingID}
	s, err = savePause(ctx, tx, p, s, next, "pause_requested")
	if err != nil {
		return s, err
	}
	if in.Level == "stop_now" && (in.startsAt == nil || !in.startsAt.After(now)) {
		return stopPausedWork(ctx, tx, p, s, "stop_now")
	}
	return advancePause(ctx, tx, p, s)
}

func pauseDeadlinePassed(ctx context.Context, tx pgx.Tx, pause *Pause) (bool, error) {
	var expired bool
	err := tx.QueryRow(ctx, `SELECT clock_timestamp()>=$1::timestamptz`, pause.DeadlineAt).Scan(&expired)
	return expired, err
}

// The caller holds the session row lock. Expiry releases only the cooperative
// control, never the worker lease or process ownership. Paused handovers remain
// resumable even when their original planning deadline is in the past.
func expirePause(ctx context.Context, tx pgx.Tx, p tenant.Principal, s Session) (Session, error) {
	var err error
	s, err = advancePause(ctx, tx, p, s)
	if err != nil {
		return s, err
	}
	if s.Pause == nil || (s.Pause.State != "requested" && s.Pause.State != "planned") {
		return s, nil
	}
	expired, err := pauseDeadlinePassed(ctx, tx, s.Pause)
	if err != nil || !expired {
		return s, err
	}
	before, err := scanControl(tx.QueryRow(ctx, `SELECT `+controlColumns+` FROM harness_controls WHERE id=$1 AND session_id=$2 FOR UPDATE`, s.Pause.ControlID, s.ID))
	if err != nil {
		return s, err
	}
	if before.State != "completed" {
		after, err := scanControl(tx.QueryRow(ctx, `UPDATE harness_controls SET state='completed',outcome='rejected',reason='pause_deadline_expired',claimed_at=coalesce(claimed_at,clock_timestamp()),completed_at=clock_timestamp() WHERE id=$1 RETURNING `+controlColumns, before.ID))
		if err != nil {
			return s, err
		}
		if err = record(ctx, tx, p, s, "control_completed", before, after); err != nil {
			return s, err
		}
	}
	next := *s.Pause
	next.State = "cancelled"
	s, err = savePause(ctx, tx, p, s, next, "pause_cancelled")
	if err != nil || next.Level == "" || s.StoppedAt != nil || s.ArchivedAt != nil {
		return s, err
	}
	return stopPausedWork(ctx, tx, p, s, "pause_deadline")
}

// Reuse the tenant's periodic lost-contact runner, including for managed
// sessions which keep heartbeating and generations waiting to revive.
func sweepPauseDeadlines(ctx context.Context, tx pgx.Tx, tenantID string) error {
	rows, err := tx.Query(ctx, `SELECT `+sessionColumns+` FROM harness_sessions
 WHERE archived_at IS NULL AND pause_record->>'state' IN ('requested','planned')
 AND ((pause_record->>'deadline_at')::timestamptz<=clock_timestamp()+interval '2 minutes' OR pause_record->>'leaving_request_id' IS NOT NULL)
 ORDER BY id LIMIT $1 FOR UPDATE SKIP LOCKED`, lostContactBatch)
	if err != nil {
		return err
	}
	var expired []Session
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			rows.Close()
			return err
		}
		expired = append(expired, s)
	}
	rows.Close()
	if err = rows.Err(); err != nil || len(expired) == 0 {
		return err
	}
	system := tenant.Principal{TenantID: tenantID, Kind: tenant.Agent, Name: "System", Roles: []string{"system"}}
	if err = tx.QueryRow(ctx, `SELECT aeon_authz_system_actor($1::uuid)::text`, tenantID).Scan(&system.ID); err != nil {
		return err
	}
	for _, s := range expired {
		if _, err = expirePause(ctx, tx, system, s); err != nil {
			return err
		}
	}
	return nil
}

func (m *Module) planPause(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in struct {
		ControlID string `json:"control_id"`
		Point     string `json:"handover_point"`
	}
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if !workorders.UUID(in.ControlID) || !utf8.ValidString(in.Point) || strings.TrimSpace(in.Point) == "" || utf8.RuneCountInString(in.Point) > 1000 || strings.ContainsRune(in.Point, 0) {
		return nil, workorders.Fail(400, "exact pause control and bounded handover point required")
	}
	s, err := worker(r.Context(), tx, r, p)
	if err != nil {
		return nil, err
	}
	if s.Pause == nil || s.Pause.ControlID != strings.ToLower(in.ControlID) {
		return nil, workorders.Fail(409, "pause request changed")
	}
	if expired, err := pauseDeadlinePassed(r.Context(), tx, s.Pause); err != nil {
		return nil, err
	} else if expired {
		return nil, workorders.Fail(409, "pause deadline expired")
	}
	next := *s.Pause
	var now time.Time
	if err := tx.QueryRow(r.Context(), `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return nil, err
	}
	if next.Level == "stop_now" || next.StopRequested || next.StartsAt != nil && next.StartsAt.After(now) {
		return nil, workorders.Fail(409, "pause is not yet eligible for a cooperative handover")
	}
	if next.State == "planned" && next.HandoverPoint == in.Point {
		return s, nil
	}
	if next.State != "requested" {
		return nil, workorders.Fail(409, "pause must be requested before planning")
	}
	if err = tx.QueryRow(r.Context(), `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return nil, err
	}
	next.State, next.HandoverPoint, next.PlannedAt = "planned", in.Point, &now
	c, err := scanControl(tx.QueryRow(r.Context(), `UPDATE harness_controls SET state='claimed',claimed_at=clock_timestamp() WHERE id=$1 AND session_id=$2 AND state='pending' RETURNING `+controlColumns, next.ControlID, s.ID))
	if err != nil {
		return nil, err
	}
	if err = record(r.Context(), tx, p, s, "control_claimed", nil, c); err != nil {
		return nil, err
	}
	return savePause(r.Context(), tx, p, s, next, "pause_planned")
}

func (h *Handover) validate() error {
	if h == nil || strings.TrimSpace(h.State) == "" || !utf8.ValidString(h.State) || utf8.RuneCountInString(h.State) > 4000 || len(h.NextSteps) < 1 || len(h.NextSteps) > 20 || h.OpenQuestions == nil || len(h.OpenQuestions) > 20 {
		return workorders.Fail(400, "handover state, next steps and open questions required")
	}
	for _, list := range [][]string{h.NextSteps, h.OpenQuestions} {
		for _, item := range list {
			if !utf8.ValidString(item) || strings.TrimSpace(item) == "" || utf8.RuneCountInString(item) > 1000 {
				return workorders.Fail(400, "invalid handover item")
			}
		}
	}
	if h.WorktreeState != "committed" && h.WorktreeState != "clean" && h.WorktreeState != "rolled_back" {
		return workorders.Fail(400, "finish, commit or roll back the current step first")
	}
	if h.CommitSHA != "" || h.WorktreeState == "committed" {
		if len(h.CommitSHA) < 7 || len(h.CommitSHA) > 40 || strings.Trim(h.CommitSHA, "0123456789abcdefABCDEF") != "" {
			return workorders.Fail(400, "committed WIP requires a commit SHA")
		}
	}
	h.CommitSHA = strings.ToLower(h.CommitSHA)
	raw, _ := json.Marshal(h)
	if len(raw) > 16000 || strings.ContainsRune(string(raw), 0) {
		return workorders.Fail(400, "handover exceeds 16000 bytes")
	}
	return nil
}

func handoverBrief(h Handover) string {
	return "State:\n" + h.State + "\n\nNext steps:\n- " + strings.Join(h.NextSteps, "\n- ") + "\n\nOpen questions:\n" + strings.Join(h.OpenQuestions, "\n") + "\n\nWorktree: " + h.WorktreeState + "\nCommit: " + h.CommitSHA
}

func finishPause(ctx context.Context, tx pgx.Tx, p tenant.Principal, s Session, h *Handover) (Session, error) {
	if s.Pause == nil || s.Pause.State != "planned" {
		return s, workorders.Fail(409, "plan a requested pause before stopping")
	}
	if expired, err := pauseDeadlinePassed(ctx, tx, s.Pause); err != nil {
		return s, err
	} else if expired {
		return s, workorders.Fail(409, "pause deadline expired")
	}
	if err := h.validate(); err != nil {
		return s, err
	}
	next := *s.Pause
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return s, err
	}
	next.State, next.Handover, next.PausedAt = "paused", h, &now
	c, err := scanControl(tx.QueryRow(ctx, `UPDATE harness_controls SET state='completed',outcome='applied',reason='paused',completed_at=clock_timestamp() WHERE id=$1 AND session_id=$2 AND state='claimed' RETURNING `+controlColumns, next.ControlID, s.ID))
	if err != nil {
		return s, err
	}
	if err = record(ctx, tx, p, s, "control_completed", nil, c); err != nil {
		return s, err
	}
	if s.TicketNodeID != nil {
		_, err = events.Append(ctx, tx, p, events.Change{NodeID: s.TicketNodeID, Type: "comment.created", After: map[string]any{"body_markdown": fmt.Sprintf("Paused session %s\n\n%s", s.ID, handoverBrief(*h))}})
		if err != nil {
			return s, err
		}
	}
	s, err = savePause(ctx, tx, p, s, next, "paused")
	if err != nil {
		return s, err
	}
	return closeGeneration(ctx, tx, p, s, "paused")
}

type resumeProof struct {
	Ref   string `json:"harness_session_ref"`
	Lease string `json:"worker_lease"`
	Host  string `json:"host"`
}
type resumeRequest struct {
	CoordinatorID string       `json:"coordinator_session_id"`
	Registration  *resumeProof `json:"registration"`
}
type resumeResult struct {
	Session      Session        `json:"session"`
	Continuation Continuation   `json:"continuation"`
	Registration map[string]any `json:"registration"`
	Successor    *Session       `json:"successor,omitempty"`
}

func resumeRecipe(s Session) resumeResult {
	c := Continuation{SucceedsID: s.ID, Handover: *s.Pause.Handover, Brief: handoverBrief(*s.Pause.Handover)}
	out := resumeResult{Session: s, Continuation: c, Registration: map[string]any{
		"succeeds_session_id": s.ID, "agent_principal_id": s.AgentPrincipalID, "harness": s.Harness, "host": s.Host, "management_mode": s.Management, "role": s.Role, "parent_harness_session_id": s.ParentID, "ticket_node_id": s.TicketNodeID, "work_shape": s.WorkShape, "advertised_capabilities": s.Capabilities,
		"display_label": s.DisplayLabel, "model": s.Model, "reasoning_effort": s.ReasoningEffort, "account_label": s.AccountLabel, "harness_version": s.HarnessVersion, "worktree": s.Worktree, "branch": s.Branch, "brief": "Continuation of " + s.ID,
	}}
	if s.Generator != nil {
		out.Registration["generator"] = *s.Generator
	}
	if s.Command != nil {
		out.Registration["command"] = *s.Command
	}
	return out
}

func requestResume(ctx context.Context, tx pgx.Tx, p tenant.Principal, s Session) (resumeResult, error) {
	if s.ArchivedAt != nil || s.StoppedAt == nil || s.StopReason == nil || *s.StopReason != "paused" || s.Pause == nil || s.Pause.Handover == nil || (s.Pause.State != "paused" && s.Pause.State != "resume_requested" && s.Pause.State != "resumed") {
		return resumeResult{}, workorders.Fail(409, "paused session with a handover required")
	}
	if s.Pause.State == "paused" {
		next := *s.Pause
		var now time.Time
		if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
			return resumeResult{}, err
		}
		next.State, next.ResumeRequestedAt = "resume_requested", &now
		var err error
		s, err = savePause(ctx, tx, p, s, next, "resume_requested")
		if err != nil {
			return resumeResult{}, err
		}
	}
	return resumeRecipe(s), nil
}

func (m *Module) resumePause(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in resumeRequest
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if err := lockHierarchy(r.Context(), tx, r.PathValue("projectId")); err != nil {
		return nil, err
	}
	owner, admin, parent, err := pauseController(r, tx, p, in.CoordinatorID)
	if err != nil {
		return nil, err
	}
	s, err := load(r.Context(), tx, r.PathValue("projectId"), r.PathValue("sessionId"), true)
	if err != nil {
		return nil, err
	}
	if !pauseAllowed(s, owner, admin, parent) {
		return nil, workorders.Fail(403, "session owner or proven parent coordinator required")
	}
	out, err := requestResume(r.Context(), tx, p, s)
	if err != nil || in.Registration == nil {
		return out, err
	}
	if err = authz.RequireTx(r.Context(), tx, p, "harness.write", authz.Scope{ProjectID: s.ProjectID}); err != nil {
		return nil, err
	}
	if p.Kind == tenant.Agent && p.ID != s.AgentPrincipalID {
		return nil, workorders.Fail(403, "an agent may resume only its own principal")
	}
	out.Registration["harness_session_ref"], out.Registration["worker_lease"] = in.Registration.Ref, in.Registration.Lease
	if in.Registration.Host != "" {
		out.Registration["host"] = in.Registration.Host
	}
	raw, err := json.Marshal(out.Registration)
	if err != nil {
		return nil, err
	}
	rr := r.Clone(r.Context())
	rr.Body = ioBody(raw)
	next, err := m.register(rr, tx, p)
	if err != nil {
		return nil, err
	}
	successor := next.(Session)
	delete(out.Registration, "harness_session_ref")
	delete(out.Registration, "worker_lease")
	out.Successor = &successor
	out.Session, err = load(r.Context(), tx, s.ProjectID, s.ID, false)
	return out, err
}

func (m *Module) pauseBatch(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	return m.transitionPauseBatch(r, tx, p, false)
}
func (m *Module) resumeBatch(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	return m.transitionPauseBatch(r, tx, p, true)
}

func (m *Module) transitionPauseBatch(r *http.Request, tx pgx.Tx, p tenant.Principal, resume bool) (any, error) {
	var in pauseRequest
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	return m.transitionPauseBatchInput(r, tx, p, resume, in, 200, 200)
}

type pauseSkippedSession struct {
	ID           string  `json:"id"`
	ProjectID    string  `json:"project_id"`
	DisplayLabel *string `json:"display_label,omitempty"`
	Reason       string  `json:"reason"`
}

func (m *Module) transitionPauseBatchInput(r *http.Request, tx pgx.Tx, p tenant.Principal, resume bool, in pauseRequest, limit, skipLimit int) (any, error) {
	if !resume {
		if err := resolvePauseDefault(r.Context(), tx, p, &in); err != nil {
			return nil, err
		}
	}
	if err := in.validate(); err != nil {
		return nil, err
	}
	if err := lockHierarchy(r.Context(), tx, r.PathValue("projectId")); err != nil {
		return nil, err
	}
	owner, admin, parent, err := pauseController(r, tx, p, in.CoordinatorID)
	if err != nil {
		return nil, err
	}
	if in.Except == nil {
		in.Except = []string{}
	}
	filter := `stopped_at IS NULL AND (pause_record IS NULL OR pause_record->>'state'='cancelled'
 OR (pause_record->>'state' IN ('requested','planned') AND (pause_record->>'deadline_at')::timestamptz<=clock_timestamp()))`
	if resume {
		filter = `stopped_at IS NOT NULL AND stop_reason='paused' AND pause_record->>'state'='paused'`
	}
	selection := ` FROM harness_sessions WHERE project_id=$1 AND archived_at IS NULL AND ` + filter + `
 AND (owner_principal_id=$2::uuid OR $3 OR parent_id=$4::uuid) AND NOT(id=ANY($5::uuid[]))`
	capable := ""
	if !resume && in.Level != "stop_now" {
		// Unsupported managed workers must not roll back other pauses or
		// occupy the first page forever when the caller repeats a batch.
		capable = ` AND ('inbox'=ANY(capabilities) OR 'pause'=ANY(capabilities))`
	}
	rows, err := tx.Query(r.Context(), `SELECT `+sessionColumns+selection+capable+` ORDER BY id LIMIT $6 FOR UPDATE`, r.PathValue("projectId"), nullable(owner), admin, nullable(parent), in.Except, limit+1)
	if err != nil {
		return nil, err
	}
	sessions := []Session{}
	for rows.Next() {
		s, e := scanSession(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		sessions = append(sessions, s)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	more := len(sessions) > limit
	if more {
		sessions = sessions[:limit]
	}
	items := []any{}
	for _, s := range sessions {
		if resume {
			out, e := requestResume(r.Context(), tx, p, s)
			if e != nil {
				return nil, e
			}
			items = append(items, out)
		} else {
			out, e := requestPause(r.Context(), tx, p, s, in)
			if e != nil {
				return nil, e
			}
			items = append(items, out)
		}
	}
	result := map[string]any{"items": items, "more": more}
	if !resume {
		unsupported := ` AND NOT('inbox'=ANY(capabilities) OR 'pause'=ANY(capabilities))`
		if in.Level == "stop_now" {
			unsupported = ` AND false`
		}
		rows, err := tx.Query(r.Context(), `SELECT id::text,project_id::text,display_label`+selection+`
 `+unsupported+` ORDER BY id LIMIT $6`, r.PathValue("projectId"), nullable(owner), admin, nullable(parent), in.Except, skipLimit+1)
		if err != nil {
			return nil, err
		}
		skipped := []pauseSkippedSession{}
		for rows.Next() {
			s := pauseSkippedSession{Reason: "inbox_delivery_unavailable"}
			if err := rows.Scan(&s.ID, &s.ProjectID, &s.DisplayLabel); err != nil {
				rows.Close()
				return nil, err
			}
			skipped = append(skipped, s)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		result["skipped_more"] = len(skipped) > skipLimit
		if len(skipped) > skipLimit {
			skipped = skipped[:skipLimit]
		}
		result["skipped"] = skipped
	}
	return result, nil
}

func (m *Module) pauseAll(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	return m.transitionAllPauses(r, tx, p, false)
}
func (m *Module) resumeAll(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	return m.transitionAllPauses(r, tx, p, true)
}

func (m *Module) transitionAllPauses(r *http.Request, tx pgx.Tx, p tenant.Principal, resume bool) (any, error) {
	if p.Kind != tenant.Person {
		return nil, workorders.Fail(403, "workspace pause and resume require a person; coordinators use their project")
	}
	var in pauseRequest
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if err := in.validate(); err != nil {
		return nil, err
	}
	if err := authz.RequireTx(r.Context(), tx, p, "harness.control", authz.Scope{AnyProject: true}); err != nil {
		return nil, err
	}
	allowed, err := authz.ProjectsTx(r.Context(), tx, p)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(r.Context(), `SELECT DISTINCT project_id::text FROM harness_sessions WHERE archived_at IS NULL ORDER BY project_id::text`)
	if err != nil {
		return nil, err
	}
	projects := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		if allowed("harness.control", id) {
			projects = append(projects, id)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	items := []any{}
	skipped := []pauseSkippedSession{}
	skippedMore := false
	more := false
	for _, id := range projects {
		projectRequest := r.Clone(r.Context())
		projectRequest.SetPathValue("projectId", id)
		result, e := m.transitionPauseBatchInput(projectRequest, tx, p, resume, in, 200-len(items), 200-len(skipped))
		if e != nil {
			return nil, e
		}
		batch := result.(map[string]any)
		items = append(items, batch["items"].([]any)...)
		if !resume {
			skipped = append(skipped, batch["skipped"].([]pauseSkippedSession)...)
			skippedMore = skippedMore || batch["skipped_more"].(bool)
		}
		if batch["more"].(bool) {
			more = true
			break
		}
	}
	result := map[string]any{"items": items, "more": more}
	if !resume {
		result["skipped"], result["skipped_more"] = skipped, skippedMore
	}
	return result, nil
}

// A paused worker is the sole extension to the coordinator predecessor rule.
// This is checked while the hierarchy and predecessor row are locked.
func pausedPredecessor(s Session, in registration) error {
	if s.StoppedAt == nil || s.StopReason == nil || *s.StopReason != "paused" || s.Pause == nil || s.Pause.Handover == nil || s.Pause.State != "resume_requested" || s.HandedOverToID != nil || s.ArchivedAt != nil {
		return workorders.Fail(409, "requested paused predecessor required")
	}
	if s.AgentPrincipalID != in.AgentPrincipalID || s.Role != in.Role || s.Harness != in.Harness || !same(s.Generator, in.Generator) || !same(s.Command, in.Command) || s.Management != in.Management || !same(s.Branch, in.Branch) || !same(s.Worktree, in.Worktree) || !same(s.Model, in.Model) || !same(s.ReasoningEffort, in.ReasoningEffort) || !same(s.TicketNodeID, in.TicketNodeID) || s.WorkShape != in.WorkShape || !same(s.ParentID, in.ParentID) || in.RunID != nil || in.WorkOrderID != nil {
		return workorders.Fail(409, "continuation must retain principal, harness, generator, command, model, branch, worktree and ticket")
	}
	return nil
}

func inheritPauseRegistration(ctx context.Context, tx pgx.Tx, projectID string, in *registration) error {
	old, err := load(ctx, tx, projectID, *in.SucceedsID, true)
	if err != nil {
		return err
	}
	if old.Pause == nil || old.StopReason == nil || *old.StopReason != "paused" {
		return nil
	}
	for _, pair := range []struct {
		dst **string
		src *string
	}{
		{&in.Generator, old.Generator}, {&in.Command, old.Command},
		{&in.Model, old.Model}, {&in.ReasoningEffort, old.ReasoningEffort}, {&in.Worktree, old.Worktree}, {&in.Branch, old.Branch},
		{&in.AccountLabel, old.AccountLabel}, {&in.DisplayLabel, old.DisplayLabel}, {&in.HarnessVersion, old.HarnessVersion},
		{&in.ParentID, old.ParentID}, {&in.TicketNodeID, old.TicketNodeID},
	} {
		if *pair.dst == nil {
			*pair.dst = pair.src
		}
	}
	if in.WorkShape == "" || in.WorkShape == "unknown" {
		in.WorkShape = old.WorkShape
	}
	if in.Capabilities == nil {
		in.Capabilities = old.Capabilities
	}
	if in.Brief == nil {
		label := "Continuation of " + old.ID
		in.Brief = &label
	}
	return nil
}

func completeResume(ctx context.Context, tx pgx.Tx, p tenant.Principal, old, next Session) (Session, error) {
	c := Continuation{SucceedsID: old.ID, Handover: *old.Pause.Handover, Brief: handoverBrief(*old.Pause.Handover)}
	raw, err := json.Marshal(c)
	if err != nil {
		return next, err
	}
	next, err = scanSession(tx.QueryRow(ctx, `UPDATE harness_sessions SET continuation_handover=$2::jsonb,owner_principal_id=$3,revision=revision+1 WHERE id=$1 RETURNING `+sessionColumns, next.ID, string(raw), old.ownerID))
	if err != nil {
		return next, err
	}
	if old.Role == "coordinator" {
		if err = adoptChildren(ctx, tx, p, old, next); err != nil {
			return next, err
		}
		// Closed paused children still need a live parent for their continuation.
		// Ordinary ended children keep their historical parent, as before.
		rows, e := tx.Query(ctx, `SELECT `+sessionColumns+` FROM harness_sessions WHERE parent_id=$1 AND stop_reason='paused' AND archived_at IS NULL AND pause_record->>'state' IN ('paused','resume_requested') ORDER BY id FOR UPDATE`, old.ID)
		if e != nil {
			return next, e
		}
		children := []Session{}
		for rows.Next() {
			child, e := scanSession(rows)
			if e != nil {
				rows.Close()
				return next, e
			}
			children = append(children, child)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return next, e
		}
		for _, child := range children {
			adopted, e := scanSession(tx.QueryRow(ctx, `UPDATE harness_sessions SET parent_id=$2,adopted_from_id=$3,revision=revision+1 WHERE id=$1 RETURNING `+sessionColumns, child.ID, next.ID, old.ID))
			if e != nil {
				return next, e
			}
			if e = record(ctx, tx, p, adopted, "adopted", child, adopted); e != nil {
				return next, e
			}
		}
	} else {
		_, err = tx.Exec(ctx, `UPDATE harness_sessions SET handed_over_to_id=$2,revision=revision+1 WHERE id=$1`, old.ID, next.ID)
		if err != nil {
			return next, err
		}
	}
	updated := *old.Pause
	updated.State, updated.SuccessorID = "resumed", next.ID
	_, err = savePause(ctx, tx, p, old, updated, "resumed")
	return next, err
}

// Decode accepts at most the endpoint's existing body limit. Resume registers
// through the same validation and transaction as an ordinary generation.
func ioBody(raw []byte) io.ReadCloser { return io.NopCloser(strings.NewReader(string(raw))) }
