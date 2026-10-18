// SPDX-License-Identifier: AGPL-3.0-only

package ownedprocess

import (
	"context"
	"errors"
	"os/exec"
	"time"
)

// Command owns a newly spawned process group until exit and pipe cleanup.
// Wait is safe for multiple callers; cancellation never signals a reaped PID.
type Command struct {
	done chan struct{}
	err  error
}

// Start starts an exec.Command (not CommandContext) with a single cancellation
// owner. Configure's Setpgid is applied in the child before exec; a successful
// Start proves that setup succeeded, even if the child exits before Getpgid can
// observe it. WaitGroup retains that leader until descendant signaling ends.
func Start(ctx context.Context, cmd *exec.Cmd) (*Command, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !TrackingSupported() || !Configure(cmd) {
		return nil, errors.New("safe child lifetime observation unsupported")
	}
	if cmd.Cancel != nil {
		return nil, errors.New("owned command must not have another cancellation owner")
	}
	// Also bound exec's copy goroutines if an output writer escapes the group.
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	life := Track(cmd)
	life.verified = true // successful Setpgid+exec handshake, before publishing life
	p := &Command{done: make(chan struct{})}
	wait := make(chan error, 1)
	go func() { wait <- life.WaitGroup() }()
	go func() {
		defer close(p.done)
		select {
		case p.err = <-wait:
		case <-ctx.Done():
			// If exit observation already revoked signaling, WaitGroup owns
			// cleanup. Otherwise signal through the retained lifetime fence.
			_ = life.Signal(true)
			p.err = <-wait
		}
		p.err = errors.Join(p.err, ctx.Err())
	}()
	return p, nil
}

func (p *Command) Wait() error { <-p.done; return p.err }

func Run(ctx context.Context, cmd *exec.Cmd) error {
	p, err := Start(ctx, cmd)
	if err != nil {
		return err
	}
	return p.Wait()
}
