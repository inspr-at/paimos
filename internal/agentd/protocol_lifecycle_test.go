// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/ownedprocess"
)

// No child or provider is launched. An unavailable lifetime and a closed
// waitDone model an already-reaped root; the test retains its stdout writer.
func heldWire(t *testing.T) (*wireProcess, *os.File, *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	p := &wireProcess{cmd: &exec.Cmd{Process: &os.Process{Pid: 3456}}, lifetime: &ownedprocess.Lifetime{},
		stdout: r, pending: map[string]chan json.RawMessage{}, readDone: make(chan struct{}), waitDone: make(chan struct{})}
	close(p.waitDone)
	t.Cleanup(func() {
		_ = w.Close()
		_ = r.Close()
		select {
		case <-p.readDone:
		case <-time.After(time.Second):
			t.Error("synthetic reader did not exit")
		}
	})
	return p, r, w
}

func assertReaderReleased(t *testing.T, p *wireProcess, r, w *os.File) {
	t.Helper()
	select {
	case <-p.readDone:
	default:
		t.Fatal("owner returned with reader still running")
	}
	if _, err := r.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("owned read descriptor still open: %v", err)
	}
	if _, err := w.Stat(); err != nil {
		t.Fatalf("cleanup closed another owner's writer: %v", err)
	}
	if p.readErr == nil {
		t.Fatal("local closure was published as source EOF")
	}
}

func TestWireFinalOwnersReleaseHeldStdout(t *testing.T) {
	for _, owner := range []string{"pi", "claude", "cursor_early_exit", "cursor_terminal"} {
		t.Run(owner, func(t *testing.T) {
			p, r, w := heldWire(t)
			var observations atomic.Int32
			p.observe = func(AdapterEvent) { observations.Add(1) }
			p.setOnEvent(func(json.RawMessage) { observations.Add(1) })
			rootErr := errors.New("synthetic root exit")
			p.waitErr = rootErr
			var wait func() error
			switch owner {
			case "pi":
				wait = (&piProcess{wireProcess: p}).Wait
			case "claude":
				wait = (&claudeProcess{wireProcess: p, assetDir: t.TempDir()}).Wait
			case "cursor_early_exit":
				wait = (&cursorProcess{wireProcess: p, done: make(chan struct{})}).Wait
			case "cursor_terminal":
				// Select the terminal branch deterministically. Stop cannot signal
				// this unavailable lifetime, even without an exit notification.
				p.waitDone = make(chan struct{})
				cp := &cursorProcess{wireProcess: p, done: make(chan struct{})}
				cp.finish(nil)
				wait = cp.Wait
			}
			go p.read(r)
			result := make(chan error, 1)
			go func() { result <- wait() }()
			select {
			case err := <-result:
				if owner == "cursor_terminal" && err != nil || owner != "cursor_terminal" && err == nil {
					t.Fatalf("owner changed its outcome: %v", err)
				}
				if (owner == "pi" || owner == "claude") && !errors.Is(err, rootErr) {
					t.Fatal("lost original root error")
				}
			case <-time.After(3 * readerCleanupTimeout):
				t.Fatal("owner cleanup unbounded")
			}
			assertReaderReleased(t, p, r, w)
			if _, err := io.WriteString(w, "{}\n"); err == nil || observations.Load() != 0 {
				t.Fatal("settled owner accepted a late callback or local-close diagnostic")
			}
			var joins sync.WaitGroup
			for i := 0; i < 8; i++ {
				joins.Go(func() {
					p.closeReader()
					if err := p.finishReader(); err != nil {
						t.Error(err)
					}
				})
			}
			joins.Wait()
		})
	}
}

func TestWireFailedStartReleasesHeldStdout(t *testing.T) {
	p, r, w := heldWire(t)
	go p.read(r)
	failure := errors.New("synthetic initialize rejection")
	proc, err := p.failStart(failure)
	if proc != nil || !errors.Is(err, failure) {
		t.Fatal("failed start changed its result")
	}
	assertReaderReleased(t, p, r, w)
	// Early notifications must never be replayed after failed ownership transfer.
	p.eventMu.Lock()
	p.earlyEvents = []json.RawMessage{json.RawMessage(`{}`)}
	p.eventMu.Unlock()
	p.setOnEvent(func(json.RawMessage) { t.Error("failed start replayed early notification") })
}

func TestWireCleanupSuppressesBufferedCallbacks(t *testing.T) {
	p, r, w := heldWire(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var calls atomic.Int32
	p.setOnEvent(func(json.RawMessage) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
	})
	go p.read(r)
	if _, err := io.WriteString(w, "{}\n{}\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("reader did not enter callback")
	}
	p.closeReader()
	// A slow observer cannot make local ownership cleanup wait indefinitely.
	if err := p.finishReader(); err == nil {
		t.Fatal("blocked callback incorrectly reported joined")
	}
	once.Do(func() { close(release) })
	if err := p.finishReader(); err != nil {
		t.Fatal(err)
	}
	assertReaderReleased(t, p, r, w)
	if calls.Load() != 1 {
		t.Fatal("locally closed reader dispatched a buffered callback")
	}
}

func TestWireReaderCallbacksCanStop(t *testing.T) {
	for _, origin := range []string{"notification", "stream_error"} {
		t.Run(origin, func(t *testing.T) {
			p, r, w := heldWire(t)
			called := make(chan error, 1)
			stop := func() { called <- p.Stop(t.Context()) }
			p.setOnEvent(func(json.RawMessage) { stop() })
			p.observe = func(AdapterEvent) { stop() }
			if origin == "notification" {
				go p.read(r)
				_, _ = io.WriteString(w, "{}\n")
			} else {
				reader, writer := io.Pipe()
				go p.read(reader)
				_ = writer.CloseWithError(errors.New("synthetic read failure"))
				t.Cleanup(func() { _ = reader.Close() })
			}
			select {
			case err := <-called:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("Stop deadlocked in reader callback")
			}
			if p.readerClosed.Load() {
				t.Fatal("Stop closed stream before final owner could drain")
			}
			p.discardReader()
			assertReaderReleased(t, p, r, w)
		})
	}
}

func TestWireRootExitDrainsBufferedAccounting(t *testing.T) {
	p, r, w := heldWire(t)
	var calls atomic.Int32
	p.setOnEvent(func(json.RawMessage) { calls.Add(1) })
	_, _ = io.WriteString(w, "{}\n{}\n")
	_ = w.Close()
	go p.read(r)
	if err := p.Wait(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || p.readErr != nil {
		t.Fatal("root exit discarded buffered accounting or actual EOF")
	}
	if _, err := r.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatal("EOF did not release owned descriptor")
	}
}

type heldWireAdapter struct{ proc Process }

func (*heldWireAdapter) Name() string                       { return Codex }
func (*heldWireAdapter) Probe(context.Context, string) bool { return true }
func (a *heldWireAdapter) Start(context.Context, StartRequest, func(AdapterEvent)) (Process, error) {
	return a.proc, nil
}

type rejectStartedAPI struct{ *fakeAPI }

func (a *rejectStartedAPI) Report(ctx context.Context, id string, report Telemetry) error {
	if report.Kind == "started" {
		return errors.New("synthetic started report rejection")
	}
	return a.fakeAPI.Report(ctx, id, report)
}

func TestWireSupervisorFailedOwnershipTransferClosesReader(t *testing.T) {
	s, api, _ := testSupervisor(t)
	defer s.Close(context.Background())
	p, r, w := heldWire(t)
	go p.read(r)
	s.api = &rejectStartedAPI{api}
	s.adapters[Codex] = &heldWireAdapter{proc: &piProcess{wireProcess: p}}
	if err := s.PollOnce(t.Context()); err == nil {
		t.Fatal("startup report rejection ignored")
	}
	assertReaderReleased(t, p, r, w)
}
