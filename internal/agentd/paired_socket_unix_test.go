// SPDX-License-Identifier: AGPL-3.0-only
//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package agentd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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

// This child holds a real lifetime flock until the parent sends SIGKILL. The
// legacy mode recreates a crash during the removed quarantine implementation.
func TestPairedSocketProcessFixture(t *testing.T) {
	if os.Getenv("AEON_SOCKET_PROCESS_FIXTURE") != "1" {
		return
	}
	args := os.Args[len(os.Args)-4:]
	store, err := agentsetup.OpenStore(args[0], false)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s := &Supervisor{state: store, daemonID: args[1]}
	input := bufio.NewReader(os.Stdin)
	fmt.Println("waiting")
	if _, err := input.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	if args[3] == "legacy" {
		sockets, err := agentsetup.OpenStore(filepath.Dir(args[2]), false)
		if err != nil {
			t.Fatal(err)
		}
		defer sockets.Close()
		lock, err := sockets.LockNamed(filepath.Base(args[2]) + ".lock")
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Close()
		listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: args[2], Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		listener.SetUnlinkOnClose(false)
		defer listener.Close()
		if err := os.Chmod(args[2], 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(args[2]+".token", []byte("legacy fixture"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(args[2], filepath.Join(sockets.Path(), ".s01234567")); err != nil {
			t.Fatal(err)
		}
	} else {
		local, err := ServePairedLocal(s, args[2])
		if errors.Is(err, agentsetup.ErrBusy) {
			if !strings.Contains(err.Error(), "agentd is already running for this state root") {
				t.Fatalf("unclear busy refusal: %v", err)
			}
			fmt.Println(err)
			fmt.Println("busy")
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		defer local.Close()
	}
	fmt.Println("ready")
	_, _ = input.ReadString('\n')
}

type socketProcess struct {
	cmd    *exec.Cmd
	input  io.WriteCloser
	lines  <-chan string
	done   chan struct{}
	mu     sync.Mutex
	output strings.Builder
}

func startSocketProcess(t *testing.T, s *Supervisor, socket, mode string) *socketProcess {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestPairedSocketProcessFixture$", "--", s.state.Path(), s.DaemonID(), socket, mode)
	cmd.Env = append(os.Environ(), "AEON_SOCKET_PROCESS_FIXTURE=1")
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = cmd.Stdout // capture the complete combined child diagnostic stream
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &socketProcess{cmd: cmd, input: input, done: make(chan struct{})}
	t.Cleanup(func() {
		_ = input.Close()
		_ = cmd.Process.Kill()
		<-p.done
		if cmd.ProcessState == nil {
			_ = cmd.Wait()
		}
		if t.Failed() {
			t.Logf("socket fixture full output:\n%s", p.diagnostics())
		}
	})
	lines := make(chan string, 8)
	p.lines = lines
	go func() {
		defer close(p.done)
		defer close(lines)
		scanner := bufio.NewScanner(output)
		scanner.Buffer(make([]byte, 4096), 1<<20)
		for scanner.Scan() {
			line := scanner.Text()
			p.mu.Lock()
			p.output.WriteString(line + "\n")
			p.mu.Unlock()
			if line == "waiting" || line == "ready" || line == "busy" {
				lines <- line
			}
		}
		if err := scanner.Err(); err != nil {
			p.mu.Lock()
			fmt.Fprintf(&p.output, "read fixture output: %v\n", err)
			p.mu.Unlock()
		}
	}()
	if got := p.line(t); got != "waiting" {
		t.Fatalf("fixture readiness: %q", got)
	}
	return p
}

func (p *socketProcess) diagnostics() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.output.String()
}

func (p *socketProcess) line(t *testing.T) string {
	t.Helper()
	select {
	case line, ok := <-p.lines:
		if !ok {
			t.Fatalf("socket fixture exited before readiness; full output:\n%s", p.diagnostics())
		}
		return line
	case <-time.After(15 * time.Second):
		t.Fatalf("socket fixture timed out; output so far:\n%s", p.diagnostics())
		return ""
	}
}

func (p *socketProcess) start(t *testing.T) {
	t.Helper()
	if _, err := io.WriteString(p.input, "start\n"); err != nil {
		t.Fatal(err)
	}
}

func (p *socketProcess) kill(t *testing.T) {
	t.Helper()
	if err := p.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-p.done
	if err := p.cmd.Wait(); err == nil {
		t.Fatal("fixture did not die from SIGKILL")
	}
}

func assertSocketConnects(t *testing.T, socket string) {
	t.Helper()
	conn, err := net.DialTimeout("unix", socket, time.Second)
	if err != nil {
		t.Fatalf("listener unavailable: %v", err)
	}
	conn.Close()
}

func TestPairedSocketLiveLockAndSIGKILLRecovery(t *testing.T) {
	s, _, _ := testSupervisor(t)
	defer s.Close(context.Background())
	socket := filepath.Join(socketTestDir(t), "agentd.sock")
	child := startSocketProcess(t, s, socket, "normal")
	child.start(t)
	if got := child.line(t); got != "ready" {
		t.Fatalf("fixture startup: %q", got)
	}
	before := socketArtifacts(t, filepath.Dir(socket))
	for _, start := range []func(*Supervisor, string, ...*AttachManager) (*LocalServer, error){ServePairedLocal, ServeLocal} {
		if local, err := start(s, socket); !errors.Is(err, agentsetup.ErrBusy) || !strings.Contains(err.Error(), "agentd is already running for this state root") {
			if local != nil {
				local.Close()
			}
			t.Fatalf("live listener not protected: %v", err)
		}
		assertSocketArtifacts(t, filepath.Dir(socket), before)
		assertSocketConnects(t, socket)
	}
	child.kill(t)
	assertSocketArtifacts(t, filepath.Dir(socket), before)
	local, err := ServePairedLocal(s, socket)
	if err != nil {
		t.Fatalf("restart after SIGKILL: %v", err)
	}
	defer local.Close()
	assertSocketConnects(t, socket)
	if err := local.Close(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{socket, socket + ".token", socket + ".owner.json"} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("clean shutdown residue: %s: %v", path, err)
		}
	}
	if info, err := os.Lstat(socket + ".lock"); err != nil || !os.SameFile(before["agentd.sock.lock"], info) {
		t.Fatalf("shutdown changed the permanent lock inode: %v", err)
	}
	next, err := ServePairedLocal(s, socket)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	before = socketArtifacts(t, filepath.Dir(socket))
	// Repeated shutdown must not touch a successor after releasing the lock.
	local.Close()
	assertSocketArtifacts(t, filepath.Dir(socket), before)
	assertSocketConnects(t, socket)
}

func TestPairedSocketRecoversLegacyQuarantineCrash(t *testing.T) {
	s, _, _ := testSupervisor(t)
	defer s.Close(context.Background())
	dir := socketTestDir(t)
	socket := filepath.Join(dir, "agentd.sock")
	child := startSocketProcess(t, s, socket, "legacy")
	child.start(t)
	if got := child.line(t); got != "ready" {
		t.Fatalf("legacy fixture startup: %q", got)
	}
	child.kill(t)
	// Cover both a canonical stranded token and a quarantined token, plus
	// an obsolete owner record. No JSON ownership record is needed to recover.
	for _, name := range []string{".s89abcdef", "agentd.sock.owner.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("legacy fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	local, err := ServePairedLocal(s, socket)
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	assertSocketConnects(t, socket)
	for _, name := range []string{".s01234567", ".s89abcdef", "agentd.sock.owner.json"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("legacy residue %s: %v", name, err)
		}
	}
}

func TestPairedSocketConcurrentProcessesExactlyOneWins(t *testing.T) {
	for trial := 0; trial < 20; trial++ {
		t.Run(fmt.Sprint(trial), testPairedSocketConcurrentProcesses)
	}
}

func testPairedSocketConcurrentProcesses(t *testing.T) {
	s, _, _ := testSupervisor(t)
	defer s.Close(context.Background())
	socket := filepath.Join(socketTestDir(t), "agentd.sock")
	a := startSocketProcess(t, s, socket, "normal")
	b := startSocketProcess(t, s, socket, "normal")
	a.start(t)
	b.start(t)
	first, second := a.line(t), b.line(t)
	if !(first == "ready" && second == "busy" || first == "busy" && second == "ready") {
		t.Fatalf("concurrent startup results: %q, %q", first, second)
	}
	assertSocketConnects(t, socket)
}

func TestPairedSocketLiveListenerSurvivesLostLockPath(t *testing.T) {
	for _, mutation := range []string{"unlink", "replace"} {
		t.Run(mutation, func(t *testing.T) {
			s, _, _ := testSupervisor(t)
			defer s.Close(context.Background())
			socket := filepath.Join(socketTestDir(t), "agentd.sock")
			child := startSocketProcess(t, s, socket, "normal")
			child.start(t)
			if got := child.line(t); got != "ready" {
				t.Fatalf("fixture startup: %q", got)
			}
			if mutation == "unlink" {
				if err := os.Remove(socket + ".lock"); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(socket+".replacement", nil, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(socket+".replacement", socket+".lock"); err != nil {
					t.Fatal(err)
				}
			}
			before := socketArtifacts(t, filepath.Dir(socket))
			contender := startSocketProcess(t, s, socket, "normal")
			contender.start(t)
			if got := contender.line(t); got != "busy" {
				t.Fatalf("lost-lock live listener accepted another start: %q", got)
			}
			if mutation == "unlink" {
				// A replacement lock may be created, but the socket and token
				// must retain their exact inode, mode, size and timestamps.
				info, err := os.Lstat(socket + ".lock")
				if err != nil {
					t.Fatal(err)
				}
				before["agentd.sock.lock"] = info
			}
			assertSocketArtifacts(t, filepath.Dir(socket), before)
			assertSocketConnects(t, socket)
			child.kill(t)
			local, err := ServePairedLocal(s, socket)
			if err != nil {
				t.Fatalf("lost-lock SIGKILL recovery: %v", err)
			}
			defer local.Close()
			assertSocketConnects(t, socket)
		})
	}
}

func TestPairedSocketShutdownRequiresVerifiedLock(t *testing.T) {
	s, _, _ := testSupervisor(t)
	defer s.Close(context.Background())
	socket := filepath.Join(socketTestDir(t), "agentd.sock")
	local, err := ServePairedLocal(s, socket)
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	if err := os.Remove(socket + ".lock"); err != nil {
		t.Fatal(err)
	}
	before := socketArtifacts(t, filepath.Dir(socket))
	if err := local.Close(); !errors.Is(err, agentsetup.ErrCollision) {
		t.Fatalf("shutdown accepted lost lock identity: %v", err)
	}
	assertSocketArtifacts(t, filepath.Dir(socket), before)
	next, err := ServePairedLocal(s, socket)
	if err != nil {
		t.Fatalf("shutdown residue recovery: %v", err)
	}
	defer next.Close()
	assertSocketConnects(t, socket)
}

func TestPairedSocketShutdownPreservesStillLiveSocket(t *testing.T) {
	s, _, _ := testSupervisor(t)
	defer s.Close(context.Background())
	socket := filepath.Join(socketTestDir(t), "agentd.sock")
	local, err := ServePairedLocal(s, socket)
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	// A duplicated listener descriptor can outlive http.Server.Close. Even
	// shutdown with the right lock must preserve a socket that still accepts.
	duplicate, err := local.Listener.(*net.UnixListener).File()
	if err != nil {
		t.Fatal(err)
	}
	defer duplicate.Close()
	before := socketArtifacts(t, filepath.Dir(socket))
	if err := local.Close(); !errors.Is(err, agentsetup.ErrBusy) {
		t.Fatalf("shutdown removed a still-live listener: %v", err)
	}
	assertSocketArtifacts(t, filepath.Dir(socket), before)
	assertSocketConnects(t, socket)
	duplicate.Close()
	next, err := ServePairedLocal(s, socket)
	if err != nil {
		t.Fatalf("duplicate listener residue recovery: %v", err)
	}
	defer next.Close()
	assertSocketConnects(t, socket)
}

func socketArtifacts(t *testing.T, dir string) map[string]os.FileInfo {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	result := make(map[string]os.FileInfo)
	for _, entry := range entries {
		info, err := os.Lstat(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		result[entry.Name()] = info
	}
	return result
}

func assertSocketArtifacts(t *testing.T, dir string, before map[string]os.FileInfo) {
	t.Helper()
	after := socketArtifacts(t, dir)
	if len(before) != len(after) {
		t.Fatalf("artifact count changed: %d -> %d", len(before), len(after))
	}
	for name, info := range before {
		got := after[name]
		if got == nil || !os.SameFile(info, got) || info.Mode() != got.Mode() || info.ModTime() != got.ModTime() || info.Size() != got.Size() {
			t.Fatalf("artifact changed: %s", name)
		}
	}
}

func TestPairedSocketRefusesUnsafeArtifacts(t *testing.T) {
	for _, target := range []string{"agentd.sock.lock", "agentd.sock", "agentd.sock.token", "agentd.sock.owner.json", ".s01234567"} {
		for _, kind := range []string{"symlink", "hardlink", "public", "world-writable", "directory"} {
			t.Run(target+"/"+kind, func(t *testing.T) {
				s, _, _ := testSupervisor(t)
				defer s.Close(context.Background())
				dir := socketTestDir(t)
				socket := filepath.Join(dir, "agentd.sock")
				listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
				if err != nil {
					t.Fatal(err)
				}
				listener.SetUnlinkOnClose(false)
				listener.Close()
				if err := os.Chmod(socket, 0600); err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{"agentd.sock.lock", "agentd.sock.token", "agentd.sock.owner.json", ".s01234567"} {
					if err := os.WriteFile(filepath.Join(dir, name), []byte("fixture"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				path := filepath.Join(dir, target)
				switch kind {
				case "symlink", "directory":
					if err := os.Rename(path, path+".original"); err != nil {
						t.Fatal(err)
					}
					if kind == "symlink" {
						err = os.Symlink(path+".original", path)
					} else {
						err = os.Mkdir(path, 0700)
					}
				case "hardlink":
					err = os.Link(path, path+".link")
				case "public":
					err = os.Chmod(path, 0644)
				case "world-writable":
					err = os.Chmod(path, 0666)
				}
				if err != nil {
					t.Fatal(err)
				}
				before := socketArtifacts(t, dir)
				if local, err := ServePairedLocal(s, socket); !errors.Is(err, agentsetup.ErrUnsafePath) {
					if local != nil {
						local.Close()
					}
					t.Fatalf("unsafe artifacts accepted: %v", err)
				}
				assertSocketArtifacts(t, dir, before)
			})
		}
	}
}
