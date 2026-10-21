// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"os"
	"path/filepath"
	"time"
)

// Read only kernel metadata. Never adopt, signal or inspect the old process's
// credentials/arguments. Missing/reused/foreign initial PIDs fail closed.
func pinNativeExit(pid int, workspace, claudePath string) (func(context.Context) error, error) {
	if pid <= 1 || pid == os.Getpid() {
		return nil, ErrScope
	}
	physical, err := filepath.EvalSymlinks(claudePath)
	if err != nil {
		return nil, ErrNotOwned
	}
	before, err := observeAttachProcess(pid)
	if err != nil || before.UID != os.Geteuid() || before.Executable != physical || before.CWD != workspace || before.Started == "" {
		return nil, ErrNotOwned
	}
	return func(ctx context.Context) error {
		return waitNativeExit(ctx, before, observeAttachProcessIdentity, func() <-chan time.Time { return time.After(250 * time.Millisecond) })
	}, nil
}

func waitNativeExit(ctx context.Context, before attachObservation, observe func(int) (attachObservation, error), after func() <-chan time.Time) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		current, err := observe(before.PID)
		if err == errAttachExited {
			return nil
		}
		if err != nil {
			return ErrProcessesUnconfirmed
		}
		if !sameAttachProcessIdentity(before, current) {
			return ErrProcessesUnconfirmed
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-after():
		}
	}
}

// prepareAfterExit requires this owner's exact retained C0 source. The public
// PID remains only a selector; no saved or foreign process is adopted/signaled.
// The source's observed group exit AND persisted release precede a fresh claim.
func (h *NativeHost) prepareAfterExit(ctx context.Context, local nativecontract.LocalStart) error {
	if local.AfterExit == nil && local.NativeResumeRef == nil && local.Intent.ResumeFromOwnedSessionID == nil {
		return nil
	}
	if local.AfterExit == nil || local.NativeResumeRef == nil || local.Intent.ResumeFromOwnedSessionID == nil {
		return nativeRefusal(nativecontract.ReasonAfterExitUnconfirmed)
	}
	barrier := *local.AfterExit
	h.mu.Lock()
	var source *nativeOwned
	for _, e := range h.entries {
		if e.grant.ExpectedBinding != nil && e.grant.ExpectedBinding.OwnedSessionID == *local.Intent.ResumeFromOwnedSessionID {
			source = e
			break
		}
	}
	if source != nil {
		for _, e := range h.entries {
			if e == source {
				continue
			}
			e.mu.Lock()
			transport, exited := e.transport, e.exited
			e.mu.Unlock()
			if transport != nil && !exited && transport.Identity().NativeSessionRef == *local.NativeResumeRef {
				h.mu.Unlock()
				return nativeRefusal(nativecontract.ReasonAfterExitUnconfirmed)
			}
		}
	}
	h.mu.Unlock()
	if source == nil {
		return nativeRefusal(nativecontract.ReasonAfterExitUnconfirmed)
	}
	source.mu.Lock()
	transport := source.transport
	source.mu.Unlock()
	if transport == nil {
		return nativeRefusal(nativecontract.ReasonAfterExitUnconfirmed)
	}
	identity := transport.Identity()
	if identity.PID != int(barrier.PID) || identity.ProcessIdentityDigest != barrier.ProcessIdentityDigest || !identity.StartedAt.Equal(barrier.ProcessStartedAt) || identity.NativeSessionRef != *local.NativeResumeRef || source.local.Intent.ComputerID != local.Intent.ComputerID || source.local.Intent.AccountID != local.Intent.AccountID || source.local.Intent.WorkspaceID != local.Intent.WorkspaceID || source.local.Cwd != local.Cwd || !h.s.capacityNow().Before(barrier.Deadline) {
		return nativeRefusal(nativecontract.ReasonAfterExitUnconfirmed)
	}
	ctx, cancel := context.WithDeadline(ctx, barrier.Deadline)
	defer cancel()
	exit, err := transport.Wait(ctx)
	if err != nil || !exit.RootExited || !exit.GroupAbsent || exit.ProcessIdentityDigest != identity.ProcessIdentityDigest {
		return nativeRefusal(nativecontract.ReasonAfterExitUnconfirmed)
	}
	select {
	case <-source.released:
		return nil
	case <-ctx.Done():
		return nativeRefusal(nativecontract.ReasonAfterExitUnconfirmed)
	}
}
