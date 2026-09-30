// SPDX-License-Identifier: AGPL-3.0-only
//go:build darwin

package agentd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/attachwatch"
	"golang.org/x/sys/unix"
)

func TestAttachDarwinExitedProcessIsKernelConfirmed(t *testing.T) {
	cmd := exec.Command("/usr/bin/true")
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

func TestAttachDarwinMissingIdentityRequiresESRCH(t *testing.T) {
	for _, kind := range []string{"PID mismatch", "sysctl EIO", "sysctl denied", "empty result"} {
		for _, probeErr := range []error{nil, syscall.EPERM, syscall.EIO, syscall.ESRCH} {
			t.Run(fmt.Sprintf("%s/kill=%v", kind, probeErr), func(t *testing.T) {
				first := &unix.KinfoProc{}
				first.Proc.P_pid = 123
				var readErr error
				switch kind {
				case "PID mismatch":
					first.Proc.P_pid = 456
					first.Proc.P_stat = 5 // A different zombie proves nothing.
				case "sysctl EIO":
					readErr = syscall.EIO
				case "sysctl denied":
					readErr = syscall.EPERM
				case "empty result":
					first = nil
				}
				probed := false
				err := checkAttachDarwinProcess(123, first, readErr, func() error { probed = true; return probeErr })
				if !probed || err == nil || errors.Is(err, errAttachExited) != errors.Is(probeErr, syscall.ESRCH) {
					t.Fatalf("unconfirmed process exit: %v", err)
				}
			})
		}
	}
}

// Paths deliberately stay empty: login/sshd ancestors cannot expose their cwd
// to this user. The target's full image/folder validation is tested separately.
func attachDarwinTerminalFixture(kind string) (attachObservation, attachObservation, map[int]attachObservation) {
	process := func(pid, uid, parent, session int) attachObservation {
		return attachObservation{Process: attachwatch.Process{PID: pid, UID: uid, Started: fmt.Sprintf("start-%d", pid)}, Parent: parent, Session: session, TTY: true}
	}
	uid := os.Getuid()
	peer, target := process(30, uid, 20, 10), process(40, uid, 50, 10)
	leader := process(10, 0, 90, 10)
	terminal := process(90, uid, 1, 90)
	if kind == "sshd-session" {
		terminal.UID = 0
	}
	if kind == "tmux" {
		leader.UID, leader.Parent = uid, 1
	}
	return peer, target, map[int]attachObservation{30: peer, 40: target, 10: leader, 20: process(20, uid, 10, 10), 50: process(50, uid, 10, 10), 90: terminal}
}

func attachDarwinFixtureObserver(graph map[int]attachObservation) func(int) (attachObservation, error) {
	return func(pid int) (attachObservation, error) {
		if p, ok := graph[pid]; ok {
			return p, nil
		}
		return attachObservation{}, errors.New("unavailable fixture process")
	}
}

func TestAttachDarwinTerminalAncestry(t *testing.T) {
	for _, kind := range []string{"login", "sshd-session", "tmux"} {
		t.Run(kind, func(t *testing.T) {
			peer, target, graph := attachDarwinTerminalFixture(kind)
			if !independentAttachPeer(peer, target, attachDarwinFixtureObserver(graph)) {
				t.Fatal("independent terminal refused without ancestor paths")
			}
		})
	}
}

func TestAttachDarwinAncestryRefusesUnsafeChains(t *testing.T) {
	for _, attack := range []string{"helper descends from target", "target descends from helper", "leader descends from target", "foreign helper", "foreign helper ancestor", "foreign target ancestor", "foreign leader", "lost tty", "self session", "dead leader", "wrong leader session", "missing start", "cycle"} {
		t.Run(attack, func(t *testing.T) {
			peer, target, graph := attachDarwinTerminalFixture("login")
			pid := 20
			p := graph[pid]
			switch attack {
			case "helper descends from target":
				p.Parent = target.PID
			case "target descends from helper":
				pid, p = 50, graph[50]
				p.Parent = peer.PID
			case "leader descends from target":
				// A reparented helper still belongs to its original session.
				peer.Parent, target.Parent = 1, 1
				graph[peer.PID], graph[target.PID] = peer, target
				pid, p = 10, graph[10]
				p.Parent = target.PID
			case "foreign helper":
				pid, p = 30, peer
				p.UID++
				peer = p
			case "foreign helper ancestor", "foreign target ancestor", "foreign leader":
				if attack == "foreign target ancestor" {
					pid = 50
				} else if attack == "foreign leader" {
					pid = 10
				}
				p = graph[pid]
				p.UID = os.Getuid() + 1
			case "lost tty":
				pid, p = 30, peer
				p.TTY = false
			case "self session":
				pid, p = 30, peer
				p.Session = p.PID
			case "dead leader":
				delete(graph, 10)
			case "wrong leader session":
				pid, p = 10, graph[10]
				p.Session = 90
			case "missing start":
				p.Started = ""
			case "cycle":
				p.Parent = peer.PID
			}
			graph[pid] = p
			if independentAttachPeer(peer, target, attachDarwinFixtureObserver(graph)) {
				t.Fatal("unsafe process graph accepted")
			}
		})
	}
}

func TestAttachDarwinAncestryRefusesPIDReuse(t *testing.T) {
	for _, pid := range []int{30, 40, 20, 10, 90, 50} {
		t.Run(fmt.Sprint(pid), func(t *testing.T) {
			peer, target, graph := attachDarwinTerminalFixture("login")
			reads := 0
			observe := attachDarwinFixtureObserver(graph)
			if independentAttachPeer(peer, target, func(requested int) (attachObservation, error) {
				p, err := observe(requested)
				if requested == pid {
					reads++
					if reads > 1 {
						p.Started = "reused-pid"
					}
				}
				return p, err
			}) {
				t.Fatal("PID reuse accepted during ancestry decision")
			}
		})
	}
}

func TestAttachDarwinAncestryPinsHelperAndTarget(t *testing.T) {
	for _, pid := range []int{30, 40} {
		t.Run(fmt.Sprint(pid), func(t *testing.T) {
			peer, target, graph := attachDarwinTerminalFixture("login")
			p := graph[pid]
			p.Started = "reused-before-check"
			graph[pid] = p
			if independentAttachPeer(peer, target, attachDarwinFixtureObserver(graph)) {
				t.Fatal("reused helper/target identity accepted")
			}
		})
	}
}

func TestAttachDarwinTerminalManagerKeepsFullTargetValidation(t *testing.T) {
	for _, kind := range []string{"login", "sshd-session", "tmux"} {
		t.Run(kind, func(t *testing.T) {
			m, _, selected, req, _ := attachIdentityFixture(t, Claude)
			peer, target, graph := attachDarwinTerminalFixture(kind)
			target.Executable, target.CWD = selected.Executable, selected.CWD
			*selected = target
			m.observe = func(pid int) (attachObservation, error) {
				if pid != target.PID {
					t.Fatal("ancestry required a cwd/image read")
				}
				return *selected, nil
			}
			m.ancestry = attachDarwinFixtureObserver(graph)
			preview, err := m.handle(t.Context(), peer, req)
			if err != nil || preview.Snapshot.Process != target.Process {
				t.Fatal("full target preview failed", err)
			}
			if _, err = m.handle(t.Context(), peer, AttachLocalRequest{Operation: "confirm", ID: preview.ID, Digest: preview.Digest}); err != nil {
				t.Fatal("root-led confirmation failed", err)
			}
			m.sessions[preview.ID].touched = time.Now().Add(-2 * time.Second)
			if _, err = m.handle(t.Context(), peer, AttachLocalRequest{Operation: "poll", ID: preview.ID, Digest: preview.Digest}); err != nil {
				t.Fatal("root-led poll failed", err)
			}
			selected.CWD += "-outside"
			if _, err = m.handle(t.Context(), peer, req); err == nil {
				t.Fatal("target outside approved folder accepted")
			}
		})
	}
}

func TestAttachDarwinKernelIdentityHasNoPaths(t *testing.T) {
	p, err := observeAttachProcessIdentity(os.Getpid())
	if err != nil || p.PID != os.Getpid() || p.UID != os.Getuid() || p.Started == "" || p.Session < 1 || p.CWD != "" || p.Executable != "" {
		t.Fatal("basic kernel observation unavailable or includes paths", err)
	}
	// launchd is root-owned, but its metadata must remain readable without cwd.
	p, err = observeAttachProcessIdentity(1)
	if err != nil || p.PID != 1 || p.UID != 0 || p.Started == "" {
		t.Fatal("root process metadata unavailable", err)
	}
}

func TestAttachDarwinAncestryRecheckedBeforeUpload(t *testing.T) {
	m, _, selected, req, _ := attachIdentityFixture(t, Claude)
	peer, target, graph := attachDarwinTerminalFixture("login")
	target.Executable, target.CWD = selected.Executable, selected.CWD
	*selected = target
	req.StatusOnly = false
	req.Transcript = attachFixtureFile(t, "history\n")
	m.ancestry = attachDarwinFixtureObserver(graph)
	var sent []attachwatch.DeviceRequest
	m.cfg.Exchange = func(_ context.Context, in attachwatch.DeviceRequest) (attachwatch.View, error) {
		sent = append(sent, in)
		state := "pending"
		if in.Operation == "poll" {
			state = "active"
		}
		return attachwatch.View{State: state, RequestID: in.RequestID, Snapshot: in.Snapshot, Digest: in.Digest, ConsentMode: attachwatch.ConsentAeon, ConsentDigest: attachwatch.ConsentDigest(in.RequestID, in.Digest, attachwatch.ConsentAeon)}, nil
	}
	preview, err := m.handle(t.Context(), peer, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.handle(t.Context(), peer, AttachLocalRequest{Operation: "confirm", ID: preview.ID, Digest: preview.Digest}); err != nil {
		t.Fatal(err)
	}
	poll := AttachLocalRequest{Operation: "poll", ID: preview.ID, Digest: preview.Digest}
	m.sessions[preview.ID].touched = time.Now().Add(-2 * time.Second)
	if _, err = m.handle(t.Context(), peer, poll); err != nil {
		t.Fatal(err)
	}
	appendAttach(t, req.Transcript, "must never upload\n")
	signatureCalls := 0
	fixtureSignature := m.signature
	m.signature = func(ctx context.Context, pid string) (attachSignature, error) {
		signatureCalls++
		return fixtureSignature(ctx, pid)
	}
	reads := 0
	m.observe = func(pid int) (attachObservation, error) {
		if pid != target.PID {
			t.Fatal("ancestor path read")
		}
		reads++
		if reads == 2 {
			// Change only the ancestry, after the first poll guard and tail read.
			p := graph[50]
			p.Parent = peer.PID
			graph[50] = p
		}
		return *selected, nil
	}
	sent = nil
	m.sessions[preview.ID].touched = time.Now().Add(-2 * time.Second)
	if _, err = m.handle(t.Context(), peer, poll); err == nil || len(m.sessions) != 0 || signatureCalls != 0 {
		t.Fatal("late ancestry change retained watch or reached signature verification")
	}
	for _, request := range sent {
		if request.Operation != "detach" || request.Text != "" {
			t.Fatal("late ancestry change uploaded a turn")
		}
	}
}
