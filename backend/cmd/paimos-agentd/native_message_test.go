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
	r.publishSession(session.ID, reportedSession{ready: true, workerLease: "fixture-lease", publicID: publicReporterSession, projectID: 6, identity: session.Identity})
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
	known, _ := r.reportedSession(session.ID)
	known.terminal = true
	r.publishSession(session.ID, known)
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
	r.publishSession(session.ID, reportedSession{publicID: publicReporterSession, identity: session.Identity, projectID: 6})
	select {
	case <-started:
		t.Fatal("send preceded first heartbeat")
	case <-time.After(30 * time.Millisecond):
	}
	known, _ := r.reportedSession(session.ID)
	known.ready = true
	known.workerLease = "fixture-lease"
	r.publishSession(session.ID, known)
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

// A ready sender must not wait behind an unrelated network heartbeat. This
// reproduces the inverse of the native-send-does-not-block-reporting contract.
func TestNativeMessageReadySenderSurvivesBusyReporter(t *testing.T) {
	blocked := make(chan struct{})
	release := make(chan struct{})
	busy := false
	runner := func(ctx context.Context, _ string, args, _ []string, _ io.Reader) ([]byte, error) {
		switch args[2] {
		case "register":
			return json.Marshal(harnessSessionResponse{ID: publicReporterSession, ProjectID: 6, AgentName: "worker", Harness: "codex"})
		case "heartbeat":
			if busy {
				close(blocked)
				select {
				case <-release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return reporterSessionEvidence("worker", "working"), nil
		case "yield":
			return json.Marshal(harnessYieldResponse{Session: harnessSessionResponse{ID: publicReporterSession, ProjectID: 6, AgentName: "worker", Harness: "codex"}})
		case "send-native":
			return []byte(`{"message_id":"33333333-3333-4333-8333-333333333333","thread_id":"33333333-3333-4333-8333-333333333333","delivered":true}`), nil
		default:
			return nil, errors.New("unexpected command")
		}
	}
	r, err := newCLIReporterWithRunner("ppm", "test", "/paimos", nil, runner, newMemoryReporterLeaseStore())
	if err != nil {
		t.Fatal(err)
	}
	if err := r.BindController(&recordingReporterController{}); err != nil {
		t.Fatal(err)
	}
	session := agentd.Session{ID: localReporterSession, ProjectID: 6, Identity: "codex:worker", Adapter: "codex", Managed: true, State: agentd.StateRunning, Capabilities: []agentd.Capability{agentd.CapabilityStatus, agentd.CapabilityStop}}
	status := agentd.Status{Instance: "ppm", Sessions: []agentd.Session{session}}
	if err := r.ReportStatus(context.Background(), status); err != nil {
		t.Fatal(err)
	}
	busy = true
	reportDone := make(chan error, 1)
	go func() { reportDone <- r.ReportStatus(context.Background(), status) }()
	<-blocked
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	receipt := r.SendNativeMessage(ctx, session, "call-busy-reporter", agentd.NativeMessage{To: "claude:peer", Body: "status"})
	close(release)
	if err := <-reportDone; err != nil {
		t.Fatal(err)
	}
	if !receipt.Delivered || receipt.Error != "" {
		t.Fatalf("ready sender blocked by heartbeat: %+v", receipt)
	}
}

func TestNativeMessageRejectsUnprovenOrForeignSenderWithoutRecreatingLease(t *testing.T) {
	for _, kind := range []string{"unregistered", "unconfirmed", "missing_lease", "terminal", "project", "identity", "public_id"} {
		t.Run(kind, func(t *testing.T) {
			leases := newMemoryReporterLeaseStore().(*memoryReporterLeaseStore)
			r, err := newCLIReporterWithRunner("ppm", "test", "/paimos", nil,
				func(context.Context, string, []string, []string, io.Reader) ([]byte, error) {
					t.Error("unproven sender reached transport")
					return nil, errors.New("unexpected send")
				}, leases)
			if err != nil {
				t.Fatal(err)
			}
			session := agentd.Session{ID: localReporterSession, Identity: "claude:worker", ProjectID: 6, State: agentd.StateRunning, Managed: true, Adapter: "claude"}
			known := reportedSession{ready: true, workerLease: "fixture-lease", publicID: publicReporterSession, identity: session.Identity, projectID: 6}
			switch kind {
			case "unconfirmed":
				known.ready = false
			case "missing_lease":
				known.workerLease = ""
			case "terminal":
				known.terminal = true
			case "project":
				known.projectID++
			case "identity":
				known.identity = "claude:foreign"
			case "public_id":
				known.publicID = "invalid"
			}
			if kind != "unregistered" {
				r.publishSession(session.ID, known)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			got := r.SendNativeMessage(ctx, session, "call-refused", agentd.NativeMessage{To: "codex:peer", Body: "fixture"})
			if got.Error != "sender_unavailable" || got.Delivered {
				t.Fatal("unproven sender not refused")
			}
			if len(leases.values) != 0 {
				t.Fatal("native send recreated worker lease")
			}
			if kind == "terminal" {
				current, _ := r.reportedSession(session.ID)
				if current.ready || current.workerLease != "" {
					t.Fatal("terminal proof retained")
				}
			}
		})
	}
}

func TestNativeMessageRemoteClosureRevokesPublishedProof(t *testing.T) {
	leases := newMemoryReporterLeaseStore().(*memoryReporterLeaseStore)
	r, err := newCLIReporterWithRunner("ppm", "test", "/paimos", nil,
		func(context.Context, string, []string, []string, io.Reader) ([]byte, error) {
			t.Error("closed sender reached network")
			return nil, errors.New("unexpected send")
		}, leases)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.BindController(&recordingReporterController{}); err != nil {
		t.Fatal(err)
	}
	session := agentd.Session{ID: localReporterSession, Identity: "claude:worker", ProjectID: 6, State: agentd.StateRunning, Managed: true, Adapter: "claude", Reporter: agentd.ReporterState{PublicSessionID: publicReporterSession, RemoteClosed: true}}
	r.publishSession(session.ID, reportedSession{ready: true, workerLease: "fixture-lease", publicID: publicReporterSession, identity: session.Identity, projectID: 6})
	if err := r.ReportStatus(context.Background(), agentd.Status{Instance: "ppm", Sessions: []agentd.Session{session}}); err != nil {
		t.Fatal(err)
	}
	got := r.SendNativeMessage(context.Background(), session, "call-closed", agentd.NativeMessage{To: "codex:peer", Body: "fixture"})
	if got.Error != "sender_unavailable" || got.Delivered {
		t.Fatal("remote closure did not revoke sender")
	}
	known, _ := r.reportedSession(session.ID)
	if known.ready || known.workerLease != "" || !known.terminal || len(leases.values) != 0 {
		t.Fatal("remote closure retained or recreated proof")
	}
}
