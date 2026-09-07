//go:build !paimos_test_unsupported

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/dispatchprofile"
	"github.com/inspr-at/paimos/backend/pirpc"
	"github.com/inspr-at/paimos/backend/pirpctest"
)

const piHelperTest = "TestPiFakeChildProcess"

func piTestProfile(t *testing.T) dispatchprofile.Profile {
	t.Helper()
	profile, err := dispatchprofile.Resolve("pi-anthropic-sonnet-high", dispatchprofile.CatalogVersion, AdapterPi)
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

func piTestRegistry(t *testing.T) (PiAccountRegistry, string) {
	t.Helper()
	dir := t.TempDir()
	raw, err := json.Marshal(map[string]any{
		"accounts": []map[string]string{{"key": "operator-pi", "agent_dir": dir}},
	})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := ParsePiAccountRegistry(raw)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return registry, canonical
}

func piTestAdapter(t *testing.T, mode string, extraEnv ...string) (*PiAdapter, string) {
	t.Helper()
	registry, dir := piTestRegistry(t)
	adapter := NewPiAdapter(os.Args[0])
	adapter.SetAccounts(registry)
	adapter.command = pirpctest.Command(piHelperTest, mode, extraEnv...)
	return adapter, dir
}

func piStartRequest(t *testing.T, profile dispatchprofile.Profile) StartRequest {
	t.Helper()
	store, err := openPiQueueStore(t.TempDir(), "ppm-pi-queue")
	if err != nil {
		t.Fatal(err)
	}
	return StartRequest{
		Adapter: AdapterPi, Workspace: t.TempDir(), Prompt: "work", Identity: "pi:worker", ProjectID: 957,
		AccountKey: "operator-pi", ExpectedAccountLabel: AccountPiContext, ResolvedProfile: &profile,
		queue: store, generation: uuid.NewString(),
	}
}

func TestPiAdapterStartValidatesEffectiveStateAndAcceptsPrompt(t *testing.T) {
	profile := piTestProfile(t)
	adapter, dir := piTestAdapter(t, "serve")
	var started bool
	process, err := adapter.Start(context.Background(), piStartRequest(t, profile), func(event AdapterEvent) {
		if event.Kind == EventSessionStarted {
			started = true
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = process.Stop(context.Background(), ControlRequest{CorrelationID: "pi-test-stop"}) }()
	key, label := process.(accountSelection).AccountSelection()
	if process.PID() <= 0 || !started || key != "operator-pi" || label != AccountPiContext {
		t.Fatalf("pid=%d started=%t key=%s label=%s", process.PID(), started, key, label)
	}
	if process.(*piProcess).session.FrozenConfig().AgentDir != dir {
		t.Fatal("frozen agent dir drifted from selected context")
	}
}

func TestPiAdapterInterruptClearsQueueBeforeAbortAndHoldsUnicode(t *testing.T) {
	profile := piTestProfile(t)
	trace := filepath.Join(t.TempDir(), "trace")
	adapter, _ := piTestAdapter(t, "native-queue", pirpctest.TraceEnv+"="+trace)
	process, err := adapter.Start(context.Background(), piStartRequest(t, profile), func(AdapterEvent) {})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = process.Stop(context.Background(), ControlRequest{CorrelationID: "pi-test-stop"}) }()
	wantSteer := "hold\u2028this"
	wantFollow := "then\u2029summarize"
	if _, err := process.Steer(context.Background(), ControlRequest{CorrelationID: "steer-1", Text: wantSteer}); err != nil {
		t.Fatal(err)
	}
	inbox, ok := process.(InboxProcess)
	if !ok {
		t.Fatal("pi process must implement inbox")
	}
	if _, err := inbox.Inbox(context.Background(), ControlRequest{CorrelationID: "follow-1", Text: wantFollow}); err != nil {
		t.Fatal(err)
	}
	effect, err := process.Interrupt(context.Background(), ControlRequest{CorrelationID: "pause-1"})
	if err != nil || effect.Primitive != piPausePrimitive {
		t.Fatalf("interrupt=%+v err=%v", effect, err)
	}
	held := process.(*piProcess)
	if !held.DeliveryHeld() || held.InboxReady() {
		t.Fatal("pause must hold delivery")
	}
	retained, err := held.queue.held(held.scope.Generation)
	if err != nil || retained.Outcome != piHeldOutcomeHeld ||
		len(retained.Steering) != 1 || retained.Steering[0] != wantSteer ||
		len(retained.FollowUp) != 1 || retained.FollowUp[0] != wantFollow {
		t.Fatalf("held=%+v err=%v", retained, err)
	}
	raw, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	clearAt := strings.Index(text, "cmd=clear_queue")
	abortAt := strings.Index(text, "cmd=abort")
	if clearAt < 0 || abortAt < 0 || clearAt > abortAt || strings.Contains(text, "continued=true") {
		t.Fatalf("pause order/continuation trace=%q", text)
	}
	if strings.Count(text, "cmd=clear_queue") != 1 {
		t.Fatalf("duplicate pause reissued clear_queue, trace=%q", text)
	}
	if _, err := inbox.Inbox(context.Background(), ControlRequest{CorrelationID: "blocked", Text: "nope"}); err == nil {
		t.Fatal("inbox must refuse concurrent delivery across pause")
	}
	if _, err := process.Steer(context.Background(), ControlRequest{CorrelationID: "blocked-steer", Text: "nope"}); err == nil {
		t.Fatal("steer must refuse concurrent delivery across pause")
	}
	again, err := process.Interrupt(context.Background(), ControlRequest{CorrelationID: "pause-2"})
	if err != nil || again.Primitive != piPausePrimitive {
		t.Fatalf("duplicate pause=%+v err=%v", again, err)
	}
}

func TestPiAdapterAbortOnlyWouldContinueQueuedFollowUp(t *testing.T) {
	profile := piTestProfile(t)
	trace := filepath.Join(t.TempDir(), "trace")
	adapter, _ := piTestAdapter(t, "native-queue", pirpctest.TraceEnv+"="+trace)
	process, err := adapter.Start(context.Background(), piStartRequest(t, profile), func(AdapterEvent) {})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = process.Stop(context.Background(), ControlRequest{CorrelationID: "pi-test-stop"}) }()
	if _, err := process.(*piProcess).session.FollowUp(context.Background(), "follow-direct", "keep going"); err != nil {
		t.Fatal(err)
	}
	if _, err := process.(*piProcess).session.Abort(context.Background(), "abort-only"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		raw, readErr := os.ReadFile(trace)
		if readErr == nil && strings.Contains(string(raw), "continued=true") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("abort-only native queue must continue follow-up so pause tests stay meaningful")
}

func TestPiAdapterStopAccountsForQueuedFollowUp(t *testing.T) {
	profile := piTestProfile(t)
	trace := filepath.Join(t.TempDir(), "trace")
	adapter, _ := piTestAdapter(t, "native-queue", pirpctest.TraceEnv+"="+trace)
	process, err := adapter.Start(context.Background(), piStartRequest(t, profile), func(AdapterEvent) {})
	if err != nil {
		t.Fatal(err)
	}
	want := "do not drop\u2028me"
	if _, err := process.(InboxProcess).Inbox(context.Background(), ControlRequest{CorrelationID: "queued-stop", Text: want}); err != nil {
		t.Fatal(err)
	}
	if _, err := process.Stop(context.Background(), ControlRequest{CorrelationID: "stop-queued"}); err != nil {
		t.Fatal(err)
	}
	retained, err := process.(*piProcess).queue.held(process.(*piProcess).scope.Generation)
	if err != nil || len(retained.FollowUp) == 0 || retained.FollowUp[0] != want || retained.Outcome != piHeldOutcomeTerminal {
		t.Fatalf("stop discarded queued instruction: %+v err=%v", retained, err)
	}
	raw, _ := os.ReadFile(trace)
	if !strings.Contains(string(raw), "cmd=clear_queue") || strings.Contains(string(raw), "continued=true") {
		t.Fatalf("stop must clear before kill, trace=%q", raw)
	}
}

func TestPiAdapterInboxUsesFollowUpWhileStreaming(t *testing.T) {
	profile := piTestProfile(t)
	adapter, _ := piTestAdapter(t, "serve", pirpctest.StreamingEnv+"=1")
	process, err := adapter.Start(context.Background(), piStartRequest(t, profile), func(AdapterEvent) {})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = process.Stop(context.Background(), ControlRequest{CorrelationID: "pi-test-stop"}) }()
	inbox, ok := process.(InboxProcess)
	if !ok {
		t.Fatal("pi process must implement inbox")
	}
	effect, err := inbox.Inbox(context.Background(), ControlRequest{CorrelationID: "inbox-follow", Text: "after"})
	if err != nil || effect.Primitive != piFollowUpPrimitive {
		t.Fatalf("effect=%+v err=%v", effect, err)
	}
}

func TestPiAdapterRejectsMissingAccountCodexKeyAndMissingProfile(t *testing.T) {
	adapter, _ := piTestAdapter(t, "serve")
	profile := piTestProfile(t)
	if _, err := adapter.Start(context.Background(), StartRequest{
		Adapter: AdapterPi, Workspace: t.TempDir(), Prompt: "work", Identity: "pi:worker", ProjectID: 957,
		ResolvedProfile: &profile,
	}, nil); err == nil || !strings.Contains(err.Error(), "account") {
		t.Fatalf("missing account err=%v", err)
	}
	if _, err := adapter.Start(context.Background(), StartRequest{
		Adapter: AdapterPi, Workspace: t.TempDir(), Prompt: "work", Identity: "pi:worker", ProjectID: 957,
		AccountKey: "codex-home", ResolvedProfile: &profile,
	}, nil); err == nil || !strings.Contains(err.Error(), "account") {
		t.Fatalf("codex key err=%v", err)
	}
	if _, err := adapter.Start(context.Background(), StartRequest{
		Adapter: AdapterPi, Workspace: t.TempDir(), Prompt: "work", Identity: "pi:worker", ProjectID: 957,
		AccountKey: "operator-pi",
	}, nil); err != ErrDispatchProfile {
		t.Fatalf("missing profile err=%v", err)
	}
	if _, err := adapter.Start(context.Background(), StartRequest{
		Adapter: AdapterPi, Workspace: t.TempDir(), Prompt: "work", Identity: "pi:worker", ProjectID: 957,
		AccountKey: "operator-pi", ExpectedAccountLabel: "chatgpt", ResolvedProfile: &profile,
	}, nil); err == nil || !strings.Contains(err.Error(), "account") {
		t.Fatalf("mixed Codex expected label err=%v", err)
	}
	if _, err := adapter.Start(context.Background(), StartRequest{
		Adapter: AdapterPi, Workspace: t.TempDir(), Prompt: "work", Identity: "pi:worker",
		AccountKey: "operator-pi", ResolvedProfile: &profile,
	}, nil); err == nil || !strings.Contains(err.Error(), "project") {
		t.Fatalf("missing project err=%v", err)
	}
}

func TestPiAdapterAgentSettledIsTerminalCompletion(t *testing.T) {
	profile := piTestProfile(t)
	adapter, _ := piTestAdapter(t, "serve")
	completed := make(chan struct{}, 1)
	process, err := adapter.Start(context.Background(), piStartRequest(t, profile), func(event AdapterEvent) {
		if event.Kind == EventTurnCompleted {
			completed <- struct{}{}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = process.Stop(context.Background(), ControlRequest{CorrelationID: "pi-test-stop"}) }()
	select {
	case <-completed:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("agent_settled must surface terminal completion")
	}
}

func TestPiAdapterSupervisorSteerReplayAndPauseRace(t *testing.T) {
	profile := piTestProfile(t)
	adapter, _ := piTestAdapter(t, "native-queue")
	resolver := &profileResolver{profile: profile}
	supervisor, err := NewSupervisor(SupervisorConfig{
		Instance: "ppm-pi", Adapters: []Adapter{adapter}, DispatchResolver: resolver, StateRoot: t.TempDir(),
		WorkspaceInspector: func(_ context.Context, path, mode string) (WorkspaceProvenance, error) {
			return WorkspaceProvenance{CanonicalPath: path, Identity: strings.Repeat("c", 64), Kind: WorkspaceDirectory, Mode: mode}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = supervisor.Close(context.Background()) })
	session, err := supervisor.Start(context.Background(), StartRequest{
		Adapter: AdapterPi, Workspace: t.TempDir(), Prompt: "work", Identity: "pi:worker", ProjectID: 957,
		DispatchProfileID: profile.ID, DispatchProfileVersion: profile.Version,
		AccountKey: "operator-pi", ExpectedAccountLabel: AccountPiContext,
	})
	if err != nil {
		t.Fatal(err)
	}
	if session.AccountKey != "operator-pi" || session.AccountLabel != AccountPiContext {
		t.Fatalf("session account=%+v", session)
	}
	request := ControlRequest{Instance: "ppm-pi", ProjectID: 957, Identity: "pi:worker", CorrelationID: "steer-replay", Text: "adjust"}
	first, err := supervisor.Steer(context.Background(), session.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := supervisor.Steer(context.Background(), session.ID, request)
	if err != nil || second.CorrelationID != first.CorrelationID || second.Primitive != piSteerPrimitive {
		t.Fatalf("first=%+v second=%+v err=%v", first, second, err)
	}
	pause := ControlRequest{Instance: "ppm-pi", ProjectID: 957, Identity: "pi:worker", CorrelationID: "pause-replay"}
	applied, err := supervisor.Interrupt(context.Background(), session.ID, pause)
	if err != nil || applied.Primitive != piPausePrimitive {
		t.Fatalf("pause=%+v err=%v", applied, err)
	}
	replay, err := supervisor.Interrupt(context.Background(), session.ID, pause)
	if err != nil || replay.CorrelationID != applied.CorrelationID || replay.Primitive != piPausePrimitive {
		t.Fatalf("pause replay=%+v err=%v", replay, err)
	}
	if !supervisor.DeliveryHeld(session.ID) || supervisor.InboxReady(session.ID) {
		t.Fatal("paused session still advertised ready")
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = supervisor.Interrupt(context.Background(), session.ID, ControlRequest{Instance: "ppm-pi", ProjectID: 957, Identity: "pi:worker", CorrelationID: "pause-race-a"})
	}()
	go func() {
		defer wg.Done()
		_, _ = supervisor.Interrupt(context.Background(), session.ID, ControlRequest{Instance: "ppm-pi", ProjectID: 957, Identity: "pi:worker", CorrelationID: "pause-race-b"})
	}()
	wg.Wait()
	_, _ = supervisor.Stop(context.Background(), session.ID, ControlRequest{Instance: "ppm-pi", ProjectID: 957, Identity: "pi:worker", CorrelationID: "stop-pi"})
}

func TestPiAdapterClearEOFDoesNotAdvertisePause(t *testing.T) {
	profile := piTestProfile(t)
	adapter, _ := piTestAdapter(t, "clear-eof")
	process, err := adapter.Start(context.Background(), piStartRequest(t, profile), func(AdapterEvent) {})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = process.Stop(context.Background(), ControlRequest{CorrelationID: "pi-test-stop"}) }()
	if _, err := process.(InboxProcess).Inbox(context.Background(), ControlRequest{CorrelationID: "queued", Text: "keep"}); err != nil {
		t.Fatal(err)
	}
	if _, err := process.Interrupt(context.Background(), ControlRequest{CorrelationID: "pause-eof"}); err == nil {
		t.Fatal("clear_queue EOF must not advertise pause")
	}
	if process.(*piProcess).DeliveryHeld() {
		t.Fatal("failed pause left the session marked paused")
	}
}

func piWorkspaceInspector(_ context.Context, path, mode string) (WorkspaceProvenance, error) {
	return WorkspaceProvenance{CanonicalPath: path, Identity: strings.Repeat("c", 64), Kind: WorkspaceDirectory, Mode: mode}, nil
}

func TestPiQueueUnicodeRetainedAfterSupervisorRestart(t *testing.T) {
	profile := piTestProfile(t)
	adapter, _ := piTestAdapter(t, "native-queue")
	root := t.TempDir()
	supervisor, err := NewSupervisor(SupervisorConfig{
		Instance: "ppm-pi-restart", Adapters: []Adapter{adapter}, DispatchResolver: &profileResolver{profile: profile},
		StateRoot: root, WorkspaceInspector: piWorkspaceInspector,
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := supervisor.Start(context.Background(), StartRequest{
		Adapter: AdapterPi, Workspace: t.TempDir(), Prompt: "work", Identity: "pi:worker", ProjectID: 957,
		DispatchProfileID: profile.ID, DispatchProfileVersion: profile.Version,
		AccountKey: "operator-pi", ExpectedAccountLabel: AccountPiContext,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantSteer := "keep\u2028steer"
	wantFollow := "keep\u2029follow"
	if _, err := supervisor.Steer(context.Background(), session.ID, ControlRequest{Instance: "ppm-pi-restart", ProjectID: 957, Identity: "pi:worker", CorrelationID: "steer-keep", Text: wantSteer}); err != nil {
		t.Fatal(err)
	}
	if _, err := supervisor.Inbox(context.Background(), session.ID, ControlRequest{Instance: "ppm-pi-restart", ProjectID: 957, Identity: "pi:worker", CorrelationID: "follow-keep", Text: wantFollow}); err != nil {
		t.Fatal(err)
	}
	if _, err := supervisor.Interrupt(context.Background(), session.ID, ControlRequest{Instance: "ppm-pi-restart", ProjectID: 957, Identity: "pi:worker", CorrelationID: "pause-keep"}); err != nil {
		t.Fatal(err)
	}
	before, err := supervisor.HeldQueue(session.ID)
	if err != nil || before.Outcome != piHeldOutcomeHeld || len(before.Steering) != 1 || before.Steering[0] != wantSteer || len(before.FollowUp) != 1 || before.FollowUp[0] != wantFollow {
		t.Fatalf("before restart held=%+v err=%v", before, err)
	}
	if err := supervisor.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	recovered, err := NewSupervisor(SupervisorConfig{
		Instance: "ppm-pi-restart", Adapters: []Adapter{adapter}, DispatchResolver: &profileResolver{profile: profile},
		StateRoot: root, WorkspaceInspector: piWorkspaceInspector,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = recovered.Close(context.Background()) })
	after, err := recovered.HeldQueue(session.ID)
	if err != nil || after.Outcome != piHeldOutcomeTerminal || len(after.Steering) != 1 || after.Steering[0] != wantSteer || len(after.FollowUp) != 1 || after.FollowUp[0] != wantFollow {
		t.Fatalf("after restart held=%+v err=%v", after, err)
	}
	report, err := recovered.QueueRetention(session.ID, ControlRequest{Instance: "ppm-pi-restart", ProjectID: 957, Identity: "pi:worker"})
	if err != nil || report.Outcome != piHeldOutcomeTerminal || report.Steering != 1 || report.FollowUp != 1 || report.Generation != session.ID {
		t.Fatalf("after restart retention=%+v err=%v", report, err)
	}
	if _, err := recovered.ResumeQueue(context.Background(), session.ID, ControlRequest{Instance: "ppm-pi-restart", ProjectID: 957, Identity: "pi:worker", CorrelationID: "resume-dead"}); err == nil {
		t.Fatal("resume after restart must refuse a dead generation")
	}
}

func TestPiQueueResumeReinjectsHeldInstructions(t *testing.T) {
	profile := piTestProfile(t)
	trace := filepath.Join(t.TempDir(), "trace")
	adapter, _ := piTestAdapter(t, "native-queue", pirpctest.TraceEnv+"="+trace)
	supervisor, err := NewSupervisor(SupervisorConfig{
		Instance: "ppm-pi-resume", Adapters: []Adapter{adapter}, DispatchResolver: &profileResolver{profile: profile},
		StateRoot: t.TempDir(), WorkspaceInspector: piWorkspaceInspector,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = supervisor.Close(context.Background()) })
	session, err := supervisor.Start(context.Background(), StartRequest{
		Adapter: AdapterPi, Workspace: t.TempDir(), Prompt: "work", Identity: "pi:worker", ProjectID: 957,
		DispatchProfileID: profile.ID, DispatchProfileVersion: profile.Version,
		AccountKey: "operator-pi", ExpectedAccountLabel: AccountPiContext,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "resume\u2028me"
	if _, err := supervisor.Inbox(context.Background(), session.ID, ControlRequest{Instance: "ppm-pi-resume", ProjectID: 957, Identity: "pi:worker", CorrelationID: "follow-resume", Text: want}); err != nil {
		t.Fatal(err)
	}
	if _, err := supervisor.Interrupt(context.Background(), session.ID, ControlRequest{Instance: "ppm-pi-resume", ProjectID: 957, Identity: "pi:worker", CorrelationID: "pause-resume"}); err != nil {
		t.Fatal(err)
	}
	if _, err := supervisor.ResumeQueue(context.Background(), session.ID, ControlRequest{Instance: "ppm-pi-resume", ProjectID: 957, Identity: "pi:worker", CorrelationID: "resume-1"}); err != nil {
		t.Fatal(err)
	}
	if supervisor.DeliveryHeld(session.ID) {
		t.Fatal("resume left delivery held")
	}
	again, err := supervisor.ResumeQueue(context.Background(), session.ID, ControlRequest{Instance: "ppm-pi-resume", ProjectID: 957, Identity: "pi:worker", CorrelationID: "resume-1"})
	if err != nil || again.CorrelationID != "resume-1" {
		t.Fatalf("resume replay=%+v err=%v", again, err)
	}
	raw, _ := os.ReadFile(trace)
	if strings.Count(string(raw), "cmd=follow_up") != 2 {
		t.Fatalf("resume follow-up count want 2, trace=%q", raw)
	}
}

func TestPiStopRefusesWhenClearQueueFails(t *testing.T) {
	profile := piTestProfile(t)
	adapter, _ := piTestAdapter(t, "clear-eof")
	process, err := adapter.Start(context.Background(), piStartRequest(t, profile), func(AdapterEvent) {})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := process.(InboxProcess).Inbox(context.Background(), ControlRequest{CorrelationID: "queued-stop-eof", Text: "keep"}); err != nil {
		t.Fatal(err)
	}
	pid := process.PID()
	if pid <= 0 {
		t.Fatal("child was not running")
	}
	effect, err := process.Stop(context.Background(), ControlRequest{CorrelationID: "stop-eof"})
	if err == nil {
		t.Fatal("stop must refuse success when clear_queue fails")
	}
	if effect.Primitive != piStopPrimitive {
		t.Fatalf("stop must still reap with a stop primitive: %+v", effect)
	}
	assertOwnedChildReaped(t, process, pid)
	if !strings.Contains(strings.Join(retainedPiQueueTexts(t, process), "\n"), "keep") {
		t.Fatal("failed clear discarded durable queued text")
	}
}

func TestPiUnaccountedNativeExtraRefusesPause(t *testing.T) {
	profile := piTestProfile(t)
	adapter, _ := piTestAdapter(t, "clear-extra")
	process, err := adapter.Start(context.Background(), piStartRequest(t, profile), func(AdapterEvent) {})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := process.(InboxProcess).Inbox(context.Background(), ControlRequest{CorrelationID: "follow-extra", Text: "mine"}); err != nil {
		t.Fatal(err)
	}
	if _, err := process.Interrupt(context.Background(), ControlRequest{CorrelationID: "pause-extra"}); err == nil {
		t.Fatal("unaccounted native extra must not advertise pause")
	}
	if process.(*piProcess).pauseAccounted() {
		t.Fatal("unaccounted extra was treated as accounted pause")
	}
	held, err := process.(*piProcess).queue.held(process.(*piProcess).scope.Generation)
	if err != nil || held.Outcome != piHeldOutcomeAmbiguous || !strings.Contains(strings.Join(held.FollowUp, "\n"), "ghost-extra") {
		t.Fatalf("unaccounted extra was not retained: %+v err=%v", held, err)
	}
	pid := process.PID()
	if pid <= 0 {
		t.Fatal("child was not running")
	}
	effect, err := process.Stop(context.Background(), ControlRequest{CorrelationID: "stop-extra"})
	if err == nil || !errors.Is(err, errPiQueueAmbiguous) {
		t.Fatalf("ambiguous stop must not advertise loss-free success: effect=%+v err=%v", effect, err)
	}
	if effect.Primitive != piStopPrimitive {
		t.Fatalf("ambiguous stop must still reap: %+v", effect)
	}
	assertOwnedChildReaped(t, process, pid)
	held, err = process.(*piProcess).queue.held(process.(*piProcess).scope.Generation)
	if err != nil || held.Outcome != piHeldOutcomeAmbiguous || !strings.Contains(strings.Join(held.FollowUp, "\n"), "ghost-extra") {
		t.Fatalf("stop dropped ambiguous extras: %+v err=%v", held, err)
	}
}

func TestPiQueueStoreRecoversUnicodeAfterReopen(t *testing.T) {
	root := t.TempDir()
	store, err := openPiQueueStore(root, "ppm-pi-spool")
	if err != nil {
		t.Fatal(err)
	}
	generation := uuid.NewString()
	scope := piQueueScope{
		Generation: generation, ProjectID: 957, Identity: "pi:worker", AccountKey: "operator-pi",
		AccountLabel: AccountPiContext, ProfileID: "pi-anthropic-sonnet-high", ProfileVersion: "1",
	}
	wantSteer := "keep\u2028steer"
	wantFollow := "keep\u2029follow"
	if err := store.reserve(scope, piQueueKindSteer, wantSteer, "steer-keep"); err != nil {
		t.Fatal(err)
	}
	if err := store.markQueued(generation, "steer-keep", piQueueKindSteer); err != nil {
		t.Fatal(err)
	}
	if err := store.reserve(scope, piQueueKindFollowUp, wantFollow, "follow-keep"); err != nil {
		t.Fatal(err)
	}
	if err := store.markQueued(generation, "follow-keep", piQueueKindFollowUp); err != nil {
		t.Fatal(err)
	}
	if err := store.beginHold(generation, "pause-keep"); err != nil {
		t.Fatal(err)
	}
	if err := store.commitHold(generation, "pause-keep", pirpc.ClearQueueData{Steering: []string{wantSteer}, FollowUp: []string{wantFollow}}, false); err != nil {
		t.Fatal(err)
	}
	recovered, err := openPiQueueStore(root, "ppm-pi-spool")
	if err != nil {
		t.Fatal(err)
	}
	held, err := recovered.held(generation)
	if err != nil || held.Outcome != piHeldOutcomeHeld || held.Outcome == piHeldOutcomeTerminal || len(held.Steering) != 1 || held.Steering[0] != wantSteer || len(held.FollowUp) != 1 || held.FollowUp[0] != wantFollow {
		t.Fatalf("unclean reopen lost held unicode or rewrote terminal: %+v err=%v", held, err)
	}
}

func TestPiAbortFailureRefusesPauseAndRetainsQueue(t *testing.T) {
	profile := piTestProfile(t)
	adapter, _ := piTestAdapter(t, "abort-fail")
	process, err := adapter.Start(context.Background(), piStartRequest(t, profile), func(AdapterEvent) {})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = process.Stop(context.Background(), ControlRequest{CorrelationID: "pi-test-stop"}) }()
	want := "keep\u2028after-clear"
	if _, err := process.(InboxProcess).Inbox(context.Background(), ControlRequest{CorrelationID: "queued-abort", Text: want}); err != nil {
		t.Fatal(err)
	}
	if _, err := process.Interrupt(context.Background(), ControlRequest{CorrelationID: "pause-abort"}); err == nil {
		t.Fatal("abort failure must not advertise pause")
	}
	if process.(*piProcess).pauseAccounted() {
		t.Fatal("abort failure left the session marked paused")
	}
	if !process.(*piProcess).DeliveryHeld() {
		t.Fatal("cleared but un-aborted queue must still hold delivery")
	}
	held, err := process.(*piProcess).queue.held(process.(*piProcess).scope.Generation)
	if err != nil || held.Outcome != piHeldOutcomeHeld || len(held.FollowUp) != 1 || held.FollowUp[0] != want {
		t.Fatalf("abort failure discarded queued text: %+v err=%v", held, err)
	}
}

func TestPiConcurrentInboxVersusPause(t *testing.T) {
	profile := piTestProfile(t)
	adapter, _ := piTestAdapter(t, "native-queue")
	process, err := adapter.Start(context.Background(), piStartRequest(t, profile), func(AdapterEvent) {})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = process.Stop(context.Background(), ControlRequest{CorrelationID: "pi-test-stop"}) }()
	inbox := process.(InboxProcess)
	if _, err := inbox.Inbox(context.Background(), ControlRequest{CorrelationID: "follow-base", Text: "base\u2028item"}); err != nil {
		t.Fatal(err)
	}
	var inboxErr, pauseErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, inboxErr = inbox.Inbox(context.Background(), ControlRequest{CorrelationID: "follow-race", Text: "race\u2029item"})
	}()
	go func() {
		defer wg.Done()
		_, pauseErr = process.Interrupt(context.Background(), ControlRequest{CorrelationID: "pause-race"})
	}()
	wg.Wait()
	if pauseErr != nil {
		t.Fatalf("pause lost the race with error %v", pauseErr)
	}
	if inboxErr != nil && !errors.Is(inboxErr, errPiPaused) {
		t.Fatalf("inbox race err=%v", inboxErr)
	}
	held, err := process.(*piProcess).queue.held(process.(*piProcess).scope.Generation)
	if err != nil || held.Outcome != piHeldOutcomeHeld {
		t.Fatalf("race dropped queue accounting: %+v err=%v inboxErr=%v", held, err, inboxErr)
	}
	joined := strings.Join(held.FollowUp, "\n")
	if !strings.Contains(joined, "base\u2028item") {
		t.Fatalf("race dropped the already-queued instruction: %+v inboxErr=%v", held, inboxErr)
	}
	if inboxErr == nil && (!strings.Contains(joined, "race\u2029item") || len(held.FollowUp) != 2) {
		t.Fatalf("accepted racing inbox was not retained: %+v", held)
	}
	if inboxErr != nil && len(held.FollowUp) != 1 {
		t.Fatalf("paused inbox still mutated held queue: %+v", held)
	}
}

func TestPiSteerAcceptIsNotTerminalExecution(t *testing.T) {
	profile := piTestProfile(t)
	completed := make(chan struct{}, 1)
	adapter, _ := piTestAdapter(t, "native-queue")
	process, err := adapter.Start(context.Background(), piStartRequest(t, profile), func(event AdapterEvent) {
		if event.Kind == EventTurnCompleted {
			completed <- struct{}{}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = process.Stop(context.Background(), ControlRequest{CorrelationID: "pi-test-stop"}) }()
	select {
	case <-completed:
	case <-time.After(200 * time.Millisecond):
	}
	for len(completed) > 0 {
		<-completed
	}
	if _, err := process.Steer(context.Background(), ControlRequest{CorrelationID: "steer-not-done", Text: "queued only"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-completed:
		t.Fatal("steer accept must not be observed as terminal execution")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestPiQueueRetentionControlSurface(t *testing.T) {
	profile := piTestProfile(t)
	adapter, _ := piTestAdapter(t, "native-queue")
	supervisor, err := NewSupervisor(SupervisorConfig{
		Instance: "ppm-pi-queue-ctl", Adapters: []Adapter{adapter}, DispatchResolver: &profileResolver{profile: profile},
		StateRoot: t.TempDir(), WorkspaceInspector: piWorkspaceInspector,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = supervisor.Close(context.Background()) })
	session, err := supervisor.Start(context.Background(), StartRequest{
		Adapter: AdapterPi, Workspace: t.TempDir(), Prompt: "secret-prompt-must-not-leak", Identity: "pi:worker", ProjectID: 957,
		DispatchProfileID: profile.ID, DispatchProfileVersion: profile.Version,
		AccountKey: "operator-pi", ExpectedAccountLabel: AccountPiContext,
	})
	if err != nil {
		t.Fatal(err)
	}
	scope := ControlRequest{Instance: "ppm-pi-queue-ctl", ProjectID: 957, Identity: "pi:worker"}
	wrong := scope
	wrong.ProjectID++
	if _, err := supervisor.QueueRetention(session.ID, wrong); !errors.Is(err, ErrControlScopeMismatch) {
		t.Fatalf("queue-retention scope error=%v", err)
	}
	if _, err := supervisor.ResumeQueue(context.Background(), session.ID, ControlRequest{
		Instance: "ppm-pi-queue-ctl", ProjectID: 958, Identity: "pi:worker", CorrelationID: "resume-wrong-scope",
	}); !errors.Is(err, ErrControlScopeMismatch) {
		t.Fatalf("resume-queue scope error=%v", err)
	}
	if _, err := supervisor.ResumeQueue(context.Background(), session.ID, ControlRequest{
		Instance: "ppm-pi-queue-ctl", ProjectID: 957, Identity: "pi:worker", CorrelationID: "resume-not-paused",
	}); err == nil {
		t.Fatal("resume of a live unpaused generation must refuse")
	}
	wantSteer := "keep\u2028secret-steer"
	wantFollow := "keep\u2029secret-follow"
	if _, err := supervisor.Steer(context.Background(), session.ID, ControlRequest{
		Instance: "ppm-pi-queue-ctl", ProjectID: 957, Identity: "pi:worker", CorrelationID: "steer-keep", Text: wantSteer,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := supervisor.Inbox(context.Background(), session.ID, ControlRequest{
		Instance: "ppm-pi-queue-ctl", ProjectID: 957, Identity: "pi:worker", CorrelationID: "follow-keep", Text: wantFollow,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := supervisor.Interrupt(context.Background(), session.ID, ControlRequest{
		Instance: "ppm-pi-queue-ctl", ProjectID: 957, Identity: "pi:worker", CorrelationID: "pause-keep",
	}); err != nil {
		t.Fatal(err)
	}
	report, err := supervisor.QueueRetention(session.ID, scope)
	if err != nil || report.Outcome != piHeldOutcomeHeld || report.Steering != 1 || report.FollowUp != 1 || report.Generation != session.ID {
		t.Fatalf("held retention=%+v err=%v", report, err)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), wantSteer) || strings.Contains(string(encoded), wantFollow) || strings.Contains(string(encoded), "secret-prompt-must-not-leak") {
		t.Fatalf("queue-retention leaked payload text: %s", encoded)
	}
	raw, err := json.Marshal(scope)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	httpRequest := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+session.ID+"/queue-retention", bytes.NewReader(raw))
	httpRequest.Header.Set("Content-Type", "application/json")
	transportHandler(supervisor).ServeHTTP(recorder, httpRequest)
	if recorder.Code != http.StatusOK {
		t.Fatalf("queue-retention HTTP %d body=%s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), wantSteer) || strings.Contains(recorder.Body.String(), wantFollow) {
		t.Fatalf("transport queue-retention leaked payload text: %s", recorder.Body.String())
	}
	wrongRaw, err := json.Marshal(wrong)
	if err != nil {
		t.Fatal(err)
	}
	forbidden := httptest.NewRecorder()
	httpRequest = httptest.NewRequest(http.MethodPost, "/v1/sessions/"+session.ID+"/queue-retention", bytes.NewReader(wrongRaw))
	httpRequest.Header.Set("Content-Type", "application/json")
	transportHandler(supervisor).ServeHTTP(forbidden, httpRequest)
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("queue-retention scope HTTP %d body=%s", forbidden.Code, forbidden.Body.String())
	}
	first, err := supervisor.ResumeQueue(context.Background(), session.ID, ControlRequest{
		Instance: "ppm-pi-queue-ctl", ProjectID: 957, Identity: "pi:worker", CorrelationID: "resume-keep",
	})
	if err != nil || first.CorrelationID != "resume-keep" || first.Primitive != piResumePrimitive {
		t.Fatalf("resume=%+v err=%v", first, err)
	}
	again, err := supervisor.ResumeQueue(context.Background(), session.ID, ControlRequest{
		Instance: "ppm-pi-queue-ctl", ProjectID: 957, Identity: "pi:worker", CorrelationID: "resume-keep",
	})
	if err != nil || again.CorrelationID != first.CorrelationID || again.Primitive != first.Primitive {
		t.Fatalf("resume replay=%+v err=%v", again, err)
	}
}

func TestPiSupervisorStopReapsAmbiguousQueue(t *testing.T) {
	profile := piTestProfile(t)
	adapter, _ := piTestAdapter(t, "clear-extra")
	supervisor, err := NewSupervisor(SupervisorConfig{
		Instance: "ppm-pi-stop-amb", Adapters: []Adapter{adapter}, DispatchResolver: &profileResolver{profile: profile},
		StateRoot: t.TempDir(), WorkspaceInspector: piWorkspaceInspector,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = supervisor.Close(context.Background()) })
	session, err := supervisor.Start(context.Background(), StartRequest{
		Adapter: AdapterPi, Workspace: t.TempDir(), Prompt: "work", Identity: "pi:worker", ProjectID: 957,
		DispatchProfileID: profile.ID, DispatchProfileVersion: profile.Version,
		AccountKey: "operator-pi", ExpectedAccountLabel: AccountPiContext,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := supervisor.Inbox(context.Background(), session.ID, ControlRequest{
		Instance: "ppm-pi-stop-amb", ProjectID: 957, Identity: "pi:worker", CorrelationID: "follow-extra", Text: "mine",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := supervisor.Interrupt(context.Background(), session.ID, ControlRequest{
		Instance: "ppm-pi-stop-amb", ProjectID: 957, Identity: "pi:worker", CorrelationID: "pause-extra",
	}); err == nil {
		t.Fatal("unaccounted extra must not advertise pause")
	}
	pid := 0
	for _, row := range supervisor.Status().Sessions {
		if row.ID == session.ID {
			pid = row.PID
		}
	}
	if pid <= 0 {
		t.Fatal("child was not running")
	}
	receipt, err := supervisor.Stop(context.Background(), session.ID, ControlRequest{
		Instance: "ppm-pi-stop-amb", ProjectID: 957, Identity: "pi:worker", CorrelationID: "stop-extra",
	})
	if err == nil || !errors.Is(err, errPiQueueAmbiguous) {
		t.Fatalf("ambiguous supervisor stop must not advertise loss-free success: receipt=%+v err=%v", receipt, err)
	}
	if receipt.Primitive != piStopPrimitive || receipt.Operation != "stop" || receipt.SessionID != session.ID {
		t.Fatalf("ambiguous stop receipt=%+v", receipt)
	}
	if err := syscall.Kill(pid, 0); err == nil {
		t.Fatal("supervisor stop left the owned child live")
	}
	held, err := supervisor.HeldQueue(session.ID)
	if err != nil || held.Outcome != piHeldOutcomeAmbiguous || !strings.Contains(strings.Join(held.FollowUp, "\n"), "ghost-extra") {
		t.Fatalf("stop dropped ambiguous extras: %+v err=%v", held, err)
	}
}

func assertOwnedChildReaped(t *testing.T, process Process, pid int) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- process.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("owned child Wait did not complete after stop")
	}
	if err := syscall.Kill(pid, 0); err == nil {
		t.Fatalf("owned child pid %d is still live", pid)
	}
}

func retainedPiQueueTexts(t *testing.T, process Process) []string {
	t.Helper()
	pp := process.(*piProcess)
	pp.queue.mu.Lock()
	defer pp.queue.mu.Unlock()
	var texts []string
	for _, record := range pp.queue.generationRecordsLocked(pp.scope.Generation) {
		text, err := pp.queue.readPayload(record.Key, record.TextSHA256, record.TextBytes)
		if err != nil {
			continue
		}
		texts = append(texts, text)
	}
	return texts
}

// TestPiFakeChildProcess hosts the test-only fake Pi RPC child.
func TestPiFakeChildProcess(t *testing.T) {
	if !pirpctest.Run() {
		return
	}
}
