// SPDX-License-Identifier: AGPL-3.0-only

package nativecontract_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/reportercontract"
	"gopkg.in/yaml.v3"
)

const testID = "00000000-0000-4000-8000-000000000001"

// api/openapi.yaml is ignored output, so the package must also work from a
// fresh checkout. The shared generator publishes its cache atomically.
func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	output, err := exec.CommandContext(ctx, "node", "../../api/generate.mjs", "--write").CombinedOutput()
	cancel()
	if err != nil {
		fmt.Fprintf(os.Stderr, "prepare native OpenAPI: %v\n%s", err, output)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func binding() nativecontract.Binding {
	return nativecontract.Binding{TenantID: testID, ProjectID: testID, OwnedSessionID: testID, HarnessSessionID: testID, RuntimeEpoch: testID, DaemonID: "test-daemon", DaemonGeneration: "test-epoch", BindingRevision: 1, RunID: nil}
}

func wire(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func input() nativecontract.Input {
	return nativecontract.Input{RequestID: testID, ExpectedBinding: binding(), InputID: testID, Mode: "now", Text: "A bounded test prompt", ExpiresAt: time.Date(2026, 10, 13, 1, 0, 0, 0, time.UTC)}
}

func frame(t *testing.T, kind string, payload any) nativecontract.StreamEvent {
	return nativecontract.StreamEvent{Binding: binding(), Sequence: "1", Seq: "1", Generation: 1, Source: "native", ObservedAt: time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC), Kind: kind, Payload: wire(t, payload), DroppedFrames: "0"}
}

func requireReason(t *testing.T, err error, want nativecontract.Reason) {
	t.Helper()
	var problem *nativecontract.ValidationError
	if !errors.As(err, &problem) || problem.Reason != want {
		t.Fatalf("got %v, want exact reason %s", err, want)
	}
}

// Named risk: contract drift or ambiguous input lets an adapter act under the
// wrong runtime, an unbounded frame, or an unsupported provider/view variant.
func TestNativeContractRejectsUnsafeWire(t *testing.T) {
	v, err := nativecontract.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	b := binding()
	caps := nativecontract.Capabilities{Input: true, ChatSupported: true, Interrupt: true, InterruptSupported: true, EventsSupported: true, Stop: true, Resume: true}
	ctx := nativecontract.ValidationContext{Binding: &b, OwnedSessionID: testID, PrincipalKind: "person", View: "structured", Mode: "structured", State: "running", Generation: 1, Capabilities: &caps}
	valid := input()
	if err := v.ValidateBoundJSON("NativeInput", wire(t, valid), ctx); err != nil {
		t.Fatalf("valid control rejected: %v", err)
	}
	delta := frame(t, "assistant.delta", nativecontract.AssistantDelta{MessageID: testID, Text: "A delta"})
	if err := v.ValidateBoundJSON("NativeStreamEvent", wire(t, delta), ctx); err != nil {
		t.Fatalf("valid delta rejected: %v", err)
	}

	wrong := valid
	wrong.ExpectedBinding.RuntimeEpoch = "00000000-0000-4000-8000-000000000002"
	oversize := valid
	oversize.Text = strings.Repeat("ä", 32769) // Character count fits; UTF-8 bytes do not.
	tooLargeFrame := frame(t, "assistant.delta", nativecontract.AssistantDelta{MessageID: testID, Text: strings.Repeat("x", 65536)})
	unknown := delta
	unknown.Kind = "unqualified.vendor_event"
	mismatch := delta
	mismatch.Kind = "terminal"
	transcript := delta
	transcript.Source = "transcript"
	wrongSequence := delta
	wrongSequence.Seq = "2"
	tooLargeSequence := delta
	tooLargeSequence.Seq = "18446744073709551616"
	tooLargeSequence.Sequence = tooLargeSequence.Seq
	failedInput := frame(t, "input.refused", nativecontract.InputFailure{InputID: testID, Retryable: true})
	force := nativecontract.Stop{RequestID: testID, ExpectedBinding: b, Kind: "force", ExpectedProcessIdentityDigest: strings.Repeat("a", 64), ExpiresAt: valid.ExpiresAt, ForceAuthorizationID: nil}
	for _, tc := range []struct {
		name, schema string
		value        any
		reason       nativecontract.Reason
	}{
		{"wrong runtime", "NativeInput", wrong, nativecontract.ReasonWrongGeneration},
		{"byte bound before work", "NativeInput", oversize, nativecontract.ReasonBufferFull},
		{"frame envelope bound", "NativeStreamEvent", tooLargeFrame, nativecontract.ReasonBufferFull},
		{"unknown event variant", "NativeStreamEvent", unknown, nativecontract.ReasonUnsupported},
		{"wrong kind payload", "NativeStreamEvent", mismatch, nativecontract.ReasonInvalidInput},
		{"transcript v2 unavailable", "NativeStreamEvent", transcript, nativecontract.ReasonUnsupported},
		{"sequence aliases disagree", "NativeStreamEvent", wrongSequence, nativecontract.ReasonInvalidInput},
		{"uint64 overflow", "NativeStreamEvent", tooLargeSequence, nativecontract.ReasonBufferFull},
		{"refusal is final", "NativeStreamEvent", failedInput, nativecontract.ReasonInvalidInput},
		{"force requires step up", "NativeStop", force, nativecontract.ReasonGrantMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requireReason(t, v.ValidateBoundJSON(tc.schema, wire(t, tc.value), ctx), tc.reason)
		})
	}
	// Negative control: the stale input still passes shape-only validation.
	// Thus the wrong-runtime assertion depends on the exact-binding guard.
	if err := v.ValidateJSON("NativeInput", wire(t, wrong)); err != nil {
		t.Fatalf("wrong-runtime fixture fails for an unrelated shape reason: %v", err)
	}

	t.Run("unsupported capability", func(t *testing.T) {
		limited := caps
		limited.Input = false
		limited.ChatSupported = false
		c := ctx
		c.Capabilities = &limited
		requireReason(t, v.ValidateBoundJSON("NativeInput", wire(t, valid), c), nativecontract.ReasonUnsupported)
	})
	t.Run("unknown duplicate or missing field", func(t *testing.T) {
		data := wire(t, valid)
		for _, bad := range [][]byte{
			append(append([]byte{}, data[:len(data)-1]...), []byte(`,"credential":"test-only"}`)...),
			append(append([]byte{}, data[:len(data)-1]...), []byte(`,"mode":"now"}`)...),
			bytes.Replace(data, []byte(`"text":"A bounded test prompt",`), nil, 1),
		} {
			requireReason(t, v.ValidateJSON("NativeInput", bad), nativecontract.ReasonInvalidInput)
		}
	})
	t.Run("local correlation and protocol", func(t *testing.T) {
		envelope := nativecontract.LocalEnvelope{Protocol: nativecontract.LocalProtocol, RequestID: testID, Method: "input", ExpectedBinding: &b, Params: wire(t, valid)}
		data := wire(t, envelope)
		if _, err := v.DecodeLocal(data, nativecontract.LocalProtocol, "input", ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := v.DecodeLocal(data, "aeon-native-session/2", "input", ctx); err == nil {
			t.Fatal("accepted an unsupported protocol")
		} else {
			requireReason(t, err, nativecontract.ReasonUnsupported)
		}
		envelope.RequestID = "00000000-0000-4000-8000-000000000002"
		_, err := v.DecodeLocal(wire(t, envelope), nativecontract.LocalProtocol, "input", ctx)
		requireReason(t, err, nativecontract.ReasonIdentityMismatch)
	})
	t.Run("gapless exact generation", func(t *testing.T) {
		guard := nativecontract.SequenceGuard{Binding: b, Generation: 1, Next: 1}
		if err := guard.Accept(delta); err != nil {
			t.Fatal(err)
		}
		requireReason(t, guard.Accept(delta), nativecontract.ReasonInvalidInput)
		if guard.Next != 2 {
			t.Fatal("failed sequence moved the guard")
		}
		next := delta
		next.Seq = "2"
		next.Sequence = "2"
		next.Generation = 2
		requireReason(t, guard.Accept(next), nativecontract.ReasonWrongGeneration)
		next.Generation = 1
		if err := guard.Accept(next); err != nil {
			t.Fatal(err)
		}
		last := nativecontract.SequenceGuard{Binding: b, Generation: 1, Next: math.MaxUint64}
		next.Seq = "18446744073709551615"
		next.Sequence = next.Seq
		if err := last.Accept(next); err != nil {
			t.Fatal(err)
		}
		requireReason(t, last.Accept(next), nativecontract.ReasonInvalidInput)
		id, err := nativecontract.EventID(1, math.MaxUint64)
		if err != nil {
			t.Fatal(err)
		}
		gen, seq, err := nativecontract.ParseEventID(id)
		if err != nil || gen != 1 || seq != math.MaxUint64 {
			t.Fatal("event ID lost its sequence")
		}
		_, _, err = nativecontract.ParseEventID("01:2")
		requireReason(t, err, nativecontract.ReasonInvalidInput)
	})
	t.Run("agent terminal boundary and projection", func(t *testing.T) {
		terminal := frame(t, "terminal", nativecontract.TerminalFrame{TerminalEpoch: testID, Sequence: "1", DataBase64: "eA==", Rows: 24, Cols: 80, Reset: false})
		terminalCaps := caps
		terminalCaps.TerminalView = true
		c := ctx
		c.PrincipalKind = "agent"
		c.Mode = "terminal"
		c.State = "taken"
		c.View = "terminal"
		c.Capabilities = &terminalCaps
		requireReason(t, v.ValidateBoundJSON("NativeStreamEvent", wire(t, terminal), c), nativecontract.ReasonWrongPrincipal)
		c.Private = true
		publication := nativecontract.Publish{RequestID: testID, ExpectedBinding: b, Events: []nativecontract.StreamEvent{terminal}, Snapshot: nil}
		if err := v.ValidateBoundJSON("NativePublish", wire(t, publication), c); err != nil {
			t.Fatalf("private paired producer cannot publish: %v", err)
		}
		requireReason(t, v.ValidateBoundJSON("NativeStreamEvent", wire(t, terminal), c), nativecontract.ReasonWrongPrincipal)
		viewerURL := "https://viewer.example.test/session"
		projected := nativecontract.PersonProjection(nativecontract.SessionView{TerminalViewerURL: &viewerURL, Capabilities: terminalCaps}, "agent", true)
		if projected.TerminalViewerURL != nil || projected.Capabilities.TerminalView || projected.Capabilities.TakeSupported {
			t.Fatal("agent projection exports a terminal capability or URL")
		}
	})
	t.Run("nullable start receipt is not private evidence", func(t *testing.T) {
		receipt := nativecontract.Receipt{RequestID: testID, OperationID: testID, Operation: "start", State: "accepted", SourceSequence: "0", UpdatedAt: valid.ExpiresAt}
		if err := v.ValidateJSON("NativeReceipt", wire(t, receipt)); err != nil {
			t.Fatal(err)
		}
		c := ctx
		c.Private = true
		requireReason(t, v.ValidateBoundJSON("NativeReceipt", wire(t, receipt), c), nativecontract.ReasonIdentityMismatch)
		receipt.State = "delivered"
		requireReason(t, v.ValidateJSON("NativeReceipt", wire(t, receipt)), nativecontract.ReasonUnsupported)
	})
	t.Run("typed command has exactly one arm", func(t *testing.T) {
		if err := nativecontract.ValidateCommand(nativecontract.Command{Kind: "input", Input: &valid}); err != nil {
			t.Fatal(err)
		}
		requireReason(t, nativecontract.ValidateCommand(nativecontract.Command{Kind: "input"}), nativecontract.ReasonUnsupported)
		requireReason(t, nativecontract.ValidateCommand(nativecontract.Command{Kind: "input", Input: &valid, Stop: &force}), nativecontract.ReasonInvalidInput)
	})
	t.Run("local authority refuses wire serialization", func(t *testing.T) {
		for _, value := range []any{nativecontract.Actor{}, nativecontract.DaemonActor{}, nativecontract.LocalProfile{}, nativecontract.LaunchSpec{}, nativecontract.PTYLaunchSpec{}, nativecontract.ToolCapability{Token: []byte("test-only-proof")}} {
			if _, err := json.Marshal(value); err == nil {
				t.Fatalf("local-only %T serialized", value)
			}
		}
		proof := []byte("test-only-proof")
		capability := nativecontract.ToolCapability{Token: proof}
		if err := capability.Close(); err != nil {
			t.Fatal(err)
		}
		for _, b := range proof {
			if b != 0 {
				t.Fatal("closed tool proof was not erased")
			}
		}
	})
}

// The authored area, DTO declarations and pin expansion must share one exact
// source. This also proves legacy pins remain unchanged by the additive area.
func TestNativeContractAreaParity(t *testing.T) {
	data, err := os.ReadFile("contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Schemas map[string]any `json:"schemas"`
	}
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	area, err := os.ReadFile("../../api/areas/native-sessions.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]any `yaml:"schemas"`
		} `yaml:"components"`
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(area, &doc); err != nil {
		t.Fatal(err)
	}
	normalized, err := json.Marshal(doc.Components.Schemas)
	if err != nil {
		t.Fatal(err)
	}
	var actual map[string]any
	if err := json.Unmarshal(normalized, &actual); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(spec.Schemas, actual) {
		t.Fatal("area schema differs from frozen contract")
	}
	for _, route := range nativecontract.PublicRoutes() {
		path := strings.TrimPrefix(route.Path, "/api")
		operation, ok := doc.Paths[path][strings.ToLower(route.Method)].(map[string]any)
		if !ok || operation["operationId"] != route.OperationID {
			t.Fatalf("missing/wrong authored route: %s %s", route.Method, path)
		}
	}
	bundle, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	pin, err := reportercontract.NativeSessionPin(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if pin.Version != nativecontract.ContractVersion || len(pin.SHA256) != 64 || !bytes.Contains(pin.Shape, []byte("assistant.final")) || !bytes.Contains(pin.Shape, []byte("text/event-stream")) {
		t.Fatal("native pin omits stream variants or version")
	}
	var previous map[string]reportercontract.Pin
	pins, err := os.ReadFile("../reportercontract/pins.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(pins, &previous); err != nil {
		t.Fatal(err)
	}
	current, err := reportercontract.Current(bundle)
	if err != nil {
		t.Fatal(err)
	}
	for name, old := range previous {
		if now, ok := current[name]; !ok || old.SHA256 != now.SHA256 || old.Version != now.Version {
			t.Fatalf("changed legacy pin %s", name)
		}
	}
}
