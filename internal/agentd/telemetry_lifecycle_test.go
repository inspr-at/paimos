// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type telemetryHTTPAPI struct {
	*fakeAPI
	remote *Remote
}

func (a *telemetryHTTPAPI) Report(ctx context.Context, id string, report Telemetry) error {
	return a.remote.Report(ctx, id, report)
}

func useTelemetryHTTP(t *testing.T, s *Supervisor, api *fakeAPI, handler http.HandlerFunc) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	remote := NewRemote(server.URL, "test-key")
	remote.daemonID, remote.generation = s.daemonID, s.generation
	s.api = &telemetryHTTPAPI{fakeAPI: api, remote: remote}
}

func awaitTelemetryMonitor(t *testing.T, entry *owned) {
	t.Helper()
	select {
	case <-entry.monitorDone:
	case <-time.After(3 * time.Second):
		t.Fatal("owned child exit was not observed")
	}
}

func TestTelemetryHTTPAuthorityAndOutagePreserveActiveProcess(t *testing.T) {
	for _, tc := range []struct {
		status  int
		message string
	}{
		{401, "invalid key"}, {403, "enrollment revoked"},
		{409, "daemon generation conflict"}, {409, "enrollment_draining"},
		{410, "enrollment_revoked"}, {408, "request timeout"},
		{429, "slow down"}, {503, "unavailable"},
	} {
		t.Run(fmt.Sprintf("%d_%s", tc.status, tc.message), func(t *testing.T) {
			s, api, proc := testSupervisor(t)
			useTelemetryHTTP(t, s, api, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprintf(w, `{"error":%q}`, tc.message)
			})
			if err := s.StartRun(t.Context(), api.run); err == nil {
				t.Fatal("initial telemetry failure hidden")
			}
			entry := s.runs[api.run.ID]
			s.observe(entry, AdapterEvent{Kind: "usage", InputTokensDelta: 7})
			s.settlePending(t.Context())
			select {
			case <-proc.stopped:
				t.Fatal("authority/outage response stopped active work")
			default:
			}
			if s.accountAvailable("account") {
				t.Fatal("uncertain authority did not fence new dispatch")
			}
			if st := s.Lifecycle(""); len(st.ActiveRunIDs) != 1 || len(st.SettlementPendingRunIDs) != 1 {
				t.Fatalf("lost active process or settlement evidence: %+v", st)
			}
			// A local exit is observable even when the revoked key cannot settle.
			proc.once.Do(func() { close(proc.stopped) })
			awaitTelemetryMonitor(t, entry)
			entry.mu.Lock()
			defer entry.mu.Unlock()
			p := entry.record.Pending
			if !entry.record.ExitObserved || len(p) != 3 || p[0].Sequence != 1 || p[1].Sequence != 2 || p[1].InputTokensDelta != 7 || p[2].Sequence != 3 || p[2].Kind != "finished" {
				t.Fatal("exit or exact unsettled telemetry lost")
			}
		})
	}
}

type earlyTelemetryAdapter struct{ fakeAdapter }

func (a *earlyTelemetryAdapter) Start(ctx context.Context, request StartRequest, observe func(AdapterEvent)) (Process, error) {
	observe(AdapterEvent{Kind: "status"})
	return a.fakeAdapter.Start(ctx, request, observe)
}

func TestTelemetryProtocolStopsOnlyOwnedChild(t *testing.T) {
	for _, phase := range []string{"observe", "heartbeat", "settlement_retry", "startup", "before_process_binding"} {
		t.Run(phase, func(t *testing.T) {
			s, api, proc := testSupervisor(t)
			if phase == "heartbeat" {
				s.heartbeatInterval = 10 * time.Millisecond
			}
			var rejection atomic.Int64
			useTelemetryHTTP(t, s, api, func(w http.ResponseWriter, r *http.Request) {
				if status := rejection.Load(); r.URL.Path == "/api/runs/run/telemetry" && status != 0 {
					if phase == "before_process_binding" {
						// Later outage must not erase a confirmed pre-bind rejection.
						rejection.Store(503)
					}
					w.WriteHeader(int(status))
					_, _ = w.Write([]byte(`{"error":"status report requires status"}`))
					return
				}
				w.WriteHeader(http.StatusNoContent)
			})
			// Another owned child, sharing the same account, must stay alive.
			// Give the sibling its read-only verification scratch so the fixture
			// never requires two writers in the same physical checkout.
			sibling := &fakeProcess{stopped: make(chan struct{})}
			t.Cleanup(func() { _ = sibling.Stop(context.Background()) })
			siblingAdapter := &verificationFakeAdapter{fakeAdapter: fakeAdapter{proc: sibling}}
			s.adapters[Codex] = siblingAdapter
			siblingRun := api.run
			siblingRun.ID, siblingRun.AccountID = "sibling", "account"
			no, duration := false, int64(60)
			siblingRun.Purpose, siblingRun.VerificationTask, siblingRun.VerificationPolicy = VerificationPurpose, VerificationTask, "read_only"
			siblingRun.RepositoryMutationAllowed, siblingRun.MaxDurationSeconds = &no, &duration
			if err := s.StartRun(t.Context(), siblingRun); err != nil {
				t.Fatal(err)
			}
			if siblingAdapter.request.Workspace == s.workspace || siblingAdapter.request.Tools != nil {
				t.Fatal("sibling verification was not isolated")
			}
			api.run.ID = "run"
			s.adapters[Codex] = &fakeAdapter{proc: proc}
			if phase == "startup" || phase == "before_process_binding" {
				rejection.Store(400)
			}
			if phase == "before_process_binding" {
				s.adapters[Codex] = &earlyTelemetryAdapter{fakeAdapter{proc: proc}}
			}
			err := s.StartRun(t.Context(), api.run)
			if phase == "startup" || phase == "before_process_binding" {
				if !errors.Is(err, ErrTelemetryProtocol) {
					t.Fatalf("protocol rejection not reported: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			entry := s.runs["run"]
			if phase == "settlement_retry" {
				rejection.Store(503)
				s.observe(entry, AdapterEvent{Kind: "status"})
				select {
				case <-proc.stopped:
					t.Fatal("outage stopped child before protocol rejection")
				default:
				}
				rejection.Store(400)
				s.settlePending(t.Context())
			} else if phase == "observe" {
				rejection.Store(400)
				s.observe(entry, AdapterEvent{Kind: "status"})
			} else if phase == "heartbeat" {
				rejection.Store(400)
			}
			awaitTelemetryMonitor(t, entry)
			select {
			case <-sibling.stopped:
				t.Fatal("protocol error stopped unrelated owned child")
			default:
			}
			entry.mu.Lock()
			defer entry.mu.Unlock()
			p := entry.record.Pending
			if !entry.record.ExitObserved || entry.record.State != "failed" || len(p) < 2 || p[len(p)-1].ErrorCode != "app_server_protocol" {
				t.Fatal("protocol failure lost truthful exit/failure or unsettled evidence")
			}
		})
	}
}
