package handlers_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/agentd"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/baselinebatch"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/dispatchprofile"
	"github.com/inspr-at/paimos/backend/lifecycleclient"
	"github.com/inspr-at/paimos/backend/lifecycleintents"
	"github.com/inspr-at/paimos/backend/managedharness"
	"github.com/inspr-at/paimos/backend/models"
)

const ownedLease = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func ownedDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// probeAdapter is a deterministic owned adapter: it answers the account probes
// the readiness check performs and refuses to spawn anything, so no test can
// accidentally launch a real model.
type probeAdapter struct {
	label string
	keys  map[string]bool
}

func (probeAdapter) Name() string { return agentd.AdapterCodex }
func (probeAdapter) Capabilities() []agentd.Capability {
	return []agentd.Capability{agentd.CapabilityStatus, agentd.CapabilityInterrupt, agentd.CapabilityStop}
}
func (a probeAdapter) AccountLabel(context.Context) string { return a.label }
func (a probeAdapter) HasAccount(key string) bool          { return a.keys[key] }
func (probeAdapter) Start(context.Context, agentd.StartRequest, func(agentd.AdapterEvent)) (agentd.Process, error) {
	return nil, errors.New("the readiness fixture never spawns a child")
}

// ownedDaemon is a deterministic stand-in for paimos-agentd that speaks the
// real protocol: it registers a runtime with a real lease, claims intents,
// transitions them, runs the real agentd readiness probe against a real host
// fixture, and registers a real managed harness session. Nothing about the
// server side is faked, and no readiness row is ever written directly.
type ownedDaemon struct {
	t          *testing.T
	project    int64
	service    *lifecycleintents.Service
	harness    *managedharness.Service
	reporter   auth.Principal
	supervisor *agentd.Supervisor
	runtime    lifecycleintents.Runtime
	host       string
	workspace  string
	identity   string
	kernel     string
	loader     string
	accountKey string
	profile    dispatchprofile.Profile
	hostKind   string
	generationDigest string
	platformInputs   agentd.ReadinessInputs
}

func newOwnedDaemon(t *testing.T, projectID, userID int64) *ownedDaemon {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	hostKind, generationDigest, platformInputs := ownedPlatformFixture(t, root)
	workspace, kernel, loader := ownedWorkspaceFixture(t, root)
	identity := strings.Repeat("d", 64)
	supervisor, err := agentd.NewSupervisor(agentd.SupervisorConfig{
		Instance: "ppm-owned", Adapters: []agentd.Adapter{probeAdapter{label: "chatgpt", keys: map[string]bool{"coordinator": true}}},
		WorkspaceInspector: func(_ context.Context, path, mode string) (agentd.WorkspaceProvenance, error) {
			return agentd.WorkspaceProvenance{CanonicalPath: path, Identity: identity, Kind: "directory", Mode: mode}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = supervisor.Close(context.Background()) })
	profile, err := dispatchprofile.Resolve("codex-sol-high", dispatchprofile.CatalogVersion, "codex")
	if err != nil {
		t.Fatal(err)
	}
	res, err := db.DB.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes) VALUES(?,'owned-runtime','not-a-credential','fixture','*')`, userID)
	if err != nil {
		t.Fatal(err)
	}
	keyID, _ := res.LastInsertId()
	reporter, err := auth.NewAPIKeyPrincipal(keyID, userID, auth.ParseScopes("*"))
	if err != nil {
		t.Fatal(err)
	}
	daemon := &ownedDaemon{
		t: t, project: projectID, service: lifecycleintents.NewService(db.DB), harness: managedharness.NewService(db.DB),
		reporter: reporter, supervisor: supervisor, host: "owned-fixture-host",
		workspace: workspace, identity: identity, kernel: kernel, loader: loader, accountKey: "coordinator", profile: profile,
		hostKind: hostKind, generationDigest: generationDigest, platformInputs: platformInputs,
	}
	daemon.register()
	return daemon
}

func ownedWorkspaceFixture(t *testing.T, root string) (workspace, kernel, loader string) {
	t.Helper()
	workspace = filepath.Join(root, "workspace")
	if err := os.MkdirAll(filepath.Join(workspace, "doctrine", "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	kernel = filepath.Join(workspace, "doctrine", "docs", "AGENTS-KERNEL.md")
	if err := os.WriteFile(kernel, []byte("# AGENTS — Kernel\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loader = filepath.Join(workspace, "CLAUDE.md")
	if err := os.WriteFile(loader, []byte("@./doctrine/docs/AGENTS-KERNEL.md\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return workspace, kernel, loader
}

func ownedPlatformFixture(t *testing.T, root string) (hostKind, generationDigest string, inputs agentd.ReadinessInputs) {
	t.Helper()
	if runtime.GOOS == "darwin" {
		store := filepath.Join(root, "store", "3dz1kvq6mrfjxc7g8w2p4nhy5bt9saul-home-manager-generation")
		if err := os.MkdirAll(store, 0o755); err != nil {
			t.Fatal(err)
		}
		current := filepath.Join(root, "home-manager")
		if err := os.Symlink(store, current); err != nil {
			t.Fatal(err)
		}
		return "macos-home-manager",
			ownedDigest([]byte("3dz1kvq6mrfjxc7g8w2p4nhy5bt9saul-home-manager-generation")),
			agentd.ReadinessInputs{HomeManagerCurrent: current}
	}
	storeNix := filepath.Join(root, "store", "3dz1kvq6mrfjxc7g8w2p4nhy5bt9saul-nixos-system")
	if err := os.MkdirAll(storeNix, 0o755); err != nil {
		t.Fatal(err)
	}
	nixCurrent := filepath.Join(root, "nixos-system")
	if err := os.Symlink(storeNix, nixCurrent); err != nil {
		t.Fatal(err)
	}
	storeHM := filepath.Join(root, "store", "3dz1kvq6mrfjxc7g8w2p4nhy5bt9saul-home-manager-generation")
	if err := os.MkdirAll(storeHM, 0o755); err != nil {
		t.Fatal(err)
	}
	hmCurrent := filepath.Join(root, "home-manager")
	if err := os.Symlink(storeHM, hmCurrent); err != nil {
		t.Fatal(err)
	}
	nixosMarker := filepath.Join(root, "etc", "nixos")
	if err := os.MkdirAll(nixosMarker, 0o755); err != nil {
		t.Fatal(err)
	}
	return "nixos-home-manager",
		ownedDigest([]byte("3dz1kvq6mrfjxc7g8w2p4nhy5bt9saul-nixos-system")),
		agentd.ReadinessInputs{
			NixOSMarker:        nixosMarker,
			NixOSCurrent:       nixCurrent,
			HomeManagerCurrent: hmCurrent,
		}
}

// register performs the real authenticated runtime registration; the returned
// runtime is what every later claim and transition is authorized against.
func (d *ownedDaemon) register() {
	d.t.Helper()
	runtime, err := d.service.RegisterRuntime(context.Background(), d.reporter, d.project, ownedLease, lifecycleintents.Registration{
		Generation:    uuid.NewString(),
		Host:          d.host,
		AccountLabel:  "chatgpt",
		SchemaVersion: lifecycleintents.AccountChoiceSchemaV2,
		Accounts:      []lifecycleintents.AccountChoice{{Key: d.accountKey, Label: "Coordinator"}},
		Profiles:      []lifecycleintents.Profile{{ID: d.profile.ID, Version: d.profile.Version}},
		Workspaces:    []lifecycleintents.Workspace{{Handle: uuid.NewString(), Identity: d.identity, Label: "Work"}},
	})
	if err != nil {
		d.t.Fatalf("owned runtime registration: %v", err)
	}
	d.runtime = runtime
}

func (d *ownedDaemon) workerSelection(agent string) map[string]any {
	return map[string]any{
		"worker_name": agent, "runtime_id": d.runtime.ID, "runtime_generation": d.runtime.Generation,
		"account_label": d.runtime.AccountLabel, "account_key": d.accountKey,
		"profile_id": d.profile.ID, "profile_version": d.profile.Version,
		"workspace_handle": d.runtime.Workspaces[0].Handle,
	}
}

func (d *ownedDaemon) readinessSpec(intent lifecycleintents.Intent, mutate func(*agentd.ReadinessSpec)) agentd.ReadinessSpec {
	d.t.Helper()
	kernel, err := os.ReadFile(d.kernel)
	if err != nil {
		d.t.Fatal(err)
	}
	spec := agentd.ReadinessSpec{
		BaselineDigest: intent.Request.BaselineDigest,
		AccountLabel:   intent.Request.AccountLabel,
		AccountKey:     intent.Request.AccountKey,
		Profile:        d.profile,
		Expect: agentd.ReadinessExpectation{
			HostKind:             d.hostKind,
			GenerationDigest:     d.generationDigest,
			DoctrineKernelDigest: ownedDigest(kernel),
			Tools:                []string{"git"},
		},
		Inputs: agentd.ReadinessInputs{
			WorkspaceRoot: d.workspace, WorkspaceIdentity: d.identity, WorkspaceMode: d.profile.WorkspaceMode,
			DoctrineKernel: d.kernel, DoctrineLoader: d.loader, Instance: "ppm-owned",
			HomeManagerCurrent:   d.platformInputs.HomeManagerCurrent,
			HomeManagerInstalled: d.platformInputs.HomeManagerInstalled,
			NixOSMarker:          d.platformInputs.NixOSMarker,
			NixOSCurrent:         d.platformInputs.NixOSCurrent,
			NixOSInstalled:       d.platformInputs.NixOSInstalled,
		},
		Doctor: func(context.Context) (agentd.ReadinessDoctorReport, error) {
			return agentd.ReadinessDoctorReport{Instance: "ppm-owned", Ready: true,
				Layers: []agentd.ReadinessDoctorLayer{{Name: "daemon_generation", State: "known", Code: "live_owned_generation"}}}, nil
		},
	}
	if mutate != nil {
		mutate(&spec)
	}
	return spec
}

// step claims one intent and drives it through the real transition protocol.
// It returns the claimed intent, or nil when the authority has no work.
func (d *ownedDaemon) step(mutate func(*agentd.ReadinessSpec)) *lifecycleintents.Intent {
	d.t.Helper()
	ctx := context.Background()
	intent, err := d.service.Claim(ctx, d.reporter, d.project, d.runtime.ID, ownedLease)
	if err != nil {
		d.t.Fatalf("claim: %v", err)
	}
	if intent == nil {
		return nil
	}
	executing, err := d.service.Transition(ctx, d.reporter, d.project, intent.ID, ownedLease, lifecycleintents.Transition{
		RuntimeID: d.runtime.ID, RuntimeGeneration: d.runtime.Generation, ExpectedRevision: intent.Revision, State: "executing",
	})
	if err != nil {
		d.t.Fatalf("executing transition for %s: %v", intent.Request.Operation, err)
	}
	switch intent.Request.Operation {
	case "readiness":
		observation, err := d.supervisor.ObserveReadiness(ctx, d.readinessSpec(executing, mutate))
		if err != nil {
			d.t.Fatalf("owned readiness probe: %v", err)
		}
		result := lifecycleclient.ReadinessResult(observation)
		done, err := d.service.Transition(ctx, d.reporter, d.project, intent.ID, ownedLease, lifecycleintents.Transition{
			RuntimeID: d.runtime.ID, RuntimeGeneration: d.runtime.Generation, ExpectedRevision: executing.Revision,
			State: "completed", Reason: "applied", Readiness: result.Readiness,
		})
		if err != nil {
			d.t.Fatalf("readiness completion: %v", err)
		}
		return &done
	case "start":
		session := d.spawnSession(executing)
		done, err := d.service.Transition(ctx, d.reporter, d.project, intent.ID, ownedLease, lifecycleintents.Transition{
			RuntimeID: d.runtime.ID, RuntimeGeneration: d.runtime.Generation, ExpectedRevision: executing.Revision,
			State: "completed", Reason: "applied", ResultSessionID: session,
		})
		if err != nil {
			d.t.Fatalf("start completion: %v", err)
		}
		return &done
	}
	d.t.Fatalf("unexpected claimed operation %q", intent.Request.Operation)
	return nil
}

// spawnSession registers the managed harness session the start intent asked
// for, exactly as a daemon does after its child reports in.
func (d *ownedDaemon) spawnSession(intent lifecycleintents.Intent) string {
	d.t.Helper()
	ctx := context.Background()
	session, _, err := d.harness.Register(ctx, managedharness.RegisterInput{
		ProjectID: d.project, AgentName: intent.Request.AgentName, Harness: d.profile.Harness, Host: d.host,
		SessionRef: uuid.NewString(), WorkerLease: ownedLease, ManagementMode: managedharness.ManagementManaged,
		Role: intent.Request.Role, TicketID: intent.Request.TicketID, WorkShape: intent.Request.WorkShape,
		SteerMode: managedharness.SteerNone,
		Workspace: &models.HarnessWorkspaceProvenance{CanonicalPath: d.workspace, Identity: d.identity,
			Kind: "directory", Mode: d.profile.WorkspaceMode},
		DispatchProfileID: d.profile.ID, DispatchProfileVersion: d.profile.Version,
		AccountLabel: intent.Request.AccountLabel, AccountKey: intent.Request.AccountKey,
		Capabilities: models.HarnessCapabilities{Status: true, Interrupt: true, Stop: true},
	})
	if err != nil {
		d.t.Fatalf("owned session registration: %v", err)
	}
	if _, err := d.harness.HeartbeatWithActivity(ctx, session.ID, managedharness.PhaseWorking,
		managedharness.ActivityEvidence{Sequence: 1, Kind: "turn_started"}); err != nil {
		d.t.Fatalf("owned session heartbeat: %v", err)
	}
	if err := d.service.RegisterSession(ctx, d.reporter, d.project, d.runtime.ID, ownedLease, ownedLease,
		lifecycleintents.SessionRegistration{SessionID: session.ID, Generation: intent.NewGeneration}); err != nil {
		d.t.Fatalf("owned session lifecycle registration: %v", err)
	}
	return session.ID
}

func promoteSuperAdmin(t *testing.T, username string) int64 {
	t.Helper()
	if _, err := db.DB.Exec(`UPDATE users SET is_super_admin=1 WHERE username=?`, username); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := db.DB.QueryRow(`SELECT id FROM users WHERE username=?`, username).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func optIn(t *testing.T, ts *testServer, projectID int64) {
	t.Helper()
	resp := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/opt-in", projectID), map[string]any{"enabled": true})
	if resp.StatusCode != 200 {
		t.Fatalf("opt-in=%d %s", resp.StatusCode, baselineReadBody(resp))
	}
}

func workflowOf(t *testing.T, ts *testServer, projectID int64) baselinebatch.Workflow {
	t.Helper()
	var workflow baselinebatch.Workflow
	decode(t, ts.get(t, fmt.Sprintf("/api/projects/%d/baseline-batches/", projectID), ts.adminCookie), &workflow)
	return workflow
}

// TestBaselineBatchOwnedExecution drives the whole agent-mode vertical through
// real routes and the real owned protocol: opt-in, import, review, an owned
// readiness probe, a start the lifecycle authority actually accepts, the
// daemon's claim/execute/complete cycle, and human controls with real effects.
func TestBaselineBatchOwnedExecution(t *testing.T) {
	ts := newTestServer(t)
	userID := promoteSuperAdmin(t, "admin")
	projectID := responseID(t, ts.post(t, "/api/projects", ts.adminCookie, map[string]string{"name": "Owned stream", "key": "OWN"}))
	if _, err := db.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'codex')`, projectID); err != nil {
		t.Fatal(err)
	}

	// AC8: before the explicit opt-in the stream is inert and writes are refused.
	before := workflowOf(t, ts, projectID)
	if before.INSPRStreamEnabled || before.INSPRGating || !before.LegacyUnaffected || before.Draft != nil {
		t.Fatalf("stream active without opt-in: %+v", before)
	}
	handover, _ := validHandover(t)
	if resp := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/import", projectID),
		map[string]any{"handover": json.RawMessage(handover)}); resp.StatusCode != 409 {
		t.Fatalf("import without opt-in=%d %s", resp.StatusCode, baselineReadBody(resp))
	}
	optIn(t, ts, projectID)

	imported := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/import", projectID),
		map[string]any{"handover": json.RawMessage(handover)})
	if imported.StatusCode != 201 {
		t.Fatalf("import=%d %s", imported.StatusCode, baselineReadBody(imported))
	}
	var draft baselinebatch.Draft
	decode(t, imported, &draft)
	// AC2: the pre-start disclosure is numeric, labelled and unobserved.
	if draft.Impact.RequirementCount != 1 || draft.Impact.Forecast.Kind != baselinebatch.ForecastGuess ||
		draft.Impact.Forecast.Label != "guessed" || draft.Impact.Forecast.Observed {
		t.Fatalf("impact estimate=%+v", draft.Impact)
	}

	daemon := newOwnedDaemon(t, projectID, userID)
	worker := daemon.workerSelection("codex")
	if resp := patchBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d", projectID, draft.ID),
		map[string]any{"execution_mode": "automatic", "worker": worker}); resp.StatusCode != 200 {
		t.Fatalf("select worker=%d %s", resp.StatusCode, baselineReadBody(resp))
	}

	// AC5: with no owned observation, the agent mode blocks and says why.
	blocked := workflowOf(t, ts, projectID)
	if blocked.Readiness == nil || blocked.Readiness.Status == "ready" ||
		blocked.Readiness.BlockingReason != "readiness_observation_missing" {
		t.Fatalf("pre-probe readiness=%+v", blocked.Readiness)
	}
	if blocked.Readiness.Basis != baselinebatch.BasisOwnedDaemonProbe || !blocked.Readiness.ClientReadyIgnored {
		t.Fatalf("readiness basis=%+v", blocked.Readiness)
	}

	reviewBody := map[string]any{"execution_mode": "automatic", "worker": worker, "selected_requirement_refs": []string{"req.login"}}
	review := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/review", projectID, draft.ID), reviewBody)
	if review.StatusCode != 200 {
		t.Fatalf("review=%d %s", review.StatusCode, baselineReadBody(review))
	}
	decode(t, review, &draft)
	startBody := map[string]any{
		"idempotency_key": "owned-start-key-01", "review_id": *draft.ReviewID, "draft_revision": draft.Revision,
		"confirm": true, "content_digest": draft.Baseline.ContentDigest, "revision_seal": draft.Baseline.RevisionSeal,
		"execution_mode": "automatic", "selected_requirement_refs": []string{"req.login"}, "worker": worker,
	}
	if resp := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/start", projectID, draft.ID), startBody); resp.StatusCode != 409 ||
		!strings.Contains(baselineReadBody(resp), "readiness_observation_missing") {
		t.Fatalf("start without observation=%d %s", resp.StatusCode, baselineReadBody(resp))
	}

	// The human asks for an owned probe; the daemon performs it for real.
	probe := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/readiness", projectID, draft.ID), nil)
	if probe.StatusCode != 200 {
		t.Fatalf("readiness request=%d %s", probe.StatusCode, baselineReadBody(probe))
	}
	var pending baselinebatch.ReadinessEvidence
	decode(t, probe, &pending)
	if pending.ProbeState != "requested" || pending.Status == "ready" {
		t.Fatalf("probe not queued as evidence-free: %+v", pending)
	}
	if daemon.step(nil) == nil {
		t.Fatal("owned daemon found no readiness intent to claim")
	}

	ready := workflowOf(t, ts, projectID)
	if ready.Readiness == nil || ready.Readiness.Status != "ready" || ready.Readiness.BlockingReason != "" {
		t.Fatalf("post-probe readiness=%+v", ready.Readiness)
	}
	if !ready.Readiness.NamedAccountProof || !ready.Readiness.ModelProfileProof || !ready.Readiness.WorkspaceProof {
		t.Fatalf("readiness proof flags=%+v", ready.Readiness)
	}
	if ready.Readiness.WorkspaceIdentity != daemon.identity || ready.Readiness.IntentID == "" {
		t.Fatalf("observation not bound to the owned workspace: %+v", ready.Readiness)
	}
	if len(ready.Readiness.Checks) < len(lifecycleintents.RequiredReadinessChecks) {
		t.Fatalf("observation carries no checks: %+v", ready.Readiness.Checks)
	}
	observedAt, err := time.Parse(time.RFC3339Nano, strings.Replace(ready.Readiness.ObservedAt, "Z", "Z", 1))
	if err != nil {
		t.Fatalf("observed_at=%q: %v", ready.Readiness.ObservedAt, err)
	}
	freshUntil, err := time.Parse(time.RFC3339Nano, ready.Readiness.FreshUntil)
	if err != nil {
		t.Fatalf("fresh_until=%q: %v", ready.Readiness.FreshUntil, err)
	}
	// The stored deadline may never outlive the runtime registration that
	// produced it, even though the contract allows 300s.
	runtimeExpiry, err := time.Parse(time.RFC3339Nano, daemon.runtime.ExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	if freshUntil.After(runtimeExpiry) || !freshUntil.After(observedAt) {
		t.Fatalf("freshness not clamped to runtime ownership: observed=%s fresh=%s runtime=%s",
			observedAt, freshUntil, runtimeExpiry)
	}

	started := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/start", projectID, draft.ID), startBody)
	if started.StatusCode != 200 {
		t.Fatalf("owned start=%d %s", started.StatusCode, baselineReadBody(started))
	}
	var batch baselinebatch.Batch
	decode(t, started, &batch)
	if batch.LifecycleIntentID == "" || batch.AttemptID == nil || batch.ReadinessIntentID != ready.Readiness.IntentID {
		t.Fatalf("start wiring=%+v", batch)
	}
	if batch.Status != baselinebatch.BatchQueued {
		t.Fatalf("queued start state=%s progress=%+v", batch.Status, batch.Progress)
	}
	// B5: no orphan agent_runs row is created for owned execution.
	var runs int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM agent_runs WHERE issue_id=?`, batch.IssueID).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 0 {
		t.Fatalf("owned start created %d unreachable agent_runs rows", runs)
	}
	// The intent is genuinely claimable and completable by the real protocol.
	if daemon.step(nil) == nil {
		t.Fatal("owned daemon found no start intent to claim")
	}
	active := workflowOf(t, ts, projectID)
	if active.ActiveBatch == nil || active.ActiveBatch.Status != baselinebatch.BatchActive {
		t.Fatalf("state after owned execution=%+v", active.ActiveBatch)
	}
	if active.ActiveBatch.Progress.SessionID == "" || active.ActiveBatch.Progress.IntentState != "completed" {
		t.Fatalf("progress not derived from the owned feeds: %+v", active.ActiveBatch.Progress)
	}
	if active.ActiveBatch.Progress.EvidenceObserved {
		t.Fatalf("no stage has signalled yet, but evidence is claimed observed: %+v", active.ActiveBatch.Progress)
	}
	measured, guess := forecastKinds(active.ActiveBatch.Forecasts)
	if measured == nil || guess == nil {
		t.Fatalf("forecasts=%+v", active.ActiveBatch.Forecasts)
	}
	if measured.Percent != 0 || measured.Label != "observed" || measured.Observed {
		t.Fatalf("measured forecast misreports evidence: %+v", measured)
	}
	if measured.EducatedETASeconds == nil || measured.EducatedETALabel != "guessed" {
		t.Fatalf("measured missing educated ETA fallback: %+v", measured)
	}
	if len(active.Batches) == 0 {
		t.Fatal("batch history is empty")
	}

	// AC3/AC4: pause reaches the actual owned process through the PAI-903
	// control ledger, attributed to this human.
	pauseKey := uuid.NewString()
	pause := postBaseline(t, ts, ts.adminCookie, controlPath(projectID, batch.ID), map[string]any{"action": "pause", "request_key": pauseKey})
	if pause.StatusCode != 200 {
		t.Fatalf("pause=%d %s", pause.StatusCode, baselineReadBody(pause))
	}
	decode(t, pause, &batch)
	if batch.Status != baselinebatch.BatchPaused || batch.ControlReason != "owned_session_interrupted" {
		t.Fatalf("pause state=%s reason=%s", batch.Status, batch.ControlReason)
	}
	var kind, controlState string
	var requestedBy int64
	if err := db.DB.QueryRow(`SELECT kind,state,requested_by_user_id FROM harness_session_controls WHERE harness_session_id=?`,
		active.ActiveBatch.Progress.SessionID).Scan(&kind, &controlState, &requestedBy); err != nil {
		t.Fatalf("no owned control row: %v", err)
	}
	if kind != managedharness.ControlInterrupt || controlState != "pending" || requestedBy != userID {
		t.Fatalf("owned control=%s/%s by %d", kind, controlState, requestedBy)
	}
	// Replaying the same request key does not produce a second interrupt.
	if resp := postBaseline(t, ts, ts.adminCookie, controlPath(projectID, batch.ID),
		map[string]any{"action": "pause", "request_key": pauseKey}); resp.StatusCode != 200 {
		t.Fatalf("pause replay=%d %s", resp.StatusCode, baselineReadBody(resp))
	}
	var controls int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM harness_session_controls WHERE harness_session_id=?`,
		active.ActiveBatch.Progress.SessionID).Scan(&controls); err != nil {
		t.Fatal(err)
	}
	if controls != 1 {
		t.Fatalf("replayed pause produced %d controls", controls)
	}

	// Resume on a live interrupted worker has no owned effect, so it is refused
	// with its reason instead of moving a status column.
	resume := postBaseline(t, ts, ts.adminCookie, controlPath(projectID, batch.ID), map[string]any{"action": "resume"})
	if resume.StatusCode != 409 || !strings.Contains(baselineReadBody(resume), "owned_session_resume_unavailable") {
		t.Fatalf("resume=%d %s", resume.StatusCode, baselineReadBody(resume))
	}
	current := workflowOf(t, ts, projectID)
	for _, option := range current.ActiveBatch.Controls {
		if option.Action == "resume" && option.Available {
			t.Fatalf("unavailable resume advertised as a control: %+v", option)
		}
		if option.Action == "cancel" && (!option.Available || option.Effect != "harness_control") {
			t.Fatalf("cancel control=%+v", option)
		}
	}

	cancel := postBaseline(t, ts, ts.adminCookie, controlPath(projectID, batch.ID), map[string]any{"action": "cancel"})
	if cancel.StatusCode != 200 {
		t.Fatalf("cancel=%d %s", cancel.StatusCode, baselineReadBody(cancel))
	}
	decode(t, cancel, &batch)
	if batch.Status != baselinebatch.BatchCancelled {
		t.Fatalf("cancel state=%s", batch.Status)
	}
	var stops int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM harness_session_controls WHERE harness_session_id=? AND kind='stop'`,
		active.ActiveBatch.Progress.SessionID).Scan(&stops); err != nil {
		t.Fatal(err)
	}
	if stops != 1 {
		t.Fatalf("cancel wrote %d stop controls", stops)
	}
}

func controlPath(projectID, batchID int64) string {
	return fmt.Sprintf("/api/projects/%d/baseline-batches/batches/%d/control", projectID, batchID)
}

func forecastKinds(forecasts []baselinebatch.Forecast) (measured, guess *baselinebatch.Forecast) {
	for i := range forecasts {
		switch forecasts[i].Kind {
		case baselinebatch.ForecastMeasured:
			measured = &forecasts[i]
		case baselinebatch.ForecastGuess:
			guess = &forecasts[i]
		}
	}
	return measured, guess
}

// TestBaselineBatchOwnedFailuresBlockStart covers the ways an owned observation
// can be missing, refused or ambiguous. None of them may produce a started
// batch, and each must name its own reason.
func TestBaselineBatchOwnedFailuresBlockStart(t *testing.T) {
	ts := newTestServer(t)
	userID := promoteSuperAdmin(t, "admin")
	projectID := responseID(t, ts.post(t, "/api/projects", ts.adminCookie, map[string]string{"name": "Owned failures", "key": "OWF"}))
	if _, err := db.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'codex')`, projectID); err != nil {
		t.Fatal(err)
	}
	optIn(t, ts, projectID)
	handover, _ := validHandover(t)
	var draft baselinebatch.Draft
	decode(t, postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/import", projectID),
		map[string]any{"handover": json.RawMessage(handover)}), &draft)
	daemon := newOwnedDaemon(t, projectID, userID)
	worker := daemon.workerSelection("codex")
	if resp := patchBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d", projectID, draft.ID),
		map[string]any{"execution_mode": "automatic", "worker": worker}); resp.StatusCode != 200 {
		t.Fatalf("select worker=%d %s", resp.StatusCode, baselineReadBody(resp))
	}

	// A host that fails a required check reports needs_setup, and the start
	// stays blocked with that reason.
	if resp := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/readiness", projectID, draft.ID), nil); resp.StatusCode != 200 {
		t.Fatalf("readiness request=%d %s", resp.StatusCode, baselineReadBody(resp))
	}
	daemon.step(func(spec *agentd.ReadinessSpec) {
		spec.Expect.GenerationDigest = ownedDigest([]byte("some-other-generation-entirely"))
	})
	notReady := workflowOf(t, ts, projectID)
	if notReady.Readiness == nil || notReady.Readiness.Status != "needs_setup" ||
		notReady.Readiness.BlockingReason != "readiness_needs_setup" || notReady.Readiness.NextAction != "activate_home_manager" {
		t.Fatalf("defective host readiness=%+v", notReady.Readiness)
	}

	review := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/review", projectID, draft.ID),
		map[string]any{"execution_mode": "automatic", "worker": worker, "selected_requirement_refs": []string{"req.login"}})
	if review.StatusCode != 200 {
		t.Fatalf("review=%d %s", review.StatusCode, baselineReadBody(review))
	}
	decode(t, review, &draft)
	startBody := map[string]any{
		"idempotency_key": "owned-blocked-key-1", "review_id": *draft.ReviewID, "draft_revision": draft.Revision,
		"confirm": true, "content_digest": draft.Baseline.ContentDigest, "revision_seal": draft.Baseline.RevisionSeal,
		"execution_mode": "automatic", "selected_requirement_refs": []string{"req.login"}, "worker": worker,
	}
	resp := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/start", projectID, draft.ID), startBody)
	if resp.StatusCode != 409 || !strings.Contains(baselineReadBody(resp), "readiness_needs_setup") {
		t.Fatalf("blocked start=%d %s", resp.StatusCode, baselineReadBody(resp))
	}
	var batches int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM baseline_batch_batches WHERE project_id=?`, projectID).Scan(&batches); err != nil {
		t.Fatal(err)
	}
	if batches != 0 {
		t.Fatalf("blocked start created %d batches", batches)
	}

	// A stale observation is not evidence: expiring the runtime registration
	// takes the whole selection offline again.
	if _, err := db.DB.Exec(`UPDATE lifecycle_runtimes SET expires_at=? WHERE id=?`,
		time.Now().UTC().Add(-time.Minute).Format("2006-01-02T15:04:05.000Z"), daemon.runtime.ID); err != nil {
		t.Fatal(err)
	}
	stale := workflowOf(t, ts, projectID)
	if stale.Readiness == nil || stale.Readiness.BlockingReason != "runtime_offline" {
		t.Fatalf("stale runtime readiness=%+v", stale.Readiness)
	}
	resp = postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/start", projectID, draft.ID), startBody)
	if resp.StatusCode != 409 || !strings.Contains(baselineReadBody(resp), "runtime_offline") {
		t.Fatalf("stale start=%d %s", resp.StatusCode, baselineReadBody(resp))
	}
}

// TestBaselineBatchAmbiguousLaunchReplay proves an unknown launch outcome stays
// unknown: the batch reports it, and the human is not offered a resume that
// would double-start the same work.
func TestBaselineBatchAmbiguousLaunchReplay(t *testing.T) {
	ts := newTestServer(t)
	userID := promoteSuperAdmin(t, "admin")
	projectID := responseID(t, ts.post(t, "/api/projects", ts.adminCookie, map[string]string{"name": "Ambiguous", "key": "AMB"}))
	if _, err := db.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'codex')`, projectID); err != nil {
		t.Fatal(err)
	}
	optIn(t, ts, projectID)
	handover, _ := validHandover(t)
	var draft baselinebatch.Draft
	decode(t, postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/import", projectID),
		map[string]any{"handover": json.RawMessage(handover)}), &draft)
	daemon := newOwnedDaemon(t, projectID, userID)
	worker := daemon.workerSelection("codex")
	patchBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d", projectID, draft.ID),
		map[string]any{"execution_mode": "assisted", "worker": worker})
	postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/readiness", projectID, draft.ID), nil)
	daemon.step(nil)
	review := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/review", projectID, draft.ID),
		map[string]any{"execution_mode": "assisted", "worker": worker, "selected_requirement_refs": []string{"req.login"}})
	decode(t, review, &draft)
	started := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/start", projectID, draft.ID), map[string]any{
		"idempotency_key": "ambiguous-start-01", "review_id": *draft.ReviewID, "draft_revision": draft.Revision,
		"confirm": true, "content_digest": draft.Baseline.ContentDigest, "revision_seal": draft.Baseline.RevisionSeal,
		"execution_mode": "assisted", "selected_requirement_refs": []string{"req.login"}, "worker": worker,
	})
	if started.StatusCode != 200 {
		t.Fatalf("start=%d %s", started.StatusCode, baselineReadBody(started))
	}
	var batch baselinebatch.Batch
	decode(t, started, &batch)

	// The daemon claims and begins executing, then loses its outcome.
	ctx := context.Background()
	intent, err := daemon.service.Claim(ctx, daemon.reporter, projectID, daemon.runtime.ID, ownedLease)
	if err != nil || intent == nil {
		t.Fatalf("claim=%v err=%v", intent, err)
	}
	executing, err := daemon.service.Transition(ctx, daemon.reporter, projectID, intent.ID, ownedLease, lifecycleintents.Transition{
		RuntimeID: daemon.runtime.ID, RuntimeGeneration: daemon.runtime.Generation, ExpectedRevision: intent.Revision, State: "executing",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = daemon.service.Transition(ctx, daemon.reporter, projectID, intent.ID, ownedLease, lifecycleintents.Transition{
		RuntimeID: daemon.runtime.ID, RuntimeGeneration: daemon.runtime.Generation, ExpectedRevision: executing.Revision,
		State: "failed", Reason: "outcome_unknown",
	}); err != nil {
		t.Fatal(err)
	}
	workflow := workflowOf(t, ts, projectID)
	if workflow.ActiveBatch == nil || workflow.ActiveBatch.Status != baselinebatch.BatchBlocked {
		t.Fatalf("ambiguous outcome state=%+v", workflow.ActiveBatch)
	}
	if workflow.ActiveBatch.Progress.BlockingReason != "lifecycle_failed_outcome_unknown" {
		t.Fatalf("blocking reason=%q", workflow.ActiveBatch.Progress.BlockingReason)
	}
	for _, option := range workflow.ActiveBatch.Controls {
		if option.Action == "resume" && option.Available {
			t.Fatal("an unknown launch outcome offered a resume")
		}
	}
	// A replayed confirmation returns the same batch instead of launching again.
	replay := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/start", projectID, draft.ID), map[string]any{
		"idempotency_key": "ambiguous-start-01", "review_id": *draft.ReviewID, "draft_revision": draft.Revision,
		"confirm": true, "content_digest": draft.Baseline.ContentDigest, "revision_seal": draft.Baseline.RevisionSeal,
		"execution_mode": "assisted", "selected_requirement_refs": []string{"req.login"}, "worker": worker,
	})
	if replay.StatusCode != 200 {
		t.Fatalf("replay=%d %s", replay.StatusCode, baselineReadBody(replay))
	}
	var replayed baselinebatch.Batch
	decode(t, replay, &replayed)
	if replayed.ID != batch.ID || replayed.LifecycleIntentID != batch.LifecycleIntentID {
		t.Fatalf("replay produced a second launch: %+v vs %+v", replayed, batch)
	}
	var intents int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM lifecycle_intents WHERE project_id=? AND json_extract(request_json,'$.operation')='start'`,
		projectID).Scan(&intents); err != nil {
		t.Fatal(err)
	}
	if intents != 1 {
		t.Fatalf("replay produced %d start intents", intents)
	}
}

// TestBaselineBatchLifecycleAuthorityRefusalFailsStart proves the start does not
// succeed when the lifecycle authority would refuse the intent: no batch, no
// issue and no attempt survive the refusal.
func TestBaselineBatchLifecycleAuthorityRefusalFailsStart(t *testing.T) {
	ts := newTestServer(t)
	userID := promoteSuperAdmin(t, "admin")
	projectID := responseID(t, ts.post(t, "/api/projects", ts.adminCookie, map[string]string{"name": "Refused", "key": "REF"}))
	if _, err := db.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'codex')`, projectID); err != nil {
		t.Fatal(err)
	}
	optIn(t, ts, projectID)
	handover, _ := validHandover(t)
	var draft baselinebatch.Draft
	decode(t, postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/import", projectID),
		map[string]any{"handover": json.RawMessage(handover)}), &draft)
	daemon := newOwnedDaemon(t, projectID, userID)
	worker := daemon.workerSelection("codex")
	patchBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d", projectID, draft.ID),
		map[string]any{"execution_mode": "automatic", "worker": worker})
	postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/readiness", projectID, draft.ID), nil)
	daemon.step(nil)
	review := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/review", projectID, draft.ID),
		map[string]any{"execution_mode": "automatic", "worker": worker, "selected_requirement_refs": []string{"req.login"}})
	decode(t, review, &draft)

	// The lifecycle authority is super-admin gated. Losing that authority must
	// fail the start outright, not accept it and strand the batch.
	if _, err := db.DB.Exec(`UPDATE users SET is_super_admin=0 WHERE id=?`, userID); err != nil {
		t.Fatal(err)
	}
	resp := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/start", projectID, draft.ID), map[string]any{
		"idempotency_key": "refused-start-01", "review_id": *draft.ReviewID, "draft_revision": draft.Revision,
		"confirm": true, "content_digest": draft.Baseline.ContentDigest, "revision_seal": draft.Baseline.RevisionSeal,
		"execution_mode": "automatic", "selected_requirement_refs": []string{"req.login"}, "worker": worker,
	})
	if resp.StatusCode != 409 || !strings.Contains(baselineReadBody(resp), "lifecycle_start_unavailable") {
		t.Fatalf("refused start=%d %s", resp.StatusCode, baselineReadBody(resp))
	}
	for table, query := range map[string]string{
		"batches":  `SELECT COUNT(*) FROM baseline_batch_batches WHERE project_id=?`,
		"intents":  `SELECT COUNT(*) FROM lifecycle_intents WHERE project_id=? AND json_extract(request_json,'$.operation')='start'`,
		"attempts": `SELECT COUNT(*) FROM issues WHERE project_id=? AND title LIKE 'Delivery batch%'`,
	} {
		var count int
		if err := db.DB.QueryRow(query, projectID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("refused start left %d %s behind", count, table)
		}
	}
}

// TestBaselineBatchAPIKeyAndViewerCannotWrite sends real requests with a real
// API key and a real viewer session against the write routes.
func TestBaselineBatchAPIKeyAndViewerCannotWrite(t *testing.T) {
	ts := newTestServer(t)
	projectID := responseID(t, ts.post(t, "/api/projects", ts.adminCookie, map[string]string{"name": "Authority", "key": "AUT"}))
	optIn(t, ts, projectID)
	handover, _ := validHandover(t)
	var draft baselinebatch.Draft
	decode(t, postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/import", projectID),
		map[string]any{"handover": json.RawMessage(handover)}), &draft)
	review := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/review", projectID, draft.ID),
		map[string]any{"execution_mode": "manual", "selected_requirement_refs": []string{"req.login"}})
	decode(t, review, &draft)

	var keyBody struct {
		Key string `json:"key"`
	}
	decode(t, ts.post(t, "/api/auth/api-keys", ts.adminCookie, map[string]string{"name": "batch-bot"}), &keyBody)
	if keyBody.Key == "" {
		t.Fatal("api key missing")
	}
	startBody, _ := json.Marshal(map[string]any{
		"idempotency_key": "api-key-start-001", "review_id": *draft.ReviewID, "draft_revision": draft.Revision,
		"confirm": true, "content_digest": draft.Baseline.ContentDigest, "revision_seal": draft.Baseline.RevisionSeal,
		"execution_mode": "manual", "selected_requirement_refs": []string{"req.login"}, "worker": map[string]any{},
	})
	request, _ := http.NewRequest(http.MethodPost,
		ts.srv.URL+fmt.Sprintf("/api/projects/%d/baseline-batches/%d/start", projectID, draft.ID), strings.NewReader(string(startBody)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+keyBody.Key)
	resp, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode == 200 {
		t.Fatalf("API key confirmed a human start: %s", baselineReadBody(resp))
	}
	var started int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM baseline_batch_batches WHERE project_id=?`, projectID).Scan(&started); err != nil {
		t.Fatal(err)
	}
	if started != 0 {
		t.Fatalf("API key start created %d batches", started)
	}

	// A project viewer reads the workflow but cannot write to it.
	var memberID int64
	if err := db.DB.QueryRow(`SELECT id FROM users WHERE username='member'`).Scan(&memberID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO project_members(project_id,user_id,access_level) VALUES(?,?,'viewer')
		ON CONFLICT(project_id,user_id) DO UPDATE SET access_level='viewer'`, projectID, memberID); err != nil {
		t.Fatal(err)
	}
	if resp := ts.get(t, fmt.Sprintf("/api/projects/%d/baseline-batches/", projectID), ts.memberCookie); resp.StatusCode != 200 {
		t.Fatalf("viewer read=%d %s", resp.StatusCode, baselineReadBody(resp))
	}
	for _, path := range []string{
		fmt.Sprintf("/api/projects/%d/baseline-batches/import", projectID),
		fmt.Sprintf("/api/projects/%d/baseline-batches/%d/review", projectID, draft.ID),
		fmt.Sprintf("/api/projects/%d/baseline-batches/%d/start", projectID, draft.ID),
		fmt.Sprintf("/api/projects/%d/baseline-batches/opt-in", projectID),
	} {
		if resp := postBaseline(t, ts, ts.memberCookie, path, map[string]any{"handover": json.RawMessage(handover), "enabled": false}); resp.StatusCode < 400 {
			t.Fatalf("viewer write to %s = %d", path, resp.StatusCode)
		}
	}
}

// TestBaselineBatchClosedDraftCannotBeReReviewed covers the consumed-draft
// resurrection path: it is a typed conflict, never a 500 and never a second
// batch off an already delivered baseline.
func TestBaselineBatchClosedDraftCannotBeReReviewed(t *testing.T) {
	ts := newTestServer(t)
	projectID := responseID(t, ts.post(t, "/api/projects", ts.adminCookie, map[string]string{"name": "Closed draft", "key": "CLD"}))
	optIn(t, ts, projectID)
	handover, _ := validHandover(t)
	var draft baselinebatch.Draft
	decode(t, postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/import", projectID),
		map[string]any{"handover": json.RawMessage(handover)}), &draft)
	review := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/review", projectID, draft.ID),
		map[string]any{"execution_mode": "manual", "selected_requirement_refs": []string{"req.login"}})
	decode(t, review, &draft)
	first := draft.ID
	started := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/start", projectID, draft.ID), map[string]any{
		"idempotency_key": "closed-draft-start", "review_id": *draft.ReviewID, "draft_revision": draft.Revision,
		"confirm": true, "content_digest": draft.Baseline.ContentDigest, "revision_seal": draft.Baseline.RevisionSeal,
		"execution_mode": "manual", "selected_requirement_refs": []string{"req.login"}, "worker": map[string]any{},
	})
	if started.StatusCode != 200 {
		t.Fatalf("start=%d %s", started.StatusCode, baselineReadBody(started))
	}
	// A later import opens a new draft; the consumed one stays closed.
	second, _ := handoverWith(t, []baselinebatch.Requirement{{
		Ref: "req.second", Statement: "Second baseline", AcceptanceCriteria: []string{"Distinct"}, ConstraintRefs: []string{},
	}}, "", "")
	if resp := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/import", projectID),
		map[string]any{"handover": json.RawMessage(second)}); resp.StatusCode != 201 {
		t.Fatalf("later import=%d %s", resp.StatusCode, baselineReadBody(resp))
	}
	resp := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/review", projectID, first),
		map[string]any{"execution_mode": "manual", "selected_requirement_refs": []string{"req.login"}})
	if resp.StatusCode != 409 {
		t.Fatalf("closed draft review=%d %s", resp.StatusCode, baselineReadBody(resp))
	}
	var batches int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM baseline_batch_batches WHERE project_id=?`, projectID).Scan(&batches); err != nil {
		t.Fatal(err)
	}
	if batches != 1 {
		t.Fatalf("closed draft produced %d batches", batches)
	}
}
