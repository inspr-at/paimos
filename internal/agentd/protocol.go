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
)

type wireProcess struct {
	identity    ownedprocess.Identity
	lifetime    *ownedprocess.Lifetime
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	stdout      io.ReadCloser
	writeMu     sync.Mutex
	mu          sync.Mutex
	eventMu     sync.Mutex
	pending     map[string]chan json.RawMessage
	readDone    chan struct{}
	waitDone    chan struct{}
	waitErr     error
	readErr     error // published before readDone closes; nil means actual EOF
	next        atomic.Int64
	observe     func(AdapterEvent)
	onEvent     func(json.RawMessage)
	earlyEvents []json.RawMessage
	threadID    string
	sessionID   string
	turnID      string
	protocol    string
}

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
	if !ownedprocess.TrackingSupported() {
		return nil, errors.New("safe child lifetime observation unsupported")
	}
	path, err := pinnedExecutable(path)
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
	go func() { defer stdout.Close(); p.read(stdout) }()
	processID, err := randomID()
	if err != nil {
		_ = ownedprocess.Signal(cmd, true)
		_ = cmd.Wait()
		return nil, err
	}
	p.identity = ownedprocess.Identity{ProcessID: processID, RootPID: cmd.Process.Pid, GroupID: cmd.Process.Pid, StartedAt: time.Now().UTC()}
	p.lifetime = ownedprocess.Track(cmd)
	go func() { p.waitErr = p.lifetime.Wait(); close(p.waitDone) }()
	return p, nil
}

func (p *wireProcess) PID() int    { return p.cmd.Process.Pid }
func (p *wireProcess) Wait() error { <-p.waitDone; return p.waitErr }

func (p *wireProcess) read(src io.Reader) {
	defer close(p.readDone)
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 4096), 8<<20)
	for scanner.Scan() {
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
		if id != "" && (p.protocol != "pi" || frame.Type == "response") {
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
		if p.observe != nil {
			p.observe(AdapterEvent{Kind: "status", ErrorCode: "event_stream_bound"})
		}
	}
}

func (p *wireProcess) send(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(data) > 2<<20 {
		return errors.New("adapter request exceeds bound")
	}
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	_, err = p.stdin.Write(append(data, '\n'))
	if err != nil {
		return errors.New("adapter input unavailable")
	}
	return nil
}

func (p *wireProcess) setOnEvent(fn func(json.RawMessage)) {
	p.eventMu.Lock()
	p.onEvent = fn
	for _, raw := range p.earlyEvents {
		fn(raw)
	}
	p.earlyEvents = nil
	p.eventMu.Unlock()
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
	if err := p.send(frame); err != nil {
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
	if json.Unmarshal(raw, &response) != nil || len(response.Error) > 0 && string(response.Error) != "null" {
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
			return nil
		default:
			return ErrNotOwned
		}
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
		if err := p.lifetime.Verify(); err == nil {
			return p.lifetime.Signal(true)
		}
		return nil
	}
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
