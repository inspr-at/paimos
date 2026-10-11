// SPDX-License-Identifier: AGPL-3.0-only
package routineexec

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/agentruns"
	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/approvals"
	"github.com/inspr-at/paimos/internal/crossreview"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/delivery"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/ownedprocess"
	"github.com/inspr-at/paimos/internal/recurrences"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/routineactions"
	"github.com/inspr-at/paimos/internal/routineactions/evidence"
	"github.com/inspr-at/paimos/internal/routinebudget"
	"github.com/inspr-at/paimos/internal/routineguard"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// qualificationAdapter transports an actual owned test-binary child. Only
// vendor observations are synthetic: consent, assignment, claim, settlement,
// action authorization, review, person decision and Done use their real owners.
// It deliberately does not certify native provider/tool isolation or replace
// agentd's separate journal/restart guards listed in the qualification catalogue.
type qualificationAdapter struct{ starts atomic.Int32 }

func (*qualificationAdapter) Name() string { return "controlled-fixture" }
func (a *qualificationAdapter) Start(ctx context.Context, in agentd.StartRequest, _ func(agentd.AdapterEvent)) (agentd.Process, error) {
	bin, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(bin, "-test.run=^TestRoutineControlledQualification$")
	cmd.Dir = in.Workspace
	// No inherited credentials, hooks, vendor profiles or model processes.
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "AEON_QUALIFICATION_CHILD=1", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull}
	input, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		_ = input.Close()
		return nil, err
	}
	child, err := ownedprocess.Start(ctx, cmd)
	if err != nil {
		_ = input.Close()
		_ = output.Close()
		return nil, err
	}
	a.starts.Add(1)
	p := &qualificationProcess{child: child, input: input, cmd: cmd, done: make(chan error, 1), ready: make(chan string, 2)}
	go func() {
		scanner := bufio.NewScanner(output)
		scanner.Buffer(make([]byte, 1024), 8192)
		if scanner.Scan() {
			p.ready <- scanner.Text()
		}
		for scanner.Scan() {
			if scanner.Text() == "committed" {
				p.ready <- scanner.Text()
			}
		}
		err := errors.Join(scanner.Err(), child.Wait())
		p.exited.Store(true)
		p.done <- err
	}()
	return p, nil
}

type qualificationProcess struct {
	child  *ownedprocess.Command
	cmd    *exec.Cmd
	input  io.WriteCloser
	ready  chan string
	done   chan error
	exited atomic.Bool
}

func (p *qualificationProcess) PID() int    { return p.cmd.Process.Pid }
func (p *qualificationProcess) Wait() error { return p.child.Wait() }
func (*qualificationProcess) Control(context.Context, string, string) error {
	return agentd.ErrUnsupported
}
func (*qualificationProcess) Stop(context.Context) error { return agentd.ErrUnsupported }

func qualificationChild(t *testing.T) {
	t.Helper()
	fmt.Printf("ready:%d\n", os.Getpid())
	reader := bufio.NewReader(io.LimitReader(os.Stdin, 128))
	for {
		command, err := reader.ReadString('\n')
		must(t, err)
		switch strings.TrimSpace(command) {
		case "build", "fix":
			must(t, os.WriteFile("change.txt", []byte(command), 0600))
			qualificationGit(t, ".", "add", "change.txt")
			qualificationGit(t, ".", "commit", "-qm", "controlled "+strings.TrimSpace(command))
			fmt.Println("committed")
		case "exit":
			return
		default:
			t.Fatal("invalid controlled child command")
		}
	}
}

func qualificationGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir, "-c", "core.hooksPath=" + os.DevNull}, args...)...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull}
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("controlled git %v: %v %s", args, err, raw)
	}
	return strings.TrimSpace(string(raw))
}

// The only outward adapter can create one fake PR; it has no merge/deploy
// method. The barrier sits after the committed send claim and external effect.
type qualificationPulls struct {
	mu               sync.Mutex
	pull             *routineactions.Pull
	posts            int
	created, release chan struct{}
}

func (*qualificationPulls) Configured(string, string) bool { return true }
func (p *qualificationPulls) Find(_ context.Context, _ routineactions.PROpen, _ string) (*routineactions.Pull, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pull == nil {
		return nil, nil
	}
	out := *p.pull
	return &out, nil
}
func (p *qualificationPulls) Open(ctx context.Context, in routineactions.PROpen, ref string, claim func() (bool, error)) (*routineactions.Pull, error) {
	allowed, err := claim()
	if err != nil || !allowed {
		return nil, err
	}
	p.mu.Lock()
	p.posts++
	p.pull = &routineactions.Pull{Number: 7, Repository: in.Repository, Branch: in.Branch, BaseBranch: in.BaseBranch, BaseSHA: in.BaseSHA, HeadSHA: in.HeadSHA, OperationRef: ref, Open: true}
	p.mu.Unlock()
	close(p.created)
	select {
	case <-p.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return nil, errors.New("controlled PR reply lost after committed effect")
}

// Read-only source observations feed delivery's real CI/review/Done policies.
type qualificationGitHub struct {
	pull  delivery.Pull
	facts evidence.PipelineFacts
	merge *delivery.MergeFact
}

func (g *qualificationGitHub) Pull(context.Context, int64) (delivery.Pull, error) { return g.pull, nil }
func (g *qualificationGitHub) OpenPulls(context.Context) ([]delivery.Pull, error) {
	return []delivery.Pull{g.pull}, nil
}
func (g *qualificationGitHub) Group(context.Context, string, string, string) ([]delivery.Pull, error) {
	return []delivery.Pull{g.pull}, nil
}
func (g *qualificationGitHub) Shipping(context.Context, string, string) (delivery.ShipFacts, error) {
	return g.facts.Facts, nil
}
func (g *qualificationGitHub) MergedPull(context.Context, int64) (*delivery.MergeFact, error) {
	return g.merge, nil
}
func (g *qualificationGitHub) MergedGroup(context.Context, string) ([]delivery.MergeFact, error) {
	if g.merge == nil {
		return nil, nil
	}
	return []delivery.MergeFact{*g.merge}, nil
}

// Risk R1/R7/R9/R15/R16/R17: individually valid receipts fail to compose,
// causing a duplicate writer/PR/charge or false Done after restart. Use one
// retained run and real local commits, exact child Wait receipts, a PR barrier,
// injected evaluation time and wrong-reason controls. Other fault boundaries
// reuse the package guards mapped in testdata/qualification-catalogue.json.
func TestRoutineControlledQualification(t *testing.T) {
	if os.Getenv("AEON_QUALIFICATION_CHILD") == "1" {
		qualificationChild(t)
		return
	}
	d := setupDispatchOn(t, setupWithActions(t, "off", false, []string{"work.create", "pr.open", "pipeline.request"}, "project"))
	f := d.fixture
	prepareRoutineReviewer(t, f)
	// The scoped daemon executes actions, but still cannot decide an approval.
	f.agent.Scopes = append(f.agent.Scopes, "work_orders.write", "nodes.write", "approvals.propose", "approvals.read")
	f.exec(`UPDATE agent_keys SET scopes=$2 WHERE principal_id=$1`, f.agent.ID, f.agent.Scopes)
	must(t, f.d.Admin.QueryRow(t.Context(), `SELECT id::text FROM agent_keys WHERE principal_id=$1`, f.agent.ID).Scan(&f.agent.KeyID))
	f.agent.AuthKeyID = f.agent.KeyID
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	root, err := filepath.EvalSymlinks(t.TempDir())
	must(t, err)
	repo, tree := filepath.Join(root, "repository"), filepath.Join(root, "worker")
	must(t, os.Mkdir(repo, 0700))
	qualificationGit(t, repo, "init", "-q", "-b", "main")
	qualificationGit(t, repo, "config", "user.name", "Qualification fixture")
	qualificationGit(t, repo, "config", "user.email", "qualification@example.invalid")
	must(t, os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0600))
	qualificationGit(t, repo, "add", "base.txt")
	qualificationGit(t, repo, "commit", "-qm", "controlled base")
	base := qualificationGit(t, repo, "rev-parse", "HEAD")
	qualificationGit(t, repo, "worktree", "add", "-q", "-b", "work/qualified", tree)
	worktree := routineguard.Digest([]string{f.run, tree})
	adapter := &qualificationAdapter{}
	start := func(dir string) *qualificationProcess {
		t.Helper()
		child, err := adapter.Start(ctx, agentd.StartRequest{Workspace: dir}, nil)
		must(t, err)
		p := child.(*qualificationProcess)
		if got := dbtest.Await(t, ctx, p.ready); got != fmt.Sprintf("ready:%d", p.PID()) {
			t.Fatalf("wrong owned child handshake: %q", got)
		}
		return p
	}
	var beforeBuildExit func()
	finish := func(p *qualificationProcess, command string) {
		t.Helper()
		if p.exited.Load() {
			t.Fatal("process exited before its barrier was released")
		}
		_, err := io.WriteString(p.input, command+"\n")
		must(t, err)
		if command == "build" || command == "fix" {
			if got := dbtest.Await(t, ctx, p.ready); got != "committed" {
				t.Fatalf("missing commit receipt: %q", got)
			}
			if command == "build" && beforeBuildExit != nil {
				beforeBuildExit()
			}
			_, err = io.WriteString(p.input, "exit\n")
			must(t, err)
		}
		must(t, p.input.Close())
		must(t, dbtest.Await(t, ctx, p.done))
		must(t, p.Wait())
	}
	// setupDispatch supplies an adopted, current lead. Its actual fake process
	// remains owned while the worker/review/fix chain runs; automatic lead startup
	// is qualified separately, never inferred from this adopted session.
	lead := start(repo)
	identity := ownedprocess.Identity{DaemonID: "budget-daemon", Generation: "budget-generation", ProcessID: strings.Repeat("1", 32), RootPID: lead.PID(), GroupID: lead.PID(), StartedAt: time.Now().UTC()}
	f.exec(`UPDATE harness_sessions SET process_ownership=$2 WHERE id=$1`, d.session, identity)
	reviews := crossreview.New(f.d.App, nil)
	d.options.Reviews = reviews
	restart := func() {
		d.e = New(f.d.App, d.options)
		f.mux = http.NewServeMux()
		runs := agentruns.NewWithLeadAdmission(f.d.App, d.options.LeadAdmission, func(ctx context.Context, tx pgx.Tx, p tenant.Principal, r agentruns.Run, _ agentruns.Telemetry, pending *[]events.Change) error {
			return agentaccounts.SettleDeferred(ctx, tx, p, r.ID, pending)
		})
		must(t, d.e.Bind(runs))
		runs.Mount(f.mux)
		approvals.New(f.d.App).Mount(f.mux)
	}
	restart()
	dispatch := func() agentruns.RoutineQueueResult {
		t.Helper()
		var out agentruns.RoutineQueueResult
		must(t, f.tx(func(tx pgx.Tx) error {
			var err error
			out, err = d.e.DispatchTx(ctx, tx, f.owner.TenantID, f.run, d.session, 1)
			return err
		}))
		return out
	}
	if out := dispatch(); !out.Routed || out.Replay {
		t.Fatalf("initial dispatch: %+v", out)
	}
	restart() // Discard the assignment response and reconstruct the server.
	if out := dispatch(); !out.Routed || !out.Replay || out.RunID != d.agentRun {
		t.Fatalf("lost dispatch replay: %+v", out)
	}
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		must(t, f.d.Admin.QueryRow(ctx, query, args...).Scan(&n))
		return n
	}
	call := func(p tenant.Principal, method, path string, body any, headers map[string]string, want int) []byte {
		t.Helper()
		raw, err := json.Marshal(body)
		must(t, err)
		r := httptest.NewRequest(method, path, strings.NewReader(string(raw))).WithContext(tenant.WithPrincipal(ctx, p))
		for key, value := range headers {
			r.Header.Set(key, value)
		}
		w := httptest.NewRecorder()
		f.mux.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: got %d want %d: %s", method, path, w.Code, want, w.Body.String())
		}
		return w.Body.Bytes()
	}
	claim := func(run string) agentsetup.RoutineGrant {
		t.Helper()
		var g agentsetup.RoutineGrant
		must(t, f.tx(func(tx pgx.Tx) error { return loadActorGrant(ctx, tx, run, &g) }))
		var handoff agentruns.WorkerHandoff
		var raw []byte
		must(t, f.d.Admin.QueryRow(ctx, `SELECT trace->'worker_assignment' FROM agent_runs WHERE id=$1`, run).Scan(&raw))
		if len(raw) > 0 {
			must(t, json.Unmarshal(raw, &handoff))
		}
		rows, err := f.d.Admin.Query(ctx, `SELECT id::text FROM account_reservations WHERE run_id=$1 AND state='active' ORDER BY id`, run)
		must(t, err)
		reservations := []string{}
		for rows.Next() {
			var id string
			must(t, rows.Scan(&id))
			reservations = append(reservations, id)
		}
		must(t, rows.Err())
		rows.Close()
		body := map[string]any{"daemon_id": g.DaemonID, "daemon_generation": g.DaemonGeneration, "reservation_ids": reservations, "routine": agentsetup.RoutinePickup{LaunchID: g.LaunchID, WorktreeID: worktree, Capability: g.Capability, DaemonDigest: g.DaemonDigest, CapabilityDigest: g.CapabilityDigest, ComputerID: g.ComputerID}}
		if handoff.BriefSHA256 != "" {
			body["handoff"] = agentruns.WorkerPickup{TicketRevision: handoff.TicketRevision, WorkOrderRevision: handoff.WorkOrderRevision, BriefSHA256: handoff.BriefSHA256, WorktreeID: worktree}
		}
		headers := map[string]string{"Authorization": "Bearer " + f.token, agentpairing.LedgerGenerationHeader: g.LedgerGeneration}
		call(f.agent, "POST", "/api/runs/"+run+"/claim", body, headers, 200)
		restart() // A lost claim reply still returns the original committed recipe.
		call(f.agent, "POST", "/api/runs/"+run+"/claim", body, headers, 200)
		return g
	}
	report := func(run string, g agentsetup.RoutineGrant, payload agentruns.Telemetry) {
		t.Helper()
		headers := map[string]string{"Authorization": "Bearer " + f.token, agentpairing.LedgerGenerationHeader: g.LedgerGeneration, agentruns.DaemonHeader: g.DaemonID, agentruns.GenerationHeader: g.DaemonGeneration}
		call(f.agent, "POST", "/api/runs/"+run+"/telemetry", payload, headers, 200)
		restart()
		call(f.agent, "POST", "/api/runs/"+run+"/telemetry", payload, headers, 200)
	}
	settle := func(run string, p *qualificationProcess) {
		t.Helper()
		if !p.exited.Load() {
			t.Fatal("live child cannot settle its root grant")
		}
		var grant string
		must(t, f.d.Admin.QueryRow(ctx, `SELECT id::text FROM routine_budget_grants WHERE agent_run_id=$1 AND parent_id IS NULL`, run).Scan(&grant))
		in := routinebudget.Settlement{EventKey: "exit:" + run, Usage: routinebudget.Amount{RecoveryMS: 1000}, UsageComplete: true, TokensKnown: true, PaidCostKnown: true, ExitConfirmed: true, EvidenceDigest: routineguard.Digest([]any{run, p.PID(), "observed_wait"})}
		for range 2 {
			must(t, f.tx(func(tx pgx.Tx) error { _, err := f.broker.SettleTx(ctx, tx, f.owner, f.run, grant, in); return err }))
		}
	}
	advance := func(run, reason string) map[string]any {
		t.Helper()
		var receipt string
		must(t, f.d.Admin.QueryRow(ctx, `SELECT id::text FROM routine_effect_outbox WHERE run_id=$1 AND effect_key=$2`, f.run, "finish:"+run).Scan(&receipt))
		restart()
		var advanced bool
		must(t, f.tx(func(tx pgx.Tx) error {
			var err error
			advanced, err = d.e.ContinueTx(ctx, tx, f.owner.TenantID, receipt)
			return err
		}))
		var raw []byte
		must(t, f.d.Admin.QueryRow(ctx, `SELECT result FROM routine_effect_outbox WHERE id=$1`, receipt).Scan(&raw))
		out := map[string]any{}
		must(t, json.Unmarshal(raw, &out))
		if reason != "" {
			if advanced || out["wait_reason"] != reason {
				t.Fatalf("wrong continuation refusal: advanced=%v result=%+v want %s", advanced, out, reason)
			}
			return out
		}
		if !advanced {
			t.Fatalf("continuation remained pending: %+v", out)
		}
		must(t, f.tx(func(tx pgx.Tx) error {
			replay, err := d.e.ContinueTx(ctx, tx, f.owner.TenantID, receipt)
			if err == nil && replay {
				t.Fatal("completion replay advanced twice")
			}
			return err
		}))
		return out
	}
	runActor := func(run, command, verdict string, cr *reviewgate.CommitRange) string {
		t.Helper()
		g := claim(run)
		p := start(tree)
		var model string
		must(t, f.d.Admin.QueryRow(ctx, `SELECT p.model FROM agent_runs r JOIN model_profiles p ON p.id=r.model_profile_id WHERE r.id=$1`, run).Scan(&model))
		report(run, g, agentruns.Telemetry{Sequence: 1, Kind: "started", Model: model, Evidence: "vendor_reported"})
		if count(`SELECT count(*) FROM routine_launch_intents WHERE id=$1 AND state='exit_observed'`, g.LaunchID) != 0 {
			t.Fatal("start fabricated exit")
		}
		finish(p, command)
		if verdict != "" {
			must(t, f.tx(func(tx pgx.Tx) error {
				var order, id string
				if err := tx.QueryRow(ctx, `SELECT work_order_id::text FROM agent_runs WHERE id=$1`, run).Scan(&order); err != nil {
					return err
				}
				if err := tx.QueryRow(ctx, `INSERT INTO work_evidence(tenant_id,work_order_id,submitted_by_principal_id,kind,reference,run_id) VALUES($1,$2,$3,'text',$4,$5) RETURNING id::text`, f.owner.TenantID, order, f.agent.ID, verdict, run).Scan(&id); err != nil {
					return err
				}
				return reviewgate.RecordEvidence(ctx, tx, order, id, f.agent.ID, run, "text", verdict, g.DaemonID, g.DaemonGeneration)
			}))
		}
		if cr != nil {
			cr.HeadSHA = qualificationGit(t, tree, "rev-parse", "HEAD")
		}
		report(run, g, agentruns.Telemetry{Sequence: 2, Kind: "finished", Status: "completed", ProcessState: "exited", ReviewRange: cr})
		advance(run, "previous_actor_exit_or_usage_unconfirmed")
		settle(run, p)
		if count(`SELECT count(*) FROM routine_launch_intents WHERE id=$1 AND state='exit_observed' AND worktree_id=$2`, g.LaunchID, worktree) != 1 {
			t.Fatal("lost exit/worktree provenance")
		}
		return run
	}
	buildRange := reviewgate.CommitRange{Repository: "example/qualification", BaseSHA: base}
	// Fixed evaluation observations are supplied before the final-write callback.
	// The callback uses the real recurrence authority and private verifier latch.
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	var current routineactions.Request
	definitions := recurrences.New(f.d.App).WithExecutionRuntime(f.broker.Runtime)

	pulls := &qualificationPulls{created: make(chan struct{}), release: make(chan struct{})}
	newBroker := func() *routineactions.Broker {
		return routineactions.New(f.d.App, func(ctx context.Context, tx pgx.Tx, p tenant.Principal, b routineactions.Binding, c routineguard.Context) (tenant.Principal, routineguard.Decision, string, error) {
			bound := recurrences.New(f.d.App).WithExecutionRuntime(f.broker.Runtime)
			bound.WithActionEvaluator(func(ctx context.Context, tx pgx.Tx, owner tenant.Principal, r recurrences.Recurrence, decision routineguard.Decision) (routineguard.Decision, string, error) {
				var evaluated routineactions.Request
				if err := json.Unmarshal([]byte(c.Text), &evaluated); err != nil {
					return decision, "", err
				}
				binding := routineguard.ActionBinding{TenantID: owner.TenantID, RunID: f.run, ActionID: f.action, AuthorRunID: d.agentRun, ProjectID: f.project, TargetID: evaluated.TargetID, TargetRevision: evaluated.ExpectedRevision, TargetUpdatedAt: time.UnixMicro(evaluated.ExpectedRevision), Kind: evaluated.Kind, PayloadDigest: routineguard.Digest(evaluated)}
				if evaluated.PROpen != nil {
					binding.HeadSHA = evaluated.PROpen.HeadSHA
				} else if evaluated.PipelineRequest != nil {
					binding.HeadSHA = evaluated.PipelineRequest.HeadSHA
				}
				request := routineguard.EvaluationRequest{Binding: binding, ContextDigest: decision.ContextDigest, PolicyDigest: decision.PolicyDigest, FamilyPolicy: reviewgate.DefaultFamilyPolicy(), Author: modelregistry.VerifiedModel{Harness: "codex", RequestedModel: "gpt-6.1-sol", EffectiveModel: "gpt-6.1-sol", Evidence: "vendor_reported"}, ProfileID: f.profile, AccountID: f.account, Harness: "claude", Model: "opus", Family: "anthropic", Capability: routineguard.EvaluationCapability, CreatedAt: now, Deadline: now.Add(routineguard.MaxEvaluationTime)}
				request.Digest = routineguard.Digest(request)
				verified, err := routineguard.VerifyEvaluation(nil, c, binding, request, "completed", modelregistry.VerifiedModel{Harness: "claude", RequestedModel: "opus", EffectiveModel: "claude-opus-5-5", Evidence: "vendor_reported"}, routineguard.StructuredVerdict{RequestDigest: request.Digest, Verdict: routineguard.Allow, Reason: "Controlled exact action observation."}, request.FamilyPolicy, now)
				if err != nil {
					return decision, "", err
				}
				var id string
				err = tx.QueryRow(ctx, `SELECT id::text FROM routine_guard_evaluations WHERE recurrence_id=$1 AND context_digest=$2 LIMIT 1`, r.ID, decision.ContextDigest).Scan(&id)
				if errors.Is(err, pgx.ErrNoRows) {
					raw, _ := json.Marshal(verified)
					err = tx.QueryRow(ctx, `INSERT INTO routine_guard_evaluations(tenant_id,recurrence_id,scope_kind,scope_project_id,owner_principal_id,output_project_id,definition_revision,checkpoint,policy_digest,context_digest,hard_version,preset_version,result,required_evaluation,decision) VALUES($1,$2,'project',$4,$3,$4,$5,'action',$6,$7,$8,$9,'allow',true,$10) RETURNING id::text`, owner.TenantID, r.ID, owner.ID, f.project, r.Revision, decision.PolicyDigest, decision.ContextDigest, decision.HardVersion, decision.PresetVersion, raw).Scan(&id)
				}
				return verified, id, err
			})
			return bound.AuthorizeActionTx(ctx, tx, p, recurrences.ActionBinding(b), c)
		}).WithPullRequests(pulls).WithObservation(func(ctx context.Context, tx pgx.Tx, p tenant.Principal, b routineactions.Binding) (tenant.Principal, error) {
			return definitions.AuthorizeObservationTx(ctx, tx, p, recurrences.ActionBinding(b))
		})
	}
	actions := newBroker()
	var attempt string
	must(t, f.d.Admin.QueryRow(ctx, `SELECT id::text FROM routine_attempts WHERE agent_run_id=$1`, d.agentRun).Scan(&attempt))
	request := func(key, kind string) routineactions.Request {
		var at time.Time
		must(t, f.d.Admin.QueryRow(ctx, `SELECT updated_at FROM nodes WHERE id=$1`, f.work).Scan(&at))
		return routineactions.Request{AttemptID: attempt, ActionKey: key, Kind: kind, ProjectID: f.project, TargetID: f.work, ExpectedRevision: at.UnixMicro()}
	}
	var out routineactions.Receipt
	beforeBuildExit = func() {
		buildRange.HeadSHA = qualificationGit(t, tree, "rev-parse", "HEAD")
		evaluation := start(repo)
		finish(evaluation, "exit")
		current = request("pr", "pr.open")
		current.PROpen = &routineactions.PROpen{Repository: buildRange.Repository, Branch: "work/qualified", BaseBranch: "main", BaseSHA: base, HeadSHA: buildRange.HeadSHA, Title: "AEON-1225: controlled qualification"}
		// A fake caller's wrong attempt must be rejected before any PR is posted.
		wrong := current
		wrong.AttemptID = f.run
		_, err = actions.Execute(ctx, f.agent, f.run, wrong)
		var refusal *workorders.Error
		if !errors.As(err, &refusal) || refusal.Status != 404 || refusal.Message != "routine_grant_not_found" {
			t.Fatalf("wrong-error control: %v", err)
		}
		result := make(chan routineactions.Receipt, 1)
		failure := make(chan error, 1)
		go func() { out, err := actions.Execute(ctx, f.agent, f.run, current); result <- out; failure <- err }()
		dbtest.Await(t, ctx, pulls.created)
		// A concurrent recovering server can observe the committed PR while its
		// creator still waits for the lost reply; neither may POST again.
		var actionID string
		must(t, f.d.Admin.QueryRow(ctx, `SELECT id::text FROM routine_actions WHERE run_id=$1 AND action_key='pr'`, f.run).Scan(&actionID))
		recovered, err := newBroker().ReconcilePR(ctx, f.owner.TenantID, actionID)
		must(t, err)
		if recovered.State != "succeeded" || recovered.PullRequest != 7 {
			t.Fatalf("concurrent PR recovery: %+v", recovered)
		}
		close(pulls.release)
		must(t, dbtest.Await(t, ctx, failure))
		original := dbtest.Await(t, ctx, result)
		if original.ID != recovered.ID || original.OperationRef != recovered.OperationRef || pulls.posts != 1 {
			t.Fatal("lost reply duplicated the PR identity/effect")
		}
		current = request("pipeline", "pipeline.request")
		current.PipelineRequest = &routineactions.PipelineRequest{PRActionKey: "pr", HeadSHA: buildRange.HeadSHA}

		var err error
		out, err = actions.Execute(ctx, f.agent, f.run, current)
		must(t, err)
	}
	runActor(d.agentRun, "build", "", &buildRange)
	first := advance(d.agentRun, "")
	reviewRun := first["agent_run_id"].(string)
	runActor(reviewRun, "exit", "FINDING: high change.txt:1 Needs a fix.\nVERDICT: changes", nil)
	fix := advance(reviewRun, "")
	fixRun := fix["agent_run_id"].(string)
	fixedRange := reviewgate.CommitRange{Repository: buildRange.Repository, BaseSHA: buildRange.HeadSHA}
	runActor(fixRun, "fix", "", &fixedRange)
	rereview := advance(fixRun, "")
	rereviewRun := rereview["agent_run_id"].(string)
	runActor(rereviewRun, "exit", "VERDICT: ok", nil)
	if handoff := advance(rereviewRun, ""); handoff["state"] != "awaiting_pipeline" || handoff["head_sha"] != fixedRange.HeadSHA {
		t.Fatalf("wrong coordinator handoff: %+v", handoff)
	}
	finish(lead, "exit")
	gh := &qualificationGitHub{}
	checks := []delivery.Check{}
	for _, name := range []string{"go", "web", "release-check", "e2e", "migration-compat"} {
		checks = append(checks, delivery.Check{Name: name, Status: "completed", Conclusion: "success"})
	}
	number := int64(7)
	gh.pull = delivery.Pull{Number: 7, Title: "AEON-1225: controlled qualification", Branch: "work/qualified", Head: fixedRange.HeadSHA, Base: base, Open: true, Checks: checks}
	gh.facts = evidence.PipelineFacts{Facts: evidence.Facts{PR: &number, PushedHead: fixedRange.HeadSHA, PullHead: fixedRange.HeadSHA, Base: base, Open: true, MergeableKnown: true, Checks: checks}, ArtifactSHA: fixedRange.HeadSHA}
	config := crossreview.AppConfig{TenantID: f.owner.TenantID, Repository: buildRange.Repository, InstallationID: "7", ID: "7", KeyFile: filepath.Join(root, "unused-fixture-key")}
	webhookKey := []byte(strings.Repeat("controlled-webhook-fixture-", 2))
	landing := delivery.New(f.d.App, config, webhookKey, gh, nil).WithRoutineReviews(reviews).WithRoutineArtifacts(func(context.Context, string, string) (string, error) { return gh.facts.ArtifactSHA, nil })
	actions = newBroker().WithPipeline(landing)
	pipeline := func(state, reason string) routineactions.Receipt {
		t.Helper()
		actions = newBroker().WithPipeline(landing)
		r, err := actions.ReconcilePipeline(ctx, f.owner.TenantID, out.ID)
		must(t, err)
		if r.State != state || r.WaitReason != reason {
			t.Fatalf("pipeline: %+v want %s/%s", r, state, reason)
		}
		return r
	}
	gh.facts.Facts.Checks[0].Conclusion = "failure"
	pipeline("pending", "pipeline_ci_or_artifact_pending")
	gh.facts.Facts.Checks[0].Conclusion = "success"
	out = pipeline("awaiting_decision", "")
	call(f.agent, "POST", "/api/approvals/"+out.ApprovalID+"/decision", map[string]string{"decision": "approved"}, nil, 403)
	call(f.owner, "POST", "/api/approvals/"+out.ApprovalID+"/decision", map[string]string{"decision": "approved"}, nil, 200)
	pipeline("awaiting_merge", "")
	// Merge occurs only in the person's controlled fixture repository. Delivery
	// consumes an authenticated read-only observation and writes the actual Done.
	qualificationGit(t, repo, "merge", "--no-ff", "-qm", "controlled person merge", "work/qualified")
	merge := qualificationGit(t, repo, "rev-parse", "HEAD")
	gh.pull.Open, gh.pull.Merged = false, true
	gh.facts.Facts.Open, gh.facts.Facts.Merged = false, true
	gh.merge = &delivery.MergeFact{PR: &number, SHA: merge, Head: fixedRange.HeadSHA, Title: "AEON-1225: controlled qualification", Branch: "work/qualified", At: time.Now().UTC(), MergedBy: "fixture-person"}
	pipeline("pending", "merge_done_receipt_pending_or_refused")
	f.exec(`UPDATE nodes SET key='AEON-1225',state='qa',fields=fields||'{"pill_en":"Controlled proof","pill_de":"Kontrollierter Nachweis","benefit_en":"Routine receipts are checked together.","benefit_de":"Routinenbelege werden gemeinsam geprüft."}'::jsonb WHERE id=$1`, f.work)
	landing.Mount(f.mux)
	raw, _ := json.Marshal(map[string]any{"action": "closed", "installation": map[string]int{"id": 7}, "repository": map[string]string{"full_name": buildRange.Repository, "default_branch": "main"}, "pull_request": map[string]any{"number": 7, "head": map[string]string{"sha": fixedRange.HeadSHA, "ref": "work/qualified"}, "base": map[string]any{"sha": base, "ref": "main", "repo": map[string]string{"full_name": buildRange.Repository}}}})
	for range 2 {
		r := httptest.NewRequest("POST", "/api/github/webhook", strings.NewReader(string(raw)))
		mac := hmac.New(sha256.New, webhookKey)
		_, _ = mac.Write(raw)
		r.Header.Set("X-GitHub-Event", "pull_request")
		r.Header.Set("X-GitHub-Delivery", "qualification-merge")
		r.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		w := httptest.NewRecorder()
		f.mux.ServeHTTP(w, r)
		if w.Code != 204 {
			t.Fatalf("actual merge Done writer: %d %s", w.Code, w.Body.String())
		}
	}
	pipeline("succeeded", "")
	if count(`SELECT count(*) FROM nodes WHERE id=$1 AND state='done'`, f.work) != 1 || count(`SELECT count(*) FROM events WHERE node_id=$1 AND type='delivery.merge_done'`, f.work) != 1 {
		t.Fatal("merge did not produce exactly one real Done")
	}
	if count(`SELECT count(*) FROM routine_budget_settlements WHERE run_id=$1`, f.run) != 4 || count(`SELECT count(*) FROM routine_budget_grants WHERE run_id=$1 AND state<>'settled'`, f.run) != 0 || count(`SELECT count(*) FROM routine_launch_intents WHERE routine_run_id=$1 AND kind='worker'`, f.run) != 4 || adapter.starts.Load() != 6 {
		t.Fatal("duplicate/missing actors, observed exits or settlements")
	}
	if count(`SELECT count(*) FROM routine_runs WHERE id=$1 AND state='completed'`, f.run) != 1 {
		t.Fatal("routine did not observe completion")
	}
	if got := qualificationGit(t, tree, "rev-parse", "HEAD"); got != fixedRange.HeadSHA {
		t.Fatal("recovery replaced the preserved worktree")
	}
}
