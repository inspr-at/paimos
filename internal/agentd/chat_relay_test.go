// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/client"
)

// Risk: a final answer assembled from deltas, or emitted for a failed or
// interrupted turn, would persist text the harness never declared final.
func TestChatFinalComesOnlyFromHarnessCompletionRecords(t *testing.T) {
	var got []ChatUpdate
	p := &wireProcess{threadID: "thread-1", turnID: "turn-1", observe: func(ev AdapterEvent) { got = append(got, *ev.Chat) }}
	codex := func(raw string) { p.observeChat(Codex, json.RawMessage(raw)) }
	codex(`{"method":"turn/started","params":{"threadId":"thread-1","turn":{"id":"turn-1","status":"inProgress"}}}`)
	codex(`{"method":"item/agentMessage/delta","params":{"threadId":"thread-1","turnId":"turn-1","delta":"Hel"}}`)
	codex(`{"method":"item/completed","params":{"threadId":"thread-1","turnId":"turn-1","item":{"id":"m0","type":"agentMessage","phase":"commentary","text":"COMMENTARY"}}}`)
	codex(`{"method":"item/completed","params":{"threadId":"thread-1","turnId":"turn-1","item":{"id":"m1","type":"agentMessage","text":"Harness final"}}}`)
	codex(`{"method":"turn/completed","params":{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed"}}}`)
	want := []ChatUpdate{chatState("running"), chatText("Hel"), chatFinal("Harness final"), chatState("idle")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("codex final: %#v", got)
	}
	for _, end := range []string{
		`{"method":"turn/completed","params":{"threadId":"thread-1","turn":{"id":"turn-2","status":"interrupted"}}}`,
		`{"method":"turn/failed","params":{"threadId":"thread-1","turn":{"id":"turn-2"}}}`,
	} {
		got, p.turnID = nil, "turn-2"
		codex(`{"method":"turn/started","params":{"threadId":"thread-1","turn":{"id":"turn-2","status":"inProgress"}}}`)
		codex(`{"method":"item/completed","params":{"threadId":"thread-1","turnId":"turn-2","item":{"id":"m2","type":"agentMessage","text":"NOT FINAL"}}}`)
		codex(end)
		for _, u := range got {
			if u.SessionUpdate == "final" {
				t.Fatalf("final for an unsuccessful turn: %s", end)
			}
		}
	}
	got, p.turnID = nil, "turn-3"
	codex(`{"method":"item/agentMessage/delta","params":{"threadId":"thread-1","turnId":"turn-3","delta":"only deltas"}}`)
	codex(`{"method":"turn/completed","params":{"threadId":"thread-1","turn":{"id":"turn-3","status":"completed"}}}`)
	if !reflect.DeepEqual(got, []ChatUpdate{chatText("only deltas"), chatState("idle")}) {
		t.Fatalf("deltas assembled into a final: %#v", got)
	}
	got = nil
	p.observeChat(Claude, json.RawMessage(`{"kind":"chat_final","text":"Claude final"}`))
	p.observeChat(Claude, json.RawMessage(`{"kind":"chat_final","text":"`+strings.Repeat("x", chatChunkBytes+1)+`"}`))
	if !reflect.DeepEqual(got, []ChatUpdate{chatFinal("Claude final")}) {
		t.Fatalf("claude final: %#v", got)
	}
	if ValidChatUpdate(chatFinal("x")) || !chatFinal("x").valid() {
		t.Fatal("final must be valid for the relay but never for the live route")
	}
}

// Risk: the Claude bridge reports a final from a review, an error result or
// partial output instead of the SDK's own successful result record.
func TestClaudeBridgeEmitsFinalFromResultRecordOnly(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("Node required for Claude bridge fixture")
	}
	bridge, err := claudeAssets.ReadFile("claudeassets/bridge.mjs")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, result string
		final        bool
	}{
		{"success", `{type:'result',subtype:'success',is_error:false,result:'fixture-final'}`, true},
		{"error", `{type:'result',subtype:'error_during_execution',is_error:true,result:'fixture-final'}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			bridgePath, sdkPath := filepath.Join(root, "bridge.mjs"), filepath.Join(root, "sdk.mjs")
			sdk := `export function query() {
 return {streamInput:async()=>{},close:()=>{},async *[Symbol.asyncIterator](){
 yield {type:'system',subtype:'init',session_id:'session',model:'model',capabilities:[]};
 yield {type:'stream_event',event:{type:'content_block_delta',delta:{type:'text_delta',text:'fixture-'}}};
 yield ` + tc.result + `;
 }};
}`
			if err := os.WriteFile(bridgePath, bridge, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(sdkPath, []byte(sdk), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, node, bridgePath, sdkPath, "/bin/true", root)
			cmd.Stdin = strings.NewReader(`{"op":"start","purpose":"managed","prompt":"Fixture","capabilities":[]}` + "\n")
			out, _ := cmd.Output() // The fixture SDK ends without a stop request.
			if ctx.Err() != nil {
				t.Fatal("bridge fixture hung")
			}
			var finals []ChatUpdate
			p := &wireProcess{observe: func(ev AdapterEvent) {
				if ev.Chat.SessionUpdate == "final" {
					finals = append(finals, *ev.Chat)
				}
			}}
			for _, line := range bytes.Split(out, []byte("\n")) {
				if len(line) > 0 {
					p.observeChat(Claude, line)
				}
			}
			if tc.final && !reflect.DeepEqual(finals, []ChatUpdate{chatFinal("fixture-final")}) || !tc.final && len(finals) != 0 {
				t.Fatalf("bridge finals %#v from %s", finals, out)
			}
		})
	}
}

type fakeRelayAPI struct {
	mu        sync.Mutex
	binding   ChatRelayBinding
	bindErrs  []error
	lookups   int
	live      []ChatSessionEvent
	liveErrs  []error
	liveGate  chan struct{}
	finals    []string
	finalErrs []error
	inputs    []ChatInput
	receipts  []string
	calls     chan string
}

func newFakeRelayAPI() *fakeRelayAPI {
	return &fakeRelayAPI{binding: ChatRelayBinding{"conversation", "1"}, calls: make(chan string, 1024)}
}

func (f *fakeRelayAPI) note(call string) {
	select {
	case f.calls <- call:
	default:
	}
}

func next[T any](list *[]T) (T, bool) {
	var zero T
	if len(*list) == 0 {
		return zero, false
	}
	v := (*list)[0]
	*list = (*list)[1:]
	return v, true
}

func (f *fakeRelayAPI) CurrentChatBinding(context.Context, HarnessSession) (ChatRelayBinding, error) {
	f.mu.Lock()
	f.lookups++
	err, _ := next(&f.bindErrs)
	b := f.binding
	f.mu.Unlock()
	f.note("lookup")
	return b, err
}

func (f *fakeRelayAPI) PublishChatLive(_ context.Context, _ HarnessSession, _ ChatRelayBinding, ev ChatSessionEvent) error {
	f.mu.Lock()
	gate := f.liveGate
	f.mu.Unlock()
	if gate != nil {
		f.note("live-blocked")
		<-gate
	}
	f.mu.Lock()
	err, _ := next(&f.liveErrs)
	if err == nil {
		f.live = append(f.live, ev)
	}
	f.mu.Unlock()
	f.note("live")
	return err
}

func (f *fakeRelayAPI) PersistChatFinal(_ context.Context, _ HarnessSession, _ ChatRelayBinding, clientID, body string) error {
	f.mu.Lock()
	f.finals = append(f.finals, clientID+"|"+body)
	err, _ := next(&f.finalErrs)
	f.mu.Unlock()
	f.note("final")
	return err
}

func (f *fakeRelayAPI) ChatInputs(context.Context, HarnessSession, ChatRelayBinding, string) (ChatInputPage, error) {
	f.mu.Lock()
	page := ChatInputPage{Items: append([]ChatInput(nil), f.inputs...)}
	f.mu.Unlock()
	f.note("inputs")
	return page, nil
}

func (f *fakeRelayAPI) ReportChatReceipt(_ context.Context, _ HarnessSession, _ ChatRelayBinding, id, state string) error {
	f.mu.Lock()
	f.receipts = append(f.receipts, id+":"+state)
	f.mu.Unlock()
	f.note("receipt")
	return nil
}

func waitCall(t *testing.T, calls <-chan string, want string) {
	t.Helper()
	timeout := time.NewTimer(10 * time.Second) // hang guard only
	defer timeout.Stop()
	for {
		select {
		case got := <-calls:
			if got == want {
				return
			}
		case <-timeout.C:
			t.Fatalf("no %s call", want)
		}
	}
}

func receive(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(10 * time.Second): // hang guard only
		t.Fatal("no delivery")
		return ""
	}
}

// eventually waits for a condition another goroutine establishes; the bound
// only guards against hangs and is not part of any assertion.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("never: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func chatInput(id, state string) ChatInput {
	var in ChatInput
	in.Message.ID, in.Message.Body, in.Receipt.State = id, "PRIVATE_SENTINEL person input", state
	return in
}

// captureLogs proves the content-free logging claim for everything the relay
// and its gate do while the test runs.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buf
}

// Risks: an unbounded or silently lossy queue, a final persisted twice or with
// a changed identity on retry, hot retry loops, sending while unbound, and
// continuing against a server that has no relay routes.
func TestChatRelayBoundedQueueBackoffRebindAndUnsupported(t *testing.T) {
	logs := captureLogs(t)
	api := newFakeRelayAPI()
	var mu sync.Mutex
	var waits []time.Duration
	after := func(d time.Duration) <-chan time.Time {
		mu.Lock()
		waits = append(waits, d)
		mu.Unlock()
		fire := make(chan time.Time, 1)
		fire <- time.Time{}
		return fire
	}
	api.bindErrs = []error{&client.StatusError{Status: 404, Message: "chat binding unavailable"}}
	gate := make(chan struct{})
	api.liveGate = gate
	var reasons []string
	r := NewChatRelay(api, HarnessSession{ID: "session", Lease: "lease"}, ChatRelayOptions{After: after, Unsupported: func(reason string) {
		mu.Lock()
		defer mu.Unlock()
		reasons = append(reasons, reason)
	}})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go r.Run(ctx)
	waitCall(t, api.calls, "lookup") // unbound: rechecks after 15 s plus jitter
	waitCall(t, api.calls, "lookup")
	mu.Lock()
	if len(waits) != 1 || waits[0] < chatRelayRecheck || waits[0] >= chatRelayRecheck+3*time.Second {
		t.Fatalf("unbound recheck waits %v", waits)
	}
	waits = nil
	mu.Unlock()
	chunk := func(seq uint64) {
		u := chatText("PRIVATE_SENTINEL chunk")
		r.Observe(ChatSessionEvent{Sequence: seq, Update: &u})
	}
	chunk(1)
	waitCall(t, api.calls, "live-blocked")
	for seq := uint64(2); seq <= 101; seq++ {
		chunk(seq)
	}
	r.mu.Lock()
	queued, dropped := len(r.items), r.dropped
	r.mu.Unlock()
	if queued != chatRelayLiveLimit || dropped != 37 {
		t.Fatalf("queue %d dropped %d", queued, dropped)
	}
	api.mu.Lock()
	api.liveGate = nil
	api.mu.Unlock()
	close(gate)
	for i := 0; i < 1+chatRelayLiveLimit; i++ {
		waitCall(t, api.calls, "live")
	}
	api.mu.Lock()
	live := append([]ChatSessionEvent(nil), api.live...)
	api.mu.Unlock()
	if len(live) != 65 || live[0].Sequence != 1 || live[1].Sequence != 38 || live[1].DroppedEvents != 37 || live[64].Sequence != 101 {
		t.Fatalf("relayed %d frames, second %+v", len(live), live[1])
	}

	// A final retries with backoff and an unchanged client message ID.
	api.mu.Lock()
	api.finalErrs = []error{errors.New("connection reset"), &client.StatusError{Status: 503, Message: "busy"}}
	api.mu.Unlock()
	final := chatFinal("PRIVATE_SENTINEL final")
	r.Observe(ChatSessionEvent{Sequence: 102, Update: &final})
	for i := 0; i < 3; i++ {
		waitCall(t, api.calls, "final")
	}
	api.mu.Lock()
	finals := append([]string(nil), api.finals...)
	api.mu.Unlock()
	mu.Lock()
	backoff := append([]time.Duration(nil), waits...)
	mu.Unlock()
	if len(finals) != 3 || finals[0] != finals[1] || finals[1] != finals[2] || !strings.HasPrefix(finals[0], chatFinalClientID("session", 102)) {
		t.Fatalf("final attempts %v", finals)
	}
	if len(backoff) != 2 || backoff[0] < 200*time.Millisecond || backoff[0] > 300*time.Millisecond || backoff[1] < 400*time.Millisecond || backoff[1] > 600*time.Millisecond {
		t.Fatalf("backoff %v", backoff)
	}

	// 409 means the binding changed: look it up again, then resend once.
	api.mu.Lock()
	api.liveErrs = []error{&client.StatusError{Status: 409, Message: "binding changed"}}
	lookups := api.lookups
	api.mu.Unlock()
	chunk(103)
	waitCall(t, api.calls, "live")
	waitCall(t, api.calls, "lookup")
	waitCall(t, api.calls, "live")
	api.mu.Lock()
	if api.lookups != lookups+1 || api.live[len(api.live)-1].Sequence != 103 {
		t.Fatalf("rebind lookups %d, last %+v", api.lookups-lookups, api.live[len(api.live)-1])
	}
	// A server without the routes ends the relay and reports once.
	api.liveErrs = []error{&client.StatusError{Status: 404, Message: "not found"}}
	api.mu.Unlock()
	chunk(104)
	r.Wait(10 * time.Second)
	select {
	case <-r.done:
	default:
		t.Fatal("relay kept running against a server without routes")
	}
	chunk(105)
	r.mu.Lock()
	remaining := len(r.items)
	r.mu.Unlock()
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(reasons, []string{"routes_unavailable"}) || remaining != 0 {
		t.Fatalf("unsupported reasons %v, queued %d", reasons, remaining)
	}
	if strings.Contains(logs.String(), "PRIVATE_SENTINEL") {
		t.Fatal("relay logged payload")
	}
}

// Risk: delivered/read claimed from fetching or queueing, or from a turn that
// started before the input was written (LEAD (c)).
func TestChatRelayReceiptsFollowExplicitTurnMarkers(t *testing.T) {
	logs := captureLogs(t)
	api := newFakeRelayAPI()
	api.inputs = []ChatInput{chatInput("done", "delivered"), chatInput("m1", "sent")}
	ticks := make(chan time.Time)
	now := time.Unix(1_700_000_000, 0)
	var clockMu sync.Mutex
	delivered := make(chan string, 4)
	var settledMu sync.Mutex
	var settled []string
	r := NewChatRelay(api, HarnessSession{ID: "session", Lease: "lease"}, ChatRelayOptions{
		After: func(time.Duration) <-chan time.Time { return ticks },
		Now: func() time.Time {
			clockMu.Lock()
			defer clockMu.Unlock()
			return now
		},
		Deliver: func(_ context.Context, id, body string) error {
			if !strings.Contains(body, "PRIVATE_SENTINEL") {
				t.Error("input body not delivered")
			}
			delivered <- id
			return nil
		},
		Settled: func(id string) {
			settledMu.Lock()
			defer settledMu.Unlock()
			settled = append(settled, id)
		},
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go r.Run(ctx)
	waitCall(t, api.calls, "lookup")
	state := func(seq uint64, s string) {
		u := chatState(s)
		r.Observe(ChatSessionEvent{Sequence: seq, Update: &u})
	}
	// Busy harness: nothing is written; a turn already running proves nothing.
	state(1, "running")
	select {
	case id := <-delivered:
		t.Fatalf("input %s written to a busy harness", id)
	default:
	}
	state(2, "idle")
	if id := receive(t, delivered); id != "m1" {
		t.Fatalf("delivered %s", id)
	}
	api.mu.Lock() // writing alone is no evidence
	if len(api.receipts) != 0 {
		t.Fatalf("receipt before a turn start: %v", api.receipts)
	}
	api.mu.Unlock()
	state(3, "running")
	waitCall(t, api.calls, "receipt")
	state(4, "requires_action")
	state(5, "idle")
	waitCall(t, api.calls, "receipt")
	api.mu.Lock()
	receipts := append([]string(nil), api.receipts...)
	api.inputs = append(api.inputs, chatInput("m2", "sent"))
	api.mu.Unlock()
	settledMu.Lock()
	settledIDs := append([]string(nil), settled...)
	settledMu.Unlock()
	if !reflect.DeepEqual(receipts, []string{"m1:delivered", "m1:read"}) || !reflect.DeepEqual(settledIDs, []string{"m1"}) {
		t.Fatalf("receipts %v settled %v", receipts, settledIDs)
	}
	// The next poll skips m1 (attempted) and writes m2. If no turn starts in
	// time, it stays "sent": no receipt is invented.
	ticks <- time.Time{}
	if id := receive(t, delivered); id != "m2" {
		t.Fatalf("delivered %s", id)
	}
	state(6, "idle")
	clockMu.Lock()
	now = now.Add(chatRelayEvidence)
	clockMu.Unlock()
	ticks <- time.Time{}
	waitCall(t, api.calls, "inputs")
	state(7, "running")
	state(8, "idle")
	r.Close()
	r.Wait(10 * time.Second)
	api.mu.Lock()
	defer api.mu.Unlock()
	if !reflect.DeepEqual(api.receipts, []string{"m1:delivered", "m1:read"}) {
		t.Fatalf("receipt invented for m2: %v", api.receipts)
	}
	if strings.Contains(logs.String(), "PRIVATE_SENTINEL") {
		t.Fatal("relay logged payload")
	}
}

type relaySupervisorAPI struct {
	*fakeAPI
	relay       ChatRelayAPI
	refuseChat  bool
	registerErr []string
}

func (a *relaySupervisorAPI) RegisterHarness(ctx context.Context, s HarnessSession, agent, run, order, harness, host string, caps []string) (HarnessSession, error) {
	if a.refuseChat && slices.Contains(caps, chatCapability) {
		a.mu.Lock()
		a.registerErr = append(a.registerErr, strings.Join(caps, ","))
		a.mu.Unlock()
		return HarnessSession{}, &client.StatusError{Status: 400, Message: "invalid capability"}
	}
	return a.fakeAPI.RegisterHarness(ctx, s, agent, run, order, harness, host, caps)
}
func (a *relaySupervisorAPI) CurrentChatBinding(ctx context.Context, s HarnessSession) (ChatRelayBinding, error) {
	return a.relay.CurrentChatBinding(ctx, s)
}
func (a *relaySupervisorAPI) PublishChatLive(ctx context.Context, s HarnessSession, b ChatRelayBinding, ev ChatSessionEvent) error {
	return a.relay.PublishChatLive(ctx, s, b, ev)
}
func (a *relaySupervisorAPI) PersistChatFinal(ctx context.Context, s HarnessSession, b ChatRelayBinding, id, body string) error {
	return a.relay.PersistChatFinal(ctx, s, b, id, body)
}
func (a *relaySupervisorAPI) ChatInputs(ctx context.Context, s HarnessSession, b ChatRelayBinding, after string) (ChatInputPage, error) {
	return a.relay.ChatInputs(ctx, s, b, after)
}
func (a *relaySupervisorAPI) ReportChatReceipt(ctx context.Context, s HarnessSession, b ChatRelayBinding, id, state string) error {
	return a.relay.ReportChatReceipt(ctx, s, b, id, state)
}

func relaySupervisor(t *testing.T, api *relaySupervisorAPI, logged *[]string) (*Supervisor, *fakeProcess) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace, state := filepath.Join(root, "work"), filepath.Join(root, "state")
	for _, dir := range []string{workspace, state} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	api.fakeAPI.run = Run{ID: "run", WorkOrderID: "order", AgentPrincipalID: "agent", ModelProfileID: "profile", Status: "queued"}
	api.fakeAPI.profile = Profile{ID: "profile", Harness: Codex, Model: "model", Effort: "high"}
	p := &fakeProcess{stopped: make(chan struct{})}
	s, err := NewSupervisor(context.Background(), Config{API: api, StateRoot: state, DaemonID: "daemon", Workspace: workspace,
		Adapters: []Adapter{&fakeAdapter{proc: p}}, EstimatedUnits: map[string]int64{"requests": 1}, Accounts: []EnrolledAccount{{ID: "account", Key: "local", Harness: Codex}}})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	s.chatGate.log = func(reason string) {
		mu.Lock()
		defer mu.Unlock()
		*logged = append(*logged, reason)
	}
	t.Cleanup(func() {
		_ = p.Stop(context.Background())
		for _, e := range s.runs {
			e.mu.Lock()
			done := e.monitorDone
			e.mu.Unlock()
			if done != nil {
				<-done
			}
		}
		_ = s.Close(context.Background())
	})
	return s, p
}

// Risk: the relay is not wired to owned runs, or person input bypasses the
// journaled inbox control that prevents re-injection after a crash.
func TestSupervisorRelaysOwnedChatThroughJournaledInput(t *testing.T) {
	relay := newFakeRelayAPI()
	relay.inputs = []ChatInput{chatInput("m1", "sent")}
	api := &relaySupervisorAPI{fakeAPI: &fakeAPI{}, relay: relay}
	var logged []string
	s, p := relaySupervisor(t, api, &logged)
	if err := s.PollOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	api.mu.Lock()
	caps := append([]string(nil), api.harnessCaps...)
	api.mu.Unlock()
	entry := s.runs["run"]
	if !slices.Contains(caps, chatCapability) || entry.relay == nil {
		t.Fatalf("owned chat run without relay: %v", caps)
	}
	waitCall(t, relay.calls, "lookup")
	chunk := chatText("PRIVATE_SENTINEL")
	s.observe(entry, AdapterEvent{Chat: &chunk})
	waitCall(t, relay.calls, "live")
	idle := chatState("idle")
	s.observe(entry, AdapterEvent{Chat: &idle})
	eventually(t, "input written to the owned process", func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.calls == 1
	})
	entry.mu.Lock()
	_, journaled := entry.record.Controls["chat:m1"]
	entry.mu.Unlock()
	if !journaled {
		t.Fatal("input not written through the journaled inbox control")
	}
	running := chatState("running")
	s.observe(entry, AdapterEvent{Chat: &running})
	waitCall(t, relay.calls, "receipt")
	final := chatFinal("relay final")
	s.observe(entry, AdapterEvent{Chat: &final})
	waitCall(t, relay.calls, "final")
	relay.mu.Lock()
	live, receipts, finals := relay.live, append([]string(nil), relay.receipts...), append([]string(nil), relay.finals...)
	relay.mu.Unlock()
	if live[0].Binding.SessionID != "session" || live[0].Binding.RunID != "run" || !reflect.DeepEqual(receipts, []string{"m1:delivered"}) || len(finals) != 1 || !strings.HasSuffix(finals[0], "|relay final") {
		t.Fatalf("relay wiring: binding %+v receipts %v finals %v", live[0].Binding, receipts, finals)
	}
	eventually(t, "delivered input left the replay journal", func() bool {
		entry.mu.Lock()
		defer entry.mu.Unlock()
		_, journaled := entry.record.Controls["chat:m1"]
		return !journaled
	})
	if len(logged) != 0 {
		t.Fatalf("relay logged %v", logged)
	}
}

// Acceptance (AEON-1074): a new agentd against a release 128 server registers
// without the chat capability, keeps the run working and logs one content-free
// line; a server that accepts the capability but lacks the routes, or has chat
// switched off, disables only the relay without a second line.
func TestNewAgentdAgainstServerWithoutRelayKeepsRunsAndLogsOnce(t *testing.T) {
	logs := captureLogs(t)
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not found"}`))
	}))
	t.Cleanup(old.Close)
	remote := NewRemote(old.URL, "aeon_fixture_secret")
	api := &relaySupervisorAPI{fakeAPI: &fakeAPI{}, relay: remote, refuseChat: true}
	var logged []string
	s, _ := relaySupervisor(t, api, &logged)
	if err := s.PollOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	api.mu.Lock()
	caps, refused := append([]string(nil), api.harnessCaps...), len(api.registerErr)
	api.mu.Unlock()
	entry := s.runs["run"]
	entry.mu.Lock()
	state, relay := entry.record.State, entry.relay
	entry.mu.Unlock()
	if refused != 1 || slices.Contains(caps, chatCapability) || !slices.Contains(caps, "inbox") || state != "running" || relay != nil {
		t.Fatalf("fallback registration: refused=%d caps=%v state=%s relay=%v", refused, caps, state, relay != nil)
	}
	if !reflect.DeepEqual(logged, []string{"capability_refused"}) {
		t.Fatalf("diagnostics %v", logged)
	}
	// Live chat events of the run still go nowhere and break nothing.
	chunk := chatText("PRIVATE_SENTINEL")
	s.observe(entry, AdapterEvent{Chat: &chunk})
	if err := s.serviceHarness(t.Context(), entry); err != nil {
		t.Fatal(err)
	}
	// A server accepting the capability without routes (or with chat disabled).
	direct := NewChatRelay(remote, HarnessSession{ID: "session", Lease: strings.Repeat("l", 32)}, ChatRelayOptions{Unsupported: s.chatGate.disable})
	direct.Run(t.Context())
	if !reflect.DeepEqual(logged, []string{"capability_refused"}) || s.chatGate.enabled() {
		t.Fatalf("second refusal: diagnostics %v enabled %v", logged, s.chatGate.enabled())
	}
	s.chatGate.now = func() time.Time { return time.Now().Add(chatRelayOff) }
	if !s.chatGate.enabled() {
		t.Fatal("relay never renegotiated after the server upgrade window")
	}
	if strings.Contains(logs.String(), "PRIVATE_SENTINEL") {
		t.Fatal("payload logged")
	}
}
