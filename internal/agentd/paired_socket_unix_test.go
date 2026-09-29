// SPDX-License-Identifier: AGPL-3.0-only
//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package agentd

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/agentsetup"
)

func socketTestDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "aeon-bind-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// The subprocess deliberately exits without defers, leaving the real listener
// and token inodes behind while the kernel releases its lifetime lock.
func TestPairedSocketCrashFixture(t *testing.T) {
	if os.Getenv("AEON_SOCKET_CRASH_FIXTURE") != "1" {
		return
	}
	args := os.Args[len(os.Args)-3:]
	store, err := agentsetup.OpenStore(args[0], false)
	if err != nil {
		t.Fatal(err)
	}
	s := &Supervisor{state: store, daemonID: args[1]}
	serve := ServePairedLocal
	if os.Getenv("AEON_SOCKET_CRASH_BEFORE_OWNER") == "1" {
		// ServeLocal has bound/chmodded the socket and written the token, but
		// ServePairedLocal has not yet published the ownership record.
		sockets, err := agentsetup.OpenStore(filepath.Dir(args[2]), false)
		if err != nil {
			t.Fatal(err)
		}
		lock, err := sockets.LockNamed(filepath.Base(args[2]) + ".lock")
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Close()
		defer sockets.Close()
		serve = ServeLocal
	}
	if _, err := serve(s, args[2]); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

func crashedSocket(t *testing.T, s *Supervisor, socket string, beforeOwner ...bool) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestPairedSocketCrashFixture$", "--", s.state.Path(), s.DaemonID(), socket)
	cmd.Env = append(os.Environ(), "AEON_SOCKET_CRASH_FIXTURE=1")
	if len(beforeOwner) > 0 && beforeOwner[0] {
		cmd.Env = append(cmd.Env, "AEON_SOCKET_CRASH_BEFORE_OWNER=1")
	}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("crash fixture: %v: %s", err, output)
	}
}

func TestPairedSocketRecoversCrashBeforeOwnerRecord(t *testing.T) {
	for _, withToken := range []bool{true, false} {
		t.Run(map[bool]string{true: "with-token", false: "socket-only"}[withToken], func(t *testing.T) {
			s, _, _ := testSupervisor(t)
			defer s.Close(context.Background())
			socket := filepath.Join(socketTestDir(t), "agentd.sock")
			crashedSocket(t, s, socket, true)
			if _, err := os.Lstat(socket + ".owner.json"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("crash fixture published owner record: %v", err)
			}
			if !withToken {
				if err := os.Remove(socket + ".token"); err != nil {
					t.Fatal(err)
				}
			}
			local, err := ServePairedLocal(s, socket)
			if err != nil {
				t.Fatalf("recover real orphan socket: %v", err)
			}
			defer local.Close()
			conn, err := net.Dial("unix", socket)
			if err != nil {
				t.Fatalf("recovered listener unavailable: %v", err)
			}
			conn.Close()
			if _, err := os.Lstat(socket + ".owner.json"); err != nil {
				t.Fatalf("recovered listener missing owner: %v", err)
			}
			if _, err := ServePairedLocal(s, socket); !errors.Is(err, agentsetup.ErrBusy) {
				t.Fatalf("recovered listener not locked: %v", err)
			}
		})
	}
}

func TestPairedSocketUnrecordedRecoveryRefusesUnsafeArtifacts(t *testing.T) {
	for _, kind := range []string{"live", "socket-mode", "socket-file", "socket-symlink", "socket-hardlink", "token-mode", "token-symlink", "token-hardlink", "owner-invalid", "owner-symlink"} {
		t.Run(kind, func(t *testing.T) {
			s, _, _ := testSupervisor(t)
			defer s.Close(context.Background())
			socket := filepath.Join(socketTestDir(t), "agentd.sock")
			if kind == "live" {
				local, err := ServeLocal(s, socket)
				if err != nil {
					t.Fatal(err)
				}
				defer local.Close()
			} else {
				crashedSocket(t, s, socket, true)
			}
			target := socket
			if strings.HasPrefix(kind, "token-") {
				target += ".token"
			}
			var err error
			switch kind {
			case "socket-mode", "token-mode":
				err = os.Chmod(target, 0644)
			case "socket-file", "socket-symlink", "token-symlink":
				if err := os.Rename(target, target+".original"); err != nil {
					t.Fatal(err)
				}
				if kind == "socket-file" {
					err = os.WriteFile(target, []byte("unrelated fixture"), 0600)
				} else {
					err = os.Symlink(target+".original", target)
				}
			case "socket-hardlink", "token-hardlink":
				err = os.Link(target, target+".link")
			case "owner-invalid":
				err = os.WriteFile(socket+".owner.json", []byte("invalid fixture"), 0600)
			case "owner-symlink":
				err = os.Symlink(socket+".missing", socket+".owner.json")
			}
			if err != nil {
				t.Fatal(err)
			}
			beforeSocket, err := os.Lstat(socket)
			if err != nil {
				t.Fatal(err)
			}
			beforeToken, err := os.Lstat(socket + ".token")
			if err != nil {
				t.Fatal(err)
			}
			if local, err := ServePairedLocal(s, socket); err == nil {
				local.Close()
				t.Fatal("unsafe orphan artifacts adopted")
			}
			for path, before := range map[string]os.FileInfo{socket: beforeSocket, socket + ".token": beforeToken} {
				after, err := os.Lstat(path)
				if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
					t.Fatalf("refusal changed artifact %s: %v", path, err)
				}
			}
			if kind == "live" {
				conn, err := net.Dial("unix", socket)
				if err != nil {
					t.Fatalf("live listener disturbed: %v", err)
				}
				conn.Close()
			}
		})
	}
}

func TestPairedSocketRecoversCrashAndExcludesLiveListener(t *testing.T) {
	s, _, _ := testSupervisor(t)
	defer s.Close(context.Background())
	socket := filepath.Join(socketTestDir(t), "agentd.sock")
	crashedSocket(t, s, socket)
	local, err := ServePairedLocal(s, socket)
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	before, _ := os.Lstat(socket)
	if _, err := ServePairedLocal(s, socket); !errors.Is(err, agentsetup.ErrBusy) {
		t.Fatalf("live listener not protected: %v", err)
	}
	after, _ := os.Lstat(socket)
	if !os.SameFile(before, after) {
		t.Fatal("live listener replaced")
	}
	if err := local.Close(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{socket, socket + ".token", socket + ".owner.json"} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("clean shutdown residue: %s: %v", path, err)
		}
	}
	next, err := ServePairedLocal(s, socket)
	if err != nil {
		t.Fatal(err)
	}
	next.Close()
}

func TestPairedSocketRecoveryPreservesUnrelatedFiles(t *testing.T) {
	for _, kind := range []string{"replacement", "symlink", "hardlink", "public-token", "other-daemon", "unrecorded"} {
		t.Run(kind, func(t *testing.T) {
			s, _, _ := testSupervisor(t)
			defer s.Close(context.Background())
			socket := filepath.Join(socketTestDir(t), "agentd.sock")
			if kind != "unrecorded" {
				crashedSocket(t, s, socket)
			}
			target := socket + ".token"
			switch kind {
			case "replacement", "symlink", "hardlink":
				// Keep the original inode alive so the replacement cannot reuse it.
				original := target + ".original"
				if err := os.Rename(target, original); err != nil {
					t.Fatal(err)
				}
				var err error
				switch kind {
				case "replacement":
					err = os.WriteFile(target, []byte("unrelated fixture"), 0600)
				case "symlink":
					err = os.Symlink(original, target)
				case "hardlink":
					err = os.Link(original, target)
				}
				if err != nil {
					t.Fatal(err)
				}
			case "public-token":
				if err := os.Chmod(target, 0644); err != nil {
					t.Fatal(err)
				}
			case "other-daemon":
				s.daemonID = "another-daemon"
			case "unrecorded":
				if err := os.WriteFile(target, []byte("unrelated fixture"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.Lstat(target)
			if err != nil {
				t.Fatal(err)
			}
			if local, err := ServePairedLocal(s, socket); err == nil {
				local.Close()
				t.Fatal("unrelated artifact adopted")
			}
			after, err := os.Lstat(target)
			if err != nil || !os.SameFile(before, after) {
				t.Fatal("unrelated artifact modified")
			}
		})
	}
}
