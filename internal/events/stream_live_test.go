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
)

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
