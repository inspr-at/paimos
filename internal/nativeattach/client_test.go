// SPDX-License-Identifier: AGPL-3.0-only

package nativeattach

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/internal/nativecontract"
)

var fixtureTime = time.Date(2026, 10, 13, 2, 0, 0, 0, time.UTC)

func fixtureView() nativecontract.SessionView {
	b := nativecontract.Binding{TenantID: uuid.NewString(), ProjectID: uuid.NewString(), OwnedSessionID: uuid.NewString(), HarnessSessionID: uuid.NewString(), RuntimeEpoch: uuid.NewString(), DaemonID: uuid.NewString(), DaemonGeneration: uuid.NewString(), BindingRevision: 1}
	return nativecontract.SessionView{OwnedSessionID: b.OwnedSessionID, CurrentBinding: &b, OwnerPersonID: uuid.NewString(), Role: "coordinator", Profile: "native", Harness: "claude", State: "running", Mode: "structured", Revision: 1, ConversationRef: "fixture-conversation", Generation: 1,
		Capabilities: nativecontract.Capabilities{Input: true, ChatSupported: true, Interrupt: true, InterruptSupported: true, EventsSupported: true, Steer: "native", Quota: "unsupported", QualificationDigest: strings.Repeat("a", 64)}}
}

type clockWait struct {
	at time.Time
	ch chan time.Time
}
type testClock struct {
	mu         sync.Mutex
	now        time.Time
	waits      []clockWait
	registered chan time.Duration
}

func newClock() *testClock {
	return &testClock{now: fixtureTime, registered: make(chan time.Duration, 64)}
}
func (c *testClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *testClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	ch := make(chan time.Time, 1)
	c.waits = append(c.waits, clockWait{c.now.Add(d), ch})
	c.mu.Unlock()
	c.registered <- d
	return ch
}
func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	var pending []clockWait
	for _, wait := range c.waits {
		if !c.now.Before(wait.at) {
			wait.ch <- c.now
		} else {
			pending = append(pending, wait)
		}
	}
	c.waits = pending
}

func await[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("barrier did not complete")
		var zero T
		return zero
	}
}

type viewer struct{ client, mode string }
type fakeSocket struct {
	mu                           sync.Mutex
	view                         nativecontract.SessionView
	clock                        *testClock
	socket                       string
	validator                    *nativecontract.Validator
	viewers                      map[string]viewer
	subscribed                   chan string
	frames                       map[string]chan nativecontract.StreamEvent
	inputAttempts                int
	inputs                       []nativecontract.Input
	lastAttempt                  nativecontract.Input
	receipts                     []nativecontract.Receipt
	running                      bool
	detaches                     int
	controllerProof              string
	attachEntered, attachRelease chan struct{}
	leaseMS                      int64
	rejectRenew                  bool
	onDetach                     func()
}

func newFakeSocket(t *testing.T) *fakeSocket {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	v, err := nativecontract.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSocket{view: fixtureView(), clock: newClock(), socket: filepath.Join(dir, "agentd.sock"), validator: v, viewers: map[string]viewer{}, frames: map[string]chan nativecontract.StreamEvent{}, subscribed: make(chan string, 16), running: true, controllerProof: "fake-derived-controller-proof", leaseMS: 30000}
	listener, err := net.Listen("unix", f.socket)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(f.socket, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(f.socket+".token", []byte(strings.Repeat("x", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(f.serve)}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return f
}

func (f *fakeSocket) snapshot() nativecontract.SessionView {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.view
}

func (f *fakeSocket) open(t *testing.T, control bool) *Session {
	t.Helper()
	s, err := Open(context.Background(), f.socket, Options{Snapshot: f.snapshot(), Control: control, Clock: f.clock})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = s.Detach(ctx)
	})
	return s
}

func (f *fakeSocket) refusal(w http.ResponseWriter, reason nativecontract.Reason) {
	w.WriteHeader(http.StatusConflict)
	_ = json.NewEncoder(w).Encode(nativecontract.Error{Reason: reason})
}

func (f *fakeSocket) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(nativecontract.LocalProtocolHeader, nativecontract.LocalProtocol)
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("x", 32) || r.Header.Get(nativecontract.LocalProtocolHeader) != nativecontract.LocalProtocol {
		f.refusal(w, nativecontract.ReasonGrantMissing)
		return
	}
	method := strings.TrimPrefix(r.URL.Path, "/v1/native-sessions/"+f.view.OwnedSessionID+"/")
	if method == "attach/renew" {
		method = "renew"
	}
	if r.Method == http.MethodGet && method == "stream" {
		id := r.Header.Get(nativecontract.ViewerHeader)
		f.mu.Lock()
		_, authorized := f.viewers[id]
		ch := make(chan nativecontract.StreamEvent, 1)
		f.frames[id] = ch
		f.mu.Unlock()
		if !authorized {
			f.refusal(w, nativecontract.ReasonGrantMissing)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		f.subscribed <- id
		for {
			select {
			case <-r.Context().Done():
				return
			case e := <-ch:
				raw, _ := json.Marshal(e)
				_, _ = fmt.Fprintf(w, "id: %d:%s\nevent: %s\ndata: %s\n\n", e.Generation, e.Seq, e.Kind, raw)
				w.(http.Flusher).Flush()
			}
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method == http.MethodGet && method == "receipts" {
		if _, ok := f.viewers[r.Header.Get(nativecontract.ViewerHeader)]; !ok {
			f.refusal(w, nativecontract.ReasonGrantMissing)
			return
		}
		_ = json.NewEncoder(w).Encode(nativecontract.ReceiptPage{Items: f.receipts})
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, nativecontract.RequestBodyBytes+1))
	if err != nil {
		f.refusal(w, nativecontract.ReasonInvalidInput)
		return
	}
	var packet nativecontract.LocalEnvelope
	_ = json.Unmarshal(raw, &packet)
	if method == "input" {
		f.inputAttempts++
		_ = json.Unmarshal(packet.Params, &f.lastAttempt)
	}
	ctx := nativecontract.ValidationContext{Binding: f.view.CurrentBinding, Generation: f.view.Generation, PrincipalKind: "person", Capabilities: &f.view.Capabilities, Mode: f.view.Mode, State: f.view.State}
	packet, err = f.validator.DecodeLocal(raw, r.Header.Get(nativecontract.LocalProtocolHeader), method, ctx)
	if err != nil {
		var v *nativecontract.ValidationError
		if errors.As(err, &v) {
			f.refusal(w, v.Reason)
		} else {
			f.refusal(w, nativecontract.ReasonInvalidInput)
		}
		return
	}
	if method == "attach" {
		var in nativecontract.AttachIntent
		_ = json.Unmarshal(packet.Params, &in)
		if in.Mode == "control" && f.view.Controller != nil {
			f.refusal(w, nativecontract.ReasonControllerConflict)
			return
		}
		id := uuid.NewString()
		f.viewers[id] = viewer{in.ClientID, in.Mode}
		if in.Mode == "control" {
			f.view.Controller = &nativecontract.ControllerView{ControllerID: uuid.NewString(), ActorID: f.view.OwnerPersonID, ClientID: in.ClientID, Epoch: uuid.NewString(), ExpiresAt: f.clock.Now().Add(30 * time.Second)}
		}
		out := f.attachResult(id)
		if f.attachEntered != nil {
			entered, release := f.attachEntered, f.attachRelease
			f.mu.Unlock()
			close(entered)
			<-release
			f.mu.Lock()
		}
		_ = json.NewEncoder(w).Encode(out)
		return
	}
	id := r.Header.Get(nativecontract.ViewerHeader)
	v, ok := f.viewers[id]
	if !ok {
		f.refusal(w, nativecontract.ReasonGrantMissing)
		return
	}
	if method == "renew" {
		if f.rejectRenew {
			f.refusal(w, nativecontract.ReasonPermissionRevoked)
			return
		}
		_ = json.NewEncoder(w).Encode(f.attachResult(id))
		return
	}
	if method == "input" || method == "interrupt" {
		if v.mode != "control" || r.Header.Get(nativecontract.ControlHeader) != f.controllerProof {
			f.refusal(w, nativecontract.ReasonGrantMissing)
			return
		}
	}
	receipt := nativecontract.Receipt{RequestID: packet.RequestID, OperationID: uuid.NewString(), Binding: f.view.CurrentBinding, Operation: method, State: "applied", SourceSequence: "0", UpdatedAt: f.clock.Now()}
	switch method {
	case "detach":
		if f.onDetach != nil {
			f.onDetach()
		}
		f.detaches++
		delete(f.viewers, id)
		if f.view.Controller != nil && f.view.Controller.ClientID == v.client {
			f.view.Controller = nil
		}
	case "input":
		var in nativecontract.Input
		_ = json.Unmarshal(packet.Params, &in)
		f.inputs = append(f.inputs, in)
		receipt.InputID = &in.InputID
		receipt.State = "accepted"
		f.receipts = append(f.receipts, receipt)
	case "stop":
		f.running = false
	case "interrupt":
	default:
		f.refusal(w, nativecontract.ReasonUnsupported)
		return
	}
	_ = json.NewEncoder(w).Encode(nativecontract.OperationResult{Receipt: receipt})
}

func (f *fakeSocket) attachResult(id string) nativecontract.AttachResult {
	v := f.viewers[id]
	out := nativecontract.AttachResult{ViewerID: id, Binding: *f.view.CurrentBinding, Mode: v.mode, ExpiresAt: f.clock.Now().Add(24 * time.Hour), ExpiresInMS: f.leaseMS, Controller: f.view.Controller, StreamPath: "/v1/native-sessions/" + f.view.OwnedSessionID + "/stream", Snapshot: f.view}
	if v.mode == "control" {
		proof := f.controllerProof
		out.ControlCapability = &proof
	}
	return out
}

func (f *fakeSocket) successor() {
	f.mu.Lock()
	defer f.mu.Unlock()
	b := *f.view.CurrentBinding
	b.RuntimeEpoch, b.HarnessSessionID = uuid.NewString(), uuid.NewString()
	b.BindingRevision++
	f.view.CurrentBinding = &b
	f.view.Generation++
}

// Risk: a disconnect may stop LEAD, or a public ID may send input to its
// successor. These assertions establish the actual host writes and occupancy,
// rather than accepting any arbitrary client/server failure as a pass.
func TestFakeSocketDetachViewerAndStaleBinding(t *testing.T) {
	t.Run("viewer cannot input and detach leaves writer", func(t *testing.T) {
		f := newFakeSocket(t)
		s := f.open(t, false)
		if _, err := s.Submit(context.Background(), "do not deliver", "now"); !errors.Is(err, ErrReadOnly) {
			t.Fatalf("expected read-only refusal, got %v", err)
		}
		if err := s.Detach(context.Background()); err != nil {
			t.Fatal(err)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.inputAttempts != 0 || len(f.inputs) != 0 || !f.running || f.detaches != 1 {
			t.Fatal("viewer wrote input or detach changed host occupancy")
		}
	})
	t.Run("many viewers one explicit controller", func(t *testing.T) {
		f := newFakeSocket(t)
		_ = f.open(t, false)
		_ = f.open(t, false)
		controller := f.open(t, true)
		_, err := Open(context.Background(), f.socket, Options{Snapshot: f.snapshot(), Control: true, Clock: f.clock})
		var refused *nativecontract.ValidationError
		if !errors.As(err, &refused) || refused.Reason != nativecontract.ReasonControllerConflict {
			t.Fatalf("expected controller conflict, got %v", err)
		}
		receipt, err := controller.Submit(context.Background(), "one retained input", "after_turn")
		if err != nil || receipt.State != "accepted" {
			t.Fatalf("expected accepted intent, got %v", err)
		}
		page, err := controller.Receipts(context.Background(), "")
		if err != nil || len(page.Items) != 1 || page.Items[0].InputID == nil || *page.Items[0].InputID != *receipt.InputID || page.Items[0].State != "accepted" {
			t.Fatalf("receipt did not retain the input assertion: %v", err)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if len(f.inputs) != 1 || f.inputs[0].Text != "one retained input" || f.inputs[0].AllowSameConversationSuccessor {
			t.Fatal("input changed, was duplicated or can target a successor")
		}
	})
	t.Run("stale binding never targets successor", func(t *testing.T) {
		f := newFakeSocket(t)
		s := f.open(t, true)
		original := s.binding
		f.successor()
		_, err := s.Submit(context.Background(), "old intent", "now")
		var refused *nativecontract.ValidationError
		if !errors.As(err, &refused) || refused.Reason != nativecontract.ReasonWrongGeneration {
			t.Fatalf("expected exact generation refusal, got %v", err)
		}
		if _, err = s.Submit(context.Background(), "no automatic retry", "now"); !errors.Is(err, ErrBindingChanged) {
			t.Fatalf("authority was not closed: %v", err)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.inputAttempts != 1 || !f.lastAttempt.ExpectedBinding.Equal(original) || len(f.inputs) != 0 || !f.running {
			t.Fatal("old intent was retargeted, retried or ended the writer")
		}
	})
}

func TestAttachLeaseUsesElapsedServerBudget(t *testing.T) {
	f := newFakeSocket(t)
	f.attachEntered, f.attachRelease = make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := Open(context.Background(), f.socket, Options{Snapshot: f.snapshot(), Control: true, Clock: f.clock})
		done <- err
	}()
	await(t, f.attachEntered)
	f.clock.advance(31 * time.Second)
	close(f.attachRelease)
	if err := await(t, done); !errors.Is(err, ErrLeaseExpired) {
		t.Fatalf("expired monotonic budget was accepted using wall-clock expires_at: %v", err)
	}
}
