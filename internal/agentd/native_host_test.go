// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/internal/client"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativesessions/daemon"
)

// nativeHostServer is the paired server as the C0 host sees it: start and
// command claims, the owner's view, receipts and the per-runtime command queue.
type nativeHostServer struct {
	mu        sync.Mutex
	now       time.Time
	tenant    string
	owner     string
	project   string
	policy    nativecontract.Policy
	launch    nativecontract.LaunchConfig
	sessions  map[string]*nativeHostSession // intent request ID
	commands  map[string]*nativeHostCommand // operation ID
	revoked   map[string]bool               // intent request IDs whose launch re-check is refused
	refused   map[string]bool               // owned session IDs whose receipts are refused
	rejected  []nativecontract.Receipt
	completed chan nativecontract.Receipt
}
type nativeHostSession struct {
	local     nativecontract.LocalStart
	operation string
	binding   nativecontract.Binding
	view      nativecontract.SessionView
}
type nativeHostCommand struct {
	item    nativecontract.PendingCommand
	command nativecontract.DaemonCommand
	done    bool
}

func nativeHostRefuse(w http.ResponseWriter, status int, reason nativecontract.Reason) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(nativecontract.Error{Reason: reason})
}

func (f *nativeHostServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	proof, err := daemon.Decode(r)
	if err != nil {
		nativeHostRefuse(w, http.StatusForbidden, nativecontract.ReasonGrantMissing)
		return
	}
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	f.mu.Lock()
	defer f.mu.Unlock()
	var out any
	status, reason := 0, nativecontract.Reason("")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/agentd/native-sessions/claim":
		var in nativecontract.Claim
		_ = json.Unmarshal(raw, &in)
		out, status, reason = f.claim(proof, in)
	case r.Method == http.MethodGet && r.URL.Path == "/api/agentd/native-sessions/commands":
		out = f.pending(proof)
	case r.Method == http.MethodPost && r.URL.Path == "/api/agentd/native-sessions/receipts":
		var in nativecontract.Receipt
		_ = json.Unmarshal(raw, &in)
		out, status, reason = f.receipt(in)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/projects/"+f.project+"/native-sessions/"):
		s := f.byOwned(strings.TrimPrefix(r.URL.Path, "/api/projects/"+f.project+"/native-sessions/"))
		if s == nil {
			status, reason = http.StatusNotFound, nativecontract.ReasonInvalidInput
		} else {
			out = s.view
		}
	default:
		status, reason = http.StatusNotFound, nativecontract.ReasonInvalidInput
	}
	if status != 0 {
		nativeHostRefuse(w, status, reason)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (f *nativeHostServer) byOwned(owned string) *nativeHostSession {
	for _, s := range f.sessions {
		if s.binding.OwnedSessionID == owned {
			return s
		}
	}
	return nil
}

func (f *nativeHostServer) claim(proof daemon.Proof, in nativecontract.Claim) (any, int, nativecontract.Reason) {
	switch in.ClaimKind {
	case "start":
		if proof.LocalStart == nil {
			return nil, http.StatusForbidden, nativecontract.ReasonGrantMissing
		}
		local := *proof.LocalStart
		s := f.sessions[local.Intent.RequestID]
		if s == nil {
			s = &nativeHostSession{local: local, operation: uuid.NewString(), binding: nativecontract.Binding{TenantID: f.tenant, ProjectID: f.project, OwnedSessionID: uuid.NewString(), HarnessSessionID: uuid.NewString(), RuntimeEpoch: uuid.NewString(), DaemonID: in.DaemonID, DaemonGeneration: in.DaemonGeneration, BindingRevision: 2}}
			s.view = nativecontract.SessionView{OwnedSessionID: s.binding.OwnedSessionID, CurrentBinding: &s.binding, OwnerPersonID: f.owner, Role: "coordinator", Profile: "native", Harness: Claude, State: "starting", Mode: "structured", Revision: 2, ConversationRef: "fixture-conversation"}
			f.sessions[local.Intent.RequestID] = s
		} else if f.revoked[local.Intent.RequestID] {
			return nil, http.StatusForbidden, nativecontract.ReasonPermissionRevoked
		}
		b := s.binding
		return nativecontract.ClaimResult{OperationID: s.operation, OwnedSessionID: b.OwnedSessionID, Intent: local.Intent, Policy: f.policy, ExpectedBinding: &b, ExpiresAt: f.now.Add(20 * time.Second), ClaimKind: "start", LaunchAuthorized: true, LaunchConfig: f.launch, AuthorizationTTLMS: 20000}, 0, ""
	case "command":
		c := f.commands[in.OperationID]
		if c == nil || c.done {
			return nil, http.StatusForbidden, nativecontract.ReasonGrantMissing
		}
		b, command := c.item.ExpectedBinding, c.command
		s := f.byOwned(b.OwnedSessionID)
		return nativecontract.ClaimResult{OperationID: in.OperationID, OwnedSessionID: b.OwnedSessionID, Intent: s.local.Intent, Policy: f.policy, ExpectedBinding: &b, ExpiresAt: f.now.Add(20 * time.Second), ClaimKind: "command", LaunchConfig: f.launch, Command: &command, CommandActor: &nativecontract.CommandActor{TenantID: f.tenant, PrincipalID: f.owner, OwnerPersonID: f.owner, GrantID: f.policy.GrantID, GrantRevision: f.policy.GrantRevision}, AuthorizationTTLMS: 20000}, 0, ""
	}
	return nil, http.StatusBadRequest, nativecontract.ReasonUnsupported
}

func (f *nativeHostServer) pending(proof daemon.Proof) nativecontract.PendingCommandPage {
	identity, _, _ := proof.Runtime()
	page := nativecontract.PendingCommandPage{Items: []nativecontract.PendingCommand{}}
	operations := make([]string, 0, len(f.commands))
	for op := range f.commands {
		operations = append(operations, op)
	}
	slices.Sort(operations)
	for _, op := range operations {
		if c := f.commands[op]; !c.done && c.item.ExpectedBinding.RuntimeEpoch == identity.RuntimeEpoch {
			page.Items = append(page.Items, c.item)
		}
	}
	return page
}

func (f *nativeHostServer) receipt(in nativecontract.Receipt) (any, int, nativecontract.Reason) {
	if in.Binding == nil {
		return nil, http.StatusBadRequest, nativecontract.ReasonInvalidInput
	}
	s := f.byOwned(in.Binding.OwnedSessionID)
	if s == nil || !s.binding.Equal(*in.Binding) {
		return nil, http.StatusConflict, nativecontract.ReasonWrongGeneration
	}
	if in.Operation == "start" {
		switch in.State {
		case "applied":
			s.view.State = "running"
		case "rejected":
			s.view.State, s.view.Reason = "failed", in.Reason
			f.rejected = append(f.rejected, in)
		}
		view := s.view
		return nativecontract.OperationResult{Receipt: in, Session: &view}, 0, ""
	}
	if f.refused[in.Binding.OwnedSessionID] {
		return nil, http.StatusForbidden, nativecontract.ReasonPermissionRevoked
	}
	if c := f.commands[in.OperationID]; c != nil && (in.State == "turn_completed" || in.State == "applied" || in.State == "exit_confirmed") {
		c.done = true
	}
	if in.State == "turn_completed" {
		f.completed <- in
	}
	view := s.view
	return nativecontract.OperationResult{Receipt: in, Session: &view}, 0, ""
}

// queue is the person's accepted command; only its exact runtime may claim it.
func (f *nativeHostServer) queue(b nativecontract.Binding, kind, request string, params any) {
	raw, _ := json.Marshal(params)
	f.mu.Lock()
	defer f.mu.Unlock()
	op := uuid.NewString()
	f.commands[op] = &nativeHostCommand{item: nativecontract.PendingCommand{OperationID: op, OwnedSessionID: b.OwnedSessionID, RequestID: request, Kind: kind, ExpectedBinding: b, ExpiresAt: f.now.Add(time.Minute)}, command: nativecontract.DaemonCommand{Kind: kind, Params: raw}}
}

type nativeHostProfiles struct{ root string }

func (p nativeHostProfiles) Resolve(_ context.Context, q nativecontract.ProfileRequest) (nativecontract.LocalProfile, error) {
	return nativecontract.LocalProfile{AccountID: q.AccountID, AccountHome: p.root, UserHome: p.root, WorkspaceRoot: p.root, ConversationRoot: p.root, ProfileBindingRevision: q.BindingRevision}, nil
}

// nativeHostProvider stands in for an owned provider. Turns are explicit
// test observations, never inferred from a successful write.
type nativeHostProvider struct {
	mu       sync.Mutex
	identity nativecontract.NativeIdentity
	events   chan nativecontract.ProviderEvent
	sequence uint64
	inputs   []string
	closes   int
	exited   bool
	now      time.Time
}

func (p *nativeHostProvider) Send(_ context.Context, in nativecontract.Input) (nativecontract.NativeEvidence, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sequence++
	p.inputs = append(p.inputs, in.InputID)
	return nativecontract.NativeEvidence{Stage: "write_acknowledged", InputID: in.InputID, RequestID: in.RequestID, SourceSequence: p.sequence, ObservedAt: p.now}, nil
}
func (*nativeHostProvider) Interrupt(context.Context, nativecontract.Interrupt) (nativecontract.NativeEvidence, error) {
	return nativecontract.NativeEvidence{}, ErrUnsupported
}
func (*nativeHostProvider) Decide(context.Context, nativecontract.ApprovalDecision) (nativecontract.NativeEvidence, error) {
	return nativecontract.NativeEvidence{}, ErrUnsupported
}
func (p *nativeHostProvider) Close(context.Context) (nativecontract.ExitEvidence, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closes++
	p.exited = true
	return nativecontract.ExitEvidence{ProcessIdentityDigest: p.identity.ProcessIdentityDigest, RootExited: true, GroupAbsent: true, ObservedAt: p.now}, nil
}
func (p *nativeHostProvider) Wait(context.Context) (nativecontract.ExitEvidence, error) {
	return p.ObserveExit(context.Background())
}
func (p *nativeHostProvider) ObserveExit(context.Context) (nativecontract.ExitEvidence, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.exited {
		return nativecontract.ExitEvidence{}, ErrProcessesUnconfirmed
	}
	return nativecontract.ExitEvidence{ProcessIdentityDigest: p.identity.ProcessIdentityDigest, RootExited: true, GroupAbsent: true, ObservedAt: p.now}, nil
}
func (*nativeHostProvider) Capabilities() nativecontract.Capabilities {
	return nativecontract.Capabilities{Steer: "unsupported", Quota: "unsupported"}
}
func (p *nativeHostProvider) Identity() nativecontract.NativeIdentity     { return p.identity }
func (p *nativeHostProvider) Events() <-chan nativecontract.ProviderEvent { return p.events }
func (p *nativeHostProvider) received(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Contains(p.inputs, id)
}
func (p *nativeHostProvider) closed() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closes
}

// complete reports the exact turn for an acknowledged input.
func (p *nativeHostProvider) complete(in nativecontract.Input) {
	p.mu.Lock()
	seq := p.sequence
	p.sequence += 2
	p.mu.Unlock()
	id, turn := in.InputID, uuid.NewString()
	for i, state := range []string{"turn_started", "turn_completed"} {
		p.events <- nativecontract.ProviderEvent{Identity: p.identity, SourceSequence: seq + uint64(i) + 1, Receipt: &nativecontract.Receipt{RequestID: in.RequestID, InputID: &id, TurnRef: &turn, State: state}}
	}
}

type nativeHostLauncher struct {
	mu        sync.Mutex
	now       time.Time
	opens     int
	providers map[string]*nativeHostProvider // runtime epoch
}

func (l *nativeHostLauncher) Open(_ context.Context, spec nativecontract.LaunchSpec) (nativecontract.NativeTransport, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.opens++
	digest := sha256.Sum256([]byte(spec.Binding.RuntimeEpoch))
	p := &nativeHostProvider{now: l.now, events: make(chan nativecontract.ProviderEvent, 16), identity: nativecontract.NativeIdentity{Provider: Claude, NativeSessionRef: uuid.NewString(), RuntimeEpoch: spec.Binding.RuntimeEpoch, PID: 4000 + l.opens, StartedAt: l.now, ProcessIdentityDigest: hex.EncodeToString(digest[:])}}
	l.providers[spec.Binding.RuntimeEpoch] = p
	return p, nil
}

type nativeHostFixture struct {
	s        *Supervisor
	h        *NativeHost
	server   *nativeHostServer
	launcher *nativeHostLauncher
	root     string
	computer string
}

// newNativeHostFixture composes the production C0 host with one enrolled
// Claude account per login on a real shared ledger; one clock drives all.
func newNativeHostFixture(t *testing.T, accounts int) *nativeHostFixture {
	t.Helper()
	s, api, _ := testSupervisor(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	s.capacityNow = func() time.Time { return now }
	s.tenantID, s.principalID = uuid.NewString(), uuid.NewString()
	s.accounts = nil
	for i := range accounts {
		s.accounts = append(s.accounts, EnrolledAccount{ID: uuid.NewString(), Key: "key-" + strconv.Itoa(i), Harness: Claude, VerifiedIdentity: "login-" + strconv.Itoa(i)})
	}
	_, _ = enableLedgerFixture(t, s, api)
	server := &nativeHostServer{now: now, tenant: s.tenantID, owner: uuid.NewString(), project: uuid.NewString(), sessions: map[string]*nativeHostSession{}, commands: map[string]*nativeHostCommand{}, revoked: map[string]bool{}, refused: map[string]bool{}, completed: make(chan nativecontract.Receipt, 1024),
		policy: nativecontract.Policy{GrantID: uuid.NewString(), GrantRevision: 1, ExpiresAt: now.Add(time.Hour), ToolPolicyDigest: strings.Repeat("a", 64), SettingsPolicyDigest: strings.Repeat("b", 64), PermissionPolicy: "supervised", LocalHistory: "vendor_native", Continuation: "manual", MaximumRuntimeSeconds: 3600, MaximumTurns: 1000, MaximumTokens: 1000000},
		launch: nativecontract.LaunchConfig{ModelProfileID: uuid.NewString(), Model: "fixture-model", ServiceTier: "default", ToolPolicyDigest: strings.Repeat("a", 64), SettingsPolicyDigest: strings.Repeat("b", 64)}}
	paired := httptest.NewServer(server)
	t.Cleanup(paired.Close)
	f := &nativeHostFixture{s: s, server: server, root: s.workspace, computer: uuid.NewString(), launcher: &nativeHostLauncher{now: now, providers: map[string]*nativeHostProvider{}}}
	config := NativeProduction(NewRemote(paired.URL, "fixture-runtime"), f.computer, server.owner, nativeHostProfiles{f.root}, nativecontract.Capabilities{Input: true, Stop: true, Steer: "unsupported", Quota: "unsupported", QualificationDigest: strings.Repeat("e", 64)})
	config.Launcher = f.launcher
	config.Bind(s)
	f.h = config.Host.(*NativeHost)
	t.Cleanup(func() {
		f.launcher.mu.Lock()
		defer f.launcher.mu.Unlock()
		for _, p := range f.launcher.providers {
			close(p.events)
		}
		f.h.mu.Lock()
		defer f.h.mu.Unlock()
		for _, e := range f.h.entries {
			if e.cancel != nil {
				e.cancel()
			}
			_ = e.journal.Close()
		}
	})
	return f
}

func (f *nativeHostFixture) local(account int) nativecontract.LocalStart {
	a := f.s.accounts[account]
	return nativecontract.LocalStart{Intent: nativecontract.StartIntent{RequestID: uuid.NewString(), Role: "coordinator", Profile: "native", Harness: Claude, ComputerID: f.computer, AccountID: a.ID, WorkspaceID: "fixture-workspace", ModelProfileID: f.server.launch.ModelProfileID, PolicyID: f.server.policy.GrantID}, Cwd: f.root, AccountKey: a.Key}
}

func (f *nativeHostFixture) start(t *testing.T, account int) (nativecontract.Binding, *nativeHostProvider) {
	t.Helper()
	out, err := f.h.Start(t.Context(), f.local(account))
	if err != nil || out.Session == nil || out.Session.State != "running" || out.Session.CurrentBinding == nil {
		t.Fatal("native start did not run", out, err)
	}
	b := *out.Session.CurrentBinding
	f.launcher.mu.Lock()
	defer f.launcher.mu.Unlock()
	return b, f.launcher.providers[b.RuntimeEpoch]
}

func (f *nativeHostFixture) input(b nativecontract.Binding) nativecontract.Input {
	in := nativecontract.Input{RequestID: uuid.NewString(), ExpectedBinding: b, InputID: uuid.NewString(), Mode: "now", Text: "fixture person input", ExpiresAt: f.server.now.Add(time.Minute)}
	f.server.queue(b, "input", in.RequestID, in)
	return in
}

func (f *nativeHostFixture) stop(b nativecontract.Binding, p *nativeHostProvider) nativecontract.Stop {
	stop := nativecontract.Stop{RequestID: uuid.NewString(), ExpectedBinding: b, Kind: "graceful", ExpectedProcessIdentityDigest: p.identity.ProcessIdentityDigest, ExpiresAt: f.server.now.Add(time.Minute)}
	f.server.queue(b, "stop", stop.RequestID, stop)
	return stop
}

// waitCompleted is a barrier on the server receiving the exact completion.
func (f *nativeHostFixture) waitCompleted(t *testing.T, in nativecontract.Input) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	for {
		select {
		case r := <-f.server.completed:
			if r.InputID != nil && *r.InputID == in.InputID {
				return
			}
		case <-ctx.Done():
			t.Fatal("exact turn completion was not reported")
		}
	}
}

func (f *nativeHostFixture) groups(t *testing.T) int {
	t.Helper()
	data, _, err := f.s.ledger.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	return len(data.Groups)
}

// Risk: a start refused after its claim but before fork (here the launch
// re-check finds consent withdrawn) keeps the account's ledger hold, the host
// entry and the server root, so every later start of that account is refused.
func TestNativeHostAbandonsStartRefusedBeforeFork(t *testing.T) {
	f := newNativeHostFixture(t, 1)
	local := f.local(0)
	f.server.revoked[local.Intent.RequestID] = true
	_, err := f.h.Start(t.Context(), local)
	var status *client.StatusError
	if !errors.As(err, &status) || status.Status != http.StatusForbidden || !strings.Contains(status.Message, string(nativecontract.ReasonPermissionRevoked)) {
		t.Fatal("withdrawn consent did not refuse the start for its own reason", err)
	}
	if f.launcher.opens != 0 {
		t.Fatal("refused start reached the provider")
	}
	f.server.mu.Lock()
	rejected := slices.Clone(f.server.rejected)
	claimed := f.server.sessions[local.Intent.RequestID].binding
	f.server.mu.Unlock()
	if len(rejected) != 1 || rejected[0].Reason == nil || *rejected[0].Reason != nativecontract.ReasonPermissionRevoked || !rejected[0].Binding.Equal(claimed) || rejected[0].Operation != "start" {
		t.Fatal("claimed server root was not settled by an exact abandonment", rejected)
	}
	f.h.mu.Lock()
	entries := len(f.h.entries)
	f.h.mu.Unlock()
	if entries != 0 || f.groups(t) != 0 {
		t.Fatal("refused start kept its host entry or ledger hold", entries, f.groups(t))
	}
	raw, err := f.s.state.Read("lead-runtime-"+local.Intent.RequestID+".json", 32768)
	if err != nil {
		t.Fatal(err)
	}
	if record, err := decodeLeadRecord(raw); err != nil || !record.Abandoned || record.Attempted {
		t.Fatal("refused start left a possible-fork record", record, err)
	}
	// The same account can be acquired again by a fresh person intent.
	f.start(t, 0)
}

// Risk: one session whose receipts are refused for good aborts the whole
// daemon poll, so another session's input and every stop (its own included)
// are not executed; map order only made the starvation intermittent.
func TestNativeHostPollIsolatesSessionWithRefusedReports(t *testing.T) {
	f := newNativeHostFixture(t, 2)
	faulted, faultedProvider := f.start(t, 0)
	healthy, healthyProvider := f.start(t, 1)
	f.server.mu.Lock()
	f.server.refused[faulted.OwnedSessionID] = true
	f.server.mu.Unlock()
	f.input(faulted)
	reached := f.input(healthy)
	var status *client.StatusError
	if err := f.h.Poll(t.Context()); !errors.As(err, &status) || status.Status != http.StatusForbidden {
		t.Fatal("refused receipts were not reported", err)
	}
	if !healthyProvider.received(reached.InputID) {
		t.Fatal("another session's refused receipts withheld this session's input")
	}
	// The faulted session's report outbox is refused for good: its later input
	// is not written (its outcome could not be reported), but its stop is.
	withheld := f.input(faulted)
	f.stop(faulted, faultedProvider)
	f.stop(healthy, healthyProvider)
	if err := f.h.Poll(t.Context()); !errors.As(err, &status) || status.Status != http.StatusForbidden {
		t.Fatal("refused exit receipt was not reported", err)
	}
	if faultedProvider.closed() != 1 || healthyProvider.closed() != 1 {
		t.Fatal("a person's stop was not executed", faultedProvider.closed(), healthyProvider.closed())
	}
	if _, err := f.h.Input(t.Context(), withheld); !errors.As(err, &status) || status.Status != http.StatusForbidden || faultedProvider.received(withheld.InputID) {
		t.Fatal("input was written while its receipts are refused", err)
	}
	// Only the healthy exit was reported; the unreported exit keeps its hold.
	if f.groups(t) != 1 {
		t.Fatal("ledger did not keep exactly the unreported owner", f.groups(t))
	}
}

// Risk: the session limits count lifetime totals: after 128 completed inputs a
// session refuses every input, and after 32 exited sessions the daemon refuses
// every start with host_full.
func TestNativeHostLimitsCountOnlyLiveWork(t *testing.T) {
	t.Run("inputs", func(t *testing.T) {
		f := newNativeHostFixture(t, 1)
		b, p := f.start(t, 0)
		for i := range 129 {
			in := f.input(b)
			if r, err := f.h.Input(t.Context(), in); err != nil || r.Receipt.State != "write_acknowledged" {
				t.Fatal("sequential input refused", i, r, err)
			}
			p.complete(in)
			f.waitCompleted(t, in)
		}
	})
	t.Run("sessions", func(t *testing.T) {
		f := newNativeHostFixture(t, 1)
		for i := range 33 {
			b, p := f.start(t, 0)
			if r, err := f.h.Stop(t.Context(), f.stop(b, p)); err != nil || r.Receipt.State != "exit_confirmed" {
				t.Fatal("session did not exit", i, r, err)
			}
		}
		// Exited owners are retained only as bounded successor sources.
		f.start(t, 0)
		f.h.mu.Lock()
		entries := len(f.h.entries)
		f.h.mu.Unlock()
		if entries != nativeRetainedExits+1 {
			t.Fatal("exited owners were not bounded", entries)
		}
	})
}

// nativeC0Recorder acknowledges writes through the transport's own observer,
// as the bridge's control_applied frame does.
type nativeC0Recorder struct {
	t   *nativeC0Transport
	ack bool
}

func (r *nativeC0Recorder) frame(kind, correlation, turn string) {
	raw, _ := json.Marshal(map[string]string{"kind": kind, "correlation_id": correlation, "native_turn_ref": turn})
	r.t.observe(raw)
}
func (r *nativeC0Recorder) Input(_ context.Context, id, _ string) error {
	if r.ack {
		r.frame("control_applied", id, "")
	}
	return nil
}
func (r *nativeC0Recorder) nativeControl(_ context.Context, id, _ string) error {
	r.frame("control_applied", id, "")
	return nil
}

// Risk: completed inputs and applied interrupts stay in the C0 transport's
// pending set, so a long session refuses every input after 128 turns; an
// unconfirmed input must still block the next one.
func TestNativeC0TransportCountsOnlyLiveWork(t *testing.T) {
	now := time.Now().UTC()
	owner := &nativeOwned{host: &NativeHost{s: &Supervisor{capacityNow: func() time.Time { return now }}}}
	b := nativecontract.Binding{TenantID: uuid.NewString(), ProjectID: uuid.NewString(), OwnedSessionID: uuid.NewString(), HarnessSessionID: uuid.NewString(), RuntimeEpoch: uuid.NewString(), DaemonID: "daemon", DaemonGeneration: "generation", BindingRevision: 2}
	transport := newNativeC0Transport(owner, nativecontract.LaunchSpec{Binding: b}, nativecontract.Capabilities{})
	recorder := &nativeC0Recorder{t: transport, ack: true}
	transport.writer = recorder
	input := func() nativecontract.Input {
		return nativecontract.Input{RequestID: uuid.NewString(), ExpectedBinding: b, InputID: uuid.NewString(), Mode: "now", Text: "turn", ExpiresAt: now.Add(time.Minute)}
	}
	for i := range 129 {
		in := input()
		if e, err := transport.Send(t.Context(), in); err != nil || e.Stage != "write_acknowledged" {
			t.Fatal("sequential input refused", i, e, err)
		}
		turn := uuid.NewString()
		recorder.frame("turn_started", in.InputID, turn)
		if i%2 == 0 {
			if e, err := transport.Interrupt(t.Context(), nativecontract.Interrupt{RequestID: uuid.NewString(), ExpectedBinding: b, ExpectedTurnRef: turn, ExpiresAt: now.Add(time.Minute)}); err != nil || e.Stage != "applied" {
				t.Fatal("exact interrupt refused", i, e, err)
			}
		}
		recorder.frame("turn_completed", in.InputID, turn)
		for _, want := range []string{"turn_started", "turn_completed"} {
			select {
			case event := <-transport.Events():
				if event.Receipt == nil || event.Receipt.State != want || *event.Receipt.InputID != in.InputID {
					t.Fatal("transport reported the wrong turn evidence", event)
				}
			default:
				t.Fatal("transport did not report the exact turn", want)
			}
		}
	}
	recorder.ack = false
	if _, err := transport.Send(t.Context(), input()); err == nil {
		t.Fatal("unacknowledged input reported delivery")
	}
	var refusal *nativecontract.ValidationError
	if _, err := transport.Send(t.Context(), input()); !errors.As(err, &refusal) || refusal.Reason != nativecontract.ReasonInputOutcomeUnconfirmed {
		t.Fatal("unconfirmed input stopped counting as live work", err)
	}
}
