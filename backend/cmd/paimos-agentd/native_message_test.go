package main

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/inspr-at/paimos/backend/agentd"
	"io"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestNativeMessageReporterBindsProofAndRedacts(t *testing.T) {
	var calls [][]string
	broken := false
	r, err := newCLIReporterWithRunner("ppm", "test", "/paimos", nil, func(_ context.Context, _ string, args, _ []string, input io.Reader) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		raw, _ := io.ReadAll(input)
		var frame struct {
			Lease   string               `json:"worker_lease"`
			Message agentd.NativeMessage `json:"message"`
		}
		if json.Unmarshal(raw, &frame) != nil || frame.Lease == "" || frame.Message.Body != "private body" {
			t.Fatal("private frame missing")
		}
		if strings.Contains(strings.Join(args, " "), frame.Lease) || slices.Contains(args, "private body") {
			t.Fatal("payload or proof in argv")
		}
		if broken {
			return []byte(`{"error":"private diagnostic"}`), errors.New("private diagnostic")
		}
		return []byte(`{"message_id":"33333333-3333-4333-8333-333333333333","thread_id":"33333333-3333-4333-8333-333333333333","delivered":true,"error":"private diagnostic","unexpected":"private diagnostic"}`), nil
	}, newMemoryReporterLeaseStore())
	if err != nil {
		t.Fatal(err)
	}
	session := agentd.Session{ID: localReporterSession, ProjectID: 6, Identity: "codex:worker", Adapter: "codex", Managed: true, State: agentd.StateRunning}
	message := agentd.NativeMessage{To: "claude:peer", Body: "private body"}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if receipt := r.SendNativeMessage(ctx, session, "call-1", message); receipt.Error == "" || len(calls) != 0 {
		t.Fatal("unregistered sender used authority")
	}
	r.sessions[session.ID] = reportedSession{ready: true, publicID: publicReporterSession, projectID: 6, identity: session.Identity}
	for range 2 {
		receipt := r.SendNativeMessage(context.Background(), session, "call-1", message)
		if receipt.Error != "" || !receipt.Delivered {
			t.Fatalf("receipt=%+v", receipt)
		}
	}
	if !slices.Equal(calls[0], calls[1]) {
		t.Fatal("native retry changed idempotency or attribution")
	}
	for _, v := range []string{"--project-id", "6", "--session", publicReporterSession, "--agent", "worker"} {
		if !slices.Contains(calls[0], v) {
			t.Fatalf("missing %s", v)
		}
	}
	broken = true
	if receipt := r.SendNativeMessage(context.Background(), session, "call-2", message); receipt.Error != "send_failed" {
		t.Fatal("private error escaped")
	}
	known := r.sessions[session.ID]
	known.terminal = true
	r.sessions[session.ID] = known
	if receipt := r.SendNativeMessage(context.Background(), session, "call-3", message); receipt.Error == "" || len(calls) != 3 {
		t.Fatal("terminal sender used authority")
	}
}

func TestNativeMessageWaitsForRegistrationWithoutBlockingReporter(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	r, err := newCLIReporterWithRunner("ppm", "test", "/paimos", nil, func(ctx context.Context, _ string, _, _ []string, _ io.Reader) ([]byte, error) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return []byte(`{"message_id":"33333333-3333-4333-8333-333333333333","thread_id":"33333333-3333-4333-8333-333333333333","delivered":true}`), nil
	}, newMemoryReporterLeaseStore())
	if err != nil {
		t.Fatal(err)
	}
	session := agentd.Session{ID: localReporterSession, Identity: "codex:worker", ProjectID: 6, State: agentd.StateRunning, Managed: true, Adapter: "codex"}
	result := make(chan agentd.NativeMessageReceipt, 1)
	go func() {
		result <- r.SendNativeMessage(context.Background(), session, "call-ready", agentd.NativeMessage{To: "claude:peer", Body: "status"})
	}()
	r.mu.Lock()
	r.sessions[session.ID] = reportedSession{publicID: publicReporterSession, identity: session.Identity, projectID: 6}
	r.mu.Unlock()
	select {
	case <-started:
		t.Fatal("send preceded first heartbeat")
	case <-time.After(30 * time.Millisecond):
	}
	r.mu.Lock()
	known := r.sessions[session.ID]
	known.ready = true
	r.sessions[session.ID] = known
	r.mu.Unlock()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("registered sender did not resume")
	}
	if !r.mu.TryLock() {
		close(release)
		t.Fatal("network send blocked all heartbeats")
	}
	r.mu.Unlock()
	close(release)
	if receipt := <-result; !receipt.Delivered {
		t.Fatalf("receipt=%+v", receipt)
	}
}
