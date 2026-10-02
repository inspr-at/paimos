// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/sessionusage"
)

func TestTierControlCannotUseLocalSocketAndFencesOwnership(t *testing.T) {
	s, a, e, p := managedFixture(t)
	e.managedPolicy = false
	identity := p.identity
	identity.DaemonID = s.daemonID
	identity.Generation = s.generation
	expiry := time.Now().Add(time.Minute)
	req := ControlRequest{TenantID: s.tenantID, PrincipalID: s.principalID, RunID: a.run.ID, Generation: s.generation, CorrelationID: "tier-change", Operation: "tier", Value: "fast", ExpectedOwnership: &identity, ExpiresAt: &expiry, deadline: expiry}
	if _, err := s.Control(context.Background(), req); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("local socket escalated tier: %v", err)
	}
	identity.Generation = "wrong"
	if _, err := s.control(t.Context(), req, true); !errors.Is(err, ErrGeneration) {
		t.Fatal(err)
	}
	identity.Generation = s.generation
	if _, err := s.control(t.Context(), req, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.control(t.Context(), req, true); err != nil {
		t.Fatal("idempotent tier receipt lost", err)
	}
}

func TestCodexTierRequiresIdleSafePoint(t *testing.T) {
	p := &codexProcess{}
	// No native traffic or process access is allowed during an active turn.
	p.wireProcess = &wireProcess{}
	if err := p.changeTier(t.Context(), "fast"); !errors.Is(err, ErrSettingRejected) {
		t.Fatal(err)
	}
	if err := p.changeTier(t.Context(), "ultra"); !errors.Is(err, ErrSettingRejected) {
		t.Fatal(err)
	}
}

func TestCodexTierNativeAcknowledgementAndUndo(t *testing.T) {
	reader, writer := io.Pipe()
	input := codexSyntheticInput{requests: make(chan []byte, 1)}
	wire := &wireProcess{stdin: input, stdout: reader, pending: map[string]chan json.RawMessage{}, threadID: "owned-thread", protocol: "jsonrpc", readDone: make(chan struct{}), waitDone: make(chan struct{})}
	var observed string
	wire.observe = func(e AdapterEvent) { observed = e.HarnessTier }
	p := &codexProcess{wireProcess: wire, persistent: true, terminalSeen: true, acknowledged: true, terminal: &sessionusage.CodexTerminal{Clean: true}, serviceTier: "default"}
	go wire.read(reader)
	t.Cleanup(func() { _ = writer.Close(); _ = reader.Close(); <-wire.readDone })
	for _, tc := range []struct {
		tier, native, thread string
		accepted             bool
	}{
		{"fast", "fast", "owned-thread", true},
		{"default", "default", "owned-thread", true},
		{"fast", "fast", "foreign-thread", false},
	} {
		done := make(chan error, 1)
		go func() { done <- p.changeTier(t.Context(), tc.tier) }()
		var request map[string]any
		select {
		case b := <-input.requests:
			if err := json.Unmarshal(b, &request); err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("missing native tier request")
		}
		params := request["params"].(map[string]any)
		if request["method"] != "thread/resume" || params["threadId"] != "owned-thread" || params["config"].(map[string]any)["service_tier"] != tc.native {
			t.Fatal(request)
		}
		response, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request["id"], "result": map[string]any{"thread": map[string]string{"id": tc.thread}}})
		if _, err := writer.Write(append(response, '\n')); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-done:
			if (err == nil) != tc.accepted {
				t.Fatalf("%s accepted=%v: %v", tc.tier, tc.accepted, err)
			}
		case <-time.After(time.Second):
			t.Fatal("tier acknowledgement did not settle")
		}
		want := tc.tier
		if !tc.accepted {
			want = "default"
		}
		if p.serviceTier != want || observed != want {
			t.Fatalf("tier changed without matching native ack: %s %s", p.serviceTier, observed)
		}
	}
}
