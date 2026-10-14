// SPDX-License-Identifier: AGPL-3.0-only

package nativepty

import (
	"context"
	"encoding/base64"
	"io"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/internal/nativecontract"
)

type clientWire struct {
	view                     nativecontract.SessionView
	calls                    []string
	epochs                   []string
	bytes                    []byte
	clock                    nativecontract.Clock
	takeState, inputState    string
	stale, wrongConversation bool
	resizeNotice             chan [2]int
}

func (w *clientWire) Post(_ context.Context, b nativecontract.Binding, method, id string, params any, schema, viewer, proof string, dest any) error {
	w.calls = append(w.calls, method)
	if method == "attach" || method == "renew" {
		client := ""
		epoch := uuid.NewString()
		viewerID := uuid.NewString()
		if method == "attach" {
			client = params.(nativecontract.AttachIntent).ClientID
			w.epochs = append(w.epochs, epoch)
		} else {
			client = w.view.Controller.ClientID
			epoch = w.view.Controller.Epoch
			viewerID = viewer
		}
		controller := nativecontract.ControllerView{ControllerID: uuid.NewString(), ActorID: w.view.OwnerPersonID, ClientID: client, Epoch: epoch, ExpiresAt: w.clock.Now().Add(30 * time.Second)}
		if method == "renew" {
			controller.ControllerID = w.view.Controller.ControllerID
		}
		w.view.Controller = &controller
		capability := strings.Repeat("p", 32)
		*dest.(*nativecontract.AttachResult) = nativecontract.AttachResult{Binding: b, Mode: "control", ViewerID: viewerID, Controller: &controller, ControlCapability: &capability, ExpiresAt: controller.ExpiresAt, ExpiresInMS: 30000, StreamPath: localPath(b, "stream"), Snapshot: w.view}
		return nil
	}
	state := "applied"
	if method == "take" || method == "return" {
		state = "accepted"
		if w.takeState != "" {
			state = w.takeState
		}
		if state == "accepted" {
			next := cloneBinding(b)
			if !w.stale {
				next.RuntimeEpoch = uuid.NewString()
				next.HarnessSessionID = uuid.NewString()
				next.BindingRevision++
				w.view.Generation++
				w.view.Revision++
			}
			w.view.CurrentBinding = &next
			w.view.Controller = nil
			if method == "take" {
				w.view.Mode, w.view.State = "terminal", "taken"
			} else {
				w.view.Mode, w.view.State = "structured", "idle"
			}
			if w.wrongConversation {
				w.view.ConversationRef = "foreign-conversation"
			}
		}
	}
	if method == "pty_input" {
		state = "write_acknowledged"
		if w.inputState != "" {
			state = w.inputState
		}
		in := params.(nativecontract.PTYInput)
		data, _ := base64.StdEncoding.DecodeString(in.DataBase64)
		w.bytes = append(w.bytes, data...)
	}
	*dest.(*nativecontract.OperationResult) = nativecontract.OperationResult{Receipt: nativecontract.Receipt{RequestID: id, OperationID: uuid.NewString(), Binding: &b, Operation: method, State: state, SourceSequence: "0", UpdatedAt: w.clock.Now()}}
	if method == "resize" && w.resizeNotice != nil {
		in := params.(nativecontract.Resize)
		w.resizeNotice <- [2]int{int(in.Cols), int(in.Rows)}
	}
	return nil
}
func (*clientWire) Watch(ctx context.Context, _ nativecontract.SessionView, _ string, _ func(nativecontract.StreamEvent) error) error {
	<-ctx.Done()
	return ctx.Err()
}
func (*clientWire) Close() {}
func clientFixture(t *testing.T, clock nativecontract.Clock) (*TakeClient, *clientWire) {
	t.Helper()
	_, f, _, _, _ := takeoverFixture(t)
	if clock == nil {
		clock = &testClock{now: time.Now()}
	}
	w := &clientWire{clock: clock, view: nativecontract.SessionView{CurrentBinding: &f.spec.Binding, OwnedSessionID: f.spec.Binding.OwnedSessionID, OwnerPersonID: uuid.NewString(), Role: "coordinator", Profile: "native", Harness: "claude", State: "running", Mode: "structured", ConversationRef: "fixture-conversation", Generation: 1, Revision: 1, Capabilities: nativecontract.Capabilities{Take: true, TakeSupported: true, TerminalView: true, EventsSupported: true, Steer: "unsupported", Quota: "unsupported", QualificationDigest: strings.Repeat("a", 64)}}}
	c, err := OpenClient(context.Background(), ClientConfig{Snapshot: w.view, Refresh: func(context.Context) (nativecontract.SessionView, error) { return w.view, nil }, Wire: w, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	return c, w
}
func countMethod(w *clientWire, method string) int {
	n := 0
	for _, call := range w.calls {
		if call == method {
			n++
		}
	}
	return n
}

func TestNativeTakeClientRebindsAndDetachNeverReturns(t *testing.T) {
	c, w := clientFixture(t, nil)
	old := c.binding
	if err := c.Begin(context.Background(), 132, 40); err != nil {
		t.Fatal(err)
	}
	if c.binding.Equal(old) || len(w.epochs) != 2 || w.epochs[0] == w.epochs[1] {
		t.Fatal("runtime/controller was carried forward")
	}
	if err := c.Write(context.Background(), []byte("\x1b")); err != nil {
		t.Fatal(err)
	}
	if err := c.Detach(context.Background()); err != nil {
		t.Fatal(err)
	}
	if string(w.bytes) != "\x1b" || countMethod(w, "take") != 1 || countMethod(w, "return") != 0 || countMethod(w, "stop") != 0 || countMethod(w, "detach") != 1 {
		t.Fatal("detach or Esc changed runtime lifetime")
	}
}
func TestNativeTakeClientRequiresActualFreshSuccessor(t *testing.T) {
	for _, kind := range []string{"stale", "foreign", "uncertain"} {
		t.Run(kind, func(t *testing.T) {
			c, w := clientFixture(t, nil)
			switch kind {
			case "stale":
				w.stale = true
			case "foreign":
				w.wrongConversation = true
			case "uncertain":
				w.takeState = "uncertain"
			}
			err := c.Begin(context.Background(), 120, 36)
			want := nativecontract.ReasonWrongGeneration
			if kind == "foreign" {
				want = nativecontract.ReasonIdentityMismatch
			}
			if kind == "uncertain" {
				want = nativecontract.ReasonInputOutcomeUnconfirmed
			}
			requireReason(t, err, want)
			if countMethod(w, "attach") != 1 || countMethod(w, "resize") != 0 || countMethod(w, "take") != 1 {
				t.Fatal("unconfirmed take proceeded or retried")
			}
		})
	}
}
func TestNativeTakeClientAcknowledgementAndHandBack(t *testing.T) {
	c, w := clientFixture(t, nil)
	if err := c.Begin(context.Background(), 120, 36); err != nil {
		t.Fatal(err)
	}
	w.inputState = "accepted"
	requireReason(t, c.Write(context.Background(), []byte("x")), nativecontract.ReasonInputOutcomeUnconfirmed)
	requireReason(t, c.Write(context.Background(), []byte("x")), nativecontract.ReasonControllerExpired)
	if countMethod(w, "pty_input") != 1 {
		t.Fatal("uncertain bytes resent")
	}
	c, w = clientFixture(t, nil)
	if err := c.Begin(context.Background(), 120, 36); err != nil {
		t.Fatal(err)
	}
	if err := c.HandBack(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.Detach(context.Background()); err != nil {
		t.Fatal(err)
	}
	if countMethod(w, "attach") != 2 || countMethod(w, "return") != 1 || countMethod(w, "stop") != 0 || countMethod(w, "detach") != 0 {
		t.Fatal("hand-back claimed successor control or silently stopped")
	}
	c, w = clientFixture(t, nil)
	if err := c.Begin(context.Background(), 120, 36); err != nil {
		t.Fatal(err)
	}
	w.view.State = "returning"
	requireReason(t, c.Renew(context.Background()), nativecontract.ReasonOwnershipUnconfirmed)
	requireReason(t, c.Write(context.Background(), []byte("x")), nativecontract.ReasonControllerExpired)
	requireReason(t, c.Resize(context.Background(), 132, 40, false), nativecontract.ReasonControllerExpired)
	if countMethod(w, "pty_input") != 0 || countMethod(w, "resize") != 1 {
		t.Fatal("paused control sent input or resize")
	}
}

type timerNotice struct {
	duration time.Duration
	fire     chan time.Time
}
type manualClock struct {
	now    time.Time
	timers chan timerNotice
}

func (c *manualClock) Now() time.Time { return c.now }
func (c *manualClock) After(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	c.timers <- timerNotice{d, ch}
	return ch
}

type runTerminal struct {
	mu                            sync.Mutex
	cols, rows, entered, restored int
	input                         chan []byte
}

func (t *runTerminal) Write(b []byte) (int, error) { return len(b), nil }
func (t *runTerminal) Enter() error                { t.entered++; return nil }
func (t *runTerminal) Restore() error              { t.restored++; return nil }
func (t *runTerminal) Interactive() bool           { return true }
func (t *runTerminal) Size() (int, int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cols, t.rows, nil
}
func (t *runTerminal) Read(ctx context.Context, b []byte) (int, error) {
	select {
	case data := <-t.input:
		if data == nil {
			return 0, io.EOF
		}
		return copy(b, data), nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}
func (t *runTerminal) size(cols, rows int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cols, t.rows = cols, rows
}

func TestPTYResizeStormDebounceAndTerminalRestore(t *testing.T) {
	clock := &manualClock{now: time.Now(), timers: make(chan timerNotice, 16)}
	c, w := clientFixture(t, clock)
	w.resizeNotice = make(chan [2]int, 8)
	terminal := &runTerminal{cols: 120, rows: 36, input: make(chan []byte, 1)}
	signals := make(chan os.Signal)
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { done <- Run(ctx, c, terminal, RunOptions{Resizes: signals}) }()
	select {
	case size := <-w.resizeNotice:
		if size != [2]int{120, 36} {
			t.Fatal("take did not send holder size")
		}
	case <-ctx.Done():
		t.Fatal("initial resize missing")
	}
	renew := <-clock.timers
	if renew.duration != 5*time.Second {
		t.Fatal("wrong lease heartbeat")
	}
	var last timerNotice
	for i := 0; i < 3; i++ {
		terminal.size(130+i, 40)
		signals <- syscall.SIGWINCH
		last = <-clock.timers
		if last.duration != ResizeDebounce {
			t.Fatal("wrong resize debounce")
		}
	}
	last.fire <- clock.now
	select {
	case size := <-w.resizeNotice:
		if size != [2]int{132, 40} {
			t.Fatal("storm did not apply final size")
		}
	case <-ctx.Done():
		t.Fatal("debounced resize missing")
	}
	terminal.input <- []byte{'\x1b', 0x1d, 'd'}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("raw terminal did not restore/detach")
	}
	if countMethod(w, "resize") != 2 || string(w.bytes) != "\x1b" || countMethod(w, "return") != 0 || terminal.entered != 1 || terminal.restored != 1 {
		t.Fatal("resize storm, Esc, detach or terminal restoration regression")
	}
}
