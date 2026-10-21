// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"bytes"
	"context"
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
	"github.com/inspr-at/paimos/internal/nativecontract"
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
	t *testing.T
}

func (s compositionStore) Claim(ctx context.Context, d nativecontract.DaemonActor, in nativecontract.Claim) (nativecontract.ClaimResult, error) {
	out, err := s.Store.Claim(ctx, d, in)
	if err != nil {
		s.t.Logf("claim refusal: %T %v", err, err)
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
	mu       sync.Mutex
	identity nativecontract.NativeIdentity
	binding  nativecontract.Binding
	events   chan nativecontract.ProviderEvent
	input    nativecontract.Input
	closed   bool
	now      time.Time
}

func (p *compositionProvider) Open(_ context.Context, s nativecontract.LaunchSpec) (nativecontract.NativeTransport, error) {
	p.binding = s.Binding
	p.identity = nativecontract.NativeIdentity{Provider: "claude", NativeSessionRef: guuid.NewString(), RuntimeEpoch: s.Binding.RuntimeEpoch, PID: 12345, StartedAt: p.now, ProcessIdentityDigest: strings.Repeat("f", 64)}
	return p, nil
}
func (p *compositionProvider) Ownership() (ownedprocess.Identity, error) {
	return ownedprocess.Identity{DaemonID: p.binding.DaemonID, Generation: p.binding.DaemonGeneration, ProcessID: p.binding.RuntimeEpoch, RootPID: p.identity.PID, GroupID: p.identity.PID, StartedAt: p.now}, nil
}
func (p *compositionProvider) Send(_ context.Context, in nativecontract.Input) (nativecontract.NativeEvidence, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.input = in
	return nativecontract.NativeEvidence{Stage: "write_acknowledged", InputID: in.InputID, RequestID: in.RequestID, SourceSequence: 1, ObservedAt: p.now}, nil
}
func (p *compositionProvider) Interrupt(_ context.Context, in nativecontract.Interrupt) (nativecontract.NativeEvidence, error) {
	return nativecontract.NativeEvidence{Stage: "applied", RequestID: in.RequestID, TurnRef: in.ExpectedTurnRef, SourceSequence: 4, ObservedAt: p.now}, nil
}
func (*compositionProvider) Decide(context.Context, nativecontract.ApprovalDecision) (nativecontract.NativeEvidence, error) {
	return nativecontract.NativeEvidence{}, errors.New("not qualified")
}
func (p *compositionProvider) Close(context.Context) (nativecontract.ExitEvidence, error) {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	return nativecontract.ExitEvidence{ProcessIdentityDigest: p.identity.ProcessIdentityDigest, RootExited: true, GroupAbsent: true, ObservedAt: p.now}, nil
}
func (p *compositionProvider) Wait(ctx context.Context) (nativecontract.ExitEvidence, error) {
	return p.Close(ctx)
}
func (p *compositionProvider) Capabilities() nativecontract.Capabilities {
	return nativecontract.Capabilities{Steer: "unsupported", Quota: "unsupported"}
}
func (p *compositionProvider) Identity() nativecontract.NativeIdentity     { return p.identity }
func (p *compositionProvider) Events() <-chan nativecontract.ProviderEvent { return p.events }
func (p *compositionProvider) turn(state, turn string, seq uint64) {
	p.mu.Lock()
	in := p.input
	p.mu.Unlock()
	p.events <- nativecontract.ProviderEvent{Identity: p.identity, SourceSequence: seq, Receipt: &nativecontract.Receipt{RequestID: in.RequestID, InputID: &in.InputID, TurnRef: &turn, State: state}}
}

// Risk: the installed production start/command/receipt/final paths borrow the
// daemon's person authority, infer turn/final evidence, or release on detach.
// The real person approvals, issued pairing key and transaction authorities
// surround a fake owned provider; channels establish each observation.
func TestProductionCompositionStartsDeliversAndConfirmsExit(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f, a := newPairedStartFixture(t, consent.Facts{WorkspaceRoot: root, RetainFinals: true})
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
	module, err := nativesessions.New(nativecontract.Services{Store: compositionStore{f.s, t}, Frames: relay, Finals: f.s, Qualified: true})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	module.Mount(mux)
	chatModule.Mount(mux)
	approvals.New(f.d.App).Mount(mux)
	finalDone, turnDone := make(chan struct{}, 2), make(chan struct{}, 2)
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
			if v.State == "turn_completed" {
				turnDone <- struct{}{}
			}
		}
	}))
	defer server.Close()
	remote := agentd.NewRemote(server.URL, "fixture-runtime")
	config := agentd.NativeProduction(remote, f.intent.ComputerID, f.person.ID, compositionProfiles{f.intent.AccountID, root}, a.Capabilities)
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
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", path, bytes.NewReader(raw)).WithContext(f.personCtx(t))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 200 && w.Code != 201 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		if out != nil {
			if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
				t.Fatal(err)
			}
		}
	}
	var role chat.Role
	personCall("/api/projects/"+f.project+"/chat-roles", chat.RoleCreate{Kind: "lead", SlotKey: "lead"}, &role)
	var thread chat.Thread
	personCall("/api/projects/"+f.project+"/chat-threads/resolve", chat.ThreadResolve{RoleID: role.ID}, &thread)
	personCall("/api/chat-threads/"+thread.ID+"/binding", chat.BindingWrite{SessionID: b.HarnessSessionID, ExpectedEpoch: "0"}, &thread)
	// Retention is a distinct actual person decision, made through approvals.
	var facts consent.Facts
	var proposer tenant.Principal
	proposer.TenantID = f.person.TenantID
	proposer.Kind = tenant.Agent
	var rawFacts []byte
	if err = f.d.Admin.QueryRow(t.Context(), `SELECT r.native_consent,k.principal_id::text,k.id::text,k.scopes FROM approval_requests r JOIN native_session_consents c ON c.tenant_id=r.tenant_id AND c.grant_id=r.id JOIN agent_keys k ON k.tenant_id=c.tenant_id AND k.principal_id=c.agent_principal_id AND k.prefix='nativeproposal' WHERE r.id=$1`, f.intent.PolicyID).Scan(&rawFacts, &proposer.ID, &proposer.KeyID, &proposer.Scopes); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(rawFacts, &facts); err != nil {
		t.Fatal(err)
	}
	proposer.AuthKeyID = proposer.KeyID
	proposer.KeyCreatorID = f.person.ID
	raw, _ := json.Marshal(map[string]any{"scope": consent.RetentionScope, "resource_kind": "node", "resource_id": f.project, "rationale": "Separate retention", "expires_at": f.now.Add(time.Hour), "native_consent": facts})
	r := httptest.NewRequest("POST", "/api/approvals", bytes.NewReader(raw)).WithContext(tenant.WithPrincipal(t.Context(), proposer))
	r.Header.Set("Authorization", "Bearer aeon_nativeproposal_"+strings.Repeat("p", 64))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 201 {
		t.Fatalf("retention %d %s", w.Code, w.Body.String())
	}
	var approval approvals.Approval
	_ = json.Unmarshal(w.Body.Bytes(), &approval)
	personCall("/api/approvals/"+approval.ID+"/decision", map[string]string{"decision": "approved"}, nil)
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
	if err != nil || stopped.Receipt.State != "exit_confirmed" {
		t.Fatal("stop", stopped, err)
	}
	var held bool
	if err = f.d.Admin.QueryRow(t.Context(), `SELECT released_at IS NULL FROM native_session_roots WHERE owned_session_id=$1`, b.OwnedSessionID).Scan(&held); err != nil || held {
		t.Fatal("confirmed root still held", held, err)
	}
	if queued.Receipt.State != "accepted" {
		t.Fatal("queue claimed native delivery")
	}
}
func ioNop(raw []byte) io.ReadCloser { return io.NopCloser(bytes.NewReader(raw)) }
