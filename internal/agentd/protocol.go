// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/inspr-at/paimos/internal/ownedprocess"

	"github.com/inspr-at/paimos/internal/capacity"
)

type wireProcess struct {
	limitVendor   string
	vendorLimited atomic.Bool

	identity    ownedprocess.Identity
	lifetime    *ownedprocess.Lifetime
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	stdout      io.ReadCloser
	writeMu     contextMutex
	mu          sync.Mutex
	eventMu     sync.Mutex
	pending     map[string]chan json.RawMessage
	readDone    chan struct{}
	waitDone    chan struct{}
	waitErr     error
	readErr     error // published before readDone closes; nil means actual EOF
	next        atomic.Int64
	observe     func(AdapterEvent)
	chatDropped atomic.Uint64     // content-free count; never stores rejected frames
	chatTools   map[string]string // bounded ACP tool identities, memory-only
	onEvent     func(json.RawMessage)
	earlyEvents []json.RawMessage
	threadID    string
	sessionID   string
	turnID      string
	protocol    string

	readerClose  sync.Once
	readerClosed atomic.Bool
}

// DroppedChatFrames counts unsupported or invalid chat projections only.
func (p *wireProcess) DroppedChatFrames() uint64 { return p.chatDropped.Load() }

func pinnedExecutable(path string) (string, error) {
	if path == "" || !filepath.IsAbs(path) {
		return "", errors.New("adapter executable must be an absolute path")
	}
	physical, err := filepath.EvalSymlinks(path)
	if err != nil || physical != path {
		return "", errors.New("adapter executable is not pinned to a physical path")
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Mode().Perm()&0111 == 0 {
		return "", errors.New("adapter executable unavailable")
	}
	return path, nil
}

func launchWire(path string, args []string, workspace string, environment []string, protocol string, observe func(AdapterEvent)) (*wireProcess, error) {
	return launchWireChecked(path, args, workspace, environment, protocol, observe, func(p *wireProcess) error { return p.lifetime.Verify() })
}

// The per-launch check allows lifecycle tests to place an exit precisely
// between the initial group check and the retained lifetime's verification.
func launchWireChecked(path string, args []string, workspace string, environment []string, protocol string, observe func(AdapterEvent), check func(*wireProcess) error) (*wireProcess, error) {
	if !ownedprocess.TrackingSupported() {
		return nil, errors.New("safe child lifetime observation unsupported")
	}
	path, err := pinnedExecutable(path)
	if err != nil {
		return nil, err
	}
	// Allocate identity before creating a child or reader to own on failure.
	processID, err := randomID()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(path, args...)
	cmd.Dir = workspace
	if environment != nil {
		cmd.Env = environment
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	// Own the read descriptor: exec.Cmd.Wait must not close it while buffered
	// terminal/usage frames are still draining after the child exits.
	stdout, childStdout, err := os.Pipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	cmd.Stdout = childStdout
	defer childStdout.Close()
	cmd.Stderr = io.Discard
	// Bound exec's stderr copier even if a writer survives group cleanup.
	cmd.WaitDelay = 2 * time.Second
	configured := ownedprocess.Configure(cmd)
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, errors.New("adapter child start failed")
	}
	if err := ownedprocess.Verify(cmd, configured); err != nil {
		_ = ownedprocess.Signal(cmd, true)
		_ = cmd.Wait()
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, err
	}
	p := &wireProcess{cmd: cmd, stdin: stdin, stdout: stdout, pending: map[string]chan json.RawMessage{}, readDone: make(chan struct{}), waitDone: make(chan struct{}), observe: observe, protocol: protocol}
	p.identity = ownedprocess.Identity{ProcessID: processID, RootPID: cmd.Process.Pid, GroupID: cmd.Process.Pid, StartedAt: time.Now().UTC()}
	p.lifetime = ownedprocess.Track(cmd)
	if err := check(p); err != nil {
		// Start completed Setpgid and no waiter has reaped the leader yet.
		// Its reserved PID still identifies this launch's group even when
		// the leader has exited and verification can no longer observe it.
		_ = ownedprocess.Signal(cmd, true)
		_ = p.lifetime.Wait()
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, err
	}
	go func() { p.waitErr = p.lifetime.WaitGroup(); _ = stdin.Close(); close(p.waitDone) }()
	go p.read(stdout)
	return p, nil
}

func (p *wireProcess) PID() int { return p.cmd.Process.Pid }
func (p *wireProcess) ProcessExited() bool {
	select {
	case <-p.waitDone:
		return true
	default:
		return false
	}
}
func (p *wireProcess) Wait() error {
	<-p.waitDone
	return errors.Join(p.waitErr, p.finishReader())
}

const readerCleanupTimeout = 2 * time.Second

// closeReader discards this owner's stream, not the process or any descendant.
// It never joins: an observer running on the reader may itself call Stop.
// Atomic suppression also covers frames already buffered in Scanner.
func (p *wireProcess) closeReader() {
	p.readerClose.Do(func() {
		p.readerClosed.Store(true)
		if p.stdout != nil {
			_ = p.stdout.Close()
		}
	})
}

// finishReader belongs to final owners only, never the root waiter or Stop.
// Allow buffered accounting to drain, then release a pipe held by another
// writer. A callback already in flight gets a bounded join; no later callback
// is dispatched after closeReader. Normal EOF closes the descriptor in read.
func (p *wireProcess) finishReader() error {
	timer := time.NewTimer(readerCleanupTimeout)
	defer timer.Stop()
	if !p.readerClosed.Load() {
		select {
		case <-p.readDone:
			return nil
		case <-timer.C:
		}
		p.closeReader()
		timer.Reset(readerCleanupTimeout)
	}
	select {
	case <-p.readDone:
		return nil
	case <-timer.C:
		return errors.New("adapter reader cleanup unconfirmed")
	}
}

// A failed Start transfers no Process to the monitor. It must release the
// reader itself, including when Stop finds the root already reaped.
func (p *wireProcess) failStart(err error) (Process, error) {
	p.closeReader()
	cleanupErr := errors.Join(p.Stop(context.Background()), p.finishReader())
	// A vendor refusal is a settled run only when the owned child exit is proven.
	// Return that process to the supervisor for normal vendor_limit telemetry.
	if cleanupErr == nil && p.vendorLimited.Load() && p.ProcessExited() {
		return &refusedProcess{p}, nil
	}
	return nil, errors.Join(err, cleanupErr)
}

// A refused startup exposes exit evidence and no controls.
type refusedProcess struct{ *wireProcess }

func (*refusedProcess) Control(context.Context, string, string) error { return ErrNotOwned }

// discardReader is also available to the supervisor if startup fails after
// Start succeeds but before a monitor takes ownership of Wait.
func (p *wireProcess) discardReader() {
	p.closeReader()
	_ = p.finishReader()
}

func (p *wireProcess) read(src io.Reader) {
	defer func() {
		if p.readerClosed.Load() && p.readErr == nil {
			p.readErr = errors.New("adapter event stream locally closed")
		}
		if p.stdout != nil {
			_ = p.stdout.Close()
		}
		close(p.readDone)
	}()
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 4096), 8<<20)
	for scanner.Scan() {
		if p.readerClosed.Load() {
			break
		}
		raw := append(json.RawMessage(nil), scanner.Bytes()...)
		var frame struct {
			ID     json.RawMessage `json:"id"`
			Type   string          `json:"type"`
			Method string          `json:"method"`
			Kind   string          `json:"kind"`
		}
		if json.Unmarshal(raw, &frame) != nil {
			p.readErr = errors.New("adapter event stream malformed")
			break
		}
		id := strings.Trim(string(frame.ID), "\"")
		if id != "" && (p.protocol == "pi" && frame.Type == "response" || p.protocol != "pi" && frame.Method == "") {
			p.mu.Lock()
			ch := p.pending[id]
			if ch != nil {
				delete(p.pending, id)
			}
			p.mu.Unlock()
			if ch != nil {
				ch <- raw
				continue
			}
		}
		p.eventMu.Lock()
		if p.readerClosed.Load() {
			p.eventMu.Unlock()
			break
		}
		if p.onEvent == nil {
			if len(p.earlyEvents) >= 32 {
				p.readErr = errors.New("adapter early event bound")
				p.eventMu.Unlock()
				break
			}
			p.earlyEvents = append(p.earlyEvents, raw)
		} else {
			p.onEvent(raw)
		}
		p.eventMu.Unlock()
	}
	if scanner.Err() != nil {
		p.readErr = errors.New("adapter event stream incomplete")
		if p.observe != nil && !p.readerClosed.Load() {
			p.observe(AdapterEvent{Kind: "status", ErrorCode: "event_stream_bound"})
		}
	}
}

func (p *wireProcess) send(v any) error {
	return p.sendContext(context.Background(), v)
}

func (p *wireProcess) sendContext(ctx context.Context, v any) error {
	ctx, cancel := operationContext(ctx)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	// A 512 KB rules file plus a bounded prompt can expand sixfold in JSON.
	// Keep transport bounded while allowing the decoded launch limits.
	if len(data) > 8<<20 {
		return errors.New("adapter request exceeds bound")
	}
	if err := p.writeMu.LockContext(ctx); err != nil {
		return err
	}
	defer p.writeMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= 0 {
		return context.DeadlineExceeded
	}
	// Closing the owned pipe interrupts a blocked Write. Never leave an
	// asynchronous writer that could deliver an expired frame later. A partial
	// frame poisons the stream, so cancellation also ends the owned child.
	closed := make(chan struct{})
	closeInput := func() {
		_ = p.stdin.Close()
		p.closeReader()
		if p.lifetime != nil {
			_ = p.lifetime.Signal(true)
		}
	}
	stopClose := context.AfterFunc(ctx, func() {
		closeInput()
		close(closed)
	})
	_, err = p.stdin.Write(append(data, '\n'))
	stopped := stopClose()
	if !stopped {
		<-closed
	}
	if ctx.Err() != nil {
		if stopped {
			closeInput()
		}
		// Await the retained root's cleanup, not a numeric PID lookup. Do not
		// join the reader here: an event callback may itself be this writer.
		if p.waitDone != nil {
			select {
			case <-p.waitDone:
				return errors.Join(ctx.Err(), p.cleanupResult())
			case <-time.After(readerCleanupTimeout):
				return errors.Join(ctx.Err(), errors.New("adapter child cleanup unconfirmed"))
			}
		}
		return ctx.Err()
	}
	if err != nil {
		return errors.New("adapter input unavailable")
	}
	return nil
}

func (p *wireProcess) setOnEvent(fn func(json.RawMessage)) {
	p.eventMu.Lock()
	defer p.eventMu.Unlock()
	if p.readerClosed.Load() {
		p.earlyEvents = nil
		return
	}
	p.onEvent = fn
	for _, raw := range p.earlyEvents {
		if p.readerClosed.Load() {
			break
		}
		fn(raw)
	}
	p.earlyEvents = nil
}

func (p *wireProcess) request(ctx context.Context, protocol, method string, params any) (json.RawMessage, error) {
	number := p.next.Add(1)
	id := strconv.FormatInt(number, 10)
	ch := make(chan json.RawMessage, 1)
	p.mu.Lock()
	p.pending[id] = ch
	p.mu.Unlock()
	defer func() { p.mu.Lock(); delete(p.pending, id); p.mu.Unlock() }()
	var frame any
	if protocol == "pi" {
		m := map[string]any{"id": id, "type": method}
		if params != nil {
			for key, value := range params.(map[string]any) {
				m[key] = value
			}
		}
		frame = m
	} else {
		frame = map[string]any{"jsonrpc": "2.0", "id": number, "method": method, "params": params}
	}
	if err := p.sendContext(ctx, frame); err != nil {
		return nil, err
	}
	var raw json.RawMessage
	select {
	case raw = <-ch:
	case <-p.readDone:
		// EOF can race a response already delivered by the same reader.
		select {
		case raw = <-ch:
		default:
			return nil, errors.New("adapter protocol closed")
		}
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	var response struct {
		Result  json.RawMessage `json:"result"`
		Error   json.RawMessage `json:"error"`
		Success *bool           `json:"success"`
		Data    json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &response) != nil {
		return nil, errors.New("adapter protocol rejected request")
	}
	if len(response.Error) > 0 && string(response.Error) != "null" {
		if hit := capacity.VendorLimit(p.limitVendor, raw, nil, time.Now().UTC()); hit != nil {
			p.emitVendorLimit(hit)
		}
		if (method == "thread/start" || method == "turn/start") && explicitModelInvalid(response.Error) {
			return nil, errModelInvalid
		}
		// Only this explicit rejection proves steer did not inject input.
		// Never expose raw vendor errors or retry transport/malformed responses.
		var rejection struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		if method == "turn/steer" && json.Unmarshal(response.Error, &rejection) == nil &&
			(rejection.Code == -32600 || rejection.Code == -32602) &&
			(rejection.Message == "no active turn" || strings.HasPrefix(rejection.Message, "no active turn to steer")) {
			return nil, errCodexNoActiveTurn
		}
		return nil, errors.New("adapter protocol rejected request")
	}
	if protocol == "pi" {
		if response.Success == nil || !*response.Success {
			return nil, errors.New("Pi RPC rejected request")
		}
		return response.Data, nil
	}
	if response.Result == nil {
		return nil, errors.New("adapter protocol response missing result")
	}
	return response.Result, nil
}

// GracefulStop sends TERM only. A timeout is a visible rejected control, never
// implicit authorization to forcefully terminate the process group.
func (p *wireProcess) GracefulStop(ctx context.Context) error {
	if err := p.lifetime.Verify(); err != nil {
		return ErrNotOwned
	}
	if err := p.lifetime.Signal(false); err != nil {
		return err
	}
	select {
	case <-p.waitDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(2 * time.Second):
		return ErrGracefulTimeout
	}
}

func (p *wireProcess) Stop(ctx context.Context) error {
	if p.cmd.Process == nil {
		return ErrNotOwned
	}
	if err := p.lifetime.Verify(); err != nil {
		select {
		case <-p.waitDone:
			return p.cleanupResult()
		default:
			return ErrNotOwned
		}
	}
	if err := p.lifetime.Signal(false); err != nil {
		return err
	}
	select {
	case <-p.waitDone:
		return p.cleanupResult()
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(2 * time.Second):
		// WaitGroup may already own final signaling. In either case observe
		// cleanup before reporting that this owned process has stopped.
		_ = p.lifetime.Signal(true)
		select {
		case <-p.waitDone:
			return p.cleanupResult()
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(readerCleanupTimeout):
			return ErrForceExitUnconfirmed
		}
	}
}

func (p *wireProcess) cleanupResult() error {
	if errors.Is(p.waitErr, ownedprocess.ErrCleanupUnconfirmed) {
		return ownedprocess.ErrCleanupUnconfirmed
	}
	return nil
}

// Ownership is available only while this exact, unreaped group leader is owned.
func (p *wireProcess) Ownership() (ownedprocess.Identity, error) {
	if err := p.lifetime.Verify(); err != nil {
		return ownedprocess.Identity{}, ErrNotOwned
	}
	return p.identity, nil
}
func (p *wireProcess) ForceStop(ctx context.Context, expected ownedprocess.Identity, expiresAt time.Time) error {
	if expected.ProcessID != p.identity.ProcessID || expected.RootPID != p.identity.RootPID || expected.GroupID != p.identity.GroupID || !expected.StartedAt.Equal(p.identity.StartedAt) {
		return ErrNotOwned
	}
	if err := p.lifetime.SignalBefore(ctx, true, expiresAt); err != nil {
		if errors.Is(err, ownedprocess.ErrAuthorizationExpired) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return ErrNotOwned
	}
	select {
	case <-p.waitDone:
		return nil
	case <-ctx.Done():
		return ErrForceExitUnconfirmed
	}
}
