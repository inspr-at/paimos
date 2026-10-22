// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	guuid "github.com/google/uuid"
	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/approvals"
	"github.com/inspr-at/paimos/internal/chat"
	"github.com/inspr-at/paimos/internal/client"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativereceipts"
	"github.com/inspr-at/paimos/internal/nativesessions"
	"github.com/inspr-at/paimos/internal/nativesessions/consent"
	"github.com/inspr-at/paimos/internal/nativesessions/receipts"
	"github.com/inspr-at/paimos/internal/nativesessions/stream"
	"github.com/inspr-at/paimos/internal/nativestream"
	"github.com/inspr-at/paimos/internal/ownedprocess"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type compositionStore struct {
	*Store
	t       *testing.T
	denials chan error
}

func (s compositionStore) Claim(ctx context.Context, d nativecontract.DaemonActor, in nativecontract.Claim) (nativecontract.ClaimResult, error) {
	out, err := s.Store.Claim(ctx, d, in)
	if err != nil {
		s.t.Logf("claim refusal: %T %v", err, err)
	}
	return out, err
}
func (s compositionStore) Report(ctx context.Context, d nativecontract.DaemonActor, in nativecontract.Receipt) (nativecontract.OperationResult, error) {
	out, err := s.Store.Report(ctx, d, in)
	if err != nil {
		s.t.Logf("receipt refusal: %T %v", err, err)
		if s.denials != nil {
			select {
			case s.denials <- err:
			default:
			}
		}
	}
	return out, err
}
func (s compositionStore) DaemonRequest(r *http.Request) (context.Context, nativecontract.DaemonActor, error) {
	ctx, d, err := s.Store.DaemonRequest(r)
	p, _ := tenant.PrincipalFrom(r.Context())
	if err != nil || ctx == nil || d.PrincipalID != p.ID || d.TenantID != p.TenantID || d.PrincipalKind != string(p.Kind) {
		s.t.Logf("daemon request identity mismatch: %v", err)
	}
	return ctx, d, err
}

type compositionAPI struct {
	agentd.API
	f      *startFixture
	ledger string
}

func (a *compositionAPI) Identity(context.Context) (string, string, error) {
	return a.f.agent.TenantID, a.f.agent.ID, nil
}
func (a *compositionAPI) LedgerView(context.Context) (agentsetup.View, int, error) {
	return agentsetup.View{ServerCapabilities: []string{agentsetup.LedgerCapability}}, 2, nil
}
func (a *compositionAPI) EnrollLedger(_ context.Context, g string) error { a.ledger = g; return nil }
func (*compositionAPI) SetLedgerGeneration(string)                       {}

type compositionAdapter struct{ agentd.Adapter }

func (compositionAdapter) Name() string                       { return agentd.Claude }
func (compositionAdapter) Probe(context.Context, string) bool { return true }

type compositionProfiles struct{ account, root string }

func (p compositionProfiles) Resolve(_ context.Context, q nativecontract.ProfileRequest) (nativecontract.LocalProfile, error) {
	return nativecontract.LocalProfile{AccountID: p.account, AccountHome: p.root, UserHome: p.root, WorkspaceRoot: p.root, ConversationRoot: p.root, ProfileBindingRevision: q.BindingRevision}, nil
}

type compositionProvider struct {
	written     chan nativecontract.Input
	unconfirmed bool
	mu          sync.Mutex
	identity    nativecontract.NativeIdentity
	binding     nativecontract.Binding
	events      chan nativecontract.ProviderEvent
	input       nativecontract.Input
	closed      bool
	sequence    uint64
	confirmed   bool
	closes      int
	now         time.Time
	age         time.Duration
	opens       int
}

func (p *compositionProvider) Open(_ context.Context, s nativecontract.LaunchSpec) (nativecontract.NativeTransport, error) {
	p.binding = s.Binding
	p.identity = nativecontract.NativeIdentity{Provider: "claude", NativeSessionRef: guuid.NewString(), RuntimeEpoch: s.Binding.RuntimeEpoch, PID: 12345, StartedAt: p.now, ProcessIdentityDigest: strings.Repeat("f", 64)}
	if p.age > 0 {
		p.opens++
		p.confirmed, p.closed = false, false
		p.identity.StartedAt = p.now.Add(-p.age + time.Duration(p.opens)*time.Second)
		sum := sha256.Sum256([]byte(s.Binding.RuntimeEpoch))
		p.identity.ProcessIdentityDigest = hex.EncodeToString(sum[:])
	}
	if s.ResumeRef != "" {
		p.identity.NativeSessionRef = s.ResumeRef
	}
	return p, nil
}
func (p *compositionProvider) Ownership() (ownedprocess.Identity, error) {
	return ownedprocess.Identity{DaemonID: p.binding.DaemonID, Generation: p.binding.DaemonGeneration, ProcessID: p.binding.RuntimeEpoch, RootPID: p.identity.PID, GroupID: p.identity.PID, StartedAt: p.now}, nil
}
func (p *compositionProvider) Send(_ context.Context, in nativecontract.Input) (nativecontract.NativeEvidence, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.input = in
	p.sequence++
	if p.written != nil {
		p.written <- in
	}
	if p.unconfirmed {
		return nativecontract.NativeEvidence{}, errors.New("fixture provider acknowledgement lost")
	}
	return nativecontract.NativeEvidence{Stage: "write_acknowledged", InputID: in.InputID, RequestID: in.RequestID, SourceSequence: p.sequence, ObservedAt: p.now}, nil
}
func (p *compositionProvider) Interrupt(_ context.Context, in nativecontract.Interrupt) (nativecontract.NativeEvidence, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sequence++
	return nativecontract.NativeEvidence{Stage: "applied", RequestID: in.RequestID, TurnRef: in.ExpectedTurnRef, SourceSequence: p.sequence, ObservedAt: p.now}, nil
}
func (*compositionProvider) Decide(context.Context, nativecontract.ApprovalDecision) (nativecontract.NativeEvidence, error) {
	return nativecontract.NativeEvidence{}, errors.New("not qualified")
}
func (p *compositionProvider) Close(context.Context) (nativecontract.ExitEvidence, error) {
	p.mu.Lock()
	p.closed = true
	p.closes++
	confirmed := p.confirmed
	p.mu.Unlock()
	return nativecontract.ExitEvidence{ProcessIdentityDigest: p.identity.ProcessIdentityDigest, RootExited: true, GroupAbsent: confirmed, ObservedAt: p.now}, nil
}
func (p *compositionProvider) ObserveExit(context.Context) (nativecontract.ExitEvidence, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.confirmed {
		return nativecontract.ExitEvidence{}, agentd.ErrProcessesUnconfirmed
	}
	return nativecontract.ExitEvidence{ProcessIdentityDigest: p.identity.ProcessIdentityDigest, RootExited: true, GroupAbsent: true, ObservedAt: p.now}, nil
}
func (p *compositionProvider) Wait(ctx context.Context) (nativecontract.ExitEvidence, error) {
	return p.ObserveExit(ctx)
}
func (p *compositionProvider) Capabilities() nativecontract.Capabilities {
	return nativecontract.Capabilities{Steer: "unsupported", Quota: "unsupported"}
}
func (p *compositionProvider) Identity() nativecontract.NativeIdentity     { return p.identity }
func (p *compositionProvider) Events() <-chan nativecontract.ProviderEvent { return p.events }
func (p *compositionProvider) turn(state, turn string, seq uint64) {
	p.mu.Lock()
	in := p.input
	p.sequence = seq
	p.mu.Unlock()
	p.events <- nativecontract.ProviderEvent{Identity: p.identity, SourceSequence: seq, Receipt: &nativecontract.Receipt{RequestID: in.RequestID, InputID: &in.InputID, TurnRef: &turn, State: state}}
}

// compositionHookProfiles runs before once inside the first resolve: after the
// start claim and before the daemon's final launch re-check.
type compositionHookProfiles struct {
	compositionProfiles
	once   sync.Once
	before func()
}

func (p *compositionHookProfiles) Resolve(ctx context.Context, q nativecontract.ProfileRequest) (nativecontract.LocalProfile, error) {
	if p.before != nil {
		p.once.Do(p.before)
	}
	return p.compositionProfiles.Resolve(ctx, q)
}

// productionComposition is the installed server module and paired daemon host
// around a fake owned provider; channels establish each server observation.
type productionComposition struct {
	f                                *startFixture
	mux                              *http.ServeMux
	config                           *agentd.NativeSessionConfig
	provider                         *compositionProvider
	profiles                         *compositionHookProfiles
	root                             string
	denials                          chan error
	finalDone, turnDone, turnStarted chan struct{}
	exitDone                         chan struct{}
}

func (c *productionComposition) personCall(t *testing.T, path string, body, out any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", path, bytes.NewReader(raw)).WithContext(c.f.personCtx(t))
	w := httptest.NewRecorder()
	c.mux.ServeHTTP(w, r)
	if w.Code != 200 && w.Code != 201 {
		t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
	}
	if out != nil {
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			t.Fatal(err)
		}
	}
}

// approve records a separate actual person decision, through approvals, on a
// proposal with the original session consent's facts; it returns its ID.
func (c *productionComposition) approve(t *testing.T, scope string) string {
	t.Helper()
	f := c.f
	var facts consent.Facts
	var proposer tenant.Principal
	proposer.TenantID = f.person.TenantID
	proposer.Kind = tenant.Agent
	var rawFacts []byte
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT r.native_consent,k.principal_id::text,k.id::text,k.scopes FROM approval_requests r JOIN native_session_consents c ON c.tenant_id=r.tenant_id AND c.grant_id=r.id JOIN agent_keys k ON k.tenant_id=c.tenant_id AND k.principal_id=c.agent_principal_id AND k.prefix='nativeproposal' WHERE r.id=$1`, f.intent.PolicyID).Scan(&rawFacts, &proposer.ID, &proposer.KeyID, &proposer.Scopes); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rawFacts, &facts); err != nil {
		t.Fatal(err)
	}
	proposer.AuthKeyID = proposer.KeyID
	proposer.KeyCreatorID = f.person.ID
	raw, _ := json.Marshal(map[string]any{"scope": scope, "resource_kind": "node", "resource_id": f.project, "rationale": "Separate person decision", "expires_at": f.now.Add(time.Hour), "native_consent": facts})
	r := httptest.NewRequest("POST", "/api/approvals", bytes.NewReader(raw)).WithContext(tenant.WithPrincipal(t.Context(), proposer))
	r.Header.Set("Authorization", "Bearer aeon_nativeproposal_"+strings.Repeat("p", 64))
	w := httptest.NewRecorder()
	c.mux.ServeHTTP(w, r)
	if w.Code != 201 {
		t.Fatalf("%s approval %d %s", scope, w.Code, w.Body.String())
	}
	var approval approvals.Approval
	_ = json.Unmarshal(w.Body.Bytes(), &approval)
	c.personCall(t, "/api/approvals/"+approval.ID+"/decision", map[string]string{"decision": "approved"}, nil)
	return approval.ID
}

type compositionOptions struct {
	disableRetention bool
	configure        func(*startFixture, *consent.Authority, *agentd.NativeSessionConfig)
	notifications    http.HandlerFunc
	afterReceipt     func(nativecontract.Receipt)
}

func composeProduction(t *testing.T, options ...compositionOptions) *productionComposition {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var option compositionOptions
	if len(options) > 0 {
		option = options[0]
	}
	f, a := newPairedStartFixture(t, consent.Facts{WorkspaceRoot: root, RetainFinals: !option.disableRetention})
	if option.configure != nil {
		option.configure(f, &a, nil)
	}
	a.Capabilities.Input = true
	a.Capabilities.ChatSupported = true
	a.Capabilities.InterruptSupported = true
	a.Capabilities.Interrupt = true
	a.Capabilities.Stop = true
	a.Capabilities.CorrelatedReceipts = true
	chatModule := chat.New(f.d.App, chat.Options{Enabled: true})
	repo, err := receipts.New(f.d.App, receipts.CurrentAuthority{Consent: a}, chatModule, func() time.Time { return f.now })
	if err != nil {
		t.Fatal(err)
	}
	projector, err := nativestream.NewProjector(nativestream.ExportPolicy{Text: func(_ context.Context, _ nativecontract.Binding, text string) (string, error) { return text, nil }})
	if err != nil {
		t.Fatal(err)
	}
	relay, err := stream.NewRelay(projector, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.s.authority = a
	f.s.observer = PrivateObserver{}
	f.s.BindProduction(a, repo, relay)
	denials := make(chan error, 8)
	module, err := nativesessions.New(nativecontract.Services{Store: compositionStore{Store: f.s, t: t, denials: denials}, Frames: relay, Finals: f.s, Qualified: true})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	module.Mount(mux)
	chatModule.Mount(mux)
	approvals.New(f.d.App).Mount(mux)
	if option.notifications != nil {
		mux.HandleFunc("GET /api/projects/{projectId}/harness-sessions/{sessionId}/notifications", option.notifications)
	}
	finalDone, turnDone, turnStarted := make(chan struct{}, 2), make(chan struct{}, 2), make(chan struct{}, 4)
	exitDone := make(chan struct{}, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := []byte(nil)
		if r.Body != nil {
			var body bytes.Buffer
			_, _ = body.ReadFrom(r.Body)
			raw = body.Bytes()
			r.Body = ioNop(raw)
		}
		r = r.WithContext(tenant.WithPrincipal(r.Context(), f.agent))
		if strings.HasPrefix(r.URL.Path, "/api/agentd/native-sessions/") {
			if _, _, err := f.s.DaemonRequest(r); err != nil {
				t.Logf("private daemon admission failed: %T %v", err, err)
			}
		}
		mux.ServeHTTP(w, r)
		if r.URL.Path == "/api/agentd/native-sessions/finals" {
			finalDone <- struct{}{}
		}
		if r.URL.Path == "/api/agentd/native-sessions/receipts" {
			var v nativecontract.Receipt
			_ = json.Unmarshal(raw, &v)
			if option.afterReceipt != nil {
				option.afterReceipt(v)
			}
			if v.State == "turn_started" {
				turnStarted <- struct{}{}
			}
			if v.State == "turn_completed" {
				turnDone <- struct{}{}
			}
			if v.Operation == "start" && v.State == "exit_confirmed" {
				exitDone <- struct{}{}
			}
		}
	}))
	t.Cleanup(server.Close)
	remote := agentd.NewRemote(server.URL, "fixture-runtime")
	profiles := &compositionHookProfiles{compositionProfiles: compositionProfiles{f.intent.AccountID, root}}
	config := agentd.NativeProduction(remote, f.intent.ComputerID, f.person.ID, profiles, a.Capabilities)
	if option.configure != nil {
		option.configure(f, &a, config)
	}
	provider := &compositionProvider{now: f.now, events: make(chan nativecontract.ProviderEvent, 16)}
	config.Launcher = provider
	api := &compositionAPI{f: f}
	state := filepath.Join(root, "state")
	if err = os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	supervisor, err := agentd.NewSupervisor(t.Context(), agentd.Config{API: api, StateRoot: state, Workspace: root, DaemonID: f.daemon.DaemonID, Accounts: []agentd.EnrolledAccount{{ID: f.intent.AccountID, Key: "real-own-account", Harness: "claude"}}, Adapters: []agentd.Adapter{compositionAdapter{}}, NativeSessions: config, Now: func() time.Time { return f.now }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = supervisor.Close(context.Background()) })
	if err = supervisor.EnableLedger(t.Context(), agentd.LedgerConfig{Root: root, Path: filepath.Join(root, "ledger"), Label: "cm.aeon.agentd.composition", Origin: "https://native.test"}); err != nil {
		t.Fatal(err)
	}
	f.daemon.DaemonGeneration = supervisor.Generation()
	f.daemon.LedgerGeneration = api.ledger
	f.seed(t, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET last_daemon_generation=$2 WHERE id=$1`, f.intent.AccountID, supervisor.Generation()); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE agent_pairing_computers SET ledger_generation=$2 WHERE id=$1`, f.intent.ComputerID, api.ledger)
		return err
	})
	return &productionComposition{f: f, mux: mux, config: config, provider: provider, profiles: profiles, root: root, denials: denials, finalDone: finalDone, turnDone: turnDone, turnStarted: turnStarted, exitDone: exitDone}
}

// Risk: the installed production start/command/receipt/final paths borrow the
// daemon's person authority, infer turn/final evidence, or release on detach.
// The real person approvals, issued pairing key and transaction authorities
// surround a fake owned provider; channels establish each observation.
func TestProductionCompositionStartsDeliversAndConfirmsExit(t *testing.T) {
	t.Run("live inbox priority, evidence and offline fallback", exerciseLiveInbox)
	c := composeProduction(t)
	f, config, provider, root := c.f, c.config, c.provider, c.root
	finalDone, turnDone, turnStarted, denials := c.finalDone, c.turnDone, c.turnStarted, c.denials
	started := f.begin(t)
	result, err := config.Host.Start(t.Context(), nativecontract.LocalStart{Intent: f.intent, Cwd: root, AccountKey: "real-own-account"})
	if err != nil {
		t.Fatal("start", err)
	}
	if result.Session == nil || result.Session.State != "running" || result.Receipt.OperationID != started.Receipt.OperationID {
		t.Fatal("start did not become running", result)
	}
	b := *result.Session.CurrentBinding
	personCall := func(path string, body, out any) {
		t.Helper()
		c.personCall(t, path, body, out)
	}
	var role chat.Role
	personCall("/api/projects/"+f.project+"/chat-roles", chat.RoleCreate{Kind: "lead", SlotKey: "lead"}, &role)
	var thread chat.Thread
	personCall("/api/projects/"+f.project+"/chat-threads/resolve", chat.ThreadResolve{RoleID: role.ID}, &thread)
	personCall("/api/chat-threads/"+thread.ID+"/binding", chat.BindingWrite{SessionID: b.HarnessSessionID, ExpectedEpoch: "0"}, &thread)
	// Retention is a distinct actual person decision, made through approvals.
	c.approve(t, consent.RetentionScope)
	in := nativecontract.Input{RequestID: guuid.NewString(), InputID: guuid.NewString(), ExpectedBinding: b, Mode: "now", Text: "person input stays exact", ExpiresAt: f.now.Add(time.Minute)}
	queued, err := f.s.Mutate(f.personCtx(t), f.actor, nativecontract.Command{Kind: "input", Input: &in})
	if err != nil {
		t.Fatal("queue", err)
	}
	delivered, err := config.Host.Input(t.Context(), in)
	if err != nil || delivered.Receipt.State != "write_acknowledged" {
		t.Fatal("input", delivered, err)
	}
	if provider.input.Text != in.Text {
		t.Fatal("provider did not receive exact person text")
	}
	turn := guuid.NewString()
	provider.turn("turn_started", turn, 2)
	select {
	case <-turnStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("exact turn did not start")
	}
	// Receipt completion and final are separate explicit observations.
	provider.turn("turn_completed", turn, 3)
	select {
	case <-turnDone:
	case <-time.After(10 * time.Second):
		t.Fatal("completion report did not arrive")
	}
	final := nativecontract.FinalPublish{RequestID: guuid.NewString(), FinalID: guuid.NewString(), ExpectedBinding: b, InputID: in.InputID, TurnRef: turn, Text: "explicit provider final"}
	provider.events <- nativecontract.ProviderEvent{Identity: provider.identity, Final: &final}
	select {
	case <-finalDone:
	case <-time.After(10 * time.Second):
		t.Fatal("explicit final did not arrive")
	}
	var count int
	if err = f.d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM native_session_finals WHERE tenant_id=$1 AND final_id=$2 AND state='persisted'`, f.person.TenantID, final.FinalID).Scan(&count); err != nil || count != 1 {
		t.Fatal("final was not persisted", count, err)
	}
	replayed, err := f.s.Mutate(f.personCtx(t), f.actor, nativecontract.Command{Kind: "input", Input: &in})
	if err != nil || replayed.Receipt.State != "turn_completed" {
		t.Fatal("completed retry did not preserve outcome", replayed, err)
	}
	hold := in
	hold.RequestID = guuid.NewString()
	hold.InputID = guuid.NewString()
	hold.Text = "hold for an exact interrupt"
	if _, err = f.s.Mutate(f.personCtx(t), f.actor, nativecontract.Command{Kind: "input", Input: &hold}); err != nil {
		t.Fatal(err)
	}
	if _, err = config.Host.Input(t.Context(), hold); err != nil {
		t.Fatal(err)
	}
	heldTurn := guuid.NewString()
	provider.turn("turn_started", heldTurn, 5)
	select {
	case <-turnStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("interrupt turn did not start")
	}
	interrupt := nativecontract.Interrupt{RequestID: guuid.NewString(), ExpectedBinding: b, ExpectedTurnRef: heldTurn, ExpiresAt: f.now.Add(time.Minute)}
	if _, err = f.s.Mutate(f.personCtx(t), f.actor, nativecontract.Command{Kind: "interrupt", Interrupt: &interrupt}); err != nil {
		t.Fatal(err)
	}
	interrupted, err := config.Host.Interrupt(t.Context(), interrupt)
	if err != nil || interrupted.Receipt.State != "applied" {
		t.Fatal("exact interrupt", interrupted, err)
	}
	if _, err = config.Host.Detach(t.Context(), nativecontract.Detach{RequestID: guuid.NewString(), ExpectedBinding: b, ViewerID: guuid.NewString()}); err == nil {
		t.Fatal("attach is not installed by this slice")
	}
	if provider.closed {
		t.Fatal("detach closed owned writer")
	}
	stop := nativecontract.Stop{RequestID: guuid.NewString(), ExpectedBinding: b, Kind: "graceful", ExpectedProcessIdentityDigest: provider.identity.ProcessIdentityDigest, ExpiresAt: f.now.Add(time.Minute)}
	if _, err = f.s.Mutate(f.personCtx(t), f.actor, nativecontract.Command{Kind: "stop", Stop: &stop}); err != nil {
		t.Fatal("queue stop", err)
	}
	stopped, err := config.Host.Stop(t.Context(), stop)
	if err == nil || stopped.Receipt.State != "uncertain" {
		t.Fatal("unconfirmed group exit reported success", stopped, err)
	}
	var held bool
	if err = f.d.Admin.QueryRow(t.Context(), `SELECT released_at IS NULL FROM native_session_roots WHERE owned_session_id=$1`, b.OwnedSessionID).Scan(&held); err != nil || !held {
		t.Fatal("uncertain exit released root", held, err)
	}
	provider.mu.Lock()
	provider.confirmed = true
	provider.mu.Unlock()
	if err = config.Poll(t.Context()); err != nil {
		t.Fatal("exit reconciliation", err)
	}
	if provider.closes != 1 {
		t.Fatal("exit reconciliation repeated native close")
	}

	if err = f.d.Admin.QueryRow(t.Context(), `SELECT released_at IS NULL FROM native_session_roots WHERE owned_session_id=$1`, b.OwnedSessionID).Scan(&held); err != nil || held {
		t.Fatal("confirmed root still held", held, err)
	}
	// A fresh PERSON intent and exact retained source permit one successor.
	successorIntent := f.intent
	successorIntent.RequestID = guuid.NewString()
	successorIntent.ResumeFromOwnedSessionID = &b.OwnedSessionID
	if _, err = f.s.Start(f.personCtx(t), f.actor, successorIntent); err != nil {
		t.Fatal("successor intent", err)
	}
	successor := &compositionProvider{now: f.now, events: make(chan nativecontract.ProviderEvent, 16)}
	config.Launcher = successor
	resume := provider.identity.NativeSessionRef
	local := nativecontract.LocalStart{Intent: successorIntent, Cwd: root, AccountKey: "real-own-account", NativeResumeRef: &resume, AfterExit: &nativecontract.ExitBarrier{PID: int64(provider.identity.PID), ProcessStartedAt: provider.identity.StartedAt, ProcessIdentityDigest: provider.identity.ProcessIdentityDigest, Deadline: f.now.Add(time.Minute)}}
	next, err := config.Host.Start(t.Context(), local)
	if err != nil || next.Session == nil || next.Session.State != "running" {
		t.Fatal("retained-source handover", next, err)
	}
	nextBinding := *next.Session.CurrentBinding
	personCall("/api/chat-threads/"+thread.ID+"/binding", chat.BindingWrite{SessionID: nextBinding.HarnessSessionID, ExpectedEpoch: thread.BindingEpoch}, &thread)
	active := hold
	active.ExpectedBinding = nextBinding
	active.RequestID = guuid.NewString()
	active.InputID = guuid.NewString()
	if _, err = f.s.Mutate(f.personCtx(t), f.actor, nativecontract.Command{Kind: "input", Input: &active}); err != nil {
		t.Fatal(err)
	}
	if _, err = config.Host.Input(t.Context(), active); err != nil {
		t.Fatal(err)
	}
	personCall("/api/approvals/"+f.intent.PolicyID+"/revoke", nil, nil)
	denied := active
	denied.RequestID = guuid.NewString()
	denied.InputID = guuid.NewString()
	if _, err = f.s.Mutate(f.personCtx(t), f.actor, nativecontract.Command{Kind: "input", Input: &denied}); err == nil {
		t.Fatal("withdrawn consent accepted next input")
	} else {
		var v *nativecontract.ValidationError
		if !errors.As(err, &v) || v.Reason != nativecontract.ReasonPermissionRevoked {
			t.Fatal("wrong consent refusal", err)
		}
	}
	successor.turn("turn_started", guuid.NewString(), 2)
	select {
	case err := <-denials:
		var v *nativereceipts.Error
		if !errors.As(err, &v) || v.Reason != nativecontract.ReasonPermissionRevoked {
			t.Fatal("wrong receipt refusal", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("withdrawn consent accepted receipt")
	}
	if err = f.d.Admin.QueryRow(t.Context(), `SELECT released_at IS NULL FROM native_session_roots WHERE owned_session_id=$1`, nextBinding.OwnedSessionID).Scan(&held); err != nil || !held {
		t.Fatal("withdrawal erased native ownership", held, err)
	}
	if queued.Receipt.State != "accepted" {
		t.Fatal("queue claimed native delivery")
	}
}

// Risk: a start refused after its claim but before fork (here the person
// withdraws consent before the daemon's final launch re-check) keeps the
// server root, the local ledger hold and the host slot, so the same account
// can never start again. Abandonment itself must not need that consent.
func TestProductionCompositionAbandonsStartRefusedBeforeFork(t *testing.T) {
	c := composeProduction(t)
	f := c.f
	started := f.begin(t)
	c.profiles.before = func() { c.personCall(t, "/api/approvals/"+f.intent.PolicyID+"/revoke", nil, nil) }
	_, err := c.config.Host.Start(t.Context(), nativecontract.LocalStart{Intent: f.intent, Cwd: c.root, AccountKey: "real-own-account"})
	var status *client.StatusError
	if !errors.As(err, &status) || status.Status != http.StatusForbidden || !strings.Contains(status.Message, string(nativecontract.ReasonPermissionRevoked)) {
		t.Fatal("withdrawn consent did not refuse the launch for its own reason", err)
	}
	if c.provider.binding.RuntimeEpoch != "" {
		t.Fatal("refused start reached the provider")
	}
	var released, stopped bool
	var state, reason string
	if err = f.d.Admin.QueryRow(t.Context(), `SELECT r.released_at IS NOT NULL,h.stopped_at IS NOT NULL,b.view->>'state',coalesce(b.view->>'reason','') FROM native_session_roots r
 JOIN native_session_bindings b ON b.id=r.owned_session_id JOIN harness_sessions h ON h.id=b.current_harness_session_id WHERE r.owned_session_id=$1`, started.Session.OwnedSessionID).Scan(&released, &stopped, &state, &reason); err != nil {
		t.Fatal(err)
	}
	if !released || !stopped || state != "failed" || reason != string(nativecontract.ReasonPermissionRevoked) {
		t.Fatal("claimed root was not settled as verified no-fork abandonment", released, stopped, state, reason)
	}
	ledger, err := agentsetup.OpenSharedLedger(filepath.Join(c.root, "ledger"), false)
	if err != nil {
		t.Fatal(err)
	}
	data, _, err := ledger.Snapshot()
	_ = ledger.Close()
	if err != nil || len(data.Groups) != 0 {
		t.Fatal("refused start kept the account's ledger hold", data.Groups, err)
	}
	// A fresh person consent and intent start the same account again.
	f.intent.PolicyID = c.approve(t, consent.SessionScope)
	f.intent.RequestID = guuid.NewString()
	next := f.begin(t)
	result, err := c.config.Host.Start(t.Context(), nativecontract.LocalStart{Intent: f.intent, Cwd: c.root, AccountKey: "real-own-account"})
	if err != nil || result.Session == nil || result.Session.State != "running" || result.Receipt.OperationID != next.Receipt.OperationID {
		t.Fatal("the account could not start again after abandonment", result, err)
	}
}

// Risk: once consent is withdrawn and the session's reports are refused for
// good, the production command listing, command claim, stop authorization and
// exit receipt all require the revoked grant, so the exact owner can never stop
// the faulted session and its native process, root and ledger hold stay.
func TestProductionCompositionOwnerStopsFaultedSessionAfterWithdrawal(t *testing.T) {
	c := composeProduction(t)
	f, config, provider := c.f, c.config, c.provider
	f.begin(t)
	result, err := config.Host.Start(t.Context(), nativecontract.LocalStart{Intent: f.intent, Cwd: c.root, AccountKey: "real-own-account"})
	if err != nil || result.Session == nil || result.Session.State != "running" {
		t.Fatal("start", result, err)
	}
	b := *result.Session.CurrentBinding
	var role chat.Role
	c.personCall(t, "/api/projects/"+f.project+"/chat-roles", chat.RoleCreate{Kind: "lead", SlotKey: "lead"}, &role)
	var thread chat.Thread
	c.personCall(t, "/api/projects/"+f.project+"/chat-threads/resolve", chat.ThreadResolve{RoleID: role.ID}, &thread)
	c.personCall(t, "/api/chat-threads/"+thread.ID+"/binding", chat.BindingWrite{SessionID: b.HarnessSessionID, ExpectedEpoch: "0"}, &thread)
	c.approve(t, consent.RetentionScope)
	in := nativecontract.Input{RequestID: guuid.NewString(), InputID: guuid.NewString(), ExpectedBinding: b, Mode: "now", Text: "work before withdrawal", ExpiresAt: f.now.Add(time.Minute)}
	if _, err = f.s.Mutate(f.personCtx(t), f.actor, nativecontract.Command{Kind: "input", Input: &in}); err != nil {
		t.Fatal("queue", err)
	}
	if delivered, err := config.Host.Input(t.Context(), in); err != nil || delivered.Receipt.State != "write_acknowledged" {
		t.Fatal("input", delivered, err)
	}
	// A real person revocation; the next provider report is refused for good.
	c.personCall(t, "/api/approvals/"+f.intent.PolicyID+"/revoke", nil, nil)
	provider.turn("turn_started", guuid.NewString(), 2)
	select {
	case err := <-c.denials:
		var v *nativereceipts.Error
		if !errors.As(err, &v) || v.Reason != nativecontract.ReasonPermissionRevoked {
			t.Fatal("wrong receipt refusal", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("withdrawn consent accepted receipt")
	}
	var status *client.StatusError
	if err = config.Poll(t.Context()); !errors.As(err, &status) || status.Status != http.StatusForbidden {
		t.Fatal("the refused report did not fault the session", err)
	}
	// Stop only: other new work stays refused for the withdrawn session.
	interrupt := nativecontract.Interrupt{RequestID: guuid.NewString(), ExpectedBinding: b, ExpectedTurnRef: guuid.NewString(), ExpiresAt: f.now.Add(time.Minute)}
	var refusal *nativecontract.ValidationError
	if _, err = f.s.Mutate(f.personCtx(t), f.actor, nativecontract.Command{Kind: "interrupt", Interrupt: &interrupt}); !errors.As(err, &refusal) || refusal.Reason != nativecontract.ReasonPermissionRevoked {
		t.Fatal("withdrawn consent accepted an interrupt", err)
	}
	stop := nativecontract.Stop{RequestID: guuid.NewString(), ExpectedBinding: b, Kind: "graceful", ExpectedProcessIdentityDigest: provider.identity.ProcessIdentityDigest, ExpiresAt: f.now.Add(time.Minute)}
	queued, err := f.s.Mutate(f.personCtx(t), f.actor, nativecontract.Command{Kind: "stop", Stop: &stop})
	if err != nil {
		t.Fatal("the exact owner could not stop after withdrawal", err)
	}
	provider.mu.Lock()
	provider.confirmed = true
	provider.mu.Unlock()
	// The faulted owner's poll lists, claims and executes only the stop; the
	// refused input report cannot hold back the stop's own exit receipt.
	if err = config.Poll(t.Context()); err != nil {
		t.Fatal("faulted poll did not execute the stop", err)
	}
	if provider.closes != 1 {
		t.Fatal("stop was not executed exactly once", provider.closes)
	}
	var stopState, inputState, viewState string
	var released, stopped bool
	if err = f.d.Admin.QueryRow(t.Context(), `SELECT (SELECT receipt->>'state' FROM native_session_receipts WHERE operation_id=$2),
 (SELECT receipt->>'state' FROM native_session_receipts WHERE owned_session_id=$1 AND receipt->>'operation'='input'),
 b.view->>'state',r.released_at IS NOT NULL,h.stopped_at IS NOT NULL FROM native_session_bindings b
 JOIN native_session_roots r ON r.owned_session_id=b.id JOIN harness_sessions h ON h.id=b.current_harness_session_id WHERE b.id=$1`, b.OwnedSessionID, queued.Receipt.OperationID).Scan(&stopState, &inputState, &viewState, &released, &stopped); err != nil {
		t.Fatal(err)
	}
	if stopState != "exit_confirmed" || viewState != "stopped" || !released || !stopped {
		t.Fatal("the confirmed exit did not settle the faulted session", stopState, viewState, released, stopped)
	}
	if inputState != "write_acknowledged" {
		t.Fatal("withdrawn consent accepted an input outcome", inputState)
	}
	ledger, err := agentsetup.OpenSharedLedger(filepath.Join(c.root, "ledger"), false)
	if err != nil {
		t.Fatal(err)
	}
	data, _, err := ledger.Snapshot()
	_ = ledger.Close()
	if err != nil || len(data.Groups) != 0 {
		t.Fatal("the stopped session kept its ledger hold", data.Groups, err)
	}
}

// Risk: a verified no-fork abandonment releases the root's slot but keeps its
// quota holds and its period-usage estimate, so a refused launch that never
// opened a provider exhausts the quota and the account cannot start again.
func TestProductionCompositionAbandonedStartReturnsItsQuota(t *testing.T) {
	for _, unit := range []string{"window", "runs", "requests", "tokens", "percent"} {
		t.Run(unit, func(t *testing.T) {
			c := composeProduction(t)
			f := c.f
			// Quota for exactly one launch.
			f.seed(t, func(tx pgx.Tx) error {
				if unit == "window" {
					_, err := tx.Exec(t.Context(), `INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance,pace_model)
 VALUES($1,$2,$3::timestamptz-interval '1 hour',$3::timestamptz+interval '1 hour','requests',1,'unrestricted')`, f.person.TenantID, f.intent.AccountID, f.now)
					return err
				}
				amount := int64(1)
				if unit == "tokens" {
					amount = 100_000
				}
				_, err := tx.Exec(t.Context(), `INSERT INTO account_limit_rules(tenant_id,account_id,amount,unit,period,created_by) VALUES($1,$2,$3,$4,'day',$5)`, f.person.TenantID, f.intent.AccountID, amount, unit, f.person.ID)
				return err
			})
			reserved := func() int64 {
				t.Helper()
				var held int64
				if err := f.d.Admin.QueryRow(t.Context(), `SELECT coalesce(sum(reserved),0)::bigint FROM account_allowance_windows WHERE account_id=$1`, f.intent.AccountID).Scan(&held); err != nil {
					t.Fatal(err)
				}
				return held
			}
			f.begin(t)
			c.profiles.before = func() {
				if reserved() < 1 {
					t.Error("the claim did not reserve its quota")
				}
				c.personCall(t, "/api/approvals/"+f.intent.PolicyID+"/revoke", nil, nil)
			}
			if _, err := c.config.Host.Start(t.Context(), nativecontract.LocalStart{Intent: f.intent, Cwd: c.root, AccountKey: "real-own-account"}); err == nil {
				t.Fatal("withdrawn consent did not refuse the launch")
			}
			if c.provider.binding.RuntimeEpoch != "" {
				t.Fatal("refused start reached the provider")
			}
			var state string
			if err := f.d.Admin.QueryRow(t.Context(), `SELECT hold_state FROM native_session_roots WHERE account_id=$1 AND released_at IS NOT NULL`, f.intent.AccountID).Scan(&state); err != nil || state != "abandoned" {
				t.Fatal("refused start was not settled as abandoned", state, err)
			}
			if held := reserved(); held != 0 {
				t.Fatal("abandonment kept quota holds", held)
			}
			// The retry on the same account fits the same single-launch quota.
			f.intent.PolicyID = c.approve(t, consent.SessionScope)
			f.intent.RequestID = guuid.NewString()
			next := f.begin(t)
			result, err := c.config.Host.Start(t.Context(), nativecontract.LocalStart{Intent: f.intent, Cwd: c.root, AccountKey: "real-own-account"})
			if err != nil || result.Session == nil || result.Session.State != "running" || result.Receipt.OperationID != next.Receipt.OperationID {
				t.Fatalf("the account's %s quota stayed charged by a launch that never opened a provider: %v", unit, err)
			}
			if held := reserved(); held < 1 {
				t.Fatal("the retry did not hold its own quota", held)
			}
		})
	}
}

func ioNop(raw []byte) io.ReadCloser { return io.NopCloser(bytes.NewReader(raw)) }
