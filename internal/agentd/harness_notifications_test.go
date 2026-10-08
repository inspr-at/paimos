// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type hintAPI struct {
	*fakeAPI
	connected  chan func()
	deliveries chan struct{}
	controls   chan string
	queued     []HarnessDelivery
}

func (a *hintAPI) WatchHarness(ctx context.Context, _ HarnessSession, wake func()) error {
	select {
	case a.connected <- wake:
	case <-ctx.Done():
		return ctx.Err()
	}
	<-ctx.Done()
	return ctx.Err()
}
func (a *hintAPI) CompleteHarnessDelivery(ctx context.Context, session HarnessSession, delivery HarnessDelivery) error {
	err := a.fakeAPI.CompleteHarnessDelivery(ctx, session, delivery)
	a.mu.Lock()
	a.queued = a.queued[1:]
	a.mu.Unlock()
	a.deliveries <- struct{}{}
	return err
}
func (a *hintAPI) DrainHarness(context.Context, HarnessSession) ([]HarnessDelivery, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.queued) == 0 {
		return nil, nil
	}
	return append([]HarnessDelivery(nil), a.queued[:1]...), nil
}
func (a *hintAPI) CompleteHarnessControl(ctx context.Context, session HarnessSession, id, outcome, reason string) error {
	err := a.fakeAPI.CompleteHarnessControl(ctx, session, id, outcome, reason)
	a.controls <- id + ":" + outcome
	return err
}

func TestHarnessHintsDrainAndStopWithoutHeartbeatOrPoll(t *testing.T) {
	// Risk: a pushed hint is ignored until a timer, or becomes an unauthorized
	// process command. No timer can fire in this loop; only queue reads act.
	s, api, process := testSupervisor(t)
	hints := &hintAPI{fakeAPI: api, connected: make(chan func(), 1), deliveries: make(chan struct{}, 2), controls: make(chan string, 2)}
	s.api = hints
	entry := &owned{record: Record{TenantID: s.tenantID, PrincipalID: s.principalID, RunID: "run", Generation: s.generation, State: "running", PID: process.PID(), Controls: map[string]replay{}}, harness: HarnessSession{ID: "session", ProjectID: "project"}, process: process, inboxCapable: true, replies: map[string]string{}, monitorDone: make(chan struct{}), harnessWake: make(chan struct{}, 1)}
	s.runs["run"] = entry
	ended := make(chan struct{})
	go func() { defer close(ended); s.heartbeatLoop(entry, nil, nil) }()
	go s.watchHarness(entry)
	t.Cleanup(func() { close(entry.monitorDone); <-ended })
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var wake func()
	select {
	case wake = <-hints.connected:
	case <-ctx.Done():
		t.Fatal("notification watcher never connected")
	}
	api.mu.Lock()
	hints.queued = []HarnessDelivery{{ID: "delivery", MessageID: "message", SenderPrincipalID: "person", Body: "check the current work"}, {ID: "next-delivery", MessageID: "next-message", SenderPrincipalID: "person", Body: "also check the final handover"}}
	api.mu.Unlock()
	wake()
	for range 2 {
		select {
		case <-hints.deliveries:
		case <-ctx.Done():
			t.Fatal("one hint did not drain the full queued burst")
		}
	}
	process.mu.Lock()
	calls := process.calls
	process.mu.Unlock()
	if calls != 2 {
		t.Fatalf("message inserted %d times", calls)
	}
	api.mu.Lock()
	beats := api.harnessBeats
	api.harnessControls = []HarnessControl{{ID: "stop", Kind: "stop"}}
	api.mu.Unlock()
	if beats != 0 {
		t.Fatal("hint emitted a lease heartbeat")
	}
	wake()
	select {
	case result := <-hints.controls:
		if result != "stop:applied" {
			t.Fatal(result)
		}
	case <-ctx.Done():
		t.Fatal("hint did not apply queued Stop")
	}
	select {
	case <-process.stopped:
	default:
		t.Fatal("Stop completion preceded process stop")
	}
}

func TestRemoteHarnessNotificationsBoundFramesAndCredentials(t *testing.T) {
	for _, oversized := range []bool{false, true} {
		t.Run(fmt.Sprint(oversized), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/projects/project/harness-sessions/session/notifications" || r.Header.Get("Authorization") != "Bearer fixture-token" || r.Header.Get("X-Aeon-Worker-Lease") != "fixture-lease" {
					t.Error("notification request lost its exact binding")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if oversized {
					_, _ = fmt.Fprint(w, "event: harness.wake\ndata: "+strings.Repeat("x", 17<<10)+"\n\n")
					return
				}
				_, _ = fmt.Fprint(w, "event: stream.ping\ndata: {}\n\nevent: harness.wake\ndata: {}\n\nevent: other\ndata: {}\n\n")
			}))
			defer server.Close()
			remote := NewRemote(server.URL, "fixture-token")
			wakes := 0
			err := remote.WatchHarness(t.Context(), HarnessSession{ID: "session", ProjectID: "project", Lease: "fixture-lease"}, func() { wakes++ })
			if oversized {
				if err == nil || err == io.EOF || wakes != 0 {
					t.Fatal("oversized frame produced a wake")
				}
				return
			}
			if err != io.EOF || wakes != 1 {
				t.Fatalf("wake parsing: count=%d error=%v", wakes, err)
			}
		})
	}
}
