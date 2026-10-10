// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/hostcapacity"
)

type cadenceAPI struct {
	*fakeAPI
	queueCalls, probeCalls, recoveryCalls, hostCalls int
	observations                                     []AccountProbeObservation
	runs                                             []Run
	probeErr, claimErr                               error
}

func (a *cadenceAPI) Queued(context.Context) ([]Run, error) { a.queueCalls++; return a.runs, nil }
func (a *cadenceAPI) ProbeBatch(_ context.Context, _, _ string, probes []AccountProbeObservation) []error {
	a.probeCalls++
	a.observations = append(a.observations, probes...)
	out := make([]error, len(probes))
	for i := range out {
		out[i] = a.probeErr
	}
	return out
}
func (a *cadenceAPI) ClaimAgentRecoveries(context.Context, string, string) ([]RecoveryRequest, error) {
	a.recoveryCalls++
	return nil, nil
}
func (*cadenceAPI) CompleteAgentRecovery(context.Context, string, string, RecoveryReport) error {
	return nil
}
func (a *cadenceAPI) HostCapacity(context.Context) (hostcapacity.View, error) {
	a.hostCalls++
	return hostcapacity.View{}, nil
}
func (a *cadenceAPI) Claim(ctx context.Context, id, daemon, generation string, ids []string) error {
	if a.claimErr != nil {
		return a.claimErr
	}
	return a.fakeAPI.Claim(ctx, id, daemon, generation, ids)
}

// Risk: queue backoff must not age health or delay recovery, and a hint must
// trigger a fresh queue read without retaining any positive dispatch advice.
func TestSupervisorIdleQueueCadenceAndHints(t *testing.T) {
	s, base, _ := testSupervisor(t)
	a := &cadenceAPI{fakeAPI: base}
	s.api = a
	s.accounts = []EnrolledAccount{{ID: "account", Key: "local", Harness: Codex}, {ID: "second", Key: "second", Harness: Codex}, {ID: "third", Key: "third", Harness: Codex}}
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	s.capacityNow = func() time.Time { return now }
	for tick := range 12 {
		now = now.Add(5 * time.Second)
		if err := s.PollScheduledOnce(t.Context()); err != nil {
			t.Fatal(err)
		}
		wantQueue := 1
		if tick >= 6 {
			wantQueue = 2
		}
		if a.queueCalls != wantQueue {
			t.Fatalf("tick %d queue calls=%d want=%d", tick, a.queueCalls, wantQueue)
		}
	}
	if a.probeCalls != 12 || len(a.observations) != 36 || a.recoveryCalls != 12 || a.hostCalls != 12 {
		t.Fatalf("probe batches=%d observations=%d recovery=%d host=%d", a.probeCalls, len(a.observations), a.recoveryCalls, a.hostCalls)
	}
	for i, o := range a.observations {
		if !o.Status.OK || o.ObservedAt != time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC).Add(time.Duration(i/3+1)*5*time.Second) {
			t.Fatalf("observation %d lost health/time: %+v", i, o)
		}
	}
	t.Log("per minute: queue 12 -> 2; three-account transport 36 -> 12; individual observations 36; recovery 12; host capacity 12; paired lifecycle remains 12 plus hints")
	for range 100 {
		s.wakeQueue()
	}
	select {
	case <-s.QueueWake():
	default:
		t.Fatal("hint did not wake daemon wait")
	}
	if err := s.PollScheduledOnce(t.Context()); err != nil || a.queueCalls != 3 {
		t.Fatalf("hint did not scan immediately: calls=%d err=%v", a.queueCalls, err)
	}
	if err := s.PollScheduledOnce(t.Context()); err != nil || a.queueCalls != 3 {
		t.Fatalf("coalesced hints caused extra scan: calls=%d err=%v", a.queueCalls, err)
	}
	now = now.Add(emptyQueueInterval)
	if err := s.PollScheduledOnce(t.Context()); err != nil || a.queueCalls != 4 {
		t.Fatalf("missed hint safety scan: calls=%d err=%v", a.queueCalls, err)
	}
}

func TestSupervisorQueueHintsCannotBypassDispatchFences(t *testing.T) {
	for _, fence := range []string{"reconciliation", "tombstone", "probe", "claim"} {
		t.Run(fence, func(t *testing.T) {
			s, base, _ := testSupervisor(t)
			a := &cadenceAPI{fakeAPI: base, runs: []Run{base.run}}
			s.api = a
			s.queueNext = time.Now().Add(time.Hour)
			s.wakeQueue()
			switch fence {
			case "reconciliation":
				s.SetPairingFailure(agentsetup.PairingServerUnavailable)
			case "tombstone":
				if err := PersistFence(s.state.Path(), s.daemonID, ""); err != nil {
					t.Fatal(err)
				}
			case "probe":
				a.probeErr = errors.New("stale probe rejected")
			case "claim":
				a.claimErr = errors.New("current permission revoked")
			}
			err := s.PollScheduledOnce(t.Context())
			if fence == "probe" && (err == nil || !s.blockedAccounts["account"] || s.probedAccounts["account"]) {
				t.Fatalf("rejected probe became ready: %v", err)
			}
			if fence == "claim" && !errors.Is(err, a.claimErr) {
				t.Fatalf("final claim rejection lost: %v", err)
			}
			if base.claims != 0 || len(s.runs) != 0 && s.runs["run"].process != nil {
				t.Fatalf("%s hint launched work", fence)
			}
		})
	}
}

func TestRemoteProbeBatchPreservesIndividualResults(t *testing.T) {
	var calls int
	now := time.Now()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/agent-accounts/probes" {
			t.Errorf("unexpected route %s", r.URL.Path)
		}
		var in struct {
			Items []struct {
				AccountID  string    `json:"account_id"`
				ObservedAt time.Time `json:"observed_at"`
				Probe      struct {
					Available bool   `json:"available"`
					Failure   string `json:"failure"`
				} `json:"probe"`
			} `json:"items"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Error(err)
			return
		}
		if len(in.Items) != 3 || !in.Items[0].ObservedAt.Equal(now) || !in.Items[0].Probe.Available || in.Items[1].Probe.Failure != ProbeAuthFailed || in.Items[2].Probe.Available {
			t.Errorf("lost individual observation: %+v", in)
		}
		fmt.Fprint(w, `{"items":[{"account_id":"a","status":200},{"account_id":"b","status":403},{"account_id":"c","status":409}]}`)
	}))
	defer server.Close()
	results := NewRemote(server.URL, "fixture").ProbeBatch(t.Context(), "daemon", "generation", []AccountProbeObservation{{"a", probeOK, now}, {"b", probeAuthFailed, now}, {"c", probeUnavailable, now}})
	if calls != 1 || len(results) != 3 || results[0] != nil || results[1] == nil || results[2] == nil {
		t.Fatalf("calls=%d results=%v", calls, results)
	}
}

// Barrier proves that a long-lived hint stream stays open until cancellation;
// pings and unrelated frames do not request additional queue reads.
func TestRemoteQueueHintsStayOpenAndBoundFrames(t *testing.T) {
	for _, oversized := range []bool{false, true} {
		t.Run(fmt.Sprint(oversized), func(t *testing.T) {
			seen := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/runs/queued/notifications" {
					t.Errorf("unexpected route %s", r.URL.Path)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "event: stream.ping\ndata: {}\n\nevent: queue.wake\ndata: {}\n\n")
				if oversized {
					for range 2000 {
						fmt.Fprint(w, "data: oversized\n")
					}
					fmt.Fprint(w, "\n")
				}
				w.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
				case <-release:
				}
			}))
			defer server.Close()
			defer close(release)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				done <- NewRemote(server.URL, "fixture").WatchQueue(ctx, func() { once.Do(func() { close(seen) }) })
			}()
			select {
			case <-seen:
			case <-time.After(5 * time.Second):
				t.Fatal("no queue hint")
			}
			if !oversized {
				select {
				case err := <-done:
					t.Fatalf("stream ended before cancellation: %v", err)
				default:
				}
				cancel()
			}
			select {
			case err := <-done:
				if oversized && (err == nil || err.Error() != "queue notification frame too large") {
					t.Fatalf("wrong bound failure: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("watch did not stop")
			}
		})
	}
}
