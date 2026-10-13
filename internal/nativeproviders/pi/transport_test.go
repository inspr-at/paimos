// SPDX-License-Identifier: AGPL-3.0-only

package pi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	nc "github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativeproviders/acp"
)

const fixtureID = "11111111-1111-4111-8111-111111111111"

type fixtureWire struct {
	identity                                                 nc.NativeIdentity
	observe                                                  func(json.RawMessage)
	requests                                                 []map[string]any
	wrongID, wrongSession, wrongModel, revoked, groupPresent bool
	interaction                                              bool
}

func (w *fixtureWire) Identity() nc.NativeIdentity      { return w.identity }
func (w *fixtureWire) Observe(fn func(json.RawMessage)) { w.observe = fn }
func (w *fixtureWire) Verify(ctx context.Context, _ nc.LaunchSpec) error {
	if w.revoked {
		return errors.New("revoked")
	}
	return ctx.Err()
}
func (w *fixtureWire) Notify(context.Context, nc.LaunchSpec, json.RawMessage) error {
	return errors.New("unqualified Pi notification")
}
func (w *fixtureWire) Request(ctx context.Context, spec nc.LaunchSpec, raw json.RawMessage, written func()) (json.RawMessage, error) {
	if err := w.Verify(ctx, spec); err != nil {
		return nil, err
	}
	var frame map[string]any
	if err := json.Unmarshal(raw, &frame); err != nil {
		return nil, err
	}
	w.requests = append(w.requests, frame)
	if written != nil {
		written()
	}
	var data any
	switch frame["type"] {
	case "get_state":
		session, model := "fixture-session", "fixture-model"
		if w.wrongSession {
			session = "other-session"
		}
		if w.wrongModel {
			model = "other-model"
		}
		data = map[string]any{"sessionId": session, "model": map[string]string{"provider": "openrouter", "id": model}, "thinkingLevel": "high"}
	case "prompt":
		if w.interaction {
			w.observe([]byte(`{"type":"extension_ui_request","id":"private-question","method":"confirm","message":"PRIVATE_SENTINEL"}`))
		}
	default:
		return nil, errors.New("unqualified method")
	}
	id := frame["id"]
	if w.wrongID {
		id = "wrong-command"
	}
	return json.Marshal(map[string]any{"type": "response", "id": id, "command": frame["type"], "success": true, "data": data})
}
func (w *fixtureWire) Close(context.Context) (nc.ExitEvidence, error) {
	return nc.ExitEvidence{ProcessIdentityDigest: w.identity.ProcessIdentityDigest, RootExited: true, GroupAbsent: !w.groupPresent, ObservedAt: w.identity.StartedAt.Add(time.Second)}, nil
}
func (w *fixtureWire) Wait(ctx context.Context) (nc.ExitEvidence, error) { return w.Close(ctx) }

func fixture(t *testing.T) (acp.Qualification, nc.LaunchSpec, *fixtureWire, time.Time) {
	t.Helper()
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	root := t.TempDir()
	q := acp.Qualification{Harness: "pi", Binary: nc.BinaryPin{Path: filepath.Join(root, "bin", "pi"), SHA256: acp.Digest("fixture-pi-binary"), Version: "fixture-pi-1", Protocol: Protocol}, ToolPolicyDigest: acp.Digest("bounded-tools"), SettingsPolicyDigest: acp.Digest("isolated-settings")}
	account := filepath.Join(root, "account")
	effort := "high"
	binding := nc.Binding{TenantID: fixtureID, ProjectID: fixtureID, OwnedSessionID: fixtureID, HarnessSessionID: fixtureID, RuntimeEpoch: fixtureID, DaemonID: "fixture-daemon", DaemonGeneration: "fixture-generation", BindingRevision: 2, RunID: text(fixtureID)}
	spec := nc.LaunchSpec{Binding: binding, Profile: nc.LocalProfile{AccountID: fixtureID, AccountHome: account, UserHome: filepath.Join(root, "user"), WorkspaceRoot: filepath.Join(root, "work"), Binary: q.Binary, Environment: []string{"HOME=" + filepath.Join(root, "user"), "PI_CODING_AGENT_DIR=" + account}}, Policy: nc.Policy{GrantID: fixtureID, GrantRevision: 1, ExpiresAt: now.Add(time.Hour), ToolPolicyDigest: q.ToolPolicyDigest, SettingsPolicyDigest: q.SettingsPolicyDigest, PermissionPolicy: "supervised", LocalHistory: "disabled", Continuation: "manual", MaximumRuntimeSeconds: 600, MaximumTurns: 10, MaximumTokens: 10000}, Cwd: filepath.Join(root, "work"), Mode: "structured", OwnedWriterID: "fixture-owner", Lifetime: context.Background(), Options: nc.LaunchConfig{ModelProfileID: fixtureID, Model: "openrouter/fixture-model", Effort: &effort, ServiceTier: "default", ToolPolicyDigest: q.ToolPolicyDigest, SettingsPolicyDigest: q.SettingsPolicyDigest}}
	w := &fixtureWire{identity: nc.NativeIdentity{Provider: "pi", RuntimeEpoch: fixtureID, ProcessIdentityDigest: acp.Digest("owned-process"), PID: 42, StartedAt: now}}
	return q, spec, w, now
}
func input(spec nc.LaunchSpec, mode string, now time.Time) nc.Input {
	return nc.Input{RequestID: fixtureID, InputID: fixtureID, ExpectedBinding: spec.Binding, Mode: mode, Text: "bounded input", ExpiresAt: now.Add(time.Minute)}
}
func reason(t *testing.T, err error, want nc.Reason) {
	t.Helper()
	var r *nc.ValidationError
	if !errors.As(err, &r) || r.Reason != want {
		t.Fatalf("wrong failure: got %v want %s", err, want)
	}
}

// Risk: a Pi command acknowledgement is not evidence that queued text was
// consumed or that the next assistant message answered a specific chat input.
func TestPiQualificationMatrix(t *testing.T) {
	q, spec, w, now := fixture(t)
	row := Matrix(q)
	caps := row.Capabilities
	if !row.LivePersistence || row.Resume || row.Approvals || row.ForceExit || row.Confined || !row.GracefulExit || row.BusyInput != "next_step" || row.FinalCorrelation != "uncorrelated" || row.Quota != "unsupported" {
		t.Fatal("dishonest matrix", row)
	}
	if !caps.Input || caps.Steer != "next_step" || !caps.Stop || !caps.EventsSupported || caps.ChatSupported || caps.CorrelatedReceipts || caps.Interrupt || caps.InterruptSupported || caps.Resume || caps.Take || caps.TakeSupported || caps.TerminalView || caps.Expert || caps.Approvals || caps.Questions {
		t.Fatal("capability drift", caps)
	}
	if err := acp.ValidateDTO("NativeCapabilities", caps); err != nil {
		t.Fatal(err)
	}
	var argv []string
	l := Launcher{Qualification: q, Now: func() time.Time { return now }, OpenWire: func(_ context.Context, got nc.LaunchSpec, args []string) (acp.Wire, error) {
		argv = args
		if got.Policy != spec.Policy || got.Profile.AccountID != spec.Profile.AccountID {
			t.Fatal("policy/account drift")
		}
		return w, nil
	}}
	transport, err := l.Open(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"--mode rpc", "--no-session", "--no-extensions", "--no-skills", "--no-prompt-templates"} {
		if !strings.Contains(strings.Join(argv, " "), flag) {
			t.Fatal("native safety flag missing", flag)
		}
	}
	for _, mode := range []string{"now", "after_turn"} {
		e, err := transport.Send(context.Background(), input(spec, mode, now))
		if err != nil || e.Stage != "write_acknowledged" || e.TurnRef != "" {
			t.Fatal("false delivery evidence", e, err)
		}
	}
	var behaviors []any
	for _, request := range w.requests {
		if request["type"] == "prompt" {
			behaviors = append(behaviors, request["streamingBehavior"])
		}
	}
	if len(behaviors) != 2 || behaviors[0] != "steer" || behaviors[1] != "followUp" {
		t.Fatal("native queue semantics", behaviors)
	}
	v, err := nc.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(input(spec, "now", now))
	reason(t, v.ValidateBoundJSON("NativeInput", raw, nc.ValidationContext{Binding: &spec.Binding, Capabilities: &caps}), nc.ReasonUnsupported)
	_, err = transport.Interrupt(context.Background(), nc.Interrupt{})
	reason(t, err, nc.ReasonUnsupported)
	_, err = transport.Decide(context.Background(), nc.ApprovalDecision{})
	reason(t, err, nc.ReasonUnsupported)
	frames, err := os.ReadFile("testdata/events.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	for _, frame := range bytes.Split(bytes.TrimSpace(frames), []byte("\n")) {
		w.observe(frame)
	}
	p := transport.(*Transport)
	var projected []nc.StructuredFrame
	for len(p.events) > 0 {
		e := <-p.events
		if e.Final != nil || e.Approval != nil || e.Quota != nil || e.Expert != nil {
			t.Fatal("uncorrelated final/private payload projected")
		}
		if err := acp.ValidateDTO("NativeStructuredFrame", e.Structured); err != nil {
			t.Fatal(err)
		}
		projected = append(projected, *e.Structured)
	}
	encoded, _ := json.Marshal(projected)
	if bytes.Contains(encoded, []byte("PRIVATE_SENTINEL")) || bytes.Contains(encoded, []byte("private-tool-id")) || !bytes.Contains(encoded, []byte("Hello ")) || len(projected) != 5 {
		t.Fatalf("unsafe/missing live projection: %s", encoded)
	}
	w.groupPresent = true
	_, err = transport.Close(context.Background())
	reason(t, err, nc.ReasonOwnershipUnconfirmed)
	w.groupPresent = false
	exit, err := transport.Wait(context.Background())
	if err != nil || !exit.GroupAbsent {
		t.Fatal("exit proof", err)
	}
	q.Binary.SHA256 = ""
	if Matrix(q).Capabilities.Input {
		t.Fatal("unqualified pin advertised")
	}
}

func TestPiRefusesUnprovedNativeActions(t *testing.T) {
	for _, scenario := range []string{"resume", "confined", "native history", "tool policy", "pin drift", "wrong response id", "account model drift", "session drift", "revocation", "extension question"} {
		t.Run(scenario, func(t *testing.T) {
			q, spec, w, now := fixture(t)
			opened := false
			l := Launcher{Qualification: q, Now: func() time.Time { return now }, OpenWire: func(context.Context, nc.LaunchSpec, []string) (acp.Wire, error) { opened = true; return w, nil }}
			want := nc.ReasonUnsupported
			prestart := true
			switch scenario {
			case "resume":
				spec.ResumeRef = "saved"
			case "confined":
				spec.Mode = "confined"
			case "native history":
				spec.Policy.LocalHistory = "vendor_native"
			case "tool policy":
				spec.Options.ToolPolicyDigest = acp.Digest("other-policy")
				want = nc.ReasonPermissionRevoked
			case "pin drift":
				spec.Profile.Binary.Version = "other"
				want = nc.ReasonPinInvalid
			default:
				prestart = false
			}
			transport, err := l.Open(context.Background(), spec)
			if prestart {
				reason(t, err, want)
				if opened {
					t.Fatal("unsafe fork")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "wrong response id":
				w.wrongID = true
				want = nc.ReasonInputOutcomeUnconfirmed
			case "account model drift":
				w.wrongModel = true
				want = nc.ReasonIdentityMismatch
			case "session drift":
				w.wrongSession = true
				want = nc.ReasonIdentityMismatch
			case "revocation":
				w.revoked = true
				want = nc.ReasonPermissionRevoked
			case "extension question":
				w.interaction = true
			}
			_, err = transport.Send(context.Background(), input(spec, "now", now))
			reason(t, err, want)
			if scenario == "extension question" {
				for _, r := range w.requests {
					if r["type"] == "extension_ui_response" {
						t.Fatal("question was auto-answered")
					}
				}
			}
		})
	}
}
