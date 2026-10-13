// SPDX-License-Identifier: AGPL-3.0-only

package nativestream

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/nativecontract"
)

const fixtureID = "00000000-0000-4000-8000-000000000001"

func fixtureBinding() nativecontract.Binding {
	return nativecontract.Binding{TenantID: fixtureID, ProjectID: fixtureID, OwnedSessionID: fixtureID, HarnessSessionID: fixtureID, RuntimeEpoch: fixtureID, DaemonID: fixtureID, DaemonGeneration: fixtureID, BindingRevision: 1}
}

func fixtureSnapshot() nativecontract.SessionView {
	binding := fixtureBinding()
	return nativecontract.SessionView{OwnedSessionID: fixtureID, CurrentBinding: &binding, OwnerPersonID: fixtureID, Role: "coordinator", Profile: "native", Harness: "claude", State: "idle", Mode: "structured", Revision: 1, Generation: 1, ConversationRef: "opaque-conversation", Capabilities: nativecontract.Capabilities{Input: true, ChatSupported: true, Interrupt: true, InterruptSupported: true, Stop: true, Expert: true, EventsSupported: true, Steer: "unsupported", Quota: "unsupported", QualificationDigest: strings.Repeat("a", 64)}}
}

func fixtureAccess(view string, private bool) nativecontract.ValidationContext {
	snapshot := fixtureSnapshot()
	kind := "person"
	if private {
		kind = "agent"
	}
	return nativecontract.ValidationContext{Binding: snapshot.CurrentBinding, Generation: 1, PrincipalKind: kind, View: view, Mode: snapshot.Mode, State: snapshot.State, Capabilities: &snapshot.Capabilities, Private: private}
}

func fixtureProjector(t *testing.T) *Projector {
	t.Helper()
	p, err := NewProjector(ExportPolicy{Text: func(_ context.Context, _ nativecontract.Binding, text string) (string, error) { return text, nil }})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func fixtureEvent(seq uint64, kind string, payload any) nativecontract.StreamEvent {
	b, _ := NewBuffer(fixtureBinding(), 1, nil)
	event := b.event(seq, kind, payload)
	b.Close()
	return event
}

func requireReason(t *testing.T, err error, reason nativecontract.Reason) {
	t.Helper()
	var invalid *nativecontract.ValidationError
	if !errors.As(err, &invalid) || invalid.Reason != reason {
		t.Fatalf("got %v; want exact reason %s", err, reason)
	}
}

func waitSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("barrier did not arrive")
	}
}

// Named risk: arbitrary Expert JSON leaks credentials, private reasoning and
// tool bodies, or claims a raw result as retained final history.
func TestNativeExpertProjectionFiltersAndBounds(t *testing.T) {
	p := fixtureProjector(t)
	raw := []byte(`{"type":"assistant","session_id":"native-session","uuid":"native-message","authorization":"SYNTHETIC_AUTH","cwd":"SYNTHETIC_PATH","message":{"id":"native-message","role":"assistant","content":[{"type":"text","text":"visible answer"},{"type":"thinking","thinking":"SYNTHETIC_REASONING"},{"type":"tool_use","input":{"value":"SYNTHETIC_TOOL_INPUT"}},{"type":"tool_result","content":"SYNTHETIC_TOOL_RESULT"}]}}`)
	frame := p.Expert(context.Background(), fixtureBinding(), "claude", raw)
	if frame.Omitted || frame.EventName != "assistant" || !strings.Contains(frame.FrameJSON, "visible answer") || !strings.Contains(frame.FrameJSON, `"type":"assistant"`) {
		t.Fatal("qualified native envelope/text was not preserved")
	}
	for _, forbidden := range []string{"SYNTHETIC_", "native-session", "native-message", "thinking", "tool_use", "tool_result", "authorization", "cwd"} {
		if strings.Contains(frame.FrameJSON, forbidden) {
			t.Fatal("unsafe native field escaped projection:", forbidden)
		}
	}
	if len(frame.RedactedPaths) < 6 || len(frame.RedactedPaths) > nativecontract.ExpertRedactionPaths {
		t.Fatal("redactions were not bounded and visible")
	}
	clean, err := p.Sanitize(context.Background(), fixtureEvent(1, "expert", frame), fixtureAccess("expert", false))
	if err != nil {
		t.Fatal(err)
	}
	var again nativecontract.ExpertFrame
	_ = json.Unmarshal(clean.Payload, &again)
	if again.FrameJSON != frame.FrameJSON {
		t.Fatal("server revalidation changed safe native correlations")
	}
	for _, raw := range []string{`{"type":"unknown","token":"SYNTHETIC_AUTH"}`, `{"type":"assistant","type":"result"}`, strings.Repeat("[", 17) + "0" + strings.Repeat("]", 17), `{"type":"system","subtype":"SYNTHETIC_UNKNOWN_SUBTYPE"}`} {
		frame := p.Expert(context.Background(), fixtureBinding(), "claude", []byte(raw))
		if !frame.Omitted || frame.FrameJSON != "{}" {
			t.Fatal("unknown/invalid native payload was passed through")
		}
	}
	tooMany := map[string]any{"type": "system", "subtype": "init"}
	for i := 0; i < 65; i++ {
		tooMany[strings.Repeat("x", i+1)] = true
	}
	many, _ := json.Marshal(tooMany)
	if frame := p.Expert(context.Background(), fixtureBinding(), "claude", many); !frame.Omitted || frame.FrameJSON != "{}" {
		t.Fatal("redaction overflow was truncated into an apparently complete body")
	}
	if frame := p.Expert(context.Background(), fixtureBinding(), "claude", []byte(strings.Repeat("x", nativecontract.NativeFrameBeforeDecodeBytes+1))); !frame.Omitted {
		t.Fatal("oversized raw frame was accepted")
	}
	codex := p.Expert(context.Background(), fixtureBinding(), "codex", []byte(`{"method":"item/started","params":{"threadId":"native-thread","turnId":"native-turn","item":{"id":"native-item","type":"commandExecution","command":"SYNTHETIC_COMMAND","env":{"token":"SYNTHETIC_AUTH"},"text":"SYNTHETIC_RESULT"}}}`))
	if codex.Omitted || strings.Contains(codex.FrameJSON, "SYNTHETIC_") || strings.Contains(codex.FrameJSON, "native-thread") || !strings.Contains(codex.FrameJSON, "commandExecution") {
		t.Fatal("Codex projection lost metadata or exported tool bodies")
	}
	result := p.Expert(context.Background(), fixtureBinding(), "claude", []byte(`{"type":"result","subtype":"success","result":"SYNTHETIC_FINAL"}`))
	if strings.Contains(result.FrameJSON, "SYNTHETIC_FINAL") {
		t.Fatal("raw native final entered live replay")
	}
	noText, err := NewProjector(ExportPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if frame := noText.Expert(context.Background(), fixtureBinding(), "claude", []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"unqualified text"}]}}`)); strings.Contains(frame.FrameJSON, "unqualified text") {
		t.Fatal("missing output qualification allowed text")
	}
	secretText := p.Expert(context.Background(), fixtureBinding(), "claude", []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"Authorization: Bearer SYNTHETIC_AUTH"}]}}`))
	if strings.Contains(secretText.FrameJSON, "SYNTHETIC_AUTH") {
		t.Fatal("credential-shaped text escaped mandatory filtering")
	}
}

type testClock struct {
	mu     sync.Mutex
	now    time.Time
	timers map[time.Duration]chan time.Time
}

func newTestClock() *testClock {
	return &testClock{now: time.Date(2026, 10, 13, 1, 0, 0, 0, time.UTC), timers: map[time.Duration]chan time.Time{}}
}
func (c *testClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *testClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.timers[d] = ch
	return ch
}
func (c *testClock) advance(d time.Duration) { c.mu.Lock(); c.now = c.now.Add(d); c.mu.Unlock() }

func TestTransientRingReportsLossAndRejectsStaleReplay(t *testing.T) {
	clock := newTestClock()
	buffer, err := NewBuffer(fixtureBinding(), 1, clock)
	if err != nil {
		t.Fatal(err)
	}
	defer buffer.Close()
	for i := uint64(1); i <= 100; i++ {
		event := fixtureEvent(i, "assistant.delta", nativecontract.AssistantDelta{MessageID: fixtureID, Text: strings.Repeat("v", 8192)})
		if err := buffer.Append([]nativecontract.StreamEvent{event}); err != nil {
			t.Fatal(err)
		}
	}
	count, size, next := buffer.Stats()
	if count > nativecontract.LiveReplayFrames || size > nativecontract.LiveReplayBytes || next != 101 {
		t.Fatal("ring exceeded its count/byte bound or lost source order")
	}
	gap, position, _, _ := buffer.ReadAfter(0)
	var loss nativecontract.Gap
	_ = json.Unmarshal(gap.Payload, &loss)
	if gap.Kind != "gap" || loss.Reason != "buffer_evicted" || loss.DroppedFrames == "0" || !loss.ReloadSnapshot {
		t.Fatal("slow reader was told replay was complete")
	}
	retained, _, _, _ := buffer.ReadAfter(position)
	if retained.Kind != "assistant.delta" {
		t.Fatal("gap did not resume at the actual retained prefix")
	}
	_, err = buffer.Position("1:0", 1)
	requireReason(t, err, nativecontract.ReasonCursorExpired)
	_, err = buffer.Position("2:100", 1)
	requireReason(t, err, nativecontract.ReasonWrongGeneration)
	page, err := buffer.Page(nativecontract.EventQuery{Generation: 1, Limit: 2}, func(string) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || !page.Truncated || page.NextBefore == nil || page.Items[0].Seq != "100" || page.Items[1].Seq != "99" {
		t.Fatal("bounded page did not expose incomplete ordered replay")
	}
	bad := fixtureEvent(102, "assistant.delta", nativecontract.AssistantDelta{MessageID: fixtureID, Text: "wrong order"})
	requireReason(t, buffer.Append([]nativecontract.StreamEvent{bad}), nativecontract.ReasonInvalidInput)
	clock.advance(nativecontract.LiveReplayIdleSeconds * time.Second)
	if frames, bytes, _ := buffer.Stats(); frames != 0 || bytes != 0 {
		t.Fatal("idle expiry retained raw content")
	}
	_, err = buffer.Position("1:99", 1)
	requireReason(t, err, nativecontract.ReasonCursorExpired)
}

type deadlineRecorder struct {
	*httptest.ResponseRecorder
	flushes chan struct{}
}

func newRecorder() *deadlineRecorder {
	return &deadlineRecorder{ResponseRecorder: httptest.NewRecorder(), flushes: make(chan struct{}, 32)}
}
func (w *deadlineRecorder) SetReadDeadline(time.Time) error  { return nil }
func (w *deadlineRecorder) SetWriteDeadline(time.Time) error { return nil }
func (w *deadlineRecorder) Flush() {
	w.ResponseRecorder.Flush()
	select {
	case w.flushes <- struct{}{}:
	default:
	}
}

type fakeAuthority struct {
	actor   nativecontract.Actor
	access  nativecontract.ValidationContext
	revoked bool
	peer    bool
}

func (a *fakeAuthority) Authenticate(r *http.Request) (nativecontract.Actor, error) {
	if !a.peer || r.Header.Get("Authorization") != "Bearer local-test-proof" {
		return nativecontract.Actor{}, Refuse(nativecontract.ReasonGrantMissing)
	}
	return a.actor, nil
}
func (a *fakeAuthority) WithAccess(ctx context.Context, _ nativecontract.Actor, _ LocalRequest, deliver func(nativecontract.ValidationContext) error) error {
	if a.revoked {
		return Refuse(nativecontract.ReasonPermissionRevoked)
	}
	return deliver(a.access)
}

type fakeHost struct {
	nativecontract.Host
	writes, detaches int
}

func (h *fakeHost) Input(_ context.Context, value nativecontract.Input) (nativecontract.OperationResult, error) {
	h.writes++
	return nativecontract.OperationResult{Receipt: nativecontract.Receipt{RequestID: value.RequestID, OperationID: fixtureID, Binding: &value.ExpectedBinding, Operation: "input", State: "accepted", SourceSequence: "0", UpdatedAt: time.Now()}}, nil
}
func (h *fakeHost) Detach(_ context.Context, value nativecontract.Detach) (nativecontract.OperationResult, error) {
	h.detaches++
	return nativecontract.OperationResult{Receipt: nativecontract.Receipt{RequestID: value.RequestID, OperationID: fixtureID, Binding: &value.ExpectedBinding, Operation: "detach", State: "applied", SourceSequence: "0", UpdatedAt: time.Now()}}, nil
}

// Named risk: an owner socket token, stale body or queued grant could be mistaken
// for native write authority. Invalid requests must never reach Host.
func TestLocalHostProtocolRequiresPeerGrantAndExactBinding(t *testing.T) {
	host := &fakeHost{}
	authority := &fakeAuthority{actor: nativecontract.Actor{TenantID: fixtureID, PrincipalID: fixtureID, PrincipalKind: "person"}, access: fixtureAccess("structured", false), peer: true}
	adapter, err := NewLocalAdapter(host, authority, fixtureProjector(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	adapter.Mount(mux)
	for _, route := range nativecontract.LocalRoutes() {
		path := strings.ReplaceAll(strings.ReplaceAll(route.Path, "{ownedSessionId}", fixtureID), "{approvalId}", fixtureID)
		r := httptest.NewRequest(route.Method, path, nil)
		w := newRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 401 || host.writes != 0 || host.detaches != 0 {
			t.Fatal("unauthenticated native route reached Host", route.OperationID)
		}
	}
	input := nativecontract.Input{RequestID: fixtureID, ExpectedBinding: fixtureBinding(), InputID: fixtureID, Mode: "after_turn", Text: "authorized input", ExpiresAt: time.Now().Add(time.Minute)}
	raw, _ := json.Marshal(input)
	envelope := nativecontract.LocalEnvelope{Protocol: nativecontract.LocalProtocol, RequestID: fixtureID, Method: "input", ExpectedBinding: &input.ExpectedBinding, Params: raw}
	send := func(body []byte, proof, protocol, capability string) *deadlineRecorder {
		r := httptest.NewRequest("POST", "/v1/native-sessions/"+fixtureID+"/input", strings.NewReader(string(body)))
		r.Header.Set("Authorization", proof)
		r.Header.Set(nativecontract.LocalProtocolHeader, protocol)
		r.Header.Set(nativecontract.ControlHeader, capability)
		w := newRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	body, _ := json.Marshal(envelope)
	for _, variant := range []string{"missing peer", "wrong protocol", "missing controller", "revoked", "stale binding", "changed request", "unknown field", "oversize"} {
		candidate := append([]byte(nil), body...)
		proof := "Bearer local-test-proof"
		protocol := nativecontract.LocalProtocol
		capability := "test-controller"
		want := 403
		reason := nativecontract.ReasonGrantMissing
		switch variant {
		case "missing peer":
			authority.peer = false
			want = 401
		case "wrong protocol":
			protocol = "unsupported"
			want = 400
			reason = nativecontract.ReasonUnsupported
		case "missing controller":
			capability = ""
		case "revoked":
			authority.revoked = true
			reason = nativecontract.ReasonPermissionRevoked
		case "stale binding":
			candidate = []byte(strings.ReplaceAll(string(body), `"binding_revision":1`, `"binding_revision":2`))
			want = 409
			reason = nativecontract.ReasonWrongGeneration
		case "changed request":
			changed := envelope
			changed.RequestID = "00000000-0000-4000-8000-000000000002"
			candidate, _ = json.Marshal(changed)
			want = 409
			reason = nativecontract.ReasonIdentityMismatch
		case "unknown field":
			candidate = []byte(strings.TrimSuffix(string(body), "}") + `,"extra":true}`)
			want = 400
			reason = nativecontract.ReasonInvalidInput
		case "oversize":
			candidate = []byte(strings.Repeat("x", nativecontract.RequestBodyBytes+1))
			want = 413
			reason = nativecontract.ReasonBufferFull
		}
		w := send(candidate, proof, protocol, capability)
		var failure nativecontract.Error
		_ = json.Unmarshal(w.Body.Bytes(), &failure)
		if w.Code != want || failure.Reason != reason || host.writes != 0 {
			t.Fatalf("%s: HTTP %d / %s, want %d / %s, native writes %d", variant, w.Code, failure.Reason, want, reason, host.writes)
		}
		authority.peer = true
		authority.revoked = false
	}
	if w := send(body, "Bearer local-test-proof", nativecontract.LocalProtocol, "test-controller"); w.Code != 200 || host.writes != 1 || w.Header().Get(nativecontract.LocalProtocolHeader) != nativecontract.LocalProtocol {
		t.Fatal("valid bounded input did not reach Host exactly once")
	}
	detach := nativecontract.Detach{RequestID: fixtureID, ExpectedBinding: fixtureBinding(), ViewerID: fixtureID}
	params, _ := json.Marshal(detach)
	envelope.Method = "detach"
	envelope.Params = params
	body, _ = json.Marshal(envelope)
	r := httptest.NewRequest("POST", "/v1/native-sessions/"+fixtureID+"/detach", strings.NewReader(string(body)))
	r.Header.Set("Authorization", "Bearer local-test-proof")
	r.Header.Set(nativecontract.LocalProtocolHeader, nativecontract.LocalProtocol)
	w := newRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 200 || host.detaches != 1 || host.writes != 1 {
		t.Fatal("detach was not a viewer-only Host operation")
	}
}

type queuedSubscription struct {
	ch     chan nativecontract.StreamEvent
	closed bool
}

func (s *queuedSubscription) Events() <-chan nativecontract.StreamEvent { return s.ch }
func (s *queuedSubscription) Close() error                              { s.closed = true; return nil }

func TestSSEFencesQueuedContentAndKeepalive(t *testing.T) {
	for _, variant := range []string{"queued frame", "keepalive"} {
		t.Run(variant, func(t *testing.T) {
			clock := newTestClock()
			sub := &queuedSubscription{ch: make(chan nativecontract.StreamEvent, 2)}
			sub.ch <- fixtureEvent(0, "snapshot", fixtureSnapshot())
			if variant == "queued frame" {
				sub.ch <- fixtureEvent(1, "assistant.delta", nativecontract.AssistantDelta{MessageID: fixtureID, Text: "QUEUED_CONTENT_MUST_NOT_LEAK"})
			}
			var mu sync.Mutex
			revoked := false
			calls := 0
			entered := make(chan struct{})
			release := make(chan struct{})
			fence := func(ctx context.Context, deliver func(nativecontract.ValidationContext) error) error {
				calls++
				if calls == 2 {
					close(entered)
					select {
					case <-release:
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				mu.Lock()
				defer mu.Unlock()
				if revoked {
					return Refuse(nativecontract.ReasonPermissionRevoked)
				}
				return deliver(fixtureAccess("structured", false))
			}
			w := newRecorder()
			done := make(chan struct{})
			go func() {
				ServeEvents(w, httptest.NewRequest("GET", "/stream", nil), sub, fence, fixtureProjector(t), clock)
				close(done)
			}()
			waitSignal(t, w.flushes)
			if variant == "keepalive" {
				clock.mu.Lock()
				timer := clock.timers[nativecontract.KeepaliveSeconds*time.Second]
				clock.mu.Unlock()
				timer <- clock.Now()
			}
			waitSignal(t, entered)
			mu.Lock()
			revoked = true
			mu.Unlock()
			close(release)
			waitSignal(t, done)
			if !sub.closed || !strings.Contains(w.Body.String(), "event: snapshot") || !strings.Contains(w.Body.String(), `"reason":"revoked"`) || strings.Contains(w.Body.String(), "QUEUED_CONTENT_MUST_NOT_LEAK") || strings.Contains(w.Body.String(), ": keepalive") {
				t.Fatal("revocation did not cancel queued content/keepalive with a visible end")
			}
		})
	}
}

type fakeFinals struct {
	values []nativecontract.FinalDelivery
	fail   bool
}

func (s *fakeFinals) Persist(_ context.Context, value nativecontract.FinalDelivery) (nativecontract.FinalReceipt, error) {
	if s.fail {
		return nativecontract.FinalReceipt{}, errors.New("synthetic persistence refusal")
	}
	s.values = append(s.values, value)
	return nativecontract.FinalReceipt{FinalID: value.FinalID, State: "persisted", UpdatedAt: time.Now()}, nil
}

type stalledPublisher struct {
	batches chan nativecontract.Publish
	release chan struct{}
}

func (p *stalledPublisher) Publish(ctx context.Context, batch nativecontract.Publish) (nativecontract.OperationResult, error) {
	select {
	case p.batches <- batch:
	case <-ctx.Done():
		return nativecontract.OperationResult{}, ctx.Err()
	}
	select {
	case <-p.release:
		return nativecontract.OperationResult{}, nil
	case <-ctx.Done():
		return nativecontract.OperationResult{}, ctx.Err()
	}
}

func TestOutboundLossRemainsTransientAndFinalUsesSink(t *testing.T) {
	sink := &fakeFinals{}
	out, err := NewOutbound(fixtureBinding(), 1, fixtureAccess("", true), fixtureProjector(t), sink, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	publisher := &stalledPublisher{batches: make(chan nativecontract.Publish), release: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- out.Pump(ctx, publisher) }()
	observe := func(source uint64, text string) {
		t.Helper()
		err := out.Observe(ctx, nativecontract.ProviderEvent{SourceSequence: source, Identity: nativecontract.NativeIdentity{RuntimeEpoch: fixtureID}, Structured: &nativecontract.StructuredFrame{Kind: "assistant_delta", Text: &text}})
		if err != nil {
			t.Fatal(err)
		}
	}
	observe(1, "in flight")
	select {
	case first := <-publisher.batches:
		if len(first.Events) != 1 || first.Events[0].Seq != "1" {
			t.Fatal("wrong first native sequence")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("publisher barrier did not arrive")
	}
	for i := uint64(2); i <= 100; i++ {
		observe(i, strings.Repeat("v", 8192))
	}
	if count, size, next := out.Stats(); count > nativecontract.LiveReplayFrames || size > nativecontract.LiveReplayBytes || next != 101 {
		t.Fatal("outbound outage grew unbounded memory")
	}
	final := nativecontract.FinalPublish{RequestID: fixtureID, ExpectedBinding: fixtureBinding(), FinalID: fixtureID, InputID: fixtureID, TurnRef: "qualified-turn", Text: "EXPLICIT_FINAL_ONLY_IN_SINK", DrainGrantID: fixtureID}
	if err := out.Observe(ctx, nativecontract.ProviderEvent{SourceSequence: 101, Identity: nativecontract.NativeIdentity{RuntimeEpoch: fixtureID}, Final: &final}); err != nil {
		t.Fatal(err)
	}
	if len(sink.values) != 1 || sink.values[0].Text != final.Text {
		t.Fatal("explicit final did not use FinalSink")
	}
	sink.fail = true
	err = out.Observe(ctx, nativecontract.ProviderEvent{SourceSequence: 102, Identity: nativecontract.NativeIdentity{RuntimeEpoch: fixtureID}, Final: &final})
	requireReason(t, err, nativecontract.ReasonNativeFailure)
	publisher.release <- struct{}{}
	select {
	case next := <-publisher.batches:
		if len(next.Events) < 2 || next.Events[0].Kind != "gap" {
			t.Fatal("outage loss was invisible")
		}
		for _, event := range next.Events {
			if strings.Contains(string(event.Payload), final.Text) || event.Kind == "assistant.final" {
				t.Fatal("explicit final entered live replay")
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("recovery batch did not arrive")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("pump did not terminate on its owning lifetime")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pump did not terminate")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestHTTPPublisherBoundsAndBindsAcknowledgement(t *testing.T) {
	request := nativecontract.Publish{RequestID: fixtureID, ExpectedBinding: fixtureBinding(), Events: []nativecontract.StreamEvent{fixtureEvent(1, "assistant.delta", nativecontract.AssistantDelta{MessageID: fixtureID, Text: "visible"})}}
	for _, variant := range []string{"ok", "wrong binding", "oversize response", "redirect", "unsafe error"} {
		t.Run(variant, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.String() != "https://native.invalid/api/agentd/native-sessions/events" || r.Header.Get("X-Aeon-Worker-Lease") != "synthetic-proof" || r.URL.RawQuery != "" {
					t.Fatal("paired proof/route escaped the header boundary")
				}
				binding := fixtureBinding()
				if variant == "wrong binding" {
					binding.BindingRevision++
				}
				result := nativecontract.OperationResult{Receipt: nativecontract.Receipt{RequestID: fixtureID, OperationID: fixtureID, Binding: &binding, Operation: "start", State: "applied", SourceSequence: "0", UpdatedAt: time.Now()}}
				body, _ := json.Marshal(result)
				status := 200
				if variant == "oversize response" {
					body = []byte(strings.Repeat("x", nativecontract.RequestBodyBytes+1))
				}
				if variant == "redirect" {
					status = 302
				}
				if variant == "unsafe error" {
					status = 500
					body = []byte(`{"raw":"SYNTHETIC_ERROR_DETAIL"}`)
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Aeon-Contract": []string{nativecontract.ContractVersion}, "Location": []string{"https://foreign.invalid/"}}, Body: io.NopCloser(strings.NewReader(string(body))), ContentLength: int64(len(body))}, nil
			})}
			publisher, err := NewHTTPPublisher("https://native.invalid", client, func(r *http.Request) error { r.Header.Set("X-Aeon-Worker-Lease", "synthetic-proof"); return nil })
			if err != nil {
				t.Fatal(err)
			}
			_, err = publisher.Publish(context.Background(), request)
			if variant == "ok" && err != nil {
				t.Fatal(err)
			}
			if variant != "ok" && err == nil {
				t.Fatal("invalid acknowledgement was accepted")
			}
			if calls != 1 {
				t.Fatal("publisher followed a credential-bearing redirect")
			}
			if variant == "wrong binding" {
				requireReason(t, err, nativecontract.ReasonIdentityMismatch)
			}
			if variant == "oversize response" {
				requireReason(t, err, nativecontract.ReasonBufferFull)
			}
			if err != nil && strings.Contains(err.Error(), "SYNTHETIC_ERROR_DETAIL") {
				t.Fatal("native error body entered diagnostics")
			}
		})
	}
}
