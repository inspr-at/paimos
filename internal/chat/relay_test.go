// SPDX-License-Identifier: AGPL-3.0-only

package chat

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workerlease"
)

func (f *fixture) grantSend(t *testing.T) {
	t.Helper()
	f.agent.Scopes = append(f.agent.Scopes, "chat.send")
	if _, err := f.d.Admin.Exec(t.Context(), `UPDATE agent_keys SET scopes=$2 WHERE principal_id=$1`, f.agent.ID, f.agent.Scopes); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) current(session, lease string) *httptest.ResponseRecorder {
	return f.call(f.agent, "POST", "/api/chat-deliveries/binding/current", CurrentBindingRequest{session}, lease)
}

// Risk: the daemon learns another session's conversation, or a managed run
// streams with weaker proof than an unmanaged registration (LEAD (a)/(b)).
func TestManagedChatSessionUsesLeaseBoundCurrentBinding(t *testing.T) {
	f := newFixture(t)
	thread := f.thread(t, f.alice, f.role(t, f.alice, f.project, "worker", "relay-current"))
	first, firstLease := f.sessionWith(t, f.agent.ID, f.alice, f.project, "worker", "managed", "chat", "inbox")
	expect(t, f.current(first, firstLease), 404) // not bound yet
	f.bind(t, f.alice, thread, first, "0")
	w := f.current(first, firstLease)
	got := decode[CurrentBinding](t, w)
	if got != (CurrentBinding{Contract: "chat-live-v1", ConversationID: thread.ID, BindingEpoch: "1"}) {
		t.Fatalf("current binding %+v", got)
	}
	for _, hidden := range []string{"private-", "lease", "session", "role", "person", "body"} {
		if strings.Contains(w.Body.String(), hidden) {
			t.Fatalf("current binding leaks %s", hidden)
		}
	}
	publish := func(session, lease, epoch string) *httptest.ResponseRecorder {
		e := agentd.ChatSessionEvent{Binding: agentd.ChatBinding{TenantID: f.agent.TenantID, PrincipalID: f.agent.ID, SessionID: session}, Sequence: 1, Update: &agentd.ChatUpdate{SessionUpdate: "agent_message_chunk", Content: &agentd.ChatContent{Type: "text", Text: "managed-relay-canary"}}}
		return f.call(f.agent, "POST", "/api/chat-deliveries/live", livePublish{WorkerBindingRequest{thread.ID, session, epoch}, e}, lease)
	}
	expect(t, publish(first, firstLease, "1"), 200)
	final := agentd.ChatUpdate{SessionUpdate: "final", Content: &agentd.ChatContent{Type: "text", Text: "final-on-live-route"}}
	expect(t, f.call(f.agent, "POST", "/api/chat-deliveries/live", livePublish{WorkerBindingRequest{thread.ID, first, "1"}, agentd.ChatSessionEvent{Binding: agentd.ChatBinding{TenantID: f.agent.TenantID, PrincipalID: f.agent.ID, SessionID: first}, Sequence: 2, Update: &final}}, firstLease), 400)

	// Wrong or missing lease, a person caller and a session of another agent.
	for _, lease := range []string{"", "wrong-lease-fixture-00000000000000000"} {
		expect(t, f.current(first, lease), 404)
		expect(t, publish(first, lease, "1"), 404)
	}
	expect(t, f.call(f.alice, "POST", "/api/chat-deliveries/binding/current", CurrentBindingRequest{first}, firstLease), 404)
	other := uid()
	if _, err := f.d.Admin.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'agent','other agent')`, f.agent.TenantID, other); err != nil {
		t.Fatal(err)
	}
	foreign, foreignLease := f.sessionWith(t, other, f.alice, f.project, "worker", "managed", "chat")
	expect(t, f.current(foreign, foreignLease), 404)
	// Managed without the chat capability is never a chat registration.
	plain, plainLease := f.sessionWith(t, f.agent.ID, f.alice, f.project, "worker", "managed", "inbox")
	expect(t, f.current(plain, plainLease), 404)
	expect(t, f.call(f.alice, "POST", "/api/chat-threads/"+thread.ID+"/binding", map[string]string{"expected_epoch": "1", "session_id": plain}, ""), 404)
	// An unbound live session never sees the thread bound to another session.
	unbound, unboundLease := f.sessionWith(t, f.agent.ID, f.alice, f.project, "worker", "managed", "chat")
	expect(t, f.current(unbound, unboundLease), 404)

	// Stale heartbeat refuses until the session beats again.
	if _, err := f.d.Admin.Exec(t.Context(), `UPDATE harness_sessions SET created_at=clock_timestamp()-interval '3 minutes',heartbeat_at=NULL WHERE id=$1`, first); err != nil {
		t.Fatal(err)
	}
	expect(t, f.current(first, firstLease), 404)
	expect(t, publish(first, firstLease, "1"), 404)
	if _, err := f.d.Admin.Exec(t.Context(), `UPDATE harness_sessions SET heartbeat_at=clock_timestamp() WHERE id=$1`, first); err != nil {
		t.Fatal(err)
	}
	decode[CurrentBinding](t, f.current(first, firstLease))

	// Handover supersedes the first session's epoch.
	second, secondLease := f.sessionWith(t, f.agent.ID, f.alice, f.project, "worker", "managed", "chat")
	f.bind(t, f.alice, thread, second, "1")
	expect(t, f.current(first, firstLease), 404)
	expect(t, publish(first, firstLease, "1"), 404)
	if got := decode[CurrentBinding](t, f.current(second, secondLease)); got.ConversationID != thread.ID || got.BindingEpoch != "2" {
		t.Fatalf("successor binding %+v", got)
	}
	expect(t, publish(second, secondLease, "1"), 404)
	expect(t, publish(second, secondLease, "2"), 200)
	// Revoked key scope and a stopped session refuse.
	if _, err := f.d.Admin.Exec(t.Context(), `UPDATE agent_keys SET scopes=ARRAY['harness.worker'] WHERE principal_id=$1`, f.agent.ID); err != nil {
		t.Fatal(err)
	}
	expect(t, f.current(second, secondLease), 404)
	if _, err := f.d.Admin.Exec(t.Context(), `UPDATE agent_keys SET scopes=$2 WHERE principal_id=$1`, f.agent.ID, f.agent.Scopes); err != nil {
		t.Fatal(err)
	}
	f.stopNative(t, second)
	expect(t, f.current(second, secondLease), 404)
	expect(t, publish(second, secondLease, "2"), 404)
}

// Risk (AEON-1082): timing disappears on restart/readback, a changed timing
// replays as the same payload, or generic flags on an older daemon falsely
// offer native steering. Strict legacy DTOs must retain their original shape.
func TestChatOutboxTimingIsDurableNegotiatedAndSessionBound(t *testing.T) {
	f := newFixture(t)
	f.grantSend(t)
	thread := f.thread(t, f.alice, f.role(t, f.alice, f.project, "worker", "input-timing"))
	session, lease := f.sessionWith(t, f.agent.ID, f.alice, f.project, "worker", "managed", "chat", "inbox", "steer")
	f.bind(t, f.alice, thread, session, "0")
	path := "/api/chat-threads/" + thread.ID + "/outbox"
	steer := outboxSend{ClientID: "native-input", Body: "correction", Level: "steer", Session: session}
	expect(t, f.call(f.alice, "POST", path, steer, ""), 409)
	if got := decode[CurrentBinding](t, f.current(session, lease)); got.InputTiming != "" {
		t.Fatal("older daemon received an unknown binding field")
	}
	legacy := f.call(f.alice, "POST", path, outboxSend{ClientID: "legacy-input", Body: "after this turn"}, "")
	decode[outboxResult](t, legacy)
	if strings.Contains(legacy.Body.String(), "delivery_level") {
		t.Fatal("legacy sender received an unknown message field")
	}
	if _, err := f.d.Admin.Exec(t.Context(), `UPDATE harness_sessions SET capabilities=array_append(capabilities,'chat_timing_v1') WHERE id=$1`, session); err != nil {
		t.Fatal(err)
	}
	if got := decode[CurrentBinding](t, f.current(session, lease)); got.InputTiming != "native" {
		t.Fatalf("qualified timing %+v", got)
	}
	r := httptest.NewRequest("GET", "/api/chat-sessions/"+session+"/thread", nil).WithContext(tenant.WithPrincipal(t.Context(), f.alice))
	r.Header.Set("Aeon-Chat-Timing", "1")
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	if got := decode[Thread](t, w); got.InputTiming != "native" {
		t.Fatalf("composer timing %+v", got)
	}
	if got := decode[Thread](t, f.call(f.alice, "GET", "/api/chat-sessions/"+session+"/thread", nil, "")); got.InputTiming != "" {
		t.Fatal("older viewer received an unknown timing field")
	}
	sent := decode[outboxResult](t, f.call(f.alice, "POST", path, steer, ""))
	if sent.Message.Level != "steer" || sent.Receipt.State != "sent" {
		t.Fatalf("acceptance overclaimed or lost timing: %+v", sent)
	}
	if replay := decode[outboxResult](t, f.call(f.alice, "POST", path, steer, "")); replay.Message.ID != sent.Message.ID {
		t.Fatal("retry changed message identity")
	}
	changed := steer
	changed.Level = "simple"
	expect(t, f.call(f.alice, "POST", path, changed, ""), 409)
	changed.ClientID, changed.Level = "invalid-timing", "immediate-stop"
	expect(t, f.call(f.alice, "POST", path, changed, ""), 400)
	proof := WorkerBindingRequest{thread.ID, session, "1"}
	oldPage := f.call(f.agent, "POST", "/api/chat-deliveries/outbox", workerPageRequest{WorkerBindingRequest: proof}, lease)
	decode[outboxPage](t, oldPage)
	if strings.Contains(oldPage.Body.String(), "delivery_level") {
		t.Fatal("older strict worker received an unknown message field")
	}
	page := decode[outboxPage](t, f.call(f.agent, "POST", "/api/chat-deliveries/outbox", workerPageRequest{WorkerBindingRequest: proof, IncludeTiming: true}, lease))
	if len(page.Items) != 2 || page.Items[0].Message.Level != "simple" || page.Items[1].Message.Level != "steer" {
		t.Fatalf("worker timing did not survive readback: %+v", page.Items)
	}
	history := decode[historyPage](t, f.call(f.alice, "GET", "/api/chat-threads/"+thread.ID+"/messages?include_timing=true", nil, ""))
	if len(history.Items) != 2 || history.Items[0].Message.Level != "simple" || history.Items[1].Message.Level != "steer" {
		t.Fatalf("history lost timing: %+v", history.Items)
	}
	other, otherLease := f.sessionWith(t, f.agent.ID, f.alice, f.project, "worker", "managed", "chat", "inbox", "steer", "chat_timing_v1")
	f.bind(t, f.alice, thread, other, "1")
	steer.ClientID = "late-input"
	expect(t, f.call(f.alice, "POST", path, steer, ""), 409)
	expect(t, f.call(f.bob, "POST", path, steer, ""), 404)
	next := decode[outboxPage](t, f.call(f.agent, "POST", "/api/chat-deliveries/outbox", workerPageRequest{WorkerBindingRequest: WorkerBindingRequest{thread.ID, other, "2"}, IncludeTiming: true}, otherLease))
	if len(next.Items) != 0 {
		t.Fatal("successor received the previous session's input")
	}
}

// Risk: an older daemon that never relays makes the chat look broken. The
// thread stays a successful response with "unavailable" readiness, the live
// view opens without an error, and final delivery is unaffected.
func TestChatWithoutRelayIsUnavailableNotAnError(t *testing.T) {
	f := newFixture(t)
	f.grantSend(t)
	thread := f.thread(t, f.alice, f.role(t, f.alice, f.project, "worker", "no-relay"))
	session, lease := f.session(t, f.alice, f.project, "worker", "unmanaged")
	f.bind(t, f.alice, thread, session, "0")
	got := decode[Thread](t, f.call(f.alice, "GET", "/api/chat-threads/"+thread.ID, nil, ""))
	if got.Readiness.State != "unavailable" || len(got.Readiness.Capabilities) != 0 {
		t.Fatalf("readiness %+v", got.Readiness)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mux.ServeHTTP(w, r.WithContext(tenant.WithPrincipal(r.Context(), f.alice)))
	}))
	t.Cleanup(server.Close)
	if frame := openLive(t, server, thread.ID, "").next(t); frame.kind != "ready" {
		t.Fatalf("live view without relay: %+v", frame)
	}
	proof := WorkerBindingRequest{thread.ID, session, "1"}
	decode[outboxResult](t, f.call(f.agent, "POST", "/api/chat-deliveries/final", finalSend{proof, outboxSend{ClientID: "older-daemon-final", Body: "final without relay"}}, lease))
	page := decode[outboxPage](t, f.call(f.alice, "GET", "/api/chat-threads/"+thread.ID+"/outbox", nil, ""))
	if len(page.Items) != 1 || page.Items[0].Message.Body != "final without relay" {
		t.Fatal("final delivery depends on the relay")
	}
}

// lossyFinal simulates a lost response for the first final: the server commits,
// the daemon sees an error and retries with the same client message ID.
type lossyFinal struct {
	*agentd.Remote
	mu     sync.Mutex
	lost   bool
	finals []string
}

func (l *lossyFinal) PersistChatFinal(ctx context.Context, s agentd.HarnessSession, b agentd.ChatRelayBinding, clientID, body string) error {
	err := l.Remote.PersistChatFinal(ctx, s, b, clientID, body)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.finals = append(l.finals, clientID)
	if err == nil && !l.lost {
		l.lost = true
		return errors.New("fixture: response lost after commit")
	}
	return err
}

// Acceptance (AEON-1074): the agentd relay carries an S1 fixture stream through
// the real server to an SSE viewer within one second, and the explicit final
// persists exactly once although its response was lost and it was retried.
func TestAgentdRelayReachesViewerAndPersistsFinalOnce(t *testing.T) {
	f := newFixture(t)
	f.grantSend(t)
	thread := f.thread(t, f.alice, f.role(t, f.alice, f.project, "worker", "relay-e2e"))
	session, lease := f.sessionWith(t, f.agent.ID, f.alice, f.project, "worker", "managed", "chat", "inbox")
	f.bind(t, f.alice, thread, session, "0")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := f.alice
		auth := r.Header.Get("Authorization")
		if auth == "Bearer "+f.key || auth == workerlease.Value(f.key, lease) {
			p = f.agent
		}
		f.mux.ServeHTTP(w, r.WithContext(tenant.WithPrincipal(r.Context(), p)))
	}))
	t.Cleanup(server.Close)
	viewer := openLive(t, server, thread.ID, "")
	if viewer.next(t).kind != "ready" {
		t.Fatal("viewer not ready")
	}
	api := &lossyFinal{Remote: agentd.NewRemote(server.URL, f.key)}
	timers := make(chan time.Duration, 16)
	relay := agentd.NewChatRelay(api, agentd.HarnessSession{ID: session, ProjectID: f.project, Lease: lease}, agentd.ChatRelayOptions{
		After: func(d time.Duration) <-chan time.Time {
			select {
			case timers <- d:
			default:
			}
			fire := make(chan time.Time, 1)
			fire <- time.Time{} // Backoff elapses at once; the test proves the retry, not the wait.
			return fire
		},
	})
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go relay.Run(ctx)

	// The S1 fixture stream: the Claude bridge's normalized chat frames.
	raw, err := os.ReadFile(filepath.Join("..", "agentd", "testdata", "chat-claude.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	binding := agentd.ChatBinding{TenantID: f.agent.TenantID, PrincipalID: f.agent.ID, RunID: "run-e2e", Generation: "generation-e2e", SessionID: session}
	var sequence uint64
	observe := func(u agentd.ChatUpdate) {
		sequence++
		relay.Observe(agentd.ChatSessionEvent{Binding: binding, Sequence: sequence, Update: &u})
	}
	lines := bufio.NewScanner(strings.NewReader(string(raw)))
	sent := 0
	for lines.Scan() {
		var frame struct {
			Kind   string             `json:"kind"`
			Update *agentd.ChatUpdate `json:"update"`
		}
		if json.Unmarshal(lines.Bytes(), &frame) != nil || frame.Kind != "chat" || frame.Update == nil {
			continue
		}
		start := time.Now()
		observe(*frame.Update)
		sent++
		got := viewer.next(t)
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Fatalf("frame %d reached the viewer after %s", sent, elapsed)
		}
		var payload struct {
			Type   string            `json:"type"`
			Update agentd.ChatUpdate `json:"update"`
		}
		if got.kind != "chat" || json.Unmarshal([]byte(got.data), &payload) != nil || payload.Type != "update" || payload.Update.SessionUpdate != frame.Update.SessionUpdate {
			t.Fatalf("viewer frame %d: %+v", sent, got)
		}
	}
	if sent != 5 {
		t.Fatalf("fixture frames relayed: %d", sent)
	}
	observe(agentd.ChatUpdate{SessionUpdate: "final", Content: &agentd.ChatContent{Type: "text", Text: "relay-final-canary"}})
	for {
		got := viewer.next(t)
		if got.kind == "chat" && strings.Contains(got.data, `"type":"message"`) {
			if strings.Contains(got.data, "relay-final-canary") {
				t.Fatal("final notification carried the body")
			}
			break
		}
	}
	// The retry is idempotent: the same client ID twice, one stored message.
	deadline := time.Now().Add(10 * time.Second)
	for {
		api.mu.Lock()
		finals := append([]string(nil), api.finals...)
		api.mu.Unlock()
		if len(finals) >= 2 {
			if finals[0] != finals[1] {
				t.Fatalf("retry changed the client message ID: %v", finals)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("final was not retried: %v", finals)
		}
		select {
		case <-timers:
		case <-time.After(50 * time.Millisecond):
		}
	}
	var finals, interim int
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT count(*) FILTER (WHERE body='relay-final-canary'),count(*) FILTER (WHERE body<>'relay-final-canary') FROM inbox_messages WHERE chat_thread_id=$1`, thread.ID).Scan(&finals, &interim); err != nil {
		t.Fatal(err)
	}
	if finals != 1 || interim != 0 {
		t.Fatalf("final stored %d times, interim rows %d", finals, interim)
	}
	relay.Close()
	relay.Wait(10 * time.Second)
}
