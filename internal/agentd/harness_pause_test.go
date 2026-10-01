// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/harness"
)

func TestRemoteHeartbeatExposesDurablePause(t *testing.T) {
	pause := harness.Pause{ControlID: "12345678-1234-1234-9234-123456789012", State: "requested", DeadlineAt: time.Now().Add(time.Minute)}
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
	pause *harness.Pause
}

func (a *pauseTestAPI) HeartbeatHarnessPause(ctx context.Context, s HarnessSession, phase string) (*harness.Pause, error) {
	return a.pause, a.fakeAPI.HeartbeatHarness(ctx, s, phase)
}

func TestPauseHeartbeatUsesExistingInboxAndNeverStopsProcess(t *testing.T) {
	s, a, e, p := managedFixture(t)
	e.mu.Lock()
	e.inboxCapable = true
	e.mu.Unlock()
	s.api = &pauseTestAPI{fakeAPI: a, pause: &harness.Pause{ControlID: "12345678-1234-1234-9234-123456789012", State: "requested", DeadlineAt: time.Now().Add(time.Minute)}}
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
