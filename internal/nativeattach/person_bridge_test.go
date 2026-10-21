// SPDX-License-Identifier: AGPL-3.0-only

package nativeattach_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/internal/agentd"
	"github.com/inspr-at/paimos/internal/client"
	"github.com/inspr-at/paimos/internal/nativeattach"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativepty"
	"github.com/inspr-at/paimos/internal/nativesessions"
	"github.com/inspr-at/paimos/internal/nativestream"
	"github.com/inspr-at/paimos/internal/tenant"
)

const personCookie = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"

type bootstrapStore struct {
	nativecontract.Store
	mu       sync.Mutex
	view     nativecontract.SessionView
	now      time.Time
	viewer   string
	proof    string
	denied   bool
	pending  map[string]string
	switches []string
}

func (s *bootstrapStore) snapshot() nativecontract.SessionView {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.view
}
func (s *bootstrapStore) View(_ context.Context, a nativecontract.Actor, project, id string) (nativecontract.SessionView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a.TenantID != s.view.CurrentBinding.TenantID || project != s.view.CurrentBinding.ProjectID || id != s.view.OwnedSessionID {
		return nativecontract.SessionView{}, nativestream.Refuse(nativecontract.ReasonIdentityMismatch)
	}
	return s.view, nil
}
func (s *bootstrapStore) ByHarness(ctx context.Context, actor nativecontract.Actor, project, harness string) (nativecontract.SessionView, error) {
	view := s.snapshot()
	if view.CurrentBinding.HarnessSessionID != harness {
		return nativecontract.SessionView{}, nativestream.Refuse(nativecontract.ReasonIdentityMismatch)
	}
	return s.View(ctx, actor, project, view.OwnedSessionID)
}
func (s *bootstrapStore) Attach(ctx context.Context, a nativecontract.Actor, in nativecontract.AttachIntent) (nativecontract.AttachResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !nativeattach.LocalPersonProjection(ctx) || a.PrincipalID != s.view.OwnerPersonID || s.denied || !in.ExpectedBinding.Equal(*s.view.CurrentBinding) {
		return nativecontract.AttachResult{}, nativestream.Refuse(nativecontract.ReasonPermissionRevoked)
	}
	s.viewer = uuid.NewString()
	var capability *string
	if in.Mode == "control" {
		if s.view.Controller != nil {
			return nativecontract.AttachResult{}, nativestream.Refuse(nativecontract.ReasonControllerConflict)
		}
		s.view.Controller = &nativecontract.ControllerView{ControllerID: s.viewer, ActorID: a.PrincipalID, ClientID: in.ClientID, Epoch: uuid.NewString(), ExpiresAt: s.now.Add(30 * time.Second)}
		capability = &s.proof
	}
	return nativecontract.AttachResult{Binding: *s.view.CurrentBinding, Mode: in.Mode, ViewerID: s.viewer, ExpiresAt: s.now.Add(30 * time.Second), ExpiresInMS: 30000, Controller: s.view.Controller, ControlCapability: capability, Snapshot: s.view, StreamPath: apiPath(*s.view.CurrentBinding, "stream")}, nil
}
func (s *bootstrapStore) Renew(ctx context.Context, a nativecontract.Actor, in nativecontract.AttachRenew) (nativecontract.AttachResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !nativeattach.LocalPersonProjection(ctx) || nativeattach.LocalControlCapability(ctx) != s.proof || a.PrincipalID != s.view.OwnerPersonID || in.ViewerID != s.viewer || in.ControllerEpoch == nil || s.view.Controller == nil || *in.ControllerEpoch != s.view.Controller.Epoch || s.denied {
		return nativecontract.AttachResult{}, nativestream.Refuse(nativecontract.ReasonPermissionRevoked)
	}
	return nativecontract.AttachResult{Binding: *s.view.CurrentBinding, Mode: "control", ViewerID: s.viewer, ExpiresAt: s.now.Add(30 * time.Second), ExpiresInMS: 30000, Controller: s.view.Controller, ControlCapability: &s.proof, Snapshot: s.view, StreamPath: apiPath(*s.view.CurrentBinding, "stream")}, nil
}
func (s *bootstrapStore) Mutate(ctx context.Context, a nativecontract.Actor, command nativecontract.Command) (nativecontract.OperationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var id, epoch string
	var binding nativecontract.Binding
	var revision int64
	switch command.Kind {
	case "detach":
		in := command.Detach
		if in == nil || in.ViewerID != s.viewer || a.PrincipalID != s.view.OwnerPersonID {
			return nativecontract.OperationResult{}, nativestream.Refuse(nativecontract.ReasonWrongPrincipal)
		}
		s.view.Controller = nil
		return operation(in.RequestID, "detach", in.ExpectedBinding, "applied", s.now), nil
	case "take":
		in := command.Take
		id, epoch, binding, revision = in.RequestID, in.ControllerEpoch, in.ExpectedBinding, in.ExpectedRevision
	case "return":
		in := command.Return
		id, epoch, binding, revision = in.RequestID, in.ControllerEpoch, in.ExpectedBinding, in.ExpectedRevision
	default:
		return nativecontract.OperationResult{}, nativestream.Refuse(nativecontract.ReasonUnsupported)
	}
	if s.denied || a.PrincipalID != s.view.OwnerPersonID || a.PrincipalKind != "person" || !binding.Equal(*s.view.CurrentBinding) || s.view.Controller == nil || epoch != s.view.Controller.Epoch || revision != s.view.Revision || nativeattach.LocalControlCapability(ctx) != s.proof {
		return nativecontract.OperationResult{}, nativestream.Refuse(nativecontract.ReasonPermissionRevoked)
	}
	s.pending[id] = command.Kind
	return operation(id, command.Kind, binding, "accepted", s.now), nil
}

func operation(id, method string, b nativecontract.Binding, state string, now time.Time) nativecontract.OperationResult {
	return nativecontract.OperationResult{Receipt: nativecontract.Receipt{RequestID: id, OperationID: id, Binding: &b, Operation: method, State: state, SourceSequence: "0", UpdatedAt: now}}
}
func apiPath(b nativecontract.Binding, method string) string {
	p := "/api/projects/" + b.ProjectID + "/native-sessions/" + b.OwnedSessionID
	if method != "" {
		p += "/" + method
	}
	return p
}

// The fake owning host can only execute an operation actually accepted by the
// public person store. It has no authority to create the server intent itself.
type bootstrapHost struct {
	nativecontract.Host
	store *bootstrapStore
}

func (h bootstrapHost) transition(id, method string, b nativecontract.Binding) (nativecontract.OperationResult, error) {
	s := h.store
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending[id] != method {
		return nativecontract.OperationResult{}, errors.New("host called before person write was authorized")
	}
	delete(s.pending, id)
	next := b
	next.RuntimeEpoch, next.HarnessSessionID = uuid.NewString(), uuid.NewString()
	next.BindingRevision++
	s.view.CurrentBinding, s.view.Controller = &next, nil
	s.view.Generation++
	s.view.Revision++
	if method == "take" {
		s.view.Mode, s.view.State = "terminal", "taken"
	} else {
		s.view.Mode, s.view.State = "structured", "idle"
	}
	s.switches = append(s.switches, method)
	return operation(id, method, b, "applied", s.now), nil
}
func (h bootstrapHost) Take(_ context.Context, in nativecontract.TakeIntent) (nativecontract.OperationResult, error) {
	return h.transition(in.RequestID, "take", in.ExpectedBinding)
}
func (h bootstrapHost) Return(_ context.Context, in nativecontract.ReturnIntent) (nativecontract.OperationResult, error) {
	return h.transition(in.RequestID, "return", in.ExpectedBinding)
}

type bootstrapAPI struct {
	agentd.API
	tenant, agent string
}

func (a bootstrapAPI) Identity(context.Context) (string, string, error) {
	return a.tenant, a.agent, nil
}

type bootstrapFrames struct{ nativecontract.FrameSource }
type bootstrapFinals struct{ nativecontract.FinalSink }

func newBootstrap(t *testing.T) (string, *bootstrapStore, *client.Client) {
	t.Helper()
	b := nativecontract.Binding{TenantID: uuid.NewString(), ProjectID: uuid.NewString(), OwnedSessionID: uuid.NewString(), HarnessSessionID: uuid.NewString(), RuntimeEpoch: uuid.NewString(), DaemonID: uuid.NewString(), DaemonGeneration: uuid.NewString(), BindingRevision: 1}
	s := &bootstrapStore{now: time.Now(), proof: strings.Repeat("p", 32), pending: map[string]string{}, view: nativecontract.SessionView{OwnedSessionID: b.OwnedSessionID, OwnerPersonID: uuid.NewString(), CurrentBinding: &b, Role: "coordinator", Profile: "native", Harness: "claude", State: "running", Mode: "structured", Revision: 1, Generation: 1, ConversationRef: "fixture-conversation", Capabilities: nativecontract.Capabilities{Input: true, ChatSupported: true, EventsSupported: true, Take: true, TakeSupported: true, TerminalView: true, Steer: "unsupported", Quota: "unsupported", QualificationDigest: strings.Repeat("a", 64)}}}
	m, err := nativesessions.New(nativecontract.Services{Store: s, Frames: bootstrapFrames{}, Finals: bootstrapFinals{}, Qualified: true})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	m.Mount(mux)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("socket bearer leaked to person API")
			http.Error(w, "wrong authentication", http.StatusUnauthorized)
			return
		}
		cookie, err := r.Cookie("aeon_session")
		if err != nil {
			http.Error(w, "person required", http.StatusUnauthorized)
			return
		}
		p := tenant.Principal{ID: s.view.OwnerPersonID, TenantID: b.TenantID, Kind: tenant.Person}
		if cookie.Value == strings.Repeat("a", 64) {
			p.Kind = tenant.Agent
		}
		if cookie.Value == strings.Repeat("b", 64) {
			p.TenantID = uuid.NewString()
		}
		if r.URL.Path == "/api/me" {
			_ = json.NewEncoder(w).Encode(client.Me{Principal: client.Principal{ID: p.ID, TenantID: p.TenantID, Kind: string(p.Kind)}, Tenant: client.Tenant{ID: p.TenantID}})
			return
		}
		mux.ServeHTTP(w, r.WithContext(tenant.WithPrincipal(r.Context(), p)))
	}))
	t.Cleanup(server.Close)
	bridge, err := nativeattach.NewPersonBridge(server.URL, b.TenantID, bootstrapHost{store: s})
	if err != nil {
		t.Fatal(err)
	}
	projector, err := nativestream.NewProjector(nativestream.ExportPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	short, err := os.MkdirTemp("", "np-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(short) })
	short, err = filepath.EvalSymlinks(short)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(short, 0700); err != nil {
		t.Fatal(err)
	}
	supervisor, err := agentd.NewSupervisor(t.Context(), agentd.Config{API: bootstrapAPI{tenant: b.TenantID, agent: uuid.NewString()}, DaemonID: b.DaemonID, Workspace: short, StateRoot: filepath.Join(short, "state"), Accounts: []agentd.EnrolledAccount{{ID: uuid.NewString(), Key: "fixture", DependencyBlocked: true}}, NativeSessions: &agentd.NativeSessionConfig{Host: bridge, LocalAuthority: bridge, Projector: projector}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := supervisor.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	local, err := agentd.ServeLocal(supervisor, filepath.Join(short, "agentd.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := local.Close(); err != nil {
			t.Error(err)
		}
	})
	return local.Socket, s, client.NewSession(server.URL, personCookie)
}

// Risk: the production socket treats an agent, its bearer or a caller-supplied
// person ID as human proof. Exercise the real socket/peer wrapper, public person
// transport and both CLI wire implementations, without claiming PTY qualification.
func TestNativePersonBootstrapAttachTakeHandBack(t *testing.T) {
	socket, store, person := newBootstrap(t)
	proof, err := nativeattach.NewPersonProof(person)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	view := store.snapshot()
	var projection nativecontract.SessionView
	if err = person.Do(ctx, http.MethodGet, apiPath(*view.CurrentBinding, ""), nil, &projection); err != nil {
		t.Fatal(err)
	}
	if projection.Capabilities.Take || projection.Capabilities.TakeSupported {
		t.Fatal("ordinary web projection advertised take")
	}
	if err = person.DoWithHeaders(ctx, http.MethodGet, apiPath(*view.CurrentBinding, ""), nil, &projection, map[string]string{nativeattach.LocalPersonHeader: nativeattach.LocalPersonVersion}); err != nil {
		t.Fatal(err)
	}
	if !projection.Capabilities.Take || !projection.Capabilities.TakeSupported {
		t.Fatal("local person projection lost qualified take")
	}
	agent := client.NewSession(person.BaseURL, strings.Repeat("a", 64))
	if err = agent.DoWithHeaders(ctx, http.MethodGet, apiPath(*view.CurrentBinding, ""), nil, &projection, map[string]string{nativeattach.LocalPersonHeader: nativeattach.LocalPersonVersion}); err != nil {
		t.Fatal(err)
	}
	if projection.Capabilities.Take || projection.Capabilities.TakeSupported || projection.Capabilities.TerminalView {
		t.Fatal("forged local selector granted an agent terminal capabilities")
	}

	attached, err := nativeattach.Open(ctx, socket, nativeattach.Options{Snapshot: view, PersonProof: proof})
	if err != nil {
		t.Fatal(err)
	}
	if err = attached.Detach(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = nativeattach.NewPersonProof(client.New(person.BaseURL, "synthetic-agent-key")); err == nil {
		t.Fatal("agent key became person proof")
	}
	for _, cookie := range []string{"", strings.Repeat("a", 64), strings.Repeat("b", 64)} {
		var deniedProof *nativeattach.PersonProof
		if cookie != "" {
			deniedProof, err = nativeattach.NewPersonProof(client.NewSession(person.BaseURL, cookie))
			if err != nil {
				t.Fatal(err)
			}
		}
		_, err = nativeattach.Open(ctx, socket, nativeattach.Options{Snapshot: view, PersonProof: deniedProof})
		var refusal *nativecontract.ValidationError
		if !errors.As(err, &refusal) || refusal.Reason != nativecontract.ReasonGrantMissing {
			t.Fatalf("wrong socket person refusal: %v", err)
		}
	}
	wire, err := nativepty.NewSocketWire(socket, proof)
	if err != nil {
		t.Fatal(err)
	}
	defer wire.Close()
	attach := func() nativecontract.AttachResult {
		t.Helper()
		view = store.snapshot()
		in := nativecontract.AttachIntent{RequestID: uuid.NewString(), ExpectedBinding: *view.CurrentBinding, ClientID: uuid.NewString(), Mode: "control", View: view.Mode}
		var out nativecontract.AttachResult
		if err := wire.Post(ctx, in.ExpectedBinding, "attach", in.RequestID, in, "NativeAttachResult", "", "", &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	controller := attach()
	if !controller.Snapshot.Capabilities.Take || controller.ControlCapability == nil {
		t.Fatal("local projection lost current control capabilities")
	}
	renew := nativecontract.AttachRenew{RequestID: uuid.NewString(), ExpectedBinding: controller.Binding, ViewerID: controller.ViewerID, ControllerEpoch: &controller.Controller.Epoch}
	if err = wire.Post(ctx, renew.ExpectedBinding, "renew", renew.RequestID, renew, "NativeAttachResult", controller.ViewerID, *controller.ControlCapability, &controller); err != nil {
		t.Fatal(err)
	}
	take := nativecontract.TakeIntent{RequestID: uuid.NewString(), ExpectedBinding: controller.Binding, ControllerEpoch: controller.Controller.Epoch, ExpectedRevision: view.Revision, Cols: 120, Rows: 36, ExpiresAt: store.now.Add(20 * time.Second)}
	var result nativecontract.OperationResult
	// Simulate permission revocation after a readable preflight. No host call
	// may occur until the server's final mutation independently authorizes it.
	store.mu.Lock()
	store.denied = true
	store.mu.Unlock()
	err = wire.Post(ctx, take.ExpectedBinding, "take", take.RequestID, take, "NativeOperationResult", controller.ViewerID, *controller.ControlCapability, &result)
	var refusal *nativecontract.ValidationError
	if !errors.As(err, &refusal) || refusal.Reason != nativecontract.ReasonPermissionRevoked {
		t.Fatalf("wrong revoked-write refusal: %v", err)
	}
	store.mu.Lock()
	if len(store.switches) != 0 {
		t.Fatal("revoked person reached owning host")
	}
	store.denied = false
	store.mu.Unlock()
	take.RequestID = uuid.NewString()
	if err = wire.Post(ctx, take.ExpectedBinding, "take", take.RequestID, take, "NativeOperationResult", controller.ViewerID, *controller.ControlCapability, &result); err != nil {
		t.Fatal(err)
	}
	if result.Receipt.State != "applied" || !result.Receipt.Binding.Equal(take.ExpectedBinding) {
		t.Fatal("transition did not retain request generation")
	}
	controller = attach()
	if controller.Binding.RuntimeEpoch == take.ExpectedBinding.RuntimeEpoch {
		t.Fatal("take retained stale runtime")
	}
	back := nativecontract.ReturnIntent{RequestID: uuid.NewString(), ExpectedBinding: controller.Binding, ControllerEpoch: controller.Controller.Epoch, ExpectedRevision: view.Revision, ExpiresAt: store.now.Add(20 * time.Second)}
	if err = wire.Post(ctx, back.ExpectedBinding, "return", back.RequestID, back, "NativeOperationResult", controller.ViewerID, *controller.ControlCapability, &result); err != nil {
		t.Fatal(err)
	}
	final := store.snapshot()
	if final.Mode != "structured" || final.CurrentBinding.RuntimeEpoch == back.ExpectedBinding.RuntimeEpoch || strings.Join(store.switches, ",") != "take,return" {
		t.Fatal("hand-back did not execute exactly once per transition")
	}
	// A readable snapshot and verified person never authorize a stale command.
	err = wire.Post(ctx, back.ExpectedBinding, "return", back.RequestID, back, "NativeOperationResult", controller.ViewerID, *controller.ControlCapability, &result)
	if !errors.As(err, &refusal) || refusal.Reason != nativecontract.ReasonWrongGeneration {
		t.Fatalf("wrong stale-epoch refusal: %v", err)
	}
}
