//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestOwnerStampMatchesStartAndRejectsReuse(t *testing.T) {
	stamp, err := readOwnerStamp(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if stamp.Start == "" || stamp.PID != os.Getpid() || !ownerAlive(os.Getpid(), stamp.Start) {
		t.Fatalf("live stamp %#v", stamp)
	}
	if ownerAlive(os.Getpid(), stamp.Start+"9") || ownerAlive(os.Getpid(), "") {
		t.Fatal("a different start identity was treated as the same process")
	}
}

func TestOwnerStampRejectsZombie(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	deadline := time.Now().Add(2 * time.Second)
	zombie := false
	for time.Now().Before(deadline) {
		killErr := syscall.Kill(pid, 0)
		_, readErr := readOwnerStamp(pid)
		if killErr == nil && readErr != nil {
			zombie = true
			break
		}
		if killErr != nil && readErr != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	_ = cmd.Wait()
	if !zombie {
		t.Fatal("an exited unreaped process was still treated as alive")
	}
}

func TestOpenNoFollowRejectsFIFO(t *testing.T) {
	path := t.TempDir() + "/pipe"
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		f, err := openNoFollow(path)
		if f != nil {
			_ = f.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("opened a fifo")
		}
	case <-time.After(time.Second):
		t.Fatal("opening a fifo blocked")
	}
}
