// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/ownedprocess"
)

// Risk: the daemon forks before the old writer exits, double starts after a
// lost reply, frees an idle root slot, or receipts unrelated turn activity.
// The old-process wait and SDK acceptance are explicit barriers, not sleeps.
func TestNativeManagedStartWaitsForExitAndRelaysFinalWithReceipts(t *testing.T) {
	s, api, _ := testSupervisor(t)
	adapter, start, _ := nativeLeadFixture(t)
	g := start.Native.Grant
	g.TenantID, g.PrincipalID, g.DaemonID, g.Generation = s.tenantID, s.principalID, s.daemonID, s.generation
	// This fixture's server supplies real UUID identities, as production does.
	s.tenantID, s.principalID = uidLead("01"), uidLead("02")
	g.TenantID, g.PrincipalID = s.tenantID, s.principalID
	s.accounts[0].ID, s.accounts[0].Key, s.accounts[0].Harness = g.AccountID, start.AccountKey, Claude
	s.adapters[Claude] = adapter
	s.probedAccounts[g.AccountID] = true
	_, _ = enableLedgerFixture(t, s, api)
	start.Native.Grant = g
	plan := NativeLeadPlan{Spec: *start.Native, Session: start.Session, AccountKey: start.AccountKey, Workspace: start.Workspace, Host: "fixture"}
	request := NativeSessionStartRequest{RequestID: uidLead("40"), ProjectID: g.ProjectID, Profile: "native", Role: "coordinator", Harness: Claude, Cwd: plan.Workspace, Resume: start.Native.Resume, AfterExit: 123}
	waiting, exit, registered, started := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var registrations, authorizations atomic.Int32
	var startedOnce sync.Once
	relay := newFakeRelayAPI()
	input := ChatInput{}
	input.Message.ID, input.Message.Body, input.Receipt.State = uidLead("41"), "person-input-retained-only-in-chat", "sent"
	relay.inputs = []ChatInput{input}
	s.native.config = &NativeSessionConfig{
		Authorize: func(context.Context, NativeSessionStartRequest) (NativeLeadPlan, error) {
			authorizations.Add(1)
			return plan, nil
		},
		Revalidate: func(context.Context, NativeLeadGrant) error { return nil },
		Register: func(context.Context, NativeLeadPlan) (HarnessSession, error) {
			registrations.Add(1)
			close(registered)
			return plan.Session, nil
		},
		ReportStarted: func(context.Context, HarnessSession, ownedprocess.Identity) error {
			startedOnce.Do(func() { close(started) })
			return nil
		},
		ReportExit: func(context.Context, HarnessSession, ownedprocess.Identity) error { return nil },
		Abandon:    func(context.Context, NativeLeadPlan) error { return nil },
		SessionIO: func(HarnessSession) LeadSessionIO {
			return LeadSessionIO{Yield: func(context.Context) ([]HarnessControl, error) { return nil, nil }, CompleteControl: func(context.Context, string, string, string) error { return nil }, Drain: func(context.Context) ([]HarnessDelivery, error) { return nil, nil }, CompleteDelivery: func(context.Context, HarnessDelivery) error { return nil }}
		},
		Chat: func(HarnessSession) ChatRelayAPI { return relay },
		PinExit: func(pid int, cwd, cli string) (func(context.Context) error, error) {
			if pid != 123 || cwd != start.Workspace || cli != adapter.ClaudePath {
				t.Error("process pin changed")
			}
			return func(ctx context.Context) error {
				close(waiting)
				select {
				case <-exit:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}, nil
		},
	}
	result, err := s.StartNativeSession(t.Context(), request)
	if err != nil || result.State != "waiting_for_exit" {
		t.Fatal("handover did not queue", result, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	select {
	case <-waiting:
	case <-ctx.Done():
		t.Fatal("old-writer barrier not reached")
	}
	select {
	case <-registered:
		t.Fatal("registered/launched before the old writer exit")
	default:
	}
	data, _, err := s.ledger.Snapshot()
	if err != nil || len(data.Groups) != 1 {
		t.Fatal("queued root does not retain occupancy", data, err)
	}
	if replay, err := s.StartNativeSession(t.Context(), request); err != nil || replay != result || authorizations.Load() != 1 {
		t.Fatal("same start repeated authorization or fork", replay, err)
	}
	conflict := request
	conflict.Resume = uidLead("42")
	if _, err := s.StartNativeSession(t.Context(), conflict); !errors.Is(err, ErrReplay) {
		t.Fatal("conflicting resume did not fail closed", err)
	}
	close(exit)
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("native Query was not started")
	}
	s.native.mu.Lock()
	entry := s.native.entries[request.RequestID]
	s.native.mu.Unlock()
	t.Cleanup(func() {
		entry.mu.Lock()
		process := entry.process
		entry.mu.Unlock()
		if process != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = process.Stop(ctx)
		} else {
			entry.cancel()
		}
	})
	for {
		relay.mu.Lock()
		read := false
		answered := false
		for _, receipt := range relay.receipts {
			read = read || receipt == input.Message.ID+":read"
		}
		for _, final := range relay.finals {
			answered = answered || strings.Contains(final, "person-input-retained-only-in-chat")
		}
		relay.mu.Unlock()
		if read && answered {
			break
		}
		select {
		case <-relay.calls:
		case <-ctx.Done():
			t.Fatal("native final/read receipt did not reach PAIMOS")
		}
	}
	entry.mu.Lock()
	lead := entry.process.(*nativeClaudeLead)
	record := entry.record
	entry.mu.Unlock()
	if record.Native.VendorSessionID != request.Resume || !record.Attempted || record.ExitObserved || registrations.Load() != 1 {
		t.Fatal("native conversation/owner changed", record.Native, registrations.Load())
	}
	data, _, err = s.ledger.Snapshot()
	if err != nil || len(data.Groups) != 1 {
		t.Fatal("idle turn freed root occupancy", data, err)
	}
	raw, err := s.state.Read("lead-runtime-"+request.RequestID+".json", 32768)
	if err != nil || strings.Contains(string(raw), input.Message.Body) || !strings.Contains(string(raw), input.Message.ID) {
		t.Fatal("receipt lost provenance or persisted person content", err)
	}
	entry.mu.Lock()
	for i := 0; len(entry.record.Native.Attempts) < 128; i++ {
		entry.record.Native.Attempts["capacity-"+strconv.Itoa(i)] = nativeDigest("attempt")
	}
	if err := s.writeLead(entry.record, false); err != nil {
		entry.mu.Unlock()
		t.Fatal(err)
	}
	entry.mu.Unlock()
	if err := lead.Input(ctx, uidLead("43"), "over budget"); !errors.Is(err, ErrControlUnconfirmed) {
		t.Fatal("exhausted attempt set accepted another input", err)
	}
	if err := lead.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entry.done:
	case <-ctx.Done():
		t.Fatal("owned exit not observed")
	}
	if err := s.PollNativeSessions(ctx); err != nil {
		t.Fatal("exit reporting failed", err)
	}
	data, _, err = s.ledger.Snapshot()
	if err != nil || len(data.Groups) != 0 {
		t.Fatal("confirmed exit did not settle occupancy", data, err)
	}
}

// Risk: unrelated turn markers mint delivery receipts for native chat input.
func TestNativeChatReceiptsRequireMatchingInputCorrelation(t *testing.T) {
	relay := NewChatRelay(newFakeRelayAPI(), HarnessSession{}, ChatRelayOptions{RequireInputCorrelation: true})
	relay.input = &chatPendingInput{id: "person-input", armed: true, written: true}
	running, idle := chatState("running"), chatState("idle")
	relay.Observe(ChatSessionEvent{Sequence: 1, InputID: "other-input", Update: &running})
	relay.Observe(ChatSessionEvent{Sequence: 2, InputID: "other-input", Update: &idle})
	if len(relay.receipts) != 0 || relay.input == nil || relay.input.started != 0 {
		t.Fatal("other input minted native receipts")
	}
	relay.Observe(ChatSessionEvent{Sequence: 3, InputID: "person-input", Update: &running})
	relay.Observe(ChatSessionEvent{Sequence: 4, InputID: "other-input", Update: &idle})
	if len(relay.receipts) != 1 || relay.receipts[0].state != "delivered" || relay.input == nil {
		t.Fatal("wrong completion marked input read")
	}
	relay.Observe(ChatSessionEvent{Sequence: 5, InputID: "person-input", Update: &idle})
	if len(relay.receipts) != 2 || relay.receipts[1].state != "read" || relay.input != nil {
		t.Fatal("matching completion did not settle native input")
	}
}

// Risk: socket possession or a malformed protocol envelope enables a native
// coordinator without server intent, admission and actor/tool qualification.
func TestNativeStartSocketRequiresProtocolAndQualifiedIntent(t *testing.T) {
	s, _, _ := testSupervisor(t)
	request := NativeSessionStartRequest{RequestID: uidLead("01"), ProjectID: uidLead("02"), Profile: "native", Role: "coordinator", Harness: Claude, Cwd: s.workspace}
	envelope := NativeStartEnvelope{Protocol: NativeSessionProtocol, RequestID: request.RequestID, Method: "start", ExpectedBinding: json.RawMessage("null"), Params: request}
	raw, _ := json.Marshal(envelope)
	handler := serveNativeStart(s, func(r *http.Request) bool { return r.Header.Get("Authorization") == "Bearer local-fixture" })
	for _, test := range []struct {
		name   string
		auth   bool
		header string
		body   []byte
		status int
	}{
		{"no-auth", false, NativeSessionProtocol, raw, 401},
		{"wrong-protocol", true, "other/1", raw, 400},
		{"qualified-port-missing", true, NativeSessionProtocol, raw, 503},
		{"unknown-field", true, NativeSessionProtocol, []byte(strings.TrimSuffix(string(raw), "}") + `,"extra":true}`), 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/v1/native-sessions/start", strings.NewReader(string(test.body)))
			if test.auth {
				r.Header.Set("Authorization", "Bearer local-fixture")
			}
			r.Header.Set(NativeSessionProtocolHeader, test.header)
			w := httptest.NewRecorder()
			handler(w, r)
			if w.Code != test.status {
				t.Fatal("wrong refusal", w.Code, w.Body.String())
			}
		})
	}
	if len(s.native.entries) != 0 {
		t.Fatal("disabled endpoint changed native ownership")
	}
}
