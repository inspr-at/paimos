// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/agentd"
	"github.com/inspr-at/paimos/backend/agentmessage"
	"github.com/inspr-at/paimos/backend/dispatchprofile"
	"github.com/inspr-at/paimos/backend/lifecycleclient"
	"github.com/inspr-at/paimos/backend/lifecycleintents"
)

type lifecycleFixtureAdapter struct {
	mu       sync.Mutex
	requests []agentd.StartRequest
	accounts map[string]bool
}

func (*lifecycleFixtureAdapter) Name() string                        { return "codex" }
func (*lifecycleFixtureAdapter) AccountLabel(context.Context) string { return "chatgpt" }
func (a *lifecycleFixtureAdapter) HasAccount(key string) bool        { return a.accounts[key] }
func (*lifecycleFixtureAdapter) Capabilities() []agentd.Capability {
	return []agentd.Capability{agentd.CapabilityInbox, agentd.CapabilityStatus, agentd.CapabilityStop}
}
func (a *lifecycleFixtureAdapter) Start(_ context.Context, r agentd.StartRequest, observe func(agentd.AdapterEvent)) (agentd.Process, error) {
	a.mu.Lock()
	a.requests = append(a.requests, r)
	a.mu.Unlock()
	observe(agentd.AdapterEvent{Kind: agentd.EventSessionStarted, HarnessSessionID: uuid.NewString()})
	observe(agentd.AdapterEvent{Kind: agentd.EventTurnCompleted})
	return &nativeFixtureProcess{done: make(chan struct{})}, nil
}

type lifecycleFixtureReporter struct {
	controller agentd.Controller
	public     string
}

func (r *lifecycleFixtureReporter) BindController(c agentd.Controller) error {
	r.controller = c
	return nil
}
func (*lifecycleFixtureReporter) AuthenticatedMachineID(context.Context) (string, error) {
	return "fixture-host", nil
}
func (r *lifecycleFixtureReporter) ReportStatus(ctx context.Context, status agentd.Status) error {
	for _, s := range status.Sessions {
		if s.State == agentd.StateRunning && s.Reporter.PublicSessionID == "" {
			return r.controller.CheckpointReporter(ctx, s.ID, agentd.ControlRequest{Instance: status.Instance, ProjectID: s.ProjectID, Identity: s.Identity}, agentd.ReporterState{PublicSessionID: r.public, Capabilities: []agentd.Capability{agentd.CapabilityInbox, agentd.CapabilityStatus, agentd.CapabilityStop}})
		}
	}
	return nil
}

func TestDaemonLifecycleStartsReservedGenerationAndProvesPublicMapping(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	root := t.TempDir()
	workspace, _ := filepath.EvalSymlinks(t.TempDir())
	profile, _ := dispatchprofile.Resolve("codex-sol-high", "1", "codex")
	bridge, _ := newCLIReporterWithRunner("fixture", "fixture-host", "/fixture/paimos", nil, func(context.Context, string, []string, []string, io.Reader) ([]byte, error) {
		return json.Marshal(map[string]any{"dispatch_profiles": []dispatchprofile.Profile{profile}})
	}, newMemoryReporterLeaseStore())
	a := &lifecycleFixtureAdapter{}
	reporter := &lifecycleFixtureReporter{public: uuid.NewString()}
	controller, e := agentd.NewSupervisor(agentd.SupervisorConfig{Instance: "fixture", StateRoot: root, Adapters: []agentd.Adapter{a}, Reporter: reporter, DispatchResolver: bridge, HeartbeatInterval: 20 * time.Millisecond})
	if e != nil {
		t.Fatal(e)
	}
	defer controller.Close(context.Background())
	provenance, e := controller.InspectWorkspace(ctx, workspace, agentd.WorkspaceExclusive)
	if e != nil {
		t.Fatal(e)
	}
	primary, e := newNativeConsumers(root, "fixture", bridge)
	if e != nil {
		t.Fatal(e)
	}
	defer primary.supervisor.Stop()
	primary.controller = controller
	runtimeID := uuid.NewString()
	handle := uuid.NewString()
	generation := controller.Status().DaemonID
	reserved := uuid.NewString()
	intent := lifecycleintents.Intent{SchemaVersion: 1, ID: uuid.NewString(), ProjectID: 42, State: "claimed", Revision: 2, NewGeneration: reserved, Request: lifecycleintents.Request{RequestKey: uuid.NewString(), Operation: "start", RuntimeID: runtimeID, RuntimeGeneration: generation, AccountLabel: "chatgpt", TTLSeconds: 300, WorkspaceHandle: handle, AgentName: "worker", DispatchProfileID: profile.ID, DispatchProfileVersion: profile.Version, Role: "worker", WorkShape: "unknown"}}
	mapped := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !lifecycleclient.ValidProof(r.Header.Get(lifecycleintents.RuntimeLeaseHeader)) || r.Header.Get("Authorization") != "Bearer fixture-key" {
			t.Error("runtime proof missing")
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/agents/worker.json"):
			_ = json.NewEncoder(w).Encode(map[string]any{"project": map[string]any{"id": 42, "key": "FIX"}, "agent": map[string]any{"project_id": 42, "name": "worker", "body": "canonical fixture instructions"}})
		case strings.HasSuffix(r.URL.Path, "/runtimes"):
			var in lifecycleintents.Registration
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in.Generation != generation || in.Workspaces[0].Identity != provenance.Identity {
				t.Error("configured provenance missing")
			}
			_ = json.NewEncoder(w).Encode(lifecycleintents.Runtime{ID: runtimeID, ProjectID: 42, Generation: generation, MachineID: in.Host, AccountLabel: in.AccountLabel, Workspaces: in.Workspaces, Profiles: in.Profiles, ExpiresAt: time.Now().Add(120 * time.Second).Format(time.RFC3339Nano)})
		case strings.HasSuffix(r.URL.Path, "/sessions"):
			var in lifecycleintents.SessionRegistration
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in.Generation != reserved || in.SessionID != reporter.public || !lifecycleclient.ValidProof(r.Header.Get(lifecycleintents.HarnessLeaseHeader)) {
				t.Error("public mapping lacked exact generation and worker proof")
			}
			mapped = true
			_ = json.NewEncoder(w).Encode(map[string]bool{"registered": true})
		case strings.HasSuffix(r.URL.Path, "/claim"):
			var in *lifecycleintents.Intent
			if intent.State != "completed" {
				copy := intent
				in = &copy
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "intent": in})
		case strings.HasSuffix(r.URL.Path, "/transition"):
			var in lifecycleintents.Transition
			_ = json.NewDecoder(r.Body).Decode(&in)
			if in.ExpectedRevision != intent.Revision {
				t.Error("transition revision mismatch")
			}
			if in.State == "completed" && (!mapped || in.ResultSessionID != reporter.public) {
				t.Error("completion preceded public mapping")
			}
			if in.State == "completed" && intent.Request.Operation == "reassign" && controller.Status().Sessions[0].TicketID != 0 {
				t.Error("local binding changed before server completion CAS")
			}
			intent.State = in.State
			intent.Reason = in.Reason
			intent.Revision++
			intent.ResultSessionID = in.ResultSessionID
			_ = json.NewEncoder(w).Encode(intent)
		case strings.HasSuffix(r.URL.Path, "/message-targets"):
			_ = json.NewEncoder(w).Encode(map[string]any{"targets": []any{}})
		case strings.HasSuffix(r.URL.Path, "/runtime-health"):
			var in agentmessage.RuntimeHealthInput
			_ = json.NewDecoder(r.Body).Decode(&in)
			_ = json.NewEncoder(w).Encode(agentmessage.RuntimeHealthResult{SchemaVersion: 1, State: in.State, Sequence: in.Sequence})
		default:
			t.Errorf("unexpected scoped route %s", r.URL.Path)
			http.Error(w, "unavailable", 404)
		}
	}))
	defer server.Close()
	configPath := filepath.Join(t.TempDir(), "runtime.json")
	keyPath := filepath.Join(t.TempDir(), "key")
	config := lifecycleConfig{Projects: []configuredProject{{ProjectID: 42, AccountLabel: "chatgpt", Profiles: []lifecycleintents.Profile{{ID: profile.ID, Version: profile.Version}}, Workspaces: []configuredWorkspace{{Handle: handle, Path: workspace, Identity: provenance.Identity, Label: "Fixture workspace"}}}}}
	raw, _ := json.Marshal(config)
	_ = os.WriteFile(configPath, raw, 0600)
	_ = os.WriteFile(keyPath, []byte("fixture-key"), 0600)
	d, e := newDaemonLifecycle(configPath, root, "fixture", server.URL, keyPath, controller, primary, bridge)
	if e != nil {
		t.Fatal(e)
	}
	if e = d.projects[0].step(ctx); e != nil {
		t.Fatal(e)
	}
	// The authorized server completion applies binding first; only then may the
	// daemon mirror the binding in its own persisted session record.
	ticket := int64(9)
	intent.ID = uuid.NewString()
	intent.State = "claimed"
	intent.Revision = 2
	intent.ResultSessionID = ""
	intent.Reason = ""
	intent.NewGeneration = ""
	intent.Request.RequestKey = uuid.NewString()
	intent.Request.Operation = "reassign"
	intent.Request.SessionID = reporter.public
	intent.Request.SessionGeneration = reserved
	intent.Request.ExpectedRevision = 10
	intent.Request.TicketID = &ticket
	intent.Request.WorkShape = "ship"
	if e = d.projects[0].step(ctx); e != nil {
		t.Fatal(e)
	}
	if controller.Status().Sessions[0].TicketID != ticket || intent.State != "completed" {
		t.Fatal("committed binding was not mirrored")
	}
	if intent.State != "completed" || !mapped {
		t.Fatal("actual daemon execution incomplete")
	}
	if e = d.projects[0].step(ctx); e != nil {
		t.Fatal(e)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.requests) != 1 || !strings.Contains(a.requests[0].Prompt, "canonical fixture instructions") {
		t.Fatal("canonical prompt missing or duplicate spawn")
	}
	if controller.Status().Sessions[0].ID != reserved {
		t.Fatal("server-reserved generation was not actual local generation")
	}
}

func TestDaemonLifecycleAdvertisesTwoAccountsAndRejectsWrongKey(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	root := t.TempDir()
	workspace, _ := filepath.EvalSymlinks(t.TempDir())
	profile, _ := dispatchprofile.Resolve("codex-sol-high", "1", "codex")
	bridge, _ := newCLIReporterWithRunner("fixture", "fixture-host", "/fixture/paimos", nil, func(context.Context, string, []string, []string, io.Reader) ([]byte, error) {
		return json.Marshal(map[string]any{"dispatch_profiles": []dispatchprofile.Profile{profile}})
	}, newMemoryReporterLeaseStore())
	a := &lifecycleFixtureAdapter{accounts: map[string]bool{"coordinator": true, "personal": true}}
	reporter := &lifecycleFixtureReporter{public: uuid.NewString()}
	controller, e := agentd.NewSupervisor(agentd.SupervisorConfig{Instance: "named-accounts", StateRoot: root, Adapters: []agentd.Adapter{a}, Reporter: reporter, DispatchResolver: bridge, HeartbeatInterval: 20 * time.Millisecond})
	if e != nil {
		t.Fatal(e)
	}
	defer controller.Close(context.Background())
	provenance, e := controller.InspectWorkspace(ctx, workspace, agentd.WorkspaceExclusive)
	if e != nil {
		t.Fatal(e)
	}
	primary, e := newNativeConsumers(root, "named-accounts", bridge)
	if e != nil {
		t.Fatal(e)
	}
	defer primary.supervisor.Stop()
	primary.controller = controller
	handle := uuid.NewString()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/agents/worker.json") {
			_ = json.NewEncoder(w).Encode(map[string]any{"project": map[string]any{"id": 42, "key": "FIX"}, "agent": map[string]any{"project_id": 42, "name": "worker", "body": "canonical fixture instructions"}})
			return
		}
		http.Error(w, "unavailable", 404)
	}))
	defer server.Close()
	configPath := filepath.Join(t.TempDir(), "runtime.json")
	keyPath := filepath.Join(t.TempDir(), "key")
	config := lifecycleConfig{Projects: []configuredProject{{
		ProjectID: 42, AccountLabel: "chatgpt",
		Accounts:   []configuredAccount{{Key: "coordinator", Label: "Coordinator"}, {Key: "personal", Label: "Personal"}},
		Profiles:   []lifecycleintents.Profile{{ID: profile.ID, Version: profile.Version}},
		Workspaces: []configuredWorkspace{{Handle: handle, Path: workspace, Identity: provenance.Identity, Label: "Fixture workspace"}},
	}}}
	raw, _ := json.Marshal(config)
	_ = os.WriteFile(configPath, raw, 0600)
	_ = os.WriteFile(keyPath, []byte("fixture-key"), 0600)
	d, e := newDaemonLifecycle(configPath, root, "named-accounts", server.URL, keyPath, controller, primary, bridge)
	if e != nil {
		t.Fatal(e)
	}
	reg := d.projects[0].registration
	if reg.SchemaVersion != lifecycleintents.AccountChoiceSchemaV2 || len(reg.Accounts) != 2 || reg.Accounts[0].Key != "coordinator" || reg.AccountLabel != "chatgpt" {
		t.Fatalf("advertisement=%+v", reg)
	}
	mixed := config
	mixed.Projects[0].AccountKey = "coordinator"
	mixedRaw, _ := json.Marshal(mixed)
	mixedPath := filepath.Join(t.TempDir(), "runtime.json")
	_ = os.WriteFile(mixedPath, mixedRaw, 0600)
	if _, err := newDaemonLifecycle(mixedPath, root, "named-accounts-mixed", server.URL, keyPath, controller, primary, bridge); err == nil {
		t.Fatal("mixed account_key and accounts was accepted")
	}
	intent := lifecycleintents.Intent{
		SchemaVersion: lifecycleintents.AccountChoiceSchemaV2, ID: uuid.NewString(), ProjectID: 42, State: "claimed", NewGeneration: uuid.NewString(),
		Request: lifecycleintents.Request{
			RequestKey: uuid.NewString(), Operation: "start", RuntimeID: uuid.NewString(), RuntimeGeneration: controller.Status().DaemonID,
			AccountLabel: "chatgpt", AccountKey: "missing", TTLSeconds: 120, WorkspaceHandle: handle, AgentName: "worker",
			DispatchProfileID: profile.ID, DispatchProfileVersion: profile.Version, Role: "worker", WorkShape: "unknown",
		},
	}
	if err := d.projects[0].Prepare(ctx, intent); err == nil {
		t.Fatal("unconfigured account was prepared")
	}
	intent.Request.AccountKey = "coordinator"
	if err := d.projects[0].Prepare(ctx, intent); err != nil {
		t.Fatal(err)
	}
	prepared := d.projects[0].prepared[intent.ID]
	if prepared.AccountKey != "coordinator" || prepared.ExpectedAccountLabel != "chatgpt" {
		t.Fatalf("prepared=%+v", prepared)
	}
	intent.ID, intent.Request.AccountKey, intent.NewGeneration = uuid.NewString(), "personal", uuid.NewString()
	if err := d.projects[0].Prepare(ctx, intent); err != nil || d.projects[0].prepared[intent.ID].AccountKey != "personal" {
		t.Fatal("second configured account could not be prepared")
	}
}

func TestDaemonLifecycleLegacyAccountKeyAdvertisesContractValidLabels(t *testing.T) {
	long := strings.Repeat("n", 49)
	cases := []struct {
		name, key, instance string
	}{
		{"ordinary", "coordinator", "legacy-ordinary"},
		{"namespaced", "team:alpha", "legacy-namespaced"},
		{"long", long, "legacy-long"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, cleanup := daemonLifecycleFromProject(t, tc.instance, configuredProject{
				ProjectID: 42, AccountLabel: "chatgpt", AccountKey: tc.key,
			})
			defer cleanup()
			reg := d.projects[0].registration
			if reg.SchemaVersion != lifecycleintents.AccountChoiceSchemaV2 || len(reg.Accounts) != 1 {
				t.Fatalf("advertisement=%+v", reg)
			}
			if reg.Accounts[0].Key != tc.key {
				t.Fatalf("legacy key rewritten: %+v", reg.Accounts[0])
			}
			if !lifecycleintents.ValidAccountChoiceLabel(reg.Accounts[0].Label) {
				t.Fatalf("derived label not contract-valid: %+v", reg.Accounts[0])
			}
			if err := lifecycleintents.ValidateAdvertisedAccounts(reg); err != nil {
				t.Fatalf("server contract rejected registration: %v %+v", err, reg.Accounts[0])
			}
			if tc.name == "ordinary" && reg.Accounts[0].Label != "coordinator" {
				t.Fatalf("ordinary key was not reused as label: %+v", reg.Accounts[0])
			}
			if tc.name == "namespaced" && (reg.Accounts[0].Label == tc.key || strings.Contains(reg.Accounts[0].Label, ":")) {
				t.Fatalf("colon key used as label: %+v", reg.Accounts[0])
			}
			if tc.name == "long" && (len(reg.Accounts[0].Label) > 48 || reg.Accounts[0].Label == tc.key) {
				t.Fatalf("long key used as label: %+v", reg.Accounts[0])
			}
		})
	}
}

func TestDaemonLifecycleRejectsInvalidAccountChoiceLabelsBeforeRegistration(t *testing.T) {
	secret := "sk-live-abcdefghijk"
	cases := []struct {
		name, label, cause string
	}{
		{"colon", "Work: Main", "colon not allowed"},
		{"too-long", strings.Repeat("a", 60), "exceeds 48 characters"},
		{"secret-like", secret, "secret-like value"},
		{"whitespace", " Coordinator", "surrounding whitespace"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err, cleanup := daemonLifecycleErrFromProject(t, "invalid-label-"+tc.name, configuredProject{
				ProjectID:    42,
				AccountLabel: "chatgpt",
				Accounts:     []configuredAccount{{Key: "coordinator", Label: tc.label}},
			})
			defer cleanup()
			if err == nil {
				t.Fatal("invalid label loaded")
			}
			msg := err.Error()
			if !strings.Contains(msg, "accounts[0].label") || !strings.Contains(msg, tc.cause) {
				t.Fatalf("error=%q", msg)
			}
			if strings.Contains(msg, tc.label) {
				t.Fatalf("error echoed configured label: %q", msg)
			}
		})
	}
	_, err, cleanup := daemonLifecycleErrFromProject(t, "invalid-label-profile", configuredProject{
		ProjectID:    42,
		AccountLabel: "chatgpt",
		Accounts:     []configuredAccount{{Key: "coordinator", Label: "codex-sol-high"}},
	})
	defer cleanup()
	if err == nil || !strings.Contains(err.Error(), "accounts[0].label collides with profile") {
		t.Fatalf("profile collision error=%v", err)
	}
	_, err, cleanup2 := daemonLifecycleErrFromProject(t, "invalid-label-key-ambiguity", configuredProject{
		ProjectID:    42,
		AccountLabel: "chatgpt",
		Accounts: []configuredAccount{
			{Key: "coordinator", Label: "Coordinator"},
			{Key: "personal", Label: "coordinator"},
		},
	})
	defer cleanup2()
	if err == nil || !strings.Contains(err.Error(), "accounts[1].label collides with account key") {
		t.Fatalf("key/label ambiguity error=%v", err)
	}
}

func daemonLifecycleFromProject(t *testing.T, instance string, project configuredProject) (*daemonLifecycle, func()) {
	t.Helper()
	d, err, cleanup := daemonLifecycleErrFromProject(t, instance, project)
	if err != nil {
		cleanup()
		t.Fatal(err)
	}
	return d, cleanup
}

func daemonLifecycleErrFromProject(t *testing.T, instance string, project configuredProject) (*daemonLifecycle, error, func()) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	workspace, _ := filepath.EvalSymlinks(t.TempDir())
	profile, _ := dispatchprofile.Resolve("codex-sol-high", "1", "codex")
	bridge, _ := newCLIReporterWithRunner("fixture", "fixture-host", "/fixture/paimos", nil, func(context.Context, string, []string, []string, io.Reader) ([]byte, error) {
		return json.Marshal(map[string]any{"dispatch_profiles": []dispatchprofile.Profile{profile}})
	}, newMemoryReporterLeaseStore())
	accounts := map[string]bool{"coordinator": true, "personal": true, "team:alpha": true, strings.Repeat("n", 49): true}
	a := &lifecycleFixtureAdapter{accounts: accounts}
	reporter := &lifecycleFixtureReporter{public: uuid.NewString()}
	controller, e := agentd.NewSupervisor(agentd.SupervisorConfig{Instance: instance, StateRoot: root, Adapters: []agentd.Adapter{a}, Reporter: reporter, DispatchResolver: bridge, HeartbeatInterval: 20 * time.Millisecond})
	if e != nil {
		t.Fatal(e)
	}
	primary, e := newNativeConsumers(root, instance, bridge)
	if e != nil {
		controller.Close(context.Background())
		t.Fatal(e)
	}
	primary.controller = controller
	handle := uuid.NewString()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", 404)
	}))
	cleanup := func() {
		server.Close()
		primary.supervisor.Stop()
		controller.Close(context.Background())
	}
	if project.Profiles == nil && len(project.AccountScopes) == 0 {
		project.Profiles = []lifecycleintents.Profile{{ID: profile.ID, Version: profile.Version}}
	}
	if project.Workspaces == nil {
		provenance, err := controller.InspectWorkspace(ctx, workspace, agentd.WorkspaceExclusive)
		if err != nil {
			cleanup()
			t.Fatal(err)
		}
		project.Workspaces = []configuredWorkspace{{Handle: handle, Path: workspace, Identity: provenance.Identity, Label: "Fixture workspace"}}
	}
	configPath := filepath.Join(t.TempDir(), "runtime.json")
	keyPath := filepath.Join(t.TempDir(), "key")
	raw, _ := json.Marshal(lifecycleConfig{Projects: []configuredProject{project}})
	_ = os.WriteFile(configPath, raw, 0600)
	_ = os.WriteFile(keyPath, []byte("fixture-key"), 0600)
	d, err := newDaemonLifecycle(configPath, root, instance, server.URL, keyPath, controller, primary, bridge)
	return d, err, cleanup
}

type namedLifecycleAdapter struct {
	lifecycleFixtureAdapter
	name, label string
}

func (a *namedLifecycleAdapter) Name() string                        { return a.name }
func (a *namedLifecycleAdapter) AccountLabel(context.Context) string { return a.label }

func TestDaemonLifecyclePreparesMixedCodexAndCursorScopes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	root := t.TempDir()
	codexWorkspace, _ := filepath.EvalSymlinks(t.TempDir())
	cursorWorkspace, _ := filepath.EvalSymlinks(t.TempDir())
	codexProfile, _ := dispatchprofile.Resolve("codex-sol-high", "1", "codex")
	cursorProfile, _ := dispatchprofile.Resolve("cursor-composer", "1", "cursor")
	bridge, _ := newCLIReporterWithRunner("fixture", "fixture-host", "/fixture/paimos", nil, func(context.Context, string, []string, []string, io.Reader) ([]byte, error) {
		return json.Marshal(map[string]any{"dispatch_profiles": []dispatchprofile.Profile{codexProfile, cursorProfile}})
	}, newMemoryReporterLeaseStore())
	codex := &namedLifecycleAdapter{lifecycleFixtureAdapter: lifecycleFixtureAdapter{accounts: map[string]bool{"codex-work": true, "codex-home": true, "codex-lab": true}}, name: "codex", label: "chatgpt"}
	cursor := &namedLifecycleAdapter{lifecycleFixtureAdapter: lifecycleFixtureAdapter{accounts: map[string]bool{"cursor-op": true}}, name: "cursor", label: "cursor_context"}
	reporter := &lifecycleFixtureReporter{public: uuid.NewString()}
	controller, e := agentd.NewSupervisor(agentd.SupervisorConfig{Instance: "mixed-scopes", StateRoot: root, Adapters: []agentd.Adapter{codex, cursor}, Reporter: reporter, DispatchResolver: bridge, HeartbeatInterval: 20 * time.Millisecond})
	if e != nil {
		t.Fatal(e)
	}
	defer controller.Close(context.Background())
	codexProvenance, e := controller.InspectWorkspace(ctx, codexWorkspace, agentd.WorkspaceExclusive)
	if e != nil {
		t.Fatal(e)
	}
	cursorProvenance, e := controller.InspectWorkspace(ctx, cursorWorkspace, agentd.WorkspaceExclusive)
	if e != nil {
		t.Fatal(e)
	}
	primary, e := newNativeConsumers(root, "mixed-scopes", bridge)
	if e != nil {
		t.Fatal(e)
	}
	defer primary.supervisor.Stop()
	primary.controller = controller
	codexHandle, cursorHandle := uuid.NewString(), uuid.NewString()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/agents/") {
			_ = json.NewEncoder(w).Encode(map[string]any{"project": map[string]any{"id": 42, "key": "FIX"}, "agent": map[string]any{"project_id": 42, "name": "worker", "body": "canonical fixture instructions"}})
			return
		}
		http.Error(w, "unavailable", 404)
	}))
	defer server.Close()
	config := lifecycleConfig{Projects: []configuredProject{{
		ProjectID: 42,
		AccountScopes: []lifecycleintents.AccountScope{
			{AccountLabel: "chatgpt", Accounts: []lifecycleintents.AccountChoice{{Key: "codex-work", Label: "Work"}, {Key: "codex-home", Label: "Home"}, {Key: "codex-lab", Label: "Lab"}}, Profiles: []lifecycleintents.Profile{{ID: codexProfile.ID, Version: codexProfile.Version}}},
			{AccountLabel: "cursor_context", Accounts: []lifecycleintents.AccountChoice{{Key: "cursor-op", Label: "Cursor"}}, Profiles: []lifecycleintents.Profile{{ID: cursorProfile.ID, Version: cursorProfile.Version}}},
		},
		Workspaces: []configuredWorkspace{
			{Handle: codexHandle, Path: codexWorkspace, Identity: codexProvenance.Identity, Label: "Codex"},
			{Handle: cursorHandle, Path: cursorWorkspace, Identity: cursorProvenance.Identity, Label: "Cursor"},
		},
	}}}
	configPath := filepath.Join(t.TempDir(), "runtime.json")
	keyPath := filepath.Join(t.TempDir(), "key")
	raw, _ := json.Marshal(config)
	_ = os.WriteFile(configPath, raw, 0600)
	_ = os.WriteFile(keyPath, []byte("fixture-key"), 0600)
	d, e := newDaemonLifecycle(configPath, root, "mixed-scopes", server.URL, keyPath, controller, primary, bridge)
	if e != nil {
		t.Fatal(e)
	}
	reg := d.projects[0].registration
	if reg.SchemaVersion != lifecycleintents.AccountScopeSchemaV3 || reg.AccountLabel != "" || len(reg.AccountScopes) != 2 {
		t.Fatalf("advertisement=%+v", reg)
	}
	mixed := config
	mixed.Projects[0].AccountLabel = "chatgpt"
	mixedPath := filepath.Join(t.TempDir(), "mixed.json")
	mixedRaw, _ := json.Marshal(mixed)
	_ = os.WriteFile(mixedPath, mixedRaw, 0600)
	if _, err := newDaemonLifecycle(mixedPath, root, "mixed-scopes-class", server.URL, keyPath, controller, primary, bridge); err == nil {
		t.Fatal("mixed singular class and account_scopes was accepted")
	}
	cursorIntent := lifecycleintents.Intent{
		SchemaVersion: lifecycleintents.AccountChoiceSchemaV2, ID: uuid.NewString(), ProjectID: 42, State: "claimed", NewGeneration: uuid.NewString(),
		Request: lifecycleintents.Request{
			RequestKey: uuid.NewString(), Operation: "start", RuntimeID: uuid.NewString(), RuntimeGeneration: controller.Status().DaemonID,
			AccountLabel: "cursor_context", AccountKey: "cursor-op", TTLSeconds: 120, WorkspaceHandle: cursorHandle, AgentName: "worker",
			DispatchProfileID: cursorProfile.ID, DispatchProfileVersion: cursorProfile.Version, Role: "worker", WorkShape: "unknown",
		},
	}
	if err := d.projects[0].Prepare(ctx, cursorIntent); err != nil {
		t.Fatal(err)
	}
	prepared := d.projects[0].prepared[cursorIntent.ID]
	if prepared.ExpectedAccountLabel != "cursor_context" || prepared.AccountKey != "cursor-op" || prepared.Adapter != "cursor" {
		t.Fatalf("prepared=%+v", prepared)
	}
	crossed := cursorIntent
	crossed.ID, crossed.NewGeneration, crossed.Request.RequestKey = uuid.NewString(), uuid.NewString(), uuid.NewString()
	crossed.Request.AccountLabel, crossed.Request.AccountKey = "chatgpt", "codex-work"
	if err := d.projects[0].Prepare(ctx, crossed); err == nil {
		t.Fatal("codex class prepared a cursor profile")
	}
}
