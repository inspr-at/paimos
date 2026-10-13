// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
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
