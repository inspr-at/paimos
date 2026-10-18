// SPDX-License-Identifier: AGPL-3.0-only
//go:build linux

package agentd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/attachwatch"
)

func TestAttachLinuxStatSessionIdentity(t *testing.T) {
	fields := []string{"S", "20", "30", "20", "34816", "30", "0", "0", "0", "0", "0", "0", "0", "0", "0", "0", "0", "1", "0", "12345"}
	raw := func() []byte { return []byte("30 (helper (fixture)) " + strings.Join(fields, " ")) }
	start, parent, session, tty, err := linuxAttachStat(raw())
	if err != nil || start != "12345" || parent != 20 || session != 20 || !tty {
		t.Fatal("kernel stat lost parent, session or start identity")
	}
	for _, bad := range []string{"0", "-1", "unknown"} {
		fields[3] = bad
		if _, _, _, _, err := linuxAttachStat(raw()); err == nil {
			t.Fatal("invalid session accepted")
		}
	}
	fields[3], fields[0] = "20", "Z"
	if _, _, _, _, err := linuxAttachStat(raw()); err == nil {
		t.Fatal("dead session leader accepted")
	}
}

func attachLinuxRootedTerminalFixture(t *testing.T) (*AttachManager, attachObservation, *attachObservation, AttachLocalRequest) {
	t.Helper()
	m, peer, target, req, _ := attachIdentityFixture(t, Claude)
	metadata := func(pid, uid, parent, session int) attachObservation {
		return attachObservation{Process: attachwatch.Process{PID: pid, UID: uid, Started: fmt.Sprintf("start-%d", pid)}, Parent: parent, Session: session, TTY: true}
	}
	peer.Parent, peer.Session = 20, 10
	target.Parent, target.Session = 50, 10
	graph := map[int]attachObservation{
		10: metadata(10, 0, 90, 10), // root-owned sshd session leader
		20: metadata(20, peer.UID, 10, 10),
		30: attachProcessIdentity(peer),
		40: attachProcessIdentity(*target),
		50: metadata(50, 0, 10, 10), // root-owned su/sudo target ancestor
		90: metadata(90, 0, 1, 90),
	}
	m.observe = func(pid int) (attachObservation, error) {
		if pid != target.PID {
			t.Fatalf("ancestor %d required executable/cwd access", pid)
		}
		return *target, nil
	}
	m.ancestry = func(pid int) (attachObservation, error) {
		if p, ok := graph[pid]; ok {
			return p, nil
		}
		return attachObservation{}, errors.New("fixture process unavailable")
	}
	return m, peer, target, req
}

func TestAttachLinuxRootAncestorsKeepFullTargetValidation(t *testing.T) {
	m, peer, target, req := attachLinuxRootedTerminalFixture(t)
	preview, err := m.handle(t.Context(), peer, req)
	if err != nil || preview.Snapshot.Process != target.Process {
		t.Fatal("metadata-only root ancestors prevented a fully verified preview", err)
	}
	for _, operation := range []string{"confirm", "poll"} {
		m.sessions[preview.ID].touched = time.Now().Add(-2 * time.Second)
		if _, err = m.handle(t.Context(), peer, AttachLocalRequest{Operation: operation, ID: preview.ID, Digest: preview.Digest}); err != nil {
			t.Fatalf("metadata-only root ancestors prevented %s: %v", operation, err)
		}
	}
}

func TestAttachLinuxRootAncestorsNeverRelaxTargetChecks(t *testing.T) {
	for _, operation := range []string{"preview", "confirm", "poll"} {
		for _, failure := range []string{"missing executable", "missing cwd", "outside workspace", "unavailable metadata"} {
			t.Run(operation+"/"+failure, func(t *testing.T) {
				m, peer, target, req := attachLinuxRootedTerminalFixture(t)
				exchanges := 0
				exchange := m.cfg.Exchange
				m.cfg.Exchange = func(ctx context.Context, in attachwatch.DeviceRequest) (attachwatch.View, error) {
					if in.Operation == "request" || in.Operation == "poll" {
						exchanges++
					}
					return exchange(ctx, in)
				}
				if operation != "preview" {
					preview, err := m.handle(t.Context(), peer, req)
					if err != nil {
						t.Fatal(err)
					}
					req = AttachLocalRequest{Operation: operation, ID: preview.ID, Digest: preview.Digest}
					if operation == "poll" {
						if _, err = m.handle(t.Context(), peer, AttachLocalRequest{Operation: "confirm", ID: preview.ID, Digest: preview.Digest}); err != nil {
							t.Fatal(err)
						}
						m.sessions[preview.ID].touched = time.Now().Add(-2 * time.Second)
					}
				}
				switch failure {
				case "missing executable":
					target.Executable = ""
				case "missing cwd":
					target.CWD = ""
				case "outside workspace":
					target.CWD += "-outside"
				case "unavailable metadata":
					m.observe = func(int) (attachObservation, error) {
						return attachObservation{}, errors.New("kernel identity unavailable")
					}
				}
				before := exchanges
				if _, err := m.handle(t.Context(), peer, req); err == nil || len(m.sessions) != 0 || exchanges != before {
					t.Fatalf("target verification failed open: err=%v sessions=%d exchanges=%d (before=%d)", err, len(m.sessions), exchanges, before)
				}
			})
		}
	}
}

func TestAttachLinuxIdentitySkipsAncestorPaths(t *testing.T) {
	self, err := observeAttachProcessIdentity(os.Getpid())
	if err != nil || self.PID != os.Getpid() || self.UID != os.Getuid() || self.Started == "" || self.Session < 1 || self.Executable != "" || self.CWD != "" {
		t.Fatal("kernel identity unavailable or includes paths", err)
	}
	full, err := observeAttachProcess(os.Getpid())
	if err != nil || full.Executable == "" || full.CWD == "" || full.Started != self.Started || full.UID != self.UID {
		t.Fatal("target observation lost executable or cwd", err)
	}
	root, err := observeAttachProcessIdentity(1)
	if err != nil || root.PID != 1 || root.Started == "" || root.Executable != "" || root.CWD != "" {
		t.Fatal("root ancestor metadata unavailable or includes paths", err)
	}
	if root.UID != 0 {
		t.Skip("PID 1 is not uid 0; rootless or user-namespace sandbox")
	}
}

func TestAttachLinuxExitedProcessIsKernelConfirmed(t *testing.T) {
	cmd := exec.Command("/bin/true")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if _, err := observeAttachProcess(pid); !errors.Is(err, errAttachExited) {
		t.Fatalf("exited process not distinguished: %v", err)
	}
}
