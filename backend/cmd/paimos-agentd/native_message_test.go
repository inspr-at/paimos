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
	if receipt := r.SendNativeMessage(context.Background(), session, "call-1", message); receipt.Error == "" || len(calls) != 0 {
		t.Fatal("unregistered sender used authority")
	}
	r.sessions[session.ID] = reportedSession{publicID: publicReporterSession, projectID: 6, identity: session.Identity}
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
