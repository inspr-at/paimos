// SPDX-License-Identifier: AGPL-3.0-only
package agentruns

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// WorkerHandoff lives in the existing run trace, under the run's row lock and
// event history. The run ID is the assignment ID: no second queue or lease.
// Local process ownership remains in the daemon's durable journal.
type WorkerHandoff struct {
	RunID             string     `json:"run_id"`
	WorkOrderID       string     `json:"work_order_id"`
	WorkerPrincipalID string     `json:"worker_principal_id"`
	ProjectID         string     `json:"project_id"`
	LeadSessionID     string     `json:"lead_session_id"`
	LeadGeneration    int64      `json:"lead_generation"`
	TicketID          string     `json:"ticket_id"`
	TicketRevision    time.Time  `json:"ticket_revision"`
	WorkOrderRevision int64      `json:"work_order_revision"`
	BriefSHA256       string     `json:"brief_sha256"`
	WorktreeID        string     `json:"worktree_id,omitempty"`
	State             string     `json:"state"`
	AcceptedAt        *time.Time `json:"accepted_at,omitempty"`
	LaunchedAt        *time.Time `json:"launched_at,omitempty"`
	CompletedAt       *time.Time `json:"completed_at,omitempty"`
	Outcome           string     `json:"outcome,omitempty"`
}

type WorkerPickup struct {
	TicketRevision    time.Time `json:"ticket_revision"`
	WorkOrderRevision int64     `json:"work_order_revision"`
	BriefSHA256       string    `json:"brief_sha256"`
	WorktreeID        string    `json:"worktree_id"`
}

func runHandoff(v Run) (*WorkerHandoff, error) {
	var trace struct {
		Assignment *WorkerHandoff `json:"worker_assignment"`
	}
	if len(v.Trace) > 0 {
		if err := json.Unmarshal(v.Trace, &trace); err != nil {
			return nil, err
		}
	}
	h := trace.Assignment
	if h != nil && (h.RunID != v.ID || h.WorkOrderID != v.OrderID || h.WorkerPrincipalID != v.AgentID || !workorders.UUID(h.ProjectID) || !workorders.UUID(h.TicketID)) {
		return nil, workorders.Fail(409, "assignment identity conflict")
	}
	return h, nil
}

func storeHandoff(ctx context.Context, tx pgx.Tx, id string, h *WorkerHandoff) error {
	raw, err := json.Marshal(h)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE agent_runs SET trace=jsonb_set(coalesce(trace,'{}'::jsonb),'{worker_assignment}',$2::jsonb) WHERE id=$1`, id, raw)
	return err
}

func freezeHandoff(ctx context.Context, tx pgx.Tx, t queueTicket, v Run, agent string, lead []byte) error {
	if len(lead) == 0 {
		return nil
	}
	var binding struct {
		SessionID  string `json:"session_id"`
		Generation int64  `json:"generation"`
	}
	if err := json.Unmarshal(lead, &binding); err != nil {
		return err
	}
	if t.ProjectID == nil || !workorders.UUID(binding.SessionID) || binding.Generation < 1 {
		return workorders.Fail(409, "lead assignment identity unavailable")
	}
	o, err := workorders.Load(ctx, tx, v.OrderID, true)
	if err != nil {
		return err
	}
	h := &WorkerHandoff{RunID: v.ID, WorkOrderID: v.OrderID, WorkerPrincipalID: agent, ProjectID: *t.ProjectID, LeadSessionID: binding.SessionID, LeadGeneration: binding.Generation, TicketID: t.ID, WorkOrderRevision: o.Revision, State: "assigned"}
	if err = tx.QueryRow(ctx, `SELECT updated_at FROM nodes WHERE id=$1`, t.ID).Scan(&h.TicketRevision); err != nil {
		return err
	}
	h.BriefSHA256, err = orderBrief(ctx, tx, o)
	if err != nil {
		return err
	}
	return storeHandoff(ctx, tx, v.ID, h)
}

func orderBrief(ctx context.Context, tx pgx.Tx, o workorders.Order) (string, error) {
	var title, body string
	if err := tx.QueryRow(ctx, `SELECT title,body FROM nodes WHERE id=$1`, o.NodeID).Scan(&title, &body); err != nil {
		return "", err
	}
	criteria := make([]string, 0, len(o.Criteria))
	for _, c := range o.Criteria {
		criteria = append(criteria, c.Description)
	}
	return workorders.BriefDigest(title, body, criteria)
}

func validatePickupHandoff(ctx context.Context, tx pgx.Tx, p tenant.Principal, v Run, o workorders.Order, in *WorkerPickup) error {
	h, err := runHandoff(v)
	if err != nil || h == nil {
		if err == nil && in != nil {
			return workorders.Fail(409, "run has no lead assignment")
		}
		if err == nil && v.Status == "queued" && v.QueueNodeID != nil {
			var configured bool
			if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes t JOIN project_leads l ON l.project_id=t.project_id WHERE t.id=$1)`, *v.QueueNodeID).Scan(&configured); err != nil {
				return err
			}
			if configured {
				return workorders.Fail(409, "durable lead assignment required; queue again")
			}
		}
		return err
	}
	if err = authz.RequireTx(ctx, tx, p, "run.claim", authz.Scope{ProjectID: h.ProjectID}); err != nil {
		return err
	}
	if in == nil || in.TicketRevision.IsZero() || in.WorkOrderRevision < 1 || !digest64(in.BriefSHA256) || !digest64(in.WorktreeID) {
		return workorders.Fail(400, "exact lead assignment pickup required")
	}
	if in.WorkOrderRevision != h.WorkOrderRevision || !in.TicketRevision.Equal(h.TicketRevision) || in.BriefSHA256 != h.BriefSHA256 || h.WorktreeID != "" && in.WorktreeID != h.WorktreeID {
		return workorders.Fail(409, "assignment pickup conflict")
	}
	if v.Status != "queued" {
		return nil // Exact replay stays bound to the original writer, even after lead succession.
	}
	var revision time.Time
	var project *string
	if err = tx.QueryRow(ctx, `SELECT updated_at,project_id::text FROM nodes WHERE id=$1`, h.TicketID).Scan(&revision, &project); err != nil {
		return err
	}
	brief, err := orderBrief(ctx, tx, o)
	if err != nil {
		return err
	}
	if project == nil || *project != h.ProjectID || !revision.Equal(h.TicketRevision) || o.Revision != h.WorkOrderRevision || brief != h.BriefSHA256 {
		return workorders.Fail(409, "assignment brief or ticket changed; queue again")
	}
	if err = requireWriterFree(ctx, tx, h.TicketID, v.ID); err != nil {
		return err
	}
	var workspaceBusy bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runs WHERE id<>$1 AND trace->'worker_assignment'->>'worktree_id'=$2 AND trace->'worker_assignment'->>'state' IN ('launch_unknown','launched'))`, v.ID, in.WorktreeID).Scan(&workspaceBusy); err != nil {
		return err
	}
	if workspaceBusy {
		return workorders.Fail(409, "assignment worktree already has an unconfirmed writer")
	}
	return nil
}

// Acceptance is written only after all claim checks have passed. Validation
// above has no side effects, so a committed obsolete-route release cannot
// retain a writer which was never accepted.
func acceptPickupHandoff(ctx context.Context, tx pgx.Tx, v Run, in *WorkerPickup) error {
	h, err := runHandoff(v)
	if err != nil || h == nil {
		return err
	}
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return err
	}
	h.State, h.WorktreeID, h.AcceptedAt = "launch_unknown", in.WorktreeID, &now
	return storeHandoff(ctx, tx, v.ID, h)
}

func digest64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'f' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// Tenant/tree fence serializes this check with every queue/claim writer.
// Lost heartbeat, administrative archival and terminal labels never free it.
func requireWriterFree(ctx context.Context, tx pgx.Tx, ticket, exclude string) error {
	var busy bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runs r JOIN nodes o ON o.id=r.work_order_id
	 WHERE (r.queue_node_id=$1 OR o.parent_id=$1) AND r.id::text<>$2
	 AND (r.status IN ('starting','running','waiting') OR r.trace->'worker_assignment'->>'state' IN ('launch_unknown','launched')))
	 OR EXISTS(SELECT 1 FROM harness_sessions s WHERE (s.ticket_node_id=$1 OR s.work_order_id IN(SELECT id FROM nodes WHERE parent_id=$1)
	 OR s.run_id IN(SELECT id FROM agent_runs WHERE queue_node_id=$1)) AND NOT aeon_work_session_stopped(s.stopped_at,s.stop_reason))`, ticket, exclude).Scan(&busy)
	if err != nil {
		return err
	}
	if busy {
		return workorders.Fail(409, "previous writer exit or launch outcome is unconfirmed")
	}
	return nil
}

func reportHandoff(ctx context.Context, tx pgx.Tx, p tenant.Principal, v Run, t Telemetry, status string) error {
	h, err := runHandoff(v)
	if err != nil || h == nil {
		return err
	}
	if err = authz.RequireTx(ctx, tx, p, "run.telemetry", authz.Scope{ProjectID: h.ProjectID}); err != nil {
		return err
	}
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return err
	}
	if terminal(status) {
		if t.Kind != "finished" || t.ProcessState == "" {
			return workorders.Fail(409, "assignment completion requires local process evidence")
		}
		if t.ProcessState == "not_attempted" && h.LaunchedAt != nil {
			return workorders.Fail(409, "launch receipt conflicts with no-launch evidence")
		}
		if t.ProcessState == "unconfirmed" {
			if status != "ownership_lost" {
				return workorders.Fail(409, "unconfirmed writer requires ownership_lost")
			}
		} else {
			h.State, h.CompletedAt, h.Outcome = "completed", &now, status
		}
	} else if t.Kind == "started" && h.LaunchedAt == nil {
		h.State, h.LaunchedAt = "launched", &now
	}
	return storeHandoff(ctx, tx, v.ID, h)
}

func (m *module) getHandoff(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	v, err := load(r.Context(), tx, r.PathValue("runId"), false)
	if err != nil {
		return nil, err
	}
	h, err := runHandoff(v)
	if err != nil {
		return nil, err
	}
	if h == nil {
		return nil, workorders.Fail(404, "lead assignment not found")
	}
	if err = authz.RequireTx(r.Context(), tx, p, "run.read", authz.Scope{ProjectID: h.ProjectID}); err != nil {
		return nil, err
	}
	return h, nil
}
