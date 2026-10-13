// SPDX-License-Identifier: AGPL-3.0-only

package codex

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	n "github.com/inspr-at/paimos/internal/nativecontract"
)

const fixtureID = "11111111-1111-4111-8111-111111111111"
const fixtureDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time                       { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) After(time.Duration) <-chan time.Time { return make(chan time.Time) }
func (c *fakeClock) advance(d time.Duration)              { c.mu.Lock(); c.now = c.now.Add(d); c.mu.Unlock() }

type fakeGrants struct {
	mu       sync.Mutex
	grant    WorkerGrant
	clock    *fakeClock
	approved int
}

func (g *fakeGrants) Authorize(_ context.Context, _ n.LaunchSpec) (WorkerGrant, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.grant, nil
}
func (g *fakeGrants) Approve(_ context.Context, d n.ApprovalDecision, a n.Approval) error {
	if d.StepUpAuthorizationID == nil || *d.StepUpAuthorizationID != fixtureID || d.EffectDigest != a.EffectDigest {
		return ErrPolicy
	}
	g.mu.Lock()
	g.approved++
	g.mu.Unlock()
	return nil
}
func (g *fakeGrants) FinalDrain(_ context.Context, b n.Binding) (n.FinalDrainGrant, error) {
	return n.FinalDrainGrant{GrantID: fixtureID, Binding: b, ConversationRef: opaque(b, "thread", "thread-1"),
		ExpiresAt: g.clock.Now().Add(time.Hour), MaximumFinals: 10, MaximumBytes: 65536}, nil
}

type recordedCall struct {
	method string
	params json.RawMessage
}
type fakeWire struct {
	t            *testing.T
	mu           sync.Mutex
	frames       chan json.RawMessage
	closed       chan struct{}
	closeOnce    sync.Once
	email        string
	proof        n.NativeIdentity
	calls        []recordedCall
	requests     map[string]json.RawMessage
	threadResult json.RawMessage
	steerAck     string
	startEntered chan struct{}
	startRelease chan struct{}
	startFrames  []json.RawMessage
	exit         n.ExitEvidence
	replies      []json.RawMessage
	badPolicy    bool
}

func (w *fakeWire) record(method string, params json.RawMessage) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls = append(w.calls, recordedCall{method, append(json.RawMessage(nil), params...)})
	want, ok := w.requests[method]
	if !ok {
		w.t.Errorf("unqualified RPC method %s", method)
		return
	}
	var gotValue, wantValue any
	if json.Unmarshal(params, &gotValue) != nil || json.Unmarshal(want, &wantValue) != nil || !reflect.DeepEqual(gotValue, wantValue) {
		w.t.Errorf("%s violated the pinned parameter schema (unsupported, missing or changed field)", method)
	}
}
func (w *fakeWire) Call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	w.record(method, params)
	switch method {
	case "initialize":
		return json.RawMessage(`{"userAgent":"codex-native/0.157.1"}`), nil
	case "account/read":
		w.mu.Lock()
		email := w.email
		w.mu.Unlock()
		return encode(map[string]any{"account": map[string]string{"type": "chatgpt", "email": email}, "requiresOpenaiAuth": true}), nil
	case "thread/start", "thread/resume":
		if w.badPolicy {
			return json.RawMessage(`{"thread":{"id":"thread-1"},"model":"test-model","modelProvider":"openai","cwd":"/workspace","approvalPolicy":"never","sandbox":{"type":"readOnly"}}`), nil
		}
		return w.threadResult, nil
	case "turn/start":
		for _, f := range w.startFrames {
			w.frames <- f
		}
		if w.startEntered != nil {
			close(w.startEntered)
			select {
			case <-w.startRelease:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return json.RawMessage(`{"turn":{"id":"turn-1","status":"inProgress"}}`), nil
	case "turn/steer":
		w.mu.Lock()
		ack := w.steerAck
		w.mu.Unlock()
		return encode(map[string]string{"turnId": ack}), nil
	case "turn/interrupt":
		return json.RawMessage(`{}`), nil
	default:
		return nil, ErrProtocol
	}
}
func (w *fakeWire) Notify(_ context.Context, method string, params json.RawMessage) error {
	w.record(method, params)
	return nil
}
func (w *fakeWire) Reply(_ context.Context, id, reply json.RawMessage) error {
	if string(id) != "42" {
		w.t.Error("approval reply changed native request id")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.replies = append(w.replies, append(json.RawMessage(nil), reply...))
	return nil
}
func (w *fakeWire) Frames() <-chan json.RawMessage { return w.frames }
func (w *fakeWire) Identity() n.NativeIdentity     { w.mu.Lock(); defer w.mu.Unlock(); return w.proof }
func (w *fakeWire) Close(context.Context) (n.ExitEvidence, error) {
	w.closeOnce.Do(func() { close(w.frames); close(w.closed) })
	return w.exit, nil
}
func (w *fakeWire) Wait(ctx context.Context) (n.ExitEvidence, error) {
	select {
	case <-w.closed:
		return w.exit, nil
	case <-ctx.Done():
		return n.ExitEvidence{}, ctx.Err()
	}
}
func (w *fakeWire) send(s string) { w.frames <- json.RawMessage(s) }
func (w *fakeWire) count(method string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	v := 0
	for _, c := range w.calls {
		if c.method == method {
			v++
		}
	}
	return v
}
func (w *fakeWire) changeEmail(email string) { w.mu.Lock(); w.email = email; w.mu.Unlock() }

type fakeOpener struct {
	wire  *fakeWire
	plans []Plan
}

func (o *fakeOpener) Open(_ context.Context, p Plan) (Wire, error) {
	o.plans = append(o.plans, p)
	return o.wire, nil
}

func fixture(t *testing.T) (*Launcher, n.LaunchSpec, *fakeWire, *fakeGrants, *fakeClock, *fakeOpener) {
	t.Helper()
	raw, err := os.ReadFile("testdata/app-server.json")
	if err != nil {
		t.Fatal(err)
	}
	var transcript struct {
		Pin          string                     `json:"pin"`
		Requests     map[string]json.RawMessage `json:"requests"`
		ThreadResult json.RawMessage            `json:"thread_result"`
	}
	if json.Unmarshal(raw, &transcript) != nil || transcript.Pin != ProtocolPin {
		t.Fatal("missing protocol fixture pin")
	}
	c := &fakeClock{now: time.Date(2026, 10, 13, 1, 0, 0, 0, time.UTC)}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	b := n.Binding{TenantID: fixtureID, ProjectID: fixtureID, OwnedSessionID: fixtureID, HarnessSessionID: fixtureID,
		RuntimeEpoch: fixtureID, DaemonID: fixtureID, DaemonGeneration: fixtureID, BindingRevision: 1, RunID: ptr(fixtureID)}
	effort := "high"
	spec := n.LaunchSpec{Binding: b, Mode: "structured", Cwd: "/workspace", OwnedWriterID: fixtureID, Lifetime: ctx,
		Profile: n.LocalProfile{AccountID: fixtureID, ProfileBindingRevision: 1, AccountHome: "/profiles/owner-codex", UserHome: "/profiles/owner",
			WorkspaceRoot: "/workspace", Binary: n.BinaryPin{Path: "/pinned/codex", Version: "0.157.1", Protocol: ProtocolPin, SHA256: fixtureDigest}},
		Policy: n.Policy{GrantID: fixtureID, GrantRevision: 1, ExpiresAt: c.Now().Add(time.Hour), ToolPolicyDigest: fixtureDigest,
			SettingsPolicyDigest: fixtureDigest, PermissionPolicy: "supervised", LocalHistory: "vendor_native", Continuation: "manual",
			MaximumRuntimeSeconds: 3600, MaximumTurns: 10, MaximumTokens: 10000},
		Options: n.LaunchConfig{ModelProfileID: fixtureID, Model: "test-model", Effort: &effort, ServiceTier: "default",
			ToolPolicyDigest: fixtureDigest, SettingsPolicyDigest: fixtureDigest}}
	g := &fakeGrants{clock: c, grant: WorkerGrant{Binding: b, Policy: spec.Policy, Binary: spec.Profile.Binary, AccountID: fixtureID,
		ProfileBindingRevision: 1, EnrolledEmail: "owner@example.test", Profile: "native_worker", ApprovalPolicy: "onRequest", Sandbox: "readOnly", QualificationDigest: fixtureDigest}}
	w := &fakeWire{t: t, frames: make(chan json.RawMessage, 32), closed: make(chan struct{}), email: "owner@example.test", requests: transcript.Requests,
		threadResult: transcript.ThreadResult, steerAck: "turn-1", proof: n.NativeIdentity{Provider: "codex", RuntimeEpoch: fixtureID,
			ProcessIdentityDigest: fixtureDigest, PID: 123, StartedAt: c.Now()}, exit: n.ExitEvidence{ProcessIdentityDigest: fixtureDigest, RootExited: true, GroupAbsent: true, ObservedAt: c.Now()}}
	o := &fakeOpener{wire: w}
	return &Launcher{Wires: o, Grants: g, Clock: c}, spec, w, g, c, o
}
func openFixture(t *testing.T, l *Launcher, spec n.LaunchSpec) *transport {
	t.Helper()
	tr, err := l.Open(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	s := tr.(*transport)
	t.Cleanup(func() {
		if _, err := s.Close(context.Background()); err != nil {
			t.Error(err)
		}
		<-s.readerDone
	})
	return s
}
func firstInput(spec n.LaunchSpec, c *fakeClock) n.Input {
	return n.Input{RequestID: fixtureID, InputID: fixtureID, ExpectedBinding: spec.Binding, Mode: "now", Text: "first input", ExpiresAt: c.Now().Add(time.Minute)}
}
func nextEvent(t *testing.T, s *transport) n.ProviderEvent {
	t.Helper()
	select {
	case e, ok := <-s.Events():
		if !ok {
			t.Fatal("unexpected provider EOF")
		}
		return e
	case <-time.After(5 * time.Second):
		t.Fatal("provider event barrier hung")
		return n.ProviderEvent{}
	}
}
func barrier(t *testing.T, s *transport, w *fakeWire) {
	t.Helper()
	w.send(`{"method":"item/agentMessage/delta","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"barrier","delta":"barrier"}}`)
	e := nextEvent(t, s)
	if e.Structured == nil || e.Structured.Kind != "assistant_delta" {
		t.Fatal("wrong barrier event")
	}
}
func start(t *testing.T, s *transport, spec n.LaunchSpec, c *fakeClock) n.NativeEvidence {
	t.Helper()
	e, err := s.Send(context.Background(), firstInput(spec, c))
	if err != nil {
		t.Fatal(err)
	}
	if e.Stage != "write_acknowledged" {
		t.Fatal("start reply overclaimed consumption")
	}
	return e
}

// Risk: app-server schema drift or wrong account/turn evidence must never
// authorize a native write, steering receipt, approval or durable final.
func TestNativeCodexTranscriptQualification(t *testing.T) {
	t.Run("ordered handshake and pre-ack notification barrier", func(t *testing.T) {
		l, spec, w, _, c, o := fixture(t)
		s := openFixture(t, l, spec)
		if len(o.plans) != 1 || !reflect.DeepEqual(o.plans[0].Args, []string{"app-server", "--listen", "stdio://"}) ||
			!reflect.DeepEqual(o.plans[0].Environment, []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "HOME=/profiles/owner", "CODEX_HOME=/profiles/owner-codex"}) {
			t.Fatal("child launch escaped pinned owner profile")
		}
		want := []string{"initialize", "initialized", "account/read", "thread/start"}
		for i, method := range want {
			if w.calls[i].method != method {
				t.Fatal("handshake order changed")
			}
		}
		w.startEntered, w.startRelease = make(chan struct{}), make(chan struct{})
		w.startFrames = []json.RawMessage{json.RawMessage(`{"method":"turn/started","params":{"threadId":"thread-1","turn":{"id":"turn-1","status":"inProgress"}}}`)}
		result := make(chan error, 1)
		go func() { _, err := s.Send(context.Background(), firstInput(spec, c)); result <- err }()
		<-w.startEntered
		select {
		case <-s.Events():
			t.Fatal("notification claimed input before native ack")
		default:
		}
		close(w.startRelease)
		if err := <-result; err != nil {
			t.Fatal(err)
		}
		e := nextEvent(t, s)
		if e.Receipt == nil || e.Receipt.State != "turn_started" || *e.Receipt.InputID != fixtureID {
			t.Fatal("notification lost exact consumed input")
		}
	})
	t.Run("native steering and exact interrupt", func(t *testing.T) {
		l, spec, w, _, c, _ := fixture(t)
		s := openFixture(t, l, spec)
		first := start(t, s, spec, c)
		steer := firstInput(spec, c)
		steer.Text = "steer input"
		steer.InputID = "22222222-2222-4222-8222-222222222222"
		e, err := s.Send(context.Background(), steer)
		if err != nil || e.TurnRef != first.TurnRef || e.InputID != steer.InputID || e.Stage != "write_acknowledged" {
			t.Fatal("steering evidence was not bound to the expected turn")
		}
		interrupt := n.Interrupt{RequestID: fixtureID, ExpectedBinding: spec.Binding, ExpectedTurnRef: "stale", ExpiresAt: c.Now().Add(time.Minute)}
		if _, err := s.Interrupt(context.Background(), interrupt); !errors.Is(err, ErrTurn) || w.count("turn/interrupt") != 0 {
			t.Fatal("stale interrupt reached native I/O")
		}
		interrupt.ExpectedTurnRef = first.TurnRef
		if e, err := s.Interrupt(context.Background(), interrupt); err != nil || e.Stage != "applied" {
			t.Fatal("native interrupt ack missing")
		}
		w.send(`{"method":"turn/completed","params":{"threadId":"thread-1","turn":{"id":"turn-1","status":"interrupted"}}}`)
		if e := nextEvent(t, s); e.Final != nil || e.Structured == nil || *e.Structured.State != "interrupted" {
			t.Fatal("interrupt became a successful final")
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := s.Wait(ctx); !errors.Is(err, ErrUncertain) {
			t.Fatal("turn completion became process exit")
		}
	})
	t.Run("wrong steering acknowledgement stays uncertain", func(t *testing.T) {
		l, spec, w, _, c, _ := fixture(t)
		s := openFixture(t, l, spec)
		start(t, s, spec, c)
		w.steerAck = "foreign-turn"
		input := firstInput(spec, c)
		input.Text = "steer input"
		if e, err := s.Send(context.Background(), input); !errors.Is(err, ErrUncertain) || e.Stage != "" {
			t.Fatal("wrong-turn acknowledgement reported delivery")
		}
	})
	t.Run("wrong account rejects input before native write", func(t *testing.T) {
		l, spec, w, _, c, _ := fixture(t)
		s := openFixture(t, l, spec)
		w.changeEmail("other@example.test")
		if _, err := s.Send(context.Background(), firstInput(spec, c)); !errors.Is(err, ErrIdentity) || w.count("turn/start") != 0 {
			t.Fatal("foreign account received input")
		}
	})
	t.Run("explicit completed final with scoped drain authority", func(t *testing.T) {
		l, spec, w, _, c, _ := fixture(t)
		s := openFixture(t, l, spec)
		start(t, s, spec, c)
		w.send(`{"method":"item/completed","params":{"threadId":"thread-1","turnId":"turn-1","item":{"id":"answer-1","type":"agentMessage","phase":"final_answer","text":"explicit final"}}}`)
		w.send(`{"method":"turn/completed","params":{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed","error":null}}}`)
		e := nextEvent(t, s)
		if e.Final == nil || e.Final.Text != "explicit final" || e.Final.InputID != fixtureID || e.Final.DrainGrantID != fixtureID || !sameBinding(e.Final.ExpectedBinding, spec.Binding) {
			t.Fatal("explicit final lost scoped identity")
		}
		if e.Final.FinalID == "answer-1" || e.Final.TurnRef == "turn-1" {
			t.Fatal("vendor ids escaped into publication")
		}
		v, err := n.NewValidator()
		if err != nil || v.ValidateJSON("NativeFinalPublish", encode(e.Final)) != nil {
			t.Fatal("final violated C0 schema")
		}
		if nextEvent(t, s).Structured == nil {
			t.Fatal("missing completion state")
		}
		w.send(`{"method":"turn/completed","params":{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed"}}}`)
		if e := nextEvent(t, s); e.Final != nil || e.Structured == nil || *e.Structured.State != "ownership_lost" {
			t.Fatal("duplicate terminal published another final")
		}
	})
	t.Run("wrong account rejects retained final", func(t *testing.T) {
		l, spec, w, _, c, _ := fixture(t)
		s := openFixture(t, l, spec)
		start(t, s, spec, c)
		w.send(`{"method":"item/completed","params":{"threadId":"thread-1","turnId":"turn-1","item":{"id":"answer-1","type":"agentMessage","phase":"final_answer","text":"must not publish"}}}`)
		barrier(t, s, w)
		w.changeEmail("other@example.test")
		w.send(`{"method":"turn/completed","params":{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed"}}}`)
		if e := nextEvent(t, s); e.Final != nil || e.Structured == nil || *e.Structured.State != "ownership_lost" {
			t.Fatal("foreign account final was accepted")
		}
		if _, err := s.Send(context.Background(), firstInput(spec, c)); !errors.Is(err, ErrIdentity) {
			t.Fatal("wrong final failure reason")
		}
	})
	for _, tc := range []struct {
		name, frame string
		want        error
	}{
		{"foreign thread", `{"method":"turn/completed","params":{"threadId":"foreign","turn":{"id":"turn-1","status":"completed"}}}`, ErrBinding},
		{"foreign turn", `{"method":"turn/completed","params":{"threadId":"thread-1","turn":{"id":"foreign","status":"completed"}}}`, ErrTurn},
		{"unsupported server permission request", `{"id":9,"method":"item/permissions/requestApproval","params":{}}`, ErrQualification},
		{"account update invalidates writer", `{"method":"account/updated","params":{"authMode":"chatgpt"}}`, ErrIdentity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, spec, w, _, c, _ := fixture(t)
			s := openFixture(t, l, spec)
			start(t, s, spec, c)
			w.send(tc.frame)
			nextEvent(t, s)
			if _, err := s.Send(context.Background(), firstInput(spec, c)); !errors.Is(err, tc.want) {
				t.Fatalf("expected exact failure %v, got %v", tc.want, err)
			}
		})
	}
	t.Run("approval effect and single use native response", func(t *testing.T) {
		l, spec, w, g, c, _ := fixture(t)
		s := openFixture(t, l, spec)
		start(t, s, spec, c)
		w.send(`{"id":42,"method":"item/commandExecution/requestApproval","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"cmd-1","command":"PRIVATE_COMMAND","cwd":"PRIVATE_PATH"}}`)
		e := nextEvent(t, s)
		if e.Approval == nil || strings.Contains(string(encode(e.Approval)), "PRIVATE_") {
			t.Fatal("unsafe approval projection")
		}
		d := n.ApprovalDecision{RequestID: fixtureID, ExpectedBinding: spec.Binding, ApprovalID: e.Approval.ApprovalID, ExpectedRevision: 1,
			EffectDigest: fixtureDigest, Decision: "allow_once", StepUpAuthorizationID: ptr(fixtureID)}
		if _, err := s.Decide(context.Background(), d); !errors.Is(err, ErrPolicy) || len(w.replies) != 0 {
			t.Fatal("changed effect approved")
		}
		d.EffectDigest = e.Approval.EffectDigest
		if evidence, err := s.Decide(context.Background(), d); err != nil || evidence.Stage != "write_acknowledged" || g.approved != 1 {
			t.Fatal("qualified approval not acknowledged")
		}
		if string(w.replies[0]) != `{"decision":"accept"}` {
			t.Fatal("approval widened to a session/policy change")
		}
		if _, err := s.Decide(context.Background(), d); !errors.Is(err, ErrExpired) || len(w.replies) != 1 {
			t.Fatal("approval replay reached native process")
		}
	})
	t.Run("bounded usage and account quota projection", func(t *testing.T) {
		l, spec, w, _, c, _ := fixture(t)
		s := openFixture(t, l, spec)
		start(t, s, spec, c)
		w.send(`{"method":"thread/tokenUsage/updated","params":{"threadId":"thread-1","turnId":"turn-1","tokenUsage":{"total":{"totalTokens":20,"inputTokens":15,"cachedInputTokens":5,"outputTokens":5,"reasoningOutputTokens":2}},"reasoning":"PRIVATE_REASONING"}}`)
		e := nextEvent(t, s)
		if e.Expert == nil || strings.Contains(e.Expert.FrameJSON, "PRIVATE_") || strings.Contains(e.Expert.FrameJSON, "thread-1") {
			t.Fatal("usage leaked raw identities/body")
		}
		w.send(`{"method":"account/rateLimits/updated","params":{"rateLimits":{"limitId":"codex","primary":{"usedPercent":25,"windowDurationMins":300,"resetsAt":1791856800}}}}`)
		e = nextEvent(t, s)
		if e.Quota == nil || e.Quota.AccountID != spec.Profile.AccountID || e.Quota.ProfileBindingRevision != 1 || e.Quota.Scope != "codex/primary" || *e.Quota.UsedPercent != 25 {
			t.Fatal("quota lost enrolled account scope")
		}
	})
	t.Run("commentary chunks tool bodies and failed turns are not finals", func(t *testing.T) {
		l, spec, w, _, c, _ := fixture(t)
		s := openFixture(t, l, spec)
		start(t, s, spec, c)
		w.send(`{"method":"item/reasoning/textDelta","params":{"delta":"PRIVATE_REASONING"}}`)
		w.send(`{"method":"item/completed","params":{"threadId":"thread-1","turnId":"turn-1","item":{"id":"comment","type":"agentMessage","phase":"commentary","text":"commentary"}}}`)
		w.send(`{"method":"item/completed","params":{"threadId":"thread-1","turnId":"turn-1","item":{"id":"cmd","type":"commandExecution","status":"completed","aggregatedOutput":"PRIVATE_TOOL_RESULT"}}}`)
		e := nextEvent(t, s)
		if e.Structured == nil || e.Structured.Kind != "tool_status" || e.Structured.Text != nil || strings.Contains(string(encode(e.Structured)), "PRIVATE_") {
			t.Fatal("tool body escaped")
		}
		w.send(`{"method":"item/completed","params":{"threadId":"thread-1","turnId":"turn-1","item":{"id":"answer","type":"agentMessage","text":"candidate"}}}`)
		w.send(`{"method":"turn/completed","params":{"threadId":"thread-1","turn":{"id":"turn-1","status":"failed"}}}`)
		if e := nextEvent(t, s); e.Final != nil || e.Structured == nil || *e.Structured.State != "failed" {
			t.Fatal("failed turn reported final success")
		}
	})
}

func TestNativeCodexRejectsUnqualifiedLaunch(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Launcher, *n.LaunchSpec, *fakeGrants)
		want error
	}{
		{"missing authority", func(l *Launcher, _ *n.LaunchSpec, _ *fakeGrants) { l.Grants = nil }, ErrQualification},
		{"routine profile", func(_ *Launcher, _ *n.LaunchSpec, g *fakeGrants) { g.grant.Profile = "routine" }, ErrQualification},
		{"confined profile", func(_ *Launcher, _ *n.LaunchSpec, g *fakeGrants) { g.grant.Profile = "confined" }, ErrQualification},
		{"bounded preapproval", func(_ *Launcher, s *n.LaunchSpec, g *fakeGrants) {
			s.Policy.PermissionPolicy = "bounded_preapproved"
			g.grant.Policy = s.Policy
		}, ErrQualification},
		{"changed immutable policy", func(_ *Launcher, s *n.LaunchSpec, _ *fakeGrants) { s.Policy.GrantRevision++ }, ErrPolicy},
		{"unqualified binary", func(_ *Launcher, s *n.LaunchSpec, _ *fakeGrants) { s.Profile.Binary.Version = "next" }, ErrQualification},
		{"credential environment override", func(_ *Launcher, s *n.LaunchSpec, _ *fakeGrants) {
			s.Profile.Environment = []string{"ACCESS_TOKEN=blocked"}
		}, ErrPolicy},
		{"expired grant", func(_ *Launcher, s *n.LaunchSpec, g *fakeGrants) {
			s.Policy.ExpiresAt = g.clock.Now()
			g.grant.Policy = s.Policy
		}, ErrExpired},
		{"wrong profile revision", func(_ *Launcher, s *n.LaunchSpec, _ *fakeGrants) { s.Profile.ProfileBindingRevision++ }, ErrBinding},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, spec, _, g, _, o := fixture(t)
			tc.edit(l, &spec, g)
			if _, err := l.Open(context.Background(), spec); !errors.Is(err, tc.want) || len(o.plans) != 0 {
				t.Fatalf("must reject before possible fork with %v, got %v", tc.want, err)
			}
		})
	}
	t.Run("resolved thread policy is authoritative", func(t *testing.T) {
		l, spec, w, _, _, _ := fixture(t)
		w.badPolicy = true
		if _, err := l.Open(context.Background(), spec); !errors.Is(err, ErrPolicy) || w.count("turn/start") != 0 {
			t.Fatal("unexpected resolved policy received input")
		}
	})
	t.Run("resume never imports historical finals", func(t *testing.T) {
		l, spec, w, _, _, o := fixture(t)
		spec.ResumeRef = "thread-1"
		s := openFixture(t, l, spec)
		if len(o.plans) != 1 || w.count("thread/resume") != 1 || w.count("thread/start") != 0 || s.Identity().NativeThreadRef != "thread-1" {
			t.Fatal("resume did not use exact pinned conversation")
		}
		select {
		case <-s.Events():
			t.Fatal("resume history became new writer evidence")
		default:
		}
	})
}

func TestNativeCodexRejectsUnboundedOrAmbiguousFrames(t *testing.T) {
	for name, raw := range map[string]string{
		"oversize":           strings.Repeat(" ", n.NativeFrameBeforeDecodeBytes+1),
		"duplicate identity": `{"params":{"threadId":"foreign","threadId":"owned"}}`,
		"excessive nesting":  strings.Repeat("[", n.NativeFrameDecodeDepth+2) + "0" + strings.Repeat("]", n.NativeFrameDecodeDepth+2),
		"trailing JSON":      `{} {}`,
		"invalid UTF8":       string([]byte{'"', 255, '"'}),
	} {
		t.Run(name, func(t *testing.T) {
			var f nativeFrame
			if err := decode([]byte(raw), &f); !errors.Is(err, ErrProtocol) {
				t.Fatalf("unexpected bounded decoder result: %v", err)
			}
		})
	}
}
