// SPDX-License-Identifier: AGPL-3.0-only
package harness_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/harness"
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
	// What /agents and the live chips read: the session list, the live feed
	// the chips poll (include_inactive) and the two-minute live window. All
	// come from the session row, so a quiet tick must reach them without an event.
	beatIn := func(endpoint, key string) *time.Time {
		t.Helper()
		w := f.call(f.person, "GET", endpoint, nil, "")
		expect(t, w, 200)
		for _, item := range decode(t, w)["items"].([]any) {
			if row := item.(map[string]any); row[key] == s.id {
				at, err := time.Parse(time.RFC3339Nano, row["heartbeat_at"].(string))
				if err != nil {
					t.Fatal(err)
				}
				return &at
			}
		}
		return nil
	}
	if at := beatIn("/api/harness-sessions/live", "session_id"); at != nil {
		t.Fatalf("aged session still in the live window: %v", at)
	}
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
	fresh, err := time.Parse(time.RFC3339Nano, decode(t, w)["heartbeat_at"].(string))
	if err != nil {
		t.Fatal(err)
	}
	for _, read := range [][2]string{{"/api/harness-sessions", "id"}, {"/api/harness-sessions/live?include_inactive=true", "session_id"}, {"/api/harness-sessions/live", "session_id"}} {
		if at := beatIn(read[0], read[1]); at == nil || !at.Equal(fresh) {
			t.Fatalf("%s heartbeat %v, want %v", read[0], at, fresh)
		}
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

// Risk: an otherwise quiet attached-hook heartbeat enables recovery without
// an audit event, or observing the same owned process appends another event.
func TestHeartbeatAttachedOwnershipAuditedOnce(t *testing.T) {
	v := recoveryAgent(t)
	f := v.f
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET management='unmanaged',run_id=NULL,capabilities=ARRAY['inbox','status'],process_ownership=NULL,process_observed_at=NULL,heartbeat_at=clock_timestamp() WHERE id=$1`, v.session)
		return err
	})
	count := func() int {
		t.Helper()
		var n int
		f.tx(t, f.person, func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='harness.heartbeat' AND after->>'id'=$1`, v.session).Scan(&n)
		})
		return n
	}
	baseline := count()
	beat := map[string]any{"phase": "working", "activity_sequence": 2, "attached_hook": true, "process_ownership": v.identity}
	denied := v.runtimeCall(t, v.path+"/heartbeat", beat, v.lease)
	expect(t, denied, 403)
	if !strings.Contains(denied.Body.String(), "run_capability_upgrade_required") {
		t.Fatal("unbound hook refused for wrong reason")
	}
	expect(t, f.call(f.agent, "POST", v.path+"/heartbeat", beat, v.lease), 200)
	if got := count() - baseline; got != 1 {
		t.Fatalf("first attached ownership report wrote %d heartbeat events, want 1", got)
	}
	var beforeRaw, afterRaw []byte
	f.tx(t, f.person, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT before,after FROM events WHERE type='harness.heartbeat' AND after->>'id'=$1 ORDER BY id DESC LIMIT 1`, v.session).Scan(&beforeRaw, &afterRaw)
	})
	var before, after harness.Session
	if err := json.Unmarshal(beforeRaw, &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(afterRaw, &after); err != nil {
		t.Fatal(err)
	}
	if before.ProcessOwnership != nil || slices.Contains(before.Capabilities, "attached_reconnect_v1") {
		t.Fatal("audit event lost the state before ownership was established")
	}
	if after.ProcessOwnership == nil || *after.ProcessOwnership != v.identity || !slices.Contains(after.Capabilities, "attached_reconnect_v1") {
		t.Fatal("audit event lost the verified process identity or recovery capability")
	}
	if before.Phase != after.Phase || before.Activity != after.Activity {
		t.Fatal("fixture changed phase or activity while establishing ownership")
	}
	diagnosis := f.call(f.person, "GET", v.path+"/recover-agent", nil, "")
	expect(t, diagnosis, 200)
	if decode(t, diagnosis)["action"] != "reconnect" {
		t.Fatal("verified attached ownership did not enable recovery")
	}

	// Age only the observation using the database clock, then prove the same
	// report refreshes that row without allocating another audit event.
	var observed time.Time
	f.tx(t, f.person, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `UPDATE harness_sessions SET process_observed_at=clock_timestamp()-interval '1 minute' WHERE id=$1 RETURNING process_observed_at`, v.session).Scan(&observed)
	})
	for sequence := 3; sequence <= 4; sequence++ {
		beat["activity_sequence"] = sequence
		expect(t, f.call(f.agent, "POST", v.path+"/heartbeat", beat, v.lease), 200)
		if got := count() - baseline; got != 1 {
			t.Fatalf("repeated attached ownership report wrote %d total heartbeat events, want 1", got)
		}
	}
	state := f.call(f.person, "GET", v.path, nil, "")
	expect(t, state, 200)
	var current harness.Session
	if err := json.Unmarshal(state.Body.Bytes(), &current); err != nil {
		t.Fatal(err)
	}
	if current.ProcessObservedAt == nil || !current.ProcessObservedAt.After(observed) || current.ActivitySequence != 4 {
		t.Fatal("quiet ownership reports did not persist their latest observation and sequence")
	}
}
