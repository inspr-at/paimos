// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/harness"
)

func TestOwnedHarnessRunStopsExactChildAndConfirmsExit(t *testing.T) {
	var calls []hbCall
	srv := heartbeatFixture(t, &calls, "", "")
	defer srv.Close()
	original := srv.Config.Handler
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == harnessPath(transcriptProjectID, transcriptSessionID)+"/heartbeat" {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			calls = append(calls, hbCall{method: r.Method, path: r.URL.Path, body: body})
			_ = json.NewEncoder(w).Encode(map[string]any{"pause": harness.Pause{ControlID: transcriptEntryID, State: "cancelled", Level: "stop_now", StopRequested: true, StopControlID: transcriptEntryID, StopExpiresInMS: 45000}})
			return
		}
		original.ServeHTTP(w, r)
	})
	rt, _, _ := heartbeatRuntime(t, srv)
	o := heartbeatTestOptions(t.TempDir())
	o.OwnerPID = os.Getpid()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	err := rt.runHarnessCommand(ctx, o, []string{"sh", "-c", "exec sleep 30"})
	var exit *exitError
	if !errors.As(err, &exit) || ctx.Err() != nil {
		t.Fatalf("owned stop did not finish child: %v", err)
	}
	registration := hbWhere(calls, http.MethodPost, "/harness-sessions")[0].body
	caps := registration["advertised_capabilities"].([]any)
	if len(caps) != 2 || caps[1] != "owned_stop_v1" {
		t.Fatal("one-shot job advertised a cooperative inbox")
	}
	completion := hbWhere(calls, http.MethodPost, "/complete")
	if len(completion) != 1 || completion[0].body["reason"] != "owned_group_signalled_root_exited" {
		t.Fatal("missing verified stop completion")
	}
	stops := hbWhere(calls, http.MethodPost, "/stop")
	if len(stops) != 1 || stops[0].body["reason"] != "stopped" {
		t.Fatal("stop was misreported as clean job completion")
	}
}

func TestScheduledPauseIsNotPrintedBeforeServerStart(t *testing.T) {
	var calls []hbCall
	srv := heartbeatFixture(t, &calls, "", "")
	defer srv.Close()
	rt, out, stderr := heartbeatRuntime(t, srv)
	start := time.Now().Add(time.Hour)
	rt.printHeartbeatPause(transcriptSessionID, &harness.Pause{ControlID: transcriptEntryID, State: "requested", StartsAt: &start, Level: "pause"}, true)
	if out.Len() != 0 || stderr.Len() != 0 {
		t.Fatal("scheduled request delivered before its server start")
	}
}

func TestHeartbeatWaitUsesScheduledPauseHint(t *testing.T) {
	var calls []hbCall
	srv := heartbeatFixture(t, &calls, "", "")
	defer srv.Close()
	original := srv.Config.Handler
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == harnessPath(transcriptProjectID, transcriptSessionID)+"/heartbeat" {
			_ = json.NewEncoder(w).Encode(map[string]any{"pause": harness.Pause{Level: "stop_now", State: "requested", WakeInMS: 250}})
			return
		}
		original.ServeHTTP(w, r)
	})
	rt, _, _ := heartbeatRuntime(t, srv)
	o := heartbeatTestOptions(t.TempDir())
	o.Interval = 50
	var delay time.Duration
	dep := heartbeatDeps{alive: func(int) bool { return true }, wait: func(_ context.Context, _ int, d time.Duration) error { delay = d; return context.Canceled }}
	if err := rt.runHeartbeat(t.Context(), o, dep); err != nil {
		t.Fatal(err)
	}
	if delay < time.Millisecond || delay > 250*time.Millisecond {
		t.Fatalf("deadline delayed by ordinary heartbeat: %s", delay)
	}
}
