// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/inspr-at/paimos/internal/hooknote"
	"golang.org/x/sys/unix"
)

// Risk: format validation or failed kernel inspection could disguise a live
// foreign daemon; PID reuse must compare the recorded identity, not our binary.
func TestSocketPeerLivenessIsConservative(t *testing.T) {
	// Releases 131 (b29aecd4b), 133 (66ca220d1), and 134 (d1564f33a)
	// all publish this shape, with the same pid/started/exe ownership fields.
	const legacy = `{"pid":42,"uid":501,"started":"100:1","exe":"/older/agentd","dev":1,"ino":2,"pidv":7}`
	current := hooknote.Process{PID: 42, Started: "100:1", Executable: "/older/agentd"}
	for _, tc := range []struct {
		name       string
		raw        string
		killErr    error
		process    hooknote.Process
		observeErr error
		live       bool
	}{
		{name: "release 131 133 134", raw: legacy, process: current, live: true},
		{name: "dead foreign release", raw: legacy, killErr: syscall.ESRCH},
		{name: "permission denied", raw: legacy, killErr: syscall.EPERM, observeErr: syscall.EPERM, live: true},
		{name: "observation unavailable", raw: legacy, observeErr: hooknote.ErrPeer, live: true},
		{name: "unknown metadata", raw: `{"pid":42,"uid":"future","schema":999}`, process: current, live: true},
		{name: "malformed optional identity", raw: `{"pid":42,"started":123,"exe":{}}`, process: current, live: true},
		{name: "partial observation", raw: legacy, process: hooknote.Process{PID: 42, Started: "100:1"}, live: true},
		{name: "reused pid", raw: legacy, process: hooknote.Process{PID: 42, Started: "200:1", Executable: "/older/agentd"}},
		{name: "different loaded executable", raw: legacy, process: hooknote.Process{PID: 42, Started: "100:1", Executable: "/other/program"}},
		{name: "unknown format", raw: `{"schema":999}`},
		{name: "malformed", raw: `{`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			peer := parseSocketPeer([]byte(tc.raw))
			got := peer.live(func(pid int, signal syscall.Signal) error {
				if pid != 42 || signal != 0 {
					t.Fatalf("unexpected process probe: %d %d", pid, signal)
				}
				return tc.killErr
			}, func(pid int) (hooknote.Process, error) {
				if pid != 42 || errors.Is(tc.killErr, syscall.ESRCH) {
					t.Fatal("inspected an invalid or already dead PID")
				}
				return tc.process, tc.observeErr
			})
			if got != tc.live {
				t.Fatalf("live=%v, want %v", got, tc.live)
			}
		})
	}
}

func socketLockStore(t *testing.T, boundary bool) *Store {
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
	return s
}

func staleSocket(t *testing.T, path string) {
	t.Helper()
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	l.SetUnlinkOnClose(false)
	l.Close()
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestSocketLockCleansPartialStartupAndLegacyArtifacts(t *testing.T) {
	for _, boundary := range []bool{false, true} {
		for _, artifacts := range []string{"empty", "socket-only", "token-only", "socket-token", "legacy-asides"} {
			label := map[bool]string{false: "short", true: "sun-path-boundary"}[boundary] + "/" + artifacts
			t.Run(label, func(t *testing.T) {
				s := socketLockStore(t, boundary)
				if strings.Contains(artifacts, "socket") {
					staleSocket(t, filepath.Join(s.Path(), "agentd.sock"))
				}
				if strings.Contains(artifacts, "token") {
					if err := s.Write("agentd.sock.token", []byte("fixture"), true); err != nil {
						t.Fatal(err)
					}
				}
				if artifacts == "legacy-asides" {
					staleSocket(t, filepath.Join(s.Path(), ".s01234567"))
					if err := s.Write(".s89abcdef", []byte("fixture"), true); err != nil {
						t.Fatal(err)
					}
				}
				for _, name := range []string{".settings", ".snot-hex", "another.sock.token"} {
					if err := s.Write(name, []byte("preserved fixture"), true); err != nil {
						t.Fatal(err)
					}
				}
				lock, err := s.LockSocket("agentd.sock")
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Close()
				entries, err := os.ReadDir(s.Path())
				if err != nil || len(entries) != 4 {
					t.Fatalf("unexpected cleanup result: %v: %v", entries, err)
				}
				for _, name := range []string{".settings", ".snot-hex", "another.sock.token"} {
					if b, err := s.Read(name, 128); err != nil || string(b) != "preserved fixture" {
						t.Fatalf("unrelated file changed: %s: %v", name, err)
					}
				}
				// A client can still read private data while the daemon lock is
				// held. Another startup cannot inspect or clean socket artifacts.
				if err := s.Write("agentd.sock.token", []byte("current fixture"), true); err != nil {
					t.Fatal(err)
				}
				client, err := OpenStore(s.Path(), false)
				if err != nil {
					t.Fatal(err)
				}
				defer client.Close()
				if b, err := client.Read("agentd.sock.token", 128); err != nil || string(b) != "current fixture" {
					t.Fatal("client read was blocked by daemon lock")
				}
				if other, err := client.LockSocket("agentd.sock"); !errors.Is(err, ErrBusy) {
					if other != nil {
						other.Close()
					}
					t.Fatalf("second owner accepted: %v", err)
				}
				if b, err := client.Read("agentd.sock.token", 128); err != nil || string(b) != "current fixture" {
					t.Fatal("refused start touched token")
				}
			})
		}
	}
}

func TestSocketLockRejectsRegularFileAtSocket(t *testing.T) {
	s := socketLockStore(t, false)
	if err := s.Write("agentd.sock", []byte("unrelated fixture"), true); err != nil {
		t.Fatal(err)
	}
	if lock, err := s.LockSocket("agentd.sock"); !errors.Is(err, ErrUnsafePath) {
		if lock != nil {
			lock.Close()
		}
		t.Fatalf("regular file accepted as socket: %v", err)
	}
	if b, err := s.Read("agentd.sock", 128); err != nil || string(b) != "unrelated fixture" {
		t.Fatal("regular file changed")
	}
	// A failed startup must release the lock, including cleanup failures.
	lock, err := s.LockNamed("agentd.sock.lock")
	if err != nil {
		t.Fatalf("failed startup retained lock: %v", err)
	}
	lock.Close()
}

func TestSocketArtifactsRejectAnotherUID(t *testing.T) {
	s := socketLockStore(t, false)
	staleSocket(t, filepath.Join(s.Path(), "agentd.sock"))
	for _, name := range []string{"agentd.sock.lock", "agentd.sock.token"} {
		if err := s.Write(name, []byte("fixture"), true); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []struct {
		name string
		kind uint32
	}{{"agentd.sock", unix.S_IFSOCK}, {"agentd.sock.lock", unix.S_IFREG}, {"agentd.sock.token", unix.S_IFREG}} {
		var st unix.Stat_t
		if err := unix.Lstat(filepath.Join(s.Path(), item.name), &st); err != nil {
			t.Fatal(err)
		}
		if !privateArtifact(&st, item.kind) {
			t.Fatalf("own artifact refused: %s", item.name)
		}
		// Exercise the same metadata gate used for the opened lock and for
		// cleanup, without requiring privileged chown on the test runner.
		st.Uid++
		if privateArtifact(&st, item.kind) {
			t.Fatalf("foreign uid accepted: %s", item.name)
		}
	}
}

func TestSocketCleanupStaysRelativeToPinnedDirectory(t *testing.T) {
	for _, phase := range []string{"startup", "shutdown"} {
		t.Run(phase, func(t *testing.T) {
			dir := filepath.Join(shortSocketHome(t), "state")
			s, err := OpenStore(dir, true)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			lock, err := s.LockSocket("agentd.sock")
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Close()
			lockInfo, err := lock.Stat()
			if err != nil {
				t.Fatal(err)
			}
			if phase == "startup" {
				lock.Close()
				staleSocket(t, filepath.Join(dir, ".s01234567"))
				for _, name := range []string{"agentd.sock.owner.json", ".s89abcdef"} {
					if err := s.Write(name, []byte("legacy fixture"), true); err != nil {
						t.Fatal(err)
					}
				}
			}
			staleSocket(t, filepath.Join(dir, "agentd.sock"))
			if err := s.Write("agentd.sock.token", []byte("fixture"), true); err != nil {
				t.Fatal(err)
			}
			socketInfo, err := os.Lstat(filepath.Join(dir, "agentd.sock"))
			if err != nil {
				t.Fatal(err)
			}
			tokenInfo, err := os.Lstat(filepath.Join(dir, "agentd.sock.token"))
			if err != nil {
				t.Fatal(err)
			}
			moved := dir + "-moved"
			if err := os.Rename(dir, moved); err != nil {
				t.Fatal(err)
			}
			shadow, err := OpenStore(dir, true)
			if err != nil {
				t.Fatal(err)
			}
			defer shadow.Close()
			shadowLock, err := shadow.LockSocket("agentd.sock")
			if err != nil {
				t.Fatal(err)
			}
			defer shadowLock.Close()
			listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(dir, "agentd.sock"), Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			if err := os.Chmod(listener.Addr().String(), 0600); err != nil {
				t.Fatal(err)
			}
			if err := shadow.Write("agentd.sock.token", []byte("shadow fixture"), true); err != nil {
				t.Fatal(err)
			}
			before := make(map[string]os.FileInfo)
			for _, name := range []string{"agentd.sock", "agentd.sock.token", "agentd.sock.lock"} {
				info, err := os.Lstat(filepath.Join(dir, name))
				if err != nil {
					t.Fatal(err)
				}
				before[name] = info
			}
			if phase == "startup" {
				lock, err = s.LockSocket("agentd.sock")
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Close()
			} else if err := s.CleanupSocket("agentd.sock", lock, socketInfo, tokenInfo); err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(moved)
			if err != nil || len(entries) != 1 || entries[0].Name() != "agentd.sock.lock" {
				t.Fatalf("pinned directory not cleaned: %v: %v", entries, err)
			}
			if info, err := os.Lstat(filepath.Join(moved, "agentd.sock.lock")); err != nil || !os.SameFile(lockInfo, info) {
				t.Fatalf("permanent lock changed: %v", err)
			}
			for name, info := range before {
				got, err := os.Lstat(filepath.Join(dir, name))
				if err != nil || !os.SameFile(info, got) || info.Mode() != got.Mode() || info.Size() != got.Size() || info.ModTime() != got.ModTime() {
					t.Fatalf("replacement directory artifact changed: %s: %v", name, err)
				}
			}
			// Cleanup must retain the lifetime lock, including after shutdown.
			contender, err := OpenStore(moved, false)
			if err != nil {
				t.Fatal(err)
			}
			defer contender.Close()
			if other, err := contender.LockSocket("agentd.sock"); !errors.Is(err, ErrBusy) {
				if other != nil {
					other.Close()
				}
				t.Fatalf("cleanup released lifetime lock: %v", err)
			}
		})
	}
}

func TestSocketShutdownRefusesReplacedArtifacts(t *testing.T) {
	for _, replaced := range []string{"agentd.sock", "agentd.sock.token"} {
		t.Run(replaced, func(t *testing.T) {
			s := socketLockStore(t, false)
			lock, err := s.LockSocket("agentd.sock")
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Close()
			staleSocket(t, filepath.Join(s.Path(), "agentd.sock"))
			if err := s.Write("agentd.sock.token", []byte("fixture"), true); err != nil {
				t.Fatal(err)
			}
			owned := make(map[string]os.FileInfo)
			for _, name := range []string{"agentd.sock", "agentd.sock.token"} {
				info, err := os.Lstat(filepath.Join(s.Path(), name))
				if err != nil {
					t.Fatal(err)
				}
				owned[name] = info
			}
			path := filepath.Join(s.Path(), replaced)
			if err := os.Rename(path, path+".original"); err != nil {
				t.Fatal(err)
			}
			if replaced == "agentd.sock" {
				staleSocket(t, path)
			} else if err := s.Write(replaced, []byte("replacement fixture"), true); err != nil {
				t.Fatal(err)
			}
			replacement, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.CleanupSocket("agentd.sock", lock, owned["agentd.sock"], owned["agentd.sock.token"]); !errors.Is(err, ErrCollision) {
				t.Fatalf("shutdown accepted replacement: %v", err)
			}
			for name, original := range owned {
				want := original
				if name == replaced {
					want = replacement
				}
				if got, err := os.Lstat(filepath.Join(s.Path(), name)); err != nil || !os.SameFile(want, got) {
					t.Fatalf("refused shutdown changed %s: %v", name, err)
				}
			}
		})
	}
}
