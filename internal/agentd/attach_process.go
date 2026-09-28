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

type attachObservation struct {
	attachwatch.Process
	Parent int
	TTY    bool
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
	if peer.UID != target.UID {
		return false
	}
	pid := peer.PID
	for i := 0; i < 128 && pid > 1; i++ {
		if pid == target.PID {
			return false
		}
		p, err := observe(pid)
		if err != nil || p.Parent == pid {
			return false
		}
		pid = p.Parent
	}
	return pid <= 1
}
