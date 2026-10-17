// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
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
		started    string
		startedErr error
		live       bool
	}{
		{name: "release 131 133 134", raw: legacy, process: current, live: true},
		{name: "dead foreign release", raw: legacy, killErr: syscall.ESRCH},
		{name: "permission denied", raw: legacy, killErr: syscall.EPERM, observeErr: syscall.EPERM, live: true},
		{name: "observation unavailable", raw: legacy, observeErr: hooknote.ErrPeer, live: true},
		{name: "unknown metadata", raw: `{"pid":42,"uid":"future","schema":999}`, process: current, live: true},
		{name: "malformed optional identity", raw: `{"pid":42,"started":123,"exe":{}}`, process: current, live: true},
		{name: "unknown start encoding", raw: `{"pid":42,"started":"future:time","exe":"/older/agentd"}`, process: current, live: true},
		{name: "different start encoding", raw: `{"pid":42,"started":"100","exe":"/older/agentd"}`, process: current, live: true},
		{name: "invalid executable", raw: `{"pid":42,"started":"100:1","exe":"relative"}`, process: current, live: true},
		{name: "partial observation", raw: legacy, process: hooknote.Process{PID: 42, Started: "100:1"}, live: true},
		{name: "reused pid", raw: legacy, process: hooknote.Process{PID: 42, Started: "200:1", Executable: "/older/agentd"}},
		{name: "observation failed reused start", raw: legacy, observeErr: hooknote.ErrPeer, started: "200:1"},
		{name: "observation failed matching start", raw: legacy, observeErr: hooknote.ErrPeer, started: "100:1", live: true},
		{name: "observation failed uncomparable start", raw: legacy, observeErr: hooknote.ErrPeer, started: "200", live: true},
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
			}, func(pid int) (string, error) {
				if pid != 42 || errors.Is(tc.killErr, syscall.ESRCH) {
					t.Fatal("read the start time of an invalid or already dead PID")
				}
				if tc.started == "" && tc.startedErr == nil {
					return "", hooknote.ErrPeer
				}
				return tc.started, tc.startedErr
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

// Risk: an unclean stop can leave a pin naming the pid this process was
// given. The pin is published only after LockSocket, so that pin is residue.
func TestSocketLockRecoversPinNamingThisProcess(t *testing.T) {
	self, err := hooknote.ObserveDaemonSelf()
	if err != nil {
		t.Fatal(err)
	}
	s := socketLockStore(t, false)
	socketPath := filepath.Join(s.Path(), "agentd.sock")
	staleSocket(t, socketPath)
	raw, err := json.Marshal(map[string]any{"pid": self.PID, "started": self.Started, "exe": self.Executable})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write("hook-peer.json", raw, true); err != nil {
		t.Fatal(err)
	}
	lock, err := s.LockSocket("agentd.sock")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if _, err := os.Lstat(socketPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("own pid pin left the socket: %v", err)
	}
	if _, err := s.Read("hook-peer.json", 4096); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("own pid pin survived startup: %v", err)
	}
}

// Risk: image observation fails for cwd "/", another user's process, or a
// deleted working directory. A different kernel start time still proves the
// pinned pid was reused; a matching time stays live.
func TestSocketLockRecoversReusedPIDWhenObservationFails(t *testing.T) {
	t.Run("deleted cwd", func(t *testing.T) {
		pid, started := deletedCWDProcess(t)
		if _, err := hooknote.ObserveDaemon(pid); err == nil {
			t.Fatal("deleted working directory was fully observable")
		}
		assertReusedPIDRecovered(t, pid, started, "/older/agentd")
	})
	t.Run("root cwd", func(t *testing.T) {
		pid := holdProcess(t, "/")
		if _, err := hooknote.Observe(pid); err == nil {
			t.Fatal("strict observation accepted cwd /")
		}
		observed, err := hooknote.ObserveDaemon(pid)
		if err != nil || observed.PID != pid || observed.Started == "" || observed.Executable == "" {
			t.Fatal("daemon observation of cwd /", err)
		}
		assertReusedPIDRecovered(t, pid, observed.Started, observed.Executable)
	})
}

func assertReusedPIDRecovered(t *testing.T, pid int, started, exe string) {
	t.Helper()
	s := socketLockStore(t, false)
	socketPath := filepath.Join(s.Path(), "agentd.sock")
	staleSocket(t, socketPath)
	matching, err := json.Marshal(map[string]any{"pid": pid, "started": started, "exe": exe})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write("hook-peer.json", matching, true); err != nil {
		t.Fatal(err)
	}
	if lock, err := s.LockSocket("agentd.sock"); !errors.Is(err, ErrBusy) {
		if lock != nil {
			lock.Close()
		}
		t.Fatalf("matching start was not live: %v", err)
	}
	if _, err := os.Lstat(socketPath); err != nil {
		t.Fatal("refused start removed the socket", err)
	}
	if got, err := s.Read("hook-peer.json", 4096); err != nil || string(got) != string(matching) {
		t.Fatalf("refusal rewrote the pin: %v", err)
	}
	replaced, err := json.Marshal(map[string]any{"pid": pid, "started": reusedSocketStart(t, started), "exe": exe})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write("hook-peer.json", replaced, false); err != nil {
		t.Fatal(err)
	}
	lock, err := s.LockSocket("agentd.sock")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if _, err := os.Lstat(socketPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reused pid left the socket: %v", err)
	}
	if _, err := s.Read("hook-peer.json", 4096); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reused pid pin survived startup: %v", err)
	}
}

func reusedSocketStart(t *testing.T, current string) string {
	t.Helper()
	parts := strings.Split(current, ":")
	n, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil || len(parts) > 2 {
		t.Fatalf("unusable kernel start %q", current)
	}
	parts[0] = strconv.FormatUint(n+1, 10)
	return strings.Join(parts, ":")
}

func holdProcess(t *testing.T, dir string) int {
	t.Helper()
	cat, err := exec.LookPath("cat")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(cat)
	cmd.Dir = dir
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd.Process.Pid
}

func deletedCWDProcess(t *testing.T) (int, string) {
	t.Helper()
	dir := t.TempDir()
	pid := holdProcess(t, dir)
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	started, err := hooknote.ProcessStarted(pid)
	if err != nil || started == "" {
		t.Fatalf("kernel start time: %v", err)
	}
	return pid, started
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
