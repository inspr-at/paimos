// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
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

func TestRunHeartbeatHonoursServerStop(t *testing.T) {
	for _, kind := range []string{"requested", "stopped_phase", "stopped_at", "conflict", "forbidden"} {
		t.Run(kind, func(t *testing.T) {
			var calls []hbCall
			status := `{"id":"` + transcriptSessionID + `","phase":"stopped","activity_sequence":99}`
			srv := heartbeatFixture(t, &calls, status, "")
			defer srv.Close()
			original := srv.Config.Handler
			beats := 0
			srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost && r.URL.Path == harnessPath(transcriptProjectID, transcriptSessionID)+"/heartbeat" {
					beats++
					switch kind {
					case "conflict":
						w.WriteHeader(http.StatusConflict)
					case "forbidden":
						w.WriteHeader(http.StatusForbidden)
					default:
						response := map[string]any{"id": transcriptSessionID}
						switch kind {
						case "requested":
							response["pause"] = harness.Pause{State: "cancelled", Level: "stop_now", StopRequested: true, Deliver: true}
						case "stopped_phase":
							response["phase"] = "stopped"
						case "stopped_at":
							response["stopped_at"] = time.Now().UTC()
						}
						_ = json.NewEncoder(w).Encode(response)
					}
					return
				}
				original.ServeHTTP(w, r)
			})
			rt, _, stderr := heartbeatRuntime(t, srv)
			o := heartbeatTestOptions(t.TempDir())
			// Watch this test process: the observer must never signal it.
			o.OwnerPID = os.Getpid()
			waits := 0
			dep := heartbeatDeps{alive: func(int) bool { return true }, wait: func(context.Context, int, time.Duration) error { waits++; return context.Canceled }}
			if err := rt.runHeartbeat(t.Context(), o, dep); err != nil {
				t.Fatal(err)
			}
			if beats != 1 || waits != 0 {
				t.Fatalf("beats=%d waits=%d; stop must exit before another wait", beats, waits)
			}
			disk := loadHeartbeatDisk(t, o.StateDir)
			if !disk.Closed && (!disk.Terminal || disk.TerminalReason != "stopped") {
				t.Fatalf("stop was not persisted: closed=%v terminal=%v reason=%q", disk.Closed, disk.Terminal, disk.TerminalReason)
			}
			if !strings.Contains(stderr.String(), "is stopped") {
				t.Fatalf("missing stopped result: %s", stderr)
			}
			stops := hbWhere(calls, http.MethodPost, "/stop")
			if kind == "requested" {
				if len(stops) != 1 || stops[0].body["reason"] != "stopped" {
					t.Fatal("request did not report stopped")
				}
				if len(hbWhere(calls, http.MethodPost, "/complete")) != 0 {
					t.Fatal("observer claimed verified process exit")
				}
			} else if len(stops) != 0 {
				t.Fatal("already stopped generation was stopped again")
			}
			if err := rt.runHeartbeat(t.Context(), o, dep); err != nil {
				t.Fatal(err)
			}
			if beats != 1 {
				t.Fatal("closed generation resumed heartbeating")
			}
		})
	}
}

func TestRunHeartbeatDoesNotStopForScheduledOrForeignResponse(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		t.Run(fmt.Sprintf("foreign=%t", foreign), func(t *testing.T) {
			var calls []hbCall
			srv := heartbeatFixture(t, &calls, "", "")
			defer srv.Close()
			original := srv.Config.Handler
			srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/heartbeat") {
					id := transcriptSessionID
					if foreign {
						id = transcriptEntryID
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "pause": harness.Pause{StopRequested: true, Deliver: foreign}})
					return
				}
				original.ServeHTTP(w, r)
			})
			rt, _, stderr := heartbeatRuntime(t, srv)
			o := heartbeatTestOptions(t.TempDir())
			waits := 0
			if err := rt.runHeartbeat(t.Context(), o, heartbeatDeps{alive: func(int) bool { return true }, wait: func(context.Context, int, time.Duration) error { waits++; return context.Canceled }}); err != nil {
				t.Fatal(err)
			}
			if waits != 1 {
				t.Fatal("undelivered or foreign request stopped observer")
			}
			if foreign && !strings.Contains(stderr.String(), "another generation") {
				t.Fatal("foreign response was not rejected")
			}
		})
	}
}
