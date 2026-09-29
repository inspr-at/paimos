// SPDX-License-Identifier: AGPL-3.0-only
//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package agentd

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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
	if _, err := ServePairedLocal(s, args[2]); err != nil {
		t.Fatal(err)
	}
	os.Exit(0)
}

func crashedSocket(t *testing.T, s *Supervisor, socket string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestPairedSocketCrashFixture$", "--", s.state.Path(), s.DaemonID(), socket)
	cmd.Env = append(os.Environ(), "AEON_SOCKET_CRASH_FIXTURE=1")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("crash fixture: %v: %s", err, output)
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
