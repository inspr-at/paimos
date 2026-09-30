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
// This is defence in depth, not a boundary against unrestricted same-UID code.
// Same-user code can open another terminal; Touch ID is the factor it cannot forge.
func independentAttachPeer(peer, target attachObservation, observe func(int) (attachObservation, error)) bool {
	current, err := observe(peer.PID)
	if err != nil || !sameAttachProcessIdentity(current, peer) || current.UID != target.UID || !current.TTY || current.Session <= 1 || current.Session == current.PID {
		return false
	}
	selected, err := observe(target.PID)
	if err != nil || !sameAttachProcessIdentity(selected, target) {
		return false
	}
	leader, err := observe(current.Session)
	if err != nil || leader.PID != current.Session || leader.Session != current.Session {
		return false
	}
	// A double fork can hide the parent chain, but a setsid/pty helper either
	// leads its own session or still belongs to the attacker's (possibly dead)
	// session leader. The helper, leader and target walks must remain independent.
	seen := map[int]attachObservation{current.PID: attachProcessIdentity(current), selected.PID: attachProcessIdentity(selected)}
	if before, exists := seen[leader.PID]; exists && before != attachProcessIdentity(leader) {
		return false
	}
	seen[leader.PID] = attachProcessIdentity(leader)
	if !independentAttachAncestry(current.PID, target.PID, current.UID, observe, seen) ||
		!independentAttachAncestry(leader.PID, target.PID, current.UID, observe, seen) ||
		!independentAttachAncestry(target.PID, current.PID, current.UID, observe, seen) {
		return false
	}
	// Re-read the whole graph: no PID may be reused or reparented during the
	// decision, including a root-owned login/sshd leader shared by both walks.
	for pid, before := range seen {
		after, err := observe(pid)
		if err != nil || attachProcessIdentity(after) != before {
			return false
		}
	}
	return true
}

// Ancestors need only kernel identity, never their executable or cwd. macOS
// denies those path reads for root-owned login and sshd-session processes.
// Linux denies /proc/<pid>/exe and /proc/<pid>/cwd across users, including
// root-owned sshd, su and sudo.
func attachProcessIdentity(p attachObservation) attachObservation {
	p.Executable, p.CWD = "", ""
	return p
}

func sameAttachProcessIdentity(a, b attachObservation) bool {
	return a.PID == b.PID && a.UID == b.UID && a.Started != "" && a.Started == b.Started
}

func independentAttachAncestry(pid, target, uid int, observe func(int) (attachObservation, error), seen map[int]attachObservation) bool {
	for i := 0; i < 128 && pid > 1; i++ {
		if pid == target {
			return false
		}
		p, err := observe(pid)
		if err != nil || p.PID != pid || p.Started == "" || p.Parent < 1 || p.Parent == pid || p.UID != uid && p.UID != 0 {
			return false
		}
		identity := attachProcessIdentity(p)
		if before, exists := seen[pid]; exists && before != identity {
			return false
		}
		seen[pid] = identity
		pid = p.Parent
	}
	return pid == 1
}
