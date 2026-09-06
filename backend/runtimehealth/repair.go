// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package runtimehealth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/inspr-at/paimos/backend/agentd"
)

const maxRepairAttempts = 3

type circuit struct {
	Attempts    int       `json:"attempts"`
	Open        bool      `json:"open"`
	LastAttempt time.Time `json:"last_attempt"`
}

func (r *Runtime) readCircuit() (c circuit, err error) {
	b, e := readPrivate(filepath.Join(r.directory, "runtime-repair.json"), 4096)
	if errors.Is(e, os.ErrNotExist) {
		return c, nil
	}
	if e != nil {
		return c, e
	}
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&c) != nil || decoder.Decode(&struct{}{}) != io.EOF || c.Attempts < 0 || c.Attempts > maxRepairAttempts || c.Open && c.Attempts != maxRepairAttempts {
		return c, errors.New("repair circuit corrupt")
	}
	return c, nil
}
func (r *Runtime) saveCircuit(c circuit) error {
	b, e := json.Marshal(c)
	if e != nil {
		return e
	}
	return writePrivate(filepath.Join(r.directory, "runtime-repair.json"), b)
}
func (r *Runtime) attention() error {
	p := filepath.Join(r.directory, "runtime-attention.json")
	if _, e := safeFile(p, false); e == nil {
		return nil
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	b, _ := json.Marshal(struct {
		Kind   string    `json:"kind"`
		Code   string    `json:"code"`
		At     time.Time `json:"at"`
		Action string    `json:"action"`
	}{"runtime_attention", "repair_circuit_open", r.Now().UTC(), "inspect service failure; preview runtime reset"})
	return writePrivate(p, b)
}
func (r *Runtime) remoteReady(ctx context.Context) bool {
	if r.Remote == nil {
		return false
	}
	e := r.Remote(ctx)
	return e.Auth.State == Known && e.Identity.State == Known
}
func (r *Runtime) Setup(ctx context.Context) (Report, error) {
	// Setup never writes a service definition or installs a binary. Activation
	// uses only the operator's already reviewed declarative service definition.
	if !r.remoteReady(ctx) {
		return r.Doctor(ctx), ErrActionRequired
	}
	if e := r.pathsSafe(true); e != nil {
		return r.Doctor(ctx), ErrActionRequired
	}
	svc, e := r.Service.Inspect(ctx)
	if e != nil || !svc.Verified {
		return r.Doctor(ctx), ErrActionRequired
	}
	if svc.Running {
		return r.Doctor(ctx), nil
	}
	return r.Repair(ctx)
}
func (r *Runtime) Repair(ctx context.Context) (Report, error) {
	if !r.remoteReady(ctx) || r.pathsSafe(true) != nil {
		return r.Doctor(ctx), ErrActionRequired
	}
	operation, e := lockState(r.directory, "runtime-operation.lock")
	if e != nil {
		return r.Doctor(ctx), ErrActionRequired
	}
	defer operation.Close()
	c, e := r.readCircuit()
	if e != nil {
		return r.Doctor(ctx), ErrActionRequired
	}
	if c.Attempts == maxRepairAttempts && !c.Open {
		svc, se := r.Service.Inspect(ctx)
		status, de := r.Daemon.RuntimeStatus(ctx)
		if se == nil && de == nil && svc.Verified && svc.Running && svc.PID == status.PID && status.Instance == r.Instance && !status.Closed && svc.Restarts < maxRepairAttempts {
			return r.Doctor(ctx), nil
		}
		c.Open = true
		if e = r.saveCircuit(c); e != nil {
			return r.Doctor(ctx), e
		}
	}
	if c.Open {
		if e = r.stopCircuit(ctx); e != nil {
			_ = r.attention()
			return r.Doctor(ctx), ErrActionRequired
		}
		if e = r.attention(); e != nil {
			return r.Doctor(ctx), e
		}
		return r.Doctor(ctx), ErrActionRequired
	}
	// Never let the ordinary repair flow discard ambiguous journal history.
	if r.checkJournal() != nil {
		return r.Doctor(ctx), ErrActionRequired
	}
	for c.Attempts < maxRepairAttempts {
		svc, e := r.Service.Inspect(ctx)
		if e != nil || !svc.Verified {
			return r.Doctor(ctx), ErrActionRequired
		}
		status, de := r.Daemon.RuntimeStatus(ctx)
		if de == nil && status.Instance == r.Instance && status.PID == svc.PID && svc.Running && !status.Closed && svc.Restarts < maxRepairAttempts {
			return r.Doctor(ctx), nil
		}
		if svc.Restarts >= maxRepairAttempts {
			c.Attempts = maxRepairAttempts
			c.Open = true
			if e = r.saveCircuit(c); e != nil {
				return r.Doctor(ctx), e
			}
			if e = r.stopCircuit(ctx); e != nil {
				return r.Doctor(ctx), ErrActionRequired
			}
			if e = r.attention(); e != nil {
				return r.Doctor(ctx), e
			}
			return r.Doctor(ctx), ErrActionRequired
		}
		// A held lock without a matching live daemon is not stale ownership.
		held, le := lockHeld(r.directory)
		if le != nil || held && (de != nil || status.Instance != r.Instance || status.PID != svc.PID) {
			return r.Doctor(ctx), ErrActionRequired
		}

		if held && de == nil {
			if e = r.Daemon.QuiesceRuntime(ctx, status.DaemonID, ownedSessions(status)); e != nil {
				return r.Doctor(ctx), ErrActionRequired
			}
		}
		if e = r.Service.Stop(ctx); e != nil {
			return r.Doctor(ctx), ErrActionRequired
		}
		// Confirm platform stop and retain the existing ownership lock before
		// socket cleanup. A stop command may return while the daemon exits.
		lock, e := r.awaitStoppedLock(ctx, svc.Definition, svc.PID)
		if e != nil {
			return r.Doctor(ctx), ErrActionRequired
		}
		e = r.removeStaleSocket()
		lock.Close()
		if e != nil {
			return r.Doctor(ctx), ErrActionRequired
		}
		delay := time.Second << c.Attempts
		if e = r.Wait(ctx, delay); e != nil {
			return r.Doctor(ctx), e
		}
		c.Attempts++
		c.LastAttempt = r.Now().UTC()
		c.Open = false
		if e = r.saveCircuit(c); e != nil {
			return r.Doctor(ctx), e
		}
		if e = r.Service.Start(ctx); e == nil {
			if e = r.Wait(ctx, 2*time.Second); e != nil {
				return r.Doctor(ctx), e
			}
			svc, se := r.Service.Inspect(ctx)
			status, de := r.Daemon.RuntimeStatus(ctx)
			if se == nil && de == nil && svc.Verified && svc.Running && status.Instance == r.Instance && status.PID == svc.PID && !status.Closed && svc.Restarts < maxRepairAttempts {
				// Keep the attempt budget until an operator reset. Repeated invocations
				// cannot erase crash evidence just because one short probe succeeded.

				out := r.Doctor(ctx)
				out.Layers = append(out.Layers, layer("repair", Repaired, "service_reconnected", ""))
				return out, nil
			}
		}
	}
	c.Open = true
	if e = r.saveCircuit(c); e != nil {
		return r.Doctor(ctx), e
	}
	// Disable platform auto-restart before emitting exactly one local durable
	// attention record. This never sends a message or wakes an AI model.
	stopErr := r.stopCircuit(ctx)
	attentionErr := r.attention()
	if attentionErr != nil {
		return r.Doctor(ctx), attentionErr
	}
	if stopErr != nil {
		return r.Doctor(ctx), ErrActionRequired
	}
	return r.Doctor(ctx), ErrActionRequired
}
func (r *Runtime) removeStaleSocket() error {
	p := filepath.Join(r.directory, "agentd.sock")
	_, e := safeFile(p, true)
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return e
	}
	// Ownership comes from the verified service being stopped plus its free
	// instance lock, never from a process name or PID saved on disk.
	if e = r.verifyStaleSocket(); e != nil {
		return e
	}
	if os.Remove(p) != nil {
		return errors.New("stale socket removal failed")
	}
	return syncDir(r.directory)
}

func ownedSessions(status agentd.RuntimeStatus) []string {
	out := []string{}
	for _, s := range status.Sessions {
		if s.Owned {
			out = append(out, s.ID)
		}
	}
	return out
}

// Platform ownership is sufficient to disable its crash-restart policy. When
// the private endpoint is available it must agree with the service PID; live
// children are quiesced through agentd rather than signalled from this package.
func (r *Runtime) stopCircuit(ctx context.Context) error {
	svc, e := r.Service.Inspect(ctx)
	if e != nil || !svc.Verified {
		return ErrActionRequired
	}
	status, e := r.Daemon.RuntimeStatus(ctx)
	if e == nil && svc.Running {
		if status.Instance != r.Instance || status.PID != svc.PID {
			return ErrActionRequired
		}
		if e = r.Daemon.QuiesceRuntime(ctx, status.DaemonID, ownedSessions(status)); e != nil {
			return ErrActionRequired
		}
	}
	if e = r.Service.Stop(ctx); e != nil {
		return e
	}
	lock, e := r.awaitStoppedLock(ctx, svc.Definition, svc.PID)
	if e != nil {
		return e
	}
	lock.Close()
	return nil
}

func (r *Runtime) verifyStaleSocket() error {
	path := filepath.Join(r.directory, "agentd.sock")
	if _, e := safeFile(path, true); errors.Is(e, os.ErrNotExist) {
		return nil
	} else if e != nil {
		return e
	}
	connection, e := net.DialTimeout("unix", path, 250*time.Millisecond)
	if e == nil {
		_ = connection.Close()
		return errors.New("runtime socket still has a live owner")
	}
	if !errors.Is(e, syscall.ECONNREFUSED) && !errors.Is(e, os.ErrNotExist) {
		return errors.New("runtime socket ownership unknown")
	}
	return nil
}
