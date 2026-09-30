// SPDX-License-Identifier: AGPL-3.0-only

// Package crossreview productises independent review work orders. It never
// merges, changes branch protection, or broadens an enrollment approval.
package crossreview

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
)

type Module struct {
	pool      *pgxpool.Pool
	publisher StatusPublisher
}

func New(pool *pgxpool.Pool, publisher StatusPublisher) *Module {
	return &Module{pool: pool, publisher: publisher}
}
func (m *Module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/nodes/{nodeId}/reviews", workorders.Endpoint(m.pool, "work_orders.read", false, 200, m.list))
	mux.HandleFunc("POST /api/nodes/{nodeId}/reviews", workorders.Endpoint(m.pool, "work_orders.write", false, 201, m.create))
}

type CreateInput struct {
	RequestID    string  `json:"request_id"`
	Repository   string  `json:"repository"`
	BaseSHA      string  `json:"base_sha"`
	HeadSHA      string  `json:"head_sha"`
	AuthorRunID  *string `json:"author_run_id"`
	AuthorFamily string  `json:"author_family"`
	PullRequest  *int64  `json:"pull_request"`
}
type Review struct {
	reviewgate.Binding
	OrderID        string                    `json:"work_order_id"`
	RequestID      string                    `json:"request_id"`
	RunID          *string                   `json:"run_id"`
	Status         string                    `json:"status"`
	Model          *string                   `json:"reviewer_model"`
	Effort         *string                   `json:"reviewer_effort"`
	ProfileVersion *string                   `json:"reviewer_profile_version"`
	EffectiveModel *string                   `json:"effective_model"`
	GateOpen       bool                      `json:"gate_open"`
	GateReason     string                    `json:"gate_reason"`
	Result         reviewgate.Result         `json:"result"`
	Ladder         []modelregistry.Candidate `json:"ladder"`
	Cost           *int64                    `json:"cost_micros"`
	Duration       *int64                    `json:"duration_seconds"`
	CreatedAt      time.Time                 `json:"created_at"`
	GitHubStatus   string                    `json:"github_status"`
	modelEvidence  string
}

func (m *Module) create(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	id := r.PathValue("nodeId")
	if !workorders.UUID(id) {
		return nil, workorders.Fail(400, "invalid ticket id")
	}
	var in CreateInput
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if !workorders.UUID(in.RequestID) || !reviewgate.ValidRepository(in.Repository) || !reviewgate.ValidSHA(in.BaseSHA) || !reviewgate.ValidSHA(in.HeadSHA) || in.BaseSHA == in.HeadSHA || in.AuthorRunID != nil && !workorders.UUID(*in.AuthorRunID) || in.PullRequest != nil && *in.PullRequest < 1 {
		return nil, workorders.Fail(400, "request id, repository and distinct full commit hashes required")
	}
	ctx := r.Context()
	// Same serialization as managed starts: pairing -> ticket -> order -> run.
	if err := agentpairing.Lock(ctx, tx); err != nil {
		return nil, err
	}
	var title, body, key, kind string
	var projectID *string
	var fields json.RawMessage
	err := tx.QueryRow(ctx, `SELECT n.key,n.title,n.body,n.project_id::text,n.fields,k.slug FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=$1 AND n.deleted_at IS NULL FOR UPDATE OF n`, id).Scan(&key, &title, &body, &projectID, &fields, &kind)
	if err != nil {
		return nil, err
	}
	if kind != "ticket" && kind != "task" {
		return nil, workorders.Fail(400, "review requires a ticket or task")
	}
	scope := authz.Scope{}
	if projectID != nil {
		scope.ProjectID = *projectID
	}
	for _, perm := range []string{"work_orders.write", "run.create"} {
		if err = authz.RequireTx(ctx, tx, p, perm, scope); err != nil {
			return nil, err
		}
	}
	family := in.AuthorFamily
	if in.AuthorRunID != nil {
		var owner, f string
		err = tx.QueryRow(ctx, `SELECT r.agent_principal_id::text,p.family FROM agent_runs r
            JOIN work_orders w ON w.tenant_id=r.tenant_id AND w.node_id=r.work_order_id
            JOIN nodes n ON n.tenant_id=w.tenant_id AND n.id=w.node_id
            JOIN model_profiles p ON p.tenant_id=r.tenant_id AND p.id=r.model_profile_id
            WHERE r.id=$1 AND n.parent_id=$2 AND n.deleted_at IS NULL AND w.kind='build' AND r.status='completed'`, *in.AuthorRunID, id).Scan(&owner, &f)
		if err != nil {
			return nil, workorders.Fail(400, "author run must be a completed build for this ticket")
		}
		if p.Kind == tenant.Agent && owner != p.ID {
			return nil, workorders.Fail(403, "only the author may request its review")
		}
		if family != "" && family != f {
			return nil, workorders.Fail(400, "author family conflicts with the author run")
		}
		family = f
	} else if p.Kind != tenant.Person {
		return nil, workorders.Fail(403, "agents must name their completed author run")
	}
	if !reviewgate.ValidFamily(family) {
		return nil, workorders.Fail(400, "known author family required")
	}
	request, _ := json.Marshal(in)
	var oldID string
	var oldRequest json.RawMessage
	var oldTicket string
	err = tx.QueryRow(ctx, `SELECT work_order_id::text,request,ticket_node_id::text FROM work_order_reviews WHERE request_id=$1`, in.RequestID).Scan(&oldID, &oldRequest, &oldTicket)
	if err == nil {
		var old CreateInput
		if json.Unmarshal(oldRequest, &old) != nil {
			return nil, workorders.Fail(500, "invalid stored request")
		}
		a, _ := json.Marshal(old)
		if string(a) != string(request) || oldTicket != id {
			return nil, workorders.Fail(409, "request id is already bound to another review")
		}
		return load(ctx, tx, oldID)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil {
		return nil, err
	}
	route, err := modelregistry.ResolveReview(ctx, tx, p, family, scope.ProjectID, now)
	if err != nil {
		return nil, err
	}
	var assignee *string
	b := reviewgate.Binding{TicketID: id, Repository: in.Repository, BaseSHA: in.BaseSHA, HeadSHA: in.HeadSHA, AuthorRunID: in.AuthorRunID, AuthorFamily: family, PullRequest: in.PullRequest}
	if route.Profile != nil {
		b.ProfileID = &route.Profile.ID
		b.ReviewerFamily = &route.Profile.Family
		assignee = &route.Account.RegisteredBy
	}
	var f map[string]json.RawMessage
	_ = json.Unmarshal(fields, &f)
	acceptance := ""
	if value := f["acceptance_criteria"]; len(value) > 0 {
		if json.Unmarshal(value, &acceptance) != nil {
			acceptance = string(value)
		}
	}
	snapshot := key + ": " + title + "\n\n" + body
	if strings.TrimSpace(acceptance) != "" {
		snapshot += "\n\nAcceptance criteria:\n" + acceptance
	}
	if len(snapshot) > 128<<10 {
		return nil, workorders.Fail(400, "ticket exceeds review prompt bound")
	}
	duration := int64(1800)
	o, err := workorders.Create(ctx, tx, p, workorders.CreateInput{Title: "Review " + key + " · " + in.HeadSHA[:12], Body: snapshot, Parent: &id, Assignee: assignee, MaxDuration: &duration, Criteria: []string{"Review the pinned commit range against the ticket and return a final verdict with file:line findings."}})
	if err != nil {
		return nil, err
	}
	ladder, _ := json.Marshal(route.Ladder)
	githubStatus := "unconfigured"
	if m.publisher != nil && in.PullRequest != nil && m.publisher.Configured(p.TenantID, in.Repository) {
		githubStatus = "pending"
	}
	_, err = tx.Exec(ctx, `INSERT INTO work_order_reviews(tenant_id,work_order_id,ticket_node_id,request_id,request,repository,base_sha,head_sha,author_run_id,author_family,reviewer_profile_id,reviewer_family,pull_request,ladder,github_status,ticket_snapshot)
        VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`, p.TenantID, o.NodeID, id, in.RequestID, request, b.Repository, b.BaseSHA, b.HeadSHA, b.AuthorRunID, b.AuthorFamily, b.ProfileID, b.ReviewerFamily, b.PullRequest, ladder, githubStatus, snapshot)
	if err != nil {
		return nil, err
	}
	status := "blocked"
	if route.Profile != nil {
		status = "ready"
	}
	if _, err = tx.Exec(ctx, `UPDATE work_orders SET kind='review',status=$2,revision=revision+1,updated_at=clock_timestamp() WHERE node_id=$1`, o.NodeID, status); err != nil {
		return nil, err
	}
	o, err = workorders.Load(ctx, tx, o.NodeID, true)
	if err != nil {
		return nil, err
	}
	if route.Profile != nil {
		if _, err = agentruns.QueueReview(ctx, tx, p, o, *assignee, route.Profile.ID, route.Account.ID); err != nil {
			return nil, err
		}
	}
	out, err := load(ctx, tx, o.NodeID)
	if err != nil {
		return nil, err
	}
	return out, workorders.Record(ctx, tx, p, id, "review.requested", nil, out)
}

func (m *Module) list(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	id := r.PathValue("nodeId")
	if !workorders.UUID(id) {
		return nil, workorders.Fail(400, "invalid ticket id")
	}
	var exists bool
	if err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM nodes WHERE id=$1 AND deleted_at IS NULL)`, id).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, pgx.ErrNoRows
	}
	rows, err := tx.Query(r.Context(), `SELECT work_order_id::text FROM work_order_reviews WHERE ticket_node_id=$1 ORDER BY created_at DESC,work_order_id DESC LIMIT 50`, id)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	out := []Review{}
	for _, id := range ids {
		v, err := load(r.Context(), tx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func load(ctx context.Context, tx pgx.Tx, id string) (Review, error) {
	var v Review
	var raw, ladder json.RawMessage
	var orderStatus string
	err := tx.QueryRow(ctx, `SELECT v.work_order_id::text,v.ticket_node_id::text,v.request_id::text,v.repository,v.base_sha,v.head_sha,v.author_run_id::text,v.author_family,v.reviewer_profile_id::text,v.reviewer_family,v.pull_request,
        v.run_id::text,coalesce(r.status,'blocked'),p.model,p.effort,p.version,r.effective_model,coalesce(r.model_evidence,''),v.result,v.ladder,v.created_at,v.github_status,w.status,
        CASE WHEN EXISTS(SELECT 1 FROM run_telemetry t WHERE t.tenant_id=r.tenant_id AND t.run_id=r.id AND t.cost_micros_delta>0) THEN r.cost_micros END,
        CASE WHEN r.started_at IS NOT NULL THEN greatest(0,extract(epoch FROM(coalesce(r.ended_at,clock_timestamp())-r.started_at)))::bigint END
        FROM work_order_reviews v JOIN work_orders w ON w.tenant_id=v.tenant_id AND w.node_id=v.work_order_id
        LEFT JOIN agent_runs r ON r.tenant_id=v.tenant_id AND r.id=v.run_id
        LEFT JOIN model_profiles p ON p.tenant_id=v.tenant_id AND p.id=v.reviewer_profile_id WHERE v.work_order_id=$1`, id).Scan(
		&v.OrderID, &v.TicketID, &v.RequestID, &v.Repository, &v.BaseSHA, &v.HeadSHA, &v.AuthorRunID, &v.AuthorFamily, &v.ProfileID, &v.ReviewerFamily, &v.PullRequest, &v.RunID, &v.Status, &v.Model, &v.Effort, &v.ProfileVersion, &v.EffectiveModel, &v.modelEvidence, &raw, &ladder, &v.CreatedAt, &v.GitHubStatus, &orderStatus, &v.Cost, &v.Duration)
	if err != nil {
		return v, err
	}
	if err = json.Unmarshal(raw, &v.Result); err != nil {
		return v, err
	}
	if err = json.Unmarshal(ladder, &v.Ladder); err != nil {
		return v, err
	}
	v.GateOpen, v.GateReason = reviewgate.Gate(v.Status, v.modelEvidence, v.EffectiveModel, v.Binding, v.Result)
	if v.GateOpen && (v.Model == nil || v.EffectiveModel == nil || !reviewgate.ModelMatches(*v.Model, *v.EffectiveModel)) {
		v.GateOpen = false
		v.GateReason = "The effective reviewer model differs from its pinned profile."
	}
	if orderStatus == "cancelled" {
		v.GateOpen = false
		v.GateReason = "Review was cancelled; request a new review."
	}
	return v, nil
}

// RequestForRun runs in the builder's terminal telemetry transaction. It grants
// no additional permissions. Missing permissions leave an auditable closed gate
// without preventing the builder from reporting its completion.
func (m *Module) RequestForRun(ctx context.Context, tx pgx.Tx, p tenant.Principal, runID string, commitRange reviewgate.CommitRange) error {
	var ticket string
	var project *string
	err := tx.QueryRow(ctx, `SELECT parent.id::text,parent.project_id::text FROM agent_runs r
      JOIN work_orders w ON w.tenant_id=r.tenant_id AND w.node_id=r.work_order_id
      JOIN nodes n ON n.tenant_id=w.tenant_id AND n.id=w.node_id
      JOIN nodes parent ON parent.tenant_id=n.tenant_id AND parent.id=n.parent_id
      JOIN node_kinds k ON k.tenant_id=parent.tenant_id AND k.id=parent.kind_id
      WHERE r.id=$1 AND r.agent_principal_id=$2 AND r.status='completed' AND w.kind='build'
      AND n.deleted_at IS NULL AND parent.deleted_at IS NULL AND k.slug IN ('ticket','task')`, runID, p.ID).Scan(&ticket, &project)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	scope := authz.Scope{}
	if project != nil {
		scope.ProjectID = *project
	}
	for _, perm := range []string{"work_orders.write", "run.create"} {
		if err = authz.RequireTx(ctx, tx, p, perm, scope); err != nil {
			if errors.Is(err, authz.ErrForbidden) {
				return workorders.Record(ctx, tx, p, ticket, "review.unavailable", nil, map[string]string{"author_run_id": runID, "reason": "Author lacks permission to request an independent review; gate remains closed."})
			}
			return err
		}
	}
	raw, _ := json.Marshal(CreateInput{RequestID: runID, Repository: commitRange.Repository, BaseSHA: commitRange.BaseSHA, HeadSHA: commitRange.HeadSHA, AuthorRunID: &runID})
	r, _ := http.NewRequestWithContext(ctx, "POST", "/api/nodes/"+ticket+"/reviews", bytes.NewReader(raw))
	r.SetPathValue("nodeId", ticket)
	_, err = m.create(r, tx, p)
	var validation *workorders.Error
	if errors.As(err, &validation) && validation.Status >= 400 && validation.Status < 500 {
		return workorders.Record(ctx, tx, p, ticket, "review.unavailable", nil, map[string]string{"author_run_id": runID, "reason": "Automatic review could not bind this run and commit range; gate remains closed. Request a review from the ticket."})
	}
	return err
}
