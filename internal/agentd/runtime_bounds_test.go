// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

type headerConn struct {
	io.Reader
	consumed int
}

func (c *headerConn) Read(p []byte) (int, error) {
	n, err := c.Reader.Read(p)
	c.consumed += n
	return n, err
}
func (*headerConn) Write(p []byte) (int, error)      { return len(p), nil }
func (*headerConn) Close() error                     { return nil }
func (*headerConn) LocalAddr() net.Addr              { return nil }
func (*headerConn) RemoteAddr() net.Addr             { return nil }
func (*headerConn) SetDeadline(time.Time) error      { return nil }
func (*headerConn) SetReadDeadline(time.Time) error  { return nil }
func (*headerConn) SetWriteDeadline(time.Time) error { return nil }

func TestGrokProxyHeaderConsumptionBound(t *testing.T) {
	for _, prefix := range []string{"", strings.Repeat("X: y\r\n", 600)} {
		c := &headerConn{Reader: strings.NewReader(prefix + strings.Repeat("x", 1<<20))}
		p := &grokProxy{}
		p.handle(c)
		if !p.violation.Load() || c.consumed > 4097 {
			t.Fatalf("unbounded header: consumed=%d violation=%v", c.consumed, p.violation.Load())
		}
	}
}

type enteredPipe struct {
	*os.File
	entered chan struct{}
}

func (w *enteredPipe) Write(p []byte) (int, error) {
	close(w.entered)
	return w.File.Write(p)
}

func TestWireRequestCancelsFullPipe(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	// No reader exists; a frame larger than pipe capacity cannot complete.
	input := &enteredPipe{File: w, entered: make(chan struct{})}
	p := &wireProcess{stdin: input, pending: make(map[string]chan json.RawMessage)}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := p.request(ctx, "jsonrpc", "initialize", strings.Repeat("x", 4<<20)); result <- err }()
	<-input.entered
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled write: %v", err)
		}
	case <-time.After(2 * time.Second):
		_ = w.Close()
		<-result
		t.Fatal("request ignored cancellation while blocked writing")
	}
	if _, err := w.Write([]byte("late control\n")); !errors.Is(err, os.ErrClosed) {
		t.Fatal("expired transport remained writable")
	}
}

func TestWireWriteLockWaitIsCancellable(t *testing.T) {
	p := &wireProcess{}
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result := make(chan error, 1)
	go func() { result <- p.sendContext(ctx, "expired") }()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("expired write waited for another writer")
	}
}

func TestDispatchWaitIsCancellable(t *testing.T) {
	s := &Supervisor{capacityCapturing: true}
	s.dispatchMu.Lock()
	defer s.dispatchMu.Unlock()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result := make(chan error, 1)
	go func() { result <- s.StartRun(ctx, Run{}) }()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled dispatch remained queued behind startup")
	}
}

type startupBudgetAdapter struct {
	deadline time.Time
}

func (*startupBudgetAdapter) Name() string { return Codex }
func (a *startupBudgetAdapter) Start(ctx context.Context, _ StartRequest, _ func(AdapterEvent)) (Process, error) {
	a.deadline, _ = ctx.Deadline()
	return nil, errors.New("fixture startup rejection")
}

func TestSupervisorBoundsStartupByAcceptedDuration(t *testing.T) {
	s, api, _ := testSupervisor(t)
	s.maxRunDuration = time.Minute
	a := &startupBudgetAdapter{}
	s.adapters[Codex] = a
	if err := s.StartRun(t.Context(), api.run); err == nil {
		t.Fatal("fixture startup unexpectedly succeeded")
	}
	if a.deadline.IsZero() || time.Until(a.deadline) > time.Minute {
		t.Fatal("startup had no accepted run deadline")
	}
}
