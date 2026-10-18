// SPDX-License-Identifier: AGPL-3.0-only

package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	nc "github.com/inspr-at/paimos/internal/nativecontract"
)

const testID = "11111111-1111-4111-8111-111111111111"

type fixtureWire struct {
	mu           sync.Mutex
	identity     nc.NativeIdentity
	observe      func(json.RawMessage)
	requests     []map[string]any
	start        chan struct{}
	release      chan struct{}
	stopReason   string
	wrongID      bool
	permission   bool
	revoked      bool
	groupPresent bool
	model        string
	cancel       chan struct{}
}

func (w *fixtureWire) Identity() nc.NativeIdentity {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.identity
}
func (w *fixtureWire) Observe(fn func(json.RawMessage)) { w.observe = fn }
func (w *fixtureWire) Verify(ctx context.Context, spec nc.LaunchSpec) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.revoked {
		return errors.New("revoked")
	}
	return ctx.Err()
}
func (w *fixtureWire) Request(ctx context.Context, spec nc.LaunchSpec, raw json.RawMessage, written func()) (json.RawMessage, error) {
	if err := w.Verify(ctx, spec); err != nil {
		return nil, err
	}
	var frame map[string]any
	if err := json.Unmarshal(raw, &frame); err != nil {
		return nil, err
	}
	w.mu.Lock()
	w.requests = append(w.requests, frame)
	w.mu.Unlock()
	if written != nil {
		written()
	}
	var result any
	switch frame["method"] {
	case "initialize":
		result = map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]bool{"loadSession": true}}
	case "session/new":
		result = map[string]any{"sessionId": "fixture-session", "models": map[string]string{"currentModelId": w.model}}
	case "session/set_config_option":
		params := frame["params"].(map[string]any)
		result = map[string]any{"configOptions": []map[string]any{{"id": "model", "currentValue": w.model}, {"id": params["configId"], "currentValue": params["value"]}}}
	case "session/prompt":
		if w.start != nil {
			w.start <- struct{}{}
		}
		if w.permission {
			w.observe([]byte(`{"jsonrpc":"2.0","id":900,"method":"session/request_permission","params":{"sessionId":"fixture-session","options":[{"optionId":"always","kind":"allow_always"}]}}`))
		}
		if w.release != nil {
			select {
			case <-w.release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		result = map[string]any{"stopReason": w.stopReason, "final": "PRIVATE_SENTINEL"}
	default:
		return nil, errors.New("unqualified method")
	}
	id := frame["id"]
	if w.wrongID {
		id = float64(99999)
	}
	return json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}
func (w *fixtureWire) Notify(ctx context.Context, spec nc.LaunchSpec, raw json.RawMessage) error {
	if err := w.Verify(ctx, spec); err != nil {
		return err
	}
	var frame map[string]any
	if err := json.Unmarshal(raw, &frame); err != nil {
		return err
	}
	w.mu.Lock()
	w.requests = append(w.requests, frame)
	w.mu.Unlock()
	if frame["method"] == "session/cancel" && w.cancel != nil {
		close(w.cancel)
	}
	return nil
}
func (w *fixtureWire) Close(context.Context) (nc.ExitEvidence, error) {
	id := w.Identity()
	return nc.ExitEvidence{ProcessIdentityDigest: id.ProcessIdentityDigest, RootExited: true, GroupAbsent: !w.groupPresent, ObservedAt: id.StartedAt.Add(time.Second)}, nil
}
func (w *fixtureWire) Wait(ctx context.Context) (nc.ExitEvidence, error) { return w.Close(ctx) }

func fixture(t *testing.T, harness string) (Qualification, nc.LaunchSpec, *fixtureWire, *atomic.Int64) {
	t.Helper()
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	clock := &atomic.Int64{}
	clock.Store(now.UnixNano())
	root := t.TempDir()
	q := Qualification{Harness: harness, Binary: nc.BinaryPin{Path: filepath.Join(root, "bin", harness), SHA256: Digest("fixture-binary-" + harness), Version: "fixture-" + harness + "-1", Protocol: Protocol}, ToolPolicyDigest: Digest("bounded-tools"), SettingsPolicyDigest: Digest("isolated-settings")}
	binding := nc.Binding{TenantID: testID, ProjectID: testID, OwnedSessionID: testID, HarnessSessionID: testID, RuntimeEpoch: testID, DaemonID: "fixture-daemon", DaemonGeneration: "fixture-generation", BindingRevision: 2, RunID: str(testID)}
	model, effort := "fixture-model", (*string)(nil)
	if harness == "gemini" {
		model, effort = "gemini-2.5-pro", str("16384")
	}
	if harness == "opencode" {
		model, effort = "fixture/model", str("high")
	}
	account := filepath.Join(root, "account")
	key := map[string]string{"cursor": "CURSOR_CONFIG_DIR", "grok": "GROK_HOME", "gemini": "GEMINI_CLI_HOME", "opencode": "XDG_CONFIG_HOME"}[harness]
	spec := nc.LaunchSpec{Binding: binding, Profile: nc.LocalProfile{AccountID: testID, AccountHome: account, UserHome: filepath.Join(root, "user"), WorkspaceRoot: filepath.Join(root, "work"), Binary: q.Binary, Environment: []string{"HOME=" + filepath.Join(root, "user"), key + "=" + account}}, Policy: nc.Policy{GrantID: testID, GrantRevision: 1, ExpiresAt: now.Add(time.Hour), ToolPolicyDigest: q.ToolPolicyDigest, SettingsPolicyDigest: q.SettingsPolicyDigest, PermissionPolicy: "supervised", LocalHistory: "disabled", Continuation: "manual", MaximumRuntimeSeconds: 600, MaximumTurns: 10, MaximumTokens: 10000}, Cwd: filepath.Join(root, "work"), Mode: "structured", OwnedWriterID: "fixture-owner", Lifetime: context.Background(), Options: nc.LaunchConfig{ModelProfileID: testID, Model: model, Effort: effort, ServiceTier: "default", ToolPolicyDigest: q.ToolPolicyDigest, SettingsPolicyDigest: q.SettingsPolicyDigest}}
	if harness == "gemini" {
		spec.Profile.Environment = append(spec.Profile.Environment, "GEMINI_CLI_SYSTEM_SETTINGS_PATH="+filepath.Join(account, "settings.json"))
	}
	w := &fixtureWire{identity: nc.NativeIdentity{Provider: harness, RuntimeEpoch: testID, ProcessIdentityDigest: Digest("owned-process"), PID: 42, StartedAt: now}, model: model, stopReason: "end_turn"}
	return q, spec, w, clock
}

func input(spec nc.LaunchSpec, mode string, expires time.Time) nc.Input {
	return nc.Input{RequestID: testID, InputID: testID, ExpectedBinding: spec.Binding, Mode: mode, Text: "bounded input", ExpiresAt: expires}
}
func reason(t *testing.T, err error, expected nc.Reason) {
	t.Helper()
	var r *nc.ValidationError
	if !errors.As(err, &r) || r.Reason != expected {
		t.Fatalf("wrong failure: got %v want %s", err, expected)
	}
}
func wait(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("fixture barrier did not arrive")
	}
}

// Risk: protocol fixtures must not advertise unproved actions or widen confined
// ceilings. Real subscriptions, credentials and model binaries are never used.
func TestACPQualificationMatrix(t *testing.T) {
	for _, harness := range []string{"cursor", "grok", "gemini", "opencode"} {
		t.Run(harness, func(t *testing.T) {
			q, spec, w, clock := fixture(t, harness)
			now := func() time.Time { return time.Unix(0, clock.Load()) }
			row := Matrix(q)
			if !row.LivePersistence || row.Resume || row.Approvals || row.ForceExit || row.Confined || row.FinalCorrelation != "prompt_response_only" || row.BusyInput != "queued" || row.Quota != "unsupported" {
				t.Fatal("dishonest matrix", row)
			}
			caps := row.Capabilities
			if !caps.Input || !caps.ChatSupported || !caps.CorrelatedReceipts || !caps.Stop || !caps.EventsSupported || caps.Interrupt != (harness != "grok") || caps.InterruptSupported != caps.Interrupt || caps.Resume || caps.Take || caps.TakeSupported || caps.TerminalView || caps.Expert || caps.Approvals || caps.Questions {
				t.Fatal("capability drift", caps)
			}
			if err := ValidateDTO("NativeCapabilities", caps); err != nil {
				t.Fatal(err)
			}
			var argv []string
			l := Launcher{Qualification: q, Now: now, OpenWire: func(_ context.Context, got nc.LaunchSpec, args []string) (Wire, error) {
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
			p := transport.(*Transport)
			for _, arg := range argv {
				if strings.Contains(arg, "approve") || arg == "--trust" || arg == "dontAsk" {
					t.Fatal("approval bypass")
				}
			}
			w.start, w.release = make(chan struct{}, 1), make(chan struct{})
			result := make(chan error, 1)
			go func() {
				e, err := p.Send(context.Background(), input(spec, "now", now().Add(time.Minute)))
				if err == nil && (e.Stage != "turn_completed" || e.TurnRef == "" || e.InputID != testID) {
					err = errors.New("false receipt")
				}
				result <- err
			}()
			wait(t, w.start)
			_, err = p.Send(context.Background(), input(spec, "now", now().Add(time.Minute)))
			reason(t, err, nc.ReasonUnsupported)
			raw, err := os.ReadFile("testdata/events.jsonl")
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
				w.observe(line)
			}
			_, err = p.Decide(context.Background(), nc.ApprovalDecision{})
			reason(t, err, nc.ReasonUnsupported)
			if harness == "grok" {
				_, err = p.Interrupt(context.Background(), nc.Interrupt{})
				reason(t, err, nc.ReasonUnsupported)
			}
			close(w.release)
			if err := <-result; err != nil {
				t.Fatal(err)
			}
			var output []nc.StructuredFrame
			for len(p.events) > 0 {
				e := <-p.events
				if e.Final != nil || e.Approval != nil || e.Quota != nil || e.Expert != nil {
					t.Fatal("unqualified projection")
				}
				if err := ValidateDTO("NativeStructuredFrame", e.Structured); err != nil {
					t.Fatal(err)
				}
				output = append(output, *e.Structured)
			}
			encoded, _ := json.Marshal(output)
			if bytes.Contains(encoded, []byte("PRIVATE_SENTINEL")) || bytes.Contains(encoded, []byte("private-native-id")) || !bytes.Contains(encoded, []byte("Hello ")) || len(output) != 4 {
				t.Fatalf("projection not bounded/filtered: %s", encoded)
			}
			w.groupPresent = true
			_, err = p.Close(context.Background())
			reason(t, err, nc.ReasonOwnershipUnconfirmed)
			w.groupPresent = false
			exit, err := p.Wait(context.Background())
			if err != nil || !exit.GroupAbsent {
				t.Fatal("exit proof", err)
			}
		})
	}
	t.Run("pre-fork safety ceilings", func(t *testing.T) {
		q, spec, _, clock := fixture(t, "cursor")
		mutations := []struct {
			name   string
			change func(*nc.LaunchSpec)
			reason nc.Reason
		}{
			{"resume", func(s *nc.LaunchSpec) { s.ResumeRef = "saved-conversation" }, nc.ReasonUnsupported},
			{"initial input before event consumer", func(s *nc.LaunchSpec) { v := input(*s, "now", s.Policy.ExpiresAt); s.InitialInput = &v }, nc.ReasonUnsupported},
			{"native history", func(s *nc.LaunchSpec) { s.Policy.LocalHistory = "vendor_native" }, nc.ReasonUnsupported},
			{"confined", func(s *nc.LaunchSpec) { s.Mode = "confined" }, nc.ReasonUnsupported},
			{"routine", func(s *nc.LaunchSpec) { s.Policy.Continuation = "bounded_preapproved" }, nc.ReasonUnsupported},
			{"approval bypass", func(s *nc.LaunchSpec) { s.Policy.PermissionPolicy = "bounded_preapproved" }, nc.ReasonUnsupported},
			{"tool digest", func(s *nc.LaunchSpec) { s.Policy.ToolPolicyDigest = Digest("other-policy") }, nc.ReasonPermissionRevoked},
			{"binary pin", func(s *nc.LaunchSpec) { s.Profile.Binary.Version = "other-version" }, nc.ReasonPinInvalid},
			{"ambient credentials", func(s *nc.LaunchSpec) {
				s.Profile.Environment = append(s.Profile.Environment, "VENDOR_API_KEY=synthetic")
			}, nc.ReasonUnsupported},
			{"account directory", func(s *nc.LaunchSpec) { s.Profile.Environment[1] = "CURSOR_CONFIG_DIR=/outside-account" }, nc.ReasonIdentityMismatch},
			{"missing account environment", func(s *nc.LaunchSpec) { s.Profile.Environment = nil }, nc.ReasonIdentityMismatch},
		}
		for _, tc := range mutations {
			t.Run(tc.name, func(t *testing.T) {
				copy := CloneSpec(spec)
				tc.change(&copy)
				opened := false
				l := Launcher{Qualification: q, Now: func() time.Time { return time.Unix(0, clock.Load()) }, OpenWire: func(context.Context, nc.LaunchSpec, []string) (Wire, error) { opened = true; return nil, nil }}
				_, err := l.Open(context.Background(), copy)
				reason(t, err, tc.reason)
				if opened {
					t.Fatal("unsafe fork")
				}
			})
		}
		q.Binary.SHA256 = ""
		if Matrix(q).Capabilities.Input {
			t.Fatal("unqualified pin advertised")
		}
		q.Harness = "future"
		if Matrix(q).Capabilities.Input {
			t.Fatal("unknown provider advertised")
		}
	})
}

type queueContext struct {
	context.Context
	reached chan struct{}
	once    sync.Once
}

func (c *queueContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.reached) })
	return c.Context.Done()
}

func TestACPQueueRevalidatesAtCleanBoundary(t *testing.T) {
	q, spec, w, clock := fixture(t, "cursor")
	now := func() time.Time { return time.Unix(0, clock.Load()) }
	l := Launcher{Qualification: q, Now: now, OpenWire: func(context.Context, nc.LaunchSpec, []string) (Wire, error) { return w, nil }}
	transport, err := l.Open(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	p := transport.(*Transport)
	w.start, w.release = make(chan struct{}, 2), make(chan struct{})
	first, second := make(chan error, 1), make(chan error, 1)
	go func() {
		_, err := p.Send(context.Background(), input(spec, "now", now().Add(time.Minute)))
		first <- err
	}()
	wait(t, w.start)
	ctx := &queueContext{Context: context.Background(), reached: make(chan struct{})}
	queued := input(spec, "after_turn", now().Add(time.Second))
	go func() { _, err := p.Send(ctx, queued); second <- err }()
	wait(t, ctx.reached) // Proves second input reached the occupied turn gate.
	clock.Add(int64(2 * time.Second))
	close(w.release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	reason(t, <-second, nc.ReasonGrantExpired)
	w.mu.Lock()
	defer w.mu.Unlock()
	prompts := 0
	for _, r := range w.requests {
		if r["method"] == "session/prompt" {
			prompts++
		}
	}
	if prompts != 1 {
		t.Fatal("expired queued input written", prompts)
	}
}

func TestACPProtocolFailuresHoldOwnership(t *testing.T) {
	for _, scenario := range []string{"wrong response id", "permission", "revocation", "identity drift", "unknown stop reason"} {
		t.Run(scenario, func(t *testing.T) {
			q, spec, w, clock := fixture(t, "cursor")
			now := func() time.Time { return time.Unix(0, clock.Load()) }
			l := Launcher{Qualification: q, Now: now, OpenWire: func(context.Context, nc.LaunchSpec, []string) (Wire, error) { return w, nil }}
			transport, err := l.Open(context.Background(), spec)
			if err != nil {
				t.Fatal(err)
			}
			want := nc.ReasonInputOutcomeUnconfirmed
			switch scenario {
			case "wrong response id":
				w.wrongID = true
			case "permission":
				w.permission = true
				want = nc.ReasonUnsupported
			case "revocation":
				w.revoked = true
				want = nc.ReasonPermissionRevoked
			case "identity drift":
				w.identity.PID++
				want = nc.ReasonIdentityMismatch
			case "unknown stop reason":
				w.stopReason = "limit_unknown"
			}
			_, err = transport.Send(context.Background(), input(spec, "now", now().Add(time.Minute)))
			reason(t, err, want)
			if scenario == "permission" {
				w.mu.Lock()
				defer w.mu.Unlock()
				raw, _ := json.Marshal(w.requests)
				if bytes.Contains(raw, []byte("allow_always")) || !bytes.Contains(raw, []byte("cancelled")) {
					t.Fatal("permission was not visibly refused")
				}
			}
		})
	}
}

func TestACPInterruptMatchesCurrentTurn(t *testing.T) {
	for _, harness := range []string{"cursor", "gemini", "opencode"} {
		t.Run(harness, func(t *testing.T) {
			q, spec, w, clock := fixture(t, harness)
			now := func() time.Time { return time.Unix(0, clock.Load()) }
			l := Launcher{Qualification: q, Now: now, OpenWire: func(context.Context, nc.LaunchSpec, []string) (Wire, error) { return w, nil }}
			transport, err := l.Open(context.Background(), spec)
			if err != nil {
				t.Fatal(err)
			}
			w.start, w.cancel = make(chan struct{}, 1), make(chan struct{})
			w.release = w.cancel
			w.stopReason = "cancelled"
			finished := make(chan error, 1)
			go func() {
				_, err := transport.Send(context.Background(), input(spec, "now", now().Add(time.Minute)))
				finished <- err
			}()
			wait(t, w.start)
			interrupt := nc.Interrupt{RequestID: testID, ExpectedBinding: spec.Binding, ExpectedTurnRef: "wrong-turn", ExpiresAt: now().Add(time.Minute)}
			_, err = transport.Interrupt(context.Background(), interrupt)
			reason(t, err, nc.ReasonIdentityMismatch)
			interrupt.ExpectedTurnRef = transport.Identity().NativeTurnRef
			evidence, err := transport.Interrupt(context.Background(), interrupt)
			if err != nil || evidence.Stage != "applied" || evidence.TurnRef != interrupt.ExpectedTurnRef {
				t.Fatal("unconfirmed interrupt", evidence, err)
			}
			if err := <-finished; err != nil {
				t.Fatal(err)
			}
			_, err = transport.Interrupt(context.Background(), interrupt)
			reason(t, err, nc.ReasonIdentityMismatch)
		})
	}
}

// Risk: a cancel write racing normal prompt completion must not claim that the
// native agent cancelled the turn. The cancel barrier proves the write came
// before the end_turn response; only native cancelled evidence proves success.
func TestACPInterruptRequiresNativeCancellation(t *testing.T) {
	for _, harness := range []string{"cursor", "gemini", "opencode"} {
		t.Run(harness, func(t *testing.T) {
			q, spec, w, clock := fixture(t, harness)
			now := func() time.Time { return time.Unix(0, clock.Load()) }
			l := Launcher{Qualification: q, Now: now, OpenWire: func(context.Context, nc.LaunchSpec, []string) (Wire, error) { return w, nil }}
			transport, err := l.Open(context.Background(), spec)
			if err != nil {
				t.Fatal(err)
			}
			w.start, w.cancel = make(chan struct{}, 1), make(chan struct{})
			w.release = w.cancel
			type sendResult struct {
				evidence nc.NativeEvidence
				err      error
			}
			finished := make(chan sendResult, 1)
			go func() {
				evidence, err := transport.Send(context.Background(), input(spec, "now", now().Add(time.Minute)))
				finished <- sendResult{evidence, err}
			}()
			wait(t, w.start)
			interrupt := nc.Interrupt{RequestID: testID, ExpectedBinding: spec.Binding, ExpectedTurnRef: transport.Identity().NativeTurnRef, ExpiresAt: now().Add(time.Minute)}
			evidence, err := transport.Interrupt(context.Background(), interrupt)
			wait(t, w.cancel) // Normal completion is released only by a cancel write.
			reason(t, err, nc.ReasonInputOutcomeUnconfirmed)
			if evidence.Stage != "" {
				t.Fatal("normal completion produced an interrupt receipt", evidence)
			}
			select {
			case result := <-finished:
				if result.err != nil || result.evidence.Stage != "turn_completed" || result.evidence.TurnRef != interrupt.ExpectedTurnRef || result.evidence.InputID != testID || result.evidence.RequestID != testID {
					t.Fatal("normal prompt completion was lost", result.evidence, result.err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("prompt completion did not arrive")
			}
			if transport.Identity().NativeTurnRef != "" {
				t.Fatal("finished turn remains active")
			}
			_, err = transport.Interrupt(context.Background(), interrupt)
			reason(t, err, nc.ReasonIdentityMismatch)
			// An unconfirmed cancellation does not poison a normally completed
			// transport or prevent the next authorized prompt.
			next, err := transport.Send(context.Background(), input(spec, "now", now().Add(time.Minute)))
			if err != nil || next.Stage != "turn_completed" || next.TurnRef == interrupt.ExpectedTurnRef {
				t.Fatal("successor prompt did not complete", next, err)
			}
		})
	}
}

func TestACPQueuedInputWaitsForNativeCompletion(t *testing.T) {
	q, spec, w, clock := fixture(t, "cursor")
	now := func() time.Time { return time.Unix(0, clock.Load()) }
	l := Launcher{Qualification: q, Now: now, OpenWire: func(context.Context, nc.LaunchSpec, []string) (Wire, error) { return w, nil }}
	transport, err := l.Open(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	w.start, w.release = make(chan struct{}, 2), make(chan struct{})
	first, second := make(chan error, 1), make(chan error, 1)
	go func() {
		_, err := transport.Send(context.Background(), input(spec, "now", now().Add(time.Minute)))
		first <- err
	}()
	wait(t, w.start)
	ctx := &queueContext{Context: context.Background(), reached: make(chan struct{})}
	go func() {
		_, err := transport.Send(ctx, input(spec, "after_turn", now().Add(time.Minute)))
		second <- err
	}()
	wait(t, ctx.reached)
	w.mu.Lock()
	prompts := 0
	for _, r := range w.requests {
		if r["method"] == "session/prompt" {
			prompts++
		}
	}
	w.mu.Unlock()
	if prompts != 1 {
		t.Fatal("busy input written before clean boundary")
	}
	close(w.release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	wait(t, w.start)
	if err := <-second; err != nil {
		t.Fatal(err)
	}
}

func TestACPFailedOpenRetainsOwnerHandle(t *testing.T) {
	q, spec, w, clock := fixture(t, "cursor")
	l := Launcher{Qualification: q, Now: func() time.Time { return time.Unix(0, clock.Load()) }, OpenWire: func(context.Context, nc.LaunchSpec, []string) (Wire, error) {
		return w, errors.New("post-fork failure")
	}}
	transport, err := l.Open(context.Background(), spec)
	reason(t, err, nc.ReasonOwnershipUnconfirmed)
	if transport == nil {
		t.Fatal("possible child ownership was lost")
	}
	if _, err := transport.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
