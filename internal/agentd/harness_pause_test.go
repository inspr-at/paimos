// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/inspr-at/paimos/internal/ownedprocess"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRemoteHeartbeatExposesDurablePause(t *testing.T) {
	pause := HarnessPause{ControlID: "12345678-1234-1234-9234-123456789012", State: "requested", DeadlineAt: time.Now().Add(time.Minute)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/heartbeat") {
			t.Error("wrong route")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"pause": pause, "phase": "working"})
	}))
	defer server.Close()
	remote := NewRemote(server.URL, "fixture-key")
	got, err := remote.HeartbeatHarnessPause(t.Context(), HarnessSession{ID: "session", ProjectID: "project", Harness: Codex}, "working")
	if err != nil || got == nil || got.ControlID != pause.ControlID {
		t.Fatalf("pause lost: %#v %v", got, err)
	}
}

type pauseTestAPI struct {
	*fakeAPI
	pause *HarnessPause
}

func (a *pauseTestAPI) HeartbeatHarnessPause(ctx context.Context, s HarnessSession, phase string) (*HarnessPause, error) {
	return a.pause, a.fakeAPI.HeartbeatHarness(ctx, s, phase)
}

func TestPauseHeartbeatUsesExistingInboxAndNeverStopsProcess(t *testing.T) {
	s, a, e, p := managedFixture(t)
	e.mu.Lock()
	e.inboxCapable = true
	e.mu.Unlock()
	s.api = &pauseTestAPI{fakeAPI: a, pause: &HarnessPause{ControlID: "12345678-1234-1234-9234-123456789012", State: "requested", DeadlineAt: time.Now().Add(time.Minute)}}
	for range 2 {
		if err := s.heartbeatHarness(t.Context(), e); err != nil {
			t.Fatal(err)
		}
	}
	p.mu2.Lock()
	defer p.mu2.Unlock()
	if len(p.texts) != 1 || !strings.Contains(p.texts[0], "commit WIP") {
		t.Fatalf("pause did not reach worker once: %v", p.texts)
	}
	select {
	case <-p.stopped:
		t.Fatal("cooperative pause signalled the process")
	default:
	}
}

func TestScheduledPauseWaitsAndEscalationIsDeliveredOnce(t *testing.T) {
	s, a, e, p := managedFixture(t)
	e.mu.Lock()
	e.inboxCapable = true
	e.mu.Unlock()
	start := time.Now().Add(time.Minute)
	pause := &HarnessPause{ControlID: "12345678-1234-1234-9234-123456789012", State: "requested", Level: "pause", StartsAt: &start, DeadlineAt: time.Now().Add(10 * time.Minute), Note: "Keep the failing test"}
	s.api = &pauseTestAPI{fakeAPI: a, pause: pause}
	if err := s.heartbeatHarness(t.Context(), e); err != nil {
		t.Fatal(err)
	}
	p.mu2.Lock()
	if len(p.texts) != 0 {
		t.Fatal("scheduled request delivered early")
	}
	p.mu2.Unlock()
	pause.Deliver = true
	for range 2 {
		if err := s.heartbeatHarness(t.Context(), e); err != nil {
			t.Fatal(err)
		}
	}
	pause.Level = "pause_quickly"
	for range 2 {
		if err := s.heartbeatHarness(t.Context(), e); err != nil {
			t.Fatal(err)
		}
	}
	p.mu2.Lock()
	defer p.mu2.Unlock()
	if len(p.texts) != 2 || !strings.Contains(p.texts[1], "1–2 minutes") || !strings.Contains(p.texts[1], "Keep the failing test") {
		t.Fatal(p.texts)
	}
	select {
	case <-p.stopped:
		t.Fatal("inbox pause stopped process")
	default:
	}
}

type pauseStopProcess struct {
	*managedFake
	forced int
}

func (p *pauseStopProcess) ForceStop(_ context.Context, expected ownedprocess.Identity, deadline time.Time) error {
	if expected.ProcessID != p.identity.ProcessID || time.Until(deadline) <= 0 {
		return ErrNotOwned
	}
	p.forced++
	return p.fakeProcess.Stop(context.Background())
}

func TestStopNowUsesOwnedExecutorAndRejectsWrongGeneration(t *testing.T) {
	for _, wrong := range []bool{false, true} {
		t.Run(fmt.Sprintf("wrong=%t", wrong), func(t *testing.T) {
			s, a, e, p := managedFixture(t)
			proc := &pauseStopProcess{managedFake: p}
			e.mu.Lock()
			e.process = proc
			e.mu.Unlock()
			identity := p.identity
			identity.DaemonID = s.daemonID
			identity.Generation = s.generation
			expires := time.Now().Add(time.Minute)
			generation := e.harness.ID
			if wrong {
				generation = "wrong"
			}
			c := HarnessControl{ID: "deadline-stop", Kind: "stop", ExpectedGeneration: generation, RequestPayload: &struct {
				StopNow bool `json:"stop_now"`
			}{true}, ExpectedOwnership: &identity, ExpiresAt: &expires, deadline: expires}
			a.harnessControls = []HarnessControl{c}
			err := s.serviceHarnessCycle(t.Context(), e, true)
			if wrong {
				if !errors.Is(err, ErrGeneration) || proc.forced != 0 {
					t.Fatalf("wrong generation signalled: %v", err)
				}
			} else if err != nil || proc.forced != 1 {
				t.Fatalf("owned stop failed: %v forced=%d", err, proc.forced)
			}
		})
	}
}
