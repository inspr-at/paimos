// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"

	"github.com/inspr-at/paimos/internal/attachwatch"
)

// Only a kernel-confirmed missing or zombie process yields this sentinel.
var errAttachExited = errors.New("pinned process exited")

type attachObservation struct {
	attachwatch.Process
	Parent  int
	Session int
	TTY     bool
}
type attachPeerKey struct{}

// Every new socket connection gets a kernel peer PID, never a JSON PID claim.
func attachConnContext(ctx context.Context, c net.Conn) context.Context {
	u, ok := c.(*net.UnixConn)
	if !ok {
		return ctx
	}
	raw, err := u.SyscallConn()
	if err != nil {
		return ctx
	}
	pid := 0
	if raw.Control(func(fd uintptr) { pid, _ = attachPeerPID(int(fd)) }) != nil || pid < 1 {
		return ctx
	}
	observed, err := observeAttachProcess(pid)
	if err != nil {
		return ctx
	}
	return context.WithValue(ctx, attachPeerKey{}, observed)
}
func attachPeer(r *http.Request) (attachObservation, error) {
	before, ok := r.Context().Value(attachPeerKey{}).(attachObservation)
	if !ok {
		return before, errors.New("kernel peer unavailable")
	}
	now, err := observeAttachProcess(before.PID)
	own, ownErr := os.Executable()
	if ownErr == nil {
		own, ownErr = filepath.EvalSymlinks(own)
	}
	if err != nil || ownErr != nil || now.Process != before.Process || now.UID != os.Getuid() || now.Executable != own || !now.TTY {
		return before, errors.New("interactive helper peer required")
	}
	return now, nil
}

// An injected command launched beneath the selected agent cannot approve it.
// This is defense in depth, not a boundary against unrestricted same-UID code.
func independentAttachPeer(peer, target attachObservation, observe func(int) (attachObservation, error)) bool {
	current, err := observe(peer.PID)
	if err != nil || current.Process != peer.Process || current.UID != target.UID || !current.TTY || current.Session <= 1 || current.Session == current.PID {
		return false
	}
	leader, err := observe(current.Session)
	if err != nil || leader.PID != current.Session || leader.Session != current.Session {
		return false
	}
	// A double fork can hide the parent chain, but a setsid/pty helper either
	// leads its own session or still belongs to the attacker's (possibly dead)
	// session leader. Both ancestry walks must remain independent on every poll.
	return independentAttachAncestry(current.PID, target.PID, observe) && independentAttachAncestry(leader.PID, target.PID, observe)
}

func independentAttachAncestry(pid, target int, observe func(int) (attachObservation, error)) bool {
	for i := 0; i < 128 && pid > 1; i++ {
		if pid == target {
			return false
		}
		p, err := observe(pid)
		if err != nil || p.PID != pid || p.Parent < 1 || p.Parent == pid {
			return false
		}
		pid = p.Parent
	}
	return pid == 1
}
