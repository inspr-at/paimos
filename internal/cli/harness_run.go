// SPDX-License-Identifier: AGPL-3.0-only
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/ownedprocess"
)

func (rt *runtime) harnessRun() *Command {
	o := heartbeatOptions{Interval: heartbeatInterval, Management: "unmanaged", Role: "worker"}
	var role string
	return &Command{Name: "run", Short: "Register a short job, heartbeat it, and stop its session on exit", Use: "harness run --label LABEL --ticket KEY --role reviewer -- <command> [args...]", minArgs: 1, maxArgs: -1,
		addFlags: func(fs *flagSet) {
			fs.string(&o.LinkAccountID, "account-id", 0, "optional enrolled account UUID for the one-code ownership prompt")
			fs.string(&o.LinkSetupRoot, "setup-root", 0, "private pairing state for account linking")
			fs.string(&o.LinkSocket, "socket", 0, "owner-only daemon socket for account linking")
			fs.string(&o.Project, "project", 'p', "project key (defaults to the ticket prefix)")
			fs.string(&o.Agent, "agent", 0, "authenticated agent name (defaults to the caller)")
			fs.string(&o.Harness, "harness", 0, "adapter family (defaults to the command name)")
			fs.string(&o.Generator, "generator", 0, "public media generator label")
			fs.string(&o.CommandLabel, "command", 0, "public terminal command label")
			fs.string(&o.Label, "label", 0, "public session label")
			fs.string(&o.Ticket, "ticket", 0, "ticket key")
			fs.string(&role, "role", 0, "reviewer, fixer, worker or coordinator")
			fs.string(&o.Parent, "parent-session", 0, "lead session UUID")
			fs.string(&o.Shape, "work-shape", 0, "ship or scout (reviewer defaults to scout)")
			fs.string(&o.Model, "model", 0, "public model name")
			fs.string(&o.Effort, "effort", 0, "reasoning effort")
			fs.string(&o.Host, "host", 0, "non-secret host label")
			fs.string(&o.StateDir, "state-dir", 0, "new private state directory; retained for recovery")
			fs.int(&o.Interval, "interval", "heartbeat interval in seconds (default 50)")
		}, run: func(args []string) error {
			if role == "" {
				role = "worker"
			}
			switch role {
			case "reviewer", "fixer", "worker":
				o.Role = "worker"
			case "coordinator":
				o.Role = "coordinator"
			default:
				return usagef("invalid --role")
			}
			o.Note = role
			if o.Label == "" {
				o.Label = role
			}
			if o.Project == "" {
				if i := strings.LastIndexByte(o.Ticket, '-'); i > 0 {
					o.Project = o.Ticket[:i]
				}
			}
			if o.Harness == "" {
				o.Harness = strings.TrimSuffix(filepath.Base(args[0]), ".exe")
				if o.Harness == "cursor-agent" {
					o.Harness = "cursor"
				}
			}
			if o.Ticket != "" && o.Shape == "" {
				o.Shape = "ship"
				if role == "reviewer" {
					o.Shape = "scout"
				}
			}
			if o.Agent == "" {
				me, err := rt.caller()
				if err != nil {
					return err
				}
				o.Agent = me.Principal.Name
			}
			o.OwnerPID = os.Getpid()
			if o.StateDir == "" {
				var err error
				o.StateDir, err = os.MkdirTemp("", "aeon-harness-run-")
				if err != nil {
					return err
				}
			}
			if err := o.prepare(); err != nil {
				return err
			}
			ctx, stop := signalContext()
			defer stop()
			return rt.runHarnessCommand(ctx, o, args)
		},
	}
}

func (rt *runtime) runHarnessCommand(ctx context.Context, o heartbeatOptions, args []string) error {
	if len(args) == 0 {
		return usagef("command required after --")
	}
	if !ownedprocess.TrackingSupported() {
		return errors.New("harness run requires process ownership support on this platform")
	}
	o.applyRuntimeDefaults()
	// Registration and private persistence precede execution: even a command
	// that exits immediately has one durable generation and one stop.
	session, created, err := rt.openHeartbeatSession(ctx, o, heartbeatDeps{})
	if err != nil {
		return err
	}
	defer session.hold.release()
	if !created {
		return usagef("state directory already used; choose a new directory (run-heartbeat can settle the previous generation)")
	}
	if err = saveHeartbeatSession(&session); err != nil {
		return rt.abandonHeartbeat(o, &session, err)
	}
	finish := func() error { return rt.finishHeartbeat(o, &session) }
	if ctx.Err() != nil {
		return errors.Join(&exitError{code: 143}, finish())
	}
	stopLink := rt.startAccountLink(ctx, o)
	defer stopLink()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = rt.stdin, rt.stdout, rt.stderr
	if !ownedprocess.Configure(cmd) {
		session.stopReason = stopProcessFailed
		return errors.Join(errors.New("owned process group unavailable"), finish())
	}
	if err = cmd.Start(); err != nil {
		session.stopReason = stopProcessFailed
		return errors.Join(err, finish())
	}
	life := ownedprocess.Track(cmd)
	// Track retains the group leader until signaling is finished, fencing PID
	// reuse. A very short command may already have exited before Verify.
	verified := life.Verify() == nil
	done := make(chan error, 1)
	go func() {
		if verified {
			done <- life.WaitGroup()
		} else {
			done <- life.Wait()
		}
	}()
	beatCtx, cancelBeat := context.WithCancel(context.WithoutCancel(ctx))
	beatDone := make(chan error, 1)
	go func() {
		beatDone <- rt.heartbeatLoop(beatCtx, o, heartbeatDeps{alive: func(int) bool { return true }}, &session)
	}()
	var childErr error
	select {
	case childErr = <-done:
	case <-ctx.Done():
		_ = life.Signal(false)
		timer := time.NewTimer(5 * time.Second)
		select {
		case childErr = <-done:
		case <-timer.C:
			_ = life.Signal(true)
			childErr = <-done
		}
		timer.Stop()
	}
	// The stop names how the job ended: a clean exit finishes it, any other exit
	// fails it, and an interrupt is a plain stop (AEON-437). Set before the loop
	// that closes the session sees the cancellation.
	session.stopReason = jobStopReason(ctx.Err() != nil, childErr)
	cancelBeat()
	beatErr := <-beatDone
	if beatErr != nil {
		fmt.Fprintf(rt.stderr, "harness: session cleanup needs recovery with run-heartbeat --state-dir %s\n", o.StateDir)
	}
	if ctx.Err() != nil {
		return &exitError{code: 143}
	}
	if childErr != nil {
		var exit *exec.ExitError
		if errors.As(childErr, &exit) {
			code := exit.ExitCode()
			if code < 1 {
				code = 1
			}
			return &exitError{code: code}
		}
		return childErr
	}
	return beatErr
}

// Stop reasons the server accepts for a session whose process ended.
const (
	stopProcessExited = "process_exited"
	stopProcessFailed = "process_failed"
)

// persistedStopReason is what stop.intent may carry: only how a job ended. Any other
// text, including a reason the server takes for other stops, reads back as a plain stop.
func persistedStopReason(reason string) string {
	switch reason = strings.TrimSpace(reason); reason {
	case stopProcessExited, stopProcessFailed:
		return reason
	default:
		return ""
	}
}

func jobStopReason(interrupted bool, childErr error) string {
	switch {
	case interrupted:
		return "stopped"
	case childErr == nil:
		return stopProcessExited
	default:
		return stopProcessFailed
	}
}
