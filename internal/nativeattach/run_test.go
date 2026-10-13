// SPDX-License-Identifier: AGPL-3.0-only

package nativeattach

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/internal/nativecontract"
)

var terminalFailure = errors.New("injected terminal failure")

type fakeTerminal struct {
	mu                             sync.Mutex
	input                          chan terminalRead
	writes                         chan string
	entered, readStarted, restored chan struct{}
	startOnce                      sync.Once
	output                         bytes.Buffer
	enterErr, writeErr, sizeErr    error
	restoreCount                   int
}

func newTerminal() *fakeTerminal {
	return &fakeTerminal{input: make(chan terminalRead, 4), writes: make(chan string, 32), entered: make(chan struct{}), readStarted: make(chan struct{}), restored: make(chan struct{})}
}
func (t *fakeTerminal) Interactive() bool { return true }
func (t *fakeTerminal) Enter() error      { close(t.entered); return t.enterErr }
func (t *fakeTerminal) Restore() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.restoreCount++
	close(t.restored)
	return nil
}
func (t *fakeTerminal) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.writeErr != nil {
		return 0, t.writeErr
	}
	n, err := t.output.Write(b)
	select {
	case t.writes <- string(b):
	default:
	}
	return n, err
}
func (t *fakeTerminal) Size() (int, int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return 100, 25, t.sizeErr
}
func (t *fakeTerminal) Read(ctx context.Context, b []byte) (int, error) {
	t.startOnce.Do(func() { close(t.readStarted) })
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case item := <-t.input:
		return copy(b, item.bytes), item.err
	}
}
func (t *fakeTerminal) text() string { t.mu.Lock(); defer t.mu.Unlock(); return t.output.String() }

func runTerminal(t *testing.T, f *fakeSocket, s *Session, term *fakeTerminal, ctx context.Context, resizes <-chan os.Signal) <-chan error {
	t.Helper()
	f.mu.Lock()
	f.onDetach = func() {
		select {
		case <-term.restored:
		default:
			t.Error("detach happened before terminal restoration")
		}
	}
	f.mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, s, term, RunOptions{Platform: "darwin", Resizes: resizes}) }()
	return done
}

func waitRunReady(t *testing.T, f *fakeSocket, term *fakeTerminal) string {
	t.Helper()
	await(t, term.entered)
	await(t, term.readStarted)
	return await(t, f.subscribed)
}

func waitOutput(t *testing.T, term *fakeTerminal, contains string) {
	t.Helper()
	for {
		if strings.Contains(await(t, term.writes), contains) {
			return
		}
	}
}

func eventFor(s *Session, kind, seq string, payload any) nativecontract.StreamEvent {
	raw, _ := json.Marshal(payload)
	return nativecontract.StreamEvent{Binding: s.binding, Generation: s.generation, Sequence: seq, Seq: seq, Source: "native", Kind: kind, Payload: raw, DroppedFrames: "0", ObservedAt: fixtureTime}
}

func TestTerminalRestoredOnEveryExit(t *testing.T) {
	for _, cause := range []string{"raw enter", "draw", "input", "EOF", "signal", "resize", "lease refusal"} {
		t.Run(cause, func(t *testing.T) {
			f := newFakeSocket(t)
			s := f.open(t, false)
			term := newTerminal()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			resizes := make(chan os.Signal, 1)
			if cause == "raw enter" {
				term.enterErr = terminalFailure
			}
			if cause == "draw" {
				term.writeErr = terminalFailure
			}
			done := runTerminal(t, f, s, term, ctx, resizes)
			if cause != "raw enter" && cause != "draw" {
				_ = waitRunReady(t, f, term)
				switch cause {
				case "input":
					term.input <- terminalRead{err: terminalFailure}
				case "EOF":
					term.input <- terminalRead{err: io.EOF}
				case "signal":
					cancel()
				case "resize":
					term.mu.Lock()
					term.sizeErr = terminalFailure
					term.mu.Unlock()
					resizes <- os.Interrupt
				case "lease refusal":
					f.mu.Lock()
					f.rejectRenew = true
					f.mu.Unlock()
					for await(t, f.clock.registered) != 5*time.Second {
					}
					f.clock.advance(5 * time.Second)
				}
			}
			err := await(t, done)
			if cause == "EOF" || cause == "signal" {
				if err != nil {
					t.Fatalf("detach failed: %v", err)
				}
			} else if cause == "lease refusal" {
				var refused *nativecontract.ValidationError
				if !errors.As(err, &refused) || refused.Reason != nativecontract.ReasonPermissionRevoked {
					t.Fatalf("wrong renewal failure: %v", err)
				}
			} else if !errors.Is(err, terminalFailure) {
				t.Fatalf("wrong terminal failure: %v", err)
			}
			term.mu.Lock()
			restored := term.restoreCount
			term.mu.Unlock()
			f.mu.Lock()
			running, detached := f.running, f.detaches
			f.mu.Unlock()
			if restored != 1 || !running || detached != 1 {
				t.Fatalf("restore=%d running=%v detach=%d", restored, running, detached)
			}
		})
	}
}

func TestComposerReceiptsAndEscapeLeaveSessionRunning(t *testing.T) {
	f := newFakeSocket(t)
	s := f.open(t, true)
	term := newTerminal()
	done := runTerminal(t, f, s, term, context.Background(), nil)
	id := waitRunReady(t, f, term)
	turn, state := "active-turn", "running"
	f.mu.Lock()
	frames := f.frames[id]
	f.mu.Unlock()
	frames <- eventFor(s, "structured", "1", nativecontract.StructuredFrame{Kind: "turn_state", Reference: &turn, State: &state})
	waitOutput(t, term, "[turn_state]")
	// All single letters are text inside the composer, including i and q.
	term.input <- terminalRead{bytes: []byte("aquiet input\x1b[13;9u")}
	waitOutput(t, term, ": accepted]")
	if strings.Contains(term.text(), ": delivered]") {
		t.Fatal("accepted input was displayed as native delivery")
	}
	// Discard the completed modified-Enter parser's timer registration. The
	// next registration is a barrier for the bare Esc currently being tested.
drainTimers:
	for {
		select {
		case <-f.clock.registered:
		default:
			break drainTimers
		}
	}
	term.input <- terminalRead{bytes: []byte{0x1b}}
	for await(t, f.clock.registered) != 50*time.Millisecond {
	}
	f.clock.advance(50 * time.Millisecond)
	// Explicit barrier: wait for Esc to leave the field. It must not interrupt
	// the known active turn. Only the deliberate i action may do so.
	waitOutput(t, term, "input: ")
	term.input <- terminalRead{bytes: []byte("i")}
	waitOutput(t, term, "[interrupt ")
	term.input <- terminalRead{bytes: []byte{0x1b}}
	for await(t, f.clock.registered) != 50*time.Millisecond {
	}
	f.clock.advance(50 * time.Millisecond)
	if err := await(t, done); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.inputs) != 1 || f.inputs[0].Text != "quiet input" || len(f.interrupts) != 1 || f.interrupts[0].ExpectedTurnRef != turn || !f.running || f.detaches != 1 {
		t.Fatal("composer shortcuts changed input or detach ended the session")
	}
}

func TestStreamRejectsSuccessorBeforeRendering(t *testing.T) {
	f := newFakeSocket(t)
	s := f.open(t, true)
	term := newTerminal()
	done := runTerminal(t, f, s, term, context.Background(), nil)
	id := waitRunReady(t, f, term)
	changed := eventFor(s, "assistant.delta", "1", nativecontract.AssistantDelta{MessageID: uuid.NewString(), Text: "SUCCESSOR MUST NOT RENDER"})
	changed.Binding.RuntimeEpoch = uuid.NewString()
	f.mu.Lock()
	stream := f.frames[id]
	f.mu.Unlock()
	stream <- changed
	var refused *nativecontract.ValidationError
	err := await(t, done)
	if !errors.As(err, &refused) || refused.Reason != nativecontract.ReasonWrongGeneration {
		t.Fatalf("expected stream binding refusal, got %v", err)
	}
	if strings.Contains(term.text(), "SUCCESSOR MUST NOT RENDER") {
		t.Fatal("successor content reached the old terminal")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.running || len(f.inputs) != 0 {
		t.Fatal("stale stream changed the writer")
	}
}

func TestComposerPreservesShortcutsAndBoundsTerminalData(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		c := &Composer{Focused: true, platform: platform}
		if actions := c.feed([]byte("iqs\x01\x12\x1b[200~ pasted iqs\x1b[13;5u\x1b[201~"), true); len(actions) != 0 {
			t.Fatal("text/paste/browser shortcut triggered an action")
		}
		if c.Text != "iqs pasted iqs" {
			t.Fatalf("text field changed: %q", c.Text)
		}
		if c.Escape() != keyNone || c.Focused || c.Escape() != keyDetach {
			t.Fatal("Esc did not leave field before detaching")
		}
		c.Focused = true
		seq := "\x1b[13;5u"
		if platform == "darwin" {
			seq = "\x1b[13;9u"
		}
		if actions := c.feed([]byte(seq), true); len(actions) != 1 || actions[0] != keySubmit {
			t.Fatal("platform submit was not recognized")
		}
		if strings.Contains(SubmitLabel(platform), "Ctrl") != (platform != "darwin") {
			t.Fatal("wrong submit label")
		}
		c.Focused = true
		if actions := c.feed([]byte("\x1b[27u\x1b[27u"), true); len(actions) != 2 || actions[0] != keyNone || actions[1] != keyDetach {
			t.Fatal("CSI-u Esc did not leave input before detaching")
		}
		c.Focused = true
		if actions := c.feed([]byte{0x1b, 0x1b}, true); len(actions) != 1 || actions[0] != keyNone || c.Focused || c.flushEscape() != keyDetach {
			t.Fatal("quick bare Esc presses disappeared")
		}
	}
	if strings.ContainsAny(terminalText("safe\x1b]52;c;payload\x07\u202eevil"), "\x1b\x07\u202e") {
		t.Fatal("terminal controls survived sanitizing")
	}
	log := &tail{}
	if text := log.put("expanded", strings.Repeat("\t", nativecontract.ExportedFrameBytes), false); !strings.Contains(text, "terminal text omitted") || len(text) > nativecontract.ExportedFrameBytes {
		t.Fatal("display expansion silently truncated or exceeded its bound")
	}
	for i := 0; i < 100; i++ {
		log.put(fmt.Sprint(i), strings.Repeat("x", nativecontract.ExportedFrameBytes), false)
	}
	if len(log.rows) > nativecontract.LiveReplayFrames || log.bytes > nativecontract.LiveReplayBytes {
		t.Fatal("terminal tail exceeded memory bound")
	}
	c := &Composer{Focused: true, Text: strings.Repeat("x", nativecontract.InputUTF8Bytes), platform: "linux"}
	c.feed([]byte("one more"), true)
	if len(c.Text) != nativecontract.InputUTF8Bytes {
		t.Fatal("composer exceeded input bound")
	}
}
