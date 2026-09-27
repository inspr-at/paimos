// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"

	"github.com/inspr-at/paimos/internal/sessionusage"
)

var syntheticSession = HarnessSession{ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", ProjectID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", Lease: "synthetic-worker-lease-32-characters-minimum"}

func usageSnapshot(input int64, final bool) sessionusage.UsageReport {
	output, cached := int64(20), int64(30)
	return sessionusage.UsageReport{Model: "model-a", InputTokens: &input, OutputTokens: &output, CachedInputTokens: &cached, Provisional: !final, BillingMode: "unknown"}
}

func finishUsageTest(t *testing.T, r *sessionUsageReporter) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	return r.finish(ctx)
}

func TestSessionUsageRetryExactReceiptAndFinalSettlement(t *testing.T) {
	for _, failure := range []int{0, 408, 429, 503} {
		t.Run(http.StatusText(failure), func(t *testing.T) {
			var mu sync.Mutex
			var bodies []string
			first := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.Method != "POST" || req.URL.Path != harnessPath(syntheticSession)+"/usage" || req.Header.Get("X-Aeon-Worker-Lease") != syntheticSession.Lease || req.Header.Get("Authorization") != "Bearer synthetic-key" {
					t.Error("wrong endpoint or worker binding")
				}
				b, _ := io.ReadAll(req.Body)
				mu.Lock()
				bodies = append(bodies, string(b))
				n := len(bodies)
				mu.Unlock()
				if n == 1 {
					defer close(first)
					if failure == 0 {
						conn, _, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Error(err)
							return
						}
						_ = conn.Close()
						return
					}
					w.WriteHeader(failure)
					return
				}
				_, _ = w.Write([]byte(`{"usage":{},"replayed":true}`))
			}))
			defer server.Close()
			r := newSessionUsageReporter(NewRemote(server.URL, "synthetic-key"), syntheticSession, func() bool { return false }, func() { t.Error("unexpected archive") })
			r.submit(usageSnapshot(100, false))
			select {
			case <-first:
			case <-time.After(2 * time.Second):
				t.Fatal("first request missing")
			}
			r.submit(usageSnapshot(140, false))
			r.submit(usageSnapshot(150, true))
			if err := finishUsageTest(t, r); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(bodies) < 3 || bodies[0] != bodies[1] {
				t.Fatalf("uncertain POST not retried unchanged: %d requests", len(bodies))
			}
			var last sessionusage.UsageReport
			if json.Unmarshal([]byte(bodies[len(bodies)-1]), &last) != nil || *last.InputTokens != 150 || last.Provisional || last.Sequence != 3 || last.ReportID == "" || last.BillingMode != "unknown" {
				t.Fatalf("final report: %+v", last)
			}
		})
	}
}

func TestSessionUsageArchiveAndPermanentRejectionStopWrites(t *testing.T) {
	for _, status := range []int{403, 409, 410} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			var archived atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":"harness generation archived"}`))
			}))
			defer server.Close()
			r := newSessionUsageReporter(NewRemote(server.URL, "synthetic-key"), syntheticSession, archived.Load, func() { archived.Store(true) })
			r.submit(usageSnapshot(100, false))
			err := finishUsageTest(t, r)
			if err == nil || errors.Is(err, ErrHarnessArchived) != (status == 410) || archived.Load() != (status == 410) {
				t.Fatalf("status %d handling: %v", status, err)
			}
			r.submit(usageSnapshot(200, true))
			if calls.Load() != 1 {
				t.Fatal("permanent rejection retried")
			}
		})
	}
}

func TestSessionUsageExistingFenceAndMissingBinding(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	api := NewRemote(server.URL, "synthetic-key")
	r := newSessionUsageReporter(api, syntheticSession, func() bool { return true }, func() {})
	r.submit(usageSnapshot(100, false))
	if !errors.Is(finishUsageTest(t, r), ErrHarnessArchived) || calls.Load() != 0 {
		t.Fatal("archived generation wrote usage")
	}
	for _, binding := range []HarnessSession{{}, {ID: "run", ProjectID: "project"}, {ID: "session", Lease: syntheticSession.Lease}} {
		if api.ReportSessionUsage(t.Context(), binding, usageSnapshot(100, false)) == nil {
			t.Fatal("unregistered binding accepted")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid binding made HTTP request")
	}
}

func TestSessionUsageFinalRetryDeadline(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(503) }))
	defer server.Close()
	r := newSessionUsageReporter(NewRemote(server.URL, "synthetic-key"), syntheticSession, func() bool { return false }, func() {})
	r.submit(usageSnapshot(100, true))
	ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
	defer cancel()
	if err := r.finish(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline: %v", err)
	}
	if calls.Load() < 2 {
		t.Fatal("did not retry final report")
	}
}

func TestSessionUsageFinalDeadlineIncludesJoiningReporter(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	r := newSessionUsageReporter(NewRemote("http://127.0.0.1:1", "synthetic-key"), syntheticSession, func() bool {
		once.Do(func() { close(entered) })
		<-release
		return false
	}, func() {})
	r.submit(usageSnapshot(100, false))
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("reporter did not start")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	err := r.finish(ctx)
	close(release)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("joining ignored final deadline: %v", err)
	}
	select {
	case <-r.done:
	case <-time.After(time.Second):
		t.Fatal("cancelled reporter did not exit")
	}
}

func TestManagedCodexSplitStreamToSessionEndpoint(t *testing.T) {
	var mu sync.Mutex
	var reports []sessionusage.UsageReport
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var report sessionusage.UsageReport
		if json.NewDecoder(r.Body).Decode(&report) != nil {
			t.Error("bad report")
		}
		mu.Lock()
		reports = append(reports, report)
		mu.Unlock()
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	reporter := newSessionUsageReporter(NewRemote(server.URL, "synthetic-key"), syntheticSession, func() bool { return false }, func() {})
	capture, err := sessionusage.NewManagedCodex("synthetic-thread", "model-a")
	if err != nil {
		t.Fatal(err)
	}
	var runInput, runOutput int64
	wire := &wireProcess{readDone: make(chan struct{}), pending: map[string]chan json.RawMessage{}, threadID: "synthetic-thread", protocol: "jsonrpc", turnID: "synthetic-turn", waitDone: make(chan struct{})}
	wire.observe = func(ev AdapterEvent) {
		if ev.SessionUsage != nil {
			reporter.submit(*ev.SessionUsage)
		}
		runInput += ev.InputTokensDelta
		runOutput += ev.OutputTokensDelta
	}
	proc := &codexProcess{wireProcess: wire, usage: capture, done: make(chan bool, 1), acknowledged: true}
	wire.setOnEvent(proc.notification)
	stream := strings.Join([]string{
		`{"method":"item/agentMessage/delta","params":{"delta":"synthetic private text"}}`,
		`{"method":"thread/tokenUsage/updated","params":{"threadId":"synthetic-thread","tokenUsage":{"total":{"inputTokens":100,"outputTokens":20,"cachedInputTokens":30}}}}`,
		`{"method":"thread/tokenUsage/updated","params":{"threadId":"synthetic-thread","tokenUsage":{"total":{"inputTokens":100,"outputTokens":20,"cachedInputTokens":30}}}}`,
		`{"method":"model/rerouted","params":{"threadId":"synthetic-thread","turnId":"synthetic-turn","fromModel":"model-a","toModel":"model-b","reason":"synthetic private text"}}`,
		`{"method":"thread/tokenUsage/updated","params":{"threadId":"synthetic-thread","turnId":"synthetic-turn","tokenUsage":{"total":{"inputTokens":150,"outputTokens":28,"cachedInputTokens":40},"last":{"inputTokens":50,"outputTokens":8,"cachedInputTokens":10}}}}`,
		`{"method":"turn/completed","params":{"threadId":"synthetic-thread","turn":{"id":"synthetic-turn","status":"completed"}}}`,
	}, "\n") + "\n"
	wire.read(iotest.OneByteReader(strings.NewReader(stream)))
	if err := proc.waitForTurn(func(context.Context) error { close(wire.waitDone); return nil }, time.Second); err != nil {
		t.Fatal(err)
	}
	if err := finishUsageTest(t, reporter); err != nil {
		t.Fatal(err)
	}
	if runInput != 150 || runOutput != 28 {
		t.Fatalf("run settlement changed: %d/%d", runInput, runOutput)
	}
	mu.Lock()
	defer mu.Unlock()
	final := map[string]sessionusage.UsageReport{}
	for _, r := range reports {
		final[r.Model] = r
		b, _ := json.Marshal(r)
		if strings.Contains(string(b), "synthetic") {
			t.Fatal("text or thread identity reached endpoint")
		}
	}
	if len(final) != 2 || *final["model-a"].InputTokens != 100 || *final["model-b"].InputTokens != 50 || final["model-a"].Provisional || final["model-b"].Provisional {
		t.Fatal("split stream/model/final settlement lost")
	}
}

type terminalWaitProcess struct {
	*fakeProcess
	codex *codexProcess
}

type terminalFixtureAdapter struct{ process Process }

func (*terminalFixtureAdapter) Name() string                       { return Codex }
func (*terminalFixtureAdapter) Probe(context.Context, string) bool { return true }
func (a *terminalFixtureAdapter) Start(_ context.Context, _ StartRequest, _ func(AdapterEvent)) (Process, error) {
	return a.process, nil
}

func (p *terminalWaitProcess) Wait() error {
	return p.codex.waitForTurn(func(context.Context) error { close(p.codex.waitDone); return nil }, time.Second)
}

func TestManagedCodexTerminalFailurePreventsCompletedRunAndFinalUsage(t *testing.T) {
	for _, tc := range []struct {
		name, method, turn string
		clean              bool
	}{
		{"clean", "turn/completed", `{"id":"synthetic-turn","status":"completed"}`, true},
		{"failed_method", "turn/failed", `{"id":"synthetic-turn","status":"failed","error":"PRIVATE_SENTINEL"}`, false},
		{"failed_status", "turn/completed", `{"id":"synthetic-turn","status":"failed","error":"PRIVATE_SENTINEL"}`, false},
		{"interrupted", "turn/completed", `{"id":"synthetic-turn","status":"interrupted","error":"PRIVATE_SENTINEL"}`, false},
		{"missing_status", "turn/completed", `{"id":"synthetic-turn","error":"PRIVATE_SENTINEL"}`, false},
		{"malformed_turn", "turn/completed", `"PRIVATE_SENTINEL"`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, base, fake := testSupervisor(t)
			defer s.Close(context.Background())
			api := &doneToolAPI{fakeAPI: base}
			s.api = api
			capture, err := sessionusage.NewManagedCodex("synthetic-thread", "model")
			if err != nil {
				t.Fatal(err)
			}
			var reports []sessionusage.UsageReport
			wire := &wireProcess{threadID: "synthetic-thread", turnID: "synthetic-turn", readDone: make(chan struct{}), waitDone: make(chan struct{}), observe: func(ev AdapterEvent) {
				if ev.SessionUsage != nil {
					reports = append(reports, *ev.SessionUsage)
				}
			}}
			codex := &codexProcess{wireProcess: wire, usage: capture, done: make(chan bool, 1), acknowledged: true}
			s.adapters[Codex] = &terminalFixtureAdapter{process: &terminalWaitProcess{fakeProcess: fake, codex: codex}}
			if err := s.PollOnce(t.Context()); err != nil {
				t.Fatal(err)
			}
			entry := s.runs["run"]
			entry.mu.Lock()
			entry.doneRequested = true
			entry.mu.Unlock()
			codex.notification(json.RawMessage(`{"jsonrpc":"2.0","method":"thread/tokenUsage/updated","params":{"threadId":"synthetic-thread","tokenUsage":{"total":{"inputTokens":12,"outputTokens":3,"cachedInputTokens":2}}}}`))
			terminal := `{"jsonrpc":"2.0","method":"` + tc.method + `","params":{"threadId":"synthetic-thread","turn":` + tc.turn + `}}`
			wire.eventMu.Lock()
			codex.notification(json.RawMessage(terminal))
			wire.eventMu.Unlock()
			close(wire.readDone)
			select {
			case <-entry.monitorDone:
			case <-time.After(3 * time.Second):
				t.Fatal("terminal did not settle run")
			}
			if len(reports) != 2 || reports[1].Provisional == tc.clean {
				t.Fatalf("usage finality: %+v", reports)
			}
			base.mu.Lock()
			last := base.reports[len(base.reports)-1]
			base.mu.Unlock()
			api.mu.Lock()
			done := api.done
			api.mu.Unlock()
			if last.Status == "completed" != tc.clean || done != tc.clean {
				t.Fatalf("run=%s done=%t clean=%t", last.Status, done, tc.clean)
			}
		})
	}
}

// This adapter emits only synthetic protocol notifications; no vendor process,
// transcript, credentials store or filesystem capture is used by this test.
type usageTestAdapter struct{ proc *fakeProcess }

func (*usageTestAdapter) Name() string                       { return Codex }
func (*usageTestAdapter) Probe(context.Context, string) bool { return true }
func (a *usageTestAdapter) Start(_ context.Context, _ StartRequest, observe func(AdapterEvent)) (Process, error) {
	r := usageSnapshot(100, false)
	observe(AdapterEvent{SessionUsage: &r})
	observe(AdapterEvent{Kind: "usage", InputTokensDelta: 100, OutputTokensDelta: 20})
	return a.proc, nil
}

type usageTestAPI struct {
	*fakeAPI
	remote *Remote
}

func (a *usageTestAPI) ReportSessionUsage(ctx context.Context, s HarnessSession, r sessionusage.UsageReport) error {
	return a.remote.ReportSessionUsage(ctx, s, r)
}

func TestSupervisorUsageArchiveDetachesWithoutStoppingRun(t *testing.T) {
	s, api, proc := testSupervisor(t)
	defer s.Close(context.Background())
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/projects/project/harness-sessions/session/usage" {
			t.Error("not bound to registration response")
		}
		api.mu.Lock()
		lease := api.harnessRegistration.Lease
		api.mu.Unlock()
		if r.Header.Get("X-Aeon-Worker-Lease") != lease {
			t.Error("lease mismatch")
		}
		calls.Add(1)
		w.WriteHeader(410)
		_, _ = w.Write([]byte(`{"error":"harness generation archived"}`))
	}))
	defer server.Close()
	s.api = &usageTestAPI{api, NewRemote(server.URL, "synthetic-key")}
	s.adapters[Codex] = &usageTestAdapter{proc}
	if err := s.PollOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	entry := s.runs["run"]
	s.mu.Unlock()
	if err := finishUsageTest(t, entry.usage); !errors.Is(err, ErrHarnessArchived) {
		t.Fatal(err)
	}
	select {
	case <-proc.stopped:
		t.Fatal("archive signalled child")
	default:
	}
	if err := s.serviceHarness(t.Context(), entry); err != nil {
		t.Fatal(err)
	}
	_ = proc.Stop(t.Context())
	select {
	case <-entry.monitorDone:
	case <-time.After(2 * time.Second):
		t.Fatal("run not settled")
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.harnessStops) != 0 || calls.Load() != 1 {
		t.Fatal("worker wrote after archive")
	}
	if len(api.reports) == 0 || api.reports[len(api.reports)-1].Kind != "finished" {
		t.Fatal("archive lost existing run settlement")
	}
}
