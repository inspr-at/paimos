// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/backend/agentd"
	"github.com/inspr-at/paimos/backend/dispatchprofile"
	"github.com/inspr-at/paimos/backend/models"
	"github.com/zalando/go-keyring"
)

const friendlyParentID = "11111111-1111-4111-8111-111111111111"
const friendlyPublicID = "22222222-2222-4222-8222-222222222222"

type friendlyFakeDaemon struct {
	status              agentd.Status
	startCount          int
	startErr, statusErr error
	public              bool
	request             agentd.StartRequest
}

func (f *friendlyFakeDaemon) Status(context.Context) (agentd.Status, error) {
	return f.status, f.statusErr
}
func (f *friendlyFakeDaemon) Start(_ context.Context, request agentd.StartRequest) (agentd.Session, error) {
	f.startCount++
	f.request = request
	if f.startErr != nil {
		return agentd.Session{}, f.startErr
	}
	profile, _ := dispatchprofile.Resolve(request.DispatchProfileID, request.DispatchProfileVersion, request.Adapter)
	s := agentd.Session{DispatchProfile: &profile, ID: "local-generation", HarnessSessionID: "private-vendor-reference", ProjectID: request.ProjectID, Identity: request.Identity, Adapter: request.Adapter, Role: request.Role, ParentSessionID: request.ParentSessionID, TicketID: request.TicketID, WorkShape: request.WorkShape, Managed: true, State: agentd.StateRunning, Capabilities: []agentd.Capability{agentd.CapabilityStatus, agentd.CapabilitySteer, agentd.CapabilityStop, agentd.CapabilityInterrupt}}
	if f.public {
		s.Reporter.PublicSessionID = friendlyPublicID
	}
	return s, nil
}

type friendlyFixture struct {
	daemon                        *friendlyFakeDaemon
	opts                          friendlyStartOptions
	sessions                      []models.HarnessSession
	requests, writes, configReads int
	conflict, changed             bool
	revision                      int64
}

func newFriendlyFixture(t *testing.T) *friendlyFixture {
	t.Helper()
	f := &friendlyFixture{daemon: &friendlyFakeDaemon{status: agentd.Status{Instance: "test-deployment", DaemonID: "daemon-fixture"}, public: true}}
	f.opts = friendlyStartOptions{Project: "PAI", Agent: "builder", Ticket: "PAI-921", Shape: "ship", Parent: friendlyParentID, Role: "worker", Profile: "codex-sol-high@1", Workspace: t.TempDir(), Key: "fixture-start", Deployment: "test-deployment", Label: "builder", StateRoot: t.TempDir(), ExpectedRevision: -1}
	f.sessions = []models.HarnessSession{{ID: friendlyParentID, ProjectID: 42, AgentName: "root", Harness: "codex", Role: "coordinator", Phase: "working"}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests++
		if r.Method != http.MethodGet {
			f.writes++
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/health":
			json.NewEncoder(w).Encode(map[string]any{"agent_bus_identity_enforced": true, "agent_bus_instance": "test-deployment", "deployment_instance": "test-deployment"})
		case "/api/projects":
			json.NewEncoder(w).Encode([]orchestratorProject{{ID: 42, Key: "PAI"}})
		case "/api/projects/42/agents":
			ioString(w, `[{"project_id":42,"name":"builder"}]`)
		case "/api/projects/42/agents/builder.json":
			ioString(w, `{"project":{"id":42,"key":"PAI"},"agent":{"name":"builder","project_id":42,"body":"PRIVATE FIXTURE PERSONA","non_negotiable_rules":[]}}`)
		case "/api/ai/execution-options":
			if r.URL.RawQuery != "dispatch_only=1" {
				t.Error("requested broad execution options")
			}
			json.NewEncoder(w).Encode(map[string]any{"dispatch_profiles": dispatchprofile.List()})
		case "/api/issues/PAI-921":
			ioString(w, `{"id":921,"project_id":42}`)
		case "/api/projects/42/harness-sessions":
			json.NewEncoder(w).Encode(f.sessions)
		case "/api/projects/42/harness-sessions/" + friendlyPublicID:
			profile, _ := dispatchprofile.Resolve("codex-sol-high", "1", "codex")
			snapshot := models.HarnessDispatchProfile(profile)
			request := f.daemon.request
			var parent *string
			if request.ParentSessionID != "" {
				parent = &request.ParentSessionID
			}
			var ticket *int64
			if request.TicketID != 0 {
				ticket = &request.TicketID
			}
			shape := request.WorkShape
			if shape == "" {
				shape = "unknown"
			}
			json.NewEncoder(w).Encode(models.HarnessSession{ID: friendlyPublicID, ProjectID: 42, AgentName: "builder", Harness: "codex", ManagementMode: "managed", Phase: "working", Role: request.Role, ParentSessionID: parent, TicketID: ticket, WorkShape: shape, DispatchProfile: &snapshot, Capabilities: models.HarnessCapabilities{Status: true, Interrupt: true, Stop: true}})
		case "/api/orchestrator/v1/config":
			if r.Method == http.MethodGet {
				f.configReads++
				if f.changed && f.configReads > 1 {
					f.revision++
				}
			} else {
				var body struct {
					Revision int64 `json:"expected_revision"`
				}
				json.NewDecoder(r.Body).Decode(&body)
				if f.conflict || body.Revision != f.revision {
					http.Error(w, `{"error":"conflict"}`, http.StatusConflict)
					return
				}
				f.revision++
			}
			config := orchestratorConfig{SchemaVersion: 1, Revision: f.revision}
			if f.revision > 0 {
				stamp := "2026-09-06T00:00:00Z"
				config.UpdatedAt = &stamp
			}
			if r.Method == http.MethodPut {
				config.Orchestrator = &orchestratorTarget{ProjectID: 42, ProjectKey: "PAI", ProjectAgentID: 7, Key: "builder", DisplayLabel: "builder"}
			}
			json.NewEncoder(w).Encode(config)
		case "/api/projects/42/message-targets":
			ioString(w, `{"targets":[]}`)
		default:
			t.Errorf("unexpected fixture route: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv(envURL, srv.URL)
	t.Setenv(envAPIKey, "fixture-auth-value")
	old := friendlyDaemonClient
	friendlyDaemonClient = func(string) (friendlyDaemon, error) { return f.daemon, nil }
	t.Cleanup(func() { friendlyDaemonClient = old })
	return f
}
func ioString(w http.ResponseWriter, s string) { _, _ = w.Write([]byte(s)) }

func TestFriendlyStartWorkerResolvesAndReplaysOriginalOutcome(t *testing.T) {
	f := newFriendlyFixture(t)
	result, err := runFriendlyStart(context.Background(), f.opts)
	if err != nil || result.Outcome != "started" || result.PublicSessionID != friendlyPublicID || result.State != "running" {
		t.Fatalf("start failed: %v", err)
	}
	if f.daemon.request.TicketID != 921 || f.daemon.request.ParentSessionID != friendlyParentID || f.daemon.request.DispatchProfileVersion != "1" || !strings.Contains(f.daemon.request.Prompt, "PRIVATE FIXTURE PERSONA") {
		t.Fatal("canonical resolution was not applied")
	}
	for _, name := range []string{"status", "message", "steer", "interrupt", "stop"} {
		if result.Commands[name] == "" {
			t.Errorf("missing %s command", name)
		}
	}
	requests := f.requests
	f.sessions[0].Phase = "stopped"
	replay, err := runFriendlyStart(context.Background(), f.opts)
	if err != nil || replay.PublicSessionID != result.PublicSessionID || !replay.Replayed || f.requests != requests || f.daemon.startCount != 1 {
		t.Fatal("replay did not return original result without repeated effects")
	}
	f.opts.Shape = "scout"
	if _, err := runFriendlyStart(context.Background(), f.opts); err == nil || !strings.Contains(err.Error(), "different input") {
		t.Fatal("conflicting key accepted")
	}
	raw, _ := json.Marshal(result)
	for _, forbidden := range []string{"fixture-auth-value", "PRIVATE FIXTURE PERSONA", "private-vendor-reference", f.opts.Workspace, "local-generation"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatal("result contains private runtime data")
		}
	}
}
func TestFriendlyStartDryRunAndExplainHaveNoWrites(t *testing.T) {
	for _, explain := range []bool{false, true} {
		t.Run(map[bool]string{false: "dry_run", true: "explain"}[explain], func(t *testing.T) {
			f := newFriendlyFixture(t)
			f.opts.DryRun = !explain
			f.opts.Explain = explain
			result, err := runFriendlyStart(context.Background(), f.opts)
			if err != nil || result.Outcome != "validated" || result.State != "not_started" || f.writes != 0 || f.daemon.startCount != 0 {
				t.Fatalf("preview failed: %v", err)
			}
			entries, _ := os.ReadDir(f.opts.StateRoot)
			if len(entries) != 0 {
				t.Fatal("preview wrote local retry state")
			}
		})
	}
}
func TestFriendlyStartNegativePreflight(t *testing.T) {
	tests := []struct {
		name   string
		change func(*friendlyFixture)
		want   string
	}{
		{"unsupported harness", func(f *friendlyFixture) { f.opts.Harness = "unsupported" }, "unsupported harness"},
		{"no profile", func(f *friendlyFixture) { f.opts.Model = "no-such-model" }, "0 compatible profiles"},
		{"ambiguous profile", func(f *friendlyFixture) { f.opts.Profile = ""; f.opts.Harness = "codex" }, "compatible profiles"},
		{"wrong profile version", func(f *friendlyFixture) { f.opts.Profile = "codex-sol-high@99" }, "0 compatible profiles"},
		{"cross project", func(f *friendlyFixture) { f.sessions[0].ProjectID = 99 }, "cross-project parent"},
		{"terminal parent", func(f *friendlyFixture) { f.sessions[0].Phase = "stopped" }, "parent is terminal"},
		{"ambiguous parent", func(f *friendlyFixture) { f.opts.Parent = "codex:root"; f.sessions = append(f.sessions, f.sessions[0]) }, "2 parent matches"},
		{"no parent", func(f *friendlyFixture) { f.sessions = nil }, "0 parent matches"},
		{"daemon unavailable", func(f *friendlyFixture) { f.daemon.statusErr = errors.New("private diagnostic") }, "daemon is unavailable"},
		{"daemon wrong instance", func(f *friendlyFixture) { f.daemon.status.Instance = "elsewhere" }, "identity does not match"},
		{"exclusive workspace", func(f *friendlyFixture) {
			f.daemon.status.Sessions = []agentd.Session{{Workspace: f.opts.Workspace, State: agentd.StateRunning}}
		}, "exclusively owned"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newFriendlyFixture(t)
			test.change(f)
			_, err := runFriendlyStart(context.Background(), f.opts)
			if err == nil || !strings.Contains(err.Error(), test.want) || f.daemon.startCount != 0 || f.writes != 0 {
				t.Fatalf("preflight rejection: %v", err)
			}
		})
	}
}
func TestFriendlyStartParentHandleAndSourceSelectors(t *testing.T) {
	f := newFriendlyFixture(t)
	f.opts.Parent = "codex:root"
	f.opts.Profile = ""
	f.opts.Harness = "codex"
	f.opts.Model = "gpt-5.6-sol"
	f.opts.Effort = "high"
	f.opts.Account = "local_probe"
	f.opts.Machine = "authenticated_reporter"
	result, err := runFriendlyStart(context.Background(), f.opts)
	if err != nil || result.Outcome != "started" || result.Plan.Parent != friendlyParentID {
		t.Fatalf("selectors failed: %v", err)
	}
}
func TestFriendlyStartOrchestratorCAS(t *testing.T) {
	for _, mode := range []string{"success", "conflict", "changed"} {
		t.Run(mode, func(t *testing.T) {
			f := newFriendlyFixture(t)
			f.opts.Coordinator = true
			f.opts.Role = "coordinator"
			f.opts.Parent = ""
			f.opts.Ticket = ""
			f.opts.Shape = ""
			f.sessions = nil
			f.conflict = mode == "conflict"
			f.changed = mode == "changed"
			result, err := runFriendlyStart(context.Background(), f.opts)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "success" {
				if result.Outcome != "started" || f.writes != 1 || f.daemon.startCount != 1 || f.daemon.request.ParentSessionID != "" {
					t.Fatal("orchestrator did not CAS and start")
				}
			} else if result.Outcome == "started" || f.daemon.startCount != 0 {
				t.Fatal("CAS failure spawned a generation")
			}
		})
	}
}
func TestFriendlyStartUnknownIsDurableAndNeverPrintsPrivateIDs(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(map[bool]string{false: "pending registration", true: "lost response"}[lost], func(t *testing.T) {
			f := newFriendlyFixture(t)
			f.daemon.public = false
			if lost {
				f.daemon.startErr = errors.New("private-vendor-error")
			}
			result, err := runFriendlyStart(context.Background(), f.opts)
			if err != nil || result.Outcome != "unknown" || result.PublicSessionID != "" {
				t.Fatalf("uncertain result: %v", err)
			}
			replay, err := runFriendlyStart(context.Background(), f.opts)
			if err != nil || replay.Outcome != "unknown" || f.daemon.startCount != 1 {
				t.Fatal("uncertain start retried spawn")
			}
		})
	}
}
func TestFriendlyStartDirtyWorkspaceAndGitEnvironmentIsolation(t *testing.T) {
	f := newFriendlyFixture(t)
	cmd := exec.Command("git", "init", "--quiet", f.opts.Workspace)
	if err := cmd.Run(); err != nil {
		t.Fatal("fixture git init failed")
	}
	if err := os.WriteFile(filepath.Join(f.opts.Workspace, "untracked"), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_DIR", t.TempDir())
	_, err := runFriendlyStart(context.Background(), f.opts)
	if err == nil || !strings.Contains(err.Error(), "dirty") || f.daemon.startCount != 0 {
		t.Fatalf("dirty workspace not rejected: %v", err)
	}
}
func TestFriendlyStartGuidedAndJSONUseSameResolver(t *testing.T) {
	f := newFriendlyFixture(t)
	// Keep a stopped predecessor in the catalog: guided selection must retain
	// the exact active UUID, and the retry must use that same resolved input.
	f.sessions = append(f.sessions, models.HarnessSession{ID: "33333333-3333-4333-8333-333333333333", ProjectID: 42, AgentName: "root", Harness: "codex", Role: "coordinator", Phase: "stopped"})
	o := f.opts
	o.Project = ""
	o.Agent = ""
	o.Ticket = ""
	o.Shape = ""
	o.Parent = ""
	o.Profile = ""
	var guidance bytes.Buffer
	if err := guideFriendlyStart(context.Background(), strings.NewReader("PAI\nbuilder\nPAI-921\nship\ncodex:root\ncodex-sol-high@1\n"), &guidance, &o); err != nil {
		t.Fatal(err)
	}
	result, err := runFriendlyStart(context.Background(), o)
	if err != nil || result.Outcome != "started" {
		t.Fatalf("guided start: %v", err)
	}
	out, _, err := executeCLIForTest(t, "worker", "start", "--json", "--non-interactive", "--project", "PAI", "--agent", "builder", "--ticket", "PAI-921", "--work-shape", "ship", "--parent", o.Parent, "--profile", "codex-sol-high@1", "--workspace", o.Workspace, "--state-root", o.StateRoot, "--expect-deployment-instance", o.Deployment, "--idempotency-key", o.Key, "--wait", "0s")
	var parsed friendlyStartResult
	if err != nil || json.Unmarshal([]byte(out), &parsed) != nil || parsed.PublicSessionID != friendlyPublicID || f.daemon.startCount != 1 {
		t.Fatal("CLI JSON did not replay the same validated request")
	}
}
func TestFriendlyStartUnavailableUnixDaemon(t *testing.T) {
	f := newFriendlyFixture(t)
	friendlyDaemonClient = func(socket string) (friendlyDaemon, error) { return agentd.NewClient(socket) }
	_, err := runFriendlyStart(context.Background(), f.opts)
	if err == nil || !strings.Contains(err.Error(), "daemon is unavailable") || f.writes != 0 {
		t.Fatalf("missing fixture socket: %v", err)
	}
}
func TestFriendlyStartRetryRecordConflictAndCrash(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	pending := friendlyStartResult{Outcome: "unknown", State: "unknown"}
	if err := reserveFriendlyStartRecord(dir, "key", "digest-a", pending); err != nil {
		t.Fatal(err)
	}
	if err := reserveFriendlyStartRecord(dir, "key", "digest-a", pending); err == nil {
		t.Fatal("concurrent reservation was accepted")
	}
	result, found, err := readFriendlyStartRecord(dir, "key", "digest-a")
	if err != nil || !found || result.Outcome != "unknown" {
		t.Fatal("crash intent did not survive")
	}
	if _, _, err := readFriendlyStartRecord(dir, "key", "digest-b"); err == nil {
		t.Fatal("conflicting crash retry was accepted")
	}
}

func TestFriendlyStartProfileCapabilityAndConstraintFailClosed(t *testing.T) {
	o := friendlyStartOptions{Profile: "codex-sol-high@1"}
	profile, _ := dispatchprofile.Resolve("codex-sol-high", "1", "codex")
	forged := profile
	forged.Effort = "max"
	if _, err := resolveFriendlyProfile([]dispatchprofile.Profile{forged}, o); err == nil {
		t.Fatal("altered immutable profile accepted")
	}
	request := agentd.StartRequest{Adapter: "codex"}
	for _, selector := range []string{"account-class", "account-key", "machine"} {
		scoped := o
		field := ""
		switch selector {
		case "account-class":
			scoped.Account = "chatgpt"
			field = "expected_account_label"
		case "account-key":
			scoped.Account = "coordinator"
			field = "account_key"
		default:
			scoped.Machine = "fixture-machine"
			field = "expected_machine_id"
		}
		got, err := friendlyConstrainedRequest(request, scoped)
		if err != nil {
			if !strings.Contains(err.Error(), "unsupported") {
				t.Fatal("constraint rejection was not actionable")
			}
		} else {
			raw, _ := json.Marshal(got)
			if !strings.Contains(string(raw), field) {
				t.Fatal("constraint silently dropped")
			}
		}
	}
}
func TestFriendlyStartPreviewDoesNotRequireRetryKey(t *testing.T) {
	f := newFriendlyFixture(t)
	f.opts.Key = ""
	f.opts.DryRun = true
	result, err := runFriendlyStart(context.Background(), f.opts)
	if err != nil || result.Outcome != "validated" {
		t.Fatalf("preview required a retry key: %v", err)
	}
}
func TestFriendlyStartReplaySurvivesRemovedWorkspace(t *testing.T) {
	f := newFriendlyFixture(t)
	if _, err := runFriendlyStart(context.Background(), f.opts); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(f.opts.Workspace); err != nil {
		t.Fatal(err)
	}
	replay, err := runFriendlyStart(context.Background(), f.opts)
	if err != nil || replay.Outcome != "started" || f.daemon.startCount != 1 {
		t.Fatal("completed replay depended on mutable workspace")
	}
}
func TestFriendlyStartLedgerRejectsSymlinkAndUnsafeModes(t *testing.T) {
	root := t.TempDir()
	private := filepath.Join(root, "private")
	if err := os.Mkdir(private, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(private, alias); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readFriendlyStartRecord(alias, "key", "digest"); err == nil {
		t.Fatal("symlink retry directory accepted")
	}
	if err := syncFriendlyStartDir(alias); err == nil {
		t.Fatal("symlink retry sync directory accepted")
	}
	if err := os.Chmod(private, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readFriendlyStartRecord(private, "key", "digest"); err == nil {
		t.Fatal("public retry directory accepted")
	}
	if err := syncFriendlyStartDir(private); err == nil {
		t.Fatal("public retry sync directory accepted")
	}
}

func TestFriendlyGuidedCLISelectsDisplayedRowsWithoutProfileIDs(t *testing.T) {
	f := newFriendlyFixture(t)
	// A stopped predecessor with the same harness:agent handle must not make
	// the one currently selectable active parent ambiguous.
	f.sessions = append(f.sessions, models.HarnessSession{ID: "33333333-3333-4333-8333-333333333333", ProjectID: 42, AgentName: "root", Harness: "codex", Role: "coordinator", Phase: "stopped"})
	var out, guidance bytes.Buffer
	oldOut, oldJSON := stdout, flagJSON
	stdout = &out
	defer func() { stdout = oldOut; flagJSON = oldJSON }()
	cmd := rootCmd()
	// The catalog is alphabetically ordered: codex-sol-high is row five.
	cmd.SetIn(strings.NewReader("1\n1\nPAI-921\n1\n1\n5\n"))
	cmd.SetErr(&guidance)
	cmd.SetArgs([]string{"worker", "start", "--guided", "--dry-run", "--json", "--workspace", f.opts.Workspace, "--state-root", f.opts.StateRoot, "--expect-deployment-instance", f.opts.Deployment})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var guided friendlyStartResult
	if json.Unmarshal(out.Bytes(), &guided) != nil || guided.Outcome != "validated" || guided.Plan.Profile.ID != "codex-sol-high" || guided.Plan.Parent != friendlyParentID {
		t.Fatal("guided rows did not resolve expected plan")
	}
	for _, label := range []string{"Project choices:", "PAI", "Agent choices:", "builder", "Parent choices:", "codex:root", "Profile choices:", "gpt-5.6-sol / high"} {
		if !strings.Contains(guidance.String(), label) {
			t.Fatalf("missing readable choice %s", label)
		}
	}
	f.opts.DryRun = true
	explicit, err := runFriendlyStart(context.Background(), f.opts)
	if err != nil || explicit.Plan.Profile != guided.Plan.Profile || explicit.Plan.Parent != guided.Plan.Parent || explicit.Plan.Agent != guided.Plan.Agent || f.writes != 0 || f.daemon.startCount != 0 {
		t.Fatal("guided preview diverged from shared resolver or wrote state")
	}
}

func TestFriendlyParentManualSelectionRetainsAmbiguityAndProjectGuards(t *testing.T) {
	active := models.HarnessSession{ID: friendlyParentID, ProjectID: 42, AgentName: "root", Harness: "codex", Phase: "working"}
	stopped := models.HarnessSession{ID: "33333333-3333-4333-8333-333333333333", ProjectID: 42, AgentName: "root", Harness: "codex", Phase: "stopped"}
	if _, err := resolveFriendlyParent([]models.HarnessSession{active, stopped}, 42, "codex:root"); err == nil {
		t.Fatal("manual handle selection must reject active ambiguity")
	}
	foreign := active
	foreign.ProjectID = 99
	if _, err := resolveFriendlyParent([]models.HarnessSession{foreign}, 42, foreign.ID); err == nil || !strings.Contains(err.Error(), "cross-project") {
		t.Fatalf("cross-project parent was not rejected: %v", err)
	}
}

func TestFriendlyGuidanceRefusesUndisplayedChoices(t *testing.T) {
	f := newFriendlyFixture(t)
	f.opts.Project = ""
	var out bytes.Buffer
	if err := guideFriendlyStart(context.Background(), strings.NewReader("99\n"), &out, &f.opts); err == nil || f.writes != 0 {
		t.Fatal("undisplayed choice accepted")
	}
}

type friendlyLostResponseDaemon struct {
	*friendlyFakeDaemon
	original agentd.Session
	key      string
}

func (d *friendlyLostResponseDaemon) Start(ctx context.Context, r agentd.StartRequest) (agentd.Session, error) {
	d.original, _ = d.friendlyFakeDaemon.Start(ctx, r)
	d.key = r.IdempotencyKey
	return agentd.Session{}, errors.New("lost response")
}
func (d *friendlyLostResponseDaemon) LookupStart(_ context.Context, key string) (agentd.Session, error) {
	if key != d.key {
		return agentd.Session{}, errors.New("wrong key")
	}
	return d.original, nil
}
func TestFriendlyReconcilesLostDaemonResponseWithoutRepeatedSpawn(t *testing.T) {
	f := newFriendlyFixture(t)
	daemon := &friendlyLostResponseDaemon{friendlyFakeDaemon: f.daemon}
	friendlyDaemonClient = func(string) (friendlyDaemon, error) { return daemon, nil }
	result, err := runFriendlyStart(context.Background(), f.opts)
	if err != nil || result.Outcome != "unknown" {
		t.Fatal("lost response did not stay unknown")
	}
	result, err = runFriendlyStart(context.Background(), f.opts)
	if err != nil || result.Outcome != "started" || !result.Replayed || result.PublicSessionID != friendlyPublicID || f.daemon.startCount != 1 {
		t.Fatal("original outcome was not reconciled")
	}
}

func TestFriendlyMissingParentGivesSelectedInstanceBootstrap(t *testing.T) {
	f := newFriendlyFixture(t)
	f.sessions = nil
	f.opts.Parent = ""
	friendlyNamedFixture(t)
	var out bytes.Buffer
	err := guideFriendlyStart(context.Background(), strings.NewReader(""), &out, &f.opts)
	if err == nil || (!strings.Contains(err.Error(), "paimos --instance 'fixture-instance'") || !strings.Contains(err.Error(), "orchestrator start --project PAI --guided")) {
		t.Fatal("missing parent guidance omitted selected instance/project bootstrap")
	}
	if f.writes != 0 || f.daemon.startCount != 0 {
		t.Fatal("guidance mutated state")
	}
}
func TestFriendlyDaemonHintIncludesSelectedInstance(t *testing.T) {
	f := newFriendlyFixture(t)
	f.daemon.statusErr = errors.New("unavailable")
	friendlyNamedFixture(t)
	_, err := runFriendlyStart(context.Background(), f.opts)
	if err == nil || !strings.Contains(err.Error(), "paimos --instance 'fixture-instance'") || !strings.Contains(err.Error(), "runtime setup") || !strings.Contains(err.Error(), "runtime doctor") {
		t.Fatal("daemon hint omitted selected instance")
	}
}

func friendlyNamedFixture(t *testing.T) {
	t.Helper()
	old, oldConfig := flagInstance, flagConfigPath
	flagInstance = "fixture-instance"
	flagConfigPath = filepath.Join(t.TempDir(), "config.yaml")
	t.Cleanup(func() { flagInstance = old; flagConfigPath = oldConfig })
	raw := "instances:\n  fixture-instance:\n    url: " + os.Getenv(envURL) + "\n"
	if err := os.WriteFile(flagConfigPath, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	keyring.MockInit()
	if err := keyringSet("fixture-instance", "fixture-credential"); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envURL, "")
	t.Setenv(envAPIKey, "")
}

func TestFriendlyReceiverSetupRequiresFreshEmptySlotAndExactScope(t *testing.T) {
	profile := dispatchprofile.Profile{Harness: "codex"}
	o := friendlyStartOptions{Project: "PAI", Agent: "worker", Deployment: "canonical-instance"}
	local := agentd.Session{ID: friendlyParentID, Identity: "codex:worker", HarnessSessionID: "private-fixture-reference"}
	public := models.HarnessSession{ID: friendlyPublicID, ProjectID: 42}
	plan := friendlyStartPlan{Profile: profile}
	for _, available := range []bool{false, true} {
		command := friendlyStartCommands("selected-instance", o, plan, public, local, available)["receiver-setup"]
		if strings.Contains(command, local.HarnessSessionID) || !strings.Contains(command, "--instance 'selected-instance'") || !strings.Contains(command, "--project 'PAI'") {
			t.Fatal("receiver suggestion leaked reference or lost selected scope")
		}
		if available {
			if !strings.Contains(command, "--project-id 42") || !strings.Contains(command, "--session '"+local.ID+"'") || !strings.Contains(command, "--instance 'canonical-instance'") || !strings.Contains(command, " | ") || !strings.Contains(command, "--target-ref-file -") {
				t.Fatal("receiver pipe missing exact owned scope")
			}
		} else if strings.Contains(command, "target set") || !strings.Contains(command, "runtime handoff") {
			t.Fatal("existing or unverified target offered replacement")
		}
	}
	claude := friendlyStartCommands("selected-instance", o, friendlyStartPlan{Profile: dispatchprofile.Profile{Harness: "claude"}}, public, local, true)["receiver-setup"]
	if strings.Contains(claude, "simple_fallback") || strings.Contains(claude, "target set") {
		t.Fatalf("Claude received unsupported fallback setup: %q", claude)
	}
}
