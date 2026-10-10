// SPDX-License-Identifier: AGPL-3.0-only

package events

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type listenerTracer struct{ listening chan *pgx.Conn }

func (tr listenerTracer) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if data.SQL == "LISTEN aeon_events" {
		tr.listening <- conn
	}
	return ctx
}

func (listenerTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// Risk: recycling closes a real dedicated backend but loses committed events
// in the reconnect gap, duplicates old frames, or silently stops live delivery.
// Injected expiry and handler/LISTEN barriers establish the exact ordering;
// the request timeout only guards against hangs.
func TestSSEListenerRecycleClosesBackendAndResumesWithoutLoss(t *testing.T) {
	d, a, b := fixture(t)
	listening := make(chan *pgx.Conn, 2)
	cfg := d.App.Config()
	cfg.ConnConfig.Tracer = listenerTracer{listening: listening}
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	m := New(pool).(*module)
	expiry := make(chan context.CancelFunc, 2)
	m.listenTimeout = func(ctx context.Context, lifetime time.Duration) (context.Context, context.CancelFunc) {
		if lifetime != 30*time.Minute {
			t.Errorf("listener lifetime = %v, want 30m", lifetime)
		}
		ctx, cancel := context.WithCancel(ctx)
		t.Cleanup(cancel)
		expiry <- cancel
		return ctx, cancel
	}
	mux := http.NewServeMux()
	m.Mount(mux)
	finished := make(chan struct{}, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { finished <- struct{}{} }()
		mux.ServeHTTP(w, r.WithContext(tenant.WithPrincipal(r.Context(), a)))
	}))
	t.Cleanup(srv.Close)

	resp, sc, _ := liveStream(t, srv, "latest", "", false)
	oldConn, recycle := <-listening, <-expiry
	before := appendEvents(t, d, a, 1)[0]
	if got := nextEvent(t, sc); got.ID != before.ID {
		t.Fatalf("before recycle: got %d, want %d", got.ID, before.ID)
	}
	recycle() // Force the listener's expiry without cancelling the HTTP client.
	if sc.Scan() || sc.Err() != nil {
		t.Fatalf("expired stream did not close cleanly: %q, %v", sc.Text(), sc.Err())
	}
	<-finished
	if !oldConn.IsClosed() {
		t.Fatal("expired stream retained its dedicated backend")
	}
	resp.Body.Close()
	gap := appendEvents(t, d, a, 2)
	appendEvents(t, d, b, 1) // Another tenant's event cannot enter resumed replay.
	resp, sc, ready := liveStream(t, srv, "latest", strconv.FormatInt(before.ID, 10), true)
	newConn := <-listening
	if ready != before.ID || newConn.PgConn().PID() == oldConn.PgConn().PID() {
		t.Fatal("reconnect did not preserve the cursor with a fresh backend")
	}
	for _, want := range gap {
		if got := nextEvent(t, sc); got.ID != want.ID || got.ActorPrincipalID != a.ID {
			t.Fatalf("recycle gap: got %+v, want event %d", got, want.ID)
		}
	}
	after := appendEvents(t, d, a, 1)[0]
	if got := nextEvent(t, sc); got.ID != after.ID || got.ActorPrincipalID != a.ID {
		t.Fatalf("after recycle: got %+v, want event %d", got, after.ID)
	}
	(<-expiry)()
	if sc.Scan() || sc.Err() != nil {
		t.Fatal("reconnected stream did not close cleanly")
	}
	<-finished
	if !newConn.IsClosed() {
		t.Fatal("reconnected stream retained its backend at expiry")
	}
	resp.Body.Close()
}

// liveStream opens /api/events/stream?after=... (live mode), checks that
// stream.ready says resumed as expected and returns the ID it names.
func liveStream(t *testing.T, srv *httptest.Server, after, lastEventID string, resumed bool) (*http.Response, *bufio.Scanner, int64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/events/stream?after="+after, nil)
	if err != nil {
		t.Fatal(err)
	}
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	if resp.StatusCode != 200 {
		t.Fatalf("live stream %d", resp.StatusCode)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 4096), 1<<20)
	lines := make([]string, 0, 5)
	for len(lines) < 5 && sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if len(lines) != 5 || lines[0] != ": connected" || lines[1] != "" || lines[3] != "event: stream.ready" {
		t.Fatalf("live stream opening %q (%v)", lines, sc.Err())
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(lines[2], "id: "), 10, 64)
	if err != nil || lines[4] != `data: {"after":`+strconv.FormatInt(id, 10)+`,"resumed":`+strconv.FormatBool(resumed)+`}` {
		t.Fatalf("stream.ready framing %q", lines)
	}
	if !sc.Scan() || sc.Text() != "" {
		t.Fatalf("stream.ready not terminated: %q", sc.Text())
	}
	return resp, sc, id
}

func TestSSELiveModeStartsAtLatestAndResumes(t *testing.T) {
	d, a, b := fixture(t)
	base := logPosition(t, d, a)
	srv := testServer(t, d, a, b)

	// A newly bootstrapped tenant starts at its audited seed position.
	resp, _, ready := liveStream(t, srv, "latest", "", false)
	if ready != base {
		t.Fatalf("bootstrap tenant ready at %d", ready)
	}
	resp.Body.Close()

	appendEvents(t, d, a, 3)
	appendEvents(t, d, b, 2) // another tenant's counter never shows here
	resp, sc, ready := liveStream(t, srv, "latest", "", false)
	if ready != base+3 {
		t.Fatalf("latest ready at %d, want %d", ready, base+3)
	}
	appendEvents(t, d, a, 1)
	if e := nextEvent(t, sc); e.ID != base+4 {
		t.Fatalf("live event %d, want %d (no replay)", e.ID, base+4)
	}
	resp.Body.Close()

	// A reconnect sends Last-Event-ID, which wins over the query.
	appendEvents(t, d, a, 1)
	resp, sc, ready = liveStream(t, srv, "latest", strconv.FormatInt(base+4, 10), true)
	if ready != base+4 {
		t.Fatalf("resume ready at %d, want %d", ready, base+4)
	}
	if e := nextEvent(t, sc); e.ID != base+5 {
		t.Fatalf("resumed event %d, want %d", e.ID, base+5)
	}
	resp.Body.Close()

	// A numeric after replays from there.
	resp, sc, ready = liveStream(t, srv, strconv.FormatInt(base+2, 10), "", true)
	if ready != base+2 {
		t.Fatalf("numeric resume ready at %d, want %d", ready, base+2)
	}
	if e := nextEvent(t, sc); e.ID != base+3 {
		t.Fatalf("numeric resume replayed %d first, want %d", e.ID, base+3)
	}
	resp.Body.Close()

	// A resume point from the future (another database) restarts at the
	// newest event; stream.ready names it, so the client knows to refetch.
	resp, _, ready = liveStream(t, srv, "latest", "999", false)
	if ready != base+5 {
		t.Fatalf("future resume ready at %d, want %d", ready, base+5)
	}
	resp.Body.Close()

	for _, bad := range []string{"-1", "abc", "", "1.5", "9223372036854775808"} {
		req, _ := http.NewRequestWithContext(t.Context(), "GET", srv.URL+"/api/events/stream?after="+bad, nil)
		r, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode != 400 {
			t.Fatalf("after=%q: %d", bad, r.StatusCode)
		}
	}
}

func TestSSELiveModeSkipsALongBacklog(t *testing.T) {
	d, a, b := fixture(t)
	base := logPosition(t, d, a)
	appendEvents(t, d, a, maxLiveReplay+2)
	srv := testServer(t, d, a, b)
	resp, sc, ready := liveStream(t, srv, "latest", "1", false)
	defer resp.Body.Close()
	if ready != base+maxLiveReplay+2 {
		t.Fatalf("long backlog ready at %d, want %d", ready, base+maxLiveReplay+2)
	}
	appendEvents(t, d, a, 1)
	if e := nextEvent(t, sc); e.ID != base+maxLiveReplay+3 {
		t.Fatalf("after the skip got %d", e.ID)
	}
	// Within the bound the backlog is replayed.
	resp2, sc2, ready2 := liveStream(t, srv, "latest", strconv.FormatInt(base+3, 10), true)
	defer resp2.Body.Close()
	if ready2 != base+3 {
		t.Fatalf("short backlog ready at %d", ready2)
	}
	if e := nextEvent(t, sc2); e.ID != base+4 {
		t.Fatalf("short backlog replayed %d first", e.ID)
	}
}

func TestSSELivePingPreservesResumeAndTenantCounter(t *testing.T) {
	d, a, b := fixture(t)
	appendEvents(t, d, a, 1)
	srv := testServer(t, d, a, b)
	resp, sc, ready := liveStream(t, srv, "latest", "", false)
	defer resp.Body.Close()
	for _, want := range []string{": keepalive", "", "event: stream.ping", "data: {}", ""} {
		if !sc.Scan() || sc.Text() != want {
			t.Fatalf("ping framing: got %q, want %q (%v)", sc.Text(), want, sc.Err())
		}
	}
	// No ping id or log row: reconnect at the original tenant position, then
	// read the next durable event with its usual sequential id.
	resumed, _, after := liveStream(t, srv, "latest", strconv.FormatInt(ready, 10), true)
	resumed.Body.Close()
	if after != ready {
		t.Fatalf("ping advanced the tenant counter: %d -> %d", ready, after)
	}
	appendEvents(t, d, a, 1)
	if e := nextEvent(t, sc); e.ID != ready+1 {
		t.Fatalf("event after ping: %d, want %d", e.ID, ready+1)
	}
}
