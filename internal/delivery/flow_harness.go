// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// Only linked changes enter Flow. A harness session alone does not assert a
// delivery, and an old event must not attach to a ticket moved to another project.
func (m *Module) flowHarnessItemTx(ctx context.Context, tx pgx.Tx, ticket, project, repository string, pr int64) (*flowBatch, error) {
	if !workorders.UUID(ticket) || !workorders.UUID(project) || repository != m.config.Repository {
		return nil, nil
	}
	t, err := flowTicketTx(ctx, tx, ticket)
	if err != nil || t == nil || t.Project != project {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT pull_request FROM delivery_items WHERE project_id=$1 AND ticket_node_id=$2 AND repository=$3 AND pull_request IS NOT NULL AND ($4::bigint=0 OR pull_request=$4) ORDER BY pull_request LIMIT 20`, project, ticket, repository, pr)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	prs := []int64{}
	for rows.Next() {
		var n int64
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		prs = append(prs, n)
	}
	if err := rows.Err(); err != nil || len(prs) == 0 {
		return nil, err
	}
	return &flowBatch{Item: flowItemInput{Project: project, Kind: "change", Ref: t.Key, Title: t.Title, Ticket: &ticket, Repository: repository, PRs: prs}}, nil
}

// A worker can finish before LEAD opens the PR and the projector can already
// have consumed its events. Recover those builds from the current session rows
// whenever a linked feed projects the ticket, using the live events' same keys.
func (m *Module) flowWithHarnessBuildsTx(ctx context.Context, tx pgx.Tx, b *flowBatch) (*flowBatch, error) {
	if b == nil || b.Item.Kind != "change" || b.Item.Ticket == nil || len(b.Item.PRs) == 0 || b.Item.Repository != "" && b.Item.Repository != m.config.Repository {
		return b, nil
	}
	rows, err := tx.Query(ctx, `SELECT s.id::text,s.agent_principal_id::text,
	 CASE WHEN p.kind='person' THEN 'person' ELSE 'agent' END,s.model,s.created_at,s.stopped_at,
	 aeon_session_finished(s.stopped_at,s.stop_reason,s.progress_pct)
	 FROM harness_sessions s JOIN principals p ON p.tenant_id=s.tenant_id AND p.id=s.agent_principal_id
	 WHERE s.project_id=$1 AND s.ticket_node_id=$2 AND s.role='worker' AND s.work_shape='ship'
	 AND EXISTS(SELECT 1 FROM delivery_items d JOIN nodes n ON n.tenant_id=d.tenant_id AND n.id=d.ticket_node_id
	 WHERE d.project_id=s.project_id AND d.ticket_node_id=s.ticket_node_id AND n.project_id=s.project_id
	 AND d.repository=$3 AND d.pull_request=ANY($4::bigint[]))
	 ORDER BY s.created_at,s.id LIMIT 20`, b.Item.Project, *b.Item.Ticket, m.config.Repository, b.Item.PRs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	for _, s := range b.Steps {
		if s.Source == "paimos" {
			seen[s.Key] = true
		}
	}
	for rows.Next() {
		var id, principal string
		var finished bool
		step := flowStepInput{Source: "paimos", StepKey: "build", Round: 1, Kind: "work", Actor: FlowActor{Label: "Builder"}, Merge: true}
		if err := rows.Scan(&id, &principal, &step.Actor.Type, &step.Actor.Model, &step.Started, &step.Ended, &finished); err != nil {
			return nil, err
		}
		step.Key = "harness/" + id
		if seen[step.Key] {
			continue
		}
		step.Actor.PrincipalID = &principal
		if finished {
			step.Outcome = flowStr("ok")
		}
		b.Steps = append(b.Steps, step)
	}
	return b, rows.Err()
}

func (m *Module) flowFromHarnessTx(ctx context.Context, tx pgx.Tx, e flowSourceEvent) (*flowBatch, error) {
	if e.Type == "review_run.created" || e.Type == "coordinator_review.recorded" {
		return m.flowFromHarnessReviewTx(ctx, tx, e)
	}
	var s struct {
		ID        string     `json:"id"`
		Project   string     `json:"project_id"`
		Ticket    string     `json:"ticket_node_id"`
		Principal string     `json:"agent_principal_id"`
		Role      string     `json:"role"`
		Shape     string     `json:"work_shape"`
		Model     *string    `json:"model"`
		Created   time.Time  `json:"created_at"`
		Stopped   *time.Time `json:"stopped_at"`
		Finished  bool       `json:"finished"`
	}
	if json.Unmarshal(e.After, &s) != nil || !workorders.UUID(s.ID) || !workorders.UUID(s.Principal) || s.Role != "worker" || s.Shape != "ship" || s.Created.IsZero() || e.NodeID == nil || *e.NodeID != s.Project {
		return nil, nil
	}
	b, err := m.flowHarnessItemTx(ctx, tx, s.Ticket, s.Project, m.config.Repository, 0)
	if err != nil || b == nil {
		return nil, err
	}
	actor, err := flowAgentTx(ctx, tx, &s.Principal, "Builder")
	if err != nil {
		return nil, err
	}
	actor.Model = s.Model
	start := s.Created
	if e.Type == "harness.bound" {
		start = e.At
	}
	step := flowStepInput{Source: "paimos", Key: "harness/" + s.ID, StepKey: "build", Round: 1, Kind: "work", Actor: actor, Started: start, Merge: true}
	if e.Type == "harness.stopped" || e.Type == "harness.stop_confirmed" {
		step.Ended = s.Stopped
		// A process exit, a pause or 100% alone never proves a delivery.
		if s.Finished {
			step.Outcome = flowStr("ok")
		}
	}
	b.Steps = []flowStepInput{step}
	return b, nil
}

func (m *Module) flowFromHarnessReviewTx(ctx context.Context, tx pgx.Tx, e flowSourceEvent) (*flowBatch, error) {
	var snap struct {
		ID  string `json:"id"`
		Run string `json:"run_id"`
	}
	if json.Unmarshal(e.After, &snap) != nil || e.NodeID == nil {
		return nil, nil
	}
	id := snap.Run
	if e.Type == "review_run.created" {
		id = snap.ID
	}
	if !workorders.UUID(id) {
		return nil, nil
	}
	var ticket, project, repository, principal, model, verdict string
	var pr int64
	var created time.Time
	var submitted *time.Time
	err := tx.QueryRow(ctx, `SELECT ticket_node_id::text,project_id::text,repository,pull_request,reviewer_principal_id::text,model,created_at,submitted_at,coalesce(verdict,'') FROM review_runs WHERE id=$1 AND kind='review'`, id).
		Scan(&ticket, &project, &repository, &pr, &principal, &model, &created, &submitted, &verdict)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil || ticket != *e.NodeID {
		return nil, err
	}
	b, err := m.flowHarnessItemTx(ctx, tx, ticket, project, repository, pr)
	if err != nil || b == nil {
		return nil, err
	}
	actor, err := flowAgentTx(ctx, tx, &principal, "Reviewer")
	if err != nil {
		return nil, err
	}
	actor.Model = &model
	step := flowStepInput{Source: "paimos", Key: "review_run/" + id, StepKey: "review", Round: 1, Kind: "work", Actor: actor, Started: created, Merge: true}
	if e.Type == "coordinator_review.recorded" {
		if submitted == nil || verdict != "ok" && verdict != "changes" {
			return nil, nil
		}
		step.Ended, step.Outcome = submitted, &verdict
	}
	b.Steps = []flowStepInput{step}
	return b, nil
}

// Shipping proposals stay proposals. Only an executed claim records the action;
// a queue or merge decision does not assert that GitHub has actually merged it.
func (m *Module) flowFromShipTx(ctx context.Context, tx pgx.Tx, e flowSourceEvent) (*flowBatch, error) {
	var snap shipSnapshot
	if json.Unmarshal(e.After, &snap) != nil || snap.Decision == nil {
		return nil, nil
	}
	d := snap.Decision
	if !d.Execute || !d.Claimed || d.Facts.PR == nil || e.NodeID == nil || *e.NodeID != d.Project {
		return nil, nil
	}
	var ticket string
	err := tx.QueryRow(ctx, `SELECT ticket_node_id::text FROM delivery_items WHERE repository=$1 AND pull_request=$2 AND project_id=$3 AND ticket_node_id IS NOT NULL`, d.Repository, *d.Facts.PR, d.Project).Scan(&ticket)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	b, err := m.flowHarnessItemTx(ctx, tx, ticket, d.Project, d.Repository, *d.Facts.PR)
	if err != nil || b == nil {
		return nil, err
	}
	stepKey := "queue"
	if d.Action == "merge" || d.Action == "fix" {
		stepKey = "merge_round"
	} else if d.Action != "enqueue" && d.Action != "open_pr" && d.Action != "update_pr" && d.Action != "land" {
		return nil, nil
	}
	actor, err := flowAgentTx(ctx, tx, &d.Claimant, "LEAD")
	if err != nil {
		return nil, err
	}
	b.Steps = []flowStepInput{{Source: "paimos", Key: "ship/" + d.Request, StepKey: stepKey, Round: 1, Kind: "work", Actor: actor, Started: e.At, Ended: &e.At}}
	return b, nil
}

func flowMergeStep(item string, at time.Time) flowStepInput {
	return flowStepInput{Source: "paimos", Key: "merge/" + item, StepKey: "queue", Round: 1, Kind: "work",
		Actor: FlowActor{Type: "queue", Label: "Merge"}, Started: at, Ended: &at, Outcome: flowStr("green")}
}
