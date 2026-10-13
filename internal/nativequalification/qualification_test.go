// SPDX-License-Identifier: AGPL-3.0-only

package nativequalification

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativesessions/store"
)

//go:embed testdata/sdk.mjs
var sdkFixture []byte

// Intentionally pinned independently of the installed bytes. A fixture change
// must update this pin deliberately; it does not identify a production SDK.
const sdkDigest = "5f30ad89edab9dacbeafe793d8b967f7cf7df8d5b45056a5348f93c745b916e3"
const sdkProtocol = "aeon-lead-sdk-fixture/1"

func uid(n string) string       { return "10000000-0000-4000-8000-0000000000" + n }
func digest(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }
func ptr[T any](v T) *T         { return &v }

func directory(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	return root
}

func write(t *testing.T, name string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(name, data, mode); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T) (Candidate, nativecontract.NativeIdentity, string) {
	t.Helper()
	install, workspace, home, userHome, resource := directory(t), directory(t), directory(t), directory(t), directory(t)
	installedNode, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("offline SDK qualification requires Node")
	}
	installedNode, err = filepath.EvalSymlinks(installedNode)
	if err != nil {
		t.Fatal(err)
	}
	node := filepath.Join(install, "node")
	src, err := os.Open(installedNode)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := os.OpenFile(node, os.O_CREATE|os.O_WRONLY, 0700)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(dst, src)
	if err := errors.Join(copyErr, dst.Close()); err != nil {
		t.Fatal(err)
	}
	pkg := filepath.Join(install, "lib", "node_modules", "@anthropic-ai", "claude-agent-sdk")
	if err := os.MkdirAll(pkg, 0700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(pkg, "package.json"), []byte(`{"name":"@anthropic-ai/claude-agent-sdk","main":"sdk.mjs","version":"1.2.3"}`), 0600)
	sdk := filepath.Join(pkg, "sdk.mjs")
	write(t, sdk, sdkFixture, 0600)
	cli := filepath.Join(install, "fake-vendor")
	cliBytes := []byte("#!/bin/sh\nif [ \"$1\" = auth ] && [ \"$2\" = status ]; then printf '%s\\n' '{\"loggedIn\":true,\"email\":\"qualification@example.test\",\"authMethod\":\"claude.ai\"}'; exit 0; fi\nexit 92\n")
	write(t, cli, cliBytes, 0700)
	now := time.Now().UTC()
	binding := nativecontract.Binding{TenantID: uid("01"), ProjectID: uid("02"), OwnedSessionID: uid("03"), HarnessSessionID: uid("04"), RuntimeEpoch: uid("05"), DaemonID: "fixture-daemon", DaemonGeneration: "fixture-generation", BindingRevision: 1}
	policy := nativecontract.Policy{GrantID: uid("06"), GrantRevision: 1, ExpiresAt: now.Add(time.Hour), ToolPolicyDigest: strings.Repeat("a", 64), SettingsPolicyDigest: strings.Repeat("b", 64), PermissionPolicy: "bounded_preapproved", LocalHistory: "vendor_native", Continuation: "manual", MaximumRuntimeSeconds: 3600, MaximumTurns: 128, MaximumTokens: 100000}
	actor := nativecontract.Actor{TenantID: binding.TenantID, PrincipalID: uid("07"), OwnerPersonID: uid("08"), PrincipalKind: "agent", GrantID: policy.GrantID, GrantRevision: policy.GrantRevision}
	c := Candidate{Spec: nativecontract.LaunchSpec{Binding: binding, Policy: policy, Cwd: workspace, ResumeRef: uid("09"), Mode: "structured", OwnedWriterID: "fixture-writer", Lifetime: t.Context(),
		Profile: nativecontract.LocalProfile{AccountID: uid("10"), AccountHome: home, UserHome: userHome, WorkspaceRoot: workspace, ConversationRoot: home, ProfileBindingRevision: 1,
			Binary: nativecontract.BinaryPin{Path: cli, SHA256: digest(cliBytes), Version: "fixture-vendor/1", Protocol: sdkProtocol}, SDK: nativecontract.BinaryPin{Path: sdk, SHA256: sdkDigest, Version: "1.2.3", Protocol: sdkProtocol}},
		Options: nativecontract.LaunchConfig{ModelProfileID: uid("11"), Model: "fixture-model", Effort: ptr("high"), ServiceTier: "default", ToolPolicyDigest: policy.ToolPolicyDigest, SettingsPolicyDigest: policy.SettingsPolicyDigest},
		Tools:   nativecontract.ToolCapability{URL: "http://127.0.0.1:49152", Token: make([]byte, 32), Binding: binding, ExpiresAt: policy.ExpiresAt}},
		Actor: actor, AllowedTools: []string{"aeon_read"}, ResourceRoot: resource, ResourceLeaseID: uid("12"), NativeHistoryGrantID: uid("13"), Now: func() time.Time { return now },
		Require: func(_ context.Context, got nativecontract.Actor, p nativecontract.Policy) error {
			if !reflect.DeepEqual(got, actor) || p != policy {
				return ErrGrant
			}
			return nil
		},
		RequireHistory: func(_ context.Context, got nativecontract.Actor, id string) error {
			if !reflect.DeepEqual(got, actor) || id != uid("13") {
				return ErrGrant
			}
			return nil
		}}
	i := nativecontract.NativeIdentity{Provider: "claude", NativeSessionRef: c.Spec.ResumeRef, NativeThreadRef: "thread-initial", NativeTurnRef: "turn-1", ResumeCursor: "cursor-initial", RuntimeEpoch: binding.RuntimeEpoch, ProcessIdentityDigest: strings.Repeat("c", 64), PID: 1234, StartedAt: now.Add(-time.Minute)}
	return c, i, node
}

func waitEvent(t *testing.T, events <-chan agentd.AdapterEvent, match func(agentd.AdapterEvent) bool) agentd.AdapterEvent {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	for {
		select {
		case e := <-events:
			if match(e) {
				return e
			}
		case <-ctx.Done():
			t.Fatal("offline SDK did not emit required evidence")
		}
	}
}

// Risk: native persistence imports ambient hooks/tools/credentials or expands
// confined/routine authority. The real P1a bridge runs only a pinned offline SDK.
func TestNativeLeadPinnedSDKPolicyBoundary(t *testing.T) {
	c, identity, node := fixture(t)
	if digest(sdkFixture) != sdkDigest {
		t.Fatal("offline SDK fixture pin changed")
	}
	if _, err := New(t.Context(), c, identity); err != nil {
		t.Fatal(err)
	}
	overlay, err := SessionOverlay(c.AllowedTools)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(overlay.DisabledLegacyHooks, []string{"attached-inbox", "cswap", "duplicate-subagent-registration"}) || len(overlay.SettingSources) != 0 {
		t.Fatal("legacy hooks inherited")
	}
	var settings struct {
		Hooks map[string][]any `json:"hooks"`
	}
	if json.Unmarshal(overlay.Settings, &settings) != nil || len(settings.Hooks) != 7 {
		t.Fatal("legacy session overlay missing")
	}
	for _, bucket := range settings.Hooks {
		if len(bucket) != 0 {
			t.Fatal("legacy hook command retained")
		}
	}
	for _, name := range []string{"AEON_API_KEY", "ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "OPS_TEST_SECRET"} {
		t.Setenv(name, "synthetic-qualification-sentinel")
	}
	events := make(chan agentd.AdapterEvent, 128)
	grant := agentd.NativeLeadGrant{TenantID: c.Actor.TenantID, PrincipalID: c.Actor.PrincipalID, OwnerID: c.Actor.OwnerPersonID, ProjectID: c.Spec.Binding.ProjectID, AccountID: c.Spec.Profile.AccountID, SessionID: c.Spec.Binding.HarnessSessionID, DaemonID: c.Spec.Binding.DaemonID, Generation: c.Spec.Binding.DaemonGeneration, PolicyDigest: c.Spec.Policy.ToolPolicyDigest, AccountHomeDigest: digest([]byte(c.Spec.Profile.AccountHome)), WorkspaceDigest: digest([]byte(c.Spec.Cwd)), ResumeDigest: digest([]byte(c.Spec.ResumeRef)), Revision: 1, ExpiresAt: c.Spec.Policy.ExpiresAt, HistoryApproved: true, AllowedTools: c.AllowedTools}
	var revoked atomic.Bool
	var attempts atomic.Int32
	spec := &agentd.NativeLeadSpec{Grant: grant, Resume: c.Spec.ResumeRef, Prompt: "initial", Model: "fixture-model", Effort: "high", Tools: &agentd.RunTools{URL: c.Spec.Tools.URL, Token: strings.Repeat("0", 64)}, Now: c.Now,
		Require: func(context.Context) error {
			if revoked.Load() {
				return ErrGrant
			}
			return nil
		},
		BeforeInput: func(context.Context, string, string) error { attempts.Add(1); return nil }, Observe: func(e agentd.AdapterEvent) { events <- e }}
	adapter := agentd.NewClaudeAdapter(node, c.Spec.Profile.SDK.Path, c.Spec.Profile.Binary.Path, map[string]string{"fixture": c.Spec.Profile.AccountHome})
	adapter.SetExpectedEmails(map[string]string{"fixture": "qualification@example.test"})
	request := agentd.LeadStartRequest{Native: spec, AccountKey: "fixture", Workspace: c.Spec.Cwd, Lifetime: t.Context(), Session: agentd.HarnessSession{ID: grant.SessionID, ProjectID: grant.ProjectID, Harness: agentd.Claude, Lease: strings.Repeat("l", 32)}}
	process, err := adapter.StartLead(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		revoked.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := process.Stop(ctx); err != nil {
			t.Error("offline child cleanup", err)
		}
		if err := process.Wait(); err != nil {
			t.Error("offline child wait", err)
		}
	})
	final := waitEvent(t, events, func(e agentd.AdapterEvent) bool { return e.Chat != nil && e.Chat.SessionUpdate == "final" })
	if final.Chat.Content == nil || final.Chat.Content.Text != "Explicit SDK final." {
		t.Fatal("partial output became final")
	}
	boundary, err := os.ReadFile(filepath.Join(c.Spec.Cwd, "boundary.json"))
	if err != nil {
		t.Fatal(err)
	}
	var facts map[string]any
	if json.Unmarshal(boundary, &facts) != nil {
		t.Fatal("bad fixture facts")
	}
	if facts["protocol"] != sdkProtocol {
		t.Fatal("fixture protocol pin changed")
	}
	for _, fact := range []string{"legacyHooksAbsent", "ambientToolsDenied", "boundToolAllowed", "daemonCredentialAbsent", "persistentNativeOnly", "accountHomePreserved"} {
		if facts[fact] != true {
			t.Fatal("SDK boundary failed", fact)
		}
	}
	if adapter.RoutineExecutionSupported(agentsetup.RoutineGrant{}) {
		t.Fatal("native qualification widened routines")
	}
	inputProcess, ok := process.(interface {
		Input(context.Context, string, string) error
	})
	if !ok {
		t.Fatal("native input port unavailable")
	}
	revoked.Store(true)
	if err := inputProcess.Input(t.Context(), uid("20"), "revoked input"); !errors.Is(err, ErrGrant) {
		t.Fatal("wrong revoked-input reason", err)
	}
	if attempts.Load() != 0 {
		t.Fatal("revoked input reached write-attempt boundary")
	}
	revoked.Store(false)
	if err := process.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !adapter.LeadExitConfirmed(process) {
		t.Fatal("SDK close mistaken for owned exit")
	}
}

// Risk: idle eviction drops background work, compaction loses exact resume
// identity, or a final is credited to an input that was never consumed.
func TestNativeLeadBackgroundCompactionAndConsumedFinal(t *testing.T) {
	c, identity, node := fixture(t)
	r, err := New(t.Context(), c, identity)
	if err != nil {
		t.Fatal(err)
	}
	first := nativecontract.Input{RequestID: uid("21"), InputID: uid("22"), ExpectedBinding: c.Spec.Binding, Mode: "now", Text: "first prompt remains outside evidence", ExpiresAt: c.Spec.Policy.ExpiresAt}
	second := first
	second.RequestID = uid("23")
	second.InputID = uid("24")
	second.Text = "second prompt remains outside evidence"
	if err := r.Queue(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, node, c.Spec.Profile.SDK.Path, "qualification-trace")
	command.Env = []string{"HOME=" + c.Spec.Profile.UserHome}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(io.LimitReader(stdout, (16<<10)+1))
	if readErr != nil || len(data) > 16<<10 {
		cancel()
	}
	if err := command.Wait(); err != nil {
		t.Fatal("offline trace failed", err)
	}
	if readErr != nil || len(data) > 16<<10 {
		t.Fatal("offline trace exceeded read bound")
	}
	var trace []struct{ Kind, Input, Turn, Text, Task, State, Thread, Cursor string }
	if json.Unmarshal(data, &trace) != nil || len(trace) != 8 {
		t.Fatal("pinned SDK trace changed")
	}
	for index, step := range trace {
		if index == 6 {
			if err := r.Queue(t.Context(), second); err != nil {
				t.Fatal(err)
			}
		}
		event := nativecontract.ProviderEvent{SourceSequence: uint64(index + 1), Identity: identity}
		input := first
		if step.Input == "second" {
			input = second
		}
		if step.Turn != "" {
			identity.NativeTurnRef = step.Turn
			event.Identity = identity
		}
		switch step.Kind {
		case "consumed":
			event.Receipt = &nativecontract.Receipt{RequestID: input.RequestID, Binding: &c.Spec.Binding, Operation: "input", State: "turn_started", InputID: &input.InputID, TurnRef: ptr(step.Turn)}
		case "background_task":
			event.BackgroundWork = step.State != "completed"
			event.Structured = &nativecontract.StructuredFrame{Kind: "background_task", Reference: ptr(step.Task), State: ptr(step.State)}
		case "compaction":
			identity.NativeThreadRef = step.Thread
			identity.ResumeCursor = step.Cursor
			event.Identity = identity
			event.Structured = &nativecontract.StructuredFrame{Kind: "turn_state", Reference: ptr(step.Cursor), State: ptr("compacted")}
		case "final":
			event.Final = &nativecontract.FinalPublish{RequestID: uid("25"), ExpectedBinding: c.Spec.Binding, FinalID: uid("26"), InputID: input.InputID, TurnRef: step.Turn, Text: step.Text, DrainGrantID: uid("27")}
			wrong := *event.Final
			wrong.InputID = uid("99")
			bad := event
			bad.Final = &wrong
			if _, err := r.Observe(t.Context(), bad); !errors.Is(err, ErrEvidence) {
				t.Fatal("unconsumed final refused for wrong reason", err)
			}
		}
		final, err := r.Observe(t.Context(), event)
		if err != nil {
			t.Fatal("qualified trace refused", index, err)
		}
		if step.Kind == "final" && (final == nil || final.InputID != input.InputID || final.TurnRef != step.Turn || final.Text != step.Text) {
			t.Fatal("final correlation lost")
		}
		snapshot := r.Snapshot()
		if index >= 1 && index <= 4 && (snapshot.Quiescent || snapshot.BackgroundTasks != 1) {
			t.Fatal("background task unpinned at foreground completion or compaction")
		}
		if index == 3 && (snapshot.Identity.NativeSessionRef != c.Spec.ResumeRef || snapshot.Identity.ResumeCursor != "cursor-compacted" || snapshot.Identity.NativeThreadRef != "thread-compacted") {
			t.Fatal("compaction changed conversation or lost native references")
		}
	}
	if !r.Snapshot().Quiescent {
		t.Fatal("completed trace did not settle")
	}
	// Quiescence does not supply root/group exit evidence for a successor.
	next := c
	next.Spec.Binding.RuntimeEpoch = uid("30")
	next.Spec.Binding.HarnessSessionID = uid("31")
	next.Spec.Tools.Binding = next.Spec.Binding
	previous := nativecontract.WriterRecord{Binding: c.Spec.Binding, Identity: identity, PossibleFork: true}
	if err := CheckSuccessor(t.Context(), next, previous, nativecontract.ExitEvidence{}); !errors.Is(err, ErrExit) {
		t.Fatal("idle mistaken for exit", err)
	}
	exit := nativecontract.ExitEvidence{RootExited: true, GroupAbsent: true, ProcessIdentityDigest: identity.ProcessIdentityDigest, ObservedAt: c.Now()}
	if err := CheckSuccessor(t.Context(), next, previous, exit); err != nil {
		t.Fatal(err)
	}
	exit.ProcessIdentityDigest = strings.Repeat("d", 64)
	if err := CheckSuccessor(t.Context(), next, previous, exit); !errors.Is(err, ErrExit) {
		t.Fatal("reused process identity accepted", err)
	}
}

// Risk: approval/deadline changes during an authority read, symlink escapes,
// altered SDK bytes, and wrong resource leases silently qualify an effect.
func TestNativeLeadFencesDeadlinesAndRevocationBarrier(t *testing.T) {
	c, identity, _ := fixture(t)
	r, err := New(t.Context(), c, identity)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.CheckTool(t.Context(), "mcp__aeon__aeon_read", c.Spec.Cwd, ""); err != nil {
		t.Fatal(err)
	}
	if err := r.CheckTool(t.Context(), "mcp__aeon__aeon_read", c.ResourceRoot, c.ResourceLeaseID); err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		name, path, lease string
		want              error
	}{
		{"Bash", c.Spec.Cwd, "", ErrPolicy}, {"mcp__foreign__aeon_read", c.Spec.Cwd, "", ErrPolicy},
		{"mcp__aeon__aeon_read", c.Spec.Profile.AccountHome, "", ErrFence},
		{"mcp__aeon__aeon_read", c.ResourceRoot, "wrong-lease", ErrFence},
		{"mcp__aeon__aeon_read", c.ResourceRoot, "", ErrFence},
	} {
		if err := r.CheckTool(t.Context(), check.name, check.path, check.lease); !errors.Is(err, check.want) {
			t.Fatal("wrong tool-fence reason", err)
		}
	}
	link := filepath.Join(c.Spec.Cwd, "escape")
	if err := os.Symlink(c.ResourceRoot, link); err != nil {
		t.Fatal(err)
	}
	if err := r.CheckTool(t.Context(), "mcp__aeon__aeon_read", link, ""); !errors.Is(err, ErrFence) {
		t.Fatal("symlink escape accepted", err)
	}
	noHistory := c
	noHistory.RequireHistory = nil
	if _, err := New(t.Context(), noHistory, identity); !errors.Is(err, ErrPolicy) {
		t.Fatal("history consent inferred", err)
	}
	deniedHistory := c
	deniedHistory.RequireHistory = func(context.Context, nativecontract.Actor, string) error { return ErrGrant }
	if _, err := New(t.Context(), deniedHistory, identity); !errors.Is(err, ErrGrant) {
		t.Fatal("separate history grant refused for wrong reason", err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var revoked atomic.Bool
	barrier := c
	barrier.Require = func(ctx context.Context, _ nativecontract.Actor, _ nativecontract.Policy) error {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		if revoked.Load() {
			return ErrGrant
		}
		return nil
	}
	r.candidate = barrier
	result := make(chan error, 1)
	go func() {
		result <- r.Queue(t.Context(), nativecontract.Input{RequestID: uid("40"), InputID: uid("41"), ExpectedBinding: c.Spec.Binding, Mode: "now", Text: "retained only by the caller", ExpiresAt: c.Spec.Policy.ExpiresAt})
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("authority barrier never entered")
	}
	revoked.Store(true)
	close(release)
	select {
	case err := <-result:
		if !errors.Is(err, ErrGrant) {
			t.Fatal("revocation refused for wrong reason", err)
		}
	case <-ctx.Done():
		t.Fatal("authority barrier never finished")
	}
	if r.Snapshot().PendingInputs != 0 {
		t.Fatal("revoked input retained as accepted")
	}
	clock := c.Now()
	expired := c
	expired.Now = func() time.Time { return clock }
	expired.Require = func(context.Context, nativecontract.Actor, nativecontract.Policy) error {
		clock = c.Spec.Policy.ExpiresAt
		return nil
	}
	r.candidate = expired
	if err := r.CheckTool(t.Context(), "mcp__aeon__aeon_read", c.Spec.Cwd, ""); !errors.Is(err, ErrDeadline) {
		t.Fatal("deadline not rechecked after authority read", err)
	}
	write(t, c.Spec.Profile.SDK.Path, append(append([]byte{}, sdkFixture...), []byte("\n// changed\n")...), 0600)
	if err := CheckPin(c.Spec.Profile.SDK); !errors.Is(err, ErrPin) {
		t.Fatal("changed SDK pin accepted", err)
	}
}

// Risk: terminal URLs become agent-visible credentials or the Z1 port mismatch
// is hidden by a successful 443 fixture. This deliberately retains P1b's fence.
func TestNativeLeadPersonOnlyViewerAndZ1Compatibility(t *testing.T) {
	url := "https://mbp.example.ts.net:8082/LEAD"
	view := nativecontract.SessionView{TerminalViewerURL: &url}
	person := nativecontract.PersonProjection(view, "person", true)
	agent := nativecontract.PersonProjection(view, "agent", true)
	if person.TerminalViewerURL == nil || *person.TerminalViewerURL != url || agent.TerminalViewerURL != nil {
		t.Fatal("person-only projection failed")
	}
	hosts, err := store.NewViewerHosts(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := hosts.Validate("https://mbp.example.ts.net/LEAD"); err != nil {
		t.Fatal("safe 443 publication refused", err)
	}
	var failure *store.Failure
	if err := hosts.Validate(url); !errors.As(err, &failure) || failure.Reason != nativecontract.ReasonInvalidInput {
		t.Fatal("Z1 mismatch was hidden or refused for wrong reason", err)
	}
	if err := hosts.Validate("https://mbp.example.ts.net/LEAD?token=synthetic"); !errors.As(err, &failure) || failure.Reason != nativecontract.ReasonInvalidInput {
		t.Fatal("credential URL admitted", err)
	}
}
