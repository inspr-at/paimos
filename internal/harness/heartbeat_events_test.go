// SPDX-License-Identifier: AGPL-3.0-only
package harness_test

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risk: removing per-tick events freezes /agents, loses real phase changes or
// stops advancing liveness for stall detection. SSE readiness is the listener
// barrier; old heartbeat time is seeded directly, without a timing assertion.
func TestHeartbeatQuietTicksUpdateRowAndSSEWithoutEvents(t *testing.T) {
	f := fixture(t)
	s := registerUnmanaged(t, f, false)
	beat := map[string]any{"phase": "working", "activity": "busy", "activity_sequence": 1}
	expect(t, f.call(f.agent, "POST", s.path+"/heartbeat", beat, s.lease), 200)
	count := func() int {
		t.Helper()
		var n int
		f.tx(t, f.person, func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='harness.heartbeat' AND after->>'id'=$1`, s.id).Scan(&n)
		})
		return n
	}
	if n := count(); n != 1 {
		t.Fatalf("phase change wrote %d events", n)
	}
	age(t, f, s.id, "2 minutes", false)
	events.New(f.db.App).Mount(f.mux)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mux.ServeHTTP(w, r.WithContext(tenant.WithPrincipal(r.Context(), f.person)))
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/events/stream?after=latest", nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("SSE %d", resp.StatusCode)
	}
	scanner := bufio.NewScanner(resp.Body)
	ready := false
	for scanner.Scan() {
		if scanner.Text() == "event: stream.ready" {
			ready = true
		}
		if ready && scanner.Text() == "" {
			break
		}
	}
	if !ready {
		t.Fatalf("no listener barrier: %v", scanner.Err())
	}
	beat["activity_sequence"] = 2
	w := f.call(f.agent, "POST", s.path+"/heartbeat", beat, s.lease)
	expect(t, w, 200)
	if n := count(); n != 1 {
		t.Fatalf("unchanged tick appended event: %d", n)
	}
	var lines []string
	for scanner.Scan() {
		if scanner.Text() == "" {
			if len(lines) > 0 {
				break
			}
			continue
		}
		lines = append(lines, scanner.Text())
	}
	if strings.Join(lines, "\n") != "event: harness.heartbeat\ndata: {}" {
		t.Fatalf("freshness frame changed cursor or leaked data: %q (%v)", lines, scanner.Err())
	}
	resp.Body.Close()
	if got := sessionState(t, f, s); got["heartbeat_at"] != decode(t, w)["heartbeat_at"] || got["activity_sequence"] != float64(2) {
		t.Fatal("latest session facts were lost")
	}
	if n := sweep(t, f); n != 0 {
		t.Fatalf("fresh heartbeat was treated as silent: %d", n)
	}
	beat["phase"] = "yielded"
	beat["activity_sequence"] = 3
	expect(t, f.call(f.agent, "POST", s.path+"/heartbeat", beat, s.lease), 200)
	if n := count(); n != 2 {
		t.Fatalf("real phase change wrote %d total events", n)
	}
	expect(t, f.call(f.agent, "POST", s.path+"/heartbeat", beat, s.lease), 200)
	if n := count(); n != 2 {
		t.Fatalf("state replay appended event: %d", n)
	}
}
