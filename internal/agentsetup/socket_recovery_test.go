// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func recoveryStore(t *testing.T, boundary bool) *Store {
	t.Helper()
	dir := shortSocketHome(t)
	if boundary {
		limit := 103 // sun_path minus its terminating NUL
		if runtime.GOOS == "linux" {
			limit = 107
		}
		dir = filepath.Join(dir, strings.Repeat("s", limit-len(dir)-len("/agentd.sock")-1))
	}
	s, err := OpenStore(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	lock, err := s.LockNamed("agentd.sock.lock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lock.Close() })
	return s
}

func recoveryListener(t *testing.T, path string) *net.UnixListener {
	t.Helper()
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	l.SetUnlinkOnClose(false)
	t.Cleanup(func() { l.Close() })
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	return l
}

func recoveryInfo(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func assertRecoveryInode(t *testing.T, path string, before os.FileInfo) {
	t.Helper()
	after := recoveryInfo(t, path)
	if !os.SameFile(before, after) || before.Mode() != after.Mode() {
		t.Fatalf("artifact changed: %s", path)
	}
}

func TestUnrecordedSocketRecoveryRemovesDeadSocket(t *testing.T) {
	for _, boundary := range []bool{false, true} {
		for _, withToken := range []bool{false, true} {
			label := map[bool]string{false: "short", true: "sun-path-boundary"}[boundary] + map[bool]string{false: "/socket-only", true: "/with-token"}[withToken]
			t.Run(label, func(t *testing.T) {
				s := recoveryStore(t, boundary)
				socket := filepath.Join(s.Path(), "agentd.sock")
				recoveryListener(t, socket).Close()
				if withToken {
					if err := s.Write("agentd.sock.token", []byte("fixture token"), true); err != nil {
						t.Fatal(err)
					}
				}
				if err := s.RecoverUnrecordedSocket("agentd.sock"); err != nil {
					t.Fatal(err)
				}
				entries, err := os.ReadDir(s.Path())
				if err != nil || len(entries) != 1 || entries[0].Name() != "agentd.sock.lock" {
					t.Fatalf("recovery residue: %v: %v", entries, err)
				}
			})
		}
	}
}

func TestUnrecordedSocketRecoveryReplacementSurvivesRemoval(t *testing.T) {
	for _, withToken := range []bool{false, true} {
		t.Run(map[bool]string{false: "socket-only", true: "with-token"}[withToken], func(t *testing.T) {
			s := recoveryStore(t, false)
			socket := filepath.Join(s.Path(), "agentd.sock")
			recoveryListener(t, socket).Close()
			if withToken {
				if err := s.Write("agentd.sock.token", []byte("fixture token"), true); err != nil {
					t.Fatal(err)
				}
			}
			var replacement os.FileInfo
			var aside string
			err := s.recoverUnrecordedSocket("agentd.sock", func(path string) error {
				aside = path
				if path == socket {
					t.Fatal("probe used the original pathname")
				}
				err := probeRecoverySocket(path)
				if !errors.Is(err, unix.ECONNREFUSED) {
					t.Fatalf("dead socket probe: %v", err)
				}
				// Deterministically occupy the old check-to-unlink gap with a
				// real listener; this new inode must survive the removal.
				recoveryListener(t, socket)
				replacement = recoveryInfo(t, socket)
				return err
			})
			if !errors.Is(err, ErrCollision) {
				t.Fatalf("replacement did not refuse startup: %v", err)
			}
			assertRecoveryInode(t, socket, replacement)
			if err := probeRecoverySocket(socket); err != nil {
				t.Fatalf("replacement listener unavailable: %v", err)
			}
			for _, path := range []string{aside, socket + ".token"} {
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("dead artifact retained at %s: %v", path, err)
				}
			}
		})
	}
}

func TestUnrecordedSocketRecoveryRestoresLiveSocketWithoutClobbering(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(map[bool]string{false: "restored", true: "both-kept"}[replace], func(t *testing.T) {
			s := recoveryStore(t, true)
			socket := filepath.Join(s.Path(), "agentd.sock")
			recoveryListener(t, socket)
			original := recoveryInfo(t, socket)
			if err := s.Write("agentd.sock.token", []byte("fixture token"), true); err != nil {
				t.Fatal(err)
			}
			token := recoveryInfo(t, socket+".token")
			var replacement os.FileInfo
			var aside string
			err := s.recoverUnrecordedSocket("agentd.sock", func(path string) error {
				aside = path
				if path == socket {
					t.Fatal("live socket was not renamed before probing")
				}
				assertRecoveryInode(t, path, original)
				if err := probeRecoverySocket(path); err != nil {
					t.Fatalf("renamed live socket unavailable: %v", err)
				}
				if replace {
					recoveryListener(t, socket)
					replacement = recoveryInfo(t, socket)
				}
				return nil
			})
			if !errors.Is(err, ErrCollision) {
				t.Fatalf("live listener was adopted: %v", err)
			}
			assertRecoveryInode(t, socket+".token", token)
			if replace {
				assertRecoveryInode(t, socket, replacement)
				assertRecoveryInode(t, aside, original)
				if err := probeRecoverySocket(aside); err != nil {
					t.Fatalf("original listener lost: %v", err)
				}
			} else {
				assertRecoveryInode(t, socket, original)
				if _, err := os.Lstat(aside); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("restored listener left an aside: %v", err)
				}
			}
			if err := probeRecoverySocket(socket); err != nil {
				t.Fatalf("listener at original path unavailable: %v", err)
			}
		})
	}
}

func TestUnrecordedSocketRecoveryRefusesChangesDuringProbe(t *testing.T) {
	for _, kind := range []string{"owner", "token-replaced", "token-created", "socket-mode", "probe-error"} {
		t.Run(kind, func(t *testing.T) {
			s := recoveryStore(t, false)
			socket := filepath.Join(s.Path(), "agentd.sock")
			recoveryListener(t, socket).Close()
			before := recoveryInfo(t, socket)
			if kind == "token-replaced" {
				if err := s.Write("agentd.sock.token", []byte("old fixture"), true); err != nil {
					t.Fatal(err)
				}
			}
			var token os.FileInfo
			err := s.recoverUnrecordedSocket("agentd.sock", func(path string) error {
				probeErr := probeRecoverySocket(path)
				if !errors.Is(probeErr, unix.ECONNREFUSED) {
					t.Fatal(probeErr)
				}
				var err error
				switch kind {
				case "owner":
					err = os.WriteFile(socket+".owner.json", []byte("new owner fixture"), 0600)
				case "token-replaced", "token-created":
					if kind == "token-replaced" {
						if err := os.Rename(socket+".token", socket+".old-token"); err != nil {
							t.Fatal(err)
						}
					}
					err = os.WriteFile(socket+".token", []byte("new fixture"), 0600)
					token = recoveryInfo(t, socket+".token")
				case "socket-mode":
					err = os.Chmod(path, 0666)
					before = recoveryInfo(t, path)
				case "probe-error":
					return unix.EACCES
				}
				if err != nil {
					t.Fatal(err)
				}
				return probeErr
			})
			if !errors.Is(err, ErrCollision) {
				t.Fatalf("changed artifacts adopted: %v", err)
			}
			assertRecoveryInode(t, socket, before)
			if token != nil {
				assertRecoveryInode(t, socket+".token", token)
			}
		})
	}
}

func TestRecoveryInodeRejectsAnotherUID(t *testing.T) {
	s := recoveryStore(t, false)
	socket := filepath.Join(s.Path(), "agentd.sock")
	recoveryListener(t, socket).Close()
	var st unix.Stat_t
	if err := unix.Lstat(socket, &st); err != nil {
		t.Fatal(err)
	}
	if _, err := privateRecoveryInode(&st, unix.S_IFSOCK); err != nil {
		t.Fatal(err)
	}
	// Exercise the exact metadata gate without requiring privileged chown.
	st.Uid++
	if _, err := privateRecoveryInode(&st, unix.S_IFSOCK); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("foreign socket uid accepted: %v", err)
	}
}
