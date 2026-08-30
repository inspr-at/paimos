// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentd

import (
	"context"
	"errors"
	"os/exec"
	"sync"
	"time"

	"github.com/inspr-at/paimos/backend/ownedprocess"
)

const (
	processGracePeriod = 2 * time.Second
	processKillPeriod  = 5 * time.Second
)

type ownedProcess struct {
	cmd    *exec.Cmd
	done   chan struct{}
	mu     sync.Mutex
	wait   func() error
	signal func(*exec.Cmd, bool) error

	reaping bool
	reaped  bool
	waitErr error
}

func newOwnedProcess(cmd *exec.Cmd) *ownedProcess {
	p := &ownedProcess{cmd: cmd, done: make(chan struct{}), signal: ownedprocess.Signal}
	if cmd != nil {
		p.wait = cmd.Wait
	}
	return p
}

// reapAfterDrain is the sole Cmd.Wait owner. Callers must first drain every
// StdoutPipe reader. Marking reaping before Wait closes the PID-reuse window:
// process-group signals are serialized under mu and are rejected from this
// point onward, while the child cannot have been reaped before this method.
func (p *ownedProcess) reapAfterDrain() {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.reaping || p.reaped {
		p.mu.Unlock()
		return
	}
	p.reaping = true
	wait := p.wait
	p.mu.Unlock()

	err := errors.New("owned process wait is unavailable")
	if wait != nil {
		err = wait()
	}
	p.mu.Lock()
	p.waitErr = err
	p.reaped = true
	p.mu.Unlock()
	close(p.done)
}

// finishAfterDrain terminates the exact still-unreaped process group before
// entering the sole Wait. Reader EOF is only transport loss, not exit proof:
// a child may close stdout and continue running. Once reapAfterDrain marks the
// process reaping, no later raw signal is permitted.
func (p *ownedProcess) finishAfterDrain() {
	_, _ = p.signalOwned(true)
	p.reapAfterDrain()
}

func (p *ownedProcess) PID() int {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

func (p *ownedProcess) Wait() error {
	if p == nil {
		return errors.New("owned process is unavailable")
	}
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.waitErr
}

func (*ownedProcess) Steer(context.Context, ControlRequest) (ControlEffect, error) {
	return ControlEffect{}, ErrCapabilityMissing
}

func (*ownedProcess) Interrupt(context.Context, ControlRequest) (ControlEffect, error) {
	return ControlEffect{}, ErrCapabilityMissing
}

// signalOwned may signal only before the sole reaper has announced its intent
// to call Wait. Holding mu across the raw process-group signal makes that
// ownership decision and the signal one indivisible operation.
func (p *ownedProcess) signalOwned(force bool) (bool, error) {
	if p == nil || p.cmd == nil {
		return false, errors.New("owned process is unavailable")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.reaping || p.reaped {
		return false, nil
	}
	if p.signal == nil {
		return false, errors.New("owned process signal is unavailable")
	}
	return true, p.signal(p.cmd, force)
}

func (p *ownedProcess) waitDone(ctx context.Context, limit time.Duration) error {
	select {
	case <-p.done:
		return nil
	default:
	}
	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return errors.New("owned process reap did not complete within the stop budget")
	}
}

func (p *ownedProcess) Stop(ctx context.Context, request ControlRequest) (ControlEffect, error) {
	if p == nil || p.cmd == nil {
		return ControlEffect{}, errors.New("owned process is unavailable")
	}
	sent, termErr := p.signalOwned(false)
	if !sent {
		if err := p.waitDone(ctx, processKillPeriod); err != nil {
			return ControlEffect{}, err
		}
		return ControlEffect{Primitive: "owned process already exited", CorrelationID: request.CorrelationID}, nil
	}
	grace := time.NewTimer(processGracePeriod)
	defer grace.Stop()
	select {
	case <-p.done:
		return ControlEffect{Primitive: "owned process-group terminate", CorrelationID: request.CorrelationID}, nil
	case <-ctx.Done():
		// Cancellation shortens the grace period but never abandons an exact
		// owned child. Escalate now and return only after it is reaped.
	case <-grace.C:
	}
	forcedSent, forceErr := p.signalOwned(true)
	if !forcedSent {
		if err := p.waitDone(ctx, processKillPeriod); err != nil {
			return ControlEffect{}, err
		}
		return ControlEffect{Primitive: "owned process-group terminate", CorrelationID: request.CorrelationID}, nil
	}
	if forceErr != nil && termErr != nil {
		return ControlEffect{}, errors.Join(termErr, forceErr)
	} else if forceErr != nil {
		return ControlEffect{}, forceErr
	}
	forced := time.NewTimer(processKillPeriod)
	defer forced.Stop()
	select {
	case <-p.done:
		return ControlEffect{Primitive: "owned process-group kill", CorrelationID: request.CorrelationID}, nil
	case <-forced.C:
		return ControlEffect{}, errors.New("owned process did not exit after forced termination")
	}
}
