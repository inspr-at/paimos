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
	cmd     *exec.Cmd
	done    chan struct{}
	mu      sync.Mutex
	waitErr error
}

func newOwnedProcess(cmd *exec.Cmd) *ownedProcess {
	return &ownedProcess{cmd: cmd, done: make(chan struct{})}
}

func (p *ownedProcess) startWait() {
	go func() {
		err := p.cmd.Wait()
		p.mu.Lock()
		p.waitErr = err
		p.mu.Unlock()
		close(p.done)
	}()
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

func (p *ownedProcess) Stop(ctx context.Context, request ControlRequest) (ControlEffect, error) {
	if p == nil || p.cmd == nil {
		return ControlEffect{}, errors.New("owned process is unavailable")
	}
	select {
	case <-p.done:
		return ControlEffect{Primitive: "owned process already exited", CorrelationID: request.CorrelationID}, nil
	default:
	}
	termErr := ownedprocess.Signal(p.cmd, false)
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
	if err := ownedprocess.Signal(p.cmd, true); err != nil && termErr != nil {
		return ControlEffect{}, errors.Join(termErr, err)
	} else if err != nil {
		return ControlEffect{}, err
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
