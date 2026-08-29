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

// reapAfterDrain is the sole Cmd.Wait owner. Callers invoke it only after the
// stdout reader reaches EOF. reaping is set before Wait so signalOwned cannot
// target a PID after the operating system is allowed to reuse it.
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

// signalOwned serializes group signals with the transition into Cmd.Wait.
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

func waitOwned(ctx context.Context, done <-chan struct{}) error {
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *ownedProcess) Stop(ctx context.Context, request ControlRequest) (ControlEffect, error) {
	if p == nil || p.cmd == nil {
		return ControlEffect{}, errors.New("owned process is unavailable")
	}
	sent, termErr := p.signalOwned(false)
	if !sent {
		if err := waitOwned(ctx, p.done); err != nil {
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
		if err := waitOwned(ctx, p.done); err != nil {
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
