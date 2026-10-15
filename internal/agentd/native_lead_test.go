// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/attachwatch"
	"github.com/inspr-at/paimos/internal/localjournal"
	"github.com/inspr-at/paimos/internal/ownedprocess"
)

// One fake SDK Query, ordinary void interrupt(), and a correlated turn per
// streamed input. No model CLI, vendor login, network inference or account.
const nativeSDKFixture = `export function query({ prompt, options }) {
 const queue=[]; let wake; let closed=false; let held;
 const session=options.resume || '10000000-0000-4000-8000-000000000038';
 if (options.persistSession!==true || options.settingSources.length || !options.strictMcpConfig ||
     options.permissionMode!=='dontAsk' || options.tools.length || options.plugins.length || options.additionalDirectories.length)
   throw Error('native boundary');
 const push=(v)=>{queue.push(v);wake?.();wake=null};
 const result=(m)=>({type:'result',subtype:'success',session_id:session,user_message_uuid:m.uuid,result:'sdk:'+m.message.content[0].text});
 const accept=(m)=>{
   push({type:'assistant',session_id:session,user_message_uuid:m.uuid,message:{content:[]}});
   if(m.message.content[0].text==='hold')held=m;else push(result(m));
 };
 (async()=>{
   for(const tool of ['Bash','Write','Edit','Read','mcp__foreign__send']) {
     if((await options.canUseTool(tool, {})).behavior!=='deny')throw Error('ambient authority');
     const hook=await options.hooks.PreToolUse[0].hooks[0]({tool_name:tool});
     if(hook.hookSpecificOutput?.permissionDecision!=='deny')throw Error('ambient hook');
   }
   const first=await prompt[Symbol.asyncIterator]().next();
   push({type:'system',subtype:'init',session_id:session,model:'fixture-model'});
   accept(first.value);
 })().catch(()=>{closed=true;wake?.()});
 return {
   async streamInput(input){for await(const message of input)accept(message)},
   async interrupt(){if(held){push(result(held));held=null}},
   close(){closed=true;wake?.();wake=null},
   async *[Symbol.asyncIterator](){
     while(!closed || queue.length){if(queue.length)yield queue.shift();else await new Promise(r=>wake=r)}
   }
 };
}`

// Copy the interpreter into this test's owned installation. Hosted Node
// prefixes can have untrusted ancestors; production pin checks still apply.
func copyNativeFixtureNode(t *testing.T, source, destination string) {
	t.Helper()
	input, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0700)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		t.Fatal(err)
	}
}

func nativeLeadFixture(t *testing.T) (*ClaudeAdapter, LeadStartRequest, chan AdapterEvent) {
	t.Helper()
	installedNode, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("fake SDK requires Node", err)
	}
	installedNode, err = filepath.EvalSymlinks(installedNode)
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	cli := fakeVendorPath(t, "claude")
	node, sdk := claudeAdapterDependencies(t, cli)
	copyNativeFixtureNode(t, installedNode, node)
	if err := os.WriteFile(sdk, []byte(nativeSDKFixture), 0600); err != nil {
		t.Fatal(err)
	}
	adapter := NewClaudeAdapter(node, sdk, cli, map[string]string{"local": home})
	grant := NativeLeadGrant{TenantID: uidLead("01"), PrincipalID: uidLead("02"), OwnerID: uidLead("03"), ProjectID: uidLead("04"), AccountID: uidLead("05"), SessionID: uidLead("06"), DaemonID: "fixture-daemon", Generation: "fixture-generation", PolicyDigest: strings.Repeat("a", 64), AccountHomeDigest: nativeDigest(home), WorkspaceDigest: nativeDigest(workspace), ResumeDigest: nativeDigest(uidLead("37")), Revision: 1, ExpiresAt: time.Now().Add(time.Minute), HistoryApproved: true}
	events := make(chan AdapterEvent, 128)
	var mu sync.Mutex
	attempts := map[string]string{}
	spec := &NativeLeadSpec{Grant: grant, Resume: uidLead("37"), Prompt: "initial", Model: "fixture-model", Effort: "high", Require: func(context.Context) error { return nil }, Observe: func(event AdapterEvent) { events <- event }, BeforeInput: func(_ context.Context, id, digest string) error {
		mu.Lock()
		defer mu.Unlock()
		if before := attempts[id]; before != "" {
			if before != digest {
				return ErrReplay
			}
			return ErrControlUnconfirmed
		}
		attempts[id] = digest
		return nil
	}}
	return adapter, LeadStartRequest{Native: spec, AccountKey: "local", Workspace: workspace, Lifetime: t.Context(), Session: HarnessSession{ID: grant.SessionID, ProjectID: grant.ProjectID, Harness: Claude, Lease: strings.Repeat("l", 32)}}, events
}

func waitNativeEvent(t *testing.T, events <-chan AdapterEvent, match func(AdapterEvent) bool) AdapterEvent {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	for {
		select {
		case event := <-events:
			if match(event) {
				return event
			}
		case <-ctx.Done():
			t.Fatal("fake SDK event did not arrive")
		}
	}
}

// Risk: a trusted development host masks CI failures caused by an untrusted
// Node installation prefix or UTC's distinct time.Local/time.UTC identities.
// Re-run the four affected behaviors under those host conditions, keeping
// production dependency validation and every original assertion intact.
func TestNativeFixturesSurviveUntrustedNodePrefixAndUTCTimezone(t *testing.T) {
	installedNode, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	prefix, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(prefix, 0777); err != nil {
		t.Fatal(err)
	}
	ambientNode := filepath.Join(prefix, "node")
	copyNativeFixtureNode(t, installedNode, ambientNode)
	_, sdk := claudeAdapterDependencies(t, fakeVendorPath(t, "claude"))
	_, err = agentsetup.ResolveClaudeRuntime(agentsetup.ClaudeDependencies{NodePath: ambientNode, SDKPath: sdk}, "")
	if !errors.Is(err, agentsetup.ErrUnsafePath) || !strings.Contains(err.Error(), "world-writable") {
		t.Fatal("untrusted ambient Node refused for wrong reason", err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	tests := []string{"TestNativeClaudeCoordinatorStreamsExactInputAndConfirmsExit", "TestNativeCoordinatorGrantChangesRefuseBeforeInput", "TestNativeLeadSchemaMigrationPreservesEvidenceAndRefusesUnknownVersion", "TestNativeManagedStartWaitsForExitAndRelaysFinalWithReceipts"}
	command := exec.CommandContext(ctx, executable, "-test.v", "-test.run=^("+strings.Join(tests, "|")+")$")
	command.Env = append(os.Environ(), "TZ=UTC", "PATH="+prefix+string(os.PathListSeparator)+os.Getenv("PATH"))
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("CI host conditions broke native behavior: %v\n%s", err, output)
	}
	for _, name := range tests {
		if !strings.Contains(string(output), "--- PASS: "+name+" (") {
			t.Fatal("CI host regression did not execute the expected behavior", name)
		}
	}
}

// Risk: native profile bypasses confined tools, changes account HOME, starts a
// second Query, receipts the wrong input, or calls close without observing exit.
func TestNativeClaudeCoordinatorStreamsExactInputAndConfirmsExit(t *testing.T) {
	adapter, request, events := nativeLeadFixture(t)
	process, err := adapter.StartLead(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	lead := process.(*nativeClaudeLead)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if !lead.ProcessExited() {
			_ = lead.Stop(ctx)
		}
		_ = lead.Wait()
	})
	if adapter.RoutineExecutionSupported(agentsetup.RoutineGrant{}) {
		t.Fatal("coordinator qualified ordinary routines")
	}
	if lead.cmd.Path != adapter.NodePath || lead.cmd.Args[3] != adapter.ClaudePath {
		t.Fatal("bridge changed the pinned executables")
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(lead.cmd.Env, "HOME="+userHome) || !slices.Contains(lead.cmd.Env, "CLAUDE_CONFIG_DIR="+adapter.Homes["local"]) {
		t.Fatal("native account changed login HOME or config dir")
	}
	defaultEnv, err := nativeClaudeEnvironment(filepath.Join(userHome, ".claude"), adapter.NodePath, adapter.ClaudePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range defaultEnv {
		if strings.HasPrefix(entry, "CLAUDE_CONFIG_DIR=") {
			t.Fatal("default native profile selected a different login")
		}
	}
	waitNativeEvent(t, events, func(event AdapterEvent) bool {
		return event.Chat != nil && event.Chat.SessionUpdate == "state" && event.Chat.State == "idle" && event.InputID == "initial"
	})
	if lead.ProcessExited() || adapter.LeadExitConfirmed(lead) {
		t.Fatal("turn completion released the long-lived Query")
	}
	id := uidLead("31")
	if err := lead.Input(t.Context(), id, "person message"); err != nil {
		t.Fatal(err)
	}
	final := waitNativeEvent(t, events, func(event AdapterEvent) bool { return event.Chat != nil && event.Chat.SessionUpdate == "final" })
	if final.Chat.Content.Text != "sdk:person message" || final.InputID != id {
		t.Fatal("explicit final did not name the input consumed by the same SDK Query", final.Chat, final.InputID)
	}
	waitNativeEvent(t, events, func(event AdapterEvent) bool {
		return event.InputID == id && event.Chat != nil && event.Chat.State == "idle"
	})
	if err := lead.Input(t.Context(), id, "person message"); !errors.Is(err, ErrControlUnconfirmed) {
		t.Fatal("acknowledged input was replayed", err)
	}
	if err := lead.Input(t.Context(), uidLead("32"), "hold"); err != nil {
		t.Fatal(err)
	}
	if err := lead.Control(t.Context(), "interrupt", ""); err != nil {
		t.Fatal("official void interrupt was rejected", err)
	}
	if err := lead.Control(t.Context(), "steer", "ambient"); !errors.Is(err, ErrUnsupported) {
		t.Fatal("native profile acquired unqualified steering", err)
	}
	stopCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := lead.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	if err := lead.Wait(); err != nil {
		t.Fatal(err)
	}
	if !adapter.LeadExitConfirmed(lead) {
		t.Fatal("owned group exit not confirmed")
	}
	if _, err := adapter.StartLead(t.Context(), LeadStartRequest{}); !errors.Is(err, ErrNativeUnqualified) {
		t.Fatal("ordinary lead label granted native authority", err)
	}
	// Negative control in an isolated fixture copy: restore the pre-change
	// persistSession:false behavior. The same SDK fidelity guard must reject
	// startup. No throwaway commit, real account or other worktree is used.
	bridge, err := claudeAssets.ReadFile("claudeassets/bridge.mjs")
	if err != nil {
		t.Fatal(err)
	}
	oldBridge := strings.Replace(string(bridge), "persistSession: native,", "persistSession: false,", 1)
	if oldBridge == string(bridge) {
		t.Fatal("negative control did not remove native history")
	}
	copyPath := filepath.Join(t.TempDir(), "old-bridge.mjs")
	if err := os.WriteFile(copyPath, []byte(oldBridge), 0600); err != nil {
		t.Fatal(err)
	}
	frame, err := json.Marshal(map[string]any{"op": "start", "profile": "native", "native_grant": request.Native.bridgeGrant(), "prompt": "initial", "model": "fixture-model", "effort": "high", "resume": request.Native.Resume, "capabilities": []string{"interrupt"}})
	if err != nil {
		t.Fatal(err)
	}
	negativeCtx, cancelNegative := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelNegative()
	command := exec.CommandContext(negativeCtx, adapter.NodePath, copyPath, adapter.SDKPath, adapter.ClaudePath, request.Workspace)
	command.Env, err = nativeClaudeEnvironment(adapter.Homes["local"], adapter.NodePath, adapter.ClaudePath)
	if err != nil {
		t.Fatal(err)
	}
	command.Stdin = strings.NewReader(string(frame) + "\n")
	output, err := command.Output()
	if negativeCtx.Err() != nil || err == nil || !strings.Contains(string(output), `"reason":"sdk_query_capability_missing"`) || strings.Contains(string(output), `"kind":"session_started"`) {
		t.Fatal("native history regression did not fail for the expected SDK fidelity reason", err, string(output))
	}
}

// Risk: revocation/profile/workspace changes after authorization can still
// submit input. The action must reject before recording/writing an attempt.
func TestNativeCoordinatorGrantChangesRefuseBeforeInput(t *testing.T) {
	adapter, request, _ := nativeLeadFixture(t)
	for _, change := range []string{"history", "workspace", "resume", "tools", "expired"} {
		t.Run(change, func(t *testing.T) {
			copy := *request.Native
			changed := request
			changed.Native = &copy
			switch change {
			case "history":
				copy.Grant.HistoryApproved = false
			case "workspace":
				copy.Grant.WorkspaceDigest = strings.Repeat("b", 64)
			case "resume":
				copy.Grant.ResumeDigest = strings.Repeat("b", 64)
			case "tools":
				copy.Grant.AllowedTools = []string{"Bash"}
			case "expired":
				copy.Grant.ExpiresAt = time.Now().Add(-time.Hour)
			}
			process, err := adapter.StartLead(t.Context(), changed)
			expected := ErrNativeUnqualified
			if change == "workspace" || change == "resume" {
				expected = ErrScope
			}
			if process != nil || !errors.Is(err, expected) {
				t.Fatal("changed authority launched", process, err)
			}
		})
	}
	var revoked atomic.Bool
	var attempts atomic.Int32
	request.Native.Require = func(context.Context) error {
		if revoked.Load() {
			return ErrScope
		}
		return nil
	}
	request.Native.BeforeInput = func(context.Context, string, string) error { attempts.Add(1); return nil }
	process, err := adapter.StartLead(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	lead := process.(*nativeClaudeLead)
	revoked.Store(true)
	if err := lead.Input(t.Context(), uidLead("31"), "after revocation"); !errors.Is(err, ErrScope) || attempts.Load() != 0 {
		t.Fatal("revoked input reached the attempt boundary", err, attempts.Load())
	}
	revoked.Store(false)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := lead.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	_ = lead.Wait()
}

// Risk: native controls restart the transport TTL, trust a skewed server clock,
// or record/write a control that expired in transit, in a queue or in revalidation.
// synctest supplies virtual time; the attempt port stops before any SDK write.
func TestNativeControlsPreserveMonotonicAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		transit, authorityWait time.Duration
		wallSkew               time.Duration
		ttl                    int64
		missing, queued        bool
		want                   error
		attempts, completions  int
	}{
		{name: "live_transit", transit: 2 * time.Second, wallSkew: time.Hour, ttl: 10000, attempts: 1, completions: 1},
		{name: "server_behind", wallSkew: -time.Hour, ttl: 10000, attempts: 1, completions: 1},
		{name: "server_ahead", wallSkew: time.Hour, ttl: 10000, attempts: 1, completions: 1},
		{name: "expired_transit", transit: 11 * time.Second, wallSkew: time.Hour, ttl: 10000, want: ErrControlExpired},
		{name: "queued", wallSkew: time.Hour, ttl: 10000, queued: true, want: ErrControlExpired, attempts: 1, completions: 1},
		{name: "expired_revalidation", ttl: 10000, authorityWait: 11 * time.Second, want: ErrControlExpired},
		{name: "missing_deadline", ttl: 10000, missing: true, want: ErrScope},
		{name: "oversized_ttl", ttl: 45001, want: ErrScope},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				started := time.Now()
				deadline := started.Add(time.Duration(tc.ttl) * time.Millisecond)
				expires := started.Add(10*time.Second + tc.wallSkew)
				control := HarnessControl{ID: uidLead("71"), Kind: "interrupt", ExpectedGeneration: uidLead("06"), ExpiresAt: &expires, ExpiresInMS: tc.ttl, deadline: deadline}
				if tc.missing {
					control.deadline = time.Time{}
				}
				controls := []HarnessControl{control}
				if tc.queued {
					second := control
					second.ID, second.Kind = uidLead("72"), "stop"
					controls = append(controls, second)
				}
				attempts, completions, revalidations := 0, 0, 0
				spec := &NativeLeadSpec{
					Grant:   NativeLeadGrant{TenantID: uidLead("01"), PrincipalID: uidLead("02"), OwnerID: uidLead("03"), ProjectID: uidLead("04"), AccountID: uidLead("05"), SessionID: uidLead("06"), DaemonID: "fixture-daemon", Generation: "fixture-generation", PolicyDigest: strings.Repeat("a", 64), AccountHomeDigest: strings.Repeat("a", 64), WorkspaceDigest: strings.Repeat("a", 64), Revision: 1, ExpiresAt: started.Add(time.Hour), HistoryApproved: true},
					Observe: func(AdapterEvent) {},
					Require: func(context.Context) error {
						revalidations++
						if revalidations > 1 {
							time.Sleep(tc.authorityWait) // Virtual authorization lookup.
						}
						return nil
					},
					BeforeInput: func(ctx context.Context, id, digest string) error {
						attempts++
						if got, ok := ctx.Deadline(); !ok || got != deadline || ctx.Err() != nil || id != control.ID || digest != nativeDigest("interrupt") {
							t.Error("native attempt lost the original live authorization deadline", got, ctx.Err(), id)
						}
						if tc.queued {
							time.Sleep(11 * time.Second) // First control consumes the queued budget.
						}
						return ErrControlUnconfirmed // No SDK operation in this fixture.
					},
				}
				lead := &nativeClaudeLead{claudeProcess: &claudeProcess{native: spec}, controls: map[string]nativeControlResult{}, io: LeadSessionIO{
					Yield: func(context.Context) ([]HarnessControl, error) {
						time.Sleep(tc.transit) // Virtual transport/body-decoding delay.
						return controls, nil
					},
					CompleteControl: func(_ context.Context, id, outcome, reason string) error {
						completions++
						if id != control.ID || outcome != "rejected" || reason != "native_control_unconfirmed" {
							t.Error("unexpected native receipt", id, outcome, reason)
						}
						return nil
					},
					Drain:            func(context.Context) ([]HarnessDelivery, error) { return nil, nil },
					CompleteDelivery: func(context.Context, HarnessDelivery) error { return nil },
				}}
				err := (&ClaudeAdapter{}).ServiceLead(t.Context(), lead, nil)
				if !errors.Is(err, tc.want) || attempts != tc.attempts || completions != tc.completions || len(lead.controls) != tc.attempts {
					t.Fatal("native control extended authorization or recorded an expired attempt", err, attempts, completions, len(lead.controls))
				}
				if tc.queued {
					if _, recorded := lead.controls[controls[1].ID]; recorded {
						t.Fatal("queued expired stop was recorded")
					}
				}
			})
		})
	}
}

// Risk: accepting disappearance, a reused PID, or unreadable metadata can
// resume while the original conversation writer is still unconfirmed.
func TestNativeAfterExitPinsIdentityAndWaitsForObservedExit(t *testing.T) {
	before := attachObservation{Process: attachwatch.Process{PID: 99, UID: os.Geteuid(), Started: "original"}}
	tick := make(chan time.Time)
	observed := make(chan struct{})
	var calls atomic.Int32
	exited := atomic.Bool{}
	done := make(chan error, 1)
	go func() {
		done <- waitNativeExit(t.Context(), before, func(int) (attachObservation, error) {
			if calls.Add(1) == 1 {
				close(observed)
			}
			if exited.Load() {
				return attachObservation{}, errAttachExited
			}
			return before, nil
		}, func() <-chan time.Time { return tick })
	}()
	<-observed
	select {
	case err := <-done:
		t.Fatal("resumed before exit", err)
	default:
	}
	exited.Store(true)
	tick <- time.Now()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"reused", "unreadable"} {
		observe := func(int) (attachObservation, error) {
			if state == "unreadable" {
				return attachObservation{}, errors.New("metadata unavailable")
			}
			copy := before
			copy.Started = "replacement"
			return copy, nil
		}
		if err := waitNativeExit(t.Context(), before, observe, func() <-chan time.Time { t.Fatal("uncertain identity should not wait"); return nil }); !errors.Is(err, ErrProcessesUnconfirmed) {
			t.Fatal("uncertain identity became exit proof", state, err)
		}
	}
}

// Risk: upgrading persisted local records invents native authority or permits
// a downgrade to forget retained native writers. The old envelope stays pinned.
func TestNativeLeadSchemaMigrationPreservesEvidenceAndRefusesUnknownVersion(t *testing.T) {
	old := leadRecord{Version: 1, Attempted: true, Ownership: &ownedprocess.Identity{ProcessID: "old-owned", RootPID: 99, GroupID: 99, StartedAt: time.Unix(100, 0).UTC()}, Pickup: agentsetup.LeadPickup{Demand: agentsetup.LeadDemand{ID: uidLead("01")}}}
	raw, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	migrated, err := decodeLeadRecord(raw)
	if err != nil || migrated.Version != 2 || migrated.Native != nil || !migrated.Attempted || *migrated.Ownership != *old.Ownership {
		t.Fatal("migration changed ownership or granted native authority", migrated, err)
	}
	migrated.Native = &nativeLeadRecord{SessionID: uidLead("02")}
	migrated.Version = 1
	raw, _ = json.Marshal(migrated)
	if _, err := decodeLeadRecord(raw); !errors.Is(err, ErrProcessesUnconfirmed) {
		t.Fatal("schema 1 smuggled native authority", err)
	}
	migrated.Version = 3
	raw, _ = json.Marshal(migrated)
	_, err = decodeLeadRecord(raw)
	var schema *localjournal.SchemaVersionError
	if !errors.As(err, &schema) || schema.Stored != 3 || schema.Supported != 2 {
		t.Fatal("incompatible version refused for wrong reason", err)
	}
	s, _, _ := testSupervisor(t)
	migrated.Version = 2
	migrated.Native.Attempts = map[string]string{uidLead("03"): strings.Repeat("a", 64)}
	if err := s.writeLead(migrated, true); err != nil {
		t.Fatal(err)
	}
	raw, err = s.state.Read(nativeLeadStateName(old.Pickup.Demand.ID), 32768)
	if err != nil {
		t.Fatal(err)
	}
	// The released v1 fence rejects any version other than 1, before dispatch.
	var legacy struct {
		Version int `json:"version"`
	}
	if json.Unmarshal(raw, &legacy) != nil || legacy.Version == 1 {
		t.Fatal("downgrade could open the new native record")
	}
	if err := checkRetainedLeads(s.state); !errors.Is(err, ErrProcessesUnconfirmed) {
		t.Fatal("restart adopted a retained native PID", err)
	}
}

// Risk: a root coordinator is registered as an unmanaged hook or invented leaf
// run, splitting its PAIMOS ownership from the native conversation.
func TestNativeCoordinatorRegistrationUsesManagedRootContract(t *testing.T) {
	_, request, _ := nativeLeadFixture(t)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/api/projects/"+request.Session.ProjectID+"/harness-sessions" {
			t.Error("unexpected registration route", r.Method, r.URL.Path)
		}
		var body map[string]json.RawMessage
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid registration JSON")
		}
		for _, key := range []string{"run_id", "ticket_node_id", "work_order_id", "attached_hook", "vendor_session_ref"} {
			if _, ok := body[key]; ok {
				t.Error("root acquired foreign authority", key)
			}
		}
		if string(body["role"]) != `"coordinator"` || string(body["management_mode"]) != `"managed"` {
			t.Error("root is not a managed coordinator", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id": request.Session.ID, "project_id": request.Session.ProjectID})
	}))
	defer server.Close()
	remote := NewRemote(server.URL, "fixture-scoped-key")
	plan := NativeLeadPlan{Spec: *request.Native, Session: request.Session, AccountKey: request.AccountKey, Workspace: request.Workspace, Host: "fixture"}
	plan.Spec.BeforeInput, plan.Spec.Observe = nil, nil // Process-only ports are bound after registration.
	result, err := remote.RegisterNativeCoordinator(t.Context(), plan)
	if err != nil || result.ID != request.Session.ID || result.Lease != request.Session.Lease || result.AttachedHook || calls != 1 {
		t.Fatal("managed registration binding changed", result, err, calls)
	}
}
