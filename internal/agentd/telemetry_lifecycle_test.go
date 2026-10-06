// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/client"
)

type telemetryHTTPAPI struct {
	*fakeAPI
	remote *Remote
}

func (a *telemetryHTTPAPI) Report(ctx context.Context, id string, report Telemetry) error {
	return a.remote.Report(ctx, id, report)
}

func (a *telemetryHTTPAPI) ReportForClaim(ctx context.Context, id, daemon, generation string, report Telemetry) error {
	return a.remote.ReportForClaim(ctx, id, daemon, generation, report)
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
			wantRejections := 0
			if tc.status == 401 || tc.status == 403 || tc.status == 410 || tc.message == "daemon generation conflict" {
				// Authority failures leave live children alone, then park the
				// exact outbox once exit is proven instead of retrying forever.
				wantRejections = 1
			}
			if !entry.record.ExitObserved || len(entry.record.DeadLetters) != 0 || entry.record.ReportRejections != wantRejections || len(p) != 3 || p[0].Sequence != 1 || p[1].Sequence != 2 || p[1].InputTokensDelta != 7 || p[2].Sequence != 3 || p[2].Kind != "finished" {
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
			p := append(append([]Telemetry(nil), entry.record.DeadLetters...), entry.record.Pending...)
			if !entry.record.ExitObserved || entry.record.State != "failed" || len(p) < 2 || p[len(p)-1].ErrorCode != "reporter_unavailable" {
				t.Fatal("protocol failure lost truthful exit/failure or unsettled evidence")
			}
		})
	}
}

func TestTelemetryDeadLettersBoundedAndRecoversAfterObservedExit(t *testing.T) {
	s, api, _ := testSupervisor(t)
	pending := []Telemetry{
		{Sequence: 1, Kind: "status", EffectiveModel: "invalid model", ModelEvidence: "vendor_reported"},
		{Sequence: 2, Kind: "turn", TurnCountDelta: 1},
		{Sequence: 3, Kind: "started", Status: "running"},
		{Sequence: 4, Kind: "finished", Status: "failed", ErrorCode: "app_server_protocol"},
	}
	entry := &owned{record: Record{TenantID: s.tenantID, PrincipalID: s.principalID, RunID: "run", AccountID: "account", Generation: s.generation, ExecutionMode: VerificationPurpose, LaunchState: launchAttempted, State: "failed", Sequence: 4, Pending: pending}}
	s.runs["run"] = entry
	var rejected, terminal int
	var final Telemetry
	var mu sync.Mutex
	counts := func() (int, int, Telemetry) { mu.Lock(); defer mu.Unlock(); return rejected, terminal, final }
	useTelemetryHTTP(t, s, api, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var report Telemetry
		if json.NewDecoder(r.Body).Decode(&report) != nil {
			t.Error("invalid report")
			w.WriteHeader(400)
			return
		}
		if r.Header.Get("X-Aeon-Daemon-Generation") != s.generation {
			t.Error("claim generation changed")
		}
		if report.Sequence <= 4 {
			rejected++
			w.WriteHeader(400)
			return
		}
		terminal++
		if terminal == 1 {
			final = report
			w.WriteHeader(503)
			return
		} // uncertain terminal delivery
		if !reflect.DeepEqual(final, report) {
			t.Error("terminal receipt replay changed")
		}
		w.WriteHeader(204)
	})
	for i := 0; i < 8; i++ {
		s.settlePending(t.Context())
	}
	rejectCount, terminalCount, _ := counts()
	if rejectCount != 3 || terminalCount != 0 || len(entry.record.DeadLetters) != 0 {
		t.Fatal("unconfirmed exit allowed terminal recovery or unbounded rejection retries")
	}
	// Restore only durable fixture state: retry limits survive a daemon restart.
	records := s.journal.Snapshot()
	if len(records) != 1 || records[0].ReportRejections != 3 {
		t.Fatal("retry limit was not durable")
	}
	entry.record = records[0]
	entry.record.ExitObserved = true
	if err := s.journal.Put(entry.record); err != nil {
		t.Fatal(err)
	}
	s.settlePending(t.Context())
	_, terminalCount, _ = counts()
	if terminalCount != 1 || !reflect.DeepEqual(entry.record.DeadLetters, pending) || len(entry.record.Pending) != 1 || !entry.record.SettlementGap {
		t.Fatal("terminal outage erased rejected evidence or unknown usage")
	}
	records = s.journal.Snapshot()
	entry.record = records[0]
	s.settlePending(t.Context())
	for i := 0; i < 8; i++ {
		s.settlePending(t.Context())
	}
	want := Telemetry{Sequence: 5, Kind: "finished", Status: "failed", ErrorCode: "reporter_unavailable"}
	rejectCount, terminalCount, finalReport := counts()
	if terminalCount != 2 || rejectCount != 3 || !reflect.DeepEqual(finalReport, want) || len(entry.record.Pending) != 0 || entry.record.State != "failed" {
		t.Fatal("minimal terminal recovery failed or retried settled evidence")
	}
	lifecycle := s.Lifecycle("")
	if lifecycle.VerificationResults["run"] != "failed" || len(lifecycle.SettlementPendingRunIDs) != 1 {
		t.Fatal("failed verification confused with unconfirmed usage")
	}
}

func TestTelemetryTerminalRejectionIsBounded(t *testing.T) {
	for _, oldGeneration := range []bool{false, true} {
		t.Run(fmt.Sprintf("old_generation_%t", oldGeneration), func(t *testing.T) {
			s, api, _ := testSupervisor(t)
			generation := s.generation
			// Keep the original rejection history and permit only one permanent
			// refusal of the minimal terminal recovery (formerly three).
			expectedAttempts, expectedRejections := int64(4), 4
			if oldGeneration {
				generation = "prior-generation"
				expectedAttempts, expectedRejections = 2, 2
			}
			entry := &owned{record: Record{TenantID: s.tenantID, PrincipalID: s.principalID, RunID: "run", AccountID: "account", Generation: generation, State: "failed", ExitObserved: true, Sequence: 1, Pending: []Telemetry{{Sequence: 1, Kind: "status"}}}}
			s.runs["run"] = entry
			var attempts atomic.Int64
			useTelemetryHTTP(t, s, api, func(w http.ResponseWriter, r *http.Request) { attempts.Add(1); w.WriteHeader(422) })
			for i := 0; i < 20; i++ {
				s.settlePending(t.Context())
			}
			if attempts.Load() != expectedAttempts || len(entry.record.DeadLetters) != 1 || len(entry.record.Pending) != 1 || entry.record.ReportRejections != expectedRejections || !entry.record.SettlementGap {
				t.Fatal("permanently rejected terminal lost evidence or retried forever")
			}
			// Reload the durable fallback into another generation: the bound still holds.
			entry.record = s.journal.Snapshot()[0]
			s.generation = "another-generation"
			for i := 0; i < 20; i++ {
				s.settlePending(t.Context())
			}
			if attempts.Load() != expectedAttempts {
				t.Fatal("restart reset the rejected fallback retry bound")
			}
		})
	}
}

func TestTelemetryRestartRecoversBeforeProbeChangesGeneration(t *testing.T) {
	for _, exitObserved := range []bool{true, false} {
		t.Run(fmt.Sprintf("exit_observed_%t", exitObserved), func(t *testing.T) {
			s, api, proc := testSupervisor(t)
			pending := []Telemetry{{Sequence: 1, Kind: "status", EffectiveModel: "invalid model"}, {Sequence: 2, Kind: "finished", Status: "failed", ErrorCode: "app_server_protocol"}}
			oldGeneration := s.generation
			record := Record{TenantID: s.tenantID, PrincipalID: s.principalID, RunID: "run", AccountID: "account", Generation: oldGeneration,
				Workspace: s.workspace, ExecutionMode: VerificationPurpose, LaunchState: launchAttempted, State: "failed", Sequence: 2, Pending: pending, ExitObserved: exitObserved}
			if err := s.journal.Put(record); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			restarted, err := NewSupervisor(t.Context(), Config{API: api, StateRoot: s.journalDir(), DaemonID: s.daemonID, Workspace: s.workspace,
				Adapters: []Adapter{&fakeAdapter{proc: proc}}, EstimatedUnits: map[string]int64{"requests": 1}, Accounts: s.accounts})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = restarted.Close(context.Background()) })
			if restarted.generation == oldGeneration {
				t.Fatal("fixture did not restart with a new generation")
			}
			var attempts []Telemetry
			useTelemetryHTTP(t, restarted, api, func(w http.ResponseWriter, r *http.Request) {
				var report Telemetry
				if json.NewDecoder(r.Body).Decode(&report) != nil {
					t.Error("invalid report")
					w.WriteHeader(400)
					return
				}
				if r.Header.Get("X-Aeon-Daemon-Generation") != oldGeneration {
					t.Error("recovery changed original claim generation")
				}
				attempts = append(attempts, report)
				if report.Sequence <= 2 {
					w.WriteHeader(400)
					return
				}
				w.WriteHeader(204)
			})
			// An old claim recovers before an account probe may change authority.
			restarted.settlePending(t.Context())
			entry := restarted.runs["run"]
			if !exitObserved {
				if len(attempts) != 1 || len(entry.record.DeadLetters) != 0 || entry.record.TerminalRecovery {
					t.Fatal("unconfirmed prior child allowed recovery")
				}
				return
			}
			want := Telemetry{Sequence: 3, Kind: "finished", Status: "failed", ErrorCode: "reporter_unavailable"}
			if len(attempts) != 2 || !reflect.DeepEqual(attempts[1], want) || !reflect.DeepEqual(entry.record.DeadLetters, pending) || len(entry.record.Pending) != 0 || !entry.record.SettlementGap {
				t.Fatalf("old-generation rejection did not settle in the first flush: attempts=%d pending=%d", len(attempts), len(entry.record.Pending))
			}
			// Subsequent account probes may rotate authority; settled data is not resent.
			useTelemetryHTTP(t, restarted, api, func(w http.ResponseWriter, r *http.Request) {
				t.Error("settled outbox posted after generation rotation")
				w.WriteHeader(403)
			})
			restarted.settlePending(t.Context())
			saved := restarted.journal.Snapshot()
			if len(saved) != 1 || !saved[0].TerminalRecovery || len(saved[0].Pending) != 0 || !reflect.DeepEqual(saved[0].DeadLetters, pending) {
				t.Fatal("recovery was not durable")
			}
		})
	}
}

func restartTelemetryFixture(t *testing.T, s *Supervisor) *Supervisor {
	t.Helper()
	if err := s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	var adapters []Adapter
	for _, adapter := range s.adapters {
		adapters = append(adapters, adapter)
	}
	next, err := NewSupervisor(t.Context(), Config{API: s.api, StateRoot: s.journalDir(), DaemonID: s.daemonID, Workspace: s.workspace,
		Adapters: adapters, Accounts: s.accounts, EstimatedUnits: s.estimates})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = next.Close(context.Background()) })
	if next.generation == s.generation {
		t.Fatal("restart did not rotate the local generation")
	}
	return next
}

func TestTelemetryTerminalRecoveryCountsPermanentRefusals(t *testing.T) {
	for _, oldGeneration := range []bool{false, true} {
		for _, tc := range []struct{ permanent, transient int }{{422, 503}, {409, 408}, {403, 429}} {
			t.Run(fmt.Sprintf("old_%t_status_%d", oldGeneration, tc.permanent), func(t *testing.T) {
				s, api, _ := testSupervisor(t)
				generation, rejected := s.generation, 3
				if oldGeneration {
					generation, rejected = "prior-generation", 1
				}
				pending := []Telemetry{{Sequence: 1, Kind: "status", EffectiveModel: "invalid model"}, {Sequence: 2, Kind: "finished", Status: "failed"}}
				entry := &owned{record: Record{TenantID: s.tenantID, PrincipalID: s.principalID, RunID: "run", AccountID: "account", Generation: generation,
					State: "failed", ExitObserved: true, Sequence: 2, Pending: pending, ReportRejections: rejected}}
				s.runs["run"] = entry
				var attempts atomic.Int64
				useTelemetryHTTP(t, s, api, func(w http.ResponseWriter, r *http.Request) {
					var report Telemetry
					if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
						t.Error(err)
					}
					want := Telemetry{Sequence: 3, Kind: "finished", Status: "failed", ErrorCode: "reporter_unavailable"}
					if !reflect.DeepEqual(report, want) || r.Header.Get("X-Aeon-Daemon-Generation") != generation {
						t.Error("minimal recovery changed report or claim identity")
					}
					if attempts.Add(1) == 1 {
						w.WriteHeader(tc.transient)
						return
					}
					w.WriteHeader(tc.permanent)
					_, _ = w.Write([]byte(`{"error":"run is not live"}`))
				})
				s.settlePending(t.Context())
				if attempts.Load() != 1 || entry.record.ReportRejections != rejected || !entry.record.TerminalRecovery || !reflect.DeepEqual(entry.record.DeadLetters, pending) {
					t.Fatal("entering recovery reset rejection history or lost original evidence")
				}
				// A transient failure, then another daemon generation: the exact
				// terminal remains retryable even with the original rejection count.
				s = restartTelemetryFixture(t, s)
				entry = s.runs["run"]
				for range 8 {
					s.settlePending(t.Context())
				}
				if attempts.Load() != 2 || entry.record.ReportRejections != rejected+1 || len(entry.record.Pending) != 1 || !entry.record.SettlementGap || !reflect.DeepEqual(entry.record.DeadLetters, pending) {
					t.Fatal("permanent terminal refusal was not counted and durably stopped")
				}
				s = restartTelemetryFixture(t, s)
				entry = s.runs["run"]
				for range 8 {
					s.settlePending(t.Context())
				}
				if attempts.Load() != 2 || len(entry.record.Pending) != 1 {
					t.Fatal("restart retried or erased the permanently refused terminal")
				}
			})
		}
	}
}

func TestTelemetryExitedAuthorityRefusalIsBounded(t *testing.T) {
	for _, status := range []int{401, 403, 404, 409, 410} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			s, api, _ := testSupervisor(t)
			pending := []Telemetry{{Sequence: 1, Kind: "usage", InputTokensDelta: 7}, {Sequence: 2, Kind: "finished", Status: "failed"}}
			entry := &owned{record: Record{TenantID: s.tenantID, PrincipalID: s.principalID, RunID: "run", AccountID: "account", Generation: "prior-generation",
				State: "failed", ExitObserved: true, LaunchState: launchAttempted, Sequence: 2, Pending: pending}}
			s.runs["run"] = entry
			var attempts atomic.Int64
			useTelemetryHTTP(t, s, api, func(w http.ResponseWriter, r *http.Request) {
				attempts.Add(1)
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":"daemon generation conflict"}`))
			})
			for range 8 {
				s.settlePending(t.Context())
			}
			if attempts.Load() != 1 || entry.record.ReportRejections != 1 || !reflect.DeepEqual(entry.record.Pending, pending) || len(entry.record.DeadLetters) != 0 || entry.record.TerminalRecovery {
				t.Fatal("authority refusal looped, lost exact evidence, or attempted unauthorized terminal recovery")
			}
			s = restartTelemetryFixture(t, s)
			entry = s.runs["run"]
			for range 8 {
				s.settlePending(t.Context())
			}
			if attempts.Load() != 1 || !reflect.DeepEqual(entry.record.Pending, pending) {
				t.Fatal("restart lost the authority-refusal stop or its evidence")
			}
		})
	}
}

type settlementProbeAPI struct {
	*telemetryHTTPAPI
	probes  []string
	onProbe func(string)
}

func (*settlementProbeAPI) Queued(context.Context) ([]Run, error) { return nil, nil }
func (a *settlementProbeAPI) Probe(_ context.Context, account, _, _ string, _ bool) error {
	a.probes = append(a.probes, account)
	a.onProbe(account)
	return nil
}

func TestTelemetryPollingPreservesOldSettlementAuthority(t *testing.T) {
	for _, dispatch := range []bool{true, false} {
		for _, dependencyBlocked := range []bool{false, true} {
			t.Run(fmt.Sprintf("dispatch_%t_dependency_blocked_%t", dispatch, dependencyBlocked), func(t *testing.T) {
				s, api, _ := testSupervisor(t)
				pending := []Telemetry{{Sequence: 1, Kind: "finished", Status: "failed"}}
				entry := &owned{record: Record{TenantID: s.tenantID, PrincipalID: s.principalID, RunID: "run", AccountID: "account", Generation: "prior-generation",
					State: "failed", ExitObserved: true, LaunchState: launchAttempted, Sequence: 1, Pending: pending}}
				s.runs["run"] = entry
				if err := s.journal.Put(entry.record); err != nil {
					t.Fatal(err)
				}
				s.accounts[0].DependencyBlocked = dependencyBlocked
				s.accounts = append(s.accounts, EnrolledAccount{ID: "sibling", Key: "sibling", Harness: Codex})
				var attempts atomic.Int64
				var rotated atomic.Bool
				useTelemetryHTTP(t, s, api, func(w http.ResponseWriter, r *http.Request) {
					attempt := attempts.Add(1)
					if rotated.Load() || r.Header.Get("X-Aeon-Daemon-Generation") != "prior-generation" {
						t.Error("probe rotated account authority before settlement")
						w.WriteHeader(403)
						return
					}
					if attempt == 1 {
						w.WriteHeader(503)
						return
					}
					w.WriteHeader(204)
				})
				probes := &settlementProbeAPI{telemetryHTTPAPI: s.api.(*telemetryHTTPAPI), onProbe: func(account string) {
					if account == "account" {
						rotated.Store(true)
					}
				}}
				s.api = probes
				poll := s.PollOnce
				if !dispatch {
					// pairedPollIteration uses this path after a retryable sync failure.
					s.SetPairingFailure(agentsetup.PairingServerUnavailable)
					poll = s.ProbeOnce
				}
				err := poll(t.Context())
				var status *client.StatusError
				if !errors.As(err, &status) || status.Status != 503 || attempts.Load() != 1 || rotated.Load() || !reflect.DeepEqual(probes.probes, []string{"sibling"}) || !reflect.DeepEqual(entry.record.Pending, pending) {
					t.Fatalf("failed settlement hidden, skipped, or followed by authority rotation: err=%v attempts=%d probes=%v", err, attempts.Load(), probes.probes)
				}
				if err := poll(t.Context()); err != nil {
					t.Fatal(err)
				}
				if attempts.Load() != 2 || !rotated.Load() || len(entry.record.Pending) != 0 || !reflect.DeepEqual(probes.probes, []string{"sibling", "account", "sibling"}) {
					t.Fatal("successful exact settlement did not release only the affected account probe")
				}
			})
		}
	}
}
