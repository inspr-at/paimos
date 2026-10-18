// SPDX-License-Identifier: AGPL-3.0-only
//go:build (darwin || linux) && !aeon_test_unsupported

package agentd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/inspr-at/paimos/internal/ownedprocess/processtest"
	"golang.org/x/sys/unix"
)

// This helper deliberately leaves the launch group while retaining stderr.
// Its FIFO release remains with the test; production must not signal it.
func TestWireEscapedStderrFixture(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "--wire-stderr-fixture" {
		return
	}
	if _, err := unix.Setsid(); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Exec("/bin/sh", []string{"sh", "-c", os.Args[len(os.Args)-1]}, []string{"PATH=/usr/bin:/bin"}); err != nil {
		t.Fatal(err)
	}
}

func wireStderrFixture(t *testing.T, escaped bool) (*processtest.Fixture, string) {
	t.Helper()
	shell, err := filepath.EvalSymlinks("/bin/sh")
	if err != nil {
		t.Fatal(err)
	}
	if !escaped {
		return processtest.New(t), shell
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return processtest.New(t, func(child string) string {
		return fmt.Sprintf("exec %q -test.run=^TestWireEscapedStderrFixture$ -- --wire-stderr-fixture %q", exe, child)
	}), shell
}

// WNOWAIT proves the leader has exited while reserving its PID for the
// launcher's cleanup. Darwin's waitid ABI uses an aligned siginfo buffer.
func wireLeaderExited(pid int) error {
	var info [32]uint64
	for {
		_, _, err := syscall.Syscall6(unix.SYS_WAITID, 1, uintptr(pid), uintptr(unsafe.Pointer(&info[0])), unix.WEXITED|unix.WNOWAIT, 0, 0)
		if err == syscall.EINTR {
			continue
		}
		if err != 0 {
			return err
		}
		return nil
	}
}

type checkedWireAdapter struct {
	start func() (*wireProcess, error)
}

func (*checkedWireAdapter) Name() string { return Codex }
func (a *checkedWireAdapter) Start(context.Context, StartRequest, func(AdapterEvent)) (Process, error) {
	p, err := a.start()
	if err != nil {
		return nil, err
	}
	return &piProcess{wireProcess: p}, nil
}

func TestWireRejectedOwnershipReleasesDispatch(t *testing.T) {
	for _, escaped := range []bool{false, true} {
		t.Run(map[bool]string{false: "in_group", true: "escaped_stderr"}[escaped], func(t *testing.T) {
			s, api, _ := testSupervisor(t)
			fixture, shell := wireStderrFixture(t, escaped)
			workspace := t.TempDir()
			rejected := errors.New("fixture ownership check rejected")
			checking := make(chan *wireProcess, 1)
			resumeCheck := make(chan struct{})
			var resumed sync.Once
			resume := func() { resumed.Do(func() { close(resumeCheck) }) }
			defer resume()
			s.adapters[Codex] = &checkedWireAdapter{start: func() (*wireProcess, error) {
				return launchWireChecked(shell, []string{"-c", fixture.Script}, workspace, nil, "test", nil, func(p *wireProcess) error {
					checking <- p
					<-resumeCheck
					// Linux may still resolve a zombie's group; inject the same
					// failure Darwin returns once the retained leader has exited.
					return rejected
				})
			}}
			done := make(chan error, 1)
			go func() { done <- s.StartRun(t.Context(), api.run) }()
			select {
			case p := <-checking:
				fixture.Ready(t)
				fixture.ExitRoot()
				if err := wireLeaderExited(p.PID()); err != nil {
					t.Fatal(err)
				}
			case err := <-done:
				t.Fatalf("fixture did not reach the second ownership check: %v", err)
			case <-time.After(5 * time.Second):
				t.Fatal("fixture did not observe leader exit")
			}
			resume()
			// Keep stderr open until launch cleanup has returned. On failure,
			// release it before joining so even a broken launcher leaves no worker.
			select {
			case err := <-done:
				if !errors.Is(err, rejected) {
					t.Errorf("lost ownership rejection: %v", err)
				}
			case <-time.After(5 * time.Second):
				fixture.ExitChild()
				<-done
				t.Fatal("launch cleanup blocked dispatch on inherited stderr")
			}
			if escaped {
				fixture.ExitChild()
			}
			fixture.AssertExited(t)
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			// An invalid follow-up reaches scope validation only after acquiring
			// dispatch, proving the failed launch released the real supervisor lock.
			if err := s.StartRun(ctx, Run{}); !errors.Is(err, ErrScope) {
				t.Fatalf("dispatch did not proceed after launch rejection: %v", err)
			}
		})
	}
}

func TestWireRunningWaitBoundsEscapedStderr(t *testing.T) {
	fixture, shell := wireStderrFixture(t, true)
	p, err := launchWire(shell, []string{"-c", fixture.Script}, t.TempDir(), nil, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	fixture.Ready(t)
	fixture.ExitRoot()
	select {
	case <-p.waitDone:
		if !errors.Is(p.waitErr, exec.ErrWaitDelay) {
			t.Errorf("held stderr did not report bounded drain failure: %v", p.waitErr)
		}
	case <-time.After(5 * time.Second):
		fixture.ExitChild()
		<-p.waitDone
		t.Error("running launcher waited indefinitely on escaped stderr")
	}
	p.discardReader()
	fixture.ExitChild()
	fixture.AssertExited(t)
}
