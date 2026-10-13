// SPDX-License-Identifier: AGPL-3.0-only

package stream

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativestream"
)

const testID = "00000000-0000-4000-8000-000000000001"

func testBinding() nativecontract.Binding {
	return nativecontract.Binding{TenantID: testID, ProjectID: testID, OwnedSessionID: testID, HarnessSessionID: testID, RuntimeEpoch: testID, DaemonID: testID, DaemonGeneration: testID, BindingRevision: 1}
}
func testSnapshot() nativecontract.SessionView {
	binding := testBinding()
	return nativecontract.SessionView{OwnedSessionID: testID, CurrentBinding: &binding, OwnerPersonID: testID, Role: "coordinator", Profile: "native", Harness: "claude", State: "taken", Mode: "terminal", Revision: 1, Generation: 1, ConversationRef: "qualified-conversation", Capabilities: nativecontract.Capabilities{Expert: true, TerminalView: true, EventsSupported: true, Steer: "unsupported", Quota: "unsupported", QualificationDigest: strings.Repeat("a", 64)}}
}
func testAccess(view string, private bool) nativecontract.ValidationContext {
	snapshot := testSnapshot()
	kind := "person"
	if private {
		kind = "agent"
	}
	return nativecontract.ValidationContext{Binding: snapshot.CurrentBinding, Generation: 1, PrincipalKind: kind, View: view, Mode: snapshot.Mode, State: snapshot.State, Capabilities: &snapshot.Capabilities, Private: private}
}
func testProjector(t *testing.T) *nativestream.Projector {
	t.Helper()
	p, err := nativestream.NewProjector(nativestream.ExportPolicy{Text: func(_ context.Context, _ nativecontract.Binding, text string) (string, error) { return text, nil }, Terminal: func(_ context.Context, _ nativecontract.Binding, data []byte) error {
		if strings.Contains(string(data), "SYNTHETIC_LOGIN") {
			return errors.New("screen export refused")
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func testEvent(seq uint64, kind string, payload any) nativecontract.StreamEvent {
	data, _ := json.Marshal(payload)
	position := strconv.FormatUint(seq, 10)
	return nativecontract.StreamEvent{Binding: testBinding(), Generation: 1, Sequence: position, Seq: position, Source: "native", Kind: kind, Payload: data, ObservedAt: time.Date(2026, 10, 13, 1, 0, 0, 0, time.UTC), DroppedFrames: "0"}
}
func requireEvent(t *testing.T, sub nativecontract.Subscription, kind string) nativecontract.StreamEvent {
	t.Helper()
	select {
	case event, ok := <-sub.Events():
		if !ok || event.Kind != kind {
			t.Fatalf("got closed=%t / %s; want %s", !ok, event.Kind, kind)
		}
		return event
	case <-time.After(5 * time.Second):
		t.Fatal("subscription barrier timed out")
		return nativecontract.StreamEvent{}
	}
}
func requireClosed(t *testing.T, sub nativecontract.Subscription) {
	t.Helper()
	select {
	case _, ok := <-sub.Events():
		if ok {
			t.Fatal("revoked/closed subscription delivered content")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("subscription did not close")
	}
}
func barrier(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("access barrier did not arrive")
	}
}

type rights struct {
	mu      sync.Mutex
	access  nativecontract.ValidationContext
	revoked bool
	pause   bool
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newRights(view string) *rights {
	return &rights{access: testAccess(view, false), entered: make(chan struct{}), release: make(chan struct{})}
}
func (g *rights) fence(ctx context.Context, deliver func(nativecontract.ValidationContext) error) error {
	g.mu.Lock()
	pause := g.pause
	g.mu.Unlock()
	if pause {
		g.once.Do(func() { close(g.entered) })
		select {
		case <-g.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.revoked {
		return nativestream.Refuse(nativecontract.ReasonPermissionRevoked)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return deliver(g.access)
}
func (g *rights) arm()    { g.mu.Lock(); g.pause = true; g.mu.Unlock() }
func (g *rights) revoke() { g.mu.Lock(); g.revoked = true; g.mu.Unlock() }
func privateFence(ctx context.Context, deliver func(nativecontract.ValidationContext) error) error {
	return deliver(testAccess("", true))
}
func publish(t *testing.T, r *Relay, events ...nativecontract.StreamEvent) {
	t.Helper()
	if err := r.Publish(context.Background(), nativecontract.Publish{RequestID: testID, ExpectedBinding: testBinding(), Events: events}, privateFence); err != nil {
		t.Fatal(err)
	}
}
func subscribe(t *testing.T, r *Relay, guard *rights) nativecontract.Subscription {
	t.Helper()
	sub, err := r.SubscribeView(context.Background(), testBinding(), Viewer{Query: nativecontract.StreamQuery{Generation: 1, View: guard.access.View}, Fence: guard.fence})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sub.Close() })
	requireEvent(t, sub, "snapshot")
	return sub
}

// Named risk: a slow raw relay leaks private vendor data, grows memory, or sends
// already-queued content after access withdrawal. Barriers establish both races;
// timeouts only detect hangs, and fixture content remains available for assertions.
func TestSlowSubscriberRevocationAndViewIsolation(t *testing.T) {
	r, err := NewRelay(testProjector(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Register(testSnapshot()); err != nil {
		t.Fatal(err)
	}
	// Identical session UUIDs in separate tenants must not replace each other.
	other := testSnapshot()
	otherBinding := *other.CurrentBinding
	otherBinding.TenantID = "00000000-0000-4000-8000-000000000002"
	other.CurrentBinding = &otherBinding
	if err := r.Register(other); err != nil {
		t.Fatal("tenant-scoped owned session collided:", err)
	}
	if _, _, _, err := r.Stats(testBinding()); err != nil {
		t.Fatal("other tenant replaced the current session")
	}
	if _, err := r.Subscribe(context.Background(), testBinding()); err == nil {
		t.Fatal("FrameSource accepted a caller without authenticated viewer context")
	}
	agentAccess := testAccess("expert", false)
	agentAccess.PrincipalKind = "agent"
	_, err = r.SubscribeView(context.Background(), testBinding(), Viewer{Query: nativecontract.StreamQuery{Generation: 1, View: "expert"}, Fence: func(ctx context.Context, deliver func(nativecontract.ValidationContext) error) error {
		return deliver(agentAccess)
	}})
	var denied *nativecontract.ValidationError
	if !errors.As(err, &denied) || denied.Reason != nativecontract.ReasonWrongPrincipal {
		t.Fatal("agent principal received an Expert view")
	}
	structured := subscribe(t, r, newRights("structured"))
	expert := subscribe(t, r, newRights("expert"))
	terminal := subscribe(t, r, newRights("terminal"))
	text := "shared normalized output"
	publish(t, r, testEvent(1, "structured", nativecontract.StructuredFrame{Kind: "assistant_delta", Text: &text}), testEvent(2, "expert", nativecontract.ExpertFrame{Provider: "claude", EventName: "assistant", FrameJSON: `{"type":"assistant","authorization":"SYNTHETIC_AUTH","message":{"content":[{"type":"text","text":"expert text"},{"type":"thinking","thinking":"SYNTHETIC_REASONING"},{"type":"tool_result","content":"SYNTHETIC_TOOL_BODY"}]}}`, RedactedPaths: []string{}, OriginalBytes: 300}), testEvent(3, "terminal", nativecontract.TerminalFrame{TerminalEpoch: testID, Sequence: "1", DataBase64: base64.StdEncoding.EncodeToString([]byte("qualified screen")), Rows: 24, Cols: 80}))
	for _, sub := range []nativecontract.Subscription{structured, expert, terminal} {
		event := requireEvent(t, sub, "structured")
		if !event.Binding.Equal(testBinding()) || event.Seq != "1" {
			t.Fatal("views do not share one owned source binding/order")
		}
	}
	frame := requireEvent(t, expert, "expert")
	if strings.Contains(string(frame.Payload), "SYNTHETIC_") || !strings.Contains(string(frame.Payload), "expert text") {
		t.Fatal("server trusted raw Expert body without revalidation")
	}
	if frame := requireEvent(t, terminal, "terminal"); frame.Seq != "3" {
		t.Fatal("terminal source correlation changed")
	}
	structured.Close()
	expert.Close()
	terminal.Close()
	guard := newRights("expert")
	slow := subscribe(t, r, guard)
	guard.arm()
	queuedText := "QUEUED_BEFORE_REVOCATION"
	publish(t, r, testEvent(4, "structured", nativecontract.StructuredFrame{Kind: "assistant_delta", Text: &queuedText}))
	barrier(t, guard.entered)
	// The queued frame is held before the final permission lock. The reader
	// continues independently and evicts the oldest frames in bounded RAM.
	for seq := uint64(5); seq <= 100; seq++ {
		text := strings.Repeat("v", 8192)
		publish(t, r, testEvent(seq, "structured", nativecontract.StructuredFrame{Kind: "assistant_delta", Text: &text}))
	}
	frames, bytes, _, err := r.Stats(testBinding())
	if err != nil {
		t.Fatal(err)
	}
	if frames > nativecontract.LiveReplayFrames || bytes > nativecontract.LiveReplayBytes {
		t.Fatal("stalled subscriber grew relay memory past its bounds")
	}
	guard.revoke()
	close(guard.release)
	requireClosed(t, slow)
	// A fresh authorized page exposes the missing prefix rather than calling
	// the retained token/Expert ring complete or durable history.
	reader := newRights("expert")
	page, err := r.Page(context.Background(), testBinding(), nativecontract.EventQuery{Generation: 1, Limit: 2}, Viewer{Query: nativecontract.StreamQuery{Generation: 1, View: "expert"}, Fence: reader.fence})
	if err != nil {
		t.Fatal(err)
	}
	if !page.Truncated || len(page.Items) != 2 || page.Items[0].Seq != "100" {
		t.Fatal("eviction was not visible in the retained ordered page")
	}
	_, err = r.SubscribeView(context.Background(), testBinding(), Viewer{Query: nativecontract.StreamQuery{Generation: 1, View: "expert", After: "1:3"}, Fence: reader.fence})
	var refusal *nativecontract.ValidationError
	if !errors.As(err, &refusal) || refusal.Reason != nativecontract.ReasonCursorExpired {
		t.Fatal("evicted cursor was not rejected before SSE headers")
	}
}

func TestLiveGapClosesAndBindingReplacementRefusesOldView(t *testing.T) {
	r, err := NewRelay(testProjector(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Register(testSnapshot()); err != nil {
		t.Fatal(err)
	}
	guard := newRights("structured")
	sub := subscribe(t, r, guard)
	guard.arm()
	first := "in flight"
	publish(t, r, testEvent(1, "structured", nativecontract.StructuredFrame{Kind: "assistant_delta", Text: &first}))
	barrier(t, guard.entered)
	for seq := uint64(2); seq <= 100; seq++ {
		text := "retained"
		publish(t, r, testEvent(seq, "structured", nativecontract.StructuredFrame{Kind: "assistant_delta", Text: &text}))
	}
	close(guard.release)
	requireEvent(t, sub, "structured")
	gap := requireEvent(t, sub, "gap")
	var loss nativecontract.Gap
	_ = json.Unmarshal(gap.Payload, &loss)
	if loss.DroppedFrames == "0" || loss.Reason != "buffer_evicted" || !loss.ReloadSnapshot {
		t.Fatal("in-stream loss was concealed")
	}
	requireClosed(t, sub)
	oldGuard := newRights("expert")
	old := subscribe(t, r, oldGuard)
	successor := testSnapshot()
	binding := *successor.CurrentBinding
	binding.RuntimeEpoch = "00000000-0000-4000-8000-000000000002"
	binding.HarnessSessionID = binding.RuntimeEpoch
	binding.BindingRevision = 2
	successor.CurrentBinding = &binding
	successor.Generation = 2
	successor.Revision = 2
	if err := r.Register(successor); err != nil {
		t.Fatal(err)
	}
	requireClosed(t, old)
	_, err = r.SubscribeView(context.Background(), testBinding(), Viewer{Query: nativecontract.StreamQuery{Generation: 1, View: "expert"}, Fence: oldGuard.fence})
	var refusal *nativecontract.ValidationError
	if !errors.As(err, &refusal) || refusal.Reason != nativecontract.ReasonWrongGeneration {
		t.Fatal("stale subscriber followed successor runtime")
	}
}

type readAuthority struct {
	viewer  Viewer
	binding nativecontract.Binding
}

func (a readAuthority) AuthenticateViewer(*http.Request) (nativecontract.Binding, Viewer, error) {
	return a.binding, a.viewer, nil
}

type streamRecorder struct {
	*httptest.ResponseRecorder
	flushed chan struct{}
}

func (w *streamRecorder) SetWriteDeadline(time.Time) error { return nil }
func (w *streamRecorder) Flush() {
	w.ResponseRecorder.Flush()
	select {
	case w.flushed <- struct{}{}:
	default:
	}
}

func TestServerHTTPAuthenticatesViewerAndEmitsGapEnd(t *testing.T) {
	r, err := NewRelay(testProjector(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Register(testSnapshot()); err != nil {
		t.Fatal(err)
	}
	guard := newRights("structured")
	viewer := Viewer{Query: nativecontract.StreamQuery{Generation: 1, View: "structured"}, Fence: guard.fence}
	handler, err := NewHTTPHandler(r, readAuthority{viewer: viewer, binding: testBinding()})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/projects/{projectId}/native-sessions/{ownedSessionId}/stream", handler.Stream)
	query := url.Values{"generation": {"1"}, "expected_runtime_epoch": {testID}, "expected_harness_session_id": {testID}, "expected_binding_revision": {"1"}}
	request := func() *http.Request {
		req := httptest.NewRequest("GET", "/api/projects/"+testID+"/native-sessions/"+testID+"/stream?"+query.Encode(), nil)
		req.Header.Set(nativecontract.ViewerHeader, testID)
		return req
	}
	missing := request()
	missing.Header.Del(nativecontract.ViewerHeader)
	bad := httptest.NewRecorder()
	mux.ServeHTTP(bad, missing)
	if bad.Code != 403 || !strings.Contains(bad.Body.String(), `"reason":"grant_missing"`) {
		t.Fatal("missing attach viewer reached server stream")
	}
	query.Set("expected_binding_revision", "2")
	bad = httptest.NewRecorder()
	mux.ServeHTTP(bad, request())
	if bad.Code != 409 || !strings.Contains(bad.Body.String(), `"reason":"wrong_generation"`) {
		t.Fatal("stale HTTP selectors reached a new runtime")
	}
	query.Set("expected_binding_revision", "1")
	w := &streamRecorder{ResponseRecorder: httptest.NewRecorder(), flushed: make(chan struct{}, 8)}
	req := request()
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	done := make(chan struct{})
	go func() { mux.ServeHTTP(w, req.WithContext(ctx)); close(done) }()
	barrier(t, w.flushed)
	publish(t, r, testEvent(1, "gap", nativecontract.Gap{Reason: "frame_omitted", DroppedFrames: "0", ReloadSnapshot: true}))
	barrier(t, done)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Body.String(), "event: snapshot") || !strings.Contains(w.Body.String(), "event: gap") || !strings.Contains(w.Body.String(), "event: end") || !strings.Contains(w.Body.String(), "id: 1:1") {
		t.Fatal("HTTP transient loss did not emit the C0 snapshot/gap/end stream")
	}
}
