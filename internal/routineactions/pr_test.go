// SPDX-License-Identifier: AGPL-3.0-only
package routineactions

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/approvals"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/routineactions/evidence"
	"github.com/inspr-at/paimos/internal/routineguard"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type externalFixture struct {
	t                                         *testing.T
	d                                         *dbtest.DB
	person, agent                             tenant.Principal
	project, target, run, attempt, recurrence string
	b                                         *Broker
	now                                       time.Time
}

func (f *externalFixture) tx(fn func(pgx.Tx) error) {
	f.t.Helper()
	if err := db.InTenant(dbtest.Seed(f.t.Context()), f.d.App, f.person.TenantID, fn); err != nil {
		f.t.Fatal(err)
	}
}
func (f *externalFixture) exec(sql string, args ...any) {
	f.t.Helper()
	f.tx(func(tx pgx.Tx) error { _, err := tx.Exec(f.t.Context(), sql, args...); return err })
}
func externalSetup(t *testing.T) *externalFixture {
	t.Helper()
	f := &externalFixture{t: t, d: dbtest.Open(t), now: time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)}
	f.person.Kind = tenant.Person
	f.agent.Kind = tenant.Agent
	f.agent.Scopes = []string{"work_orders.write", "nodes.write", "approvals.propose", "approvals.read"}
	if err := f.d.Admin.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('external-actions','External fixture') RETURNING id::text`).Scan(&f.person.TenantID); err != nil {
		t.Fatal(err)
	}
	f.agent.TenantID = f.person.TenantID
	f.tx(func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Owner') RETURNING id::text`, f.person.TenantID).Scan(&f.person.ID); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Worker') RETURNING id::text`, f.person.TenantID).Scan(&f.agent.ID)
	})
	dbtest.BindRole(t, f.d, f.person.TenantID, f.person.ID, "owner")
	dbtest.BindRole(t, f.d, f.person.TenantID, f.agent.ID, "admin")
	f.tx(func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'EXT-1','External project' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, f.person.TenantID).Scan(&f.project); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id) SELECT $1,id,'EXT-2','External work',$2 FROM node_kinds WHERE tenant_id=$1 AND slug='work' RETURNING id::text`, f.person.TenantID, f.project).Scan(&f.target); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO recurrences(tenant_id,project_id,parent_id,template,trigger,created_by_principal_id) VALUES($1,$2,$3,'{}','{"kind":"event"}',$4) RETURNING id::text`, f.person.TenantID, f.project, f.target, f.person.ID).Scan(&f.recurrence); err != nil {
			return err
		}
		assignment := json.RawMessage(`{"allowed_actions":["pr.open","pipeline.request","work.create"],"sources":[]}`)
		if _, err := tx.Exec(t.Context(), `INSERT INTO recurrence_definitions(tenant_id,recurrence_id,scope_type,scope_project_id,owner_principal_id,output_project_id,output_parent_id,assignment) VALUES($1,$2,'project',$3,$4,$3,$5,$6)`, f.person.TenantID, f.recurrence, f.project, f.person.ID, f.target, assignment); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO recurrence_occurrences(tenant_id,recurrence_id,occurrence_key,number,scheduled_at,node_id,outcome) VALUES($1,$2,'manual:test',1,now(),$3,'created')`, f.person.TenantID, f.recurrence, f.target); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO routine_runs(tenant_id,recurrence_id,occurrence_key,definition_revision,scope_type,scope_project_id,owner_principal_id,output_project_id,output_parent_id,assignment,assignment_digest,policy_snapshot,policy_digest,work_node_id,source_receipt,state,execute_consent,consent_revision,execution_principal_id) VALUES($1,$2,'manual:test',1,'project',$3,$4,$3,$5,$6,$7,'{}',$7,$5,'{}','running',true,1,$8) RETURNING id::text`, f.person.TenantID, f.recurrence, f.project, f.person.ID, f.target, assignment, routineguard.Digest(assignment), f.agent.ID).Scan(&f.run); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO routine_attempts(tenant_id,run_id,attempt_key,role,principal_id,assignment_digest,state) VALUES($1,$2,'build','build',$3,$4,'running') RETURNING id::text`, f.person.TenantID, f.run, f.agent.ID, routineguard.Digest(assignment)).Scan(&f.attempt)
	})
	// A qualified evaluator fixture returns a decision through the real private
	// latch verifier. No model is launched and no production qualification is
	// claimed. Live owner/caller/run/target authority remains the broker's code.
	f.b = New(f.d.App, func(ctx context.Context, tx pgx.Tx, p tenant.Principal, b Binding, c routineguard.Context) (tenant.Principal, routineguard.Decision, string, error) {
		d, err := routineguard.Evaluate(nil, c)
		if err != nil {
			return p, d, "", err
		}
		var in Request
		if err := json.Unmarshal([]byte(c.Text), &in); err != nil {
			return p, d, "", err
		}
		binding := routineguard.ActionBinding{TenantID: p.TenantID, RunID: b.RunID, ActionID: b.RunID, AuthorRunID: b.RunID, ProjectID: b.ProjectID, TargetID: in.TargetID, TargetRevision: in.ExpectedRevision, TargetUpdatedAt: time.UnixMicro(in.ExpectedRevision), Kind: in.Kind, PayloadDigest: routineguard.Digest(in), HeadSHA: strings.Repeat("b", 40)}
		r := routineguard.EvaluationRequest{Binding: binding, ContextDigest: d.ContextDigest, PolicyDigest: d.PolicyDigest, FamilyPolicy: reviewgate.DefaultFamilyPolicy(), Author: modelregistry.VerifiedModel{Harness: "codex", RequestedModel: "gpt-6.1-sol", EffectiveModel: "gpt-6.1-sol", Evidence: "vendor_reported"}, ProfileID: b.RunID, AccountID: b.RunID, Harness: "claude", Model: "opus", Family: "anthropic", Capability: routineguard.EvaluationCapability, CreatedAt: f.now, Deadline: f.now.Add(routineguard.MaxEvaluationTime)}
		r.Digest = routineguard.Digest(r)
		d, err = routineguard.VerifyEvaluation(nil, c, binding, r, "completed", modelregistry.VerifiedModel{Harness: "claude", RequestedModel: "opus", EffectiveModel: "claude-opus-5-5", Evidence: "vendor_reported"}, routineguard.StructuredVerdict{RequestDigest: r.Digest, Verdict: routineguard.Allow, Reason: "Qualified fixture exact result."}, r.FamilyPolicy, f.now)
		if err != nil {
			return p, d, "", err
		}
		var id string
		err = tx.QueryRow(ctx, `SELECT id::text FROM routine_guard_evaluations WHERE recurrence_id=$1 AND context_digest=$2 LIMIT 1`, b.RecurrenceID, d.ContextDigest).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			raw, _ := json.Marshal(d)
			err = tx.QueryRow(ctx, `INSERT INTO routine_guard_evaluations(tenant_id,recurrence_id,scope_kind,scope_project_id,owner_principal_id,output_project_id,definition_revision,checkpoint,policy_digest,context_digest,hard_version,preset_version,result,required_evaluation,decision) VALUES($1,$2,'project',$3,$4,$3,1,'action',$5,$6,$7,$8,'allow',true,$9) RETURNING id::text`, p.TenantID, b.RecurrenceID, b.ProjectID, b.OwnerID, d.PolicyDigest, d.ContextDigest, d.HardVersion, d.PresetVersion, raw).Scan(&id)
		}
		return f.agent, d, id, err
	})
	return f
}
func (f *externalFixture) request(key, kind string) Request {
	var at time.Time
	f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(f.t.Context(), `SELECT updated_at FROM nodes WHERE id=$1`, f.target).Scan(&at)
	})
	return Request{AttemptID: f.attempt, ActionKey: key, Kind: kind, ProjectID: f.project, TargetID: f.target, ExpectedRevision: at.UnixMicro()}
}

type lostPull struct {
	mu          sync.Mutex
	pull        *Pull
	posts       int
	unavailable bool
	afterFind   func()
}

func (p *lostPull) Configured(string, string) bool { return !p.unavailable }
func (p *lostPull) Find(_ context.Context, _ PROpen, _ string) (*Pull, error) {
	p.mu.Lock()
	var out *Pull
	if p.pull != nil {
		v := *p.pull
		out = &v
	}
	hook := p.afterFind
	p.mu.Unlock()
	if hook != nil {
		hook()
	}
	return out, nil
}
func (p *lostPull) Open(_ context.Context, in PROpen, ref string) (*Pull, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.posts++
	p.pull = &Pull{Number: 7, Repository: in.Repository, Branch: in.Branch, BaseBranch: in.BaseBranch, BaseSHA: in.BaseSHA, HeadSHA: in.HeadSHA, OperationRef: ref, Open: true}
	return nil, errors.New("injected response loss after creation")
}

// Risks R1/R8/R17: duplicate irreversible PR effects after response loss or
// concurrent retry, success from unavailable App/stale head, and authority
// revoked between the read and final write. Barriers establish ordering.
func TestRoutinePRResponseLossAndFinalAuthority(t *testing.T) {
	f := externalSetup(t)
	in := f.request("open-pr", "pr.open")
	in.PROpen = &PROpen{Repository: "example/routine", Branch: "work/change", BaseBranch: "main", BaseSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("b", 40), Title: "EXT-2: change"}
	p := &lostPull{unavailable: true}
	f.b.WithPullRequests(p)
	out, err := f.b.Execute(t.Context(), f.agent, f.run, in)
	if err != nil || out.State != "pending" || out.WaitReason != "github_app_unavailable" || p.posts != 0 {
		t.Fatalf("unavailable falsely succeeded: %+v %v", out, err)
	}
	p.unavailable = false
	out, err = f.b.Execute(t.Context(), f.agent, f.run, in)
	if err != nil || out.State != "pending" || p.posts != 1 {
		t.Fatalf("response loss: %+v %v posts=%d", out, err, p.posts)
	}
	// Restart the broker, then race two recovery reads of the same durable send.
	restarted := New(f.d.App, f.b.authorize).WithPullRequests(p)
	start := make(chan struct{})
	results := make(chan Receipt, 2)
	failures := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			r, e := restarted.ReconcilePR(t.Context(), f.agent.TenantID, out.ID)
			results <- r
			failures <- e
		}()
	}
	close(start)
	for range 2 {
		if e := <-failures; e != nil {
			t.Fatal(e)
		}
		r := <-results
		if r.State != "succeeded" || r.PullRequest != 7 || r.OperationRef != out.OperationRef {
			t.Fatalf("reconciliation: %+v", r)
		}
	}
	if p.posts != 1 {
		t.Fatalf("duplicate PR posts=%d", p.posts)
	}
	changed := in
	changed.PROpen = &PROpen{}
	*changed.PROpen = *in.PROpen
	changed.PROpen.HeadSHA = strings.Repeat("c", 40)
	_, err = f.b.Execute(t.Context(), f.agent, f.run, changed)
	assertFailure(t, err, 409, "action_key_conflict")
	// A sent intent whose remote head changed stays pending without another POST.
	stale := in
	stale.ActionKey = "stale"
	p.pull = nil
	r, err := f.b.Execute(t.Context(), f.agent, f.run, stale)
	if err != nil {
		t.Fatal(err)
	}
	p.pull.HeadSHA = strings.Repeat("d", 40)
	r, err = f.b.ReconcilePR(t.Context(), f.agent.TenantID, r.ID)
	if err != nil || r.State != "pending" || r.WaitReason != "pr_binding_changed" || p.posts != 2 {
		t.Fatalf("stale PR: %+v %v", r, err)
	}
	// Block a read after its receipt was loaded. The owner revocation commits
	// while no network transaction holds the tenant/tree fence.
	entered, release := make(chan struct{}), make(chan struct{})
	p.afterFind = func() { close(entered); <-release }
	result := make(chan error, 1)
	go func() { _, err := f.b.ReconcilePR(t.Context(), f.agent.TenantID, r.ID); result <- err }()
	<-entered
	f.exec(`DELETE FROM role_bindings WHERE principal_id=$1`, f.person.ID)
	close(release)
	if err := <-result; !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("revocation wrong failure: %v", err)
	}
	if p.posts != 2 {
		t.Fatal("revocation created another PR")
	}
}

// Risks R1/R15/R17: non-code output claiming completion while an action is
// pending, worker self-approval, or an unrelated/stale person approval.
func TestRoutineNonCodeCompletionNeedsSettledActionsAndPerson(t *testing.T) {
	f := externalSetup(t)
	in := f.request("pipeline", "pipeline.request")
	in.PipelineRequest = &PipelineRequest{NonCode: true, ResultDigest: strings.Repeat("e", 64)}
	out, err := f.b.Execute(t.Context(), f.agent, f.run, in)
	if err != nil || out.State != "pending" {
		t.Fatalf("prepare: %+v %v", out, err)
	}
	_, err = f.b.ReconcilePipeline(t.Context(), f.agent.TenantID, out.ID)
	assertFailure(t, err, 409, "coordinator_pipeline_handoff_required")
	f.exec(`UPDATE routine_attempts SET state='completed',process_receipt='{"process_state":"exited"}' WHERE run_id=$1`, f.run)
	f.exec(`UPDATE routine_runs SET state='awaiting_pipeline' WHERE id=$1`, f.run)
	f.exec(`INSERT INTO routine_actions(tenant_id,run_id,action_key,kind,request_digest,state) VALUES($1,$2,'required-knowledge','knowledge.write',$3,'pending')`, f.agent.TenantID, f.run, strings.Repeat("a", 64))
	_, err = f.b.ReconcilePipeline(t.Context(), f.agent.TenantID, out.ID)
	assertFailure(t, err, 409, "catalogue_actions_unsettled")
	f.exec(`UPDATE routine_actions SET state='succeeded' WHERE run_id=$1 AND action_key='required-knowledge'`, f.run)
	r, err := f.b.ReconcilePipeline(t.Context(), f.agent.TenantID, out.ID)
	if err != nil || r.State != "awaiting_decision" || r.ApprovalID == "" || r.PullRequest != 0 {
		t.Fatalf("non-code person handoff: %+v %v", r, err)
	}
	mux := http.NewServeMux()
	approvals.New(f.d.App).Mount(mux)
	decide := func(p tenant.Principal, id string, code int) {
		t.Helper()
		req := httptest.NewRequest("POST", "/api/approvals/"+id+"/decision", strings.NewReader(`{"decision":"approved"}`)).WithContext(tenant.WithPrincipal(t.Context(), p))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != code {
			t.Fatalf("decision: %d %s", w.Code, w.Body.String())
		}
	}
	decide(f.agent, r.ApprovalID, 403)
	decide(f.person, r.ApprovalID, 200)
	// The exact approved hold settles the non-code result without a PR.
	r, err = f.b.ReconcilePipeline(t.Context(), f.agent.TenantID, out.ID)
	if err != nil || r.State != "succeeded" || r.PullRequest != 0 {
		t.Fatalf("approved non-code: %+v %v", r, err)
	}
	var state string
	var prs int
	f.tx(func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `SELECT state FROM routine_runs WHERE id=$1`, f.run).Scan(&state); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM routine_actions WHERE run_id=$1 AND kind='pr.open'`, f.run).Scan(&prs)
	})
	if state != "completed" || prs != 0 {
		t.Fatalf("fabricated PR/completion: %s %d", state, prs)
	}
}

// qualifiedPipeline models the read-only adapter's recorded review/CI facts.
// Delivery's behaviour test separately exercises its real required-check and
// artifact policies; these observations exercise binding and person decisions.
type qualifiedPipeline struct {
	facts  evidence.PipelineFacts
	review evidence.Review
	ready  bool
}

func (p *qualifiedPipeline) RoutineSnapshot(context.Context, string, string, string, string) (evidence.PipelineFacts, error) {
	f := p.facts
	f.ObservedAt = time.Now().UTC()
	return f, nil
}
func (p *qualifiedPipeline) RoutineReadyTx(context.Context, pgx.Tx, string, string, string, evidence.PipelineFacts) (string, bool, error) {
	return strings.Repeat("e", 64), p.ready, nil
}
func (p *qualifiedPipeline) RoutineReviewTx(context.Context, pgx.Tx, string, string) (evidence.Review, error) {
	return p.review, nil
}
func (p *qualifiedPipeline) RoutineMergeResultTx(context.Context, pgx.Tx, string, string, string, string, int64) (bool, string, error) {
	return false, "merge_done_receipt_pending_or_refused", nil
}

// Risk R15/R17: AEON-1008 necessarily changes updated_at when it marks Done.
// Observation must recognize that exact receipt without granting a stale write
// to the node. Stale review/CI and a changed queue SHA require a fresh person
// decision, and a revoked approval must still hold the routine.
func TestRoutineMergeObservationKeepsDoneRevisionAndApproval(t *testing.T) {
	f := externalSetup(t)
	f.b.WithObservation(func(_ context.Context, _ pgx.Tx, _ tenant.Principal, _ Binding) (tenant.Principal, error) {
		return f.agent, nil
	})
	prIn := f.request("pr", "pr.open")
	prIn.PROpen = &PROpen{Repository: "example/routine", Branch: "work/change", BaseBranch: "main", BaseSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("b", 40), Title: "EXT-2: change"}
	p := &lostPull{}
	f.b.WithPullRequests(p)
	pr, err := f.b.Execute(t.Context(), f.agent, f.run, prIn)
	if err != nil {
		t.Fatal(err)
	}
	pr, err = f.b.ReconcilePR(t.Context(), f.agent.TenantID, pr.ID)
	if err != nil || pr.State != "succeeded" {
		t.Fatalf("PR fixture: %+v %v", pr, err)
	}
	in := f.request("pipeline", "pipeline.request")
	in.PipelineRequest = &PipelineRequest{PRActionKey: "pr", HeadSHA: prIn.PROpen.HeadSHA}
	out, err := f.b.Execute(t.Context(), f.agent, f.run, in)
	if err != nil {
		t.Fatal(err)
	}
	f.exec(`UPDATE routine_attempts SET state='completed',process_receipt='{"process_state":"exited"}' WHERE run_id=$1`, f.run)
	f.exec(`UPDATE routine_runs SET state='awaiting_pipeline' WHERE id=$1`, f.run)
	prNumber := int64(7)
	pipeline := &qualifiedPipeline{facts: evidence.PipelineFacts{Facts: evidence.Facts{PR: &prNumber, Base: prIn.PROpen.BaseSHA, PullHead: strings.Repeat("f", 40)}, ArtifactSHA: prIn.PROpen.HeadSHA}, ready: true,
		review: evidence.Review{OrderID: f.run, Repository: prIn.PROpen.Repository, BaseSHA: prIn.PROpen.BaseSHA, HeadSHA: prIn.PROpen.HeadSHA, Verdict: "ok", PolicyDigest: strings.Repeat("e", 64)}}
	f.b.WithPipeline(pipeline)
	out, err = f.b.ReconcilePipeline(t.Context(), f.agent.TenantID, out.ID)
	if err != nil || out.State != "pending" || out.WaitReason != "pipeline_head_changed" || out.ApprovalID != "" {
		t.Fatalf("stale head: %+v %v", out, err)
	}
	pipeline.facts.Facts.PullHead = prIn.PROpen.HeadSHA
	out, err = f.b.ReconcilePipeline(t.Context(), f.agent.TenantID, out.ID)
	if err != nil || out.State != "pending" || out.WaitReason != "pipeline_review_stale" || out.ApprovalID != "" {
		t.Fatalf("stale review: %+v %v", out, err)
	}
	pipeline.review.GateOpen = true
	out, err = f.b.ReconcilePipeline(t.Context(), f.agent.TenantID, out.ID)
	if err != nil || out.State != "awaiting_decision" || out.ApprovalID == "" {
		t.Fatalf("code person handoff: %+v %v", out, err)
	}
	mux := http.NewServeMux()
	approvals.New(f.d.App).Mount(mux)
	decide := func(person tenant.Principal, id string, want int) {
		t.Helper()
		req := httptest.NewRequest("POST", "/api/approvals/"+id+"/decision", strings.NewReader(`{"decision":"approved"}`)).WithContext(tenant.WithPrincipal(t.Context(), person))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("decision: %d %s", w.Code, w.Body.String())
		}
	}
	decide(f.agent, out.ApprovalID, 403)
	decide(f.person, out.ApprovalID, 200)
	out, err = f.b.ReconcilePipeline(t.Context(), f.agent.TenantID, out.ID)
	if err != nil || out.State != "awaiting_merge" {
		t.Fatalf("approved pipeline: %+v %v", out, err)
	}
	previousApproval := out.ApprovalID
	pipeline.facts.Facts.Queued = true
	pipeline.facts.Facts.QueueHead = strings.Repeat("c", 40)
	pipeline.ready = false // The new merge SHA's checks/artifact remain unavailable.
	out, err = f.b.ReconcilePipeline(t.Context(), f.agent.TenantID, out.ID)
	if err != nil || out.State != "pending" || out.WaitReason != "pipeline_ci_or_artifact_pending" {
		t.Fatalf("stale queue proof: %+v %v", out, err)
	}
	pipeline.facts.ArtifactSHA = pipeline.facts.Facts.QueueHead
	pipeline.ready = true
	out, err = f.b.ReconcilePipeline(t.Context(), f.agent.TenantID, out.ID)
	if err != nil || out.State != "awaiting_decision" || out.ApprovalID == previousApproval {
		t.Fatalf("queue reused approval: %+v %v", out, err)
	}
	decide(f.person, out.ApprovalID, 200)
	out, err = f.b.ReconcilePipeline(t.Context(), f.agent.TenantID, out.ID)
	if err != nil || out.State != "awaiting_merge" {
		t.Fatalf("refreshed pipeline: %+v %v", out, err)
	}
	holdID := out.ApprovalID
	if _, done, err := f.b.observeMerge(t.Context(), f.agent.TenantID, out.ID); err != nil || done {
		t.Fatalf("missing merge falsely completed: done=%v %v", done, err)
	}
	// Represent the exact receipt and revision produced by the existing writer.
	f.exec(`UPDATE nodes SET state='done',updated_at=updated_at+interval '1 second' WHERE id=$1`, f.target)
	f.exec(`INSERT INTO events(tenant_id,node_id,actor_principal_id,type,metadata) VALUES($1,$2,$3,'delivery.merge_done',jsonb_build_object('repository',$4::text,'head_sha',$5::text,'merge_sha',$6::text,'pull_request',7))`, f.agent.TenantID, f.target, f.person.ID, prIn.PROpen.Repository, prIn.PROpen.HeadSHA, strings.Repeat("e", 40))
	f.exec(`UPDATE agent_permission_grants SET revoked_at=clock_timestamp() WHERE approval_request_id=$1`, holdID)
	if _, done, err := f.b.observeMerge(t.Context(), f.agent.TenantID, out.ID); err != nil || done {
		t.Fatalf("revoked approval falsely completed: done=%v %v", done, err)
	}
	f.exec(`UPDATE agent_permission_grants SET revoked_at=NULL WHERE approval_request_id=$1`, holdID)
	var before, after time.Time
	f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT updated_at FROM nodes WHERE id=$1`, f.target).Scan(&before)
	})
	r, err := f.b.ReconcilePipeline(t.Context(), f.agent.TenantID, out.ID)
	if err != nil || r.State != "succeeded" {
		t.Fatalf("exact observed Done: %+v %v", r, err)
	}
	f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT updated_at FROM nodes WHERE id=$1`, f.target).Scan(&after)
	})
	if !before.Equal(after) {
		t.Fatal("routine observation rewrote work status/revision")
	}
}
