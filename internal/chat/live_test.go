// SPDX-License-Identifier: AGPL-3.0-only

package chat

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5/pgxpool"
)

type liveTestStream struct {
	response *http.Response
	scanner  *bufio.Scanner
	cancel   context.CancelFunc
}
type testFrame struct{ id, kind, data string }

func openLive(t *testing.T, server *httptest.Server, thread, cursor string) *liveTestStream {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	req, err := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/chat-threads/"+thread+"/live", nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if cursor != "" {
		req.Header.Set("Last-Event-ID", cursor)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" {
		resp.Body.Close()
		cancel()
		t.Fatalf("live status %d", resp.StatusCode)
	}
	s := &liveTestStream{resp, bufio.NewScanner(resp.Body), cancel}
	s.scanner.Buffer(make([]byte, 4096), 256<<10)
	t.Cleanup(func() { cancel(); resp.Body.Close() })
	return s
}
func (s *liveTestStream) next(t *testing.T) testFrame {
	t.Helper()
	f := testFrame{}
	for s.scanner.Scan() {
		line := s.scanner.Text()
		if line == "" && f.kind != "" {
			return f
		}
		if v, ok := strings.CutPrefix(line, "id: "); ok {
			f.id = v
		}
		if v, ok := strings.CutPrefix(line, "event: "); ok {
			f.kind = v
		}
		if v, ok := strings.CutPrefix(line, "data: "); ok {
			f.data = v
		}
	}
	t.Fatalf("stream closed before frame: %v", s.scanner.Err())
	return f
}

// Risks: participant/tenant leaks, persisted interim content, duplicate replies
// on reconnect, and receipt states that claim work the recipient never did.
func TestChatLiveViewersReplayFinalOnlyAndReceipts(t *testing.T) {
	f := newFixture(t)
	f.agent.Scopes = append(f.agent.Scopes, "chat.send")
	if _, err := f.d.Admin.Exec(t.Context(), `UPDATE agent_keys SET scopes=$2 WHERE principal_id=$1`, f.agent.ID, f.agent.Scopes); err != nil {
		t.Fatal(err)
	}
	thread := f.thread(t, f.alice, f.role(t, f.alice, f.project, "worker", "live-test"))
	session, lease := f.session(t, f.alice, f.project, "worker", "unmanaged")
	f.bind(t, f.alice, thread, session, "0")
	proof := WorkerBindingRequest{thread.ID, session, "1"}
	path := "/api/chat-threads/" + thread.ID
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mux.ServeHTTP(w, r.WithContext(tenant.WithPrincipal(r.Context(), f.alice)))
	}))
	t.Cleanup(server.Close)
	a, b := openLive(t, server, thread.ID, ""), openLive(t, server, thread.ID, "")
	if a.next(t).kind != "ready" || b.next(t).kind != "ready" {
		t.Fatal("missing subscriber barrier")
	}
	publish := func(sequence uint64, text string) bool {
		t.Helper()
		e := agentd.ChatSessionEvent{Binding: agentd.ChatBinding{TenantID: f.agent.TenantID, PrincipalID: f.agent.ID, SessionID: session}, Sequence: sequence, Update: &agentd.ChatUpdate{SessionUpdate: "agent_message_chunk", Content: &agentd.ChatContent{Type: "text", Text: text}}}
		out := decode[struct {
			Accepted bool `json:"accepted"`
		}](t, f.call(f.agent, "POST", "/api/chat-deliveries/live", livePublish{proof, e}, lease))
		return out.Accepted
	}
	if !publish(1, "interim-private-canary") {
		t.Fatal("first delta not accepted")
	}
	firstA, firstB := a.next(t), b.next(t)
	if firstA.kind != "chat" || firstA.id != firstB.id || firstA.data != firstB.data || !strings.Contains(firstA.data, "interim-private-canary") {
		t.Fatal("viewers did not receive the same scoped delta")
	}
	a.cancel()
	a.response.Body.Close()
	if !publish(2, "second-live-canary") || publish(2, "second-live-canary") {
		t.Fatal("source sequence was not idempotent")
	}
	secondB := b.next(t)
	a = openLive(t, server, thread.ID, firstA.id)
	if a.next(t).kind != "ready" {
		t.Fatal("reconnect missing ready")
	}
	secondA := a.next(t)
	if secondA.id != secondB.id || secondA.data != secondB.data || strings.Contains(secondA.data, "interim-private-canary") {
		t.Fatal("reconnect duplicated or lost a delta")
	}
	if !publish(3, "third-live-canary") {
		t.Fatal("third delta rejected")
	}
	if a.next(t).data != b.next(t).data {
		t.Fatal("duplicate replay displaced next delta")
	}
	var stored int
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM inbox_messages WHERE chat_thread_id=$1)+(SELECT count(*) FROM events WHERE tenant_id=$2 AND (coalesce(after::text,'') LIKE '%live-canary%' OR coalesce(after::text,'') LIKE '%interim-private-canary%' OR coalesce(before::text,'') LIKE '%interim-private-canary%' OR coalesce(metadata::text,'') LIKE '%interim-private-canary%'))`, thread.ID, f.alice.TenantID).Scan(&stored); err != nil || stored != 0 {
		t.Fatalf("interim persisted: count=%d err=%v", stored, err)
	}
	for _, p := range []tenant.Principal{f.bob, f.admin, f.foreign, f.agent} {
		w := f.call(p, "GET", path+"/live", nil, "")
		expect(t, w, 404)
		if strings.Contains(w.Body.String(), "canary") || strings.Contains(w.Body.String(), thread.ID) {
			t.Fatal("unreadable thread disclosed data")
		}
		expect(t, f.call(p, "GET", path+"/outbox", nil, ""), 404)
		expect(t, f.call(p, "POST", path+"/outbox", outboxSend{ClientID: "forged", Body: "forged input"}, ""), 404)
	}
	wrong := proof
	wrong.BindingEpoch = "2"
	e := agentd.ChatSessionEvent{Binding: agentd.ChatBinding{TenantID: f.agent.TenantID, PrincipalID: f.agent.ID, SessionID: session}, Sequence: 4, Update: &agentd.ChatUpdate{SessionUpdate: "state", State: "running"}}
	expect(t, f.call(f.agent, "POST", "/api/chat-deliveries/live", livePublish{wrong, e}, lease), 404)
	expect(t, f.call(f.agent, "POST", "/api/chat-deliveries/live", livePublish{proof, e}, ""), 404)
	// The outbox stores only explicit final input/replies and replays stable IDs.
	sent := decode[outboxResult](t, f.call(f.alice, "POST", path+"/outbox", outboxSend{ClientID: "send-1", Body: "final input"}, ""))
	if sent.Receipt.State != "sent" || sent.Message.ID == "" {
		t.Fatal("outbox invented delivery")
	}
	again := decode[outboxResult](t, f.call(f.alice, "POST", path+"/outbox", outboxSend{ClientID: "send-1", Body: "final input"}, ""))
	if again.Message.ID != sent.Message.ID {
		t.Fatal("retry duplicated input")
	}
	expect(t, f.call(f.alice, "POST", path+"/outbox", outboxSend{ClientID: "send-1", Body: "changed"}, ""), 409)
	page := decode[outboxPage](t, f.call(f.agent, "POST", "/api/chat-deliveries/outbox", workerPageRequest{WorkerBindingRequest: proof}, lease))
	if len(page.Items) != 1 || page.Items[0].Receipt.State != "sent" {
		t.Fatal("fetch marked delivered")
	}
	rc := workerReceiptRequest{proof, sent.Message.ID, "read"}
	expect(t, f.call(f.agent, "POST", "/api/chat-deliveries/outbox/receipt", rc, lease), 409)
	rc.State = "delivered"
	if decode[outboxResult](t, f.call(f.agent, "POST", "/api/chat-deliveries/outbox/receipt", rc, lease)).Receipt.State != "delivered" {
		t.Fatal("delivery evidence missing")
	}
	rc.State = "read"
	if decode[outboxResult](t, f.call(f.agent, "POST", "/api/chat-deliveries/outbox/receipt", rc, lease)).Receipt.State != "read" {
		t.Fatal("read evidence missing")
	}
	rc.State = "delivered"
	if decode[outboxResult](t, f.call(f.agent, "POST", "/api/chat-deliveries/outbox/receipt", rc, lease)).Receipt.State != "read" {
		t.Fatal("receipt regressed")
	}
	reply := finalSend{proof, outboxSend{ClientID: "reply-1", Body: "final answer", ReplyTo: &sent.Message.ID}}
	// Current stored key scopes, rather than the principal snapshot, decide
	// final writes even when an earlier request already had chat.send.
	if _, err := f.d.Admin.Exec(t.Context(), `UPDATE agent_keys SET scopes=ARRAY['chat.receive','harness.worker','inbox.send'] WHERE principal_id=$1`, f.agent.ID); err != nil {
		t.Fatal(err)
	}
	expect(t, f.call(f.agent, "POST", "/api/chat-deliveries/final", reply, lease), 404)
	if _, err := f.d.Admin.Exec(t.Context(), `UPDATE agent_keys SET scopes=$2 WHERE principal_id=$1`, f.agent.ID, f.agent.Scopes); err != nil {
		t.Fatal(err)
	}
	final := decode[outboxResult](t, f.call(f.agent, "POST", "/api/chat-deliveries/final", reply, lease))
	if decode[outboxResult](t, f.call(f.agent, "POST", "/api/chat-deliveries/final", reply, lease)).Message.ID != final.Message.ID {
		t.Fatal("duplicate final reply")
	}
	history := decode[historyPage](t, f.call(f.alice, "GET", path+"/messages", nil, ""))
	if len(history.Items) != 2 || history.Items[1].Message.Body != "final answer" {
		t.Fatal("final transcript incomplete")
	}
	// The session panel tells the agent's saved reply from the person's input
	// by its author (AEON-1071).
	for i, want := range []bool{false, true} {
		item := history.Items[i].Message
		if item.Sender == "" || (item.Sender == f.agent.ID) != want {
			t.Fatalf("history item %d author %q, agent=%v", i, item.Sender, want)
		}
		if _, err := time.Parse(time.RFC3339Nano, item.Created); err != nil {
			t.Fatalf("history item %d time %q: %v", i, item.Created, err)
		}
	}
	decode[seenMarker](t, f.call(f.alice, "PUT", path+"/read-marker", seenUnion{[]string{final.Message.ID}, "0"}, ""))
	personPage := decode[outboxPage](t, f.call(f.alice, "GET", path+"/outbox?limit=1", nil, ""))
	if personPage.Next == nil || len(personPage.Items) != 1 || personPage.Items[0].Receipt.State != "read" {
		t.Fatal("bounded receipt page missing cursor/evidence")
	}
	personPage = decode[outboxPage](t, f.call(f.alice, "GET", path+"/outbox?after="+*personPage.Next, nil, ""))
	if len(personPage.Items) != 1 || personPage.Items[0].Message.ID != final.Message.ID || personPage.Items[0].Receipt.State != "read" {
		t.Fatal("exact person read evidence absent")
	}
	expect(t, f.call(f.alice, "POST", path+"/outbox", outboxSend{ClientID: "too-large", Body: strings.Repeat("x", 65537)}, ""), 413)
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM inbox_messages WHERE chat_thread_id=$1`, thread.ID).Scan(&stored); err != nil || stored != 2 {
		t.Fatal("invalid/retried message wrote transcript")
	}
	raw := []byte(`{"client_message_id":"invalid-utf8","body":"`)
	raw = append(raw, 0xff)
	raw = append(raw, []byte(`"}`)...)
	request := httptest.NewRequest("POST", path+"/outbox", strings.NewReader(string(raw))).WithContext(tenant.WithPrincipal(t.Context(), f.alice))
	recorder := httptest.NewRecorder()
	f.mux.ServeHTTP(recorder, request)
	expect(t, recorder, 400)
	if !strings.Contains(recorder.Body.String(), "invalid UTF-8") {
		t.Fatal("invalid text rejected for the wrong reason")
	}
	// Old RAM cursors must fail explicitly after byte/count eviction.
	for sequence := uint64(6); sequence < 76; sequence++ {
		publish(sequence, "bounded-replay")
	}
	expired := f.call(f.alice, "GET", path+"/live?after="+firstA.id, nil, "")
	expect(t, expired, 409)
	if !strings.Contains(expired.Body.String(), "reload final history") {
		t.Fatal("expired cursor rejected for the wrong reason")
	}
	a.cancel()
	a.response.Body.Close()
	b.cancel()
	b.response.Body.Close()
	a, b = openLive(t, server, thread.ID, ""), openLive(t, server, thread.ID, "")
	if a.next(t).kind != "ready" || b.next(t).kind != "ready" {
		t.Fatal("revocation subscriber barrier missing")
	}
	// Revocation is established before publication; the active viewers must
	// close before disclosing any subsequent normalized content.
	if _, err := f.d.Admin.Exec(t.Context(), `DELETE FROM role_bindings WHERE principal_id=$1`, f.alice.ID); err != nil {
		t.Fatal(err)
	}
	publish(76, "revoked-live-canary")
	for _, stream := range []*liveTestStream{a, b} {
		for stream.scanner.Scan() {
			if strings.Contains(stream.scanner.Text(), "revoked-live-canary") {
				t.Fatal("revoked viewer received live text")
			}
		}
		if err := stream.scanner.Err(); err != nil {
			t.Fatalf("revocation did not cleanly close stream: %v", err)
		}
	}
}

// Risk: concurrent threads/slow readers retaining unbounded interim content.
// A start barrier establishes concurrency; the clock proves idle expiry.
func TestChatLiveFiftyThreadsRemainBounded(t *testing.T) {
	now := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	b := liveRelay{now: func() time.Time { return now }}
	start := make(chan struct{})
	var wg sync.WaitGroup
	errors := make(chan error, 50)
	cancels := []func(){}
	for i := 0; i < 50; i++ {
		c := liveCursor{Tenant: "tenant", Person: "person", Thread: fmt.Sprintf("thread-%d", i)}
		cursor, _, cancel, err := b.subscribe(c, "")
		if err != nil {
			t.Fatal(err)
		}
		defer cancel()
		cancels = append(cancels, cancel)
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for seq := uint64(1); seq <= 96; seq++ {
				if _, err := b.publish(liveKey(c.Tenant, c.Thread), "session", "1", seq, map[string]string{"text": strings.Repeat("x", 65536)}); err != nil {
					errors <- err
					return
				}
			}
			if _, _, err := b.next(cursor); err == nil {
				errors <- fmt.Errorf("slow reader missed explicit replay gap")
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	if len(b.threads) != 50 || b.viewers != 50 {
		t.Fatalf("load counts: %d/%d", len(b.threads), b.viewers)
	}
	for _, v := range b.threads {
		if v.bytes > liveByteLimit || len(v.frames) > liveFrameLimit || len(v.viewers) != 1 {
			t.Fatal("unbounded replay/queue")
		}
		for _, f := range v.frames {
			if !json.Valid(f.raw) {
				t.Fatal("invalid wire payload")
			}
		}
	}
	// Remove every viewer, then advance only the injected clock.
	for _, cancel := range cancels {
		cancel()
	}
	now = now.Add(liveRetention)
	_, _, cancel, err := b.subscribe(liveCursor{Tenant: "tenant", Person: "person", Thread: "fresh"}, "")
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if len(b.threads) != 1 || b.viewers != 0 {
		t.Fatal("idle buffers/subscriptions retained")
	}
}

// Risk: stalled SSE writes retain tenant locks or exhaust the shared pool.
// Flush barriers hold 50 HTTP viewers in the transport while real DB writes
// and acquisition of every production-sized pool slot must still complete.
func TestChatLiveFiftyHTTPViewersReleaseDatabaseBeforeWrite(t *testing.T) {
	f := newFixture(t)
	threads := make([]Thread, 50)
	for i := range threads {
		threads[i] = f.thread(t, f.alice, f.role(t, f.alice, f.project, "worker", fmt.Sprintf("slow-%d", i)))
	}
	session, _ := f.session(t, f.alice, f.project, "worker", "unmanaged")
	f.bind(t, f.alice, threads[0], session, "0")
	cfg := f.d.App.Config()
	cfg.MaxConns = 16
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	f.m.pool = pool
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	entered, release := make(chan struct{}, 50), make(chan struct{})
	var wg sync.WaitGroup
	defer func() { close(release); cancel(); wg.Wait() }()
	start := make(chan struct{})
	for _, thread := range threads {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			r := httptest.NewRequest("GET", "/api/chat-threads/"+thread.ID+"/live", nil).WithContext(tenant.WithPrincipal(ctx, f.alice))
			w := &blockedLiveWriter{ResponseRecorder: httptest.NewRecorder(), entered: entered, release: release, ctx: ctx}
			f.mux.ServeHTTP(w, r)
		}()
	}
	close(start)
	wait := func() {
		t.Helper()
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("HTTP viewer did not reach the slow-write barrier")
		}
	}
	wait()
	// NOWAIT proves overlap without relying on an elapsed-time threshold.
	tx, err := f.d.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR NO KEY UPDATE NOWAIT`, f.alice.TenantID)
	_ = tx.Rollback(ctx)
	if err != nil {
		t.Fatalf("slow HTTP viewer retained the tenant write fence: %v", err)
	}
	for i := 1; i < len(threads); i++ {
		wait()
	}
	write := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		write <- f.call(f.alice, "POST", "/api/chat-threads/"+threads[0].ID+"/outbox", outboxSend{ClientID: "during-slow-writes", Body: "final input"}, "")
	}()
	select {
	case result := <-write:
		expect(t, result, 200)
	case <-ctx.Done():
		t.Fatal("tenant mutation blocked behind SSE transport")
	}
	connections := make([]*pgxpool.Conn, 0, cfg.MaxConns)
	defer func() {
		for _, conn := range connections {
			conn.Release()
		}
	}()
	for range int(cfg.MaxConns) {
		conn, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatalf("slow viewers retained pool slots: %v", err)
		}
		connections = append(connections, conn)
	}
}

type blockedLiveWriter struct {
	*httptest.ResponseRecorder
	entered chan<- struct{}
	release <-chan struct{}
	ctx     context.Context
	once    sync.Once
}

func (w *blockedLiveWriter) Flush() {
	w.once.Do(func() {
		w.entered <- struct{}{}
		select {
		case <-w.release:
		case <-w.ctx.Done():
		}
	})
	w.ResponseRecorder.Flush()
}

// Risk: a tenant can consume all server RAM/viewer slots, including through
// body-free durable-state hints. Idle eviction must explicitly expire cursors.
func TestChatLiveTenantCapacityAndIdleEviction(t *testing.T) {
	t.Run("notifications do not allocate", func(t *testing.T) {
		now := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
		b := liveRelay{now: func() time.Time { return now }}
		if _, err := b.publish(liveKey("busy", "absent"), "", "", 0, map[string]string{"type": "read_marker"}); err != nil {
			t.Fatal(err)
		}
		if len(b.threads) != 0 {
			t.Fatal("content-free notification allocated a replay buffer")
		}
		key := liveKey("busy", "expired")
		if _, err := b.publish(key, "session", "1", 1, map[string]string{"text": "delta"}); err != nil {
			t.Fatal(err)
		}
		now = now.Add(liveRetention)
		if _, err := b.publish(key, "", "", 0, map[string]string{"type": "read_marker"}); err != nil {
			t.Fatal(err)
		}
		if len(b.threads) != 0 {
			t.Fatal("content-free notification recreated an expired buffer")
		}
	})
	t.Run("tenant buffer cap evicts oldest idle", func(t *testing.T) {
		now := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
		b := liveRelay{now: func() time.Time { return now }}
		old, _, stop, err := b.subscribe(liveCursor{Tenant: "busy", Person: "person", Thread: "old"}, "")
		if err != nil {
			t.Fatal(err)
		}
		stop()
		for i := 0; i < 64; i++ {
			now = now.Add(time.Millisecond)
			if _, err := b.publish(liveKey("busy", fmt.Sprint(i)), "session", "1", 1, map[string]string{"text": "delta"}); err != nil {
				t.Fatal(err)
			}
		}
		if _, _, err := b.next(old); err == nil {
			t.Fatal("oldest idle buffer was retained beyond the tenant cap")
		}
		if len(b.threads) != 64 {
			t.Fatalf("tenant replay buffers = %d, want 64", len(b.threads))
		}
		_, _, stop, err = b.subscribe(liveCursor{Tenant: "other", Person: "person", Thread: "fresh"}, "")
		if err != nil {
			t.Fatal("busy tenant denied another tenant:", err)
		}
		stop()
	})
	t.Run("tenant viewers reserve other tenants capacity", func(t *testing.T) {
		b := liveRelay{}
		for i := 0; i < 64; i++ {
			_, _, stop, err := b.subscribe(liveCursor{Tenant: "busy", Person: "person", Thread: fmt.Sprint(i / 4)}, "")
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
		}
		_, _, stop, err := b.subscribe(liveCursor{Tenant: "busy", Person: "person", Thread: "excess"}, "")
		if err == nil {
			stop()
			t.Fatal("tenant admitted more than 64 viewers")
		}
		w := httptest.NewRecorder()
		liveError(w, err)
		expect(t, w, 429)
		if !strings.Contains(w.Body.String(), "viewer limit") {
			t.Fatal("tenant viewer refused for the wrong reason")
		}
		_, _, stop, err = b.subscribe(liveCursor{Tenant: "other", Person: "person", Thread: "fresh"}, "")
		if err != nil {
			t.Fatal("busy tenant denied another viewer:", err)
		}
		stop()
	})
	t.Run("global cap evicts idle", func(t *testing.T) {
		b := liveRelay{}
		for i := 0; i < liveThreadLimit; i++ {
			if _, err := b.publish(liveKey(fmt.Sprintf("tenant-%d", i/32), fmt.Sprint(i)), "session", "1", 1, map[string]string{"text": "delta"}); err != nil {
				t.Fatal(err)
			}
		}
		_, _, stop, err := b.subscribe(liveCursor{Tenant: "new-tenant", Person: "person", Thread: "fresh"}, "")
		if err != nil {
			t.Fatal("global idle capacity was not reclaimed:", err)
		}
		stop()
		if len(b.threads) != liveThreadLimit {
			t.Fatal("global buffer cap changed during eviction")
		}
	})
}

// Risk: reconnecting phones loop silently on replay from an obsolete binding.
// Mutation completes before reconnect; no interim text from that session may
// be disclosed, and resync advances the cursor past the obsolete frame.
func TestChatLiveObsoleteBindingReplayRequestsResync(t *testing.T) {
	for _, change := range []string{"handover", "stopped", "heartbeat expired"} {
		t.Run(change, func(t *testing.T) {
			f := newFixture(t)
			thread := f.thread(t, f.alice, f.role(t, f.alice, f.project, "worker", "replay-handover"))
			session, lease := f.session(t, f.alice, f.project, "worker", "unmanaged")
			f.bind(t, f.alice, thread, session, "0")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				f.mux.ServeHTTP(w, r.WithContext(tenant.WithPrincipal(r.Context(), f.alice)))
			}))
			t.Cleanup(server.Close)
			stream := openLive(t, server, thread.ID, "")
			ready := stream.next(t)
			if ready.kind != "ready" {
				t.Fatal("missing initial cursor barrier")
			}
			stream.cancel()
			stream.response.Body.Close()
			proof := WorkerBindingRequest{thread.ID, session, "1"}
			event := agentd.ChatSessionEvent{Binding: agentd.ChatBinding{TenantID: f.agent.TenantID, PrincipalID: f.agent.ID, SessionID: session}, Sequence: 1, Update: &agentd.ChatUpdate{SessionUpdate: "agent_message_chunk", Content: &agentd.ChatContent{Type: "text", Text: "obsolete-binding-canary"}}}
			expect(t, f.call(f.agent, "POST", "/api/chat-deliveries/live", livePublish{proof, event}, lease), 200)
			switch change {
			case "handover":
				successor, _ := f.session(t, f.alice, f.project, "worker", "unmanaged")
				f.bind(t, f.alice, thread, successor, "1")
			case "stopped":
				if _, err := f.d.Admin.Exec(t.Context(), `UPDATE harness_sessions SET stopped_at=clock_timestamp(),phase='stopped' WHERE id=$1`, session); err != nil {
					t.Fatal(err)
				}
			case "heartbeat expired":
				if _, err := f.d.Admin.Exec(t.Context(), `UPDATE harness_sessions SET heartbeat_at=clock_timestamp()-interval '3 minutes' WHERE id=$1`, session); err != nil {
					t.Fatal(err)
				}
			}
			stream = openLive(t, server, thread.ID, ready.id)
			if stream.next(t).kind != "ready" {
				t.Fatal("missing reconnect cursor")
			}
			resync := stream.next(t)
			if resync.kind != "resync" || resync.data != `{"reason":"binding_changed"}` || resync.id == ready.id {
				t.Fatalf("obsolete replay must explicitly resync and advance: %+v", resync)
			}
			for stream.scanner.Scan() {
				t.Fatal("stream disclosed data after binding resync:", stream.scanner.Text())
			}
			if err := stream.scanner.Err(); err != nil {
				t.Fatal("binding resync did not close cleanly:", err)
			}
			// A reconnect using the resync cursor cannot replay that frame again.
			reopened := openLive(t, server, thread.ID, resync.id)
			if next := reopened.next(t); next.kind != "ready" || next.id != resync.id {
				t.Fatal("resync cursor was not accepted")
			}
		})
	}
}
