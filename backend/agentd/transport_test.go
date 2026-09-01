// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

//go:build unix && !paimos_test_unsupported

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
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/agentmessage/harness"
)

func TestStopTransportNeverTurnsFailedReplayIntoHTTP200(t *testing.T) {
	stopFailure := &ownedStopFailure{detail: "transport stop failed"}
	process := &failedStopProcess{fakeProcess: newFakeProcess(9875), failure: stopFailure}
	supervisor, err := NewSupervisor(SupervisorConfig{Instance: "ppm-stop-transport", Adapters: []Adapter{&fakeAdapter{name: AdapterCodex, process: process, threadID: "thread-stop-transport"}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = supervisor.Close(context.Background()) })
	session, err := supervisor.Start(context.Background(), StartRequest{
		Adapter: AdapterCodex, Workspace: t.TempDir(), Prompt: "work", Identity: "codex:stop-transport", ProjectID: 870,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := scopedControl(supervisor, session, "control-stop-transport-replay", "")
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	call := func() *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		httpRequest := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+session.ID+"/stop", bytes.NewReader(raw))
		httpRequest.Header.Set("Content-Type", "application/json")
		transportHandler(supervisor).ServeHTTP(recorder, httpRequest)
		return recorder
	}
	first := call()
	if first.Code != http.StatusBadRequest {
		t.Fatalf("first failed stop status=%d body=%s", first.Code, first.Body.String())
	}
	process.waited <- nil
	entry, err := supervisor.get(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entry.monitorDone:
	case <-time.After(time.Second):
		t.Fatal("child finalization did not complete")
	}
	replay := call()
	if replay.Code != first.Code || replay.Body.String() != first.Body.String() {
		t.Fatalf("failed replay status/body=%d %s want=%d %s", replay.Code, replay.Body.String(), first.Code, first.Body.String())
	}
	process.mu.Lock()
	stops := process.stops
	process.mu.Unlock()
	if stops != 1 {
		t.Fatalf("failed transport replay reached vendor %d times, want once", stops)
	}
}

func TestUnixTransportIsPrivateAndRoundTrips(t *testing.T) {
	process := newFakeProcess(9876)
	adapter := &fakeAdapter{name: AdapterCodex, process: process, threadID: "thread-local"}
	supervisor, err := NewSupervisor(SupervisorConfig{Instance: "ppm-transport", Adapters: []Adapter{adapter}})
	if err != nil {
		t.Fatal(err)
	}
	socketDir, err := os.MkdirTemp("/tmp", "pai849-agentd-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socket := filepath.Join(socketDir, "agentd.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveDone := make(chan error, 1)
	go func() { serveDone <- Serve(ctx, socket, supervisor) }()

	client, err := NewClient(socket)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err = client.Status(context.Background()); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon did not start: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	info, err := os.Lstat(socket)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode=%v", info.Mode())
	}
	second, err := NewSupervisor(SupervisorConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if err := Serve(context.Background(), socket, second); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second daemon on live socket error=%v", err)
	}

	session, err := client.Start(context.Background(), StartRequest{
		Adapter: AdapterCodex, Workspace: t.TempDir(), Prompt: "secret prompt is body-only", Identity: "codex:local", ProjectID: 849,
	})
	if err != nil {
		t.Fatal(err)
	}
	wrongScope := scopedControl(supervisor, session, "delivery-wrong-scope", "must not apply")
	wrongScope.ProjectID++
	if _, err := client.Steer(context.Background(), session.ID, wrongScope); !errors.Is(err, ErrControlScopeMismatch) {
		t.Fatalf("cross-project transport error=%v, want ErrControlScopeMismatch", err)
	}
	target, err := json.Marshal(harness.AgentdTarget{Socket: socket, SessionID: session.ID})
	if err != nil {
		t.Fatal(err)
	}
	result, err := harness.Deliver(context.Background(), harness.AdapterAgentdCodex, harness.DeliverRequest{
		Level: harness.LevelSteer, Body: "leased body", TargetRef: string(target), CorrelationID: "delivery-leased-122",
		Instance: "ppm-transport", ProjectID: 849, Identity: "codex:local",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.EffectiveLevel != harness.LevelSteer || result.Primitive != "codex app-server turn/steer" {
		t.Fatalf("managed delivery result=%+v", result)
	}
	if _, err := harness.Deliver(context.Background(), harness.AdapterAgentdCodex, harness.DeliverRequest{
		Level: harness.LevelSteer, Body: "leased body", TargetRef: string(target), CorrelationID: "delivery-leased-122",
		Instance: "ppm-transport", ProjectID: 849, Identity: "codex:local",
	}); err != nil {
		t.Fatalf("idempotent managed delivery retry: %v", err)
	}
	process.mu.Lock()
	steers := len(process.steers)
	process.mu.Unlock()
	if steers != 1 {
		t.Fatalf("leased managed delivery reached process %d times, want once", steers)
	}
	if _, err := client.Steer(context.Background(), session.ID, scopedControl(supervisor, session, "delivery-123", "continue")); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Interrupt(context.Background(), session.ID, scopedControl(supervisor, session, "control-124", "")); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Stop(context.Background(), session.ID, scopedControl(supervisor, session, "control-125", "")); err != nil {
		t.Fatal(err)
	}

	cancel()
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("daemon did not stop")
	}
}
